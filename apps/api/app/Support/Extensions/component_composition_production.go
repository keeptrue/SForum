package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
	extensioncomposition "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionComposition"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// ProductionComponentCompositionConfig wires the Host-owned composition stack.
// PermissionAuthorizer 为 nil 时仅依赖 Actor 上限（SuperAdmin / 空 permission）。
type ProductionComponentCompositionConfig struct {
	Registry             *ComponentRegistry
	Manager              *Manager
	PluginRenderer       ComponentSSRRenderer
	PermissionAuthorizer ComponentPermissionAuthorizer
	ResolvePolicy        ComponentCallPolicyResolver
}

// ProductionComponentComposition is the production-bound ComponentCompositionExecutor
// service. Bootstrap owns one instance on the lifecycle stack.
type ProductionComponentComposition struct {
	executor   *ComponentCompositionExecutor
	registry   *ComponentRegistry
	renderer   *ComponentSSRRendererProduction
	packageSSR *PackageLocalComponentSSRRenderer
}

// ComponentSSRRendererProduction is the Host Core-safe renderer. Core artifacts
// never leave the Host process; plugins are fail-closed unless PluginRenderer
// is supplied by a later transport seam.
type ComponentSSRRendererProduction struct {
	PluginRenderer ComponentSSRRenderer
}

// ComponentRuntimeAdmissionProduction acquires exact Manager leases for plugin
// artifacts and returns a no-op lease for Host Core.
type ComponentRuntimeAdmissionProduction struct {
	Manager *Manager
}

type coreComponentAdmissionLease struct {
	ctx  context.Context
	once sync.Once
}

type managerComponentAdmissionLease struct {
	lease  *RuntimeAdmissionLease
	caller context.Context
	once   sync.Once
}

// NewProductionComponentComposition builds the production executor with Core
// renderer, Manager admission, and default target bindings.
func NewProductionComponentComposition(
	config ProductionComponentCompositionConfig,
) (*ProductionComponentComposition, error) {
	if config.Registry == nil {
		return nil, fmt.Errorf("%w: component registry is required", ErrComponentCompositionInvalid)
	}
	// 未显式注入 PluginRenderer 时，使用 Host 包本地 SSR（digest-fenced 编译缓存）。
	packageSSR := NewPackageLocalComponentSSRRenderer()
	pluginRenderer := config.PluginRenderer
	if pluginRenderer == nil {
		pluginRenderer = packageSSR
	}
	renderer := &ComponentSSRRendererProduction{PluginRenderer: pluginRenderer}
	admission := &ComponentRuntimeAdmissionProduction{Manager: config.Manager}
	permissions := config.PermissionAuthorizer
	if permissions == nil {
		// Actor 上限已在 authorize 路径完成；nil 表示不额外做 live RBAC 复核。
		permissions = ComponentPermissionAuthorizerFunc(func(context.Context, int64, string) (bool, error) {
			return true, nil
		})
	}
	service := &ProductionComponentComposition{
		registry: config.Registry, renderer: renderer, packageSSR: packageSSR,
	}
	executor, err := NewComponentCompositionExecutor(ComponentCompositionExecutorConfig{
		Registry:             config.Registry,
		Renderer:             renderer,
		ResolveTarget:        service.ResolveTarget,
		Admission:            admission,
		PermissionAuthorizer: permissions,
		ResolvePolicy:        config.ResolvePolicy,
	})
	if err != nil {
		return nil, err
	}
	service.executor = executor
	return service, nil
}

// PublishPackageSSR 在组件生命周期发布成功后编译包本地 SSR 模板。
// 外部自定义 PluginRenderer 时仍会更新内部缓存，以便后续切回默认渲染器。
func (s *ProductionComponentComposition) PublishPackageSSR(extension extensions.Extension) error {
	if s == nil || s.packageSSR == nil {
		return nil
	}
	if len(extension.Manifest.Components) == 0 {
		s.packageSSR.RemoveExtension(extension.ID)
		return nil
	}
	return s.packageSSR.Publish(extension)
}

// RemovePackageSSR 在组件禁用/卸载时丢弃包本地 SSR 缓存。
func (s *ProductionComponentComposition) RemovePackageSSR(extensionID, packageDigest string) {
	if s == nil || s.packageSSR == nil {
		return
	}
	if packageDigest != "" {
		s.packageSSR.RemovePackage(packageDigest)
		return
	}
	s.packageSSR.RemoveExtension(extensionID)
}

