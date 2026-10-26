package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtocolV2RouteCarriesExactTypedRequestAndHostActor(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	response, err := client.InvokeRouteContext(context.Background(), ProtocolV2RouteRequest{
		RouteID: "demo.route", ContractVersion: "demo.route@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodPost, Path: "/demo/41",
		Authority: protocolV2FilteredHostRequestAuthority(),
		Headers:   http.Header{"X-Test": {"one", "two"}}, PathParameters: map[string]string{"id": "41"},
		QueryParameters: map[string]string{"page": "2"}, RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		Body: map[string]any{"title": "hello"}, BodyPresent: true,
		Actor:         NewProtocolV2RouteActor(42, true, map[string]bool{"topics.write": true, "ignored": false, "*": true}),
		CorrelationID: "trace-route", Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.GetRouteId() != "demo.route" || received.GetContractVersion() != "demo.route@1" ||
		received.GetRouteAction() != extensionmanifest.RouteActionAdd ||
		received.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER ||
		received.GetMethod() != http.MethodPost || received.GetPath() != "/demo/41" ||
		received.GetRequestAuthorityMode() != pluginwire.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_FILTERED ||
		received.GetGuardKind() != pluginwire.RouteGuardKind_ROUTE_GUARD_KIND_HOST ||
		received.GetPathParameters()["id"] != "41" || received.GetQueryParameters()["page"] != "2" ||
		received.GetBody().GetSchemaId() != "demo.request" || received.GetBody().GetSchemaVersion() != "1" ||
		received.GetBody().GetValue().AsMap()["title"] != "hello" {
		t.Fatalf("request = %#v", received)
	}
	if query := received.GetQueryParameterValues(); len(query) != 1 || query[0].GetKey() != "page" ||
		!reflect.DeepEqual(query[0].GetValues(), []string{"2"}) {
		t.Fatalf("lossless query = %#v", query)
	}
	if actor := received.GetContext().GetActor(); actor.GetUserId() != 42 ||
		!reflect.DeepEqual(actor.GetPermissionKeys(), []string{"*", "topics.write"}) {
		t.Fatalf("actor = %#v", actor)
	}
	if response.StatusCode != http.StatusCreated || !response.BodyPresent || response.Body["ok"] != true ||
		!reflect.DeepEqual(response.Headers.Values("X-Result"), []string{"one", "two"}) {
		t.Fatalf("response = %#v", response)
	}
	if client.requestContext(context.Background(), "ordinary").GetActor() != nil ||
		NewProtocolV2RouteActor(42, false, map[string]bool{"*": true}) != nil {
		t.Fatal("ordinary or anonymous context acquired actor authority")
	}
}

func TestProtocolV2RouteCarriesRepeatedQueryValuesLosslessly(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	legacy := map[string]string{"legacy": "only", "tag": "first"}
	all := map[string][]string{
		"empty": {""},
		"page":  {"2"},
		"tag":   {"first", "a+b", "slash/value", ""},
	}
	request := protocolV2RouteTestRequest()
	request.QueryParameters = legacy
	request.QueryParameterValues = all
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received.GetQueryParameters(), map[string]string{
		"empty": "", "legacy": "only", "page": "2", "tag": "first",
	}) {
		t.Fatalf("legacy query map = %#v", received.GetQueryParameters())
	}
	query := received.GetQueryParameterValues()
	if len(query) != 4 || query[0].GetKey() != "empty" || query[1].GetKey() != "legacy" ||
		query[2].GetKey() != "page" || query[3].GetKey() != "tag" ||
		!reflect.DeepEqual(query[0].GetValues(), []string{""}) ||
		!reflect.DeepEqual(query[1].GetValues(), []string{"only"}) ||
		!reflect.DeepEqual(query[2].GetValues(), []string{"2"}) ||
		!reflect.DeepEqual(query[3].GetValues(), []string{"first", "a+b", "slash/value", ""}) {
		t.Fatalf("lossless query entries = %#v", query)
	}

	legacy["legacy"] = "caller-mutated"
	all["tag"][0] = "caller-mutated"
	if received.GetQueryParameters()["legacy"] != "only" || received.GetQueryParameterValues()[3].GetValues()[0] != "first" {
		t.Fatalf("wire query leaked caller maps: %#v / %#v", received.GetQueryParameters(), received.GetQueryParameterValues())
	}
}

func TestProtocolV2RouteRejectsAmbiguousRepeatedQueryValues(t *testing.T) {
	tests := []struct {
		name   string
		legacy map[string]string
		all    map[string][]string
	}{
		{name: "conflicting first value", legacy: map[string]string{"tag": "legacy"}, all: map[string][]string{"tag": {"new", "second"}}},
		{name: "missing values", all: map[string][]string{"tag": nil}},
		{name: "empty values", all: map[string][]string{"tag": {}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := protocolV2RouteQueryParameters(test.legacy, test.all); !errors.Is(err, ErrProtocolV2RouteInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProtocolV2RouteCarriesCatalogIssuedActorDelegations(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	issuer := &recordingProtocolV2ActorDelegationIssuer{grants: []hostapi.ProtocolV2ActorDelegationGrant{
		{CommandID: "sforum.user.status", CommandVersion: "1", IdempotencyKey: "route-request-42", Token: "signed-token"},
	}}
	client.delegations = issuer
	client.hostCommands = true
	request := protocolV2RouteTestRequest()
	request.Actor = NewProtocolV2RouteActor(42, true, map[string]bool{"user.manage": true})
	request.IdempotencyKey = "route-request-42"
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 1 || issuer.request.ActorUserID != 42 || issuer.request.IdempotencyKey != request.IdempotencyKey ||
		issuer.request.Runtime.GetInstanceId() != client.identity.GetInstanceId() ||
		!reflect.DeepEqual(issuer.request.PermissionKeys, []string{"user.manage"}) {
		t.Fatalf("issuer request = %#v", issuer.request)
	}
	delegations := received.GetContext().GetHostCommandDelegations()
	if received.GetContext().GetIdempotencyKey() != request.IdempotencyKey || len(delegations) != 1 ||
		delegations[0].GetCommandId() != "sforum.user.status" || delegations[0].GetToken() != "signed-token" {
		t.Fatalf("route context = %#v", received.GetContext())
	}

	issuer.calls = 0
	request.Actor = nil
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 0 {
		t.Fatal("anonymous invocation acquired an actor delegation")
	}
	request.Actor = NewProtocolV2RouteActor(42, true, map[string]bool{"user.manage": true})
	client.hostCommands = false
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 0 || len(received.GetContext().GetHostCommandDelegations()) != 0 {
		t.Fatal("artifact without host_commands acquired an actor delegation")
	}
	client.hostCommands = true
	background := client.requestContext(context.Background(), "background-job")
	if issuer.calls != 0 || background.GetActor() != nil || background.GetIdempotencyKey() != "" ||
		len(background.GetHostCommandDelegations()) != 0 {
		t.Fatalf("background context acquired actor authority: %#v", background)
	}
}

func TestProtocolV2RouteRejectsUnavailableOrMalformedActorDelegations(t *testing.T) {
	request := protocolV2RouteTestRequest()
	request.Actor = NewProtocolV2RouteActor(42, true, map[string]bool{"user.manage": true})
	request.IdempotencyKey = "route-request-42"
	for _, test := range []struct {
		name   string
		issuer hostapi.ProtocolV2ActorDelegationBundleIssuer
		want   error
	}{
		{name: "issuer unavailable", want: ErrProtocolV2ActorDelegationUnavailable},
		{name: "issuer failure", issuer: &recordingProtocolV2ActorDelegationIssuer{
			err: errors.New("signing key unavailable"),
		}, want: ErrProtocolV2ActorDelegationUnavailable},
		{name: "wrong key", issuer: &recordingProtocolV2ActorDelegationIssuer{grants: []hostapi.ProtocolV2ActorDelegationGrant{{
			CommandID: "sforum.user.status", CommandVersion: "1", IdempotencyKey: "other", Token: "signed-token",
		}}}, want: ErrProtocolV2ActorDelegationInvalid},
		{name: "duplicate command", issuer: &recordingProtocolV2ActorDelegationIssuer{grants: []hostapi.ProtocolV2ActorDelegationGrant{
			{CommandID: "sforum.user.status", CommandVersion: "1", IdempotencyKey: "route-request-42", Token: "signed-one"},
			{CommandID: "sforum.user.status", CommandVersion: "1", IdempotencyKey: "route-request-42", Token: "signed-two"},
		}}, want: ErrProtocolV2ActorDelegationInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				called = true
				return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
			})
			client.delegations = test.issuer
			client.hostCommands = true
			if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			if called {
				t.Fatal("malformed delegation reached the plugin")
			}
		})
	}
}

func TestProtocolV2RouteFailsClosedOnResponseDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pluginwire.RouteResponse)
		want   error
	}{
		{"nil context", func(response *pluginwire.RouteResponse) { response.Context = nil }, ErrProtocolV2RouteInvalid},
		{"wrong request", func(response *pluginwire.RouteResponse) { response.Context.RequestId = "wrong" }, ErrProtocolV2RouteInvalid},
		{"wrong identity", func(response *pluginwire.RouteResponse) { response.Context.Extension.InstanceId = "replacement" }, ErrProtocolV2RouteInvalid},
		{"invalid time", func(response *pluginwire.RouteResponse) { response.Context.ServerTime = nil }, ErrProtocolV2RouteInvalid},
		{"wrong schema", func(response *pluginwire.RouteResponse) { response.Body.SchemaVersion = "2" }, ErrProtocolV2RouteInvalid},
		{"stream with body", func(response *pluginwire.RouteResponse) { response.StreamFollows = true }, ErrProtocolV2RouteInvalid},
		{"informational terminal", func(response *pluginwire.RouteResponse) { response.StatusCode = http.StatusEarlyHints }, ErrProtocolV2RouteInvalid},
		{"switching protocols without stream", func(response *pluginwire.RouteResponse) { response.StatusCode = http.StatusSwitchingProtocols }, ErrProtocolV2RouteInvalid},
		{"invalid header name", func(response *pluginwire.RouteResponse) { response.Headers[0].Name = "Bad\r\nName" }, ErrProtocolV2RouteInvalid},
		{"invalid header value", func(response *pluginwire.RouteResponse) { response.Headers[0].Values[0] = "bad\r\nvalue" }, ErrProtocolV2RouteInvalid},
		{"handler patch", func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{
				Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REMOVE, Path: "/body/title",
			}}
		}, ErrProtocolV2RouteInvalid},
		{"typed error", func(response *pluginwire.RouteResponse) {
			response.Error = &protocolwire.ErrorDetail{Code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Reason: "route.denied"}
		}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				response := protocolV2RouteTestResponse(request, map[string]any{"ok": true})
				test.mutate(response)
				return response, nil
			})
			_, err := client.InvokeRouteContext(context.Background(), protocolV2RouteTestRequest())
			if test.name == "typed error" {
				var typed *ProtocolV2Error
				if !errors.As(err, &typed) || typed.Reason != "route.denied" {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if !errors.Is(err, ErrProtocolV2RouteResponseInvalid) {
				t.Fatalf("response drift lacks stable classification: %v", err)
			}
		})
	}
}

