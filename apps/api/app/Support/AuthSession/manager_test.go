package authsession

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
)

func TestStartResetsSessionAndStoresUser(t *testing.T) {
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{HashSecret: "test-secret"})
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		info, err := manager.Start(c, 42)
		if err != nil {
			return err
		}
		if info.ID == "" || info.Hash == "" {
			t.Fatalf("expected session info to include id and hash, got %#v", info)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected session cookie")
	}
}

func TestCurrentUserIDRefreshesAndRenewsSession(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
	})
	manager.now = func() time.Time { return baseTime }

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if !ok || userID != 42 {
			t.Fatalf("expected current user 42, got %d ok=%v", userID, ok)
		}
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/auth-state", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserIDWithoutRenewal(c)
		if err != nil {
			return err
		}
		if !ok || userID != 42 {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	oldCookie := loginResp.Cookies()[0]

	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	sessionReq := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	sessionReq.AddCookie(oldCookie)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	defer sessionResp.Body.Close()

	if sessionResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", sessionResp.StatusCode)
	}
	if len(sessionResp.Cookies()) == 0 {
		t.Fatal("expected refreshed session cookie")
	}
	newCookie := sessionResp.Cookies()[0]
	if newCookie.Value == oldCookie.Value {
		t.Fatal("expected renewed session id after renewal interval")
	}
	for _, credential := range []struct {
		name       string
		cookie     *http.Cookie
		wantStatus int
	}{
		{name: "old", cookie: oldCookie, wantStatus: fiber.StatusUnauthorized},
		{name: "new", cookie: newCookie, wantStatus: fiber.StatusNoContent},
	} {
		request := httptest.NewRequest(fiber.MethodGet, "/auth-state", nil)
		request.AddCookie(credential.cookie)
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("%s credential replay: %v", credential.name, err)
		}
		response.Body.Close()
		if response.StatusCode != credential.wantStatus {
			t.Fatalf("%s credential status=%d want=%d", credential.name, response.StatusCode, credential.wantStatus)
		}
	}
}

func TestCurrentUserIDRenewalGateDenyFailsClosed(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	gateCalls := 0
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalEffectGate: func(_ context.Context, userID int64, _ int64, _ RenewalEffect) error {
			gateCalls++
			if userID != 42 {
				t.Fatalf("renewal gate user=%d", userID)
			}
			return errors.New("session policy denied")
		},
	})
	manager.now = func() time.Time { return baseTime }

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		for lookup := 0; lookup < 2; lookup++ {
			userID, ok, err := manager.CurrentUserID(c)
			if err != nil || ok || userID != 0 {
				t.Fatalf("denied renew lookup %d user=%d ok=%v err=%v", lookup, userID, ok, err)
			}
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginResp.Body.Close()
	cookie := loginResp.Cookies()[0]

	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	sessionReq := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	sessionReq.AddCookie(cookie)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sessionResp.Body.Close()
	if sessionResp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d", sessionResp.StatusCode)
	}
	if gateCalls != 1 {
		t.Fatalf("gateCalls=%d", gateCalls)
	}
	if len(sessionResp.Cookies()) != 0 {
		t.Fatalf("denied renew emitted cookies=%#v", sessionResp.Cookies())
	}
}

