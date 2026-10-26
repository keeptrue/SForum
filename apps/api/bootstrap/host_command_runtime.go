package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// deferredIdentityAuthorityMutationGate freezes the Host Command catalog before
// the lifecycle stack publishes the Session Policy store, then adopts that store
// so identity status commands share the same writer fence as session effects.
type deferredIdentityAuthorityMutationGate struct {
	mu   sync.RWMutex
	gate identity.IdentityAuthorityMutationGate
}

func (g *deferredIdentityAuthorityMutationGate) Set(gate identity.IdentityAuthorityMutationGate) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.gate = gate
	g.mu.Unlock()
}

func (g *deferredIdentityAuthorityMutationGate) RunSessionPolicyMutation(
	ctx context.Context,
	mutation func() error,
) error {
	if mutation == nil {
		return errors.New("identity: authority mutation is required")
	}
	if g == nil {
		return mutation()
	}
	g.mu.RLock()
	gate := g.gate
	g.mu.RUnlock()
	if gate == nil {
		// 启动早期 Session Policy 尚未发布：命令目录已冻结，但共享 fence 尚未可用。
		// 直接执行 mutation，与 Identity Store 未绑 gate 时的路径一致。
		return mutation()
	}
	return gate.RunSessionPolicyMutation(ctx, mutation)
}

type protocolV2AttachmentStatusMutator struct {
	store *attachments.PostgresStore
}

// protocolV2AttachmentExtensionRuntime 把「扩展复用 Core 附件」接到 Host Command：
// 约束（站点上限/引用上限）来自附件服务，引用行读写走 ExtensionReference store。
// 两条依赖分开注入是有意的：策略是站点配置，引用行是附件领域数据。
type protocolV2AttachmentExtensionRuntime struct {
	service *attachments.Service
	store   *extensionreference.Store
}

func (r protocolV2AttachmentExtensionRuntime) ExtensionAttachmentPolicy(ctx context.Context) (extensionreference.Policy, error) {
	if r.service == nil {
		return extensionreference.Policy{}, fmt.Errorf("attachment extension policy service is unavailable")
	}
	return r.service.ExtensionAttachmentPolicy(ctx)
}

func (r protocolV2AttachmentExtensionRuntime) PrepareClaim(
	ctx context.Context,
	tx pgx.Tx,
	input extensionreference.ClaimInput,
) (*extensionreference.ClaimPlan, error) {
	if r.store == nil {
		return nil, fmt.Errorf("attachment extension reference store is unavailable")
	}
	return r.store.PrepareClaim(ctx, tx, input)
}

func (r protocolV2AttachmentExtensionRuntime) ApplyClaim(
	ctx context.Context,
	tx pgx.Tx,
	plan *extensionreference.ClaimPlan,
) (extensionreference.Claim, error) {
	if r.store == nil {
		return extensionreference.Claim{}, fmt.Errorf("attachment extension reference store is unavailable")
	}
	return r.store.ApplyClaim(ctx, tx, plan)
}

func (r protocolV2AttachmentExtensionRuntime) Count(
	ctx context.Context,
	tx pgx.Tx,
	input extensionreference.ReleaseInput,
) (int, error) {
	if r.store == nil {
		return 0, fmt.Errorf("attachment extension reference store is unavailable")
	}
	return r.store.Count(ctx, tx, input)
}

func (r protocolV2AttachmentExtensionRuntime) Release(
	ctx context.Context,
	tx pgx.Tx,
	input extensionreference.ReleaseInput,
) (extensionreference.Release, error) {
	if r.store == nil {
		return extensionreference.Release{}, fmt.Errorf("attachment extension reference store is unavailable")
	}
	return r.store.Release(ctx, tx, input)
}

func (r protocolV2AttachmentExtensionRuntime) List(
	ctx context.Context,
	tx pgx.Tx,
	input extensionreference.ListInput,
) (extensionreference.List, error) {
	if r.store == nil {
		return extensionreference.List{}, fmt.Errorf("attachment extension reference store is unavailable")
	}
	return r.store.List(ctx, tx, input)
}

type protocolV2CommandRuntimeBinder func(*hostapi.Gateway) error

// 命令目录必须在首个插件 broker 注册前冻结，而事件总线（Manager）与 AI 网关
// （依赖 Secret Store）都在那之后才就绪。两个 deferred 目标就是这中间的占位：
// 它们实现命令层需要的窄接口，未回填时显式报告不可用 —— 插件不会「以为发布
// 成功了」也不会「以为补全没接线就自己直连供应商」。
type deferredEventPublisher struct {
	mu        sync.RWMutex
	publisher hostapi.ProtocolV2EventPublisher
}

func (p *deferredEventPublisher) Set(publisher hostapi.ProtocolV2EventPublisher) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.publisher = publisher
	p.mu.Unlock()
}

