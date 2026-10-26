package extensionmanifest

import (
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

const (
	ManifestFileName = "sforum.extension.json"

	TypePlugin = "plugin"
	TypeTheme  = "theme"

	RouteAccessPublic     = "public"
	RouteAccessLogin      = "login"
	RouteAccessPermission = "permission"

	// HookMaximumTimeoutMS 与 Protocol V2 unary deadline 对齐，防止同步
	// listener 通过 manifest 长期占用 Host admission/concurrency 槽位。
	HookMaximumTimeoutMS = 5000
	// ProviderSlotMaximumTimeoutMS 仅约束带请求/响应 schema 的 V2 slot。
	// 旧 Host provider 的超时由各 owner contract 管理，不能在 Manifest V3 中静默收紧。
	ProviderSlotMaximumTimeoutMS = 5000

	// 插件任务策略受 Host 容量约束。默认值延续既有的每扩展并发上限，
	// 同时避免 River 全局默认值变化后静默改变 manifest 契约。
	PluginJobDefaultConcurrencyLimit    = 4
	PluginJobMaximumConcurrencyLimit    = 16
	PluginJobMaximumAttempts            = 25
	PluginJobDefaultBoundedAttempts     = 5
	PluginJobDefaultExponentialAttempts = 10
	PluginJobDefaultRetryDelaySeconds   = 30
	// 插件可声明的队列名。封闭集合的理由是资源归属：default 与 Core 日常任务共享，
	// ai 是站点给慢速模型调用留的低并发通道；其余队列（critical/mail/notifications/
	// search/maintenance）承载 Core 自己的投递，不向插件开放。
	PluginJobQueueDefault             = "default"
	PluginJobQueueAI                  = "ai"
	PluginJobMaximumRetryDelaySeconds = 3600
	PluginCommandMaximumTimeoutMS     = 5000

	// Manifest V3 的稳定标识、引用和单族声明上限也由运行时 Registry 复用。
	// 预检与发布必须接受/拒绝同一份包，不能把边界分散成两套漂移的常量。
	ManifestIDMaximumLength       = 81
	ContractVersionMaximumLength  = 256
	HandlerReferenceMaximumLength = 256
	SchemaReferenceMaximumLength  = 256
	ContentDeclarationsMaximum    = 512

	// 路由字段变更是最小权限 allowlist：禁止授权整个文档，并限制每次请求需要复制、比较的声明规模。
	RouteMutableFieldsMaximumCount = 64
	RouteMutableFieldMaximumBytes  = 256
	RouteMutableFieldMaximumTokens = 32
)

var (
	ErrInvalidManifest = errors.New("extensions: invalid manifest")

	manifestIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	// manifestVersionPattern 约束 Version 字段，防止 "../../../tmp/evil" 之类的路径穿越
	// 在 filepath.Join(extensionRoot, id, version) 时逃逸出 extensionRoot（C1）。
	// 允许语义化版本常见字符，禁止 / \ 与纯 . / .. 。
	manifestVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+\-~]{0,63}$`)
)

type Manifest struct {
	// ManifestVersion 必须显式为 V3；旧版清单不再进入安装或运行时。
	ManifestVersion int            `json:"manifestVersion,omitempty"`
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	URL             string         `json:"url"`
	Author          ManifestAuthor `json:"author"`
	Version         string         `json:"version"`
	Type            string         `json:"type"`
	SForumVersion   string         `json:"sforumVersion"`
	// Langs 是可选的本地化覆盖。顶层 name/description/author 为默认英文；
	// 未声明 langs 时无需翻译，直接使用顶层字段。
	Langs       map[string]ManifestLocale `json:"langs,omitempty"`
	Permissions []string                  `json:"permissions"`
	// Capabilities 为插件声明的 Host 能力（F2.1）。主题必须为空。
	// 未声明时宿主仍会按 jobs/settings/providers/backend 推断最小集。
	Capabilities []string `json:"capabilities,omitempty"`
	// RequiresFeatures 为站点产品开关依赖（F4.5）。须为宿主 features.* 目录 key；
	// 启用时若任一开关关闭则拒绝。主题必须为空。
	RequiresFeatures []string               `json:"requiresFeatures,omitempty"`
	Settings         []ManifestSetting      `json:"-"`
	SettingsDocument SettingsDocument       `json:"-"`
	Migrations       []ManifestMigration    `json:"migrations"`
	Backend          ManifestBackend        `json:"backend"`
	Admin            ManifestAdmin          `json:"admin"`
	AdminPages       []ManifestAdminPage    `json:"adminPages"`
	Routes           []ManifestRoute        `json:"routes"`
	Hooks            []ManifestHook         `json:"hooks"`
	Events           []ManifestEvent        `json:"events"`
	Jobs             []ManifestJob          `json:"jobs"`
	Providers        []ManifestProvider     `json:"providers"`
	Contributions    []ManifestContribution `json:"contributions"`
	Guards           []ManifestGuard        `json:"guards,omitempty"`
	Schedules        []ManifestSchedule     `json:"schedules,omitempty"`
	Components       []ManifestComponent    `json:"components,omitempty"`
	Templates        []ManifestTemplate     `json:"templates,omitempty"`
	Assets           []ManifestAsset        `json:"assets,omitempty"`
	Content          []ManifestContent      `json:"content,omitempty"`
	// Editor is the P10 Tiptap node/mark/command/toolbar surface (trusted L2).
	Editor []ManifestEditor `json:"editor,omitempty"`
	// Entities is the P10 Entity Type / Taxonomy / Field Schema surface.
	Entities              []ManifestEntity               `json:"entities,omitempty"`
	Database              *ManifestDatabase              `json:"database,omitempty"`
	Cache                 []ManifestCache                `json:"cache,omitempty"`
	SEO                   []ManifestSEO                  `json:"seo,omitempty"`
	Services              []ManifestService              `json:"services,omitempty"`
	Commands              []ManifestCommand              `json:"commands,omitempty"`
	AdminSurfaces         []ManifestAdminSurface         `json:"adminSurfaces,omitempty"`
	Queries               []ManifestQuery                `json:"queries,omitempty"`
	QueryResultFilters    []ManifestQueryResultFilter    `json:"queryResultFilters,omitempty"`
	Identity              *ManifestIdentity              `json:"identity,omitempty"`
	PermissionDefinitions []ManifestPermissionDefinition `json:"permissionDefinitions,omitempty"`
	NotificationTypes     []ManifestNotificationType     `json:"notificationTypes,omitempty"`
	Media                 []ManifestMediaPipeline        `json:"media,omitempty"`
	Navigation            []ManifestNavigation           `json:"navigation,omitempty"`
	Regions               []ManifestRegion               `json:"regions,omitempty"`
	Dependencies          []ManifestDependency           `json:"dependencies,omitempty"`
	Lifecycle             *ManifestLifecycle             `json:"lifecycle,omitempty"`
	OpenAPI               []ManifestOpenAPIFragment      `json:"openapi,omitempty"`
	AITools               []ManifestAITool               `json:"aiTools,omitempty"`
	PackageFiles          []ManifestPackageFile          `json:"packageFiles,omitempty"`
}

type ManifestAuthor struct {
	Name  string `json:"name"`
	URL   string `json:"url,omitempty"`
	Email string `json:"email,omitempty"`
}

// ManifestLocale 覆盖可展示文案。字段均可选，有值才覆盖顶层默认。
type ManifestLocale struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	URL         string         `json:"url,omitempty"`
	Author      ManifestAuthor `json:"author,omitempty"`
}

type ManifestMigration struct {
	ID              string `json:"id,omitempty"`
	ContractVersion string `json:"contractVersion,omitempty"`
	Path            string `json:"path"`
	Digest          string `json:"digest,omitempty"`
	Transaction     string `json:"transaction,omitempty"`
}

type ManifestBackend struct {
	Entry           string `json:"entry"`
	RPC             string `json:"rpc"`
	ProtocolVersion int    `json:"protocolVersion,omitempty"`
	Digest          string `json:"digest,omitempty"`
	HostAPIVersion  string `json:"hostApiVersion,omitempty"`
}

type ManifestAdmin struct {
	Entry string              `json:"entry,omitempty"`
	Pages []ManifestAdminPage `json:"pages,omitempty"`
}

type ManifestAdminPage struct {
	Path        string `json:"path"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	View        string `json:"view,omitempty"`
	Menu        bool   `json:"menu,omitempty"`
	// Group 把同一扩展的多个 menu 页签收进一个独立的可展开侧栏分组；
	// 空值表示沿用旧行为（平铺在「扩展管理」文件夹内）。
	Group      string          `json:"group,omitempty"`
	Order      int             `json:"order,omitempty"`
	Permission string          `json:"permission,omitempty"`
	Component  *AdminComponent `json:"component,omitempty"`
	// HostActions 声明宿主为该管理页额外提供的动作 ID（首版仅
	// extension.install）。只允许出现在 view=component 的页面上：动作是
	// 宿主中介的，不是插件进程能力。
	HostActions []string `json:"hostActions,omitempty"`
}

