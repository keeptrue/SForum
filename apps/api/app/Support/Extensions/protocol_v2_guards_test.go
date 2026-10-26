package extensionsruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtocolV2CustomGuardInvokesExactFrozenContract(t *testing.T) {
	var received *pluginwire.RouteRequest
	client := protocolV2GuardTestClient(t, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		received = proto.Clone(request).(*pluginwire.RouteRequest)
		return protocolV2GuardTestResponse(request, http.StatusNoContent), nil
	})
	request := protocolV2GuardTestRequest()
	request.Headers = http.Header{
		"X-Request-ID": {"request-41"},
		"Cookie":       {"session=secret"}, "Authorization": {"Bearer secret"},
		"X-API-Key": {"api-key-secret"}, "X-Auth-Token": {"auth-token-secret"},
		"connection": {"X-Guard-Lower-Hop"}, "CONNECTION": {"X-Guard-Upper-Hop"},
		"X-Guard-Lower-Hop": {"lower-secret"}, "X-Guard-Upper-Hop": {"upper-secret"},
	}
	request.PathParameters = map[string]string{"topic": "41"}
	request.QueryParameters = map[string]string{"preview": "1"}
	request.Body = map[string]any{"title": "hello"}
	request.BodyPresent = true
	request.Actor = NewProtocolV2RouteActor(42, true, map[string]bool{"topic.write": true})

	if err := client.InvokeGuardContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if received == nil || received.GetRouteId() != request.GuardID ||
		received.GetContractVersion() != request.GuardContractVersion || received.GetMethod() != request.Method ||
		received.GetRequestAuthorityMode() != pluginwire.RouteRequestAuthorityMode_ROUTE_REQUEST_AUTHORITY_MODE_FILTERED ||
		received.GetGuardKind() != pluginwire.RouteGuardKind_ROUTE_GUARD_KIND_CUSTOM ||
		received.GetPath() != request.Path || received.GetPathParameters()["topic"] != "41" ||
		received.GetQueryParameters()["preview"] != "1" || received.GetContext().GetActor().GetUserId() != 42 ||
		received.GetBody().GetSchemaId() != "demo.request" || received.GetBody().GetSchemaVersion() != "1" {
		t.Fatalf("guard request = %#v", received)
	}
	headers, err := protocolV2RouteHTTPHeaders(received.GetHeaders())
	if err != nil || headers.Get("X-SForum-Guard-Route-ID") != request.RouteID ||
		headers.Get("X-SForum-Guard-Route-Contract") != request.RouteContractVersion ||
		headers.Get("X-SForum-Guard-Kind") != "custom" || headers.Get("X-Request-ID") != "request-41" {
		t.Fatalf("guard headers = %#v, %v", headers, err)
	}
	assertProtocolV2CredentialHeaders(t, headers, request.Headers, false)
	assertProtocolV2BlockedHeaders(t, headers, "Connection", "X-Guard-Lower-Hop", "X-Guard-Upper-Hop")
}

func TestProtocolV2CustomGuardMapsDenyAndRejectsMutation(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		client := protocolV2GuardTestClient(t, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
			return protocolV2GuardTestResponse(request, status), nil
		})
		if err := client.InvokeGuardContext(context.Background(), protocolV2GuardTestRequest()); !errors.Is(err, ErrProtocolV2GuardDenied) {
			t.Fatalf("status %d error = %v", status, err)
		} else {
			assertProtocolV2GuardCallFailure(t, err, ProtocolV2GuardFailureDenied)
		}
	}

	tests := []struct {
		name   string
		mutate func(*pluginwire.RouteResponse)
	}{
		{name: "success body", mutate: func(response *pluginwire.RouteResponse) {
			response.Body = &protocolwire.TypedDocument{SchemaId: "forged", SchemaVersion: "1", Value: &structpb.Struct{}}
		}},
		{name: "success header", mutate: func(response *pluginwire.RouteResponse) {
			response.Headers = []*protocolwire.Header{{Name: "X-Forged", Values: []string{"1"}}}
		}},
		{name: "stream", mutate: func(response *pluginwire.RouteResponse) { response.StreamFollows = true }},
		{name: "redirect", mutate: func(response *pluginwire.RouteResponse) { response.StatusCode = http.StatusFound }},
		{name: "wrong identity", mutate: func(response *pluginwire.RouteResponse) { response.Context.Extension.InstanceId = "forged" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := protocolV2GuardTestClient(t, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				response := protocolV2GuardTestResponse(request, http.StatusNoContent)
				test.mutate(response)
				return response, nil
			})
			if err := client.InvokeGuardContext(context.Background(), protocolV2GuardTestRequest()); !errors.Is(err, ErrProtocolV2GuardInvalid) {
				t.Fatalf("error = %v", err)
			} else {
				assertProtocolV2GuardCallFailure(t, err, ProtocolV2GuardFailureProtocol)
			}
		})
	}
}

