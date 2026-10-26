package extensionscontroller

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensioncomposition "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionComposition"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const maxUploadedArchiveBytes = extensions.MaxArchiveBytes

const (
	PublicFrontendHeaderExtensionID      = "X-SForum-Public-Extension-ID"
	PublicFrontendHeaderExtensionVersion = "X-SForum-Public-Extension-Version"
	PublicFrontendHeaderPackageDigest    = "X-SForum-Public-Package-Digest"
	PublicFrontendHeaderImpactDigest     = "X-SForum-Public-Impact-Digest"
	PublicFrontendHeaderComponentID      = "X-SForum-Public-Component-ID"
	CodePublicFrontendBridgeStale        = "extension.public_frontend_bridge_stale"
)

var ErrPublicFrontendBridgeStale = errors.New("extensions: public frontend bridge identity is stale")

type Controller struct {
	service         *extensions.Service
	frontend        TrustedFrontendService
	users           identity.ActorStore
	sessions        *authsession.Manager
	gateway         RouteGateway
	routeProviders  *routes.ProviderSelectionAPI
	routeInspector  *routes.Inspector
	routeCatalogAPI *routes.Catalog
	// queryCatalogAPI 为 nil 时 query-catalog 路由 fail closed（503）。
	queryCatalogAPI QueryCatalogSource
	// routeIncidents 是崩溃循环隔离持久状态的只读边界（M5 管理员诊断）。
	routeIncidents apphttp.RouteRuntimeIncidentReader
	cacheRegistry  *cacheregistry.Registry
	cacheInspect   func(*cacheregistry.Registry, int) (hostapi.HostCacheInspectionSnapshot, error)
	// componentInspector / navigationInspector /
	// assetRegistry / themeRuntime 仅服务 admin 检查器；为 nil 时对应路由 fail closed 为 503。
	componentInspector  extensioncomposition.Inspector
	navigationInspector *navigationregistry.Inspector
	assetRegistry       *assetregistry.Registry
	themeRuntime        *pages.ThemeRuntimeRegistry
	routeContracts      RouteContractCatalog
	routeAuditor        audit.IDWriter
	providerSlots       *extensionsruntime.ProviderSlotSelectionAPI
	providerProber      ProviderSlotProber
	providerAuditor     audit.IDWriter
	adminSurfaces       AdminSurfaceRuntime
	adminAuditor        audit.Writer
	// editorRegistry 为 nil 时公开 editor-catalog 返回空 modules（fail-closed）。
	editorRegistry *editorregistry.Registry
	// entityRegistry 为 nil 时公开 entity-catalog 返回空 entities（fail-closed）。
	entityRegistry *entityregistry.Registry
	// contentRegistry 为 nil 时公开 content-catalog 返回空 content（fail-closed）。
	contentRegistry *contentregistry.Registry
	// mediaRegistry 为 nil 时公开 media-catalog 返回空 policies/processors（fail-closed）。
	mediaRegistry *mediaregistry.Registry
	// devTrustSessions 为 nil 时 dev 信任会话路由 fail-closed（仅开发环境注入）。
	devTrustSessions DevTrustSessionService
	// versionGC 是版本 GC 管理端入口的读写边界（阶段 5）；nil 时路由 fail-closed。
	versionGC VersionGCManagementService
	// remoteArchives 为 nil 时链接安装路由 fail-closed（503）。
	remoteArchives ArchiveFetcher
	// cleanupConvergence 是卸载物理清理的聚焦协作对象；nil 时免键收尾路由
	// fail-closed（503），列表徽标投影静默降级。
	cleanupConvergence *extensions.CleanupConvergenceService
	// installer 与 service 同源；测试可注入 fake 以验证编排顺序。
	installer archiveInstaller
}

type TrustedFrontendService interface {
	Frontend(context.Context, identity.Actor, string) (extensions.FrontendStatus, error)
	Grant(context.Context, identity.Actor, string, extensions.GrantFrontendInput) (extensions.FrontendStatus, error)
	Revoke(context.Context, identity.Actor, string) (extensions.FrontendStatus, error)
}

type TrustedFrontendAssetService interface {
	Asset(context.Context, identity.Actor, string, string, string) (extensions.FrontendAsset, error)
}

