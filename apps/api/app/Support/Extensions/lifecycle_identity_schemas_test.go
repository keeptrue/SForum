package extensionsruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestBuildLifecycleIdentityPublicationBindsExactUserFieldAndProviderSchemas(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 141, "")
	field := &extension.Manifest.Identity.UserFields[0]
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".field.code.schema", "schemas/field-code.json", "1",
		`{"type":"string","minLength":2}`)
	extension.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: extension.ID + ".provider", ContractVersion: extension.ID + ".provider@1",
		Kind: "risk", Handler: "identity.risk", Operations: []extensionmanifest.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: "schemas/risk-input.json",
			OutputSchema: extension.ID + ".risk.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
		}},
	}}
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.input", "schemas/risk-input.json", "1",
		`{"type":"object","required":["risk"],"properties":{"risk":{"type":"boolean"}},"additionalProperties":false}`)
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.output", "schemas/risk-output.json", "1",
		`{"type":"object","required":["disposition"],"properties":{"disposition":{"enum":["allow","deny"]}},"additionalProperties":false}`)
	binding := lifecycleRegistryBinding(extension, "identity-exact-schemas")
	publication, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil || publication == nil {
		t.Fatalf("build exact Identity publication = %#v, %v", publication, err)
	}
	gotField := publication.Identity.UserFields[0]
	provider := publication.Identity.Providers[0]
	operation := provider.Operations[0]
	if gotField.Schema != field.Schema || gotField.SchemaWireReference != extension.ID+".field.code.schema@1" ||
		gotField.SchemaDigest == "" || operation.InputSchema != "schemas/risk-input.json" ||
		operation.InputSchemaWireReference != extension.ID+".risk.input@1" ||
		operation.OutputSchemaWireReference != extension.ID+".risk.output@1" ||
		operation.InputSchemaDigest == "" || operation.OutputSchemaDigest == "" {
		t.Fatalf("exact Identity metadata = %#v / %#v", gotField, operation)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateUserFieldValue(identityregistry.UserFieldSchemaClaim{
		FieldID: gotField.ID, ContractVersion: gotField.ContractVersion, Artifact: publication.Artifact,
	}, "ok"); err != nil {
		t.Fatalf("exact user-field validator = %v", err)
	}
	if err := registry.ValidateUserFieldValue(identityregistry.UserFieldSchemaClaim{
		FieldID: gotField.ID, ContractVersion: gotField.ContractVersion, Artifact: publication.Artifact,
	}, "x"); !errors.Is(err, identityregistry.ErrSchemaValueInvalid) {
		t.Fatalf("invalid exact user-field value = %v", err)
	}
	providerClaim := identityregistry.ProviderOperationSchemaClaim{
		ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
		Operation: operation.Name, Artifact: publication.Artifact,
	}
	if err := registry.ValidateProviderOperationInput(providerClaim, map[string]any{"risk": true}); err != nil {
		t.Fatalf("exact provider input validator = %v", err)
	}
	if err := registry.ValidateProviderOperationOutput(providerClaim, map[string]any{"disposition": "allow"}); err != nil {
		t.Fatalf("exact provider output validator = %v", err)
	}
}

func TestBuildLifecycleIdentityPublicationBindsPackagePathUserFieldSchema(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 145, "")
	field := &extension.Manifest.Identity.UserFields[0]
	field.Schema = "schemas/field-code.json"
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".field.code.schema", field.Schema, "1",
		`{"type":"string","minLength":2}`)

	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil || publication == nil {
		t.Fatalf("build package-path user-field publication = %#v, %v", publication, err)
	}
	got := publication.Identity.UserFields[0]
	if got.Schema != field.Schema || got.SchemaWireReference != extension.ID+".field.code.schema@1" ||
		got.SchemaDigest == "" {
		t.Fatalf("package-path user-field metadata = %#v", got)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	claim := identityregistry.UserFieldSchemaClaim{
		FieldID: got.ID, ContractVersion: got.ContractVersion, Artifact: publication.Artifact,
	}
	if err := registry.ValidateUserFieldValue(claim, "ok"); err != nil {
		t.Fatalf("valid package-path user-field value = %v", err)
	}
	if err := registry.ValidateUserFieldValue(claim, "x"); !errors.Is(err, identityregistry.ErrSchemaValueInvalid) {
		t.Fatalf("invalid package-path user-field value = %v", err)
	}
}

