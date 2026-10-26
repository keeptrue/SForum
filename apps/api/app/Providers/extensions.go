package providers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensionscontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Extensions"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	toolplugin "github.com/keeptrue/sforum/apps/api/app/Support/AI/toolplugin"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensioncomposition "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionComposition"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionprotocol "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionProtocol"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type ExtensionsProvider struct {
	controller *extensionscontroller.Controller
}

type extensionRuntime interface {
	extensions.RuntimeManager
	RouteTarget(extensionID string) (extensionprotocol.RouteTarget, bool)
	AcquireActiveRuntimeCall(context.Context, string, extensionruntime.RuntimeCallClass) (extensionruntime.RuntimeInstanceSnapshot, *extensionruntime.RuntimeAdmissionLease, error)
}

func NewExtensionsProvider(store extensions.Store, users identity.ActorStore, sessions *authsession.Manager, extensionRoot string, builtinRoot string) *ExtensionsProvider {
	return NewExtensionsProviderWithRuntime(store, users, sessions, extensionRoot, builtinRoot, nil)
}

// NewExtensionsProviderWithRuntime 构造扩展 API；公开主题始终使用运行时 Page Registry。
func NewExtensionsProviderWithRuntime(store extensions.Store, users identity.ActorStore, sessions *authsession.Manager, extensionRoot string, builtinRoot string, runtime extensionRuntime, options ...extensions.ServiceOption) *ExtensionsProvider {
	service := extensions.NewServiceWithBuiltins(store, extensionRoot, builtinRoot)
	var gateway extensionscontroller.RouteGateway
	if runtime != nil {
		service = extensions.NewServiceWithOptions(store, extensionRoot, builtinRoot, runtime, options...)
		gateway = extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	}
	return &ExtensionsProvider{
		controller: extensionscontroller.NewControllerWithGateway(service, users, sessions, gateway).
			WithCleanupConvergence(extensions.NewCleanupConvergenceService(service)),
	}
}

func NewExtensionsProviderWithService(
	service *extensions.Service,
	users identity.ActorStore,
	sessions *authsession.Manager,
	runtime extensionRuntime,
	frontend extensionscontroller.TrustedFrontendService,
) *ExtensionsProvider {
	var gateway extensionscontroller.RouteGateway
	if runtime != nil {
		gateway = extensionRouteGateway{runtime: runtime, gateway: extensionsruntime.NewRouteGateway()}
	}
	controller := extensionscontroller.NewControllerWithGateway(service, users, sessions, gateway)
	controller.WithTrustedRuntime(frontend)
	controller.WithCleanupConvergence(extensions.NewCleanupConvergenceService(service))
	return &ExtensionsProvider{controller: controller}
}

func (p *ExtensionsProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}

func (p *ExtensionsProvider) WithRouteProviderSelection(
	api *routes.ProviderSelectionAPI,
	auditor audit.IDWriter,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRouteProviderSelection(api, auditor)
	}
	return p
}

func (p *ExtensionsProvider) WithProviderSlotSelection(
	api *extensionsruntime.ProviderSlotSelectionAPI,
	prober extensionscontroller.ProviderSlotProber,
	auditor audit.IDWriter,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithProviderSlotSelection(api, prober, auditor)
	}
	return p
}

func (p *ExtensionsProvider) WithRouteInspector(inspector *routes.Inspector) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRouteInspector(inspector)
	}
	return p
}

// WithQueryCatalog 透传密封 Core 查询目录的只读边界。
func (p *ExtensionsProvider) WithQueryCatalog(source extensionscontroller.QueryCatalogSource) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithQueryCatalog(source)
	}
	return p
}

// WithRouteIncidentReader 透传崩溃循环隔离持久状态的只读边界。
func (p *ExtensionsProvider) WithRouteIncidentReader(reader apphttp.RouteRuntimeIncidentReader) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRouteIncidentReader(reader)
	}
	return p
}

// WithDevTrustSessions 透传开发期信任会话边界；仅 development 装配调用。
func (p *ExtensionsProvider) WithDevTrustSessions(service extensionscontroller.DevTrustSessionService) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithDevTrustSessions(service)
	}
	return p
}

