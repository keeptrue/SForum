package bootstrap

import (
	"context"
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
