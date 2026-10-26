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
	if trust := s.trustForActor(ctx, actor); trust.active {
		if trust.commentCooldown > 0 && (cooldown <= 0 || trust.commentCooldown > cooldown) {
			cooldown = trust.commentCooldown
		}
		if trust.dailyComment > 0 && (daily <= 0 || trust.dailyComment < daily) {
			daily = trust.dailyComment
		}
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

// trustLimits 是 forum 侧缓存的新人策略快照；由 SettingsResolver 扩展或 options 注入。
// 当前从 ForumSettings 之外的 resolver 可选接口读取，缺省不启用新人限制。
type trustLimits struct {
	active          bool
	topicCooldown   int
	commentCooldown int
	dailyTopic      int
	dailyComment    int
	forbidLinks     bool
	forbidAttach    bool
}

// TrustPolicyResolver 可选：由 options 适配器实现，向 forum 注入新人阶梯。
type TrustPolicyResolver interface {
	NewUserTrustDays(ctx context.Context) (int, error)
	NewUserTopicCooldownSeconds(ctx context.Context) (int, error)
	NewUserCommentCooldownSeconds(ctx context.Context) (int, error)
	NewUserDailyTopicLimit(ctx context.Context) (int, error)
	NewUserDailyCommentLimit(ctx context.Context) (int, error)
	NewUserForbidOutboundLinks(ctx context.Context) (bool, error)
}
