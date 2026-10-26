package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestRoleSuggestionControllerCookieAuthorityListAndDecision(t *testing.T) {
	registry := &roleSuggestionControllerRegistry{
		page: identityregistry.RoleSuggestionPage{Items: []identityregistry.RoleSuggestion{{
			ID: 41, PermissionKey: "demo.publish", OwnerExtensionID: "demo.identity",
			RoleKey: "member", ApprovalState: identityregistry.RoleSuggestionPending, Revision: 1,
		}}},
		decision: identityregistry.RoleSuggestion{
			ID: 41, PermissionKey: "demo.publish", OwnerExtensionID: "demo.identity",
			RoleKey: "member", ApprovalState: identityregistry.RoleSuggestionApproved, Applied: true, Revision: 2,
		},
	}
	app := newRoleSuggestionControllerTestApp(t, registry, map[int64]identity.Actor{
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
	})
	cookie := loginRoleSuggestionControllerUser(t, app, 7)

	response := roleSuggestionControllerRequest(t, app, http.MethodGet,
		"/api/v1/roles/suggestions?approvalState=pending&roleKey=member&permissionKey=demo.publish&ownerExtensionId=demo.identity&limit=25&cursor=cursor-1",
		cookie, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", response.StatusCode)
	}
	var page roleSuggestionTestEnvelope[identityregistry.RoleSuggestionPage]
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(page.Data.Items) != 1 || registry.pageInput.Filter.Limit != 25 ||
		registry.pageInput.Filter.ApprovalState != "pending" || registry.pageInput.Filter.RoleKey != "member" ||
		registry.pageInput.Filter.PermissionKey != "demo.publish" ||
		registry.pageInput.Filter.OwnerExtensionID != "demo.identity" || registry.pageInput.Cursor != "cursor-1" {
		t.Fatalf("page=%#v input=%#v", page.Data, registry.pageInput)
	}
	if page.Data.Items[0].Applied || registry.decisionCalls != 0 {
		t.Fatalf("listing suggestions granted authority: item=%#v decisions=%d", page.Data.Items[0], registry.decisionCalls)
	}

	response = roleSuggestionControllerRequest(t, app, http.MethodPost,
		"/api/v1/roles/suggestions/41/decision", cookie,
		[]byte(`{"expectedRevision":1,"approvalState":"approved"}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("decision status=%d", response.StatusCode)
	}
	var decided roleSuggestionTestEnvelope[identityregistry.RoleSuggestion]
	if err := json.NewDecoder(response.Body).Decode(&decided); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !decided.Data.Applied || registry.decisionInput.ID != 41 ||
		registry.decisionInput.ExpectedRevision != 1 || registry.decisionInput.ApprovalState != "approved" ||
		registry.decisionInput.ActorUserID != 7 {
		t.Fatalf("decision=%#v input=%#v", decided.Data, registry.decisionInput)
	}
}

func TestRoleSuggestionControllerRejectsMissingCookieAndDeniedActor(t *testing.T) {
	registry := &roleSuggestionControllerRegistry{}
	app := newRoleSuggestionControllerTestApp(t, registry, map[int64]identity.Actor{
		8: {ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		9: {ID: 9, Status: identity.UserStatusDisabled, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
	})

	response := roleSuggestionControllerRequest(t, app, http.MethodGet, "/api/v1/roles/suggestions", nil, nil)
	assertRoleSuggestionError(t, response, http.StatusUnauthorized, "auth.required")
	if registry.pageCalls != 0 {
		t.Fatalf("repository called without cookie: %d", registry.pageCalls)
	}

	for _, userID := range []int64{8, 9} {
		cookie := loginRoleSuggestionControllerUser(t, app, userID)
		response = roleSuggestionControllerRequest(t, app, http.MethodGet, "/api/v1/roles/suggestions", cookie, nil)
		assertRoleSuggestionError(t, response, http.StatusForbidden, "permission.denied")
		response = roleSuggestionControllerRequest(t, app, http.MethodPost,
			"/api/v1/roles/suggestions/1/decision", cookie,
			[]byte(`{"expectedRevision":1,"approvalState":"approved"}`))
		assertRoleSuggestionError(t, response, http.StatusForbidden, "permission.denied")
	}
	if registry.pageCalls != 0 || registry.decisionCalls != 0 {
		t.Fatalf("repository calls after denied actor: page=%d decision=%d", registry.pageCalls, registry.decisionCalls)
	}
}

func TestRoleSuggestionControllerRejectsPersonalAccessTokenAuthority(t *testing.T) {
	registry := &roleSuggestionControllerRegistry{}
	app := newRoleSuggestionControllerTestApp(t, registry, map[int64]identity.Actor{
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
	})
	cookie := loginRoleSuggestionControllerUser(t, app, 7)

	for _, test := range []struct {
		name   string
		method string
		path   string
		cookie *http.Cookie
		body   []byte
	}{
		{name: "PAT list", method: http.MethodGet, path: "/api/v1/roles/suggestions"},
		{name: "PAT decision", method: http.MethodPost, path: "/api/v1/roles/suggestions/1/decision",
			body: []byte(`{"expectedRevision":1,"approvalState":"approved"}`)},
		{name: "mixed PAT and cookie list", method: http.MethodGet, path: "/api/v1/roles/suggestions", cookie: cookie},
		{name: "mixed PAT and cookie decision", method: http.MethodPost, path: "/api/v1/roles/suggestions/1/decision", cookie: cookie,
			body: []byte(`{"expectedRevision":1,"approvalState":"approved"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := roleSuggestionControllerPATRequest(t, app, test.method, test.path, test.cookie, test.body)
			assertRoleSuggestionError(t, response, http.StatusForbidden, "identity.role_suggestion.cookie_required")
		})
	}
	if registry.pageCalls != 0 || registry.decisionCalls != 0 {
		t.Fatalf("PAT reached role suggestion repository: page=%d decision=%d", registry.pageCalls, registry.decisionCalls)
	}
}