func TestCurrentUserIDWithoutRenewalNeverCallsGate(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalEffectGate: func(context.Context, int64, int64, RenewalEffect) error {
			t.Fatal("Host-local authentication must not call renewal gate")
			return nil
		},
	})
	manager.now = func() time.Time { return baseTime }

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/revoke", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserIDWithoutRenewal(c)
		if err != nil || !ok || userID != 42 {
			t.Fatalf("Host-local auth user=%d ok=%t err=%v", userID, ok, err)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginResp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	revokeRequest := httptest.NewRequest(fiber.MethodGet, "/revoke", nil)
	revokeRequest.AddCookie(loginResp.Cookies()[0])
	revokeResponse, err := app.Test(revokeRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeResponse.Body.Close()
	if revokeResponse.StatusCode != fiber.StatusOK || len(revokeResponse.Cookies()) != 0 {
		t.Fatalf("Host-local response status=%d cookies=%#v", revokeResponse.StatusCode, revokeResponse.Cookies())
	}
}

func TestCurrentUserIDRepeatedLookupAfterDueRenewalRemainsAuthenticated(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	gateCalls := 0
	effectCalls := 0
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalEffectGate: func(ctx context.Context, userID int64, _ int64, effect RenewalEffect) error {
			gateCalls++
			if userID != 42 {
				t.Fatalf("renewal user=%d", userID)
			}
			effectCalls++
			return effect(ctx)
		},
	})
	manager.now = func() time.Time { return baseTime }

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/double-read", func(c fiber.Ctx) error {
		for index := 0; index < 2; index++ {
			userID, ok, err := manager.CurrentUserID(c)
			if err != nil || !ok || userID != 42 {
				t.Fatalf("lookup %d user=%d ok=%t err=%v", index, userID, ok, err)
			}
		}
		if sid, err := manager.CurrentSID(c); err != nil || sid == "" {
			t.Fatalf("sid=%q err=%v", sid, err)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginResponse, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	oldCookie := loginResponse.Cookies()[0]
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	request := httptest.NewRequest(fiber.MethodGet, "/double-read", nil)
	request.AddCookie(oldCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || gateCalls != 1 || effectCalls != 1 {
		t.Fatalf("status=%d gate=%d effect=%d", response.StatusCode, gateCalls, effectCalls)
	}
	if len(response.Cookies()) != 1 || response.Cookies()[0].Value == oldCookie.Value {
		t.Fatalf("renewed cookies=%#v old=%q", response.Cookies(), oldCookie.Value)
	}
}

func TestCurrentUserIDRejectsEscapedRenewalEffect(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	escaped := make(chan RenewalEffect, 1)
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalEffectGate: func(_ context.Context, _ int64, _ int64, effect RenewalEffect) error {
			escaped <- effect
			return nil
		},
	})
	manager.now = func() time.Time { return baseTime }
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserID(c)
		if err != nil || ok || userID != 0 {
			t.Fatalf("escaped effect user=%d ok=%t err=%v", userID, ok, err)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginResponse, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	request.AddCookie(loginResponse.Cookies()[0])
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || len(response.Cookies()) != 0 {
		t.Fatalf("status=%d cookies=%#v", response.StatusCode, response.Cookies())
	}
	if err := (<-escaped)(context.Background()); !errors.Is(err, ErrRenewalRejected) {
		t.Fatalf("escaped callback err=%v", err)
	}
}

func TestCurrentUserIDLegacyRenewalGateRemainsCompatible(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	gateCalls := 0
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalGate: func(context.Context, int64) error {
			gateCalls++
			return nil
		},
	})
	manager.now = func() time.Time { return baseTime }
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		_, _, err := manager.CurrentUserID(c)
		return err
	})
	loginResponse, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	request.AddCookie(loginResponse.Cookies()[0])
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || gateCalls != 1 || len(response.Cookies()) != 1 {
		t.Fatalf("status=%d gate=%d cookies=%#v", response.StatusCode, gateCalls, response.Cookies())
	}
}

func TestCurrentUserIDLegacyRenewalGateDenyFailsClosed(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	gateCalls := 0
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalGate: func(context.Context, int64) error {
			gateCalls++
			return errors.New("legacy renewal denied")
		},
	})
	manager.now = func() time.Time { return baseTime }
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		for lookup := 0; lookup < 2; lookup++ {
			userID, ok, err := manager.CurrentUserID(c)
			if err != nil || ok || userID != 0 {
				t.Fatalf("legacy deny lookup %d user=%d ok=%t err=%v", lookup, userID, ok, err)
			}
		}
		return c.SendStatus(fiber.StatusOK)
	})
	loginResponse, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	request.AddCookie(loginResponse.Cookies()[0])
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || gateCalls != 1 || len(response.Cookies()) != 0 {
		t.Fatalf("status=%d gate=%d cookies=%#v", response.StatusCode, gateCalls, response.Cookies())
	}
}

