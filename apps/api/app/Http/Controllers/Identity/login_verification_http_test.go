package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	"github.com/keeptrue/sforum/apps/api/config"
)

type staticLoginVerificationPolicy struct {
	policy identity.LoginVerificationPolicy
}

func (p staticLoginVerificationPolicy) LoginVerificationPolicy(context.Context) (identity.LoginVerificationPolicy, error) {
	return p.policy, nil
}

// newLoginVerificationTestApp 组装登录强化用例：ALTCHA 只开 login_risk purpose，
// 策略由静态解析器给出；withTokens 时同时挂 PAT 服务以覆盖豁免路径。
func newLoginVerificationTestApp(t *testing.T, policy identity.LoginVerificationPolicy, withTokens bool) (*fiber.App, *sessionTestStore, *identity.Service) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	service.WithLoginVerification(staticLoginVerificationPolicy{policy: policy})
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "login-verification-test-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	verifier := humanverify.NewService(
		humanverify.ServiceConfig{
			Enabled:         true,
			EnabledPurposes: map[humanverify.Purpose]bool{humanverify.PurposeLoginRisk: true},
		},
		fakeAltchaProvider{},
		nil,
	)
	controller := NewControllerWithAuthSessions(service, manager, verifier)
	dependencies := apphttp.Dependencies{RouteProviders: []apphttp.RouteProvider{controller}}
	if withTokens {
		tokenService := apitokens.NewService(newMemoryAPITokenStore(), store)
		controller.WithAPITokens(tokenService)
		dependencies.BearerTokens = apphttp.TokenServiceAdapter{Service: tokenService}
	}
	return apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, dependencies), store, service
}

func registerAccountWithUserAgent(t *testing.T, app *fiber.App, userAgent string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"username": "alice", "email": "alice@example.com", "password": "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/register", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("register request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("register expected 201, got %d", resp.StatusCode)
	}
}

func performLoginWithUserAgent(t *testing.T, app *fiber.App, userAgent string, body map[string]any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/login", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	return resp
}

func TestLoginAlwaysTriggerRequiresHumanVerification(t *testing.T) {
	app, _, _ := newLoginVerificationTestApp(t, identity.LoginVerificationPolicy{Enabled: true, Always: true}, false)
	registerTestAccount(t, app)

	missing := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
	})
	missing.Body.Close()
	if missing.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("always trigger must require verification, got %d", missing.StatusCode)
	}

	valid := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"humanVerification": map[string]any{"provider": "altcha", "token": "valid-token"},
	})
	valid.Body.Close()
	if valid.StatusCode != nethttp.StatusOK {
		t.Fatalf("valid verification must allow login, got %d", valid.StatusCode)
	}
}

// 登录同时签发 PAT 的请求豁免人机验证。这是已记录的取舍：
// 该路径仍然签发浏览器会话 cookie，因此等同保留一处可绕过的验证豁免面。
func TestLoginAPITokenRequestSkipsHumanVerification(t *testing.T) {
	app, _, _ := newLoginVerificationTestApp(t, identity.LoginVerificationPolicy{Enabled: true, Always: true}, true)
	registerTestAccount(t, app)

	resp := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"issueApiToken": map[string]any{
			"name":   "SForum CLI",
			"scopes": []string{identity.PermissionTopicCreate},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("issueApiToken login must bypass verification, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("bypass path is expected to still issue a session cookie")
	}
}

func TestLoginNewDeviceTriggerUsesUserAgentFingerprint(t *testing.T) {
	app, store, service := newLoginVerificationTestApp(t, identity.LoginVerificationPolicy{Enabled: true, NewDevice: true}, false)
	registerAccountWithUserAgent(t, app, "SForumTest/1.0")
	if len(store.sessions) == 0 || store.sessions[0].userAgentRaw != "SForumTest/1.0" {
		t.Fatalf("registration must record the user agent fingerprint, got %#v", store.sessions)
	}

	userID := store.sessions[0].userID
	if known, err := service.HasKnownDevice(context.Background(), userID, "SForumTest/1.0"); err != nil || !known {
		t.Fatalf("registration fingerprint should be known: known=%v err=%v", known, err)
	}
	if known, err := service.HasKnownDevice(context.Background(), userID, "SForumTest/2.0"); err != nil || known {
		t.Fatalf("other fingerprint should be unknown: known=%v err=%v", known, err)
	}
	unknown := performLoginWithUserAgent(t, app, "SForumTest/2.0", map[string]any{
		"login": "alice", "password": "correct horse battery staple",
	})
	unknown.Body.Close()
	if unknown.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("unknown device must require verification, got %d", unknown.StatusCode)
	}

	known := performLoginWithUserAgent(t, app, "SForumTest/1.0", map[string]any{
		"login": "alice", "password": "correct horse battery staple",
	})
	known.Body.Close()
	if known.StatusCode != nethttp.StatusOK {
		t.Fatalf("known device must pass, got %d", known.StatusCode)
	}
}
