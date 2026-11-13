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
		items = append(items, badgesjobs.GrantedBadge{ID: badge.ID, Slug: badge.Slug, Name: badge.Name})
	}
	return items, nil
}

// badgeGrantedNotifier 是「徽章授予 → 站内/邮件通知」的唯一实现。
//
// 与 reaction 的关键差异：这里**不做自授抑制**。徽章是公开荣誉，管理员给自己
// 授予徽章同样应该收到通知；reaction 的「自赞静默」是防互动噪音，不适用于此。
//
// 四条授予路径（自动评估 job / 手动授予 / 批量授予 / 重算）都要发通知，但它们的
// 端口类型不同：job 侧用 `badgesjobs.GrantedBadge`，领域侧用 `badges.GrantedBadge`。
// 同一种方法名不能在同一类型上写两份签名，所以下面用两个薄适配器包住同一个实现——
// **写通知的逻辑只有这里一份**，别在适配器里复制循环。
type badgeGrantedNotifier struct {
	outbox *notifications.Outbox
}

func (n badgeGrantedNotifier) notify(ctx context.Context, userID int64, granted []badges.GrantedBadge) error {
	if n.outbox == nil || userID <= 0 {
		return nil
	}
	for _, badge := range granted {
		if err := n.outbox.NotifyBadgeGranted(ctx, notifications.BadgeGrantedEvent{
			RecipientUserID: userID,
			BadgeID:         badge.ID,
			BadgeSlug:       badge.Slug,
			BadgeName:       badge.Name,
		}); err != nil {
			return err
		}
	}
	return nil
}

// badgesJobGrantedNotifier 实现 `badgesjobs.GrantedNotifier`（自动评估 job）。
type badgesJobGrantedNotifier struct{ badgeGrantedNotifier }

func (n badgesJobGrantedNotifier) NotifyBadgeGranted(ctx context.Context, userID int64, granted []badgesjobs.GrantedBadge) error {
	items := make([]badges.GrantedBadge, 0, len(granted))
	for _, badge := range granted {
		items = append(items, badges.GrantedBadge{ID: badge.ID, Slug: badge.Slug, Name: badge.Name})
	}
	return n.notify(ctx, userID, items)
}

// badgesDomainGrantedNotifier 实现 `badges.GrantNotifier`（手动/批量/重算）。
type badgesDomainGrantedNotifier struct{ badgeGrantedNotifier }

