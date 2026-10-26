package extensionopenapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

type fixtureOptions struct {
	extensionID         string
	path                string
	manifestPath        string
	method              string
	action              string
	targetID            string
	operationID         string
	namespace           string
	requestSchema       string
	responseSchema      string
	guard               string
	permission          string
	rateLimit           string
	idempotency         string
	requiredIdempotency bool
	omitPolicyMetadata  bool
	mode                string
	document            string
	schema              string
}

func defaultFixtureOptions(extensionID string) fixtureOptions {
	namespace := extensionID + ".api"
	return fixtureOptions{
		extensionID: extensionID, path: "/api/catalog/{id}", manifestPath: "/api/catalog/:id",
		method: "GET", action: extensionmanifest.RouteActionAdd,
		operationID: namespace + ".getCatalog", namespace: namespace,
		responseSchema: extensionID + ".catalog.response@1", guard: extensionmanifest.GuardCorePublic,
		rateLimit: "public.read@1", idempotency: "disabled",
		mode:   extensionmanifest.RouteModeHTTP,
		schema: `{"Catalog":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}`,
	}
}

func buildFixture(t *testing.T, options fixtureOptions) Artifact {
	t.Helper()
	if options.document == "" {
		options.document = fixtureDocument(options)
	}
	root := t.TempDir()
	writeFixtureFile(t, root, "openapi/routes.yaml", []byte(options.document))
	writeFixtureFile(t, root, "openapi/schemas/common.json", []byte(options.schema))

	routeID := options.extensionID + ".catalog"
	route := extensionmanifest.ManifestRoute{
		ID: routeID, ContractVersion: routeID + "@1", Action: options.action, TargetID: options.targetID,
		Path: options.manifestPath, Methods: []string{options.method}, Guard: options.guard,
		Permission: options.permission, Fallback: "closed", Mode: options.mode,
		Handler: "route.catalog", RequestSchema: options.requestSchema, ResponseSchema: options.responseSchema,
	}
	if options.action == extensionmanifest.RouteActionAlias || options.action == extensionmanifest.RouteActionRewrite {
		route.Handler = ""
		route.RequestSchema = ""
		route.ResponseSchema = ""
	}
	manifest := extensionmanifest.Manifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              options.extensionID, Name: "OpenAPI Fixture", Description: "Static aggregate fixture.",
		URL: "https://example.com/fixture", Author: extensionmanifest.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensionmanifest.TypePlugin, SForumVersion: ">=0.1.0",
		Routes:      []extensionmanifest.ManifestRoute{route},
		Permissions: nonEmptyValues(options.permission),
		OpenAPI: []extensionmanifest.ManifestOpenAPIFragment{{
			ID: options.extensionID + ".openapi", ContractVersion: options.extensionID + ".openapi@1",
			Path: "openapi/routes.yaml", Digest: digestBytes([]byte(options.document)), Namespace: options.namespace,
		}},
		PackageFiles: []extensionmanifest.ManifestPackageFile{
			{ID: options.extensionID + ".file.openapi", Kind: "openapi", Path: "openapi/routes.yaml", Digest: digestBytes([]byte(options.document))},
			{ID: options.extensionID + ".file.schema", Kind: "schema", Path: "openapi/schemas/common.json", Digest: digestBytes([]byte(options.schema))},
		},
	}
	// 声明式 raw 守卫必须带 entry + digest，且 entry 需要有匹配的 kind=executable
	// packageFiles 条目（v3_validate_ui.go 的规则）。
	// 声明式 raw 守卫必须带 entry + digest，且 entry 需要有匹配的 kind=executable
	// packageFiles 条目（v3_validate_ui.go 的规则）。守卫入口必须是**独立文件**：
	// packageFiles 的 path 必须唯一，不能复用 openapi/routes.yaml。
	if route.Guard == fixtureRawGuardID(options.extensionID) {
		guardEntry := "guard/entry.txt"
		guardBody := []byte("fixture raw_request guard entry\n")
		writeFixtureFile(t, root, guardEntry, guardBody)
		guardDigest := digestBytes(guardBody)
		manifest.Guards = []extensionmanifest.ManifestGuard{{
			ID:              fixtureRawGuardID(options.extensionID),
			ContractVersion: fixtureRawGuardID(options.extensionID) + "@1",
			Kind:            "raw_request", Entry: guardEntry, Digest: guardDigest,
		}}
		manifest.PackageFiles = append(manifest.PackageFiles,
			extensionmanifest.ManifestPackageFile{
				ID: options.extensionID + ".file.guard", Kind: "executable",
				Path: guardEntry, Digest: guardDigest,
			})
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, extensionmanifest.ManifestFileName, manifestBody)
	loaded, err := extensionmanifest.LoadPackage(root)
	if err != nil {
		t.Fatalf("load fixture manifest: %v\n%s", err, manifestBody)
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return Artifact{
		Root: root, ExtensionID: loaded.ID, Version: loaded.Version, PackageDigest: packageDigest, Manifest: loaded,
		Policies: []RoutePolicy{{
			RouteID: routeID, Method: options.method, RateLimit: options.rateLimit,
			Idempotency: options.idempotency, Security: fixtureSecurity(options.guard),
		}},
	}
}

