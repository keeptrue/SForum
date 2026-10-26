package extensionopenapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestRouteSchemaCatalogBuildsImmutableExactArtifactValidator(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.catalog"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	if catalog.Revision() == "" || len(catalog.Bindings()) != 1 {
		t.Fatalf("revision=%q bindings=%#v", catalog.Revision(), catalog.Bindings())
	}
	binding := catalog.Bindings()[0]
	if binding.ExtensionID != fixture.ExtensionID || binding.ExtensionVersion != fixture.Version ||
		binding.PackageDigest != fixture.PackageDigest || binding.SchemaID != "schema.catalog.catalog.response@1" ||
		binding.Direction != RouteSchemaResponse || binding.MediaType != "application/json" || binding.ResponseStatus != "200" ||
		binding.SchemaDigest == "" {
		t.Fatalf("binding=%#v", binding)
	}
	bindings := catalog.Bindings()
	bindings[0].SchemaID = "mutated"
	if catalog.Bindings()[0].SchemaID == "mutated" {
		t.Fatal("catalog bindings are mutable through getter")
	}
	if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, []byte(`{"id":"42"}`)); err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	for _, payload := range [][]byte{
		[]byte(`{}`), []byte(`{"id":42}`), []byte(`not-json`), []byte(`{"id":"42"} {}`),
	} {
		if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, payload); !errors.Is(err, ErrRouteSchemaPayloadInvalid) {
			t.Fatalf("payload %q error=%v", payload, err)
		}
	}
}

func TestRouteSchemaCatalogDoesNotPublishOrRequireRoutePolicies(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.policy-neutral"))
	fixture.Policies = nil
	if _, err := Build(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("public aggregate accepted missing Host policies: %v", err)
	}

	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	binding := catalog.Bindings()[0]
	if err := validateFixtureRouteSchema(
		catalog, context.Background(), routeSchemaFixtureArtifact(fixture), binding, []byte(`{"id":"42"}`),
	); err != nil {
		t.Fatalf("policy-neutral schema validation: %v", err)
	}
}

func TestRouteSchemaCatalogRejectsMissingAndCrossArtifactLookups(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.exact"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	schemaID := "schema.exact.catalog.response@1"
	binding := catalog.Bindings()[0]
	binding.SchemaID = "schema.exact.missing@1"
	if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, []byte(`{}`)); !errors.Is(err, ErrRouteSchemaMissing) {
		t.Fatalf("missing error=%v", err)
	}
	wrongDigest := artifact
	wrongDigest.PackageDigest = strings.Repeat("f", 64)
	binding.SchemaID = schemaID
	if err := validateFixtureRouteSchema(catalog, context.Background(), wrongDigest, binding, []byte(`{"id":"42"}`)); !errors.Is(err, ErrRouteSchemaArtifactMismatch) {
		t.Fatalf("digest error=%v", err)
	}
	wrongVersion := artifact
	wrongVersion.ExtensionVersion = "2.0.0"
	if err := validateFixtureRouteSchema(catalog, context.Background(), wrongVersion, binding, []byte(`{"id":"42"}`)); !errors.Is(err, ErrRouteSchemaMissing) {
		t.Fatalf("version error=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateFixtureRouteSchema(catalog, cancelled, artifact, binding, []byte(`{"id":"42"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestRouteSchemaCatalogRejectsNonJSONOperationMediaSchemas(t *testing.T) {
	options := defaultFixtureOptions("schema.ambiguous")
	document := fixtureDocument(options)
	document = strings.Replace(document,
		"          content:\n            application/json:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n",
		"          content:\n            application/json:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n            text/plain:\n              schema:\n                type: integer\n",
		1,
	)
	options.document = document
	fixture := buildFixture(t, options)
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}}); !errors.Is(err, ErrRouteSchemaCatalogInvalid) {
		t.Fatalf("non-JSON media error=%v", err)
	}
}

