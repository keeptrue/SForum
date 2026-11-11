package themecompiler

// Page ViewModels deliberately use only passive, presentation-safe DTOs.
// There are no maps, interface fields, session objects, credentials, or raw
// actor models in this graph. Rich HTML is represented only by sealed SafeHTML.

type PageViewModelKind string

const (
	ViewModelHome    PageViewModelKind = "home"
	ViewModelList    PageViewModelKind = "list"
	ViewModelDetail  PageViewModelKind = "detail"
	ViewModelProfile PageViewModelKind = "profile"
	ViewModelError   PageViewModelKind = "error"
	ViewModelCreate  PageViewModelKind = "create"

	ViewModelSettings      PageViewModelKind = "settings"
	ViewModelNotifications PageViewModelKind = "notifications"
	ViewModelModeration    PageViewModelKind = "moderation"
	ViewModelAuth          PageViewModelKind = "auth"
	ViewModelLegal         PageViewModelKind = "legal"
	ViewModelDevelopment   PageViewModelKind = "development"
	// ViewModelUtility：宿主工具页（无业务列表/详情语义），例：站外链接中转页。
	ViewModelUtility PageViewModelKind = "utility"
)

type PageViewModelSchema struct {
	PageID        string            `json:"pageId"`
	SchemaVersion string            `json:"schemaVersion"`
	Kind          PageViewModelKind `json:"kind"`
}

type BoundPageViewModel struct {
	pageID             string
	schemaVersion      string
	pluginSchemaDigest string
	themePackageDigest string
	value              any
	seo                PageSEOView
}

func (m BoundPageViewModel) PageID() string        { return m.pageID }
func (m BoundPageViewModel) SchemaVersion() string { return m.schemaVersion }

type PageViewModelBase struct {
	PageID        string           `json:"pageId"`
	SchemaVersion string           `json:"schemaVersion"`
	Locale        string           `json:"locale"`
	Route         PageRouteView    `json:"route"`
	Viewer        PageViewerState  `json:"viewer"`
	Pagination    *PaginationView  `json:"pagination,omitempty"`
	SEO           PageSEOView      `json:"seo"`
	Navigation    []NavigationItem `json:"navigation,omitempty"`
	Breadcrumbs   []BreadcrumbItem `json:"breadcrumbs,omitempty"`
	Regions       []PageRegion     `json:"regions,omitempty"`
}

type PageRouteView struct {
	Path   string       `json:"path"`
	Params []RouteParam `json:"params,omitempty"`
}

type RouteParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PageViewerState is a projection, never the identity Actor/session object.
// Permissions are copied strings used for presentation only; API policy stays
// authoritative for every mutation.
type PageViewerState struct {
	Authenticated bool     `json:"authenticated"`
	UserID        int64    `json:"userId,omitempty"`
	Username      string   `json:"username,omitempty"`
	DisplayName   string   `json:"displayName,omitempty"`
	AvatarURL     string   `json:"avatarUrl,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
}

type PaginationView struct {
	Page        int    `json:"page"`
	PageSize    int    `json:"pageSize"`
	Total       int64  `json:"total"`
	PreviousURL string `json:"previousUrl,omitempty"`
	NextURL     string `json:"nextUrl,omitempty"`
}

type PageSEOView struct {
	Title          string               `json:"title"`
	Description    string               `json:"description,omitempty"`
	CanonicalURL   string               `json:"canonicalUrl,omitempty"`
	Robots         string               `json:"robots,omitempty"`
	AlternateLinks []AlternateLink      `json:"alternateLinks,omitempty"`
	StructuredData []StructuredDataView `json:"structuredData,omitempty"`
}

type AlternateLink struct {
	Locale string `json:"locale"`
	URL    string `json:"url"`
}

// StructuredDataView covers the common inspectable fields without accepting
// arbitrary JSON-LD maps from themes or plugins.
type StructuredDataView struct {
	Kind        string `json:"kind"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	DateCreated string `json:"dateCreated,omitempty"`
	DateUpdated string `json:"dateUpdated,omitempty"`
}

type NavigationItem struct {
	ID       string           `json:"id"`
	Label    string           `json:"label"`
	URL      string           `json:"url"`
	Active   bool             `json:"active,omitempty"`
	Children []NavigationItem `json:"children,omitempty"`
}

