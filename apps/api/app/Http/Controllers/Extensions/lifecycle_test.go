package extensionscontroller

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

type lifecycleControllerRepository struct {
	operation  extensions.LifecycleOperation
	operations []extensions.LifecycleOperation
	steps      []extensions.LifecycleStepAttempt
	recoveries []extensions.LifecycleRecoveryDecision
	lastLimit  int
}

func (r *lifecycleControllerRepository) Operation(_ context.Context, extensionID string, operationID int64) (extensions.LifecycleOperation, error) {
	if r.operation.ExtensionID != extensionID || r.operation.ID != operationID {
		return extensions.LifecycleOperation{}, extensions.ErrLifecycleOperationNotFound
	}
	return r.operation, nil
}

func (*lifecycleControllerRepository) OpenOperation(context.Context, string) (extensions.LifecycleOperation, error) {
	return extensions.LifecycleOperation{}, extensions.ErrLifecycleOperationNotFound
}

func (r *lifecycleControllerRepository) ListOperations(_ context.Context, extensionID string, limit int) ([]extensions.LifecycleOperation, error) {
	r.lastLimit = limit
	items := make([]extensions.LifecycleOperation, 0, len(r.operations))
	for _, operation := range r.operations {
		if operation.ExtensionID == extensionID {
			items = append(items, operation)
		}
	}
	return items, nil
}

func (r *lifecycleControllerRepository) ListStepAttempts(_ context.Context, operationID int64) ([]extensions.LifecycleStepAttempt, error) {
	items := make([]extensions.LifecycleStepAttempt, 0, len(r.steps))
	for _, step := range r.steps {
		if step.OperationID == operationID {
			items = append(items, step)
		}
	}
	return items, nil
}

func (r *lifecycleControllerRepository) ListRecoveryDecisions(_ context.Context, operationID int64) ([]extensions.LifecycleRecoveryDecision, error) {
	items := make([]extensions.LifecycleRecoveryDecision, 0, len(r.recoveries))
	for _, decision := range r.recoveries {
		if decision.OperationID == operationID {
			items = append(items, decision)
		}
	}
	return items, nil
}

