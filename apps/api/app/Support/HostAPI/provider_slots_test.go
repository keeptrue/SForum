package hostapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2ProviderBrokerUsesOnlyAttestedCallerIdentity(t *testing.T) {
	broker := &recordingProtocolV2ProviderBroker{}
	server := &protocolV2ServiceDiscoveryServer{core: &protocolV2Core{providers: broker}}
	trusted := &protocolv2.ExtensionIdentity{
		ExtensionId: "consumer.b", ExtensionVersion: "1.0.0", ArtifactDigest: "trusted-digest", InstanceId: "trusted-runtime",
	}
	forged := &protocolv2.ExtensionIdentity{
		ExtensionId: "attacker", ExtensionVersion: "9.9.9", ArtifactDigest: "forged", InstanceId: "forged-runtime",
	}
	value, err := structpb.NewStruct(map[string]any{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.InvokeProvider(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), trusted),
		&hostv2.ProviderInvokeRequest{
			Context: &protocolv2.RequestContext{Extension: forged}, SlotId: "provider.a.delivery",
			ContractVersion: "provider.a.delivery@1", Operation: "invoke",
			Input: &protocolv2.TypedDocument{SchemaId: "provider.a.delivery.request", SchemaVersion: "1", Value: value},
		},
	)
	if err != nil || response.GetError() != nil {
		t.Fatalf("provider response = %#v, %v", response, err)
	}
	if broker.input.Caller.ExtensionID != trusted.GetExtensionId() ||
		broker.input.Caller.ExtensionVersion != trusted.GetExtensionVersion() ||
		broker.input.Caller.ArtifactDigest != trusted.GetArtifactDigest() ||
		broker.input.Caller.RuntimeInstanceID != trusted.GetInstanceId() {
		t.Fatalf("broker caller = %#v", broker.input.Caller)
	}
	if broker.input.Caller.ExtensionID == forged.GetExtensionId() || broker.input.InputSchema != "provider.a.delivery.request@1" {
		t.Fatalf("request identity/schema was trusted = %#v", broker.input)
	}
}

func TestProtocolV2ProviderBrokerRejectsUnattestedCaller(t *testing.T) {
	broker := &recordingProtocolV2ProviderBroker{}
	server := &protocolV2ServiceDiscoveryServer{core: &protocolV2Core{providers: broker}}
	response, err := server.InvokeProvider(context.Background(), &hostv2.ProviderInvokeRequest{})
	if err != nil || response.GetError().GetReason() != "host.provider_caller_unattested" || broker.called {
		t.Fatalf("unattested response = %#v, called=%t, err=%v", response, broker.called, err)
	}
}

func TestProtocolV2ProviderBrokerDoesNotLeakInternalErrors(t *testing.T) {
	secret := strings.Repeat("database-password-and-runtime-path", 100)
	detail := protocolV2ProviderFailure(&ProtocolV2ProviderError{
		Reason: "host.provider_response_invalid", Err: errors.New(secret),
	})
	if detail.GetReason() != "host.provider_response_invalid" || strings.Contains(detail.GetMessage(), "password") || len(detail.GetMessage()) > 160 {
		t.Fatalf("public provider error leaked internals = %#v", detail)
	}
}

func TestProtocolV2ProviderPublicErrorCodes(t *testing.T) {
	tests := []struct {
		reason    string
		code      protocolv2.ErrorCode
		retryable bool
	}{
		{"host.provider_caller_denied", protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, false},
		{"host.provider_caller_unattested", protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, false},
		{"host.provider_request_invalid", protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, false},
		{"host.provider_response_invalid", protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, false},
		{"host.provider_not_found", protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, true},
		{"host.provider_timeout", protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, false},
		{"host.provider_cancelled", protocolv2.ErrorCode_ERROR_CODE_CANCELLED, false},
		{"host.provider_broker_unavailable", protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, true},
	}
	for _, test := range tests {
		detail := protocolV2ProviderPublicError(test.reason)
		if detail.GetCode() != test.code || detail.GetRetryable() != test.retryable || detail.GetMessage() == "" {
			t.Fatalf("%s = %#v", test.reason, detail)
		}
	}
}

type recordingProtocolV2ProviderBroker struct {
	called bool
	input  ProtocolV2ProviderInvocation
}

func (b *recordingProtocolV2ProviderBroker) InvokeProtocolV2Provider(_ context.Context, input ProtocolV2ProviderInvocation) (ProtocolV2ProviderResult, error) {
	b.called = true
	b.input = input
	return ProtocolV2ProviderResult{
		ProviderID: "provider.a.delivery", ProviderExtension: "provider.a", RuntimeInstanceID: "provider-runtime",
		ResponseSchema: "provider.a.delivery.response@1", Output: map[string]any{"status": "ok"}, Attempts: 1,
	}, nil
}