func (p *deferredEventPublisher) EmitFromExtension(
	ctx context.Context,
	extensionID string,
	envelope appevents.Envelope,
) appevents.Result {
	if p == nil {
		return appevents.Result{OK: false, Reason: "events.publisher_unavailable"}
	}
	p.mu.RLock()
	publisher := p.publisher
	p.mu.RUnlock()
	if publisher == nil {
		return appevents.Result{OK: false, Reason: "events.publisher_unavailable"}
	}
	return publisher.EmitFromExtension(ctx, extensionID, envelope)
}

type deferredAIGateway struct {
	mu      sync.RWMutex
	gateway hostapi.ProtocolV2AIGateway
}

func (g *deferredAIGateway) Set(gateway hostapi.ProtocolV2AIGateway) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.gateway = gateway
	g.mu.Unlock()
}

func (g *deferredAIGateway) Execute(ctx context.Context, request supportai.CompletionRequest) (supportai.CompletionResult, error) {
	if g == nil {
		return supportai.CompletionResult{}, supportai.ErrGatewayUnavailable
	}
	g.mu.RLock()
	gateway := g.gateway
	g.mu.RUnlock()
	if gateway == nil {
		return supportai.CompletionResult{}, supportai.ErrGatewayUnavailable
	}
	return gateway.Execute(ctx, request)
}

// protocolV2CommandRuntimeDeferred 把两个后填目标交给装配的后续阶段。
type protocolV2CommandRuntimeDeferred struct {
	Events *deferredEventPublisher
	AI     *deferredAIGateway
}

func (m protocolV2AttachmentStatusMutator) MutateProtocolV2AttachmentStatus(
	ctx context.Context,
	tx pgx.Tx,
	attachmentID int64,
	status string,
) (hostapi.ProtocolV2AttachmentStatusResult, error) {
	if m.store == nil {
		return hostapi.ProtocolV2AttachmentStatusResult{}, fmt.Errorf("attachment status store is unavailable")
	}
	attachment, err := m.store.UpdateStatusTx(ctx, tx, attachmentID, status, false)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, attachments.ErrAttachmentNotFound) {
		return hostapi.ProtocolV2AttachmentStatusResult{}, hostapi.ErrProtocolV2AttachmentNotFound
	}
	if err != nil {
		return hostapi.ProtocolV2AttachmentStatusResult{}, err
	}
	return hostapi.ProtocolV2AttachmentStatusResult{
		ID: attachment.ID, Status: attachment.Status,
		ReferenceCount: attachment.ReferenceCount, UpdatedAt: attachment.UpdatedAt,
	}, nil
}

func bindPostgresProtocolV2CommandRuntime(
	gateway *hostapi.Gateway,
	pool *pgxpool.Pool,
	jobs *supportjobs.Dispatcher,
	moderationStore *moderation.PostgresStore,
	attachmentStore *attachments.PostgresStore,
	attachmentService *attachments.Service,
	extensionReferences *extensionreference.Store,
	identityAuthority identity.IdentityAuthorityMutationGate,
) (protocolV2CommandRuntimeDeferred, error) {
	deferred := protocolV2CommandRuntimeDeferred{
		Events: &deferredEventPublisher{},
		AI:     &deferredAIGateway{},
	}
	if gateway == nil {
		return deferred, fmt.Errorf("Host Command gateway is required")
	}
	if attachmentStore == nil || attachmentService == nil || extensionReferences == nil {
		return deferred, fmt.Errorf("attachment extension reference runtime is required")
	}
	delegations, err := hostapi.NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		return deferred, fmt.Errorf("create Host Command actor delegation authority: %w", err)
	}
	runtime, err := hostapi.NewPostgresProtocolV2CommandRuntime(hostapi.PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: delegations, Jobs: jobs, Moderation: moderationStore,
		AttachmentStatuses:   protocolV2AttachmentStatusMutator{store: attachmentStore},
		AttachmentExtensions: protocolV2AttachmentExtensionRuntime{service: attachmentService, store: extensionReferences},
		IdentityAuthority:    identityAuthority,
		Events:               deferred.Events,
		AI:                   deferred.AI,
	})
	if err != nil {
		return deferred, fmt.Errorf("create Host Command runtime: %w", err)
	}
	if err := gateway.BindProtocolV2CommandRuntime(runtime); err != nil {
		return deferred, fmt.Errorf("bind Host Command runtime: %w", err)
	}
	return deferred, nil
}

func postgresProtocolV2CommandRuntimeBinder(
	pool *pgxpool.Pool,
	jobs *supportjobs.Dispatcher,
	moderationStore *moderation.PostgresStore,
	attachmentStore *attachments.PostgresStore,
	attachmentService *attachments.Service,
	extensionReferences *extensionreference.Store,
) protocolV2CommandRuntimeBinder {
	return func(gateway *hostapi.Gateway) error {
		_, err := bindPostgresProtocolV2CommandRuntime(
			gateway, pool, jobs, moderationStore,
			attachmentStore, attachmentService, extensionReferences, nil,
		)
		return err
	}
}

var _ hostapi.ProtocolV2AttachmentStatusMutator = protocolV2AttachmentStatusMutator{}
var _ hostapi.ProtocolV2AttachmentExtensionRuntime = protocolV2AttachmentExtensionRuntime{}
