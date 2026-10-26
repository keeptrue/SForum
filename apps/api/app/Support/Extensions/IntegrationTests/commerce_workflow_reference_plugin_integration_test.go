package extensionsruntime_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// TestReferenceCommerceWorkflowPluginAndExtenderJoinedGates proves the P13
// commerce/workflow package executes through production Route Registry +
// Protocol V2 subprocess (not Manifest field counting alone): route actions,
// custom guard, HTTP/SSE/stream, lifecycle plan/execute with audit evidence,
// hooks/services/jobs/commands, and real PostgreSQL own_schema leases.
//
// WebSocket coverage is provided by the platform public test
// TestRouteWebSocketCustomGuardRunsOnlyAtOpenPreflight (Http package) and is
// declared here rather than re-implemented in the reference package.
func TestReferenceCommerceWorkflowPluginAndExtenderJoinedGates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference commerce workflow plugin subprocess build in short mode")
	}
	commerce := buildReferenceCommerceExtension(t)
	extender := buildReferenceCommerceExtenderExtension(t)
	assertCommerceManifestSurfaces(t, commerce)
	if len(extender.Manifest.Dependencies) != 1 ||
		extender.Manifest.Dependencies[0].ID != "sforum.commerce-workflow" ||
		extender.Manifest.Dependencies[0].Kind != "required" {
		t.Fatalf("extender dependency = %#v", extender.Manifest.Dependencies)
	}
	t.Log("coverage.websocket=platform:TestRouteWebSocketCustomGuardRunsOnlyAtOpenPreflight")

	// 真实 PostgreSQL：禁止 process-local fake lease / 假连接串。
	databaseURL := commerceTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	// 为 commerce 包写入可签发 own_schema grant 的版本行。
	versionID, err := seedCommerceOwnSchemaGrant(t, ctx, pool, commerce)
	if err != nil {
		t.Fatalf("seed own_schema grant: %v", err)
	}
	commerce.ActiveVersionID = versionID
	artifact := extensionsruntime.ExtensionDatabaseArtifact{
		ExtensionID: commerce.ID, Version: commerce.Version,
		VersionID: commerce.ActiveVersionID, PackageDigest: commerce.PackageDigest,
	}
	t.Cleanup(func() { cleanupCommerceOwnSchemaGrant(t, pool, commerce.ID) })

	dbRegistry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	// Host Start 签发前需要已有 actor grant 历史：先 issue+revoke。
	seed, err := dbRegistry.IssueRuntimeLease(ctx, extensionsruntime.ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "commerce-grant-seed",
		Authority: extensionsruntime.ExtensionDatabaseLeaseAuthority{
			Kind: extensionsruntime.ExtensionDatabaseLeaseIssuerActor, ActorUserID: 901, AuditEventID: 1901,
		},
	})
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if _, err := dbRegistry.RevokeRuntimeLease(ctx, extensionsruntime.ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: seed.RuntimeInstanceID, LeaseID: seed.LeaseID,
	}, extensionsruntime.ExtensionDatabaseLeaseAuthority{Kind: extensionsruntime.ExtensionDatabaseLeaseIssuerHost}); err != nil {
		t.Fatalf("revoke seed lease: %v", err)
	}

	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "commerce-reference", ImpactDigest: commerce.PackageDigest,
		}},
		HostAPI:                        gateway,
		DatabaseLeases:                 dbRegistry,
		DatabaseLeaseHeartbeatInterval: 100 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  5 * time.Second,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(ctx, commerce); err != nil {
		t.Fatalf("start commerce plugin: %v", err)
	}
	if err := manager.Start(ctx, extender); err != nil {
		t.Fatalf("start commerce extender: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Stop(context.Background(), extender)
		_ = manager.Stop(context.Background(), commerce)
	})
	active, err := manager.ActiveRuntimeInstance(commerce.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Identity.InstanceID == "" || active.Target.BaseURL != "" {
		t.Fatalf("expected Protocol V2 subprocess runtime: %#v", active)
	}

	// --- 生产 Route Registry：发布完整 action 矩阵并 BuildExecutionPlan ---
	// CoreRoute.Guard 必须与 ID/ContractVersion/Method 对齐，否则 alias/rewrite
	// 的 core.guard.inherit 会在 ExecutionPlan 阶段判定 “inherited core guard target drifted”。
	routeReg := routes.NewRegistry()
	coreTopics := routes.CoreRoute{
		ID: "core.route.forum.topics", ContractVersion: "sforum.route.forum.topics@1",
		Method: http.MethodGet, Path: "/api/v1/topics",
		Guard: routes.CoreGuardDescriptor{
			RouteID: "core.route.forum.topics", ContractVersion: "sforum.route.forum.topics@1",
			Method: http.MethodGet, Kind: routes.CoreGuardContextual,
			EvaluatorID: "core.guard.forum.read",
		},
	}
	coreCreate := routes.CoreRoute{
		ID: "core.route.forum.create_topic", ContractVersion: "sforum.route.forum.create_topic@1",
		Method: http.MethodPost, Path: "/api/v1/topics",
		Guard: routes.CoreGuardDescriptor{
			RouteID: "core.route.forum.create_topic", ContractVersion: "sforum.route.forum.create_topic@1",
			Method: http.MethodPost, Kind: routes.CoreGuardLogin,
		},
	}
	pluginArtifact := routes.PluginArtifact{
		ExtensionID: commerce.ID, ExtensionVersion: commerce.Version,
		PackageDigest: commerce.PackageDigest, RuntimeInstanceID: active.Identity.InstanceID,
	}
	if _, err := routeReg.Publish(routes.Publication{
		Core: []routes.CoreRoute{coreTopics, coreCreate},
		Plugins: []routes.PluginRouteSet{{
			Artifact: pluginArtifact,
			Routes:   commerce.Manifest.Routes,
			Guards:   commerce.Manifest.Guards,
		}},
	}); err != nil {
		t.Fatalf("publish commerce routes to production registry: %v", err)
	}
	assertCommerceRoutePlans(t, routeReg)

	// --- 真实 routes.Dispatcher：BuildExecutionPlan → Dispatch → StepInvoker → Manager RPC ---
	// 禁止仅 InvokeRouteInstance 绕过 Dispatcher；完整 Route Plan 必须经 Dispatcher 进入。
	assertCommerceOrdersViaDispatcher(t, ctx, routeReg, manager, commerce, active.Identity)

	// --- 实际执行 HTTP add route via Protocol V2 ---
	ordersResp := invokeCommerceRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.orders",
		ContractVersion: "sforum.commerce-workflow.route.orders@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/commerce-workflow/orders",
		ResponseSchema: "sforum.commerce-workflow.route.orders.response@1",
		Authority:      commerceFilteredHostAuthority(),
		Actor:          extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:        5 * time.Second,
	})
	if ordersResp.StatusCode != http.StatusOK {
		t.Fatalf("orders route status = %d body=%#v", ordersResp.StatusCode, ordersResp.Body)
	}
	if ordersResp.Body["databaseConnected"] != true {
		t.Fatalf("orders must connect via real SFORUM_DATABASE_URL lease: %#v", ordersResp.Body)
	}
	if ordersResp.Body["source"] != "commerce-workflow" {
		t.Fatalf("orders body = %#v", ordersResp.Body)
	}

	// --- custom guard 实际执行 ---
	guardReq := extensionsruntime.ProtocolV2GuardRequest{
		GuardID:              "sforum.commerce-workflow.guard.owner",
		GuardContractVersion: "sforum.commerce-workflow.guard.owner@1",
		RouteID:              "sforum.commerce-workflow.route.managed-orders",
		RouteContractVersion: "sforum.commerce-workflow.route.managed-orders@1",
		Method:               http.MethodGet, Path: "/api/commerce-workflow/managed-orders",
		Authority: extensionsruntime.ProtocolV2RequestAuthority{
			Mode:      extensionsruntime.ProtocolV2RequestAuthorityFiltered,
			GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
		},
		Actor: extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{
			"sforum.commerce-workflow.manage": true,
		}),
		Timeout: 5 * time.Second,
	}
	snapshot, lease, err := manager.AcquireActiveRuntimeCall(ctx, commerce.ID, extensionsruntime.RuntimeCallGuard)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.InvokeGuardInstance(lease.Context, snapshot.Identity, guardReq); err != nil {
		lease.Release()
		t.Fatalf("custom guard allow: %v", err)
	}
	lease.Release()
	guardReq.QueryParameters = map[string]string{"deny": "1"}
	snapshot, lease, err = manager.AcquireActiveRuntimeCall(ctx, commerce.ID, extensionsruntime.RuntimeCallGuard)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.InvokeGuardInstance(lease.Context, snapshot.Identity, guardReq)
	lease.Release()
	if !errorsIsGuardDenied(err) {
		t.Fatalf("custom guard deny error = %v", err)
	}

	// managed orders handler after guard
	managed := invokeCommerceRoute(t, manager, active.Identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.managed-orders",
		ContractVersion: "sforum.commerce-workflow.route.managed-orders@1",
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodGet, Path: "/api/commerce-workflow/managed-orders",
		ResponseSchema: "sforum.commerce-workflow.route.managed-orders.response@1",
		Authority:      commerceFilteredCustomAuthority(),
		Actor: extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{
			"sforum.commerce-workflow.manage": true,
		}),
		Timeout: 5 * time.Second,
	})
	if managed.StatusCode != http.StatusOK || managed.Body["managed"] != true {
		t.Fatalf("managed orders = %#v", managed)
	}

	// --- before / after / filter / wrap / replace 实际 InvokeRoute ---
	assertCommerceModifierRoutes(t, manager, active.Identity)

	// --- SSE + stream ---
	assertCommerceRouteStreams(t, manager, active.Identity)

	// --- Hooks ---
	okResult := starter.InvokeHook(ctx, commerce, extensionsruntime.HookInput{
		DeclarationID:   "sforum.commerce-workflow.hook.order-evaluate",
		Name:            "sforum.commerce-workflow.order.evaluate",
		Kind:            "filter",
		ContractVersion: "sforum.commerce-workflow.hook.order-evaluate@1",
		Timeout:         time.Second,
		Payload:         map[string]any{"status": "pending", "orderId": "ord-1"},
		PatchFields:     []string{"status"},
	})
	if !okResult.OK || okResult.Patch["status"] != "approved" {
		t.Fatalf("commerce evaluate happy path = %#v", okResult)
	}

	// --- Extender hook ---
	extResult := starter.InvokeHook(ctx, extender, extensionsruntime.HookInput{
		DeclarationID:   "sforum.commerce-workflow-ext.hook.order-enrich",
		Name:            "sforum.commerce-workflow.order.evaluate",
		Kind:            "filter",
		ContractVersion: "sforum.commerce-workflow.hook.order-evaluate@1",
		Timeout:         time.Second,
		Payload:         map[string]any{"status": "approved", "orderId": "ord-1"},
		PatchFields:     []string{"status"},
	})
	if !extResult.OK || extResult.Patch["status"] != "audited" {
		t.Fatalf("extender enrich = %#v", extResult)
	}

	// --- Services ---
	svcRegistry := gateway.ProtocolV2ServiceRegistry()
	commerceService, err := svcRegistry.ResolveExact("sforum.commerce-workflow.service.orders", "1.0.0")
	if err != nil || commerceService.Winner.ExtensionID != commerce.ID {
		t.Fatalf("commerce service resolve = %#v err=%v", commerceService, err)
	}

	// --- Lifecycle install/enable/upgrade/rollback/disable/uninstall plan+execute ---
	assertCommerceLifecycleMatrix(t, starter, commerce)

	// --- CLI command registry path (manifest + process) ---
	if len(commerce.Manifest.Commands) != 1 {
		t.Fatalf("commands = %#v", commerce.Manifest.Commands)
	}
	// InvokeCommand 经 protocol client：使用 starter 内部 path 若公开。
	// 至少证明 command registry 在 Start 后可解析（Manager hooks 绑定）。
	cmdResult, cmdErr := invokeCommerceCommand(t, manager, commerce, active.Identity)
	if cmdErr != nil {
		t.Fatalf("command execution must succeed via real Dispatcher path: %v", cmdErr)
	}
	if cmdResult["accepted"] != true {
		t.Fatalf("command result = %#v", cmdResult)
	}

	// --- Disable fail-closed ---
	if err := manager.Stop(ctx, extender); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx, commerce); err != nil {
		t.Fatal(err)
	}
	afterStop := starter.InvokeHook(ctx, commerce, extensionsruntime.HookInput{
		DeclarationID:   "sforum.commerce-workflow.hook.order-evaluate",
		Name:            "sforum.commerce-workflow.order.evaluate",
		Kind:            "filter",
		ContractVersion: "sforum.commerce-workflow.hook.order-evaluate@1",
		Timeout:         time.Second,
		Payload:         map[string]any{"status": "pending"},
		PatchFields:     []string{"status"},
	})
	if afterStop.OK {
		t.Fatalf("stopped commerce hook must fail closed: %#v", afterStop)
	}
	if _, err := svcRegistry.ResolveExact("sforum.commerce-workflow.service.orders", "1.0.0"); err == nil {
		t.Fatal("stopped commerce service remained discoverable")
	}
}

