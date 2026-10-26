package pluginv2

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIdentityProviderRegistryDispatchesExactReservedOperation(t *testing.T) {
	var calls atomic.Int32
	registry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(func(
		_ context.Context,
		call *IdentityProviderCall,
	) (*protocolwire.TypedDocument, error) {
		calls.Add(1)
		if call.ID != "demo.plugin.identity.risk" || call.ContractVersion != "demo.plugin.identity.risk@1" ||
			call.Kind != "risk" || call.Handler != "identity.risk" || call.Operation != "risk.evaluate" ||
			!DocumentMatchesSchema(call.Input, "demo.plugin.identity.risk.input@1") {
			t.Fatalf("unexpected identity call: %#v", call)
		}
		return NewTypedDocument("demo.plugin.identity.risk.output@1", map[string]any{"disposition": "allow"})
	}))
	if err != nil {
		t.Fatal(err)
	}

	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Execute != nil || len(definitions[0].Operations) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	definitions[0].Operations[0].Name = "changed"
	if registry.Definitions()[0].Operations[0].Name != "risk.evaluate" {
		t.Fatal("Definitions leaked mutable operation storage")
	}

	response, err := registry.ProviderCall(t.Context(), testIdentityProviderRequest(familyTestContext(familyTestIdentity())))
	if err != nil || response.GetError() != nil || calls.Load() != 1 ||
		!DocumentMatchesSchema(response.GetOutput(), "demo.plugin.identity.risk.output@1") {
		t.Fatalf("response = %#v, calls = %d, error = %v", response, calls.Load(), err)
	}
}

