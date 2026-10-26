package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2NotificationEmitPreviewIsBoundedAndRedacted(t *testing.T) {
	request := protocolV2NotificationRequest(t, map[string]any{
		"type": "fixture.notifications.order_ready", "payloadVersion": 1,
		"payload":          map[string]any{"orderId": "private-order"},
		"recipientUserIds": []any{"12", "42"},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.notifications.route.orders"},
	})
	definition := newProtocolV2NotificationEmitCommandDefinition(nil, nil)
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ActorMode != protocolV2CommandActorService || len(definition.RequiredPermissions) != 0 {
		t.Fatalf("notification emit authority = %#v", definition)
	}
	if len(plan.Impact) != 1 || strings.Contains(plan.Impact[0].GetSummary(), "private-order") ||
		plan.Impact[0].GetResourceId() != "fixture.notifications.order_ready" {
		t.Fatalf("notification impact leaked payload or drifted: %#v", plan.Impact)
	}
	values := plan.ProjectedResult.GetValue().AsMap()
	if values["planned"] != true || values["recipientCount"] != "2" || values["createdCount"] != "0" {
		t.Fatalf("notification projection = %#v", values)
	}
}

func TestProtocolV2NotificationEmitRejectsAuthorityAndRecipientFields(t *testing.T) {
	tests := []map[string]any{
		{"type": "fixture.notifications.notice", "payloadVersion": 1, "payload": map[string]any{}, "recipientUserIds": []any{}},
		{"type": "fixture.notifications.notice", "payloadVersion": 1, "payload": map[string]any{}, "recipientUserIds": []any{"1", "1"}},
		{"type": "fixture.notifications.notice", "payloadVersion": 1, "payload": map[string]any{}, "recipientUserIds": []any{1}},
		{"type": "fixture.notifications.notice", "payloadVersion": 1, "payload": map[string]any{}, "recipientUserIds": []any{"1"}, "actorUserId": "9"},
		{"type": "fixture.notifications.notice", "payloadVersion": 1, "payload": map[string]any{}, "recipientUserIds": []any{"1"}, "session": "forged"},
	}
	for index, values := range tests {
		_, _, _, err := protocolV2NotificationEmitInputFromRequest(protocolV2NotificationRequest(t, values))
		if err == nil {
			t.Fatalf("invalid notification input case %d accepted", index)
		}
	}
}

func TestProtocolV2NotificationPayloadSchemaIsDigestBoundAndLocal(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["orderId"],"properties":{"orderId":{"type":"string","maxLength":32}}}`)
	digest := sha256.Sum256(body)
	file := extensionmanifest.ManifestPackageFile{
		ID: "fixture.notifications.schema.order-ready", Kind: "schema", Path: "schemas/order-ready.json",
		Digest: hex.EncodeToString(digest[:]), Version: "1",
	}
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, file.Path), body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, gotDigest, err := protocolV2LoadNotificationSchema(root, file)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocolV2ValidateNotificationPayload(loaded, gotDigest, []byte(`{"orderId":"42"}`)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if err := protocolV2ValidateNotificationPayload(loaded, gotDigest, []byte(`{"orderId":42}`)); err == nil {
		t.Fatal("schema-invalid payload accepted")
	}
	file.Digest = strings.Repeat("0", 64)
	if _, _, err := protocolV2LoadNotificationSchema(root, file); err == nil {
		t.Fatal("schema digest drift accepted")
	}
	external := []byte(`{"$ref":"https://attacker.invalid/schema.json"}`)
	if err := protocolV2ValidateNotificationPayload(external, hex.EncodeToString(digest[:]), []byte(`{}`)); err == nil {
		t.Fatal("external schema reference accepted")
	}
}

func TestProtocolV2NotificationErrorsExposeStableReasonsOnly(t *testing.T) {
	for _, reason := range []string{
		"notification.type_unknown", "notification.type_inactive", "notification.type_not_owned",
		"notification.payload_invalid", "notification.target_invalid", "notification.recipient_invalid", "notification.rate_limited",
	} {
		var commandErr *protocolV2CommandError
		if !errors.As(protocolV2NotificationError(reason), &commandErr) || commandErr.detail.GetReason() != reason ||
			strings.Contains(commandErr.detail.GetMessage(), "fixture.notifications") {
			t.Fatalf("unstable or leaking reason %q: %#v", reason, commandErr)
		}
	}
}

func protocolV2NotificationRequest(t *testing.T, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	document, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context:   &protocolv2.RequestContext{Extension: &protocolv2.ExtensionIdentity{ExtensionId: "fixture.notifications"}, IdempotencyKey: "emit-1"},
		CommandId: CommandNotificationsEmitID, CommandVersion: CommandNotificationsEmitVersion, IdempotencyKey: "emit-1",
		Input: &protocolv2.TypedDocument{SchemaId: CommandNotificationsEmitInputSchema, SchemaVersion: CommandNotificationsEmitSchemaVersion, Value: document},
	}
}