func TestLifecycleInspectionHTTPReturnsOnlyAllowlistedFields(t *testing.T) {
	now := time.Date(2026, time.July, 14, 3, 0, 0, 0, time.UTC)
	operation := extensions.LifecycleOperation{
		ID: 41, ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ArtifactDigests: json.RawMessage(`{"secret":"artifact"}`),
		Operation: extensions.LifecycleOperationUninstall, State: extensions.LifecycleStateFailed,
		PlanVersion: "demo.plugin.lifecycle@1", IdempotencyKey: "private-idempotency",
		RequestFingerprint: "private-fingerprint", AuthorityType: extensions.LifecycleAuthorityTrustGrant,
		TrustGrantID: 9, AuthoritySnapshot: json.RawMessage(`{"secret":"authority"}`),
		RequestedByUserID: 1, AuditEventID: 8, RemovalMode: extensions.LifecycleRemovalComplete,
		Forced: true, AttemptCount: 2, Revision: 5, CurrentStepID: "lifecycle.uninstall.04.uninstall",
		Checkpoint: json.RawMessage(`{"secret":"checkpoint"}`), Progress: json.RawMessage(`{"secret":"progress"}`),
		TerminalResult: extensions.LifecycleTerminalFailed, ResultDocument: json.RawMessage(`{"secret":"operation-result"}`),
		Error: extensions.LifecycleExecutionError{
			Code: "external.cleanup", Message: "cleanup failed", Retryable: true,
			Metadata: json.RawMessage(`{"secret":"error-metadata"}`),
		},
		CreatedAt: now, UpdatedAt: now,
	}
	step := extensions.LifecycleStepAttempt{
		ID: 52, OperationID: operation.ID, StepID: operation.CurrentStepID,
		LifecycleAction: extensions.LifecycleOperationUninstall, PlanVersion: operation.PlanVersion,
		Attempt: 2, Status: extensions.LifecycleStepFailed, Checkpoint: "private-step-checkpoint",
		CompletedUnits: 1, TotalUnits: 3, ProgressMessage: "Cleaning external resources",
		InputDocument: json.RawMessage(`{"secret":"step-input"}`), ResultDocument: json.RawMessage(`{"secret":"step-result"}`),
		Error: extensions.LifecycleExecutionError{
			Code: "external.cleanup", Message: "cleanup failed",
			Metadata: json.RawMessage(`{"secret":"step-error-metadata"}`),
		},
		SkipReason: "operator accepted residual resources", Forced: true,
		ActorUserID: 1, AuditEventID: 8, LeaseOwnerToken: "private-lease-token", LeaseRevision: 4,
		CreatedAt: now, UpdatedAt: now,
	}
	recovery := extensions.LifecycleRecoveryDecision{
		ID: 53, OperationID: operation.ID, OperationAttempt: 2,
		Decision: extensions.LifecycleRecoverySkipStep, EscalateForced: true,
		Reason: "operator accepted residual resources", ActorUserID: 1, AuditEventID: 9, CreatedAt: now,
	}
	repository := &lifecycleControllerRepository{
		operation: operation, operations: []extensions.LifecycleOperation{operation},
		steps: []extensions.LifecycleStepAttempt{step}, recoveries: []extensions.LifecycleRecoveryDecision{recovery},
	}
	app, manager := newLifecycleInspectionTestApp(t, repository)
	cookie := loginExtensionUser(t, app, manager, 1)

	listResponse := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle", cookie)
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected lifecycle history 200, got %d: %s", listResponse.StatusCode, responseBody(t, listResponse))
	}
	var listEnvelope testEnvelope[[]extensions.LifecycleOperationSummary]
	if err := json.NewDecoder(listResponse.Body).Decode(&listEnvelope); err != nil {
		t.Fatalf("decode lifecycle history: %v", err)
	}
	listResponse.Body.Close()
	if len(listEnvelope.Data) != 1 || listEnvelope.Data[0].ID != operation.ID || repository.lastLimit != defaultLifecycleHistoryLimit {
		t.Fatalf("unexpected lifecycle history: %#v limit=%d", listEnvelope.Data, repository.lastLimit)
	}

	detailResponse := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle/41", cookie)
	if detailResponse.StatusCode != http.StatusOK {
		t.Fatalf("expected lifecycle detail 200, got %d: %s", detailResponse.StatusCode, responseBody(t, detailResponse))
	}
	defer detailResponse.Body.Close()
	var detailEnvelope testEnvelope[json.RawMessage]
	if err := json.NewDecoder(detailResponse.Body).Decode(&detailEnvelope); err != nil {
		t.Fatalf("decode lifecycle detail: %v", err)
	}
	var detail extensions.LifecycleOperationDetail
	if err := json.Unmarshal(detailEnvelope.Data, &detail); err != nil {
		t.Fatalf("decode lifecycle detail data: %v", err)
	}
	if detail.ID != operation.ID || len(detail.Steps) != 1 || detail.Steps[0].ID != step.ID ||
		len(detail.Recoveries) != 1 || detail.Recoveries[0].ID != recovery.ID {
		t.Fatalf("unexpected lifecycle detail: %#v", detail)
	}
	document := string(detailEnvelope.Data)
	for _, forbidden := range []string{
		"artifactDigests", "authorityType", "authoritySnapshot", "trustGrantId",
		"idempotencyKey", "requestFingerprint", "checkpoint", "progress\"",
		"inputDocument", "resultDocument", "metadata", "leaseOwnerToken",
		"leaseExpiresAt", "leaseRevision", "leaseHeartbeatAt", "private-idempotency",
		"private-fingerprint", "private-lease-token", "\"secret\"",
		"external.cleanup", "cleanup failed", "Cleaning external resources",
	} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("lifecycle HTTP response leaked %q: %s", forbidden, document)
		}
	}
	if detail.Error.Code != "lifecycle.action_failed" || detail.Steps[0].Error.Code != "lifecycle.action_failed" ||
		detail.Steps[0].ProgressMessage != "" {
		t.Fatalf("lifecycle HTTP errors were not canonicalized: %#v", detail)
	}

	wrongExtensionResponse := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/other.plugin/lifecycle/41", cookie)
	assertLifecycleHTTPError(t, wrongExtensionResponse, http.StatusNotFound, lifecycleOperationNotFoundReason)
}