func TestProtocolV2TerminalStatusAllowsOnlyWebSocketInformationalResponse(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		streamFollows bool
		valid         bool
	}{
		{name: "ordinary terminal", status: http.StatusOK, valid: true},
		{name: "websocket upgrade", status: http.StatusSwitchingProtocols, streamFollows: true, valid: true},
		{name: "switching without stream", status: http.StatusSwitchingProtocols},
		{name: "continue with stream", status: http.StatusContinue, streamFollows: true},
		{name: "early hints with stream", status: http.StatusEarlyHints, streamFollows: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := protocolV2RouteTerminalResponse(&pluginwire.RouteResponse{
				StatusCode: uint32(test.status), StreamFollows: test.streamFollows,
			}, "")
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && !errors.Is(err, ErrProtocolV2RouteInvalid) {
				t.Fatalf("status=%d stream=%t error=%v", test.status, test.streamFollows, err)
			}
		})
	}
}

func TestProtocolV2RouteAcceptsAuthenticatedStreamPreflight(t *testing.T) {
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		response := protocolV2RouteTestResponse(request, nil)
		response.Body = nil
		response.StreamFollows = true
		return response, nil
	})
	response, err := client.InvokeRouteContext(context.Background(), protocolV2RouteTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !response.StreamFollows || response.BodyPresent || response.StatusCode != http.StatusCreated || response.Headers.Get("X-Result") != "one" {
		t.Fatalf("stream preflight=%#v", response)
	}
}

