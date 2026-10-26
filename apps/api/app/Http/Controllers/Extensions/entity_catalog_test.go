package extensionscontroller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
)

func TestPublicEntityCatalogEmptyWithoutRegistry(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	controller := NewController(nil, nil, nil)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/entity-catalog", nil)
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
	var envelope testEnvelope[entityregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != entityregistry.CatalogSchemaVersion {
		t.Fatalf("schema = %q", envelope.Data.SchemaVersion)
	}
	if len(envelope.Data.Entities) != 0 {
		t.Fatalf("expected empty entities, got %#v", envelope.Data.Entities)
	}
}

func TestPublicEntityCatalogProjectsPublishedEntities(t *testing.T) {
	t.Parallel()
	registry := entityregistry.New()
	// Reuse package-local demo shape via minimal Publish matching registry tests.
	digest := strings.Repeat("ab", 32)
	if _, err := registry.Publish(entityregistry.Publication{
		Artifact: entityregistry.Artifact{
			ExtensionID: "demo.catalog", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 7,
		},
		Entities: []entityregistry.Declaration{{
			ID: "demo.catalog.entity.product", ContractVersion: "demo.catalog.entity.product@1",
			Kind: entityregistry.KindEntity, Label: "产品", StorageKey: "demo.catalog.product",
			PermissionCreate:   "demo.catalog.product.create",
			PermissionRead:     "demo.catalog.product.read",
			PermissionUpdate:   "demo.catalog.product.update",
			PermissionDelete:   "demo.catalog.product.delete",
			PermissionImport:   "demo.catalog.product.import",
			PermissionExport:   "demo.catalog.product.export",
			ImportExportPolicy: entityregistry.ImportExportAllow,
			DeletionPolicy:     entityregistry.DeletionSoft,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	controller := NewController(nil, nil, nil).WithEntityRegistry(registry)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/entity-catalog", nil)
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
	if resp.Header.Get("X-SForum-Entity-Catalog-Digest") == "" {
		t.Fatal("expected entity catalog digest header")
	}
	var envelope testEnvelope[entityregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Entities) != 1 || envelope.Data.Entities[0].ID != "demo.catalog.entity.product" {
		t.Fatalf("entities = %#v", envelope.Data.Entities)
	}
}
