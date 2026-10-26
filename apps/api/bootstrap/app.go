package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionprotocol "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionProtocol"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

type API struct {
	App  *fiber.App
	Addr string
	SEO  *seoregistry.ExecutionRuntime

	closeOnce sync.Once
	close     func()
	failures  <-chan error
}

type extensionRuntime interface {
	extensions.RuntimeManager
	appevents.Publisher
	RouteTarget(extensionID string) (extensionprotocol.RouteTarget, bool)
	AcquireActiveRuntimeCall(context.Context, string, extensionruntime.RuntimeCallClass) (extensionruntime.RuntimeInstanceSnapshot, *extensionruntime.RuntimeAdmissionLease, error)
	AcquireRuntimeCall(context.Context, extensionruntime.RuntimeInstanceIdentity, extensionruntime.RuntimeCallClass) (*extensionruntime.RuntimeAdmissionLease, error)
	AdminSurfaceSnapshot(string) extensionsruntime.AdminSurfaceRegistrySnapshot
	ResolveAdminSurface(string) (extensionsruntime.AdminSurfaceContract, error)
	InvokeAdminSurface(context.Context, extensionsruntime.AdminSurfaceInvocation) (extensionsruntime.AdminSurfaceInvocationResult, error)
	Reconcile(ctx context.Context, items []extensions.Extension)
	Close(ctx context.Context)
	// SendMail 供 embed worker 的 mail.deliver 复用同一 runtime（P0 共享插件进程）。
	SendMail(ctx context.Context, extensionID string, request extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error)
	// 附件存储槽 RPC（E6.2）；与 StorageRuntime 对齐。
	extensionsruntime.StorageRuntime
	// 搜索引擎槽 RPC（search.provider）；与 SearchRuntime 对齐。
	extensionsruntime.SearchRuntime
	protocolV2ProviderBrokerSource
}

var newExtensionRuntimeManager = func(
	store extensions.Store,
	hostAPI extensionsruntime.HostAPIRegistrar,
	settings extensionsruntime.PluginSettings,
	trust extensionsruntime.RuntimeTrustSource,
	databaseLeases extensionsruntime.RuntimeDatabaseLeaseRegistry,
) extensionRuntime {
	if settings == nil {
		settings = store
	}
	return extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
			Settings:       settings,
			HostAPI:        hostAPI,
			Trust:          trust,
			DatabaseLeases: databaseLeases,
		}),
		DeliveryStore: store,
	})
}

func bindAPIExtensionRuntime(
	store extensions.Store,
	hostAPI extensionsruntime.HostAPIRegistrar,
	service *extensions.Service,
	trust extensionsruntime.RuntimeTrustSource,
	databaseLeases extensionsruntime.RuntimeDatabaseLeaseRegistry,
) extensionRuntime {
	runtime := newExtensionRuntimeManager(store, hostAPI, service, trust, databaseLeases)
	extensions.WithRuntimeManager(runtime)(service)
	return runtime
}

func NewAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) (*API, error) {
	// 分阶段装配：foundation → extension platform → domain/HTTP/worker。
	// 各阶段在失败时自行关闭已获得的资源；成功后由 API.close 统一收尾。
	return buildProductionAPI(ctx, cfg, logger)
}

