package extensionscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/config"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const adminSurfaceTestPermission = "fixture.admin.manage"

func TestAdminSurfaceHTTPFiltersPermissionsCompositionAndInternals(t *testing.T) {
	runtime := newAdminSurfaceControllerRuntime()
	auditor := &adminSurfaceControllerAuditor{}
	app := newAdminSurfaceControllerApp(t, runtime, auditor)
	admin := loginAdminSurfaceControllerUser(t, app, 1)
	privileged := loginAdminSurfaceControllerUser(t, app, 2)
	pluginOnly := loginAdminSurfaceControllerUser(t, app, 3)

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/admin-surfaces", nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/admin-surfaces", pluginOnly)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin list status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/admin-surfaces?kind=future", admin)
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid kind status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	response.Body.Close()

	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/admin-surfaces", admin)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin list status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var catalog testEnvelope[AdminSurfaceCatalogView]
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if catalog.Data.Revision != 7 || len(catalog.Data.Surfaces) != 1 || catalog.Data.Surfaces[0].ID != "fixture.admin.surface.public" {
		t.Fatalf("admin catalog = %#v", catalog.Data)
	}
	response = performExtensionRequest(t, app, http.MethodGet,
		"/api/v1/admin/admin-surfaces?placementId=core.component.page.admin.users", privileged)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("placement list status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var placed testEnvelope[AdminSurfaceCatalogView]
	if err := json.NewDecoder(response.Body).Decode(&placed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(placed.Data.Surfaces) != 2 || placed.Data.Surfaces[0].PlacementID != "core.component.page.admin.users" ||
		placed.Data.Surfaces[1].PlacementID != "core.component.page.admin.users" {
		t.Fatalf("placement catalog = %#v", placed.Data)
	}

	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/admin-surfaces", privileged)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("privileged list status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var raw testEnvelope[map[string]any]
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	surfaces, ok := raw.Data["surfaces"].([]any)
	if !ok || len(surfaces) != 3 {
		t.Fatalf("privileged raw catalog = %#v", raw.Data)
	}
	for _, item := range surfaces {
		surface := item.(map[string]any)
		for _, key := range []string{"artifactDigest", "runtimeInstanceId", "handler", "permission"} {
			if _, exposed := surface[key]; exposed {
				t.Fatalf("surface exposes %s: %#v", key, surface)
			}
		}
	}
}

func TestAdminSurfaceHTTPCommandRequiresIdempotencyKeyBeforeAudit(t *testing.T) {
	runtime := newAdminSurfaceControllerRuntime()
	contract := runtime.contracts["fixture.admin.surface.protected"]
	contract.Operation = extensions.AdminSurfaceOperationCommand
	runtime.contracts[contract.ID] = contract
	for index := range runtime.snapshot.Surfaces {
		if runtime.snapshot.Surfaces[index].ID == contract.ID {
			runtime.snapshot.Surfaces[index] = contract
		}
	}
	auditor := &adminSurfaceControllerAuditor{}
	app := newAdminSurfaceControllerApp(t, runtime, auditor)
	privileged := loginAdminSurfaceControllerUser(t, app, 2)
	response := performExtensionJSONRequest(t, app, http.MethodPost,
		"/api/v1/admin/admin-surfaces/fixture.admin.surface.protected/invoke", privileged,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"SForum"}}`)
	if response.StatusCode != http.StatusUnprocessableEntity || runtime.calls != 0 || len(auditor.events) != 0 {
		t.Fatalf("status=%d calls=%d audits=%#v body=%s", response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
	}
	response.Body.Close()
}

func TestAdminSurfaceHTTPEnforcesDeclarationAndAuditsExactInvocation(t *testing.T) {
	runtime := newAdminSurfaceControllerRuntime()
	auditor := &adminSurfaceControllerAuditor{}
	app := newAdminSurfaceControllerApp(t, runtime, auditor)
	admin := loginAdminSurfaceControllerUser(t, app, 1)
	privileged := loginAdminSurfaceControllerUser(t, app, 2)
	path := "/api/v1/admin/admin-surfaces/fixture.admin.surface.protected/invoke"

	response := performExtensionJSONRequest(t, app, http.MethodPost, path, admin,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"Denied"}}`)
	if response.StatusCode != http.StatusForbidden || runtime.calls != 0 || len(auditor.events) != 0 {
		t.Fatalf("denied status=%d calls=%d audits=%#v body=%s", response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
	}
	response.Body.Close()
	for name, body := range map[string]string{
		"missing input": `{"contractVersion":"fixture.admin.surface.protected@1"}`,
		"null input":    `{"contractVersion":"fixture.admin.surface.protected@1","input":null}`,
	} {
		response = performExtensionJSONRequest(t, app, http.MethodPost,
			"/api/v1/admin/admin-surfaces/fixture.admin.surface.protected/invoke", privileged, body)
		if response.StatusCode != http.StatusUnprocessableEntity || runtime.calls != 0 || len(auditor.events) != 0 {
			t.Fatalf("%s status=%d calls=%d audits=%#v body=%s", name, response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
		}
		response.Body.Close()
	}
	response = performAdminSurfaceJSONRequest(t, app, path, privileged,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"Invalid key"}}`, "has whitespace")
	if response.StatusCode != http.StatusUnprocessableEntity || runtime.calls != 0 || len(auditor.events) != 0 {
		t.Fatalf("invalid key status=%d calls=%d audits=%#v body=%s", response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
	}
	response.Body.Close()

	response = performAdminSurfaceJSONRequest(t, app, path, privileged,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"SForum"}}`, "admin-surface-request-2")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("invoke status=%d body=%s", response.StatusCode, responseBody(t, response))
	}
	var result testEnvelope[AdminSurfaceInvocationView]
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if runtime.calls != 1 || result.Data.Surface.ID != "fixture.admin.surface.protected" || result.Data.Output["title"] != "Rendered" {
		t.Fatalf("calls=%d result=%#v", runtime.calls, result.Data)
	}
	if runtime.input.Actor == nil || runtime.input.Actor.UserID != 2 ||
		!reflect.DeepEqual(runtime.input.Actor.PermissionKeys, []string{identity.PermissionAdminAccess, adminSurfaceTestPermission}) ||
		runtime.input.IdempotencyKey != "admin-surface-request-2" {
		t.Fatalf("runtime invocation = %#v", runtime.input)
	}
	if len(auditor.events) != 2 || auditor.events[0].Action != audit.ActionExtensionAdminSurface ||
		auditor.events[0].ActorUserID != 2 || auditor.events[0].Metadata["status"] != "attempted" ||
		auditor.events[1].Metadata["status"] != "succeeded" ||
		auditor.events[0].Metadata["artifactDigest"] != strings.Repeat("a", 64) ||
		auditor.events[0].Metadata["runtimeInstanceId"] != "runtime-admin" {
		t.Fatalf("audit events = %#v", auditor.events)
	}

	response = performExtensionJSONRequest(t, app, http.MethodPost, path, privileged,
		`{"contractVersion":"fixture.admin.surface.protected@2","input":{"title":"Stale"}}`)
	if response.StatusCode != http.StatusConflict || runtime.calls != 1 || len(auditor.events) != 4 ||
		auditor.events[3].Metadata["status"] != "failed" {
		t.Fatalf("stale status=%d calls=%d audits=%#v body=%s", response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
	}
	response.Body.Close()
}