func TestProtocolV2StreamPreflightSkipsResponseSchemaBodyValidation(t *testing.T) {
	// Opaque stream boundary: declared responseSchema is catalog identity only.
	// StreamFollows preflight must not require or validate a TypedDocument body.
	response, err := protocolV2RouteTerminalResponse(&pluginwire.RouteResponse{
		StatusCode: http.StatusOK, StreamFollows: true,
	}, "demo.stream.response@1")
	if err != nil {
		t.Fatal(err)
	}
	if !response.StreamFollows || response.BodyPresent || response.StatusCode != http.StatusOK {
		t.Fatalf("stream preflight=%#v", response)
	}
}

func TestProtocolV2RoutePropagatesCallerCancellation(t *testing.T) {
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(ctx context.Context, _ *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancelCause(context.Background())
	want := errors.New("exact caller cancellation")
	cancel(want)
	if _, err := client.InvokeRouteContext(ctx, protocolV2RouteTestRequest()); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestProtocolV2RouteCreatesUniqueTraceWithoutCallerCorrelation(t *testing.T) {
	traces := make(chan string, 2)
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		traces <- request.GetContext().GetTrace().GetTraceId()
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	request := protocolV2RouteTestRequest()
	for range 2 {
		if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-traces, <-traces
	if first == "" || second == "" || first == second {
		t.Fatalf("trace ids = %q, %q", first, second)
	}
}

func TestProtocolV2FrozenRouteMatchesHeadAndGlobalMiddleware(t *testing.T) {
	client := &protocolV2Client{routes: []extensions.ManifestRoute{
		{ID: "demo.get", ContractVersion: "demo.get@1", Action: extensionmanifest.RouteActionAdd, Methods: []string{http.MethodGet}, Mode: extensionmanifest.RouteModeHTTP, Guard: extensionmanifest.GuardCorePublic},
		{ID: "demo.sse", ContractVersion: "demo.sse@1", Action: extensionmanifest.RouteActionAdd, Methods: []string{http.MethodGet}, Mode: extensionmanifest.RouteModeSSE, Guard: extensionmanifest.GuardCorePublic},
		{ID: "demo.global", ContractVersion: "demo.global@1", Action: extensionmanifest.RouteActionGlobalMiddleware, Guard: extensionmanifest.GuardCorePublic},
	}}
	for _, request := range []ProtocolV2RouteRequest{
		{RouteID: "demo.get", ContractVersion: "demo.get@1", RouteAction: extensionmanifest.RouteActionAdd, InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodHead, Authority: protocolV2FilteredHostRequestAuthority()},
		{RouteID: "demo.global", ContractVersion: "demo.global@1", RouteAction: extensionmanifest.RouteActionGlobalMiddleware, InvocationStage: ProtocolV2RouteInvocationStageRequest, Method: http.MethodDelete, Authority: protocolV2FilteredHostRequestAuthority()},
	} {
		if err := client.validateFrozenRoute(request); err != nil {
			t.Fatalf("request %#v: %v", request, err)
		}
	}
	if err := client.validateFrozenRoute(ProtocolV2RouteRequest{
		RouteID: "demo.get", ContractVersion: "demo.get@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodPost,
		Authority: protocolV2FilteredHostRequestAuthority(),
	}); !errors.Is(err, ErrProtocolV2RouteInvalid) {
		t.Fatalf("POST error = %v", err)
	}
	if err := client.validateFrozenRoute(ProtocolV2RouteRequest{
		RouteID: "demo.sse", ContractVersion: "demo.sse@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodHead,
		Authority: protocolV2FilteredHostRequestAuthority(),
	}); !errors.Is(err, ErrProtocolV2RouteInvalid) {
		t.Fatalf("SSE HEAD error = %v", err)
	}
}

func TestProtocolV2RouteMapsRequestMutationStage(t *testing.T) {
	requestFields := []string{"/body/title", "/headers/x~1trace", "/query/page"}
	responseFields := []string{"/body/summary"}
	addValue := []byte(`"updated"`)
	replaceValue := []byte(`9007199254740993`)
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		response := protocolV2RouteMutationTestResponse(request)
		response.RequestPatch = []*pluginwire.RoutePatchOperation{
			{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD, Path: "/body/title", ValueJson: addValue},
			{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REPLACE, Path: "/query/page", ValueJson: replaceValue},
			{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REMOVE, Path: "/headers/x~1trace"},
		}
		return response, nil
	})
	client.routes = []extensions.ManifestRoute{{
		ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionFilter,
		Methods: []string{http.MethodPost}, Guard: extensionmanifest.GuardCorePublic,
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		MutableRequestFields: requestFields, MutableResponseFields: responseFields,
	}}
	request := protocolV2RouteTestRequest()
	request.Method = http.MethodPost
	request.RouteAction = extensionmanifest.RouteActionFilter
	request.InvocationStage = ProtocolV2RouteInvocationStageRequest
	request.MutableRequestFields = requestFields
	request.MutableResponseFields = responseFields
	request.Body = map[string]any{"title": "original"}
	request.BodyPresent = true
	response, err := client.InvokeRouteContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if received.GetRouteAction() != extensionmanifest.RouteActionFilter ||
		received.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_REQUEST ||
		received.GetPriorResponse() != nil ||
		!reflect.DeepEqual(received.GetMutableRequestFields(), requestFields) ||
		!reflect.DeepEqual(received.GetMutableResponseFields(), responseFields) {
		t.Fatalf("wire request = %#v", received)
	}
	if response.StatusCode != 0 || response.BodyPresent || response.StreamFollows || len(response.ResponsePatch) != 0 ||
		len(response.RequestPatch) != 3 || response.RequestPatch[0].Kind != ProtocolV2RoutePatchAdd ||
		response.RequestPatch[1].Kind != ProtocolV2RoutePatchReplace || response.RequestPatch[2].Kind != ProtocolV2RoutePatchRemove ||
		response.RequestPatch[2].Value != nil {
		t.Fatalf("mutation response = %#v", response)
	}
	assertProtocolV2RawJSON(t, response.RequestPatch[0].Value, "updated")
	if string(response.RequestPatch[1].Value) != string(replaceValue) {
		t.Fatalf("large integer JSON = %q, want exact %q", response.RequestPatch[1].Value, replaceValue)
	}

	requestFields[0] = "/caller-mutated"
	responseFields[0] = "/caller-mutated"
	if received.GetMutableRequestFields()[0] != "/body/title" || received.GetMutableResponseFields()[0] != "/body/summary" {
		t.Fatalf("wire allowlists leaked caller slices: %#v / %#v", received.GetMutableRequestFields(), received.GetMutableResponseFields())
	}
}

func TestProtocolV2RouteMapsResponseMutationStageAndPriorResponse(t *testing.T) {
	requestFields := []string{"/body/title"}
	responseFields := []string{"/status", "/headers/cache-control", "/body/summary"}
	value := []byte(`"filtered"`)
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		response := protocolV2RouteMutationTestResponse(request)
		response.ResponsePatch = []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REPLACE,
			Path: "/body/summary", ValueJson: value,
		}}
		return response, nil
	})
	client.routes = []extensions.ManifestRoute{{
		ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionFilter,
		Methods: []string{http.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		MutableRequestFields: requestFields, MutableResponseFields: responseFields,
	}}
	prior := &ProtocolV2RouteResponseDocument{
		StatusCode: http.StatusAccepted, Headers: http.Header{
			"X-Prior": {"one", "two"}, "Location": {"/canonical"},
			"Set-Cookie": {"session=secret"}, "Authorization": {"Bearer secret"},
			"X-SForum-Internal": {"secret"}, "Connection": {"X-Dynamic-Hop"}, "X-Dynamic-Hop": {"secret"},
		},
		Body: map[string]any{"summary": "original"}, BodyPresent: true,
	}
	request := protocolV2RouteTestRequest()
	request.RouteAction = extensionmanifest.RouteActionFilter
	request.InvocationStage = ProtocolV2RouteInvocationStageResponse
	request.MutableRequestFields = requestFields
	request.MutableResponseFields = responseFields
	request.PriorResponse = prior
	response, err := client.InvokeRouteContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wirePrior := received.GetPriorResponse()
	if received.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_RESPONSE ||
		wirePrior.GetStatusCode() != http.StatusAccepted ||
		wirePrior.GetBody().GetSchemaId() != "demo.response" || wirePrior.GetBody().GetSchemaVersion() != "1" ||
		wirePrior.GetBody().GetValue().AsMap()["summary"] != "original" {
		t.Fatalf("wire prior response = %#v", wirePrior)
	}
	wirePriorHeaders, err := protocolV2RouteHTTPHeaders(wirePrior.GetHeaders())
	if err != nil {
		t.Fatal(err)
	}
	if wirePriorHeaders.Get("X-Prior") != "one" || wirePriorHeaders.Get("Location") != "/canonical" ||
		wirePriorHeaders.Get("Set-Cookie") != "" || wirePriorHeaders.Get("Authorization") != "" ||
		wirePriorHeaders.Get("X-SForum-Internal") != "" || wirePriorHeaders.Get("X-Dynamic-Hop") != "" ||
		wirePriorHeaders.Get("Connection") != "" {
		t.Fatalf("filtered prior response headers = %#v", wirePriorHeaders)
	}
	if len(response.RequestPatch) != 0 || len(response.ResponsePatch) != 1 ||
		response.ResponsePatch[0].Kind != ProtocolV2RoutePatchReplace || response.ResponsePatch[0].Path != "/body/summary" {
		t.Fatalf("mutation response = %#v", response)
	}
	assertProtocolV2RawJSON(t, response.ResponsePatch[0].Value, "filtered")

	prior.Headers.Set("X-Prior", "caller-mutated")
	prior.Body["summary"] = "caller-mutated"
	if wirePriorHeaders.Get("X-Prior") != "one" || wirePrior.GetBody().GetValue().AsMap()["summary"] != "original" {
		t.Fatalf("wire prior response leaked caller state: %#v", wirePrior)
	}
}

