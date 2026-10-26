package hostapi

import (
	"context"
	"errors"
	"testing"
	"time"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2AttachmentExtensionClaimCommandContract(t *testing.T) {
	definition := newProtocolV2AttachmentExtensionClaimCommandDefinition(nil)
	if definition.ActorMode != protocolV2CommandActorDelegated {
		t.Fatalf("actor mode = %q", definition.ActorMode)
	}
	// 能上传才可能认领；归属判定在命令体内完成，普通用户自助建应用不必持
	// attachment.manage。
	if len(definition.RequiredPermissions) != 1 || definition.RequiredPermissions[0] != identity.PermissionAttachmentUpload {
		t.Fatalf("permissions = %#v", definition.RequiredPermissions)
	}
	plan, err := definition.Preview(context.Background(), protocolV2AttachmentExtensionClaimRequest(t, map[string]any{
		"attachmentPublicId": "abc123", "context": "oauth2-client:sfo_1",
	}, time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Policy) != 1 || !plan.Policy[0].GetAllowed() {
		t.Fatalf("policy = %#v", plan.Policy)
	}
	if len(plan.Impact) != 1 || plan.Impact[0].GetModule() != "attachments" || plan.Impact[0].GetAction() != "claim" {
		t.Fatalf("impact = %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["publicId"] != "abc123" {
		t.Fatalf("projection = %#v", values)
	}
	for _, leaked := range []string{"objectKey", "provider", "storagePublicUrl"} {
		if _, exposed := values[leaked]; exposed {
			t.Fatalf("claim result must not expose %q", leaked)
		}
	}
}

func TestProtocolV2AttachmentExtensionClaimRejectsAmbiguousInput(t *testing.T) {
	revision := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	for _, test := range []struct {
		name     string
		input    map[string]any
		revision string
	}{
		{name: "missing context", input: map[string]any{"attachmentPublicId": "abc"}, revision: revision},
		{name: "missing public id", input: map[string]any{"context": "icon"}, revision: revision},
		{name: "unknown field", input: map[string]any{"attachmentPublicId": "abc", "context": "icon", "force": true}, revision: revision},
		{name: "missing revision", input: map[string]any{"attachmentPublicId": "abc", "context": "icon"}},
		{name: "invalid revision", input: map[string]any{"attachmentPublicId": "abc", "context": "icon"}, revision: "not-a-time"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := protocolV2AttachmentExtensionClaimRequest(t, test.input, test.revision)
			if _, err := protocolV2AttachmentExtensionClaimInputFromRequest(request); err == nil {
				t.Fatal("unexpectedly accepted")
			}
		})
	}
}

func TestProtocolV2AttachmentExtensionReleaseRequiresExactlyOneScope(t *testing.T) {
	for _, test := range []struct {
		name  string
		input map[string]any
		ok    bool
	}{
		{name: "context only", input: map[string]any{"context": "oauth2-client:sfo_1"}, ok: true},
		{name: "public id only", input: map[string]any{"attachmentPublicId": "abc"}, ok: true},
		{name: "both", input: map[string]any{"context": "icon", "attachmentPublicId": "abc"}},
		{name: "neither", input: map[string]any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := protocolV2AttachmentExtensionReleaseRequest(t, test.input)
			input, err := protocolV2AttachmentExtensionReleaseInputFromRequest(request)
			if test.ok {
				if err != nil {
					t.Fatal(err)
				}
				if input.ExtensionID != "example.oauth2" {
					t.Fatalf("extension id must come from the verified runtime identity, got %q", input.ExtensionID)
				}
				return
			}
			if err == nil {
				t.Fatal("unexpectedly accepted")
			}
		})
	}
}

func TestProtocolV2AttachmentExtensionListClampsAndProjects(t *testing.T) {
	definition := newProtocolV2AttachmentExtensionListCommandDefinition(nil)
	request := protocolV2AttachmentExtensionListRequest(t, map[string]any{"page": 2, "perPage": 100000})
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["page"] != "2" {
		t.Fatalf("projection = %#v", values)
	}
	if _, isList := values["items"].([]any); !isList {
		t.Fatalf("projection items = %#v", values["items"])
	}
	if _, err := protocolV2AttachmentExtensionListInputFromRequest(protocolV2AttachmentExtensionListRequest(t, map[string]any{
		"page": -1,
	})); err == nil {
		t.Fatal("negative page accepted")
	}
}

func TestProtocolV2AttachmentExtensionErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err    error
		code   protocolv2.ErrorCode
		reason string
	}{
		{err: extensionreference.ErrNotFound, code: protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, reason: "host.attachment_not_found"},
		{err: extensionreference.ErrForbidden, code: protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, reason: "host.attachment_extension_forbidden"},
		{err: extensionreference.ErrTooLarge, code: protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, reason: "host.attachment_extension_too_large"},
		{err: extensionreference.ErrLimitReached, code: protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, reason: "host.attachment_extension_reference_limit"},
		{err: extensionreference.ErrRevisionConflict, code: protocolv2.ErrorCode_ERROR_CODE_CONFLICT, reason: "host.attachment_revision_conflict"},
		{err: extensionreference.ErrConflict, code: protocolv2.ErrorCode_ERROR_CODE_CONFLICT, reason: "host.attachment_extension_conflict"},
		{err: extensionreference.ErrInvalid, code: protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, reason: "host.attachment_extension_invalid"},
	} {
		mapped := protocolV2AttachmentExtensionCommandError(test.err)
		var commandErr *protocolV2CommandError
		if !errors.As(mapped, &commandErr) || commandErr.detail == nil {
			t.Fatalf("%v was not mapped to a Host command error", test.err)
		}
		if commandErr.detail.GetCode() != test.code || commandErr.detail.GetReason() != test.reason {
			t.Fatalf("%v mapped to %v/%s", test.err, commandErr.detail.GetCode(), commandErr.detail.GetReason())
		}
	}
	if protocolV2AttachmentExtensionCommandError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func protocolV2AttachmentExtensionRequest(t *testing.T, values map[string]any, revision string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			Extension:      &protocolv2.ExtensionIdentity{ExtensionId: "example.oauth2"},
			IdempotencyKey: "attachment-extension-1",
		},
		IdempotencyKey:   "attachment-extension-1",
		ExpectedRevision: revision,
		Input:            &protocolv2.TypedDocument{Value: value},
	}
}

func protocolV2AttachmentExtensionClaimRequest(t *testing.T, values map[string]any, revision string) *hostv2.CommandRequest {
	t.Helper()
	request := protocolV2AttachmentExtensionRequest(t, values, revision)
	request.CommandId = CommandAttachmentExtensionClaimID
	request.CommandVersion = CommandAttachmentExtensionClaimVersion
	return request
}

func protocolV2AttachmentExtensionReleaseRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	request := protocolV2AttachmentExtensionRequest(t, values, "")
	request.CommandId = CommandAttachmentExtensionReleaseID
	request.CommandVersion = CommandAttachmentExtensionReleaseVersion
	return request
}

func protocolV2AttachmentExtensionListRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	request := protocolV2AttachmentExtensionRequest(t, values, "")
	request.CommandId = CommandAttachmentExtensionListID
	request.CommandVersion = CommandAttachmentExtensionListVersion
	return request
}