func secureSessionID() string {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(fmt.Errorf("generate session id: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(token[:])
}

// shouldUseSecureCookie 委托 config 包，与 CSRF CookieSecure 共用同一判定。
func shouldUseSecureCookie(cfg config.Config) bool {
	return config.ShouldUseSecureCookie(cfg)
}

func (api *API) Close() {
	if api == nil {
		return
	}
	api.closeOnce.Do(func() {
		if api.close != nil {
			api.close()
		}
	})
}

// Failures reports terminal host-owned runtime failures that require process
// termination. Safe Mode and test instances without a coordinator return nil.
func (api *API) Failures() <-chan error {
	if api == nil {
		return nil
	}
	return api.failures
}

// identityPermissionAdapter 将 identity store 接到 Host API 权限检查。
type identityPermissionAdapter struct {
	store *identity.PostgresStore
}

func (a identityPermissionAdapter) HasPermission(ctx context.Context, userID int64, permission string) (bool, error) {
	if a.store == nil {
		return false, fmt.Errorf("identity store unavailable")
	}
	actor, err := a.store.LoadActor(ctx, userID)
	if err != nil {
		return false, err
	}
	return actor.Can(permission), nil
}

// identityUserAdapter 提供安全用户字段给 Host API。
// 扩展用户字段走 Host-owned value store 的实时权限与 Schema 校验。
type identityUserAdapter struct {
	store  *identity.PostgresStore
	fields identity.SafeUserFieldReader
}

func (a identityUserAdapter) GetUserSafe(
	ctx context.Context,
	userID int64,
	actorUserID int64,
	declaredFields []string,
) (map[string]any, error) {
	if a.store == nil {
		return nil, fmt.Errorf("identity store unavailable")
	}
	user, err := a.store.GetCurrentUser(ctx, userID)
	if err != nil {
		return nil, hostapi.ErrNotFound
	}
	projected, err := identity.ProjectSafeUser(ctx, user, actorUserID, declaredFields, a.fields)
	if err != nil {
		return nil, err
	}
	// 公开内容计数：仅当调用方显式声明 topicCount/commentCount 时才查询，
	// 避免给这条投影路径无条件加聚合查询。
	wantTopicCount, wantCommentCount := false, false
	for _, field := range declaredFields {
		switch strings.TrimSpace(field) {
		case "topicCount":
			wantTopicCount = true
		case "commentCount":
			wantCommentCount = true
		}
	}
	if wantTopicCount || wantCommentCount {
		topicCount, commentCount, err := a.store.GetUserPublicContentCounts(ctx, userID)
		if err != nil {
			return nil, err
		}
		if wantTopicCount {
			projected["topicCount"] = topicCount
		}
		if wantCommentCount {
			projected["commentCount"] = commentCount
		}
	}
	return projected, nil
}

func apiAddress(cfg config.Config) string {
	return fmt.Sprintf("%s:%s", cfg.HTTPHost, cfg.HTTPPort)
}

func newHumanVerifyService(cfg config.Config, store humanverify.Store) (*humanverify.Service, error) {
	return humanverify.NewConfiguredService(humanVerifyConfigFromConfig(cfg), store)
}

type avatarOptionsAdapter struct {
	options *options.Service
}

func (a avatarOptionsAdapter) AvatarOptions(ctx context.Context) (avatar.Options, error) {
	if a.options == nil {
		return avatar.DefaultOptions(), nil
	}
	resolved, err := a.options.AvatarOptions(ctx)
	if err != nil {
		return avatar.Options{}, err
	}
	return avatar.Options{
		AllowUpload:           resolved.AllowUpload,
		DefaultProvider:       resolved.DefaultProvider,
		GravatarBaseURL:       resolved.GravatarBaseURL,
		GravatarHashAlgorithm: resolved.GravatarHashAlgorithm,
		DefaultStaticURL:      resolved.DefaultStaticURL,
		MaxSizeKB:             resolved.MaxSizeKB,
		MaxDimension:          resolved.MaxDimension,
		AllowGIF:              resolved.AllowGIF,
		CompressEnabled:       resolved.CompressEnabled,
		TargetDimension:       resolved.TargetDimension,
		CompressQuality:       resolved.CompressQuality,
	}, nil
}

func optionsDefaultsFromConfig(cfg config.Config) options.Defaults {
	return options.Defaults{
		SiteName:                  cfg.AppName,
		SiteURL:                   cfg.AppURL,
		DefaultLocale:             cfg.AppLocale,
		SupportedLocales:          cfg.SupportedLocales,
		HumanVerificationProvider: cfg.HumanVerificationProvider,
		AltchaSecret:              cfg.AltchaSecret,
		AltchaChallengeTTL:        cfg.AltchaChallengeTTL,
		AltchaCost:                cfg.AltchaCost,
	}
}

func humanVerifyConfigFromConfig(cfg config.Config) humanverify.RuntimeConfig {
	return humanverify.RuntimeConfig{
		Provider:        cfg.HumanVerificationProvider,
		AltchaSecret:    cfg.AltchaSecret,
		AltchaTTL:       cfg.AltchaChallengeTTL,
		AltchaCost:      cfg.AltchaCost,
		RateLimit:       60,
		RateLimitWindow: time.Minute,
	}
}

// PageRouteTargetRuntime 是 Loader 目标解析所需的最小运行时能力。
// 生产装配注入完整 extensionRuntime；抽成窄接口后，合并测试可以直接复用同一条
// 准入规则（而不是在测试里重新实现一遍），行为与装配方式均未改变。
type PageRouteTargetRuntime interface {
	AcquireActiveRuntimeCall(
		context.Context, string, extensionsruntime.RuntimeCallClass,
	) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error)
}

// NewPageRouteTargetSource 把 Loader 目标解析绑定到 exact runtime admission lease。
func NewPageRouteTargetSource(runtime PageRouteTargetRuntime) pages.RouteTargetSource {
	return pageRouteTargetAdapter{runtime: runtime}
}

// NewPageRouteTargetSourceWithRoutes 组装 V2 取数所需的声明路由来源。
// 未注入时 V2 分支 fail closed（仍然只有 loopback HTTP 传输可用）。
func NewPageRouteTargetSourceWithRoutes(runtime PageRouteTargetRuntime, store pageDataRouteRecordSource) pages.RouteTargetSource {
	return pageRouteTargetAdapter{runtime: runtime, routes: pageDataRouteAdapter{store: store}}
}

// pageRouteInvoker 由运行时 Manager 实现：按 exact identity 调用 Protocol V2 路由。
// 与 loopback HTTP 不同，它不需要插件回报 BaseURL。
type pageRouteInvoker interface {
	InvokeRouteInstance(
		context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.ProtocolV2RouteRequest,
	) (extensionsruntime.ProtocolV2RouteResponse, error)
}

// pageRouteTargetAdapter 为 PageDataLoader 绑定 exact runtime admission lease。
// 注意：本适配器的传输前提是插件回报 loopback HTTP BaseURL。Protocol V2 明确
// 拒绝 loopback（`protocol_v2_client.go:463`），而 V1 已不再受支持
// （`protocol_v2_client.go:188` 返回 ErrUnsupportedProtocol），因此当前没有任何
// 运行中的插件能通过这里的 BaseURL 检查；dataSource=plugin 的页面贡献会拿到
// "plugin not enabled or runtime unavailable" 并回退。真实行为由
// IntegrationTests/TestPageLoaderGatewayHasNoTransportForProtocolV2Runtime pin 住。
type pageRouteTargetAdapter struct {
	runtime PageRouteTargetRuntime
	routes  pageDataRouteSource
}

func (a pageRouteTargetAdapter) AcquireRouteTarget(ctx context.Context, artifact pages.RuntimeArtifact) (pages.LoaderRouteTarget, bool) {
	if a.runtime == nil {
		return pages.LoaderRouteTarget{}, false
	}
	target, admission, err := a.runtime.AcquireActiveRuntimeCall(ctx, artifact.ExtensionID, extensionsruntime.RuntimeCallPage)
	if err != nil {
		return pages.LoaderRouteTarget{}, false
	}
	if artifact.ExtensionVersion != "" && target.ExtensionVersion != artifact.ExtensionVersion ||
		artifact.PackageDigest != "" && target.ArtifactDigest != artifact.PackageDigest ||
		artifact.RuntimeInstanceID != "" && target.Identity.InstanceID != artifact.RuntimeInstanceID {
		admission.Release()
		return pages.LoaderRouteTarget{}, false
	}
	if base := strings.TrimSpace(target.Target.BaseURL); base != "" {
		return pages.LoaderRouteTarget{
			BaseURL: base,
			Context: admission.Context,
			Release: admission.Release,
		}, true
	}
	// Protocol V2 明确拒绝 loopback，因此这里没有 BaseURL 是**正常**形态：
	// 只要运行时能按 exact identity 调用 V2 路由，就提供一个取数闭包作为传输。
	// 安全规则见 pageDataRouteAdapter：只有宿主拥有的 public 守卫路由可被调用。
	invoke, ok := a.protocolV2LoaderInvoker(ctx, target, artifact)
	if !ok {
		admission.Release()
		return pages.LoaderRouteTarget{}, false
	}
	return pages.LoaderRouteTarget{
		Invoke:  invoke,
		Context: admission.Context,
		Release: admission.Release,
	}, true
}

// protocolV2LoaderInvoker 绑定 exact runtime instance 与 V2 路由调用。
// 闭包只接收已解析的语义输入（route/params/locale/actor 提示），不接触 HTTP 或任意 URL。
func (a pageRouteTargetAdapter) protocolV2LoaderInvoker(
	ctx context.Context,
	target extensionsruntime.RuntimeInstanceSnapshot,
	artifact pages.RuntimeArtifact,
) (pages.LoaderDocumentFunc, bool) {
	if a.routes == nil {
		return nil, false
	}
	invoker, ok := a.runtime.(pageRouteInvoker)
	if !ok {
		return nil, false
	}
	identity := target.Identity
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return nil, false
	}
	return func(
		callCtx context.Context,
		route string,
		params map[string]string,
		locale string,
		actorID int64,
	) ([]byte, int, error) {
		declaration, ok := a.routes.PageDataRoute(callCtx, artifact.ExtensionID, fiber.MethodGet, route)
		if !ok {
			return nil, 0, pageDataRouteRefused("route is not declared as a public data route")
		}
		// 精确制品：声明必须与贡献冻结的版本/摘要一致，避免用新声明服务旧制品。
		if artifact.ExtensionVersion != "" && declaration.ExtensionVersion != artifact.ExtensionVersion {
			return nil, 0, pageDataRouteRefused("declaration version does not match the frozen artifact")
		}
		if artifact.PackageDigest != "" && declaration.PackageDigest != artifact.PackageDigest {
			return nil, 0, pageDataRouteRefused("declaration digest does not match the frozen artifact")
		}
		query := map[string]string{}
		if locale != "" {
			query["locale"] = locale
		}
		// actor 提示：loader 不携带身份，因此这里只作为**非授权**提示透传，
		// 插件不得据此判定权限（与 HTTP 传输的 X-SForum-Actor-ID-Hint 同义）。
		if actorID > 0 {
			query["actorHint"] = strconv.FormatInt(actorID, 10)
		}
		response, err := invoker.InvokeRouteInstance(callCtx, identity, extensionsruntime.ProtocolV2RouteRequest{
			RouteID: declaration.RouteID, ContractVersion: declaration.ContractVersion,
			RouteAction: declaration.RouteAction,
			// 终端阶段：数据取数由插件处理器返回完整文档。
			InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
			Method:          fiber.MethodGet, Path: route,
			PathParameters: params, QueryParameters: query,
			RequestSchema: declaration.RequestSchema, ResponseSchema: declaration.ResponseSchema,
			// 过滤授权：loader 绝不转发 cookie/authorization 等凭证。
			Authority: extensionsruntime.ProtocolV2RequestAuthority{
				Mode:      extensionsruntime.ProtocolV2RequestAuthorityFiltered,
				GuardKind: extensionsruntime.ProtocolV2RequestGuardHost,
			},
		})
		if err != nil {
			return nil, 0, err
		}
		status := response.StatusCode
		if status == 0 {
			status = httpStatusOK
		}
		if !response.BodyPresent || response.Body == nil {
			return nil, status, pageDataRouteRefused("plugin returned no document body")
		}
		document, err := json.Marshal(response.Body)
		if err != nil {
			return nil, 0, pageDataRouteRefused("plugin document is not encodable")
		}
		return document, status, nil
	}, true
}

