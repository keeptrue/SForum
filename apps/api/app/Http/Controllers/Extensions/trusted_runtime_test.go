package extensionscontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestTrustedRuntimeControllerUsesImmediateStatusResponses(t *testing.T) {
	frontend := &fakeTrustedFrontendHTTPService{status: extensions.FrontendStatus{
		ExtensionID: "demo.plugin", Kind: extensions.AdminFrontendKindPrebuiltComponent,
		TrustState: extensions.FrontendTrustRequired,
	}}
	app, manager := newTrustedRuntimeTestApp(t, frontend)
	managerCookie := loginExtensionUser(t, app, manager, 2)
	superCookie := loginExtensionUser(t, app, manager, 1)

	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/frontend", managerCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("frontend status: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/frontend/trust", managerCookie, `{"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("manager grant: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	frontend.grant = extensions.FrontendStatus{ExtensionID: "demo.plugin", Kind: extensions.AdminFrontendKindPrebuiltComponent, TrustState: extensions.FrontendTrustTrusted}
	resp = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/frontend/trust", superCookie, `{"digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("super-admin grant: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = performExtensionJSONRequest(t, app, http.MethodPost, "/api/v1/admin/extensions/demo.plugin/frontend/trust", superCookie, `{"digest":"not-a-digest"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid digest: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	frontend.revoke = extensions.FrontendStatus{ExtensionID: "demo.plugin", Kind: extensions.AdminFrontendKindPrebuiltComponent, TrustState: extensions.FrontendTrustRevoked}
	resp = performExtensionRequest(t, app, http.MethodDelete, "/api/v1/admin/extensions/demo.plugin/frontend/trust", superCookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("immediate revoke: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/web-releases", superCookie)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("removed release route must be 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTrustedRuntimeControllerServesOnlyImmutableAdminAssets(t *testing.T) {
	frontend := &fakeTrustedFrontendHTTPService{
		asset: extensions.FrontendAsset{
			Body: []byte("export const apiVersion = 1\n"), ContentType: "application/javascript; charset=utf-8", ETag: `"abc"`,
		},
		componentAsset: extensions.FrontendAsset{
			Body: []byte("export const componentId = 'dashboard'\n"), ContentType: "application/javascript; charset=utf-8", ETag: `"dashboard"`,
		},
	}
	app, manager := newTrustedRuntimeTestApp(t, frontend)
	cookie := loginExtensionUser(t, app, manager, 2)
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resp := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/frontend/assets/"+digest+"/entry", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected asset 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if body.String() != "export const apiVersion = 1\n" || resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected immutable response: headers=%v body=%q", resp.Header, body.String())
	}

	resp = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/frontend/assets/"+digest+"/dashboard/entry", cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected component asset 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	body.Reset()
	_, _ = body.ReadFrom(resp.Body)
	if body.String() != "export const componentId = 'dashboard'\n" ||
		frontend.requestedComponent != "dashboard" || frontend.requestedAsset != "entry" ||
		resp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		resp.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("unexpected component asset response: component=%q asset=%q headers=%v body=%q", frontend.requestedComponent, frontend.requestedAsset, resp.Header, body.String())
	}
}

func TestTrustedRuntimeControllerServesPublicL2DescriptorAndImmutableAssetsWithoutSession(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fileDigest := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	frontend := &fakeTrustedFrontendHTTPService{
		publicComponent: extensions.PublicFrontendComponent{
			SchemaVersion: extensions.PublicFrontendSchemaV1, APIVersion: extensions.PublicFrontendAPIVersion,
			ExtensionID: "demo.plugin", ComponentID: "demo.plugin.component.card", PackageDigest: digest,
		},
		publicAsset: extensions.FrontendAsset{
			Body: []byte("export const apiVersion = 1\n"), ContentType: "application/javascript; charset=utf-8",
			ETag: `"` + fileDigest + `"`, Digest: fileDigest, Integrity: "sha256-test",
		},
	}
	app, _ := newTrustedRuntimeTestApp(t, frontend)
	componentPath := "/api/v1/extensions/runtime/demo.plugin/components/demo.plugin.component.card"
	resp := performExtensionRequest(t, app, http.MethodGet, componentPath, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("public descriptor: status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	var envelope struct {
		Data extensions.PublicFrontendComponent `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if envelope.Data.ComponentID != "demo.plugin.component.card" || envelope.Data.SchemaVersion != extensions.PublicFrontendSchemaV1 {
		t.Fatalf("descriptor=%#v", envelope.Data)
	}

	assetPath := "/api/v1/extensions/runtime/demo.plugin/assets/" + digest + "/" + fileDigest + "/demo.plugin.component.card.l2.entry"
	resp = performExtensionRequest(t, app, http.MethodGet, assetPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public asset: status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if body.String() != "export const apiVersion = 1\n" ||
		resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		resp.Header.Get("X-SForum-Asset-Digest") != fileDigest ||
		resp.Header.Get("X-SForum-Asset-Integrity") != "sha256-test" ||
		resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Fatalf("public asset headers=%v body=%q", resp.Header, body.String())
	}

	packagePath := "/api/v1/extensions/runtime/demo.plugin/packages/" + digest + "/frontend/public/chunks/card.mjs"
	resp = performExtensionRequest(t, app, http.MethodGet, packagePath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public package asset: status=%d", resp.StatusCode)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/javascript; charset=utf-8" ||
		resp.Header.Get("X-Frame-Options") != "DENY" ||
		resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" ||
		resp.Header.Get("X-SForum-Asset-Digest") != fileDigest {
		t.Fatalf("public package asset headers=%v", resp.Header)
	}
}

func TestTrustedRuntimeControllerHidesInvalidOrRevokedPublicL2(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	frontend := &fakeTrustedFrontendHTTPService{publicErr: extensions.ErrPublicFrontendUnavailable}
	app, _ := newTrustedRuntimeTestApp(t, frontend)
	paths := []string{
		"/api/v1/extensions/runtime/demo.plugin/components/demo.plugin.component.card",
		"/api/v1/extensions/runtime/demo.plugin/assets/not-a-digest/" + digest + "/demo.plugin.asset",
		"/api/v1/extensions/runtime/demo.plugin/assets/" + digest + "/" + digest + "/demo.plugin.asset",
		"/api/v1/extensions/runtime/page-policy",
		"/api/v1/extensions/runtime/page-policy?component=demo.plugin/demo.plugin.component.card",
	}
	for _, path := range paths {
		resp := performExtensionRequest(t, app, http.MethodGet, path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: expected hidden 404, got %d", path, resp.StatusCode)
		}
	}
}

func TestTrustedRuntimeControllerServesPublicPagePolicyWithoutSession(t *testing.T) {
	headerValue := "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https://cdn.example"
	frontend := &fakeTrustedFrontendHTTPService{
		publicPolicy: extensions.PublicFrontendPolicy{
			SchemaVersion: extensions.PublicFrontendPolicySchemaV1,
			GraphDigest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DocumentPolicy: extensions.PublicFrontendDocumentPolicy{
				SchemaVersion: extensions.PublicFrontendDocumentPolicySchemaV1,
				Digest:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				HeaderValue:   headerValue,
				Directives: []extensions.PublicFrontendPolicyDirective{
					{Name: "default-src", Sources: []string{"'none'"}},
					{Name: "script-src", Sources: []string{"'self'"}},
				},
			},
		},
	}
	app, _ := newTrustedRuntimeTestApp(t, frontend)
	path := "/api/v1/extensions/runtime/page-policy?component=demo.plugin%2Fdemo.plugin.component.card&component=demo.plugin%2Fdemo.plugin.component.card"
	resp := performExtensionRequest(t, app, http.MethodGet, path, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK ||
		resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("X-SForum-Document-Policy-Digest") != frontend.publicPolicy.DocumentPolicy.Digest {
		t.Fatalf("public page policy headers: status=%d headers=%v", resp.StatusCode, resp.Header)
	}
	var envelope struct {
		Data extensions.PublicFrontendPolicy `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.SchemaVersion != extensions.PublicFrontendPolicySchemaV1 ||
		envelope.Data.DocumentPolicy.HeaderValue != headerValue ||
		envelope.Data.DocumentPolicy.Digest != frontend.publicPolicy.DocumentPolicy.Digest {
		t.Fatalf("policy payload=%#v", envelope.Data)
	}
}

func TestTrustedRuntimeControllerRejectsInvalidPublicPagePolicyQuery(t *testing.T) {
	app, _ := newTrustedRuntimeTestApp(t, &fakeTrustedFrontendHTTPService{})
	for _, path := range []string{
		"/api/v1/extensions/runtime/page-policy?component=only-extension",
		"/api/v1/extensions/runtime/page-policy?component=a/b/c",
		"/api/v1/extensions/runtime/page-policy?component=",
	} {
		resp := performExtensionRequest(t, app, http.MethodGet, path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: expected 422, got %d", path, resp.StatusCode)
		}
	}
}

func newTrustedRuntimeTestApp(t *testing.T, frontend TrustedFrontendService) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
	}}
	store := &controllerFakeStore{items: map[string]extensions.Extension{}}
	controller := NewController(extensions.NewService(store, "storage/extensions"), users, manager).WithTrustedRuntime(frontend)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			id := int64(1)
			if c.Params("id") == "2" {
				id = 2
			}
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, loginProvider}})
	return app, manager
}

