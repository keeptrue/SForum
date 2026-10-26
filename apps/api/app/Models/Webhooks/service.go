package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
)

type TxEnqueuer interface {
	EnqueueTx(context.Context, pgx.Tx, river.JobArgs, supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error)
}

// Service 管理端点配置，并把 core observe 事件扇出为 webhook 投递。
type Service struct {
	store Store
	pool  *pgxpool.Pool
	jobs  TxEnqueuer
	// allowHTTP 非生产环境可允许 http:// 目标；生产默认仅 https。
	allowHTTP bool
	// cipher 加密 signing secret；与 web_options 共用 OptionCipher。
	cipher *crypto.OptionCipher
}

func NewService(store Store, pool *pgxpool.Pool, jobs TxEnqueuer) *Service {
	return &Service{store: store, pool: pool, jobs: jobs}
}

// WithAllowHTTP 开发/测试可显式允许 http webhook 目标（生产勿开）。
func (s *Service) WithAllowHTTP(allow bool) *Service {
	if s != nil {
		s.allowHTTP = allow
	}
	return s
}

// WithCipher 注入 AES-GCM 加密器用于 webhook secret 静态加密。
func (s *Service) WithCipher(c *crypto.OptionCipher) *Service {
	if s != nil {
		s.cipher = c
	}
	return s
}

func (s *Service) ListEndpoints(ctx context.Context, actor identity.Actor) ([]Endpoint, error) {
	if err := requireManage(actor); err != nil {
		return nil, err
	}
	records, err := s.store.ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(records))
	for _, record := range records {
		out = append(out, PublicEndpoint(record))
	}
	return out, nil
}

func (s *Service) CreateEndpoint(ctx context.Context, actor identity.Actor, input CreateEndpointInput) (Endpoint, error) {
	if err := requireManage(actor); err != nil {
		return Endpoint{}, err
	}
	if err := s.validateEndpointInput(input.Name, input.TargetURL); err != nil {
		return Endpoint{}, err
	}
	if input.Secret != "" {
		enc, err := s.encryptSecret(input.Secret)
		if err != nil {
			return Endpoint{}, err
		}
		input.Secret = enc
	}
	record, err := s.store.CreateEndpoint(ctx, input)
	if err != nil {
		return Endpoint{}, err
	}
	// PublicEndpoint 不暴露 secret；HasSecret 基于存储非空即可。
	return PublicEndpoint(record), nil
}

func (s *Service) UpdateEndpoint(ctx context.Context, actor identity.Actor, id int64, input UpdateEndpointInput) (Endpoint, error) {
	if err := requireManage(actor); err != nil {
		return Endpoint{}, err
	}
	if input.Name != nil || input.TargetURL != nil {
		name := ""
		if input.Name != nil {
			name = *input.Name
		}
		target := ""
		if input.TargetURL != nil {
			target = *input.TargetURL
		}
		// 部分更新时只校验提供的字段。
		if input.Name != nil && strings.TrimSpace(name) == "" {
			return Endpoint{}, ErrInvalidEndpoint
		}
		if input.TargetURL != nil {
			if err := validateURL(target, s != nil && s.allowHTTP); err != nil {
				return Endpoint{}, err
			}
		}
	}
	if input.Secret != nil && strings.TrimSpace(*input.Secret) != "" {
		enc, err := s.encryptSecret(strings.TrimSpace(*input.Secret))
		if err != nil {
			return Endpoint{}, err
		}
		input.Secret = &enc
	}
	record, err := s.store.UpdateEndpoint(ctx, id, input)
	if err != nil {
		return Endpoint{}, err
	}
	return PublicEndpoint(record), nil
}

// DecryptEndpointSecret 投递时解密 secret；错误密钥 fail closed。
func (s *Service) DecryptEndpointSecret(stored string) (string, error) {
	return s.decryptSecret(stored)
}

func (s *Service) encryptSecret(plaintext string) (string, error) {
	if s == nil || s.cipher == nil {
		return plaintext, nil
	}
	return s.cipher.Encrypt(plaintext)
}

func (s *Service) decryptSecret(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if s == nil || s.cipher == nil || !s.cipher.Enabled() {
		if crypto.IsEncrypted(stored) {
			return "", fmt.Errorf("webhooks: encrypted secret requires option encryption key")
		}
		return stored, nil
	}
	if !crypto.IsEncrypted(stored) {
		// 历史明文：可读；调用方可选择回写（投递路径懒迁移）。
		return stored, nil
	}
	plain, err := s.cipher.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("webhooks: secret decrypt failed: %w", err)
	}
	return plain, nil
}