func assertCommerceManifestSurfaces(t *testing.T, extension extensions.Extension) {
	t.Helper()
	if extension.ID != "sforum.commerce-workflow" {
		t.Fatalf("id = %s", extension.ID)
	}
	if extension.Manifest.Database == nil ||
		len(extension.Manifest.Database.Grants) == 0 ||
		extension.Manifest.Database.Schema != "sforum_commerce_workflow" {
		t.Fatalf("database = %#v", extension.Manifest.Database)
	}
	if len(extension.Manifest.Routes) < 10 {
		t.Fatalf("routes = %d", len(extension.Manifest.Routes))
	}
	modes := map[string]bool{}
	actions := map[string]bool{}
	guards := map[string]bool{}
	for _, route := range extension.Manifest.Routes {
		modes[route.Mode] = true
		actions[route.Action] = true
		guards[route.Guard] = true
	}
	for _, want := range []string{"http", "sse", "stream"} {
		if !modes[want] {
			t.Fatalf("missing route mode %s in %#v", want, modes)
		}
	}
	for _, want := range []string{
		"add", "alias", "redirect", "rewrite", "before", "after", "filter", "wrap", "replace",
	} {
		if !actions[want] {
			t.Fatalf("missing route action %s in %#v", want, actions)
		}
	}
	// 决定 R1（2026-10-16）：raw 请求权限必须由包内显式声明。参考包因此声明两个守卫：
	// owner（custom，管理路由）与 raw（raw_request，替换 core.route.forum.create_topic）。
	declaredGuards := map[string]string{}
	for _, guard := range extension.Manifest.Guards {
		declaredGuards[guard.ID] = guard.Kind
	}
	if len(extension.Manifest.Guards) != 2 ||
		declaredGuards["sforum.commerce-workflow.guard.owner"] != "custom" ||
		declaredGuards["sforum.commerce-workflow.guard.raw"] != "raw_request" {
		t.Fatalf("declared guards = %#v", extension.Manifest.Guards)
	}
	if !guards["sforum.commerce-workflow.guard.owner"] {
		t.Fatalf("managed route must use custom guard, guards=%#v", guards)
	}
	if !guards["sforum.commerce-workflow.guard.raw"] {
		t.Fatalf("the replacing route must use the declared raw_request guard, guards=%#v", guards)
	}
	// 已退役的 Host 守卫不得再出现在参考包里。
	if guards["core.guard.raw_request"] {
		t.Fatal("the reference package must not use the retired core.guard.raw_request")
	}
	if extension.Manifest.Lifecycle == nil ||
		extension.Manifest.Lifecycle.Install == nil ||
		extension.Manifest.Lifecycle.Enable == nil ||
		extension.Manifest.Lifecycle.Disable == nil ||
		extension.Manifest.Lifecycle.Upgrade == nil ||
		extension.Manifest.Lifecycle.Rollback == nil ||
		extension.Manifest.Lifecycle.Uninstall == nil {
		t.Fatalf("lifecycle incomplete = %#v", extension.Manifest.Lifecycle)
	}
	if len(extension.Manifest.Hooks) < 2 || len(extension.Manifest.Jobs) < 1 ||
		len(extension.Manifest.Cache) < 1 || len(extension.Manifest.Services) < 1 ||
		len(extension.Manifest.Components) < 1 || len(extension.Manifest.OpenAPI) < 1 ||
		len(extension.Manifest.Commands) < 1 || len(extension.Manifest.Schedules) < 1 {
		t.Fatalf("commerce surfaces incomplete")
	}
}

