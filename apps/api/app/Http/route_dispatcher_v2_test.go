package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"reflect"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestBufferedRouteStepInvokerSelectsV2WithOneAdmission(t *testing.T) {
	runtime := newRouteDispatcherV2Runtime(t)
	invoker := NewBufferedRouteStepInvoker(runtime)
	input := routeDispatcherV2Invocation(t)
	result, err := invoker.Invoke(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || runtime.activeCalls != 1 {
		t.Fatalf("calls=%d activeCalls=%d", runtime.calls, runtime.activeCalls)
	}
	if runtime.request.RouteID != "demo.route" || runtime.request.ContractVersion != "demo.route@1" ||
		runtime.request.RouteAction != "add" || runtime.request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageHandler ||
		runtime.request.PathParameters["id"] != "41" || runtime.request.QueryParameters["page"] != "2" ||
		!reflect.DeepEqual(runtime.request.QueryParameterValues["page"], []string{"2"}) ||
		runtime.request.Body["title"] != "hello" || runtime.request.Actor.UserID != 42 ||
		runtime.request.IdempotencyKey != "route-request-42" ||
		!reflect.DeepEqual(runtime.request.Actor.PermissionKeys, []string{"*", "topics.write"}) {
		t.Fatalf("request = %#v", runtime.request)
	}
	if result.Response == nil || result.Response.Status != stdhttp.StatusCreated || string(result.Response.Body) != `{"ok":true}` ||
		result.Response.Headers.Get("X-Result") != "ok" || result.Response.Headers.Get("X-SForum-Actor-ID") != "" ||
		result.Response.Headers.Get("Set-Cookie") != "" || result.Response.Headers.Get("Link") != "" ||
		!result.SideEffectStarted || !result.ResponseStarted {
		t.Fatalf("result = %#v", result)
	}
	if snapshot := runtime.gate.Snapshot(); snapshot.ActiveTotal != 0 {
		t.Fatalf("admission leaked: %#v", snapshot)
	}
}

func TestBufferedRouteStepInvokerV2FencesFallbackBeforeRemoteError(t *testing.T) {
	runtime := newRouteDispatcherV2Runtime(t)
	runtime.err = errors.New("plugin crashed after receiving request")
	input := routeDispatcherV2Invocation(t)
	commit := input.Commit
	result, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input)
	if err == nil || !result.SideEffectStarted || result.ResponseStarted || commit.State() != routes.RouteCommitSideEffectStarted {
		t.Fatalf("result=%#v commit=%s err=%v", result, commit.State(), err)
	}
}

func TestBufferedRouteStepInvokerV2RejectsAmbiguousOrUntypedInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*routes.RouteInvocation)
	}{
		{"malformed JSON", func(input *routes.RouteInvocation) { input.Request.Body = []byte(`{"title":`) }},
		{"JSON array", func(input *routes.RouteInvocation) { input.Request.Body = []byte(`[1,2]`) }},
		{"JSON scalar", func(input *routes.RouteInvocation) { input.Request.Body = []byte(`true`) }},
		{"body without schema", func(input *routes.RouteInvocation) { input.Step.RequestSchema = "" }},
		{"repeated idempotency key", func(input *routes.RouteInvocation) {
			input.Request.Headers["Idempotency-Key"] = []string{"one", "two"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRouteDispatcherV2Runtime(t)
			input := routeDispatcherV2Invocation(t)
			test.mutate(&input)
			if _, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input); !errors.Is(err, ErrRouteRuntimeTarget) {
				t.Fatalf("error = %v", err)
			}
			if runtime.calls != 0 {
				t.Fatalf("remote called %d times", runtime.calls)
			}
		})
	}
}