// WithVersionGC 透传版本 GC 管理端边界（阶段 5）；生产可用，无条件注入。
func (p *ExtensionsProvider) WithVersionGC(service extensionscontroller.VersionGCManagementService) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithVersionGC(service)
	}
	return p
}

func (p *ExtensionsProvider) WithCacheInspector(
	registry *cacheregistry.Registry,
	inspector *hostapi.HostCacheInspector,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithCacheInspector(registry, inspector)
	}
	return p
}

// WithComponentCompositionInspector wires P9 composition registry traces.
func (p *ExtensionsProvider) WithComponentCompositionInspector(
	inspector extensioncomposition.Inspector,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithComponentCompositionInspector(inspector)
	}
	return p
}

// WithNavigationInspector wires P9 navigation/region inspector traces.
func (p *ExtensionsProvider) WithNavigationInspector(
	inspector *navigationregistry.Inspector,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithNavigationInspector(inspector)
	}
	return p
}

// WithAssetInspector wires P9 Asset Registry redacted admin inspection.
func (p *ExtensionsProvider) WithAssetInspector(
	registry *assetregistry.Registry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAssetInspector(registry)
	}
	return p
}

// WithThemeRuntimeInspector wires P9 Theme Runtime redacted admin inspection.
func (p *ExtensionsProvider) WithThemeRuntimeInspector(
	registry *pages.ThemeRuntimeRegistry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithThemeRuntimeInspector(registry)
	}
	return p
}

// WithEditorRegistry wires P10 Editor Registry for public editor catalog.
func (p *ExtensionsProvider) WithEditorRegistry(
	registry *editorregistry.Registry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithEditorRegistry(registry)
	}
	return p
}

// WithEntityRegistry wires P10 Entity Registry for public entity catalog.
func (p *ExtensionsProvider) WithEntityRegistry(
	registry *entityregistry.Registry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithEntityRegistry(registry)
	}
	return p
}

// WithContentRegistry wires P10 Content Registry for public content catalog.
func (p *ExtensionsProvider) WithContentRegistry(
	registry *contentregistry.Registry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithContentRegistry(registry)
	}
	return p
}

// WithMediaRegistry wires P10 Media Registry for public media catalog.
func (p *ExtensionsProvider) WithMediaRegistry(
	registry *mediaregistry.Registry,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithMediaRegistry(registry)
	}
	return p
}

func (p *ExtensionsProvider) WithRouteContractCatalog(
	catalog extensionscontroller.RouteContractCatalog,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRouteContractCatalog(catalog)
	}
	return p
}

func (p *ExtensionsProvider) WithAdminSurfaces(
	runtime extensionscontroller.AdminSurfaceRuntime,
	auditor audit.Writer,
) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAdminSurfaces(runtime, auditor)
	}
	return p
}

// WithRemoteArchiveFetcher injects the HTTPS archive downloader used by the
// install-from-url route. Omitted means that route fails closed.
func (p *ExtensionsProvider) WithRemoteArchiveFetcher(fetcher extensionscontroller.ArchiveFetcher) *ExtensionsProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRemoteArchiveFetcher(fetcher)
	}
	return p
}

type extensionRouteGateway struct {
	runtime extensionRuntime
	gateway *extensionsruntime.RouteGateway
}

// protocolV2RouteInvoker 是「按 exact 运行时实例派发一条 V2 路由」的最小边界。
//
// 协议 v2 插件按设计不回报 HTTP BaseURL（见 Support/Extensions 的
// isPluginRouteTargetNone），因此 legacy 代理前缀只能经这条 gRPC 通道到达插件。
// 这里只声明接口，具体实现仍由运行时包持有，宿主不另写协议。
//
// 它与 dispatchProtocolV2 一起留在本文件（而不是拆到 extensions_route_protocol_v2.go）：
// 这两个声明要引用 Support/Extensions 的 Protocol V2 类型，而架构闸只允许既有文件
// import 那个 legacy 包；纯 HTTP→V2 的字段转换才搬到了同包新文件。
type protocolV2RouteInvoker interface {
	InvokeRouteInstance(
		context.Context,
		extensionsruntime.RuntimeInstanceIdentity,
		extensionsruntime.ProtocolV2RouteRequest,
	) (extensionsruntime.ProtocolV2RouteResponse, error)
}

