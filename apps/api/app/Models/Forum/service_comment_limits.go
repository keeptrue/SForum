package forum

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 评论创建的发言节流：冷却与日限额。它被拆到独立文件，是因为 service_ops.go 已经
// 到达架构门禁的行数上限；这里也是唯一需要区分人类与机器人发言策略的地方。
func (s *Service) enforceCommentCreateLimitsForActor(ctx context.Context, actor identity.Actor, settings ForumSettings) error {
	// 机器人不受人类发言节流约束。它们由系统驱动，且已经在上游闸门（站点速率、
	// 每用户配额、月度预算）被限过一次；在这里再套一层冷却只会让回复静默丢失，
	// 而用户看到的现象是「AI 不回我」，完全看不出是限流。
	if actor.Kind.IsBot() {
		return nil
	}
	now := time.Now().UTC()
	cooldown := settings.CommentCooldownSeconds
	daily := settings.DailyCommentLimit
	if trust := limitsForActor(ctx, s.trust, actor); trust.active {
		cooldown = resolveLimit(cooldown, trust.commentCooldown, trust.overrideCommentCooldown)
		daily = resolveLimit(daily, trust.dailyComment, trust.overrideDailyComment)
	}
	if cooldown > 0 {
		lastAt, ok, err := s.store.LatestAuthorCommentCreatedAt(ctx, actor.ID)
		if err != nil {
			return err
		}
		if !cooldownElapsed(lastAt, ok, cooldown, now) {
			return newCooldownError(ErrCommentCooldown, lastAt, cooldown)
		}
	}
	if daily > 0 {
		count, err := s.store.CountAuthorCommentsSince(ctx, actor.ID, dayStartUTC(now))
		if err != nil {
			return err
		}
		if count >= int64(daily) {
			return ErrDailyCommentLimit
		}
	}
	return nil
}

// trustLimits 是信任阶梯在 forum 侧的快照。
//
// 两种来源共用这个形状（D2：不许并存两套动态阶梯）：
//   - TL0 / 未解析（开关关、读失败）走「新人阶梯」：tighten* 语义是**更严者生效**，
//     即只能比站点默认更紧，永远不能放宽；
//   - TL1+ 走「分档限额」：override* 是**显式覆盖**（nil = 继承站点默认），
//     0 有语义（免冷却），所以必须与 tighten 分开表达。
type trustLimits struct {
	active          bool
	topicCooldown   int
	commentCooldown int
	dailyTopic      int
	dailyComment    int
	forbidLinks     bool
	// forbidAttach 保留字段：options 里有 trust.new_user.forbid_attachments，
	// 但全仓没有任何执行点（D20 登记为 residual，本刀不接线）。
	forbidAttach bool

	overrideTopicCooldown   *int
	overrideCommentCooldown *int
	overrideDailyTopic      *int
	overrideDailyComment    *int
}

// TrustLevelLimits 是一档等级的限额覆盖；nil 字段表示继承站点默认。
type TrustLevelLimits struct {
	TopicCooldownSeconds   *int
	CommentCooldownSeconds *int
	DailyTopicLimit        *int
	DailyCommentLimit      *int
}

// TrustPolicyResolver 可选：由 options 适配器实现，向 forum 注入新人阶梯。
type TrustPolicyResolver interface {
	NewUserTrustDays(ctx context.Context) (int, error)
	NewUserTopicCooldownSeconds(ctx context.Context) (int, error)
	NewUserCommentCooldownSeconds(ctx context.Context) (int, error)
	NewUserDailyTopicLimit(ctx context.Context) (int, error)
	NewUserDailyCommentLimit(ctx context.Context) (int, error)
	NewUserForbidOutboundLinks(ctx context.Context) (bool, error)
	// LevelLimits 返回某档等级的限额覆盖（trust.levels 里的 levels[].limits）。
	// 未配置/产品面关闭时返回零值（全部继承站点默认）。
	LevelLimits(ctx context.Context, level int) (TrustLevelLimits, error)
}
