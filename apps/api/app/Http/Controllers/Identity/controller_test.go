package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	"github.com/keeptrue/sforum/apps/api/config"
)

// passwordResetFakeStore 仅满足 identity.Store 接口以构造 PasswordResetService。
// 密码重置请求路径只调用 GetCredentialByLogin（未知邮箱静默成功），
// 其余方法不应被触发，故零值返回；如被调用会因零值语义自然中断。
type passwordResetFakeStore struct{}

func (passwordResetFakeStore) LoadActor(context.Context, int64) (identity.Actor, error) {
	return identity.Actor{}, identity.ErrCredentialNotFound
}
func (passwordResetFakeStore) WithBootstrapTx(_ context.Context, fn func(context.Context, identity.TxStore) error) error {
	return fn(context.Background(), passwordResetTxStore{})
}
func (passwordResetFakeStore) AnyUserExists(context.Context) (bool, error) { return false, nil }

// UserKindOf：这份 fake 只服务密码重置路径，那里不涉及机器人语义。
func (passwordResetFakeStore) UserKindOf(context.Context, int64) (identity.UserKind, error) {
	return identity.UserKindHuman, nil
}
func (passwordResetFakeStore) FindRegistrationConflicts(context.Context, string, string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{}, nil
}
func (passwordResetFakeStore) GetCurrentUser(context.Context, int64) (identity.CurrentUser, error) {
	return identity.CurrentUser{}, identity.ErrCredentialNotFound
}
func (passwordResetFakeStore) GetCurrentUserByEmail(context.Context, string) (identity.CurrentUser, error) {
	return identity.CurrentUser{}, identity.ErrUserNotFound
}
func (passwordResetFakeStore) GetCredentialByLogin(_ context.Context, _ string) (identity.CredentialUser, error) {
	// 未知邮箱：静默成功，passwordReset 流程不创建令牌，聚焦测试 HV 校验链路。
	return identity.CredentialUser{}, identity.ErrCredentialNotFound
}
func (passwordResetFakeStore) ListPermissions(context.Context) ([]identity.Permission, error) {
	return nil, nil
}
func (passwordResetFakeStore) ListPermissionMatrix(context.Context) (identity.PermissionMatrix, error) {
	return identity.PermissionMatrix{}, nil
}
func (passwordResetFakeStore) ListUsers(context.Context, identity.UserListInput) (identity.AdminUserList, error) {
	return identity.AdminUserList{}, nil
}
func (passwordResetFakeStore) GetAdminUser(context.Context, int64) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (passwordResetFakeStore) UpdateAdminUser(context.Context, int64, int64, identity.AdminUpdateUserInput) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (passwordResetFakeStore) ListRoles(context.Context) ([]identity.Role, error) { return nil, nil }
func (passwordResetFakeStore) CreateRole(context.Context, identity.RoleInput) (identity.Role, error) {
	return identity.Role{}, nil
}
func (passwordResetFakeStore) UpdateRole(context.Context, string, identity.RoleInput) (identity.Role, error) {
	return identity.Role{}, nil
}
func (passwordResetFakeStore) DeleteRole(context.Context, string) error { return nil }
func (passwordResetFakeStore) ReplaceRolePermissions(context.Context, int64, string, []string) error {
	return nil
}
func (passwordResetFakeStore) ReplaceUserRoles(context.Context, int64, int64, []string) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (passwordResetFakeStore) ReplaceUserPermissionOverrides(context.Context, int64, int64, identity.PermissionOverrides) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (passwordResetFakeStore) RecordLoginAudit(context.Context, identity.LoginAudit) error {
	return nil
}
func (passwordResetFakeStore) CreatePasswordResetToken(context.Context, identity.CreatePasswordResetTokenInput) (identity.PasswordResetToken, error) {
	return identity.PasswordResetToken{}, nil
}
func (passwordResetFakeStore) ConsumePasswordResetToken(context.Context, string) (int64, error) {
	// 默认模拟无效/过期令牌，便于确认路径映射 4xx 而非 500。
	return 0, identity.ErrPasswordResetTokenNotFound
}
func (passwordResetFakeStore) UpdateUserPassword(context.Context, int64, string) error { return nil }
func (passwordResetFakeStore) ConfirmPasswordResetAtomic(context.Context, string, string, string) (int64, error) {
	return 0, identity.ErrPasswordResetTokenNotFound
}
func (passwordResetFakeStore) GetUserTokenVersion(context.Context, int64) (int64, error) {
	return 0, nil
}
func (passwordResetFakeStore) IncrementUserTokenVersion(context.Context, int64) error { return nil }

