package extensionopenapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestBuildProducesDeterministicImmutableExactArtifactAggregate(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("catalog.docs"))
	first, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision() == "" || first.Revision() != second.Revision() || string(first.Document()) != string(second.Document()) {
		t.Fatalf("aggregate is not deterministic: %q != %q", first.Revision(), second.Revision())
	}
	operations := first.GeneratedClientOperations()
	if len(operations) != 1 || operations[0].RouteID != "catalog.docs.catalog" ||
		operations[0].PackageDigest != fixture.PackageDigest || operations[0].RateLimit != "public.read@1" {
		t.Fatalf("generated operations = %#v", operations)
	}
	if sources := first.Sources(); len(sources) != 1 || sources[0].PackageDigest != fixture.PackageDigest ||
		sources[0].Digest != fixture.Manifest.OpenAPI[0].Digest {
		t.Fatalf("sources = %#v", sources)
	}
	var document map[string]any
	if err := json.Unmarshal(first.Document(), &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	operation := paths["/api/catalog/{id}"].(map[string]any)["get"].(map[string]any)
	responses := operation["responses"].(map[string]any)
	response := responses["200"].(map[string]any)
	schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if reference, _ := schema["$ref"].(string); !strings.HasPrefix(reference, "#/x-sforum-sources/source_") || !strings.HasSuffix(reference, "/Catalog") {
		t.Fatalf("reference was not made self-contained: %#v", schema)
	}
	if len(document["x-sforum-sources"].(map[string]any)) != 2 {
		t.Fatalf("referenced package documents missing: %#v", document["x-sforum-sources"])
	}

	returnedDocument := first.Document()
	returnedDocument[0] = '!'
	operations[0].RouteID = "mutated"
	if first.Document()[0] == '!' || first.GeneratedClientOperations()[0].RouteID == "mutated" {
		t.Fatal("snapshot was mutable through a getter")
	}
}

func TestBuildValidatesUnsafeRequestPermissionAndPolicyMetadata(t *testing.T) {
	options := defaultFixtureOptions("catalog.write")
	options.method = "POST"
	options.requestSchema = "catalog.write.catalog.request@1"
	options.guard = extensionmanifest.GuardCorePermission
	options.permission = "catalog.write.manage"
	options.rateLimit = "actor.write@2"
	options.idempotency = "required.24h@1"
	fixture := buildFixture(t, options)
	snapshot, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	operation := snapshot.GeneratedClientOperations()[0]
	if operation.RequestSchema != options.requestSchema || operation.Permission != options.permission ||
		operation.RateLimit != options.rateLimit || operation.Idempotency != options.idempotency {
		t.Fatalf("generated operation = %#v", operation)
	}

	drift := defaultFixtureOptions("catalog.write-drift")
	drift.method = "POST"
	drift.requestSchema = "catalog.write-drift.catalog.request@1"
	drift.document = strings.Replace(fixtureDocument(drift), "x-sforum-request-schema: "+drift.requestSchema, "x-sforum-request-schema: wrong.request@1", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, drift)}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("request metadata drift error = %v", err)
	}
}

func TestBuildEmptyAggregateIsDeterministicAndValid(t *testing.T) {
	first, err := Build(BuildInput{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(BuildInput{})
	if err != nil || first.Revision() != second.Revision() {
		t.Fatalf("empty aggregate = %q, %q, %v", first.Revision(), second.Revision(), err)
	}
	var document map[string]any
	if err := json.Unmarshal(first.Document(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" || len(document["paths"].(map[string]any)) != 0 {
		t.Fatalf("empty document = %#v", document)
	}
}

func TestBuildRejectsArtifactAndManifestIdentityDrift(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("artifact.docs"))
	t.Run("tree digest", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(fixture.Root, "extra.txt"), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("error = %v", err)
		}
	})

	clean := buildFixture(t, defaultFixtureOptions("manifest.docs"))
	clean.Manifest.Name = "Caller supplied a different manifest"
	if _, err := Build(BuildInput{Artifacts: []Artifact{clean}}); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("manifest mismatch error = %v", err)
	}
}