func (g extensionRouteGateway) Proxy(c fiber.Ctx, input extensionscontroller.ProxyInput) error {
	target, admission, err := g.runtime.AcquireActiveRuntimeCall(c.Context(), input.Matched.Extension.ID, extensionruntime.RuntimeCallRoute)
	if err != nil {
		return errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	if !publicFrontendRuntimeMatches(input.PublicFrontendExact, target) {
		return extensionscontroller.ErrPublicFrontendBridgeStale
	}
	timeout := 3 * time.Second
	if input.Matched.Route.TimeoutMS > 0 {
		timeout = time.Duration(input.Matched.Route.TimeoutMS) * time.Millisecond
	}
	if target.Target.BaseURL == "" {
		return g.dispatchProtocolV2(c, input, target, admission, timeout)
	}
	targetPath := input.Matched.Path
	if query := c.Request().URI().QueryString(); len(query) > 0 {
		targetPath += "?" + string(query)
	}
	actorID := ""
	if input.HasActor {
		actorID = strconv.FormatInt(input.Actor.ID, 10)
	}
	return g.gateway.Proxy(&extensionprotocol.ProxyInput{
		Context:     admission.Context,
		Request:     c.Request(),
		Response:    c.Response(),
		ExtensionID: input.Matched.Extension.ID,
		ActorID:     actorID,
		Locale:      c.Get("Accept-Language"),
		TargetBase:  target.Target.BaseURL,
		TargetPath:  targetPath,
		Timeout:     timeout,
	})
}

// dispatchProtocolV2 把 legacy 代理请求改写成一次 exact-instance V2 路由调用。
//
// 只处理「运行时不提供 HTTP BaseURL」的目标：v1 插件的 HTTP 代理分支保持原样。
// 请求按声明路由的同一口径组装（声明路径 + 原 query + 类型化 body + 宿主推导的
// 请求权威 + 宿主侧 actor），响应再把状态码/头/body 写回 Fiber。
//
// 守卫不在这里：login/permission 判定由 controller 在调用本网关之前完成，本函数
// 只在「已经授权」的请求上做传输选择，不改变 401/403 语义。
func (g extensionRouteGateway) dispatchProtocolV2(
	c fiber.Ctx,
	input extensionscontroller.ProxyInput,
	target extensionsruntime.RuntimeInstanceSnapshot,
	admission *extensionruntime.RuntimeAdmissionLease,
	timeout time.Duration,
) error {
	invoker, ok := g.runtime.(protocolV2RouteInvoker)
	if !ok {
		// 运行时没有 V2 派发能力：维持原来的 fail closed 语义。
		return extensions.ErrRuntimeUnavailable
	}
	authority, err := extensionsruntime.ProtocolV2RouteRequestAuthority(
		input.Matched.Route, input.Matched.Extension.Manifest.Guards,
	)
	if err != nil {
		return errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	query, queryValues, err := protocolV2RouteQuery(string(c.Request().URI().QueryString()))
	if err != nil {
		return err
	}
	body, present, err := protocolV2RouteBody(input.Matched.Route.RequestSchema, c.Body())
	if err != nil {
		return err
	}
	var actor *extensionsruntime.ProtocolV2RouteActor
	if input.HasActor {
		actor = extensionsruntime.NewProtocolV2RouteActor(input.Actor.ID, true, input.Actor.Permissions)
	}
	callCtx, cancel := context.WithTimeout(admission.Context, timeout)
	defer cancel()
	response, err := invoker.InvokeRouteInstance(callCtx, target.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:               input.Matched.Route.ID,
		ContractVersion:       input.Matched.Route.ContractVersion,
		RouteAction:           input.Matched.Route.Action,
		InvocationStage:       extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:                c.Method(),
		Path:                  input.Matched.Path,
		Headers:               protocolV2RouteRequestHeaders(c),
		QueryParameters:       query,
		QueryParameterValues:  queryValues,
		RequestSchema:         input.Matched.Route.RequestSchema,
		ResponseSchema:        input.Matched.Route.ResponseSchema,
		MutableRequestFields:  input.Matched.Route.MutableRequestFields,
		MutableResponseFields: input.Matched.Route.MutableResponseFields,
		Body:                  body,
		BodyPresent:           present,
		Authority:             authority,
		Actor:                 actor,
		Timeout:               timeout,
	})
	if err != nil {
		return errors.Join(extensions.ErrRuntimeFailed, err)
	}
	return writeProtocolV2RouteResponse(c, response)
}

func writeProtocolV2RouteResponse(c fiber.Ctx, response extensionsruntime.ProtocolV2RouteResponse) error {
	if response.StreamFollows {
		return errors.Join(extensions.ErrRuntimeFailed, extensionsruntime.ErrProtocolV2RouteStream)
	}
	status := response.StatusCode
	if status == 0 {
		status = fiber.StatusOK
	}
	if !routes.ValidTerminalResponseStatus(extensionmanifest.RouteModeHTTP, status) {
		return errors.Join(extensions.ErrRuntimeFailed, extensionsruntime.ErrProtocolV2RouteResponseInvalid)
	}
	var payload []byte
	if response.BodyPresent {
		encoded, err := json.Marshal(response.Body)
		if err != nil {
			return errors.Join(extensions.ErrRuntimeFailed, err)
		}
		payload = encoded
	}
	// 与声明路由派发同一套响应头过滤：插件不能借 legacy 前缀写回 Link/Set-Cookie 等。
	for name, values := range routes.FilterPluginResponseHeaders(response.Headers) {
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}
	if len(payload) > 0 && len(c.Response().Header.Peek(fiber.HeaderContentType)) == 0 {
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	c.Status(status)
	return c.Send(payload)
}

func publicFrontendRuntimeMatches(
	exact *extensionscontroller.PublicFrontendBridgeIdentity,
	target extensionsruntime.RuntimeInstanceSnapshot,
) bool {
	if exact == nil {
		return true
	}
	return target.Identity.ExtensionID == exact.ExtensionID &&
		target.ExtensionVersion == exact.ExtensionVersion &&
		target.ArtifactDigest == exact.PackageDigest
}

// aiToolRuntimeInvoker 把 Support/AI 的本地派发类型翻译成扩展运行时的传输请求。
//
// AI 包不能 import 扩展运行时（后者反向依赖命令层，会成环），因此边界上做一次
// 显式翻译。翻译是逐字段的，没有隐式转换：任何一侧新增字段都必须同时改这里，
// 否则「宿主以为发出去的」与「插件实际收到的」会悄悄分叉。
//
// 它放在这里（而不是装配代码旁边）是为了让「模型侧工具接口 → 真实子进程」这条
// 链路只有一个翻译实现：bootstrap 装配与子进程集成测试用的是同一个适配器。
type aiToolRuntimeInvoker struct {
	runtime *extensionsruntime.AIToolRuntimeInvoker
}

// NewAIToolInvoker 把当前进程的插件运行时接到 Support/AI 的插件工具派发面上。
// runtime 为 nil 时返回 nil，调用方必须显式处理「没有运行时可派发」。
func NewAIToolInvoker(runtime *extensionsruntime.Manager) toolplugin.PluginToolInvoker {
	if runtime == nil {
		return nil
	}
	return &aiToolRuntimeInvoker{runtime: extensionsruntime.NewAIToolRuntimeInvoker(runtime)}
}

func (i *aiToolRuntimeInvoker) InvokeVersionedAITool(
	ctx context.Context,
	extension extensions.Extension,
	call toolplugin.PluginToolCall,
) (toolplugin.PluginToolResult, error) {
	if i == nil || i.runtime == nil {
		return toolplugin.PluginToolResult{}, errors.New("ai tool runtime is unavailable")
	}
	response, err := i.runtime.InvokeVersionedAITool(ctx, extension, extensionsruntime.VersionedAIToolRequest{
		DeclarationID:   call.DeclarationID,
		ContractVersion: call.ContractVersion,
		Handler:         call.Handler,
		Timeout:         call.Timeout,
		Name:            call.Name,
		Arguments:       call.Arguments,
		Locale:          call.Locale,
		TopicID:         call.TopicID,
		ViewerUserID:    call.ViewerUserID,
	})
	if err != nil {
		return toolplugin.PluginToolResult{}, err
	}
	return toolplugin.PluginToolResult{Content: response.Content, IsError: response.IsError}, nil
}

var _ toolplugin.PluginToolInvoker = (*aiToolRuntimeInvoker)(nil)
