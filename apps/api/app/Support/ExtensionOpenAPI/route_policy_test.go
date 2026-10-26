package extensionopenapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestHostRoutePoliciesUseExactHostOwnedDefaults(t *testing.T) {
	manifest := extensionmanifest.Manifest{
		OpenAPI: []extensionmanifest.ManifestOpenAPIFragment{{ID: "policy.openapi"}},
		Routes: []extensionmanifest.ManifestRoute{
			{ID: "policy.public", Action: extensionmanifest.RouteActionAdd, Methods: []string{"GET", "POST"}, Guard: extensionmanifest.GuardCorePublic},
			{ID: "policy.permission", Action: extensionmanifest.RouteActionReplace, Methods: []string{"PATCH"}, Guard: extensionmanifest.GuardCorePermission},
			{ID: "policy.inherit", Action: extensionmanifest.RouteActionReplace, Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreInherit},
			{ID: "policy.raw", Action: extensionmanifest.RouteActionAdd, Methods: []string{"DELETE"}, Guard: extensionmanifest.GuardCoreRaw},
			{ID: "policy.custom", Action: extensionmanifest.RouteActionAdd, Methods: []string{"PUT"}, Guard: "policy.custom.guard"},
			{ID: "policy.before", Action: extensionmanifest.RouteActionBefore, Methods: []string{"GET"}, Guard: extensionmanifest.GuardCoreInherit},
		},
	}

	policies := HostRoutePolicies(manifest)
	if len(policies) != 6 {
		t.Fatalf("policies = %#v", policies)
	}
	wantSecurity := map[string]string{
		"policy.public\x00GET":       SecurityPublic,
		"policy.public\x00POST":      SecurityPublic,
		"policy.permission\x00PATCH": SecurityAuthenticated,
		"policy.inherit\x00GET":      SecurityHostInherited,
		"policy.raw\x00DELETE":       SecurityPluginOwned,
		"policy.custom\x00PUT":       SecurityPluginOwned,
	}
	for _, policy := range policies {
		key := routeMethodKey(policy.RouteID, policy.Method)
		if policy.Security != wantSecurity[key] || policy.RateLimit != PolicyDisabled || policy.Idempotency != PolicyDisabled {
			t.Fatalf("policy %q = %#v", key, policy)
		}
		delete(wantSecurity, key)
	}
	if len(wantSecurity) != 0 {
		t.Fatalf("missing policies = %#v", wantSecurity)
	}
}

func TestHostRoutePoliciesRemainEmptyWithoutOpenAPIFragments(t *testing.T) {
	manifest := extensionmanifest.Manifest{Routes: []extensionmanifest.ManifestRoute{{
		ID: "policy.undocumented", Action: extensionmanifest.RouteActionAdd,
		Methods: []string{"GET"}, Guard: extensionmanifest.GuardCorePublic,
	}}}
	if policies := HostRoutePolicies(manifest); len(policies) != 0 {
		t.Fatalf("undocumented route policies = %#v", policies)
	}
}

