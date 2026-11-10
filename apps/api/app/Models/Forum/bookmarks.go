package forum

import (
	"context"
	"errors"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// NameFeatureBookmarks 是本域的产品开关名；定义在 Options，这里只做域内别名。
const NameFeatureBookmarks = options.NameFeatureBookmarks

const (
	BookmarkTargetTopic   = ReactionTargetTopic
	BookmarkTargetComment = ReactionTargetComment

	// BookmarkVisibilityPrivate / BookmarkVisibilityPublic 是 v1 的两档可见性。
	// 收藏默认私密：公开是显式选择，不是默认副作用。
	BookmarkVisibilityPrivate = "private"
	BookmarkVisibilityPublic  = "public"

	// CodeBookmarksDisabled 是运营者关闭 features.bookmarks 时的具名原因。
	CodeBookmarksDisabled = "forum.bookmarks_disabled"

	// CodeBookmarkReminderConflict 是「同时要求清除与设置提醒」的具名原因。
	CodeBookmarkReminderConflict = "forum.bookmark_reminder_conflict"
	// CodeBookmarkReminderInPast 是提醒时间早于当前时刻（超出时钟偏差）的具名原因。
	CodeBookmarkReminderInPast = "forum.bookmark_reminder_in_past"
	// CodeBookmarkReminderTooFar 是提醒时间超出允许前瞻窗口的具名原因。
	CodeBookmarkReminderTooFar = "forum.bookmark_reminder_too_far"
	// CodeBookmarkReminderLimit 是未到期提醒数量超限的具名原因。
	CodeBookmarkReminderLimit = "forum.bookmark_reminder_limit"

	// BookmarkReminderLimit 是单用户未到期提醒上限。
	//
	// 计数与写入在同一事务里，但**不做串行化**：两个并发请求可以同时看到
	// limit-1 并各自插入一条，最终略超上限。这是刻意的取舍——为提醒设一个硬
	// 上界不值得引入 per-user 咨询锁（会让高频收藏路径互相等待），而且提醒 job
	// 是批量扫描，多几条只是下一轮多处理几条。
	BookmarkReminderLimit = 200
	// BookmarkReminderClockSkew 允许的时钟偏差：略早于 now 的时间仍被接受。
	BookmarkReminderClockSkew = 60 * time.Second
	// BookmarkReminderMaxAheadDays 是提醒时间的前瞻窗口上限（2 年）。
	BookmarkReminderMaxAheadDays = 730

	// CodeBookmarkTargetInvalid 是「目标类型非法/目标无法解析」的具名原因。
	// 与 reaction 同姿态：收藏目标是多态的，借用主题的 not-found 会在评论目标上
	// 给出错误语义，且不能成为存在性预言机。
	CodeBookmarkTargetInvalid = "forum.bookmark_target_invalid"

	// BookmarkListDefaultLimit / BookmarkListMaxLimit / BookmarkListMaxOffset
	// 约束个人收藏名单（GET /profile/bookmarks 与公开名单）。
	BookmarkListDefaultLimit = 20
	BookmarkListMaxLimit     = 50
	BookmarkListMaxOffset    = 1000
)

var (
	// ErrBookmarksDisabled 表示站点产品开关关闭；HTTP 层映射为 403。
	ErrBookmarksDisabled = errors.New("forum: bookmarks are disabled")
	// ErrInvalidBookmarkTarget 表示目标类型非法或目标不存在/不可见。
	ErrInvalidBookmarkTarget = errors.New("forum: invalid bookmark target")
	// ErrBookmarkReminderConflict 表示同一次请求既要清除又要设置提醒。
	ErrBookmarkReminderConflict = errors.New("forum: bookmark reminder clear and set conflict")
	// ErrBookmarkReminderInPast 表示提醒时间已经过去（超出时钟偏差）。
	ErrBookmarkReminderInPast = errors.New("forum: bookmark reminder is in the past")
	// ErrBookmarkReminderTooFar 表示提醒时间超出前瞻窗口。
	ErrBookmarkReminderTooFar = errors.New("forum: bookmark reminder is too far in the future")
	// ErrBookmarkReminderLimit 表示未到期提醒数量已达上限。
	ErrBookmarkReminderLimit = errors.New("forum: too many pending bookmark reminders")
)

// BookmarkState 是一次收藏切换后的权威状态。字段与 OpenAPI BookmarkState 同名。
type BookmarkState struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Visibility string `json:"visibility"`
	Bookmarked bool   `json:"bookmarked"`
	// PublicBookmarkCount 是目标的公开收藏计数（只统计 visibility='public' 行）。
	PublicBookmarkCount int64      `json:"publicBookmarkCount"`
	RemindAt            *time.Time `json:"remindAt,omitempty"`
	// RemindedAt 是提醒已发出的时间；与 RemindAt 一起区分「将提醒 / 已提醒」。
	RemindedAt *time.Time `json:"remindedAt,omitempty"`
}

// BookmarkEntry 是收藏名单里的一行：目标摘要 + 收藏元数据。
// 私密行只出现在行主本人的名单里；他人名单与稳定视图只含公开行。
type BookmarkEntry struct {
	TargetType string              `json:"targetType"`
	TargetID   int64               `json:"targetId"`
	Visibility string              `json:"visibility"`
	CreatedAt  time.Time           `json:"createdAt"`
	Topic      *TopicSummary       `json:"topic,omitempty"`
	Comment    *BookmarkCommentRef `json:"comment,omitempty"`
	// RemindAt / RemindedAt 只出现在行主本人的名单里：他人名单只含公开行，
	// 而提醒是行主私有计划数据（稳定视图同样不输出这两列）。
	RemindAt   *time.Time `json:"remindAt,omitempty"`
	RemindedAt *time.Time `json:"remindedAt,omitempty"`
}

// BookmarkCommentRef 是评论目标的公开摘要投影。
//
// 刻意不复用 CommentSummary：后者是内部扫描结构（无 JSON 标签，字段名会变成
// PascalCase，且 path_key 这类内部定位数据不该出现在名单里）。
type BookmarkCommentRef struct {
	ID              int64     `json:"id"`
	TopicID         int64     `json:"topicId"`
	AuthorUserID    int64     `json:"authorUserId"`
	Depth           int       `json:"depth"`
	Status          string    `json:"status"`
	CurrentRevision int64     `json:"currentRevision"`
	CreatedAt       time.Time `json:"createdAt"`
}

// BookmarkList 是收藏名单的有界分页结果。
type BookmarkList struct {
	Items   []BookmarkEntry `json:"items"`
	Total   int64           `json:"total"`
	HasMore bool            `json:"hasMore"`
}

// BookmarkReader 为公开读模型补上「当前访客是否已收藏」。
//
// 与 ReactionReader 同一契约：实现必须在缓存之后调用（bookmarkedByMe 一旦写进
// 缓存就会在访客之间串味），装饰失败必须 fail-open。
type BookmarkReader interface {
	DecorateTopic(ctx context.Context, viewer identity.Actor, topic *TopicDetail) error
	DecorateComments(ctx context.Context, viewer identity.Actor, comments []Comment) error
}

// BookmarkLister 是 Profile 域对收藏名单的最小依赖。
//
// 刻意只暴露名单一个能力：Forum 不暴露写路径给 Profile，SQL 也留在 Forum 的
// store 里（Profile store 不写收藏查询）。viewerID 用于「本人看全部、他人只看
// 公开」的口径；viewerID <= 0 表示匿名访客。
type BookmarkLister interface {
	ListUserBookmarks(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkList, error)
}
