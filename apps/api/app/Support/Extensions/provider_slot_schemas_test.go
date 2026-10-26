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

func TestProviderSlotRegistryCompilesExactSchemasAndRejectsInvalidDocuments(t *testing.T) {
	extension := providerSchemaFixture(t, "providers.owner", providerSlotDefinition(10),
		`{"type":"object","required":["message"],"properties":{"message":{"type":"string"}},"additionalProperties":false}`,
		`{"type":"object","required":["status"],"properties":{"status":{"const":"ok"}},"additionalProperties":false}`,
	)
	registry := NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(extension, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if !registry.HasCompiledSchemas(providerSlotID, providerContractVersion) {
		t.Fatal("exact provider schemas were not compiled")
	}
	if err := registry.ValidateDocument(providerSlotID, providerRequestSchema, map[string]any{"message": "valid"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateDocument(providerSlotID, providerRequestSchema, map[string]any{"message": float64(42)}); !errors.Is(err, ErrProviderSlotInvalid) {
		t.Fatalf("invalid-but-JSON request = %v", err)
	}
	if err := registry.ValidateDocument(providerSlotID, providerResponseSchema, map[string]any{"status": "wrong"}); !errors.Is(err, ErrProviderSlotInvalid) {
		t.Fatalf("invalid-but-JSON response = %v", err)
	}
}

func TestProviderSlotRegistryRejectsCandidateSchemaDigestDrift(t *testing.T) {
	owner := providerSchemaFixture(t, "providers.owner", providerSlotDefinition(10),
		`{"type":"object","properties":{"message":{"type":"string"}}}`,
		`{"type":"object","properties":{"status":{"type":"string"}}}`,
	)
	consumerDeclaration := providerSlotConsumer("providers.consumer.delivery", 20)
	consumer := providerSchemaFixture(t, "providers.consumer", consumerDeclaration,
		`{"type":"object","properties":{"message":{"type":"number"}}}`,
		`{"type":"object","properties":{"status":{"type":"string"}}}`,
	)
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	registry := NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("candidate schema digest drift = %v", err)
	}
}

func TestProviderSlotRegistryAcceptsCandidateWithExactSchemaDigests(t *testing.T) {
	requestSchema := `{"type":"object","properties":{"message":{"type":"string"}}}`
	responseSchema := `{"type":"object","properties":{"status":{"type":"string"}}}`
	owner := providerSchemaFixture(t, "providers.owner", providerSlotDefinition(10), requestSchema, responseSchema)
	consumer := providerSchemaFixture(t, "providers.consumer", providerSlotConsumer("providers.consumer.delivery", 20), requestSchema, responseSchema)
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: owner.ID, Version: "^1.0.0", Kind: "required"}}
	registry := NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(owner, "owner-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatalf("exact candidate schemas: %v", err)
	}
	resolution, err := registry.Discover(ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || len(resolution.Candidates) != 2 || resolution.Candidates[0].Artifact.ExtensionID != consumer.ID {
		t.Fatalf("candidate resolution = %#v, %v", resolution, err)
	}
}

func TestProviderSlotRegistryRejectsUpgradeSchemaDriftWithoutContractVersion(t *testing.T) {
	owner := providerSchemaFixture(t, "providers.owner", providerSlotDefinition(10),
		`{"type":"object","properties":{"message":{"type":"string"}}}`,
		`{"type":"object","properties":{"status":{"type":"string"}}}`,
	)
	upgraded := providerSchemaFixture(t, "providers.owner", providerSlotDefinition(10),
		`{"type":"object","properties":{"message":{"type":"number"}}}`,
		`{"type":"object","properties":{"status":{"type":"string"}}}`,
	)
	upgraded.Version, upgraded.Manifest.Version = "1.1.0", "1.1.0"
	upgraded.PackageDigest = strings.Repeat("b", 64)
	registry := NewVersionedProviderSlotRegistry()
	if err := registry.ReplaceRuntime(owner, "owner-runtime-1"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(upgraded, "owner-runtime-2"); !errors.Is(err, ErrProviderSlotConflict) {
		t.Fatalf("same contract schema drift = %v", err)
	}
	resolution, err := registry.Discover(ProviderSlotCaller{}, providerSlotID, providerContractVersion)
	if err != nil || resolution.Contract.Artifact.RuntimeInstanceID != "owner-runtime-1" {
		t.Fatalf("rejected drift changed snapshot = %#v, %v", resolution, err)
	}
}

func TestProviderSlotSchemaUsesLegacyContentRootAndRejectsSymlinkEscape(t *testing.T) {
	t.Run("legacy zip content root", func(t *testing.T) {
		versionDir := t.TempDir()
		zipPath := filepath.Join(versionDir, "package.zip")
		if err := os.WriteFile(zipPath, []byte("zip-placeholder"), 0o600); err != nil {
			t.Fatal(err)
		}
		extension := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
		extension.PackageDigest = ""
		extension.PackagePath = zipPath
		extension.Source = extensions.SourceUploaded
		writeProviderSchemaFixture(t, &extension, filepath.Join(versionDir, "files"), providerRequestSchema,
			`{"type":"object"}`)
		if validator, _, err := compileExactProviderSchema(extension, providerRequestSchema); err != nil || validator == nil {
			t.Fatalf("legacy content root schema = %T, %v", validator, err)
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.json")
		body := []byte(`{"type":"object"}`)
		if err := os.WriteFile(outside, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "schemas", "request.json")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		extension := versionedProviderExtension("providers.owner", strings.Repeat("a", 64), providerSlotDefinition(10))
		extension.PackagePath = root
		extension.Manifest.PackageFiles = []extensions.ManifestPackageFile{{
			ID: "providers.owner.delivery.request", Kind: "schema", Path: "schemas/request.json",
			Digest: hex.EncodeToString(digest[:]), Version: "1",
		}}
		if _, _, err := compileExactProviderSchema(extension, providerRequestSchema); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink escape = %v", err)
		}
	})
}

func providerSchemaFixture(t *testing.T, id string, declaration extensions.ManifestProvider, requestSchema, responseSchema string) extensions.Extension {
	t.Helper()
	extension := versionedProviderExtension(id, strings.Repeat("a", 64), declaration)
	extension.PackagePath = t.TempDir()
	writeProviderSchemaFixture(t, &extension, extension.PackagePath, providerRequestSchema, requestSchema)
	writeProviderSchemaFixture(t, &extension, extension.PackagePath, providerResponseSchema, responseSchema)
	return extension
}

func writeProviderSchemaFixture(t *testing.T, extension *extensions.Extension, root, reference, schema string) {
	t.Helper()
	schemaID, version, err := protocolV2SchemaRef(reference)
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(schemaID, "providers.owner.delivery.") + ".json"
	path := filepath.Join("schemas", name)
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(schema)
	if err := os.WriteFile(fullPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: schemaID, Kind: "schema", Path: filepath.ToSlash(path), Digest: hex.EncodeToString(digest[:]), Version: version,
	})
}