const httpStatusOK = 200

// pageDataRouteRefused 包装为 pages.ErrLoaderRouteRefused，让 gateway 把「这条数据路由
// 不允许被调用」与「调用失败」区分开（前者 503 稳定回退，后者 504）。
func pageDataRouteRefused(reason string) error {
	return fmt.Errorf("%w: %s", pages.ErrLoaderRouteRefused, reason)
}

// pageDataRouteDeclaration 是 V2 loader 需要的「已声明数据路由」视图。
//
// 为什么从扩展记录读而不是 GuardPolicyCatalog.LookupDeclaredRoute：后者只暴露
// Access/Permission，缺少 Protocol V2 路由调用必需的 RouteID / ContractVersion /
// schema 引用。
type pageDataRouteDeclaration struct {
	RouteID          string
	ContractVersion  string
	RouteAction      string
	RequestSchema    string
	ResponseSchema   string
	Guard            string
	ExtensionVersion string
	PackageDigest    string
}

// pageDataRouteSource 按方法+路径解析插件 manifest 中声明的数据路由。
type pageDataRouteSource interface {
	PageDataRoute(ctx context.Context, extensionID, method, path string) (pageDataRouteDeclaration, bool)
}

// pageDataRouteRecordSource 是 pageDataRouteAdapter 需要的最小读取面。
// 只依赖 Get（扩展记录含 manifest），避免把整个 extensions.Store 拖进适配器。
type pageDataRouteRecordSource interface {
	Get(ctx context.Context, id string) (extensions.Extension, error)
}

