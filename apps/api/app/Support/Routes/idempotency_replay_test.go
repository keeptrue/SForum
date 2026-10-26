package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRouteReplayBindingIgnoresRuntimeRestartButPinsPlanSemantics(t *testing.T) {
	before := dispatchPluginStep(RoutePhaseBefore, "demo.route.binding_before", extensionmanifest.RouteActionBefore)
	before.MutableRequestFields = []string{"/query/tag/0"}
	second := dispatchPluginStep(RoutePhaseBefore, "demo.route.binding_second", extensionmanifest.RouteActionBefore)
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.binding_handler", extensionmanifest.RouteActionAdd)
	base := dispatchPlan("POST", "/binding", nil, []RouteExecutionStep{before, second, handler}, 2)
	request := DispatchRequest{Method: "POST", Path: "/binding", Query: "tag=a&tag=b"}

	want, err := BuildRouteReplayBinding(base, request)
	if err != nil {
		t.Fatal(err)
	}
	restarted := mutateRouteReplayPlan(t, base, func(chain []RouteExecutionStep) {
		for index := range chain {
			chain[index].Provider.Artifact.RuntimeInstanceID = "runtime-restarted"
		}
	})
	got, err := BuildRouteReplayBinding(restarted, request)
	if err != nil || got != want {
		t.Fatalf("runtime restart binding=%#v want=%#v error=%v", got, want, err)
	}

	tests := []struct {
		name   string
		mutate func([]RouteExecutionStep)
	}{
		{name: "artifact digest", mutate: func(chain []RouteExecutionStep) {
			chain[0].Provider.Artifact.PackageDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{name: "request schema", mutate: func(chain []RouteExecutionStep) { chain[0].RequestSchema += ".v2" }},
		{name: "guard", mutate: func(chain []RouteExecutionStep) { chain[0].Guard = extensionmanifest.GuardCoreLogin }},
		{name: "allowlist", mutate: func(chain []RouteExecutionStep) {
			chain[0].MutableRequestFields = []string{"/query/tag/1"}
		}},
		{name: "order", mutate: func(chain []RouteExecutionStep) { chain[0], chain[1] = chain[1], chain[0] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateRouteReplayPlan(t, base, test.mutate)
			binding, err := BuildRouteReplayBinding(changed, request)
			if err != nil {
				t.Fatalf("changed plan is invalid: %v", err)
			}
			if binding.PlanDigest == want.PlanDigest {
				t.Fatalf("changed plan retained digest %s", binding.PlanDigest)
			}
		})
	}
}

func TestRouteReplayPlanDigestPinsBoundPolicyAndPreservesLegacyV1(t *testing.T) {
	handler := dispatchPluginStep(RoutePhaseHandler, "demo.route.bound_policy", extensionmanifest.RouteActionAdd)
	legacy := dispatchPlan("POST", "/bound-policy", nil, []RouteExecutionStep{handler}, 0)
	legacyDigest, err := routeReplayPlanDigest(legacy)
	if err != nil {
		t.Fatal(err)
	}
	chain := legacy.Chain()
	for index := range chain {
		chain[index].Provider.Artifact.RuntimeInstanceID = ""
	}
	legacyDocument, err := json.Marshal(struct {
		Schema string               `json:"schema"`
		Chain  []RouteExecutionStep `json:"chain"`
	}{Schema: "sforum.required-route-plan@1", Chain: chain})
	if err != nil {
		t.Fatal(err)
	}
	legacySum := sha256.Sum256(legacyDocument)
	if want := hex.EncodeToString(legacySum[:]); legacyDigest != want {
		t.Fatalf("legacy digest = %q, want exact v1 %q", legacyDigest, want)
	}

	required := legacy
	required.policy = RouteExecutionPolicy{
		RateLimit: routePolicyRateLimitIPWrite, Idempotency: routePolicyIdempotencyRequired24h,
		IdempotencyRequired: true,
	}
	required.policyBound = true
	requiredDigest, err := routeReplayPlanDigest(required)
	if err != nil {
		t.Fatal(err)
	}
	if requiredDigest == legacyDigest {
		t.Fatalf("bound required policy retained legacy digest %s", requiredDigest)
	}

	disabled := required
	disabled.policy = RouteExecutionPolicy{RateLimit: routePolicyDisabled, Idempotency: routePolicyDisabled}
	disabledDigest, err := routeReplayPlanDigest(disabled)
	if err != nil {
		t.Fatal(err)
	}
	if disabledDigest == requiredDigest {
		t.Fatalf("policy change retained bound digest %s", disabledDigest)
	}

	restarted := required
	restarted.chain = required.Chain()
	for index := range restarted.chain {
		restarted.chain[index].Provider.Artifact.RuntimeInstanceID = "runtime-restarted"
	}
	restartedDigest, err := routeReplayPlanDigest(restarted)
	if err != nil || restartedDigest != requiredDigest {
		t.Fatalf("runtime restart digest = %q, want %q, error = %v", restartedDigest, requiredDigest, err)
	}
}

func TestRouteReplayRequestDigestPreservesRepeatedQueryValueOrderAndLiveCredentials(t *testing.T) {
	base := DispatchRequest{
		Method: "POST", Path: "/binding", Query: "tag=a&tag=b&z=1",
		Params: map[string]string{},
		Headers: http.Header{
			"Authorization": {"Bearer first"},
			"Cookie":        {"session=first"},
			"X-Ordinary":    {"one"},
		},
	}
	want, err := routeReplayRequestDigest(base)
	if err != nil {
		t.Fatal(err)
	}

	keyReordered := cloneDispatchRequest(base)
	keyReordered.Query = "z=1&tag=a&tag=b"
	got, err := routeReplayRequestDigest(keyReordered)
	if err != nil || got != want {
		t.Fatalf("key reorder digest=%q want=%q error=%v", got, want, err)
	}

	valueReordered := cloneDispatchRequest(base)
	valueReordered.Query = "tag=b&tag=a&z=1"
	got, err = routeReplayRequestDigest(valueReordered)
	if err != nil || got == want {
		t.Fatalf("value reorder digest=%q want different from %q error=%v", got, want, err)
	}

	rotated := cloneDispatchRequest(base)
	rotated.Headers.Set("Authorization", "Bearer second")
	rotated.Headers.Set("Cookie", "session=second")
	got, err = routeReplayRequestDigest(rotated)
	if err != nil || got != want {
		t.Fatalf("credential rotation digest=%q want=%q error=%v", got, want, err)
	}

	ordinaryChanged := cloneDispatchRequest(base)
	ordinaryChanged.Headers.Set("X-Ordinary", "two")
	got, err = routeReplayRequestDigest(ordinaryChanged)
	if err != nil || got == want {
		t.Fatalf("ordinary header digest=%q want different from %q error=%v", got, want, err)
	}
}

func mutateRouteReplayPlan(
	t *testing.T,
	plan RouteExecutionPlan,
	mutate func([]RouteExecutionStep),
) RouteExecutionPlan {
	t.Helper()
	chain := plan.Chain()
	mutate(chain)
	return dispatchPlan(plan.Method(), plan.Path(), plan.Params(), chain, plan.terminalIndex)
}
