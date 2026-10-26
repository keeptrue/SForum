package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

// T1E controller HTTP：allowed/denied、callback replay/exact-artifact、
// redaction、admin permission、legacy complete 410。

type t1eLinkStore struct {
	links     map[int64]identity.ExternalIdentityLink
	listCalls int
	nextID    int64
}

func newT1ELinkStore(links ...identity.ExternalIdentityLink) *t1eLinkStore {
	s := &t1eLinkStore{links: map[int64]identity.ExternalIdentityLink{}, nextID: 1}
	for _, l := range links {
		if l.ID == 0 {
			l.ID = s.nextID
			s.nextID++
		}
		s.links[l.ID] = l
		if s.nextID <= l.ID {
			s.nextID = l.ID + 1
		}
	}
	return s
}

func (s *t1eLinkStore) Link(context.Context, identity.LinkExternalIdentityInput, identity.ExternalIdentityLinkCommitFence) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s *t1eLinkStore) Unlink(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s *t1eLinkStore) Erase(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s *t1eLinkStore) Get(_ context.Context, id int64) (identity.ExternalIdentityLink, error) {
	if l, ok := s.links[id]; ok {
		return l, nil
	}
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *t1eLinkStore) FindActive(context.Context, string, string) (identity.ExternalIdentityLink, error) {
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}
func (s *t1eLinkStore) ListUser(_ context.Context, userID int64) ([]identity.ExternalIdentityLink, error) {
	s.listCalls++
	out := make([]identity.ExternalIdentityLink, 0)
	for _, l := range s.links {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	return out, nil
}

func newT1EExternalAuthApp(t *testing.T) (*fiber.App, *sessionTestStore, *Controller) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{HashSecret: "t1e-http-secret", SessionStore: store, TokenVersion: store.GetUserTokenVersion},
	)
	controller := NewControllerWithAuthSessions(service, manager, nil)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, store, controller
}

func promoteProviderManage(t *testing.T, store *sessionTestStore, userID int64) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	u := store.users[userID]
	u.Permissions = append(u.Permissions, identity.PermissionIdentityProviderManage)
	// super_admin 角色让 Actor.Can 直接通过（可选）。
	u.RoleKeys = append(u.RoleKeys, identity.RoleSuperAdmin)
	store.users[userID] = u
}

func TestT1E_LegacyAuthCompleteReturns410(t *testing.T) {
	controller := &Controller{}
	app := fiber.New()
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)

	for _, op := range []string{"login", "registration", "link"} {
		payload, _ := json.Marshal(map[string]any{
			"correlationId": "c1", "completionToken": "code",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/demo.auth/"+op+"/complete", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("op=%s status=%d body=%s", op, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "auth.provider_callback_required") {
			t.Fatalf("op=%s body=%s", op, body)
		}
	}
}

func TestT1E_CallbackReplayRedirectsWithoutSecrets(t *testing.T) {
	stateStore := identity.NewInMemoryCallbackStateStore()
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	providerID := "demo.auth"
	tx := identity.CallbackTransaction{
		State: "state-once", ProviderID: providerID, Operation: identity.ExternalAuthOperationLogin,
		OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
		AbsoluteCallbackURL: "https://forum.example.com/api/v1/auth/providers/demo.auth/callback",
		CodeVerifier:        "host-verifier-secret", CorrelationID: "c1",
		RedirectPath: "/topics/1",
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	if err := stateStore.Save(ctx, tx); err != nil {
		t.Fatal(err)
	}
	// 先消费一次，模拟已用 state。
	if _, err := stateStore.Consume(ctx, "state-once"); err != nil {
		t.Fatal(err)
	}

	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: providerID, ContractVersion: providerID + "@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{Name: identity.AuthOperationLoginComplete}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "rt-1",
		},
	}
	activation := identity.NewMemoryProviderActivationStore()
	login := true
	_, _ = activation.Upsert(ctx, identity.ProviderActivationInput{
		ProviderID: providerID, OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
		LoginEnabled: &login,
	})
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	// authFlow 非 nil 才能过 handler 前置检查；complete 不会被调用（state 已重放）。
	controller := &Controller{
		callbackStateStore:  stateStore,
		externalAuthService: svc,
		authFlow:            &identity.AuthProviderFlow{},
	}
	app := fiber.New()
	app.Get("/api/v1/auth/providers/:providerId/callback", controller.externalAuthCallback)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/demo.auth/callback?state=state-once&code=abc", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "auth.provider_callback_replayed") {
		t.Fatalf("replay redirect location=%s", loc)
	}
	if strings.Contains(loc, "host-verifier-secret") || strings.Contains(loc, digest) {
		t.Fatalf("redirect leaked sensitive material: %s", loc)
	}
}

