package profile

import (
	"context"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// BookmarkPage 是资料页收藏名单的一页。
//
// 形状刻意与 forum.BookmarkList 解耦成 Profile 自己的对外视图：Profile 只承诺
// 「资料页需要的字段」，Forum 侧改内部分页结构不该直接改变资料页契约。
type BookmarkPage struct {
	Items   []BookmarkItem `json:"items"`
	Total   int64          `json:"total"`
	HasMore bool           `json:"hasMore"`
}

// BookmarkItem 是名单里的一行：目标摘要 + 收藏元数据。
// 私密行只出现在行主本人的名单里；他人视角只含公开行。
type BookmarkItem struct {
	TargetType string    `json:"targetType"`
	TargetID   int64     `json:"targetId"`
	Visibility string    `json:"visibility"`
	CreatedAt  time.Time `json:"createdAt"`
	// Topic / Comment 至多一个非空；目标已删或不可见时两者皆空，
	// 调用方据 targetType/targetId 自行呈现（不虚构摘要）。
	Topic   *forum.TopicSummary       `json:"topic,omitempty"`
	Comment *forum.BookmarkCommentRef `json:"comment,omitempty"`
	// RemindAt / RemindedAt 只对行主本人可见（他人名单只含公开行）。
	RemindAt   *time.Time `json:"remindAt,omitempty"`
	RemindedAt *time.Time `json:"remindedAt,omitempty"`
}

// BookmarkView 是名单读取结果：Page 是响应体，OwnerView 只供 HTTP 层选择缓存策略。
// 它不是序列化类型——缓存头不该出现在响应体里。
type BookmarkView struct {
	Page      BookmarkPage
	OwnerView bool
}

// BookmarkLister 是 Profile 对收藏域的最小依赖（由 Forum 的 BookmarkService 实现）。
//
// 刻意只有一个方法：Profile 不写收藏、不判断可见性，也不持有收藏 SQL。
// viewerID 用于「本人看全部、他人只看公开」的口径；viewerID <= 0 表示匿名访客。
type BookmarkLister interface {
	ListForViewer(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkPage, error)
}