type BreadcrumbItem struct {
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

type PageRegion struct {
	ID    string           `json:"id"`
	Items []PageRegionItem `json:"items,omitempty"`
}

// PageRegionItem is intentionally fixed-shape. P9 may version additional
// component props, but a generic props map is never part of a Page ViewModel.
type PageRegionItem struct {
	ComponentID string `json:"componentId"`
	Label       string `json:"label,omitempty"`
	Text        string `json:"text,omitempty"`
	URL         string `json:"url,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
}

type PublicUserView struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

type TaxonomyLinkView struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Count       int64  `json:"count,omitempty"`
}

type TopicSummaryView struct {
	ID         int64              `json:"id"`
	Title      string             `json:"title"`
	URL        string             `json:"url"`
	Excerpt    string             `json:"excerpt,omitempty"`
	Author     PublicUserView     `json:"author"`
	Category   TaxonomyLinkView   `json:"category"`
	Tags       []TaxonomyLinkView `json:"tags,omitempty"`
	ReplyCount int64              `json:"replyCount"`
	CreatedAt  string             `json:"createdAt"`
	UpdatedAt  string             `json:"updatedAt"`
}

type CommentView struct {
	ID        int64          `json:"id"`
	Author    PublicUserView `json:"author"`
	Body      SafeHTML       `json:"body"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
}

type HomePageViewModel struct {
	Base       PageViewModelBase  `json:"base"`
	Topics     []TopicSummaryView `json:"topics"`
	Categories []TaxonomyLinkView `json:"categories,omitempty"`
	Tags       []TaxonomyLinkView `json:"tags,omitempty"`
	Search     *SearchStateView   `json:"search,omitempty"`
}

type PageListView struct {
	Title       string             `json:"title"`
	Description SafeHTML           `json:"description"`
	Items       []TopicSummaryView `json:"items"`
	Taxonomy    []TaxonomyLinkView `json:"taxonomy,omitempty"`
	Search      *SearchStateView   `json:"search,omitempty"`
}

type CategoryIndexPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	List PageListView      `json:"list"`
}

type CategoryShowPageViewModel struct {
	Base     PageViewModelBase `json:"base"`
	Category TaxonomyLinkView  `json:"category"`
	List     PageListView      `json:"list"`
}

type TagIndexPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	List PageListView      `json:"list"`
}

// BadgeCatalogItemView 是公开徽章目录里的一枚徽章。
//
// 字段集是公开面的白名单：不含 threshold / metric（阈值是运营调参，公开即不可撤回），
// 也不含 enabled / grantedCount / 时间戳。Earned 是查看者相关字段，由页面装配时
// 每请求合并，不进任何共享缓存。
type BadgeCatalogItemView struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Tier        string `json:"tier"`
	Group       string `json:"group"`
	Earned      bool   `json:"earned"`
}

// BadgeCatalogGroupView 是目录的一个分组外壳；空分组不会出现。
type BadgeCatalogGroupView struct {
	Key    string                `json:"key"`
	Name   string                `json:"name"`
	Badges []BadgeCatalogItemView `json:"badges"`
}

// BadgeIndexPageViewModel 是 /badges 公共目录页的载荷。
//
// Truncated 在顶层：截断是整份目录的属性，不是某个分组的属性。
type BadgeIndexPageViewModel struct {
	Base      PageViewModelBase       `json:"base"`
	Groups    []BadgeCatalogGroupView `json:"groups"`
	Truncated bool                    `json:"truncated"`
}

type TagShowPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Tag  TaxonomyLinkView  `json:"tag"`
	List PageListView      `json:"list"`
}

type TopicDetailPageViewModel struct {
	Base     PageViewModelBase `json:"base"`
	Topic    TopicSummaryView  `json:"topic"`
	Body     SafeHTML          `json:"body"`
	Comments []CommentView     `json:"comments"`
}

type ProfilePageViewModel struct {
	Base    PageViewModelBase  `json:"base"`
	Profile PublicUserView     `json:"profile"`
	Bio     SafeHTML           `json:"bio"`
	Topics  []TopicSummaryView `json:"topics,omitempty"`
}

type SearchResultView struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Excerpt string `json:"excerpt,omitempty"`
}

// SearchStateView lives inside the shared home/search/list presentation contracts.
type SearchStateView struct {
	Query   string             `json:"query"`
	Results []SearchResultView `json:"results"`
}

// HostFormBoundary identifies a Host-owned interactive form island. It never
// carries submitted values, credentials, CSRF material, or session state.
type HostFormBoundary struct {
	ComponentID    string   `json:"componentId"`
	ActionRouteIDs []string `json:"actionRouteIds"`
}

type TopicCreatePageViewModel struct {
	Base       PageViewModelBase  `json:"base"`
	Form       HostFormBoundary   `json:"form"`
	Categories []TaxonomyLinkView `json:"categories"`
	Tags       []TaxonomyLinkView `json:"tags,omitempty"`
}

// TopicReplyPageViewModel 高级回复独立页：完整编辑器 + create_comment 边界。
type TopicReplyPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

