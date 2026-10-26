package extensionsruntime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

const (
	aiCommandFixtureID    = "sforum.ai-command-reference"
	aiCommandFixtureHook  = "sforum.ai-command-reference.hook.complete"
	aiCommandFixtureName  = "sforum.ai-command-reference.complete-requested"
	aiCommandFixturePurp  = "sforum.ai-command-reference.summary"
	aiCommandFixtureGrant = int64(1201)
)

// M1 的端到端验证：真实插件子进程发起 sforum.ai.complete@1，宿主在同一个网关里
// 做闸门、记账与执行 trace。四个用例分别覆盖「成功并记账」「扩展日配额用尽」
// 「站点开关关闭零出站」「能力未授予」。
func TestAIPluginCommandReferenceEntersHostGatesAndAccounting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping AI command reference plugin subprocess build in short mode")
	}
	extension := buildReferenceFixtureExtension(t, "sforum-ai-command-reference", aiCommandFixtureID, aiCommandFixtureGrant, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := newMigratedFixtureDatabase(t, ctx, "ai_command_reference")
	grantID := seedReferenceArtifact(t, ctx, pool, &extension)

	provider := newAIPluginCommandProvider(t)
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	profile := settings.Profiles[0]
	profile.BaseURL = provider.server.URL
	profile.Enabled = true
	settings.Profiles[0] = profile
	// 配额取 1：第二次调用必须撞扩展日配额，而且这次拒绝不允许产生任何出站。
	settings.Gates.ExtensionDailyQuota = 1
	store := supportai.NewPostgresStore(pool)
	if _, err := store.SaveSettings(ctx, settings, 0); err != nil {
		t.Fatalf("save ai settings: %v", err)
	}
	secrets := newAIPluginCommandSecrets(t, ctx, pool, profile.APIKeyRef)
	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: store, Usage: store, Traces: store,
		Creds: supportai.NewSecretStoreResolver(secrets),
		Invoker: supportai.NewHTTPInvoker(supportai.HTTPInvokerOptions{
			Client: provider.server.Client(), SkipURLValidation: true,
		}),
	})

	authority, err := hostapi.NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	commandRuntime, err := hostapi.NewPostgresProtocolV2CommandRuntime(hostapi.PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: authority,
		Jobs:                 supportjobs.NewDispatcher(aiCommandFixtureRiverClient{}),
		Moderation:           moderation.NewPostgresStore(pool),
		AttachmentStatuses:   protocolV2RouteAttachmentMutator{},
		AttachmentExtensions: protocolV2RouteAttachmentMutator{},
		AI:                   gateway,
	})
	if err != nil {
		t.Fatalf("build AI command runtime: %v", err)
	}
	hostGateway, _ := newProtocolV2HostGateway()
	if err := hostGateway.BindProtocolV2CommandRuntime(commandRuntime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hostGateway.Close() })
	// 能力源就是宿主生产用的解析函数（manifest → 能力集合），因此「未授予」是一次
	// 真实的清单差异，而不是测试里另写一套判定。
	capability := &aiCommandFixtureCapabilities{
		extensionID: extension.ID,
		keys:        resolvedAIFixtureCapabilities(extension.Manifest),
	}
	hostGateway.BindCommandCapabilitySource(capability)

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: strconv.FormatInt(grantID, 10), ImpactDigest: extension.PackageDigest,
		}},
		HostAPI: hostGateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start AI command fixture: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	invoke := func(idempotencyKey string) extensionsruntime.HookResult {
		t.Helper()
		return starter.InvokeHook(ctx, extension, extensionsruntime.HookInput{
			DeclarationID: aiCommandFixtureHook, Name: aiCommandFixtureName, Kind: "observe",
			ContractVersion: aiCommandFixtureHook + "@1",
			InputSchema:     "sforum.ai-command-reference.hook.complete.input@1",
			ResultSchema:    "sforum.ai-command-reference.hook.complete.result@1",
			FailurePolicy:   "fail_closed", Timeout: 5 * time.Second,
			Payload: map[string]any{
				"purpose": aiCommandFixturePurp, "costClass": "economy",
				"prompt": "summarize the fixture", "idempotencyKey": idempotencyKey,
			},
		})
	}
	assertRejected := func(t *testing.T, result extensionsruntime.HookResult, wantState, wantReason string) {
		t.Helper()
		if got := stringResultValue(result, "state"); got != wantState {
			t.Fatalf("state = %q, want %q; result=%#v", got, wantState, result)
		}
		if got := stringResultValue(result, "reason"); got != wantReason {
			t.Fatalf("reason = %q, want %q; result=%#v", got, wantReason, result)
		}
	}

	// 1) 已授予能力 + 站点开启 + 配额充足：一次真实补全，并且记账归属到调用方插件。
	accepted := invoke("ai-command-1")
	if !accepted.OK {
		t.Fatalf("granted plugin AI completion = %#v", accepted)
	}
	if got := stringResultValue(accepted, "state"); got != "COMMAND_STATE_COMMITTED" {
		t.Fatalf("state = %q, want COMMITTED; result=%#v", got, accepted)
	}
	if got := stringResultValue(accepted, "text"); got != "mock answer" {
		t.Fatalf("completion text = %q", got)
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls.Load())
	}
	if count := countAIFixtureRows(t, ctx, pool,
		`SELECT count(*) FROM ai_executions WHERE caller_extension_id=$1 AND purpose=$2 AND cost_class='economy' AND status='succeeded'`,
		aiCommandFixtureID, aiCommandFixturePurp); count != 1 {
		t.Fatalf("accounted extensions = %d, want the calling plugin once", count)
	}
	if count := countAIFixtureRows(t, ctx, pool,
		`SELECT count(*) FROM ai_usage_counters WHERE scope='extension' AND scope_key=$1 AND calls=1`,
		aiCommandFixtureID); count == 0 {
		t.Fatal("extension usage counter was not written for the calling plugin")
	}

	// 2) 额度用尽：第二次调用在出站之前被拒，并且只有调用方被拒。
	// 闸门在 Execute 的事务里判定，因此状态是 ROLLED_BACK 而不是 REJECTED。
	overQuota := invoke("ai-command-2")
	assertRejected(t, overQuota, "COMMAND_STATE_ROLLED_BACK", "ai.quota_exceeded")
	if provider.calls.Load() != 1 {
		t.Fatalf("quota rejection performed outbound calls: %d", provider.calls.Load())
	}
	if count := countAIFixtureRows(t, ctx, pool,
		`SELECT count(*) FROM ai_executions WHERE status='denied' AND gate_reason='quota_exceeded' AND gate_scope='extension'`); count == 0 {
		t.Fatal("quota rejection did not write a denied execution trace")
	}

	// 3) 站点开关关闭：零出站，并且原因是「网关关闭」而不是「供应商失败」。
	settings.Enabled = false
	if _, err := store.SaveSettings(ctx, settings, settings.Revision); err != nil {
		t.Fatalf("disable site gateway: %v", err)
	}
	disabled := invoke("ai-command-3")
	assertRejected(t, disabled, "COMMAND_STATE_ROLLED_BACK", "ai.gateway_disabled")
	if provider.calls.Load() != 1 {
		t.Fatalf("disabled gateway performed outbound calls: %d", provider.calls.Load())
	}
	if count := countAIFixtureRows(t, ctx, pool,
		`SELECT count(*) FROM ai_executions WHERE status='denied' AND gate_reason='gateway_disabled' AND gate_scope='site'`); count == 0 {
		t.Fatal("disabled gateway did not write a denied execution trace")
	}

	// 4) 能力未授予：命令目录在 Plan 阶段就拒绝（REJECTED，不进事务、不进网关），
	// 插件不能靠伪造请求绕过进程能力。
	capability.keys = nil
	ungranted := invoke("ai-command-4")
	assertRejected(t, ungranted, "COMMAND_STATE_REJECTED", "host.command_capability_denied")
	if provider.calls.Load() != 1 {
		t.Fatalf("ungranted capability performed outbound calls: %d", provider.calls.Load())
	}
}