func rebuildFixtureManifest(t *testing.T, artifact Artifact, mutate func(*extensionmanifest.Manifest)) Artifact {
	t.Helper()
	manifest := artifact.Manifest
	mutate(&manifest)
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, artifact.Root, extensionmanifest.ManifestFileName, body)
	loaded, err := extensionmanifest.LoadPackage(artifact.Root)
	if err != nil {
		t.Fatalf("reload fixture manifest: %v\n%s", err, body)
	}
	packageDigest, err := extensionpackage.DigestTree(artifact.Root)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Manifest = loaded
	artifact.PackageDigest = packageDigest
	return artifact
}

// fixtureRawGuardID 是 fixture 自带的 raw_request 守卫 id。
// 决定 R1（2026-10-16）：raw 请求权限必须由包内显式声明，Host 的
// core.guard.raw_request 已被拒绝，因此这里生成一个声明式守卫供测试使用。
func fixtureRawGuardID(extensionID string) string {
	return extensionID + ".guard.raw"
}

func fixtureSecurity(guard string) string {
	if guard == extensionmanifest.GuardCoreLogin || guard == extensionmanifest.GuardCorePermission {
		return SecurityAuthenticated
	}
	return SecurityPublic
}

func nonEmptyValues(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func fixtureDocument(options fixtureOptions) string {
	parameters := ""
	names := make([]string, 0)
	for name := range pathTemplateParameters(options.path) {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 || options.requiredIdempotency {
		parameters = "      parameters:\n"
		for _, name := range names {
			parameters += fmt.Sprintf("        - name: %s\n          in: path\n          required: true\n          schema:\n            type: string\n", name)
		}
		if options.requiredIdempotency {
			parameters += "        - name: Idempotency-Key\n          in: header\n          required: true\n          schema:\n            type: string\n            maxLength: 128\n"
		}
	}
	requestBody := ""
	if options.requestSchema != "" {
		requestBody = "      requestBody:\n        required: true\n        content:\n          application/json:\n            schema:\n              $ref: 'schemas/common.json#/Catalog'\n"
	}
	responseContent := ""
	if options.responseSchema != "" {
		responseContent = "          content:\n            application/json:\n              schema:\n                $ref: 'schemas/common.json#/Catalog'\n"
	}
	permission := ""
	if options.permission != "" {
		permission = fmt.Sprintf("      x-sforum-permission: %s\n", options.permission)
	}
	requestMetadata := ""
	if options.requestSchema != "" {
		requestMetadata = fmt.Sprintf("      x-sforum-request-schema: %s\n", options.requestSchema)
	}
	responseMetadata := ""
	if options.responseSchema != "" {
		responseMetadata = fmt.Sprintf("      x-sforum-response-schema: %s\n", options.responseSchema)
	}
	policyMetadata := fmt.Sprintf("      x-sforum-rate-limit: %s\n      x-sforum-idempotency: %s\n", options.rateLimit, options.idempotency)
	if options.omitPolicyMetadata {
		policyMetadata = ""
	}
	return fmt.Sprintf(`openapi: 3.1.0
info:
  title: Fixture
  version: 1.0.0
paths:
  %s:
    %s:
      operationId: %s
      x-sforum-route-id: %s.catalog
      x-sforum-contract-version: %s.catalog@1
      x-sforum-guard: %s
%s%s%s%s%s%s      responses:
        "200":
          description: ok
%s`, options.path, strings.ToLower(options.method), options.operationID,
		options.extensionID, options.extensionID, options.guard,
		permission, requestMetadata, responseMetadata, policyMetadata,
		parameters, requestBody, responseContent)
}

func writeFixtureFile(t *testing.T, root, name string, body []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digestBytes(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
