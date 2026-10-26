package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// T1C：原子 CAS 激活、有效可用性、catalog fail-closed、probe 不得 ok=true。

func t1cDigest(seed string) string {
	if len(seed) >= 64 {
		return seed[:64]
	}
	return strings.Repeat(seed, 64)[:64]
}

func t1cLive(providerID, digest string, ops ...string) identityregistry.ProviderContribution {
	operations := make([]identityregistry.ProviderOperation, 0, len(ops))
	for _, name := range ops {
		operations = append(operations, identityregistry.ProviderOperation{Name: name})
	}
	if len(operations) == 0 {
		operations = []identityregistry.ProviderOperation{{Name: AuthOperationLoginStart}}
	}
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: providerID, ContractVersion: providerID + "@1",
			Kind: identityregistry.ProviderKindAuth, Operations: operations,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext." + providerID, ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 7, RuntimeInstanceID: "rt-" + providerID,
		},
	}
}

func t1cSvc(live identityregistry.ProviderContribution, store ProviderActivationStore, safe bool) *ExternalAuthService {
	return NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		SafeMode:        func() bool { return safe },
		ProviderContribution: func(id string) (identityregistry.ProviderContribution, error) {
			if id != live.ID {
				return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
			}
			return live, nil
		},
	})
}

// TestT1C_MemoryStore_CASAllowedStaleAndNoMutation 覆盖 allowed / stale / no-mutation。
func TestT1C_MemoryStore_CASAllowedStaleAndNoMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryProviderActivationStore()
	digest := t1cDigest("a")
	login := true

	// allowed create
	created, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
		ExpectedRevision: 0, LoginEnabled: &login,
	})
	if err != nil || !created.LoginEnabled || created.Revision != 1 {
		t.Fatalf("create: act=%+v err=%v", created, err)
	}

	// stale revision → no mutation
	before, _ := store.Get(ctx, "p1")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
		ExpectedRevision: 99, LoginEnabled: &login,
	}); !errors.Is(err, ErrProviderActivationCASConflict) {
		t.Fatalf("stale want CAS conflict, got %v", err)
	}
	afterStale, _ := store.Get(ctx, "p1")
	if afterStale.Revision != before.Revision || afterStale.LoginEnabled != before.LoginEnabled {
		t.Fatalf("stale mutated state: before=%+v after=%+v", before, afterStale)
	}

	// no-mutation：相同状态不递增 revision
	same, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
		ExpectedRevision: 1, LoginEnabled: &login,
	})
	if !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatalf("no-mutation want ErrProviderActivationNoMutation, got %v", err)
	}
	if same.Revision != 1 {
		t.Fatalf("no-mutation bumped revision to %d", same.Revision)
	}

	// allowed update
	reg := true
	updated, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
		ExpectedRevision: 1, RegistrationEnabled: &reg,
	})
	if err != nil || !updated.RegistrationEnabled || updated.Revision != 2 {
		t.Fatalf("update: %+v err=%v", updated, err)
	}

	// reset allowed then no-mutation
	reset, err := store.ResetOperationsToDefaults(ctx, "p1")
	if err != nil || reset.LoginEnabled || reset.RegistrationEnabled || reset.Revision != 3 {
		t.Fatalf("reset: %+v err=%v", reset, err)
	}
	if _, err := store.ResetOperationsToDefaults(ctx, "p1"); !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatalf("reset no-mutation: %v", err)
	}
}

