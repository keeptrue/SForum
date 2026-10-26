package extensionscontroller

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	"github.com/keeptrue/sforum/apps/api/config"
)

func TestRouteProviderAdminHTTPPermissionsAuditCASAndStaleReset(t *testing.T) {
	registry, artifact, key, providerRouteID := routeProviderControllerRegistry(t)
	store := &routeProviderControllerStore{}
	api := routes.NewProviderSelectionAPI(registry, store)
	auditor := &routeProviderControllerAuditor{nextID: 70}
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		3: {ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}},
		4: {ID: 4, Status: identity.UserStatusActive},
	}}
	controller := NewController(nil, actors, manager).WithRouteProviderSelection(api, auditor)
	login := extensionRouteProviderFunc(func(router fiber.Router) {
		router.Post("/test-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, id)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, login},
	})
	superCookie := loginRouteProviderUser(t, app, 1)
	viewerCookie := loginRouteProviderUser(t, app, 2)
	managerCookie := loginRouteProviderUser(t, app, 3)
	noViewCookie := loginRouteProviderUser(t, app, 4)
	unauthorized := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-providers/conflicts", nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated conflicts status=%d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	forbiddenRead := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-providers/conflicts", noViewCookie)
	if forbiddenRead.StatusCode != http.StatusForbidden {
		t.Fatalf("no-view conflicts status=%d", forbiddenRead.StatusCode)
	}
	forbiddenRead.Body.Close()

	conflicts := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-providers/conflicts", viewerCookie)
	if conflicts.StatusCode != http.StatusOK {
		t.Fatalf("viewer conflicts status=%d body=%s", conflicts.StatusCode, responseBody(t, conflicts))
	}
	var conflictBody testEnvelope[[]routeProviderConflictResponse]
	if err := json.NewDecoder(conflicts.Body).Decode(&conflictBody); err != nil {
		t.Fatal(err)
	}
	conflicts.Body.Close()
	if len(conflictBody.Data) != 1 || len(conflictBody.Data[0].Candidates) != 2 {
		t.Fatalf("conflict inspector = %#v", conflictBody.Data)
	}
	var pluginCandidate routeProviderCandidate
	for _, candidate := range conflictBody.Data[0].Candidates {
		if candidate.ProviderKind == routes.ProviderPlugin {
			pluginCandidate = candidate
		} else if candidate.Artifact != nil {
			t.Fatalf("core candidate exposed a selectable artifact: %#v", candidate)
		}
	}
	if pluginCandidate.Guard == "" || pluginCandidate.Handler == "" || pluginCandidate.Mode == "" ||
		pluginCandidate.RequestSchema == "" || pluginCandidate.ResponseSchema == "" {
		t.Fatalf("plugin risk inspector = %#v", pluginCandidate)
	}

	selectBody := `{"targetRouteId":"` + key.TargetRouteID + `","targetContractVersion":"` + key.TargetContractVersion +
		`","method":"POST","pathSignature":"` + key.PathSignature + `","providerRouteId":"` + providerRouteID +
		`","providerContractVersion":"` + providerRouteID + `@1","providerArtifact":{"extensionId":"` + artifact.ExtensionID +
		`","extensionVersion":"` + artifact.ExtensionVersion + `","packageDigest":"` + artifact.PackageDigest +
		`","runtimeInstanceId":"` + artifact.RuntimeInstanceID + `"},"expectedRevision":0}`
	denied := routeProviderJSONRequest(t, app, "/api/v1/admin/extensions/route-providers/selection", managerCookie, selectBody)
	if denied.StatusCode != http.StatusForbidden || len(auditor.events) != 0 {
		t.Fatalf("manager select status=%d audit=%#v", denied.StatusCode, auditor.events)
	}
	denied.Body.Close()

	store.beforeSelect = func(request routes.SelectProviderRequest) {
		if len(auditor.events) != 1 || request.AuditEventID != 71 || request.ActorUserID != 1 {
			t.Fatalf("selection ran before attributable audit: request=%#v events=%#v", request, auditor.events)
		}
	}
	selected := routeProviderJSONRequest(t, app, "/api/v1/admin/extensions/route-providers/selection", superCookie, selectBody)
	if selected.StatusCode != http.StatusOK {
		t.Fatalf("select status=%d body=%s", selected.StatusCode, responseBody(t, selected))
	}
	selected.Body.Close()
	selectedQuery := "?targetRouteId=" + key.TargetRouteID + "&targetContractVersion=" + key.TargetContractVersion +
		"&method=POST&pathSignature=" + key.PathSignature
	events := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-providers/events"+selectedQuery, viewerCookie)
	if events.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d body=%s", events.StatusCode, responseBody(t, events))
	}
	events.Body.Close()

	// Simulate a target contract bump. Management inspection must still return
	// the old durable key/revision so an operator can CAS reset it.
	bumpedTarget := routes.CoreRoute{
		ID: key.TargetRouteID, ContractVersion: "sforum.route.controller.create@2",
		Method: "POST", Path: "/controller/topics",
	}
	replacement := routeProviderControllerReplacement(providerRouteID, key.TargetRouteID)
	if _, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{bumpedTarget},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}
	query := "?targetRouteId=" + key.TargetRouteID + "&targetContractVersion=" + bumpedTarget.ContractVersion +
		"&method=POST&pathSignature=" + key.PathSignature
	current := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/route-providers/selection"+query, viewerCookie)
	if current.StatusCode != http.StatusOK {
		t.Fatalf("stale current status=%d body=%s", current.StatusCode, responseBody(t, current))
	}
	var currentBody testEnvelope[routes.ProviderSelection]
	if err := json.NewDecoder(current.Body).Decode(&currentBody); err != nil {
		t.Fatal(err)
	}
	current.Body.Close()
	if currentBody.Data.Revision != 1 || currentBody.Data.Key.TargetContractVersion != key.TargetContractVersion {
		t.Fatalf("stale desired binding = %#v", currentBody.Data)
	}

	resetBody, _ := json.Marshal(routeProviderResetRequest{
		TargetRouteID:         currentBody.Data.Key.TargetRouteID,
		TargetContractVersion: currentBody.Data.Key.TargetContractVersion,
		Method:                currentBody.Data.Key.Method, PathSignature: currentBody.Data.Key.PathSignature,
		ExpectedRevision: currentBody.Data.Revision, ReasonCode: "contract_changed",
	})
	store.beforeReset = func(request routes.ResetProviderRequest) {
		if len(auditor.events) != 2 || request.AuditEventID != 72 || request.ExpectedRevision != 1 {
			t.Fatalf("reset ran before attributable audit: request=%#v events=%#v", request, auditor.events)
		}
	}
	reset := routeProviderJSONRequest(t, app, "/api/v1/admin/extensions/route-providers/selection/reset", superCookie, string(resetBody))
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("stale reset status=%d body=%s", reset.StatusCode, responseBody(t, reset))
	}
	reset.Body.Close()
	if store.current != nil {
		t.Fatalf("stale desired selection survived CAS reset: %#v", store.current)
	}
}

