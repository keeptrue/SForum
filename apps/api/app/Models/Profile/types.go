package profile

import (
	"context"
	"errors"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

const (
	// 字段长度上限。避免过度校验，只做基本边界保护。
	maxBioLength       = 500
	maxSignatureLength = 200
	maxLocationLength  = 100
	maxWebsiteLength   = 200

	CodeProfileNotFound      = "profile.not_found"
	CodeProfileInvalid       = "profile.invalid"
	CodeAvatarUploadDisabled = "profile.avatar_upload_disabled"
	CodeNicknameDisabled     = "profile.nickname_disabled"
	CodeNicknameInvalid      = "profile.nickname_invalid"
	CodeNicknameCooldown     = "profile.nickname_cooldown"
)

// NicknameState / NicknameAvailability 复用 Identity 的对外视图，
// 避免两套同名字段各写一份、日后漂移。
type (
	NicknameState        = identity.NicknameState
	NicknameAvailability = identity.NicknameAvailability
)

var (
	ErrProfileNotFound      = errors.New("profile: not found")
	ErrProfileInvalid       = errors.New("profile: invalid profile input")
	ErrAvatarUploadDisabled = errors.New("profile: avatar upload disabled")
)

const (
	AvatarKindUploaded = "uploaded"
	AvatarKindInitials = "initials"
	AvatarKindGravatar = "gravatar"
	AvatarKindStatic   = "static"
)

// AvatarView 是前端和主题渲染头像所需的稳定视图。
type AvatarView = avatar.View

// Profile 是用户资料行（user_profiles）。
type Profile struct {
	UserID             int64      `json:"userId"`
	Bio                string     `json:"bio"`
	Signature          string     `json:"signature"`
	Location           string     `json:"location"`
	WebsiteURL         string     `json:"websiteUrl"`
	AvatarAttachmentID *int64     `json:"avatarAttachmentId,omitempty"`
	Avatar             AvatarView `json:"avatar"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// PublicProfile 是公开资料页聚合：用户摘要 + 资料 + 统计 + 近期公开主题。
type PublicProfile struct {
	UserID       int64                `json:"userId"`
	Username     string               `json:"username"`
	DisplayName  string               `json:"displayName"`
	Profile      Profile              `json:"profile"`
	TopicCount   int64                `json:"topicCount"`
	CommentCount int64                `json:"commentCount"`
	RecentTopics []forum.TopicSummary `json:"recentTopics"`
	Activities   []ProfileActivity    `json:"activities"`
	JoinedAt     time.Time            `json:"joinedAt"`
	// ExtensionTabs 来自 forum.profile.tabs 贡献（F4.3）；宿主渲染，无插件 HTML。
	ExtensionTabs []ProfileExtensionTab `json:"extensionTabs,omitempty"`
	// Nickname 只在自助端点（GET/PUT /profile）填充：公开资料页不需要暴露
	// 「还能不能改名」这类账号状态。
	Nickname *NicknameState `json:"nickname,omitempty"`
	// PreviousDisplayNames 仅在运营开启 profile.nickname.history_public 时出现，
	// 最多一条；它属于公开资料页，因此不会出现在自助响应里。
	PreviousDisplayNames []string `json:"previousDisplayNames,omitempty"`
}

// ProfileActivity 是公开个人主页的主题/回复时间线条目。
type ProfileActivity struct {
	Kind      string               `json:"kind"`
	Topic     ProfileActivityTopic `json:"topic"`
	CommentID *int64               `json:"commentId,omitempty"`
	// CommentPage 是该回复在主题 flat 视图分页下的页码（1-based）。
	// 仅 kind=comment 时有值；前端据此生成 /page/N#comment-{id} 深链，避免仅靠 hash 时 SSR 看不到 fragment。
	CommentPage *int      `json:"commentPage,omitempty"`
	Excerpt     string    `json:"excerpt"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ActivityKind 是活动分页 kind 查询参数。
const (
	ActivityKindTopic   = "topic"
	ActivityKindComment = "comment"
)

// ListActivitiesInput 是公开活动分页查询。
type ListActivitiesInput struct {
	Username string
	Kind     string
	Page     int
	PerPage  int
}

// ActivityPage 是公开活动分页响应。
type ActivityPage struct {
	Items   []ProfileActivity `json:"items"`
	Page    int               `json:"page"`
	PerPage int               `json:"perPage"`
	Total   int64             `json:"total"`
	HasMore bool              `json:"hasMore"`
	Kind    string            `json:"kind"`
}

// ProfileActivityTopic 是活动条目链接所需的最小公开主题信息。
type ProfileActivityTopic struct {
	ID             int64     `json:"id"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	CategorySlug   string    `json:"categorySlug"`
	CategoryName   string    `json:"categoryName"`
	CommentCount   int64     `json:"commentCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	LastActivityAt time.Time `json:"lastActivityAt"`
}

// ProfileCommentActivity 是 store 层回读的公开回复活动。
type ProfileCommentActivity struct {
	CommentID int64
	// CommentPage 是该评论在所属主题 flat 列表中的页码（与 ListComments/ResolveCommentPage 同源）。
	CommentPage int
	Topic       ProfileActivityTopic
	Excerpt     string
	CreatedAt   time.Time
}

// ProfileExtensionTab 是公开资料页扩展 tab/section 描述符。
type ProfileExtensionTab struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Order       int               `json:"order"`
	Label       map[string]string `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	// Kind: extensionRoute | hostLink
	Kind   string `json:"kind"`
	Method string `json:"method,omitempty"`
	// URL 对 extensionRoute 为 /extensions/{id}{path}；对 hostLink 为站内 href。
	URL string `json:"url"`
}

// ProfileTabProvider 解析 forum.profile.tabs。
type ProfileTabProvider interface {
	ProfileTabs(ctx context.Context) ([]ProfileExtensionTab, error)
}

// UpdateProfileInput 是当前用户更新自己资料的输入。所有指针字段为 nil 表示不改。
type UpdateProfileInput struct {
	Bio                *string
	Signature          *string
	Location           *string
	WebsiteURL         *string
	AvatarAttachmentID *int64
	// DisplayName 是昵称。它不落在 user_profiles，而由 Identity 昵称服务
	// 负责策略、冷却、历史与审计。
	DisplayName *string
}

// 公开用户检索的边界。查询词必须非空：空查询等于「列出全站用户」，
// 那是成员目录而不是检索，任何调用方都不该得到它。
const (
	UserSearchDefaultLimit  = 8
	UserSearchMaxLimit      = 20
	UserSearchMaxQueryRunes = 80
)

// SearchUsersInput 是公开用户检索输入。
type SearchUsersInput struct {
	Query string
	Limit int
}

// UserSearchHit 是一条公开可达的用户结果。
//
// 它刻意只带 id / username / displayName：邮箱、状态、角色、封禁历史都不参与
// 检索也不出现在结果里，因此这条读取路径在类型层面就无法泄露非公开字段。
type UserSearchHit struct {
	UserID      int64  `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

// UserSearchResult 是公开用户检索结果。
type UserSearchResult struct {
	Items []UserSearchHit `json:"items"`
}

// UserProfileSummary 是 store 层返回的用户基本信息（来自 users 表）。
type UserProfileSummary struct {
	UserID      int64
	Username    string
	DisplayName string
	Email       string
	JoinedAt    time.Time
}

// ProfileStats 是用户的公开统计。
type ProfileStats struct {
	TopicCount   int64
	CommentCount int64
}

type AvatarAttachment = avatar.Attachment

// AvatarUser 是头像视图构建所需的最小用户信息。
type AvatarUser = avatar.User

// AvatarSource 是调用方已解析好的头像来源，避免列表页逐项回查 profile。
type AvatarSource = avatar.Source