func TestProtocolV2RouteRawAuthorityCanReadPriorResponseCredentials(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteMutationTestResponse(request), nil
	})
	client.routes = []extensions.ManifestRoute{{
		ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionAfter,
		Methods: []string{http.MethodPost}, Guard: protocolV2DeclaredRawGuardID,
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
	}}
	// 决定 R1（2026-10-16）：读取先前响应凭证需要 raw 权限，而 raw 权限只来自包内声明的
	// kind=raw_request 守卫；Host 的 core.guard.raw_request 已被拒绝。
	useProtocolV2DeclaredRawGuard(client)
	request := protocolV2RouteTestRequest()
	request.RouteAction = extensionmanifest.RouteActionAfter
	request.InvocationStage = ProtocolV2RouteInvocationStageResponse
	request.Method = http.MethodPost
	request.Authority = ProtocolV2RequestAuthority{
		Mode: ProtocolV2RequestAuthorityRaw, GuardKind: ProtocolV2RequestGuardRawRequest,
	}
	request.PriorResponse = &ProtocolV2RouteResponseDocument{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Set-Cookie": {"session=secret"}, "Authorization": {"Bearer secret"},
			"Cookie": {"session=request-style"}, "X-CSRF-Token": {"host-secret"},
			"X-SForum-Internal": {"secret"}, "Connection": {"X-Dynamic-Hop"}, "X-Dynamic-Hop": {"secret"},
		},
	}
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetPriorResponse().GetHeaders())
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("Set-Cookie") != "session=secret" || headers.Get("Authorization") != "Bearer secret" ||
		headers.Get("Cookie") != "session=request-style" || headers.Get("X-CSRF-Token") != "" ||
		headers.Get("X-SForum-Internal") != "" || headers.Get("Connection") != "" || headers.Get("X-Dynamic-Hop") != "" {
		t.Fatalf("raw prior response headers = %#v", headers)
	}
}