type fakeTrustedFrontendHTTPService struct {
	status             extensions.FrontendStatus
	grant              extensions.FrontendStatus
	revoke             extensions.FrontendStatus
	asset              extensions.FrontendAsset
	componentAsset     extensions.FrontendAsset
	requestedComponent string
	requestedAsset     string
	challenge          extensions.FrontendTrustChallenge
	publicComponent    extensions.PublicFrontendComponent
	publicAsset        extensions.FrontendAsset
	publicPolicy       extensions.PublicFrontendPolicy
	publicErr          error
}

func (s *fakeTrustedFrontendHTTPService) Asset(context.Context, identity.Actor, string, string, string) (extensions.FrontendAsset, error) {
	return s.asset, nil
}

func (s *fakeTrustedFrontendHTTPService) ComponentAsset(_ context.Context, _ identity.Actor, _, _, componentID, assetName string) (extensions.FrontendAsset, error) {
	s.requestedComponent = componentID
	s.requestedAsset = assetName
	return s.componentAsset, nil
}

func (s *fakeTrustedFrontendHTTPService) Challenge(_ context.Context, actor identity.Actor, _ string) (extensions.FrontendTrustChallenge, error) {
	if !actor.IsSuperAdmin() {
		return extensions.FrontendTrustChallenge{}, identity.ErrPermissionDenied
	}
	return s.challenge, nil
}