type ManifestRoute struct {
	ID                    string   `json:"id,omitempty"`
	ContractVersion       string   `json:"contractVersion,omitempty"`
	Action                string   `json:"action,omitempty"`
	TargetID              string   `json:"targetId,omitempty"`
	Path                  string   `json:"path"`
	Methods               []string `json:"methods"`
	Access                string   `json:"access,omitempty"`
	Permission            string   `json:"permission,omitempty"`
	Guard                 string   `json:"guard,omitempty"`
	Priority              int      `json:"priority,omitempty"`
	Fallback              string   `json:"fallback,omitempty"`
	Mode                  string   `json:"mode,omitempty"`
	Destination           string   `json:"destination,omitempty"`
	StatusCode            int      `json:"statusCode,omitempty"`
	Handler               string   `json:"handler,omitempty"`
	RequestSchema         string   `json:"requestSchema,omitempty"`
	ResponseSchema        string   `json:"responseSchema,omitempty"`
	MutableRequestFields  []string `json:"mutableRequestFields,omitempty"`
	MutableResponseFields []string `json:"mutableResponseFields,omitempty"`
	TimeoutMS             int      `json:"timeoutMs,omitempty"`
}

type ManifestHook struct {
	ID              string   `json:"id,omitempty"`
	ContractVersion string   `json:"contractVersion,omitempty"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind,omitempty"`
	TargetID        string   `json:"targetId,omitempty"`
	Handler         string   `json:"handler,omitempty"`
	InputSchema     string   `json:"inputSchema,omitempty"`
	ResultSchema    string   `json:"resultSchema,omitempty"`
	Priority        int      `json:"priority,omitempty"`
	Execution       string   `json:"execution,omitempty"`
	FailurePolicy   string   `json:"failurePolicy,omitempty"`
	TimeoutMS       int      `json:"timeoutMs,omitempty"`
	MutableFields   []string `json:"mutableFields,omitempty"`
}