func TestBuildLifecycleIdentityPublicationBindsProviderPackagePathSchemas(t *testing.T) {
	extension := lifecyclePackagePathIdentityProviderExtension(146)
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.input", "schemas/risk-input.json", "1",
		`{"type":"object","required":["risk"],"properties":{"risk":{"type":"boolean"}},"additionalProperties":false}`)
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.output", "schemas/risk-output.json", "1",
		`{"type":"object","required":["disposition"],"properties":{"disposition":{"enum":["allow","deny"]}},"additionalProperties":false}`)

	binding := lifecycleRegistryBinding(extension, "identity-package-path-provider")
	publication, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil || publication == nil {
		t.Fatalf("build package-path provider publication = %#v, %v", publication, err)
	}
	provider := publication.Identity.Providers[0]
	operation := provider.Operations[0]
	if operation.InputSchema != "schemas/risk-input.json" ||
		operation.InputSchemaWireReference != extension.ID+".risk.input@1" ||
		operation.OutputSchema != "schemas/risk-output.json" ||
		operation.OutputSchemaWireReference != extension.ID+".risk.output@1" ||
		operation.InputSchemaDigest == "" || operation.OutputSchemaDigest == "" {
		t.Fatalf("package-path provider metadata = %#v", operation)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	claim := identityregistry.ProviderOperationSchemaClaim{
		ProviderID: provider.ID, ContractVersion: provider.ContractVersion,
		Operation: operation.Name, Artifact: publication.Artifact,
	}
	if err := registry.ValidateProviderOperationInput(claim, map[string]any{"risk": true}); err != nil {
		t.Fatalf("valid package-path provider input = %v", err)
	}
	if err := registry.ValidateProviderOperationOutput(claim, map[string]any{"disposition": "allow"}); err != nil {
		t.Fatalf("valid package-path provider output = %v", err)
	}
}

func TestBuildLifecycleIdentityPublicationRejectsInvalidProviderPackagePathSchemas(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*extensions.Extension)
	}{
		{name: "undeclared input", mutate: func(extension *extensions.Extension) {
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.output", "schemas/risk-output.json", "1", `{"type":"object"}`)
		}},
		{name: "undeclared output", mutate: func(extension *extensions.Extension) {
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.input", "schemas/risk-input.json", "1", `{"type":"object"}`)
		}},
		{name: "wrong-kind input", mutate: func(extension *extensions.Extension) {
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.input", "schemas/risk-input.json", "1", `{"type":"object"}`)
			extension.Manifest.PackageFiles[len(extension.Manifest.PackageFiles)-1].Kind = "asset"
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.output", "schemas/risk-output.json", "1", `{"type":"object"}`)
		}},
		{name: "wrong-kind output", mutate: func(extension *extensions.Extension) {
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.input", "schemas/risk-input.json", "1", `{"type":"object"}`)
			writeLifecycleIdentitySchema(t, extension, extension.ID+".risk.output", "schemas/risk-output.json", "1", `{"type":"object"}`)
			extension.Manifest.PackageFiles[len(extension.Manifest.PackageFiles)-1].Kind = "asset"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := lifecyclePackagePathIdentityProviderExtension(147)
			test.mutate(&extension)
			binding := lifecycleRegistryBinding(extension, "identity-invalid-package-path-provider")
			if _, err := buildLifecycleIdentityPublication(extension, binding); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
				t.Fatalf("invalid package-path provider Schema = %v", err)
			}
		})
	}
}

