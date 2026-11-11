// Package pages 是宿主 Page Registry 的权威目录与解析入口。
// 每个核心公开页有稳定 id；主题/插件通过贡献替换或新增视图，不能覆盖核心 API。
package pages

import (
	"fmt"
	"sort"
	"strings"
)

// Access 描述页面访问类别（与 manifest route access 对齐，便于 L1 贡献校验）。
// 未知非空值必须在安装/启用预检时失败（fail closed），不得按 public 放行。
type Access string

const (
	AccessPublic     Access = "public"
	AccessLogin      Access = "login"
	AccessGuest      Access = "guest"
	AccessModeration Access = "moderation"
	AccessPermission Access = "permission"
)

// PageLayout 描述公开页是否由宿主提供完整公共外壳（选中主题外壳，缺失时宿主回退）。
//
// 默认值是 site：公开页共享同一几何契约（AGENTS.md），而插件公开页此前是全站唯一
// 没有 navbar/footer 的页面。曾经的顾虑是「追溯性加 chrome 可能造成双层导航」，
// 但实测不成立——全仓 5 个插件页模板无一挂载 sf-navbar/sf-footer（对照：主题模板
// 34 个全部挂载），因此没有任何现有制品会被双层 chrome。外壳属于呈现层，平台本来
// 就在切主题时重渲染公开页呈现而不重新批准插件制品。
//
// 作者想要自己拥有 chrome（例如自行挂载导航岛）时显式声明 bare。
type PageLayout string

const (
	LayoutSite PageLayout = "site"
	LayoutBare PageLayout = "bare"
)

// NormalizeLayout 空值 → site（平台默认，与其他公开页一致）；
// 仅允许已知枚举，未知非空值 fail closed（安装/启用预检即失败）。
func NormalizeLayout(raw string) (PageLayout, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return LayoutSite, nil
	}
	switch PageLayout(v) {
	case LayoutSite, LayoutBare:
		return PageLayout(v), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidLayout, raw)
	}
}

// ValidLayout 是否为已规范化的合法 layout。
func ValidLayout(l PageLayout) bool {
	_, err := NormalizeLayout(string(l))
	return err == nil
}

// NormalizeAccess 空值 → public；仅允许已知枚举。
func NormalizeAccess(raw string) (Access, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return AccessPublic, nil
	}
	switch Access(v) {
	case AccessPublic, AccessLogin, AccessGuest, AccessModeration, AccessPermission:
		return Access(v), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidAccess, raw)
	}
}

// ValidAccess 是否为已规范化的合法 access。
func ValidAccess(a Access) bool {
	_, err := NormalizeAccess(string(a))
	return err == nil
}

// ProviderCore 始终是内置回退提供者。
const ProviderCore = "core"

// PageDefinition 是目录中的一条核心页面定义。
type PageDefinition struct {
	ID               string   `json:"id"`
	PathPattern      string   `json:"pathPattern"`
	Access           Access   `json:"access"`
	ContractVersion  string   `json:"contractVersion"`
	CoreComponent    string   `json:"coreComponent"`
	Replaceable      bool     `json:"replaceable"`
	Themeable        bool     `json:"themeable"`
	Virtual          bool     `json:"virtual"`
	RequiresFeatures []string `json:"requiresFeatures,omitempty"`
	Notes            string   `json:"notes,omitempty"`
}

// ResolvedPage 是解析结果：目录条目 + 当前生效的提供者。
type ResolvedPage struct {
	Page           PageDefinition `json:"page"`
	Provider       string         `json:"provider"`
	ExtensionID    string         `json:"extensionId,omitempty"`
	ContributionID string         `json:"contributionId,omitempty"`
	Action         string         `json:"action,omitempty"` // core | replace | add
	Fallback       bool           `json:"fallback"`
	// L1 渲染载荷（provider 非 core 时由 HTTP 层填充 HTML）。
	TemplatePath string `json:"templatePath,omitempty"`
	TemplateHTML string `json:"templateHtml,omitempty"`
	// View 与 Component 描述正文的提供方式：view=component 时 Component 为包内公开
	// L2 组件 id，正文由 Web 端经既有公开前端描述符端点挂载（TemplateHTML 为空）。
	View      string `json:"view,omitempty"`
	Component string `json:"component,omitempty"`
	// Label 页面展示标题（可选）：来自贡献声明，前端在模板无 h1 时用它做 document.title 兜底。
	Label string `json:"label,omitempty"`
	// SchemaDescriptorFile / SchemaContractRef：view=schema 的正文来源（包内描述符文件与其契约引用）。
	SchemaDescriptorFile string `json:"schemaDescriptorFile,omitempty"`
	SchemaContractRef    string `json:"schemaContractRef,omitempty"`
	DataSource           string `json:"dataSource,omitempty"`
	DataRoute            string `json:"dataRoute,omitempty"`
	// DataSchema 为包内相对路径；replace 必须带入 ResolvedPage，否则 LoadForResolved 会丢 schema 校验。
	DataSchema        string `json:"dataSchema,omitempty"`
	Version           string `json:"version,omitempty"`
	PackageDigest     string `json:"packageDigest,omitempty"`
	RuntimeInstanceID string `json:"runtimeInstanceId,omitempty"`
	PackageRoot       string `json:"-"` // 仅服务端内部使用，不序列化
}