func routeProviderControllerRegistry(t *testing.T) (*routes.Registry, routes.PluginArtifact, routes.ProviderSelectionKey, string) {
	t.Helper()
	registry := routes.NewRegistry()
	artifact := routes.PluginArtifact{
		ExtensionID: "controller.route", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "controller-runtime",
	}
	target := routes.CoreRoute{
		ID: "core.route.controller.create", ContractVersion: "sforum.route.controller.create@1",
		Method: "POST", Path: "/controller/topics",
	}
	providerRouteID := "controller.route.writer"
	replacement := routeProviderControllerReplacement(providerRouteID, target.ID)
	snapshot, err := registry.Publish(routes.Publication{
		Core:    []routes.CoreRoute{target},
		Plugins: []routes.PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var signature string
	for _, route := range snapshot.Routes {
		if route.ID == target.ID {
			signature = route.PathSignature
		}
	}
	return registry, artifact, routes.ProviderSelectionKey{
		TargetRouteID: target.ID, TargetContractVersion: target.ContractVersion,
		Method: "POST", PathSignature: signature,
	}, providerRouteID
}

func routeProviderControllerReplacement(id, targetID string) extensionmanifest.ManifestRoute {
	return extensionmanifest.ManifestRoute{
		ID: id, ContractVersion: id + "@1", Action: extensionmanifest.RouteActionReplace,
		TargetID: targetID, Path: "/controller/topics", Methods: []string{"POST"},
		Guard: extensionmanifest.GuardCoreInherit, Priority: 100, Fallback: "closed",
		Mode: extensionmanifest.RouteModeHTTP, Handler: "route.write",
		RequestSchema: id + ".request@1", ResponseSchema: id + ".response@1",
	}
}

func routeProviderJSONRequest(t *testing.T, app *fiber.App, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func loginRouteProviderUser(t *testing.T, app *fiber.App, userID int64) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/test-login/"+strconv.FormatInt(userID, 10), nil)
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

type routeProviderControllerStore struct {
	mu           sync.Mutex
	current      *routes.ProviderSelection
	events       []routes.ProviderSelectionEvent
	beforeSelect func(routes.SelectProviderRequest)
	beforeReset  func(routes.ResetProviderRequest)
}

func (s *routeProviderControllerStore) Desired(_ context.Context, key routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil || s.current.Key.TargetRouteID != key.TargetRouteID ||
		s.current.Key.Method != key.Method || s.current.Key.PathSignature != key.PathSignature {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionNotFound
	}
	return *s.current, nil
}

func (s *routeProviderControllerStore) Selected(ctx context.Context, key routes.ProviderSelectionKey) (routes.ProviderSelection, error) {
	value, err := s.Desired(ctx, key)
	if err != nil {
		return routes.ProviderSelection{}, err
	}
	if value.Key != key {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionStale
	}
	return value, nil
}

func (s *routeProviderControllerStore) Select(_ context.Context, request routes.SelectProviderRequest) (routes.ProviderSelection, error) {
	if s.beforeSelect != nil {
		s.beforeSelect(request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil || request.ExpectedRevision != 0 {
		return routes.ProviderSelection{}, routes.ErrProviderSelectionRevisionConflict
	}
	now := time.Now().UTC()
	value := routes.ProviderSelection{
		Key: request.Key, ProviderRouteID: request.ProviderRouteID,
		ProviderContractVersion: request.ProviderContractVersion,
		ProviderExtensionID:     request.ProviderArtifact.ExtensionID, ProviderExtensionVersionID: 41,
		ProviderExtensionVersion: request.ProviderArtifact.ExtensionVersion,
		ProviderPackageDigest:    request.ProviderArtifact.PackageDigest,
		SelectedByUserID:         request.ActorUserID, SelectionAuditEventID: request.AuditEventID,
		Revision: 1, SelectedAt: now, UpdatedAt: now,
	}
	s.current = &value
	return value, nil
}

func (s *routeProviderControllerStore) Reset(_ context.Context, request routes.ResetProviderRequest) error {
	if s.beforeReset != nil {
		s.beforeReset(request)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return routes.ErrProviderSelectionNotFound
	}
	if s.current.Key != request.Key || s.current.Revision != request.ExpectedRevision {
		return routes.ErrProviderSelectionRevisionConflict
	}
	s.current = nil
	return nil
}

func (*routeProviderControllerStore) InvalidateExtension(context.Context, routes.InvalidateProviderRequest) (int64, error) {
	return 0, nil
}

func (s *routeProviderControllerStore) ListEvents(context.Context, routes.ProviderSelectionKey, int) ([]routes.ProviderSelectionEvent, error) {
	return append([]routes.ProviderSelectionEvent(nil), s.events...), nil
}

type routeProviderControllerAuditor struct {
	mu     sync.Mutex
	nextID int64
	events []audit.Event
}

func (w *routeProviderControllerAuditor) Append(ctx context.Context, event audit.Event) error {
	_, err := w.AppendReturningID(ctx, event)
	return err
}

func (w *routeProviderControllerAuditor) AppendReturningID(_ context.Context, event audit.Event) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextID++
	w.events = append(w.events, event)
	return w.nextID, nil
}

var _ routes.ProviderSelectionStore = (*routeProviderControllerStore)(nil)
var _ audit.IDWriter = (*routeProviderControllerAuditor)(nil)