// TestT1C_MemoryStore_ConcurrentCAS 并发乐观更新：恰好一个成功。
func TestT1C_MemoryStore_ConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryProviderActivationStore()
	digest := t1cDigest("c")
	login := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
		ExpectedRevision: 0, LoginEnabled: &login,
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, conflict := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// 初始行 priority=0；从 1 起步保证每个并发写入都是真实变更，
		// 避免 n=0 抢先命中 no-mutation 语义导致计数不确定。
		prio := n + 1
			_, err := store.Upsert(ctx, ProviderActivationInput{
				ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
				ExpectedRevision: 1, Priority: &prio,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if errors.Is(err, ErrProviderActivationCASConflict) {
				conflict++
			} else {
				t.Errorf("unexpected err: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if success != 1 || conflict != 15 {
		t.Fatalf("concurrent CAS success=%d conflict=%d", success, conflict)
	}
	got, _ := store.Get(ctx, "p1")
	if got.Revision != 2 {
		t.Fatalf("revision after concurrent CAS = %d", got.Revision)
	}
}

// TestT1C_ProbeNeverPersistsOKTrue pending/unavailable 不得写 ok=true。
func TestT1C_ProbeNeverPersistsOKTrue(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryProviderActivationStore()
	digest := t1cDigest("p")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "p1", OwnerExtensionID: "ext.p1", OwnerPackageDigest: digest,
	}); err != nil {
		t.Fatal(err)
	}
	// 即使调用方误传 OK=true + pending reason，store 也强制 false。
	if err := store.RecordProbe(ctx, ProviderActivationProbeResult{
		ProviderID: "p1", OK: true, Reason: ProbeReasonPending, At: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastProbeOK == nil || *got.LastProbeOK {
		t.Fatalf("probe_pending must not persist ok=true: %+v", got.LastProbeOK)
	}
	if got.LastProbeReason != ProbeReasonPending {
		t.Fatalf("reason=%s", got.LastProbeReason)
	}
}

// TestT1C_EffectiveAvailability_ArtifactSafeModeAndOps 覆盖 artifact / Safe Mode / 不支持操作。
func TestT1C_EffectiveAvailability_ArtifactSafeModeAndOps(t *testing.T) {
	ctx := context.Background()
	digest := t1cDigest("x")
	live := t1cLive("demo.auth", digest, AuthOperationLoginStart)
	store := NewMemoryProviderActivationStore()
	login := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, OwnerExtensionVerID: live.Artifact.VersionID,
		LoginEnabled: &login,
	}); err != nil {
		t.Fatal(err)
	}

	// happy path
	svc := t1cSvc(live, store, false)
	ok, err := svc.IsEffectivelyAvailable(ctx, "demo.auth", ExternalAuthOperationLogin)
	if err != nil || !ok {
		t.Fatalf("expected available: ok=%v err=%v", ok, err)
	}

	// Safe Mode
	svcSafe := t1cSvc(live, store, true)
	ok, err = svcSafe.IsEffectivelyAvailable(ctx, "demo.auth", ExternalAuthOperationLogin)
	if err != nil || ok {
		t.Fatalf("safe mode must remove availability: ok=%v err=%v", ok, err)
	}

	// artifact 漂移：live digest 变了，激活行仍绑旧 digest
	liveNew := t1cLive("demo.auth", t1cDigest("y"), AuthOperationLoginStart)
	svcDrift := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return liveNew, nil
		},
	})
	ok, err = svcDrift.IsEffectivelyAvailable(ctx, "demo.auth", ExternalAuthOperationLogin)
	if err != nil || ok {
		t.Fatalf("artifact drift must remove availability: ok=%v err=%v", ok, err)
	}

	// 不支持的 operation（live 只有 login）
	ok, err = svc.IsEffectivelyAvailable(ctx, "demo.auth", ExternalAuthOperationRegistration)
	if err != nil || ok {
		t.Fatalf("unsupported op must be unavailable: ok=%v err=%v", ok, err)
	}

	// PrepareActivationInput 拒绝启用不支持操作
	reg := true
	if _, err := svc.PrepareActivationInput("demo.auth", nil, &reg, nil, nil, 1); !errors.Is(err, ErrProviderActivationUnsupportedOperation) {
		t.Fatalf("prepare unsupported: %v", err)
	}
}