var coreCatalog = []PageDefinition{
	{ID: "forum.home", PathPattern: "/", Access: AccessPublic, ContractVersion: "sforum.page.home@1", CoreComponent: "pages/index", Replaceable: true},
	{ID: "forum.search", PathPattern: "/search", Access: AccessPublic, ContractVersion: "sforum.page.search@1", CoreComponent: "pages/search", Replaceable: true, RequiresFeatures: []string{"features.search"}},
	{ID: "forum.category.index", PathPattern: "/categories", Access: AccessPublic, ContractVersion: "sforum.page.category_index@1", CoreComponent: "pages/categories/index", Replaceable: true},
	{ID: "forum.category.show", PathPattern: "/c/:categorySlug", Access: AccessPublic, ContractVersion: "sforum.page.category_show@1", CoreComponent: "pages/c/[categorySlug]", Replaceable: true},
	{ID: "forum.tag.index", PathPattern: "/tags", Access: AccessPublic, ContractVersion: "sforum.page.tag_index@1", CoreComponent: "pages/tags/index", Replaceable: true, Notes: "gated by forum.tags.public_pages"},
	{ID: "forum.tag.show", PathPattern: "/tags/:tagSlug", Access: AccessPublic, ContractVersion: "sforum.page.tag_show@1", CoreComponent: "pages/tags/[tagSlug]", Replaceable: true, Notes: "gated by forum.tags.public_pages"},
	// 公开徽章目录：全部启用徽章 + 查看者自己的 earned。必须写成**一整行**——
	// scripts/v3-catalog/generate.mjs 用逐行正则解析本段，拆行会让 public-page-surfaces.json
	// 与源码「一致地错」，--check 仍绿（B-30）。
	{ID: "forum.badge.index", PathPattern: "/badges", Access: AccessPublic, ContractVersion: "sforum.page.badge_index@1", CoreComponent: "pages/badges/index", Replaceable: true, RequiresFeatures: []string{"features.badges"}},
	{ID: "forum.topic.show", PathPattern: "/t/:path(.*)", Access: AccessPublic, ContractVersion: "sforum.page.topic_show@1", CoreComponent: "pages/t/[...path]", Replaceable: true, Notes: "seo.topic_url_mode"},
	{ID: "forum.topic.create", PathPattern: "/topics/new", Access: AccessLogin, ContractVersion: "sforum.page.topic_create@1", CoreComponent: "pages/topics/new", Replaceable: true},
	// 高级回复：完整编辑器独立页；query topic / parent 由 Host 岛读取。
	{ID: "forum.topic.reply", PathPattern: "/topics/reply", Access: AccessLogin, ContractVersion: "sforum.page.topic_reply@1", CoreComponent: "pages/topics/reply", Replaceable: true},
	// 主题编辑独立页：按 id 定位（编辑可能改 slug，slug URL 会自失效）；权限校验由岛与 API 负责。
	{ID: "forum.topic.edit", PathPattern: "/topics/:topicId/edit", Access: AccessLogin, ContractVersion: "sforum.page.topic_edit@1", CoreComponent: "pages/topics/[topicId]/edit", Replaceable: true},
	{ID: "forum.profile.show", PathPattern: "/u/:username", Access: AccessPublic, ContractVersion: "sforum.page.profile_show@1", CoreComponent: "pages/u/[username]", Replaceable: true, RequiresFeatures: []string{"features.public_profiles"}},
	{ID: "forum.settings.profile", PathPattern: "/settings/profile", Access: AccessLogin, ContractVersion: "sforum.page.settings_profile@1", CoreComponent: "pages/settings/profile", Replaceable: true},
	{ID: "forum.settings.appearance", PathPattern: "/settings/appearance", Access: AccessLogin, ContractVersion: "sforum.page.settings_appearance@1", CoreComponent: "pages/settings/appearance", Replaceable: true},
	{ID: "forum.settings.login_methods", PathPattern: "/settings/security/login-methods", Access: AccessLogin, ContractVersion: "sforum.page.settings_login_methods@1", CoreComponent: "pages/settings/security/login-methods", Replaceable: true},
	{ID: "forum.settings.password", PathPattern: "/settings/security/password", Access: AccessLogin, ContractVersion: "sforum.page.settings_password@1", CoreComponent: "pages/settings/security/password", Replaceable: true},
	{ID: "forum.settings.security", PathPattern: "/settings/security", Access: AccessLogin, ContractVersion: "sforum.page.settings_security@1", CoreComponent: "pages/settings/security/index", Replaceable: true},
	{ID: "forum.settings.tokens", PathPattern: "/settings/tokens", Access: AccessLogin, ContractVersion: "sforum.page.settings_tokens@1", CoreComponent: "pages/settings/tokens", Replaceable: true},
	{ID: "forum.settings.notifications", PathPattern: "/settings/notifications", Access: AccessLogin, ContractVersion: "sforum.page.settings_notifications@1", CoreComponent: "pages/settings/notifications", Replaceable: true},
	{ID: "forum.notifications", PathPattern: "/notifications", Access: AccessLogin, ContractVersion: "sforum.page.notifications@1", CoreComponent: "pages/notifications/index", Replaceable: true},
	{ID: "forum.notification.show", PathPattern: "/notifications/:notificationId", Access: AccessLogin, ContractVersion: "sforum.page.notification_show@1", CoreComponent: "pages/notifications/[notificationId]", Replaceable: true},
	{ID: "moderation.review", PathPattern: "/moderation", Access: AccessModeration, ContractVersion: "sforum.page.moderation_review@1", CoreComponent: "pages/moderation/index", Replaceable: false, Themeable: true, Notes: "theme may place the Host-owned moderation island; plugins cannot replace the workbench"},
	{ID: "auth.login", PathPattern: "/login", Access: AccessGuest, ContractVersion: "sforum.page.login@1", CoreComponent: "pages/login", Replaceable: true, Notes: "replace must embed host login form island"},
	{ID: "auth.register", PathPattern: "/register", Access: AccessGuest, ContractVersion: "sforum.page.register@1", CoreComponent: "pages/register", Replaceable: true, RequiresFeatures: []string{"features.registration"}},
	{ID: "auth.external_continuation", PathPattern: "/auth/continue", Access: AccessPublic, ContractVersion: "sforum.page.external_auth_continuation@1", CoreComponent: "pages/auth/continue", Replaceable: true, Notes: "replace must embed host external-auth continuation island"},
	{ID: "auth.email_verification", PathPattern: "/email-verification", Access: AccessLogin, ContractVersion: "sforum.page.email_verification@1", CoreComponent: "pages/email-verification", Replaceable: true, Notes: "replace must embed host email-verification island"},
	{ID: "auth.forgot_password", PathPattern: "/forgot-password", Access: AccessPublic, ContractVersion: "sforum.page.forgot_password@1", CoreComponent: "pages/forgot-password", Replaceable: true},
	{ID: "auth.reset_password", PathPattern: "/reset-password", Access: AccessPublic, ContractVersion: "sforum.page.reset_password@1", CoreComponent: "pages/reset-password", Replaceable: true},
	{ID: "site.terms", PathPattern: "/terms", Access: AccessPublic, ContractVersion: "sforum.page.terms@1", CoreComponent: "pages/terms", Replaceable: true},
	{ID: "site.privacy", PathPattern: "/privacy", Access: AccessPublic, ContractVersion: "sforum.page.privacy@1", CoreComponent: "pages/privacy", Replaceable: true},
	{ID: "site.guidelines", PathPattern: "/guidelines", Access: AccessPublic, ContractVersion: "sforum.page.guidelines@1", CoreComponent: "pages/guidelines", Replaceable: true},
	// 站外链接中转页：重启的安全面，必须由宿主固定提供。不可替换 ⇒ 也不可主题化
	// （normalizePageDefinition 让 Replaceable 蕴含 Themeable），主题与插件都无法摘掉
	// 目的地提示；呈现仍走 SFHostPublicChrome，与公开页几何一致。
	{ID: "site.outbound", PathPattern: "/outbound", Access: AccessPublic, ContractVersion: "sforum.page.outbound@1", CoreComponent: "pages/outbound", Replaceable: false, Notes: "host-owned 站外链接中转页；access 恒为 public，判定在点击时进行"},
	{ID: "system.forbidden", PathPattern: "", Access: AccessPublic, ContractVersion: "sforum.page.forbidden@1", CoreComponent: "error", Replaceable: true, Virtual: true},
	{ID: "system.not_found", PathPattern: "", Access: AccessPublic, ContractVersion: "sforum.page.not_found@1", CoreComponent: "error", Replaceable: true, Virtual: true},
	{ID: "system.rate_limited", PathPattern: "", Access: AccessPublic, ContractVersion: "sforum.page.rate_limited@1", CoreComponent: "error", Replaceable: true, Virtual: true},
	{ID: "system.server_error", PathPattern: "", Access: AccessPublic, ContractVersion: "sforum.page.server_error@1", CoreComponent: "error", Replaceable: true, Virtual: true},
	{ID: "dev.components", PathPattern: "/components", Access: AccessPublic, ContractVersion: "sforum.page.dev_components@1", CoreComponent: "pages/components", Replaceable: false, Notes: "dev gallery"},
}