func TestMapAdminSurfaceErrorMapsTypedRuntimeFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "typed invalid", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT}, wantStatus: http.StatusUnprocessableEntity, wantCode: CodeAdminSurfaceInvalid},
		{name: "typed permission", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_PERMISSION_DENIED}, wantStatus: http.StatusForbidden, wantCode: "permission.denied"},
		{name: "typed missing", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND}, wantStatus: http.StatusNotFound, wantCode: CodeAdminSurfaceNotFound},
		{name: "typed conflict", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT}, wantStatus: http.StatusConflict, wantCode: CodeAdminSurfaceStale},
		{name: "typed timeout", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED}, wantStatus: http.StatusGatewayTimeout, wantCode: CodeAdminSurfaceUnavailable},
		{name: "typed unavailable", err: &extensionsruntime.ProtocolV2Error{Code: protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE}, wantStatus: http.StatusServiceUnavailable, wantCode: CodeAdminSurfaceUnavailable},
		{name: "grpc invalid", err: status.Error(codes.InvalidArgument, "invalid"), wantStatus: http.StatusUnprocessableEntity, wantCode: CodeAdminSurfaceInvalid},
		{name: "grpc conflict", err: status.Error(codes.Aborted, "conflict"), wantStatus: http.StatusConflict, wantCode: CodeAdminSurfaceStale},
		{name: "grpc unavailable", err: status.Error(codes.Unavailable, "unavailable"), wantStatus: http.StatusServiceUnavailable, wantCode: CodeAdminSurfaceUnavailable},
		{name: "delegation invalid", err: extensionsruntime.ErrProtocolV2ActorDelegationInvalid, wantStatus: http.StatusUnprocessableEntity, wantCode: CodeAdminSurfaceInvalid},
		{name: "delegation unavailable", err: extensionsruntime.ErrProtocolV2ActorDelegationUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: CodeAdminSurfaceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapAdminSurfaceError(test.err)
			fiberErr, ok := mapped.(*fiber.Error)
			if !ok || fiberErr.Code != test.wantStatus || fiberErr.Message != test.wantCode {
				t.Fatalf("mapped error = %#v", mapped)
			}
		})
	}
}

