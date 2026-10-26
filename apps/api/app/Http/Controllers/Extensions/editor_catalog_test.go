package extensionscontroller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
)

func TestPublicEditorCatalogEmptyWithoutRegistry(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	controller := NewController(nil, nil, nil)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/editor-catalog", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope testEnvelope[editorregistry.Catalog]
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != editorregistry.CatalogSchemaVersion {
		t.Fatalf("schema = %q", envelope.Data.SchemaVersion)
	}
	if len(envelope.Data.Modules) != 0 {
		t.Fatalf("expected empty modules, got %#v", envelope.Data.Modules)
	}
	assertEditorCatalogArrays(t, body, 0)
}

func TestPublicEditorCatalogProjectsPublishedModules(t *testing.T) {
	t.Parallel()
	registry := editorregistry.New()
	packageDigest := strings.Repeat("ab", 32)
	moduleDigest := strings.Repeat("cd", 32)
	if _, err := registry.Publish(editorregistry.Publication{
		Artifact: editorregistry.Artifact{
			ExtensionID: "demo.editor", ExtensionVersion: "1.0.0",
			PackageDigest: packageDigest, VersionID: 1,
		},
		Editor: []editorregistry.Declaration{{
			ID: "demo.editor.node.vote", ContractVersion: "demo.editor.node.vote@1",
			Kind: editorregistry.KindNode, Schema: "demo.editor.vote@1", ExtensionName: "demoVote",
			L2Module: "frontend/editor/vote.mjs", L2Digest: moduleDigest,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	controller := NewController(nil, nil, nil).WithEditorRegistry(registry)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/editor-catalog", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-SForum-Editor-Catalog-Digest") == "" {
		t.Fatal("expected catalog digest header")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope testEnvelope[editorregistry.Catalog]
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Modules) != 1 {
		t.Fatalf("modules = %#v", envelope.Data.Modules)
	}
	module := envelope.Data.Modules[0]
	if module.ExtensionID != "demo.editor" ||
		!strings.Contains(module.AssetPath, packageDigest) ||
		module.L2Digest != moduleDigest {
		t.Fatalf("module = %#v", module)
	}
	assertEditorCatalogArrays(t, body, 1)
}

func assertEditorCatalogArrays(t *testing.T, body []byte, moduleCount int) {
	t.Helper()
	var envelope struct {
		Data struct {
			Modules  []map[string]json.RawMessage `json:"modules"`
			Toolbars json.RawMessage              `json:"toolbars"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Modules) != moduleCount || len(envelope.Data.Toolbars) == 0 || envelope.Data.Toolbars[0] != '[' {
		t.Fatalf("catalog arrays are not serialized canonically: %s", body)
	}
	for _, module := range envelope.Data.Modules {
		for _, field := range []string{"nodes", "marks", "commands", "toolbars"} {
			value := module[field]
			if len(value) == 0 || value[0] != '[' {
				t.Fatalf("module %s must be an array: %s", field, body)
			}
		}
	}
}
