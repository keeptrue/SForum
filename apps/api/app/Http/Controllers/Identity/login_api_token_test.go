package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

// memoryAPITokenStore 是 PAT 内存存储，供登录签发链路做端到端断言。
type memoryAPITokenStore struct {
	mu     sync.Mutex
	nextID int64
	rows   []apitokens.Record
}

func newMemoryAPITokenStore() *memoryAPITokenStore { return &memoryAPITokenStore{nextID: 1} }

func (s *memoryAPITokenStore) Create(_ context.Context, userID int64, publicID, tokenHash, name string, scopes []string, expiresAt *time.Time) (apitokens.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := apitokens.Record{
		ID: s.nextID, UserID: userID, PublicID: publicID, TokenHash: tokenHash, Name: name,
		Scopes: append([]string{}, scopes...), ExpiresAt: expiresAt, CreatedAt: time.Now().UTC(),
	}
	s.nextID++
	s.rows = append(s.rows, record)
	return record, nil
}

func (s *memoryAPITokenStore) ListByUser(_ context.Context, userID int64, includeRevoked bool) ([]apitokens.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apitokens.Record, 0, len(s.rows))
	for _, row := range s.rows {
		if row.UserID != userID {
			continue
		}
		if row.RevokedAt != nil && !includeRevoked {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *memoryAPITokenStore) GetByPublicID(_ context.Context, publicID string) (apitokens.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.PublicID == publicID {
			return row, nil
		}
	}
	return apitokens.Record{}, apitokens.ErrTokenNotFound
}

func (s *memoryAPITokenStore) GetByIDForUser(_ context.Context, userID, id int64) (apitokens.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.ID == id && row.UserID == userID {
			return row, nil
		}
	}
	return apitokens.Record{}, apitokens.ErrTokenNotFound
}

func (s *memoryAPITokenStore) Revoke(_ context.Context, userID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.rows {
		if s.rows[index].ID == id && s.rows[index].UserID == userID {
			now := time.Now().UTC()
			s.rows[index].RevokedAt = &now
			return nil
		}
	}
	return apitokens.ErrTokenNotFound
}

func (s *memoryAPITokenStore) TouchLastUsed(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.rows {
		if s.rows[index].ID == id {
			now := time.Now().UTC()
			s.rows[index].LastUsedAt = &now
			return nil
		}
	}
	return nil
}

func (s *memoryAPITokenStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func newLoginAPITokenTestApp(t *testing.T) (*fiber.App, *memoryAPITokenStore) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "login-apitoken-test-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	tokenStore := newMemoryAPITokenStore()
	tokenService := apitokens.NewService(tokenStore, store)
	controller := NewControllerWithAuthSessions(service, manager, nil).WithAPITokens(tokenService)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
		BearerTokens:   apphttp.TokenServiceAdapter{Service: tokenService},
	})
	return app, tokenStore
}

func registerTestAccount(t *testing.T, app *fiber.App) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"username": "alice", "email": "alice@example.com", "password": "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/register", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("register request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("register expected 201, got %d", resp.StatusCode)
	}
}

func decodeEnvelope(t *testing.T, resp *nethttp.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope %s: %v", string(body), err)
	}
	return envelope
}

func dataObject(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %#v", envelope["data"])
	}
	return data
}

func errorReason(t *testing.T, envelope map[string]any) string {
	t.Helper()
	data := dataObject(t, envelope)
	reason, _ := data["reason"].(string)
	return reason
}

// 原生客户端登录：一次请求拿到会话与 PAT，且该 PAT 能直接驱动后续 Bearer 请求。
func TestLoginIssuesAPITokenForNativeClient(t *testing.T) {
	app, tokenStore := newLoginAPITokenTestApp(t)
	registerTestAccount(t, app)

	resp := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"issueApiToken": map[string]any{
			"name":   "SForum iOS",
			"scopes": []string{identity.PermissionTopicCreate},
		},
	})
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("login expected 200, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected session cookie on login response")
	}
	envelope := decodeEnvelope(t, resp)
	data := dataObject(t, envelope)

	user, ok := data["user"].(map[string]any)
	if !ok || user["username"] != "alice" {
		t.Fatalf("login data.user unexpected: %#v", data["user"])
	}
	issued, ok := data["apiToken"].(map[string]any)
	if !ok {
		t.Fatalf("login data.apiToken missing: %#v", data)
	}
	plaintext, _ := issued["token"].(string)
	if !strings.HasPrefix(plaintext, apitokens.TokenPrefix) {
		t.Fatalf("issued token must use the sft_ prefix, got %q", plaintext)
	}
	if issued["name"] != "SForum iOS" {
		t.Fatalf("issued token name unexpected: %#v", issued["name"])
	}
	if tokenStore.count() != 1 {
		t.Fatalf("expected exactly one stored token, got %d", tokenStore.count())
	}

	// 令牌必须立即可用：后续请求只带 Bearer，不再依赖 cookie。
	sessionReq := httptest.NewRequest(nethttp.MethodGet, "/api/v1/auth/session", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+plaintext)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("bearer session request failed: %v", err)
	}
	if sessionResp.StatusCode != nethttp.StatusOK {
		t.Fatalf("bearer session expected 200, got %d", sessionResp.StatusCode)
	}
	sessionEnvelope := decodeEnvelope(t, sessionResp)
	if username, _ := dataObject(t, sessionEnvelope)["username"].(string); username != "alice" {
		t.Fatalf("bearer session returned unexpected user %q", username)
	}
}

