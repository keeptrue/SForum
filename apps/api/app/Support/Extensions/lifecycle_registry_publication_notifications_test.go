package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecycleNotificationUpgradeRollbackDisableAndUninstall(t *testing.T) {
	ctx := context.Background()
	registry := notifications.NewRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Notifications: registry})
	v1 := lifecycleNotificationExtension("demo.catalog", "1.0.0", "a", 1, extensions.StatusEnabled)
	v2 := lifecycleNotificationExtension("demo.catalog", "2.0.0", "b", 2, extensions.StatusEnabled)
	v1Material := lifecycleRegistryMaterial{extension: v1}
	v2Material := lifecycleRegistryMaterial{extension: v2}

	if err := boundary.reconcileNotifications(ctx, v1.ID, nil, &v1Material, &v1Material); err != nil {
		t.Fatalf("enable v1: %v", err)
	}
	if got := registry.Resolve(v1.Manifest.NotificationTypes[0].ID); got.Owner != notificationDescriptorOwner(&v1Material) || got.PayloadVersion != 1 {
		t.Fatalf("enabled v1 descriptor = %#v", got)
	}
	if err := boundary.reconcileNotifications(ctx, v1.ID, &v1Material, &v2Material, &v2Material); err != nil {
		t.Fatalf("upgrade v2: %v", err)
	}
	if got := registry.Resolve(v2.Manifest.NotificationTypes[0].ID); got.Owner != notificationDescriptorOwner(&v2Material) || got.PayloadVersion != 2 {
		t.Fatalf("upgraded descriptor = %#v", got)
	}
	if err := boundary.reconcileNotifications(ctx, v1.ID, &v1Material, &v2Material, &v1Material); err != nil {
		t.Fatalf("rollback v1: %v", err)
	}
	if got := registry.Resolve(v1.Manifest.NotificationTypes[0].ID); got.Owner != notificationDescriptorOwner(&v1Material) || got.PayloadVersion != 1 {
		t.Fatalf("rolled-back descriptor = %#v", got)
	}

	for _, operation := range []string{"disable", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			localRegistry := notifications.NewRegistry()
			localBoundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Notifications: localRegistry})
			if err := localBoundary.reconcileNotifications(ctx, v1.ID, nil, &v1Material, &v1Material); err != nil {
				t.Fatal(err)
			}
			if err := localBoundary.reconcileNotifications(ctx, v1.ID, &v1Material, nil, nil); err != nil {
				t.Fatal(err)
			}
			fallback := localRegistry.Resolve(v1.Manifest.NotificationTypes[0].ID)
			if fallback.Active || fallback.Category != "plugin_unknown" {
				t.Fatalf("%s fallback = %#v", operation, fallback)
			}
		})
	}
}

func TestLifecycleNotificationStartupRestoreExactInventoryAndSafeMode(t *testing.T) {
	ctx := context.Background()
	registry := notifications.NewRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Notifications: registry})
	enabledV1 := lifecycleNotificationExtension("demo.catalog", "1.0.0", "c", 1, extensions.StatusEnabled)
	enabledV2 := lifecycleNotificationExtension("demo.catalog", "2.0.0", "d", 2, extensions.StatusEnabled)
	disabled := lifecycleNotificationExtension("other.catalog", "1.0.0", "e", 1, extensions.StatusDisabled)

	if err := boundary.restoreNotificationPublications(ctx, []extensions.Extension{enabledV1, disabled}, false); err != nil {
		t.Fatal(err)
	}
	if got := registry.Resolve(enabledV1.Manifest.NotificationTypes[0].ID); got.Owner.ArtifactDigest != enabledV1.PackageDigest {
		t.Fatalf("restored v1 = %#v", got)
	}
	if got := registry.Resolve(disabled.Manifest.NotificationTypes[0].ID); got.Active {
		t.Fatalf("disabled publication restored = %#v", got)
	}

	if err := boundary.restoreNotificationPublications(ctx, []extensions.Extension{enabledV1}, true); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.Snapshot(); !snapshot.SafeMode {
		t.Fatalf("safe mode snapshot = %#v", snapshot)
	} else if _, exists := snapshot.Descriptors[enabledV1.Manifest.NotificationTypes[0].ID]; exists {
		t.Fatal("safe mode retained plugin declaration")
	}

	if err := boundary.restoreNotificationPublications(ctx, []extensions.Extension{enabledV2}, false); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if snapshot.SafeMode || snapshot.Descriptors[enabledV2.Manifest.NotificationTypes[0].ID].Owner.ArtifactDigest != enabledV2.PackageDigest {
		t.Fatalf("safe-mode exit did not publish exact v2 = %#v", snapshot)
	}
}

