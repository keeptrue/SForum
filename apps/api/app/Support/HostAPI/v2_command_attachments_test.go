package hostapi

import (
	"context"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2AttachmentStatusCommandPlansMetadataOnly(t *testing.T) {
	revision := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	request := protocolV2AttachmentStatusRequest(t, map[string]any{
		"attachmentId": "42", "status": protocolV2AttachmentStatusDisabled,
	}, revision.Format(time.RFC3339Nano))
	definition := newProtocolV2AttachmentStatusCommandDefinition(nil)
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorDelegated || len(definition.RequiredPermissions) != 1 ||
		definition.RequiredPermissions[0] != identity.PermissionAttachmentManage {
		t.Fatalf("unexpected actor policy: %#v", definition)
	}
	if len(plan.Impact) != 1 || plan.Impact[0].GetModule() != "attachments" || !plan.Impact[0].GetReversible() {
		t.Fatalf("unexpected impact: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["attachmentId"] != "42" || values["status"] != protocolV2AttachmentStatusDisabled {
		t.Fatalf("unexpected projection: %#v", values)
	}
	if _, exposed := values["objectKey"]; exposed {
		t.Fatal("attachment command must not expose storage object keys")
	}
}

func TestProtocolV2AttachmentStatusCommandRejectsDeleteAndAmbiguousShapes(t *testing.T) {
	revision := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	tests := []struct {
		input    map[string]any
		revision string
	}{
		{input: map[string]any{"attachmentId": 42, "status": protocolV2AttachmentStatusDisabled}, revision: revision},
		{input: map[string]any{"attachmentId": "42", "status": protocolV2AttachmentStatusDeleted}, revision: revision},
		{input: map[string]any{"attachmentId": "42", "status": protocolV2AttachmentStatusActive, "deleteObject": true}, revision: revision},
		{input: map[string]any{"attachmentId": "42", "status": protocolV2AttachmentStatusActive}},
	}
	for index, test := range tests {
		request := protocolV2AttachmentStatusRequest(t, test.input, test.revision)
		if _, err := protocolV2AttachmentStatusMutationFromRequest(request); err == nil {
			t.Fatalf("case %d unexpectedly accepted", index)
		}
	}
}

func protocolV2AttachmentStatusRequest(t *testing.T, values map[string]any, revision string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "example.media"}, IdempotencyKey: "attachment-42"},
		CommandId: CommandAttachmentStatusSetID, CommandVersion: CommandAttachmentStatusSetVersion,
		IdempotencyKey: "attachment-42", ExpectedRevision: revision,
		Input: &protocolv2.TypedDocument{SchemaId: CommandAttachmentStatusInputSchemaID, SchemaVersion: CommandAttachmentStatusSchemaVersion, Value: value},
	}
}