// 会话目录方法：密码重置测试路径不触发，零值返回以满足接口。
func (passwordResetFakeStore) CreateSession(context.Context, authsession.SessionRecordInput) error {
	return nil
}
func (passwordResetFakeStore) IsSessionRevoked(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (passwordResetFakeStore) ListUserSessions(context.Context, int64, string, bool, int, int) (identity.SessionListResult, error) {
	return identity.SessionListResult{}, nil
}
func (passwordResetFakeStore) RevokeSession(context.Context, int64, string, string) error {
	return nil
}
func (passwordResetFakeStore) RevokeOtherSessions(context.Context, int64, string, string) (int, error) {
	return 0, nil
}
func (passwordResetFakeStore) RevokeUserSessions(context.Context, int64, string) (int, error) {
	return 0, nil
}
func (passwordResetFakeStore) ClearUserClientIPs(context.Context, int64) (identity.ClearUserClientIPsResult, error) {
	return identity.ClearUserClientIPsResult{}, nil
}

func (passwordResetFakeStore) DeleteOldRevokedSessions(context.Context, int) (int, error) {
	return 0, nil
}
func (passwordResetFakeStore) EnforceMaxSessions(context.Context, int64, string, int) (int, error) {
	return 0, nil
}
func (passwordResetFakeStore) TouchSessionLastSeen(context.Context, int64, string) error {
	return nil
}
func (passwordResetFakeStore) HasSessionFingerprint(context.Context, int64, string) (bool, error) {
	return false, nil
}

// passwordResetTxStore 仅满足 identity.TxStore（WithBootstrapTx 回调入参）。
type passwordResetTxStore struct{}

func (passwordResetTxStore) AnyUserExists(context.Context) (bool, error) { return false, nil }
func (passwordResetTxStore) FindRegistrationConflicts(context.Context, string, string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{}, nil
}
func (passwordResetTxStore) CreateUser(context.Context, identity.CreateUserInput) (identity.CurrentUser, error) {
	return identity.CurrentUser{}, nil
}
func (passwordResetTxStore) CreateCredential(context.Context, int64, string) error { return nil }
func (passwordResetTxStore) GetDefaultRole(context.Context) (identity.Role, error) {
	return identity.Role{}, nil
}
func (passwordResetTxStore) GetRole(context.Context, string) (identity.Role, error) {
	return identity.Role{}, nil
}
func (passwordResetTxStore) AssignRole(context.Context, int64, int64) error { return nil }
func (passwordResetTxStore) LoadCurrentUserAccess(context.Context, *identity.CurrentUser) error {
	return nil
}

// fakeAltchaProvider 仅满足 humanverify.Provider 接口供测试构造启用态 verifier。
// Challenge 返回空 payload，Verify 对非空 token 返回成功，模拟 ALTCHA 校验通过。
type fakeAltchaProvider struct{}

func (fakeAltchaProvider) Challenge(_ context.Context, purpose humanverify.Purpose, _ humanverify.Subject) (humanverify.Challenge, error) {
	return humanverify.Challenge{Provider: humanverify.ProviderAltcha, Purpose: purpose, Payload: map[string]any{}}, nil
}
func (fakeAltchaProvider) Verify(_ context.Context, _ humanverify.VerifyRequest) (humanverify.VerifyResult, error) {
	return humanverify.VerifyResult{Verified: true, Code: humanverify.CodeOK}, nil
}

func newIdentityTestApp(t *testing.T, verifier humanverify.Verifier) *fiber.App {
	t.Helper()
	// 真实 PasswordResetService：未知邮箱静默成功，聚焦测试 HV 校验链路。
	passwordReset := identity.NewPasswordResetService(
		passwordResetFakeStore{},
		nil,
		identity.PasswordResetConfig{SiteName: "TestSite", SiteURL: "https://forum.test"},
	)
	controller := NewControllerWithPasswordReset(
		nil, // identity.Service 在密码重置请求路径不被调用
		&authsession.Manager{},
		verifier,
		passwordReset,
		nil, // mailService: 未知邮箱不触发投递
		nil, // options: 密码重置请求路径不读取站点名
	)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app
}

func performPasswordResetRequest(t *testing.T, app *fiber.App, body any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/password-reset/request", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

func TestResolveMailTestRecipientPrefersExplicitThenAdminEmail(t *testing.T) {
	// 显式收件人优先。
	if got, ok := resolveMailTestRecipient("ops@example.com", "admin@example.com"); !ok || got != "ops@example.com" {
		t.Fatalf("expected explicit recipient, got %q ok=%v", got, ok)
	}
	// 空白显式值回落到管理员邮箱。
	if got, ok := resolveMailTestRecipient("  ", "admin@example.com"); !ok || got != "admin@example.com" {
		t.Fatalf("expected admin email fallback, got %q ok=%v", got, ok)
	}
	// 两者皆无效时失败。
	if _, ok := resolveMailTestRecipient("", ""); ok {
		t.Fatal("expected failure when both recipients empty")
	}
	if _, ok := resolveMailTestRecipient("not-an-email", "also-bad"); ok {
		t.Fatal("expected failure for invalid addresses")
	}
	// 不允许 display-name 形式，与 site.admin_email 规范化一致。
	if _, ok := resolveMailTestRecipient("Name <ops@example.com>", ""); ok {
		t.Fatal("expected rejection of display-name address form")
	}
}

func performPasswordResetConfirm(t *testing.T, app *fiber.App, body any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/password-reset/confirm", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

// TestPasswordResetConfirmInvalidTokenReturns422 回归：无效令牌必须 422，不得 500。
func TestPasswordResetConfirmInvalidTokenReturns422(t *testing.T) {
	app := newIdentityTestApp(t, humanverify.NewDisabledService())
	resp := performPasswordResetConfirm(t, app, map[string]any{
		"token":       "not-a-real-token",
		"newPassword": "a-very-strong-password",
	})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid reset token, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Fiber NewError 经统一错误封装后 message 应含稳定 reason。
	raw, _ := json.Marshal(body)
	if !bytes.Contains(raw, []byte("auth.password_reset_invalid")) {
		t.Fatalf("expected auth.password_reset_invalid in response, got %s", raw)
	}
}

// TestPasswordResetSkipsHumanVerificationWhenDisabled 验证：当 password_reset HV 未启用时，
// 缺少 token 的请求正常通过（静默成功 200），修复未破坏默认禁用场景。
func TestPasswordResetSkipsHumanVerificationWhenDisabled(t *testing.T) {
	// 未启用任何 purpose 的 verifier：Verify 直接返回 nil。
	app := newIdentityTestApp(t, humanverify.NewDisabledService())

	resp := performPasswordResetRequest(t, app, map[string]any{"email": "nobody@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 when HV disabled, got %d", resp.StatusCode)
	}
}

type denyMailResendLimiter struct{}

func (denyMailResendLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, nil
}

func (denyMailResendLimiter) RetryAfter(context.Context, string) (time.Duration, error) {
	return 20 * time.Second, nil
}

func TestPasswordResetRateLimitReturnsStructuredRetryTime(t *testing.T) {
	passwordReset := identity.NewPasswordResetService(
		passwordResetFakeStore{}, nil, identity.PasswordResetConfig{},
	).WithRateLimiter(denyMailResendLimiter{})
	controller := NewControllerWithPasswordReset(
		nil, &authsession.Manager{}, humanverify.NewDisabledService(), passwordReset, nil, nil,
	)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})

	resp := performPasswordResetRequest(t, app, map[string]any{"email": "nobody@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var body struct {
		Data struct {
			Reason            string    `json:"reason"`
			RetryAfterSeconds int       `json:"retryAfterSeconds"`
			RetryAt           time.Time `json:"retryAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Reason != "auth.password_reset_rate_limited" || body.Data.RetryAfterSeconds < 19 || !body.Data.RetryAt.After(time.Now()) {
		t.Fatalf("unexpected rate-limit response: %#v", body.Data)
	}
}

// TestPasswordResetRequiresTokenWhenEnabled 验证修复核心：
// 启用 password_reset HV 后，仅发送 {email}（旧前端行为）应被拒绝（422 required），
// 而非静默放行——证明 HV 校验链路在启用时生效，且不会误放行无 token 请求。
func TestPasswordResetRequiresTokenWhenEnabled(t *testing.T) {
	// 构造启用 password_reset purpose 的 verifier。
	verifier := humanverify.NewService(
		humanverify.ServiceConfig{
			Enabled: true,
			EnabledPurposes: map[humanverify.Purpose]bool{
				humanverify.PurposePasswordReset: true,
			},
		},
		fakeAltchaProvider{}, // provider 仅占位，token 为空时 Verify 提前返回 ErrRequired
		nil,
	)
	app := newIdentityTestApp(t, verifier)

	resp := performPasswordResetRequest(t, app, map[string]any{"email": "nobody@example.com"})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when HV enabled but token missing, got %d", resp.StatusCode)
	}
}

// TestPasswordResetReadsNestedHumanVerificationField 验证修复关键差异：
// 启用 HV 时，必须从嵌套 humanVerification 对象读取 token（而非顶层字段）。
// 提供嵌套的有效结构（altcha provider + 非空 token），断言请求通过校验（200），
// 证明嵌套字段被正确解析、token 未因二次绑定丢失。
// （对比：若仍走旧的二次绑定逻辑，嵌套 token 会丢失，请求被 required 拒绝。）
func TestPasswordResetReadsNestedHumanVerificationField(t *testing.T) {
	verifier := humanverify.NewService(
		humanverify.ServiceConfig{
			Enabled: true,
			EnabledPurposes: map[humanverify.Purpose]bool{
				humanverify.PurposePasswordReset: true,
			},
		},
		fakeAltchaProvider{}, // 对任意非空 token 返回成功
		nil,
	)
	app := newIdentityTestApp(t, verifier)

	// 嵌套结构携带 altcha provider 与非空 token。
	resp := performPasswordResetRequest(t, app, map[string]any{
		"email": "nobody@example.com",
		"humanVerification": map[string]any{
			"provider": "altcha",
			"token":    "fake-token",
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 (nested token should pass verification), got %d", resp.StatusCode)
	}
}
