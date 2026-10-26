package hostapi

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2ModerationCommandPlansAtomicDecision(t *testing.T) {
	request := protocolV2ModerationRequest(t, map[string]any{
		"source": moderation.SourceReport, "targetType": moderation.TargetTypeTopic,
		"targetId": "42", "reportId": "9", "action": moderation.ActionHideAndClose,
		"reviewNote": "confirmed spam",
	})
	definition := newProtocolV2ModerationCommandDefinition(nil, nil)
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorDelegated || len(definition.RequiredPermissions) != 1 ||
		definition.RequiredPermissions[0] != identity.PermissionModerationReview {
		t.Fatalf("unexpected actor policy: %#v", definition)
	}
	if len(plan.Impact) != 3 || plan.Impact[0].GetModule() != "moderation" || plan.Impact[2].GetModule() != "search" {
		t.Fatalf("unexpected impact: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["targetId"] != "42" || values["action"] != moderation.ActionHideAndClose {
		t.Fatalf("unexpected projection: %#v", values)
	}
}

func TestProtocolV2ModerationCommandRejectsInvalidDecisionShapes(t *testing.T) {
	tests := []map[string]any{
		{"source": moderation.SourceReport, "targetType": moderation.TargetTypeTopic, "targetId": "42", "action": moderation.ActionHideAndClose, "reviewNote": "missing report"},
		{"source": moderation.SourcePrePublish, "targetType": moderation.TargetTypeTopic, "targetId": 42, "action": moderation.ActionApprove},
		{"source": moderation.SourcePrePublish, "targetType": moderation.TargetTypeTopic, "targetId": "42", "action": moderation.ActionHideAndClose},
		{"source": moderation.SourcePrePublish, "targetType": moderation.TargetTypeTopic, "targetId": "42", "action": moderation.ActionReject, "force": true},
	}
	for index, input := range tests {
		request := protocolV2ModerationRequest(t, input)
		if _, err := protocolV2ModerationDecisionFromRequest(request, 7); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}

func protocolV2ModerationRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "example.moderation"}, IdempotencyKey: "moderation-42"},
		CommandId: CommandModerationDecisionSubmitID, CommandVersion: CommandModerationDecisionSubmitVersion,
		IdempotencyKey: "moderation-42",
		Input:          &protocolv2.TypedDocument{SchemaId: CommandModerationDecisionInputSchemaID, SchemaVersion: CommandModerationDecisionSchemaVersion, Value: value},
	}
}
