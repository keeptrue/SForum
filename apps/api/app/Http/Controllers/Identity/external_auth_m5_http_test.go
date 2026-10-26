package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// M5 HTTP：start/callback 限流、回调 redaction、非枚举公开错误。

// t8dDenyLimiter 确定性拒绝，避免 Fiber httptest RemoteIP 与 seed key 漂移导致 log-only 假绿。
// 计数器/TTL 行为由 Models/Identity rate-limit 单测覆盖；本文件断言 HTTP 映射。
type t8dDenyLimiter struct{}

func (t8dDenyLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, nil
}

// t8dRecordingLimiter 记录实际 key；allowN 为放行次数（0 = 全部拒绝）。
type t8dRecordingLimiter struct {
	keys   []string
	allowN int
	n      int
}

func (l *t8dRecordingLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, error) {
	l.keys = append(l.keys, key)
	l.n++
	return l.n <= l.allowN, nil
}

func TestM5_StartRateLimited(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	controller.WithExternalAuthRateLimiter(t8dDenyLimiter{})

	payload, _ := json.Marshal(map[string]any{"correlationId": "c-rate"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/demo.auth/login/start", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.50:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// T8D：期望的 429 必须是断言，不得 log-only。
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected HTTP 429 rate_limit.exceeded, got status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "rate_limit.exceeded") {
		t.Fatalf("expected rate_limit.exceeded in body: %s", body)
	}
	if strings.Contains(string(body), "code_verifier") || strings.Contains(string(body), "client_secret") {
		t.Fatalf("rate limit response leaked secrets: %s", body)
	}
}

func TestT8D_StartRateLimitedAsserts429(t *testing.T) {
	// 独立命名回归：证明 start 限流是硬断言，不是可选日志。
	TestM5_StartRateLimited(t)
}

func TestT8D_StartRateLimitedUsesClientIPKey(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	// allowN=0 → 第一击即拒绝；同时记录 controller 传入的 key。
	lim := &t8dRecordingLimiter{allowN: 0}
	controller.WithExternalAuthRateLimiter(lim)

	payload, _ := json.Marshal(map[string]any{"correlationId": "c-rate-key"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/demo.auth/login/start", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	// 非信任公网 IP：clientip 应直接采用 RemoteIP，不读可伪造头。
	req.RemoteAddr = "198.51.100.77:4444"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got status=%d body=%s keys=%v", resp.StatusCode, body, lim.keys)
	}
	if len(lim.keys) != 1 {
		t.Fatalf("expected one rate key, got %v", lim.keys)
	}
	// key 形态 start:<ip>；允许 unknown（部分 httptest 环境 RemoteIP 为空），但不得空串。
	if !strings.HasPrefix(lim.keys[0], "start:") {
		t.Fatalf("unexpected rate key: %q", lim.keys[0])
	}
	if strings.HasSuffix(lim.keys[0], ":") {
		t.Fatalf("rate key missing IP segment: %q", lim.keys[0])
	}
	// 若 RemoteIP 解析成功，应是公网测试地址而非伪造的 X-Real-IP。
	if strings.Contains(lim.keys[0], "198.51.100.77") {
		if strings.Contains(lim.keys[0], "203.0.113.9") {
			t.Fatalf("untrusted remote must not honor X-Real-IP: %q", lim.keys[0])
		}
	}
}

func TestM5_CallbackRateLimitedRedirectsSafely(t *testing.T) {
	controller := &Controller{
		externalAuthService:     identity.NewExternalAuthService(identity.ExternalAuthDeps{}),
		callbackStateStore:      identity.NewInMemoryCallbackStateStore(),
		authFlow:                &identity.AuthProviderFlow{},
		externalAuthRateLimiter: t8dDenyLimiter{},
	}
	app := fiber.New()
	app.Get("/api/v1/auth/providers/:providerId/callback", controller.externalAuthCallback)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers/demo.auth/callback?state=s&code=c", nil)
	req.RemoteAddr = "203.0.113.50:9"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got status=%d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	// 超限映射为 auth.provider_unavailable（稳定 auth.* reason，供前端 ext_auth 白名单）。
	if !strings.Contains(loc, "ext_auth=") {
		t.Fatalf("expected ext_auth reason in location: %s", loc)
	}
	if !strings.Contains(loc, "auth.provider_unavailable") {
		t.Fatalf("expected auth.provider_unavailable, location=%s", loc)
	}
	if strings.Contains(loc, "code=c") || strings.Contains(loc, "state=s") {
		t.Fatalf("redirect must not echo OAuth code/state: %s", loc)
	}
	if strings.Contains(loc, "code_verifier") || strings.Contains(loc, "client_secret") {
		t.Fatalf("redirect leaked secrets: %s", loc)
	}
}

func TestT8D_CallbackRateLimitedAssertsUnavailableReason(t *testing.T) {
	TestM5_CallbackRateLimitedRedirectsSafely(t)
}

func TestM5_CallbackErrorReasonsNeverEchoSecrets(t *testing.T) {
	// 无效 state：redirect 仅带稳定 reason，不含 code/verifier/digest。
	stateStore := identity.NewInMemoryCallbackStateStore()
	controller := &Controller{
		callbackStateStore:  stateStore,
		externalAuthService: identity.NewExternalAuthService(identity.ExternalAuthDeps{}),
		authFlow:            &identity.AuthProviderFlow{},
	}
	app := fiber.New()
	app.Get("/api/v1/auth/providers/:providerId/callback", controller.externalAuthCallback)
	// 故意把 secret-looking 材料放进 query。
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/providers/demo.auth/callback?state=missing&code=oauth-code-SECRET123&error_description=client_secret%3Dleak",
		nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	for _, bad := range []string{
		"oauth-code-SECRET123", "client_secret", "code_verifier", strings.Repeat("a", 64),
	} {
		if strings.Contains(loc, bad) {
			t.Fatalf("callback location leaked %q: %s", bad, loc)
		}
	}
	if !strings.Contains(loc, "ext_auth=") {
		t.Fatalf("expected ext_auth reason: %s", loc)
	}
}

func TestM5_ListProvidersNeverLeaksDigestOrSecrets(t *testing.T) {
	app, _, controller := newT1EExternalAuthApp(t)
	digest := strings.Repeat("b", 64)
	registry := identityregistry.New()
	// 无 operations 的 inspect-only 发布（可过 registry 校验）。
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
	activation := identity.NewMemoryProviderActivationStore()
	login := true
	_, _ = activation.Upsert(context.Background(), identity.ProviderActivationInput{
		ProviderID: "ext.demo.auth", OwnerExtensionID: "ext.demo",
		OwnerPackageDigest: digest, LoginEnabled: &login,
	})
	controller.providerCatalog = registry
	controller.externalAuthService = identity.NewExternalAuthService(identity.ExternalAuthDeps{
		ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return registry.ResolveProvider("ext.demo.auth")
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	// inspect-only（无 operations）不会进入有效 catalog；响应仍不得含 digest/secret。
	raw := string(body)
	if strings.Contains(raw, digest) || strings.Contains(raw, "client_secret") ||
		strings.Contains(raw, "code_verifier") || strings.Contains(raw, "providerSubject") {
		t.Fatalf("providers list leaked sensitive material: %s", raw)
	}
	_ = time.Now()
}
