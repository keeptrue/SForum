package pagescontroller

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin"
)

// Pages 控制器的传输层类型与响应辅助：页面解析响应的序列化形状、失败原因常量，
// 以及只被该响应路径使用的布局映射与缓存头策略。

type resolveResponse struct {
	Page           pages.PageDefinition `json:"page"`
	Provider       string               `json:"provider"`
	ExtensionID    string               `json:"extensionId,omitempty"`
	ContributionID string               `json:"contributionId,omitempty"`
	Action         string               `json:"action"`
	// Layout: site | bare。前端据此决定是否由宿主提供完整公共外壳（恰好一个 navbar/footer）。
	// 空值按 bare 处理：给已有精确制品追溯性加 chrome 是更危险的方向。
	Layout string `json:"layout,omitempty"`
	// 主题外壳：仅 layout=site 时尝试填充。空值表示回落到宿主公共外壳。
	// 同时给出主题身份，使外壳可归因到精确的主题制品。
	ShellHTML                 string `json:"shellHtml,omitempty"`
	ShellProvider             string `json:"shellProvider,omitempty"`
	ShellVersion              string `json:"shellVersion,omitempty"`
	ShellPackageDigest        string `json:"shellPackageDigest,omitempty"`
	Fallback                  bool   `json:"fallback"`
	Reason                    string `json:"reason,omitempty"`
	SelectedProvider          string `json:"selectedProvider,omitempty"`
	SelectedExtensionID       string `json:"selectedExtensionId,omitempty"`
	SelectedContributionID    string `json:"selectedContributionId,omitempty"`
	SelectedVersion           string `json:"selectedVersion,omitempty"`
	SelectedPackageDigest     string `json:"selectedPackageDigest,omitempty"`
	SelectedRuntimeInstanceID string `json:"selectedRuntimeInstanceId,omitempty"`
	NodeRevision              uint64 `json:"nodeRevision,omitempty"`
	TemplatePath              string `json:"templatePath,omitempty"`
	TemplateHTML              string `json:"templateHtml,omitempty"`
	// Label 页面展示标题（可选）：来自贡献声明，前端在模板无 h1 时用它做 document.title 兜底。
	Label string `json:"label,omitempty"`
	// View: template | component。component 时 Component 为包内公开 L2 组件 id，
	// 正文由 Web 端经既有公开前端描述符端点挂载（TemplateHTML 为空是正常状态）。
	View      string `json:"view,omitempty"`
	Component string `json:"component,omitempty"`
	// SchemaView: view=schema 时的有界页面描述符（list|detail|form|notice）。
	// 与预检共用同一次读取（sdk/plugin.LoadSchemaViewDescriptor），因此不会出现
	// 「校验通过、返回另一种形状」。
	SchemaView   map[string]any           `json:"schemaView,omitempty"`
	DataSource   string                   `json:"dataSource,omitempty"`
	DataRoute    string                   `json:"dataRoute,omitempty"`
	RouteParams  map[string]string        `json:"routeParams,omitempty"`
	LoaderData   any                      `json:"loaderData,omitempty"`
	LoaderError  string                   `json:"loaderError,omitempty"`
	Contract     string                   `json:"contractVersion,omitempty"`
	RenderOutput *pages.ThemeRenderedPage `json:"renderOutput,omitempty"`
}

const (
	resolveReasonAuthoritativeCore    = "authoritative_core"
	resolveReasonViewModelUnavailable = "view_model_unavailable"
	resolveReasonRenderFailed         = "render_failed"
	resolveReasonArtifactMismatch     = "artifact_mismatch"
	resolveReasonRuntimeUnavailable   = "runtime_unavailable"
)

// resolvedLayout 归一化贡献的 layout。
// 空值走平台默认（site）；未知值回退 bare——安装预检已保证未知值不会到达这里，
// 真出现了说明有绕过准入的路径，此时不额外添加 chrome 是更保守的方向。
func resolvedLayout(layout pages.PageLayout) pages.PageLayout {
	normalized, err := pages.NormalizeLayout(string(layout))
	if err != nil {
		return pages.LayoutBare
	}
	return normalized
}

func setPrivatePageResponse(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Vary(fiber.HeaderCookie, fiber.HeaderAuthorization, fiber.HeaderAcceptLanguage)
}