// pageDataRouteAdapter 从扩展记录读取 manifest 路由声明。
//
// 安全规则（R2，fail closed）：loader 是宿主发起的取数调用，**不携带 actor 身份**，
// 因此只能调用宿主可自行判定的守卫：仅接受 `core.guard.public`。
// login/permission 需要 actor（loader 不提供），custom/raw_request 守卫拥有自己的
// 授权判断，宿主不得代其判定。
type pageDataRouteAdapter struct {
	store pageDataRouteRecordSource
}

func (a pageDataRouteAdapter) PageDataRoute(
	ctx context.Context, extensionID, method, path string,
) (pageDataRouteDeclaration, bool) {
	if a.store == nil {
		return pageDataRouteDeclaration{}, false
	}
	item, err := a.store.Get(ctx, extensionID)
	if err != nil {
		return pageDataRouteDeclaration{}, false
	}
	wantedMethod := strings.ToUpper(strings.TrimSpace(method))
	wantedPath := strings.TrimSpace(path)
	if wantedPath != "" && !strings.HasPrefix(wantedPath, "/") {
		wantedPath = "/" + wantedPath
	}
	var matched *extensions.ManifestRoute
	for index := range item.Manifest.Routes {
		route := &item.Manifest.Routes[index]
		if strings.TrimSpace(route.Path) != wantedPath {
			continue
		}
		declared := false
		for _, candidate := range route.Methods {
			if strings.ToUpper(strings.TrimSpace(candidate)) == wantedMethod {
				declared = true
				break
			}
		}
		if !declared {
			continue
		}
		if matched != nil {
			// 同方法同路径重复声明：歧义即拒绝。
			return pageDataRouteDeclaration{}, false
		}
		matched = route
	}
	if matched == nil {
		return pageDataRouteDeclaration{}, false
	}
	// 只有宿主拥有的公开守卫可被 loader 直接调用。
	if strings.TrimSpace(matched.Guard) != extensionmanifest.GuardCorePublic {
		return pageDataRouteDeclaration{}, false
	}
	if strings.TrimSpace(matched.ID) == "" || strings.TrimSpace(matched.ContractVersion) == "" {
		return pageDataRouteDeclaration{}, false
	}
	return pageDataRouteDeclaration{
		RouteID: matched.ID, ContractVersion: matched.ContractVersion, RouteAction: matched.Action,
		RequestSchema: matched.RequestSchema, ResponseSchema: matched.ResponseSchema,
		Guard: matched.Guard, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
	}, true
}

// pagePackageRootAdapter 解析扩展包内容根（loader schema 文件）。
type pagePackageRootAdapter struct {
	store extensions.Store
}

func (a pagePackageRootAdapter) PackageRoot(extensionID string) (string, bool) {
	if a.store == nil {
		return "", false
	}
	item, err := a.store.Get(context.Background(), extensionID)
	if err != nil {
		return "", false
	}
	root := extensions.PackageContentRoot(item)
	if root == "" {
		return "", false
	}
	return root, true
}
