package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	health "github.com/keeptrue/sforum/apps/api/app/Support/Health"
	"github.com/keeptrue/sforum/apps/api/config"
)

var errRedisDown = errors.New("redis: connection refused")

// 一次 Redis 抖动曾经让整个 /api/v1 变成 403/500：limiter 把 storage 错误抛成 500，
// csrf 把所有未知错误归成 403，于是「依赖不可用」被读成「请求不合法 / 没有权限」，
// 连就绪探针都在说胡话。这个用例把四条语义钉在一起：
//  1. 读请求不受影响；
//  2. 写请求 503 + Retry-After: 1（不是 403，也不是 500）；
//  3. /health 与 /ready 仍然给出各自的正确语义；
//  4. 至少一条 warn 日志带着 storage 的原始错误文本。
func TestStorageOutageFailsWritesClosedAndKeepsReadsAlive(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	app := apphttp.NewApp(storageOutageConfig(), logger, apphttp.Dependencies{
		Storage: outageStorage{err: errRedisDown},
		RouteProviders: []apphttp.RouteProvider{routeProviderFunc(func(api fiber.Router) {
			api.Get("/read", func(c fiber.Ctx) error { return c.SendString("read-ok") })
			api.Post("/write", func(c fiber.Ctx) error { return c.SendString("write-ok") })
		})},
		Ready: readyWithPGOnly,
	})

	get := func(path string, headers map[string]string) *http.Response {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("GET %s failed: %v", path, err)
		}
		return response
	}

	// csrf 在 GET 上也会记账（读 cookie + 续期 token），所以这条读请求正是
	// 「storage 故障会不会把读请求带下水」的判定点。
	read := get("/api/v1/read", map[string]string{fiber.HeaderCookie: "csrf_=stale-token"})
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("reads must survive a storage outage, got %d", read.StatusCode)
	}

	healthy := get("/api/v1/health", nil)
	defer healthy.Body.Close()
	if healthy.StatusCode != http.StatusOK {
		t.Fatalf("/health must stay 200 during a storage outage, got %d", healthy.StatusCode)
	}
	var healthBody apiEnvelope[struct {
		Status string `json:"status"`
	}]
	if err := json.NewDecoder(healthy.Body).Decode(&healthBody); err != nil {
		t.Fatalf("decode health envelope: %v", err)
	}
	if healthBody.Data.Status != "ok" {
		t.Fatalf("/health must keep reporting liveness, got %q", healthBody.Data.Status)
	}

	ready := get("/api/v1/ready", nil)
	defer ready.Body.Close()
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("/ready must stay 200 for a healthy PG with a degraded Redis, got %d", ready.StatusCode)
	}
	var readyBody apiEnvelope[struct {
		Ready bool `json:"ready"`
	}]
	if err := json.NewDecoder(ready.Body).Decode(&readyBody); err != nil {
		t.Fatalf("decode ready envelope: %v", err)
	}
	if !readyBody.Data.Ready {
		t.Fatal("/ready must keep its own verdict instead of inheriting the storage failure")
	}

	write, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/write", nil))
	if err != nil {
		t.Fatalf("write request failed: %v", err)
	}
	defer write.Body.Close()
	if write.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a write hitting a storage fault must be 503, got %d", write.StatusCode)
	}
	if retryAfter := write.Header.Get(fiber.HeaderRetryAfter); retryAfter != "1" {
		t.Fatalf("expected Retry-After: 1, got %q", retryAfter)
	}
	writeEnvelope := apiEnvelope[apiErrorData]{}
	if err := json.NewDecoder(write.Body).Decode(&writeEnvelope); err != nil {
		t.Fatalf("decode write envelope: %v", err)
	}
	if writeEnvelope.Data.Reason != "service.not_ready" {
		t.Fatalf("expected service.not_ready, got %q", writeEnvelope.Data.Reason)
	}
	if strings.Contains(writeEnvelope.Message, "csrf") {
		t.Fatalf("a storage outage must not be reported as a CSRF failure: %q", writeEnvelope.Message)
	}

	if logged := logs.String(); !strings.Contains(logged, errRedisDown.Error()) {
		t.Fatalf("expected the raw storage error in the warn log, got %q", logged)
	}
}