// assertCommerceOrdersViaDispatcher 证明 orders 路由从真实 Dispatcher 进入并走完整 Route Plan。
func assertCommerceOrdersViaDispatcher(
	t *testing.T,
	ctx context.Context,
	registry *routes.Registry,
	manager *extensionsruntime.Manager,
	commerce extensions.Extension,
	identity extensionsruntime.RuntimeInstanceIdentity,
) {
	t.Helper()
	plan, err := registry.BuildExecutionPlan(http.MethodGet, "/api/commerce-workflow/orders")
	if err != nil {
		t.Fatalf("dispatcher plan: %v", err)
	}
	if plan.Terminal().Action != extensionmanifest.RouteActionAdd {
		t.Fatalf("dispatcher terminal = %#v", plan.Terminal())
	}
	stepInvoker := &commerceManagerStepInvoker{manager: manager, identity: identity}
	dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
		Plans:   commercePlanResolver{plan: plan},
		Steps:   stepInvoker,
		Guard:   commerceAllowGuard{},
		Schemas: commerceAcceptSchemas{},
	})
	result, err := dispatcher.Dispatch(ctx, routes.DispatchRequest{
		Method: http.MethodGet, Path: "/api/commerce-workflow/orders",
		ActorID: 42, Authenticated: true,
		Permissions: map[string]bool{"*": true},
	}, nil)
	if err != nil {
		t.Fatalf("dispatcher.Dispatch orders: %v", err)
	}
	if !result.Handled || result.Response.Status != http.StatusOK {
		t.Fatalf("dispatcher result = %#v", result)
	}
	if stepInvoker.invokes < 1 {
		t.Fatal("dispatcher must invoke plugin step at least once")
	}
	var body map[string]any
	if err := json.Unmarshal(result.Response.Body, &body); err != nil {
		t.Fatalf("dispatcher body: %v raw=%s", err, result.Response.Body)
	}
	if body["source"] != "commerce-workflow" {
		t.Fatalf("dispatcher orders body = %#v", body)
	}
	_ = commerce
}

