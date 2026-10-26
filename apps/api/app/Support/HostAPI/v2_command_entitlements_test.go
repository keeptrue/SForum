package hostapi

import (
	"context"
	"strings"
	"testing"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2EntitlementCommandPlansProviderNeutralGrant(t *testing.T) {
	definition := newProtocolV2EntitlementCommandDefinition(nil)
	// 权益命令是 actorless service authority：后台/插件服务调用，不接受 actor delegation。
	if definition.ActorMode != protocolV2CommandActorService || len(definition.RequiredPermissions) != 0 {
		t.Fatalf("entitlement actor mode = %q permissions=%#v", definition.ActorMode, definition.RequiredPermissions)
	}
	request := protocolV2EntitlementCommandRequest(t, map[string]any{
		"action":    "grant",
		"subject":   map[string]any{"type": "user", "id": "42"},
		"scope":     map[string]any{"kind": "capability", "capability": "forum.supporter"},
		"source":    map[string]any{"type": "membership", "id": "order-7"},
		"validFrom": time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	})

	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Policy) != 1 || !plan.Policy[0].GetAllowed() || len(plan.Impact) != 1 || plan.Impact[0].GetAction() != "grant" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if plan.ProjectedResult.GetSchemaId() != CommandEntitlementsMutationOutputSchemaID ||
		plan.ProjectedResult.GetValue().AsMap()["planned"] != true {
		t.Fatalf("unexpected projected result: %#v", plan.ProjectedResult)
	}
	key, err := protocolV2EntitlementIdempotencyKey(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "hostcmd:") || len(key) != len("hostcmd:")+64 {
		t.Fatalf("unexpected internal idempotency key %q", key)
	}
}

func TestProtocolV2EntitlementCommandRejectsAmbiguousInputs(t *testing.T) {
	definition := newProtocolV2EntitlementCommandDefinition(nil)
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{name: "unknown field", input: map[string]any{"action": "grant", "unknown": true}, expected: "host.command_input_invalid"},
		{name: "provider billing field", input: map[string]any{"action": "grant", "currency": "USD"}, expected: "host.command_input_invalid"},
		{name: "transition without id", input: map[string]any{"action": "revoke"}, expected: "host.command_input_invalid"},
		{name: "unknown action", input: map[string]any{"action": "charge"}, expected: "host.command_input_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := protocolV2EntitlementCommandRequest(t, test.input)
			if test.name == "transition without id" {
				request.ExpectedRevision = "1"
			}
			_, err := definition.Preview(context.Background(), request)
			if err == nil || protocolV2CommandErrorDetail(err, "fallback").GetReason() != test.expected {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProtocolV2EntitlementCommandRequiresRevisionForTransitions(t *testing.T) {
	request := protocolV2EntitlementCommandRequest(t, map[string]any{"action": "revoke", "entitlementId": "9"})
	if _, err := protocolV2EntitlementMutationFromRequest(request); err == nil {
		t.Fatal("expected transition without expected revision to fail")
	}
	request.ExpectedRevision = "3"
	mutation, err := protocolV2EntitlementMutationFromRequest(request)
	if err != nil || mutation.entitlementID != 9 || mutation.transition.EntitlementID != 9 || mutation.expectedRevision != 3 {
		t.Fatalf("mutation = %#v, %v", mutation, err)
	}
	request.ExpectedRevision = "invalid"
	if _, err := protocolV2EntitlementMutationFromRequest(request); err == nil {
		t.Fatal("expected invalid revision to fail during planning")
	}
}

func protocolV2EntitlementCommandRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			Extension:      &protocolv2.ExtensionIdentity{ExtensionId: "example.membership"},
			IdempotencyKey: "request-1",
		},
		CommandId: CommandEntitlementsMutateID, CommandVersion: CommandEntitlementsMutateVersion,
		IdempotencyKey: "request-1",
		Input: &protocolv2.TypedDocument{
			SchemaId: CommandEntitlementsMutationInputSchemaID, SchemaVersion: CommandEntitlementsMutationSchemaVersion,
			Value: value,
		},
	}
}
