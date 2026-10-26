package hostapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
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
		newProtocolV2EventsEmitCommandDefinition(config.Events),
		newProtocolV2AICompleteCommandDefinition(config.AI),
	}
	definitions = append(definitions,
		newProtocolV2AttachmentExtensionCommandDefinitions(config.AttachmentExtensions)...)
	engine, err := newProtocolV2CommandEngineWithInvalidationJobs(
		NewPostgresProtocolV2HostCommandBackend(config.Pool),
		config.ActorDelegations,
		config.Jobs,
		definitions...,
	)
	if err != nil {
		return nil, err
	}
	return newProtocolV2CommandRuntime(engine), nil
}