func TestIdentityProviderRegistryRejectsDriftAndUnsafeContext(t *testing.T) {
	var calls atomic.Int32
	registry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(func(
		context.Context,
		*IdentityProviderCall,
	) (*protocolwire.TypedDocument, error) {
		calls.Add(1)
		return NewTypedDocument("demo.plugin.identity.risk.output@1", nil)
	}))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*pluginwire.ProviderCallRequest)
		reason string
	}{
		{"slot", func(request *pluginwire.ProviderCallRequest) { request.SlotId = "mail.provider" }, "identity_provider.slot_mismatch"},
		{"provider", func(request *pluginwire.ProviderCallRequest) { request.DeclarationId = "demo.plugin.identity.missing" }, "identity_provider.not_found"},
		{"contract", func(request *pluginwire.ProviderCallRequest) { request.ContractVersion = "demo.plugin.identity.risk@2" }, "identity_provider.contract_mismatch"},
		{"operation", func(request *pluginwire.ProviderCallRequest) { request.Operation = "session.evaluate" }, "identity_provider.operation_not_declared"},
		{"schema", func(request *pluginwire.ProviderCallRequest) { request.Input.SchemaId = "wrong" }, "identity_provider.schema_mismatch"},
		{"authority", func(request *pluginwire.ProviderCallRequest) {
			request.Context.GrantedAuthority = []*protocolwire.AuthorityGrant{{Key: "users.read"}}
		}, "identity_provider.context_authority_forbidden"},
		{"unsafe actor", func(request *pluginwire.ProviderCallRequest) {
			request.Context.Actor = &protocolwire.Actor{UserId: 7, SessionId: "secret-session"}
		}, "identity_provider.context_actor_unsafe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testIdentityProviderRequest(familyTestContext(familyTestIdentity()))
			test.change(request)
			response, callErr := registry.ProviderCall(t.Context(), request)
			if callErr != nil || response.GetError().GetReason() != test.reason {
				t.Fatalf("response = %#v, error = %v", response, callErr)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached handler %d times", calls.Load())
	}
}

func TestIdentityProviderRegistryRejectsInvalidDefinitionMatrix(t *testing.T) {
	handler := func(context.Context, *IdentityProviderCall) (*protocolwire.TypedDocument, error) {
		return NewTypedDocument("demo.plugin.identity.risk.output@1", nil)
	}
	tests := []struct {
		name   string
		change func(*IdentityProviderDefinition)
	}{
		{"unknown kind", func(definition *IdentityProviderDefinition) { definition.Kind = "unknown" }},
		{"kind operation mismatch", func(definition *IdentityProviderDefinition) { definition.Kind = "session" }},
		{"non fixed policy", func(definition *IdentityProviderDefinition) {
			definition.Operations[0].FailurePolicy = extensionmanifest.IdentityProviderFailureOmit
		}},
		{"duplicate operation", func(definition *IdentityProviderDefinition) {
			definition.Operations = append(definition.Operations, definition.Operations[0])
		}},
		{"zero timeout", func(definition *IdentityProviderDefinition) { definition.Operations[0].TimeoutMS = 0 }},
		{"excess timeout", func(definition *IdentityProviderDefinition) {
			definition.Operations[0].TimeoutMS = extensionmanifest.ManifestIdentityProviderMaximumTimeoutMS + 1
		}},
		{"path schema", func(definition *IdentityProviderDefinition) {
			definition.Operations[0].InputSchema = "schemas/risk-input.json"
		}},
		{"nil handler", func(definition *IdentityProviderDefinition) { definition.Execute = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := testIdentityProviderDefinition(handler)
			test.change(&definition)
			if _, err := NewIdentityProviderRegistry(definition); !errors.Is(err, ErrInvalidIdentityProviderDefinition) {
				t.Fatalf("definition error = %v", err)
			}
		})
	}
}

func TestIdentityProviderRegistryOwnsDefinitionsAndDocuments(t *testing.T) {
	handlerOutput, err := NewTypedDocument(
		"demo.plugin.identity.risk.output@1",
		map[string]any{"disposition": "allow"},
	)
	if err != nil {
		t.Fatal(err)
	}
	definition := testIdentityProviderDefinition(func(
		_ context.Context,
		call *IdentityProviderCall,
	) (*protocolwire.TypedDocument, error) {
		call.Context.RequestId = "handler-mutated"
		call.Input.Value.Fields["signal"] = structpb.NewStringValue("handler-mutated")
		return handlerOutput, nil
	})
	registry, err := NewIdentityProviderRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition.Operations[0].Name = "caller-mutated"
	if got := registry.Definitions()[0].Operations[0].Name; got != "risk.evaluate" {
		t.Fatalf("registered operation changed through caller storage: %q", got)
	}

	request := testIdentityProviderRequest(familyTestContext(familyTestIdentity()))
	response, err := registry.ProviderCall(t.Context(), request)
	if err != nil || response.GetError() != nil {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
	if request.GetContext().GetRequestId() != "req-1" ||
		TypedDocumentValues(request.GetInput())["signal"] != "login" {
		t.Fatalf("handler mutated caller request: %#v", request)
	}
	handlerOutput.Value.Fields["disposition"] = structpb.NewStringValue("deny")
	if got := TypedDocumentValues(response.GetOutput())["disposition"]; got != "allow" {
		t.Fatalf("handler retained response output storage: %#v", got)
	}
}

func TestIdentityProviderRegistryValidatesAndCleansHandlerFailures(t *testing.T) {
	tests := []struct {
		name        string
		handler     IdentityProviderHandler
		code        protocolwire.ErrorCode
		reason      string
		message     string
		notContains string
	}{
		{
			name: "typed family error",
			handler: func(context.Context, *IdentityProviderCall) (*protocolwire.TypedDocument, error) {
				return nil, &FamilyError{
					Code:   protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
					Reason: "identity_provider.denied", Message: "Provider denied the proposal.",
				}
			},
			code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED, reason: "identity_provider.denied",
			message: "Provider denied the proposal.",
		},
		{
			name: "opaque error",
			handler: func(context.Context, *IdentityProviderCall) (*protocolwire.TypedDocument, error) {
				return nil, errors.New("private implementation detail")
			},
			code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, reason: "identity_provider.handler_failed",
			message: "Plugin identity provider handler failed.", notContains: "private implementation detail",
		},
		{
			name: "output schema mismatch",
			handler: func(context.Context, *IdentityProviderCall) (*protocolwire.TypedDocument, error) {
				return NewTypedDocument("demo.plugin.identity.wrong@1", nil)
			},
			code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, reason: "identity_provider.schema_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(test.handler))
			if err != nil {
				t.Fatal(err)
			}
			response, callErr := registry.ProviderCall(
				t.Context(),
				testIdentityProviderRequest(familyTestContext(familyTestIdentity())),
			)
			detail := response.GetError()
			if callErr != nil || detail.GetCode() != test.code || detail.GetReason() != test.reason ||
				(test.message != "" && detail.GetMessage() != test.message) ||
				(test.notContains != "" && strings.Contains(detail.GetMessage(), test.notContains)) {
				t.Fatalf("response = %#v, error = %v", response, callErr)
			}
		})
	}
}

func TestIdentityProviderRegistryPropagatesCancellationAndDeadline(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		entered := make(chan struct{})
		registry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(func(
			ctx context.Context,
			_ *IdentityProviderCall,
		) (*protocolwire.TypedDocument, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			<-entered
			cancel()
		}()
		response, callErr := registry.ProviderCall(
			ctx,
			testIdentityProviderRequest(familyTestContext(familyTestIdentity())),
		)
		if response != nil || !errors.Is(callErr, context.Canceled) {
			t.Fatalf("response = %#v, error = %v", response, callErr)
		}
	})

	t.Run("request deadline", func(t *testing.T) {
		registry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(func(
			ctx context.Context,
			_ *IdentityProviderCall,
		) (*protocolwire.TypedDocument, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		if err != nil {
			t.Fatal(err)
		}
		request := testIdentityProviderRequest(familyTestContext(familyTestIdentity()))
		request.Context.Deadline = timestamppb.New(time.Now().Add(100 * time.Millisecond))
		response, callErr := registry.ProviderCall(t.Context(), request)
		if response != nil || !errors.Is(callErr, context.DeadlineExceeded) {
			t.Fatalf("response = %#v, error = %v", response, callErr)
		}
	})
}

func TestServerIdentityProviderDispatchRequiresExactFeatureAndRegistry(t *testing.T) {
	for _, test := range []struct {
		name             string
		serverRegistry   bool
		hostFeature      *protocolwire.ProtocolFeature
		wantIdentityCall bool
	}{
		{name: "missing registry", hostFeature: IdentityRuntimeProtocolFeature()},
		{name: "missing feature", serverRegistry: true},
		{name: "wrong feature", serverRegistry: true, hostFeature: &protocolwire.ProtocolFeature{Name: IdentityRuntimeFeatureName, Version: "2"}},
		{name: "exact", serverRegistry: true, hostFeature: IdentityRuntimeProtocolFeature(), wantIdentityCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var identityCalls, genericCalls atomic.Int32
			identityRegistry, err := NewIdentityProviderRegistry(testIdentityProviderDefinition(func(
				context.Context,
				*IdentityProviderCall,
			) (*protocolwire.TypedDocument, error) {
				identityCalls.Add(1)
				return NewTypedDocument("demo.plugin.identity.risk.output@1", map[string]any{"disposition": "allow"})
			}))
			if err != nil {
				t.Fatal(err)
			}
			genericRegistry, err := NewProviderRegistry(ProviderDefinition{
				ID: "demo.plugin.generic", Slot: "demo.generic", ContractVersion: "demo.plugin.generic@1",
				Label: "Generic", Handler: "generic.invoke",
				RequestSchema: "demo.plugin.generic.input@1", ResponseSchema: "demo.plugin.generic.output@1",
				Execute: func(context.Context, *ProviderCall) (*protocolwire.TypedDocument, error) {
					genericCalls.Add(1)
					return NewTypedDocument("demo.plugin.generic.output@1", nil)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := NewServer().WithFeatures(IdentityRuntimeProtocolFeature()).WithProviderRegistry(genericRegistry)
			if test.serverRegistry {
				server.WithIdentityProviderRegistry(identityRegistry)
			}
			handshake := validHandshakeRequest()
			if test.hostFeature != nil {
				handshake.HostFeatures = append(handshake.HostFeatures, test.hostFeature)
			}
			handshakeResponse, err := server.Handshake(t.Context(), handshake)
			if err != nil || handshakeResponse.GetError() != nil {
				t.Fatalf("handshake = %#v, error = %v", handshakeResponse, err)
			}
			response, callErr := server.ProviderCall(t.Context(), testIdentityProviderRequest(handshake.Context))
			if test.wantIdentityCall {
				if callErr != nil || response.GetError() != nil || identityCalls.Load() != 1 {
					t.Fatalf("response = %#v, identity calls = %d, error = %v", response, identityCalls.Load(), callErr)
				}
			} else if status.Code(callErr) != codes.Unimplemented || response != nil || identityCalls.Load() != 0 {
				t.Fatalf("response = %#v, identity calls = %d, code = %v", response, identityCalls.Load(), status.Code(callErr))
			}
			if genericCalls.Load() != 0 {
				t.Fatalf("reserved identity call fell through generic registry %d times", genericCalls.Load())
			}
		})
	}
}

func TestGenericProviderRegistryRejectsReservedIdentitySlot(t *testing.T) {
	_, err := NewProviderRegistry(ProviderDefinition{
		ID: "demo.plugin.identity", Slot: IdentityRuntimeProviderSlot, ContractVersion: "demo.plugin.identity@1",
		Label: "Identity", Handler: "identity.invoke",
		RequestSchema: "demo.plugin.identity.input@1", ResponseSchema: "demo.plugin.identity.output@1",
		Execute: func(context.Context, *ProviderCall) (*protocolwire.TypedDocument, error) { return nil, nil },
	})
	if !errors.Is(err, ErrInvalidProviderDefinition) {
		t.Fatalf("reserved generic provider error = %v", err)
	}
}

func testIdentityProviderDefinition(handler IdentityProviderHandler) IdentityProviderDefinition {
	return IdentityProviderDefinition{
		ID: "demo.plugin.identity.risk", ContractVersion: "demo.plugin.identity.risk@1",
		Kind: "risk", Handler: "identity.risk",
		Operations: []IdentityProviderOperationDefinition{{
			Name: "risk.evaluate", InputSchema: "demo.plugin.identity.risk.input@1",
			OutputSchema: "demo.plugin.identity.risk.output@1", TimeoutMS: 1000,
			FailurePolicy: extensionmanifest.IdentityProviderFailureFailClosed,
		}},
		Execute: handler,
	}
}

func testIdentityProviderRequest(ctx *protocolwire.RequestContext) *pluginwire.ProviderCallRequest {
	input, err := NewTypedDocument("demo.plugin.identity.risk.input@1", map[string]any{"signal": "login"})
	if err != nil {
		panic(err)
	}
	return &pluginwire.ProviderCallRequest{
		Context: ctx, SlotId: IdentityRuntimeProviderSlot, Operation: "risk.evaluate",
		ContractVersion: "demo.plugin.identity.risk@1", DeclarationId: "demo.plugin.identity.risk", Input: input,
	}
}