type ManifestEvent struct {
	ID              string `json:"id,omitempty"`
	ContractVersion string `json:"contractVersion,omitempty"`
	Name            string `json:"name"`
	Kind            string `json:"kind,omitempty"`
	Handler         string `json:"handler,omitempty"`
	InputSchema     string `json:"inputSchema,omitempty"`
	ResultSchema    string `json:"resultSchema,omitempty"`
	Priority        int    `json:"priority,omitempty"`
	TimeoutMS       int    `json:"timeoutMs,omitempty"`
}

type ManifestJob struct {
	ID                string `json:"id,omitempty"`
	ContractVersion   string `json:"contractVersion,omitempty"`
	Name              string `json:"name"`
	Handler           string `json:"handler,omitempty"`
	PayloadSchema     string `json:"payloadSchema,omitempty"`
	RetryPolicy       string `json:"retryPolicy,omitempty"`
	MaxAttempts       int    `json:"maxAttempts,omitempty"`
	RetryDelaySeconds int    `json:"retryDelaySeconds,omitempty"`
	ConcurrencyLimit  int    `json:"concurrencyLimit,omitempty"`
	// Queue 让插件把慢任务放进宿主公开的慢速通道（默认 default，可选 ai）。
	// 取值刻意封闭：critical/mail/notifications 这些队列承载 Core 自己的投递，
	// 允许插件往那里塞任务等于让第三方决定站内邮件与通知的延迟。
	Queue string `json:"queue,omitempty"`
}