func TestCurrentUserIDRenewalEffectGateTakesPrecedence(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	effectCalls := 0
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		RenewalGate: func(context.Context, int64) error {
			t.Fatal("legacy gate ran despite exact effect gate")
			return nil
		},
		RenewalEffectGate: func(ctx context.Context, _ int64, _ int64, effect RenewalEffect) error {
			effectCalls++
			return effect(ctx)
		},
	})
	manager.now = func() time.Time { return baseTime }
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserID(c)
		if err != nil || !ok || userID != 42 {
			t.Fatalf("renewed user=%d ok=%t err=%v", userID, ok, err)
		}
		return c.SendStatus(fiber.StatusOK)
	})
	loginResponse, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResponse.Body.Close()
	oldCookie := loginResponse.Cookies()[0]
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	request.AddCookie(oldCookie)
	response, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusOK || effectCalls != 1 || len(response.Cookies()) != 1 ||
		response.Cookies()[0].Value == oldCookie.Value {
		t.Fatalf("status=%d effects=%d cookies=%#v", response.StatusCode, effectCalls, response.Cookies())
	}
}

// TestTokenVersionInvalidationAfterIncrement 验证 M8：当用户令牌版本号递增后（如密码重置），
// 旧 session 的 CurrentUserID 返回 ok=false，会话立即失效。
func TestTokenVersionInvalidationAfterIncrement(t *testing.T) {
	currentVersion := int64(5)
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret: "test-secret",
		TokenVersion: func(_ context.Context, _ int64) (int64, error) {
			return currentVersion, nil
		},
	})

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	app.Get("/session", func(c fiber.Ctx) error {
		_, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("expected current user to be valid before token version increment")
		}
		return c.SendStatus(fiber.StatusOK)
	})
	app.Get("/session-after-reset", func(c fiber.Ctx) error {
		_, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("expected current user to be INVALID after token version increment (password reset)")
		}
		return c.SendStatus(fiber.StatusOK)
	})

	// 1. 登录（写入版本号 5）。
	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	cookie := loginResp.Cookies()[0]

	// 2. 登录后立即校验会话有效。
	sessionReq := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	sessionReq.AddCookie(cookie)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	sessionResp.Body.Close()
	if sessionResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected session valid 200, got %d", sessionResp.StatusCode)
	}

	// 3. 模拟密码重置：递增令牌版本号。
	currentVersion = 6

	// 4. 同一 session cookie 校验：版本不匹配，会话应失效。
	resetReq := httptest.NewRequest(fiber.MethodGet, "/session-after-reset", nil)
	resetReq.AddCookie(cookie)
	resetResp, err := app.Test(resetReq)
	if err != nil {
		t.Fatalf("session-after-reset request failed: %v", err)
	}
	resetResp.Body.Close()
	if resetResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 (handler ok with invalid session), got %d", resetResp.StatusCode)
	}
}

// fakeSessionStore 是会话目录的内存实现，用于测试 Manager 与目录的交互。
type fakeSessionStore struct {
	mu          sync.Mutex
	createErr   error
	created     []SessionRecordInput
	revoked     map[string]bool // sid -> revoked
	touched     []string
	revokeCalls []fakeRevoke
}

type fakeRevoke struct {
	userID int64
	sid    string
	reason string
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{revoked: map[string]bool{}}
}

func (f *fakeSessionStore) CreateSession(_ context.Context, input SessionRecordInput) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, input)
	f.revoked[input.SID] = false
	return nil
}

func (f *fakeSessionStore) IsSessionRevoked(_ context.Context, _ int64, sid string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	revoked, ok := f.revoked[sid]
	if !ok {
		return true, nil // 不存在视为已撤销（保守拒绝）
	}
	return revoked, nil
}

func (f *fakeSessionStore) TouchSessionLastSeen(_ context.Context, _ int64, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, sid)
	return nil
}

func (f *fakeSessionStore) RevokeSession(_ context.Context, userID int64, sid string, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[sid] = true
	f.revokeCalls = append(f.revokeCalls, fakeRevoke{userID: userID, sid: sid, reason: reason})
	return nil
}