// TopicEditPageViewModel 主题编辑独立页：宿主编辑器岛 + update_topic 边界。
// 主题定位与旧内容加载由编辑岛按路由 topicId 自行完成。
type TopicEditPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type ProfileSettingsPageViewModel struct {
	Base    PageViewModelBase `json:"base"`
	Form    HostFormBoundary  `json:"form"`
	Profile PublicUserView    `json:"profile"`
}

// AppearanceSettingsPageViewModel exposes only the Host preference boundary;
// private account values remain in the authenticated Host island.
type AppearanceSettingsPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type SecurityDeviceView struct {
	Label      string `json:"label"`
	Current    bool   `json:"current"`
	LastSeenAt string `json:"lastSeenAt"`
}

type SecuritySettingsPageViewModel struct {
	Base                 PageViewModelBase    `json:"base"`
	Form                 HostFormBoundary     `json:"form"`
	CredentialConfigured bool                 `json:"credentialConfigured"`
	Devices              []SecurityDeviceView `json:"devices,omitempty"`
}

// PersonalAccessTokensPageViewModel 只声明 Host 岛和 PAT 管理边界；
// 明文密钥、scope 选择和令牌列表都由 Host 岛通过登录态 API 读取。
type PersonalAccessTokensPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

// LoginMethodsSettingsPageViewModel 只声明 Host 岛和外部身份路由边界；
// 绑定主体和 OAuth 状态都不会进入主题 ViewModel。
type LoginMethodsSettingsPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

// LocalPasswordSettingsPageViewModel 只声明 Host 岛和本地密码保存边界；
// 密码材料不会进入主题 ViewModel。
type LocalPasswordSettingsPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

// NotificationSettingsPageViewModel 只声明 Host 岛与已审查路由边界；
// 偏好、浏览器订阅和密钥材料不会进入主题 ViewModel。
type NotificationSettingsPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type NotificationItemView struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Read      bool   `json:"read"`
	CreatedAt string `json:"createdAt"`
}

type NotificationsPageViewModel struct {
	Base  PageViewModelBase      `json:"base"`
	Items []NotificationItemView `json:"items"`
}

type ModerationQueueItemView struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

type ModerationReviewPageViewModel struct {
	Base  PageViewModelBase         `json:"base"`
	Items []ModerationQueueItemView `json:"items"`
}

type LoginPageViewModel struct {
	Base                PageViewModelBase `json:"base"`
	Form                HostFormBoundary  `json:"form"`
	RegistrationEnabled bool              `json:"registrationEnabled"`
	RecoveryEnabled     bool              `json:"recoveryEnabled"`
}

type RegisterPageViewModel struct {
	Base                PageViewModelBase `json:"base"`
	Form                HostFormBoundary  `json:"form"`
	RegistrationEnabled bool              `json:"registrationEnabled"`
}

type ExternalAuthContinuationPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type EmailVerificationPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type ForgotPasswordPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	Form HostFormBoundary  `json:"form"`
}

type ResetPasswordPageViewModel struct {
	Base           PageViewModelBase `json:"base"`
	Form           HostFormBoundary  `json:"form"`
	ChallengeReady bool              `json:"challengeReady"`
}

type TermsPageViewModel struct {
	Base      PageViewModelBase `json:"base"`
	Content   SafeHTML          `json:"content"`
	UpdatedAt string            `json:"updatedAt"`
}

type PrivacyPageViewModel struct {
	Base      PageViewModelBase `json:"base"`
	Content   SafeHTML          `json:"content"`
	UpdatedAt string            `json:"updatedAt"`
}

type GuidelinesPageViewModel struct {
	Base      PageViewModelBase `json:"base"`
	Content   SafeHTML          `json:"content"`
	UpdatedAt string            `json:"updatedAt"`
}

type ComponentPreviewView struct {
	ComponentID string `json:"componentId"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type DevelopmentComponentsPageViewModel struct {
	Base       PageViewModelBase      `json:"base"`
	Components []ComponentPreviewView `json:"components"`
}

type ErrorPageViewModel struct {
	Base       PageViewModelBase `json:"base"`
	StatusCode int               `json:"statusCode"`
	Title      string            `json:"title"`
	Message    string            `json:"message"`
}

// OutboundPageViewModel 只承载「当前是否启用中转」这一运营开关；
// 目标地址由页面自行从 ?u= 解码（与前端指令共用同一解码实现），
// 服务端不下发、也不签发任何跳转凭据。
type OutboundPageViewModel struct {
	Base PageViewModelBase `json:"base"`
	// InterstitialEnabled 关闭时中转页立即放行（运营可在后台关掉中转）。
	InterstitialEnabled bool `json:"interstitialEnabled"`
}
