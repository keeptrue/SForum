package pagescontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	pageviewmodels "github.com/keeptrue/sforum/apps/api/app/Models/PageViewModels"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	regioncatalog "github.com/keeptrue/sforum/apps/api/app/Support/RegionCatalog"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// ThemePackageStore 页面解析所需的扩展包最小接口（避免测试实现完整 Store）。
type ThemePackageStore interface {
	Get(ctx context.Context, id string) (extensions.Extension, error)
	ActiveTheme(ctx context.Context) (extensions.Extension, error)
}

type pageViewerStore interface {
	GetCurrentUser(context.Context, int64) (identity.CurrentUser, error)
}

// PageRegionSource 解析某页面标准区域的插件贡献内容(forum.page.regions)。
type PageRegionSource interface {
	PageRegions(ctx context.Context, pageID string) ([]sitechrome.PageRegionViewModel, error)
}

// Controller 暴露 Page Registry 管理与公开解析 API。
type Controller struct {
	registry *pages.Registry
	users    identity.ActorStore
	sessions *authsession.Manager
	themes   ThemePackageStore
	auditor  audit.Writer
	loader   *pages.LoaderGateway
	runtime  *pages.ThemeRuntimeRegistry
	coreData *pageviewmodels.CorePageViewModelSource
	regions  PageRegionSource
}

// WithPageRegions 注入区域贡献解析源(forum.page.regions)。
func (h *Controller) WithPageRegions(source PageRegionSource) *Controller {
	if h != nil {
		h.regions = source
	}
	return h
}

func (h *Controller) WithThemeRuntime(runtime *pages.ThemeRuntimeRegistry) *Controller {
	if h != nil {
		h.runtime = runtime
	}
	return h
}

func (h *Controller) WithCorePageViewModels(source *pageviewmodels.CorePageViewModelSource) *Controller {
	if h != nil {
		h.coreData = source
	}
	return h
}

func NewController(registry *pages.Registry, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{registry: registry, users: users, sessions: sessions}
}

func NewControllerWithThemes(registry *pages.Registry, users identity.ActorStore, sessions *authsession.Manager, themes ThemePackageStore) *Controller {
	return &Controller{registry: registry, users: users, sessions: sessions, themes: themes}
}

// WithAuditor 注入 audit_events 写入（批准/恢复必须审计）。
func (h *Controller) WithAuditor(w audit.Writer) *Controller {
	if h != nil {
		h.auditor = w
	}
	return h
}

// WithLoader 注入受控 PageDataLoader 网关（生产 SSR）。
func (h *Controller) WithLoader(g *pages.LoaderGateway) *Controller {
	if h != nil {
		h.loader = g
	}
	return h
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	// 公开：解析单个页面（前台 outlet / SSR 使用）
	api.Get("/pages/resolve", h.resolve)          // ?id=forum.home
	api.Get("/pages/resolve-path", h.resolvePath) // ?path=/docs/x
	api.Get("/pages/catalog", h.publicCatalog)
	api.Get("/site/active-theme/skin", h.activeSkin)
	api.Get("/site/theme-assets/:extensionId/*", h.themeAsset)
	api.Get("/site/page-regions", h.pageRegions) // ?page=forum.home

	admin := api.Group("/admin/pages")
	admin.Get("", h.adminList)
	admin.Get("/added", h.adminAdded)
	admin.Get("/activate-preview/:extensionId", h.activatePreview)
	admin.Get("/:pageId", h.adminGet)
	admin.Post("/:pageId/approve", h.adminApprove)
	admin.Post("/:pageId/restore-core", h.adminRestore)
}