func TestProtocolV2RouteValidatesActionStageMatrix(t *testing.T) {
	prior := &ProtocolV2RouteResponseDocument{StatusCode: http.StatusOK}
	valid := []ProtocolV2RouteRequest{
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAdd, ProtocolV2RouteInvocationStageHandler, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionReplace, ProtocolV2RouteInvocationStageHandler, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionGlobalMiddleware, ProtocolV2RouteInvocationStageRequest, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionBefore, ProtocolV2RouteInvocationStageRequest, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionFilter, ProtocolV2RouteInvocationStageRequest, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionWrap, ProtocolV2RouteInvocationStageRequest, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionFilter, ProtocolV2RouteInvocationStageResponse, prior),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionWrap, ProtocolV2RouteInvocationStageResponse, prior),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAfter, ProtocolV2RouteInvocationStageResponse, prior),
	}
	for _, request := range valid {
		if err := validateProtocolV2RouteRequest(request); err != nil {
			t.Errorf("valid %s/%s rejected: %v", request.RouteAction, request.InvocationStage, err)
		}
	}

	invalid := []ProtocolV2RouteRequest{
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAdd, "", nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionBefore, ProtocolV2RouteInvocationStageHandler, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAdd, ProtocolV2RouteInvocationStageHandler, prior),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAdd, ProtocolV2RouteInvocationStageRequest, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionFilter, ProtocolV2RouteInvocationStageRequest, prior),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionBefore, ProtocolV2RouteInvocationStageResponse, prior),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAfter, ProtocolV2RouteInvocationStageResponse, nil),
		protocolV2RouteRequestForStage(extensionmanifest.RouteActionAfter, ProtocolV2RouteInvocationStageResponse, &ProtocolV2RouteResponseDocument{}),
	}
	handlerFields := protocolV2RouteRequestForStage(extensionmanifest.RouteActionAdd, ProtocolV2RouteInvocationStageHandler, nil)
	handlerFields.MutableRequestFields = []string{"/body"}
	invalid = append(invalid, handlerFields)
	priorWithoutSchema := protocolV2RouteRequestForStage(extensionmanifest.RouteActionAfter, ProtocolV2RouteInvocationStageResponse, &ProtocolV2RouteResponseDocument{
		StatusCode: http.StatusOK, BodyPresent: true, Body: map[string]any{"ok": true},
	})
	priorWithoutSchema.ResponseSchema = ""
	invalid = append(invalid, priorWithoutSchema)
	for _, request := range invalid {
		if err := validateProtocolV2RouteRequest(request); !errors.Is(err, ErrProtocolV2RouteInvalid) {
			t.Errorf("invalid %s/%s error = %v", request.RouteAction, request.InvocationStage, err)
		}
	}
}