func TestExternalAuthCallbackRejectsWrongBrowserBeforeProviderEffect(t *testing.T) {
	stateStore := identity.NewInMemoryCallbackStateStore()
	browserCookie, browserDigest := externalAuthBrowserCookieForTest()
	tx := identity.CallbackTransaction{
		State: "browser-bound-state", ProviderID: "demo.auth", Operation: identity.ExternalAuthOperationLogin,
		OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: strings.Repeat("a", 64),
		BrowserBindingDigest: browserDigest, CodeVerifier: "host-verifier", CorrelationID: "browser-bound",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := stateStore.Save(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	controller := &Controller{
		callbackStateStore: stateStore, externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{}),
		authFlow: &identity.AuthProviderFlow{},
	}
	app := fiber.New()
	app.Get("/callback/:providerId", controller.externalAuthCallback)
	request := httptest.NewRequest(http.MethodGet, "/callback/demo.auth?state=browser-bound-state&code=provider-code", nil)
	request.AddCookie(&http.Cookie{Name: browserCookie.Name, Value: strings.Repeat("c", 43)})
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || !strings.Contains(response.Header.Get("Location"), "auth.provider_callback_invalid") {
		t.Fatalf("status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestExternalAuthBrowserCookieUsesCrossSiteModeOnlyWhenSecure(t *testing.T) {
	tests := []struct {
		name         string
		appEnv       string
		appURL       string
		wantSecure   bool
		wantSameSite http.SameSite
	}{
		{
			name:         "production callback round trip",
			appEnv:       "production",
			appURL:       "https://forum.example.com",
			wantSecure:   true,
			wantSameSite: http.SameSiteNoneMode,
		},
		{
			name:         "https staging callback round trip",
			appEnv:       "staging",
			appURL:       "https://forum.example.com",
			wantSecure:   true,
			wantSameSite: http.SameSiteNoneMode,
		},
		{
			name:         "local http development",
			appEnv:       "development",
			appURL:       "http://127.0.0.1:3000",
			wantSecure:   false,
			wantSameSite: http.SameSiteLaxMode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &Controller{appEnv: test.appEnv, appURL: test.appURL}
			app := fiber.New()
			app.Get("/binding", func(c fiber.Ctx) error {
				_, err := controller.ensureExternalAuthBrowserBinding(c)
				return err
			})

			response, err := app.Test(httptest.NewRequest(http.MethodGet, "/binding", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			cookies := response.Cookies()
			if len(cookies) != 1 {
				t.Fatalf("cookies=%#v", cookies)
			}
			cookie := cookies[0]
			if cookie.Name != externalAuthBrowserCookieName || !cookie.HttpOnly || cookie.Path != "/" {
				t.Fatalf("cookie attributes=%#v", cookie)
			}
			if cookie.Secure != test.wantSecure || cookie.SameSite != test.wantSameSite {
				t.Fatalf("secure=%t sameSite=%v", cookie.Secure, cookie.SameSite)
			}
		})
	}
}

func TestT1E_CallbackExactArtifactMismatchRedirect(t *testing.T) {
	stateStore := identity.NewInMemoryCallbackStateStore()
	ctx := context.Background()
	digest := strings.Repeat("b", 64)
	providerID := "demo.auth"
	tx := identity.CallbackTransaction{
		State: "state-art", ProviderID: providerID, Operation: identity.ExternalAuthOperationLogin,
		OwnerExtensionID: "ext.demo.auth", OwnerExtensionVersion: "1.0.0",
		OwnerPackageDigest: digest, AbsoluteCallbackURL: "https://forum.example.com/cb",
		CodeVerifier: "v", CorrelationID: "c", RedirectPath: "/topics/2",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := stateStore.Save(ctx, tx); err != nil {
		t.Fatal(err)
	}
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: providerID, ContractVersion: providerID + "@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{Name: identity.AuthOperationLoginComplete}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("c", 64), VersionID: 1, RuntimeInstanceID: "rt",
		},
	}
	activation := identity.NewMemoryProviderActivationStore()
	login := true
	_, _ = activation.Upsert(ctx, identity.ProviderActivationInput{
		ProviderID: providerID, OwnerExtensionID: "ext.demo.auth", OwnerPackageDigest: digest,
		LoginEnabled: &login,
	})
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	controller := &Controller{
		callbackStateStore:  stateStore,
		externalAuthService: svc,
		authFlow:            &identity.AuthProviderFlow{},
	}
	app := fiber.New()
	app.Get("/api/v1/auth/providers/:providerId/callback", controller.externalAuthCallback)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/demo.auth/callback?state=state-art&code=x", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "auth.provider_callback_invalid") {
		t.Fatalf("artifact mismatch location=%s", loc)
	}
}

func TestT1E_ExternalIdentitiesDeniedWithoutSession(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	links := newT1ELinkStore(identity.ExternalIdentityLink{
		ID: 1, UserID: 9, ProviderID: "demo.auth", Status: identity.ExternalIdentityLinkStatusActive,
	})
	controller.externalLinkStore = links

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/external-identities", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unauthenticated list must deny")
	}
	if links.listCalls != 0 {
		t.Fatalf("denied path must not list links")
	}
}

