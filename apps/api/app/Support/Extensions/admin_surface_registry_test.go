package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestAdminSurfaceRegistryPublishesAllKindsDeterministically(t *testing.T) {
	kinds := []string{
		"navigation", "dashboard", "list_column", "list_filter", "row_action", "bulk_action",
		"form", "notice", "editor_panel", "detail_region", "importer", "exporter",
	}
	extension := adminSurfaceExtension("demo.admin", nil)
	for index, kind := range kinds {
		extension.Manifest.AdminSurfaces = append(extension.Manifest.AdminSurfaces, extensions.ManifestAdminSurface{
			ID: extension.ID + ".surface." + kind, ContractVersion: extension.ID + ".surface." + kind + "@1",
			Kind: kind, Action: "add", Label: kind, Handler: "admin." + kind, Priority: index,
		})
	}
	registry := NewAdminSurfaceRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-admin"); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot("")
	if snapshot.Revision != 1 || len(snapshot.Surfaces) != len(kinds) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	for index := 1; index < len(snapshot.Surfaces); index++ {
		if adminSurfaceBefore(snapshot.Surfaces[index], snapshot.Surfaces[index-1]) {
			t.Fatalf("surface order = %#v", snapshot.Surfaces)
		}
	}
	if filtered := registry.Snapshot("importer"); len(filtered.Surfaces) != 1 || filtered.Surfaces[0].Kind != "importer" {
		t.Fatalf("filtered snapshot = %#v", filtered)
	}
}

func TestAdminSurfaceRegistryComposesDeclaredDependencies(t *testing.T) {
	owner := adminSurfaceExtension("admin.owner", []extensions.ManifestAdminSurface{{
		ID: "admin.owner.surface.users", ContractVersion: "admin.owner.surface.users@1",
		Kind: "list_column", Action: "add", Label: "Users", Handler: "admin.users",
	}})
	consumer := adminSurfaceExtension("admin.consumer", []extensions.ManifestAdminSurface{{
		ID: "admin.consumer.surface.score", ContractVersion: "admin.consumer.surface.score@1",
		Kind: "list_column", Action: "after", TargetID: "admin.owner.surface.users",
		Label: "Score", Handler: "admin.score", Priority: 20,
	}})
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "optional"}}
	registry := NewAdminSurfaceRegistry()
	if err := registry.ReplaceRuntime(consumer, "runtime-consumer"); err != nil {
		t.Fatalf("optional missing owner: %v", err)
	}
	if snapshot := registry.Snapshot(""); len(snapshot.Surfaces) != 0 {
		t.Fatalf("optional missing snapshot = %#v", snapshot)
	}
	if err := registry.ReplaceRuntime(owner, "runtime-owner"); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.Snapshot("list_column"); len(snapshot.Surfaces) != 2 {
		t.Fatalf("composed snapshot = %#v", snapshot)
	}
	if removed, err := registry.RemoveRuntime(owner.ID, "runtime-owner"); err != nil || !removed {
		t.Fatalf("optional owner remove = %t, %v", removed, err)
	}

	required := consumer
	required.ID, required.Manifest.ID = "admin.required", "admin.required"
	required.Manifest.AdminSurfaces[0].ID = "admin.required.surface.score"
	required.Manifest.AdminSurfaces[0].ContractVersion = "admin.required.surface.score@1"
	required.Manifest.Dependencies[0].Kind = "required"
	if err := registry.ReplaceRuntime(required, "runtime-required"); !errors.Is(err, ErrAdminSurfaceRegistryConflict) {
		t.Fatalf("required missing owner = %v", err)
	}
}

