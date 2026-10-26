package forum

import (
	"context"
	"errors"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

const (
	SourceFormatMarkdown = "markdown"
	SourceFormatHTML     = "html"
	// SourceFormatEditorDocument 走 Host EditorDocument Accept 管线（native Tiptap JSON）。
	SourceFormatEditorDocument = "editor-document"

	EditorTypeMarkdown = "markdown"
	EditorTypeTiptap   = "tiptap"

	// v2 启用 goldmark GFM 扩展（表格/删除线/自动链接/任务列表）并放开对应 sanitizer 规则。
	// 存量帖子保留 v1 HTML，下次编辑时自然升级到 v2（不做批量重渲染）。
	RenderVersion = "goldmark-bluemonday-v2"
	// RenderVersionEditorDocument 标记正文经 sforum.editor-document@1 管线验收。
	RenderVersionEditorDocument = "sforum.editor-document@1"

	TopicStatusActive   = "active"
	TopicStatusLocked   = "locked"
	TopicStatusHidden   = "hidden"
	TopicStatusDeleted  = "deleted"
	TopicStatusPending  = "pending"
	TopicStatusRejected = "rejected"

	CommentStatusActive   = "active"
	CommentStatusHidden   = "hidden"
	CommentStatusDeleted  = "deleted"
	CommentStatusPending  = "pending"
	CommentStatusRejected = "rejected"

	TagStatusActive   = "active"
	TagStatusPending  = "pending"
	TagStatusDisabled = "disabled"

	TagCreationModeControlled = "controlled"
	TagCreationModeReview     = "review"
	TagCreationModeOpen       = "open"

	// 主题生命周期动作。hide/restore 归版主（topic.delete_any），
	// lock/unlock 归 topic.lock，pin/unpin 归 topic.pin。
	TopicActionHide    = "hide"
	TopicActionRestore = "restore"
	TopicActionLock    = "lock"
	TopicActionUnlock  = "unlock"
	TopicActionPin     = "pin"
	TopicActionUnpin   = "unpin"

	CodeInvalidContent                = "forum.content_invalid"
	CodeInvalidTopic                  = "forum.topic_invalid"
	CodeTopicNotFound                 = "forum.topic_not_found"
	CodeCommentNotFound               = "forum.comment_not_found"
	CodeTopicClosed                   = "forum.topic_closed"
	CodeInvalidTag                    = "forum.tag_invalid"
	CodeTagNotFound                   = "forum.tag_not_found"
	CodeInvalidSettings               = "forum.settings_invalid"
	CodeInvalidAction                 = "forum.topic_action_invalid"
	CodeUseSearch                     = "forum.use_search_endpoint"
	CodeRevisionNotFound              = "forum.revision_not_found"
	CodeRevisionRedacted              = "forum.revision_redacted"
	CodeRevisionConflict              = "forum.revision_conflict"
	CodeRevisionReasonRequired        = "forum.revision_reason_required"
	CodeRevisionNotRestorable         = "forum.revision_not_restorable"
	CodeRevisionAttachmentUnavailable = "forum.revision_attachment_unavailable"
	CodeRevisionCategoryUnavailable   = "forum.revision_category_unavailable"
	CodeRevisionTagUnavailable        = "forum.revision_tag_unavailable"
	CodeRevisionRedactionForbidden    = "forum.revision_redaction_forbidden"
	// 非法/过期/与 sort 不匹配的 keyset 游标。
	CodeInvalidCursor         = "forum.cursor_invalid"
	CodeReindexRunning        = "forum.reindex_running"    // 已有重建在进行
	CodeReindexNoRun          = "forum.reindex_no_run"     // 尚无重建记录
	CodeSearchUnavailable     = "forum.search_unavailable" // 搜索服务不可用
	CodeTitleTooShort         = "forum.title_too_short"
	CodeTitleTooLong          = "forum.title_too_long"
	CodeContentTooShort       = "forum.content_too_short"
	CodeContentTooLong        = "forum.content_too_long"
	CodeCommentTooShort       = "forum.comment_too_short"
	CodeCommentTooLong        = "forum.comment_too_long"
	CodeCommentNestingDeep    = "forum.comment_nesting_too_deep"
	CodeEditWindowExpired     = "forum.edit_window_expired"
	CodeTopicCooldown         = "forum.topic_cooldown"
	CodeOutboundLinkForbidden = "forum.outbound_link_forbidden"
	CodeMentionsLimit         = "forum.mentions_limit"
	CodeCommentCooldown       = "forum.comment_cooldown"
	CodeDailyTopicLimit       = "forum.daily_topic_limit"
	CodeDailyCommentLimit     = "forum.daily_comment_limit"
	CodeTagMinRequired        = "forum.tag_min_required"
	// 游客阅读策略为 login_required 时，未登录访问公开阅读接口。
	CodeGuestLoginRequired = "forum.guest_login_required"
	// 重复标题策略 block。
	CodeDuplicateTitle = "forum.duplicate_title"
)

var (
	ErrInvalidContent        = errors.New("forum: invalid content")
	ErrInvalidTopic          = errors.New("forum: invalid topic")
	ErrTopicNotFound         = errors.New("forum: topic not found")
	ErrCommentNotFound       = errors.New("forum: comment not found")
	ErrTopicClosed           = errors.New("forum: topic is closed")
	ErrInvalidTag            = errors.New("forum: invalid tag")
	ErrTagNotFound           = errors.New("forum: tag not found")
	ErrInvalidSettings       = errors.New("forum: invalid settings")
	ErrInvalidAction         = errors.New("forum: invalid topic action")
	ErrTitleTooShort         = errors.New("forum: title too short")
	ErrTitleTooLong          = errors.New("forum: title too long")
	ErrContentTooShort       = errors.New("forum: content too short")
	ErrContentTooLong        = errors.New("forum: content too long")
	ErrCommentTooShort       = errors.New("forum: comment too short")
	ErrCommentTooLong        = errors.New("forum: comment too long")
	ErrCommentNestingDeep    = errors.New("forum: comment nesting too deep")
	ErrEditWindowExpired     = errors.New("forum: edit window expired")
	ErrTopicCooldown         = errors.New("forum: topic cooldown")
	ErrCommentCooldown       = errors.New("forum: comment cooldown")
	ErrOutboundLinkForbidden = errors.New("forum: outbound links forbidden for new users")
	ErrMentionsLimit         = errors.New("forum: too many mentions")
	ErrDailyTopicLimit       = errors.New("forum: daily topic limit")
	ErrDailyCommentLimit     = errors.New("forum: daily comment limit")
	ErrTagMinRequired        = errors.New("forum: tag minimum required")
	// ErrUseSearchEndpoint 表示 topics 列表不再支持关键词检索，应改用专用搜索端点。
	ErrUseSearchEndpoint = errors.New("forum: use search endpoint")
	// ErrInvalidCursor 表示 after 游标非法、损坏或与当前 sort 不匹配。
	ErrInvalidCursor = errors.New("forum: invalid list cursor")
	// ErrGuestLoginRequired 游客阅读关闭时，匿名读请求被拒绝。
	ErrGuestLoginRequired = errors.New("forum: guest login required")
	// ErrDuplicateTitle 站点 duplicateTitlePolicy=block 时拒绝重复标题。
	ErrDuplicateTitle = errors.New("forum: duplicate title")
	// ErrRevisionNotFound 表示目标历史版本不存在；不区分主题/评论以避免枚举细节。
	ErrRevisionNotFound = errors.New("forum: revision not found")
	// ErrRevisionRedacted 表示历史版本 payload 已被超管清除，不能预览或返回源文。
	ErrRevisionRedacted = errors.New("forum: revision redacted")
	// ErrRevisionConflict 表示客户端提交的并发令牌已过期。
	ErrRevisionConflict = errors.New("forum: revision conflict")
	// ErrRevisionReasonRequired 表示跨作者编辑缺少受限审计理由。
	ErrRevisionReasonRequired        = errors.New("forum: revision reason required")
	ErrRevisionNotRestorable         = errors.New("forum: revision not restorable")
	ErrRevisionAttachmentUnavailable = errors.New("forum: revision attachment unavailable")
	ErrRevisionCategoryUnavailable   = errors.New("forum: revision category unavailable")
	ErrRevisionTagUnavailable        = errors.New("forum: revision tag unavailable")
	ErrRevisionRedactionForbidden    = errors.New("forum: revision redaction forbidden")
)

// CooldownError 携带服务端权威恢复时间，并保留 errors.Is 所需的稳定错误原因。
type CooldownError struct {
	Cause   error
	RetryAt time.Time
}

func (e *CooldownError) Error() string {
	return e.Cause.Error()
}

func (e *CooldownError) Unwrap() error {
	return e.Cause
}

// TopicSearchIndexer 是 forum 包对搜索索引调度的抽象。
// 由 search 支持包实现并注入 Service，避免 forum 反向依赖 job/搜索引擎。
// 实现应异步（入队）且对调用方安全；nil 时 Service 自动降级为不索引。
type TopicSearchIndexer interface {
	// EnqueueIndex 调度重新索引指定主题（用于创建/更新/评论/恢复/置顶等）。
	EnqueueIndex(ctx context.Context, topicID int64) error
	// EnqueueDelete 调度从索引移除指定主题（用于删除/隐藏）。
	EnqueueDelete(ctx context.Context, topicID int64) error
}

type TopicExtensionActionProvider interface {
	TopicExtensionActions(ctx context.Context) ([]TopicExtensionAction, error)
}

// CommentExtensionActionProvider 解析 forum.comment.actions（E2.2）。
// 描述符挂在 CommentList 上（列表级一次解析），前端再挂到每行菜单；无 per-row 插件 RPC。
type CommentExtensionActionProvider interface {
	CommentExtensionActions(ctx context.Context) ([]CommentExtensionAction, error)
}

// TopicExtensionSurfaceProvider 解析主题次级贡献点（E2.1 sidebar/badges + E2.4 list badges）。
// 与 TopicExtensionActionProvider 分离，避免破坏现有注入点；nil 时不装饰。
type TopicExtensionSurfaceProvider interface {
	TopicExtensionSidebar(ctx context.Context) ([]TopicExtensionSidebarItem, error)
	TopicExtensionBadges(ctx context.Context) ([]TopicExtensionBadge, error)
	// TopicExtensionListBadges 列表级一次解析；挂在 TopicList 上，非 per-row RPC。
	TopicExtensionListBadges(ctx context.Context) ([]TopicExtensionBadge, error)
}

type ContentInput struct {
	RawContent    string `json:"rawContent"`
	SourceFormat  string `json:"sourceFormat"`
	EditorType    string `json:"editorType"`
	EditorVersion string `json:"editorVersion"`
	Locale        string `json:"-"`
	// AttachmentIDs nil 表示未提交该字段；显式空数组表示移除全部正文附件。
	AttachmentIDs *[]int64 `json:"attachmentIds,omitempty"`
}

type Category struct {
	ID           int64     `json:"id"`
	GroupID      int64     `json:"groupId"`
	GroupSlug    string    `json:"groupSlug"`
	GroupName    string    `json:"groupName"`
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Icon         string    `json:"icon"`
	IconColor    string    `json:"iconColor"`
	Visibility   string    `json:"visibility"`
	Position     int       `json:"position"`
	DefaultSort  string    `json:"defaultSort"`
	TopicCount   int64     `json:"topicCount"`
	CommentCount int64     `json:"commentCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type CategoryGroup struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility"`
	Position    int        `json:"position"`
	Categories  []Category `json:"categories,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type Tag struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	IconColor   string    `json:"iconColor"`
	Status      string    `json:"status"`
	TopicCount  int64     `json:"topicCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type UserSummary struct {
	ID          int64       `json:"id"`
	Username    string      `json:"username"`
	DisplayName string      `json:"displayName"`
	Avatar      avatar.View `json:"avatar"`
}

type TopicListInput struct {
	Page    int
	PerPage int
	// After 非空时走 keyset，忽略 Page（cursor 优先于 page，M5）。
	After        string
	CategorySlug string
	TagSlug      string
	Query        string
	// Sort: latest | active | hot；空则由 service 用站点默认排序填充。
	Sort string
}

type TopicList struct {
	Items []TopicSummary `json:"items"`
	// Total 公开列表总数：分类/标签为冗余 topic_count（可短暂陈旧）；
	// 首页/多过滤为近似值。禁止依赖其为严格实时全表精确值（见 D1）。
	// Infinite scroll 应优先 HasMore / NextCursor。
	Total int64 `json:"total"`
	// TotalApproximate 为 true 时 total 为估计/交集近似（首页、多过滤）；
	// 单分类/单标签为 false（冗余计数，UI 不显示「约」）。
	TotalApproximate bool `json:"totalApproximate,omitempty"`
	Page             int  `json:"page"`
	PerPage          int  `json:"perPage"`
	// HasMore 是否还有下一页（M5 公开列表必填语义）。
	HasMore bool `json:"hasMore"`
	// NextCursor 有下一页时的 opaque after 游标；无则空。
	NextCursor string `json:"nextCursor,omitempty"`
	// ExtensionListBadges 来自 forum.topic.list.badges（E2.4）；列表级一次返回。
	ExtensionListBadges []TopicExtensionBadge `json:"extensionListBadges,omitempty"`
}

type TopicSummary struct {
	ID           int64        `json:"id"`
	CategoryID   int64        `json:"categoryId"`
	CategorySlug string       `json:"categorySlug"`
	CategoryName string       `json:"categoryName"`
	AuthorUserID int64        `json:"authorUserId"`
	Author       *UserSummary `json:"author,omitempty"`
	// LastReplyAuthor 最近一条 active 评论作者；无评论时与 Author 相同（列表「最近回复」列）。
	LastReplyAuthor *UserSummary `json:"lastReplyAuthor,omitempty"`
	Title           string       `json:"title"`
	Slug            string       `json:"slug"`
	Status          string       `json:"status"`
	IsPinned        bool         `json:"isPinned"`
	CommentCount    int64        `json:"commentCount"`
	ViewCount       int64        `json:"viewCount"`
	// HotScore 仅服务端 keyset 编码使用，不序列化到公开 JSON。
	HotScore       int64             `json:"-"`
	Tags           []TopicTagSummary `json:"tags,omitempty"`
	Excerpt        string            `json:"excerpt"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	LastActivityAt time.Time         `json:"lastActivityAt"`
	// CurrentRevision 是内容乐观并发令牌。混合迁移期旧内容以 >=1 的有效值回读，绝不暴露 0。
	CurrentRevision int64 `json:"currentRevision"`
	// Edited 主题正文是否曾被编辑（showTopicEditMark 开启时填充）。
	Edited bool `json:"edited,omitempty"`
	// ContentEdited 由存储层根据 post_revisions 得出，不直接暴露。
	ContentEdited bool `json:"-"`
}

type TopicDetail struct {
	TopicSummary
	Content PublicRenderedContent `json:"content"`
	// EditedAt 是当前已接受修订的提交时间；仅在编辑标记设置开启时对外保留。
	EditedAt *time.Time `json:"editedAt,omitempty"`
	// UpdateApplied 仅供写路径抑制 no-op 的事件、缓存和索引副作用，不进入 API。
	UpdateApplied       bool     `json:"-"`
	UpdateChangedFields []string `json:"-"`
	// Contributors 主题正文贡献者（作者 + 编辑/恢复 actor），作者优先，最多 5 个。
	// 默认全露出 staff 编辑者；隐私开关以后再加。
	Contributors []UserSummary `json:"contributors,omitempty"`
	// ContributorCount 去重后的贡献者总数（可大于 len(Contributors)）。
	ContributorCount int                         `json:"contributorCount,omitempty"`
	ExtensionActions []TopicExtensionAction      `json:"extensionActions,omitempty"`
	ExtensionSidebar []TopicExtensionSidebarItem `json:"extensionSidebar,omitempty"`
	ExtensionBadges  []TopicExtensionBadge       `json:"extensionBadges,omitempty"`
	// ProtectedContent marks a request-only composition boundary. It is never
	// serialized or persisted and only drives HTTP cache policy.
	ProtectedContent bool `json:"-"`
}

// TopicContributionEvent 是公开安全的贡献时间线条目：无 reason、无正文、无 diff。
type TopicContributionEvent struct {
	RevisionNo             int64        `json:"revisionNo"`
	Current                bool         `json:"current"`
	Actor                  *UserSummary `json:"actor,omitempty"`
	Operation              string       `json:"operation"`
	Origin                 string       `json:"origin"`
	ChangedFields          []string     `json:"changedFields"`
	CommittedAt            time.Time    `json:"committedAt"`
	RestoredFromRevisionNo *int64       `json:"restoredFromRevisionNo,omitempty"`
	Redacted               bool         `json:"redacted"`
}

// TopicContributionTimeline 公开贡献时间线（keyset 分页，与修订列表同游标语义）。
type TopicContributionTimeline struct {
	Items      []TopicContributionEvent `json:"items"`
	PerPage    int                      `json:"perPage"`
	HasMore    bool                     `json:"hasMore"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

type TopicExtensionAction struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Label       map[string]string `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Confirm     bool              `json:"confirm,omitempty"`
}

// CommentExtensionAction 是 forum.comment.actions 宿主安全描述符（E2.2）。
// RequiresAuth 仅 UX：游客可隐藏；unsafe 仍由扩展路由代理鉴权。
type CommentExtensionAction struct {
	ExtensionID  string            `json:"extensionId"`
	ID           string            `json:"id"`
	Label        map[string]string `json:"label,omitempty"`
	Icon         string            `json:"icon,omitempty"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Confirm      bool              `json:"confirm,omitempty"`
	RequiresAuth bool              `json:"requiresAuth,omitempty"`
}

// TopicExtensionSidebarItem 是 forum.topic.sidebar 宿主安全描述符（E2.1）。
// Kind: extensionRoute | hostLink；URL 对 extensionRoute 为 /extensions/{id}{path}。
type TopicExtensionSidebarItem struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Order       int               `json:"order"`
	Label       map[string]string `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Kind        string            `json:"kind"`
	Method      string            `json:"method,omitempty"`
	URL         string            `json:"url"`
}

// TopicExtensionBadge 是 forum.topic.badges 宿主安全描述符（E2.1）。
// Tone: neutral|info|success|warning|danger；Href 可选站内相对路径。
type TopicExtensionBadge struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Order       int               `json:"order"`
	Label       map[string]string `json:"label,omitempty"`
	Tone        string            `json:"tone"`
	Href        string            `json:"href,omitempty"`
}

// ComposerToolbarAction 是 forum.composer.toolbar 贡献的宿主安全描述符（F4.3）。
type ComposerToolbarAction struct {
	ExtensionID string            `json:"extensionId"`
	ID          string            `json:"id"`
	Order       int               `json:"order"`
	Label       map[string]string `json:"label,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Confirm     bool              `json:"confirm,omitempty"`
}