func TestT1E_ExternalIdentitiesRedactedWhenAuthed(t *testing.T) {
	app, store, controller := newT1EExternalAuthApp(t)
	digest := strings.Repeat("e", 64)
	links := newT1ELinkStore(identity.ExternalIdentityLink{
		ID: 11, UserID: 1, ProviderID: "demo.auth", Status: identity.ExternalIdentityLinkStatusActive,
		OwnerExtensionID: "ext.demo.auth",
		LinkedAt:         time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC),
	})
	controller.externalLinkStore = links

	cookie := registerAndLogin(t, app)
	_ = store

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/external-identities", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), digest) || strings.Contains(string(body), "providerSubject") {
		t.Fatalf("response leaked digest/subject: %s", body)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("expected 1 link, got %#v", env.Data)
	}
	if env.Data[0]["providerId"] != "demo.auth" {
		t.Fatalf("providerId=%v", env.Data[0]["providerId"])
	}
	if _, ok := env.Data[0]["providerSubjectDigest"]; ok {
		t.Fatalf("must not expose digest field")
	}
}

func TestT1E_UnlinkAndPasswordDeniedWithoutSession(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{})
	controller.externalLinkStore = newT1ELinkStore()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/external-identities/1", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unlink without session must deny")
	}

	payload, _ := json.Marshal(map[string]any{"password": "correct horse battery staple"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("password setup without session must deny")
	}
}

func TestT1E_AdminProvidersDeniedWithoutPermission(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	activation := identity.NewMemoryProviderActivationStore()
	controller.activationStore = activation
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: activation,
	})

	cookie := registerAndLogin(t, app) // member, 无 identity.provider.manage

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/admin/identity/providers"},
		{http.MethodPost, "/api/v1/admin/identity/providers/reset"},
		{http.MethodPost, "/api/v1/admin/identity/providers/demo.auth/probe"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(cookie)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, resp.StatusCode, body)
		}
	}

	before, _ := activation.List(context.Background())
	payload, _ := json.Marshal(map[string]any{"expectedRevision": 0, "loginEnabled": true})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/identity/providers/demo.auth", bytes.NewReader(payload))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("patch denied status=%d", resp.StatusCode)
	}
	after, _ := activation.List(context.Background())
	if len(after) != len(before) {
		t.Fatalf("denied patch mutated activation catalog")
	}
}

