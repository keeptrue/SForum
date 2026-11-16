package extensions

import (
	"encoding/json"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// M5：影响文档必须能回答「哪条路由的授权由哪个精确制品拥有」。该信息以**派生视图**
// 提供（TrustGuardAuthorizationView），因为向 TrustImpact 增加字段会改变 impact_digest，
// 而 impact_digest 是 extension_trust_grants 的身份键。
func TestGuardAuthorizationViewsJoinRoutesAndDeclarations(t *testing.T) {
	digest := strings.Repeat("b", 64)
	impact := TrustImpact{
		Routes: []extensionmanifest.ManifestRoute{{
			ID: "acme.ga.route.orders", ContractVersion: "acme.ga.route.orders@1",
			Action: extensionmanifest.RouteActionBefore, TargetID: "core.route.forum.topics",
			Path: "/api/v1/topics", Methods: []string{"GET"}, Guard: "acme.ga.guard.owner",
			Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.before",
			MutableRequestFields: []string{"/query/acme-ga-trace"},
		}, {
			ID: "acme.ga.route.read", ContractVersion: "acme.ga.route.read@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/api/v1/acme-ga", Methods: []string{"GET"},
			Guard: extensionmanifest.GuardCoreGuest, Access: "public", Fallback: "closed",
			Mode: extensionmanifest.RouteModeHTTP, Handler: "route.read",
		}},
		GuardDeclarations: []extensionmanifest.ManifestGuard{{
			ID: "acme.ga.guard.owner", ContractVersion: "acme.ga.guard.owner@1",
			Kind: extensionmanifest.GuardKindCustom, Entry: "backend/guard", Digest: digest,
			Permissions: []string{"acme.ga.manage"},
		}},
	}

	views := GuardAuthorizationViews(impact)
	if len(views) != 2 {
		t.Fatalf("views = %#v", views)
	}
	first := views[0]
	if first.RouteID != "acme.ga.route.orders" || first.GuardID != "acme.ga.guard.owner" {
		t.Fatalf("route/guard identity = %#v", first)
	}
	// EX2-05b：target 类动作必须披露「骑在哪条 Host 路由上」，否则管理员批准时
	// 只看到路径，看不到被接管的那条 Core 路由。
	if first.RouteAction != extensionmanifest.RouteActionBefore || first.TargetID != "core.route.forum.topics" {
		t.Fatalf("route target disclosure = %#v", first)
	}
	if first.GuardKind != extensionmanifest.GuardKindCustom || first.GuardDigest != digest ||
		first.GuardEntry != "backend/guard" || first.GuardContractVersion != "acme.ga.guard.owner@1" {
		t.Fatalf("guard artifact identity = %#v", first)
	}
	if first.Risk != extensionmanifest.GuardRiskExtensionCode || !first.RequiresExactTrust || first.RawRequest {
		t.Fatalf("risk classification = %#v", first)
	}
	if first.Fallback != "closed" || len(first.MutableRequestFields) != 1 || len(first.MutableResponseFields) != 0 {
		t.Fatalf("route scope/requested fields = %#v", first)
	}
	if len(first.GuardPermissions) != 1 || first.GuardPermissions[0] != "acme.ga.manage" {
		t.Fatalf("guard permissions = %#v", first.GuardPermissions)
	}
	second := views[1]
	if second.TargetID != "" {
		t.Fatalf("add route must not disclose a target: %#v", second)
	}
	if second.Risk != extensionmanifest.GuardRiskHostOwned || second.RequiresExactTrust || second.RawRequest {
		t.Fatalf("core guard classification = %#v", second)
	}
	if second.GuardDigest != "" || second.GuardEntry != "" {
		t.Fatalf("core guard must disclose no plugin artifact: %#v", second)
	}

	// 视图必须与影响文档解耦：调用派生视图不得改变任何摘要输入。
	body, err := json.Marshal(impact)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "guardAuthorization") {
		t.Fatalf("derived guard view leaked into the digested impact document")
	}
}