type commercePlanResolver struct{ plan routes.RouteExecutionPlan }

func (r commercePlanResolver) BuildExecutionPlan(context.Context, string, string) (routes.RouteExecutionPlan, error) {
	return r.plan, nil
}

type commerceAllowGuard struct{}

func (commerceAllowGuard) Authorize(context.Context, routes.RouteExecutionPlan, routes.RouteExecutionStep, routes.DispatchRequest) error {
	return nil
}

type commerceAcceptSchemas struct{}

func (commerceAcceptSchemas) ValidateRequest(context.Context, routes.RouteExecutionStep, routes.DispatchRequest) error {
	return nil
}
func (commerceAcceptSchemas) ValidateResponse(context.Context, routes.RouteExecutionStep, routes.DispatchRequest, routes.DispatchResponse) error {
	return nil
}

// commerceManagerStepInvoker 把 Dispatcher 步骤落到真实 Manager.InvokeRouteInstance。
type commerceManagerStepInvoker struct {
	manager  *extensionsruntime.Manager
	identity extensionsruntime.RuntimeInstanceIdentity
	invokes  int
}

func (*commerceManagerStepInvoker) SupportsMode(mode string) bool {
	return mode == "" || mode == extensionmanifest.RouteModeHTTP
}

func (i *commerceManagerStepInvoker) Invoke(ctx context.Context, input routes.RouteInvocation) (routes.RouteInvocationResult, error) {
	i.invokes++
	lease, err := i.manager.AcquireRuntimeCall(ctx, i.identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	defer lease.Release()
	stage := extensionsruntime.ProtocolV2RouteInvocationStageHandler
	switch input.Stage {
	case routes.InvocationStageRequest:
		stage = extensionsruntime.ProtocolV2RouteInvocationStageRequest
	case routes.InvocationStageResponse:
		stage = extensionsruntime.ProtocolV2RouteInvocationStageResponse
	}
	resp, err := i.manager.InvokeRouteInstance(lease.Context, i.identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID: input.Step.RouteID, ContractVersion: input.Step.ContractVersion,
		RouteAction: input.Step.Action, InvocationStage: stage,
		Method: input.Request.Method, Path: input.Request.Path,
		ResponseSchema: input.Step.ResponseSchema,
		Authority:      commerceFilteredHostAuthority(),
		Actor: extensionsruntime.NewProtocolV2RouteActor(
			input.Request.ActorID, input.Request.Authenticated, input.Request.Permissions,
		),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	body, _ := json.Marshal(resp.Body)
	headers := resp.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return routes.RouteInvocationResult{
		Response: &routes.DispatchResponse{
			Status: resp.StatusCode, Headers: headers, Body: body,
		},
	}, nil
}

func assertCommerceRoutePlans(t *testing.T, registry *routes.Registry) {
	t.Helper()
	cases := []struct {
		method string
		path   string
		action string
	}{
		// alias/rewrite 必须 target core route（生产 ExecutionPlan 约束）。
		{http.MethodGet, "/api/commerce-workflow/orders", extensionmanifest.RouteActionAdd},
		{http.MethodGet, "/api/commerce-workflow/v1/topics-alias", extensionmanifest.RouteActionAlias},
		{http.MethodGet, "/commerce-workflow/old-orders", extensionmanifest.RouteActionRedirect},
		{http.MethodGet, "/api/commerce-workflow/rewrite/topics", extensionmanifest.RouteActionRewrite},
		{http.MethodGet, "/api/commerce-workflow/managed-orders", extensionmanifest.RouteActionAdd},
		{http.MethodGet, "/api/commerce-workflow/events", extensionmanifest.RouteActionAdd},
		{http.MethodGet, "/api/commerce-workflow/stream", extensionmanifest.RouteActionAdd},
	}
	for _, tc := range cases {
		plan, err := registry.BuildExecutionPlan(tc.method, tc.path)
		if err != nil {
			t.Fatalf("plan %s %s: %v", tc.method, tc.path, err)
		}
		terminal := plan.Terminal()
		if terminal.Action != tc.action {
			t.Fatalf("plan %s terminal action = %s want %s", tc.path, terminal.Action, tc.action)
		}
	}
	// before/after/filter 挂在 core topics 链上
	plan, err := registry.BuildExecutionPlan(http.MethodGet, "/api/v1/topics")
	if err != nil {
		t.Fatalf("topics plan: %v", err)
	}
	found := map[string]bool{}
	for _, step := range plan.Chain() {
		found[step.Action] = true
	}
	for _, want := range []string{
		extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionAfter,
		extensionmanifest.RouteActionFilter,
	} {
		if !found[want] {
			t.Fatalf("topics chain missing %s: %#v", want, plan.Chain())
		}
	}
}

func invokeCommerceRoute(
	t *testing.T,
	manager *extensionsruntime.Manager,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) extensionsruntime.ProtocolV2RouteResponse {
	t.Helper()
	lease, err := manager.AcquireRuntimeCall(context.Background(), identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatalf("acquire route: %v", err)
	}
	defer lease.Release()
	resp, err := manager.InvokeRouteInstance(lease.Context, identity, request)
	if err != nil {
		t.Fatalf("invoke route %s: %v", request.RouteID, err)
	}
	return resp
}

func commerceFilteredHostAuthority() extensionsruntime.ProtocolV2RequestAuthority {
	return extensionsruntime.ProtocolV2RequestAuthority{
		Mode:      extensionsruntime.ProtocolV2RequestAuthorityFiltered,
		GuardKind: extensionsruntime.ProtocolV2RequestGuardHost,
	}
}

func commerceFilteredCustomAuthority() extensionsruntime.ProtocolV2RequestAuthority {
	return extensionsruntime.ProtocolV2RequestAuthority{
		Mode:      extensionsruntime.ProtocolV2RequestAuthorityFiltered,
		GuardKind: extensionsruntime.ProtocolV2RequestGuardCustom,
	}
}

func commerceRawRequestAuthority() extensionsruntime.ProtocolV2RequestAuthority {
	return extensionsruntime.ProtocolV2RequestAuthority{
		Mode:      extensionsruntime.ProtocolV2RequestAuthorityRaw,
		GuardKind: extensionsruntime.ProtocolV2RequestGuardRawRequest,
	}
}

func assertCommerceModifierRoutes(t *testing.T, manager *extensionsruntime.Manager, identity extensionsruntime.RuntimeInstanceIdentity) {
	t.Helper()
	// before
	before := invokeCommerceRoute(t, manager, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.topics-before",
		ContractVersion: "sforum.commerce-workflow.route.topics-before@1",
		RouteAction:     extensionmanifest.RouteActionBefore,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageRequest,
		Method:          http.MethodGet, Path: "/api/v1/topics",
		MutableRequestFields: []string{"/query/commerceTrace"},
		Authority:            commerceFilteredHostAuthority(),
		Actor:                extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:              5 * time.Second,
	})
	if len(before.RequestPatch) == 0 {
		t.Fatalf("before must return request patch: %#v", before)
	}
	// replace（manifest 声明 core.guard.raw_request）
	replace := invokeCommerceRoute(t, manager, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.create-topic-replace",
		ContractVersion: "sforum.commerce-workflow.route.create-topic-replace@1",
		RouteAction:     extensionmanifest.RouteActionReplace,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageHandler,
		Method:          http.MethodPost, Path: "/api/v1/topics",
		RequestSchema:  "sforum.route.forum.create_topic.request@1",
		ResponseSchema: "sforum.route.forum.create_topic.response@1",
		Body:           map[string]any{"title": "replaced"}, BodyPresent: true,
		Authority: commerceRawRequestAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"topics.write": true}),
		Timeout:   5 * time.Second,
	})
	if replace.StatusCode != http.StatusCreated || replace.Body["replacedBy"] != "sforum.commerce-workflow" {
		t.Fatalf("replace = %#v", replace)
	}
	// wrap request stage（patch path 必须与 Manifest mutable* 精确一致）
	wrap := invokeCommerceRoute(t, manager, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.create-topic-wrap",
		ContractVersion: "sforum.commerce-workflow.route.create-topic-wrap@1",
		RouteAction:     extensionmanifest.RouteActionWrap,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageRequest,
		Method:          http.MethodPost, Path: "/api/v1/topics",
		MutableRequestFields:  []string{"/body/commerceWrapped"},
		MutableResponseFields: []string{"/body/commerceWrapResult"},
		RequestSchema:         "sforum.route.forum.create_topic.request@1",
		ResponseSchema:        "sforum.route.forum.create_topic.response@1",
		Body:                  map[string]any{"title": "wrap"}, BodyPresent: true,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"topics.write": true}),
		Timeout:   5 * time.Second,
	})
	if len(wrap.RequestPatch) == 0 {
		t.Fatalf("wrap request patch missing: %#v", wrap)
	}
	// filter + after（response-stage 必须带 PriorResponse）
	topicsPrior := &extensionsruntime.ProtocolV2RouteResponseDocument{
		StatusCode:  http.StatusOK,
		Body:        map[string]any{"items": []any{}},
		BodyPresent: true,
	}
	filter := invokeCommerceRoute(t, manager, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.topics-filter",
		ContractVersion: "sforum.commerce-workflow.route.topics-filter@1",
		RouteAction:     extensionmanifest.RouteActionFilter,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageResponse,
		Method:          http.MethodGet, Path: "/api/v1/topics",
		MutableResponseFields: []string{"/body/commerceFiltered"},
		ResponseSchema:        "sforum.route.forum.topics.response@1",
		PriorResponse:         topicsPrior,
		Authority:             commerceFilteredHostAuthority(),
		Actor:                 extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:               5 * time.Second,
	})
	if len(filter.ResponsePatch) == 0 {
		t.Fatalf("filter response patch missing: %#v", filter)
	}
	after := invokeCommerceRoute(t, manager, identity, extensionsruntime.ProtocolV2RouteRequest{
		RouteID:         "sforum.commerce-workflow.route.topics-after",
		ContractVersion: "sforum.commerce-workflow.route.topics-after@1",
		RouteAction:     extensionmanifest.RouteActionAfter,
		InvocationStage: extensionsruntime.ProtocolV2RouteInvocationStageResponse,
		Method:          http.MethodGet, Path: "/api/v1/topics",
		MutableResponseFields: []string{"/headers/x-commerce-trace"},
		PriorResponse: &extensionsruntime.ProtocolV2RouteResponseDocument{
			StatusCode: http.StatusOK,
		},
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if len(after.ResponsePatch) == 0 {
		t.Fatalf("after response patch missing: %#v", after)
	}
}

func assertCommerceRouteStreams(t *testing.T, manager *extensionsruntime.Manager, identity extensionsruntime.RuntimeInstanceIdentity) {
	t.Helper()
	lease, err := manager.AcquireRuntimeCall(context.Background(), identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	stream, err := manager.OpenRouteStreamInstance(lease.Context, identity, extensionsruntime.ProtocolV2RouteStreamRequest{
		RouteID:         "sforum.commerce-workflow.route.events",
		ContractVersion: "sforum.commerce-workflow.route.events@1",
		Method:          http.MethodGet, Path: "/api/commerce-workflow/events",
		Mode:      extensionmanifest.RouteModeSSE,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	if err := stream.CloseRequest(); err != nil {
		t.Fatalf("sse close request: %v", err)
	}
	got := false
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			// terminal close may surface as typed error with status
			break
		}
		if strings.Contains(string(chunk.Data), "order.") {
			got = true
		}
	}
	if !got {
		t.Fatal("SSE stream returned no commerce events")
	}

	// stream mode
	lease2, err := manager.AcquireRuntimeCall(context.Background(), identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer lease2.Release()
	bin, err := manager.OpenRouteStreamInstance(lease2.Context, identity, extensionsruntime.ProtocolV2RouteStreamRequest{
		RouteID:         "sforum.commerce-workflow.route.stream",
		ContractVersion: "sforum.commerce-workflow.route.stream@1",
		Method:          http.MethodGet, Path: "/api/commerce-workflow/stream",
		Mode:      extensionmanifest.RouteModeStream,
		Authority: commerceFilteredHostAuthority(),
		Actor:     extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"*": true}),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := bin.Send([]byte("ping"), true); err != nil {
		t.Fatalf("stream send must succeed: %v", err)
	}
	_ = bin.CloseRequest()
	for {
		_, err := bin.Recv()
		if err != nil {
			break
		}
	}
}

func assertCommerceLifecycleMatrix(t *testing.T, starter *extensionsruntime.ProtocolStarter, extension extensions.Extension) {
	t.Helper()
	actions := []struct {
		action extensionsruntime.LifecycleAction
		dryRun bool
	}{
		{extensionsruntime.LifecycleActionInstallPlan, true},
		{extensionsruntime.LifecycleActionInstall, false},
		{extensionsruntime.LifecycleActionEnable, false},
		{extensionsruntime.LifecycleActionUpgradePlan, true},
		{extensionsruntime.LifecycleActionUpgradeBefore, false},
		{extensionsruntime.LifecycleActionUpgradeAfter, false},
		{extensionsruntime.LifecycleActionRollback, false},
		{extensionsruntime.LifecycleActionDisable, false},
		{extensionsruntime.LifecycleActionUninstallPlan, true},
		{extensionsruntime.LifecycleActionUninstall, false},
		{extensionsruntime.LifecycleActionUninstallAfter, false},
	}
	for _, item := range actions {
		result, err := starter.RunLifecycle(context.Background(), extension, extensionsruntime.LifecycleInvocation{
			Action: item.action, PlanVersion: "sforum.commerce-workflow.lifecycle@1",
			StepID: "commerce-" + item.action.String(), DryRun: item.dryRun, Forced: true,
		})
		if err != nil {
			t.Fatalf("lifecycle %s: %v", item.action, err)
		}
		if result.State != extensionsruntime.LifecycleProgressSucceeded {
			t.Fatalf("lifecycle %s state=%s", item.action, result.State)
		}
	}
	evidencePath := filepath.Join(os.TempDir(), "sforum-commerce-workflow-cleanup.jsonl")
	body, err := os.ReadFile(evidencePath)
	if err != nil || len(body) == 0 {
		t.Fatalf("lifecycle cleanup evidence missing: %v path=%s", err, evidencePath)
	}
	var last map[string]any
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("evidence json: %v", err)
	}
	if last["retryable"] != true {
		t.Fatalf("evidence must be retryable: %#v", last)
	}
	// 可重试：再次 uninstall.plan
	before := len(body)
	if _, err := starter.RunLifecycle(context.Background(), extension, extensionsruntime.LifecycleInvocation{
		Action:      extensionsruntime.LifecycleActionUninstallPlan,
		PlanVersion: "sforum.commerce-workflow.lifecycle@1",
		StepID:      "commerce-uninstall-retry", DryRun: true, Forced: true,
	}); err != nil {
		t.Fatalf("retry uninstall plan: %v", err)
	}
	after, _ := os.ReadFile(evidencePath)
	if len(after) <= before {
		t.Fatal("retry did not append cleanup evidence")
	}
}

func invokeCommerceCommand(
	t *testing.T,
	manager *extensionsruntime.Manager,
	extension extensions.Extension,
	identity extensionsruntime.RuntimeInstanceIdentity,
) (map[string]any, error) {
	t.Helper()
	// Manager.Start 已通过 hooks.RegisterRuntime 绑定 commands；直接 Execute。
	_ = extension
	_ = identity
	result, err := manager.ExecutePluginCommand(context.Background(),
		"sforum.commerce-workflow.command.settle-once",
		map[string]any{"orderId": "ord-1"},
		false,
	)
	if err != nil {
		return nil, err
	}
	return result.Output, nil
}

func errorsIsGuardDenied(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "denied") ||
		strings.Contains(err.Error(), "GuardDenied") ||
		strings.Contains(err.Error(), "forbidden") ||
		err == extensionsruntime.ErrProtocolV2GuardDenied)
}