func TestProtocolV2GuardCallFailureClassifiesPostRPCWithoutLeakingPluginText(t *testing.T) {
	const secret = "plugin-reason-secret"
	tests := []struct {
		name   string
		invoke func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error)
		kind   ProtocolV2GuardFailureKind
		compat error
	}{
		{
			name: "crash", kind: ProtocolV2GuardFailureCrash, compat: ErrProtocolV2GuardRuntimeFailed,
			invoke: func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				return nil, status.Error(codes.Unavailable, secret)
			},
		},
		{
			name: "timeout", kind: ProtocolV2GuardFailureTimeout, compat: context.DeadlineExceeded,
			invoke: func(ctx context.Context, _ *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name: "canceled", kind: ProtocolV2GuardFailureCanceled, compat: context.Canceled,
			invoke: func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				return nil, status.Error(codes.Canceled, secret)
			},
		},
		{
			name: "protocol", kind: ProtocolV2GuardFailureProtocol, compat: ErrProtocolV2GuardInvalid,
			invoke: func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
				response := protocolV2GuardTestResponse(request, http.StatusNoContent)
				response.Error = &protocolwire.ErrorDetail{
					Code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, Reason: secret, Message: secret,
				}
				return response, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := protocolV2GuardTestClient(t, test.invoke)
			request := protocolV2GuardTestRequest()
			if test.kind == ProtocolV2GuardFailureTimeout {
				request.Timeout = 10 * time.Millisecond
			}
			err := client.InvokeGuardContext(context.Background(), request)
			assertProtocolV2GuardCallFailure(t, err, test.kind)
			if !errors.Is(err, test.compat) || strings.Contains(err.Error(), secret) {
				t.Fatalf("kind=%q error=%v", test.kind, err)
			}
		})
	}
}

func TestProtocolV2CustomGuardRejectsForgedBindingAndReservedHeaders(t *testing.T) {
	client := protocolV2GuardTestClient(t, func(_ context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
		return protocolV2GuardTestResponse(request, http.StatusNoContent), nil
	})
	tests := []struct {
		name   string
		mutate func(*ProtocolV2GuardRequest)
	}{
		{name: "guard id", mutate: func(request *ProtocolV2GuardRequest) { request.GuardID = "demo.guard.other" }},
		{name: "guard contract", mutate: func(request *ProtocolV2GuardRequest) { request.GuardContractVersion = "demo.guard.owner@2" }},
		{name: "route id", mutate: func(request *ProtocolV2GuardRequest) { request.RouteID = "demo.route.other" }},
		{name: "route contract", mutate: func(request *ProtocolV2GuardRequest) { request.RouteContractVersion = "demo.route@2" }},
		{name: "method", mutate: func(request *ProtocolV2GuardRequest) { request.Method = http.MethodDelete }},
		{name: "schema", mutate: func(request *ProtocolV2GuardRequest) { request.RequestSchema = "demo.other@1" }},
		{name: "reserved header", mutate: func(request *ProtocolV2GuardRequest) {
			request.Headers = http.Header{"X-SForum-Actor-ID": []string{"42"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := protocolV2GuardTestRequest()
			test.mutate(&request)
			if err := client.InvokeGuardContext(context.Background(), request); !errors.Is(err, ErrProtocolV2GuardInvalid) {
				t.Fatalf("error = %v", err)
			} else {
				var callFailure *ProtocolV2GuardCallFailure
				if errors.As(err, &callFailure) {
					t.Fatalf("pre-RPC rejection claimed runtime execution: %#v", callFailure)
				}
			}
		})
	}
}

func assertProtocolV2GuardCallFailure(t *testing.T, err error, kind ProtocolV2GuardFailureKind) {
	t.Helper()
	var failure *ProtocolV2GuardCallFailure
	if !errors.As(err, &failure) || failure.Kind() != kind || !failure.RuntimeExecutionObserved() {
		t.Fatalf("guard call failure kind=%q error=%#v", kind, err)
	}
}

func protocolV2GuardTestClient(
	t *testing.T,
	invoke func(context.Context, *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error),
) *protocolV2Client {
	t.Helper()
	client := newProtocolV2RouteTestClient(t, "runtime-guard", invoke)
	client.guards = []extensions.ManifestGuard{{
		ID: "demo.guard.owner", ContractVersion: "demo.guard.owner@1", Kind: "custom",
		Entry: "backend/guard", Digest: "guard-digest",
	}}
	client.routes = []extensions.ManifestRoute{{
		ID: "demo.route", ContractVersion: "demo.route@1", Action: "add", Guard: "demo.guard.owner",
		Methods: []string{http.MethodPost}, Mode: "http", RequestSchema: "demo.request@1",
	}}
	return client
}

func protocolV2GuardTestRequest() ProtocolV2GuardRequest {
	return ProtocolV2GuardRequest{
		GuardID: "demo.guard.owner", GuardContractVersion: "demo.guard.owner@1",
		RouteID: "demo.route", RouteContractVersion: "demo.route@1",
		Authority: protocolV2FilteredCustomRequestAuthority(),
		Method:    http.MethodPost, Path: "/demo", RequestSchema: "demo.request@1", Timeout: time.Second,
	}
}

func protocolV2GuardTestResponse(request *pluginwire.RouteRequest, status int) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(), Trace: proto.Clone(request.GetContext().GetTrace()).(*protocolwire.TraceContext),
			Extension: proto.Clone(request.GetContext().GetExtension()).(*protocolwire.ExtensionIdentity), ServerTime: timestamppb.Now(),
		},
		StatusCode: uint32(status),
	}
}
