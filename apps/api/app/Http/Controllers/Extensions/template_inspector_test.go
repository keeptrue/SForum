package extensionscontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/config"
)

const (
	templateInspectorTestDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	templateInspectorTestDigestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	templateInspectorTestDigestC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestTemplateInspectorAllowsExtensionViewAndRedactsSensitiveFields(t *testing.T) {
	app, registry := newTemplateInspectorTestApp(t, true)
	viewer := loginTemplateInspectorUser(t, app, 1)

	stageTemplateInspectorFixtures(t, registry)

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/template-inspector", viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var envelope testEnvelope[TemplateInspectorSnapshot]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	snapshot := envelope.Data
	if snapshot.SchemaVersion != templateInspectorSchemaVersion {
		t.Fatalf("schemaVersion=%q", snapshot.SchemaVersion)
	}
	if snapshot.Revision == 0 || snapshot.SnapshotCount < 2 {
		t.Fatalf("snapshot counts missing: %#v", snapshot)
	}
	if snapshot.ActiveTheme != "demo.active-theme" || snapshot.DefaultTheme != "demo.default-theme" {
		t.Fatalf("active/default theme = %#v", snapshot)
	}
	if snapshot.OverrideCount < 1 {
		t.Fatalf("expected override count: %#v", snapshot)
	}
	if len(snapshot.Snapshots) < 2 {
		t.Fatalf("snapshots = %#v", snapshot.Snapshots)
	}

	var foundActive, foundPlugin bool
	for _, item := range snapshot.Snapshots {
		if item.ExtensionID == "demo.active-theme" {
			foundActive = true
			if item.Kind != "theme" || !item.Active || item.PackageDigest != templateInspectorTestDigestA {
				t.Fatalf("active theme item = %#v", item)
			}
			if len(item.ContributionIDs) == 0 {
				t.Fatalf("active theme missing contributions: %#v", item)
			}
			if len(item.OverrideTargets) == 0 {
				t.Fatalf("active theme missing override targets: %#v", item)
			}
		}
		if item.ExtensionID == "sforum.plugin-page-business-e2e" {
			foundPlugin = true
			if item.Kind != "plugin" || item.PackageDigest != templateInspectorTestDigestC {
				t.Fatalf("plugin item = %#v", item)
			}
		}
	}
	if !foundActive || !foundPlugin {
		t.Fatalf("expected active theme and plugin snapshots: %#v", snapshot.Snapshots)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// 绝对路径、模板正文、view-model 载荷不得出现在脱敏视图中。
	for _, forbidden := range []string{
		`"/var/`, `"/tmp/`, `"/app/`, `"C:\\`, `"C:/`,
		`"packageRoot"`, `"html"`, `"<html"`, `"loaderData"`,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("template inspector disclosed %q: %s", forbidden, encoded)
		}
	}
}

func TestTemplateInspectorDeniesWithoutPermission(t *testing.T) {
	app, _ := newTemplateInspectorTestApp(t, true)
	denied := loginTemplateInspectorUser(t, app, 3)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/template-inspector", denied)
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("denied status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	_ = response.Body.Close()
}

func TestTemplateInspectorFailsClosedWhenUnavailable(t *testing.T) {
	app, _ := newTemplateInspectorTestApp(t, false)
	viewer := loginTemplateInspectorUser(t, app, 1)
	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/template-inspector", viewer)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	_ = response.Body.Close()
}

func TestTemplateInspectorRejectsInvalidLimit(t *testing.T) {
	app, _ := newTemplateInspectorTestApp(t, true)
	viewer := loginTemplateInspectorUser(t, app, 1)
	for _, value := range []string{"0", "-1", "201", "invalid"} {
		response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/template-inspector?limit="+value, viewer)
		if response.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("limit=%s status=%d body=%s", value, response.StatusCode, responseBody(t, response))
		}
		_ = response.Body.Close()
	}
}

func TestTemplateInspectorAppliesSnapshotLimit(t *testing.T) {
	app, registry := newTemplateInspectorTestApp(t, true)
	viewer := loginTemplateInspectorUser(t, app, 1)
	stageTemplateInspectorFixtures(t, registry)

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/template-inspector?limit=1", viewer)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var envelope testEnvelope[TemplateInspectorSnapshot]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if envelope.Data.SnapshotCount < 2 {
		t.Fatalf("full counts lost: %#v", envelope.Data)
	}
	if len(envelope.Data.Snapshots) != 1 {
		t.Fatalf("limit not applied: listed=%d", len(envelope.Data.Snapshots))
	}
}

