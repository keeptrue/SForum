package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func externalRegistrationContinuationHarness(
	t *testing.T,
	registrationActivated bool,
	registrationOpen bool,
	linkActivated bool,
) (*Controller, *identity.ExternalAuthService, identity.ExternalAuthAssertion) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{
				{Name: identity.AuthOperationLoginComplete},
				{Name: identity.AuthOperationRegistrationComplete},
				{Name: identity.AuthOperationLinkComplete},
			},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1, RuntimeInstanceID: "runtime-demo-auth",
		},
	}
	activation := identity.NewMemoryProviderActivationStore()
	loginEnabled := true
	if _, err := activation.Upsert(t.Context(), identity.ProviderActivationInput{
		ProviderID: live.ID, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: live.Artifact.PackageDigest,
		LoginEnabled:       &loginEnabled, RegistrationEnabled: &registrationActivated,
		LinkEnabled: &linkActivated,
	}); err != nil {
		t.Fatal(err)
	}
	svc := identity.NewExternalAuthService(identity.ExternalAuthDeps{
		LinkStore:       newT1ELinkStore(),
		ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
		AnyUserExists: func(context.Context) (bool, error) { return true, nil },
		RegistrationEnabled: func(context.Context) (bool, error) {
			return registrationOpen, nil
		},
	})
	controller := &Controller{
		externalAuthService:     svc,
		registrationTicketStore: identity.NewInMemoryRegistrationTicketStore(),
	}
	assertion := identity.ExternalAuthAssertion{
		ProviderID: live.ID, ProviderContractVersion: live.ContractVersion,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerExtensionVersion: live.Artifact.ExtensionVersion,
		OwnerPackageDigest: digest, Operation: identity.ExternalAuthOperationLogin,
		SubjectDigest: digest, CorrelationID: "login-continuation",
		UsernameHint: "octocat", DisplayName: "The Octocat",
		EmailHint: "octocat@example.com", EmailVerified: true,
	}
	return controller, svc, assertion
}

type continuationRecentAuth struct{ allowed bool }

func (r continuationRecentAuth) IsSessionRecentlyAuthenticated(context.Context, int64, string) (bool, error) {
	return r.allowed, nil
}

type continuationLinkStore struct {
	linkCalls int
	existing  *identity.ExternalIdentityLink
}

func (s *continuationLinkStore) Link(_ context.Context, input identity.LinkExternalIdentityInput, fence identity.ExternalIdentityLinkCommitFence) (identity.ExternalIdentityLinkMutation, error) {
	if fence != nil {
		if err := fence(); err != nil {
			return identity.ExternalIdentityLinkMutation{}, err
		}
	}
	s.linkCalls++
	return identity.ExternalIdentityLinkMutation{Link: identity.ExternalIdentityLink{
		ID: 71, UserID: input.UserID, ProviderID: input.Provider.ID,
		Status: identity.ExternalIdentityLinkStatusActive,
	}}, nil
}

func (s *continuationLinkStore) Unlink(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}

func (s *continuationLinkStore) Erase(context.Context, identity.TransitionExternalIdentityLinkInput) (identity.ExternalIdentityLinkMutation, error) {
	return identity.ExternalIdentityLinkMutation{}, nil
}

func (s *continuationLinkStore) Get(context.Context, int64) (identity.ExternalIdentityLink, error) {
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}

func (s *continuationLinkStore) FindActive(context.Context, string, string) (identity.ExternalIdentityLink, error) {
	if s.existing != nil {
		return *s.existing, nil
	}
	return identity.ExternalIdentityLink{}, identity.ErrExternalIdentityLinkNotFound
}

func (s *continuationLinkStore) ListUser(context.Context, int64) ([]identity.ExternalIdentityLink, error) {
	return nil, nil
}

