package identitycontroller

import (
	"bytes"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type errorEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Reason string              `json:"reason"`
		Fields map[string][]string `json:"fields"`
	} `json:"data"`
}

func updateUserRequestWithCookie(t *testing.T, app *fiber.App, cookie *nethttp.Cookie, userID string, body map[string]any) (*nethttp.Response, errorEnvelope) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPatch, "/api/v1/users/"+userID, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("update user request failed: %v", err)
	}
	var envelope errorEnvelope
	if decodeErr := json.NewDecoder(resp.Body).Decode(&envelope); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	resp.Body.Close()
	return resp, envelope
}

// 保存账户失败必须说清是哪个字段，且不能再提示「注册失败」。
func TestUpdateAdminUserEndpointReturnsFieldErrors(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)
	registerTestUser(t, app, "bob", "bob@example.com")

	resp, envelope := updateUserRequestWithCookie(t, app, cookie, "2", map[string]any{
		"displayName": "   ",
	})
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	if envelope.Data.Reason != identity.CodeUserInvalidUpdate {
		t.Fatalf("reason = %q, want %q", envelope.Data.Reason, identity.CodeUserInvalidUpdate)
	}
	if len(envelope.Data.Fields["displayName"]) == 0 {
		t.Fatalf("expected displayName field error, got %+v", envelope.Data.Fields)
	}
	// 后台语境不能复用注册文案：这是本次修复的原始症状。
	if strings.Contains(envelope.Message, "注册失败") || strings.Contains(envelope.Message, "Registration failed") {
		t.Fatalf("admin update must not reuse the registration copy, got %q", envelope.Message)
	}
}

// 一个字段都没提交时给出独立 reason，而不是笼统的「内容不正确」。
func TestUpdateAdminUserEndpointReportsEmptyUpdate(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)
	registerTestUser(t, app, "bob", "bob@example.com")

	resp, envelope := updateUserRequestWithCookie(t, app, cookie, "2", map[string]any{})
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	if envelope.Data.Reason != identity.CodeUserUpdateEmpty {
		t.Fatalf("reason = %q, want %q", envelope.Data.Reason, identity.CodeUserUpdateEmpty)
	}
	if len(envelope.Data.Fields) != 0 {
		t.Fatalf("empty update must not carry fields, got %+v", envelope.Data.Fields)
	}
}

// 唯一性冲突要保持 409，但同样带字段，前端才能标红输入框。
func TestUpdateAdminUserEndpointReturnsConflictFields(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)
	registerTestUser(t, app, "bob", "bob@example.com")

	resp, envelope := updateUserRequestWithCookie(t, app, cookie, "2", map[string]any{
		"username": "alice",
	})
	if resp.StatusCode != nethttp.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	if envelope.Data.Reason != identity.CodeUsernameOrEmailTaken {
		t.Fatalf("reason = %q, want %q", envelope.Data.Reason, identity.CodeUsernameOrEmailTaken)
	}
	if len(envelope.Data.Fields["username"]) == 0 {
		t.Fatalf("expected username field error, got %+v", envelope.Data.Fields)
	}
}

func TestMapIdentityErrorCarriesAdminFieldErrors(t *testing.T) {
	t.Parallel()

	fields := identity.FieldMessages{identity.FieldDisplayName: {identity.MessageDisplayNameRequired}}
	apiErr, ok := mapIdentityError(&identity.AdminUserFieldsError{
		Reason: identity.CodeUserInvalidUpdate,
		Fields: fields,
	}).(*apphttp.APIError)
	if !ok {
		t.Fatal("expected *apphttp.APIError")
	}
	if apiErr.Status != fiber.StatusUnprocessableEntity || apiErr.Reason != identity.CodeUserInvalidUpdate {
		t.Fatalf("unexpected api error: %+v", apiErr)
	}
	if len(apiErr.Fields[identity.FieldDisplayName]) == 0 {
		t.Fatalf("fields were dropped: %+v", apiErr.Fields)
	}

	// 零值 reason 不能漏到响应里：data.reason 是契约必填项。
	fallback, ok := mapIdentityError(&identity.AdminUserFieldsError{Fields: fields}).(*apphttp.APIError)
	if !ok {
		t.Fatal("expected *apphttp.APIError for empty reason")
	}
	if fallback.Reason != identity.CodeUserInvalidUpdate {
		t.Fatalf("empty reason must fall back to %q, got %q", identity.CodeUserInvalidUpdate, fallback.Reason)
	}

	conflict, ok := mapIdentityError(&identity.AdminUserConflictError{
		Fields: identity.FieldMessages{identity.FieldUsername: {identity.MessageUserUsernameTaken}},
	}).(*apphttp.APIError)
	if !ok {
		t.Fatal("expected *apphttp.APIError for conflict")
	}
	if conflict.Status != fiber.StatusConflict || conflict.Reason != identity.CodeUsernameOrEmailTaken {
		t.Fatalf("unexpected conflict mapping: %+v", conflict)
	}
	if len(conflict.Fields[identity.FieldUsername]) == 0 {
		t.Fatalf("conflict fields were dropped: %+v", conflict.Fields)
	}
}

// 注册链路的 reason 不能被后台分支抢走。
func TestMapIdentityErrorKeepsRegisterInvalid(t *testing.T) {
	t.Parallel()

	apiErr, ok := mapIdentityError(&identity.RegisterInvalidError{
		Fields: identity.FieldMessages{identity.FieldUsername: {identity.MessageUsernameRequired}},
	}).(*apphttp.APIError)
	if !ok {
		t.Fatal("expected *apphttp.APIError")
	}
	if apiErr.Status != fiber.StatusUnprocessableEntity || apiErr.Reason != identity.CodeRegisterInvalid {
		t.Fatalf("unexpected register mapping: %+v", apiErr)
	}
}

// PUT /users/{id}/roles 的未知用户组必须带 roleKeys 字段：抽屉里那块表单才有落点。
func TestReplaceUserRolesEndpointReturnsRoleKeysFieldError(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerTestUser(t, app, "alice", "alice@example.com")
	promoteToUserManager(t, app, store, 1)
	registerTestUser(t, app, "bob", "bob@example.com")

	payload, err := json.Marshal(map[string]any{"roleKeys": []string{"missing-role"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPut, "/api/v1/users/2/roles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("replace user roles request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
	var envelope errorEnvelope
	if decodeErr := json.NewDecoder(resp.Body).Decode(&envelope); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	// reason 仍是 role.invalid（前端只读 fields，不按 reason 分支）。
	if envelope.Data.Reason != identity.CodeRoleInvalid {
		t.Fatalf("reason = %q, want %q", envelope.Data.Reason, identity.CodeRoleInvalid)
	}
	if len(envelope.Data.Fields["roleKeys"]) == 0 {
		t.Fatalf("expected roleKeys field error, got %+v", envelope.Data.Fields)
	}
}