// Compose is the stable production entrypoint for Host composition.
// Callers may omit Binding; production resolves the Host default for the target.
func (s *ProductionComponentComposition) Compose(
	ctx context.Context,
	request ComponentCompositionRequest,
) (ComponentCompositionResult, error) {
	if s == nil || s.executor == nil || s.registry == nil {
		return ComponentCompositionResult{}, ErrComponentCompositionInvalid
	}
	if request.Binding.Contract.ValidateProps == nil || request.Binding.Contract.ValidateResult == nil {
		plan, err := s.registry.ResolvePlan(request.TargetID, request.TargetContractVersion)
		if err != nil {
			return ComponentCompositionResult{}, err
		}
		binding, err := s.ResolveTarget(ctx, plan.Target)
		if err != nil {
			return ComponentCompositionResult{}, err
		}
		request.Binding = binding
	}
	return s.executor.Compose(ctx, request)
}

// InspectorTraces exposes the detached composition trace ring for admin tools.
func (s *ProductionComponentComposition) InspectorTraces() []ComponentCompositionTrace {
	if s == nil || s.executor == nil {
		return nil
	}
	return s.executor.InspectorTraces()
}

// ResolveTarget supplies Host-owned bindings. Page targets retain primary SSR
// content; validators deliberately accept any props/result until a target owner
// publishes a stricter contract.
func (s *ProductionComponentComposition) ResolveTarget(
	_ context.Context,
	target ComponentTarget,
) (ComponentTargetBinding, error) {
	// KindPage 必须保留主内容；普通 component 默认不强制 SEO 主内容围栏。
	retainPrimary := target.Kind == componentcatalog.KindPage
	if target.Kind == "" {
		// 未标注 kind 的 Core 目标按 page 语义保护主内容。
		retainPrimary = target.Core
	}
	binding := ComponentTargetBinding{
		Contract: ComponentCompositionContract{
			ValidateProps:        allowAnyComponentDocument,
			ValidateResult:       allowAnyComponentDocument,
			RetainPrimaryContent: retainPrimary,
		},
	}
	if target.Core {
		// Core 目标必须有 Host fallback；生产路径用稳定文本占位，主题 L1 仍是主 HTML。
		targetID := target.ID
		binding.Fallback = func(_ context.Context, call ComponentFallbackCall) (ComponentRenderResponse, error) {
			label := strings.TrimSpace(targetID)
			if label == "" {
				label = strings.TrimSpace(call.TargetID)
			}
			if label == "" {
				label = "core"
			}
			return ComponentRenderResponse{
				Document:  map[string]any{},
				Fragments: []ComponentRenderFragment{{Text: label, PrimaryContent: retainPrimary}},
			}, nil
		}
	}
	return binding, nil
}

// ComposePageTarget maps a Page Registry id (e.g. forum.home) to the Host
// component target (core.component.page.forum.home) and composes it. Returned
// HTML is only non-Core contribution output so theme primary content is never
// replaced by this helper.
func (s *ProductionComponentComposition) ComposePageTarget(
	ctx context.Context,
	pageID string,
	props map[string]any,
	actor ComponentActorAuthority,
) ([]string, error) {
	if s == nil || s.executor == nil || s.registry == nil {
		return nil, ErrComponentCompositionInvalid
	}
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return nil, ErrComponentCompositionInvalid
	}
	targetID := "core.component.page." + pageID
	core, ok := componentcatalog.FindCoreComponent(targetID)
	if !ok {
		// 未知 page id：fail-open（调用方视为无贡献）。
		return nil, nil
	}
	plan, err := s.registry.ResolvePlan(core.ID, core.ContractVersion)
	if err != nil {
		if errors.Is(err, ErrComponentRegistryTargetNotFound) {
			return nil, nil
		}
		return nil, err
	}
	// 无第三方贡献时跳过 Compose，避免无意义 Core fallback 开销。
	if len(plan.Contributions) == 0 && plan.ReplaceWinner == nil && plan.Target.Provider == nil {
		return nil, nil
	}
	binding, err := s.ResolveTarget(ctx, plan.Target)
	if err != nil {
		return nil, err
	}
	if props == nil {
		props = map[string]any{}
	}
	result, err := s.Compose(ctx, ComponentCompositionRequest{
		TargetID:              core.ID,
		TargetContractVersion: core.ContractVersion,
		Actor:                 actor,
		Props:                 props,
		Binding:               binding,
	})
	if err != nil {
		// SEO 主内容围栏对页面出口 fail closed。
		if errors.Is(err, ErrComponentCompositionSEO) {
			return nil, pages.ErrPageCompositionSEO
		}
		return nil, err
	}
	return componentNonCoreHTMLSegments(result.Segments), nil
}

