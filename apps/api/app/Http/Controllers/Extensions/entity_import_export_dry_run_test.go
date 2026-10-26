package extensionscontroller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestEntityImportExportDryRunRequiresLoginAndGatesPermission(t *testing.T) {
	t.Parallel()
	registry := entityregistry.New()
	digest := strings.Repeat("ab", 32)
	entityID := "demo.catalog.entity.product"
	if _, err := registry.Publish(entityregistry.Publication{
		Artifact: entityregistry.Artifact{
			ExtensionID: "demo.catalog", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 7,
		},
		Entities: []entityregistry.Declaration{{
			ID: entityID, ContractVersion: entityID + "@1",
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

	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		// extension.view + entity export：路由允许，export decision.allowed=true。
		1: {
			ID: 1, Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionView: true,
				"demo.catalog.product.export":    true,
			},
		},
		// extension.view 但无 entity export：HTTP 200 + decision.allowed=false。
		2: {
			ID: 2, Status: identity.UserStatusActive,
			Permissions: map[string]bool{
				identity.PermissionExtensionView: true,
				"demo.catalog.product.read":      true,
			},
		},
		// 仅有 entity export、无 extension.view：路由级 403。
		3: {
			ID: 3, Status: identity.UserStatusActive,
			Permissions: map[string]bool{"demo.catalog.product.export": true},
		},
	}}
	controller := NewController(nil, users, manager).WithEntityRegistry(registry)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			id := c.Params("id")
			userID := int64(1)
			switch id {
			case "2":
				userID = 2
			case "3":
				userID = 3
			}
			_, err := manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})

	path := "/api/v1/admin/extensions/entity-catalog/" + entityID + "/import-export-dry-run?action=export"
	anonymous := performExtensionRequest(t, app, http.MethodGet, path, nil)
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.StatusCode)
	}
	anonymous.Body.Close()

	// 无 extension.view：403，即使持有 entity export 键。
	noView := loginExtensionUser(t, app, manager, 3)
	noViewResp := performExtensionRequest(t, app, http.MethodGet, path, noView)
	if noViewResp.StatusCode != http.StatusForbidden {
		t.Fatalf("no extension.view status = %d body=%s", noViewResp.StatusCode, responseBody(t, noViewResp))
	}
	noViewResp.Body.Close()

	// extension.view + export：export dry-run Allowed=true，import Allowed=false。
	exporter := loginExtensionUser(t, app, manager, 1)
	exportResp := performExtensionRequest(t, app, http.MethodGet, path, exporter)
	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d body=%s", exportResp.StatusCode, responseBody(t, exportResp))
	}
	var exportEnvelope testEnvelope[entityregistry.ImportExportDryRun]
	if err := json.NewDecoder(exportResp.Body).Decode(&exportEnvelope); err != nil {
		t.Fatal(err)
	}
	exportResp.Body.Close()
	if !exportEnvelope.Data.DryRun || exportEnvelope.Data.Executes || !exportEnvelope.Data.Decision.Allowed {
		t.Fatalf("export dry-run = %#v", exportEnvelope.Data)
	}
	if !exportEnvelope.Data.Plan.CanExport {
		t.Fatalf("plan = %#v", exportEnvelope.Data.Plan)
	}

	importPath := "/api/v1/admin/extensions/entity-catalog/" + entityID + "/import-export-dry-run?action=import"
	importResp := performExtensionRequest(t, app, http.MethodGet, importPath, exporter)
	if importResp.StatusCode != http.StatusOK {
		t.Fatalf("import status = %d", importResp.StatusCode)
	}
	var importEnvelope testEnvelope[entityregistry.ImportExportDryRun]
	if err := json.NewDecoder(importResp.Body).Decode(&importEnvelope); err != nil {
		t.Fatal(err)
	}
	importResp.Body.Close()
	if importEnvelope.Data.Decision.Allowed || importEnvelope.Data.Decision.Reason != "permission_denied" {
		t.Fatalf("import deny = %#v", importEnvelope.Data.Decision)
	}

	// extension.view 但无 import/export 权限：export 亦 deny（200 + allowed=false）。
	reader := loginExtensionUser(t, app, manager, 2)
	readerResp := performExtensionRequest(t, app, http.MethodGet, path, reader)
	if readerResp.StatusCode != http.StatusOK {
		t.Fatalf("reader status = %d", readerResp.StatusCode)
	}
	var readerEnvelope testEnvelope[entityregistry.ImportExportDryRun]
	if err := json.NewDecoder(readerResp.Body).Decode(&readerEnvelope); err != nil {
		t.Fatal(err)
	}
	readerResp.Body.Close()
	if readerEnvelope.Data.Decision.Allowed {
		t.Fatalf("reader should be denied: %#v", readerEnvelope.Data)
	}

	missing := performExtensionRequest(t, app, http.MethodGet,
		"/api/v1/admin/extensions/entity-catalog/missing.entity/import-export-dry-run?action=export", exporter)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.StatusCode)
	}
	missing.Body.Close()
}
