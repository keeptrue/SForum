package extensions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	ActivationTriggerEnable  = "enable"
	ActivationTriggerStartup = "startup"

	ActivationStatusStarting = "starting"
	ActivationStatusHealthy  = "healthy"
	ActivationStatusFailed   = "failed"
	ActivationStatusSkipped  = "skipped"
)

var ErrActivationAttemptNotFound = errors.New("extensions: activation attempt not found")

type ActivationAttempt struct {
	ID               int64      `json:"id"`
	ExtensionID      string     `json:"extensionId"`
	ExtensionVersion string     `json:"extensionVersion"`
	PackageDigest    string     `json:"packageDigest"`
	BootID           string     `json:"bootId"`
	Trigger          string     `json:"trigger"`
	Status           string     `json:"status"`
	ActorUserID      int64      `json:"actorUserId,omitempty"`
	FailureReason    string     `json:"failureReason,omitempty"`
	StartedAt        time.Time  `json:"startedAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
}

type ActivationAttemptStore interface {
	LatestActivationAttempt(context.Context, string, string) (ActivationAttempt, error)
	BeginActivationAttempt(context.Context, Extension, string, string, int64) (ActivationAttempt, error)
	CompleteActivationAttempt(context.Context, int64, string, string) error
	RecordSkippedActivation(context.Context, Extension, string, string) error
}

type ActivationCoordinator struct {
	store   ActivationAttemptStore
	auditor audit.Writer
}

func NewActivationCoordinator(store ActivationAttemptStore) *ActivationCoordinator {
	return &ActivationCoordinator{store: store}
}

func (c *ActivationCoordinator) WithAuditor(writer audit.Writer) *ActivationCoordinator {
	c.auditor = writer
	return c
}

func (c *ActivationCoordinator) ShouldSkipStartup(ctx context.Context, extension Extension, bootID string) (bool, error) {
	if c == nil || c.store == nil || extension.Status != StatusEnabled || !isExecutablePlugin(extension) {
		return false, nil
	}
	latest, err := c.store.LatestActivationAttempt(ctx, extension.ID, extension.PackageDigest)
	if errors.Is(err, ErrActivationAttemptNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if latest.Status == ActivationStatusHealthy {
		return false, nil
	}
	if latest.Status == ActivationStatusStarting {
		if err := c.store.CompleteActivationAttempt(ctx, latest.ID, ActivationStatusFailed, "previous_boot_incomplete"); err != nil {
			return false, err
		}
	}
	if err := c.store.RecordSkippedActivation(ctx, extension, normalizedBootID(bootID), "boot_loop_guard"); err != nil {
		return false, err
	}
	c.appendAudit(ctx, audit.ActionExtensionActivationSkipped, extension, "boot_loop_guard")
	return true, nil
}

func (c *ActivationCoordinator) Start(
	ctx context.Context,
	runtime RuntimeManager,
	extension Extension,
	trigger string,
	actorUserID int64,
	bootID string,
) error {
	if runtime == nil || !isExecutablePlugin(extension) {
		return nil
	}
	if c == nil || c.store == nil {
		return runtime.Start(activationRuntimeContext(ctx, c, extension, actorUserID), extension)
	}
	attempt, err := c.store.BeginActivationAttempt(ctx, extension, trigger, normalizedBootID(bootID), actorUserID)
	if err != nil {
		return err
	}
	startCtx := activationRuntimeContext(ctx, c, extension, actorUserID)
	if err := runtime.Start(startCtx, extension); err != nil {
		_ = c.store.CompleteActivationAttempt(ctx, attempt.ID, ActivationStatusFailed, boundedActivationReason(err.Error()))
		c.appendAudit(ctx, audit.ActionExtensionActivationFailed, extension, err.Error())
		return err
	}
	if err := c.store.CompleteActivationAttempt(ctx, attempt.ID, ActivationStatusHealthy, ""); err != nil {
		_ = runtime.Stop(ctx, extension)
		return fmt.Errorf("complete extension activation attempt: %w", err)
	}
	return nil
}

func (c *ActivationCoordinator) appendAudit(ctx context.Context, action string, extension Extension, reason string) {
	if c == nil || c.auditor == nil {
		return
	}
	_ = c.auditor.Append(ctx, audit.Event{Action: action, Metadata: map[string]any{
		"extensionId": extension.ID, "version": extension.Version,
		"packageDigest": extension.PackageDigest, "reason": boundedActivationReason(reason),
	}})
}

// activationRuntimeContext 为一次操作者发起的启动准备运行时上下文。
//
// 需要精确数据库授权的插件（own_schema 及以上）只有在 actor + 审计锚点都到位
// 时才能自建授权行；这里在启动前写一条 extension.database_grant 审计事件，并把
// actor/事件 id 通过 context 交给运行时租约层。Host 权威启动（actorUserID <= 0）
// 与审计写入失败都只返回原 ctx，于是租约层退回 host 分支：缺精确授权就失败关闭，
// 绝不静默造出无审计锚点的授权。
func activationRuntimeContext(
	ctx context.Context,
	coordinator *ActivationCoordinator,
	extension Extension,
	actorUserID int64,
) context.Context {
	if coordinator == nil || coordinator.auditor == nil || actorUserID <= 0 || !RequiresDatabaseGrantAuthority(extension) {
		return ctx
	}
	writer, ok := coordinator.auditor.(audit.IDWriter)
	if !ok {
		return ctx
	}
	eventID, err := writer.AppendReturningID(ctx, audit.Event{
		ActorUserID: actorUserID,
		Action:      audit.ActionExtensionDatabaseGrant,
		Metadata: map[string]any{
			"extensionId": extension.ID, "version": extension.Version,
			"packageDigest": extension.PackageDigest, "trigger": "activation",
		},
	})
	if err != nil || eventID <= 0 {
		return ctx
	}
	return WithDatabaseGrantAuthority(ctx, DatabaseGrantAuthority{
		ActorUserID: actorUserID, AuditEventID: eventID,
	})
}

func isExecutablePlugin(extension Extension) bool {
	return extension.Type == TypePlugin && strings.TrimSpace(extension.Manifest.Backend.Entry) != ""
}

func NewActivationBootID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return hex.EncodeToString(random)
	}
	return fmt.Sprintf("boot-%d", time.Now().UTC().UnixNano())
}

func normalizedBootID(bootID string) string {
	bootID = strings.TrimSpace(bootID)
	if bootID == "" {
		return NewActivationBootID()
	}
	return bootID
}

func boundedActivationReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 2048 {
		return reason[:2048]
	}
	return reason
}

// DatabaseGrantAuthority 是一次「由操作者发起」的启动所携带的授权凭据。
//
// 扩展数据库运行时租约只在两种权威下签发（见
// Support/Extensions 的 ExtensionDatabaseLeaseIssuer*）：
//
//   - Host 权威：启动期没有操作者（进程重启、Reconcile、Safe Mode 恢复）。
//     它只能复用已经存在的精确授权行，缺行即失败关闭。
//   - Actor 权威：运营者在后台显式启用/升级插件，此时携带 actorUserId 与
//     归属同一次操作的审计事件 id。只有这条分支允许自建 own_schema 等直接
//     运行时权力的精确授权行。
//
// 该凭据通过启动 context 传递，而不是通过 Manager/Starter 的签名逐层透传：
// 启动上下文是唯一的传递边界，配置里没有第二个可以伪造它的入口，调用方
// 也无法把它用在 host 分支以外的任何地方。
type DatabaseGrantAuthority struct {
	ActorUserID  int64
	AuditEventID int64
}

type databaseGrantAuthorityContextKey struct{}

// WithDatabaseGrantAuthority 把操作者授权附到 ctx 上。nil ctx 或无效凭据
// （任一 id 非正）原样返回：宁可不供给，也不写入一个没有审计锚点的半截权威。
func WithDatabaseGrantAuthority(ctx context.Context, authorities ...DatabaseGrantAuthority) context.Context {
	if ctx == nil || len(authorities) == 0 {
		return ctx
	}
	authority := authorities[0]
	if authority.ActorUserID <= 0 || authority.AuditEventID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, databaseGrantAuthorityContextKey{}, authority)
}

// DatabaseGrantAuthorityFrom 读取 ctx 上的操作者授权；没有则返回零值与 false。
func DatabaseGrantAuthorityFrom(ctx context.Context) (DatabaseGrantAuthority, bool) {
	if ctx == nil {
		return DatabaseGrantAuthority{}, false
	}
	authority, ok := ctx.Value(databaseGrantAuthorityContextKey{}).(DatabaseGrantAuthority)
	if !ok || authority.ActorUserID <= 0 || authority.AuditEventID <= 0 {
		return DatabaseGrantAuthority{}, false
	}
	return authority, true
}

// RequiresDatabaseGrantAuthority 报告该扩展是否会申请需要精确授权行的数据库
// 直接运行时权力。它只决定「这次启动有没有必要写授权审计锚点」，不参与授权
// 判定本身：真正的判定仍在运行时租约层。
func RequiresDatabaseGrantAuthority(extension Extension) bool {
	database := extension.Manifest.Database
	if database == nil {
		return false
	}
	for _, grant := range extensionmanifest.DatabaseGrants(database) {
		switch grant {
		case extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantKernel:
			return true
		}
	}
	return false
}
