package extensionscontroller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

func TestPublicContentCatalogEmptyWithoutRegistry(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	controller := NewController(nil, nil, nil)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/content-catalog", nil)
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
	var envelope testEnvelope[contentregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != contentregistry.CatalogSchemaVersion {
		t.Fatalf("schema = %q", envelope.Data.SchemaVersion)
	}
	if len(envelope.Data.Content) != 0 {
		t.Fatalf("expected empty content, got %#v", envelope.Data.Content)
	}
}

func TestPublicContentCatalogProjectsPublishedContent(t *testing.T) {
	t.Parallel()
	registry := contentregistry.New()
	digest := strings.Repeat("ab", 32)
	if _, err := registry.Publish(contentregistry.Publication{
		Artifact: contentregistry.Artifact{
			ExtensionID: "demo.content", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 3, RuntimeInstanceID: "demo.content-runtime",
		},
		Content: []contentregistry.Declaration{{
			ID: "demo.content.block.card", ContractVersion: "demo.content.block.card@1",
			Kind: contentregistry.KindBlock, Handler: "block", Schema: "demo.content.block.card.schema@1",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	controller := NewController(nil, nil, nil).WithContentRegistry(registry)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/content-catalog", nil)
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
	if resp.Header.Get("X-SForum-Content-Catalog-Digest") == "" {
		t.Fatal("expected content catalog digest header")
	}
	var envelope testEnvelope[contentregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Content) != 1 || envelope.Data.Content[0].ID != "demo.content.block.card" {
		t.Fatalf("content = %#v", envelope.Data.Content)
	}
	if envelope.Data.Content[0].ExtensionID != "demo.content" {
		t.Fatalf("extension = %#v", envelope.Data.Content[0])
	}
}