// TestBeginGeneratesSIDAndRegistersInDirectory 验证：Begin 生成稳定 sid 写入 payload，
// Save 后会话目录登记一条记录（含 device 信息）。
func TestBeginGeneratesSIDAndRegistersInDirectory(t *testing.T) {
	dir := newFakeSessionStore()
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})

	var pendingSID string
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		if pending.Info().SID == "" {
			t.Fatal("expected Begin to generate a non-empty SID")
		}
		pendingSID = pending.Info().SID
		// 调用方在 Save 前设置解析好的设备信息。
		pending.SetDeviceInfo(SessionRecordInput{
			UserID: 42, DeviceName: "Chrome on macOS", Browser: "Chrome", OS: "macOS",
			IPAddress: "1.2.3.4", IPPrefix: "1.2.3.*", UserAgentRaw: "Mozilla/5.0",
		})
		return pending.Save()
	})

	req := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	dir.mu.Lock()
	createdCount := len(dir.created)
	var rec SessionRecordInput
	if createdCount > 0 {
		rec = dir.created[0]
	}
	dir.mu.Unlock()

	if createdCount != 1 {
		t.Fatalf("expected 1 session registered in directory, got %d", createdCount)
	}
	if rec.SID != pendingSID {
		t.Fatalf("expected directory SID %q to match pending SID %q", rec.SID, pendingSID)
	}
	if rec.UserID != 42 || rec.DeviceName != "Chrome on macOS" {
		t.Fatalf("unexpected directory record: %+v", rec)
	}
}

// TestPendingSaveReturnsDirectoryCreateError 验证：当会话目录 CreateSession 失败时，
// Pending.Save 返回错误而非静默成功。
//
// 为什么不能忽略：CurrentUserID 后续用 IsSessionRevoked 校验 sid，若 CreateSession 失败
// 导致 user_sessions 无此 sid，IsSessionRevoked 会保守返回 revoked=true，用户登录成功后
// 下一次请求立即被判定未登录。返回错误让 controller 直接拒绝本次登录，避免这种不一致。
func TestPendingSaveReturnsDirectoryCreateError(t *testing.T) {
	dir := newFakeSessionStore()
	dir.createErr = errors.New("directory unavailable")
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
		if err := pending.Save(); err != nil {
			// 目录写入失败应向上传播，让 controller 拒绝本次登录。
			return fiber.NewError(fiber.StatusServiceUnavailable, "session directory unavailable")
		}
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	// Save 返回错误后 handler 返回 503，登录被正确拒绝（而非静默成功）。
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Fatalf("expected 503 when directory create fails, got %d", resp.StatusCode)
	}
}

// TestCurrentUserIDInvalidatedWhenSessionRevoked 验证：当会话目录标记某 sid 已下线后，
// 该会话的下一次 CurrentUserID 返回 ok=false（实现「下次请求失效」）。
func TestCurrentUserIDInvalidatedWhenSessionRevoked(t *testing.T) {
	dir := newFakeSessionStore()
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
		return pending.Save()
	})
	app.Get("/session", func(c fiber.Ctx) error {
		_, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("expected current user to be INVALID after session revoked")
		}
		return c.SendStatus(fiber.StatusOK)
	})

	// 1. 登录。
	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	cookie := loginResp.Cookies()[0]

	// 2. 模拟「下线此设备」：在目录里标记该 sid revoked。
	dir.mu.Lock()
	for sid := range dir.revoked {
		dir.revoked[sid] = true
	}
	dir.mu.Unlock()

	// 3. 同一 cookie 再请求：CurrentUserID 应判定未登录。
	sessionReq := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	sessionReq.AddCookie(cookie)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	defer sessionResp.Body.Close()
	if sessionResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected 200 (handler ok with invalid session), got %d", sessionResp.StatusCode)
	}
}