func TestProtocolV2RouteRejectsMaliciousMutationResponses(t *testing.T) {
	value := []byte(`"value"`)
	validPatch := func() *pluginwire.RoutePatchOperation {
		return &pluginwire.RoutePatchOperation{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD, Path: "/allowed", ValueJson: value,
		}
	}
	tests := []struct {
		name   string
		stage  ProtocolV2RouteInvocationStage
		mutate func(*pluginwire.RouteResponse)
	}{
		{"request terminal", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) { response.StatusCode = http.StatusOK }},
		{"request wrong direction", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.ResponsePatch = []*pluginwire.RoutePatchOperation{validPatch()}
		}},
		{"response terminal", ProtocolV2RouteInvocationStageResponse, func(response *pluginwire.RouteResponse) { response.Body = &protocolwire.TypedDocument{} }},
		{"response wrong direction", ProtocolV2RouteInvocationStageResponse, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{validPatch()}
		}},
		{"too many", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = make([]*pluginwire.RoutePatchOperation, extensionmanifest.RouteMutableFieldsMaximumCount+1)
			for index := range response.RequestPatch {
				response.RequestPatch[index] = validPatch()
			}
		}},
		{"nil operation", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{nil}
		}},
		{"unknown kind", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{Kind: 99, Path: "/allowed", ValueJson: value}}
		}},
		{"legacy value", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{
				Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
				Path: "/allowed", Value: structpb.NewStringValue("legacy"),
			}}
		}},
		{"add missing value", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD, Path: "/allowed"}}
		}},
		{"replace invalid JSON", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REPLACE, Path: "/allowed", ValueJson: []byte("{")}}
		}},
		{"remove with value", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REMOVE, Path: "/allowed", ValueJson: value}}
		}},
		{"path outside allowlist", ProtocolV2RouteInvocationStageRequest, func(response *pluginwire.RouteResponse) {
			response.RequestPatch = []*pluginwire.RoutePatchOperation{{Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_REMOVE, Path: "/other"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				response := protocolV2RouteMutationTestResponse(request)
				test.mutate(response)
				return response, nil
			})
			client.routes = []extensions.ManifestRoute{{
				ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionFilter,
				Methods: []string{http.MethodGet}, Guard: extensionmanifest.GuardCorePublic,
				RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
				MutableRequestFields: []string{"/allowed"}, MutableResponseFields: []string{"/allowed"},
			}}
			request := protocolV2RouteTestRequest()
			request.RouteAction = extensionmanifest.RouteActionFilter
			request.InvocationStage = test.stage
			request.MutableRequestFields = []string{"/allowed"}
			request.MutableResponseFields = []string{"/allowed"}
			if test.stage == ProtocolV2RouteInvocationStageResponse {
				request.PriorResponse = &ProtocolV2RouteResponseDocument{StatusCode: http.StatusOK}
			}
			if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, ErrProtocolV2RouteInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProtocolV2FrozenRouteRejectsMutationAuthorityDrift(t *testing.T) {
	client := &protocolV2Client{routes: []extensions.ManifestRoute{{
		ID: "demo.filter", ContractVersion: "demo.filter@1", Action: extensionmanifest.RouteActionFilter,
		Methods: []string{http.MethodPost}, Guard: extensionmanifest.GuardCorePublic,
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		MutableRequestFields: []string{"/body/title", "/query/page"}, MutableResponseFields: []string{"/body/summary"},
	}}}
	exact := ProtocolV2RouteRequest{
		RouteID: "demo.filter", ContractVersion: "demo.filter@1", RouteAction: extensionmanifest.RouteActionFilter,
		InvocationStage: ProtocolV2RouteInvocationStageRequest, Method: http.MethodPost,
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		MutableRequestFields: []string{"/body/title", "/query/page"}, MutableResponseFields: []string{"/body/summary"},
		Authority: protocolV2FilteredHostRequestAuthority(),
	}
	if err := client.validateFrozenRoute(exact); err != nil {
		t.Fatalf("exact frozen route rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ProtocolV2RouteRequest)
	}{
		{"action", func(request *ProtocolV2RouteRequest) { request.RouteAction = extensionmanifest.RouteActionWrap }},
		{"request allowlist", func(request *ProtocolV2RouteRequest) { request.MutableRequestFields[0] = "/body/other" }},
		{"request allowlist order", func(request *ProtocolV2RouteRequest) {
			request.MutableRequestFields[0], request.MutableRequestFields[1] = request.MutableRequestFields[1], request.MutableRequestFields[0]
		}},
		{"response allowlist", func(request *ProtocolV2RouteRequest) { request.MutableResponseFields[0] = "/body/other" }},
		{"request schema", func(request *ProtocolV2RouteRequest) { request.RequestSchema = "demo.request@2" }},
		{"response schema", func(request *ProtocolV2RouteRequest) { request.ResponseSchema = "demo.response@2" }},
		{"method", func(request *ProtocolV2RouteRequest) { request.Method = http.MethodGet }},
		{"authority", func(request *ProtocolV2RouteRequest) {
			request.Authority = ProtocolV2RequestAuthority{Mode: ProtocolV2RequestAuthorityRaw, GuardKind: ProtocolV2RequestGuardRawRequest}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := exact
			request.MutableRequestFields = append([]string(nil), exact.MutableRequestFields...)
			request.MutableResponseFields = append([]string(nil), exact.MutableResponseFields...)
			test.mutate(&request)
			if err := client.validateFrozenRoute(request); !errors.Is(err, ErrProtocolV2RouteInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProtocolV2RoutePatchJSONIsDetached(t *testing.T) {
	value := []byte(` {"nested":{"value":9007199254740993}} `)
	operations := []*pluginwire.RoutePatchOperation{{
		Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD, Path: "/allowed", ValueJson: value,
	}}
	result, err := protocolV2RoutePatchOperations(operations, []string{"/allowed"})
	if err != nil {
		t.Fatal(err)
	}
	want := string(value)
	value[1] = '['
	if string(result[0].Value) != want {
		t.Fatalf("detached JSON = %q, want exact %q", result[0].Value, want)
	}
}

func TestProtocolV2RouteDeclarationSnapshotClonesMutableFields(t *testing.T) {
	routes := []extensions.ManifestRoute{{
		ID: "demo.filter", ContractVersion: "demo.filter@1", Action: extensionmanifest.RouteActionFilter,
		Methods: []string{http.MethodGet}, MutableRequestFields: []string{"/query"},
		MutableResponseFields: []string{"/headers/cache-control"},
	}}
	snapshot := cloneProtocolV2Routes(routes)
	client := newProtocolV2Client(nil, protocolV2ClientConfig{routes: routes})

	routes[0].Methods[0] = http.MethodPost
	routes[0].MutableRequestFields[0] = "/caller-mutated"
	routes[0].MutableResponseFields[0] = "/caller-mutated"
	assertProtocolV2RouteMutableFields(t, snapshot[0])
	assertProtocolV2RouteMutableFields(t, client.routes[0])

	snapshot[0].MutableRequestFields[0] = "/snapshot-mutated"
	snapshot[0].MutableResponseFields[0] = "/snapshot-mutated"
	assertProtocolV2RouteMutableFields(t, client.routes[0])
}

func assertProtocolV2RouteMutableFields(t *testing.T, route extensions.ManifestRoute) {
	t.Helper()
	if !reflect.DeepEqual(route.Methods, []string{http.MethodGet}) ||
		!reflect.DeepEqual(route.MutableRequestFields, []string{"/query"}) ||
		!reflect.DeepEqual(route.MutableResponseFields, []string{"/headers/cache-control"}) {
		t.Fatalf("frozen route declaration = %#v", route)
	}
}

type protocolV2RouteTestServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer
	invoke func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error)
}

type recordingProtocolV2ActorDelegationIssuer struct {
	request hostapi.ProtocolV2ActorDelegationBundleRequest
	grants  []hostapi.ProtocolV2ActorDelegationGrant
	err     error
	calls   int
}

func (i *recordingProtocolV2ActorDelegationIssuer) IssueProtocolV2ActorDelegations(
	_ context.Context,
	request hostapi.ProtocolV2ActorDelegationBundleRequest,
) ([]hostapi.ProtocolV2ActorDelegationGrant, error) {
	i.calls++
	i.request = request
	return append([]hostapi.ProtocolV2ActorDelegationGrant(nil), i.grants...), i.err
}

func (s *protocolV2RouteTestServer) InvokeRoute(ctx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	return s.invoke(ctx, request)
}

func newProtocolV2RouteTestClient(
	t *testing.T,
	instanceID string,
	invoke func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error),
) *protocolV2Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pluginwire.RegisterPluginRuntimeServiceServer(server, &protocolV2RouteTestServer{invoke: invoke})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///route-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: instanceID,
	}
	return newProtocolV2Client(pluginwire.NewPluginRuntimeServiceClient(connection), protocolV2ClientConfig{
		identity: identity, instance: instanceID,
		routes: []extensions.ManifestRoute{{
			ID: "demo.route", ContractVersion: "demo.route@1", Action: extensionmanifest.RouteActionAdd,
			Methods: []string{http.MethodGet, http.MethodPost},
			Guard:   extensionmanifest.GuardCorePublic, RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
		}},
	})
}

func protocolV2RouteRequestForStage(
	action string,
	stage ProtocolV2RouteInvocationStage,
	prior *ProtocolV2RouteResponseDocument,
) ProtocolV2RouteRequest {
	request := protocolV2RouteTestRequest()
	request.RouteAction = action
	request.InvocationStage = stage
	request.PriorResponse = prior
	return request
}

func protocolV2RouteMutationTestResponse(request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{Context: &protocolwire.ResponseContext{
		RequestId: request.GetContext().GetRequestId(), Trace: proto.Clone(request.GetContext().GetTrace()).(*protocolwire.TraceContext),
		Extension: proto.Clone(request.GetContext().GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
	}}
}

func assertProtocolV2RawJSON(t *testing.T, raw json.RawMessage, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode patch JSON %q: %v", raw, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch JSON = %#v, want %#v", got, want)
	}
}

func protocolV2RouteTestRequest() ProtocolV2RouteRequest {
	return ProtocolV2RouteRequest{
		RouteID: "demo.route", ContractVersion: "demo.route@1", RouteAction: extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet, Path: "/demo",
		Authority:     protocolV2FilteredHostRequestAuthority(),
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1", Timeout: time.Second,
	}
}

func protocolV2RouteTestResponse(request *pluginwire.RouteRequest, body map[string]any) *pluginwire.RouteResponse {
	value, _ := structpb.NewStruct(body)
	return &pluginwire.RouteResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(), Trace: proto.Clone(request.GetContext().GetTrace()).(*protocolwire.TraceContext),
			Extension: proto.Clone(request.GetContext().GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
		},
		StatusCode: http.StatusCreated,
		Headers:    []*protocolwire.Header{{Name: "X-Result", Values: []string{"one", "two"}}},
		Body:       &protocolwire.TypedDocument{SchemaId: "demo.response", SchemaVersion: "1", Value: value},
	}
}

// 同意式读取：只有「路由/管理端调用 + 活 actor + 精确制品声明 users.email.read」
// 才签发 self-email 的 query 委派；匿名、后台、无能力都不签发。
func TestProtocolV2RouteIssuesConsentQueryDelegationOnlyWithCapability(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = request
		return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
	})
	issuer := &recordingProtocolV2QueryDelegationIssuer{grant: hostapi.ProtocolV2QueryActorDelegationGrant{
		QueryID: hostapi.QuerySelfEmailRegistryID, ContractVersion: hostapi.QuerySelfEmailRegistryContractVersion,
		PlanVersion:    hostapi.QuerySelfEmailRegistryPlanVersion,
		ResultSchemaID: hostapi.QuerySelfEmailSchemaID, ResultSchemaVersion: "1",
		Scope: hostapi.ProtocolV2ConsentQueryScope, Token: "self-email-token",
	}}
	client.queryDelegations = issuer
	client.capabilities = capabilities.NewSet([]string{capabilities.UsersEmailRead})
	request := protocolV2RouteTestRequest()
	request.Actor = NewProtocolV2RouteActor(42, true, nil)
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 1 || issuer.request.ActorUserID != 42 ||
		issuer.request.QueryID != hostapi.QuerySelfEmailRegistryID ||
		issuer.request.Scope != hostapi.ProtocolV2ConsentQueryScope ||
		issuer.request.MaxCost != hostapi.ProtocolV2ConsentQueryMaxCost ||
		issuer.request.Runtime.GetInstanceId() != client.identity.GetInstanceId() {
		t.Fatalf("issuer request = %#v", issuer.request)
	}
	delegations := received.GetContext().GetHostQueryDelegations()
	if len(delegations) != 1 || delegations[0].GetQueryId() != hostapi.QuerySelfEmailRegistryID ||
		delegations[0].GetContractVersion() != hostapi.QuerySelfEmailRegistryContractVersion ||
		delegations[0].GetPlanVersion() != hostapi.QuerySelfEmailRegistryPlanVersion ||
		delegations[0].GetResultSchemaId() != hostapi.QuerySelfEmailSchemaID ||
		delegations[0].GetResultSchemaVersion() != "1" ||
		delegations[0].GetScope() != hostapi.ProtocolV2ConsentQueryScope ||
		delegations[0].GetToken() != "self-email-token" {
		t.Fatalf("route context delegations = %#v", delegations)
	}

	// 无能力：不签发，也不带任何委派。
	issuer.calls = 0
	client.capabilities = capabilities.NewSet([]string{capabilities.HostAPI})
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 0 || len(received.GetContext().GetHostQueryDelegations()) != 0 {
		t.Fatalf("artifact without users.email.read acquired a delegation: %#v", received.GetContext().GetHostQueryDelegations())
	}

	// 匿名：没有可绑定的 actor，不签发（公开路由仍然正常工作）。
	client.capabilities = capabilities.NewSet([]string{capabilities.UsersEmailRead})
	request.Actor = nil
	if _, err := client.InvokeRouteContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 0 || len(received.GetContext().GetHostQueryDelegations()) != 0 {
		t.Fatal("anonymous invocation acquired a consent query delegation")
	}

	// 后台（job/schedule/queue）走 requestContext，永远拿不到委派。
	request.Actor = NewProtocolV2RouteActor(42, true, nil)
	background := client.requestContext(context.Background(), "background-job")
	if len(background.GetHostQueryDelegations()) != 0 || background.GetActor() != nil {
		t.Fatalf("background context acquired actor authority: %#v", background)
	}
}