// Catalog 返回核心页面目录的稳定副本（按 id 排序）。
func Catalog() []PageDefinition {
	out := make([]PageDefinition, len(coreCatalog))
	copy(out, coreCatalog)
	for index := range out {
		out[index] = normalizePageDefinition(out[index])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Find 按 id 查找核心页面定义。
func Find(id string) (PageDefinition, bool) {
	id = strings.TrimSpace(id)
	for _, page := range coreCatalog {
		if page.ID == id {
			return normalizePageDefinition(page), true
		}
	}
	return PageDefinition{}, false
}

// Replaceable 页面保持既有主题能力；Themeable 可单独开放受限 L1 壳层，
// 而不允许插件接管 Core 页面行为。
func normalizePageDefinition(page PageDefinition) PageDefinition {
	page.Themeable = page.Themeable || page.Replaceable
	return page
}

// IsSystemErrorPage reports whether a page is a Host-selected virtual error surface.
func IsSystemErrorPage(id string) bool {
	page, ok := Find(strings.TrimSpace(id))
	return ok && page.Virtual && strings.HasPrefix(page.ID, "system.")
}

// ValidateCatalog 检查目录不变量（启动与单测使用）。
func ValidateCatalog() error {
	seenID := map[string]struct{}{}
	seenPath := map[string]struct{}{}
	for _, page := range coreCatalog {
		if page.ID == "" {
			return fmt.Errorf("pages: empty page id")
		}
		if _, ok := seenID[page.ID]; ok {
			return fmt.Errorf("pages: duplicate id %q", page.ID)
		}
		seenID[page.ID] = struct{}{}
		if page.PathPattern != "" {
			if _, ok := seenPath[page.PathPattern]; ok {
				return fmt.Errorf("pages: duplicate path %q", page.PathPattern)
			}
			seenPath[page.PathPattern] = struct{}{}
			if isReservedPath(page.PathPattern) {
				return fmt.Errorf("pages: core path collides with reserved prefix %q", page.PathPattern)
			}
		}
		if page.ContractVersion == "" || page.CoreComponent == "" {
			return fmt.Errorf("pages: %s missing contract or core component", page.ID)
		}
	}
	return nil
}

// ResolveCore 在无扩展贡献时返回始终-core 的解析结果。
func ResolveCore(id string) (ResolvedPage, error) {
	page, ok := Find(id)
	if !ok {
		return ResolvedPage{}, fmt.Errorf("pages: unknown page id %q", id)
	}
	return ResolvedPage{
		Page:     page,
		Provider: ProviderCore,
		Action:   "core",
		Fallback: false,
	}, nil
}

// MatchPath 在去掉可选 locale 前缀后，按目录 path pattern 做简单匹配。
// 仅用于管理/调试列表；请求路由仍由 Nuxt 文件路由负责。
func MatchPath(requestPath string) (PageDefinition, bool) {
	path := stripLocalePrefix(requestPath)
	if path == "" {
		path = "/"
	}
	for _, page := range coreCatalog {
		if page.PathPattern == "" {
			continue
		}
		if pathMatches(page.PathPattern, path) {
			return page, true
		}
	}
	return PageDefinition{}, false
}

func stripLocalePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// 仅剥离 /en 或 /en-US 这类前缀；默认 locale 无前缀。
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) == 0 {
		return "/"
	}
	first := parts[0]
	if looksLikeLocale(first) {
		rest := strings.Join(parts[1:], "/")
		if rest == "" {
			return "/"
		}
		return "/" + rest
	}
	return p
}

func looksLikeLocale(s string) bool {
	// en, en-US, zh-CN
	if len(s) == 2 {
		return s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
	}
	if len(s) == 5 && s[2] == '-' {
		return looksLikeLocale(s[:2]) && s[3] >= 'A' && s[3] <= 'Z' && s[4] >= 'A' && s[4] <= 'Z'
	}
	return false
}

func pathMatches(pattern, path string) bool {
	if pattern == path {
		return true
	}
	// 简化：:param 与 catch-all 分段匹配
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if pattern == "/" {
		return path == "/"
	}
	if strings.Contains(pattern, "(.*)") || strings.Contains(pattern, "...") {
		// catch-all：前缀段相等即可
		if len(pp) == 0 {
			return true
		}
		prefix := pp[0]
		if strings.HasPrefix(prefix, ":") {
			return len(ps) >= 1
		}
		return len(ps) >= 1 && ps[0] == prefix
	}
	if len(pp) != len(ps) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], ":") {
			continue
		}
		if pp[i] != ps[i] {
			return false
		}
	}
	return true
}