// ComposePageHTML implements pages.PageCompositionRenderer without importing
// the pages package (Extensions already depends on Pages in lifecycle code).
func (s *ProductionComponentComposition) ComposePageHTML(
	ctx context.Context,
	pageID string,
	props map[string]any,
) ([]string, error) {
	return s.ComposePageTarget(ctx, pageID, props, ComponentActorAuthority{})
}

func (r *ComponentSSRRendererProduction) RenderComponent(
	ctx context.Context,
	call ComponentRenderCall,
) (ComponentRenderResponse, error) {
	if r == nil {
		return ComponentRenderResponse{}, ErrComponentCompositionCrash
	}
	if isHostCoreComponentArtifact(call.Artifact) {
		return coreComponentRenderResponse(call), nil
	}
	if r.PluginRenderer != nil {
		return r.PluginRenderer.RenderComponent(ctx, call)
	}
	// 插件 SSR 传输尚未接入时 fail closed，由 executor 的 FailurePolicy 决定是否 fallback。
	return ComponentRenderResponse{}, ErrComponentCompositionCrash
}

func (a *ComponentRuntimeAdmissionProduction) AcquireComponentRuntime(
	ctx context.Context,
	request ComponentRuntimeAdmissionRequest,
) (ComponentRuntimeAdmissionLease, error) {
	if ctx == nil {
		return nil, ErrComponentCompositionUnauthorized
	}
	if isHostCoreComponentArtifact(request.Artifact) {
		return &coreComponentAdmissionLease{ctx: ctx}, nil
	}
	if a == nil {
		return nil, fmt.Errorf("%w: component runtime admission is unavailable", ErrComponentCompositionUnauthorized)
	}
	identity := RuntimeInstanceIdentity{
		ExtensionID: strings.TrimSpace(request.Artifact.ExtensionID),
		InstanceID:  strings.TrimSpace(request.Artifact.RuntimeInstanceID),
	}
	if identity.ExtensionID == "" || identity.InstanceID == "" {
		return nil, fmt.Errorf("%w: plugin artifact identity is incomplete", ErrComponentCompositionUnauthorized)
	}
	// Component Registry 使用 host-component-package:* 作为声明式包身份，不绑定插件进程。
	// 包本地 SSR 可在无 Manager 时准入；若 Manager 上恰好有同名 process 则走精确 lease。
	if strings.HasPrefix(identity.InstanceID, "host-component-package:") {
		if a.Manager != nil && a.Manager.RuntimeInstanceAvailable(identity) {
			lease, err := a.Manager.AcquireRuntimeCall(ctx, identity, RuntimeCallPage)
			if err != nil {
				return nil, err
			}
			if lease == nil {
				return nil, ErrComponentCompositionUnauthorized
			}
			return &managerComponentAdmissionLease{lease: lease, caller: ctx}, nil
		}
		// 声明式组件包：Host 包本地 SSR / 无进程绑定。
		return &coreComponentAdmissionLease{ctx: ctx}, nil
	}
	if a.Manager == nil {
		return nil, fmt.Errorf("%w: component runtime manager unavailable", ErrComponentCompositionUnauthorized)
	}
	lease, err := a.Manager.AcquireRuntimeCall(ctx, identity, RuntimeCallPage)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, ErrComponentCompositionUnauthorized
	}
	return &managerComponentAdmissionLease{lease: lease, caller: ctx}, nil
}

func (l *coreComponentAdmissionLease) Context() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}

func (l *coreComponentAdmissionLease) Validate(ctx context.Context) error {
	if l == nil || l.ctx == nil {
		return ErrComponentCompositionUnauthorized
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (l *coreComponentAdmissionLease) Release() {
	if l != nil {
		l.once.Do(func() {})
	}
}

func (l *managerComponentAdmissionLease) Context() context.Context {
	if l == nil || l.lease == nil {
		return nil
	}
	return l.lease.Context
}

func (l *managerComponentAdmissionLease) Validate(ctx context.Context) error {
	if l == nil || l.lease == nil || l.lease.Context == nil {
		return ErrComponentCompositionUnauthorized
	}
	if l.caller != nil {
		if err := l.caller.Err(); err != nil {
			return err
		}
	}
	if err := l.lease.Context.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrComponentCompositionUnauthorized, err)
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (l *managerComponentAdmissionLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.lease != nil {
			l.lease.Release()
		}
	})
}