func TestBuildAcceptsExplicitCoreReplacementAndRejectsOtherCollisions(t *testing.T) {
	core := CoreOperation{RouteID: "core.route.catalog", Path: "/api/catalog/:id", Method: "GET", OperationID: "coreCatalog"}
	add := buildFixture(t, defaultFixtureOptions("collision.add"))
	if _, err := Build(BuildInput{Core: []CoreOperation{core}, Artifacts: []Artifact{add}}); !errors.Is(err, ErrCollision) {
		t.Fatalf("core/add collision error = %v", err)
	}

	replaceOptions := defaultFixtureOptions("collision.replace")
	replaceOptions.action = extensionmanifest.RouteActionReplace
	replaceOptions.targetID = core.RouteID
	replace := buildFixture(t, replaceOptions)
	if _, err := Build(BuildInput{Core: []CoreOperation{core}, Artifacts: []Artifact{replace}}); err != nil {
		t.Fatalf("explicit replacement failed: %v", err)
	}

	firstOptions := defaultFixtureOptions("collision.first")
	firstOptions.path, firstOptions.manifestPath = "/shared/{id}", "/shared/:id"
	first := buildFixture(t, firstOptions)
	secondOptions := defaultFixtureOptions("collision.second")
	secondOptions.path, secondOptions.manifestPath = "/shared/{slug}", "/shared/:slug"
	second := buildFixture(t, secondOptions)
	if _, err := Build(BuildInput{Artifacts: []Artifact{first, second}}); !errors.Is(err, ErrCollision) {
		t.Fatalf("plugin path collision error = %v", err)
	}
}

func TestBuildRejectsNamespaceAndOperationIDCollisions(t *testing.T) {
	firstOptions := defaultFixtureOptions("identity.first")
	firstOptions.path, firstOptions.manifestPath = "/first", "/first"
	first := buildFixture(t, firstOptions)

	namespaceOptions := defaultFixtureOptions("identity.second")
	namespaceOptions.path, namespaceOptions.manifestPath = "/second", "/second"
	namespaceOptions.namespace = firstOptions.namespace
	namespaceOptions.operationID = firstOptions.namespace + ".second"
	namespace := buildFixture(t, namespaceOptions)
	if _, err := Build(BuildInput{Artifacts: []Artifact{first, namespace}}); !errors.Is(err, ErrCollision) {
		t.Fatalf("namespace collision error = %v", err)
	}

	if _, err := Build(BuildInput{Core: []CoreOperation{{
		RouteID: "core.route.operation", Path: "/core", Method: "GET", OperationID: firstOptions.operationID,
	}}, Artifacts: []Artifact{first}}); !errors.Is(err, ErrCollision) {
		t.Fatalf("operationId collision error = %v", err)
	}
}

func TestBuildRejectsRouteMetadataAndBodySchemaDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fixtureOptions)
	}{
		{"route id", func(o *fixtureOptions) {
			o.document = strings.Replace(fixtureDocument(*o), o.extensionID+".catalog\n", o.extensionID+".missing\n", 1)
		}},
		{"contract version", func(o *fixtureOptions) {
			o.document = strings.Replace(fixtureDocument(*o), o.extensionID+".catalog@1", o.extensionID+".catalog@2", 1)
		}},
		{"guard", func(o *fixtureOptions) {
			o.document = strings.Replace(fixtureDocument(*o), "core.guard.public", "core.guard.login", 1)
		}},
		{"rate limit", func(o *fixtureOptions) {
			o.document = strings.Replace(fixtureDocument(*o), "public.read@1", "unlimited", 1)
		}},
		{"idempotency", func(o *fixtureOptions) { o.document = strings.Replace(fixtureDocument(*o), "disabled", "required", 1) }},
		{"missing response schema", func(o *fixtureOptions) {
			o.document = strings.Replace(fixtureDocument(*o), "          content:\n            application/json:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n", "", 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := defaultFixtureOptions("drift." + strings.ReplaceAll(test.name, " ", "-"))
			test.mutate(&options)
			fixture := buildFixture(t, options)
			if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBuildRejectsInvalidOpenAPIVersionAndMissingRouteCoverage(t *testing.T) {
	version := defaultFixtureOptions("invalid.version")
	version.document = strings.Replace(fixtureDocument(version), "openapi: 3.1.0", "openapi: 3.0.3", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, version)}}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("version error = %v", err)
	}

	coverage := defaultFixtureOptions("invalid.coverage")
	coverage.document = strings.Replace(fixtureDocument(coverage), "x-sforum-route-id: invalid.coverage.catalog", "x-sforum-route-id: invalid.coverage.unknown", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, coverage)}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("coverage error = %v", err)
	}

	method := defaultFixtureOptions("invalid.method-key")
	method.document = strings.Replace(fixtureDocument(method), "    get:\n", "    GET:\n", 1)
	if _, err := Build(BuildInput{Artifacts: []Artifact{buildFixture(t, method)}}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("method key error = %v", err)
	}
}
