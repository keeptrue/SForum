package ai_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// TestGatewayLiveProviderCall 打一次真实供应商调用。默认跳过，只有显式提供
// SFORUM_AI_LIVE_KEY 时才运行：它会产生真实的、计费的出站请求。
//
//	SFORUM_AI_LIVE_KEY=sk-... SFORUM_TEST_DATABASE_URL=postgres://... \
//	  go test ./app/Support/AI/... -run LiveProvider -v
//
// 与 mock 用例的区别：这里刻意不跳过出站守卫，因此公网可达性、DNS 校验、
// 真实 TLS 与供应商的真实响应结构都会被走一遍。
func TestGatewayLiveProviderCall(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("SFORUM_AI_LIVE_KEY"))
	if apiKey == "" {
		t.Skip("SFORUM_AI_LIVE_KEY is not set; live provider call skipped")
	}
	pool := newAIFixturePool(t)
	ctx := context.Background()

	backing, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("secret backing: %v", err)
	}
	secrets, err := secretstore.NewWithOptions(secretstore.Options{Store: backing, AllowTransparent: true})
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	profile := deepseekProfile()
	if _, err := secrets.Put(ctx,
		secretstore.Ref{Namespace: "core", SecretID: "ai.deepseek.api_key"},
		[]byte(apiKey), secretstore.PutOptions{Actor: "live-check"}); err != nil {
		t.Fatalf("put secret: %v", err)
	}

	store := ai.NewPostgresStore(pool)
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0] = profile
	settings.Profiles[0].Enabled = true
	settings.Profiles[0].Defaults.MaxTokens = 32
	settings.Profiles[0].Defaults.TimeoutMS = 45_000
	settings.Gates = ai.GateSettings{RateLimitPerMinute: 5, ExtensionDailyQuota: 20, UserDailyQuota: 5, MonthlyBudgetMicros: ai.MicroPerUnit}
	if _, err := store.SaveSettings(ctx, settings, 0); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	// 出站走真实守卫：公网地址解析、TLS、超时都按生产路径执行。
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: store,
		Usage:    store,
		Traces:   store,
		Creds:    ai.NewSecretStoreResolver(secrets),
		Invoker:  ai.NewHTTPInvoker(ai.HTTPInvokerOptions{Timeout: 50 * time.Second}),
		Clock:    fixtureNow,
	})

	request := ai.CompletionRequest{
		Purpose:   "live.probe",
		CostClass: ai.CostClassEconomy,
		System:    "你只回复一个字，不要标点。",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "回复：好"}}},
		},
		MaxTokens:     16,
		PromptVersion: "live-probe@1",
		Metadata:      ai.Metadata{CallerExtensionID: "sforum.live-probe"},
	}

	result, err := gateway.Execute(ctx, request)
	if err != nil {
		t.Fatalf("live call failed: %v", err)
	}
	if strings.TrimSpace(result.Text) == "" {
		t.Fatalf("live provider returned empty text: %+v", result)
	}
	if result.Usage.InputTokens <= 0 || result.Usage.OutputTokens <= 0 {
		t.Fatalf("live provider returned no usage: %+v", result.Usage)
	}
	if result.StopReason != ai.StopReasonEnd {
		t.Fatalf("unexpected stop reason: %q", result.StopReason)
	}
	t.Logf("live reply=%q usage=%+v latencyMs=%d model=%s",
		result.Text, result.Usage, result.LatencyMS, result.Provider.Model)

	// 真实调用也必须留下可回放的 trace 与用量。
	traces, err := store.ListExecutions(ctx, 5)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(traces) != 1 || traces[0].Status != ai.ExecutionStatusSucceeded {
		t.Fatalf("live call trace = %+v", traces)
	}
	if traces[0].InputTokens != result.Usage.InputTokens {
		t.Fatalf("trace usage must match the result: %+v vs %+v", traces[0], result.Usage)
	}
	usage, err := store.Snapshot(ctx, fixtureNow().Add(30*time.Second), "sforum.live-probe", 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if usage.Site.DayCalls != 1 || usage.Extension.DayCalls != 1 {
		t.Fatalf("live usage accounting = %+v", usage)
	}
}
