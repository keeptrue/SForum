package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
	privacy "github.com/keeptrue/sforum/apps/api/app/Support/Privacy"
)

// NewPrivacyRegistry 构造生产形态的 Privacy Registry：真实 RBAC 权限门 +
// PostgreSQL 审计。API 装配与带外 CLI 恢复（`privacy:erase-user`）共用它，
// 让「谁能擦除、擦了什么」只有一份真相。
//
// 这也是 D2 拍板的落地：`Registry.EraseUser` 的第一个生产入口是这条 CLI
// （管理端 UI 另议）；权限门槛就是这里的真实 RBAC，不是「actor 非空即允许」。
func NewPrivacyRegistry(pool *pgxpool.Pool) (*privacy.Registry, error) {
	if pool == nil {
		return nil, fmt.Errorf("bootstrap: privacy registry requires PostgreSQL")
	}
	registry := privacy.New()
	auditor, err := privacy.NewPostgresAuditor(pool)
	if err != nil {
		return nil, fmt.Errorf("create privacy auditor: %w", err)
	}
	registry.SetAuditor(auditor)
	registry.SetPermissionCheck(func(ctx context.Context, actor, userID, operation string) error {
		return checkPrivacyPermission(ctx, identity.NewPostgresStore(pool), actor, userID, operation)
	})
	return registry, nil
}

// checkPrivacyPermission 使用真实 RBAC：export/erase 需要 user.manage（或 super_admin）。
// 禁止「actor 字符串非空即允许」。
func checkPrivacyPermission(
	ctx context.Context,
	store *identity.PostgresStore,
	actorRef, userID, operation string,
) error {
	_ = userID
	_ = operation
	actorRef = strings.TrimSpace(actorRef)
	if actorRef == "" || store == nil {
		return privacy.ErrPermissionDenied
	}
	// actor 形如 user:123
	var userIDInt int64
	if !strings.HasPrefix(actorRef, "user:") {
		return privacy.ErrPermissionDenied
	}
	if _, err := fmt.Sscanf(actorRef, "user:%d", &userIDInt); err != nil || userIDInt <= 0 {
		return privacy.ErrPermissionDenied
	}
	actor, err := store.LoadActor(ctx, userIDInt)
	if err != nil {
		return privacy.ErrPermissionDenied
	}
	// super_admin 或 user.manage 可执行隐私导出/擦除。
	if actor.IsSuperAdmin() || actor.Can(identity.PermissionUserManage) {
		return nil
	}
	return privacy.ErrPermissionDenied
}

// NewPrivacyRegistryForOutband 给带外 CLI 用：在 NewPrivacyRegistry 之上，把
// 宿主自有的「插件命名空间会话状态」擦除 contribution 按与 API 装配**完全相同
// 的注册路径**接进来。没有它，CLI 会报告成功却擦不掉插件会话状态——入口
// 「声明了却擦不全」比没有入口更糟（D2 的完整语义）。
//
// Redis 不可达时 fail closed 并报命名错误，绝不静默跳过该 contribution。
func NewPrivacyRegistryForOutband(
	ctx context.Context,
	pool *pgxpool.Pool,
	redisAddr string,
	redisPassword string,
	redisDB int,
) (*privacy.Registry, error) {
	registry, err := NewPrivacyRegistry(pool)
	if err != nil {
		return nil, err
	}
	installationID, err := installationidentity.NewPostgresRepository(pool).Ensure(ctx)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: resolve installation identity: %w", err)
	}
	client := redis.NewClient(&redis.Options{Addr: redisAddr, Password: redisPassword, DB: redisDB})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("bootstrap: privacy:erase-user requires Redis at %s (plugin session state erasure): %w", redisAddr, err)
	}
	backend, err := pluginsessionstate.NewRedisBackend(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("bootstrap: create plugin session state backend: %w", err)
	}
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("bootstrap: create plugin session state store: %w", err)
	}
	sessionState := &productionPluginSessionState{Store: store, installationID: installationID}
	if err := registerPluginSessionStatePrivacyContribution(registry, sessionState, identity.NewPostgresStore(pool)); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("bootstrap: register plugin session state privacy contribution: %w", err)
	}
	return registry, nil
}
