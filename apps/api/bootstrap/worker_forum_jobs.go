package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	forumjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Forum"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/config"
)

// 本文件承载 forum 域的 worker 注册。原本这些函数散落在 worker.go 里，
// 而 worker.go 已经顶到 1000 行闸门：新增一个周期任务不应以「先撑破闸门」
// 为代价，所以按域拆出来。

// registerForumAutoLockWorker 注册闲置主题自动锁帖 worker。
// idle days 每次执行时从 web_options 读取；0 或缺失时 job 空跑。
func registerForumAutoLockWorker(registry *supportjobs.Registry, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) {
	optionStore := options.NewPostgresStore(pool)
	optionsService := options.NewServiceWithDefaults(optionStore, optionsDefaultsFromConfig(cfg))
	forumStore := forum.NewPostgresStore(pool)

	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[forumjobs.AutoLockIdleArgs](workers, &forumjobs.AutoLockIdleWorker{
			Locker: forumStore,
			IdleDays: func(ctx context.Context) (int, error) {
				raw, err := optionsService.WebOption(ctx, options.NameForumTopicsAutoLockIdleDays)
				if err != nil {
					return 0, err
				}
				if days, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && days > 0 {
					return days, nil
				}
				return 0, nil
			},
			Logger: logger,
		})
	})
}

// registerForumFlushViewCountsWorker 注册浏览量刷盘（D3 / M2）。
// redisClient 为 nil 时仍注册 worker，但 Drain 空跑（嵌入路径应注入 shared Redis）。
func registerForumFlushViewCountsWorker(registry *supportjobs.Registry, pool *pgxpool.Pool, redisClient *redis.Client, logger *slog.Logger) {
	forumStore := forum.NewPostgresStore(pool)
	counter := forum.NewRedisTopicViewCounter(redisClient).WithLogger(logger)
	forumjobs.RegisterFlushViewCounts(registry, &forumjobs.FlushViewCountsWorker{
		Drainer: counter,
		Store:   forumStore,
		Logger:  logger,
	})
}

// deferredTxEnqueuer 让 worker 侧的 Outbox 先注册、后绑定 River client。
//
// 与 hostapi.RiverJobEnqueuer 同一姿态：注册阶段 client 还不存在（它要等
// workers 构建完才能建），所以这里留一个可后绑定的壳；未绑定前入队会明确
// 失败，而不是静默丢掉邮件投递。
type deferredTxEnqueuer struct {
	mu         sync.RWMutex
	dispatcher *supportjobs.Dispatcher
}

// Bind 在 River client 建好后回填 dispatcher。
func (e *deferredTxEnqueuer) Bind(dispatcher *supportjobs.Dispatcher) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dispatcher = dispatcher
}

func (e *deferredTxEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error) {
	if e == nil {
		return nil, fmt.Errorf("worker job enqueuer is not configured")
	}
	e.mu.RLock()
	dispatcher := e.dispatcher
	e.mu.RUnlock()
	if dispatcher == nil {
		return nil, fmt.Errorf("worker job enqueuer is not bound to a river client")
	}
	return dispatcher.EnqueueTx(ctx, tx, args, opts)
}

// registerForumBookmarkRemindersWorker 注册到期收藏提醒 worker，并登记它的
// 周期 schedule（每分钟一轮）。返回的 enqueuer 必须在 River client 建好后 Bind，
// 否则提醒通知的邮件投递会入队失败。
func registerForumBookmarkRemindersWorker(
	registry *supportjobs.Registry,
	coreScheduleConstructors map[string]river.PeriodicJobConstructor,
	wrapEnabled func(string, river.PeriodicJobConstructor) river.PeriodicJobConstructor,
	cfg config.Config,
	pool *pgxpool.Pool,
	logger *slog.Logger,
) (*deferredTxEnqueuer, *deferredBackfillEnqueuer) {
	optionStore := options.NewPostgresStore(pool)
	optionsService := options.NewServiceWithDefaults(optionStore, optionsDefaultsFromConfig(cfg))
	enqueuer := &deferredTxEnqueuer{}
	outbox := notifications.NewOutbox(pool, notifications.NewPostgresStore(pool), enqueuer, options.NewMailSettings(optionsService))
	// 复用既有的 forumNotificationAdapter：它已实现 forum.NotificationWriter，
	// 并额外实现了 BookmarkReminderNotifier（提醒投影）。
	forumStore := forum.NewPostgresStore(pool).WithCommentNotifications(forumNotificationAdapter{outbox: outbox})
	forumjobs.RegisterBookmarkReminders(registry, forumStore, logger)
	// 徽章评估 worker 与收藏提醒共用同一个 outbox：通知出口只有一套装配。
	registerBadgesEvaluateUserWorker(registry, pool, outbox, logger)
	// 规则回扫 worker：与徽章评估共用 pool 与同一个 outbox（回扫授予也要发通知），
	// 入队入口延迟到 River client 建好后 Bind。
	backfillEnqueuer := &deferredBackfillEnqueuer{}
	registerBadgesBackfillWorker(registry, pool, outbox, backfillEnqueuer, logger)
	// 由调用方在 River client 建好后 Bind，见 worker.go。
	coreScheduleConstructors[supportjobs.ScheduleForumBookmarkReminders] = wrapEnabled(
		supportjobs.ScheduleForumBookmarkReminders,
		func() (river.JobArgs, *river.InsertOpts) {
			return forumjobs.BookmarkRemindersArgs{}, nil
		},
	)
	return enqueuer, backfillEnqueuer
}