func TestRouteSchemaCatalogExcludesOpaqueStreamPayloadValidators(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		method         string
		requestSchema  string
		requestMedia   string
		responseMedia  string
		responseStatus string
	}{
		{name: "sse", mode: extensionmanifest.RouteModeSSE, method: "GET", responseMedia: "text/event-stream", responseStatus: "200"},
		{name: "websocket", mode: extensionmanifest.RouteModeWebSocket, method: "GET", responseMedia: "application/octet-stream", responseStatus: "101"},
		{name: "stream", mode: extensionmanifest.RouteModeStream, method: "GET", responseMedia: "application/octet-stream", responseStatus: "200"},
		{name: "multipart", mode: extensionmanifest.RouteModeMultipart, method: "POST", requestSchema: "opaque.multipart.request@1", requestMedia: "multipart/form-data", responseMedia: "application/octet-stream", responseStatus: "200"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := defaultFixtureOptions("opaque." + test.name)
			options.mode = test.mode
			options.method = test.method
			options.requestSchema = test.requestSchema
			document := fixtureDocument(options)
			if test.requestMedia != "" {
				document = strings.Replace(document, "application/json:", test.requestMedia+":", 1)
			}
			document = strings.Replace(document, "application/json:", test.responseMedia+":", 1)
			document = strings.Replace(document, `"200":`, `"`+test.responseStatus+`":`, 1)
			options.document = document
			fixture := buildFixture(t, options)

			catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
			if err != nil {
				t.Fatal(err)
			}
			if bindings := catalog.Bindings(); len(bindings) != 0 {
				t.Fatalf("opaque %s published JSON validators: %#v", test.mode, bindings)
			}

			aggregate, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
			if err != nil {
				t.Fatal(err)
			}
			operations := aggregate.GeneratedClientOperations()
			if len(operations) != 1 || operations[0].Mode != test.mode ||
				operations[0].StreamContract != StreamContractOpaqueBytesV1 ||
				operations[0].PayloadValidation != PayloadValidationPluginOwned {
				t.Fatalf("opaque generated operation = %#v", operations)
			}
		})
	}
}

func TestOpaqueStreamOpenAPIContentDoesNotRequireManifestJSONSchemaID(t *testing.T) {
	options := defaultFixtureOptions("opaque.schema-less")
	options.mode = extensionmanifest.RouteModeStream
	options.responseSchema = ""
	options.document = strings.Replace(
		fixtureDocument(options),
		"          description: ok\n",
		"          description: ok\n          content:\n            application/octet-stream:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n",
		1,
	)
	fixture := buildFixture(t, options)

	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Bindings()) != 0 {
		t.Fatalf("schema-less opaque stream published JSON bindings: %#v", catalog.Bindings())
	}
	aggregate, err := Build(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	operations := aggregate.GeneratedClientOperations()
	if len(operations) != 1 || operations[0].ResponseSchema != "" ||
		operations[0].StreamContract != StreamContractOpaqueBytesV1 ||
		operations[0].PayloadValidation != PayloadValidationPluginOwned {
		t.Fatalf("schema-less opaque generated operation = %#v", operations)
	}
}

func TestRouteSchemaCatalogValidatesStructuredJSONMedia(t *testing.T) {
	options := defaultFixtureOptions("schema.problem-json")
	options.document = strings.Replace(
		fixtureDocument(options), "application/json:", "application/problem+json:", 1,
	)
	fixture := buildFixture(t, options)
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	binding := catalog.Bindings()[0]
	if binding.MediaType != "application/problem+json" {
		t.Fatalf("binding=%#v", binding)
	}
	if err := validateFixtureRouteSchema(
		catalog, context.Background(), routeSchemaFixtureArtifact(fixture), binding, []byte(`{"id":"42"}`),
	); err != nil {
		t.Fatalf("structured JSON validation: %v", err)
	}
}