type aiCommandFixtureProvider struct {
	server *httptest.Server
	calls  atomic.Int64
}

func newAIPluginCommandProvider(t *testing.T) *aiCommandFixtureProvider {
	t.Helper()
	provider := &aiCommandFixtureProvider{}
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"mock answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`))
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func newAIPluginCommandSecrets(t *testing.T, ctx context.Context, pool *pgxpool.Pool, reference string) *secretstore.Service {
	t.Helper()
	backing, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("secret store backing: %v", err)
	}
	secrets, err := secretstore.NewWithOptions(secretstore.Options{Store: backing, AllowTransparent: true})
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	ref, err := secretstore.ParseReference(reference)
	if err != nil {
		t.Fatalf("profile credential reference %q: %v", reference, err)
	}
	if _, err := secrets.Put(ctx, ref, []byte("sk-fixture"), secretstore.PutOptions{Actor: "ai-command-fixture"}); err != nil {
		t.Fatalf("put provider credential: %v", err)
	}
	return secrets
}

type aiCommandFixtureCapabilities struct {
	extensionID string
	keys        []string
}

func (s *aiCommandFixtureCapabilities) CapabilitiesFor(_ context.Context, extensionID string) (capabilities.Set, error) {
	if s == nil || extensionID != s.extensionID {
		return capabilities.NewSet(nil), nil
	}
	return capabilities.NewSet(s.keys), nil
}

func (s *aiCommandFixtureCapabilities) DeclaredJobKinds(context.Context, string) ([]string, error) {
	return nil, nil
}

func resolvedAIFixtureCapabilities(manifest extensions.Manifest) []string {
	keys, _ := extensionmanifest.ResolvedCapabilities(manifest)
	return keys
}

func stringResultValue(result extensionsruntime.HookResult, key string) string {
	value, _ := result.Result[key].(string)
	return value
}

func countAIFixtureRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

type aiCommandFixtureRiverClient struct{}

func (aiCommandFixtureRiverClient) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}

func (aiCommandFixtureRiverClient) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, nil
}

func (aiCommandFixtureRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, nil
}
