package pagescontroller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	pageviewmodels "github.com/keeptrue/sforum/apps/api/app/Models/PageViewModels"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/config"
)

type pagesEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type pagesActors struct {
	actors map[int64]identity.Actor
}

func (s pagesActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	a, ok := s.actors[userID]
	if !ok {
		return identity.Actor{}, identity.ErrUserNotFound
	}
	return a, nil
}

func (s pagesActors) GetCurrentUser(_ context.Context, userID int64) (identity.CurrentUser, error) {
	actor, ok := s.actors[userID]
	if !ok {
		return identity.CurrentUser{}, identity.ErrUserNotFound
	}
	return identity.CurrentUser{
		ID: actor.ID, Username: "member", DisplayName: "Member", Status: actor.Status,
		RoleKeys: append([]string(nil), actor.RoleKeys...),
	}, nil
}

type pagesThemeStore struct {
	active extensions.Extension
	items  map[string]extensions.Extension
	gets   int
}

func (s *pagesThemeStore) Get(_ context.Context, id string) (extensions.Extension, error) {
	s.gets++
	if e, ok := s.items[id]; ok {
		return e, nil
	}
	return extensions.Extension{}, extensions.ErrExtensionNotFound
}

func (s *pagesThemeStore) ActiveTheme(context.Context) (extensions.Extension, error) {
	if s.active.ID == "" {
		return extensions.Extension{}, extensions.ErrExtensionNotFound
	}
	return s.active, nil
}

type auditSink struct {
	events []audit.Event
}

func (a *auditSink) Append(_ context.Context, e audit.Event) error {
	a.events = append(a.events, e)
	return nil
}

type fakeRouteTargets struct {
	bases map[string]string
}

type exactRouteTargets struct {
	want   pages.RuntimeArtifact
	base   string
	called int
}

func (f *exactRouteTargets) AcquireRouteTarget(ctx context.Context, artifact pages.RuntimeArtifact) (pages.LoaderRouteTarget, bool) {
	f.called++
	if artifact != f.want {
		return pages.LoaderRouteTarget{}, false
	}
	return pages.LoaderRouteTarget{BaseURL: f.base, Context: ctx, Release: func() {}}, true
}

func (f fakeRouteTargets) AcquireRouteTarget(ctx context.Context, artifact pages.RuntimeArtifact) (pages.LoaderRouteTarget, bool) {
	b, ok := f.bases[artifact.ExtensionID]
	return pages.LoaderRouteTarget{BaseURL: b, Context: ctx, Release: func() {}}, ok
}

func fixtureDemoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	// Controllers/Pages → Controllers → Http → app → api → apps → repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../"))
	return filepath.Join(root, "extensions/fixtures/plugins/page-registry-demo")
}

// fixtureComponentRoot 指向 view=component 公开页的参照包。
func fixtureComponentRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../"))
	return filepath.Join(root, "extensions/fixtures/plugins/sforum-page-component-reference")
}

type pagesRouteProvider func(api fiber.Router)

func (f pagesRouteProvider) RegisterRoutes(api fiber.Router) { f(api) }

type roundTripFunc func(*nethttp.Request) (*nethttp.Response, error)

func (f roundTripFunc) Do(_ context.Context, req *nethttp.Request) (*nethttp.Response, error) {
	return f(req)
}