func (n badgesDomainGrantedNotifier) NotifyBadgeGranted(ctx context.Context, userID int64, granted []badges.GrantedBadge) error {
	return n.notify(ctx, userID, granted)
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
	notifier := badgesJobGrantedNotifier{badgeGrantedNotifier{outbox: outbox}}
	badgesjobs.RegisterEvaluateUser(registry, badgesEvaluatorAdapter{evaluator: evaluator}, notifier, logger)
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

// badgeUserLister / badgeUserEvaluator 是回扫 runner 需要的最小能力面。
//
// 用接口而不是直接吃 `*badges.PostgresStore` / `*badges.Evaluator`：回扫的
// 「授予之后发不发通知」只能用替身证明（真跑一遍要造全站用户与规则），而
// 装配仍传具体类型，不存在第二套实现。
type badgeUserLister interface {
	ListActiveUserIDsAfter(ctx context.Context, afterUserID int64, limit int) ([]int64, error)
}

type badgeUserEvaluator interface {
	EvaluateUser(ctx context.Context, userID int64) ([]badges.Badge, error)
}

// badgesRuleBackfillRunner 执行一页回扫：按 id 游标取一批活跃用户，逐个跑评估。
//
// 只评估、不补授予之外的副作用：`EvaluateUser` 自己负责幂等（ON CONFLICT DO NOTHING）
// 与 top_badge 重算，这里不重复实现。
//
// **授予之后必须发通知**：回扫是自动授予的第五条路径（另外四条是评估 job、手动
// 授予、批量授予、全量重算）。早期这里把 `EvaluateUser` 的返回值直接丢掉，症状是
// 「运营改严规则 → 回扫补出几百枚徽章 → 收件箱一片安静」，而运营侧只看到回执。
type badgesRuleBackfillRunner struct {
	users     badgeUserLister
	evaluator badgeUserEvaluator
	notifier  badges.GrantNotifier
	logger    *slog.Logger
	batch     int
}

func (r badgesRuleBackfillRunner) BackfillRuleBatch(ctx context.Context, badgeID int64, afterUserID int64, batch int) (int64, bool, error) {
	if r.users == nil || r.evaluator == nil {
		return afterUserID, true, nil
	}
	if batch <= 0 {
		batch = r.batch
	}
	userIDs, err := r.users.ListActiveUserIDsAfter(ctx, afterUserID, batch)
	if err != nil {
		return afterUserID, false, err
	}
	// 空页 = 游标已经到头（或站上还没有活跃用户）。必须在这里收口：
	// 下面两条返回都要取 `userIDs[len-1]`，空切片会 index -1 panic，
	// 而 job 重试会再次命中同一页 → 无限崩溃循环，回扫永远跑不完。
	// 游标原样返回，让状态行停在「确实处理到的最后一个用户」。
	if len(userIDs) == 0 {
		return afterUserID, true, nil
	}
	for _, userID := range userIDs {
		granted, err := r.evaluator.EvaluateUser(ctx, userID)
		if err != nil {
			return userID, false, err
		}
		r.notifyGranted(ctx, userID, granted)
	}
	if len(userIDs) < batch {
		return userIDs[len(userIDs)-1], true, nil
	}
	return userIDs[len(userIDs)-1], false, nil
}

// notifyGranted 把本页某个用户新得的徽章投影成通知，失败只 warn。
//
// 为什么吞错而不上抛：授予已经落库（每条 grant 都是自动提交的独立语句），把通知
// 失败上抛会让 river 重跑整页，而重跑时这些授予已存在、`EvaluateUser` 返回空，
// 通知**永远不会再发**——错被「重试」洗掉，用户永远收不到。与 2026-11-13 决策 D2
// 同一姿态：授予成功、通知尽力而为，失败留一条 warn 给运维。
func (r badgesRuleBackfillRunner) notifyGranted(ctx context.Context, userID int64, granted []badges.Badge) {
	if r.notifier == nil || len(granted) == 0 {
		return
	}
	items := make([]badges.GrantedBadge, 0, len(granted))
	for _, badge := range granted {
		items = append(items, badges.GrantedBadge{ID: badge.ID, Slug: badge.Slug, Name: badge.Name})
	}
	if err := r.notifier.NotifyBadgeGranted(ctx, userID, items); err != nil && r.logger != nil {
		r.logger.Warn("notify badge granted from rule backfill failed",
			"user_id", userID, "badges", len(items), "error", err)
	}
}

// registerBadgesBackfillWorker 注册回扫 worker 并返回它的延迟绑定入口。
func registerBadgesBackfillWorker(
	registry *supportjobs.Registry,
	pool *pgxpool.Pool,
	outbox *notifications.Outbox,
	enqueuer *deferredBackfillEnqueuer,
	logger *slog.Logger,
) {
	badgesjobs.RegisterBackfillRulesWorker(registry, newBadgesBackfillWorker(pool, outbox, enqueuer, logger))
}

// newBadgesBackfillWorker 组装回扫 worker（注册路径与装配守卫测试共用同一个工厂）。
//
// 为什么单独一个函数：`Runs` 为 nil 时 worker 会「退化但不停摆」——只写日志、不写
// 进度。那条退化路径本身是对的，但如果**装配**忘了接 runs store，回扫看起来在跑、
// 状态端点却永远空着，正是这一刀要消灭的假绿。守卫落在
// `badges_wiring_test.go`：断言这个工厂产物带非 nil 的 Runs / Runner / 通知出口。
func newBadgesBackfillWorker(pool *pgxpool.Pool, outbox *notifications.Outbox, enqueuer badgesjobs.PageEnqueuer, logger *slog.Logger) *badgesjobs.BackfillRulesWorker {
	store := badges.NewPostgresStore(pool)
	evaluator := badges.NewEvaluator(store, badges.NewPostgresActivityStore(pool))
	return badgesjobs.NewBackfillRulesWorker(
		badgesRuleBackfillRunner{
			users:     store,
			evaluator: evaluator,
			notifier:  badgesDomainGrantedNotifier{badgeGrantedNotifier{outbox: outbox}},
			logger:    logger,
		},
		enqueuer,
		badges.NewPostgresBackfillRuns(pool),
		logger,
	)
}