// TestT1C_PublicCatalog_FailClosedAndFilters 公开 catalog 过滤 + Host 状态失败 fail closed。
func TestT1C_PublicCatalog_FailClosedAndFilters(t *testing.T) {
	ctx := context.Background()
	registry := identityregistry.New()

	// Host 激活目录 List 失败 → fail closed。
	failStore := &failingActivationListStore{}
	svc := NewExternalAuthService(ExternalAuthDeps{ActivationStore: failStore})
	if _, err := svc.ListEffectivePublicCatalog(ctx, registry, "zh-CN"); err == nil {
		t.Fatal("activation list failure must fail closed")
	}

	// 空 registry + 有激活行：无 live provider → 空 catalog（不静默暴露）。
	digest := t1cDigest("f")
	store := NewMemoryProviderActivationStore()
	login := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: "ext.demo.auth",
		OwnerPackageDigest: digest, LoginEnabled: &login,
	}); err != nil {
		t.Fatal(err)
	}
	svcOK := NewExternalAuthService(ExternalAuthDeps{ActivationStore: store})
	entries, err := svcOK.ListEffectivePublicCatalog(ctx, registry, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no live registry providers must yield empty catalog: %#v", entries)
	}

	// Safe Mode → 空 catalog（即使有激活）。
	svcSafe := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		SafeMode:        func() bool { return true },
	})
	entries, err = svcSafe.ListEffectivePublicCatalog(ctx, registry, "zh-CN")
	if err != nil || len(entries) != 0 {
		t.Fatalf("safe mode catalog must be empty: %#v err=%v", entries, err)
	}

	// 未激活 flag：即便将来 registry 有 provider，activation flag off 不暴露。
	// 用 Evaluate 证明（catalog 组装逻辑同源）。
	live := t1cLive("demo.auth", digest, AuthOperationLoginStart)
	offStore := NewMemoryProviderActivationStore()
	if _, err := offStore.Upsert(ctx, ProviderActivationInput{
		ProviderID: "demo.auth", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, OwnerExtensionVerID: live.Artifact.VersionID,
		// login 默认 false
	}); err != nil {
		t.Fatal(err)
	}
	svcOff := t1cSvc(live, offStore, false)
	ok, err := svcOff.IsEffectivelyAvailable(ctx, "demo.auth", ExternalAuthOperationLogin)
	if err != nil || ok {
		t.Fatalf("inactive must not be available: ok=%v err=%v", ok, err)
	}
}

// TestT1C_PrepareActivationInput_HostDerivedOwnership 所有权仅 Host 派生。
func TestT1C_PrepareActivationInput_HostDerivedOwnership(t *testing.T) {
	digest := t1cDigest("h")
	live := t1cLive("demo.auth", digest, AuthOperationLoginStart)
	svc := t1cSvc(live, NewMemoryProviderActivationStore(), false)
	login := true
	input, err := svc.PrepareActivationInput("demo.auth", &login, nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if input.OwnerExtensionID != live.Artifact.ExtensionID ||
		input.OwnerPackageDigest != live.Artifact.PackageDigest ||
		input.OwnerExtensionVerID != live.Artifact.VersionID {
		t.Fatalf("host-derived ownership mismatch: %+v", input)
	}
	if input.ProviderID != "demo.auth" || input.LoginEnabled == nil || !*input.LoginEnabled {
		t.Fatalf("input flags: %+v", input)
	}
}

type failingActivationListStore struct {
	MemoryProviderActivationStore
}

func (s *failingActivationListStore) List(context.Context) ([]ProviderActivation, error) {
	return nil, errors.New("activation store unavailable")
}

func (s *failingActivationListStore) Get(context.Context, string) (ProviderActivation, error) {
	return ProviderActivation{}, ErrProviderActivationNotFound
}

func (s *failingActivationListStore) Upsert(context.Context, ProviderActivationInput) (ProviderActivation, error) {
	return ProviderActivation{}, errors.New("unused")
}

func (s *failingActivationListStore) RecordProbe(context.Context, ProviderActivationProbeResult) error {
	return nil
}

func (s *failingActivationListStore) Delete(context.Context, string) error { return nil }

func (s *failingActivationListStore) ResetOperationsToDefaults(context.Context, string) (ProviderActivation, error) {
	return ProviderActivation{}, ErrProviderActivationNotFound
}

var _ ProviderActivationStore = (*failingActivationListStore)(nil)
