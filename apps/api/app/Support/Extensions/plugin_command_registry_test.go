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

func TestPluginCommandRegistryPublishesNamespacedExactCommands(t *testing.T) {
	registry := NewPluginCommandRegistry()
	ordinary := pluginCommandExtension("demo.commands", false)
	if err := registry.ReplaceRuntime(ordinary, "runtime-a"); err != nil {
		t.Fatal(err)
	}
	contract, err := registry.Resolve("demo.commands.command.sync", false)
	if err != nil || contract.ExtensionID != ordinary.ID || contract.ExtensionVersion != ordinary.Version ||
		contract.ArtifactDigest != ordinary.PackageDigest || contract.InstanceID != "runtime-a" || contract.Timeout != 3_000_000_000 {
		t.Fatalf("contract = %#v, %v", contract, err)
	}
	if _, err := registry.Resolve(contract.ID, true); !errors.Is(err, ErrPluginCommandSafeMode) {
		t.Fatalf("ordinary safe-mode resolve = %v", err)
	}

	recovery := pluginCommandExtension("demo.recovery", true)
	if err := registry.ReplaceRuntime(recovery, "runtime-recovery"); err != nil {
		t.Fatal(err)
	}
	if resolved, err := registry.Resolve("demo.recovery.command.sync", true); err != nil || !resolved.RecoverySafe {
		t.Fatalf("recovery-safe resolve = %#v, %v", resolved, err)
	}
	snapshot := registry.Snapshot()
	if snapshot.Revision != 2 || len(snapshot.Commands) != 2 || snapshot.Commands[0].ID >= snapshot.Commands[1].ID {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestPluginCommandRegistryRejectsNamespaceAndContractDrift(t *testing.T) {
	registry := NewPluginCommandRegistry()
	extension := pluginCommandExtension("demo.commands", false)
	if err := registry.ReplaceRuntime(extension, "runtime-a"); err != nil {
		t.Fatal(err)
	}
	invalid := pluginCommandExtension("other.commands", false)
	invalid.Manifest.Commands[0].ID = extension.Manifest.Commands[0].ID
	if err := registry.ReplaceRuntime(invalid, "runtime-b"); !errors.Is(err, ErrPluginCommandRegistryInvalid) {
		t.Fatalf("foreign namespace = %v", err)
	}
	upgraded := pluginCommandExtension("demo.commands", true)
	upgraded.Version, upgraded.Manifest.Version = "1.1.0", "1.1.0"
	upgraded.PackageDigest = strings.Repeat("b", 64)
	if err := registry.ReplaceRuntime(upgraded, "runtime-b"); !errors.Is(err, ErrPluginCommandRegistryConflict) {
		t.Fatalf("same-version authority drift = %v", err)
	}
	resolved, err := registry.Resolve(extension.Manifest.Commands[0].ID, false)
	if err != nil || resolved.InstanceID != "runtime-a" {
		t.Fatalf("rejected upgrade changed snapshot = %#v, %v", resolved, err)
	}
}

func TestPluginCommandRegistryValidatesExactSchemas(t *testing.T) {
	extension := pluginCommandExtension("demo.commands", false)
	extension.PackagePath = t.TempDir()
	writePluginCommandSchema(t, &extension, "demo.commands.command.input", "schemas/input.json",
		`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}},"additionalProperties":false}`)
	writePluginCommandSchema(t, &extension, "demo.commands.command.result", "schemas/result.json",
		`{"type":"object","required":["ok"],"properties":{"ok":{"const":true}},"additionalProperties":false}`)
	registry := NewPluginCommandRegistry()
	if err := registry.ReplaceRuntime(extension, "runtime-a"); err != nil {
		t.Fatal(err)
	}
	contract, err := registry.Resolve(extension.Manifest.Commands[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateInput(contract, map[string]any{"name": "SForum"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateInput(contract, map[string]any{"name": 42}); !errors.Is(err, ErrPluginCommandRegistryInvalid) {
		t.Fatalf("invalid input = %v", err)
	}
	if err := registry.ValidateResult(contract, map[string]any{"ok": false}); !errors.Is(err, ErrPluginCommandRegistryInvalid) {
		t.Fatalf("invalid result = %v", err)
	}
}

func TestPluginCommandRegistryRemovalIsExact(t *testing.T) {
	registry := NewPluginCommandRegistry()
	extension := pluginCommandExtension("demo.commands", false)
	if err := registry.ReplaceRuntime(extension, "runtime-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RemoveRuntime(extension.ID, "stale-runtime"); !errors.Is(err, ErrPluginCommandRegistryConflict) {
		t.Fatalf("stale remove = %v", err)
	}
	if removed, err := registry.RemoveRuntime(extension.ID, "runtime-a"); err != nil || !removed {
		t.Fatalf("exact remove = %t, %v", removed, err)
	}
	if _, err := registry.Resolve(extension.Manifest.Commands[0].ID, false); !errors.Is(err, ErrPluginCommandNotFound) {
		t.Fatalf("resolve removed = %v", err)
	}
}

func pluginCommandExtension(id string, recoverySafe bool) extensions.Extension {
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64),
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			Commands: []extensions.ManifestCommand{{
				ID: id + ".command.sync", ContractVersion: id + ".command.sync@1", Handler: "command.sync",
				InputSchema: id + ".command.input@1", ResultSchema: id + ".command.result@1",
				RecoverySafe: recoverySafe, TimeoutMS: 3000,
			}},
		},
	}
}

func writePluginCommandSchema(t *testing.T, extension *extensions.Extension, id, path, schema string) {
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
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: id, Kind: "schema", Path: path, Digest: hex.EncodeToString(digest[:]), Version: "1",
	})
}
