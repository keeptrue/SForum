package hostapi

import (
	"context"
	"testing"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2TopicVisibilityCommandPlansStateAndIndexTogether(t *testing.T) {
	revision := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
	request := protocolV2TopicVisibilityRequest(t, map[string]any{"topicId": "42", "action": forum.TopicActionHide}, revision.Format(time.RFC3339Nano))
	definition := newProtocolV2TopicVisibilityCommandDefinition(nil, nil)
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorDelegated || len(definition.RequiredPermissions) != 1 ||
		definition.RequiredPermissions[0] != identity.PermissionTopicDeleteAny {
		t.Fatalf("unexpected actor policy: %#v", definition)
	}
	if len(plan.Impact) != 2 || plan.Impact[0].GetModule() != "forum" || plan.Impact[1].GetModule() != "search" {
		t.Fatalf("unexpected impact: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["status"] != forum.TopicStatusHidden || values["planned"] != true || values["topicId"] != "42" {
		t.Fatalf("unexpected projected result: %#v", values)
	}
}

func TestProtocolV2TopicVisibilityCommandRejectsUnsafeShapes(t *testing.T) {
	revision := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	tests := []struct {
		name     string
		input    map[string]any
		revision string
	}{
		{name: "numeric id", input: map[string]any{"topicId": 42, "action": forum.TopicActionHide}, revision: revision},
		{name: "unsupported action", input: map[string]any{"topicId": "42", "action": "delete"}, revision: revision},
		{name: "unknown field", input: map[string]any{"topicId": "42", "action": forum.TopicActionHide, "force": true}, revision: revision},
		{name: "missing revision", input: map[string]any{"topicId": "42", "action": forum.TopicActionHide}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := protocolV2TopicVisibilityRequest(t, test.input, test.revision)
			_, err := protocolV2TopicVisibilityMutationFromRequest(request)
			if err == nil || protocolV2CommandErrorDetail(err, "fallback").GetReason() != "host.command_input_invalid" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func protocolV2TopicVisibilityRequest(t *testing.T, values map[string]any, revision string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "example.content"}, IdempotencyKey: "topic-42"},
		CommandId: CommandTopicVisibilitySetID, CommandVersion: CommandTopicVisibilitySetVersion,
		IdempotencyKey: "topic-42", ExpectedRevision: revision,
		Input: &protocolv2.TypedDocument{SchemaId: CommandTopicVisibilityInputSchemaID, SchemaVersion: CommandTopicVisibilitySchemaVersion, Value: value},
	}
}
