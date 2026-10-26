package identity

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// 外部认证 Host 栈的可注入聚合（见 plans/2026-07-27-github-social-login-builtin-plugin.md M1.5）。
//
// 放在 identity 包以避免 bootstrap ↔ providers 循环导入。
// 生产用 Redis 存储 callback 事务与注册票据；测试/本地无 Redis 时回退到内存实现。
// ExternalAuthService 复用 Identity Registry 解析 provider contribution，以及
// PostgresExternalIdentityLinkStore / PostgresExternalAuthStore /
// PostgresProviderActivationStore。
//
// 安全约束：
//   - raw subject 与 digest 永不出现在浏览器/API/日志；
//   - 回调事务一次性消费（Redis lua GETDEL 或内存等价）；
//   - 注册票据一次性消费；
//   - 激活目录默认全 off，CAS + audit。

// ExternalAuthStack 是 Host 外部认证栈的可注入聚合。
type ExternalAuthStack struct {
	Service             *ExternalAuthService
	CallbackStateStore  CallbackStateStore
	RegistrationTickets RegistrationTicketStore
	LinkStore           ExternalIdentityLinkStore
	ExternalAuthStore   *PostgresExternalAuthStore
	ActivationStore     ProviderActivationStore
}

// NewExternalAuthStack 构造 Host 外部认证栈。redisClient 为 nil 时回退到内存存储
// （仅适合测试/本地无 Redis 场景；生产应始终传入 Redis client）。
//
// registrationEnabled / anyUserExists 可选；为 nil 时使用 pool 内部解析。
func NewExternalAuthStack(
	pool *pgxpool.Pool,
	redisClient *redis.Client,
	registry *identityregistry.Registry,
	registrationEnabled func(context.Context) (bool, error),
) ExternalAuthStack {
	linkStore := NewPostgresExternalIdentityLinkStore(pool)
	externalAuthStore := NewPostgresExternalAuthStore(pool)
	activationStore := NewPostgresProviderActivationStore(pool)

	var callbackStore CallbackStateStore
	var ticketStore RegistrationTicketStore
	if redisClient != nil {
		callbackStore = NewRedisCallbackStateStore(redisClient, CallbackStateDefaultTTL)
		ticketStore = NewRedisRegistrationTicketStore(redisClient, RegistrationTicketDefaultTTL)
	} else {
		// 测试/本地无 Redis 回退：进程内内存存储。
		callbackStore = NewInMemoryCallbackStateStore()
		ticketStore = NewInMemoryRegistrationTicketStore()
	}

	deps := ExternalAuthDeps{
		Pool:              pool,
		LinkStore:         linkStore,
		ActivationStore:   activationStore,
		ExternalAuthStore: externalAuthStore,
		// T1D：recent-auth 绑定 session fingerprint，跨 session 隔离。
		RecentAuth:       externalAuthStore,
		RecentAuthMarker: externalAuthStore,
		ProviderContribution: func(providerID string) (identityregistry.ProviderContribution, error) {
			if registry == nil {
				return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
			}
			// Safe Mode 下第三方可执行解析失败 closed（与 ResolveProviderSnapshot 一致）。
			if registry.Snapshot().SafeMode {
				return identityregistry.ProviderContribution{}, ErrExternalAuthProviderUnavailable
			}
			return registry.ResolveProvider(providerID)
		},
		SafeMode: func() bool {
			if registry == nil {
				return false
			}
			return registry.Snapshot().SafeMode
		},
		RegistrationEnabled: registrationEnabled,
	}
	svc := NewExternalAuthService(deps)

	return ExternalAuthStack{
		Service:             svc,
		CallbackStateStore:  callbackStore,
		RegistrationTickets: ticketStore,
		LinkStore:           linkStore,
		ExternalAuthStore:   externalAuthStore,
		ActivationStore:     activationStore,
	}
}