func configureAuthenticatedContinuationTest(
	t *testing.T,
	recent bool,
	existing *identity.ExternalIdentityLink,
) (*fiber.App, *Controller, *continuationLinkStore, *http.Cookie, *http.Cookie, string) {
	t.Helper()
	app, store, controller := newT1EExternalAuthApp(t)
	sessionCookie := registerAndLogin(t, app)
	browserCookie, browserDigest := externalAuthBrowserCookieForTest()
	digest := strings.Repeat("a", 64)
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "demo.auth", ContractVersion: "demo.auth@1", Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{
				{Name: identity.AuthOperationLoginComplete}, {Name: identity.AuthOperationLinkComplete},
			},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.demo.auth", ExtensionVersion: "1.0.0", PackageDigest: digest,
			VersionID: 1, RuntimeInstanceID: "runtime-demo-auth",
		},
	}
	activation := identity.NewMemoryProviderActivationStore()
	enabled := true
	if _, err := activation.Upsert(t.Context(), identity.ProviderActivationInput{
		ProviderID: live.ID, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: live.Artifact.PackageDigest, LoginEnabled: &enabled, LinkEnabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	links := &continuationLinkStore{existing: existing}
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{
		LinkStore: links, ActivationStore: activation, RecentAuth: continuationRecentAuth{allowed: recent},
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) { return live, nil },
		LoadCurrentUser:      store.GetCurrentUser,
	})
	tickets := identity.NewInMemoryRegistrationTicketStore()
	controller.registrationTicketStore = tickets
	token := "continuation-ticket"
	now := time.Now()
	if err := tickets.Save(t.Context(), identity.RegistrationTicket{
		Token: token, ProviderID: live.ID, ProviderContractVersion: live.ContractVersion,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerExtensionVersion: live.Artifact.ExtensionVersion,
		OwnerPackageDigest: live.Artifact.PackageDigest, Operation: identity.ExternalAuthOperationLogin,
		SourceOperation: identity.ExternalAuthOperationLogin, BrowserBindingDigest: browserDigest,
		ProviderSubject: "subject-1", CorrelationID: "continuation-http",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	return app, controller, links, sessionCookie, browserCookie, token
}

func continuationPOST(t *testing.T, app *fiber.App, path, token string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"ticket": token})
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func externalAuthBrowserCookieForTest() (*http.Cookie, string) {
	raw := strings.Repeat("b", 43)
	return &http.Cookie{Name: externalAuthBrowserCookieName, Value: raw}, identity.ExternalAuthBrowserBindingDigest(raw)
}