func TestBufferedRouteStepInvokerV2PreservesRepeatedAndEmptyQueryValues(t *testing.T) {
	runtime := newRouteDispatcherV2Runtime(t)
	input := routeDispatcherV2InvocationWithQuery(t, "tag=one&tag=&tag=two&page=2")
	if _, err := NewBufferedRouteStepInvoker(runtime).Invoke(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if runtime.request.QueryParameters["tag"] != "one" ||
		!reflect.DeepEqual(runtime.request.QueryParameterValues["tag"], []string{"one", "", "two"}) ||
		!reflect.DeepEqual(runtime.request.QueryParameterValues["page"], []string{"2"}) {
		t.Fatalf("query projection = %#v / %#v", runtime.request.QueryParameters, runtime.request.QueryParameterValues)
	}
}

func TestBufferedRouteStepInvokerV2EnforcesResponseLimit(t *testing.T) {
	runtime := newRouteDispatcherV2Runtime(t)
	runtime.response.Body = map[string]any{"value": "too large"}
	invoker := NewBufferedRouteStepInvoker(runtime)
	invoker.ResponseLimit = 4
	if _, err := invoker.Invoke(context.Background(), routeDispatcherV2Invocation(t)); !errors.Is(err, ErrRouteResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
}

func TestBufferedRouteStepInvokerV2RejectsStreamPreflight(t *testing.T) {
	runtime := newRouteDispatcherV2Runtime(t)
	runtime.response.Body = nil
	runtime.response.BodyPresent = false
	runtime.response.StreamFollows = true
	result, err := NewBufferedRouteStepInvoker(runtime).Invoke(
		context.Background(), routeDispatcherV2Invocation(t),
	)
	if !errors.Is(err, ErrRouteRuntimeTarget) || !result.SideEffectStarted || result.ResponseStarted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestProtocolV2BridgeResponseShapeIsStageExact(t *testing.T) {
	patch := []extensionsruntime.ProtocolV2RoutePatchOperation{{
		Kind: extensionsruntime.ProtocolV2RoutePatchReplace, Path: "/status", Value: []byte(`204`),
	}}
	valid := []struct {
		stage    routes.InvocationStage
		response extensionsruntime.ProtocolV2RouteResponse
	}{
		{stage: routes.InvocationStageRequest, response: extensionsruntime.ProtocolV2RouteResponse{RequestPatch: patch}},
		{stage: routes.InvocationStageResponse, response: extensionsruntime.ProtocolV2RouteResponse{ResponsePatch: patch}},
		{stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: stdhttp.StatusNoContent}},
	}
	for _, test := range valid {
		if err := validateProtocolV2BridgeResponse(test.stage, test.response); err != nil {
			t.Fatalf("valid %s response rejected: %v", test.stage, err)
		}
	}

	invalid := []struct {
		name     string
		stage    routes.InvocationStage
		response extensionsruntime.ProtocolV2RouteResponse
	}{
		{name: "request terminal", stage: routes.InvocationStageRequest, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: 200}},
		{name: "request reverse patch", stage: routes.InvocationStageRequest, response: extensionsruntime.ProtocolV2RouteResponse{ResponsePatch: patch}},
		{name: "response terminal", stage: routes.InvocationStageResponse, response: extensionsruntime.ProtocolV2RouteResponse{Headers: stdhttp.Header{"X-Test": {"forged"}}}},
		{name: "response reverse patch", stage: routes.InvocationStageResponse, response: extensionsruntime.ProtocolV2RouteResponse{RequestPatch: patch}},
		{name: "handler request patch", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: 200, RequestPatch: patch}},
		{name: "handler response patch", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: 200, ResponsePatch: patch}},
		{name: "handler invalid status", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: 99}},
		{name: "handler continue", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: stdhttp.StatusContinue}},
		{name: "handler switching protocols", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: stdhttp.StatusSwitchingProtocols}},
		{name: "handler early hints", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: stdhttp.StatusEarlyHints}},
		{name: "handler body presence drift", stage: routes.InvocationStageHandler, response: extensionsruntime.ProtocolV2RouteResponse{StatusCode: 200, Body: map[string]any{"ok": true}}},
		{name: "unknown stage", stage: routes.InvocationStage("forged"), response: extensionsruntime.ProtocolV2RouteResponse{}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := validateProtocolV2BridgeResponse(test.stage, test.response); !errors.Is(err, ErrRouteRuntimeTarget) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

type routeDispatcherV2Runtime struct {
	snapshot    extensionsruntime.RuntimeInstanceSnapshot
	gate        *extensionsruntime.RuntimeAdmissionGate
	request     extensionsruntime.ProtocolV2RouteRequest
	response    extensionsruntime.ProtocolV2RouteResponse
	err         error
	calls       int
	activeCalls int
}

func newRouteDispatcherV2Runtime(t *testing.T) *routeDispatcherV2Runtime {
	t.Helper()
	artifact := routeDispatcherArtifact("demo", 'a')
	return newRouteDispatcherV2RuntimeForArtifact(t, artifact)
}

func newRouteDispatcherV2RuntimeForArtifact(t *testing.T, artifact routes.PluginArtifact) *routeDispatcherV2Runtime {
	t.Helper()
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	return &routeDispatcherV2Runtime{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: artifact.ExtensionVersion, ArtifactDigest: artifact.PackageDigest,
			Target: extensionsruntime.RouteTarget{InstanceID: identity.InstanceID}, Active: true,
		},
		response: extensionsruntime.ProtocolV2RouteResponse{
			StatusCode: stdhttp.StatusCreated,
			Headers: stdhttp.Header{
				"X-Result": {"ok"}, "X-SForum-Actor-ID": {"forged"}, "Set-Cookie": {"forged=1"},
				"Link": {"<https://evil.example/>; rel=\"canonical\"", "</asset.js>; rel=\"preload\""},
			},
			Body: map[string]any{"ok": true}, BodyPresent: true,
		},
	}
}