// isHostCoreComponentArtifact 识别无需插件进程的 Host Core 身份。
// HookArtifact 无 Core 字段：空 RuntimeInstanceID 即 Host core 路径。
func isHostCoreComponentArtifact(artifact HookArtifact) bool {
	if strings.TrimSpace(artifact.RuntimeInstanceID) != "" {
		return false
	}
	extensionID := strings.TrimSpace(artifact.ExtensionID)
	return extensionID == "" || extensionID == "core" || strings.HasPrefix(extensionID, "core.")
}

func coreComponentRenderResponse(call ComponentRenderCall) ComponentRenderResponse {
	response := ComponentRenderResponse{Artifact: call.Artifact}
	switch {
	case call.Contribution.Action == "filter-props":
		response.Document = call.Props
	case call.Contribution.Action == "filter-result",
		call.Contribution.Action == "wrap",
		call.Contribution.Action == "replace":
		response.Document = call.Result
	default:
		if call.Result != nil {
			response.Document = call.Result
		}
	}
	label := componentLabelFromProps(call.Props)
	if label == "" {
		label = strings.TrimSpace(call.Contribution.ID)
	}
	if label == "" {
		label = strings.TrimSpace(call.TargetID)
	}
	if label == "" {
		label = "core"
	}
	response.Fragments = []ComponentRenderFragment{{Text: label, PrimaryContent: true}}
	return response
}

func componentLabelFromProps(props map[string]any) string {
	if props == nil {
		return ""
	}
	for _, key := range []string{"label", "title", "html", "text"} {
		if value, ok := props[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func allowAnyComponentDocument(_ context.Context, _ map[string]any) error {
	return nil
}

// componentNonCoreHTMLSegments 仅提取插件贡献的 Host-sanitized HTML，保留主题主内容。
func componentNonCoreHTMLSegments(segments []ComponentRenderSegment) []string {
	result := make([]string, 0)
	var walk func([]ComponentRenderSegment)
	walk = func(items []ComponentRenderSegment) {
		for _, segment := range items {
			if segment.OwnerID != "" && segment.OwnerID != "core" {
				if html := strings.TrimSpace(segment.HTML); html != "" {
					result = append(result, segment.HTML)
				}
			}
			if len(segment.Children) > 0 {
				walk(segment.Children)
			}
		}
	}
	walk(segments)
	return result
}

// ComponentCompositionInspector projects runtime state into the stable,
// redacted admin inspection contract.
type ComponentCompositionInspector struct {
	registry    *ComponentRegistry
	composition *ProductionComponentComposition
}

func NewComponentCompositionInspector(registry *ComponentRegistry, composition *ProductionComponentComposition) *ComponentCompositionInspector {
	if registry == nil || composition == nil {
		return nil
	}
	return &ComponentCompositionInspector{registry: registry, composition: composition}
}

func (i *ComponentCompositionInspector) Inspect(limit int) extensioncomposition.Snapshot {
	result := extensioncomposition.Snapshot{Conflicts: []extensioncomposition.Conflict{}, Traces: []extensioncomposition.Trace{}}
	if i == nil || i.registry == nil || i.composition == nil {
		return result
	}
	snapshot := i.registry.Snapshot()
	result.Revision = snapshot.Revision
	result.SafeMode = snapshot.SafeMode
	result.TargetCount = len(snapshot.Targets)
	result.ContributionCount = len(snapshot.Contributions)
	for _, conflict := range snapshot.Conflicts {
		winnerID := ""
		if conflict.Winner != nil {
			winnerID = conflict.Winner.ID
		}
		result.Conflicts = append(result.Conflicts, extensioncomposition.Conflict{
			TargetID: conflict.TargetID, TargetContractVersion: conflict.TargetContractVersion,
			CandidateCount: len(conflict.Candidates), WinnerContributionID: winnerID,
			ExplicitSelection: conflict.ExplicitSelection,
		})
	}
	traces := i.composition.InspectorTraces()
	if limit > 0 && len(traces) > limit {
		traces = traces[len(traces)-limit:]
	}
	for _, trace := range traces {
		result.Traces = append(result.Traces, extensioncomposition.Trace{
			ID: trace.ID, Revision: trace.Revision, TargetID: trace.TargetID,
			TargetContractVersion: trace.TargetContractVersion, StartedAt: trace.StartedAt,
			DurationMicros: trace.DurationMicros, Status: trace.Status,
		})
	}
	return result
}