func TestAdminSurfaceRegistryValidatesExactSchemaAndVersionDrift(t *testing.T) {
	extension := adminSurfaceExtension("admin.schema", []extensions.ManifestAdminSurface{{
		ID: "admin.schema.surface.form", ContractVersion: "admin.schema.surface.form@1",
		Kind: "form", Action: "add", Label: "Form", Schema: "admin.schema.surface.form.props@1",
	}})
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].Schema, "schemas/form.json",
		`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}},"additionalProperties":false}`)
	registry := NewAdminSurfaceRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-schema"); err != nil {
		t.Fatal(err)
	}
	contract, err := registry.Resolve(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil || contract.SchemaDigest == "" {
		t.Fatalf("contract=%#v err=%v", contract, err)
	}
	if err := registry.ValidateDocument(contract, map[string]any{"title": "SForum"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateDocument(contract, map[string]any{"title": 42}); !errors.Is(err, ErrAdminSurfaceRegistryInvalid) {
		t.Fatalf("invalid props = %v", err)
	}
	stale := contract
	stale.InstanceID = "stale-runtime"
	if err := registry.ValidateDocument(stale, map[string]any{"title": "SForum"}); !errors.Is(err, ErrAdminSurfaceNotFound) {
		t.Fatalf("stale contract = %v", err)
	}
	drift := extension
	drift.Version, drift.Manifest.Version = "1.1.0", "1.1.0"
	drift.PackageDigest = strings.Repeat("b", 64)
	drift.Manifest.AdminSurfaces[0].Label = "Changed"
	if err := registry.ReplaceRuntime(drift, "runtime-drift"); !errors.Is(err, ErrAdminSurfaceRegistryConflict) {
		t.Fatalf("same-contract drift = %v", err)
	}
	if removed, err := registry.RemoveRuntime(extension.ID, "stale-runtime"); removed || !errors.Is(err, ErrAdminSurfaceRegistryConflict) {
		t.Fatalf("stale remove = %t, %v", removed, err)
	}
}

func TestAdminSurfaceRegistryFreezesPlacementAndDistinctSchemas(t *testing.T) {
	extension := adminSurfaceExtension("admin.typed", []extensions.ManifestAdminSurface{{
		ID: "admin.typed.surface.notice", ContractVersion: "admin.typed.surface.notice@1",
		Kind: "notice", Action: "add", PlacementID: "core.component.page.admin",
		PlacementContractVersion: "sforum.component.page.admin@1", Label: "Notice", Handler: "admin.notice",
		PropsSchema: "admin.typed.surface.notice.props@1", ResultSchema: "admin.typed.surface.notice.result@1",
		Operation: extensions.AdminSurfaceOperationQuery,
	}})
	extension.PackagePath = t.TempDir()
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].PropsSchema, "schemas/notice-props.json",
		`{"type":"object","required":["scope"],"properties":{"scope":{"type":"string"}},"additionalProperties":false}`)
	writeAdminSurfaceSchema(t, &extension, extension.Manifest.AdminSurfaces[0].ResultSchema, "schemas/notice-result.json",
		`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}},"additionalProperties":false}`)
	registry := NewAdminSurfaceRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-typed"); err != nil {
		t.Fatal(err)
	}
	contract, err := registry.Resolve(extension.Manifest.AdminSurfaces[0].ID)
	if err != nil || contract.PlacementID != "core.component.page.admin" ||
		contract.PlacementContractVersion != "sforum.component.page.admin@1" ||
		contract.PropsSchemaDigest == "" || contract.ResultSchemaDigest == "" ||
		contract.PropsSchemaDigest == contract.ResultSchemaDigest {
		t.Fatalf("typed contract=%#v err=%v", contract, err)
	}
	if err := registry.ValidateProps(contract, map[string]any{"scope": "dashboard"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateResult(contract, map[string]any{"message": "Ready"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateProps(contract, map[string]any{"message": "wrong contract"}); !errors.Is(err, ErrAdminSurfaceRegistryInvalid) {
		t.Fatalf("props/result schema crossed = %v", err)
	}
	if err := registry.ValidateResult(contract, map[string]any{"scope": "wrong contract"}); !errors.Is(err, ErrAdminSurfaceRegistryInvalid) {
		t.Fatalf("result/props schema crossed = %v", err)
	}
}

func TestHookBusAdminSurfacesPublishAndRemoveAtomically(t *testing.T) {
	owner := adminSurfaceExtension("admin.owner", []extensions.ManifestAdminSurface{{
		ID: "admin.owner.surface.users", ContractVersion: "admin.owner.surface.users@1",
		Kind: "list_column", Action: "add", Label: "Users", Handler: "admin.users",
	}})
	consumer := adminSurfaceExtension("admin.consumer", []extensions.ManifestAdminSurface{{
		ID: "admin.consumer.surface.score", ContractVersion: "admin.consumer.surface.score@1",
		Kind: "list_column", Action: "after", TargetID: "admin.owner.surface.users",
		Label: "Score", Handler: "admin.score",
	}})
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	bus := NewHookBus(HookBusConfig{})
	if err := bus.RegisterRuntime(owner, "runtime-owner"); err != nil {
		t.Fatal(err)
	}
	if err := bus.RegisterRuntime(consumer, "runtime-consumer"); err != nil {
		t.Fatal(err)
	}
	if removed := bus.UnregisterRuntime(owner.ID, "runtime-owner"); removed {
		t.Fatal("required owner removal succeeded")
	}
	if snapshot := bus.AdminSurfaces().Snapshot("list_column"); len(snapshot.Surfaces) != 2 {
		t.Fatalf("failed removal changed snapshot = %#v", snapshot)
	}

	invalid := consumer
	invalid.Version, invalid.Manifest.Version = "1.1.0", "1.1.0"
	invalid.PackageDigest = strings.Repeat("b", 64)
	invalid.Manifest.AdminSurfaces[0].Label = "Changed without a contract version"
	if err := bus.RegisterRuntime(invalid, "runtime-invalid"); !errors.Is(err, ErrAdminSurfaceRegistryConflict) {
		t.Fatalf("invalid replacement = %v", err)
	}
	current, ok := bus.RuntimeSnapshot(consumer.ID)
	if !ok || current.InstanceID != "runtime-consumer" || current.Extension.Version != "1.0.0" {
		t.Fatalf("failed replacement changed runtime = %#v, %t", current, ok)
	}
	contract, err := bus.AdminSurfaces().Resolve(consumer.Manifest.AdminSurfaces[0].ID)
	if err != nil || contract.InstanceID != "runtime-consumer" {
		t.Fatalf("failed replacement changed surface = %#v, %v", contract, err)
	}
	if !bus.UnregisterRuntime(consumer.ID, "runtime-consumer") || !bus.UnregisterRuntime(owner.ID, "runtime-owner") {
		t.Fatal("exact removal failed")
	}
}

func adminSurfaceExtension(id string, surfaces []extensions.ManifestAdminSurface) extensions.Extension {
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64),
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend:       extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			AdminSurfaces: append([]extensions.ManifestAdminSurface(nil), surfaces...),
		},
	}
}

func writeAdminSurfaceSchema(t *testing.T, extension *extensions.Extension, id, path, schema string) {
	t.Helper()
	fullPath := filepath.Join(extension.PackagePath, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(schema)
	if err := os.WriteFile(fullPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	schemaID, schemaVersion, found := strings.Cut(id, "@")
	if !found {
		t.Fatalf("schema reference %q has no version", id)
	}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: schemaID, Kind: "schema", Path: path, Digest: hex.EncodeToString(digest[:]), Version: schemaVersion,
	})
}