type TrustedFrontendComponentAssetService interface {
	ComponentAsset(context.Context, identity.Actor, string, string, string, string) (extensions.FrontendAsset, error)
}

type TrustedFrontendChallengeService interface {
	Challenge(context.Context, identity.Actor, string) (extensions.FrontendTrustChallenge, error)
}

type PublicFrontendRuntimeService interface {
	PublicComponent(context.Context, string, string) (extensions.PublicFrontendComponent, error)
	PublicAsset(context.Context, string, string, string, string) (extensions.FrontendAsset, error)
	PublicPackageAsset(context.Context, string, string, string) (extensions.FrontendAsset, error)
	// PublicPagePolicyForComponents aggregates Host-owned document CSP for exact
	// page-local L2 soft refs. Empty refs return the Host baseline when public L2
	// gates pass.
	PublicPagePolicyForComponents(context.Context, []extensions.PublicFrontendComponentRef) (extensions.PublicFrontendPolicy, error)
}

type updateSettingsRequest struct {
	Values map[string]string `json:"values"`
}

type executeSettingsActionRequest struct {
	Values  map[string]string                               `json:"values"`
	Secrets map[string]extensions.SettingsActionSecretInput `json:"secrets"`
}

func NewController(service *extensions.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return NewControllerWithGateway(service, users, sessions, nil)
}

func NewControllerWithGateway(service *extensions.Service, users identity.ActorStore, sessions *authsession.Manager, gateway RouteGateway) *Controller {
	return &Controller{service: service, users: users, sessions: sessions, gateway: gateway, installer: service}
}

// WithCleanupConvergence 注入卸载物理清理协作对象；生产装配必接，缺省时
// 免键收尾路由 fail-closed、徽标投影降级。
func (h *Controller) WithCleanupConvergence(convergence *extensions.CleanupConvergenceService) *Controller {
	h.cleanupConvergence = convergence
	return h
}

// WithRemoteArchiveFetcher injects the outbound archive downloader. Without it
// the install-from-url route fails closed.
func (h *Controller) WithRemoteArchiveFetcher(fetcher ArchiveFetcher) *Controller {
	h.remoteArchives = fetcher
	return h
}

func (h *Controller) WithTrustedRuntime(frontend TrustedFrontendService) *Controller {
	h.frontend = frontend
	return h
}

// WithDevTrustSessions 注入开发期信任会话边界；仅 APP_ENV=development 的 API 装配调用。
func (h *Controller) WithDevTrustSessions(service DevTrustSessionService) *Controller {
	h.devTrustSessions = service
	return h
}

func (h *Controller) WithRouteProviderSelection(
	api *routes.ProviderSelectionAPI,
	auditor audit.IDWriter,
) *Controller {
	h.routeProviders = api
	h.routeInspector = routes.NewProviderSelectionInspector(api, nil)
	h.routeCatalogAPI = routes.NewProviderSelectionCatalog(api)
	h.routeAuditor = auditor
	return h
}

// WithQueryCatalog 注入密封 Core 查询目录的只读边界；nil 保持 fail closed。
func (h *Controller) WithQueryCatalog(source QueryCatalogSource) *Controller {
	h.queryCatalogAPI = source
	return h
}

func (h *Controller) WithRouteInspector(inspector *routes.Inspector) *Controller {
	h.routeInspector = inspector
	return h
}

// WithRouteIncidentReader 注入崩溃循环隔离持久状态的只读边界。
func (h *Controller) WithRouteIncidentReader(reader apphttp.RouteRuntimeIncidentReader) *Controller {
	h.routeIncidents = reader
	return h
}

func (h *Controller) WithCacheInspector(
	registry *cacheregistry.Registry,
	inspector *hostapi.HostCacheInspector,
) *Controller {
	h.cacheRegistry = registry
	h.cacheInspect = nil
	if inspector != nil {
		h.cacheInspect = inspector.Inspect
	}
	return h
}

// WithComponentCompositionInspector wires the stable redacted inspection boundary.
func (h *Controller) WithComponentCompositionInspector(inspector extensioncomposition.Inspector) *Controller {
	if h != nil {
		h.componentInspector = inspector
	}
	return h
}

// WithNavigationInspector wires the Host navigation/region inspector.
func (h *Controller) WithNavigationInspector(inspector *navigationregistry.Inspector) *Controller {
	if h != nil {
		h.navigationInspector = inspector
	}
	return h
}