func TestLifecycleInspectionHTTPPermissionParametersAndErrors(t *testing.T) {
	repository := &lifecycleControllerRepository{}
	app, manager := newLifecycleInspectionTestApp(t, repository)

	response := performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle", nil)
	assertLifecycleHTTPStatus(t, response, http.StatusUnauthorized)

	deniedCookie := loginExtensionUser(t, app, manager, 2)
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle?limit=invalid", deniedCookie)
	assertLifecycleHTTPStatus(t, response, http.StatusForbidden)

	cookie := loginExtensionUser(t, app, manager, 1)
	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle?limit=7", cookie)
	assertLifecycleHTTPStatus(t, response, http.StatusOK)
	if repository.lastLimit != 7 {
		t.Fatalf("expected strict limit 7, got %d", repository.lastLimit)
	}

	for _, suffix := range []string{
		"?limit=", "?limit=0", "?limit=-1", "?limit=01", "?limit=501",
		"?limit=invalid", "?limit=1.5", "?limit=1&limit=2",
	} {
		response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle"+suffix, cookie)
		assertLifecycleHTTPStatus(t, response, http.StatusUnprocessableEntity)
	}
	for _, operationID := range []string{"0", "-1", "01", "+1", "invalid", "9223372036854775808"} {
		response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle/"+operationID, cookie)
		assertLifecycleHTTPStatus(t, response, http.StatusUnprocessableEntity)
	}

	response = performExtensionRequest(t, app, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle/42", cookie)
	assertLifecycleHTTPError(t, response, http.StatusNotFound, lifecycleOperationNotFoundReason)

	unavailableApp, unavailableManager := newLifecycleInspectionTestApp(t, nil)
	unavailableCookie := loginExtensionUser(t, unavailableApp, unavailableManager, 1)
	response = performExtensionRequest(t, unavailableApp, http.MethodGet, "/api/v1/admin/extensions/demo.plugin/lifecycle", unavailableCookie)
	assertLifecycleHTTPError(t, response, http.StatusServiceUnavailable, lifecycleUnavailableReason)
}

func TestLifecycleRecoveryHTTPPermissionValidationAndUnavailableBoundary(t *testing.T) {
	repository := &lifecycleControllerRepository{}
	app, manager := newLifecycleInspectionTestApp(t, repository)
	path := "/api/v1/admin/extensions/demo.plugin/lifecycle/41/recovery"

	response := performExtensionJSONRequest(t, app, http.MethodPost, path, nil, `{"decision":"retry"}`)
	assertLifecycleHTTPStatus(t, response, http.StatusUnauthorized)

	viewCookie := loginExtensionUser(t, app, manager, 1)
	response = performExtensionJSONRequest(t, app, http.MethodPost, path, viewCookie, `{"decision":"retry"}`)
	assertLifecycleHTTPError(t, response, http.StatusForbidden, "permission.denied")

	manageCookie := loginExtensionUser(t, app, manager, 3)
	response = performExtensionRequest(t, app, http.MethodPost, path, manageCookie)
	assertLifecycleHTTPStatus(t, response, http.StatusUnprocessableEntity)
	response = performExtensionJSONRequest(t, app, http.MethodPost, path, manageCookie, `{`)
	assertLifecycleHTTPStatus(t, response, http.StatusUnprocessableEntity)
	response = performExtensionJSONRequest(t, app, http.MethodPost,
		"/api/v1/admin/extensions/demo.plugin/lifecycle/01/recovery", manageCookie, `{"decision":"retry"}`)
	assertLifecycleHTTPStatus(t, response, http.StatusUnprocessableEntity)

	// The manage-authorized request reaches the service dependency boundary.
	response = performExtensionJSONRequest(t, app, http.MethodPost, path, manageCookie, `{"decision":"retry"}`)
	assertLifecycleHTTPError(t, response, http.StatusServiceUnavailable, lifecycleUnavailableReason)
}

func newLifecycleInspectionTestApp(t *testing.T, repository extensions.LifecycleInspectionRepository) (*fiber.App, *authsession.Manager) {
	t.Helper()
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionView: true}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		3: {ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionPluginManage: true}},
	}}
	store := &controllerFakeStore{items: map[string]extensions.Extension{}}
	service := extensions.NewServiceWithOptions(store, "", "", nil, extensions.WithLifecycleInspectionRepository(repository))
	controller := NewController(service, users, manager)
	loginProvider := extensionRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID := int64(1)
			switch c.Params("id") {
			case "2":
				userID = 2
			case "3":
				userID = 3
			}
			_, err := manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{
		AppName: "SForum", AppEnv: "test", CSRFEnabled: false,
		AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"},
	}, slog.Default(), apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller, loginProvider}})
	return app, manager
}

func assertLifecycleHTTPStatus(t *testing.T, response *http.Response, status int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("expected status %d, got %d", status, response.StatusCode)
	}
}

func assertLifecycleHTTPError(t *testing.T, response *http.Response, status int, reason string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("expected status %d, got %d", status, response.StatusCode)
	}
	var envelope testEnvelope[testErrorData]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if envelope.Data.Reason != reason {
		t.Fatalf("expected reason %q, got %q", reason, envelope.Data.Reason)
	}
}