// CSRF 层单独命中 storage 故障（limiter 关闭）时，也必须 503，而不是它默认的 403。
func TestCSRFStorageOutageIsNotReportedAsForbidden(t *testing.T) {
	cfg := storageOutageConfig()
	cfg.LimiterWriteMax = 0 // 关掉 limiter，把故障点锁在 csrf 上
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		Storage: outageStorage{err: errRedisDown},
		RouteProviders: []apphttp.RouteProvider{routeProviderFunc(func(api fiber.Router) {
			api.Post("/write", func(c fiber.Ctx) error { return c.SendString("write-ok") })
		})},
		Ready: readyWithPGOnly,
	})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/write", nil)
	// double-submit 通过：cookie 与 header 一致，于是校验必须落到 storage 上。
	request.Header.Set(fiber.HeaderCookie, "csrf_=token-1")
	request.Header.Set("X-Csrf-Token", "token-1")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("write request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a csrf storage fault must be 503, got %d", response.StatusCode)
	}
	if retryAfter := response.Header.Get(fiber.HeaderRetryAfter); retryAfter != "1" {
		t.Fatalf("expected Retry-After: 1, got %q", retryAfter)
	}
	envelope := apiEnvelope[apiErrorData]{}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Data.Reason != "service.not_ready" {
		t.Fatalf("expected service.not_ready, got %q", envelope.Data.Reason)
	}
}

// 对照组：同一套配置换成可用 storage 时，写请求照常 200。
// 它把上面两条用例的 503 归因钉死在「storage 故障」上。
func TestWritesPassWithWorkingStorage(t *testing.T) {
	cfg := storageOutageConfig()
	cfg.LimiterWriteMax = 0
	app := apphttp.NewApp(cfg, slog.Default(), apphttp.Dependencies{
		Storage: &workingStorage{values: map[string][]byte{}},
		RouteProviders: []apphttp.RouteProvider{routeProviderFunc(func(api fiber.Router) {
			api.Get("/read", func(c fiber.Ctx) error { return c.SendString("read-ok") })
			api.Post("/write", func(c fiber.Ctx) error { return c.SendString("write-ok") })
		})},
		Ready: readyWithPGOnly,
	})

	// 先做一次读请求拿到 csrf token：csrf 是 double-submit + 服务端留痕，
	// 只有 cookie 与 header 一致但服务端没有这条 token 时，它同样是 403（合理拒绝）。
	handshake, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/read", nil))
	if err != nil {
		t.Fatalf("csrf handshake failed: %v", err)
	}
	handshake.Body.Close()
	token := ""
	for _, cookie := range handshake.Cookies() {
		if cookie.Name == "csrf_" {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("expected a csrf cookie from the read request")
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/write", nil)
	request.Header.Set(fiber.HeaderCookie, "csrf_="+token)
	request.Header.Set("X-Csrf-Token", token)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("write request failed: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with a working storage, got %d", response.StatusCode)
	}
}

func storageOutageConfig() config.Config {
	cfg := testConfig()
	cfg.CSRFEnabled = true
	// 较大的写额度：这里的 503 只能来自 storage 故障，不能是限流命中。
	cfg.LimiterWriteMax = 100
	cfg.LimiterWindow = time.Minute
	return cfg
}

func readyWithPGOnly(context.Context) health.ReadyReport {
	return health.ReadyReport{
		Status:     "ready",
		Ready:      true,
		CheckedAt:  time.Now().UTC(),
		Components: []health.ComponentResult{},
	}
}

// outageStorage 模拟 Redis 整体不可用：所有操作都失败（读也失败，这正是
// 「连会话/token 都读不出来」的形态）。
type outageStorage struct{ err error }

func (s outageStorage) GetWithContext(context.Context, string) ([]byte, error) {
	return nil, s.err
}

func (s outageStorage) Get(string) ([]byte, error) { return nil, s.err }

func (s outageStorage) SetWithContext(context.Context, string, []byte, time.Duration) error {
	return s.err
}

func (s outageStorage) Set(string, []byte, time.Duration) error { return s.err }

func (s outageStorage) DeleteWithContext(context.Context, string) error { return s.err }

func (s outageStorage) Delete(string) error { return s.err }

func (s outageStorage) ResetWithContext(context.Context) error { return s.err }

func (s outageStorage) Reset() error { return s.err }

func (s outageStorage) Close() error { return nil }

// workingStorage 是进程内存实现，只为对照组存在（不依赖真实 Redis）。
type workingStorage struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *workingStorage) GetWithContext(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *workingStorage) Get(key string) ([]byte, error) {
	return s.GetWithContext(context.Background(), key)
}

func (s *workingStorage) SetWithContext(_ context.Context, key string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *workingStorage) Set(key string, value []byte, exp time.Duration) error {
	return s.SetWithContext(context.Background(), key, value, exp)
}

func (s *workingStorage) DeleteWithContext(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func (s *workingStorage) Delete(key string) error {
	return s.DeleteWithContext(context.Background(), key)
}

func (s *workingStorage) ResetWithContext(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = map[string][]byte{}
	return nil
}

func (s *workingStorage) Reset() error { return s.ResetWithContext(context.Background()) }

func (s *workingStorage) Close() error { return nil }
