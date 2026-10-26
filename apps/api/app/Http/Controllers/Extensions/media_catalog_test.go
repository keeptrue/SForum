package extensionscontroller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"

	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
)

func TestPublicMediaCatalogEmptyWithoutRegistry(t *testing.T) {
	t.Parallel()
	app := fiber.New()
	controller := NewController(nil, nil, nil)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/media-catalog", nil)
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
	var envelope testEnvelope[mediaregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != mediaregistry.CatalogSchemaVersion {
		t.Fatalf("schema = %q", envelope.Data.SchemaVersion)
	}
	if len(envelope.Data.Policies) != 0 || len(envelope.Data.Processors) != 0 {
		t.Fatalf("expected empty media catalog, got %#v", envelope.Data)
	}
}

func TestPublicMediaCatalogProjectsPublishedMedia(t *testing.T) {
	t.Parallel()
	registry := mediaregistry.New()
	if _, err := registry.Publish(mediaregistry.Publication{
		Artifact: mediaregistry.Artifact{
			ExtensionID: "demo.media", ExtensionVersion: "1.0.0",
			PackageDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ImpactDigest:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			VersionID:     1, RuntimeInstanceID: "demo.media-runtime",
		},
		Policies: []mediaregistry.MIMEPolicyDeclaration{{
			ID: "demo.media.policy", ContractVersion: "demo.media.policy@1",
			Purpose: "general", RequiredPermission: "attachment.upload",
			AllowedMIMEs: []string{"image/png"}, AllowedExtensions: []string{"png"},
			StrictDeclaredMIME: true, Budget: mediaregistry.DefaultBudget(),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	controller := NewController(nil, nil, nil).WithMediaRegistry(registry)
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	req, err := http.NewRequest(http.MethodGet, "/api/v1/extensions/runtime/media-catalog", nil)
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
	if resp.Header.Get("X-SForum-Media-Catalog-Digest") == "" {
		t.Fatal("expected media catalog digest header")
	}
	var envelope testEnvelope[mediaregistry.Catalog]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Policies) != 1 || envelope.Data.Policies[0].ID != "demo.media.policy" {
		t.Fatalf("policies = %#v", envelope.Data.Policies)
	}
	if envelope.Data.Policies[0].ExtensionID != "demo.media" {
		t.Fatalf("extension = %#v", envelope.Data.Policies[0])
	}
}
