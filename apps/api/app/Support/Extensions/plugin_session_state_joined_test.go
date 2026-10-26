package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pluginsessionstate "github.com/keeptrue/sforum/apps/api/app/Support/PluginSessionState"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 这条测试证明 S9 的**整条真链**，插件侧不再是桩：
//
//	真实夹具 `sforum.session-checkout-reference` 的 Protocol V2 后端在临时目录现场
//	构建 → 摘要对齐 → 经**生产 go-plugin 传输**作为子进程启动（ProtocolStarter/Manager）→
//	宿主 HTTP 侧解析会话范围 → 每次 invokesign mint 一条单次调用会话委派 →
//	插件（真实二进制）回带委派调用 Host 的 PluginSessionService（真 gRPC）→
//	服务从宿主 mint 台账取会话指纹 → 真 store 落库 → 调用结算后委派被收回，
//	用插件回带的同一条令牌在真实服务上重放被拒。
//
// 唯一替身是 Redis 后端与运行时准入（HostAPI 与 PluginSessionState 的 opt-in 测试
// 覆盖真 Redis；本测试覆盖的是权威、装配与传输）。这也是 M9 最后一项（会话状态
// 工作流参考夹具）与 T3（真插件子进程版联合测试）的合并交付。
const (
	joinedSessionInstallationID = "installation-joined-test"
	joinedSessionExtensionID    = "sforum.session-checkout-reference"
	// joinedSessionFixtureDir 是夹具的物理目录名（连字符），与扩展 id（点号）解耦。
	joinedSessionFixtureDir = "sforum-session-checkout-reference"

	joinedSessionStateRouteID         = joinedSessionExtensionID + ".route.state"
	joinedSessionStateContract        = joinedSessionStateRouteID + "@1"
	joinedSessionAdvanceRouteID       = joinedSessionExtensionID + ".route.advance"
	joinedSessionAdvanceContract      = joinedSessionAdvanceRouteID + "@1"
	joinedSessionStateSchemaRef       = joinedSessionExtensionID + ".checkout-state@1"
	joinedSessionAdvanceSchemaRef     = joinedSessionExtensionID + ".advance.request@1"
	joinedSessionCheckoutNamespaceKey = "checkout/step"
)

type joinedSessionBackend struct {
	mu     sync.Mutex
	values map[string][]byte
	sets   map[string]map[string]bool
}

func newJoinedSessionBackend() *joinedSessionBackend {
	return &joinedSessionBackend{values: map[string][]byte{}, sets: map[string]map[string]bool{}}
}

func (b *joinedSessionBackend) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.values[key]
	return append([]byte(nil), value...), ok, nil
}

func (b *joinedSessionBackend) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[key] = append([]byte(nil), value...)
	return nil
}

func (b *joinedSessionBackend) Delete(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.values[key]
	delete(b.values, key)
	delete(b.sets, key)
	return ok, nil
}

func (b *joinedSessionBackend) SetAdd(_ context.Context, key string, member string, _ time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sets[key] == nil {
		b.sets[key] = map[string]bool{}
	}
	added := !b.sets[key][member]
	b.sets[key][member] = true
	return added, nil
}

func (b *joinedSessionBackend) SetMembers(_ context.Context, key string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	members := make([]string, 0, len(b.sets[key]))
	for member := range b.sets[key] {
		members = append(members, member)
	}
	return members, nil
}

func (b *joinedSessionBackend) SetRemove(_ context.Context, key string, members ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, member := range members {
		delete(b.sets[key], member)
	}
	return nil
}

// joinedSessionAdmission 与生产同形：租约包住真实网络往返。
type joinedSessionAdmission struct{}

func (joinedSessionAdmission) AcquireServiceProvider(
	ctx context.Context,
	_ hostapi.ServiceProviderIdentity,
) (hostapi.ServiceProviderAdmissionLease, error) {
	return joinedSessionLease{ctx: ctx}, nil
}

type joinedSessionLease struct{ ctx context.Context }

func (l joinedSessionLease) Context() context.Context { return l.ctx }
func (l joinedSessionLease) Release()                 {}

type joinedSessionTrust struct {
	identity extensions.RuntimeTrustIdentity
}

func (s joinedSessionTrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return s.identity, nil
}