func TestRouteSchemaCatalogSelectsExactClassAndDefaultResponseStatus(t *testing.T) {
	options := defaultFixtureOptions("schema.status")
	document := fixtureDocument(options)
	marker := "      responses:\n"
	index := strings.Index(document, marker)
	if index < 0 {
		t.Fatal("fixture responses marker missing")
	}
	options.document = document[:index] + `      responses:
        "200":
          description: exact success
          content:
            application/json:
              schema:
                $ref: 'schemas/common.json#/Catalog'
        "2XX":
          description: success class
          content:
            application/json:
              schema:
                type: object
                properties:
                  class: { type: string }
                required: [class]
        "302":
          description: redirect payload
          content:
            application/json:
              schema:
                type: object
                properties:
                  location: { type: string }
                required: [location]
        "422":
          description: invalid input
          content:
            application/json:
              schema:
                type: object
                properties:
                  code: { type: integer }
                required: [code]
        "500":
          description: server failure
          content:
            application/json:
              schema:
                type: object
                properties:
                  error: { type: string }
                required: [error]
        default:
          description: fallback
          content:
            application/json:
              schema:
                type: object
                properties:
                  fallback: { type: boolean }
                required: [fallback]
`
	fixture := buildFixture(t, options)
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	binding := catalog.Bindings()[0]
	for _, test := range []struct {
		status  int
		payload string
	}{
		{200, `{"id":"42"}`},
		{201, `{"class":"2xx"}`},
		{302, `{"location":"/next"}`},
		{422, `{"code":7}`},
		{500, `{"error":"failed"}`},
		{404, `{"fallback":true}`},
	} {
		if err := catalog.ValidateRouteSchema(
			context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, binding.Method,
			binding.Method, binding.ContractVersion, binding.Action, binding.SchemaID, "application/json", test.status, []byte(test.payload),
		); err != nil {
			t.Fatalf("status %d: %v", test.status, err)
		}
	}
	if err := catalog.ValidateRouteSchema(
		context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, binding.Method,
		binding.Method, binding.ContractVersion, binding.Action, binding.SchemaID, "application/json", 200, []byte(`{"class":"wrong"}`),
	); !errors.Is(err, ErrRouteSchemaPayloadInvalid) {
		t.Fatalf("exact status did not win over 2XX: %v", err)
	}
	if err := catalog.ValidateRouteSchema(
		context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, binding.Method,
		binding.Method, binding.ContractVersion, binding.Action, binding.SchemaID, "application/xml", 200, []byte(`<id>42</id>`),
	); !errors.Is(err, ErrRouteSchemaCatalogInvalid) {
		t.Fatalf("undocumented media error=%v", err)
	}
}

func TestRouteSchemaCatalogUsesExplicitJSONDefaultForPackageLocalMiddleware(t *testing.T) {
	options := defaultFixtureOptions("schema.local-middleware")
	fixture := buildFixture(t, options)
	const localPath = "openapi/schemas/middleware.json"
	localSchema := []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	writeFixtureFile(t, fixture.Root, localPath, localSchema)
	fixture = rebuildFixtureManifest(t, fixture, func(manifest *extensionmanifest.Manifest) {
		manifest.PackageFiles = append(manifest.PackageFiles, extensionmanifest.ManifestPackageFile{
			ID: options.extensionID + ".file.middleware-schema", Kind: "schema", Path: localPath, Digest: digestBytes(localSchema),
		})
		manifest.Routes = append(manifest.Routes, extensionmanifest.ManifestRoute{
			ID: options.extensionID + ".global", ContractVersion: options.extensionID + ".global@1",
			Action: extensionmanifest.RouteActionGlobalMiddleware, Guard: extensionmanifest.GuardCoreLogin,
			Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP, Handler: "route.global",
			RequestSchema: localPath, ResponseSchema: localPath,
		})
	})
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	seenRequest, seenResponse := false, false
	for _, binding := range catalog.Bindings() {
		if binding.Action != extensionmanifest.RouteActionGlobalMiddleware {
			continue
		}
		if binding.MediaType != "application/json" || binding.Method != "*" {
			t.Fatalf("middleware binding=%#v", binding)
		}
		if binding.Direction == RouteSchemaRequest {
			seenRequest = binding.ResponseStatus == ""
		} else {
			seenResponse = binding.ResponseStatus == "default"
		}
		status := 0
		if binding.Direction == RouteSchemaResponse {
			status = 422
		}
		if err := catalog.ValidateRouteSchema(
			context.Background(), artifact, string(binding.Direction), binding.RouteID, binding.Method,
			"GET", binding.ContractVersion, binding.Action, binding.SchemaID, binding.MediaType,
			status, []byte(`{"ok":true}`),
		); err != nil {
			t.Fatalf("validate middleware binding: %v", err)
		}
	}
	if !seenRequest || !seenResponse {
		t.Fatalf("request=%v response=%v bindings=%#v", seenRequest, seenResponse, catalog.Bindings())
	}
}