func TestT1E_AdminProvidersAllowedWithPermission(t *testing.T) {
	app, store, controller := newT1EExternalAuthApp(t)
	activation := identity.NewMemoryProviderActivationStore()
	registry := identityregistry.New()
	digest := strings.Repeat("f", 64)
	// provider.ID 必须以 ExtensionID + "." 为前缀。
	if _, err := registry.Publish(identityregistry.Publication{
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "rt-1",
		},
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "ext.demo.identity@1",
			Providers: []identityregistry.Provider{{
				ID: "ext.demo.auth", ContractVersion: "ext.demo.auth@1",
				Kind: identityregistry.ProviderKindAuth, Handler: "ext.demo.identity",
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	controller.activationStore = activation
	controller.providerCatalog = registry
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: activation,
		ProviderContribution: func(id string) (identityregistry.ProviderContribution, error) {
			return registry.ResolveProvider(id)
		},
	})

	cookie := registerAndLogin(t, app)
	promoteProviderManage(t, store, 1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/identity/providers", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "clientSecret") || strings.Contains(string(body), "codeVerifier") {
		t.Fatalf("admin list leaked secrets: %s", body)
	}
}

func TestT1E_ExternalRegistrationTicketInvalid(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	tickets := identity.NewInMemoryRegistrationTicketStore()
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{})
	controller.registrationTicketStore = tickets

	payload, _ := json.Marshal(map[string]any{
		"ticket": "missing-ticket", "username": "newuser", "email": "new@example.com",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/external-registration", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "auth.external_registration_ticket") {
		t.Fatalf("body=%s", body)
	}
}

type t8dExternalLoginLinkStore struct {
	link identity.ExternalIdentityLink
}

func (s t8dExternalLoginLinkStore) Link(context.Context, identity.LinkExternalIdentityInput, identity.ExternalIdentityLinkCommitFence) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s t8dExternalLoginLinkStore) Unlink(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s t8dExternalLoginLinkStore) Erase(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}
func (s t8dExternalLoginLinkStore) Get(context.Context, int64) (identity.ExternalIdentityLink, error) {
	return s.link, nil
}
func (s t8dExternalLoginLinkStore) FindActive(_ context.Context, providerID, digest string) (identity.ExternalIdentityLink, error) {
	if s.link.ProviderID == providerID && digest != "" {
		return s.link, nil
	}
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}
func (s t8dExternalLoginLinkStore) ListUser(context.Context, int64) ([]identity.ExternalIdentityLink, error) {
	return []identity.ExternalIdentityLink{s.link}, nil
}

type t8dRiskSource struct {
	provider identityregistry.ProviderContribution
}

func (s t8dRiskSource) RiskProviders(context.Context) ([]identityregistry.ProviderContribution, error) {
	return []identityregistry.ProviderContribution{s.provider}, nil
}

type t8dRiskInvoker struct {
	purpose string
}

func (i *t8dRiskInvoker) InvokeExact(
	ctx context.Context,
	_ identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	input map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	if operation != "risk.evaluate" || actorUserID <= 0 {
		return identity.ErrRiskEvaluationUnavailable
	}
	i.purpose, _ = input["purpose"].(string)
	return accept(ctx, map[string]any{"disposition": identity.RiskDispositionAllow}, func() error { return nil })
}

// r1BlockingRiskInvoker 在生产 Controller 的风险评估点暂停，用于证明
// external-login effect fence 会在 session 持久化前重新读取 Host 授权状态。
type r1BlockingRiskInvoker struct {
	entered chan struct{}
	release chan struct{}
}

func (i *r1BlockingRiskInvoker) InvokeExact(
	ctx context.Context,
	_ identityregistry.ProviderContribution,
	operation string,
	actorUserID int64,
	_ map[string]any,
	accept func(context.Context, map[string]any, func() error) error,
) error {
	if operation != "risk.evaluate" || actorUserID <= 0 {
		return identity.ErrRiskEvaluationUnavailable
	}
	close(i.entered)
	select {
	case <-i.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return accept(ctx, map[string]any{"disposition": identity.RiskDispositionAllow}, func() error { return nil })
}

func TestT8D_ExternalLoginCallbackUsesCanonicalRiskPurpose(t *testing.T) {
	app, store, controller := newT1EExternalAuthApp(t)
	current, err := identity.NewService(store).Register(context.Background(), identity.RegisterInput{
		Username: "t8dextlogin", Email: "t8dextlogin@example.test", Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("seed linked user: %v", err)
	}
	digest := strings.Repeat("8", 64)
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{Name: identity.AuthOperationLoginComplete}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", ExtensionVersion: "1.0.0", PackageDigest: digest,
			RuntimeInstanceID: "runtime-demo-auth",
		},
	}
	activation := identity.NewMemoryProviderActivationStore()
	login := true
	if _, err := activation.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: live.ID, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: live.Artifact.PackageDigest, LoginEnabled: &login,
	}); err != nil {
		t.Fatal(err)
	}
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{
		LinkStore: t8dExternalLoginLinkStore{link: identity.ExternalIdentityLink{
			ID: 1, UserID: current.ID, ProviderID: "demo.auth", Status: identity.ExternalIdentityLinkStatusActive,
		}},
		ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
		LoadCurrentUser: func(context.Context, int64) (identity.CurrentUser, error) {
			return current, nil
		},
	})
	riskInvoker := &t8dRiskInvoker{}
	riskEvaluator, err := identity.NewRiskEvaluator(t8dRiskSource{provider: identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.risk", Kind: identityregistry.ProviderKindRisk,
			Operations: []identityregistry.ProviderOperation{{Name: "risk.evaluate"}},
		},
	}}, riskInvoker)
	if err != nil {
		t.Fatalf("risk evaluator: %v", err)
	}
	controller.WithRiskEvaluator(riskEvaluator)

	app.Get("/t8d/external-login-callback", func(c fiber.Ctx) error {
		return controller.handleExternalLoginCallback(c, identity.CallbackTransaction{}, identity.ExternalAuthAssertion{
			ProviderID: "demo.auth", Operation: identity.ExternalAuthOperationLogin,
			SubjectDigest: digest, OwnerExtensionID: live.Artifact.ExtensionID,
			OwnerExtensionVersion: live.Artifact.ExtensionVersion, OwnerPackageDigest: digest,
			ProviderContractVersion: live.ContractVersion,
		}, "/after-login")
	})

	req := httptest.NewRequest(http.MethodGet, "/t8d/external-login-callback", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "auth.external_login_ok") {
		t.Fatalf("external login redirect=%s", loc)
	}
	if riskInvoker.purpose != "login" {
		t.Fatalf("risk purpose=%q want login", riskInvoker.purpose)
	}
	if len(resp.Cookies()) != 1 {
		t.Fatalf("external login cookies=%#v", resp.Cookies())
	}
}

