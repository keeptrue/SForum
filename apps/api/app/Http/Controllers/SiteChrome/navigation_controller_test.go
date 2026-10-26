package sitechromecontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestPublicNavigationRejectsInvalidLocationAndUsesOptionalActor(t *testing.T) {
	app, _ := newNavigationControllerApp(identity.Actor{}, controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 4}})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/site/navigation?locations=not-a-location", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid location status=%d", response.StatusCode)
	}
}

func TestAccountSettingsNavigationRequiresAuthentication(t *testing.T) {
	app, _ := newNavigationControllerApp(identity.Actor{}, controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 1}})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/site/account-navigation", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous account navigation status=%d", response.StatusCode)
	}
	actorApp, cookie := newNavigationControllerApp(identity.Actor{ID: 12, Status: identity.UserStatusActive}, controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 1}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/site/account-navigation", nil)
	request.AddCookie(cookie)
	response, err = actorApp.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated account navigation status=%d", response.StatusCode)
	}
}

func TestPublicNavigationIsPrivateAndVariesByActor(t *testing.T) {
	document := sitechrome.NavigationDocument{
		Revision: 4,
		Definitions: []sitechrome.NavigationDefinition{{
			SourceKey: "operator.members", SourceKind: sitechrome.NavigationSourceOperator,
			LinkKind: sitechrome.NavigationLinkInternal, LabelEnUS: "Members", Href: "/members",
		}},
		Placements: []sitechrome.NavigationPlacement{{
			SourceKey: "operator.members", Location: sitechrome.NavigationLocationTopbar,
			Order: 1, Enabled: true, Visibility: sitechrome.NavigationVisibilityAuthenticated,
		}},
	}
	actor := identity.Actor{ID: 22, Status: identity.UserStatusActive}
	app, cookie := newNavigationControllerApp(actor, controllerNavigationStore{document: document})

	guestResponse, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/site/navigation?locations=public.topbar.primary", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer guestResponse.Body.Close()
	assertPrivateNavigationHeaders(t, guestResponse)
	var guestEnvelope struct {
		Data sitechrome.ResolvedNavigation `json:"data"`
	}
	if err := json.NewDecoder(guestResponse.Body).Decode(&guestEnvelope); err != nil {
		t.Fatal(err)
	}
	if resolvedNavigationHasSource(guestEnvelope.Data, "operator.members") {
		t.Fatalf("guest received authenticated item: %#v", guestEnvelope.Data)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/site/navigation?locations=public.topbar.primary", nil)
	request.AddCookie(cookie)
	memberResponse, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer memberResponse.Body.Close()
	assertPrivateNavigationHeaders(t, memberResponse)
	var memberEnvelope struct {
		Data sitechrome.ResolvedNavigation `json:"data"`
	}
	if err := json.NewDecoder(memberResponse.Body).Decode(&memberEnvelope); err != nil {
		t.Fatal(err)
	}
	if !resolvedNavigationHasSource(memberEnvelope.Data, "operator.members") {
		t.Fatalf("member navigation omitted authenticated item: %#v", memberEnvelope.Data)
	}
}

func assertPrivateNavigationHeaders(t *testing.T, response *http.Response) {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	vary := map[string]bool{}
	for _, field := range strings.Split(response.Header.Get("Vary"), ",") {
		vary[strings.ToLower(strings.TrimSpace(field))] = true
	}
	for _, field := range []string{"cookie", "authorization", "accept-language"} {
		if !vary[field] {
			t.Fatalf("Vary=%q missing %s", response.Header.Get("Vary"), field)
		}
	}
}

func resolvedNavigationHasSource(resolved sitechrome.ResolvedNavigation, sourceKey string) bool {
	for _, location := range resolved.Locations {
		for _, item := range location.Items {
			if item.SourceKey == sourceKey {
				return true
			}
		}
	}
	return false
}

func TestAdminNavigationReadRequiresAuthenticationAndPermission(t *testing.T) {
	store := controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 4}}
	app, _ := newNavigationControllerApp(identity.Actor{}, store)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/site/navigation", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous admin navigation status=%d", response.StatusCode)
	}

	denied, cookie := newNavigationControllerApp(identity.Actor{ID: 9, Status: identity.UserStatusActive}, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/site/navigation", nil)
	request.AddCookie(cookie)
	response, err = denied.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("denied admin navigation status=%d", response.StatusCode)
	}

	allowed, allowedCookie := newNavigationControllerApp(identity.Actor{ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}, store)
	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/site/navigation", nil)
	request.AddCookie(allowedCookie)
	response, err = allowed.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("allowed admin navigation status=%d", response.StatusCode)
	}
}