// MaybeMigrateSecret 若 stored 为明文且 cipher 启用，返回应写回的密文。
func (s *Service) MaybeMigrateSecret(stored string) (encrypted string, ok bool) {
	if stored == "" || s == nil || s.cipher == nil || !s.cipher.Enabled() {
		return "", false
	}
	if crypto.IsEncrypted(stored) {
		return "", false
	}
	enc, err := s.cipher.Encrypt(stored)
	if err != nil {
		return "", false
	}
	return enc, true
}

func (s *Service) DeleteEndpoint(ctx context.Context, actor identity.Actor, id int64) error {
	if err := requireManage(actor); err != nil {
		return err
	}
	return s.store.DeleteEndpoint(ctx, id)
}

func (s *Service) ListDeliveries(ctx context.Context, actor identity.Actor, endpointID int64, limit int) ([]Delivery, error) {
	if err := requireManage(actor); err != nil {
		return nil, err
	}
	return s.store.ListDeliveries(ctx, endpointID, limit)
}

// Fanout 将 observe 事件写入各订阅端点的投递记录并入队（异步，不阻塞业务）。
func (s *Service) Fanout(ctx context.Context, envelope appevents.Envelope) {
	if s == nil || s.store == nil || s.pool == nil || s.jobs == nil {
		return
	}
	if envelope.Name == "" {
		return
	}
	// 仅 observe 类事件对外投递；filter 是同步业务钩子。
	if envelope.Kind != "" && envelope.Kind != appevents.KindObserve {
		return
	}
	endpoints, err := s.store.ListEnabledEndpointsForEvent(ctx, envelope.Name)
	if err != nil || len(endpoints) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"id":            envelope.ID,
		"name":          envelope.Name,
		"kind":          envelope.Kind,
		"actorUserId":   envelope.ActorUserID,
		"resourceType":  envelope.ResourceType,
		"resourceId":    envelope.ResourceID,
		"correlationId": envelope.CorrelationID,
		"payload":       envelope.Payload,
		"occurredAt":    envelope.OccurredAt,
	})
	if err != nil {
		return
	}
	for _, endpoint := range endpoints {
		if err := s.enqueueDelivery(ctx, endpoint.ID, envelope, payload); err != nil {
			// 单端点失败不影响其它订阅方。
			continue
		}
	}
}

func (s *Service) enqueueDelivery(ctx context.Context, endpointID int64, envelope appevents.Envelope, payload []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	delivery, err := s.store.CreateDeliveryTx(ctx, tx, CreateDeliveryInput{
		EndpointID:    endpointID,
		EventName:     envelope.Name,
		EventID:       envelope.ID,
		CorrelationID: envelope.CorrelationID,
		Payload:       payload,
	})
	if err != nil {
		return err
	}
	// 与 Jobs/Webhooks.DeliverArgs 同 kind/JSON，避免 Models→Jobs 循环依赖。
	args := deliverJobArgs{DeliveryID: delivery.ID}
	if _, err := s.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type deliverJobArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (deliverJobArgs) Kind() string { return "webhook.deliver" }
func (deliverJobArgs) enqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: DefaultMaxAttempts,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

func requireManage(actor identity.Actor) error {
	if !actor.Can(identity.PermissionSettingsManage) && !actor.Can(identity.PermissionSettingsSiteManage) {
		return identity.ErrPermissionDenied
	}
	return nil
}

func validateEndpointInput(name, targetURL string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidEndpoint
	}
	return validateURL(targetURL, false)
}

func (s *Service) validateEndpointInput(name, targetURL string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidEndpoint
	}
	allowHTTP := false
	if s != nil {
		allowHTTP = s.allowHTTP
	}
	return validateURL(targetURL, allowHTTP)
}

func validateURL(raw string, allowHTTP bool) error {
	if err := outboundhttp.ValidatePublicURL(raw, outboundhttp.Options{AllowHTTP: allowHTTP}); err != nil {
		// 对客户端统一返回通用校验错误，细节仅留在服务端日志/投递状态。
		return ErrInvalidURL
	}
	return nil
}

func FormatError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}
