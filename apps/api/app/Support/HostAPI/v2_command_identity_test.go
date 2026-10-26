package hostapi

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2IdentityUserStatusCommandPlansSessionRevocation(t *testing.T) {
	request := protocolV2IdentityUserStatusRequest(t, map[string]any{
		"userId": "42", "status": "disabled", "reason": "membership ended",
	}, "7")
	definition := newProtocolV2IdentityUserStatusCommandDefinition()
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorDelegated || len(definition.RequiredPermissions) != 1 ||
		definition.RequiredPermissions[0] != identity.PermissionUserManage {
		t.Fatalf("unexpected actor policy: %#v", definition)
	}
	if len(plan.Impact) != 2 || plan.Impact[1].GetAction() != "revoke_sessions" {
		t.Fatalf("unexpected impact: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["userId"] != "42" || values["revision"] != "7" {
		t.Fatalf("unexpected projection: %#v", values)
	}
}

func TestProtocolV2IdentityUserStatusCommandRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		input    map[string]any
		revision string
	}{
		{input: map[string]any{"userId": 42, "status": "disabled"}, revision: "1"},
		{input: map[string]any{"userId": "42", "status": "banned"}, revision: "1"},
		{input: map[string]any{"userId": "42", "status": "disabled", "revoke": false}, revision: "1"},
		{input: map[string]any{"userId": "42", "status": "active"}, revision: ""},
		{input: map[string]any{"userId": "42", "status": "active"}, revision: "-1"},
	}
	for index, test := range tests {
		request := protocolV2IdentityUserStatusRequest(t, test.input, test.revision)
		if _, err := protocolV2IdentityUserStatusMutationFromRequest(request); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}

func protocolV2IdentityUserStatusRequest(t *testing.T, values map[string]any, revision string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "example.identity"}, IdempotencyKey: "user-42"},
		CommandId: CommandIdentityUserStatusSetID, CommandVersion: CommandIdentityUserStatusSetVersion,
		IdempotencyKey: "user-42", ExpectedRevision: revision,
		Input: &protocolv2.TypedDocument{SchemaId: CommandIdentityUserStatusInputSchemaID, SchemaVersion: CommandIdentityUserStatusSchemaVersion, Value: value},
	}
}