// 空绑定的 core.guard.raw_request 在零守卫代码下铸造原始请求权威（R3）。视图必须
// 如实标注为 raw_request，否则操作员会以为该路由仍由宿主评估。
func TestGuardAuthorizationViewsDiscloseRawRequestAuthority(t *testing.T) {
	impact := TrustImpact{Routes: []extensionmanifest.ManifestRoute{{
		ID: "acme.ga.route.raw", ContractVersion: "acme.ga.route.raw@1",
		Action: extensionmanifest.RouteActionBefore, TargetID: "core.route.forum.topics",
		Path: "/api/v1/topics", Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreRaw,
		Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.raw",
	}}}
	views := GuardAuthorizationViews(impact)
	if len(views) != 1 {
		t.Fatalf("views = %#v", views)
	}
	if views[0].Risk != extensionmanifest.GuardRiskRawRequest || !views[0].RawRequest || !views[0].RequiresExactTrust {
		t.Fatalf("raw request disclosure = %#v", views[0])
	}
	if views[0].GuardDigest != "" {
		t.Fatalf("core raw guard has no plugin artifact: %#v", views[0])
	}
}

// 展开的视图必须是拷贝：调用方改写返回值不得污染影响文档。
func TestGuardAuthorizationViewsDetachFromImpact(t *testing.T) {
	impact := TrustImpact{
		Routes: []extensionmanifest.ManifestRoute{{
			ID: "acme.ga.route.copy", ContractVersion: "acme.ga.route.copy@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/api/v1/acme-ga-copy",
			Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreGuest,
			Mode: extensionmanifest.RouteModeHTTP, Handler: "route.copy",
			MutableRequestFields: []string{"/query/acme-ga-copy"},
		}},
	}
	views := GuardAuthorizationViews(impact)
	views[0].Methods[0] = "DELETE"
	views[0].MutableRequestFields[0] = "/query/forged"
	again := GuardAuthorizationViews(impact)
	if again[0].Methods[0] != "GET" || again[0].MutableRequestFields[0] != "/query/acme-ga-copy" {
		t.Fatalf("derived view mutated the impact: %#v", again[0])
	}
}

// EX2-05b 的「后端断言」：**每一个** target 类动作都必须把 targetId 披露出来。
// 新增一个会接管既有路由的 action 而忘了披露时，这里会红——披露缺口不能靠人记。
func TestGuardAuthorizationViewsDiscloseEveryTargetAction(t *testing.T) {
	targetActions := []string{
		extensionmanifest.RouteActionAlias,
		extensionmanifest.RouteActionRewrite,
		extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionAfter,
		extensionmanifest.RouteActionFilter,
		extensionmanifest.RouteActionWrap,
		extensionmanifest.RouteActionReplace,
	}
	for _, action := range targetActions {
		impact := TrustImpact{Routes: []extensionmanifest.ManifestRoute{{
			ID: "acme.ga.route." + action, ContractVersion: "acme.ga.route." + action + "@1",
			Action: action, TargetID: "core.route.forum.topics",
			Path: "/api/v1/topics", Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreGuest,
			Access: "public", Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.x",
		}}}
		views := GuardAuthorizationViews(impact)
		if len(views) != 1 || views[0].TargetID != "core.route.forum.topics" {
			t.Fatalf("action %q did not disclose its target: %#v", action, views)
		}
	}
	// global middleware 的作用域是可选的：写明才披露，留空就是空（只覆盖自己的 terminal）。
	for target, want := range map[string]string{
		"core.route.forum.topics": "core.route.forum.topics",
		"":                        "",
	} {
		impact := TrustImpact{Routes: []extensionmanifest.ManifestRoute{{
			ID: "acme.ga.route.global", ContractVersion: "acme.ga.route.global@1",
			Action: extensionmanifest.RouteActionGlobalMiddleware, TargetID: target,
			Path: "", Methods: nil, Guard: extensionmanifest.GuardCoreGuest,
			Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
		}}}
		if views := GuardAuthorizationViews(impact); views[0].TargetID != want {
			t.Fatalf("global middleware target %q disclosed as %q", target, views[0].TargetID)
		}
	}
}

// 披露必须能穿过序列化边界：视图是给管理面读的，字段名就是契约。
func TestGuardAuthorizationViewSerializesTargetID(t *testing.T) {
	impact := TrustImpact{Routes: []extensionmanifest.ManifestRoute{{
		ID: "acme.ga.route.orders", ContractVersion: "acme.ga.route.orders@1",
		Action: extensionmanifest.RouteActionReplace, TargetID: "core.route.forum.topics",
		Path: "/api/v1/topics", Methods: []string{"POST"}, Guard: extensionmanifest.GuardCoreGuest,
		Access: "public", Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.x",
	}}}
	body, err := json.Marshal(GuardAuthorizationViews(impact))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"targetId":"core.route.forum.topics"`) {
		t.Fatalf("targetId missing from the serialized view: %s", body)
	}
	if !strings.Contains(string(body), `"routeAction":"replace"`) {
		t.Fatalf("routeAction missing from the serialized view: %s", body)
	}
}
