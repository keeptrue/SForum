package hostapi

import (
	"context"
	"testing"

	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2EntityMetaCommandPlansAtomicOwnedFieldBatch(t *testing.T) {
	request := protocolV2EntityMetaRequest(t, map[string]any{
		"entityType": "topic", "entityId": "42",
		"values": []any{
			map[string]any{"fieldKey": "example.score", "value": 9},
			map[string]any{"fieldKey": "example.note", "value": nil},
		},
	})
	definition := newProtocolV2EntityMetaCommandDefinition(nil)
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorDelegated || len(definition.RequiredPermissions) != 1 ||
		definition.RequiredPermissions[0] != identity.PermissionEntityMetaManage {
		t.Fatalf("unexpected actor policy: %#v", definition)
	}
	if len(plan.Impact) != 2 || plan.Impact[0].GetAction() != "upsert" || plan.Impact[1].GetAction() != "delete" {
		t.Fatalf("unexpected impacts: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["entityId"] != "42" || len(values["values"].([]any)) != 2 {
		t.Fatalf("unexpected projection: %#v", values)
	}
}

func TestProtocolV2EntityMetaCommandRejectsAmbiguousBatch(t *testing.T) {
	tests := []map[string]any{
		{"entityType": "comment", "entityId": "42", "values": []any{map[string]any{"fieldKey": "x", "value": true}}},
		{"entityType": "user", "entityId": 42, "values": []any{map[string]any{"fieldKey": "x", "value": true}}},
		{"entityType": "user", "entityId": "42", "values": []any{}},
		{"entityType": "user", "entityId": "42", "values": []any{map[string]any{"fieldKey": "x", "value": true}, map[string]any{"fieldKey": "x", "value": false}}},
		{"entityType": "user", "entityId": "42", "values": []any{map[string]any{"fieldKey": "x", "value": true, "visibility": "admin"}}},
	}
	for index, input := range tests {
		request := protocolV2EntityMetaRequest(t, input)
		if _, err := protocolV2EntityMetaMutationFromRequest(request); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
	request := protocolV2EntityMetaRequest(t, map[string]any{
		"entityType": entitymeta.EntityUser, "entityId": "42",
		"values": []any{map[string]any{"fieldKey": "x", "value": true}},
	})
	request.ExpectedRevision = "forbidden"
	if _, err := protocolV2EntityMetaMutationFromRequest(request); err == nil {
		t.Fatal("expected unsupported aggregate revision to fail")
	}
}

func protocolV2EntityMetaRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "example.meta"}, IdempotencyKey: "meta-42"},
		CommandId: CommandEntityMetaValuesUpsertID, CommandVersion: CommandEntityMetaValuesUpsertVersion,
		IdempotencyKey: "meta-42",
		Input:          &protocolv2.TypedDocument{SchemaId: CommandEntityMetaValuesInputSchemaID, SchemaVersion: CommandEntityMetaValuesSchemaVersion, Value: value},
	}
}