type ManifestProvider struct {
	ID              string `json:"id,omitempty"`
	ContractVersion string `json:"contractVersion,omitempty"`
	Slot            string `json:"slot"`
	TargetID        string `json:"targetId,omitempty"`
	Label           string `json:"label"`
	Handler         string `json:"handler,omitempty"`
	RequestSchema   string `json:"requestSchema,omitempty"`
	ResponseSchema  string `json:"responseSchema,omitempty"`
	Fallback        string `json:"fallback,omitempty"`
	Priority        int    `json:"priority,omitempty"`
	TimeoutMS       int    `json:"timeoutMs,omitempty"`
	MultiInstance   bool   `json:"multiInstance,omitempty"`
}
type ManifestContribution struct {
	Point   string            `json:"point"`
	ID      string            `json:"id"`
	Order   int               `json:"order,omitempty"`
	Label   map[string]string `json:"label,omitempty"`
	Icon    string            `json:"icon,omitempty"`
	Payload json.RawMessage   `json:"payload,omitempty"`
	// EnabledBySetting 非空时：仅当该 boolean 设置解析为真（已存值或 schema 默认）时，贡献才进入 EffectiveContributions。
	// 用于「默认隐藏、后台可开」的公共 UI 贡献，避免插件启用后强制露出演示徽章。
	EnabledBySetting string `json:"enabledBySetting,omitempty"`
}

// 稳定贡献点 ID（F4.3/E2 起与目录同步；新增点必须改此处 + OpenAPI + 文档 regenerate）。
const (
	PointForumTopicActions     = "forum.topic.actions"
	PointForumTopicSidebar     = "forum.topic.sidebar"
	PointForumTopicBadges      = "forum.topic.badges"
	PointForumCommentActions   = "forum.comment.actions"
	PointForumNavItems         = "forum.nav.items"
	PointForumTopicListBadges  = "forum.topic.list.badges"
	PointForumComposerToolbar  = "forum.composer.toolbar"
	PointForumProfileTabs      = "forum.profile.tabs"
	PointAdminDashboardWidgets = "admin.dashboard.widgets"
	PointSystemHealthChecks    = "system.health.checks"
	PointForumPageRegions      = "forum.page.regions"
)

// 宿主拥有的 payloadType；禁止可执行 JSON。
const (
	PayloadTypeExtensionRoute   = "extensionRoute"
	PayloadTypeProfileSection   = "profileSection"
	PayloadTypeTopicSidebarCard = "topicSidebarCard"
	PayloadTypeTopicBadge       = "topicBadge"
	PayloadTypeNavItem          = "navItem"
	PayloadTypeDashboardLink    = "dashboardLink"
	PayloadTypeHealthDescriptor = "healthDescriptor"
	PayloadTypeRegionPlacement  = "regionPlacement"
)

// TopicActionContributionPayload 用于 forum.topic.actions / forum.comment.actions（extensionRoute）。
// RequiresAuth 为 UX 提示：前端可对游客隐藏；写操作仍由扩展路由代理做权威鉴权。
type TopicActionContributionPayload struct {
	Type         string `json:"type"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Confirm      bool   `json:"confirm,omitempty"`
	RequiresAuth bool   `json:"requiresAuth,omitempty"`
}

// CommentActionContributionPayload 与主题动作同形（E2.2）。
type CommentActionContributionPayload = TopicActionContributionPayload

// ComposerToolbarContributionPayload 用于 forum.composer.toolbar（extensionRoute）。
// 与主题操作相同：宿主渲染按钮，执行走扩展路由代理。
type ComposerToolbarContributionPayload = TopicActionContributionPayload

// ProfileTabContributionPayload 用于 forum.profile.tabs（profileSection）。
// type=extensionRoute：走扩展路由；type=hostLink：仅允许站内相对路径（非 /api）。
type ProfileTabContributionPayload struct {
	Type   string `json:"type"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Href   string `json:"href,omitempty"`
}

// TopicSidebarContributionPayload 用于 forum.topic.sidebar（topicSidebarCard）。
// 卡片标题/图标来自 contribution.label / icon；payload 只描述跳转目标。
// type=extensionRoute：走扩展路由代理；type=hostLink：仅站内相对路径（非 /api）。
type TopicSidebarContributionPayload struct {
	Type   string `json:"type"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Href   string `json:"href,omitempty"`
}

// TopicBadgeContributionPayload 用于 forum.topic.badges / forum.topic.list.badges（topicBadge）。
// 文案来自 contribution.label；tone 为宿主枚举；可选 host 相对链接（无外链）。
type TopicBadgeContributionPayload struct {
	Tone string `json:"tone"`
	Href string `json:"href,omitempty"`
}

// NavItemContributionPayload 用于 forum.nav.items（navItem）。
// 标签/图标来自 contribution.label / icon；payload 只描述公开跳转目标。
// type=hostLink：站内相对路径（非 /api、非 /admin）；type=extensionRoute：公开扩展路由（导航以 GET 为主）。
type NavItemContributionPayload struct {
	Type   string `json:"type"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Href   string `json:"href,omitempty"`
}

