package ai_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 端到端集成测试：真实 Postgres（独立 schema）+ 真实 Secret Store + 受控出站
// 打到 mock 供应商。它验证的是行为，不是结构：闸门计数、凭证解密、协议解析、
// 记账、执行 trace 是否真的串起来了。
const aiFixtureSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY
);
CREATE TABLE ai_settings (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  document JSONB NOT NULL,
  revision BIGINT NOT NULL DEFAULT 1,
  updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE ai_usage_counters (
  scope TEXT NOT NULL CHECK (scope IN ('site', 'extension', 'user')),
  scope_key TEXT NOT NULL DEFAULT '',
  window_kind TEXT NOT NULL CHECK (window_kind IN ('minute', 'day', 'month')),
  window_start TIMESTAMPTZ NOT NULL,
  calls BIGINT NOT NULL DEFAULT 0,
  input_tokens BIGINT NOT NULL DEFAULT 0,
  output_tokens BIGINT NOT NULL DEFAULT 0,
  spend_micros BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (scope, scope_key, window_kind, window_start)
);
CREATE INDEX ai_usage_counters_window_idx ON ai_usage_counters (window_kind, window_start);
CREATE TABLE ai_executions (
  id BIGSERIAL PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  purpose TEXT NOT NULL,
  cost_class TEXT NOT NULL DEFAULT '',
  caller_extension_id TEXT NOT NULL DEFAULT '',
  profile_id TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'denied')),
  gate_reason TEXT NOT NULL DEFAULT '',
  gate_scope TEXT NOT NULL DEFAULT '',
  prompt_version TEXT NOT NULL DEFAULT '',
  config_revision BIGINT NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  spend_micros BIGINT NOT NULL DEFAULT 0,
  cache_hit BOOLEAN NOT NULL DEFAULT FALSE,
  error_summary TEXT NOT NULL DEFAULT '',
  resource_type TEXT NOT NULL DEFAULT '',
  resource_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ai_executions_created_idx ON ai_executions (created_at DESC);
CREATE INDEX ai_executions_resource_idx
  ON ai_executions (resource_type, resource_id, created_at DESC, id DESC);
CREATE TABLE secret_store (
  namespace TEXT NOT NULL,
  secret_id TEXT NOT NULL,
  version BIGINT NOT NULL CHECK (version > 0),
  value TEXT NOT NULL DEFAULT '',
  media_type TEXT NOT NULL DEFAULT 'text/plain',
  purposes TEXT[] NOT NULL DEFAULT '{}',
  revoked BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  created_by TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (namespace, secret_id, version)
);
`

func newAIFixturePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	schema := "ai_gateway_" + hex.EncodeToString(random)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create fixture schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, aiFixtureSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
		t.Fatalf("create fixture tables: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	return pool
}

type mockProvider struct {
	server *httptest.Server
	calls  atomic.Int64
	// status/latency 可被单个用例改写。
	status  atomic.Int64
	latency time.Duration
}

func newMockProvider(t *testing.T) *mockProvider {
	t.Helper()
	provider := &mockProvider{latency: 0}
	provider.status.Store(http.StatusOK)
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.calls.Add(1)
		if provider.latency > 0 {
			time.Sleep(provider.latency)
		}
		status := int(provider.status.Load())
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"mock provider rejected the request"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"mock answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":2}}}`))
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *mockProvider) profile() ai.Profile {
	profile := deepseekProfile()
	profile.BaseURL = p.server.URL
	profile.Defaults.TimeoutMS = 5_000
	return profile
}

