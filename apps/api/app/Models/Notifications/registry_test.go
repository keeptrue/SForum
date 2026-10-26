package notifications_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRegistryExactArtifactLifecycleAndHistoricalFallback(t *testing.T) {
	ctx := context.Background()
	registry := notifications.NewRegistry()
	v1 := registryOwner("demo.catalog", "1.0.0", "a")
	v2 := registryOwner("demo.catalog", "2.0.0", "b")
	v1Declaration := registryDeclaration("demo.catalog.notice", 1)
	v2Declaration := registryDeclaration("demo.catalog.notice", 2)

	published, err := registry.Publish(ctx, v1, []extensionmanifest.ManifestNotificationType{v1Declaration}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := published.Descriptors[v1Declaration.ID]; got.Owner != v1 || got.PayloadVersion != 1 {
		t.Fatalf("v1 descriptor = %#v", got)
	}
	// Returned snapshots are immutable copies.
	published.Descriptors[v1Declaration.ID] = notifications.TypeDescriptor{}
	if got := registry.Resolve(v1Declaration.ID); got.Owner != v1 {
		t.Fatalf("snapshot mutation rewrote registry = %#v", got)
	}
	if _, err := registry.Publish(ctx, v2, []extensionmanifest.ManifestNotificationType{v2Declaration}, 0); !errors.Is(err, notifications.ErrRegistryRevisionConflict) {
		t.Fatalf("stale publish error = %v", err)
	}
	if _, err := registry.Deactivate(ctx, v2, registry.Snapshot().Revision); !errors.Is(err, notifications.ErrRegistryOwnerConflict) {
		t.Fatalf("wrong artifact deactivation error = %v", err)
	}

	upgraded, err := registry.Publish(ctx, v2, []extensionmanifest.ManifestNotificationType{v2Declaration}, registry.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := upgraded.Descriptors[v2Declaration.ID]; got.Owner != v2 || got.PayloadVersion != 2 {
		t.Fatalf("upgraded descriptor = %#v", got)
	}
	rolledBack, err := registry.Publish(ctx, v1, []extensionmanifest.ManifestNotificationType{v1Declaration}, registry.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := rolledBack.Descriptors[v1Declaration.ID]; got.Owner != v1 || got.PayloadVersion != 1 {
		t.Fatalf("rolled-back descriptor = %#v", got)
	}

	if _, err := registry.Deactivate(ctx, v1, registry.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	fallback := registry.Resolve(v1Declaration.ID)
	if fallback.Active || fallback.Category != "plugin_unknown" || fallback.TargetKind != "none" {
		t.Fatalf("historical fallback = %#v", fallback)
	}
}

func TestRegistryRestoreSafeModeConflictAndCoreAuthority(t *testing.T) {
	ctx := context.Background()
	registry := notifications.NewRegistry()
	demo := registryOwner("demo", "1.0.0", "c")
	catalog := registryOwner("demo.catalog", "1.0.0", "d")
	shared := registryDeclaration("demo.catalog.notice", 1)

	if _, err := registry.Restore(ctx, []notifications.RegistryPublication{
		{Owner: demo, Declarations: []extensionmanifest.ManifestNotificationType{shared}},
		{Owner: catalog, Declarations: []extensionmanifest.ManifestNotificationType{shared}},
	}, false); !errors.Is(err, notifications.ErrRegistryOwnerConflict) {
		t.Fatalf("cross-owner restore error = %v", err)
	}
	required := shared
	required.Required = true
	if _, err := registry.Publish(ctx, catalog, []extensionmanifest.ManifestNotificationType{required}, registry.Snapshot().Revision); !errors.Is(err, notifications.ErrRegistryDescriptorInvalid) {
		t.Fatalf("plugin required type error = %v", err)
	}
	core := shared
	core.ID = notifications.TypeReply
	core.ContractVersion = notifications.TypeReply + "@1"
	if _, err := registry.Publish(ctx, catalog, []extensionmanifest.ManifestNotificationType{core}, registry.Snapshot().Revision); !errors.Is(err, notifications.ErrRegistryDescriptorInvalid) {
		t.Fatalf("foreign namespace error = %v", err)
	}

	if _, err := registry.Restore(ctx, []notifications.RegistryPublication{{
		Owner: catalog, Declarations: []extensionmanifest.ManifestNotificationType{shared},
	}}, false); err != nil {
		t.Fatal(err)
	}
	safe, err := registry.SetSafeMode(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !safe.SafeMode || safe.Descriptors[notifications.TypeReply].Type == "" {
		t.Fatalf("safe-mode core snapshot = %#v", safe)
	}
	if _, exists := safe.Descriptors[shared.ID]; exists {
		t.Fatal("safe mode retained plugin descriptor")
	}
	restored, err := registry.SetSafeMode(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.SafeMode || restored.Descriptors[shared.ID].Owner != catalog {
		t.Fatalf("safe-mode exit = %#v", restored)
	}
}

func registryOwner(extensionID, version, digestCharacter string) notifications.DescriptorOwner {
	return notifications.DescriptorOwner{
		ExtensionID: extensionID, Version: version, ArtifactDigest: strings.Repeat(digestCharacter, 64),
	}
}

func registryDeclaration(typeID string, payloadVersion int) extensionmanifest.ManifestNotificationType {
	return extensionmanifest.ManifestNotificationType{
		ID: typeID, ContractVersion: typeID + "@1", PayloadVersion: payloadVersion,
		Category: "plugin", PayloadSchema: typeID + ".payload@1",
		Label:      extensionmanifest.LocalizedText{Default: "Notice"},
		Body:       extensionmanifest.LocalizedText{Default: "A notice arrived."},
		TargetKind: "none", Channels: []string{"in_app", "web_push"},
	}
}