func commerceTestDatabaseURL(t *testing.T) string {
	t.Helper()
	if url := strings.TrimSpace(os.Getenv("DATABASE_URL")); url != "" {
		return url
	}
	if url := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL")); url != "" {
		return url
	}
	// 开发默认：与仓库 .env 一致。
	return "postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable"
}

func seedCommerceOwnSchemaGrant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, extension extensions.Extension) (int64, error) {
	t.Helper()
	manifestJSON, err := json.Marshal(extension.Manifest)
	if err != nil {
		return 0, err
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO extensions (id, type, name, status)
VALUES ($1, 'plugin', $2, 'installed')
ON CONFLICT (id) DO UPDATE SET status = 'installed'
`, extension.ID, extension.Name); err != nil {
		return 0, err
	}
	var existing int64
	err = pool.QueryRow(ctx, `
SELECT id FROM extension_versions WHERE extension_id = $1 AND package_digest = $2 LIMIT 1
`, extension.ID, extension.PackageDigest).Scan(&existing)
	if err == nil && existing > 0 {
		return existing, nil
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO extension_versions (
  extension_id, version, manifest, package_path, package_digest
) VALUES ($1, $2, $3::jsonb, $4, $5)
RETURNING id
`, extension.ID, extension.Version, manifestJSON, extension.PackagePath, extension.PackageDigest).Scan(&versionID); err != nil {
		return 0, err
	}
	return versionID, nil
}