func (s *fakeTrustedFrontendHTTPService) Frontend(context.Context, identity.Actor, string) (extensions.FrontendStatus, error) {
	return s.status, nil
}

func (s *fakeTrustedFrontendHTTPService) Grant(_ context.Context, actor identity.Actor, _ string, _ extensions.GrantFrontendInput) (extensions.FrontendStatus, error) {
	if !actor.IsSuperAdmin() {
		return extensions.FrontendStatus{}, identity.ErrPermissionDenied
	}
	return s.grant, nil
}

func (s *fakeTrustedFrontendHTTPService) Revoke(_ context.Context, actor identity.Actor, _ string) (extensions.FrontendStatus, error) {
	if !actor.IsSuperAdmin() {
		return extensions.FrontendStatus{}, identity.ErrPermissionDenied
	}
	return s.revoke, nil
}

func (s *fakeTrustedFrontendHTTPService) PublicComponent(context.Context, string, string) (extensions.PublicFrontendComponent, error) {
	return s.publicComponent, s.publicErr
}

func (s *fakeTrustedFrontendHTTPService) PublicAsset(context.Context, string, string, string, string) (extensions.FrontendAsset, error) {
	return s.publicAsset, s.publicErr
}

func (s *fakeTrustedFrontendHTTPService) PublicPackageAsset(context.Context, string, string, string) (extensions.FrontendAsset, error) {
	return s.publicAsset, s.publicErr
}

func (s *fakeTrustedFrontendHTTPService) PublicPagePolicyForComponents(
	_ context.Context,
	_ []extensions.PublicFrontendComponentRef,
) (extensions.PublicFrontendPolicy, error) {
	if s.publicErr != nil {
		return extensions.PublicFrontendPolicy{}, s.publicErr
	}
	if s.publicPolicy.SchemaVersion == "" {
		return extensions.PublicFrontendPolicy{
			SchemaVersion: extensions.PublicFrontendPolicySchemaV1,
			DocumentPolicy: extensions.PublicFrontendDocumentPolicy{
				SchemaVersion: extensions.PublicFrontendDocumentPolicySchemaV1,
				Digest:        "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				HeaderValue:   "default-src 'none'; script-src 'self'; style-src 'self'",
			},
		}, nil
	}
	return s.publicPolicy, nil
}