func newPagesTestApp(t *testing.T) (*fiber.App, *pages.Registry, *pagesThemeStore, *auditSink) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := pagesActors{actors: map[int64]identity.Actor{
		1: {
			ID: 1, Status: identity.UserStatusActive,
			RoleKeys:    []string{identity.RoleSuperAdmin},
			Permissions: map[string]bool{identity.PermissionExtensionView: true, identity.PermissionExtensionManage: true},
		},
		2: {
			ID: 2, Status: identity.UserStatusActive,
			RoleKeys:    []string{identity.RoleMember},
			Permissions: map[string]bool{},
		},
		3: {
			ID: 3, Status: identity.UserStatusActive,
			RoleKeys:    []string{identity.RoleModerator},
			Permissions: map[string]bool{identity.PermissionModerationReview: true},
		},
		4: {
			ID: 4, Status: identity.UserStatusActive,
			RoleKeys:    []string{identity.RoleMember},
			Permissions: map[string]bool{identity.PermissionExtensionView: true},
		},
	}}
	store := pages.NewMemoryStore()
	reg := pages.NewRegistry(store)
	demoRoot := fixtureDemoRoot(t)
	themeStore := &pagesThemeStore{
		items: map[string]extensions.Extension{
			"sforum.page-registry-demo": {
				ID: "sforum.page-registry-demo", Type: extensions.TypePlugin,
				Version: "1.0.0", PackageDigest: "digest-v1", PackagePath: demoRoot,
				Status: extensions.StatusEnabled,
			},
			"demo.theme": {
				ID: "demo.theme", Type: extensions.TypeTheme,
				Version: "1.0.0", PackageDigest: "theme-d", PackagePath: demoRoot,
				Status: extensions.StatusEnabled,
			},
		},
		active: extensions.Extension{
			ID: "demo.theme", Type: extensions.TypeTheme,
			Version: "1.0.0", PackageDigest: "theme-d", PackagePath: demoRoot,
		},
	}
	bridge := pages.NewExtensionBridge(reg)
	_ = bridge.RegisterPluginPackage(context.Background(), pages.ThemeExtension{
		ID: "sforum.page-registry-demo", Version: "1.0.0",
		PackagePath: demoRoot, PackageDigest: "digest-v1",
	})
	// view=component 公开页的参照包（与模板页同包对照）。
	componentRoot := fixtureComponentRoot(t)
	themeStore.items["sforum.page-component-reference"] = extensions.Extension{
		ID: "sforum.page-component-reference", Type: extensions.TypePlugin,
		Version: "1.0.0", PackageDigest: "digest-component-v1", PackagePath: componentRoot,
		Status: extensions.StatusEnabled,
	}
	_ = bridge.RegisterPluginPackage(context.Background(), pages.ThemeExtension{
		ID: "sforum.page-component-reference", Version: "1.0.0",
		PackagePath: componentRoot, PackageDigest: "digest-component-v1",
	})

	loader := pages.NewPageDataLoader(roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{
			StatusCode: 200,
			Header:     nethttp.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"title":"hello","slug":"` + r.URL.Query().Get("param.slug") + `"}`)),
		}, nil
	}))
	gw := pages.NewLoaderGateway(loader, fakeRouteTargets{
		bases: map[string]string{"sforum.page-registry-demo": "http://127.0.0.1:19999"},
	})

	sink := &auditSink{}
	ctrl := NewControllerWithThemes(reg, users, manager, themeStore).
		WithAuditor(sink).
		WithLoader(gw)

	loginProvider := pagesRouteProvider(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			id := c.Params("id")
			var userID int64
			for _, ch := range id {
				userID = userID*10 + int64(ch-'0')
			}
			_, err := manager.Start(c, userID)
			return err
		})
	})
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{loginProvider, pagesRouteProvider(func(api fiber.Router) {
			ctrl.RegisterRoutes(api)
		})},
	})
	return app, reg, themeStore, sink
}

func performPages(t *testing.T, app *fiber.App, method, path string, body []byte, cookie *nethttp.Cookie) *nethttp.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// 读完 body 缓冲，避免 app.Test 连接复用时污染后续请求
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp
}

func loginPagesUser(t *testing.T, app *fiber.App, userID int64) *nethttp.Cookie {
	t.Helper()
	resp := performPages(t, app, nethttp.MethodPost, "/api/v1/test-login/"+itoa(userID), nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		return c
	}
	t.Fatal("no session cookie")
	return nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestResolveCore(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.home", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Data["provider"] != "core" || body.Data["fallback"] != false ||
		body.Data["reason"] != resolveReasonAuthoritativeCore ||
		body.Data["selectedProvider"] != pages.ProviderCore {
		t.Fatalf("%#v", body.Data)
	}
}

func TestResolveCoreBindsOnlyMatchingRequestPath(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.category.show&path=/c/support", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("matching path status %d", resp.StatusCode)
	}
	var body pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&body)
	params, ok := body.Data["routeParams"].(map[string]any)
	if !ok || params["categorySlug"] != "support" {
		t.Fatalf("route params %#v", body.Data["routeParams"])
	}

	rejected := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.home&path=/u/alice", nil, nil)
	if rejected.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("mismatched path status %d", rejected.StatusCode)
	}

	for _, pageID := range []string{
		"system.forbidden",
		"system.not_found",
		"system.rate_limited",
		"system.server_error",
	} {
		response := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id="+pageID+"&path=/current/error/path", nil, nil)
		if response.StatusCode != nethttp.StatusOK {
			t.Fatalf("%s virtual path status %d", pageID, response.StatusCode)
		}
	}
}

func TestParseViewModelQueryBoundsNestedPageFilters(t *testing.T) {
	query, err := parseViewModelQuery("page=2&q=hello+world&tag=go&tag=vue")
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("page") != "2" || query.Get("q") != "hello world" || len(query["tag"]) != 2 {
		t.Fatalf("parsed query %#v", query)
	}
	if _, err := parseViewModelQuery("broken=%zz"); err == nil {
		t.Fatal("malformed nested query must fail closed")
	}
	if _, err := parseViewModelQuery(strings.Repeat("x", 4097)); err == nil {
		t.Fatal("oversized nested query must fail closed")
	}
}

func TestResolveCompiledThemeAvoidsPackageStoreAndFailsClosedOnStaleArtifact(t *testing.T) {
	for _, test := range []struct {
		name           string
		registryDigest string
		template       string
		wantProvider   string
		wantOutput     bool
		wantSource     string
		wantFallback   bool
		wantReason     string
	}{
		{name: "exact snapshot", registryDigest: strings.Repeat("a", 64), template: `<main>compiled home</main><sf-home-page></sf-home-page>`, wantProvider: "compiled.theme", wantOutput: true, wantSource: pages.ThemeRenderSourceActiveTheme},
		{name: "runtime emergency fallback", registryDigest: strings.Repeat("a", 64), template: `<main>{{asset "missing"}}</main><sf-home-page></sf-home-page>`, wantProvider: pages.ProviderCore, wantOutput: true, wantSource: pages.ThemeRenderSourceEmergency, wantFallback: true, wantReason: resolveReasonRenderFailed},
		{name: "stale registry artifact", registryDigest: strings.Repeat("b", 64), template: `<main>compiled home</main><sf-home-page></sf-home-page>`, wantProvider: pages.ProviderCore, wantReason: resolveReasonArtifactMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"pages":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "templates/home.html"), []byte(test.template), 0o600); err != nil {
				t.Fatal(err)
			}
			artifact := pages.RuntimeArtifact{
				ExtensionID: "compiled.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
			}
			compiledContribution := pages.PageContribution{
				ID: "compiled.home", Action: pages.ActionReplace, Target: "forum.home",
				Template: "templates/home.html", Contract: "sforum.page.home@1",
				ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
			}
			snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
				Artifact: artifact, PackageRoot: root, Contributions: []pages.PageContribution{compiledContribution},
				SiteName: "SForum", Locales: []string{"zh-CN"},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtimeRegistry := pages.NewThemeRuntimeRegistry()
			runtimeRegistry.Publish(snapshot)

			registered := compiledContribution
			registered.PackageDigest = test.registryDigest
			registry := pages.NewRegistry(pages.NewMemoryStore())
			if err := registry.RegisterContributions(artifact.ExtensionID, []pages.PageContribution{registered}); err != nil {
				t.Fatal(err)
			}
			if err := registry.ApproveReplace(context.Background(), pages.ProviderBinding{
				PageID: "forum.home", ExtensionID: artifact.ExtensionID, ContributionID: registered.ID,
				Version: registered.Version, PackageDigest: registered.PackageDigest,
				ContractVersion: registered.Contract, ApprovedBy: 1,
			}); err != nil {
				t.Fatal(err)
			}

			manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
			themeStore := &pagesThemeStore{items: map[string]extensions.Extension{
				artifact.ExtensionID: {ID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: test.registryDigest, PackagePath: filepath.Join(root, "missing")},
			}}
			controller := NewControllerWithThemes(registry, pagesActors{actors: map[int64]identity.Actor{}}, manager, themeStore).
				WithThemeRuntime(runtimeRegistry)
			cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}
			app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
				pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
			}})
			response := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.home", nil, nil)
			if response.StatusCode != nethttp.StatusOK {
				t.Fatalf("status=%d", response.StatusCode)
			}
			var envelope pagesEnvelope[resolveResponse]
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.Provider != test.wantProvider || (envelope.Data.RenderOutput != nil) != test.wantOutput {
				t.Fatalf("response=%#v", envelope.Data)
			}
			if envelope.Data.SelectedProvider != artifact.ExtensionID ||
				envelope.Data.SelectedPackageDigest != test.registryDigest ||
				envelope.Data.SelectedContributionID != registered.ID {
				t.Fatalf("selected artifact lost: %#v", envelope.Data)
			}
			if envelope.Data.Reason != test.wantReason {
				t.Fatalf("reason=%q want %q response=%#v", envelope.Data.Reason, test.wantReason, envelope.Data)
			}
			if test.wantOutput && (envelope.Data.RenderOutput.Source != test.wantSource || envelope.Data.Fallback != test.wantFallback) {
				t.Fatalf("fallback response=%#v", envelope.Data)
			}
			if themeStore.gets != 0 {
				t.Fatalf("request performed %d package store lookups", themeStore.gets)
			}
		})
	}
}

func TestResolveSystemErrorsUsesSelectedThemeRuntime(t *testing.T) {
	root := t.TempDir()
	artifact := pages.RuntimeArtifact{
		ExtensionID: "system-errors.theme", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("4", 64),
	}
	targets := []struct {
		pageID   string
		contract string
		template string
	}{
		{"system.forbidden", "sforum.page.forbidden@1", "templates/forbidden.html"},
		{"system.not_found", "sforum.page.not_found@1", "templates/not-found.html"},
		{"system.rate_limited", "sforum.page.rate_limited@1", "templates/rate-limited.html"},
		{"system.server_error", "sforum.page.server_error@1", "templates/server-error.html"},
	}
	contributions := make([]pages.PageContribution, 0, len(targets))
	for _, target := range targets {
		writeControllerFixtureFile(t, root, target.template, `<main data-theme-owned="presentation"><sf-error-details></sf-error-details><sf-error-actions></sf-error-actions></main>`)
		contributions = append(contributions, pages.PageContribution{
			ID:     artifact.ExtensionID + "." + strings.TrimPrefix(target.pageID, "system."),
			Action: pages.ActionReplace, Target: target.pageID, Template: target.template,
			Contract: target.contract, ExtensionID: artifact.ExtensionID,
			Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
		})
	}
	snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: contributions,
		SiteName: "SForum", Locales: []string{"zh-CN", "en-US"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	runtimeRegistry.Publish(snapshot)
	registry := pages.NewRegistry(pages.NewMemoryStore())
	if err := registry.ActivateThemeContributions(context.Background(), artifact.ExtensionID, contributions, "", 1); err != nil {
		t.Fatal(err)
	}
	controller := NewControllerWithThemes(
		registry,
		pagesActors{actors: map[int64]identity.Actor{}},
		authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"}),
		&pagesThemeStore{},
	).WithThemeRuntime(runtimeRegistry)
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
	}})
	for _, target := range targets {
		response := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id="+target.pageID+"&path=/private/current-error", nil, nil)
		if response.StatusCode != nethttp.StatusOK {
			t.Fatalf("%s status=%d", target.pageID, response.StatusCode)
		}
		var envelope pagesEnvelope[resolveResponse]
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.Provider != artifact.ExtensionID || envelope.Data.Fallback || envelope.Data.RenderOutput == nil ||
			envelope.Data.RenderOutput.Source != pages.ThemeRenderSourceActiveTheme ||
			envelope.Data.Page.ID != target.pageID || envelope.Data.Contract != target.contract {
			t.Fatalf("%s response=%#v", target.pageID, envelope.Data)
		}
		var hasDetails, hasActions bool
		for _, island := range envelope.Data.RenderOutput.Islands {
			hasDetails = hasDetails || island.ComponentID == "system.component.error_details"
			hasActions = hasActions || island.ComponentID == "system.component.error_actions"
			if island.ComponentID == "core.component.shared.sfextension_widget" {
				t.Fatalf("%s rendered public L2 island: %#v", target.pageID, envelope.Data.RenderOutput.Islands)
			}
		}
		if !hasDetails || !hasActions {
			t.Fatalf("%s islands=%#v", target.pageID, envelope.Data.RenderOutput.Islands)
		}
	}
}

type topicReplyViewOptions struct{}

func (topicReplyViewOptions) WebOption(_ context.Context, name string) (string, error) {
	switch name {
	case options.NameSiteName:
		return "SForum", nil
	case options.NameSiteURL:
		return "https://forum.example", nil
	default:
		return "", errors.New("unexpected option " + name)
	}
}

func (topicReplyViewOptions) IsFeatureEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func (topicReplyViewOptions) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	return "public", "author_and_staff", 1, true
}

type topicReplyChrome struct{}

func (topicReplyChrome) ResolvePublicNavigation(context.Context, identity.Actor, string, []string) (sitechrome.ResolvedNavigation, error) {
	return sitechrome.ResolvedNavigation{
		SchemaVersion: sitechrome.NavigationDocumentSchemaVersion,
		Revision:      1,
		Locations: []sitechrome.ResolvedNavigationLocation{{
			Location:  sitechrome.NavigationLocationTopbar,
			Supported: true,
			Items: []sitechrome.ResolvedNavigationItem{{
				SourceKey: "core.home", SourceKind: sitechrome.NavigationSourceCore,
				LinkKind: sitechrome.NavigationLinkCoreRoute, Label: "Home", Href: "/",
			}},
		}},
	}, nil
}

func (topicReplyChrome) ListPublicAnnouncements(context.Context) ([]sitechrome.Announcement, error) {
	return nil, nil
}

func TestResolveTopicReplyUsesSelectedThemeAndRequiresLogin(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join(fixtureDemoRoot(t), "..", "..", "..", ".."))
	for _, test := range []struct {
		name string
		id   string
		root string
	}{
		{name: "default", id: "sforum.default-theme", root: filepath.Join(repoRoot, "extensions/builtin/themes/sforum-default")},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := pages.RuntimeArtifact{ExtensionID: test.id, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64)}
			contribution := pages.PageContribution{
				ID: test.id + ".topic-reply", Action: pages.ActionReplace, Target: "forum.topic.reply",
				Template: "templates/topic-reply.html", Contract: "sforum.page.topic_reply@1",
				ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
			}
			snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
				Artifact: artifact, PackageRoot: test.root, Contributions: []pages.PageContribution{contribution},
				SiteName: "SForum", Locales: []string{"zh-CN"},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtimeRegistry := pages.NewThemeRuntimeRegistry()
			runtimeRegistry.Publish(snapshot)
			registry := pages.NewRegistry(pages.NewMemoryStore())
			if err := registry.RegisterContributions(artifact.ExtensionID, []pages.PageContribution{contribution}); err != nil {
				t.Fatal(err)
			}
			if err := registry.ApproveReplace(context.Background(), pages.ProviderBinding{
				PageID: "forum.topic.reply", ExtensionID: artifact.ExtensionID, ContributionID: contribution.ID,
				Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest, ContractVersion: contribution.Contract, ApprovedBy: 1,
			}); err != nil {
				t.Fatal(err)
			}
			manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
			users := pagesActors{actors: map[int64]identity.Actor{2: {
				ID: 2, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleMember}, Permissions: map[string]bool{},
			}}}
			controller := NewControllerWithThemes(registry, users, manager, &pagesThemeStore{}).
				WithThemeRuntime(runtimeRegistry).
				WithCorePageViewModels(pageviewmodels.NewCorePageViewModelSource(pageviewmodels.CorePageViewModelDependencies{
					Options: topicReplyViewOptions{}, SiteChrome: topicReplyChrome{},
				}))
			app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}, slog.Default(), apphttp.Dependencies{
				RouteProviders: []apphttp.RouteProvider{
					pagesRouteProvider(func(api fiber.Router) {
						api.Post("/test-login", func(c fiber.Ctx) error { _, err := manager.Start(c, 2); return err })
					}),
					pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
				},
			})

			path := "/api/v1/pages/resolve?id=forum.topic.reply&path=/topics/reply&query=topic%3Dinvalid"
			if response := performPages(t, app, nethttp.MethodGet, path, nil, nil); response.StatusCode != nethttp.StatusUnauthorized {
				t.Fatalf("anonymous reply resolve status=%d", response.StatusCode)
			}
			login := performPages(t, app, nethttp.MethodPost, "/api/v1/test-login", nil, nil)
			if login.StatusCode != nethttp.StatusOK {
				t.Fatalf("login status=%d", login.StatusCode)
			}
			response := performPages(t, app, nethttp.MethodGet, path, nil, login.Cookies()[0])
			if response.StatusCode != nethttp.StatusOK {
				t.Fatalf("authenticated reply resolve status=%d", response.StatusCode)
			}
			var envelope pagesEnvelope[resolveResponse]
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data.Provider != artifact.ExtensionID || envelope.Data.Fallback || envelope.Data.RenderOutput == nil {
				t.Fatalf("selected reply theme response=%#v", envelope.Data)
			}
			foundReplyIsland := false
			for _, island := range envelope.Data.RenderOutput.Islands {
				foundReplyIsland = foundReplyIsland || island.ComponentID == "forum.component.topic_reply"
			}
			if !foundReplyIsland {
				t.Fatalf("reply island=%#v", envelope.Data.RenderOutput.Islands)
			}
		})
	}
}

func TestResolvePathValidatesExactPluginDataBeforeCompiledRender(t *testing.T) {
	root := t.TempDir()
	writeControllerFixtureFile(t, root, "theme.json", `{"pages":[]}`)
	templateBody := `<article data-contract="exact">{{.title}} / {{.state}}</article>`
	schemaBody := `{"type":"object","required":["title","state"],"additionalProperties":false,"properties":{"title":{"type":"string"},"state":{"const":"published"}}}`
	writeControllerFixtureFile(t, root, "templates/article.html", templateBody)
	writeControllerFixtureFile(t, root, "schemas/article.json", schemaBody)
	templateDigest := sha256.Sum256([]byte(templateBody))
	schemaDigest := sha256.Sum256([]byte(schemaBody))
	artifact := pages.RuntimeArtifact{
		ExtensionID: "plugin.exact-page", ExtensionVersion: "2.0.0", PackageDigest: strings.Repeat("e", 64),
		RuntimeInstanceID: "runtime-exact-page",
	}
	contribution := pages.PageContribution{
		ID: "plugin.exact-page.article", Action: pages.ActionAdd, Path: "/exact-articles/:slug",
		Template: "templates/article.html", Contract: "plugin.exact-page.page.article@1", Access: pages.AccessPublic,
		DataSource: "plugin", DataRoute: "/page-data/article", DataSchema: "schemas/article.json",
		ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion,
		PackageDigest: artifact.PackageDigest, RuntimeInstanceID: artifact.RuntimeInstanceID,
	}
	snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []pages.PageContribution{contribution},
		Templates: []pages.RuntimeTemplateDeclaration{{
			ID: "plugin.exact-page.template.article", ContractVersion: "plugin.exact-page.template.article@1",
			Action: "add", Path: "templates/article.html", Digest: hex.EncodeToString(templateDigest[:]),
			ViewModelSchema: "plugin.exact-page.article.data@1", ThemeOverrideKey: "plugin.exact-page.article",
		}},
		DataSchemas: []pages.RuntimeDataSchemaDeclaration{{
			ID: "plugin.exact-page.article.data", Version: "1", Path: "schemas/article.json",
			Digest: hex.EncodeToString(schemaDigest[:]),
		}},
		PackageKind: pages.RuntimeTemplatePlugin, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	if _, _, err := runtimeRegistry.Stage(snapshot); err != nil {
		t.Fatal(err)
	}
	registry := pages.NewRegistry(pages.NewMemoryStore())
	if _, err := registry.PublishExtensionIfRevision(artifact, []pages.PageContribution{contribution}, 0); err != nil {
		t.Fatal(err)
	}
	targets := &exactRouteTargets{want: artifact, base: "http://127.0.0.1:19999"}
	payload := `{"title":"Business truth","state":"published"}`
	loader := pages.NewPageDataLoader(roundTripFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{
			StatusCode: 200, Header: nethttp.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(payload)),
		}, nil
	}))
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	themeStore := &pagesThemeStore{items: map[string]extensions.Extension{}}
	controller := NewControllerWithThemes(registry, pagesActors{actors: map[int64]identity.Actor{}}, manager, themeStore).
		WithThemeRuntime(runtimeRegistry).
		WithLoader(pages.NewLoaderGateway(loader, targets))
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
	}})
	response := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/exact-articles/one", nil, nil)
	if response.StatusCode != nethttp.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var envelope pagesEnvelope[resolveResponse]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Provider != artifact.ExtensionID || envelope.Data.Fallback || envelope.Data.RenderOutput == nil ||
		envelope.Data.RenderOutput.Source != pages.ThemeRenderSourcePlugin ||
		!strings.Contains(strings.Join(envelope.Data.RenderOutput.HTMLSegments, ""), "Business truth / published") ||
		targets.called != 1 || themeStore.gets != 0 {
		t.Fatalf("response=%#v targets=%d packageGets=%d", envelope.Data, targets.called, themeStore.gets)
	}
	loaded, ok := envelope.Data.LoaderData.(map[string]any)
	if !ok || loaded["title"] != "Business truth" || loaded["state"] != "published" {
		t.Fatalf("loader data=%#v", envelope.Data.LoaderData)
	}

	payload = `{"title":"Business truth","state":"published","themeMutation":true}`
	rejected := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/exact-articles/two", nil, nil)
	if rejected.StatusCode != nethttp.StatusOK {
		t.Fatalf("rejected status=%d", rejected.StatusCode)
	}
	var rejectedEnvelope pagesEnvelope[resolveResponse]
	if err := json.NewDecoder(rejected.Body).Decode(&rejectedEnvelope); err != nil {
		t.Fatal(err)
	}
	if !rejectedEnvelope.Data.Fallback || rejectedEnvelope.Data.LoaderData != nil || rejectedEnvelope.Data.RenderOutput == nil ||
		rejectedEnvelope.Data.RenderOutput.Source != pages.ThemeRenderSourceEmergency || targets.called != 2 || themeStore.gets != 0 {
		t.Fatalf("rejected response=%#v targets=%d packageGets=%d", rejectedEnvelope.Data, targets.called, themeStore.gets)
	}
}

// TestResolvePathUsesActiveThemeOverrideForPluginBusinessPage 证明：
// 1) 激活主题的 templates/plugins/{pluginId} 覆盖优先于插件模板；
// 2) 业务数据经 schema 密封后原样进入 loaderData；
// 3) 错误 themeOverrideKey 时 soft-skip 回落到插件模板；
// 4) 额外字段破坏契约时 fail closed 到 host_emergency，不暴露 loaderData。
func TestResolvePathUsesActiveThemeOverrideForPluginBusinessPage(t *testing.T) {
	pluginRoot := t.TempDir()
	themeRoot := t.TempDir()
	pluginTemplate := `plugin: {{.title}} / {{.state}}`
	overrideTemplate := `theme: {{.title}} / {{.state}}`
	schemaBody := `{"type":"object","required":["title","state"],"additionalProperties":false,"properties":{"title":{"type":"string"},"state":{"const":"published"}}}`
	writeControllerFixtureFile(t, pluginRoot, "theme.json", `{"pages":[]}`)
	writeControllerFixtureFile(t, pluginRoot, "templates/article.html", pluginTemplate)
	writeControllerFixtureFile(t, pluginRoot, "schemas/article.json", schemaBody)
	writeControllerFixtureFile(t, themeRoot, "theme.json", `{"pages":[]}`)
	writeControllerFixtureFile(t, themeRoot, "templates/home.html", `<main>theme home</main><sf-home-page></sf-home-page>`)
	writeControllerFixtureFile(t, themeRoot, "templates/plugins/plugin.exact-page/article.html", overrideTemplate)

	pluginTemplateDigest := sha256.Sum256([]byte(pluginTemplate))
	schemaDigest := sha256.Sum256([]byte(schemaBody))
	homeBody := `<main>theme home</main><sf-home-page></sf-home-page>`
	homeDigest := sha256.Sum256([]byte(homeBody))
	overrideDigest := sha256.Sum256([]byte(overrideTemplate))

	pluginArtifact := pages.RuntimeArtifact{
		ExtensionID: "plugin.exact-page", ExtensionVersion: "2.0.0", PackageDigest: strings.Repeat("e", 64),
		RuntimeInstanceID: "runtime-exact-page",
	}
	themeArtifact := pages.RuntimeArtifact{
		ExtensionID: "theme.presentation", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("f", 64),
	}
	contribution := pages.PageContribution{
		ID: "plugin.exact-page.article", Action: pages.ActionAdd, Path: "/exact-articles/:slug",
		Template: "templates/article.html", Contract: "plugin.exact-page.page.article@1", Access: pages.AccessPublic,
		DataSource: "plugin", DataRoute: "/page-data/article", DataSchema: "schemas/article.json",
		ExtensionID: pluginArtifact.ExtensionID, Version: pluginArtifact.ExtensionVersion,
		PackageDigest: pluginArtifact.PackageDigest, RuntimeInstanceID: pluginArtifact.RuntimeInstanceID,
	}
	pluginSnapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pluginArtifact, PackageRoot: pluginRoot, Contributions: []pages.PageContribution{contribution},
		Templates: []pages.RuntimeTemplateDeclaration{{
			ID: "plugin.exact-page.template.article", ContractVersion: "plugin.exact-page.template.article@1",
			Action: "add", Path: "templates/article.html", Digest: hex.EncodeToString(pluginTemplateDigest[:]),
			ViewModelSchema: "plugin.exact-page.article.data@1", ThemeOverrideKey: "plugin.exact-page.article",
		}},
		DataSchemas: []pages.RuntimeDataSchemaDeclaration{{
			ID: "plugin.exact-page.article.data", Version: "1", Path: "schemas/article.json",
			Digest: hex.EncodeToString(schemaDigest[:]),
		}},
		PackageKind: pages.RuntimeTemplatePlugin, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	themeSnapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: themeArtifact, PackageRoot: themeRoot, Contributions: []pages.PageContribution{{
			ID: "theme.presentation.home", Action: pages.ActionReplace, Target: "forum.home",
			Template: "templates/home.html", Contract: "sforum.page.home@1",
			ExtensionID: themeArtifact.ExtensionID, Version: themeArtifact.ExtensionVersion,
			PackageDigest: themeArtifact.PackageDigest,
		}},
		Templates: []pages.RuntimeTemplateDeclaration{
			{
				ID: "theme.presentation.template.home", ContractVersion: "theme.presentation.template.home@1",
				Action: "add", Path: "templates/home.html", Digest: hex.EncodeToString(homeDigest[:]),
				ViewModelSchema: "sforum.page.home@1",
			},
			{
				ID: "theme.presentation.template.article", ContractVersion: "theme.presentation.template.article@1",
				Action: "replace", TargetID: "plugin.exact-page.template.article",
				Path: "templates/plugins/plugin.exact-page/article.html", Digest: hex.EncodeToString(overrideDigest[:]),
				ViewModelSchema: "plugin.exact-page.article.data@1", ThemeOverrideKey: "plugin.exact-page.article",
			},
		},
		PackageKind: pages.RuntimeTemplateTheme, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	if _, _, err := runtimeRegistry.Stage(themeSnapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRegistry.ActivateExact(themeArtifact); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeRegistry.Stage(pluginSnapshot); err != nil {
		t.Fatal(err)
	}

	registry := pages.NewRegistry(pages.NewMemoryStore())
	if _, err := registry.PublishExtensionIfRevision(pluginArtifact, []pages.PageContribution{contribution}, 0); err != nil {
		t.Fatal(err)
	}
	targets := &exactRouteTargets{want: pluginArtifact, base: "http://127.0.0.1:19999"}
	payload := `{"title":"Business truth","state":"published"}`
	loader := pages.NewPageDataLoader(roundTripFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{
			StatusCode: 200, Header: nethttp.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(payload)),
		}, nil
	}))
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	themeStore := &pagesThemeStore{items: map[string]extensions.Extension{}}
	controller := NewControllerWithThemes(registry, pagesActors{actors: map[int64]identity.Actor{}}, manager, themeStore).
		WithThemeRuntime(runtimeRegistry).
		WithLoader(pages.NewLoaderGateway(loader, targets))
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
	}})

	// 兼容覆盖：active_theme_override 胜出，业务字段不变。
	response := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/exact-articles/one", nil, nil)
	if response.StatusCode != nethttp.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var envelope pagesEnvelope[resolveResponse]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	html := ""
	if envelope.Data.RenderOutput != nil {
		html = strings.Join(envelope.Data.RenderOutput.HTMLSegments, "")
	}
	if envelope.Data.Provider != pluginArtifact.ExtensionID || envelope.Data.Fallback || envelope.Data.RenderOutput == nil ||
		envelope.Data.RenderOutput.Source != pages.ThemeRenderSourceActiveOverride ||
		!strings.Contains(html, "theme: Business truth / published") ||
		strings.Contains(html, "plugin: Business truth") ||
		targets.called != 1 || themeStore.gets != 0 {
		t.Fatalf("override response=%#v html=%q targets=%d packageGets=%d", envelope.Data, html, targets.called, themeStore.gets)
	}
	loaded, ok := envelope.Data.LoaderData.(map[string]any)
	if !ok || loaded["title"] != "Business truth" || loaded["state"] != "published" {
		t.Fatalf("loader data=%#v", envelope.Data.LoaderData)
	}

	// 错误 override key：soft-skip 回插件模板，业务语义不变。
	wrongOverrideBody := `mismatched theme: {{.title}}`
	wrongOverrideDigest := sha256.Sum256([]byte(wrongOverrideBody))
	writeControllerFixtureFile(t, themeRoot, "templates/plugins/plugin.exact-page/article.html", wrongOverrideBody)
	wrongTheme, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: pages.RuntimeArtifact{
			ExtensionID: "theme.wrong-key", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		},
		PackageRoot: themeRoot, Contributions: []pages.PageContribution{{
			ID: "theme.wrong-key.home", Action: pages.ActionReplace, Target: "forum.home",
			Template: "templates/home.html", Contract: "sforum.page.home@1",
			ExtensionID: "theme.wrong-key", Version: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		}},
		Templates: []pages.RuntimeTemplateDeclaration{
			{
				ID: "theme.wrong-key.template.home", ContractVersion: "theme.wrong-key.template.home@1",
				Action: "add", Path: "templates/home.html", Digest: hex.EncodeToString(homeDigest[:]),
				ViewModelSchema: "sforum.page.home@1",
			},
			{
				ID: "theme.wrong-key.template.article", ContractVersion: "theme.wrong-key.template.article@1",
				Action: "replace", TargetID: "plugin.exact-page.template.article",
				Path: "templates/plugins/plugin.exact-page/article.html", Digest: hex.EncodeToString(wrongOverrideDigest[:]),
				ViewModelSchema: "plugin.exact-page.article.data@1", ThemeOverrideKey: "plugin.exact-page.other",
			},
		},
		PackageKind: pages.RuntimeTemplateTheme, RequireDeclaredTemplates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeRegistry.Stage(wrongTheme); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRegistry.ActivateExact(wrongTheme.Artifact()); err != nil {
		t.Fatal(err)
	}
	skipped := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/exact-articles/two", nil, nil)
	if skipped.StatusCode != nethttp.StatusOK {
		t.Fatalf("skipped status=%d", skipped.StatusCode)
	}
	var skippedEnvelope pagesEnvelope[resolveResponse]
	if err := json.NewDecoder(skipped.Body).Decode(&skippedEnvelope); err != nil {
		t.Fatal(err)
	}
	skippedHTML := ""
	if skippedEnvelope.Data.RenderOutput != nil {
		skippedHTML = strings.Join(skippedEnvelope.Data.RenderOutput.HTMLSegments, "")
	}
	if skippedEnvelope.Data.Fallback || skippedEnvelope.Data.RenderOutput == nil ||
		skippedEnvelope.Data.RenderOutput.Source != pages.ThemeRenderSourcePlugin ||
		!strings.Contains(skippedHTML, "plugin: Business truth / published") ||
		strings.Contains(skippedHTML, "mismatched theme") {
		t.Fatalf("key-drift response=%#v html=%q", skippedEnvelope.Data, skippedHTML)
	}

	// 契约破坏载荷：emergency，无 loaderData。
	payload = `{"title":"Business truth","state":"published","themeMutation":true}`
	// 重新激活兼容主题，确保 emergency 不是因 key drift。
	if _, err := runtimeRegistry.ActivateExact(themeArtifact); err != nil {
		t.Fatal(err)
	}
	// 恢复覆盖模板文件内容（路径校验已编译进 snapshot，文件内容不再被热读）。
	rejected := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/exact-articles/three", nil, nil)
	if rejected.StatusCode != nethttp.StatusOK {
		t.Fatalf("rejected status=%d", rejected.StatusCode)
	}
	var rejectedEnvelope pagesEnvelope[resolveResponse]
	if err := json.NewDecoder(rejected.Body).Decode(&rejectedEnvelope); err != nil {
		t.Fatal(err)
	}
	if !rejectedEnvelope.Data.Fallback || rejectedEnvelope.Data.LoaderData != nil || rejectedEnvelope.Data.RenderOutput == nil ||
		rejectedEnvelope.Data.RenderOutput.Source != pages.ThemeRenderSourceEmergency {
		t.Fatalf("rejected response=%#v", rejectedEnvelope.Data)
	}
}

func writeControllerFixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestActorPermissionKeysPreservesScopedAuthority(t *testing.T) {
	got := actorPermissionKeys(identity.Actor{Permissions: map[string]bool{
		"z.allowed": true,
		"a.denied":  false,
		"a.allowed": true,
	}})
	if strings.Join(got, ",") != "a.allowed,z.allowed" {
		t.Fatalf("permissions=%v", got)
	}
}

func TestResolveAddedStaticAndParams(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-docs/hello", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Data["action"] != "add" {
		t.Fatalf("%#v", body.Data)
	}
	params, _ := body.Data["routeParams"].(map[string]any)
	if params["slug"] != "hello" {
		t.Fatalf("params %#v", params)
	}
}

func TestResolveLegacyL1RecordsRequestTimeLoaderTelemetry(t *testing.T) {
	// 无 ThemeRuntimeSnapshot 覆盖的 add 路径会走 LoadTemplate；须记 LTS 遥测。
	apilts.ResetProcessForTest(apilts.New())
	t.Cleanup(func() { apilts.ResetProcessForTest(nil) })
	before := apilts.Process().ShimCalls(apilts.ThemeRequestTimeLoaderContractID)

	app, _, themes, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-docs/hello", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if themes.gets == 0 {
		t.Fatal("expected package store Get for legacy request-time L1 load")
	}
	after := apilts.Process().ShimCalls(apilts.ThemeRequestTimeLoaderContractID)
	if after <= before {
		t.Fatalf("expected theme request-time loader shim call, before=%d after=%d", before, after)
	}
}

func TestResolveCompiledThemeDoesNotRecordRequestTimeLoaderTelemetry(t *testing.T) {
	// 精确 snapshot 热路径禁止请求时读盘，也不应记 request-time loader 遥测。
	apilts.ResetProcessForTest(apilts.New())
	t.Cleanup(func() { apilts.ResetProcessForTest(nil) })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"pages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "templates/home.html"), []byte(`<main>compiled home</main><sf-home-page></sf-home-page>`), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	artifact := pages.RuntimeArtifact{
		ExtensionID: "compiled.theme", ExtensionVersion: "1.0.0", PackageDigest: digest,
	}
	contribution := pages.PageContribution{
		ID: "compiled.home", Action: pages.ActionReplace, Target: "forum.home",
		Template: "templates/home.html", Contract: "sforum.page.home@1",
		ExtensionID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: artifact.PackageDigest,
	}
	snapshot, err := pages.BuildThemeRuntimeSnapshot(pages.ThemeRuntimeBuildInput{
		Artifact: artifact, PackageRoot: root, Contributions: []pages.PageContribution{contribution},
		SiteName: "SForum", Locales: []string{"zh-CN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	runtimeRegistry.Publish(snapshot)

	registry := pages.NewRegistry(pages.NewMemoryStore())
	if err := registry.RegisterContributions(artifact.ExtensionID, []pages.PageContribution{contribution}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveReplace(context.Background(), pages.ProviderBinding{
		PageID: "forum.home", ExtensionID: artifact.ExtensionID, ContributionID: contribution.ID,
		Version: contribution.Version, PackageDigest: contribution.PackageDigest,
		ContractVersion: contribution.Contract, ApprovedBy: 1,
	}); err != nil {
		t.Fatal(err)
	}
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	// PackagePath 故意指向不存在目录：热路径若误触 LoadTemplate 会失败；我们断言不读盘。
	themeStore := &pagesThemeStore{items: map[string]extensions.Extension{
		artifact.ExtensionID: {
			ID: artifact.ExtensionID, Version: artifact.ExtensionVersion, PackageDigest: digest,
			PackagePath: filepath.Join(root, "missing"),
		},
	}}
	controller := NewControllerWithThemes(registry, pagesActors{actors: map[int64]identity.Actor{}}, manager, themeStore).
		WithThemeRuntime(runtimeRegistry)
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{
		pagesRouteProvider(func(api fiber.Router) { controller.RegisterRoutes(api) }),
	}})

	before := apilts.Process().ShimCalls(apilts.ThemeRequestTimeLoaderContractID)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.home", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if themeStore.gets != 0 {
		t.Fatalf("compiled snapshot must not hit package store, gets=%d", themeStore.gets)
	}
	after := apilts.Process().ShimCalls(apilts.ThemeRequestTimeLoaderContractID)
	if after != before {
		t.Fatalf("compiled snapshot must not record request-time loader telemetry, before=%d after=%d", before, after)
	}
}

func TestResolvePath404(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/no-such-page", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestResolvePathLogin401(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestResolvePathLoginOK(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-members", nil, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestApproveNonSuperAdmin403(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 4)
	body := []byte(`{"extensionId":"sforum.page-registry-demo","contributionId":"sforum.page-registry-demo.home","version":"1.0.0","packageDigest":"digest-v1","contractVersion":"sforum.page.home@1"}`)
	resp := performPages(t, app, nethttp.MethodPost, "/api/v1/admin/pages/forum.home/approve", body, cookie)
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestThemeActivationPreviewReturnsExactTuplesAndApprovalEligibility(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 1)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/admin/pages/activate-preview/demo.theme", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("preview status = %d", resp.StatusCode)
	}
	var body pagesEnvelope[themeActivationPreview]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	preview := body.Data
	if preview.ExtensionID != "demo.theme" || preview.Version != "1.0.0" || preview.PackageDigest != "theme-d" ||
		preview.CurrentThemeID != "demo.theme" || preview.CurrentThemeVersion != "1.0.0" || preview.CurrentThemeDigest != "theme-d" {
		t.Fatalf("preview tuples = %#v", preview)
	}
	if !preview.CanActivate || !preview.CanApproveCoreReplacements || !preview.RequiresCoreReplacementApproval || len(preview.Impacts) == 0 {
		t.Fatalf("preview eligibility = %#v", preview)
	}
	for _, impact := range preview.Impacts {
		if impact.Contribution.Action == pages.ActionReplace && !impact.RequiresApproval {
			t.Fatalf("replace impact lacks approval requirement: %#v", impact)
		}
	}
}

func TestThemeActivationPreviewUsesExactStagedArtifact(t *testing.T) {
	app, _, themes, _ := newPagesTestApp(t)
	current := themes.items["demo.theme"]
	current.StagedVersion = &extensions.ExtensionVersion{
		ID: 2, Version: "2.0.0", PackageDigest: "theme-staged-d", PackagePath: current.PackagePath,
		Manifest: current.Manifest,
	}
	themes.items[current.ID] = current
	cookie := loginPagesUser(t, app, 1)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/admin/pages/activate-preview/demo.theme", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("preview status=%d", resp.StatusCode)
	}
	var body pagesEnvelope[themeActivationPreview]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	preview := body.Data
	if preview.Version != "2.0.0" || preview.PackageDigest != "theme-staged-d" ||
		preview.CurrentThemeVersion != "1.0.0" || preview.CurrentThemeDigest != "theme-d" {
		t.Fatalf("staged preview=%#v", preview)
	}
	for _, impact := range preview.Impacts {
		if impact.Contribution.Version != "2.0.0" || impact.Contribution.PackageDigest != "theme-staged-d" {
			t.Fatalf("staged impact=%#v", impact)
		}
	}
}

func TestApproveAndResolveReplace(t *testing.T) {
	app, reg, _, sink := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 1)
	body := []byte(`{"extensionId":"sforum.page-registry-demo","contributionId":"sforum.page-registry-demo.home","version":"1.0.0","packageDigest":"digest-v1","contractVersion":"sforum.page.home@1","templatePath":"templates/evil.html"}`)
	resp := performPages(t, app, nethttp.MethodPost, "/api/v1/admin/pages/forum.home/approve", body, cookie)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("approve %d %s", resp.StatusCode, b)
	}
	if len(sink.events) == 0 {
		t.Fatal("expected audit")
	}
	rDirect, err := reg.Resolve(context.Background(), "forum.home")
	if err != nil || rDirect.Provider != "sforum.page-registry-demo" {
		t.Fatalf("registry resolve after approve: %#v err=%v", rDirect, err)
	}
	if strings.Contains(rDirect.TemplatePath, "evil") {
		t.Fatalf("forged template path stored: %s", rDirect.TemplatePath)
	}
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve?id=forum.home", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("resolve status %d", resp.StatusCode)
	}
	var env pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Data["provider"] != "sforum.page-registry-demo" {
		t.Fatalf("public resolve provider=%v fallback=%v raw=%#v", env.Data["provider"], env.Data["fallback"], env.Data)
	}
	if html, _ := env.Data["templateHtml"].(string); html == "" {
		t.Fatal("expected rendered template html")
	}
}

func TestApproveBadContract(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	cookie := loginPagesUser(t, app, 1)
	body := []byte(`{"extensionId":"sforum.page-registry-demo","contributionId":"sforum.page-registry-demo.home","version":"1.0.0","packageDigest":"digest-v1","contractVersion":"wrong@1"}`)
	resp := performPages(t, app, nethttp.MethodPost, "/api/v1/admin/pages/forum.home/approve", body, cookie)
	if resp.StatusCode != 422 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRestoreAudit(t *testing.T) {
	app, reg, _, sink := newPagesTestApp(t)
	_ = reg.ApproveReplace(context.Background(), pages.ProviderBinding{
		PageID: "forum.home", ExtensionID: "sforum.page-registry-demo",
		ContributionID: "sforum.page-registry-demo.home", Version: "1.0.0",
		PackageDigest: "digest-v1", ContractVersion: "sforum.page.home@1", ApprovedBy: 1,
	})
	cookie := loginPagesUser(t, app, 1)
	before := len(sink.events)
	resp := performPages(t, app, nethttp.MethodPost, "/api/v1/admin/pages/forum.home/restore-core", nil, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(sink.events) <= before {
		t.Fatal("expected restore audit")
	}
}

func TestThemeAssetActiveOnly(t *testing.T) {
	app, _, themes, _ := newPagesTestApp(t)
	root := themes.active.PackagePath
	cssDir := filepath.Join(root, "assets")
	_ = os.MkdirAll(cssDir, 0o755)
	cssPath := filepath.Join(cssDir, "controller-test.css")
	_ = os.WriteFile(cssPath, []byte("body{color:red}"), 0o644)
	t.Cleanup(func() { _ = os.Remove(cssPath) })

	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/site/theme-assets/demo.theme/assets/controller-test.css?v=theme-d", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("active asset %d", resp.StatusCode)
	}
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/site/theme-assets/sforum.page-registry-demo/templates/docs.html", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("inactive should 404, got %d", resp.StatusCode)
	}
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/site/theme-assets/demo.theme/assets/controller-test.css?v=wrong", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("digest mismatch %d", resp.StatusCode)
	}
}

func TestGuestAccessConflictWhenLoggedIn(t *testing.T) {
	app, reg, _, _ := newPagesTestApp(t)
	_ = reg.RegisterContributions("guest.plug", []pages.PageContribution{{
		ID: "g.login", Action: pages.ActionAdd, Path: "/guest-only-page",
		Access: pages.AccessGuest, ExtensionID: "guest.plug", Version: "1", PackageDigest: "d",
		Template: "templates/docs.html",
	}})
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/guest-only-page", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("anon guest %d", resp.StatusCode)
	}
	cookie := loginPagesUser(t, app, 2)
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/guest-only-page", nil, cookie)
	if resp.StatusCode != 409 {
		t.Fatalf("logged-in guest page should 409, got %d", resp.StatusCode)
	}
}

func TestModerationAccess(t *testing.T) {
	app, reg, _, _ := newPagesTestApp(t)
	_ = reg.RegisterContributions("mod.plug", []pages.PageContribution{{
		ID: "m.x", Action: pages.ActionAdd, Path: "/mod-only",
		Access: pages.AccessModeration, ExtensionID: "mod.plug", Version: "1", PackageDigest: "d",
	}})
	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/mod-only", nil, cookie)
	if resp.StatusCode != 403 {
		t.Fatalf("member %d", resp.StatusCode)
	}
	cookie = loginPagesUser(t, app, 3)
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/mod-only", nil, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("mod %d", resp.StatusCode)
	}
}

func TestPermissionAccess(t *testing.T) {
	app, reg, _, _ := newPagesTestApp(t)
	_ = reg.RegisterContributions("perm.plug", []pages.PageContribution{{
		ID: "p.x", Action: pages.ActionAdd, Path: "/perm-page",
		Access: pages.AccessPermission, Permission: identity.PermissionExtensionView,
		ExtensionID: "perm.plug", Version: "1", PackageDigest: "d",
	}})
	cookie := loginPagesUser(t, app, 2)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/perm-page", nil, cookie)
	if resp.StatusCode != 403 {
		t.Fatalf("deny %d", resp.StatusCode)
	}
	cookie = loginPagesUser(t, app, 4)
	resp = performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/perm-page", nil, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("allow %d", resp.StatusCode)
	}
}

func TestLoaderWiredOnResolvePath(t *testing.T) {
	app, reg, _, _ := newPagesTestApp(t)
	reg.ClearExtension("sforum.page-registry-demo")
	_ = reg.RegisterContributions("sforum.page-registry-demo", []pages.PageContribution{{
		ID: "sforum.page-registry-demo.docs", Action: pages.ActionAdd, Path: "/demo-docs/:slug",
		Template: "templates/docs.html", Access: pages.AccessPublic,
		DataSource: "plugin", DataRoute: "/docs/data",
		ExtensionID: "sforum.page-registry-demo", Version: "1.0.0", PackageDigest: "digest-v1",
		Contract: "sforum.page.plugin_docs@1",
	}})
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/demo-docs/hello", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var env pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&env)
	ld, _ := env.Data["loaderData"].(map[string]any)
	if ld == nil || ld["title"] != "hello" {
		t.Fatalf("loaderData %#v", env.Data["loaderData"])
	}
	if ld["slug"] != "hello" {
		t.Fatalf("slug param not passed: %#v", ld)
	}
}

func TestLoaderUnavailableWhenPluginRuntimeMissing(t *testing.T) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := pagesActors{actors: map[int64]identity.Actor{}}
	reg := pages.NewRegistry(pages.NewMemoryStore())
	_ = reg.RegisterContributions("offline", []pages.PageContribution{{
		ID: "o.x", Action: pages.ActionAdd, Path: "/offline-docs",
		Access: pages.AccessPublic, DataSource: "plugin", DataRoute: "/d",
		ExtensionID: "offline", Version: "1", PackageDigest: "d",
	}})
	gw := pages.NewLoaderGateway(pages.NewPageDataLoader(nil), fakeRouteTargets{bases: map[string]string{}})
	ctrl := NewController(reg, users, manager).WithLoader(gw)
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{pagesRouteProvider(func(api fiber.Router) {
			ctrl.RegisterRoutes(api)
		})},
	})
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/offline-docs", nil, nil)
	var env pagesEnvelope[map[string]any]
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Data["loaderError"] == nil || env.Data["loaderError"] == "" {
		t.Fatalf("expected loader error: %#v", env.Data)
	}
}

func TestLoaderRejectsNonLoopbackAndRedirect(t *testing.T) {
	l := pages.NewPageDataLoader(nil)
	res := l.Fetch(context.Background(), pages.LoaderRequest{
		ExtensionID: "p", Route: "/d", TargetBase: "https://evil.example",
	})
	if !res.Fallback {
		t.Fatalf("%#v", res)
	}
	l = pages.NewPageDataLoader(roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{
			StatusCode: 302,
			Header:     nethttp.Header{"Location": []string{"http://127.0.0.1/x"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}))
	res = l.Fetch(context.Background(), pages.LoaderRequest{
		ExtensionID: "p", Route: "/d", TargetBase: "http://127.0.0.1:1",
	})
	if res.Error == "" || !strings.Contains(res.Error, "redirect") {
		t.Fatalf("%#v", res)
	}
}

func TestThemeAssetRejectsSVGAndJS(t *testing.T) {
	app, _, themes, _ := newPagesTestApp(t)
	root := themes.active.PackagePath
	assets := filepath.Join(root, "assets")
	_ = os.MkdirAll(assets, 0o755)
	svg := filepath.Join(assets, "evil.svg")
	js := filepath.Join(assets, "evil.js")
	html := filepath.Join(assets, "evil.html")
	_ = os.WriteFile(svg, []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"), 0o644)
	_ = os.WriteFile(js, []byte("alert(1)"), 0o644)
	_ = os.WriteFile(html, []byte("<html></html>"), 0o644)
	t.Cleanup(func() {
		_ = os.Remove(svg)
		_ = os.Remove(js)
		_ = os.Remove(html)
	})
	for _, name := range []string{"evil.svg", "evil.js", "evil.html"} {
		resp := performPages(t, app, nethttp.MethodGet, "/api/v1/site/theme-assets/demo.theme/assets/"+name+"?v=theme-d", nil, nil)
		if resp.StatusCode != 404 && resp.StatusCode != 422 && resp.StatusCode != 403 {
			t.Fatalf("%s should be rejected, got %d", name, resp.StatusCode)
		}
	}
}

func TestThemeAssetRejectsPathTraversal(t *testing.T) {
	app, _, _, _ := newPagesTestApp(t)
	resp := performPages(t, app, nethttp.MethodGet, "/api/v1/site/theme-assets/demo.theme/assets/../../etc/passwd?v=theme-d", nil, nil)
	if resp.StatusCode == 200 {
		t.Fatal("path traversal must not succeed")
	}
}

func TestLoaderDoesNotForwardAuthHeaders(t *testing.T) {
	var sawCookie, sawAuth, sawCSRF bool
	var sawActorHint bool
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := pagesActors{actors: map[int64]identity.Actor{}}
	reg := pages.NewRegistry(pages.NewMemoryStore())
	_ = reg.RegisterContributions("hdr.plug", []pages.PageContribution{{
		ID: "h.x", Action: pages.ActionAdd, Path: "/hdr-docs",
		Access: pages.AccessPublic, DataSource: "plugin", DataRoute: "/d",
		ExtensionID: "hdr.plug", Version: "1", PackageDigest: "d",
	}})
	loader := pages.NewPageDataLoader(roundTripFunc(func(r *nethttp.Request) (*nethttp.Response, error) {
		if r.Header.Get("Cookie") != "" {
			sawCookie = true
		}
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		if r.Header.Get("X-Csrf-Token") != "" {
			sawCSRF = true
		}
		if r.Header.Get("X-SForum-Actor-ID-Hint") != "" || r.Header.Get("X-SForum-Actor-ID") != "" {
			sawActorHint = true
		}
		return &nethttp.Response{
			StatusCode: 200,
			Header:     nethttp.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}))
	gw := pages.NewLoaderGateway(loader, fakeRouteTargets{bases: map[string]string{"hdr.plug": "http://127.0.0.1:19999"}})
	ctrl := NewController(reg, users, manager).WithLoader(gw)
	cfg := config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{pagesRouteProvider(func(api fiber.Router) {
			ctrl.RegisterRoutes(api)
		})},
	})
	// 即便客户端带 Cookie/Authorization，loader 也不得转发
	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/pages/resolve-path?path=/hdr-docs", nil)
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Csrf-Token", "tok")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, raw)
	}
	if sawCookie || sawAuth || sawCSRF {
		t.Fatalf("loader must not forward cookie/auth/csrf: cookie=%v auth=%v csrf=%v", sawCookie, sawAuth, sawCSRF)
	}
	// actor hint 对匿名请求应为 false
	if sawActorHint {
		t.Fatalf("anonymous request should not send actor hint")
	}
}
