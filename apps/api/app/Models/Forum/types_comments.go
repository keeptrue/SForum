package forum

import (
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type CommentListInput struct {
	TopicID int64
	View    string
	Page    int
	PerPage int
	// After 非空时 flat 走 created_at + id keyset（时间流续页），忽略 Page（cursor 优先）。
	After string
	// TreeDescendantsPerRoot view=tree 时每个根下最多拉取的子孙数；0 时 store 用推荐默认 50。
	TreeDescendantsPerRoot int
	// Viewer 可选：用于 softDeleteVisibility 判定是否展示软删墓碑。
	// 匿名时为零值 Actor，仅能看到 active。
	Viewer identity.Actor
	// IncludeDeleted / DeletedAuthorUserID 由 Service 根据 viewer 与策略设置，调用方不得自行信任。
	IncludeDeleted      bool
	DeletedAuthorUserID int64
}

type CommentReplyListInput struct {
	CommentID           int64
	IncludeDeleted      bool
	DeletedAuthorUserID int64
}

type CommentList struct {
	Items []Comment `json:"items"`
	// Total：flat 为主题评论总数（公开路径优先 topics.comment_count）；tree 为根评论数。
	// 深翻优先 HasMore / NextCursor。
	Total   int64  `json:"total"`
	Page    int    `json:"page"`
	PerPage int    `json:"perPage"`
	View    string `json:"view"`
	HasMore bool   `json:"hasMore"`
	// NextCursor flat keyset 续页；tree 可空（用 page）。
	NextCursor string `json:"nextCursor,omitempty"`
	// ExtensionActions 列表级评论行扩展动作（E2.2）；不复制到每条 Comment。
	ExtensionActions []CommentExtensionAction `json:"extensionActions,omitempty"`
	// ProtectedContent is request-only HTTP cache-policy metadata.
	ProtectedContent bool `json:"-"`
}

type Comment struct {
	ID           int64        `json:"id"`
	TopicID      int64        `json:"topicId"`
	AuthorUserID int64        `json:"authorUserId"`
	Author       *UserSummary `json:"author,omitempty"`
	// TopBadge 是评论作者的最高级徽章（与 Author.TopBadge 同值）。
	TopBadge      *BadgeRef             `json:"topBadge,omitempty"`
	ParentID      *int64                `json:"parentId,omitempty"`
	RootCommentID int64                 `json:"rootCommentId"`
	PathKey       string                `json:"pathKey"`
	Depth         int                   `json:"depth"`
	ReplyCount    int64                 `json:"replyCount"`
	Status        string                `json:"status"`
	Content       PublicRenderedContent `json:"content"`
	ReplyTo       *ReplyReference       `json:"replyTo,omitempty"`
	Children      []Comment             `json:"children,omitempty"`
	// HasMoreChildren tree 视图下子孙被 treeDescendantsPerRoot 截断时为 true；更多走 ListCommentReplies。
	HasMoreChildren bool      `json:"hasMoreChildren,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	// CurrentRevision 是内容乐观并发令牌。混合迁移期旧内容以 >=1 的有效值回读，绝不暴露 0。
	CurrentRevision int64 `json:"currentRevision"`
	// LikeCount 是反规范化的点赞数；LikedByMe 只在有登录访客时按请求填充。
	LikeCount int64 `json:"likeCount"`
	LikedByMe bool  `json:"likedByMe,omitempty"`
	// LikedBy 是点赞者头像组预览：最多 3 个，自己优先、其余最早；只在读路径
	// 装饰（CachedStore 之后），永不进缓存或持久层。
	LikedBy []UserSummary `json:"likedBy,omitempty"`
	// BookmarkedByMe 只在有登录访客时按请求填充，永不进入缓存或持久层。
	BookmarkedByMe bool `json:"bookmarkedByMe,omitempty"`
	// Edited 评论是否曾被编辑（showCommentEditMark 开启时填充）。
	Edited bool `json:"edited,omitempty"`
	// EditedAt 是当前已接受修订的提交时间；仅在编辑标记设置开启时对外保留。
	EditedAt *time.Time `json:"editedAt,omitempty"`
	// ContentEdited 由存储层根据 post_revisions 得出，不直接暴露。
	ContentEdited bool `json:"-"`
	// UpdateApplied 仅供写路径抑制 no-op 的事件、缓存和索引副作用，不进入 API。
	UpdateApplied       bool     `json:"-"`
	UpdateChangedFields []string `json:"-"`
}

type ReplyReference struct {
	ID      int64        `json:"id"`
	Author  *UserSummary `json:"author,omitempty"`
	Excerpt string       `json:"excerpt"`
	Depth   int          `json:"depth"`
}

type CommentSummary struct {
	ID              int64
	TopicID         int64
	AuthorUserID    int64
	ParentID        *int64
	RootCommentID   int64
	PathKey         string
	Depth           int
	Status          string
	CreatedAt       time.Time
	CurrentRevision int64
}