func TestLifecycleNotificationRejectsConflictingOwner(t *testing.T) {
	ctx := context.Background()
	registry := notifications.NewRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Notifications: registry})
	sharedType := "demo.catalog.notice"
	rogueOwner := notifications.DescriptorOwner{
		ExtensionID: "demo", Version: "1.0.0", ArtifactDigest: strings.Repeat("f", 64),
	}
	rogueDeclaration := lifecycleNotificationDeclaration(sharedType, 1)
	if _, err := registry.Publish(ctx, rogueOwner, []extensionmanifest.ManifestNotificationType{rogueDeclaration}, 0); err != nil {
		t.Fatal(err)
	}
	conflicting := lifecycleNotificationExtension("demo.catalog", "1.0.0", "1", 1, extensions.StatusEnabled)
	material := lifecycleRegistryMaterial{extension: conflicting}
	if err := boundary.validateNotificationTransition(&material); !errors.Is(err, notifications.ErrRegistryOwnerConflict) {
		t.Fatalf("conflicting lifecycle validation error = %v", err)
	}
}

func TestLifecycleNotificationStartupKeepsUnavailableExecutableArtifactClosed(t *testing.T) {
	registry := notifications.NewRegistry()
	manager := NewManager(ManagerConfig{Starter: NewProtocolStarter(ProtocolStarterConfig{})})
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Notifications: registry,
	})
	executable := lifecycleNotificationExtension("runtime.catalog", "1.0.0", "2", 1, extensions.StatusEnabled)
	executable.Manifest.Backend.Entry = "bin/runtime-catalog"
	executable.Manifest.Backend.ProtocolVersion = 2
	if err := boundary.restoreNotificationPublications(context.Background(), []extensions.Extension{executable}, false); err != nil {
		t.Fatal(err)
	}
	if got := registry.Resolve(executable.Manifest.NotificationTypes[0].ID); got.Active {
		t.Fatalf("unavailable executable artifact was restored = %#v", got)
	}
}

func lifecycleNotificationExtension(
	id, version, digestCharacter string,
	payloadVersion int,
	status string,
) extensions.Extension {
	typeID := id + ".notice"
	return extensions.Extension{
		ID: id, Version: version, PackageDigest: strings.Repeat(digestCharacter, 64),
		ActiveVersionID: int64(payloadVersion), Type: extensions.TypePlugin, Status: status,
		Manifest: extensionmanifest.Manifest{
			ManifestVersion: extensionmanifest.ManifestVersionV3,
			ID:              id, Name: "Notification fixture", Version: version, Type: extensions.TypePlugin,
			NotificationTypes: []extensionmanifest.ManifestNotificationType{
				lifecycleNotificationDeclaration(typeID, payloadVersion),
			},
		},
	}
}

func lifecycleNotificationDeclaration(typeID string, payloadVersion int) extensionmanifest.ManifestNotificationType {
	return extensionmanifest.ManifestNotificationType{
		ID: typeID, ContractVersion: typeID + "@1", PayloadVersion: payloadVersion,
		Category: "plugin", PayloadSchema: typeID + ".payload@1",
		Label:      extensionmanifest.LocalizedText{Default: "Notice"},
		Body:       extensionmanifest.LocalizedText{Default: "A notice arrived."},
		TargetKind: "none", Channels: []string{"in_app", "web_push"},
	}
}