func TestRouteSchemaCatalogBindsSameContractIDToExactDirectionAndRouteContext(t *testing.T) {
	options := defaultFixtureOptions("schema.duplicate")
	options.method = "POST"
	options.requestSchema = options.responseSchema
	options.guard = "core.guard.login"
	options.rateLimit = "actor.write@1"
	options.idempotency = "required.24h@1"
	fixture := buildFixture(t, options)
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := catalog.Bindings()
	if len(bindings) != 2 || bindings[0].Direction == bindings[1].Direction {
		t.Fatalf("bindings=%#v", bindings)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	for _, binding := range bindings {
		if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, []byte(`{"id":"42"}`)); err != nil {
			t.Fatalf("valid %s binding: %v", binding.Direction, err)
		}
		for _, mutate := range []func(*RouteSchemaBinding){
			func(value *RouteSchemaBinding) { value.RouteID += ".other" },
			func(value *RouteSchemaBinding) { value.ContractVersion += ".stale" },
			func(value *RouteSchemaBinding) { value.Action = extensionmanifest.RouteActionFilter },
		} {
			changed := binding
			mutate(&changed)
			if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, changed, []byte(`{"id":"42"}`)); !errors.Is(err, ErrRouteSchemaMissing) {
				t.Fatalf("mutated binding %#v error=%v", changed, err)
			}
		}
	}
	directionalOptions := options
	directionalOptions.extensionID = "schema.directional"
	directionalOptions.namespace = "schema.directional.api"
	directionalOptions.operationID = "schema.directional.api.getCatalog"
	directionalOptions.requestSchema = "schema.directional.catalog.request@1"
	directionalOptions.responseSchema = "schema.directional.catalog.response@1"
	directionalOptions.document = ""
	directional := buildFixture(t, directionalOptions)
	directionalCatalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{directional}})
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range directionalCatalog.Bindings() {
		if binding.Direction != RouteSchemaResponse {
			continue
		}
		binding.Direction = RouteSchemaRequest
		if err := validateFixtureRouteSchema(
			directionalCatalog, context.Background(), routeSchemaFixtureArtifact(directional), binding, []byte(`{"id":"42"}`),
		); !errors.Is(err, ErrRouteSchemaMissing) {
			t.Fatalf("response-only schema used as request error=%v", err)
		}
	}
}