// newIntegrationGateway 组装真实依赖：Secret Store（transparent cipher 仅测试用）
// + Postgres 存储 + 受控出站（跳过公网预检以允许回环 mock）。
func newIntegrationGateway(t *testing.T, pool *pgxpool.Pool, provider *mockProvider) *ai.Gateway {
	t.Helper()
	secretBacking, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("secret store backing: %v", err)
	}
	secrets, err := secretstore.NewWithOptions(secretstore.Options{Store: secretBacking, AllowTransparent: true})
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	if _, err := secrets.Put(context.Background(),
		secretstore.Ref{Namespace: "core", SecretID: "ai.deepseek.api_key"},
		[]byte("sk-integration"), secretstore.PutOptions{Actor: "test"}); err != nil {
		t.Fatalf("put secret: %v", err)
	}
	store := ai.NewPostgresStore(pool)
	return ai.NewGateway(ai.GatewayConfig{
		Settings: store,
		Usage:    store,
		Traces:   store,
		Creds:    ai.NewSecretStoreResolver(secrets),
		Invoker: ai.NewHTTPInvoker(ai.HTTPInvokerOptions{
			Client:            provider.server.Client(),
			SkipURLValidation: true,
		}),
		Cache: newMemoryCache(),
		Clock: fixtureNow,
	})
}

// fixtureNow 是所有集成用例共享的注入时钟。用量快照必须用它，否则分钟窗口
// 会与真实时间错开，断言看似随机失败。
func fixtureNow() time.Time {
	return time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
}

func enableProvider(t *testing.T, pool *pgxpool.Pool, provider *mockProvider) {
	t.Helper()
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0] = provider.profile()
	settings.Profiles[0].Enabled = true
	if _, err := ai.NewPostgresStore(pool).SaveSettings(context.Background(), settings, 0); err != nil {
		t.Fatalf("save settings: %v", err)
	}
}

func TestGatewayEndToEndAgainstPostgresAndMockProvider(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)
	enableProvider(t, pool, provider)
	ctx := context.Background()

	request := validRequest()
	request.Metadata.CallerExtensionID = "sforum.moderation-assistant"
	request.PromptVersion = "moderation@1"
	request.SubjectUserID = 4242

	result, err := gateway.Execute(ctx, request)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Text != "mock answer" || result.StopReason != ai.StopReasonEnd {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 4 || result.Usage.CachedTokens != 2 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Provider.Model != "deepseek-chat" || result.Provider.Protocol != ai.ProtocolOpenAIChat {
		t.Fatalf("provider artifact = %+v", result.Provider)
	}
	if result.ConfigRevision == 0 {
		t.Fatal("config revision must be snapshotted onto the result")
	}

	// 用量必须真的落库：站点分钟/日/月 + 插件日 + 用户日五个窗口。
	usage, err := ai.NewPostgresStore(pool).Snapshot(ctx, fixtureNow().Add(30*time.Second), "sforum.moderation-assistant", 4242)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if usage.Site.MinuteCalls != 1 || usage.Site.DayCalls != 1 || usage.Site.DayTokens != 15 {
		t.Fatalf("site usage = %+v", usage.Site)
	}
	if usage.Extension.DayCalls != 1 || usage.User.DayCalls != 1 {
		t.Fatalf("scoped usage = %+v / %+v", usage.Extension, usage.User)
	}

	// 执行 trace 必须可回放：归因、提示词版本、配置版本都在。
	traces, err := ai.NewPostgresStore(pool).ListExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("traces = %+v", traces)
	}
	trace := traces[0]
	if trace.Status != ai.ExecutionStatusSucceeded || trace.PromptVersion != "moderation@1" {
		t.Fatalf("trace = %+v", trace)
	}
	if trace.CallerExtensionID != "sforum.moderation-assistant" || trace.Model != "deepseek-chat" {
		t.Fatalf("trace attribution = %+v", trace)
	}
	if trace.InputTokens != 11 || trace.CachedTokens != 2 {
		t.Fatalf("trace usage = %+v", trace)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d", provider.calls.Load())
	}
}

func TestGatewaySecondIdenticalCallHitsCacheWithoutNewSpend(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)
	enableProvider(t, pool, provider)
	ctx := context.Background()
	request := validRequest()
	for i := 0; i < 2; i++ {
		if _, err := gateway.Execute(ctx, request); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("cache must absorb the second call, provider calls = %d", provider.calls.Load())
	}
	usage, err := ai.NewPostgresStore(pool).Snapshot(ctx, fixtureNow().Add(30*time.Second), "", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if usage.Site.DayCalls != 1 || usage.Site.MinuteCalls != 1 {
		t.Fatalf("a cache hit must not double count usage: %+v", usage.Site)
	}
}