// stageSessionCheckoutFixture 复制夹具、现场构建 Protocol V2 后端，并把 manifest
// 的 backend/executable 摘要对齐到新二进制（夹具在源码树里始终可被
// `extension validate/test` 直接校验）。
func stageSessionCheckoutFixture(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	// 本文件位于 apps/api/app/Support/Extensions，需上溯 5 层到仓库根。
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	fixtureRoot := filepath.Join(repositoryRoot, "extensions/fixtures/plugins", joinedSessionFixtureDir)
	packageRoot := filepath.Join(t.TempDir(), joinedSessionExtensionID, "1.0.0")
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy %s: %v", fixtureRoot, err)
	}
	// 临时副本里不携带构建产物，避免把宿主架构的二进制带进断言。
	_ = os.Remove(filepath.Join(packageRoot, "backend", "plugin"))

	goModPath := filepath.Join(packageRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repositoryRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(packageRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build session checkout backend: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	digest := hex.EncodeToString(sum[:])

	manifestPath := filepath.Join(packageRoot, extensionmanifest.ManifestFileName)
	var manifest map[string]any
	if err := json.Unmarshal(mustReadSessionCheckoutFile(t, manifestPath), &manifest); err != nil {
		t.Fatal(err)
	}
	backend, _ := manifest["backend"].(map[string]any)
	backend["digest"] = digest
	for _, raw := range manifest["packageFiles"].([]any) {
		file, _ := raw.(map[string]any)
		if file["path"] == "backend/plugin" {
			file["digest"] = digest
		}
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return packageRoot
}

func mustReadSessionCheckoutFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPluginSessionStateJoinedHostDispatchToStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping protocol v2 subprocess build in short mode")
	}
	packageRoot := stageSessionCheckoutFixture(t)
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load staged session checkout package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	extension := extensions.Extension{
		ID: manifest.ID, Name: manifest.ID, Version: manifest.Version,
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Source: extensions.SourceUploaded, PackageDigest: packageDigest, PackagePath: packageRoot,
		CapabilityGrants: []extensions.CapabilityGrant{
			{Key: capabilities.HostAPI, Risk: capabilities.RiskLow},
		},
		Manifest: manifest,
	}

	// 宿主侧：真签发权威 + 真 store（内存后端替身）+ 真服务，绑定到生产 Gateway。
	// Gateway 实现了 S9 的 issuer 能力，这与生产装配（bootstrap/app.go 把 Gateway
	// 交给 ProtocolStarter）完全同形——mint 就在宿主的启动路径上完成。
	backend := newJoinedSessionBackend()
	store, err := pluginsessionstate.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := hostapi.NewProtocolV2SessionDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	service, err := hostapi.NewProtocolV2SessionServiceServer(
		store, authority, joinedSessionInstallationID,
		hostapi.WithProtocolV2SessionAdmission(joinedSessionAdmission{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := hostapi.NewGateway(hostapi.New(hostapi.Config{}))
	if err := gateway.BindProtocolV2SessionService(service); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust: joinedSessionTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "joined-trust", ImpactDigest: "joined-impact",
		}},
		HostAPI: gateway,
	})
	manager := NewManager(ManagerConfig{Starter: starter})
	startCtx, cancelStart := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelStart()
	if err := manager.Start(startCtx, extension); err != nil {
		t.Fatalf("start session checkout plugin: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	instanceIdentity := RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: active.Identity.InstanceID,
	}

	scope := authsession.RequestScope{
		SessionFingerprint: strings.Repeat("a", 64),
		IdleExpiresAt:      time.Now().UTC().Add(time.Hour),
		AbsoluteExpiresAt:  time.Now().UTC().Add(24 * time.Hour),
		ActorUserID:        42,
	}
	invoke := func(ctx context.Context, request ProtocolV2RouteRequest) (ProtocolV2RouteResponse, error) {
		_, lease, err := manager.AcquireActiveRuntimeCall(ctx, extension.ID, RuntimeCallRoute)
		if err != nil {
			t.Fatalf("acquire route call: %v", err)
		}
		defer lease.Release()
		return manager.InvokeRouteInstance(ctx, instanceIdentity, request)
	}
	scopedCtx := func() context.Context {
		return authsession.WithPluginSessionScope(context.Background(), scope)
	}

	// 第 1 次调用：state = 未命中 → 工作流从第 1 步开始，且插件确认收到了委派。
	first, err := invoke(scopedCtx(), ProtocolV2RouteRequest{
		RouteID: joinedSessionStateRouteID, ContractVersion: joinedSessionStateContract,
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet,
		Path:           "/session-checkout/state",
		ResponseSchema: joinedSessionStateSchemaRef,
		Authority:      protocolV2FilteredHostRequestAuthority(), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Body["step"].(float64) != 1 || first.Body["sessionBound"] != true {
		t.Fatalf("first state document = %#v (status=%d)", first.Body, first.StatusCode)
	}

	// 第 2 次调用：advance 写第 2 步；插件把委派回带（真实 gRPC 到已注册服务）。
	second, err := invoke(scopedCtx(), ProtocolV2RouteRequest{
		RouteID: joinedSessionAdvanceRouteID, ContractVersion: joinedSessionAdvanceContract,
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodPost,
		Path:           "/session-checkout/advance",
		RequestSchema:  joinedSessionAdvanceSchemaRef,
		ResponseSchema: joinedSessionStateSchemaRef,
		Body:           map[string]any{"action": "next"}, BodyPresent: true,
		Authority: protocolV2FilteredHostRequestAuthority(), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Body["step"].(float64) != 2 {
		t.Fatalf("advance document = %#v (status=%d)", second.Body, second.StatusCode)
	}
	echo, _ := second.Body["delegationEcho"].(string)
	if echo == "" {
		t.Fatalf("the plugin never received a delegation: %#v", second.Body)
	}

	// 第 3 次调用（新委派、同会话）：跨请求持久化——前一次写入的字节还在。
	third, err := invoke(scopedCtx(), ProtocolV2RouteRequest{
		RouteID: joinedSessionStateRouteID, ContractVersion: joinedSessionStateContract,
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet,
		Path:           "/session-checkout/state",
		ResponseSchema: joinedSessionStateSchemaRef,
		Authority:      protocolV2FilteredHostRequestAuthority(), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Body["step"].(float64) != 2 || third.Body["persisted"] != true {
		t.Fatalf("third state document = %#v (status=%d)", third.Body, third.StatusCode)
	}

	// 状态确实落在该会话的命名空间下，且只有它（与 stub 版相同的三方断言，但现在
	// 经真实插件二进制与真服务完成）。
	scoped := pluginsessionstate.Scope{
		InstallationID: joinedSessionInstallationID, ExtensionID: joinedSessionExtensionID,
		SessionFingerprint:   scope.SessionFingerprint,
		SessionIDleExpiresAt: scope.IdleExpiresAt, SessionAbsoluteExpiresAt: scope.AbsoluteExpiresAt,
	}
	stored, found, err := store.Get(context.Background(), scoped, joinedSessionCheckoutNamespaceKey)
	if err != nil || !found || !strings.Contains(string(stored), `"step":2`) {
		t.Fatalf("store lookup = %q/%t/%v", stored, found, err)
	}
	otherSession := scoped
	otherSession.SessionFingerprint = strings.Repeat("b", 64)
	if _, found, err := store.Get(context.Background(), otherSession, joinedSessionCheckoutNamespaceKey); err != nil || found {
		t.Fatalf("another session saw the state: found=%t err=%v", found, err)
	}

	// 单次调用语义：调用结算后，插件回带的那条委派在真实服务上重放必须被拒。
	// 运行时身份从令牌声明重建（令牌只承载运行时绑定，不含会话指纹——S8）。
	replayIdentity := joinedReplayIdentity(t, echo)
	replayed, err := service.Get(
		hostapi.ContextWithProtocolV2RuntimeIdentity(context.Background(), replayIdentity),
		&hostv2.PluginSessionGetRequest{Context: joinedReplayRequestContext(replayIdentity, echo)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GetError().GetReason() != "host.plugin_session_scope_unattested" {
		t.Fatalf("a retired delegation was accepted: %#v", replayed)
	}
	if replayed.GetFound() {
		t.Fatal("a retired delegation read state")
	}

	// 擦除语义：宿主的擦除钩子失效该会话命名空间之后，同一工作流回到第 1 步——
	// 「登出即消失」不是插件断言出来的，而是宿主真 store 上的真状态。
	if _, err := store.InvalidateSession(context.Background(), joinedSessionInstallationID, scope.SessionFingerprint); err != nil {
		t.Fatal(err)
	}
	afterErase, err := invoke(scopedCtx(), ProtocolV2RouteRequest{
		RouteID: joinedSessionStateRouteID, ContractVersion: joinedSessionStateContract,
		RouteAction:     extensionmanifest.RouteActionAdd,
		InvocationStage: ProtocolV2RouteInvocationStageHandler, Method: http.MethodGet,
		Path:           "/session-checkout/state",
		ResponseSchema: joinedSessionStateSchemaRef,
		Authority:      protocolV2FilteredHostRequestAuthority(), Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterErase.Body["step"].(float64) != 1 || afterErase.Body["sessionBound"] != true {
		t.Fatalf("state after erasure = %#v (status=%d)", afterErase.Body, afterErase.StatusCode)
	}
}

// joinedReplayIdentity 从插件回带的令牌声明里重建运行时绑定（仅用于本测试的
// 重放断言；产品路径上会议由服务拦截器发布，而不是令牌解析）。
func joinedReplayIdentity(t *testing.T, token string) *protocolwire.ExtensionIdentity {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected delegation token shape: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		ExtensionID      string `json:"extension_id"`
		ExtensionVersion string `json:"extension_version"`
		ArtifactDigest   string `json:"artifact_digest"`
		TrustGrantID     string `json:"trust_grant_id"`
		RuntimeEpoch     uint64 `json:"runtime_epoch"`
		InstanceID       string `json:"instance_id"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return &protocolwire.ExtensionIdentity{
		ExtensionId: claims.ExtensionID, ExtensionVersion: claims.ExtensionVersion,
		ArtifactDigest: claims.ArtifactDigest, TrustGrantId: claims.TrustGrantID,
		RuntimeEpoch: claims.RuntimeEpoch, InstanceId: claims.InstanceID,
	}
}

func joinedReplayRequestContext(identity *protocolwire.ExtensionIdentity, token string) *protocolwire.RequestContext {
	return &protocolwire.RequestContext{
		RequestId: "joined-replay", Extension: identity,
		Deadline:              timestamppb.New(time.Now().Add(time.Minute)),
		HostSessionDelegation: &protocolwire.HostSessionDelegation{Token: token},
	}
}