// WithAssetInspector wires the shared Host Asset Registry for the admin
// asset inspector. Nil registry keeps the route fail-closed as 503.
func (h *Controller) WithAssetInspector(registry *assetregistry.Registry) *Controller {
	if h != nil {
		h.assetRegistry = registry
	}
	return h
}

// WithThemeRuntimeInspector wires the Host Theme Runtime Registry for the
// admin template/override inspector.
func (h *Controller) WithThemeRuntimeInspector(registry *pages.ThemeRuntimeRegistry) *Controller {
	if h != nil {
		h.themeRuntime = registry
	}
	return h
}

func (h *Controller) WithProviderSlotSelection(
	api *extensionsruntime.ProviderSlotSelectionAPI,
	prober ProviderSlotProber,
	auditor audit.IDWriter,
) *Controller {
	h.providerSlots = api
	h.providerProber = prober
	h.providerAuditor = auditor
	return h
}

// entityImportExportDryRun 需 extension.view（或兼容 extension.manage）+ 登录。
// 对单一实体做 import/export 计划 + 权限 dry-run；永不执行导入导出。
// 实体级 Allowed=false 仍返回 200，以便前端展示拒绝原因（与路由级 403 分离）。
func (h *Controller) entityImportExportDryRun(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	// 与 catalog core.guard.extensions.read 对齐：Core Fiber 路径不走 Dispatcher 时
	// 仍须在 handler 内执行 view 门，避免仅登录即可窥探实体计划与 permissionKey。
	if !actor.Can(identity.PermissionExtensionView) && !actor.Can(identity.PermissionExtensionManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h == nil || h.entityRegistry == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "entity.registry_unavailable")
	}
	entityID := strings.TrimSpace(c.Params("entityId"))
	if entityID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity.id_required")
	}
	action := strings.ToLower(strings.TrimSpace(c.Query("action")))
	if action == "" {
		action = entityregistry.ActionExport
	}
	if action != entityregistry.ActionImport && action != entityregistry.ActionExport {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity.import_export_action_invalid")
	}
	contribution, err := h.entityRegistry.Resolve(entityID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "entity.not_found")
	}
	if contribution.Kind != entityregistry.KindEntity {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "entity.kind_invalid")
	}
	// 仅把 actor 实际持有的 import/export 键注入评估集；super_admin 经 Can 展开。
	held := make([]string, 0, 2)
	for _, key := range []string{contribution.PermissionImport, contribution.PermissionExport} {
		if key != "" && actor.Can(key) {
			held = append(held, key)
		}
	}
	result, err := h.entityRegistry.DryRunImportExport(
		entityID, action, entityregistry.NewActorPermissions(held...),
	)
	if err != nil {
		if errors.Is(err, entityregistry.ErrInvalid) {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "entity.import_export_invalid")
		}
		if errors.Is(err, entityregistry.ErrNotFound) {
			return fiber.NewError(fiber.StatusNotFound, "entity.not_found")
		}
		return err
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Content-Type-Options", "nosniff")
	return apphttp.OK(c, result)
}

func (h *Controller) list(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if extensionID := strings.TrimSpace(c.Query("id")); extensionID != "" {
		item, err := h.service.Detail(c.Context(), actor, extensionID)
		if errors.Is(err, extensions.ErrExtensionNotFound) {
			return apphttp.OK(c, []extensions.Extension{})
		}
		if err != nil {
			return mapExtensionError(err)
		}
		if pending := h.pendingUninstallCleanups(c); pending != nil && pending[item.ID] {
			item.PendingUninstallCleanup = true
		}
		return apphttp.OK(c, []extensions.Extension{item})
	}
	items, err := h.service.List(c.Context(), actor)
	if err != nil {
		return mapExtensionError(err)
	}
	if pending := h.pendingUninstallCleanups(c); len(pending) > 0 {
		for i := range items {
			if pending[items[i].ID] {
				items[i].PendingUninstallCleanup = true
			}
		}
	}
	return apphttp.OK(c, items)
}