// resolveThemeShell 载入选中主题声明的 L1 外壳模板及其精确制品身份。
//
// 任何一步失败都返回空值：外壳是可选的呈现能力，坏掉或缺失的外壳必须回落到
// 宿主公共外壳，而不是让插件页渲染成残缺页面。因此这里 fail-open 到宿主外壳，
// 与准入期的 fail-closed 预检形成互补——坏外壳进不了门，进门后坏了也不会毁页面。
func (h *Controller) resolveThemeShell(c fiber.Ctx, vars map[string]string) (html, provider, version, digest string) {
	if h == nil || h.themes == nil {
		return "", "", "", ""
	}
	active, err := h.themes.ActiveTheme(c.Context())
	if err != nil || strings.TrimSpace(active.ID) == "" {
		return "", "", "", ""
	}
	root := extensions.PackageContentRoot(active)
	if root == "" {
		return "", "", "", ""
	}
	pkg, err := pages.LoadThemePackage(root)
	if err != nil || strings.TrimSpace(pkg.Shell) == "" {
		return "", "", "", ""
	}
	raw, err := pages.LoadTemplate(root, pkg.Shell)
	if err != nil {
		return "", "", "", ""
	}
	rendered, err := pages.RenderTemplate(raw, vars)
	if err != nil {
		return "", "", "", ""
	}
	return rendered, active.ID, active.Version, active.PackageDigest
}

// applyThemeShell 为 site 布局装配主题外壳；其余布局与任何失败都留空，
// 由前端回落到宿主公共外壳。
func (h *Controller) applyThemeShell(c fiber.Ctx, resp *resolveResponse, params map[string]string, locale string) {
	resp.ShellHTML, resp.ShellProvider, resp.ShellVersion, resp.ShellPackageDigest = "", "", "", ""
	if resp.Layout != string(pages.LayoutSite) {
		return
	}
	vars := map[string]string{"locale": locale}
	for k, v := range params {
		vars[k] = v
	}
	resp.ShellHTML, resp.ShellProvider, resp.ShellVersion, resp.ShellPackageDigest = h.resolveThemeShell(c, vars)
}

// externalProvider 计算对外暴露的 provider/action：回退时显示 core，
// 避免前台把失败制品当成生效提供者（模板与身份字段同样保留，便于归因）。
func externalProvider(resolved pages.ResolvedPage) (string, string) {
	provider := resolved.Provider
	action := resolved.Action
	if resolved.Fallback {
		provider = pages.ProviderCore
		if action == "" {
			action = "core"
		}
	}
	return provider, action
}

// addedPageResponse 由 add 贡献构造解析响应：add 页没有管理员绑定，
// 目录条目、精确身份与正文来源（view/component）全部来自该贡献本身。
func (h *Controller) addedPageResponse(contrib pages.PageContribution, params map[string]string) resolveResponse {
	return resolveResponse{
		SchemaView: h.schemaView(
			string(contrib.View), contrib.ExtensionID, contrib.SchemaDescriptorFile, contrib.SchemaContractRef,
		),
		Page: pages.PageDefinition{
			ID:              contrib.ID,
			PathPattern:     contrib.Path,
			Access:          contrib.Access,
			ContractVersion: contrib.Contract,
			Replaceable:     false,
		},
		Provider:                  contrib.ExtensionID,
		ExtensionID:               contrib.ExtensionID,
		ContributionID:            contrib.ID,
		Action:                    string(pages.ActionAdd),
		Layout:                    string(resolvedLayout(contrib.Layout)),
		Fallback:                  false,
		SelectedProvider:          contrib.ExtensionID,
		SelectedExtensionID:       contrib.ExtensionID,
		SelectedContributionID:    contrib.ID,
		SelectedVersion:           contrib.Version,
		SelectedPackageDigest:     contrib.PackageDigest,
		SelectedRuntimeInstanceID: contrib.RuntimeInstanceID,
		TemplatePath:              contrib.Template,
		Label:                     contrib.Label,
		View:                      string(pages.NormalizePageView(string(contrib.View))),
		Component:                 contrib.ComponentID,
		DataSource:                contrib.DataSource,
		DataRoute:                 contrib.DataRoute,
		RouteParams:               params,
		Contract:                  contrib.Contract,
	}
}