func (h *Controller) resolve(c fiber.Ctx) error {
	setPrivatePageResponse(c)

	pageID := strings.TrimSpace(c.Query("id"))
	if pageID == "" {
		pageID = strings.TrimSpace(string(c.Request().URI().QueryArgs().Peek("id")))
	}
	if pageID == "" {
		pageID = strings.TrimSpace(c.Params("pageId"))
	}
	if pageID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.id_required")
	}

	var resolved pages.ResolvedPage
	var err error
	if h.registry != nil {
		resolved, err = h.registry.Resolve(c.Context(), pageID)
	} else {
		resolved, err = pages.ResolveCore(pageID)
	}
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}

	// 核心页 access：replace 后仍沿用目录 access
	if err := h.enforcePageAccess(c, resolved.Page.Access, ""); err != nil {
		return err
	}

	requestPath := strings.TrimSpace(c.Query("path"))
	routeParams := map[string]string(nil)
	if requestPath != "" {
		var matched bool
		routeParams, matched = pages.MatchCorePagePath(resolved.Page.ID, requestPath)
		if !matched {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.path_mismatch")
		}
	} else {
		// Compatibility for callers that do not yet forward the browser path.
		requestPath = resolved.Page.PathPattern
	}

	locale := strings.TrimSpace(c.Get("Accept-Language"))
	if i := strings.Index(locale, ","); i >= 0 {
		locale = locale[:i]
	}
	actorID := h.optionalActorID(c)

	var runtimeOutput *pages.ThemeRenderedPage
	runtimeCovered := false
	var loaderData any
	var loaderError string
	loaderHandled := false
	reason := ""
	if resolved.Provider == pages.ProviderCore {
		reason = resolveReasonAuthoritativeCore
	}
	setFallback := func(nextReason string) {
		if reason == "" || reason == resolveReasonAuthoritativeCore {
			reason = nextReason
		}
		resolved.Fallback = true
	}
	if resolved.Provider != pages.ProviderCore && h.runtime != nil {
		artifact := pages.RuntimeArtifact{
			ExtensionID: resolved.ExtensionID, ExtensionVersion: resolved.Version,
			PackageDigest: resolved.PackageDigest, RuntimeInstanceID: resolved.RuntimeInstanceID,
		}
		if snapshot, ok := h.runtime.Resolve(artifact, resolved.Page.ID, resolved.ContributionID); ok {
			runtimeCovered = true
			if _, pluginData := snapshot.PluginDataContract(resolved.ContributionID); pluginData {
				loaderHandled = true
				if h.loader == nil {
					loaderError = "pages: loader unavailable"
					setFallback(resolveReasonViewModelUnavailable)
				} else {
					loaded := h.loader.LoadExactForResolved(c.Context(), resolved, locale, actorID)
					if loaded.Error != "" || len(loaded.Data) == 0 {
						loaderError = loaded.Error
						setFallback(resolveReasonViewModelUnavailable)
					} else {
						output, renderErr := snapshot.RenderPluginData(
							c.Context(), loaded.Data, themecompiler.PageSEOView{Title: resolved.Page.ID}, resolved.ContributionID,
						)
						if renderErr == nil {
							resolved.TemplateHTML = snapshot.LegacyHTML(output)
							if resolved.TemplateHTML != "" {
								runtimeOutput = &output
								resolved.Fallback = output.Source == pages.ThemeRenderSourceEmergency
								if resolved.Fallback {
									reason = resolveReasonRenderFailed
								}
								if !resolved.Fallback {
									loaderData = pages.DecodeLoaderData(loaded.Data)
								}
							} else {
								setFallback(resolveReasonRenderFailed)
							}
						} else {
							loaderError = renderErr.Error()
							setFallback(resolveReasonRenderFailed)
						}
					}
				}
			} else if actor, actorErr := h.optionalActor(c); actorErr == nil {
				viewer, viewerErr := h.pageViewerForActor(c.Context(), actor)
				viewRequest := pages.CorePageViewModelRequest{
					PageID: resolved.Page.ID, Locale: locale, Path: requestPath, RouteParams: routeParams,
					Viewer: viewer, SEO: themecompiler.PageSEOView{Title: resolved.Page.ID},
				}
				var dataErr error
				if viewerErr == nil && h.coreData != nil {
					query, queryErr := parseViewModelQuery(c.Query("query"))
					if queryErr != nil {
						return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.query_invalid")
					}
					currentSID := ""
					if h.sessions != nil && actor.ID > 0 {
						currentSID, _ = h.sessions.CurrentSID(c)
					}
					viewRequest, dataErr = h.coreData.Populate(c.Context(), pageviewmodels.CorePageViewModelInput{
						Request: viewRequest, Actor: actor, CurrentSessionID: currentSID, Query: query,
					})
				}
				if errors.Is(dataErr, pageviewmodels.ErrCorePageDataNotFound) {
					return fiber.NewError(fiber.StatusNotFound, "pages.data_not_found")
				}
				if errors.Is(dataErr, pageviewmodels.ErrCorePageDataUnauthorized) {
					return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
				}
				var output pages.ThemeRenderedPage
				viewModelErr := firstPageRenderError(viewerErr, dataErr)
				renderErr := viewModelErr
				if viewModelErr == nil {
					output, renderErr = snapshot.Render(c.Context(), viewRequest, resolved.ContributionID)
				}
				// 可选组件 composition：普通错误 fail-open，SEO 围栏 fail closed。
				if renderErr == nil && h.runtime != nil {
					composed, composeErr := h.runtime.ApplyComposition(c.Context(), output, resolved.Page.ID)
					if composeErr != nil {
						if errors.Is(composeErr, pages.ErrPageCompositionSEO) {
							renderErr = composeErr
						}
					} else {
						output = composed
					}
				}
				if renderErr == nil {
					resolved.TemplateHTML = snapshot.LegacyHTML(output)
					if resolved.TemplateHTML != "" {
						runtimeOutput = &output
						// 模板链回退仍是有效 L1；只有 emergency 才交还核心页面。
						resolved.Fallback = output.Source == pages.ThemeRenderSourceEmergency
						if resolved.Fallback {
							reason = resolveReasonRenderFailed
						}
					} else {
						setFallback(resolveReasonRenderFailed)
					}
				} else if viewModelErr != nil {
					setFallback(resolveReasonViewModelUnavailable)
				} else {
					setFallback(resolveReasonRenderFailed)
				}
			} else {
				setFallback(resolveReasonViewModelUnavailable)
			}
		} else if h.runtime.Claims(resolved.ExtensionID, resolved.Page.ID, resolved.ContributionID) {
			// 精确 artifact 不匹配时禁止降级读取旧包文件。
			runtimeCovered = true
			setFallback(resolveReasonArtifactMismatch)
		}
	}

	// Unmigrated plugin/add contracts retain the explicit legacy L1 path until
	// LTS RemoveAfter + zero-shim. An exact compiled snapshot never falls back
	// to request-time IO (and must not record loader telemetry).
	if resolved.Provider != pages.ProviderCore && resolved.TemplatePath != "" && h.themes != nil {
		if !runtimeCovered {
			extID := resolved.ExtensionID
			if extID == "" {
				extID = resolved.Provider
			}
			if theme, terr := h.themes.Get(c.Context(), extID); terr == nil {
				root := extensions.PackageContentRoot(theme)
				if root != "" {
					// 请求路径残留：记 APILTS 遥测，供 LTS 删除门禁观察。
					apilts.Process().RecordShimCall(apilts.ThemeRequestTimeLoaderContractID)
					if html, lerr := pages.LoadTemplate(root, resolved.TemplatePath); lerr == nil {
						vars := map[string]string{"locale": locale}
						if rendered, rerr := pages.RenderTemplate(html, vars); rerr == nil {
							resolved.TemplateHTML = rendered
						} else {
							setFallback(resolveReasonRenderFailed)
							resolved.TemplateHTML = ""
						}
					} else {
						setFallback(resolveReasonRuntimeUnavailable)
					}
				} else {
					setFallback(resolveReasonRuntimeUnavailable)
				}
			} else {
				setFallback(resolveReasonRuntimeUnavailable)
			}
		}
	}
	// view=component 的页面正文由 Web 端挂载公开 L2 组件提供，view=schema 的正文由
	// 包内声明的有界描述符提供，两者 TemplateHTML 本来就为空，
	// 因此不参与「无模板即运行时不可用」的判定。
	if resolved.Provider != pages.ProviderCore && !pageBodyProvidedByView(resolved) &&
		runtimeOutput == nil && strings.TrimSpace(resolved.TemplateHTML) == "" && !resolved.Fallback {
		setFallback(resolveReasonRuntimeUnavailable)
	}

	resp := h.resolvePageResponse(resolved, reason, routeParams, runtimeOutput, loaderData, loaderError)
	if runtimeOutput != nil {
		resp.NodeRevision = runtimeOutput.NodeRevision
	}

	// access 通过后才调用 loader
	if !loaderHandled && !resolved.Fallback && resolved.DataSource == "plugin" && resolved.DataRoute != "" && h.loader != nil {
		lr := h.loader.LoadForResolved(c.Context(), resolved, locale, actorID)
		if lr.Error != "" {
			resp.LoaderError = lr.Error
		} else if len(lr.Data) > 0 {
			resp.LoaderData = pages.DecodeLoaderData(lr.Data)
		}
	}

	return apphttp.OK(c, resp)
}