func TestRouteSchemaCatalogIsolatesOperationVariantsThatReuseSchemaID(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.operation-isolation"))
	document := []byte(`openapi: 3.1.0
info:
  title: Operation Isolation
  version: 1.0.0
paths:
  /api/first:
    get:
      operationId: schema.operation-isolation.api.first
      x-sforum-route-id: schema.operation-isolation.first
      x-sforum-contract-version: schema.operation-isolation.first@1
      x-sforum-guard: core.guard.public
      x-sforum-response-schema: schema.operation-isolation.shared@1
      x-sforum-rate-limit: public.read@1
      x-sforum-idempotency: disabled
      responses:
        '200':
          description: first
          content:
            application/json:
              schema:
                type: object
                properties:
                  first: { const: true }
                required: [first]
                additionalProperties: false
  /api/second:
    get:
      operationId: schema.operation-isolation.api.second
      x-sforum-route-id: schema.operation-isolation.second
      x-sforum-contract-version: schema.operation-isolation.second@1
      x-sforum-guard: core.guard.public
      x-sforum-response-schema: schema.operation-isolation.shared@1
      x-sforum-rate-limit: public.read@1
      x-sforum-idempotency: disabled
      responses:
        '202':
          description: second
          content:
            application/problem+json:
              schema:
                type: object
                properties:
                  second: { const: true }
                required: [second]
                additionalProperties: false
`)
	writeFixtureFile(t, fixture.Root, "openapi/routes.yaml", document)
	sharedID := "schema.operation-isolation.shared@1"
	routesByID := []extensionmanifest.ManifestRoute{
		{
			ID: "schema.operation-isolation.first", ContractVersion: "schema.operation-isolation.first@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/api/first", Methods: []string{"GET"},
			Guard: extensionmanifest.GuardCorePublic, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
			Handler: "route.first", ResponseSchema: sharedID,
		},
		{
			ID: "schema.operation-isolation.second", ContractVersion: "schema.operation-isolation.second@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/api/second", Methods: []string{"GET"},
			Guard: extensionmanifest.GuardCorePublic, Fallback: "closed", Mode: extensionmanifest.RouteModeHTTP,
			Handler: "route.second", ResponseSchema: sharedID,
		},
	}
	fixture = rebuildFixtureManifest(t, fixture, func(manifest *extensionmanifest.Manifest) {
		manifest.Routes = routesByID
		manifest.OpenAPI[0].Digest = digestBytes(document)
		for index := range manifest.PackageFiles {
			if manifest.PackageFiles[index].Path == "openapi/routes.yaml" {
				manifest.PackageFiles[index].Digest = digestBytes(document)
			}
		}
	})
	fixture.Policies = []RoutePolicy{
		{RouteID: routesByID[0].ID, Method: "GET", RateLimit: "public.read@1", Idempotency: "disabled", Security: SecurityPublic},
		{RouteID: routesByID[1].ID, Method: "GET", RateLimit: "public.read@1", Idempotency: "disabled", Security: SecurityPublic},
	}
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	bindings := catalog.Bindings()
	if len(bindings) != 2 {
		t.Fatalf("bindings=%#v", bindings)
	}
	for _, binding := range bindings {
		payload := []byte(`{"first":true}`)
		wrongMedia, wrongStatus := "application/problem+json", 202
		if binding.RouteID == routesByID[1].ID {
			payload = []byte(`{"second":true}`)
			wrongMedia, wrongStatus = "application/json", 200
		}
		if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, payload); err != nil {
			t.Fatalf("valid exact binding %#v: %v", binding, err)
		}
		if err := catalog.ValidateRouteSchema(
			context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, binding.Method, binding.Method,
			binding.ContractVersion, binding.Action, binding.SchemaID, wrongMedia, wrongStatus, payload,
		); !errors.Is(err, ErrRouteSchemaMissing) {
			t.Fatalf("cross-operation variant rebound for %#v: %v", binding, err)
		}
	}
}

func TestRouteSchemaCatalogHEADUsesGETContractWithoutParsingBody(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.head"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	binding := catalog.Bindings()[0]
	artifact := routeSchemaFixtureArtifact(fixture)
	if err := catalog.ValidateRouteSchema(
		context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, "GET", "HEAD",
		binding.ContractVersion, binding.Action, binding.SchemaID, binding.MediaType, 200, nil,
	); err != nil {
		t.Fatalf("HEAD response: %v", err)
	}
	if err := catalog.ValidateRouteSchema(
		context.Background(), artifact, string(RouteSchemaResponse), binding.RouteID, "GET", "POST",
		binding.ContractVersion, binding.Action, binding.SchemaID, binding.MediaType, 200, nil,
	); !errors.Is(err, ErrRouteSchemaCatalogInvalid) {
		t.Fatalf("unrelated actual method error=%v", err)
	}
}

