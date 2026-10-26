package notificationscontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

type settingsStore struct {
	notificationTestStore
	preferenceUsers       []int64
	preferenceRevision    int64
	preferenceItems       []notifications.PreferenceInput
	preferenceErr         error
	adminCalls            int
	adminRevision         int64
	adminItems            []notifications.AdminPolicyUpdate
	adminErr              error
	preferenceRestoreCall int
	adminRestoreCall      int
}

func (s *settingsStore) ListPreferences(_ context.Context, userID int64) (notifications.PreferenceCatalog, error) {
	s.preferenceUsers = append(s.preferenceUsers, userID)
	return notifications.PreferenceCatalog{Revision: s.preferenceRevision}, s.preferenceErr
}

func (s *settingsStore) ReplacePreferences(_ context.Context, userID, revision int64, items []notifications.PreferenceInput) (notifications.PreferenceCatalog, error) {
	s.preferenceUsers = append(s.preferenceUsers, userID)
	s.preferenceRevision = revision
	s.preferenceItems = append([]notifications.PreferenceInput(nil), items...)
	return notifications.PreferenceCatalog{Revision: revision + 1}, s.preferenceErr
}

func (s *settingsStore) RestorePreferences(_ context.Context, userID, revision int64) (notifications.PreferenceCatalog, error) {
	s.preferenceUsers = append(s.preferenceUsers, userID)
	s.preferenceRestoreCall++
	s.preferenceRevision = revision
	return notifications.PreferenceCatalog{Revision: revision + 1}, s.preferenceErr
}

func (s *settingsStore) ListAdminPolicy(context.Context) (notifications.AdminPolicyCatalog, error) {
	s.adminCalls++
	return notifications.AdminPolicyCatalog{Revision: 7}, s.adminErr
}

func (s *settingsStore) ReplaceAdminPolicy(_ context.Context, revision int64, items []notifications.AdminPolicyUpdate) (notifications.AdminPolicyCatalog, error) {
	s.adminCalls++
	s.adminRevision = revision
	s.adminItems = append([]notifications.AdminPolicyUpdate(nil), items...)
	return notifications.AdminPolicyCatalog{Revision: revision + 1}, s.adminErr
}

func (s *settingsStore) RestoreAdminPolicy(_ context.Context, revision int64) (notifications.AdminPolicyCatalog, error) {
	s.adminCalls++
	s.adminRestoreCall++
	s.adminRevision = revision
	return notifications.AdminPolicyCatalog{Revision: revision + 1}, s.adminErr
}

type settingsActors struct {
	actor identity.Actor
}

func (s settingsActors) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, nil
}

type settingsAudit struct {
	events []audit.Event
}

func (s *settingsAudit) Append(_ context.Context, event audit.Event) error {
	s.events = append(s.events, event)
	return nil
}

type settingsCreator struct {
	input notifications.CreateInput
}

func (s *settingsCreator) Create(_ context.Context, input notifications.CreateInput) (notifications.Notification, error) {
	s.input = input
	return notifications.Notification{ID: 9, Type: input.Type, TargetType: input.TargetType}, nil
}

func TestNotificationPreferenceRoutesRequireLoginAndUseCurrentRecipient(t *testing.T) {
	store := &settingsStore{}
	app := notificationSettingsApp(store, identity.Actor{}, nil, nil)

	for _, request := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/api/v1/notification-preferences"},
		{method: http.MethodPut, path: "/api/v1/notification-preferences", body: map[string]any{"revision": 0, "items": []any{}}},
		{method: http.MethodPost, path: "/api/v1/notification-preferences/restore", body: map[string]any{"revision": 0}},
	} {
		resp := notificationSettingsRequest(t, app, request.method, request.path, "", request.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s expected 401, got %d", request.method, request.path, resp.StatusCode)
		}
	}

	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/notification-preferences", "sft_user", map[string]any{
		"revision": 0,
		"userId":   999,
		"items":    []map[string]any{{"type": "reply", "channel": "in_app", "state": "disabled"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(store.preferenceUsers) != 1 || store.preferenceUsers[0] != 42 {
		t.Fatalf("preferences must use authenticated recipient: %#v", store.preferenceUsers)
	}
	if len(store.preferenceItems) != 1 || store.preferenceItems[0].State != "disabled" {
		t.Fatalf("preference input mismatch: %#v", store.preferenceItems)
	}
}

func TestNotificationPreferencesCASRestoreAndAudit(t *testing.T) {
	store := &settingsStore{preferenceErr: notifications.ErrPreferenceConflict}
	auditor := &settingsAudit{}
	app := notificationSettingsApp(store, identity.Actor{}, auditor, nil)

	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/notification-preferences", "sft_user", map[string]any{"revision": 3, "items": []any{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || len(auditor.events) != 0 {
		t.Fatalf("conflict status=%d audits=%#v", resp.StatusCode, auditor.events)
	}

	store.preferenceErr = nil
	resp = notificationSettingsRequest(t, app, http.MethodPost, "/api/v1/notification-preferences/restore", "sft_user", map[string]any{"revision": 3})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || store.preferenceRestoreCall != 1 {
		t.Fatalf("restore status=%d calls=%d", resp.StatusCode, store.preferenceRestoreCall)
	}
	assertAudit(t, auditor.events, 42, audit.ActionNotificationPreferencesRestore, 3)
}

func TestNotificationPreferenceInputBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
		err  error
	}{
		{name: "negative revision", body: map[string]any{"revision": -1, "items": []any{}}},
		{name: "invalid state", body: map[string]any{"revision": 0, "items": []map[string]any{{"type": "reply", "channel": "in_app", "state": "sometimes"}}}, err: notifications.ErrPreferenceInvalid},
		{name: "malformed json", body: json.RawMessage(`{"revision":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &settingsStore{preferenceErr: tc.err}
			app := notificationSettingsApp(store, identity.Actor{}, nil, nil)
			resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/notification-preferences", "sft_user", tc.body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d", resp.StatusCode)
			}
		})
	}

	items := make([]notifications.PreferenceInput, maxPreferenceUpdates+1)
	store := &settingsStore{}
	app := notificationSettingsApp(store, identity.Actor{}, nil, nil)
	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/notification-preferences", "sft_user", map[string]any{"revision": 0, "items": items})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || len(store.preferenceUsers) != 0 {
		t.Fatalf("oversize status=%d store calls=%d", resp.StatusCode, len(store.preferenceUsers))
	}
}

func TestAdminNotificationPolicyPermissionAllowedAndDenied(t *testing.T) {
	for _, tc := range []struct {
		name       string
		token      string
		permission bool
		want       int
		wantCalls  int
	}{
		{name: "login required", want: http.StatusUnauthorized},
		{name: "permission denied", token: "sft_admin", want: http.StatusForbidden},
		{name: "permission allowed", token: "sft_admin", permission: true, want: http.StatusOK, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &settingsStore{}
			actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
			actor.Permissions[identity.PermissionSettingsNotificationsManage] = tc.permission
			app := notificationSettingsApp(store, actor, nil, nil)
			resp := notificationSettingsRequest(t, app, http.MethodGet, "/api/v1/admin/notifications/policy", tc.token, nil)
			resp.Body.Close()
			if resp.StatusCode != tc.want || store.adminCalls != tc.wantCalls {
				t.Fatalf("status=%d calls=%d", resp.StatusCode, store.adminCalls)
			}
		})
	}
}

func TestAdminNotificationPolicyCASRestoreAndAudit(t *testing.T) {
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsNotificationsManage: true}}
	store := &settingsStore{adminErr: notifications.ErrPolicyConflict}
	auditor := &settingsAudit{}
	app := notificationSettingsApp(store, actor, auditor, nil)

	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/admin/notifications/policy", "sft_admin", map[string]any{"revision": 7, "items": []any{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || len(auditor.events) != 0 {
		t.Fatalf("conflict status=%d audits=%#v", resp.StatusCode, auditor.events)
	}

	store.adminErr = nil
	resp = notificationSettingsRequest(t, app, http.MethodPost, "/api/v1/admin/notifications/policy/restore", "sft_admin", map[string]any{"revision": 7})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || store.adminRestoreCall != 1 {
		t.Fatalf("restore status=%d calls=%d", resp.StatusCode, store.adminRestoreCall)
	}
	assertAudit(t, auditor.events, 42, audit.ActionNotificationPolicyRestore, 7)
}

func TestAdminNotificationPolicyMutationDeniedBeforeStoreOrAudit(t *testing.T) {
	store := &settingsStore{}
	auditor := &settingsAudit{}
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive}
	app := notificationSettingsApp(store, actor, auditor, nil)
	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/admin/notifications/policy", "sft_admin", map[string]any{"revision": 7, "items": []any{}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || store.adminCalls != 0 || len(auditor.events) != 0 {
		t.Fatalf("denied status=%d store=%d audits=%#v", resp.StatusCode, store.adminCalls, auditor.events)
	}
}

func TestAdminNotificationTestTargetsCurrentActorAndAudits(t *testing.T) {
	store := &settingsStore{}
	auditor := &settingsAudit{}
	creator := &settingsCreator{}
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsNotificationsManage: true}}
	controller := NewController(store, nil, settingsActors{actor: actor}, creator).WithAuditor(auditor)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "settings"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	resp := notificationSettingsRequest(t, app, http.MethodPost, "/api/v1/admin/notifications/test", "sft_admin", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if creator.input.RecipientUserID != 42 || creator.input.Type != notifications.TypeAdminTest {
		t.Fatalf("test notification input=%#v", creator.input)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != audit.ActionNotificationChannelTest || auditor.events[0].ActorUserID != 42 {
		t.Fatalf("test notification audit=%#v", auditor.events)
	}
}

func TestAdminNotificationPolicyInputBoundaries(t *testing.T) {
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsNotificationsManage: true}}
	for _, tc := range []struct {
		name string
		body any
		err  error
	}{
		{name: "zero revision", body: map[string]any{"revision": 0, "items": []any{}}},
		{name: "invalid policy row", body: map[string]any{"revision": 1, "items": []map[string]any{{"type": "missing", "channel": "email"}}}, err: notifications.ErrPreferenceInvalid},
		{name: "malformed json", body: json.RawMessage(`{"revision":`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &settingsStore{adminErr: tc.err}
			app := notificationSettingsApp(store, actor, nil, nil)
			resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/admin/notifications/policy", "sft_admin", tc.body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d", resp.StatusCode)
			}
		})
	}

	items := make([]notifications.AdminPolicyUpdate, maxAdminPolicyUpdates+1)
	store := &settingsStore{}
	app := notificationSettingsApp(store, actor, nil, nil)
	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/admin/notifications/policy", "sft_admin", map[string]any{"revision": 1, "items": items})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || store.adminCalls != 0 {
		t.Fatalf("oversize status=%d store calls=%d", resp.StatusCode, store.adminCalls)
	}
}

func TestNotificationSettingsSuccessfulUpdatesHaveRedactedAudits(t *testing.T) {
	auditor := &settingsAudit{}
	store := &settingsStore{}
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsNotificationsManage: true}}
	app := notificationSettingsApp(store, actor, auditor, nil)

	resp := notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/notification-preferences", "sft_user", map[string]any{
		"revision": 0,
		"items":    []map[string]any{{"type": "private.plugin.type", "channel": "email", "state": "disabled"}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preference update status=%d", resp.StatusCode)
	}
	resp = notificationSettingsRequest(t, app, http.MethodPut, "/api/v1/admin/notifications/policy", "sft_admin", map[string]any{
		"revision": 1,
		"items":    []map[string]any{{"type": "private.plugin.type", "channel": "email", "enabled": true}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("policy update status=%d", resp.StatusCode)
	}
	if len(auditor.events) != 2 {
		t.Fatalf("audits=%#v", auditor.events)
	}
	if auditor.events[0].Action != audit.ActionNotificationPreferencesUpdate || auditor.events[1].Action != audit.ActionNotificationPolicyUpdate {
		t.Fatalf("audit actions=%q,%q", auditor.events[0].Action, auditor.events[1].Action)
	}
	for _, event := range auditor.events {
		encoded, _ := json.Marshal(event.Metadata)
		if bytes.Contains(encoded, []byte("private.plugin.type")) || bytes.Contains(encoded, []byte("email")) {
			t.Fatalf("audit leaked policy payload: %s", encoded)
		}
	}
}

func notificationSettingsApp(store *settingsStore, actor identity.Actor, auditor audit.Writer, bearerErr error) *fiber.App {
	controller := NewController(store, nil, settingsActors{actor: actor}, nil).WithAuditor(auditor)
	return apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens: notificationBearer{
			auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "settings"},
			err:  bearerErr,
		},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
}

func notificationSettingsRequest(t *testing.T, app *fiber.App, method, path, token string, body any) *http.Response {
	t.Helper()
	var payload []byte
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			payload = raw
		} else {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func assertAudit(t *testing.T, events []audit.Event, actorID int64, action string, revision int64) {
	t.Helper()
	if len(events) != 1 || events[0].ActorUserID != actorID || events[0].Action != action {
		t.Fatalf("audit mismatch: %#v", events)
	}
	if got, ok := events[0].Metadata["previousRevision"].(int64); !ok || got != revision {
		t.Fatalf("audit revision=%#v", events[0].Metadata["previousRevision"])
	}
}

var _ notifications.PreferenceStore = (*settingsStore)(nil)
var _ notifications.AdminPolicyStore = (*settingsStore)(nil)
