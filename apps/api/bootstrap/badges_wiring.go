package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	badgesjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Badges"
	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// badgeEvaluationEnqueuer 把 Forum / Badges 侧的 fail-open 入队契约落到 River。
//
// 契约本身没有 error 返回值（见 forum.BadgeEvaluationEnqueuer）：统计已经落库，
// 评估迟到几秒可以接受，让写请求因为队列抖动失败不可接受。所以这里只记日志。
type badgeEvaluationEnqueuer struct {
	dispatcher *supportjobs.Dispatcher
	logger     *slog.Logger
}

func newBadgeEvaluationEnqueuer(dispatcher *supportjobs.Dispatcher, logger *slog.Logger) badgeEvaluationEnqueuer {
	return badgeEvaluationEnqueuer{dispatcher: dispatcher, logger: logger}
}

// EnqueueBadgeEvaluation 入队一次 badges.evaluate_user。
//
// 去重键等价于 badges:evaluate:{userID}：args 只有 UserID，配合 river 的
// ByArgs 唯一性，同一用户短时间内的多次触发会被合并成一次。
func (e badgeEvaluationEnqueuer) EnqueueBadgeEvaluation(ctx context.Context, userID int64) {
	if e.dispatcher == nil || userID <= 0 {
		return
	}
	args := badgesjobs.EvaluateUserArgs{UserID: userID}
	if _, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
		if e.logger != nil {
			e.logger.Warn("enqueue badge evaluation failed", "user_id", userID, "error", err)
		}
	}
}

// badgesEvaluatorAdapter 把领域评估器适配成 job 层需要的形状。
//
// 转换放在装配层而不是领域层：领域不该知道 job 的存在，job 也不该知道
// badges.Badge 的完整结构（它只需要 slug + name 渲染通知）。
type badgesEvaluatorAdapter struct {
	evaluator *badges.Evaluator
}

func (a badgesEvaluatorAdapter) EvaluateUser(ctx context.Context, userID int64) ([]badgesjobs.GrantedBadge, error) {
	if a.evaluator == nil {
		return nil, nil
	}
	granted, err := a.evaluator.EvaluateUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]badgesjobs.GrantedBadge, 0, len(granted))
	for _, badge := range granted {
		items = append(items, badgesjobs.GrantedBadge{Slug: badge.Slug, Name: badge.Name})
	}
	return items, nil
}

// badgeGrantedNotifier 把「本次新授予的徽章」写成站内/邮件通知。
//
// 与 reaction 的关键差异：这里**不做自授抑制**。徽章是公开荣誉，管理员给自己
// 授予徽章同样应该收到通知；reaction 的「自赞静默」是防互动噪音，不适用于此。
type badgeGrantedNotifier struct {
	outbox *notifications.Outbox
}

func (n badgeGrantedNotifier) NotifyBadgeGranted(ctx context.Context, userID int64, granted []badgesjobs.GrantedBadge) error {
	if n.outbox == nil || userID <= 0 {
		return nil
	}
	for _, badge := range granted {
		if err := n.outbox.NotifyBadgeGranted(ctx, notifications.BadgeGrantedEvent{
			RecipientUserID: userID,
			BadgeSlug:       badge.Slug,
			BadgeName:       badge.Name,
		}); err != nil {
			return err
		}
	}
	return nil
}

// registerBadgesEvaluateUserWorker 注册自动徽章评估 worker 及其通知出口。
//
// outbox 由调用方传入（与收藏提醒共用同一个），避免为徽章再造一套邮件装配。
func registerBadgesEvaluateUserWorker(
	registry *supportjobs.Registry,
	pool *pgxpool.Pool,
	outbox *notifications.Outbox,
	logger *slog.Logger,
) {
	store := badges.NewPostgresStore(pool)
	activity := badges.NewPostgresActivityStore(pool)
	evaluator := badges.NewEvaluator(store, activity)
	badgesjobs.RegisterEvaluateUser(registry, badgesEvaluatorAdapter{evaluator: evaluator}, badgeGrantedNotifier{outbox: outbox}, logger)
}

// deferredBackfillEnqueuer 让「回扫下一页」的入队入口先注册、后绑定 River client。
//
// 与收藏提醒的 deferredTxEnqueuer 同款：worker 注册发生在 River client 建好之前，
// 所以这里先持有一个空壳，client 建好后由 worker 装配处 Bind。**不要把 dispatcher
// 传进既有注册函数的签名**——那是别人的地盘，且延迟绑定限制在一个新类型里更便宜。
type deferredBackfillEnqueuer struct {
	dispatcher *supportjobs.Dispatcher
}

// Bind 在 River client 建好后注入 dispatcher。
func (e *deferredBackfillEnqueuer) Bind(dispatcher *supportjobs.Dispatcher) {
	e.dispatcher = dispatcher
}