// resolvePageResponse 由已解析页面构造响应。
// 从 controller.go 抽出：路由壳只保留准入与回落决策，传输形状集中在这里，
// 也让 view=schema 的描述符装载有唯一的落点。
func (h *Controller) resolvePageResponse(
	resolved pages.ResolvedPage,
	reason string,
	routeParams map[string]string,
	runtimeOutput *pages.ThemeRenderedPage,
	loaderData any,
	loaderError string,
) resolveResponse {
	provider, action := externalProvider(resolved)
	return resolveResponse{
		Page:                      resolved.Page,
		Provider:                  provider,
		ExtensionID:               resolved.ExtensionID,
		ContributionID:            resolved.ContributionID,
		Action:                    action,
		Fallback:                  resolved.Fallback,
		Reason:                    reason,
		View:                      resolved.View,
		Component:                 resolved.Component,
		SchemaView:                h.schemaViewDescriptor(resolved),
		SelectedProvider:          selectedProvider(resolved),
		SelectedExtensionID:       resolved.ExtensionID,
		SelectedContributionID:    resolved.ContributionID,
		SelectedVersion:           resolved.Version,
		SelectedPackageDigest:     resolved.PackageDigest,
		SelectedRuntimeInstanceID: resolved.RuntimeInstanceID,
		TemplatePath:              resolved.TemplatePath,
		TemplateHTML:              resolved.TemplateHTML,
		Label:                     resolved.Label,
		DataSource:                resolved.DataSource,
		DataRoute:                 resolved.DataRoute,
		RouteParams:               routeParams,
		Contract:                  resolved.Page.ContractVersion,
		RenderOutput:              runtimeOutput,
		LoaderData:                loaderData,
		LoaderError:               loaderError,
	}
}

// schemaViewDescriptor 读取 view=schema 页面的有界描述符。
//
// 预检已保证包内描述符满足契约与有界规则；这里只是把同一份内容交给 Web 渲染。
// 读取失败（包被替换、制品漂移）时返回 nil：正文回落为空状态而不是渲染残缺页面，
// 与外壳的 fail-open 方向一致。
func (h *Controller) schemaViewDescriptor(resolved pages.ResolvedPage) map[string]any {
	extensionID := resolved.ExtensionID
	if extensionID == "" {
		extensionID = resolved.Provider
	}
	return h.schemaView(
		resolved.View, extensionID, resolved.SchemaDescriptorFile, resolved.SchemaContractRef,
	)
}

// schemaView 是描述符装载的唯一实现：绑定路径与 add 路径共用，
// 因此两条路径返回的形状不会分叉。
func (h *Controller) schemaView(
	view string,
	extensionID string,
	descriptorFile string,
	contractRef string,
) map[string]any {
	if pages.NormalizePageView(view) != pages.PageViewSchema || h.themes == nil {
		return nil
	}
	if strings.TrimSpace(descriptorFile) == "" || strings.TrimSpace(contractRef) == "" {
		return nil
	}
	extension, err := h.themes.Get(context.Background(), extensionID)
	if err != nil {
		return nil
	}
	descriptor, err := pluginsdk.LoadSchemaViewDescriptor(
		extensions.PackageContentRoot(extension), extension.Manifest, descriptorFile, contractRef,
	)
	if err != nil {
		return nil
	}
	return descriptor
}

// responseBodyProvidedByView 判断 add 路径的响应是否已由包内声明的 view 自带正文。
func responseBodyProvidedByView(resp resolveResponse) bool {
	if strings.TrimSpace(resp.Component) != "" {
		return true
	}
	return pages.NormalizePageView(resp.View) == pages.PageViewSchema && resp.SchemaView != nil
}

// pageBodyProvidedByView 判断正文是否由包内声明的 view 自带。
// component（公开 L2 组件）与 schema（有界描述符）都不依赖 L1 模板，
// 因此不适用「无模板即运行时不可用」的回落判定。
func pageBodyProvidedByView(resolved pages.ResolvedPage) bool {
	if strings.TrimSpace(resolved.Component) != "" {
		return true
	}
	return pages.NormalizePageView(resolved.View) == pages.PageViewSchema &&
		strings.TrimSpace(resolved.SchemaDescriptorFile) != ""
}