func TestRoleSuggestionControllerFailsClosedWithoutReviewStore(t *testing.T) {
	app := newRoleSuggestionControllerTestApp(t, nil, map[int64]identity.Actor{
		12: {ID: 12, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
	})
	cookie := loginRoleSuggestionControllerUser(t, app, 12)
	response := roleSuggestionControllerRequest(t, app, http.MethodGet, "/api/v1/roles/suggestions", cookie, nil)
	assertRoleSuggestionError(t, response, http.StatusServiceUnavailable, "identity.registry_unavailable")
}

func TestRoleSuggestionControllerRejectsInvalidRequestsBeforeRepository(t *testing.T) {
	registry := &roleSuggestionControllerRegistry{}
	app := newRoleSuggestionControllerTestApp(t, registry, map[int64]identity.Actor{
		9: {ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
	})
	cookie := loginRoleSuggestionControllerUser(t, app, 9)

	for _, path := range []string{
		"/api/v1/roles/suggestions?limit=0",
		"/api/v1/roles/suggestions?limit=101",
		"/api/v1/roles/suggestions?limit=invalid",
	} {
		response := roleSuggestionControllerRequest(t, app, http.MethodGet, path, cookie, nil)
		assertRoleSuggestionError(t, response, http.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	}
	for _, test := range []struct {
		path string
		body string
	}{
		{path: "/api/v1/roles/suggestions/invalid/decision", body: `{"expectedRevision":1,"approvalState":"approved"}`},
		{path: "/api/v1/roles/suggestions/1/decision", body: ``},
		{path: "/api/v1/roles/suggestions/1/decision", body: `{"expectedRevision":0,"approvalState":"approved"}`},
		{path: "/api/v1/roles/suggestions/1/decision", body: `{"expectedRevision":1,"approvalState":"pending"}`},
		{path: "/api/v1/roles/suggestions/1/decision", body: `{"expectedRevision":1,"approvalState":"approved","actorUserId":99}`},
		{path: "/api/v1/roles/suggestions/1/decision", body: `{"expectedRevision":1,"approvalState":"approved"}{}`},
	} {
		response := roleSuggestionControllerRequest(t, app, http.MethodPost, test.path, cookie, []byte(test.body))
		assertRoleSuggestionError(t, response, http.StatusUnprocessableEntity, "identity.role_suggestion.invalid")
	}
	if registry.pageCalls != 0 || registry.decisionCalls != 0 {
		t.Fatalf("repository calls after invalid input: page=%d decision=%d", registry.pageCalls, registry.decisionCalls)
	}
}

func TestRoleSuggestionControllerMapsStaleAndCASConflicts(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason string
	}{
		{name: "stale artifact", err: identityregistry.ErrStale, reason: "identity.role_suggestion.stale"},
		{name: "cas", err: identityregistry.ErrRevisionConflict, reason: "identity.role_suggestion.revision_conflict"},
		{name: "target", err: identityregistry.ErrTargetConflict, reason: "identity.role_suggestion.target_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &roleSuggestionControllerRegistry{decisionErr: test.err}
			app := newRoleSuggestionControllerTestApp(t, registry, map[int64]identity.Actor{
				10: {ID: 10, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionRoleManage: true}},
			})
			cookie := loginRoleSuggestionControllerUser(t, app, 10)
			response := roleSuggestionControllerRequest(t, app, http.MethodPost,
				"/api/v1/roles/suggestions/2/decision", cookie,
				[]byte(`{"expectedRevision":1,"approvalState":"approved"}`))
			assertRoleSuggestionError(t, response, http.StatusConflict, test.reason)
		})
	}
}