const pageRegionsSchemaVersion = "sforum.page-regions@1"

type pageRegionsResponse struct {
	SchemaVersion string                           `json:"schemaVersion"`
	Page          string                           `json:"page"`
	Regions       []sitechrome.PageRegionViewModel `json:"regions"`
}

// pageRegions 公开:某页面标准区域的插件贡献内容(纯描述符;widget 引用由 L2 运行时权威裁决)。
func (h *Controller) pageRegions(c fiber.Ctx) error {
	pageID := strings.TrimSpace(c.Query("page"))
	if pageID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.id_required")
	}
	if _, known := pages.Find(pageID); !known || len(regioncatalog.PageRegions(pageID)) == 0 {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	regions := []sitechrome.PageRegionViewModel{}
	if h.regions != nil {
		resolved, err := h.regions.PageRegions(c.Context(), pageID)
		if err != nil {
			return err
		}
		if resolved != nil {
			regions = resolved
		}
	}
	return apphttp.OK(c, pageRegionsResponse{
		SchemaVersion: pageRegionsSchemaVersion,
		Page:          pageID,
		Regions:       regions,
	})
}

func (h *Controller) publicCatalog(c fiber.Ctx) error {
	items := pages.Catalog()
	out := make([]pages.PageDefinition, 0, len(items))
	for _, p := range items {
		if p.ID == "dev.components" {
			continue
		}
		out = append(out, p)
	}
	return apphttp.OK(c, out)
}

