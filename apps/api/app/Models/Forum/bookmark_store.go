package forum

import (
	"context"
	"time"
)

// BookmarkApplyInput 是一次收藏写入的完整输入。
//
// RecipientUserID 复用 reaction 的语义：服务层在可见性解析时一并取得（主题作者
// 或评论作者）；收藏不写作者通知，保留字段是给提醒面（remind）留的口径，
// v1 恒为 0 且 store 不消费它。
type BookmarkApplyInput struct {
	ActorUserID int64
	TargetType  string
	TargetID    int64
	TopicID     int64
	// Visibility 是本次切换的目标可见性；空串表示维持原值（仅补行）。
	Visibility string
	// RemindAt 可选：设定/更新收藏提醒时间；nil 表示不变更。
	// 只有 Add=true 的调用允许携带；取消收藏时被忽略。
	// 设置新提醒会同时清掉 reminded_at（新提醒尚未发出）。
	RemindAt *time.Time
	// ClearReminder 为 true 时清空 remind_at 与 reminded_at。
	// 与 RemindAt 互斥（service 层已拒绝同时出现的请求）。
	ClearReminder bool
	Add           bool
}

// BookmarkStore 是收藏域的窄接口。
//
// 与 ReactionStore 同姿态：刻意不并入 Store（Store 有四个测试 fake 实现，为
// 一个新能力改它们的签名只会制造噪音）。CachedStore 用类型断言转发。
type BookmarkStore interface {
	// ApplyBookmark 在一个事务内完成：写/删收藏行 → 维护 public_bookmark_count →
	// 可见性切换的计数增减与行更新同事务。幂等：已存在的同可见性行不重复计数。
	ApplyBookmark(ctx context.Context, input BookmarkApplyInput) (BookmarkState, error)
	// BookmarkedTargetIDs 批量回答「这一页里我收藏过哪些」，避免 N+1。
	BookmarkedTargetIDs(ctx context.Context, viewerUserID int64, targetType string, targetIDs []int64) (map[int64]struct{}, error)
	// ListUserBookmarks 返回行主的收藏名单；includePrivate=false 时只出公开行
	// （他人视角），行主本人传 true。SQL 归 Forum store 所有。
	ListUserBookmarks(ctx context.Context, ownerUserID int64, includePrivate bool, limit, offset int) (BookmarkList, error)
}