func TestGatewayFailedCallStillConsumesTheCallQuota(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)
	enableProvider(t, pool, provider)
	ctx := context.Background()

	provider.status.Store(http.StatusInternalServerError)
	request := validRequest()
	request.Metadata.CallerExtensionID = "sforum.moderation-assistant"
	_, err := gateway.Execute(ctx, request)
	var unavailable *ai.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != "provider_failed" {
		t.Fatalf("err = %v", err)
	}
	traces, err := ai.NewPostgresStore(pool).ListExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(traces) != 1 || traces[0].Status != ai.ExecutionStatusFailed {
		t.Fatalf("failed call must leave a trace: %+v", traces)
	}
	if !strings.Contains(traces[0].ErrorSummary, "500") {
		t.Fatalf("error summary should carry the status: %+v", traces[0])
	}

	// 失败调用计次不计 token：重试不能逃逸配额，但也不该为没有产出付费。
	// 快照必须用与网关相同的注入时钟，否则分钟窗口对不上真实时间。
	usage, err := ai.NewPostgresStore(pool).Snapshot(ctx, fixtureNow().Add(30*time.Second), "sforum.moderation-assistant", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if usage.Site.MinuteCalls != 1 || usage.Extension.DayCalls != 1 {
		t.Fatalf("a failed attempt must count as one call: %+v", usage)
	}
	if usage.Site.DayTokens != 0 || usage.Extension.DayTokens != 0 {
		t.Fatalf("a failed attempt must not bill tokens: %+v", usage)
	}
}

func TestGatewayQuotaExhaustionIsRecordedAsDenied(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)

	store := ai.NewPostgresStore(pool)
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0] = provider.profile()
	settings.Profiles[0].Enabled = true
	settings.Gates.ExtensionDailyQuota = 1
	if _, err := store.SaveSettings(context.Background(), settings, 0); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	ctx := context.Background()
	request := validRequest()
	request.Metadata.CallerExtensionID = "vendor.plugin"
	request.PromptVersion = "moderation@1"
	if _, err := gateway.Execute(ctx, request); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	// 第二次换一个提示词版本，避免命中缓存。
	request.PromptVersion = "moderation@2"
	_, err := gateway.Execute(ctx, request)
	var unavailable *ai.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != ai.GateReasonQuotaExceeded {
		t.Fatalf("err = %v", err)
	}
	if unavailable.Posture != ai.FailurePostureOpen {
		t.Fatalf("posture = %q", unavailable.Posture)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("quota-exhausted call must not reach the provider: %d", provider.calls.Load())
	}
	traces, err := store.ListExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(traces) != 2 || traces[0].Status != ai.ExecutionStatusDenied {
		t.Fatalf("denied call must leave a trace: %+v", traces)
	}
	if traces[0].GateScope != ai.GateScopeExtension || traces[0].GateReason != ai.GateReasonQuotaExceeded {
		t.Fatalf("denied trace = %+v", traces[0])
	}
}

func newMemoryCache() ai.ResultCache { return &memoryCache{items: map[string]ai.CompletionResult{}} }

type memoryCache struct {
	items map[string]ai.CompletionResult
}

func (c *memoryCache) Get(_ context.Context, key string) (ai.CompletionResult, bool) {
	item, ok := c.items[key]
	return item, ok
}

func (c *memoryCache) Put(_ context.Context, key string, result ai.CompletionResult, _ time.Duration) {
	c.items[key] = result
}