// ComposerToolbarProvider 由扩展贡献解析器实现。
type ComposerToolbarProvider interface {
	ComposerToolbarActions(ctx context.Context) ([]ComposerToolbarAction, error)
}

type CreateTopicInput struct {
	CategorySlug string       `json:"categorySlug"`
	Title        string       `json:"title"`
	TagSlugs     []string     `json:"tagSlugs,omitempty"`
	Content      ContentInput `json:"content"`
	// IPAddress 创建时客户端真实 IP（由 HTTP 层 clientip.FromCtx 注入；不进公开 JSON）。
	IPAddress string `json:"-"`
}

type CreateTopicRecord struct {
	ID                 int64
	CategoryID         int64
	CategorySlug       string
	AuthorUserID       int64
	Title              string
	Slug               string
	TagSlugs           []string
	TagCreationMode    string
	Tags               []TopicTagSummary
	Content            RenderedContent
	Status             string
	ModerationTriggers []string
	MentionedUsernames []string
	// IPAddress 创建时真实客户端 IP（全文，管理/风控用，不进公开 API）。
	IPAddress     string
	AttachmentIDs []int64
}

// UpdateTopicInput 是作者或版主更新主题时提交的输入。content 为可选：
// 不传则只更新标题/分类/标签，传则重新渲染 raw/html/plain 并写入 post_revisions（仅源文快照）。
type UpdateTopicInput struct {
	TopicID          int64
	ExpectedRevision int64
	Reason           string
	CategorySlug     *string
	Title            *string
	TagSlugs         []string
	Content          *ContentInput
	// IPAddress 本次编辑客户端 IP（写入 last_edit_ip；创建 ip_address 不变）。
	IPAddress string `json:"-"`
	// 以下字段只由 restore service 填充，不来自 HTTP PATCH。
	Operation                   string `json:"-"`
	RestoredFromRevisionID      int64  `json:"-"`
	RestoredFromRevisionNo      int64  `json:"-"`
	HistoricalAttachmentOwnerID int64  `json:"-"`
}