func stageTemplateInspectorFixtures(t *testing.T, registry *pages.ThemeRuntimeRegistry) {
	t.Helper()
	// 使用 durable 覆盖夹具，确保 OverrideTargets 与 plugin kind 真实入图。
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../"))
	pluginRoot := filepath.Join(repoRoot, "extensions/fixtures/plugins/sforum-plugin-page-business-e2e")
	themeRoot := filepath.Join(repoRoot, "extensions/fixtures/themes/sforum-plugin-override-e2e-theme")

	const (
		pluginID      = "sforum.plugin-page-business-e2e"
		schemaVersion = "sforum.plugin-page-business-e2e.article.data@1"
		overrideKey   = "sforum.plugin-page-business-e2e.article"
		templateID    = "sforum.plugin-page-business-e2e.template.article"
	)

	// 默认主题：仅 core home 贡献，用于 DefaultTheme 标记。
	defaultRoot := t.TempDir()
	writeTemplateInspectorFile(t, defaultRoot, "templates/home.html", `<main><sf-home-page></sf-home-page></main>`)
	defaultTheme, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: "demo.default-theme", ExtensionVersion: "1.0.0",
			PackageDigest: templateInspectorTestDigestB,
		},
		PackageRoot: defaultRoot,
		Contributions: []pages.PageContribution{{
			ID: "demo.default-theme.home", Action: pages.ActionReplace, Target: "forum.home",
			Template: "templates/home.html", Contract: "sforum.page.home@1",
			ExtensionID: "demo.default-theme", Version: "1.0.0", PackageDigest: templateInspectorTestDigestB,
		}},
		SiteName: "SForum",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(defaultTheme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SetDefaultExact(defaultTheme.Artifact()); err != nil {
		t.Fatal(err)
	}

	// 激活主题：core home + 对插件模板的 replace override。
	overridePath := "templates/plugins/sforum.plugin-page-business-e2e/article.html"
	activeTheme, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: "demo.active-theme", ExtensionVersion: "1.0.0",
			PackageDigest: templateInspectorTestDigestA,
		},
		PackageRoot: themeRoot,
		Contributions: []pages.PageContribution{{
			ID: "demo.active-theme.home", Action: pages.ActionReplace, Target: "forum.home",
			Template: "templates/home.html", Contract: "sforum.page.home@1",
			ExtensionID: "demo.active-theme", Version: "1.0.0", PackageDigest: templateInspectorTestDigestA,
		}},
		Templates: []pages.RuntimeTemplateDeclaration{
			{
				ID: "demo.active-theme.template.home", ContractVersion: "demo.active-theme.template.home@1",
				Action: "add", Path: "templates/home.html",
				Digest:          fixtureDigest(t, themeRoot, "templates/home.html"),
				ViewModelSchema: "sforum.page.home@1",
			},
			{
				ID: "demo.active-theme.template.article", ContractVersion: "demo.active-theme.template.article@1",
				Action: "replace", TargetID: templateID, Path: overridePath,
				Digest:          fixtureDigest(t, themeRoot, overridePath),
				ViewModelSchema: schemaVersion, ThemeOverrideKey: overrideKey,
			},
		},
		PackageKind: pages.RuntimeTemplateTheme, RequireDeclaredTemplates: true,
		SiteName: "SForum",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(activeTheme); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ActivateExact(activeTheme.Artifact()); err != nil {
		t.Fatal(err)
	}

	// 插件页面包：InspectSnapshot 的 kind=plugin。
	plugin, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: pluginID, ExtensionVersion: "1.0.0",
			PackageDigest: templateInspectorTestDigestC, RuntimeInstanceID: "runtime-template-inspector",
		},
		PackageRoot: pluginRoot,
		Contributions: []pages.PageContribution{{
			ID: pluginID + ".article", Action: pages.ActionAdd, Path: "/e2e-articles/:slug",
			Template: "templates/article.html", Contract: pluginID + ".page.article@1",
			Access: pages.AccessPublic, DataSource: "plugin", DataRoute: "/page-data/article",
			DataSchema: "schemas/article.json", ExtensionID: pluginID, Version: "1.0.0",
			PackageDigest: templateInspectorTestDigestC, RuntimeInstanceID: "runtime-template-inspector",
		}},
		Templates: []pages.RuntimeTemplateDeclaration{{
			ID: templateID, ContractVersion: templateID + "@1", Action: "add",
			Path: "templates/article.html", Digest: fixtureDigest(t, pluginRoot, "templates/article.html"),
			ViewModelSchema: schemaVersion, ThemeOverrideKey: overrideKey,
		}},
		DataSchemas: []pages.RuntimeDataSchemaDeclaration{{
			ID: "sforum.plugin-page-business-e2e.article.data", Version: "1",
			Path: "schemas/article.json", Digest: fixtureDigest(t, pluginRoot, "schemas/article.json"),
		}},
		PackageKind: pages.RuntimeTemplatePlugin, RequireDeclaredTemplates: true,
		SiteName: "SForum",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.Stage(plugin); err != nil {
		t.Fatal(err)
	}
	_ = pluginID
}

func fixtureDigest(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeTemplateInspectorFile(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTemplateInspectorTestApp(t *testing.T, configured bool) (*fiber.App, *pages.ThemeRuntimeRegistry) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		3: {ID: 3, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager)
	var registry *pages.ThemeRuntimeRegistry
	if configured {
		registry = pages.NewThemeRuntimeRegistry()
		controller.WithThemeRuntimeInspector(registry)
	}
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/template-inspector-login/:id", func(c fiber.Ctx) error {
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

func loginTemplateInspectorUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "/api/v1/template-inspector-login/"+strconv.FormatInt(userID, 10), nil)
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