// TestDestroyMarksSessionRevokedInDirectory 验证：logout(Destroy) 时在目录标记本会话为 logout。
func TestDestroyMarksSessionRevokedInDirectory(t *testing.T) {
	dir := newFakeSessionStore()
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
		return pending.Save()
	})
	app.Post("/logout", func(c fiber.Ctx) error {
		return manager.Destroy(c)
	})

	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	cookie := loginResp.Cookies()[0]

	logoutReq := httptest.NewRequest(fiber.MethodPost, "/logout", nil)
	logoutReq.AddCookie(cookie)
	logoutResp, err := app.Test(logoutReq)
	if err != nil {
		t.Fatalf("logout request failed: %v", err)
	}
	defer logoutResp.Body.Close()

	dir.mu.Lock()
	defer dir.mu.Unlock()
	if len(dir.revokeCalls) != 1 {
		t.Fatalf("expected 1 revoke call on logout, got %d", len(dir.revokeCalls))
	}
	if dir.revokeCalls[0].reason != "logout" {
		t.Fatalf("expected revoke reason 'logout', got %q", dir.revokeCalls[0].reason)
	}
	if dir.revokeCalls[0].userID != 42 {
		t.Fatalf("expected revoke for user 42, got %d", dir.revokeCalls[0].userID)
	}
}

// TestCurrentSIDReturnsStableSID 验证：CurrentSID 返回登录时的 sid，
// 且 cookie session id 续期轮换后 sid 不变（设备列表稳定）。
func TestCurrentSIDReturnsStableSID(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	dir := newFakeSessionStore()
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		RenewalInterval: time.Minute,
		HashSecret:      "test-secret",
		SessionStore:    dir,
	})
	manager.now = func() time.Time { return baseTime }

	var loginSID string
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		loginSID = pending.Info().SID
		pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
		return pending.Save()
	})
	app.Get("/sid", func(c fiber.Ctx) error {
		sid, err := manager.CurrentSID(c)
		if err != nil {
			return err
		}
		return c.Status(fiber.StatusOK).SendString(sid)
	})

	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer loginResp.Body.Close()
	cookie := loginResp.Cookies()[0]

	// 续期后 cookie session id 变了，但 sid 应保持不变。
	manager.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	sidReq := httptest.NewRequest(fiber.MethodGet, "/sid", nil)
	sidReq.AddCookie(cookie)
	sidResp, err := app.Test(sidReq)
	if err != nil {
		t.Fatalf("sid request failed: %v", err)
	}
	defer sidResp.Body.Close()

	body := make([]byte, 256)
	n, _ := sidResp.Body.Read(body)
	currentSID := string(body[:n])
	if currentSID != loginSID {
		t.Fatalf("expected SID to remain stable %q after renewal, got %q", loginSID, currentSID)
	}
}