// UpdateTopicRecord 是 store 层更新主题的内部记录。content 为 nil 时表示不改正文。
type UpdateTopicRecord struct {
	TopicID          int64
	EditorUserID     int64
	ExpectedRevision int64
	Reason           string
	Origin           string
	AuthorUserID     int64
	CategorySlug     string
	Title            string
	Slug             string
	TagSlugs         []string
	TagCreationMode  string
	HasContent       bool
	Content          RenderedContent
	// RequeuePending 为 true 时把主题标为 pending 并写入 ModerationTriggers（编辑触发预审）。
	RequeuePending     bool
	ModerationTriggers []string
	// LastEditIP 本次编辑客户端真实 IP（全文）。
	LastEditIP         string
	ReplaceAttachments bool
	AttachmentIDs      []int64
	// Restore 复用 canonical update transaction，但必须保留来源和原作者附件边界。
	Operation                   string
	RestoredFromRevisionID      int64
	RestoredFromRevisionNo      int64
	HistoricalAttachmentOwnerID int64
}

// TopicLifecycleInput 描述一次主题生命周期动作（hide/restore/lock/unlock/pin/unpin）。
type TopicLifecycleInput struct {
	TopicID int64
	Action  string
}

// TopicLifecycleRecord 是 store 层执行生命周期动作后的主题快照。
type TopicLifecycleRecord struct {
	TopicID  int64
	Status   string
	IsPinned bool
}