// pendingUninstallCleanups 是管理端投影的辅助查询：返回存在 pending 卸载
// tombstone 的扩展集合。清理收敛边界不可用（如测试装配）时静默降级为空集，
// 徽标缺失不影响任何操作路径。
func (h *Controller) pendingUninstallCleanups(c fiber.Ctx) map[string]bool {
	if h.cleanupConvergence == nil {
		return nil
	}
	pending, err := h.cleanupConvergence.PendingUninstallCleanupExtensions(c.Context())
	if err != nil {
		return nil
	}
	return pending
}

func (h *Controller) navigation(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.Navigation(c.Context(), actor)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) install(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeInvalidArchive)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUploadedArchiveBytes+1))
	if err != nil {
		return err
	}
	result, err := h.service.InstallOrUpgradeArchive(c.Context(), actor, extensions.ArchiveInput{
		FileName:     fileHeader.Filename,
		Data:         data,
		SourceDigest: archiveSourceDigest(data),
	})
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.Created(c, result)
}

func (h *Controller) uninstall(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.UninstallInput
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&input); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	input.IdempotencyKey = c.Get("Idempotency-Key")
	result, err := h.service.UninstallWithResult(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) listMigrations(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListMigrations(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) applyMigrations(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ApplyDeclaredMigrations(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) enable(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.EnableInput
	// body 可选；空 body 表示未确认 capabilities。
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&input); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	input.IdempotencyKey = c.Get("Idempotency-Key")
	item, err := h.service.Enable(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) executableTrustStatus(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	staged, err := executableTrustTargetsStaged(c)
	if err != nil {
		return err
	}
	var status extensions.ExecutableTrustStatus
	if staged {
		status, err = h.service.ExecutableTrustStatusForStaged(c.Context(), actor, c.Params("id"))
	} else {
		status, err = h.service.ExecutableTrustStatus(c.Context(), actor, c.Params("id"))
	}
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) issueExecutableTrustChallenge(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	staged, err := executableTrustTargetsStaged(c)
	if err != nil {
		return err
	}
	var challenge extensions.TrustChallenge
	if staged {
		challenge, err = h.service.IssueExecutableTrustChallengeForStaged(c.Context(), actor, c.Params("id"))
	} else {
		challenge, err = h.service.IssueExecutableTrustChallenge(c.Context(), actor, c.Params("id"))
	}
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, challenge)
}

func executableTrustTargetsStaged(c fiber.Ctx) (bool, error) {
	switch strings.TrimSpace(c.Query("target")) {
	case "":
		return false, nil
	case "staged":
		return true, nil
	default:
		return false, fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
}

func (h *Controller) revokeExecutableTrust(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	status, err := h.service.RevokeExecutableTrust(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, status)
}

func (h *Controller) disable(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.DisableWithInput(c.Context(), actor, c.Params("id"), extensions.LifecycleRequestInput{
		IdempotencyKey: c.Get("Idempotency-Key"),
	})
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) upgrade(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.UpgradeInput
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&input); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	input.IdempotencyKey = c.Get("Idempotency-Key")
	item, err := h.service.Upgrade(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) rollback(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.RollbackInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	input.IdempotencyKey = c.Get("Idempotency-Key")
	item, err := h.service.Rollback(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) verify(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.VerifyExtension(c.Context(), actor, c.Params("id"))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) activate(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input extensions.ThemeActivationInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	item, err := h.service.ActivateThemeFromPreview(c.Context(), actor, c.Params("id"), input)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) events(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.Events(c.Context(), actor, c.Params("id"), queryInt(c, "limit", 50))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) eventDefinitions(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.EventDefinitions(c.Context(), actor)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) eventDeliveries(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.EventDeliveries(c.Context(), actor, extensions.EventDeliveryListInput{
		ExtensionID: c.Query("extensionId"),
		EventName:   c.Query("eventName"),
		Status:      c.Query("status"),
		Limit:       queryInt(c, "limit", 50),
	})
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) contributionPoints(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ContributionPoints(c.Context(), actor)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) contributions(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.Contributions(c.Context(), actor)
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) publicActiveThemeSettings(c fiber.Ctx) error {
	settings, err := h.service.PublicActiveThemeSettings(c.Context())
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) settings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.Settings(c.Context(), actor, c.Params("id"), apphttp.Locale(c))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) updateSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateSettingsRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	settings, err := h.service.UpdateSettings(c.Context(), actor, c.Params("id"), extensions.UpdateSettingsInput{Values: req.Values}, apphttp.Locale(c))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) resetSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.ResetSettings(c.Context(), actor, c.Params("id"), apphttp.Locale(c))
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) executeSettingsAction(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req executeSettingsActionRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	result, err := h.service.ExecuteSettingsAction(c.Context(), actor, c.Params("id"), c.Params("actionId"), extensions.ExecuteSettingsActionInput{Values: req.Values, Secrets: req.Secrets})
	if err != nil {
		return mapExtensionError(err)
	}
	return apphttp.OK(c, result)
}