func TestAdminSurfaceHTTPAuditFailureBlocksInvocation(t *testing.T) {
	runtime := newAdminSurfaceControllerRuntime()
	auditor := &adminSurfaceControllerAuditor{err: errors.New("audit unavailable")}
	app := newAdminSurfaceControllerApp(t, runtime, auditor)
	privileged := loginAdminSurfaceControllerUser(t, app, 2)
	response := performExtensionJSONRequest(t, app, http.MethodPost,
		"/api/v1/admin/admin-surfaces/fixture.admin.surface.protected/invoke", privileged,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"SForum"}}`)
	if response.StatusCode != http.StatusServiceUnavailable || runtime.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.StatusCode, runtime.calls, responseBody(t, response))
	}
	response.Body.Close()
}

func TestAdminSurfaceHTTPFencesPublicationSwapAfterAuditAttempt(t *testing.T) {
	runtime := newAdminSurfaceControllerRuntime()
	const surfaceID = "fixture.admin.surface.protected"
	auditor := &adminSurfaceControllerAuditor{}
	auditor.onAppend = func(event audit.Event) {
		if event.Metadata["status"] != "attempted" {
			return
		}
		contract := runtime.contracts[surfaceID]
		contract.ExtensionVersion = "2.0.0"
		contract.ArtifactDigest = strings.Repeat("b", 64)
		contract.InstanceID = "runtime-replacement"
		runtime.contracts[surfaceID] = contract
		for index := range runtime.snapshot.Surfaces {
			if runtime.snapshot.Surfaces[index].ID == surfaceID {
				runtime.snapshot.Surfaces[index] = contract
			}
		}
	}
	app := newAdminSurfaceControllerApp(t, runtime, auditor)
	privileged := loginAdminSurfaceControllerUser(t, app, 2)
	response := performExtensionJSONRequest(t, app, http.MethodPost,
		"/api/v1/admin/admin-surfaces/"+surfaceID+"/invoke", privileged,
		`{"contractVersion":"fixture.admin.surface.protected@1","input":{"title":"SForum"}}`)
	if response.StatusCode != http.StatusConflict || runtime.calls != 0 || len(auditor.events) != 2 {
		t.Fatalf("status=%d calls=%d audits=%#v body=%s", response.StatusCode, runtime.calls, auditor.events, responseBody(t, response))
	}
	response.Body.Close()
	for _, event := range auditor.events {
		if event.Metadata["artifactDigest"] != strings.Repeat("a", 64) || event.Metadata["runtimeInstanceId"] != "runtime-admin" {
			t.Fatalf("audit identity drifted after publication swap: %#v", auditor.events)
		}
	}
	if auditor.events[1].Metadata["status"] != "failed" {
		t.Fatalf("completion audit = %#v", auditor.events[1])
	}
}

type adminSurfaceControllerRuntime struct {
	snapshot  extensionsruntime.AdminSurfaceRegistrySnapshot
	contracts map[string]extensionsruntime.AdminSurfaceContract
	calls     int
	input     extensionsruntime.AdminSurfaceInvocation
}

func newAdminSurfaceControllerRuntime() *adminSurfaceControllerRuntime {
	contracts := []extensionsruntime.AdminSurfaceContract{
		adminSurfaceControllerContract("fixture.admin.surface.public", "notice", "add", "", "core.component.page.admin", ""),
		adminSurfaceControllerContract("fixture.admin.surface.protected", "notice", "add", "", "core.component.page.admin.users", adminSurfaceTestPermission),
		adminSurfaceControllerContract("fixture.admin.surface.after", "notice", "after", "fixture.admin.surface.protected", "core.component.page.admin.users", ""),
	}
	byID := make(map[string]extensionsruntime.AdminSurfaceContract, len(contracts))
	for _, contract := range contracts {
		byID[contract.ID] = contract
	}
	return &adminSurfaceControllerRuntime{
		snapshot:  extensionsruntime.AdminSurfaceRegistrySnapshot{Revision: 7, Surfaces: contracts},
		contracts: byID,
	}
}

func (r *adminSurfaceControllerRuntime) AdminSurfaceSnapshot(kind string) extensionsruntime.AdminSurfaceRegistrySnapshot {
	result := extensionsruntime.AdminSurfaceRegistrySnapshot{Revision: r.snapshot.Revision}
	for _, surface := range r.snapshot.Surfaces {
		if kind == "" || surface.Kind == kind {
			result.Surfaces = append(result.Surfaces, surface)
		}
	}
	return result
}

func (r *adminSurfaceControllerRuntime) ResolveAdminSurface(id string) (extensionsruntime.AdminSurfaceContract, error) {
	contract, ok := r.contracts[id]
	if !ok {
		return extensionsruntime.AdminSurfaceContract{}, extensionsruntime.ErrAdminSurfaceNotFound
	}
	return contract, nil
}

func (r *adminSurfaceControllerRuntime) InvokeAdminSurface(
	_ context.Context,
	input extensionsruntime.AdminSurfaceInvocation,
) (extensionsruntime.AdminSurfaceInvocationResult, error) {
	contract, err := r.ResolveAdminSurface(input.ExpectedContract.ID)
	if err != nil {
		return extensionsruntime.AdminSurfaceInvocationResult{}, err
	}
	expected := input.ExpectedContract
	if expected.ContractVersion != contract.ContractVersion || expected.ExtensionID != contract.ExtensionID ||
		expected.ExtensionVersion != contract.ExtensionVersion || expected.ArtifactDigest != contract.ArtifactDigest ||
		expected.InstanceID != contract.InstanceID || input.ContractVersion != contract.ContractVersion {
		return extensionsruntime.AdminSurfaceInvocationResult{}, extensionsruntime.ErrAdminSurfaceRuntimeStale
	}
	r.calls++
	r.input = input
	return extensionsruntime.AdminSurfaceInvocationResult{
		Contract: contract, Output: map[string]any{"title": "Rendered"},
	}, nil
}

func performAdminSurfaceJSONRequest(
	t *testing.T,
	app *fiber.App,
	path string,
	cookie *http.Cookie,
	body string,
	idempotencyKey string,
) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	return response
}

func adminSurfaceControllerContract(id, kind, action, targetID, placementID, permission string) extensionsruntime.AdminSurfaceContract {
	placementContractVersion := "sforum.component.page.admin@1"
	if placementID == "core.component.page.admin.users" {
		placementContractVersion = "sforum.component.page.admin.users@1"
	}
	return extensionsruntime.AdminSurfaceContract{
		ID: id, ContractVersion: id + "@1", ExtensionID: "fixture.admin", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("a", 64), InstanceID: "runtime-admin",
		Kind: kind, Action: action, TargetID: targetID, PlacementID: placementID,
		PlacementContractVersion: placementContractVersion, Label: id,
		Handler: "admin.render", PropsSchema: "fixture.admin.surface.props@1",
		PropsSchemaDigest: strings.Repeat("b", 64), ResultSchema: "fixture.admin.surface.result@1",
		ResultSchemaDigest: strings.Repeat("c", 64), Operation: extensions.AdminSurfaceOperationQuery,
		Permission: permission, Priority: 10,
	}
}

type adminSurfaceControllerAuditor struct {
	events   []audit.Event
	err      error
	onAppend func(audit.Event)
}

func (a *adminSurfaceControllerAuditor) Append(_ context.Context, event audit.Event) error {
	if a.err != nil {
		return a.err
	}
	a.events = append(a.events, event)
	if a.onAppend != nil {
		a.onAppend(event)
	}
	return nil
}

func newAdminSurfaceControllerApp(
	t *testing.T,
	runtime AdminSurfaceRuntime,
	auditor audit.Writer,
) *fiber.App {
	t.Helper()
	sessions := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionAdminAccess: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionAdminAccess: true, adminSurfaceTestPermission: true,
		}},
		3: {ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{adminSurfaceTestPermission: true}},
	}}
	controller := NewController(extensions.NewService(nil, t.TempDir()), actors, sessions).WithAdminSurfaces(runtime, auditor)
	login := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/admin-surface-test-login/:id", func(c fiber.Ctx) error {
			id, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := sessions.Start(c, id)
			return err
		})
	})
	return apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, login}})
}

func loginAdminSurfaceControllerUser(t *testing.T, app *fiber.App, id int64) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin-surface-test-login/"+strconv.FormatInt(id, 10), nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("login %d status=%d", id, response.StatusCode)
	}
	return response.Cookies()[0]
}

var _ AdminSurfaceRuntime = (*adminSurfaceControllerRuntime)(nil)
var _ audit.Writer = (*adminSurfaceControllerAuditor)(nil)