func TestRouteSchemaCatalogCoversComposableAndGlobalMiddleware(t *testing.T) {
	options := defaultFixtureOptions("schema.middleware")
	options.method = "POST"
	options.requestSchema = options.extensionID + ".catalog.request@1"
	options.guard = extensionmanifest.GuardCoreLogin
	options.rateLimit = "actor.write@1"
	options.idempotency = "required.24h@1"
	fixture := buildFixture(t, options)
	fixture = rebuildFixtureManifest(t, fixture, func(manifest *extensionmanifest.Manifest) {
		for _, declaration := range []struct {
			suffix  string
			action  string
			methods []string
		}{
			{"before", extensionmanifest.RouteActionBefore, []string{"POST"}},
			{"after", extensionmanifest.RouteActionAfter, []string{"POST"}},
			{"filter", extensionmanifest.RouteActionFilter, []string{"POST"}},
			{"wrap", extensionmanifest.RouteActionWrap, []string{"POST"}},
			{"global", extensionmanifest.RouteActionGlobalMiddleware, nil},
		} {
			route := extensionmanifest.ManifestRoute{
				ID: options.extensionID + "." + declaration.suffix, ContractVersion: options.extensionID + "." + declaration.suffix + "@1",
				Action: declaration.action, TargetID: options.extensionID + ".catalog", Path: options.manifestPath,
				Methods: declaration.methods, Guard: extensionmanifest.GuardCoreInherit, Fallback: "closed",
				Mode: extensionmanifest.RouteModeHTTP, Handler: "route." + declaration.suffix,
				RequestSchema: "openapi/schemas/common.json", ResponseSchema: "openapi/schemas/common.json",
			}
			if declaration.action == extensionmanifest.RouteActionGlobalMiddleware {
				route.TargetID, route.Path, route.Guard = "", "", extensionmanifest.GuardCoreLogin
			}
			manifest.Routes = append(manifest.Routes, route)
		}
	})
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := catalog.Bindings()
	if len(bindings) != 12 {
		t.Fatalf("binding count=%d bindings=%#v", len(bindings), bindings)
	}
	artifact := routeSchemaFixtureArtifact(fixture)
	seenActions := make(map[string]bool)
	compiledByDigest := make(map[string]any)
	for _, binding := range bindings {
		seenActions[binding.Action] = true
		if binding.Action == extensionmanifest.RouteActionGlobalMiddleware && binding.Method != "*" {
			t.Fatalf("global method=%q", binding.Method)
		}
		if binding.OperationID == "" || binding.ContractVersion == "" {
			t.Fatalf("incomplete exact binding=%#v", binding)
		}
		if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, []byte(`{"id":"42"}`)); err != nil {
			t.Fatalf("validate %#v: %v", binding, err)
		}
		entry := catalog.entries[routeSchemaCatalogKey(artifact, binding)]
		if shared := compiledByDigest[binding.SchemaDigest]; shared == nil {
			compiledByDigest[binding.SchemaDigest] = entry.schema
		} else if shared != entry.schema {
			t.Fatal("identical schema digests were compiled more than once")
		}
	}
	for _, action := range []string{
		extensionmanifest.RouteActionBefore, extensionmanifest.RouteActionAfter, extensionmanifest.RouteActionFilter,
		extensionmanifest.RouteActionWrap, extensionmanifest.RouteActionGlobalMiddleware,
	} {
		if !seenActions[action] {
			t.Fatalf("missing action %s", action)
		}
	}
}

func TestRouteSchemaClosureBudgetCountsUniqueTransitiveReferences(t *testing.T) {
	aggregate := map[string]any{
		"schemas": map[string]any{
			"first":  map[string]any{"$ref": "#/schemas/shared"},
			"shared": map[string]any{"type": "string", "description": strings.Repeat("x", 128)},
		},
	}
	value := map[string]any{"$ref": "#/schemas/first"}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	candidate := routeSchemaCandidate{value: value, canonical: canonical}
	one, err := routeSchemaClosureBytes(aggregate, []routeSchemaCandidate{candidate}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	two, err := routeSchemaClosureBytes(aggregate, []routeSchemaCandidate{candidate, candidate}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("deduplicated closure one=%d two=%d", one, two)
	}
	if _, err := routeSchemaClosureBytes(aggregate, []routeSchemaCandidate{candidate}, one-1); !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("closure budget error=%v", err)
	}
}