// DashboardWidgetContributionPayload 用于 admin.dashboard.widgets（dashboardLink）。
// 仅允许管理端相对路由（以 / 开头，禁止外链与 /api）。
type DashboardWidgetContributionPayload struct {
	Type     string `json:"type"`
	Route    string `json:"route"`
	Severity string `json:"severity,omitempty"`
}

// HealthCheckContributionPayload 用于 system.health.checks（healthDescriptor）。
// 宿主在 /ready 中根据插件运行时状态贡献组件项；不在 ready 路径上调用插件 RPC。
// type=extensionRuntime：按该扩展 runtime.state 映射 ok/degraded/error。
// type=static：扩展已启用则固定 ok（仅声明「插件已加载」）。
type HealthCheckContributionPayload struct {
	Type      string `json:"type"`
	Component string `json:"component"`
	Required  bool   `json:"required,omitempty"`
}

// RegionPlacementContributionPayload 用于 forum.page.regions（regionPlacement）。
// 把内容放入宿主页面的标准区域（RegionCatalog 白名单的 page×region）。
// type=hostLink：站内相对路径链接卡；type=extensionRoute：可触发的扩展路由动作卡；
// type=l2Widget：引用本 manifest 内已声明的 L2 公开组件（componentId），
// 挂载仍走公开 L2 运行时的 descriptor 签发/信任/CSP 权威裁决，此处仅为放置声明。
type RegionPlacementContributionPayload struct {
	Type        string   `json:"type"`
	Pages       []string `json:"pages"`
	Region      string   `json:"region"`
	Method      string   `json:"method,omitempty"`
	Path        string   `json:"path,omitempty"`
	Href        string   `json:"href,omitempty"`
	ComponentID string   `json:"componentId,omitempty"`
}

const PayloadRegionPlacementL2Widget = "l2Widget"

type ContributionPointDefinition struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	PayloadType string `json:"payloadType"`
}