func TestCoreOperationsExpandWildcardForOpenAPICollisionSafety(t *testing.T) {
	catalog := []routes.CoreRoute{
		{ID: "core.route.health", Path: "/api/v1/health", Method: "GET"},
		{ID: "core.route.extension_proxy", Path: "/api/v1/extensions/:extensionID/*path", Method: "*"},
	}
	operations := CoreOperations(catalog)
	if len(operations) != 1+len(openAPIMethodOrder) {
		t.Fatalf("operations = %#v", operations)
	}
	seen := make(map[string]bool)
	for _, operation := range operations {
		if operation.RouteID != "core.route.extension_proxy" {
			continue
		}
		if seen[operation.Method] || operation.OperationID != operation.RouteID+"."+strings.ToLower(operation.Method) {
			t.Fatalf("wildcard operation = %#v", operation)
		}
		seen[operation.Method] = true
	}
	if len(seen) != len(openAPIMethodOrder) {
		t.Fatalf("wildcard methods = %#v", seen)
	}
	if _, err := Build(BuildInput{Core: operations}); err != nil {
		t.Fatalf("expanded core catalog: %v", err)
	}

	options := defaultFixtureOptions("collision.legacy-proxy")
	options.path = "/api/v1/extensions/{extensionID}/{path}"
	options.manifestPath = "/api/v1/extensions/:extensionID/*path"
	fixture := buildFixture(t, options)
	if _, err := Build(BuildInput{Core: operations, Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrCollision) {
		t.Fatalf("wildcard collision error = %v", err)
	}
}

func TestBuildPublishesNonStandardSecurityOwnership(t *testing.T) {
	tests := []struct {
		name     string
		guard    string
		action   string
		targetID string
		want     string
	}{
		{name: "inherited", guard: extensionmanifest.GuardCoreInherit, action: extensionmanifest.RouteActionReplace, targetID: "core.route.demo", want: SecurityHostInherited},
		// 决定 R1：raw 权限只能来自包内显式声明的守卫，因此这里的载体是 fixture 自己
		// 声明的 kind=raw_request 守卫（不再是已退役的 core.guard.raw_request）。
		{name: "raw request", guard: fixtureRawGuardID("security.owner.raw-request"), action: extensionmanifest.RouteActionAdd, want: SecurityPluginOwned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := defaultFixtureOptions("security.owner." + strings.ReplaceAll(test.name, " ", "-"))
			options.guard, options.action, options.targetID = test.guard, test.action, test.targetID
			options.rateLimit, options.idempotency = PolicyDisabled, PolicyDisabled
			fixture := buildFixture(t, options)
			fixture.Policies = HostRoutePolicies(fixture.Manifest)
			snapshot, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
			if err != nil {
				t.Fatal(err)
			}
			generated := snapshot.GeneratedClientOperations()
			if len(generated) != 1 || generated[0].Security != test.want {
				t.Fatalf("generated operations = %#v", generated)
			}
			var document map[string]any
			if err := json.Unmarshal(snapshot.Document(), &document); err != nil {
				t.Fatal(err)
			}
			operation := document["paths"].(map[string]any)[options.path].(map[string]any)["get"].(map[string]any)
			if _, exists := operation["security"]; exists || operation[extSecurityOwner] != test.want {
				t.Fatalf("published operation = %#v", operation)
			}
		})
	}
}

func TestBuildRejectsPluginOwnedSecurityDowngradeAndStandardScheme(t *testing.T) {
	options := defaultFixtureOptions("security.owner.downgrade")
	// 同上：载体是声明式 raw_request 守卫。
	options.guard = fixtureRawGuardID(options.extensionID)
	options.rateLimit, options.idempotency = PolicyDisabled, PolicyDisabled
	fixture := buildFixture(t, options)
	fixture.Policies[0].Security = SecurityPublic
	if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("security downgrade error = %v", err)
	}

	options.document = strings.Replace(fixtureDocument(options), "    get:\n", "    get:\n      security: []\n", 1)
	fixture = buildFixture(t, options)
	fixture.Policies = HostRoutePolicies(fixture.Manifest)
	if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("plugin-owned standard security error = %v", err)
	}

	// 文档里的 x-sforum-guard 必须与清单路由使用的守卫一致，所以这里由 options.guard
	// 拼出替换目标（R1 之后载体是包内声明的 raw_request 守卫，不再是 core.guard.raw_request）。
	guardLine := "      x-sforum-guard: " + options.guard + "\n"
	options.document = strings.Replace(
		fixtureDocument(options),
		guardLine,
		guardLine+"      x-sforum-security-owner: plugin_owned\n",
		1,
	)
	if !strings.Contains(options.document, "x-sforum-security-owner: plugin_owned") {
		t.Fatal("fixture document did not gain the plugin-authored security owner")
	}
	fixture = buildFixture(t, options)
	fixture.Policies = HostRoutePolicies(fixture.Manifest)
	if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("plugin-authored security owner error = %v", err)
	}
}

func TestSchemaAggregateStripsPluginPolicyProse(t *testing.T) {
	options := defaultFixtureOptions("security.schema-only")
	options.document = strings.Replace(
		fixtureDocument(options),
		"      x-sforum-guard: core.guard.public\n",
		"      x-sforum-guard: core.guard.public\n      x-sforum-security-owner: plugin_owned\n",
		1,
	)
	fixture := buildFixture(t, options)
	fixture.Policies = nil
	snapshot, err := buildSchemaAggregate(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(snapshot.Document(), &document); err != nil {
		t.Fatal(err)
	}
	operation := document["paths"].(map[string]any)[options.path].(map[string]any)["get"].(map[string]any)
	for _, key := range []string{"security", extRateLimit, extIdempotency, extSecurityOwner} {
		if _, exists := operation[key]; exists {
			t.Fatalf("schema aggregate retained %s: %#v", key, operation)
		}
	}
}