// TestLegacySessionWithoutSIDStaysLoggedIn 验证平滑过渡：迁移前已存在的 Redis session
// 没有 sid payload（Begin 在新版本前创建），CurrentUserID 不应因此判定未登录。
// 这样部署升级后旧登录态不会集体失效，用户下次重新登录才补登记 sid。
func TestLegacySessionWithoutSIDStaysLoggedIn(t *testing.T) {
	dir := newFakeSessionStore()
	store := session.NewStore(session.Config{IdleTimeout: time.Hour})
	manager := NewManager(store, Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})

	app := fiber.New()
	// 模拟旧 session：绕过 Begin，直接用底层 store 写入一个无 sid 的 payload。
	app.Post("/seed-legacy", func(c fiber.Ctx) error {
		sess, err := store.Get(c)
		if err != nil {
			return err
		}
		if err := sess.Reset(); err != nil {
			return err
		}
		now := time.Now().UTC()
		sess.Set(sessionUserIDKey, int64(42))
		sess.Set(sessionCreatedAtKey, now)
		sess.Set(sessionRenewedAtKey, now)
		// 故意不设置 sessionSIDKey，模拟旧 session。
		return sess.Save()
	})
	app.Get("/session", func(c fiber.Ctx) error {
		userID, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if !ok || userID != 42 {
			t.Fatalf("expected legacy session to remain valid, got %d ok=%v", userID, ok)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	seedReq := httptest.NewRequest(fiber.MethodPost, "/seed-legacy", nil)
	seedResp, err := app.Test(seedReq)
	if err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	defer seedResp.Body.Close()
	cookie := seedResp.Cookies()[0]

	sessionReq := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	sessionReq.AddCookie(cookie)
	sessionResp, err := app.Test(sessionReq)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	defer sessionResp.Body.Close()
	if sessionResp.StatusCode != fiber.StatusOK {
		t.Fatalf("expected legacy session to stay logged in (200), got %d", sessionResp.StatusCode)
	}
}

// TestTouchSessionLastSeenThrottling 验证 TouchSessionLastSeen 的节流逻辑：
// 第一次请求触发展示，后续在 lastSeenInterval 内的请求不会重复触发展示，
// 只有超过该间隔才会再次触发。
func TestTouchSessionLastSeenThrottling(t *testing.T) {
	baseTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	dir := newFakeSessionStore()
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret:   "test-secret",
		SessionStore: dir,
	})
	manager.now = func() time.Time { return baseTime }

	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		pending, err := manager.Begin(c, 42)
		if err != nil {
			return err
		}
		pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
		return pending.Save()
	})
	app.Get("/session", func(c fiber.Ctx) error {
		_, ok, err := manager.CurrentUserID(c)
		if err != nil {
			return err
		}
		if !ok {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginReq := httptest.NewRequest(fiber.MethodPost, "/login", nil)
	loginResp, err := app.Test(loginReq)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	cookie := loginResp.Cookies()[0]
	loginResp.Body.Close()

	// 1. 第一次请求：由于 session 保存了最后活跃，第一次 CurrentUserID 会再次触发 TouchSessionLastSeen
	req1 := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	req1.AddCookie(cookie)
	resp1, _ := app.Test(req1)
	resp1.Body.Close()

	dir.mu.Lock()
	touchedCount := len(dir.touched)
	dir.mu.Unlock()
	if touchedCount != 1 {
		t.Fatalf("expected 1 touch call on first request, got %d", touchedCount)
	}

	// 2. 立即进行第二次请求（同一时间）：不应该触发 TouchSessionLastSeen
	req2 := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	req2.AddCookie(cookie)
	resp2, _ := app.Test(req2)
	resp2.Body.Close()

	dir.mu.Lock()
	touchedCount = len(dir.touched)
	dir.mu.Unlock()
	if touchedCount != 1 {
		t.Fatalf("expected still 1 touch call (throttled), got %d", touchedCount)
	}

	// 3. 推进时间 2 小时后请求：应该触发第二次 TouchSessionLastSeen
	manager.now = func() time.Time { return baseTime.Add(2 * time.Hour) }
	req3 := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	req3.AddCookie(cookie)
	resp3, _ := app.Test(req3)
	resp3.Body.Close()

	dir.mu.Lock()
	touchedCount = len(dir.touched)
	dir.mu.Unlock()
	if touchedCount != 2 {
		t.Fatalf("expected 2 touch calls after 2 hours, got %d", touchedCount)
	}
}

// revocationProbeStore 只改 IsSessionRevoked 的行为：用它分辨「目录说这个会话已下线」
// 与「目录答不上来」——前者是确定的身份结论，后者是依赖故障。
type revocationProbeStore struct {
	*fakeSessionStore
	revokeAll bool
	err       error
}

func (s *revocationProbeStore) IsSessionRevoked(context.Context, int64, string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.revokeAll {
		return true, nil
	}
	return false, nil
}

// TestCurrentUserIDSeparatesRevocationFromDirectoryFailure 钉住 B1 的正反两面：
//   - 目录明确说「已下线」→ 匿名（ok=false, err=nil），这是既有身份语义；
//   - 目录答不上来（PG/Redis 故障）→ ErrStoreUnavailable，让上层答 503，而不是把
//     全体在线用户静默登出。
func TestCurrentUserIDSeparatesRevocationFromDirectoryFailure(t *testing.T) {
	cases := []struct {
		name     string
		store    *revocationProbeStore
		wantAnon bool
		wantDep  bool
	}{
		{
			name:     "revoked session stays anonymous",
			store:    &revocationProbeStore{fakeSessionStore: newFakeSessionStore(), revokeAll: true},
			wantAnon: true,
		},
		{
			name:    "directory failure is a dependency failure",
			store:   &revocationProbeStore{fakeSessionStore: newFakeSessionStore(), err: errors.New("sessions table unavailable")},
			wantDep: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
				HashSecret: "test-secret", SessionStore: testCase.store,
			})
			app := fiber.New()
			app.Post("/login", func(c fiber.Ctx) error {
				pending, err := manager.Begin(c, 42)
				if err != nil {
					return err
				}
				pending.SetDeviceInfo(SessionRecordInput{UserID: 42, DeviceName: "Chrome"})
				return pending.Save()
			})
			var (
				gotID  int64
				gotOK  bool
				gotErr error
			)
			app.Get("/session", func(c fiber.Ctx) error {
				gotID, gotOK, gotErr = manager.CurrentUserID(c)
				return c.SendStatus(fiber.StatusOK)
			})

			loginResp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			loginResp.Body.Close()
			cookies := loginResp.Cookies()
			if len(cookies) == 0 {
				t.Fatal("expected a session cookie")
			}
			request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
			request.AddCookie(cookies[0])
			response, err := app.Test(request)
			if err != nil {
				t.Fatalf("session: %v", err)
			}
			response.Body.Close()

			switch {
			case testCase.wantAnon:
				if gotErr != nil || gotOK || gotID != 0 {
					t.Fatalf("a revoked session must be anonymous, got id=%d ok=%t err=%v", gotID, gotOK, gotErr)
				}
			case testCase.wantDep:
				if !errors.Is(gotErr, ErrStoreUnavailable) {
					t.Fatalf("a directory failure must be ErrStoreUnavailable, got %v", gotErr)
				}
				if gotOK {
					t.Fatalf("a dependency failure must not be reported as an authenticated session")
				}
				if !errors.Is(gotErr, testCase.store.err) {
					t.Fatalf("the original directory error must stay in the chain, got %v", gotErr)
				}
			}
		})
	}
}