func TestExternalLoginUnlinkedContinuesIntoExistingRegistration(t *testing.T) {
	controller, svc, assertion := externalRegistrationContinuationHarness(t, true, true, true)
	browserCookie, browserDigest := externalAuthBrowserCookieForTest()
	app := fiber.New()
	app.Get("/callback", func(c fiber.Ctx) error {
		return controller.handleExternalLoginCallback(c, identity.CallbackTransaction{BrowserBindingDigest: browserDigest}, assertion, "/topics")
	})
	request := httptest.NewRequest(http.MethodGet, "/callback", nil)
	request.AddCookie(browserCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil || location.Path != "/auth/continue" {
		t.Fatalf("location=%q err=%v", response.Header.Get("Location"), err)
	}
	token := location.Query().Get("ticket")
	if token == "" || location.Query().Get("redirect") != "/topics" {
		t.Fatalf("continuation query=%v", location.Query())
	}
	ticket, err := controller.registrationTicketStore.Inspect(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Operation != identity.ExternalAuthOperationLogin || ticket.SourceOperation != identity.ExternalAuthOperationLogin || ticket.BrowserBindingDigest != browserDigest {
		t.Fatalf("ticket operations target=%q source=%q", ticket.Operation, ticket.SourceOperation)
	}
	preparation, err := svc.PrepareExternalRegistration(t.Context(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.UsernameHint != "octocat" || preparation.EmailHint != "octocat@example.com" || !preparation.EmailVerified {
		t.Fatalf("preparation=%#v", preparation)
	}
	if _, err := controller.registrationTicketStore.Consume(t.Context(), token); err != nil {
		t.Fatalf("prepare consumed ticket: %v", err)
	}
}

func TestExternalLoginUnlinkedContinuationFailsClosed(t *testing.T) {
	cases := []struct {
		name                  string
		registrationActivated bool
		registrationOpen      bool
		linkActivated         bool
		wantReason            string
	}{
		{name: "both terminal operations disabled", registrationActivated: false, registrationOpen: true, linkActivated: false, wantReason: "auth.provider_not_enabled"},
		{name: "site registration closed and link disabled", registrationActivated: true, registrationOpen: false, linkActivated: false, wantReason: "auth.provider_not_enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, _, assertion := externalRegistrationContinuationHarness(t, tc.registrationActivated, tc.registrationOpen, tc.linkActivated)
			browserCookie, browserDigest := externalAuthBrowserCookieForTest()
			app := fiber.New()
			app.Get("/callback", func(c fiber.Ctx) error {
				return controller.handleExternalLoginCallback(c, identity.CallbackTransaction{BrowserBindingDigest: browserDigest}, assertion, "/topics")
			})
			request := httptest.NewRequest(http.MethodGet, "/callback", nil)
			request.AddCookie(browserCookie)
			response, err := app.Test(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			location := response.Header.Get("Location")
			if !strings.HasPrefix(location, "/login?") || !strings.Contains(location, "ext_auth="+url.QueryEscape(tc.wantReason)) || strings.Contains(location, "ticket=") {
				t.Fatalf("location=%q", location)
			}
		})
	}
}

func TestExternalLoginContinuationStillOffersExistingAccountWhenRegistrationClosed(t *testing.T) {
	controller, _, assertion := externalRegistrationContinuationHarness(t, true, false, true)
	browserCookie, browserDigest := externalAuthBrowserCookieForTest()
	app := fiber.New()
	app.Get("/callback", func(c fiber.Ctx) error {
		return controller.handleExternalLoginCallback(c, identity.CallbackTransaction{BrowserBindingDigest: browserDigest}, assertion, "/topics")
	})
	request := httptest.NewRequest(http.MethodGet, "/callback", nil)
	request.AddCookie(browserCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	location, _ := url.Parse(response.Header.Get("Location"))
	if response.StatusCode != http.StatusFound || location.Path != "/auth/continue" {
		t.Fatalf("status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestExternalRegistrationPreparationIsRedactedAndNonConsuming(t *testing.T) {
	controller, _, assertion := externalRegistrationContinuationHarness(t, true, true, true)
	assertion.EmailVerified = false
	browserCookie, browserDigest := externalAuthBrowserCookieForTest()
	var target string
	app := fiber.New()
	app.Get("/issue", func(c fiber.Ctx) error {
		var issueErr error
		target, issueErr = controller.createExternalIdentityContinuation(c, assertion, "/", browserDigest)
		return issueErr
	})
	issueRequest := httptest.NewRequest(http.MethodGet, "/issue", nil)
	issueRequest.AddCookie(browserCookie)
	issueResponse, err := app.Test(issueRequest)
	if err != nil || issueResponse.StatusCode != http.StatusOK {
		t.Fatalf("issue status=%d err=%v", issueResponse.StatusCode, err)
	}
	issueResponse.Body.Close()
	parsed, _ := url.Parse(target)
	token := parsed.Query().Get("ticket")
	app = fiber.New()
	app.Post("/prepare", controller.externalRegistrationPreparation)
	payload, _ := json.Marshal(map[string]string{"ticket": token})
	request := httptest.NewRequest(http.MethodPost, "/prepare", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(browserCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	raw := string(body)
	for _, forbidden := range []string{"providerSubject", "subjectDigest", "ownerPackageDigest", assertion.SubjectDigest, assertion.EmailHint} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("preparation leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"usernameHint":"octocat"`) || !strings.Contains(raw, `"emailVerified":false`) {
		t.Fatalf("preparation body=%s", raw)
	}
	if _, err := controller.registrationTicketStore.Consume(t.Context(), token); err != nil {
		t.Fatalf("prepare consumed ticket: %v", err)
	}
}

func TestExternalContinuationBrowserBindingRejectsCopiedTicketWithoutConsumption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{name: "missing cookie"},
		{name: "wrong cookie", cookies: []*http.Cookie{{Name: externalAuthBrowserCookieName, Value: strings.Repeat("c", 43)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, controller, links, sessionCookie, _, token := configureAuthenticatedContinuationTest(t, true, nil)
			prepare := continuationPOST(t, app, "/api/v1/auth/external-continuation/prepare", token, tc.cookies...)
			prepare.Body.Close()
			if prepare.StatusCode != http.StatusNotFound {
				t.Fatalf("prepare status=%d", prepare.StatusCode)
			}

			linkCookies := append([]*http.Cookie{sessionCookie}, tc.cookies...)
			link := continuationPOST(t, app, "/api/v1/auth/external-continuation/link", token, linkCookies...)
			link.Body.Close()
			if link.StatusCode != http.StatusNotFound {
				t.Fatalf("link status=%d", link.StatusCode)
			}
			if links.linkCalls != 0 {
				t.Fatalf("copied ticket wrote link")
			}
			if _, err := controller.registrationTicketStore.Inspect(t.Context(), token); err != nil {
				t.Fatalf("rejected copied ticket was consumed: %v", err)
			}
		})
	}
}

func TestExternalContinuationPreparationAcceptsMatchingBrowser(t *testing.T) {
	app, controller, _, _, browserCookie, token := configureAuthenticatedContinuationTest(t, true, nil)
	response := continuationPOST(t, app, "/api/v1/auth/external-continuation/prepare", token, browserCookie)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"canLinkExisting":true`) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if _, err := controller.registrationTicketStore.Inspect(t.Context(), token); err != nil {
		t.Fatalf("prepare consumed ticket: %v", err)
	}
}

func TestExternalContinuationLinkRequiresAuthenticatedRecentSession(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		app, controller, links, _, browserCookie, token := configureAuthenticatedContinuationTest(t, true, nil)
		response := continuationPOST(t, app, "/api/v1/auth/external-continuation/link", token, browserCookie)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
		if links.linkCalls != 0 {
			t.Fatal("unauthenticated request wrote link")
		}
		if _, err := controller.registrationTicketStore.Inspect(t.Context(), token); err != nil {
			t.Fatalf("unauthenticated request consumed ticket: %v", err)
		}
	})

	t.Run("stale recent authentication", func(t *testing.T) {
		app, controller, links, sessionCookie, browserCookie, token := configureAuthenticatedContinuationTest(t, false, nil)
		response := continuationPOST(t, app, "/api/v1/auth/external-continuation/link", token, sessionCookie, browserCookie)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
		if links.linkCalls != 0 {
			t.Fatal("stale recent authentication wrote link")
		}
		if _, err := controller.registrationTicketStore.Inspect(t.Context(), token); err != nil {
			t.Fatalf("stale recent authentication consumed ticket: %v", err)
		}
	})
}

func TestExternalContinuationLinkBindsCurrentUser(t *testing.T) {
	app, controller, links, sessionCookie, browserCookie, token := configureAuthenticatedContinuationTest(t, true, nil)
	response := continuationPOST(t, app, "/api/v1/auth/external-continuation/link", token, sessionCookie, browserCookie)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"id":1`) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if links.linkCalls != 1 {
		t.Fatalf("linkCalls=%d", links.linkCalls)
	}
	if _, err := controller.registrationTicketStore.Inspect(t.Context(), token); err == nil {
		t.Fatal("successful continuation left ticket reusable")
	}
}

func TestExternalContinuationLinkRejectsSubjectOwnedByAnotherUser(t *testing.T) {
	existing := &identity.ExternalIdentityLink{
		ID: 70, UserID: 99, ProviderID: "demo.auth", Status: identity.ExternalIdentityLinkStatusActive,
	}
	app, _, links, sessionCookie, browserCookie, token := configureAuthenticatedContinuationTest(t, true, existing)
	response := continuationPOST(t, app, "/api/v1/auth/external-continuation/link", token, sessionCookie, browserCookie)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "auth.external_subject_conflict") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if links.linkCalls != 0 {
		t.Fatal("subject conflict wrote link")
	}
}