func (h *Controller) adminList(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canViewPages(actor) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.registry == nil {
		items := pages.Catalog()
		list := make([]pages.ProviderListItem, 0, len(items))
		for _, p := range items {
			list = append(list, pages.ProviderListItem{Page: p, Provider: pages.ProviderCore, ContractVersion: p.ContractVersion})
		}
		return apphttp.OK(c, list)
	}
	list, err := h.registry.ListProviders(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, list)
}

func (h *Controller) adminGet(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canViewPages(actor) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	pageID := c.Params("pageId")
	var resolved pages.ResolvedPage
	if h.registry != nil {
		resolved, err = h.registry.Resolve(c.Context(), pageID)
	} else {
		resolved, err = pages.ResolveCore(pageID)
	}
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	return apphttp.OK(c, resolved)
}

type approveRequest struct {
	ExtensionID     string `json:"extensionId"`
	ContributionID  string `json:"contributionId"`
	Version         string `json:"version"`
	PackageDigest   string `json:"packageDigest"`
	ContractVersion string `json:"contractVersion"`
	// TemplatePath 已废弃：服务端从已注册贡献读取，忽略客户端值（防伪造）。
	TemplatePath string `json:"templatePath"`
}

func (h *Controller) adminApprove(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	// 核心页替换仅 super_admin；extension.theme.manage 不可绕过。
	if !actor.IsSuperAdmin() {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.registry == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "pages.registry_unavailable")
	}
	var body approveRequest
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.invalid_body")
	}
	// Fiber 可能复用 Params 底层缓冲；入库前必须 Clone。
	pageID := strings.Clone(strings.TrimSpace(c.Params("pageId")))
	// 不把客户端 templatePath 写入绑定
	err = h.registry.ApproveReplace(c.Context(), pages.ProviderBinding{
		PageID:          pageID,
		ExtensionID:     body.ExtensionID,
		ContributionID:  body.ContributionID,
		Version:         body.Version,
		PackageDigest:   body.PackageDigest,
		ContractVersion: body.ContractVersion,
		ApprovedBy:      actor.ID,
	})
	if err != nil {
		return mapPagesError(err)
	}
	h.appendPageAudit(c, actor, audit.ActionPageReplaceApprove, map[string]any{
		"pageId":          pageID,
		"extensionId":     body.ExtensionID,
		"contributionId":  body.ContributionID,
		"version":         body.Version,
		"packageDigest":   body.PackageDigest,
		"contractVersion": body.ContractVersion,
	})
	return apphttp.OK(c, map[string]any{"pageId": pageID, "provider": body.ExtensionID})
}

