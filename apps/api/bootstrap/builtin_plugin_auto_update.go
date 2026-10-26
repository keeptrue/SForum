package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/builtinauto"
	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions/systemactor"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

const (
	builtinAutoUpdateLockKey      = "sforum.extension.builtin_auto_update"
	builtinAutoUpdateTimeout      = 3 * time.Minute
	builtinAutoUpdateCloseTimeout = 5 * time.Second
)

// runBuiltinPluginAutoUpdate 是 Host 启动阶段对受保护内置插件的一次性升级扫描。
// 它永远不阻断启动：策略/锁/单个扩展失败只写日志和 extension_events。
func runBuiltinPluginAutoUpdate(
	ctx context.Context,
	safeMode bool,
	core *apiCoreStack,
	logger *slog.Logger,
) {
	if core == nil || core.extensionService == nil || core.extensionStore == nil ||
		core.identityStore == nil || core.optionsService == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorContext(ctx, "builtin plugin auto-update panicked", "panic", recovered)
		}
	}()
	recoveryActive := core.pluginRuntimeRecovery != nil && core.pluginRuntimeRecovery.Active()
	if safeMode || recoveryActive {
		logger.InfoContext(ctx, "builtin plugin auto-update skipped",
			"safeMode", safeMode, "recoveryActive", recoveryActive)
		return
	}
	withBuiltinAutoUpdateLock(ctx, core.pool, logger, func(lockCtx context.Context) {
		updater := builtinauto.New(
			core.extensionStore,
			core.extensionService,
			builtinAutoOptionPolicy{service: core.optionsService},
			&builtinAutoActorProvider{store: core.identityStore},
			&builtinAutoProviderProbe{
				store:       core.extensionStore,
				attachments: core.attachmentService,
			},
			logger,
			false,
			false,
		)
		runCtx, cancel := context.WithTimeout(lockCtx, builtinAutoUpdateTimeout)
		defer cancel()
		summary, err := updater.Run(runCtx)
		if err != nil {
			logger.WarnContext(lockCtx, "builtin plugin auto-update failed", "error", err)
			return
		}
		if summary.Empty() {
			return
		}
		logger.InfoContext(lockCtx, "builtin plugin auto-update finished",
			"updated", summary.Updated, "skipped", summary.Skipped, "failed", summary.Failed)
	})
}

// withBuiltinAutoUpdateLock 使用独立 pgx 连接持有 session advisory lock。
// 连接在扫描结束后关闭，因此即使 unlock 失败也不会把锁留在连接池里。
func withBuiltinAutoUpdateLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	logger *slog.Logger,
	fn func(context.Context),
) {
	if pool == nil || fn == nil {
		return
	}
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		logger.WarnContext(ctx, "builtin plugin auto-update lock connection failed", "error", err)
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), builtinAutoUpdateCloseTimeout)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, builtinAutoUpdateLockKey).Scan(&acquired); err != nil {
		logger.WarnContext(ctx, "builtin plugin auto-update lock acquisition failed", "error", err)
		return
	}
	if !acquired {
		logger.InfoContext(ctx, "builtin plugin auto-update skipped", "reason", "another_node_is_running")
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), builtinAutoUpdateCloseTimeout)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, builtinAutoUpdateLockKey)
	}()
	fn(ctx)
}

type builtinAutoActorProvider struct {
	store *identity.PostgresStore

	mu    sync.Mutex
	actor identity.Actor
}

func (p *builtinAutoActorProvider) EnsureBuiltinAutoActor(ctx context.Context) (identity.Actor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.actor.ID > 0 {
		return p.actor, nil
	}
	actor, err := systemactor.Ensure(ctx, p.store, systemactor.Spec{
		Username:            "builtin-auto",
		Email:               "builtin-auto@sforum.invalid",
		DisplayName:         "SForum 内置插件自动更新",
		RoleKey:             identity.RoleBuiltinAuto,
		RequireExistingUser: true,
	})
	if err != nil {
		return identity.Actor{}, err
	}
	p.actor = actor
	return actor, nil
}

type builtinAutoProviderProbe struct {
	store       *extensions.PostgresStore
	attachments *attachments.Service
}

func (p *builtinAutoProviderProbe) ProviderSelected(ctx context.Context, extensionID string) (bool, error) {
	if p == nil {
		return false, nil
	}
	if p.store != nil {
		selected, err := p.store.ProviderSlotSelected(ctx, extensionID)
		if err != nil {
			return false, err
		}
		if selected {
			return true, nil
		}
		mail, err := p.store.SelectedMailProvider(ctx)
		if err != nil {
			return false, err
		}
		if mail == extensionID {
			return true, nil
		}
		search, err := p.store.SelectedSearchProvider(ctx)
		if err != nil {
			return false, err
		}
		if search == extensionID {
			return true, nil
		}
	}
	if p.attachments != nil {
		storageID, err := p.attachments.SelectedStorageProviderExtension(ctx)
		if err != nil {
			return false, err
		}
		if storageID == extensionID {
			return true, nil
		}
	}
	return false, nil
}

type builtinAutoOptionPolicy struct {
	service *options.Service
}

func (p builtinAutoOptionPolicy) BuiltinAutoUpdateEnabled(ctx context.Context) (bool, error) {
	return options.BuiltinAutoUpdateEnabled(ctx, p.service)
}