func queryInt(c fiber.Ctx, name string, fallback int) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (h *Controller) proxyExtensionRoute(c fiber.Ctx) error {
	exact, err := h.publicFrontendBridgeIdentity(c)
	if err != nil {
		return mapExtensionError(err)
	}
	routePath := "/" + c.Params("*")
	matched, err := h.service.MatchRoute(c.Context(), c.Params("extensionId"), c.Method(), routePath)
	if err != nil {
		return mapExtensionError(err)
	}
	if exact != nil && (matched.Extension.ID != exact.ExtensionID ||
		matched.Extension.Version != exact.ExtensionVersion ||
		matched.Extension.PackageDigest != exact.PackageDigest) {
		return mapExtensionError(ErrPublicFrontendBridgeStale)
	}
	actor, hasActor, err := h.optionalActor(c)
	if err != nil {
		return err
	}
	access := matched.Route.Access
	if access == "" {
		access = extensions.RouteAccessLogin
	}
	switch access {
	case extensions.RouteAccessLogin:
		if !hasActor {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
	case extensions.RouteAccessPermission:
		if !hasActor {
			return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
		}
		if !actor.Can(matched.Route.Permission) {
			return fiber.NewError(fiber.StatusForbidden, "permission.denied")
		}
	}
	if h.gateway == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeRuntimeUnavailable)
	}
	if err := h.gateway.Proxy(c, ProxyInput{
		Matched: matched, Actor: actor, HasActor: hasActor, PublicFrontendExact: exact,
	}); err != nil {
		return mapExtensionError(err)
	}
	return nil
}