func TestRouteSchemaComplexityBudgetsNodesDepthBranchesAndRefExpansion(t *testing.T) {
	refAggregate := map[string]any{
		"schemas": map[string]any{
			"first":  map[string]any{"$ref": "#/schemas/second"},
			"second": map[string]any{"type": "string"},
		},
	}
	tests := []struct {
		name      string
		aggregate map[string]any
		value     any
		limits    routeSchemaComplexityLimits
	}{
		{
			name: "nodes", aggregate: map[string]any{},
			value:  map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}},
			limits: routeSchemaComplexityLimits{nodes: 2, depth: 10, branches: 10, refExpansions: 10},
		},
		{
			name: "depth", aggregate: map[string]any{},
			value:  map[string]any{"properties": map[string]any{"a": map[string]any{"properties": map[string]any{"b": map[string]any{"type": "string"}}}}},
			limits: routeSchemaComplexityLimits{nodes: 100, depth: 2, branches: 10, refExpansions: 10},
		},
		{
			name: "branches", aggregate: map[string]any{},
			value:  map[string]any{"oneOf": []any{true, true, true}},
			limits: routeSchemaComplexityLimits{nodes: 100, depth: 10, branches: 2, refExpansions: 10},
		},
		{
			name: "ref expansion", aggregate: refAggregate,
			value:  map[string]any{"$ref": "#/schemas/first"},
			limits: routeSchemaComplexityLimits{nodes: 100, depth: 10, branches: 10, refExpansions: 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if err := enforceRouteSchemaComplexity(
				test.aggregate, []routeSchemaCandidate{{value: test.value, canonical: canonical}}, test.limits,
			); !errors.Is(err, ErrResourceBudget) {
				t.Fatalf("complexity error=%v", err)
			}
		})
	}
}

func TestRouteSchemaCatalogRejectsProductionBranchComplexity(t *testing.T) {
	options := defaultFixtureOptions("schema.complex")
	branches := make([]any, maxRouteSchemaBranches+1)
	for index := range branches {
		branches[index] = map[string]any{"type": "string", "const": strconv.Itoa(index)}
	}
	schema, err := json.Marshal(map[string]any{"Catalog": map[string]any{"oneOf": branches}})
	if err != nil {
		t.Fatal(err)
	}
	options.schema = string(schema)
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{buildFixture(t, options)}}); !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("complex schema error=%v", err)
	}
}

func TestDecodeRouteSchemaJSONRejectsNestedDuplicateKeysAndDepth(t *testing.T) {
	if _, err := decodeRouteSchemaJSON([]byte(`{"outer":{"id":1,"id":2}}`), 1024); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key error=%v", err)
	}
	deep := strings.Repeat("[", maxRoutePayloadDepth+2) + "0" + strings.Repeat("]", maxRoutePayloadDepth+2)
	if _, err := decodeRouteSchemaJSON([]byte(deep), len(deep)); !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("payload depth error=%v", err)
	}
}

func TestRouteSchemaValidationSlotsHonorCancellationAndHostTimeout(t *testing.T) {
	t.Run("cancel while waiting", func(t *testing.T) {
		slots := make(chan struct{}, 1)
		slots <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := validateRouteSchemaWithLimits(ctx, slots, time.Second, func(context.Context) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("error=%v called=%v", err, called)
		}
	})

	t.Run("timeout retains slot until validator exits", func(t *testing.T) {
		slots := make(chan struct{}, 1)
		blocked := make(chan struct{})
		finished := make(chan struct{})
		err := validateRouteSchemaWithLimits(context.Background(), slots, 20*time.Millisecond, func(context.Context) error {
			defer close(finished)
			<-blocked
			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || len(slots) != 1 {
			t.Fatalf("error=%v occupied=%d", err, len(slots))
		}
		close(blocked)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("validator did not exit")
		}
		deadline := time.Now().Add(time.Second)
		for len(slots) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(slots) != 0 {
			t.Fatal("validation slot was not released")
		}
	})
}

func TestRouteSchemaPayloadDecodeHonorsContextAndStructuralBudgets(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := decodeRouteSchemaJSONContext(canceled, []byte(`{"ok":true}`), 1024, 10, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decode error=%v", err)
	}
	if _, err := decodeRouteSchemaJSONContext(context.Background(), []byte(`[[0]]`), 1024, 2, 10); !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("node budget error=%v", err)
	}
	if _, err := decodeRouteSchemaJSONContext(context.Background(), []byte(`[0,1]`), 1024, 10, 1); !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("item budget error=%v", err)
	}

	fixture := buildFixture(t, defaultFixtureOptions("schema.decode-slot"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	catalog.validationSlots = make(chan struct{}, 1)
	catalog.validationSlots <- struct{}{}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	err = validateFixtureRouteSchema(
		catalog, ctx, routeSchemaFixtureArtifact(fixture), catalog.Bindings()[0], []byte(`not-json`),
	)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrRouteSchemaPayloadInvalid) {
		t.Fatalf("decode ran outside validation slot: %v", err)
	}
	<-catalog.validationSlots
}

