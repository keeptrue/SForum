package hostapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type ProtocolV2IdentityAuthorityMutationGate interface {
	RunSessionPolicyMutation(context.Context, func() error) error
}

type PostgresProtocolV2CommandRuntimeConfig struct {
	Pool               *pgxpool.Pool
	ActorDelegations   *ProtocolV2ActorDelegationAuthority
	Jobs               *supportjobs.Dispatcher
	Moderation         *moderation.PostgresStore
	AttachmentStatuses ProtocolV2AttachmentStatusMutator
	// AttachmentExtensions 是扩展复用 Core 附件（claim/release/list）的领域边界。
	AttachmentExtensions ProtocolV2AttachmentExtensionRuntime
	IdentityAuthority    ProtocolV2IdentityAuthorityMutationGate
	// Events 是插件发布自身事件的宿主事件总线。为 nil 时事件发布命令报告
	// 不可用而不是被静默省略 —— 目录是密封的，缺依赖必须显式可见。
	Events ProtocolV2EventPublisher
	// AI 是插件发起补全的宿主网关。同样为 nil 时报告不可用：让插件悄悄退回
	// 自建出站就等于把成本重新放回账外。
	AI ProtocolV2AIGateway
	// TrustLevels 是信任等级来源（与 identity store 同一实例）。nil 时插件命令
	// 授权看不到已挣得权限——装配层必须注入，否则「HTTP 放行、插件 403」（D22）。
	TrustLevels identity.TrustLevelSource
	// BadgeFactKeys 返回某扩展自己声明的事实 key。nil 时 fail-closed
	// （所有权无法证明 ⇒ 一律 `fact_key_not_declared`），绝不放行。
	BadgeFactKeys ProtocolV2BadgeFactDeclarationReader
	// BadgeFacts 是插件事实的写口（`*badges.PostgresStore` 满足）。
	//
	// 为 nil 时命令**仍然注册**、但一律拒绝 `fact_write_failed`：目录是密封的，
	// 少一个依赖不能让调用方拿到 unknown command（那会让插件以为是版本不支持）。
	BadgeFacts ProtocolV2BadgeFactRecorder
}

// NewPostgresProtocolV2CommandRuntime publishes the complete immutable domain
// catalog. Missing dependencies fail boot instead of silently omitting a
// command and creating different API/worker capabilities.
func NewPostgresProtocolV2CommandRuntime(config PostgresProtocolV2CommandRuntimeConfig) (ProtocolV2CommandRuntime, error) {
	if config.Pool == nil || config.ActorDelegations == nil || config.Jobs == nil ||
		config.Moderation == nil || config.AttachmentStatuses == nil ||
		config.AttachmentExtensions == nil {
		return nil, errors.New("hostapi: complete PostgreSQL Host Command runtime dependencies are required")
	}
	identityCommand := newProtocolV2IdentityUserStatusCommandDefinition()
	if config.IdentityAuthority != nil {
		identityCommand.RunAuthorityMutation = config.IdentityAuthority.RunSessionPolicyMutation
	}
	definitions := []protocolV2CommandDefinition{
		identityCommand,
		newProtocolV2TopicVisibilityCommandDefinition(config.Pool, config.Jobs),
		newProtocolV2EntityMetaCommandDefinition(config.Pool),
		newProtocolV2ModerationCommandDefinition(config.Moderation, config.Jobs),
		newProtocolV2EntitlementCommandDefinition(config.Pool),
		newProtocolV2AttachmentStatusCommandDefinition(config.AttachmentStatuses),
		newProtocolV2ExtensionPluginDisableCommandDefinition(),
		newProtocolV2ExtensionSettingsResetCommandDefinition(),
		newProtocolV2ExtensionSettingsUpdateCommandDefinition(),
		newProtocolV2ExtensionSettingsActionCommandDefinition(),
		newProtocolV2NotificationEmitCommandDefinition(config.Pool, config.Jobs),
		newProtocolV2BadgeFactRecordCommandDefinition(config.Pool, config.BadgeFacts, config.BadgeFactKeys),
		newProtocolV2EventsEmitCommandDefinition(config.Events),
		newProtocolV2AICompleteCommandDefinition(config.AI),
	}
	definitions = append(definitions,
		newProtocolV2AttachmentExtensionCommandDefinitions(config.AttachmentExtensions, config.TrustLevels)...)
	// 两步写死：wrapper 必须原样进引擎。链式调用曾经在这里把 wrapper 换成中性
	// backend，host_commands 授权闸门因此静默失效（f63ad2cf9 的 P0）。wrapper 上的
	// WithTrustLevelSource 现在返回 wrapper，但这里仍写显式两步，读的人不必推理方法提升。
	hostBackend := NewPostgresProtocolV2HostCommandBackend(config.Pool)
	hostBackend.PostgresProtocolV2CommandBackend.WithTrustLevelSource(config.TrustLevels)
	engine, err := newProtocolV2CommandEngineWithInvalidationJobs(
		hostBackend,
		config.ActorDelegations,
		config.Jobs,
		definitions...,
	)
	if err != nil {
		return nil, err
	}
	return newProtocolV2CommandRuntime(engine), nil
}