func TestBuildLifecycleIdentityPublicationKeepsDigestlessCatalogFieldInert(t *testing.T) {
	extension := lifecycleIdentityExtension("1.0.0", 142, "")
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".unrelated.schema", "schemas/unrelated.json", "1", `{"type":"boolean"}`)
	publication, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{})
	if err != nil || publication == nil || publication.Identity.UserFields[0].SchemaDigest != "" ||
		publication.Identity.UserFields[0].SchemaWireReference != "" {
		t.Fatalf("digestless catalog-only user-field publication = %#v, %v", publication, err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	field := publication.Identity.UserFields[0]
	if err := registry.ValidateUserFieldValue(identityregistry.UserFieldSchemaClaim{
		FieldID: field.ID, ContractVersion: field.ContractVersion, Artifact: publication.Artifact,
	}, "value"); !errors.Is(err, identityregistry.ErrSchemaUnavailable) {
		t.Fatalf("digestless catalog-only field became executable = %v", err)
	}
}

func TestBuildLifecycleIdentityPublicationRejectsInvalidExactSchemaMaterial(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*extensions.Extension)
	}{
		{name: "undeclared package path", mutate: func(extension *extensions.Extension) {
			extension.Manifest.Identity.UserFields[0].Schema = "schemas/missing.json"
		}},
		{name: "versionless wire", mutate: func(extension *extensions.Extension) {
			extension.Manifest.Identity.UserFields[0].Schema = "schemas/field.json"
			writeLifecycleIdentitySchema(t, extension, extension.ID+".field.schema", "schemas/field.json", "", `{"type":"string"}`)
		}},
		{name: "declared id wrong version", mutate: func(extension *extensions.Extension) {
			field := extension.Manifest.Identity.UserFields[0]
			writeLifecycleIdentitySchema(t, extension, strings.TrimSuffix(field.Schema, "@1"), "schemas/field-v2.json", "2", `{"type":"string"}`)
		}},
		{name: "declared id wrong kind", mutate: func(extension *extensions.Extension) {
			field := extension.Manifest.Identity.UserFields[0]
			extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
				ID: strings.TrimSuffix(field.Schema, "@1"), Kind: "asset", Path: "schemas/field.json",
				Digest: strings.Repeat("a", 64), Version: "1",
			})
		}},
		{name: "digest drift", mutate: func(extension *extensions.Extension) {
			field := extension.Manifest.Identity.UserFields[0]
			writeLifecycleIdentitySchema(t, extension, strings.TrimSuffix(field.Schema, "@1"), "schemas/field.json", "1", `{"type":"string"}`)
			path := filepath.Join(extension.PackagePath, "schemas", "field.json")
			if err := os.WriteFile(path, []byte(`{"type":"number"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := lifecycleIdentityExtension("1.0.0", 143, "")
			test.mutate(&extension)
			if _, err := buildLifecycleIdentityPublication(extension, extensions.LifecycleRuntimeBinding{}); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
				t.Fatalf("invalid exact Identity Schema = %v", err)
			}
		})
	}
}

func TestLifecycleIdentityGraphIgnoresRecompiledValidatorsButRejectsDigestDrift(t *testing.T) {
	extension := lifecycleExecutableIdentitySchemaExtension(t, `{"type":"object"}`)
	binding := lifecycleRegistryBinding(extension, "identity-recompiled-runtime")
	first, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil {
		t.Fatal(err)
	}
	recompiled, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(*first); err != nil {
		t.Fatal(err)
	}
	graph, err := lifecycleIdentityGraph(registry.Snapshot(), extension.ID, recompiled, recompiled)
	if err != nil || len(graph) != 1 || !identityregistry.EqualPublicContract(graph[0], *recompiled) {
		t.Fatalf("recompiled exact graph = %#v, %v", graph, err)
	}

	driftedExtension := lifecycleExecutableIdentitySchemaExtension(t, `{"type":"array"}`)
	driftedExtension.PackageDigest = extension.PackageDigest
	driftedExtension.ActiveVersionID = extension.ActiveVersionID
	driftedBinding := lifecycleRegistryBinding(driftedExtension, "identity-recompiled-runtime")
	drifted, err := buildLifecycleIdentityPublication(driftedExtension, driftedBinding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycleIdentityGraph(registry.Snapshot(), extension.ID, drifted, drifted); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("same-artifact Schema digest drift = %v", err)
	}
}

func lifecycleExecutableIdentitySchemaExtension(t *testing.T, schema string) extensions.Extension {
	t.Helper()
	extension := lifecycleIdentityExtension("1.0.0", 144, "")
	extension.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: extension.ID + ".provider", ContractVersion: extension.ID + ".provider@1",
		Kind: "risk", Handler: "identity.risk", Operations: []extensionmanifest.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: extension.ID + ".risk.input@1",
			OutputSchema: extension.ID + ".risk.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
		}},
	}}
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.input", "schemas/risk-input.json", "1", schema)
	writeLifecycleIdentitySchema(t, &extension, extension.ID+".risk.output", "schemas/risk-output.json", "1", schema)
	return extension
}

func lifecyclePackagePathIdentityProviderExtension(versionID int64) extensions.Extension {
	extension := lifecycleIdentityExtension("1.0.0", versionID, "")
	extension.Manifest.Identity.UserFields = nil
	extension.Manifest.Identity.Providers = []extensionmanifest.ManifestIdentityProvider{{
		ID: extension.ID + ".provider", ContractVersion: extension.ID + ".provider@1",
		Kind: "risk", Handler: "identity.risk", Operations: []extensionmanifest.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: "schemas/risk-input.json",
			OutputSchema: "schemas/risk-output.json", TimeoutMS: 1000, FailurePolicy: "fail_closed",
		}},
	}}
	return extension
}