func (h *Controller) adminRestore(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.IsSuperAdmin() {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.registry == nil {
		return apphttp.OK(c, map[string]any{"pageId": c.Params("pageId"), "provider": pages.ProviderCore})
	}
	pageID := strings.Clone(strings.TrimSpace(c.Params("pageId")))
	if err := h.registry.RestoreCore(c.Context(), pageID); err != nil {
		return mapPagesError(err)
	}
	h.appendPageAudit(c, actor, audit.ActionPageRestoreCore, map[string]any{
		"pageId": pageID,
	})
	return apphttp.OK(c, map[string]any{"pageId": pageID, "provider": pages.ProviderCore})
}

func (h *Controller) appendPageAudit(c fiber.Ctx, actor identity.Actor, action string, metadata map[string]any) {
	if h == nil || h.auditor == nil {
		return
	}
	_ = h.auditor.Append(c.Context(), audit.Event{
		ActorUserID: actor.ID,
		Action:      action,
		Metadata:    metadata,
	})
}

type themeActivationImpact struct {
	Contribution     pages.PageContribution   `json:"contribution"`
	Page             *pages.PageDefinition    `json:"page,omitempty"`
	Conflicts        []pages.PageContribution `json:"conflicts,omitempty"`
	RequiresApproval bool                     `json:"requiresApproval"`
}

type themeActivationPreview struct {
	ExtensionID                     string                  `json:"extensionId"`
	Version                         string                  `json:"version"`
	PackageDigest                   string                  `json:"packageDigest"`
	CurrentThemeID                  string                  `json:"currentThemeId"`
	CurrentThemeVersion             string                  `json:"currentThemeVersion"`
	CurrentThemeDigest              string                  `json:"currentThemeDigest"`
	Impacts                         []themeActivationImpact `json:"impacts"`
	CanActivate                     bool                    `json:"canActivate"`
	CanApproveCoreReplacements      bool                    `json:"canApproveCoreReplacements"`
	RequiresCoreReplacementApproval bool                    `json:"requiresCoreReplacementApproval"`
	Note                            string                  `json:"note"`
}

// activatePreview 主题激活确认 UI：列出将新增/替换的页面、路径、安全等级与冲突。
func (h *Controller) activatePreview(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canViewPages(actor) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.themes == nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.extension_not_found")
	}
	extID := c.Params("extensionId")
	theme, err := h.themes.Get(c.Context(), extID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.extension_not_found")
	}
	if staged, ok := theme.StagedArtifact(); ok {
		// 内置/上传主题升级保持 inert，确认页必须展示将被真正激活的 exact staged 制品。
		theme = staged
	}
	pkg, err := pages.LoadThemePackage(extensions.PackageContentRoot(theme))
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.theme_package_invalid")
	}
	contribs := pages.ContributionsFromTheme(theme.ID, theme.Version, theme.PackageDigest, pkg)
	out := make([]themeActivationImpact, 0, len(contribs))
	requiresApproval := false
	for _, contrib := range contribs {
		item := themeActivationImpact{Contribution: contrib, RequiresApproval: contrib.Action == pages.ActionReplace}
		if contrib.Action == pages.ActionReplace {
			requiresApproval = true
			if page, ok := pages.Find(contrib.Target); ok {
				p := page
				item.Page = &p
			}
			if h.registry != nil {
				if list, _ := h.registry.ListProviders(c.Context()); list != nil {
					for _, p := range list {
						if p.Page.ID == contrib.Target {
							for _, cand := range p.Candidates {
								if cand.ExtensionID != contrib.ExtensionID {
									item.Conflicts = append(item.Conflicts, cand)
								}
							}
						}
					}
				}
			}
		}
		out = append(out, item)
	}
	current := extensions.Extension{}
	if active, activeErr := h.themes.ActiveTheme(c.Context()); activeErr == nil {
		current = active
	}
	return apphttp.OK(c, themeActivationPreview{
		ExtensionID: theme.ID, Version: theme.Version, PackageDigest: theme.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: current.PackageDigest,
		Impacts: out, CanActivate: canActivateTheme(actor), CanApproveCoreReplacements: actor.IsSuperAdmin(),
		RequiresCoreReplacementApproval: requiresApproval,
		Note:                            "Core page replacement requires an exact visible preview and explicit super_admin approval.",
	})
}