func ContributionPointDefinitions() []ContributionPointDefinition {
	return []ContributionPointDefinition{
		{ID: PointForumTopicActions, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Topic detail action descriptors rendered by the host UI.", PayloadType: PayloadTypeExtensionRoute},
		{ID: PointForumTopicSidebar, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Topic detail sidebar cards/links rendered by the host UI (extensionRoute or hostLink).", PayloadType: PayloadTypeTopicSidebarCard},
		{ID: PointForumTopicBadges, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Small status badges under the topic title (tone enum + optional hostLink).", PayloadType: PayloadTypeTopicBadge},
		{ID: PointForumCommentActions, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Comment row action descriptors rendered by the host UI (same extensionRoute spirit as topic actions).", PayloadType: PayloadTypeExtensionRoute},
		{ID: PointForumNavItems, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Extra public navbar entries (hostLink or public extensionRoute). Core/operator nav first; contributions secondary. No admin-only paths.", PayloadType: PayloadTypeNavItem},
		{ID: PointForumTopicListBadges, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "List-row badge descriptors for topic lists (same topicBadge shape as detail badges). List-level once; no per-row plugin RPC.", PayloadType: PayloadTypeTopicBadge},
		{ID: PointForumComposerToolbar, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Composer/editor toolbar actions rendered by the host UI; payload is an extensionRoute only.", PayloadType: PayloadTypeExtensionRoute},
		{ID: PointForumProfileTabs, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Public profile tabs/sections rendered by the host UI (extensionRoute or hostLink).", PayloadType: PayloadTypeProfileSection},
		{ID: PointAdminDashboardWidgets, Owner: "admin", Kind: ContributionPointKindDescriptor, Description: "Admin dashboard link widgets; host-owned routes only, no executable payloads.", PayloadType: PayloadTypeDashboardLink},
		{ID: PointSystemHealthChecks, Owner: "system", Kind: ContributionPointKindDescriptor, Description: "Plugin readiness components merged into GET /ready without invoking plugin RPC.", PayloadType: PayloadTypeHealthDescriptor},
		{ID: PointForumPageRegions, Owner: "forum", Kind: ContributionPointKindDescriptor, Description: "Standard page region placements (RegionCatalog whitelist): hostLink card, extensionRoute action card, or l2Widget reference resolved by the public L2 runtime.", PayloadType: PayloadTypeRegionPlacement},
	}
}

func Validate(manifest Manifest) error {
	return ValidateWithContributionPoints(manifest, ContributionPointDefinitions())
}

func validateManifest(manifest Manifest, points []ContributionPointDefinition) error {
	if err := validateManifestVersion(manifest); err != nil {
		return err
	}
	// langs 在 Normalize 丢弃空项前先校验，避免无效语言码被静默忽略。
	if err := validateManifestLangs(manifest.Langs); err != nil {
		return err
	}
	manifest = Normalize(manifest)
	if !manifestIDPattern.MatchString(manifest.ID) {
		return ErrInvalidManifest
	}
	if manifest.Name == "" || manifest.Description == "" || manifest.URL == "" || manifest.Author.Name == "" || manifest.Version == "" || manifest.SForumVersion == "" {
		return ErrInvalidManifest
	}
	// C1：Version 严格约束，防止路径穿越逃逸 extensionRoot。
	if !manifestVersionPattern.MatchString(manifest.Version) {
		return ErrInvalidManifest
	}
	if !validHTTPURL(manifest.URL) || (manifest.Author.URL != "" && !validHTTPURL(manifest.Author.URL)) {
		return ErrInvalidManifest
	}
	if manifest.Author.Email != "" {
		if _, err := mail.ParseAddress(manifest.Author.Email); err != nil {
			return ErrInvalidManifest
		}
	}
	if manifest.Type != TypePlugin && manifest.Type != TypeTheme {
		return ErrInvalidManifest
	}
	for _, setting := range manifest.Settings {
		// label 支持纯字符串或多语言 map，校验时只要求解析后非空。
		if setting.Key == "" || setting.Label.IsEmpty() || !supportedSettingType(setting.Type) || strings.Contains(setting.Key, " ") {
			return ErrInvalidManifest
		}
		if !supportedSettingWidth(setting.Width) {
			return ErrInvalidManifest
		}
		optionValues := make(map[string]struct{}, len(setting.Options))
		for _, option := range setting.Options {
			if option.Value == "" || option.Label.IsEmpty() {
				return ErrInvalidManifest
			}
			if _, exists := optionValues[option.Value]; exists {
				return ErrInvalidManifest
			}
			optionValues[option.Value] = struct{}{}
		}
		if setting.RecommendedValue != "" && len(optionValues) > 0 {
			if _, exists := optionValues[setting.RecommendedValue]; !exists {
				return ErrInvalidManifest
			}
		}
	}
	if err := validateSettingsDocument(manifest); err != nil {
		return err
	}
	if err := validateAdminDeclaration(manifest); err != nil {
		return err
	}
	if manifest.Type == TypeTheme && !isThemeManifestSupported(manifest) {
		return ErrInvalidManifest
	}
	if err := validateV3Manifest(manifest); err != nil {
		return err
	}
	// F2.1：capabilities 必须落在宿主目录内；主题禁止声明。
	if len(manifest.Capabilities) > 0 {
		if manifest.Type == TypeTheme {
			return ErrInvalidManifest
		}
		if err := capabilities.ValidateKeys(manifest.Capabilities); err != nil {
			return ErrInvalidManifest
		}
	}
	// F4.5：requiresFeatures 必须是 features.* 形式且主题禁止声明。
	if len(manifest.RequiresFeatures) > 0 {
		if manifest.Type == TypeTheme {
			return ErrInvalidManifest
		}
		seen := map[string]bool{}
		for _, name := range manifest.RequiresFeatures {
			name = strings.TrimSpace(name)
			if name == "" || !strings.HasPrefix(name, "features.") || strings.Contains(name, " ") {
				return ErrInvalidManifest
			}
			if seen[name] {
				return ErrInvalidManifest
			}
			seen[name] = true
		}
	}
	if err := validateContributions(manifest, points); err != nil {
		return err
	}
	return nil
}

func Normalize(manifest Manifest) Manifest {
	manifest.ID = NormalizeID(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.URL = strings.TrimSpace(manifest.URL)
	manifest.Author.Name = strings.TrimSpace(manifest.Author.Name)
	manifest.Author.URL = strings.TrimSpace(manifest.Author.URL)
	manifest.Author.Email = strings.TrimSpace(manifest.Author.Email)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Type = strings.ToLower(strings.TrimSpace(manifest.Type))
	manifest.SForumVersion = strings.TrimSpace(manifest.SForumVersion)
	manifest.Langs = normalizeManifestLangs(manifest.Langs)
	manifest.Capabilities = capabilities.NormalizeKeys(manifest.Capabilities)
	if len(manifest.RequiresFeatures) > 0 {
		normalized := make([]string, 0, len(manifest.RequiresFeatures))
		seen := map[string]bool{}
		for _, name := range manifest.RequiresFeatures {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			normalized = append(normalized, name)
		}
		manifest.RequiresFeatures = normalized
	}
	for index := range manifest.Settings {
		manifest.Settings[index].Key = strings.TrimSpace(manifest.Settings[index].Key)
		manifest.Settings[index].Label = manifest.Settings[index].Label.normalized()
		manifest.Settings[index].Description = manifest.Settings[index].Description.normalized()
		manifest.Settings[index].Type = strings.ToLower(strings.TrimSpace(manifest.Settings[index].Type))
		manifest.Settings[index].Default = strings.TrimSpace(manifest.Settings[index].Default)
		manifest.Settings[index].Placeholder = manifest.Settings[index].Placeholder.normalized()
		manifest.Settings[index].RecommendedValue = strings.TrimSpace(manifest.Settings[index].RecommendedValue)
		manifest.Settings[index].Width = normalizeSettingWidth(manifest.Settings[index].Width)
		manifest.Settings[index].Group = manifest.Settings[index].Group.normalized()
		manifest.Settings[index].GroupID = NormalizeID(manifest.Settings[index].GroupID)
		for optionIndex := range manifest.Settings[index].Options {
			option := &manifest.Settings[index].Options[optionIndex]
			option.Value = strings.TrimSpace(option.Value)
			option.Label = option.Label.normalized()
			option.Description = option.Description.normalized()
		}
	}
	normalizeSettingsDocument(&manifest)
	manifest.Backend.Entry = strings.TrimSpace(manifest.Backend.Entry)
	manifest.Backend.RPC = strings.TrimSpace(manifest.Backend.RPC)
	manifest.Admin.Entry = NormalizeRoutePath(manifest.Admin.Entry)
	normalizeAdminPageSlice(manifest.Admin.Pages)
	normalizeAdminPageSlice(manifest.AdminPages)
	for index := range manifest.Routes {
		manifest.Routes[index].Path = NormalizeRoutePath(manifest.Routes[index].Path)
		manifest.Routes[index].Access = strings.ToLower(strings.TrimSpace(manifest.Routes[index].Access))
		manifest.Routes[index].Permission = strings.TrimSpace(manifest.Routes[index].Permission)
		for methodIndex := range manifest.Routes[index].Methods {
			manifest.Routes[index].Methods[methodIndex] = strings.ToUpper(strings.TrimSpace(manifest.Routes[index].Methods[methodIndex]))
		}
	}
	for index := range manifest.Hooks {
		manifest.Hooks[index].Name = strings.TrimSpace(manifest.Hooks[index].Name)
	}
	for index := range manifest.Events {
		manifest.Events[index].ID = NormalizeID(manifest.Events[index].ID)
		manifest.Events[index].ContractVersion = strings.TrimSpace(manifest.Events[index].ContractVersion)
		manifest.Events[index].Name = strings.TrimSpace(manifest.Events[index].Name)
		manifest.Events[index].Kind = strings.ToLower(strings.TrimSpace(manifest.Events[index].Kind))
		manifest.Events[index].Handler = strings.TrimSpace(manifest.Events[index].Handler)
		manifest.Events[index].InputSchema = strings.TrimSpace(manifest.Events[index].InputSchema)
		manifest.Events[index].ResultSchema = strings.TrimSpace(manifest.Events[index].ResultSchema)
		if manifest.Events[index].Kind == "" {
			if definition, ok := appevents.FindDefinition(manifest.Events[index].Name); ok {
				manifest.Events[index].Kind = definition.Kind
			}
		}
	}
	for index := range manifest.Providers {
		manifest.Providers[index].Slot = strings.TrimSpace(manifest.Providers[index].Slot)
		manifest.Providers[index].Label = strings.TrimSpace(manifest.Providers[index].Label)
	}
	for index := range manifest.Contributions {
		manifest.Contributions[index] = normalizeContribution(manifest.Contributions[index])
	}
	normalizeV3Manifest(&manifest)
	return manifest
}

func supportedSettingType(value string) bool {
	switch value {
	case "text", "string", "number", "boolean", "select", "secret", "textarea":
		return true
	default:
		return false
	}
}

// SettingWidthDefault / SettingWidthFull 是 Schema UI 控件宽度。
const (
	SettingWidthDefault = "default"
	SettingWidthFull    = "full"
)

func normalizeSettingWidth(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return SettingWidthDefault
	}
	return value
}

func supportedSettingWidth(value string) bool {
	switch normalizeSettingWidth(value) {
	case SettingWidthDefault, SettingWidthFull:
		return true
	default:
		return false
	}
}

func NormalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// ValidExtensionID reports whether value satisfies the manifest ID rule. Host
// routes that accept a caller-declared extension identity reuse this instead of
// re-implementing the pattern.
func ValidExtensionID(value string) bool {
	return manifestIDPattern.MatchString(value)
}

func DeclaredEvents(manifest Manifest) []ManifestEvent {
	items := []ManifestEvent{}
	seen := map[string]bool{}
	for _, event := range manifest.Events {
		name := strings.TrimSpace(event.Name)
		if name == "" {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(event.Kind))
		if kind == "" {
			if definition, ok := appevents.FindDefinition(name); ok {
				kind = definition.Kind
			}
		}
		key := name + ":" + kind
		if seen[key] {
			continue
		}
		seen[key] = true
		event.Name = name
		event.Kind = kind
		items = append(items, event)
	}
	// 旧 hooks 字段作为 events 的兼容别名保留，统一转换给运行时消费。
	for _, hook := range manifest.Hooks {
		name := strings.TrimSpace(hook.Name)
		if name == "" {
			continue
		}
		definition, ok := appevents.FindDefinition(name)
		if !ok {
			continue
		}
		key := name + ":" + definition.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, ManifestEvent{Name: name, Kind: definition.Kind})
	}
	return items
}

func isThemeManifestSupported(manifest Manifest) bool {
	// 主题的公开页面贡献只来自 theme.json；管理端设置使用 Settings Document。
	if manifest.Backend != (ManifestBackend{}) ||
		len(manifest.Permissions) != 0 ||
		len(manifest.Capabilities) != 0 ||
		len(manifest.Migrations) != 0 ||
		len(manifest.Routes) != 0 ||
		len(manifest.Hooks) != 0 ||
		len(manifest.Events) != 0 ||
		len(manifest.Jobs) != 0 ||
		len(manifest.Providers) != 0 ||
		len(manifest.Contributions) != 0 {
		return false
	}
	if len(manifest.Guards) != 0 || len(manifest.Schedules) != 0 ||
		len(manifest.Content) != 0 || len(manifest.Editor) != 0 || len(manifest.Entities) != 0 ||
		manifest.Database != nil || len(manifest.Cache) != 0 || len(manifest.SEO) != 0 ||
		len(manifest.Services) != 0 || len(manifest.Commands) != 0 ||
		len(manifest.AdminSurfaces) != 0 || len(manifest.Queries) != 0 || len(manifest.QueryResultFilters) != 0 ||
		manifest.Identity != nil || len(manifest.PermissionDefinitions) != 0 ||
		len(manifest.Media) != 0 || manifest.Lifecycle != nil || len(manifest.OpenAPI) != 0 {
		return false
	}
	return true
}

// CapabilityResolveInput 将 manifest 转为 capabilities 解析输入（F2.1）。
func CapabilityResolveInput(manifest Manifest) capabilities.ResolveInput {
	slots := make([]string, 0, len(manifest.Providers))
	for _, provider := range manifest.Providers {
		slots = append(slots, provider.Slot)
	}
	return capabilities.ResolveInput{
		Explicit:      manifest.Capabilities,
		HasJobs:       len(manifest.Jobs) > 0,
		HasSettings:   len(manifest.Settings) > 0,
		ProviderSlots: slots,
		HasBackend:    strings.TrimSpace(manifest.Backend.Entry) != "",
	}
}

// ResolvedCapabilities 返回有效能力 key 与 implied 标记。
func ResolvedCapabilities(manifest Manifest) (keys []string, implied map[string]bool) {
	return capabilities.Resolve(CapabilityResolveInput(manifest))
}

// CapabilityGrants 返回启用审查用的能力列表。
func CapabilityGrants(manifest Manifest) []capabilities.Grant {
	return capabilities.GrantsFor(CapabilityResolveInput(manifest))
}

func validHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}
