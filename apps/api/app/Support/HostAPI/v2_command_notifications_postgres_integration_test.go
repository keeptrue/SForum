package hostapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const postgresNotificationType = "fixture.domain-command.order_ready"

func TestPostgresProtocolV2NotificationEmitExactArtifactMatrix(t *testing.T) {
	h := newPostgresDomainCommandHarness(t)
	registry, declaration := installPostgresNotificationFixture(t, h)
	spec := postgresDomainCommandSpec{
		ID: CommandNotificationsEmitID, Version: CommandNotificationsEmitVersion,
		InputSchema: CommandNotificationsEmitInputSchema, SchemaVersion: CommandNotificationsEmitSchemaVersion,
	}
	input := map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1,
		"payload":          map[string]any{"orderId": "order-private-42"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}
	defaultDisabled := h.request(t, h.identity, spec, "notification-default-disabled", input, "", 0)
	result, err := h.execute(h.identity, defaultDisabled)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || result.GetError() != nil {
		t.Fatalf("default-disabled notification emission = %#v, %v", result, err)
	}
	if h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 0 {
		t.Fatal("new plugin notification type bypassed default-disabled policy")
	}

	// Administrator admission and the inherited recommendation are independent:
	// enabled=true is the hard limit, while recommended_enabled controls inherit.
	if _, err := h.pool.Exec(h.ctx, `UPDATE notification_type_policies SET enabled=TRUE,recommended_enabled=TRUE WHERE type=$1 AND channel='in_app'`, postgresNotificationType); err != nil {
		t.Fatal(err)
	}
	request := h.request(t, h.identity, spec, "notification-emit-1", input, "", 0)

	result, err = h.execute(h.identity, request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || result.GetError() != nil {
		t.Fatalf("allowed notification emission = %#v, %v", result, err)
	}
	if h.count(t, `SELECT count(*) FROM notifications WHERE type=$1 AND recipient_user_id=$2`, postgresNotificationType, postgresDomainCommandDeniedActorID) != 1 ||
		h.count(t, `SELECT count(*) FROM extension_host_command_receipts WHERE extension_id=$1 AND command_id=$2`, h.identity.GetExtensionId(), CommandNotificationsEmitID) != 2 {
		t.Fatal("accepted notification did not share the Host Command transaction")
	}
	var acceptedMetadata []byte
	if err := h.pool.QueryRow(h.ctx, `SELECT metadata FROM audit_events WHERE id=$1`, result.GetAuditEventId()).Scan(&acceptedMetadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(acceptedMetadata), "order-private-42") || strings.Contains(string(acceptedMetadata), fmt.Sprint(postgresDomainCommandDeniedActorID)) {
		t.Fatalf("accepted audit leaked private notification data: %s", acceptedMetadata)
	}

	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO notification_preferences (user_id,type,channel,state)
		VALUES ($1,$2,'in_app','disabled')
		ON CONFLICT (user_id,type,channel) DO UPDATE SET state=EXCLUDED.state`, postgresDomainCommandDeniedActorID, postgresNotificationType); err != nil {
		t.Fatal(err)
	}
	disabledOverride := h.request(t, h.identity, spec, "notification-explicit-disabled", input, "", 0)
	result, err = h.execute(h.identity, disabledOverride)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED ||
		h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 1 {
		t.Fatalf("explicit disabled notification = %#v, %v", result, err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE notification_type_policies SET recommended_enabled=FALSE WHERE type=$1 AND channel='in_app'`, postgresNotificationType); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE notification_preferences SET state='enabled' WHERE user_id=$2 AND type=$1 AND channel='in_app'`, postgresNotificationType, postgresDomainCommandDeniedActorID); err != nil {
		t.Fatal(err)
	}
	explicitEnabled := h.request(t, h.identity, spec, "notification-explicit-enabled", input, "", 0)
	result, err = h.execute(h.identity, explicitEnabled)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED ||
		h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 2 {
		t.Fatalf("explicit enabled notification = %#v, %v", result, err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE notification_type_policies SET enabled=FALSE WHERE type=$1 AND channel='in_app'`, postgresNotificationType); err != nil {
		t.Fatal(err)
	}
	hardDisabled := h.request(t, h.identity, spec, "notification-hard-disabled", input, "", 0)
	result, err = h.execute(h.identity, hardDisabled)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED ||
		h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 2 {
		t.Fatalf("admin hard-disabled notification = %#v, %v", result, err)
	}
	replayed, err := h.execute(h.identity, proto.Clone(request).(*hostv2.CommandRequest))
	if err != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED ||
		h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 2 {
		t.Fatalf("notification idempotency replay = %#v, %v", replayed, err)
	}

	assertNotificationReject := func(key string, input map[string]any, reason string) {
		t.Helper()
		candidate := h.request(t, h.identity, spec, key, input, "", 0)
		got, executeErr := h.execute(h.identity, candidate)
		if executeErr != nil || got.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK || got.GetError().GetReason() != reason {
			t.Fatalf("%s rejection = %#v, %v", reason, got, executeErr)
		}
	}
	assertNotificationReject("notification-namespace-forgery", map[string]any{
		"type": "other.extension.notice", "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
	}, "notification.type_not_owned")
	assertNotificationReject("notification-type-unknown", map[string]any{
		"type": "fixture.domain-command.unknown", "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
	}, "notification.type_unknown")
	assertNotificationReject("notification-schema-invalid", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": 42},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}, "notification.payload_invalid")
	assertNotificationReject("notification-target-invalid", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.other"},
	}, "notification.target_invalid")
	assertNotificationReject("notification-recipient-invalid", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{"999999"},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}, "notification.recipient_invalid")
	var rejectedMetadata []byte
	if err := h.pool.QueryRow(h.ctx, `SELECT metadata FROM audit_events WHERE action='extension.notification_emit.rejected' ORDER BY id DESC LIMIT 1`).Scan(&rejectedMetadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rejectedMetadata), "999999") || strings.Contains(string(rejectedMetadata), "orderId") {
		t.Fatalf("rejected audit leaked recipient/payload: %s", rejectedMetadata)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions
		SET manifest=jsonb_set(manifest,'{notificationTypes,0,required}','true'::jsonb,TRUE)
		WHERE extension_id=$1 AND package_digest=$2`, h.identity.GetExtensionId(), h.identity.GetArtifactDigest()); err != nil {
		t.Fatal(err)
	}
	assertNotificationReject("notification-required-forgery", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}, "notification.type_not_owned")
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions
		SET manifest=jsonb_set(manifest,'{notificationTypes,0,required}','false'::jsonb,TRUE)
		WHERE extension_id=$1 AND package_digest=$2`, h.identity.GetExtensionId(), h.identity.GetArtifactDigest()); err != nil {
		t.Fatal(err)
	}

	forged := proto.Clone(h.identity).(*protocolv2.ExtensionIdentity)
	forged.ArtifactDigest = strings.Repeat("f", 64)
	forgedRequest := h.request(t, forged, spec, "notification-cross-artifact", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}, "", 0)
	forgedResult, err := h.execute(forged, forgedRequest)
	if err != nil || forgedResult.GetError().GetReason() != "host.command_identity_stale" {
		t.Fatalf("cross-artifact notification = %#v, %v", forgedResult, err)
	}

	snapshot := registry.Snapshot()
	owner := notifications.DescriptorOwner{ExtensionID: h.identity.GetExtensionId(), Version: h.identity.GetExtensionVersion(), ArtifactDigest: h.identity.GetArtifactDigest()}
	if _, err := registry.Deactivate(h.ctx, owner, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	if resolved := registry.Resolve(postgresNotificationType); resolved.Active || resolved.Owner.ExtensionID != "" {
		t.Fatalf("historical fallback retained executable owner: %#v", resolved)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE extensions SET status='disabled' WHERE id=$1`, h.identity.GetExtensionId()); err != nil {
		t.Fatal(err)
	}
	assertNotificationReject("notification-disabled", map[string]any{
		"type": declaration.ID, "payloadVersion": 1, "payload": map[string]any{"orderId": "x"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": declaration.TargetID},
	}, "notification.type_inactive")
	if h.count(t, `SELECT count(*) FROM notifications WHERE type=$1`, postgresNotificationType) != 2 {
		t.Fatal("disable changed historical notification rows")
	}
}

func TestPostgresProtocolV2NotificationEmitRateLimit(t *testing.T) {
	h := newPostgresDomainCommandHarness(t)
	installPostgresNotificationFixture(t, h)
	spec := postgresDomainCommandSpec{ID: CommandNotificationsEmitID, Version: CommandNotificationsEmitVersion, InputSchema: CommandNotificationsEmitInputSchema, SchemaVersion: CommandNotificationsEmitSchemaVersion}
	for index := 0; index < ProtocolV2NotificationEmitRequestsPerMinute; index++ {
		key := fmt.Sprintf("notification-rate-%02d", index)
		request := h.request(t, h.identity, spec, key, map[string]any{
			"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": key},
			"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
			"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
		}, "", 0)
		result, err := h.execute(h.identity, request)
		if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
			t.Fatalf("rate seed %d = %#v, %v", index, result, err)
		}
	}
	limited := h.request(t, h.identity, spec, "notification-rate-limited", map[string]any{
		"type": postgresNotificationType, "payloadVersion": 1, "payload": map[string]any{"orderId": "limited"},
		"recipientUserIds": []any{fmt.Sprint(postgresDomainCommandDeniedActorID)},
		"target":           map[string]any{"kind": "extension_route", "id": "fixture.domain-command.route.orders"},
	}, "", 0)
	result, err := h.execute(h.identity, limited)
	if err != nil || result.GetError().GetReason() != "notification.rate_limited" || !result.GetError().GetRetryable() {
		t.Fatalf("notification rate limit = %#v, %v", result, err)
	}
}

func installPostgresNotificationFixture(t *testing.T, h *postgresDomainCommandHarness) (*notifications.Registry, extensionmanifest.ManifestNotificationType) {
	t.Helper()
	root := t.TempDir()
	schemaBody := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"required":["orderId"],"properties":{"orderId":{"type":"string","minLength":1,"maxLength":64}}}`)
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schemas/order-ready.json"), schemaBody, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(schemaBody)
	declaration := extensionmanifest.ManifestNotificationType{
		ID: postgresNotificationType, ContractVersion: postgresNotificationType + "@1", PayloadVersion: 1,
		Category: "commerce", PayloadSchema: "fixture.domain-command.schema.order-ready@1",
		Label: extensionmanifest.LocalizedText{Default: "Order ready"}, Body: extensionmanifest.LocalizedText{Default: "Your order is ready."},
		TargetKind: "extension_route", TargetID: "fixture.domain-command.route.orders",
		Channels: []string{"in_app"}, RecommendedChannels: []string{"in_app"},
	}
	manifest := extensionmanifest.Manifest{
		ID: h.identity.GetExtensionId(), Version: h.identity.GetExtensionVersion(), Type: extensionmanifest.TypePlugin,
		Database:          &extensionmanifest.ManifestDatabase{Grants: []string{extensionmanifest.DatabaseGrantHostCommands}},
		NotificationTypes: []extensionmanifest.ManifestNotificationType{declaration},
		PackageFiles: []extensionmanifest.ManifestPackageFile{{
			ID: "fixture.domain-command.schema.order-ready", Kind: "schema", Path: "schemas/order-ready.json",
			Digest: hex.EncodeToString(digest[:]), Version: "1",
		}},
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions SET manifest=$2::jsonb,package_path=$3
		WHERE extension_id=$1 AND package_digest=$4`, h.identity.GetExtensionId(), manifestBody, root, h.identity.GetArtifactDigest()); err != nil {
		t.Fatal(err)
	}
	registry := notifications.NewPersistentRegistry(h.pool)
	owner := notifications.DescriptorOwner{ExtensionID: h.identity.GetExtensionId(), Version: h.identity.GetExtensionVersion(), ArtifactDigest: h.identity.GetArtifactDigest()}
	if _, err := registry.Publish(h.ctx, owner, manifest.NotificationTypes, registry.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	return registry, declaration
}