func TestProtocolV2RouteFailsClosedOnConsentDelegationIssuer(t *testing.T) {
	request := protocolV2RouteTestRequest()
	request.Actor = NewProtocolV2RouteActor(42, true, nil)
	for _, test := range []struct {
		name   string
		issuer hostapi.ProtocolV2QueryActorDelegationIssuer
		want   error
	}{
		{name: "issuer unavailable", want: ErrProtocolV2ActorDelegationUnavailable},
		{name: "issuer failure", issuer: &recordingProtocolV2QueryDelegationIssuer{
			err: errors.New("signing key unavailable"),
		}, want: ErrProtocolV2ActorDelegationUnavailable},
		{name: "wrong query", issuer: &recordingProtocolV2QueryDelegationIssuer{
			grant: hostapi.ProtocolV2QueryActorDelegationGrant{
				QueryID: "core.query.site_stats.current", ContractVersion: "core.query.site_stats.current@1",
				PlanVersion:    "core.query.site_stats.current.plan@1",
				ResultSchemaID: hostapi.QuerySiteStatsResultSchemaID, ResultSchemaVersion: "1",
				Scope: "route", Token: "other-token",
			},
		}, want: ErrProtocolV2ActorDelegationInvalid},
		{name: "missing token", issuer: &recordingProtocolV2QueryDelegationIssuer{
			grant: hostapi.ProtocolV2QueryActorDelegationGrant{
				QueryID: hostapi.QuerySelfEmailRegistryID, ContractVersion: hostapi.QuerySelfEmailRegistryContractVersion,
				PlanVersion:    hostapi.QuerySelfEmailRegistryPlanVersion,
				ResultSchemaID: hostapi.QuerySelfEmailSchemaID, ResultSchemaVersion: "1",
				Scope: hostapi.ProtocolV2ConsentQueryScope,
			},
		}, want: ErrProtocolV2ActorDelegationInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			client := newProtocolV2RouteTestClient(t, "runtime-1", func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				called = true
				return protocolV2RouteTestResponse(request, map[string]any{"ok": true}), nil
			})
			client.capabilities = capabilities.NewSet([]string{capabilities.UsersEmailRead})
			client.queryDelegations = test.issuer
			if _, err := client.InvokeRouteContext(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			if called {
				t.Fatal("malformed consent delegation reached the plugin")
			}
		})
	}
}

type recordingProtocolV2QueryDelegationIssuer struct {
	calls   int
	request hostapi.ProtocolV2QueryActorDelegationRequest
	grant   hostapi.ProtocolV2QueryActorDelegationGrant
	err     error
}

func (i *recordingProtocolV2QueryDelegationIssuer) IssueProtocolV2QueryActorDelegation(
	_ context.Context,
	request hostapi.ProtocolV2QueryActorDelegationRequest,
) (hostapi.ProtocolV2QueryActorDelegationGrant, error) {
	if i == nil {
		return hostapi.ProtocolV2QueryActorDelegationGrant{}, hostapi.ErrProtocolV2QueryDelegationInvalid
	}
	i.calls++
	i.request = request
	if i.err != nil {
		return hostapi.ProtocolV2QueryActorDelegationGrant{}, i.err
	}
	return i.grant, nil
}