// resolvePath 公开：按请求 path 解析 add 页面（动态公开路由）。
func (h *Controller) resolvePath(c fiber.Ctx) error {
	setPrivatePageResponse(c)

	path := strings.TrimSpace(c.Query("path"))
	if path == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.path_required")
	}
	if pages.IsReservedPath(path) {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	if h.registry == nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	match, ok := h.registry.ResolveAddedPathMatch(path)
	if !ok {
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	}
	contrib := match.Contribution

	// access 权威检查（fail closed）
	if err := h.enforcePageAccess(c, contrib.Access, contrib.Permission); err != nil {
		return err
	}

	locale := strings.TrimSpace(c.Query("locale"))
	if locale == "" {
		locale = strings.TrimSpace(c.Get("Accept-Language"))
		if i := strings.Index(locale, ","); i >= 0 {
			locale = locale[:i]
		}
	}
	actorID := h.optionalActorID(c)

	resp := h.addedPageResponse(contrib, match.Params)

	setRespFallback := func(reason string) {
		if resp.Reason == "" {
			resp.Reason = reason
		}
		resp.Fallback = true
	}
	runtimeCovered := false
	loaderHandled := false
	if h.runtime != nil {
		artifact := pages.RuntimeArtifact{
			ExtensionID: contrib.ExtensionID, ExtensionVersion: contrib.Version,
			PackageDigest: contrib.PackageDigest, RuntimeInstanceID: contrib.RuntimeInstanceID,
		}
		if snapshot, found := h.runtime.Resolve(artifact, contrib.ID, contrib.ID); found {
			runtimeCovered = true
			if _, pluginData := snapshot.PluginDataContract(contrib.ID); pluginData {
				loaderHandled = true
				if h.loader == nil {
					resp.LoaderError = "pages: loader unavailable"
					setRespFallback(resolveReasonViewModelUnavailable)
				} else {
					loaded := h.loader.LoadExactForContribution(c.Context(), contrib, match.Params, locale, actorID)
					if loaded.Error != "" || len(loaded.Data) == 0 {
						resp.LoaderError = loaded.Error
						setRespFallback(resolveReasonViewModelUnavailable)
					} else {
						output, renderErr := snapshot.RenderPluginData(
							c.Context(), loaded.Data, themecompiler.PageSEOView{Title: contrib.ID}, contrib.ID,
						)
						if renderErr != nil {
							resp.LoaderError = renderErr.Error()
							setRespFallback(resolveReasonRenderFailed)
						} else {
							resp.TemplateHTML = snapshot.LegacyHTML(output)
							resp.RenderOutput = &output
							resp.NodeRevision = output.NodeRevision
							resp.Fallback = resp.TemplateHTML == "" || output.Source == pages.ThemeRenderSourceEmergency
							if resp.Fallback {
								resp.Reason = resolveReasonRenderFailed
							}
							if !resp.Fallback {
								resp.LoaderData = pages.DecodeLoaderData(loaded.Data)
							}
						}
					}
				}
			}
		} else if h.runtime.Claims(contrib.ExtensionID, contrib.ID, contrib.ID) {
			runtimeCovered = true
			setRespFallback(resolveReasonArtifactMismatch)
		}
	}

	// 加载模板（注入 route params）— 仅无 snapshot 覆盖时；记 LTS 遥测。
	if !runtimeCovered && contrib.Template != "" && h.themes != nil {
		if theme, terr := h.themes.Get(c.Context(), contrib.ExtensionID); terr == nil {
			root := extensions.PackageContentRoot(theme)
			if root != "" {
				apilts.Process().RecordShimCall(apilts.ThemeRequestTimeLoaderContractID)
				if html, lerr := pages.LoadTemplate(root, contrib.Template); lerr == nil {
					vars := map[string]string{"locale": locale}
					for k, v := range match.Params {
						vars[k] = v
					}
					if rendered, rerr := pages.RenderTemplate(html, vars); rerr == nil {
						resp.TemplateHTML = rendered
					} else {
						setRespFallback(resolveReasonRenderFailed)
					}
				} else {
					setRespFallback(resolveReasonRuntimeUnavailable)
				}
			}
		} else {
			setRespFallback(resolveReasonRuntimeUnavailable)
		}
	}
	// view=component/schema 的 add 页正文由包内声明提供，TemplateHTML 为空是正常状态。
	if !responseBodyProvidedByView(resp) && resp.RenderOutput == nil && strings.TrimSpace(resp.TemplateHTML) == "" && !resp.Fallback {
		if resp.DataSource == "plugin" && resp.DataRoute != "" && h.loader != nil {
			lr := h.loader.LoadForContribution(c.Context(), contrib, match.Params, locale, actorID)
			if lr.Error != "" {
				resp.LoaderError = lr.Error
			}
		}
		setRespFallback(resolveReasonRuntimeUnavailable)
	}

	// access 已通过 → loader
	if !loaderHandled && !resp.Fallback && contrib.DataRoute != "" && h.loader != nil {
		lr := h.loader.LoadForContribution(c.Context(), contrib, match.Params, locale, actorID)
		if lr.Error != "" {
			resp.LoaderError = lr.Error
		} else if len(lr.Data) > 0 {
			resp.LoaderData = pages.DecodeLoaderData(lr.Data)
		}
	}

	h.applyThemeShell(c, &resp, match.Params, locale)
	return apphttp.OK(c, resp)
}