func TestRouteSchemaCatalogConcurrentValidationIsImmutable(t *testing.T) {
	fixture := buildFixture(t, defaultFixtureOptions("schema.concurrent"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	binding := catalog.Bindings()[0]
	artifact := routeSchemaFixtureArtifact(fixture)
	var group sync.WaitGroup
	errorsSeen := make(chan error, 64)
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 50 {
				if err := validateFixtureRouteSchema(catalog, context.Background(), artifact, binding, []byte(`{"id":"42"}`)); err != nil {
					errorsSeen <- err
					return
				}
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
}

func TestRouteSchemaCatalogRejectsCrossArtifactDriftBeforeAggregation(t *testing.T) {
	firstOptions := defaultFixtureOptions("schema.drift")
	first := buildFixture(t, firstOptions)
	secondOptions := defaultFixtureOptions("schema.drift")
	secondOptions.schema = `{"Catalog":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}}`
	second := buildFixture(t, secondOptions)
	if first.PackageDigest == second.PackageDigest {
		t.Fatal("fixture digests did not drift")
	}
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{first, second}}); !errors.Is(err, ErrRouteSchemaArtifactMismatch) {
		t.Fatalf("artifact drift error=%v", err)
	}
}

func TestRouteSchemaCatalogPreservesStrictAggregateFailuresAndPayloadBudget(t *testing.T) {
	unsafe := defaultFixtureOptions("schema.unsafe")
	unsafe.document = strings.Replace(fixtureDocument(unsafe), "schemas/common.json#/Catalog", "https://evil.example/schema.json#/Catalog", 1)
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{buildFixture(t, unsafe)}}); !errors.Is(err, ErrUnsafeReference) {
		t.Fatalf("unsafe ref error=%v", err)
	}

	missing := defaultFixtureOptions("schema.missing")
	missing.document = strings.Replace(fixtureDocument(missing),
		"          content:\n            application/json:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n", "", 1)
	if _, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{buildFixture(t, missing)}}); !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("missing operation schema error=%v", err)
	}

	fixture := buildFixture(t, defaultFixtureOptions("schema.budget"))
	catalog, err := BuildRouteSchemaCatalog(BuildInput{Artifacts: []Artifact{fixture}})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, maxRoutePayloadBytes+1)
	if err := validateFixtureRouteSchema(catalog, context.Background(), routeSchemaFixtureArtifact(fixture), catalog.Bindings()[0], payload); !errors.Is(err, ErrRouteSchemaPayloadInvalid) || !errors.Is(err, ErrResourceBudget) {
		t.Fatalf("payload budget error=%v", err)
	}
}

func validateFixtureRouteSchema(
	catalog *RouteSchemaCatalog,
	ctx context.Context,
	artifact routes.PluginArtifact,
	binding RouteSchemaBinding,
	payload []byte,
) error {
	responseStatus := 0
	if binding.Direction == RouteSchemaResponse {
		switch {
		case binding.ResponseStatus == "default":
			responseStatus = 200
		case strings.HasSuffix(binding.ResponseStatus, "XX"):
			responseStatus = int(binding.ResponseStatus[0]-'0') * 100
		default:
			responseStatus, _ = strconv.Atoi(binding.ResponseStatus)
		}
	}
	return catalog.ValidateRouteSchema(
		ctx, artifact, string(binding.Direction), binding.RouteID, binding.Method,
		fixtureActualMethod(binding.Method), binding.ContractVersion, binding.Action, binding.SchemaID, binding.MediaType, responseStatus, payload,
	)
}

func fixtureActualMethod(bindingMethod string) string {
	if bindingMethod == "*" {
		return "GET"
	}
	return bindingMethod
}

func routeSchemaFixtureArtifact(fixture Artifact) routes.PluginArtifact {
	return routes.PluginArtifact{
		ExtensionID: fixture.ExtensionID, ExtensionVersion: fixture.Version,
		PackageDigest: fixture.PackageDigest, RuntimeInstanceID: "runtime-1",
	}
}