type r1LiveProvider struct {
	mu   sync.RWMutex
	live identityregistry.ProviderContribution
	err  error
}

func (p *r1LiveProvider) resolve(string) (identityregistry.ProviderContribution, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.live, p.err
}

func (p *r1LiveProvider) replace(live identityregistry.ProviderContribution, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live, p.err = live, err
}

type r1RecentAuthMarker struct{ writes atomic.Int64 }

func (m *r1RecentAuthMarker) MarkSessionRecentlyAuthenticated(context.Context, int64, string, string, string, time.Duration) error {
	m.writes.Add(1)
	return nil
}

func TestR1_ExternalLoginEffectFenceBlocksEveryAuthorizationRevocationDuringRisk(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, activation *identity.MemoryProviderActivationStore, live *r1LiveProvider, original identityregistry.ProviderContribution, safeMode *atomic.Bool)
	}{
		{name: "safe_mode", mutate: func(_ *testing.T, _ *identity.MemoryProviderActivationStore, _ *r1LiveProvider, _ identityregistry.ProviderContribution, safeMode *atomic.Bool) {
			safeMode.Store(true)
		}},
		{name: "login_activation_off", mutate: func(t *testing.T, activation *identity.MemoryProviderActivationStore, _ *r1LiveProvider, original identityregistry.ProviderContribution, _ *atomic.Bool) {
			current, err := activation.Get(t.Context(), original.ID)
			if err != nil {
				t.Fatal(err)
			}
			off := false
			if _, err := activation.Upsert(t.Context(), identity.ProviderActivationInput{ProviderID: original.ID, OwnerExtensionID: original.Artifact.ExtensionID, OwnerPackageDigest: original.Artifact.PackageDigest, LoginEnabled: &off, ExpectedRevision: current.Revision}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "live_publication_revoked", mutate: func(_ *testing.T, _ *identity.MemoryProviderActivationStore, live *r1LiveProvider, original identityregistry.ProviderContribution, _ *atomic.Bool) {
			live.replace(original, identity.ErrAuthProviderNotFound)
		}},
		{name: "artifact_digest_replaced", mutate: func(_ *testing.T, _ *identity.MemoryProviderActivationStore, live *r1LiveProvider, original identityregistry.ProviderContribution, _ *atomic.Bool) {
			replaced := original
			replaced.Artifact.PackageDigest = strings.Repeat("a", 64)
			replaced.Artifact.ExtensionVersion = "2.0.0"
			live.replace(replaced, nil)
		}},
		{name: "contract_replaced", mutate: func(_ *testing.T, _ *identity.MemoryProviderActivationStore, live *r1LiveProvider, original identityregistry.ProviderContribution, _ *atomic.Bool) {
			replaced := original
			replaced.Provider.ContractVersion = "provider.alpha@2"
			live.replace(replaced, nil)
		}},
		{name: "login_operation_removed", mutate: func(_ *testing.T, _ *identity.MemoryProviderActivationStore, live *r1LiveProvider, original identityregistry.ProviderContribution, _ *atomic.Bool) {
			replaced := original
			replaced.Provider.Operations = nil
			live.replace(replaced, nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, store, controller := newT1EExternalAuthApp(t)
			current, err := identity.NewService(store).Register(t.Context(), identity.RegisterInput{Username: "r1fence", Email: "r1fence@example.test", Password: "correct horse battery staple"})
			if err != nil {
				t.Fatalf("seed linked user: %v", err)
			}
			digest := strings.Repeat("9", 64)
			original := identityregistry.ProviderContribution{Provider: identityregistry.Provider{ID: "provider.alpha", ContractVersion: "provider.alpha@1", Kind: identityregistry.ProviderKindAuth, Operations: []identityregistry.ProviderOperation{{Name: identity.AuthOperationLoginComplete}}}, Artifact: identityregistry.Artifact{ExtensionID: "ext.provider.alpha", ExtensionVersion: "1.0.0", PackageDigest: digest, RuntimeInstanceID: "runtime-provider-alpha"}}
			activation := identity.NewMemoryProviderActivationStore()
			on := true
			if _, err := activation.Upsert(t.Context(), identity.ProviderActivationInput{ProviderID: original.ID, OwnerExtensionID: original.Artifact.ExtensionID, OwnerPackageDigest: digest, LoginEnabled: &on}); err != nil {
				t.Fatal(err)
			}
			live := &r1LiveProvider{live: original}
			var safeMode atomic.Bool
			recent := &r1RecentAuthMarker{}
			controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{LinkStore: t8dExternalLoginLinkStore{link: identity.ExternalIdentityLink{ID: 1, UserID: current.ID, ProviderID: original.ID, Status: identity.ExternalIdentityLinkStatusActive}}, ActivationStore: activation, SafeMode: safeMode.Load, ProviderContribution: live.resolve, LoadCurrentUser: func(context.Context, int64) (identity.CurrentUser, error) { return current, nil }, RecentAuthMarker: recent})
			risk := &r1BlockingRiskInvoker{entered: make(chan struct{}), release: make(chan struct{})}
			evaluator, err := identity.NewRiskEvaluator(t8dRiskSource{provider: identityregistry.ProviderContribution{Provider: identityregistry.Provider{ID: "risk.fence", Kind: identityregistry.ProviderKindRisk, Operations: []identityregistry.ProviderOperation{{Name: "risk.evaluate"}}}}}, risk)
			if err != nil {
				t.Fatal(err)
			}
			controller.WithRiskEvaluator(evaluator)
			app.Get("/r1/external-login", func(c fiber.Ctx) error {
				return controller.handleExternalLoginCallback(c, identity.CallbackTransaction{}, identity.ExternalAuthAssertion{ProviderID: original.ID, Operation: identity.ExternalAuthOperationLogin, SubjectDigest: digest, OwnerExtensionID: original.Artifact.ExtensionID, OwnerExtensionVersion: original.Artifact.ExtensionVersion, OwnerPackageDigest: digest, ProviderContractVersion: original.ContractVersion}, "/after-login")
			})
			result := make(chan *http.Response, 1)
			failures := make(chan error, 1)
			go func() {
				response, requestErr := app.Test(httptest.NewRequest(http.MethodGet, "/r1/external-login", nil))
				if requestErr != nil {
					failures <- requestErr
					return
				}
				result <- response
			}()
			select {
			case <-risk.entered:
			case err := <-failures:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("risk evaluation did not block")
			}
			tc.mutate(t, activation, live, original, &safeMode)
			close(risk.release)
			select {
			case response := <-result:
				defer response.Body.Close()
				if response.StatusCode != http.StatusFound {
					t.Fatalf("status=%d", response.StatusCode)
				}
				if location := response.Header.Get("Location"); strings.Contains(location, "auth.external_login_ok") {
					t.Fatalf("stale external authorization issued login: %s", location)
				}
				if cookies := response.Cookies(); len(cookies) != 0 {
					t.Fatalf("denied effect created session cookies: %#v", cookies)
				}
			case err := <-failures:
				t.Fatal(err)
			case <-time.After(3 * time.Second):
				t.Fatal("blocked callback did not finish")
			}
			store.mu.Lock()
			sessions, audits := len(store.sessions), store.loginAudits
			store.mu.Unlock()
			if sessions != 0 || audits != 0 || recent.writes.Load() != 0 {
				t.Fatalf("denied effect wrote session=%d audit=%d recentAuth=%d", sessions, audits, recent.writes.Load())
			}
		})
	}
}

func TestT1E_ReservedCallbackRouteRegistered(t *testing.T) {
	controller := &Controller{}
	app := fiber.New()
	api := app.Group("/api/v1")
	controller.RegisterRoutes(api)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/demo.auth/callback", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("callback route not registered")
	}
	// 无 service 时 302 到 login
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect when stack unavailable, got %d", resp.StatusCode)
	}
}