type roleSuggestionControllerIdentityStore struct {
	passwordResetFakeStore
	actors map[int64]identity.Actor
}

func (s roleSuggestionControllerIdentityStore) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	actor, found := s.actors[userID]
	if !found {
		return identity.Actor{}, identity.ErrUserNotFound
	}
	return actor, nil
}

type roleSuggestionControllerRegistry struct {
	page          identityregistry.RoleSuggestionPage
	pageInput     identityregistry.RoleSuggestionPageInput
	pageCalls     int
	pageErr       error
	decision      identityregistry.RoleSuggestion
	decisionInput identityregistry.DecideRoleSuggestionInput
	decisionCalls int
	decisionErr   error
}

func (*roleSuggestionControllerRegistry) LoadDurableState(context.Context) (identityregistry.DurableState, error) {
	return identityregistry.DurableState{}, nil
}

func (s *roleSuggestionControllerRegistry) ListRoleSuggestionPage(_ context.Context, input identityregistry.RoleSuggestionPageInput) (identityregistry.RoleSuggestionPage, error) {
	s.pageCalls++
	s.pageInput = input
	return s.page, s.pageErr
}

func (s *roleSuggestionControllerRegistry) ListRoleSuggestions(context.Context, identityregistry.RoleSuggestionFilter) ([]identityregistry.RoleSuggestion, error) {
	return nil, nil
}

func (s *roleSuggestionControllerRegistry) DecideRoleSuggestion(_ context.Context, input identityregistry.DecideRoleSuggestionInput) (identityregistry.RoleSuggestion, error) {
	s.decisionCalls++
	s.decisionInput = input
	return s.decision, s.decisionErr
}

type roleSuggestionRouteProviderFunc func(fiber.Router)

func (f roleSuggestionRouteProviderFunc) RegisterRoutes(api fiber.Router) { f(api) }

func newRoleSuggestionControllerTestApp(
	t *testing.T,
	registry identityregistry.Store,
	actors map[int64]identity.Actor,
) *fiber.App {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "role-suggestion-test"})
	service := identity.NewService(roleSuggestionControllerIdentityStore{actors: actors}).WithIdentityRegistryStore(registry)
	controller := NewControllerWithAuthSessions(service, manager, humanverify.NewDisabledService())
	testRoutes := roleSuggestionRouteProviderFunc(func(api fiber.Router) {
		controller.RegisterRoutes(api)
		api.Post("/test/role-suggestion-login/:id", func(c fiber.Ctx) error {
			userID, err := strconv.ParseInt(c.Params("id"), 10, 64)
			if err != nil {
				return err
			}
			_, err = manager.Start(c, userID)
			return err
		})
	})
	return apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{testRoutes},
		BearerTokens:   roleSuggestionBearerAuthenticator{},
	})
}

type roleSuggestionBearerAuthenticator struct{}

func (roleSuggestionBearerAuthenticator) AuthenticatePlaintext(_ fiber.Ctx, plaintext string) (apitokens.Authenticated, error) {
	if plaintext != "sft_role_suggestion_test" {
		return apitokens.Authenticated{}, apitokens.ErrTokenInvalid
	}
	return apitokens.Authenticated{
		UserID: 7, TokenID: 91, PublicID: "role-suggestion-test", Scopes: []string{identity.PermissionRoleManage},
	}, nil
}

func loginRoleSuggestionControllerUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	response := roleSuggestionControllerRequest(t, app, http.MethodPost,
		"/api/v1/test/role-suggestion-login/"+strconv.FormatInt(userID, 10), nil, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login status=%d cookies=%d", response.StatusCode, len(response.Cookies()))
	}
	return response.Cookies()[0]
}

func roleSuggestionControllerRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	cookie *http.Cookie,
	body []byte,
) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func roleSuggestionControllerPATRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
	cookie *http.Cookie,
	body []byte,
) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer sft_role_suggestion_test")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func assertRoleSuggestionError(t *testing.T, response *http.Response, status int, reason string) {
	t.Helper()
	defer response.Body.Close()
	var envelope roleSuggestionTestEnvelope[struct {
		Reason string `json:"reason"`
	}]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || envelope.Data.Reason != reason {
		t.Fatalf("status=%d reason=%q, want %d/%q", response.StatusCode, envelope.Data.Reason, status, reason)
	}
}

type roleSuggestionTestEnvelope[T any] struct {
	Code int `json:"code"`
	Data T   `json:"data"`
}

var _ identityregistry.Store = (*roleSuggestionControllerRegistry)(nil)