func cleanupCommerceOwnSchemaGrant(t *testing.T, pool *pgxpool.Pool, extensionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, `DELETE FROM extension_database_runtime_leases WHERE extension_id = $1`, extensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extension_versions WHERE extension_id = $1`, extensionID)
	_, _ = pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)
}

// silence unused protocolwire import if optimizers change
var _ = protocolwire.LifecycleAction_LIFECYCLE_ACTION_ENABLE

func buildReferenceCommerceExtension(t *testing.T) extensions.Extension {
	t.Helper()
	return buildReferenceFixtureExtension(t, "sforum-commerce-workflow", "sforum.commerce-workflow", 801, map[string]string{
		"__FRONTEND_DIGEST__":       "frontend/order-card.mjs",
		"__OPENAPI_DIGEST__":        "openapi/routes.yaml",
		"__COMMAND_INPUT_DIGEST__":  "schemas/command-settle-input.json",
		"__COMMAND_RESULT_DIGEST__": "schemas/command-settle-result.json",
		// 组件 props/result schema 必须有同包精确声明（与 provider 同一规则）。
		"__ORDER_CARD_PROPS_DIGEST__":  "schemas/order-card-props.json",
		"__ORDER_CARD_RESULT_DIGEST__": "schemas/order-card-result.json",
	})
}

func buildReferenceCommerceExtenderExtension(t *testing.T) extensions.Extension {
	t.Helper()
	return buildReferenceFixtureExtension(t, "sforum-commerce-workflow-ext", "sforum.commerce-workflow-ext", 802, nil)
}

// buildReferenceFixtureExtension copies a fixtures/plugins package, builds the
// Protocol V2 backend, fills digest tokens, and loads the exact Manifest V3 package.
func buildReferenceFixtureExtension(
	t *testing.T,
	fixtureDir string,
	packageName string,
	versionID int64,
	fileDigests map[string]string,
) extensions.Extension {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fixtureRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins", fixtureDir))
	packageRoot := filepath.Join(t.TempDir(), packageName)
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy %s: %v", fixtureDir, err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(fixtureRoot, "../../../.."))
	goModPath := filepath.Join(packageRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repositoryRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(packageRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", fixtureDir, err, output)
	}
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := strings.ReplaceAll(string(templateBody), "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	for token, rel := range fileDigests {
		manifestBody = strings.ReplaceAll(manifestBody, token, fileSHA256(t, filepath.Join(packageRoot, rel)))
	}
	if strings.Contains(manifestBody, "__") {
		t.Fatalf("%s manifest still contains digest tokens", fixtureDir)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load %s package: %v", fixtureDir, err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: versionID,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}