// TestCurrentUserIDReportsTokenVersionFailure 钉住令牌版本来源（users.current_token_version）
// 的失败：它是依赖读，读不出来不能把已登录用户降级成匿名。
func TestCurrentUserIDReportsTokenVersionFailure(t *testing.T) {
	sourceErr := errors.New("users table unavailable")
	// 登录（Start）也要读一次版本号来写进 payload：先让它成功，再把来源打成故障，
	// 这样失败的正是「已登录会话的后续校验」这条路径。
	sourceFailing := false
	manager := NewManager(session.NewStore(session.Config{IdleTimeout: time.Hour}), Config{
		HashSecret: "test-secret",
		TokenVersion: func(context.Context, int64) (int64, error) {
			if sourceFailing {
				return 0, sourceErr
			}
			return 3, nil
		},
	})
	app := fiber.New()
	app.Post("/login", func(c fiber.Ctx) error {
		_, err := manager.Start(c, 42)
		return err
	})
	var (
		gotOK  bool
		gotErr error
	)
	app.Get("/session", func(c fiber.Ctx) error {
		_, gotOK, gotErr = manager.CurrentUserID(c)
		return c.SendStatus(fiber.StatusOK)
	})

	loginResp, err := app.Test(httptest.NewRequest(fiber.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	loginResp.Body.Close()
	cookies := loginResp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected a session cookie")
	}
	sourceFailing = true
	request := httptest.NewRequest(fiber.MethodGet, "/session", nil)
	request.AddCookie(cookies[0])
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	response.Body.Close()

	if !errors.Is(gotErr, ErrStoreUnavailable) || !errors.Is(gotErr, sourceErr) {
		t.Fatalf("expected ErrStoreUnavailable wrapping the source error, got %v", gotErr)
	}
	if gotOK {
		t.Fatal("a token version failure must not be reported as an authenticated session")
	}
}