func (h *Controller) optionalActorID(c fiber.Ctx) int64 {
	actor, err := h.optionalActor(c)
	if err != nil {
		return 0
	}
	return actor.ID
}

func (h *Controller) pageViewer(c fiber.Ctx) (themecompiler.PageViewerState, error) {
	actor, err := h.optionalActor(c)
	if err != nil {
		return themecompiler.PageViewerState{}, err
	}
	return h.pageViewerForActor(c.Context(), actor)
}

func (h *Controller) pageViewerForActor(ctx context.Context, actor identity.Actor) (themecompiler.PageViewerState, error) {
	if actor.ID == 0 {
		return themecompiler.PageViewerState{}, nil
	}
	store, ok := h.users.(pageViewerStore)
	if !ok {
		return themecompiler.PageViewerState{}, errors.New("pages: current user projection unavailable")
	}
	current, err := store.GetCurrentUser(ctx, actor.ID)
	if err != nil {
		return themecompiler.PageViewerState{}, err
	}
	return themecompiler.PageViewerState{
		Authenticated: true,
		UserID:        current.ID,
		Username:      current.Username,
		DisplayName:   current.DisplayName,
		AvatarURL:     current.Avatar.URL,
		// Actor 已包含 PAT scope 收窄结果，不能从 CurrentUser 恢复完整权限。
		Permissions: actorPermissionKeys(actor),
	}, nil
}

func parseViewModelQuery(raw string) (url.Values, error) {
	if len(raw) > 4096 {
		return nil, errors.New("pages: view model query exceeds limit")
	}
	query, err := url.ParseQuery(raw)
	if err != nil || len(query) > 32 {
		return nil, errors.New("pages: invalid view model query")
	}
	for key, values := range query {
		if len(key) > 64 || len(values) > 8 {
			return nil, errors.New("pages: invalid view model query")
		}
		for _, value := range values {
			if len(value) > 512 {
				return nil, errors.New("pages: invalid view model query")
			}
		}
	}
	return query, nil
}

func firstPageRenderError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func selectedProvider(resolved pages.ResolvedPage) string {
	if strings.TrimSpace(resolved.Provider) != "" {
		return resolved.Provider
	}
	return pages.ProviderCore
}

func actorPermissionKeys(actor identity.Actor) []string {
	permissions := make([]string, 0, len(actor.Permissions))
	for permission, allowed := range actor.Permissions {
		if allowed {
			permissions = append(permissions, permission)
		}
	}
	sort.Strings(permissions)
	return permissions
}

func (h *Controller) optionalActor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.OptionalActor(c, h.sessions, h.users)
}