// Enqueue 入队下一页；未 Bind 时返回错误（让当前页重试，而不是假装推进成功）。
func (e *deferredBackfillEnqueuer) Enqueue(ctx context.Context, args badgesjobs.BackfillRulesArgs) error {
	if e.dispatcher == nil {
		return fmt.Errorf("badge backfill enqueuer is not bound")
	}
	_, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions())
	return err
}

// badgesBackfillEnqueuer 把「需要异步评估的徽章」落成 job。
//
// 每个复杂规则徽章一个 job（游标从 0 开始），而不是一个 job 跑全部徽章：失败隔离在
// 单枚徽章上，运营也能按徽章看进度。
type badgesBackfillEnqueuer struct {
	enqueuer *deferredBackfillEnqueuer
	// runs 在入队**之前**建 running 行：job 还没被消费时状态就该存在，
	// 否则「卡在队列里」这种最常见的故障反而是唯一看不见的一种。
	runs badges.BackfillRunWriter
}

func newBadgesBackfillEnqueuer(enqueuer *deferredBackfillEnqueuer, runs badges.BackfillRunWriter) badgesBackfillEnqueuer {
	return badgesBackfillEnqueuer{enqueuer: enqueuer, runs: runs}
}

func (e badgesBackfillEnqueuer) EnqueueRuleBackfill(ctx context.Context, badgeIDs []int64) error {
	for _, badgeID := range badgeIDs {
		if badgeID <= 0 {
			continue
		}
		if e.runs != nil {
			if err := e.runs.StartBackfillRun(ctx, badgeID, badgesjobs.DefaultBackfillBatch); err != nil {
				return err
			}
		}
		args := badgesjobs.BackfillRulesArgs{BadgeID: badgeID, CursorUserID: 0, Batch: badgesjobs.DefaultBackfillBatch}
		if err := e.enqueuer.Enqueue(ctx, args); err != nil {
			// 入队失败：刚建那一行如果留 running 就永远停在「跑着」，
			// 所以当场落 failed，把原因写在 error 列里。
			if e.runs != nil {
				_ = e.runs.FailBackfillRun(ctx, badgeID, 0, "enqueue first backfill page: "+err.Error())
			}
			return err
		}
	}
	return nil
}

// badgesRuleBackfillRunner 执行一页回扫：按 id 游标取一批活跃用户，逐个跑评估。
//
// 只评估、不补授予之外的副作用：`EvaluateUser` 自己负责幂等（ON CONFLICT DO NOTHING）
// 与 top_badge 重算，这里不重复实现。
type badgesRuleBackfillRunner struct {
	store     *badges.PostgresStore
	evaluator *badges.Evaluator
	batch     int
}

func (r badgesRuleBackfillRunner) BackfillRuleBatch(ctx context.Context, badgeID int64, afterUserID int64, batch int) (int64, bool, error) {
	if r.store == nil || r.evaluator == nil {
		return afterUserID, true, nil
	}
	if batch <= 0 {
		batch = r.batch
	}
	userIDs, err := r.store.ListActiveUserIDsAfter(ctx, afterUserID, batch)
	if err != nil {
		return afterUserID, false, err
	}
	for _, userID := range userIDs {
		if _, err := r.evaluator.EvaluateUser(ctx, userID); err != nil {
			return userID, false, err
		}
	}
	if len(userIDs) < batch {
		return userIDs[len(userIDs)-1], true, nil
	}
	return userIDs[len(userIDs)-1], false, nil
}

// registerBadgesBackfillWorker 注册回扫 worker 并返回它的延迟绑定入口。
func registerBadgesBackfillWorker(
	registry *supportjobs.Registry,
	pool *pgxpool.Pool,
	enqueuer *deferredBackfillEnqueuer,
	logger *slog.Logger,
) {
	badgesjobs.RegisterBackfillRulesWorker(registry, newBadgesBackfillWorker(pool, enqueuer, logger))
}

// newBadgesBackfillWorker 组装回扫 worker（注册路径与装配守卫测试共用同一个工厂）。
//
// 为什么单独一个函数：`Runs` 为 nil 时 worker 会「退化但不停摆」——只写日志、不写
// 进度。那条退化路径本身是对的，但如果**装配**忘了接 runs store，回扫看起来在跑、
// 状态端点却永远空着，正是这一刀要消灭的假绿。守卫落在
// `badges_wiring_test.go`：断言这个工厂产物带非 nil 的 Runs / Runner。
func newBadgesBackfillWorker(pool *pgxpool.Pool, enqueuer badgesjobs.PageEnqueuer, logger *slog.Logger) *badgesjobs.BackfillRulesWorker {
	store := badges.NewPostgresStore(pool)
	evaluator := badges.NewEvaluator(store, badges.NewPostgresActivityStore(pool))
	return badgesjobs.NewBackfillRulesWorker(
		badgesRuleBackfillRunner{store: store, evaluator: evaluator},
		enqueuer,
		badges.NewPostgresBackfillRuns(pool),
		logger,
	)
}