func (r *routeDispatcherV2Runtime) InspectRuntimeInstance(identity extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	return r.snapshot, nil
}

func (r *routeDispatcherV2Runtime) AcquireRuntimeCall(ctx context.Context, identity extensionsruntime.RuntimeInstanceIdentity, class extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error) {
	if identity != r.snapshot.Identity || class != extensionsruntime.RuntimeCallRoute {
		return nil, extensionsruntime.ErrRuntimeInstanceNotActive
	}
	return r.gate.Acquire(ctx, class)
}

func (r *routeDispatcherV2Runtime) InvokeRouteInstance(
	_ context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	request extensionsruntime.ProtocolV2RouteRequest,
) (extensionsruntime.ProtocolV2RouteResponse, error) {
	if identity != r.snapshot.Identity {
		return extensionsruntime.ProtocolV2RouteResponse{}, extensionsruntime.ErrRuntimeInstanceNotFound
	}
	r.calls++
	r.activeCalls = r.gate.Snapshot().ActiveTotal
	r.request = request
	if request.InvocationStage != extensionsruntime.ProtocolV2RouteInvocationStageHandler {
		return extensionsruntime.ProtocolV2RouteResponse{}, r.err
	}
	return r.response, r.err
}

func routeDispatcherV2Invocation(t *testing.T) routes.RouteInvocation {
	t.Helper()
	return routeDispatcherV2InvocationWithQuery(t, "page=2")
}

func routeDispatcherV2InvocationWithQuery(t *testing.T, query string) routes.RouteInvocation {
	t.Helper()
	artifact := routeDispatcherArtifact("demo", 'a')
	declaration := routeDispatcherManifestRoute(
		"demo.route", "add", "/demo/:id", stdhttp.MethodPost,
	)
	declaration.RequestSchema = "demo.request@1"
	declaration.ResponseSchema = "demo.response@1"
	return captureAuthorizedRouteInvocation(t, artifact, declaration, routes.DispatchRequest{
		Method: stdhttp.MethodPost, Path: "/demo/41", Query: query,
		Headers: stdhttp.Header{
			"Content-Type": {"application/json"}, "Idempotency-Key": {"route-request-42"},
			"X-Test": {"value"}, "X-SForum-Forged": {"bad"},
		},
		Body:    []byte(`{"title":"hello"}`),
		ActorID: 42, Authenticated: true, Permissions: map[string]bool{"topics.write": true, "*": true, "ignored": false},
	})
}

var _ ExactRouteRuntime = (*routeDispatcherV2Runtime)(nil)
var _ exactRouteV2Runtime = (*routeDispatcherV2Runtime)(nil)