type TopicTagSummary struct {
	ID     int64  `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type ForumSettings struct {
	DefaultCategorySlug string `json:"defaultCategorySlug"`
	TagCreationMode     string `json:"tagCreationMode"`
	TagPublicPages      bool   `json:"tagPublicPages"`
	TagMinPerTopic      int    `json:"tagMinPerTopic"`
	TagMaxPerTopic      int    `json:"tagMaxPerTopic"`
	TopicsPerPage       int    `json:"topicsPerPage"`
	CommentsPerPage     int    `json:"commentsPerPage"`

	// 主题内容与节奏限制（rune 计长；0 表示不限，除 min 外）
	TopicTitleMinRunes     int `json:"topicTitleMinRunes"`
	TopicTitleMaxRunes     int `json:"topicTitleMaxRunes"`
	TopicContentMinRunes   int `json:"topicContentMinRunes"`
	TopicContentMaxRunes   int `json:"topicContentMaxRunes"`
	TopicEditWindowMinutes int `json:"topicEditWindowMinutes"`
	TopicCooldownSeconds   int `json:"topicCooldownSeconds"`
	DailyTopicLimit        int `json:"dailyTopicLimit"`

	// 评论内容、嵌套与节奏限制
	CommentMinRunes        int `json:"commentMinRunes"`
	CommentMaxRunes        int `json:"commentMaxRunes"`
	CommentMaxNestingDepth int `json:"commentMaxNestingDepth"`
	// TreeDescendantsPerRoot view=tree 时每个根评论最多返回的子孙数（D2，默认 50）。
	TreeDescendantsPerRoot   int `json:"treeDescendantsPerRoot"`
	CommentEditWindowMinutes int `json:"commentEditWindowMinutes"`
	CommentCooldownSeconds   int `json:"commentCooldownSeconds"`
	DailyCommentLimit        int `json:"dailyCommentLimit"`

	// 列表摘要截断长度（读路径从 plain_text 派生 excerpt 时生效）
	ExcerptRuneLimit int `json:"excerptRuneLimit"`

	// Wave 1：阅读与主题/评论行为策略
	GuestRead               string `json:"guestRead"`       // public | login_required
	ListDefaultSort         string `json:"listDefaultSort"` // latest | active | hot
	ListHotWindowDays       int    `json:"listHotWindowDays"`
	AllowAuthorCloseReplies bool   `json:"allowAuthorCloseReplies"`
	AllowAuthorDelete       bool   `json:"allowAuthorDelete"`
	AutoLockIdleDays        int    `json:"autoLockIdleDays"` // 0=关闭；>0 由周期任务执行
	ShowTopicEditMark       bool   `json:"showTopicEditMark"`
	// DuplicateTitlePolicy：off 不校验；block 服务端拒绝重复标题。
	// 历史值 warn 按 off 处理（无独立 warn 合同，避免假开关）。
	DuplicateTitlePolicy string `json:"duplicateTitlePolicy"` // off | block
	ShowCommentEditMark  bool   `json:"showCommentEditMark"`
	// SoftDeleteVisibility：软删评论谁能在列表中看到墓碑（正文已清空）。
	// author_and_staff | staff_only | hidden
	SoftDeleteVisibility string `json:"softDeleteVisibility"`
	MentionsEnabled      bool   `json:"mentionsEnabled"`
	MentionsMaxPerPost   int    `json:"mentionsMaxPerPost"`
	// 正文外链呈现：站外链接是否经站内中转页（点击时判定），以及免中转域名白名单。
	ExternalInterstitial bool   `json:"externalInterstitial"`
	TrustedDomains       string `json:"trustedDomains"`
}

type CreateCategoryGroupInput struct {
	Slug        string
	Name        string
	Description string
	Visibility  string
	Position    int
}

type UpdateCategoryGroupInput struct {
	ID          int64
	Slug        *string
	Name        *string
	Description *string
	Visibility  *string
	Position    *int
}

type CreateCategoryInput struct {
	GroupID     int64
	Slug        string
	Name        string
	Description string
	Icon        string
	IconColor   string
	Visibility  string
	Position    int
	DefaultSort string
}

type UpdateCategoryInput struct {
	ID          int64
	GroupID     *int64
	Slug        *string
	Name        *string
	Description *string
	Icon        *string
	IconColor   *string
	Visibility  *string
	Position    *int
	DefaultSort *string
}

type CreateTagInput struct {
	Slug        string
	Name        string
	Description string
	Icon        string
	IconColor   string
	Status      string
	ActorUserID int64
}

type UpdateTagInput struct {
	ID          int64
	Slug        *string
	Name        *string
	Description *string
	Icon        *string
	IconColor   *string
	Status      *string
	ActorUserID int64
}

type UpdateForumSettingsInput struct {
	DefaultCategorySlug *string
	TagCreationMode     *string
	TagPublicPages      *bool
	TagMinPerTopic      *int
	TagMaxPerTopic      *int
	TopicsPerPage       *int
	CommentsPerPage     *int

	TopicTitleMinRunes       *int
	TopicTitleMaxRunes       *int
	TopicContentMinRunes     *int
	TopicContentMaxRunes     *int
	TopicEditWindowMinutes   *int
	TopicCooldownSeconds     *int
	DailyTopicLimit          *int
	CommentMinRunes          *int
	CommentMaxRunes          *int
	CommentMaxNestingDepth   *int
	TreeDescendantsPerRoot   *int
	CommentEditWindowMinutes *int
	CommentCooldownSeconds   *int
	DailyCommentLimit        *int
	ExcerptRuneLimit         *int

	GuestRead               *string
	ListDefaultSort         *string
	ListHotWindowDays       *int
	AllowAuthorCloseReplies *bool
	AllowAuthorDelete       *bool
	AutoLockIdleDays        *int
	ShowTopicEditMark       *bool
	DuplicateTitlePolicy    *string
	ShowCommentEditMark     *bool
	SoftDeleteVisibility    *string
	MentionsEnabled         *bool
	MentionsMaxPerPost      *int
	ExternalInterstitial    *bool
	TrustedDomains          *string
}

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
	ID            int64                 `json:"id"`
	TopicID       int64                 `json:"topicId"`
	AuthorUserID  int64                 `json:"authorUserId"`
	Author        *UserSummary          `json:"author,omitempty"`
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

type NotificationTargetPreview struct {
	TopicID    int64
	TopicTitle string
	Content    NotificationTargetPreviewContent
	Context    *NotificationTargetPreviewContent
}

type NotificationTargetPreviewContent struct {
	Type    string
	ID      int64
	Excerpt string
	Author  *UserSummary
}

type CreateCommentInput struct {
	TopicID  int64        `json:"topicId"`
	ParentID *int64       `json:"parentId,omitempty"`
	Content  ContentInput `json:"content"`
	// IPAddress 创建时客户端真实 IP（由 HTTP 层 clientip.FromCtx 注入；不进公开 JSON）。
	IPAddress string `json:"-"`
}

type CreateCommentRecord struct {
	ID                 int64
	TopicID            int64
	AuthorUserID       int64
	TopicAuthorUserID  int64
	ParentID           *int64
	Parent             *CommentSummary
	Content            RenderedContent
	Status             string
	ModerationTriggers []string
	MentionedUsernames []string
	// IPAddress 创建时真实客户端 IP（全文，管理/风控用，不进公开 API）。
	IPAddress     string
	AttachmentIDs []int64
}

type PublicationInput struct {
	ActorUserID int64
	RawContent  string
}

type PublicationDecision struct {
	Pending  bool
	Triggers []string
}

type AuthorReviewItem struct {
	TargetType string    `json:"targetType"`
	TargetID   int64     `json:"targetId"`
	TopicID    int64     `json:"topicId,omitempty"`
	Title      string    `json:"title"`
	Excerpt    string    `json:"excerpt"`
	Status     string    `json:"status"`
	ReviewNote string    `json:"reviewNote"`
	CreatedAt  time.Time `json:"createdAt"`
}

type AuthorReviewList struct {
	Items []AuthorReviewItem `json:"items"`
}

type UpdateCommentInput struct {
	CommentID        int64        `json:"commentId"`
	ExpectedRevision int64        `json:"expectedRevision"`
	Reason           string       `json:"reason"`
	Content          ContentInput `json:"content"`
	// IPAddress 本次编辑客户端 IP（写入 last_edit_ip）。
	IPAddress                   string `json:"-"`
	Operation                   string `json:"-"`
	RestoredFromRevisionID      int64  `json:"-"`
	RestoredFromRevisionNo      int64  `json:"-"`
	HistoricalAttachmentOwnerID int64  `json:"-"`
}

type UpdateCommentRecord struct {
	CommentID        int64
	EditorUserID     int64
	ExpectedRevision int64
	Reason           string
	Origin           string
	AuthorUserID     int64
	Content          RenderedContent
	// RequeuePending 为 true 时把评论标为 pending 并写入 ModerationTriggers。
	RequeuePending     bool
	ModerationTriggers []string
	// LastEditIP 本次编辑客户端真实 IP（全文）。
	LastEditIP                  string
	ReplaceAttachments          bool
	AttachmentIDs               []int64
	Operation                   string
	RestoredFromRevisionID      int64
	RestoredFromRevisionNo      int64
	HistoricalAttachmentOwnerID int64
}

type RestoreRevisionInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

type RedactRevisionInput struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	Reason           string `json:"reason"`
	Confirmation     string `json:"confirmation"`
}

type RevisionRedactionRecord struct {
	TargetID         int64
	TargetType       string
	RevisionNo       int64
	ExpectedRevision int64
	ActorUserID      int64
	Reason           string
}

type CommentPosition struct {
	RootCommentID int64
	PathKey       string
	Depth         int
}

type RevisionListInput struct {
	After   string
	PerPage int
}

type RevisionList struct {
	Items      []ForumRevisionSummary `json:"items"`
	PerPage    int                    `json:"perPage"`
	HasMore    bool                   `json:"hasMore"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type ForumRevisionSummary struct {
	ID                     int64        `json:"id"`
	RevisionNo             int64        `json:"revisionNo"`
	Current                bool         `json:"current"`
	Actor                  *UserSummary `json:"actor,omitempty"`
	Operation              string       `json:"operation"`
	Origin                 string       `json:"origin"`
	Reason                 string       `json:"reason,omitempty"`
	ChangedFields          []string     `json:"changedFields"`
	CommittedAt            time.Time    `json:"committedAt"`
	RestoredFromRevisionNo *int64       `json:"restoredFromRevisionNo,omitempty"`
	SnapshotComplete       bool         `json:"snapshotComplete"`
	RestorableFields       []string     `json:"restorableFields"`
	Redacted               bool         `json:"redacted"`
}

type HistoricalPreview struct {
	HTMLContent   string `json:"htmlContent"`
	PlainText     string `json:"plainText"`
	Excerpt       string `json:"excerpt"`
	RenderVersion string `json:"renderVersion"`
}

type AttachmentAvailabilitySummary struct {
	IDs   []int64 `json:"ids"`
	Total int     `json:"total"`
}

type TopicRevisionMetadata struct {
	Title        string   `json:"title,omitempty"`
	CategorySlug string   `json:"categorySlug,omitempty"`
	TagSlugs     []string `json:"tagSlugs"`
}

type ForumRevisionDetail struct {
	ForumRevisionSummary
	RawContent    string                        `json:"rawContent"`
	SourceFormat  string                        `json:"sourceFormat"`
	EditorType    string                        `json:"editorType"`
	EditorVersion string                        `json:"editorVersion"`
	RenderVersion string                        `json:"renderVersion"`
	ContentHash   string                        `json:"contentHash"`
	Attachments   AttachmentAvailabilitySummary `json:"attachments"`
	Preview       *HistoricalPreview            `json:"preview,omitempty"`
	TopicMetadata *TopicRevisionMetadata        `json:"topicMetadata,omitempty"`
}

type AdminForumContentListInput struct {
	After          string
	PerPage        int
	Status         string
	AuthorUserID   int64
	AuthorUsername string
	UpdatedFrom    time.Time
	UpdatedTo      time.Time
	TopicID        int64
	TitlePrefix    string
	CategorySlug   string
}

type AdminForumContentList struct {
	Items      []AdminForumContentRow `json:"items"`
	PerPage    int                    `json:"perPage"`
	HasMore    bool                   `json:"hasMore"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type AdminForumContentRow struct {
	TargetType      string            `json:"targetType"`
	ID              int64             `json:"id"`
	TopicID         int64             `json:"topicId,omitempty"`
	TopicTitle      string            `json:"topicTitle,omitempty"`
	CategorySlug    string            `json:"categorySlug,omitempty"`
	AuthorUserID    int64             `json:"authorUserId"`
	Author          *UserSummary      `json:"author,omitempty"`
	Status          string            `json:"status"`
	Title           string            `json:"title,omitempty"`
	Excerpt         string            `json:"excerpt"`
	CurrentRevision int64             `json:"currentRevision"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	Tags            []TopicTagSummary `json:"tags,omitempty"`
}

type AdminForumTopicDetail struct {
	AdminForumContentRow
	Content RenderedContent `json:"content"`
	Slug    string          `json:"slug"`
}

type AdminForumCommentDetail struct {
	AdminForumContentRow
	Content       RenderedContent `json:"content"`
	ParentID      *int64          `json:"parentId,omitempty"`
	RootCommentID int64           `json:"rootCommentId"`
	PathKey       string          `json:"pathKey"`
	Depth         int             `json:"depth"`
	ReplyCount    int64           `json:"replyCount"`
}
