package extensionscontroller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

const (
	assetInspectorTestDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	assetInspectorTestDigestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	assetInspectorTestDigestC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	// ImpactDigest 专用值：确保脱敏后响应绝不回显信任影响摘要。
	assetInspectorTestImpact = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestAssetInspectorAllowsExtensionViewAndRedactsSensitiveFields(t *testing.T) {
	app, registry := newAssetInspectorTestApp(t, true)
	viewer := loginAssetInspectorUser(t, app, 1)

	if _, err := registry.Publish(assetregistry.Publication{
		Artifact: assetregistry.Artifact{
			ExtensionID: "demo.assets", ExtensionVersion: "1.0.0",
			PackageDigest: assetInspectorTestDigestA, ImpactDigest: assetInspectorTestImpact,
			OwnerKind: assetregistry.OwnerKindPlugin,
		},
		Assets: []assetregistry.Declaration{
			{
				Handle: "demo.assets.entry", ContractVersion: "demo.assets.entry@1",
				Type: "script", Path: "public/entry.mjs", Digest: assetInspectorTestDigestC,
				Module: true, Loading: "lazy", Scope: []string{"forum.component.topic"},
				CSP: []string{"script-src 'self'"},
			},
			{
				Handle: "demo.assets.style", ContractVersion: "demo.assets.style@1",
				Type: "style", Path: "public/style.css", Digest: assetInspectorTestDigestB,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/asset-inspector", viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var envelope testEnvelope[AssetInspectorSnapshot]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	snapshot := envelope.Data
	if snapshot.SchemaVersion != assetInspectorSchemaVersion {
		t.Fatalf("schemaVersion=%q", snapshot.SchemaVersion)
	}
	if snapshot.Revision != registry.Revision() || snapshot.Digest == "" {
		t.Fatalf("revision/digest missing: %#v", snapshot)
	}
	if snapshot.PublicationCount != 1 || snapshot.AssetCount != 2 || len(snapshot.Publications) != 1 {
		t.Fatalf("counts publication=%d asset=%d listed=%d", snapshot.PublicationCount, snapshot.AssetCount, len(snapshot.Publications))
	}
	publication := snapshot.Publications[0]
	if publication.ExtensionID != "demo.assets" || publication.ExtensionVersion != "1.0.0" ||
		publication.PackageDigest != assetInspectorTestDigestA || publication.OwnerKind != assetregistry.OwnerKindPlugin {
		t.Fatalf("publication identity = %#v", publication)
	}
	if len(publication.Assets) != 2 {
		t.Fatalf("assets = %#v", publication.Assets)
	}
	entry := publication.Assets[0]
	if entry.Handle != "demo.assets.entry" || entry.Type != "script" || entry.Path != "public/entry.mjs" ||
		!entry.Module || entry.Loading != "lazy" || entry.Integrity == "" || len(entry.Scope) != 1 || len(entry.CSP) != 1 {
		t.Fatalf("entry handle = %#v", entry)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// ImpactDigest 与绝对路径不得出现在脱敏视图中。
	for _, forbidden := range []string{
		`"impactDigest"`, assetInspectorTestImpact,
		`"/var/`, `"/app/`, `"C:\\`, `"C:/`,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("asset inspector disclosed %q: %s", forbidden, encoded)
		}
	}
}

func TestAssetInspectorDeniesWithoutPermission(t *testing.T) {
	app, _ := newAssetInspectorTestApp(t, true)
	denied := loginAssetInspectorUser(t, app, 3)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/asset-inspector", denied)
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("denied status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	_ = response.Body.Close()
}

func TestAssetInspectorFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newAssetInspectorTestApp(t, false)
	viewer := loginAssetInspectorUser(t, app, 1)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/asset-inspector", viewer)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	_ = response.Body.Close()
}

func TestAssetInspectorRejectsInvalidLimit(t *testing.T) {
	app, _ := newAssetInspectorTestApp(t, true)
	viewer := loginAssetInspectorUser(t, app, 1)
	for _, value := range []string{"0", "-1", "201", "invalid"} {
		response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/asset-inspector?limit="+value, viewer)
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("limit=%s status=%d body=%s", value, response.StatusCode, responseBody(t, response))
		}
		_ = response.Body.Close()
	}
}

func TestAssetInspectorAppliesPublicationLimit(t *testing.T) {
	app, registry := newAssetInspectorTestApp(t, true)
	viewer := loginAssetInspectorUser(t, app, 1)
	for _, id := range []string{"alpha.assets", "beta.assets", "gamma.assets"} {
		if _, err := registry.Publish(assetregistry.Publication{
			Artifact: assetregistry.Artifact{
				ExtensionID: id, ExtensionVersion: "1.0.0",
				PackageDigest: assetInspectorTestDigestA, ImpactDigest: assetInspectorTestDigestA,
				OwnerKind: assetregistry.OwnerKindPlugin,
			},
			Assets: []assetregistry.Declaration{{
				Handle: id + ".style", ContractVersion: id + ".style@1",
				Type: "style", Path: "style.css", Digest: assetInspectorTestDigestB,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/asset-inspector?limit=1", viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var envelope testEnvelope[AssetInspectorSnapshot]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if envelope.Data.PublicationCount != 3 || envelope.Data.AssetCount != 3 {
		t.Fatalf("full counts lost: %#v", envelope.Data)
	}
	if len(envelope.Data.Publications) != 1 {
		t.Fatalf("limit not applied: listed=%d", len(envelope.Data.Publications))
	}
}

func TestPackageRelativePathOnlyRedactsAbsoluteAndTraversal(t *testing.T) {
	cases := map[string]string{
		"public/entry.mjs": "public/entry.mjs",
		"/etc/passwd":      "",
		"../secret":        "",
		"C:\\windows":      "",
		"":                 "",
	}
	for input, want := range cases {
		if got := packageRelativePathOnly(input); got != want {
			t.Fatalf("path %q: got %q want %q", input, got, want)
		}
	}
}

func newAssetInspectorTestApp(t *testing.T, configured bool) (*fiber.App, *assetregistry.Registry) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	var registry *assetregistry.Registry
	if configured {
		registry = assetregistry.New()
		controller.WithAssetInspector(registry)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/asset-inspector-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
	return app, registry
}

func loginAssetInspectorUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/asset-inspector-login/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login %d status=%d cookies=%d", userID, response.StatusCode, len(response.Cookies()))
	}
	return response.Cookies()[0]
}