func TestAdminNavigationCommandsRequireSiteManagePermission(t *testing.T) {
	app, cookie := newNavigationControllerApp(identity.Actor{ID: 9, Status: identity.UserStatusActive}, controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 4}})
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/v1/admin/site/navigation/apply", `{}`},
		{http.MethodPost, "/api/v1/admin/site/navigation/defaults/preview", `{}`},
		{http.MethodPost, "/api/v1/admin/site/navigation/defaults/apply", `{}`},
		{http.MethodGet, "/api/v1/admin/site/navigation/snapshots", ""},
		{http.MethodGet, "/api/v1/admin/site/navigation/snapshots/1", ""},
		{http.MethodPost, "/api/v1/admin/site/navigation/snapshots/1/restore", `{}`},
		{http.MethodGet, "/api/v1/admin/site/navigation/export", ""},
		{http.MethodPost, "/api/v1/admin/site/navigation/import/preview", `{}`},
		{http.MethodPost, "/api/v1/admin/site/navigation/import/apply", `{}`},
	} {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(cookie)
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s status=%d, want forbidden", test.method, test.path, response.StatusCode)
		}
	}
}

// 浏览器真实 Accept-Language 带 q 值时，handler 必须归一化后组合，
// 而不是把原始头透传给 NavigationRegistry 导致 500。
func TestAccountSettingsNavigationAcceptsRawAcceptLanguageHeader(t *testing.T) {
	registry := navigationregistry.New()
	service := sitechrome.NewService(controllerNavigationStore{document: sitechrome.NavigationDocument{Revision: 1}}).
		WithNavigationRegistry(registry).
		WithNavigationRuntime(controllerNavigationAdmission{}, nil)
	digest := strings.Repeat("d", 64)
	if _, err := registry.Publish(navigationregistry.Publication{
		Artifact: navigationregistry.Artifact{
			ExtensionID: "sforum.oauth2-provider", ExtensionVersion: "1.0.0",
			PackageDigest: digest, ImpactDigest: digest, VersionID: 1, RuntimeInstanceID: "runtime-oauth2",
		},
		Navigation: []navigationregistry.NavigationDeclaration{{
			ID: "sforum.oauth2-provider.navigation.apps", ContractVersion: "sforum.oauth2-provider.navigation.apps@1",
			Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd,
			Label: "OAuth 应用", Href: "/settings/oauth-apps",
			Permission: "oauth2.provider.apps.manage", Visibility: navigationregistry.VisibilityAuthenticated, Order: 10,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{
		ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{"oauth2.provider.apps.manage": true},
	}
	app, cookie := newNavigationControllerAppWithService(actor, service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/site/account-navigation", nil)
	request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	request.AddCookie(cookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("raw Accept-Language status=%d, want 200", response.StatusCode)
	}
	var envelope struct {
		Data struct {
			Items []sitechrome.AccountSettingsNavItem `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ID != "sforum.oauth2-provider.navigation.apps" ||
		envelope.Data.Items[0].Href != "/settings/oauth-apps" {
		t.Fatalf("raw Accept-Language items=%#v", envelope.Data.Items)
	}
}

// controllerNavigationAdmission 只让测试声明在组合期可见，不引入真实生命周期。
type controllerNavigationAdmission struct{}

func (controllerNavigationAdmission) Available(navigationregistry.Artifact) bool { return true }

func (controllerNavigationAdmission) Acquire(ctx context.Context, artifact navigationregistry.Artifact) (navigationregistry.RuntimeLease, error) {
	return controllerNavigationLease{ctx: ctx, runtimeID: artifact.RuntimeInstanceID}, nil
}

type controllerNavigationLease struct {
	ctx       context.Context
	runtimeID string
}

func (l controllerNavigationLease) Context() context.Context  { return l.ctx }
func (l controllerNavigationLease) RuntimeInstanceID() string { return l.runtimeID }
func (controllerNavigationLease) Release()                    {}

func newNavigationControllerApp(actor identity.Actor, store controllerNavigationStore) (*fiber.App, *http.Cookie) {
	return newNavigationControllerAppWithService(actor, sitechrome.NewService(store))
}

func newNavigationControllerAppWithService(actor identity.Actor, service *sitechrome.Service) (*fiber.App, *http.Cookie) {
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{})
	controller := NewController(service, controllerNavigationActorStore{actor: actor}, sessions)
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{
			navigationControllerRouteProvider(func(api fiber.Router) {
				api.Post("/test-login", func(c fiber.Ctx) error { _, err := sessions.Start(c, actor.ID); return err })
			}),
			controller,
		},
	})
	response, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/test-login", nil))
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	if actor.ID <= 0 || len(response.Cookies()) == 0 {
		return app, nil
	}
	return app, response.Cookies()[0]
}

type navigationControllerRouteProvider func(fiber.Router)

func (f navigationControllerRouteProvider) RegisterRoutes(api fiber.Router) { f(api) }

type controllerNavigationStore struct {
	sitechrome.Store
	document sitechrome.NavigationDocument
}

func (s controllerNavigationStore) ReadNavigationDocument(context.Context) (sitechrome.NavigationDocument, error) {
	return s.document, nil
}

type controllerNavigationActorStore struct{ actor identity.Actor }

func (s controllerNavigationActorStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}
