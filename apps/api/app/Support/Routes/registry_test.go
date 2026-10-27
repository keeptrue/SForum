package routes

import (
	"errors"
	"strings"
	"sync"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRegistryPublishesCoreAndExactArtifactRoutes(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("demo.routes", "1.2.3", 'a')
	snapshot, err := registry.Publish(Publication{
		Core: []CoreRoute{coreRoute("core.route.forum.topics", "GET", "/api/v1/topics")},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			pluginRoute("demo.routes.community", "/community/:slug", 5, "GET", "PURGE"),
			pluginRoute("demo.routes.admin", "/admin/demo/:id", 0, "REPORT"),
			pluginRoute("demo.routes.api", "/api/vendor/*rest", 0, "POST"),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.SafeMode || len(snapshot.Routes) != 5 || len(snapshot.Conflicts) != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}

	tests := []struct {
		method string
		path   string
		id     string
		param  string
		value  string
	}{
		{"GET", "/api/v1/topics", "core.route.forum.topics", "", ""},
		{"GET", "/community/hello", "demo.routes.community", "slug", "hello"},
		{"PURGE", "/community/old", "demo.routes.community", "slug", "old"},
		{"REPORT", "/admin/demo/42", "demo.routes.admin", "id", "42"},
		{"POST", "/api/vendor/a/b", "demo.routes.api", "rest", "a/b"},
	}
	for _, test := range tests {
		match, err := registry.Resolve(test.method, test.path)
		if err != nil || match.Route.ID != test.id {
			t.Fatalf("resolve %s %s = %#v, %v", test.method, test.path, match, err)
		}
		if test.param != "" && match.Params[test.param] != test.value {
			t.Fatalf("params for %s = %#v", test.path, match.Params)
		}
		if match.Route.Provider.Kind == ProviderPlugin && match.Route.Provider.Artifact != artifact {
			t.Fatalf("route lost exact artifact: %#v", match.Route.Provider)
		}
		if match.Route.ID == "" || match.Route.ContractVersion == "" {
			t.Fatalf("route lost stable identity: %#v", match.Route)
		}
	}
}

func TestRegistrySpecificityAndPriorityAreDeterministic(t *testing.T) {
	registry := NewRegistry()
	base := routeArtifact("docs.base", "1.0.0", 'a')
	low := routeArtifact("docs.low", "1.0.0", 'b')
	high := routeArtifact("docs.high", "1.0.0", 'c')
	global := routeArtifact("docs.global", "1.0.0", 'd')
	_, err := registry.Publish(Publication{Plugins: []PluginRouteSet{
		{Artifact: base, Routes: []extensionmanifest.ManifestRoute{
			pluginRoute("docs.base.catchall", "/docs/*rest", 100, "GET"),
			pluginRoute("docs.base.param", "/docs/:slug", 0, "GET"),
			pluginRoute("docs.base.static", "/docs/archive", -100, "GET"),
		}},
		{Artifact: low, Routes: []extensionmanifest.ManifestRoute{
			modifierRoute("docs.low.before", "docs.base.static", "/docs/archive", extensionmanifest.RouteActionBefore, "GET", 10),
		}},
		{Artifact: high, Routes: []extensionmanifest.ManifestRoute{
			modifierRoute("docs.high.before", "docs.base.static", "/docs/archive", extensionmanifest.RouteActionBefore, "GET", 20),
		}},
		{Artifact: global, Routes: []extensionmanifest.ManifestRoute{{
			ID: "docs.global.middleware", ContractVersion: "docs.global.middleware@1",
			Action: extensionmanifest.RouteActionGlobalMiddleware, Priority: 15,
			Guard: extensionmanifest.GuardCoreLogin, Mode: extensionmanifest.RouteModeHTTP, Fallback: "closed",
			Handler: "route.global", RequestSchema: "docs.global.middleware.request@1",
			ResponseSchema: "docs.global.middleware.response@1",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	staticMatch, err := registry.Resolve("GET", "/docs/archive")
	if err != nil || staticMatch.Route.ID != "docs.base.static" {
		t.Fatalf("static match = %#v, %v", staticMatch, err)
	}
	if got := routeIDs(staticMatch.Contributions); strings.Join(got, ",") != "docs.high.before,docs.global.middleware,docs.low.before" {
		t.Fatalf("priority order = %#v", got)
	}
	paramMatch, err := registry.Resolve("GET", "/docs/guide")
	if err != nil || paramMatch.Route.ID != "docs.base.param" || paramMatch.Params["slug"] != "guide" {
		t.Fatalf("param match = %#v, %v", paramMatch, err)
	}
	catchAll, err := registry.Resolve("GET", "/docs/a/b")
	if err != nil || catchAll.Route.ID != "docs.base.catchall" || catchAll.Params["rest"] != "a/b" {
		t.Fatalf("catch-all match = %#v, %v", catchAll, err)
	}
}

func TestRegistryPreservesFiberHEADFallbackAndExplicitOverride(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Publish(Publication{Core: []CoreRoute{
		coreRoute("core.route.head.fallback", "GET", "/head/fallback"),
		coreRoute("core.route.head.get", "GET", "/head/explicit"),
		coreRoute("core.route.head.explicit", "HEAD", "/head/explicit"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := registry.Resolve("HEAD", "/head/fallback")
	if err != nil || fallback.Route.ID != "core.route.head.fallback" || fallback.Route.Method != "GET" {
		t.Fatalf("HEAD fallback = %#v, %v", fallback, err)
	}
	explicit, err := registry.Resolve("HEAD", "/head/explicit")
	if err != nil || explicit.Route.ID != "core.route.head.explicit" || explicit.Route.Method != "HEAD" {
		t.Fatalf("explicit HEAD = %#v, %v", explicit, err)
	}

	artifact := routeArtifact("head.stream", "1.0.0", 'a')
	stream := pluginRoute("head.stream.events", "/head/events", 0, "GET")
	stream.Mode = extensionmanifest.RouteModeSSE
	if _, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{stream}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("HEAD", "/head/events"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("HEAD must not enter an SSE stream: %v", err)
	}
}

func TestRegistryPublishesEveryV1ActionAndTransportMode(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("complete.routes", "1.0.0", 'a')

	alias := pluginRoute("complete.routes.alias", "/alias", 0, "GET")
	alias.Action, alias.TargetID, alias.Handler, alias.ResponseSchema = extensionmanifest.RouteActionAlias, "core.route.complete.home", "", ""
	redirect := pluginRoute("complete.routes.redirect", "/old", 0, "GET")
	redirect.Action, redirect.Handler, redirect.ResponseSchema, redirect.Destination = extensionmanifest.RouteActionRedirect, "", "", "/new"
	rewrite := pluginRoute("complete.routes.rewrite", "/rewrite", 0, "GET")
	rewrite.Action, rewrite.TargetID, rewrite.Handler, rewrite.ResponseSchema = extensionmanifest.RouteActionRewrite, "core.route.complete.home", "", ""
	replace := modifierRoute("complete.routes.replace", "core.route.complete.create", "/topics", extensionmanifest.RouteActionReplace, "POST", 100)
	global := extensionmanifest.ManifestRoute{
		ID: "complete.routes.global", ContractVersion: "complete.routes.global@1",
		Action: extensionmanifest.RouteActionGlobalMiddleware, Guard: extensionmanifest.GuardCoreLogin,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
		RequestSchema: "complete.routes.global.request@1", ResponseSchema: "complete.routes.global.response@1",
	}
	sse := pluginRoute("complete.routes.sse", "/events", 0, "GET")
	sse.Mode = extensionmanifest.RouteModeSSE
	websocket := pluginRoute("complete.routes.websocket", "/socket", 0, "GET")
	websocket.Mode = extensionmanifest.RouteModeWebSocket
	multipart := pluginRoute("complete.routes.multipart", "/upload", 0, "POST")
	multipart.Mode = extensionmanifest.RouteModeMultipart
	stream := pluginRoute("complete.routes.stream", "/stream", 0, "GET")
	stream.Mode = extensionmanifest.RouteModeStream

	snapshot, err := registry.Publish(Publication{
		Core: []CoreRoute{
			coreRoute("core.route.complete.home", "GET", "/home"),
			coreRoute("core.route.complete.topics", "GET", "/topics"),
			coreRoute("core.route.complete.create", "POST", "/topics"),
		},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			pluginRoute("complete.routes.add", "/custom", 0, "GET"), alias, redirect, rewrite,
			modifierRoute("complete.routes.before", "core.route.complete.topics", "/topics", extensionmanifest.RouteActionBefore, "GET", 40),
			modifierRoute("complete.routes.after", "core.route.complete.topics", "/topics", extensionmanifest.RouteActionAfter, "GET", 30),
			modifierRoute("complete.routes.filter", "core.route.complete.topics", "/topics", extensionmanifest.RouteActionFilter, "GET", 20),
			modifierRoute("complete.routes.wrap", "core.route.complete.topics", "/topics", extensionmanifest.RouteActionWrap, "GET", 10),
			replace, global, sse, websocket, multipart, stream,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictProviderSelection {
		t.Fatalf("conflicts = %#v", snapshot.Conflicts)
	}

	addressable := map[string]string{
		"/custom":  extensionmanifest.RouteActionAdd,
		"/alias":   extensionmanifest.RouteActionAlias,
		"/old":     extensionmanifest.RouteActionRedirect,
		"/rewrite": extensionmanifest.RouteActionRewrite,
		"/events":  extensionmanifest.RouteActionAdd,
		"/socket":  extensionmanifest.RouteActionAdd,
		"/upload":  extensionmanifest.RouteActionAdd,
		"/stream":  extensionmanifest.RouteActionAdd,
	}
	for path, action := range addressable {
		method := "GET"
		if path == "/upload" {
			method = "POST"
		}
		match, err := registry.Resolve(method, path)
		if err != nil || match.Route.Action != action || match.Route.Provider.Artifact != artifact {
			t.Fatalf("resolve %s %s = %#v, %v", method, path, match, err)
		}
	}

	topics, err := registry.Resolve("GET", "/topics")
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, route := range topics.Contributions {
		actions[route.Action] = true
	}
	for _, action := range []string{
		extensionmanifest.RouteActionBefore, extensionmanifest.RouteActionAfter,
		extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap,
		extensionmanifest.RouteActionGlobalMiddleware,
	} {
		if !actions[action] {
			t.Fatalf("missing %q contribution: %#v", action, topics.Contributions)
		}
	}
	if match, err := registry.Resolve("POST", "/topics"); !errors.Is(err, ErrAmbiguousRoute) || len(match.Candidates) != 2 {
		t.Fatalf("replace candidates = %#v, %v", match, err)
	}
}

func TestRegistryPublishesInspectableConflictsButResolvesFailClosed(t *testing.T) {
	registry := NewRegistry()
	first := routeArtifact("conflict.first", "1.0.0", 'a')
	second := routeArtifact("conflict.second", "1.0.0", 'b')
	snapshot, err := registry.Publish(Publication{Plugins: []PluginRouteSet{
		{Artifact: first, Routes: []extensionmanifest.ManifestRoute{pluginRoute("conflict.first.route", "/shared/:id", 10, "GET")}},
		{Artifact: second, Routes: []extensionmanifest.ManifestRoute{pluginRoute("conflict.second.route", "/shared/:slug", 20, "GET")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictPathMethod ||
		len(snapshot.Conflicts[0].Candidates) != 2 || snapshot.Conflicts[0].Candidates[0].ID != "conflict.second.route" {
		t.Fatalf("conflicts = %#v", snapshot.Conflicts)
	}
	match, err := registry.Resolve("GET", "/shared/value")
	if !errors.Is(err, ErrAmbiguousRoute) || match.Route.ID != "" || len(match.Candidates) != 2 {
		t.Fatalf("ambiguous resolution = %#v, %v", match, err)
	}
	if _, err := registry.Resolve("POST", "/shared/value"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("unknown method must fail closed: %v", err)
	}

	// Returned inspection data is detached from the immutable active snapshot.
	snapshot.Conflicts[0].Candidates[0].ID = "mutated"
	if registry.Conflicts()[0].Candidates[0].ID == "mutated" {
		t.Fatal("caller mutated active conflict snapshot")
	}
}

func TestRegistryInspectsWildcardAndConcreteMethodOverlaps(t *testing.T) {
	registry := NewRegistry()
	wildcard := routeArtifact("method.wildcard", "1.0.0", 'a')
	get := routeArtifact("method.get", "1.0.0", 'b')
	post := routeArtifact("method.post", "1.0.0", 'c')
	snapshot, err := registry.Publish(Publication{Plugins: []PluginRouteSet{
		{Artifact: wildcard, Routes: []extensionmanifest.ManifestRoute{pluginRoute("method.wildcard.route", "/method-overlap", 0, "*")}},
		{Artifact: get, Routes: []extensionmanifest.ManifestRoute{pluginRoute("method.get.route", "/method-overlap", 0, "GET")}},
		{Artifact: post, Routes: []extensionmanifest.ManifestRoute{pluginRoute("method.post.route", "/method-overlap", 0, "POST")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 2 {
		t.Fatalf("wildcard conflict count = %d: %#v", len(snapshot.Conflicts), snapshot.Conflicts)
	}
	for index, method := range []string{"GET", "POST"} {
		conflict := snapshot.Conflicts[index]
		if conflict.Kind != ConflictPathMethod || conflict.Method != method || len(conflict.Candidates) != 2 {
			t.Fatalf("%s wildcard conflict = %#v", method, conflict)
		}
		ids := strings.Join(routeIDs(conflict.Candidates), ",")
		if !strings.Contains(ids, "method.wildcard.route") {
			t.Fatalf("%s conflict omitted wildcard candidate: %#v", method, conflict.Candidates)
		}
	}
}

func TestRegistryInspectsWildcardStableIdentityOverlap(t *testing.T) {
	wildcard := coreRoute("core.route.method.shared", "*", "/method-identity/wildcard")
	concrete := coreRoute("core.route.method.shared", "GET", "/method-identity/concrete")
	snapshot, err := NewRegistry().Publish(Publication{Core: []CoreRoute{wildcard, concrete}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictRouteIdentity ||
		snapshot.Conflicts[0].Method != "GET" || len(snapshot.Conflicts[0].Candidates) != 2 {
		t.Fatalf("wildcard identity conflict = %#v", snapshot.Conflicts)
	}
}

func TestRegistryRequiresExplicitReplaceProviderSelection(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("replace.demo", "2.0.0", 'a')
	replacement := modifierRoute(
		"replace.demo.create", "core.route.forum.create_topic", "/api/v1/topics",
		extensionmanifest.RouteActionReplace, "POST", 100,
	)
	snapshot, err := registry.Publish(Publication{
		Core:    []CoreRoute{coreRoute("core.route.forum.create_topic", "POST", "/api/v1/topics")},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictProviderSelection ||
		snapshot.Conflicts[0].RouteID != "core.route.forum.create_topic" {
		t.Fatalf("provider conflicts = %#v", snapshot.Conflicts)
	}
	match, err := registry.Resolve("POST", "/api/v1/topics")
	if !errors.Is(err, ErrAmbiguousRoute) || len(match.Candidates) != 2 {
		t.Fatalf("replace resolution = %#v, %v", match, err)
	}
}

func TestRegistryStableIdentityAmbiguityFailsClosed(t *testing.T) {
	registry := NewRegistry()
	first := coreRoute("core.route.demo.shared", "GET", "/first")
	second := coreRoute("core.route.demo.shared", "GET", "/second")
	second.ContractVersion = "sforum.route.demo.shared@2"
	second.Guard.ContractVersion = second.ContractVersion
	snapshot, err := registry.Publish(Publication{Core: []CoreRoute{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].Kind != ConflictRouteIdentity {
		t.Fatalf("identity conflicts = %#v", snapshot.Conflicts)
	}
	match, err := registry.Resolve("GET", "/first")
	if !errors.Is(err, ErrAmbiguousRoute) || match.Route.ID != "" || len(match.Candidates) != 2 {
		t.Fatalf("stable identity ambiguity = %#v, %v", match, err)
	}
}

func TestRegistrySafeModeFiltersPluginsBeforeValidation(t *testing.T) {
	registry := NewRegistry()
	snapshot, err := registry.Publish(Publication{
		SafeMode: true,
		Core:     []CoreRoute{coreRoute("core.route.system.health", "GET", "/api/v1/health")},
		Plugins: []PluginRouteSet{{
			Artifact: PluginArtifact{ExtensionID: "broken"},
			Routes:   []extensionmanifest.ManifestRoute{{Action: "unknown", Path: "not-a-path"}},
		}},
	})
	if err != nil {
		t.Fatalf("safe mode must ignore broken plugin snapshot: %v", err)
	}
	if !snapshot.SafeMode || len(snapshot.Routes) != 1 || snapshot.Routes[0].Provider.Kind != ProviderCore {
		t.Fatalf("safe snapshot = %#v", snapshot)
	}
	if _, err := registry.Resolve("GET", "/api/v1/health"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("GET", "/broken"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("safe mode exposed plugin route: %v", err)
	}
}

func TestRegistryAcceptsAuthoritativeP0CoreCatalog(t *testing.T) {
	core := p0CoreCatalog(t)
	registry := NewRegistry()
	snapshot, err := registry.Publish(Publication{Core: core})
	if err != nil {
		t.Fatal(err)
	}
	if len(core) < 200 || len(snapshot.Routes) != len(core) || len(snapshot.Conflicts) != 0 {
		t.Fatalf("P0 catalog snapshot routes=%d conflicts=%#v", len(snapshot.Routes), snapshot.Conflicts)
	}
	match, err := registry.Resolve("GET", "/api/v1/admin/pages/added")
	if err != nil || match.Route.ID != "core.route.pages.admin_added" {
		t.Fatalf("static P0 route did not beat :pageId: %#v, %v", match, err)
	}
	match, err = registry.Resolve("PURGE", "/api/v1/extensions/demo.plugin/cache")
	if err != nil || match.Route.ID != "core.route.extensions.proxy_extension_route" {
		t.Fatalf("P0 wildcard method/catch-all route = %#v, %v", match, err)
	}
}

func p0CoreCatalog(tb testing.TB) []CoreRoute {
	tb.Helper()
	return CoreRouteCatalog()
}

func TestCoreRouteCatalogReturnsImmutableCopies(t *testing.T) {
	first := CoreRouteCatalog()
	second := CoreRouteCatalog()
	if len(first) == 0 || len(second) != len(first) {
		t.Fatalf("catalog lengths = %d, %d", len(first), len(second))
	}
	originalID := second[0].ID
	originalPermission := ""
	for index := range first {
		if len(first[index].Guard.Permissions) > 0 {
			originalPermission = second[index].Guard.Permissions[0]
			first[index].Guard.Permissions[0] = "mutated.permission"
			if CoreRouteCatalog()[index].Guard.Permissions[0] != originalPermission {
				t.Fatal("caller mutation changed generated core guard permissions")
			}
			break
		}
	}
	if originalPermission == "" {
		t.Fatal("generated catalog has no permission-bearing guard")
	}
	first[0].ID = "mutated"
	if second[0].ID != originalID || CoreRouteCatalog()[0].ID != originalID {
		t.Fatal("caller mutation changed the generated core route catalog")
	}
}

func TestRegistryDetachesCoreGuardPermissionsAtEveryReadBoundary(t *testing.T) {
	registry := NewRegistry()
	route := coreRoute("core.route.guard.immutable", "PATCH", "/guard/immutable")
	route.Guard = CoreGuardDescriptor{
		RouteID: route.ID, ContractVersion: route.ContractVersion, Method: route.Method,
		Kind: CoreGuardContextual, Permissions: []string{"topic.edit_own", "topic.edit_any"},
		EvaluatorID: "core.guard.forum.topic_edit",
	}
	publication := Publication{Core: []CoreRoute{route}}
	published, err := registry.Publish(publication)
	if err != nil {
		t.Fatal(err)
	}

	publication.Core[0].Guard.Permissions[0] = "mutated.input"
	published.Routes[0].CoreGuard.Permissions[0] = "mutated.publish_result"
	assertResolvedGuardPermission(t, registry, "topic.edit_own")

	snapshot := registry.Snapshot()
	snapshot.Routes[0].CoreGuard.Permissions[0] = "mutated.snapshot"
	assertResolvedGuardPermission(t, registry, "topic.edit_own")

	publicationSnapshot := registry.PublicationSnapshot()
	publicationSnapshot.Publication.Core[0].Guard.Permissions[0] = "mutated.publication_snapshot"
	if got := registry.PublicationSnapshot().Publication.Core[0].Guard.Permissions[0]; got != "topic.edit_own" {
		t.Fatalf("publication snapshot permission = %q", got)
	}

	match, err := registry.Resolve("PATCH", "/guard/immutable")
	if err != nil {
		t.Fatal(err)
	}
	match.Route.CoreGuard.Permissions[0] = "mutated.match"
	assertResolvedGuardPermission(t, registry, "topic.edit_own")

	route.Guard.Permissions = []string{"topic.edit_own", "topic.edit_any"}
	other := route
	other.Guard = cloneCoreGuardDescriptor(route.Guard)
	other.ID = "core.route.guard.immutable_other"
	other.ContractVersion = "sforum.route.guard.immutable_other@1"
	other.Guard.RouteID = other.ID
	other.Guard.ContractVersion = other.ContractVersion
	if _, err := registry.Publish(Publication{Core: []CoreRoute{route, other}}); err != nil {
		t.Fatal(err)
	}
	ambiguous, err := registry.Resolve("PATCH", "/guard/immutable")
	if !errors.Is(err, ErrAmbiguousRoute) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous match = %#v, %v", ambiguous, err)
	}
	ambiguous.Candidates[0].CoreGuard.Permissions[0] = "mutated.candidate"
	again, err := registry.Resolve("PATCH", "/guard/immutable")
	if !errors.Is(err, ErrAmbiguousRoute) || again.Candidates[0].CoreGuard.Permissions[0] != "topic.edit_own" {
		t.Fatalf("candidate mutation escaped into registry: %#v, %v", again, err)
	}
}

func assertResolvedGuardPermission(t *testing.T, registry *Registry, want string) {
	t.Helper()
	match, err := registry.Resolve("PATCH", "/guard/immutable")
	if err != nil {
		t.Fatal(err)
	}
	if got := match.Route.CoreGuard.Permissions[0]; got != want {
		t.Fatalf("resolved guard permission = %q, want %q", got, want)
	}
}

func TestCoreRouteCatalogHasExactReviewedGuardParity(t *testing.T) {
	catalog := CoreRouteCatalog()
	// 计数由 scripts/v3-catalog/generate.mjs 与 catalog-identities 共同约束；
	// 新增路由必须先进入 identities + OpenAPI + Guard 审核矩阵，再同步此处。
	// 363 = 362 + core.route.extensions.inspect_route_incidents（M5 隔离账本读取）。
	// 367 = 363 + 4 条开发期信任会话路由（list/create/revoke/dev-trust-refresh）。
	// 370 = 367 + 3 条版本 GC 管理端路由（candidates/confirmations/execute，阶段 5）。
	// 371 = 370 + core.route.extensions.route_catalog（路由注册表只读目录）。
	// 372 = 371 + core.route.extensions.query_catalog（密封 Core 查询目录）。
	// 373 = 372 + core.route.ai.get_run_timeline（A4 观测：按 run 回放执行记录）。
	// 374 = 373 + core.route.forum.list_comment_citations（B4 公开读：评论的 AI 依据）。
	// 375 = 374 + core.route.profile.nickname_availability（昵称改名预检）。
	// 376 = 375 + core.route.extensions.install_from_url（后台链接安装）。
	// 377 = 376 + core.route.ai.list_reply_outcomes（回复结果读面：这条触发有没有
	// 变成评论。与执行记录是两个问题，因此是独立路由而不是复用 executions）。
	// 378 = 377 + core.route.extensions.finalize_uninstall_cleanup（卸载清理免键收尾）。
	// 379 = 378 + core.route.identity.create_user（后台新增用户）。
	// 380 = 379 + core.route.attachments.reconcile_admin（附件引用计数核对）。
	if len(catalog) != 380 {
		t.Fatalf("generated core route count = %d", len(catalog))
	}
	kinds := make(map[CoreGuardKind]int)
	for _, route := range catalog {
		if coreGuardDescriptorIsZero(route.Guard) {
			t.Fatalf("route %s has no reviewed guard", route.ID)
		}
		if err := validateCoreGuardDescriptor(route); err != nil {
			t.Fatalf("route %s guard: %v", route.ID, err)
		}
		kinds[route.Guard.Kind]++
	}
	for _, kind := range []CoreGuardKind{CoreGuardPublic, CoreGuardLogin, CoreGuardSuperAdmin, CoreGuardPermissionAny, CoreGuardContextual} {
		if kinds[kind] == 0 {
			t.Fatalf("generated catalog has no %s guard", kind)
		}
	}
}

func TestRegistryRejectsUnknownInputWithoutPublishing(t *testing.T) {
	registry := NewRegistry()
	valid := Publication{Core: []CoreRoute{coreRoute("core.route.forum.topics", "GET", "/api/v1/topics")}}
	if _, err := registry.Publish(valid); err != nil {
		t.Fatal(err)
	}
	artifact := routeArtifact("invalid.demo", "1.0.0", 'a')
	invalid := []Publication{
		{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{{
			ID: "invalid.demo.unknown", ContractVersion: "invalid.demo.unknown@1", Action: "unknown",
			Path: "/unknown", Methods: []string{"GET"},
		}}}}},
		{Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
			modifierRoute("invalid.demo.before", "core.route.missing", "/missing", extensionmanifest.RouteActionBefore, "POST", 1),
		}}}},
		{Plugins: []PluginRouteSet{{Artifact: PluginArtifact{ExtensionID: "invalid.demo", ExtensionVersion: "1.0.0"}, Routes: []extensionmanifest.ManifestRoute{
			pluginRoute("invalid.demo.route", "/invalid", 0, "GET"),
		}}}},
	}
	for _, input := range invalid {
		if _, err := registry.Publish(input); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("invalid publication error = %v", err)
		}
		if registry.Revision() != 1 {
			t.Fatalf("invalid publication advanced revision to %d", registry.Revision())
		}
		match, err := registry.Resolve("GET", "/api/v1/topics")
		if err != nil || match.Route.ID != "core.route.forum.topics" {
			t.Fatalf("invalid publication changed snapshot: %#v, %v", match, err)
		}
	}
}

func TestRegistryRejectsInvalidRuntimeContractsWithoutPublishing(t *testing.T) {
	registry := NewRegistry()
	valid := Publication{Core: []CoreRoute{coreRoute("core.route.forum.topics", "GET", "/api/v1/topics")}}
	if _, err := registry.Publish(valid); err != nil {
		t.Fatal(err)
	}
	artifact := routeArtifact("runtime.contracts", "1.0.0", 'a')
	base := pluginRoute("runtime.contracts.route", "/runtime", 0, "GET")
	invalid := []extensionmanifest.ManifestRoute{
		func() extensionmanifest.ManifestRoute {
			value := base
			value.Mode = extensionmanifest.RouteModeSSE
			value.Methods = []string{"POST"}
			value.RequestSchema = value.ID + ".request@1"
			return value
		}(),
		func() extensionmanifest.ManifestRoute {
			value := base
			value.Mode = extensionmanifest.RouteModeMultipart
			return value
		}(),
		func() extensionmanifest.ManifestRoute { value := base; value.Handler = ""; return value }(),
		func() extensionmanifest.ManifestRoute {
			value := base
			value.Methods = []string{"POST"}
			value.RequestSchema = ""
			return value
		}(),
		func() extensionmanifest.ManifestRoute {
			value := base
			value.Guard = extensionmanifest.GuardCorePermission
			value.Permission = ""
			return value
		}(),
		func() extensionmanifest.ManifestRoute {
			value := base
			value.Action = extensionmanifest.RouteActionRedirect
			value.Handler = ""
			value.ResponseSchema = ""
			value.Destination = "https://example.com"
			return value
		}(),
		func() extensionmanifest.ManifestRoute {
			value := base
			value.ID = "runtime.contracts.health"
			value.Path = "/api/v1/health"
			return value
		}(),
		func() extensionmanifest.ManifestRoute {
			value := modifierRoute("runtime.contracts.replace", "core.route.system.ready", "/api/v1/ready", extensionmanifest.RouteActionReplace, "GET", 1)
			return value
		}(),
	}
	for index, route := range invalid {
		if _, err := registry.Publish(Publication{Core: valid.Core, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}}}); !errors.Is(err, ErrInvalidRoute) {
			t.Fatalf("invalid route %d error = %v", index, err)
		}
		if registry.Revision() != 1 {
			t.Fatalf("invalid route %d advanced revision to %d", index, registry.Revision())
		}
	}

	customGuard := base
	customGuard.Guard = "runtime.contracts.guard.owner"
	if _, err := registry.Publish(Publication{Core: valid.Core, Plugins: []PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{customGuard},
		Guards: []extensionmanifest.ManifestGuard{pluginGuard(customGuard.Guard, "custom")},
	}}}); err != nil {
		t.Fatalf("namespaced custom guard must remain eligible after trust confirmation: %v", err)
	}
}

func TestRegistryFreezesExactCustomGuardBinding(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("guard.binding", "1.0.0", 'a')
	route := pluginRoute("guard.binding.route", "/guard-binding", 0, "GET")
	route.Guard = "guard.binding.owner"
	guard := pluginGuard(route.Guard, "raw_request")
	guard.Permissions = []string{"guard.binding.manage"}

	publication := Publication{Plugins: []PluginRouteSet{{
		Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route},
		Guards: []extensionmanifest.ManifestGuard{guard},
	}}}
	snapshot, err := registry.Publish(publication)
	if err != nil {
		t.Fatal(err)
	}
	want := PluginGuardBinding{
		ID: guard.ID, ContractVersion: guard.ContractVersion, Kind: guard.Kind,
		Entry: guard.Entry, Digest: guard.Digest, Permissions: []string{"guard.binding.manage"},
	}
	if len(snapshot.Routes) != 1 || !equalPluginGuardBinding(snapshot.Routes[0].PluginGuard, want) {
		t.Fatalf("published guard binding = %#v", snapshot.Routes)
	}

	publication.Plugins[0].Guards[0].Digest = strings.Repeat("b", 64)
	publication.Plugins[0].Guards[0].Permissions[0] = "forged.manage"
	snapshot.Routes[0].PluginGuard.Permissions[0] = "mutated.result"
	match, err := registry.Resolve("GET", route.Path)
	if err != nil || !equalPluginGuardBinding(match.Route.PluginGuard, want) {
		t.Fatalf("immutable guard binding = %#v, %v", match.Route.PluginGuard, err)
	}
	plan, err := registry.BuildExecutionPlan("GET", route.Path)
	if err != nil || !equalPluginGuardBinding(plan.Terminal().PluginGuard, want) {
		t.Fatalf("execution guard binding = %#v, %v", plan.Terminal().PluginGuard, err)
	}
}

func TestRegistryRejectsMissingOrInvalidCustomGuardBinding(t *testing.T) {
	artifact := routeArtifact("guard.invalid", "1.0.0", 'a')
	route := pluginRoute("guard.invalid.route", "/guard-invalid", 0, "GET")
	route.Guard = "guard.invalid.owner"
	valid := pluginGuard(route.Guard, "custom")
	tests := []struct {
		name   string
		guards []extensionmanifest.ManifestGuard
	}{
		{name: "missing"},
		{name: "wrong id", guards: []extensionmanifest.ManifestGuard{pluginGuard("guard.invalid.other", "custom")}},
		{name: "wrong kind", guards: []extensionmanifest.ManifestGuard{func() extensionmanifest.ManifestGuard { value := valid; value.Kind = "builtin"; return value }()}},
		{name: "wrong digest", guards: []extensionmanifest.ManifestGuard{func() extensionmanifest.ManifestGuard { value := valid; value.Digest = "changed"; return value }()}},
		{name: "duplicate", guards: []extensionmanifest.ManifestGuard{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			_, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
				Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}, Guards: test.guards,
			}}})
			if !errors.Is(err, ErrInvalidRoute) || registry.Revision() != 0 {
				t.Fatalf("error = %v, revision = %d", err, registry.Revision())
			}
		})
	}
}

func TestRegistryRejectsNonSemverArtifactVersion(t *testing.T) {
	registry := NewRegistry()
	artifact := routeArtifact("artifact.version", "latest", 'a')
	_, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: artifact,
		Routes:   []extensionmanifest.ManifestRoute{pluginRoute("artifact.version.route", "/artifact", 0, "GET")},
	}}})
	if !errors.Is(err, ErrInvalidRoute) || registry.Revision() != 0 {
		t.Fatalf("non-semver artifact error=%v revision=%d", err, registry.Revision())
	}
}

func TestRegistryRetainsIncompatibleAliasRewriteForFailClosedPlanning(t *testing.T) {
	artifact := routeArtifact("mapping.invalid", "1.0.0", 'a')
	target := coreRoute("core.route.mapping.invalid", "GET", "/target/:id")
	for _, action := range []string{extensionmanifest.RouteActionAlias, extensionmanifest.RouteActionRewrite} {
		route := pluginRoute("mapping.invalid."+action, "/legacy", 0, "GET")
		route.Action, route.TargetID, route.Guard, route.Handler, route.ResponseSchema =
			action, target.ID, extensionmanifest.GuardCoreInherit, "", ""
		registry := NewRegistry()
		if _, err := registry.Publish(Publication{
			Core: []CoreRoute{target}, Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{route}}},
		}); err != nil || registry.Revision() != 1 {
			t.Fatalf("action %s error = %v, revision = %d", action, err, registry.Revision())
		}
		plan, err := registry.BuildExecutionPlan("GET", route.Path)
		if err != nil || plan.Terminal().TargetPath != "" {
			t.Fatalf("action %s fail-closed target = %q, %v", action, plan.Terminal().TargetPath, err)
		}
	}
}

func TestRegistryRequiresCanonicalRuntimeInstanceIdentity(t *testing.T) {
	registry := NewRegistry()
	for _, instanceID := range []string{"", " runtime-1", "runtime-1 ", "runtime instance", strings.Repeat("a", 129)} {
		artifact := routeArtifact("artifact.runtime", "1.0.0", 'a')
		artifact.RuntimeInstanceID = instanceID
		_, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
			Artifact: artifact,
			Routes:   []extensionmanifest.ManifestRoute{pluginRoute("artifact.runtime.route", "/artifact-runtime", 0, "GET")},
		}}})
		if !errors.Is(err, ErrInvalidRoute) || registry.Revision() != 0 {
			t.Fatalf("instance %q error=%v revision=%d", instanceID, err, registry.Revision())
		}
	}
}

func TestRegistryPublishIfRevisionFencesStaleWriters(t *testing.T) {
	registry := NewRegistry()
	first, err := registry.PublishIfRevision(Publication{Core: []CoreRoute{
		coreRoute("core.route.atomic.first", "GET", "/atomic/first"),
	}}, 0)
	if err != nil || first.Revision != 1 {
		t.Fatalf("first publication = %#v, %v", first, err)
	}
	current, err := registry.PublishIfRevision(Publication{Core: []CoreRoute{
		coreRoute("core.route.atomic.second", "GET", "/atomic/second"),
	}}, 0)
	if !errors.Is(err, ErrRevisionConflict) || current.Revision != 1 {
		t.Fatalf("stale publication = %#v, %v", current, err)
	}
	if _, err := registry.Resolve("GET", "/atomic/first"); err != nil {
		t.Fatalf("stale writer replaced current snapshot: %v", err)
	}
	if _, err := registry.Resolve("GET", "/atomic/second"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("stale route became visible: %v", err)
	}
}

func TestRegistryPublishIfRevisionAllowsOneConcurrentWriter(t *testing.T) {
	registry := NewRegistry()
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		index := index
		go func() {
			<-start
			_, err := registry.PublishIfRevision(Publication{Core: []CoreRoute{
				coreRoute("core.route.cas.writer", "GET", "/cas/"+string(rune('0'+index))),
			}}, 0)
			results <- err
		}()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected publication error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 || registry.Revision() != 1 {
		t.Fatalf("succeeded=%d conflicted=%d revision=%d", succeeded, conflicted, registry.Revision())
	}
}

func TestRegistrySnapshotsAreImmutableAndAtomicallyReplaced(t *testing.T) {
	registry := NewRegistry()
	core := []CoreRoute{coreRoute("core.route.forum.topics", "GET", "/api/v1/topics")}
	firstArtifact := routeArtifact("atomic.demo", "1.0.0", 'a')
	first, err := registry.Publish(Publication{Core: core, Plugins: []PluginRouteSet{{
		Artifact: firstArtifact, Routes: []extensionmanifest.ManifestRoute{
			pluginRoute("atomic.demo.one", "/atomic/one", 0, "GET"),
			pluginRoute("atomic.demo.two", "/atomic/two", 0, "GET"),
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	errorsSeen := make(chan string, 8)
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				snapshot := registry.Snapshot()
				var artifact *PluginArtifact
				for _, route := range snapshot.Routes {
					if route.Provider.Kind != ProviderPlugin {
						continue
					}
					value := route.Provider.Artifact
					if artifact == nil {
						artifact = &value
					} else if *artifact != value {
						select {
						case errorsSeen <- "reader observed mixed artifacts":
						default:
						}
						return
					}
				}
			}
		}()
	}
	for index := 2; index <= 100; index++ {
		version := "2.0." + string(rune('0'+index%10))
		artifact := routeArtifact("atomic.demo", version, rune('a'+index%6))
		if _, err := registry.Publish(Publication{Core: core, Plugins: []PluginRouteSet{{
			Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{
				pluginRoute("atomic.demo.one", "/atomic/one", 0, "GET"),
				pluginRoute("atomic.demo.two", "/atomic/two", 0, "GET"),
			},
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	readers.Wait()
	close(errorsSeen)
	for message := range errorsSeen {
		t.Fatal(message)
	}
	if first.Revision != 1 || first.Routes[1].Provider.Artifact != firstArtifact {
		t.Fatalf("captured snapshot changed: %#v", first)
	}
	if registry.Revision() != 100 {
		t.Fatalf("revision = %d", registry.Revision())
	}
}

func TestRegistrySnapshotsBindRetainedAndReplacementRuntimeInstances(t *testing.T) {
	registry := NewRegistry()
	oldArtifact := routeArtifact("runtime.bound", "1.0.0", 'a')
	oldArtifact.RuntimeInstanceID = "instance-old"
	oldSnapshot, err := registry.Publish(Publication{Plugins: []PluginRouteSet{{
		Artifact: oldArtifact,
		Routes:   []extensionmanifest.ManifestRoute{pluginRoute("runtime.bound.route", "/runtime-bound", 0, "GET")},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	newArtifact := routeArtifact("runtime.bound", "2.0.0", 'b')
	newArtifact.RuntimeInstanceID = "instance-new"
	newSnapshot, err := registry.PublishIfRevision(Publication{Plugins: []PluginRouteSet{{
		Artifact: newArtifact,
		Routes:   []extensionmanifest.ManifestRoute{pluginRoute("runtime.bound.route", "/runtime-bound", 0, "GET")},
	}}}, oldSnapshot.Revision)
	if err != nil {
		t.Fatal(err)
	}

	if oldSnapshot.Routes[0].Provider.Artifact.RuntimeInstanceID != "instance-old" {
		t.Fatalf("retained snapshot changed runtime binding: %#v", oldSnapshot.Routes[0].Provider)
	}
	if newSnapshot.Routes[0].Provider.Artifact.RuntimeInstanceID != "instance-new" {
		t.Fatalf("replacement snapshot runtime binding = %#v", newSnapshot.Routes[0].Provider)
	}
	match, err := registry.Resolve("GET", "/runtime-bound")
	if err != nil || match.Route.Provider.Artifact.RuntimeInstanceID != "instance-new" {
		t.Fatalf("active route runtime binding = %#v, %v", match, err)
	}
}

func coreRoute(id, method, path string) CoreRoute {
	route := CoreRoute{ID: id, ContractVersion: "sforum." + strings.TrimPrefix(id, "core.") + "@1", Method: method, Path: path}
	route.Guard = CoreGuardDescriptor{
		RouteID: route.ID, ContractVersion: route.ContractVersion, Method: route.Method, Kind: CoreGuardPublic,
	}
	return route
}

func routeArtifact(id, version string, digest rune) PluginArtifact {
	return PluginArtifact{
		ExtensionID: id, ExtensionVersion: version, PackageDigest: strings.Repeat(string(digest), 64),
		RuntimeInstanceID: "runtime-" + string(digest),
	}
}

func pluginRoute(id, path string, priority int, methods ...string) extensionmanifest.ManifestRoute {
	route := extensionmanifest.ManifestRoute{
		ID: id, ContractVersion: id + "@1", Action: extensionmanifest.RouteActionAdd,
		Path: path, Methods: methods, Guard: extensionmanifest.GuardCorePublic,
		Priority: priority, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.handle", ResponseSchema: id + ".response@1",
	}
	if hasUnsafeMethod(methods) {
		route.RequestSchema = id + ".request@1"
	}
	return route
}

func pluginGuard(id, kind string) extensionmanifest.ManifestGuard {
	return extensionmanifest.ManifestGuard{
		ID: id, ContractVersion: id + "@1", Kind: kind,
		Entry: "backend/guard", Digest: strings.Repeat("c", 64),
	}
}

func modifierRoute(id, targetID, path, action, method string, priority int) extensionmanifest.ManifestRoute {
	return extensionmanifest.ManifestRoute{
		ID: id, ContractVersion: id + "@1", Action: action, TargetID: targetID,
		Path: path, Methods: []string{method}, Guard: extensionmanifest.GuardCoreLogin,
		Priority: priority, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
		Handler: "route.modify", RequestSchema: id + ".request@1", ResponseSchema: id + ".response@1",
	}
}

func routeIDs(routes []Route) []string {
	result := make([]string, 0, len(routes))
	for _, route := range routes {
		result = append(result, route.ID)
	}
	return result
}
