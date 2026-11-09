package forum

import "context"

// ApplyReactionRecord 是一次点赞/取消的完整输入。
//
// RecipientUserID 由服务层在可见性解析时一并取得（主题作者或评论作者），
// 0 或等于 ActorUserID 时不写通知；通知与计数必须在同一事务里落库。
type ApplyReactionRecord struct {
	ActorUserID     int64
	TargetType      string
	TargetID        int64
	TopicID         int64
	RecipientUserID int64
	Reaction        string
	Add             bool
}

// ReactionStore 是点赞域的窄接口。
//
// 刻意不并入 Store：Store 有四个测试 fake 实现，为一个新能力改它们的签名只会
// 制造噪音。CachedStore 用类型断言转发（与 PublicShortcodeSourceStore 同姿态）。
type ReactionStore interface {
	// ApplyReaction 在一个事务内完成：写/删反应行 → 维护计数列与 hot_score →
	// 写通知投影。已存在/已删除时不重复计数，直接返回当前状态。
	ApplyReaction(ctx context.Context, input ApplyReactionRecord) (ReactionState, error)
	// LikedTargetIDs 批量回答「这一页里我赞过哪些」，避免 N+1。
	LikedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error)
	// ListReactionUsers 返回最近点赞者（keyset 语义的 OFFSET 版），有界。
	ListReactionUsers(ctx context.Context, targetType string, targetID int64, limit, offset int) (ReactionRoster, error)
}

// ReactionPreviewStore 是公开读预览的窄接口：一次批量查询返回每个目标的
// 头像组预览（最多 limit 个，自己优先、其余最早）。与 ReactionStore 同姿态，
// 刻意不并入它，也不并入 Store。
type ReactionPreviewStore interface {
	// ReactionPreviews 按 targetIDs 批量返回点赞者预览；无点赞的目标不出现在 map 里。
	ReactionPreviews(ctx context.Context, targetType string, targetIDs []int64, viewerUserID int64, limit int) (map[int64][]UserSummary, error)
}