func (h *Controller) adminAdded(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !canViewPages(actor) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.registry == nil {
		return apphttp.OK(c, []pages.PageContribution{})
	}
	return apphttp.OK(c, h.registry.AddedPages())
}

func (h *Controller) activeSkin(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	if h.runtime != nil {
		if skin, ok := h.runtime.ActiveSkin(); ok {
			return apphttp.OK(c, skin)
		}
	}
	if h.themes == nil {
		return apphttp.OK(c, pages.ActiveSkinPublic{CSS: []string{}})
	}
	theme, err := h.themes.ActiveTheme(c.Context())
	if err != nil {
		return apphttp.OK(c, pages.ActiveSkinPublic{CSS: []string{}})
	}
	skin, err := pages.SkinFromPackage(theme.ID, theme.Version, theme.PackageDigest, extensions.PackageContentRoot(theme))
	if err != nil {
		return apphttp.OK(c, pages.ActiveSkinPublic{ExtensionID: theme.ID, CSS: []string{}})
	}
	return apphttp.OK(c, skin)
}

func (h *Controller) themeAsset(c fiber.Ctx) error {
	if h.themes == nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_not_found")
	}
	extensionID := c.Params("extensionId")
	rel := strings.TrimPrefix(c.Params("*"), "/")
	wantDigest := strings.TrimSpace(c.Query("v"))
	if wantDigest == "" {
		wantDigest = strings.TrimSpace(c.Query("digest"))
	}

	active, err := h.themes.ActiveTheme(c.Context())
	if err != nil || active.ID != extensionID {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_not_found")
	}
	if active.Type != extensions.TypeTheme {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_not_found")
	}
	if wantDigest != "" && active.PackageDigest != "" && !strings.EqualFold(wantDigest, active.PackageDigest) {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_digest_mismatch")
	}

	full, err := pages.ResolveThemeAsset(extensions.PackageContentRoot(active), rel)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_not_found")
	}
	ext := strings.ToLower(filepath.Ext(full))
	ctype, ok := pages.AllowedThemeAssetExt[ext]
	if !ok {
		return fiber.NewError(fiber.StatusForbidden, "pages.asset_type_forbidden")
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "pages.asset_not_found")
	}
	if ext == ".css" {
		if err := pages.ValidateCSS(string(raw)); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.css_invalid")
		}
	}
	if ctype == "" {
		ctype = mime.TypeByExtension(ext)
		if ctype == "" {
			ctype = "application/octet-stream"
		}
	}
	c.Set("Content-Type", ctype)
	c.Set("X-Content-Type-Options", "nosniff")
	digest := sha256.Sum256(raw)
	c.Set(fiber.HeaderETag, `"`+hex.EncodeToString(digest[:])+`"`)
	c.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self'")
	c.Set("X-Frame-Options", "DENY")
	if wantDigest != "" && active.PackageDigest != "" {
		c.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Set("Cache-Control", "public, max-age=300")
	}
	return c.Send(raw)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func canViewPages(actor identity.Actor) bool {
	return actor.Can(identity.PermissionExtensionView) ||
		actor.Can(identity.PermissionExtensionThemeManage) ||
		actor.Can(identity.PermissionExtensionManage)
}

func canActivateTheme(actor identity.Actor) bool {
	return actor.Can(identity.PermissionExtensionThemeManage) || actor.Can(identity.PermissionExtensionManage)
}

func mapPagesError(err error) error {
	switch {
	case errors.Is(err, pages.ErrUnknownPage):
		return fiber.NewError(fiber.StatusNotFound, "pages.not_found")
	case errors.Is(err, pages.ErrNotReplaceable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.not_replaceable")
	case errors.Is(err, pages.ErrReservedPath):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.reserved_path")
	case errors.Is(err, pages.ErrInvalidContribution):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.invalid_contribution")
	case errors.Is(err, pages.ErrContractMismatch):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.contract_mismatch")
	case errors.Is(err, pages.ErrInvalidAccess):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "pages.invalid_access")
	case errors.Is(err, pages.ErrApprovalRequired):
		return fiber.NewError(fiber.StatusForbidden, "pages.approval_required")
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	default:
		return err
	}
}