// 未请求 issueApiToken 时响应 data 保持既有 CurrentUser 形状（浏览器路径不回归）。
func TestLoginWithoutIssueAPITokenKeepsCurrentUserShape(t *testing.T) {
	app, tokenStore := newLoginAPITokenTestApp(t)
	registerTestAccount(t, app)

	resp := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
	})
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("login expected 200, got %d", resp.StatusCode)
	}
	data := dataObject(t, decodeEnvelope(t, resp))
	if _, exists := data["apiToken"]; exists {
		t.Fatalf("apiToken must not appear without issueApiToken: %#v", data)
	}
	if _, exists := data["user"]; exists {
		t.Fatalf("data must stay CurrentUser-shaped: %#v", data)
	}
	if data["username"] != "alice" {
		t.Fatalf("data must stay CurrentUser-shaped: %#v", data)
	}
	if tokenStore.count() != 0 {
		t.Fatalf("no token should be minted, got %d", tokenStore.count())
	}
}

// 形状错误必须在签发会话前失败，且不得留下令牌或半成功状态。
func TestLoginAPITokenRequestValidation(t *testing.T) {
	app, tokenStore := newLoginAPITokenTestApp(t)
	registerTestAccount(t, app)

	cases := []struct {
		name   string
		body   map[string]any
		reason string
	}{
		{
			name:   "missing name",
			body:   map[string]any{"name": "  ", "scopes": []string{identity.PermissionTopicCreate}},
			reason: "api_token.name_required",
		},
		{
			name:   "missing scopes",
			body:   map[string]any{"name": "SForum iOS"},
			reason: "api_token.scopes_required",
		},
		{
			name:   "blank scopes",
			body:   map[string]any{"name": "SForum iOS", "scopes": []string{" "}},
			reason: "api_token.scopes_required",
		},
		{
			name:   "invalid expiry",
			body:   map[string]any{"name": "SForum iOS", "scopes": []string{identity.PermissionTopicCreate}, "expiresAt": "not-a-time"},
			reason: "api_token.invalid",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := map[string]any{"login": "alice", "password": "correct horse battery staple", "issueApiToken": testCase.body}
			resp := performLogin(t, app, body)
			if resp.StatusCode != nethttp.StatusUnprocessableEntity {
				resp.Body.Close()
				t.Fatalf("expected 422, got %d", resp.StatusCode)
			}
			if cookies := resp.Cookies(); len(cookies) != 0 {
				resp.Body.Close()
				t.Fatalf("invalid token request must not issue a session, got cookies %#v", cookies)
			}
			envelope := decodeEnvelope(t, resp)
			if reason := errorReason(t, envelope); reason != testCase.reason {
				t.Fatalf("expected reason %q, got %q", testCase.reason, reason)
			}
			if tokenStore.count() != 0 {
				t.Fatalf("no token should be minted, got %d", tokenStore.count())
			}
		})
	}
}

// 令牌权限必须是账号当前权限的子集：越权 scope 被拒绝，且不落库。
func TestLoginAPITokenScopeMustBeSubsetOfActorPermissions(t *testing.T) {
	app, tokenStore := newLoginAPITokenTestApp(t)
	registerTestAccount(t, app)

	denied := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"issueApiToken": map[string]any{
			"name":   "SForum iOS",
			"scopes": []string{identity.PermissionTopicCreate, identity.PermissionSettingsSiteManage},
		},
	})
	if denied.StatusCode != nethttp.StatusUnprocessableEntity {
		denied.Body.Close()
		t.Fatalf("scope escalation expected 422, got %d", denied.StatusCode)
	}
	if reason := errorReason(t, decodeEnvelope(t, denied)); reason != "api_token.invalid" {
		t.Fatalf("expected api_token.invalid, got %q", reason)
	}
	if tokenStore.count() != 0 {
		t.Fatalf("denied scope must not persist a token, got %d", tokenStore.count())
	}

	allowed := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"issueApiToken": map[string]any{
			"name":      "SForum iOS",
			"scopes":    []string{identity.PermissionTopicCreate},
			"expiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		},
	})
	if allowed.StatusCode != nethttp.StatusOK {
		allowed.Body.Close()
		t.Fatalf("allowed scope expected 200, got %d", allowed.StatusCode)
	}
	data := dataObject(t, decodeEnvelope(t, allowed))
	issued, _ := data["apiToken"].(map[string]any)
	if issued["expiresAt"] == nil {
		t.Fatalf("expiry must round-trip into the issued token: %#v", issued)
	}
}

// 令牌服务未接线时，显式请求签发必须明确失败，而不是静默跳过。
func TestLoginAPITokenRequestRequiresTokenService(t *testing.T) {
	store := newSessionTestStore()
	service := identity.NewService(store)
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{HashSecret: "login-apitoken-test-secret", SessionStore: store, TokenVersion: store.GetUserTokenVersion},
	)
	controller := NewControllerWithAuthSessions(service, manager, nil)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	registerTestAccount(t, app)

	resp := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"issueApiToken": map[string]any{"name": "SForum iOS", "scopes": []string{identity.PermissionTopicCreate}},
	})
	// 未装配 PAT 服务等同于该能力不可用。
	if resp.StatusCode != nethttp.StatusServiceUnavailable {
		resp.Body.Close()
		t.Fatalf("expected 503 without token service, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