// A3 的 L2 用例：真实 PostgreSQL 下按归属聚合同一次 run 的全部记录。
// 它验证的是索引与排序真的按预期工作，而不是 fake 的过滤逻辑。
func TestPostgresAggregatesExecutionsByRun(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)
	enableProvider(t, pool, provider)
	ctx := context.Background()
	store := ai.NewPostgresStore(pool)

	first := validRequest()
	first.Metadata.ResourceType = ai.ResourceTypeRun
	first.Metadata.ResourceID = "run-alpha"
	if _, err := gateway.Execute(ctx, first); err != nil {
		t.Fatalf("first execute: %v", err)
	}

	// 第二条属于同一个 run，但正文不同（避开结果缓存）。
	second := validRequest()
	second.System = "第二段系统指令"
	second.Metadata.ResourceType = ai.ResourceTypeRun
	second.Metadata.ResourceID = "run-alpha"
	if _, err := gateway.Execute(ctx, second); err != nil {
		t.Fatalf("second execute: %v", err)
	}

	// 第三条属于另一个 run：它不得出现在 run-alpha 的时间线里。
	other := validRequest()
	other.System = "第三段系统指令"
	other.Metadata.ResourceType = ai.ResourceTypeRun
	other.Metadata.ResourceID = "run-beta"
	if _, err := gateway.Execute(ctx, other); err != nil {
		t.Fatalf("third execute: %v", err)
	}

	timeline, err := store.ListRunExecutions(ctx, ai.ResourceTypeRun, "run-alpha", 50)
	if err != nil {
		t.Fatalf("list run executions: %v", err)
	}
	if len(timeline) != 2 {
		t.Fatalf("timeline = %+v", timeline)
	}
	for _, record := range timeline {
		if record.ResourceID != "run-alpha" || record.ResourceType != ai.ResourceTypeRun {
			t.Fatalf("timeline leaked another run: %+v", record)
		}
		if record.Status != ai.ExecutionStatusSucceeded {
			t.Fatalf("status = %q", record.Status)
		}
	}
	// 时间正序：created_at 相同（同一事务窗口内）时靠 id 兜底。
	if timeline[0].ID > timeline[1].ID {
		t.Fatalf("timeline must be ordered ascending: %+v", timeline)
	}

	// 未知 run 返回空列表（不是错误）。
	missing, err := store.ListRunExecutions(ctx, ai.ResourceTypeRun, "run-missing", 50)
	if err != nil {
		t.Fatalf("unknown run must not error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %+v", missing)
	}

	// 空 runId 直接返回空列表：A3 之前的历史行没有归属，拿空串查询不得变成全表扫描。
	empty, err := store.ListRunExecutions(ctx, ai.ResourceTypeRun, "", 50)
	if err != nil {
		t.Fatalf("empty run id must not error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

// A3 的 L2 用例：历史行（A3 之前的记录）resource_id 为空串，列表页照常可读，
// 时间线查询不会把它们捞出来。
func TestPostgresTimelineIgnoresLegacyRowsWithoutRunID(t *testing.T) {
	pool := newAIFixturePool(t)
	provider := newMockProvider(t)
	gateway := newIntegrationGateway(t, pool, provider)
	enableProvider(t, pool, provider)
	ctx := context.Background()
	store := ai.NewPostgresStore(pool)

	legacy := validRequest()
	if _, err := gateway.Execute(ctx, legacy); err != nil {
		t.Fatalf("legacy execute: %v", err)
	}
	traced := validRequest()
	traced.System = "带归属的那一次"
	traced.Metadata.ResourceType = ai.ResourceTypeRun
	traced.Metadata.ResourceID = "run-with-id"
	if _, err := gateway.Execute(ctx, traced); err != nil {
		t.Fatalf("traced execute: %v", err)
	}

	all, err := store.ListExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list executions = %+v", all)
	}
	legacyRows := 0
	for _, record := range all {
		if record.ResourceID == "" {
			legacyRows++
		}
	}
	if legacyRows != 1 {
		t.Fatalf("legacy row must keep an empty resource id: %+v", all)
	}

	timeline, err := store.ListRunExecutions(ctx, ai.ResourceTypeRun, "run-with-id", 50)
	if err != nil {
		t.Fatalf("list run executions: %v", err)
	}
	if len(timeline) != 1 || timeline[0].ResourceID != "run-with-id" {
		t.Fatalf("timeline = %+v", timeline)
	}
}