func (h *Controller) publicFrontendBridgeIdentity(c fiber.Ctx) (*PublicFrontendBridgeIdentity, error) {
	identity := PublicFrontendBridgeIdentity{
		ExtensionID:      strings.TrimSpace(c.Get(PublicFrontendHeaderExtensionID)),
		ExtensionVersion: strings.TrimSpace(c.Get(PublicFrontendHeaderExtensionVersion)),
		PackageDigest:    strings.TrimSpace(c.Get(PublicFrontendHeaderPackageDigest)),
		ImpactDigest:     strings.TrimSpace(c.Get(PublicFrontendHeaderImpactDigest)),
		ComponentID:      strings.TrimSpace(c.Get(PublicFrontendHeaderComponentID)),
	}
	if identity == (PublicFrontendBridgeIdentity{}) {
		return nil, nil
	}
	if identity.ExtensionID == "" || identity.ExtensionVersion == "" || identity.ComponentID == "" ||
		!frontendDigestPattern.MatchString(identity.PackageDigest) ||
		!frontendDigestPattern.MatchString(identity.ImpactDigest) ||
		identity.ExtensionID != c.Params("extensionId") {
		return nil, ErrPublicFrontendBridgeStale
	}
	runtime, ok := h.frontend.(PublicFrontendRuntimeService)
	if h.frontend == nil || !ok {
		return nil, ErrPublicFrontendBridgeStale
	}
	descriptor, err := runtime.PublicComponent(c.Context(), identity.ExtensionID, identity.ComponentID)
	if err != nil || descriptor.ExtensionID != identity.ExtensionID ||
		descriptor.ExtensionVersion != identity.ExtensionVersion ||
		descriptor.PackageDigest != identity.PackageDigest ||
		descriptor.ImpactDigest != identity.ImpactDigest ||
		descriptor.ComponentID != identity.ComponentID {
		return nil, ErrPublicFrontendBridgeStale
	}
	return &identity, nil
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func (h *Controller) optionalActor(c fiber.Ctx) (identity.Actor, bool, error) {
	actor, err := apphttp.OptionalActor(c, h.sessions, h.users)
	if err != nil {
		return identity.Actor{}, false, err
	}
	if actor.ID == 0 {
		return identity.Actor{}, false, nil
	}
	return actor, true, nil
}

// lifecycleCleanupFailureError 把卸载物理清理失败渲染成 503。类型化失败携带
// operationId / failedStep / retriable，写入 data.details 供管理端定位；
// 无类型化信息时退回裸 reason，保持既有响应形状。
func lifecycleCleanupFailureError(err error) error {
	var failure *extensions.LifecycleCleanupFailure
	if errors.As(err, &failure) {
		return apphttp.NewErrorWithDetails(fiber.StatusServiceUnavailable, extensions.CodeLifecycleCleanupFailed, map[string]any{
			"operationId": failure.OperationID,
			"failedStep":  string(failure.Step),
			"retriable":   failure.Retriable,
		})
	}
	return apphttp.NewError(fiber.StatusServiceUnavailable, extensions.CodeLifecycleCleanupFailed)
}

// 兼容性拒绝（coreVersion / 可执行平台）的映射见 errors_compatibility.go：
// 它们各自要拆 422 与 409 两个状态码，留在本文件会挤占请求处理的可读性。
func mapExtensionError(err error) error {
	if mapped := mapRemoteInstallError(err); mapped != nil {
		return mapped
	}
	if mapped := mapLifecycleInspectionError(err); mapped != nil {
		return mapped
	}
	if mapped := mapCoreIncompatibleError(err); mapped != nil {
		return mapped
	}
	if mapped := mapExecutablePlatformError(err); mapped != nil {
		return mapped
	}
	switch {
	case errors.Is(err, ErrPublicFrontendBridgeStale):
		return fiber.NewError(fiber.StatusPreconditionFailed, CodePublicFrontendBridgeStale)
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, extensions.ErrUntrustedBackendRestricted):
		return fiber.NewError(fiber.StatusForbidden, extensions.CodeUntrustedBackendRestricted)
	case errors.Is(err, extensions.ErrInvalidArchive):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeInvalidArchive)
	case errors.Is(err, extensions.ErrInvalidManifest):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeInvalidManifest)
	case errors.Is(err, extensions.ErrArchiveExpectationMismatch):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeExpectedMismatch)
	case errors.Is(err, extensions.ErrArtifactMissing):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeArtifactMissing)
	case errors.Is(err, extensions.ErrMissingArtifactCleanupInvalid):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeMissingArtifactCleanupInvalid)
	case errors.Is(err, extensions.ErrMissingArtifactCleanupUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeMissingArtifactCleanupUnavailable)
	case errors.Is(err, extensions.ErrExtensionNotFound):
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeNotFound)
	case errors.Is(err, extensions.ErrExtensionDisabled):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeExtensionDisabled)
	case errors.Is(err, extensions.ErrSettingsRevisionConflict):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeSettingsRevisionConflict)
	case errors.Is(err, extensions.ErrSettingsRestartUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeSettingsRestartUnavailable)
	case errors.Is(err, extensions.ErrSettingsRestartFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeSettingsRestartFailed)
	case errors.Is(err, extensions.ErrSettingsRollbackFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeSettingsRollbackFailed)
	case errors.Is(err, extensions.ErrSettingsActionInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeSettingsActionInvalid)
	case errors.Is(err, extensions.ErrSettingsActionUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeSettingsActionUnavailable)
	case errors.Is(err, extensions.ErrFrontendGrantNotFound):
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeFrontendTrustNotFound)
	case errors.Is(err, extensions.ErrFrontendTrustUnavailable),
		errors.Is(err, extensions.ErrFrontendGrantConflict),
		errors.Is(err, extensions.ErrFrontendGrantStateConflict):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeFrontendTrustNotFound)
	case errors.Is(err, extensions.ErrFrontendPackageChanged):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeFrontendPackageChanged)
	case errors.Is(err, extensions.ErrPreflightFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodePreflightFailed)
	case errors.Is(err, extensions.ErrBuildFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeBuildFailed)
	case errors.Is(err, extensions.ErrThemeActivationRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeThemeActivationRequired)
	case errors.Is(err, extensions.ErrThemeRuntimeUnavailable):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeThemeRuntimeUnavailable)
	case errors.Is(err, extensions.ErrThemePreviewStale):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeThemePreviewStale)
	case errors.Is(err, extensions.ErrRouteNotFound):
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeRouteNotFound)
	case errors.Is(err, extensions.ErrRouteMethodNotAllowed):
		return fiber.NewError(fiber.StatusMethodNotAllowed, extensions.CodeRouteMethodNotAllowed)
	case errors.Is(err, extensions.ErrRuntimeUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeRuntimeUnavailable)
	case errors.Is(err, extensions.ErrRuntimeFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeRuntimeFailed)
	case errors.Is(err, extensions.ErrPluginRuntimePublicationConflict),
		errors.Is(err, extensions.ErrPluginRuntimePublicationNotFound),
		errors.Is(err, extensions.ErrPluginRuntimeAckConflict),
		errors.Is(err, extensions.ErrPluginRuntimeNodeLeaseLost):
		return fiber.NewError(fiber.StatusConflict, extensions.CodePluginRuntimeConflict)
	case errors.Is(err, extensions.ErrTrustGrantNotFound):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeRequired)
	case errors.Is(err, extensions.ErrCapabilityConfirmationRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeCapabilityConfirmationRequired)
	case errors.Is(err, extensions.ErrTrustChallengeRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeRequired)
	case errors.Is(err, extensions.ErrTrustChallengeInvalid):
		return fiber.NewError(fiber.StatusForbidden, extensions.CodeTrustChallengeInvalid)
	case errors.Is(err, extensions.ErrTrustChallengeExpired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeExpired)
	case errors.Is(err, extensions.ErrTrustChallengeReplayed):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeReplayed)
	case errors.Is(err, extensions.ErrTrustChallengeStale):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustChallengeStale)
	case errors.Is(err, extensions.ErrTrustNotRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeTrustNotRequired)
	case errors.Is(err, extensions.ErrCapabilityDenied):
		return fiber.NewError(fiber.StatusForbidden, extensions.CodeCapabilityDenied)
	case errors.Is(err, extensions.ErrFeaturesRequired):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeFeaturesRequired)
	case errors.Is(err, extensions.ErrNotDeletable):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeNotDeletable)
	case errors.Is(err, extensions.ErrMustDisableFirst):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeMustDisableFirst)
	case errors.Is(err, extensions.ErrMigrationFailed):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeMigrationFailed)
	case errors.Is(err, extensions.ErrSafeModeActive):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeSafeModeActive)
	case errors.Is(err, extensions.ErrLifecycleCoordinatorInvalid),
		errors.Is(err, extensions.ErrExtensionVersionInvalid),
		errors.Is(err, extensions.ErrStagedVersionInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, extensions.CodeLifecycleInvalid)
	case errors.Is(err, extensions.ErrLifecycleCoordinatorUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeLifecycleUnavailable)
	case errors.Is(err, extensions.ErrLifecycleCoordinatorActionFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, extensions.CodeLifecycleActionFailed)
	case errors.Is(err, extensions.ErrLifecycleFingerprintConflict),
		errors.Is(err, extensions.ErrLifecycleOperationInProgress),
		errors.Is(err, extensions.ErrLifecycleCoordinatorRetryRequired),
		errors.Is(err, extensions.ErrLifecycleNotRecoverable),
		errors.Is(err, extensions.ErrLifecycleRevisionConflict),
		errors.Is(err, extensions.ErrLifecycleOperationClosed),
		errors.Is(err, extensions.ErrLifecycleStateTransitionDenied),
		errors.Is(err, extensions.ErrExtensionVersionConflict),
		errors.Is(err, extensions.ErrStagedVersionConflict):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeLifecycleConflict)
	case errors.Is(err, extensions.ErrLifecycleCleanupFinalization),
		errors.Is(err, extensions.ErrLifecycleCleanupNotFinalized):
		return lifecycleCleanupFailureError(err)
	case errors.Is(err, extensions.ErrLifecycleAuthorityNotFound):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeLifecycleAuthorityGone)
	case errors.Is(err, extensions.ErrStagedVersionNotFound):
		return fiber.NewError(fiber.StatusConflict, extensions.CodeStagedVersionNotFound)
	case errors.Is(err, extensions.ErrExtensionVersionNotFound):
		return fiber.NewError(fiber.StatusNotFound, extensions.CodeVersionNotFound)
	default:
		return err
	}
}
