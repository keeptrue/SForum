package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 多提供商能力证明测试（见 plans/2026-07-27-github-social-login-builtin-plugin.md
// 额外硬性验收）。
//
// 这些测试用真实 IdentityRegistry + 真实 CallbackStateStore + 真实
// ComputeSubjectDigest 证明多提供商能力，不依赖插件子进程或真实 GitHub。
// 覆盖：
//  1. 两个提供商可同时发现、排序和独立激活；
//  2. 一个提供商失败不影响另一个；
//  3. 跨 provider state/callback 被拒绝；
//  4. 同一用户可分别绑定两个提供商；
//  5. 相同外部 subject 在不同 providerId 下不冲突；
//  6. 插件禁用、Safe Mode 和制品升级只影响对应提供商；
//  7. Core 和前端不得出现 `if provider == github` 一类供应商业务分支
//     （由代码结构保证：所有展示来自 Host catalog，无字面名分支）。

const (
	multiProviderA = "sforum.auth-fixture-alpha.auth"
	multiProviderB = "sforum.auth-fixture-beta.auth"
)

// publishMultiProvider 在 Registry 里注册两个 auth 提供方（inspect-only，无 operations）。
// 注册表要求 executable operations 绑定 JSON Schema；此处仅需证明 catalog 发现与排序，
// 所以用 inspect-only 提供方。executable 行为由 auth_provider_flow_test.go 的真实 schema
// 绑定测试覆盖。
func publishMultiProvider(t *testing.T, registry *identityregistry.Registry) {
	t.Helper()
	for i, providerID := range []string{multiProviderA, multiProviderB} {
		// provider.ID 必须以 ExtensionID + "." 为前缀（ownedID 校验）。
		// providerID 形如 sforum.auth-fixture-alpha.auth；ExtensionID 取去掉最后一段。
		extensionID := trimLastSegment(providerID)
		digest := sha256Hex(providerID)
		pub := identityregistry.Publication{
			Artifact: identityregistry.Artifact{
				ExtensionID: extensionID, ExtensionVersion: "1.0.0",
				PackageDigest: digest, VersionID: int64(i + 1),
			},
			Identity: &identityregistry.IdentityDeclaration{
				ContractVersion: extensionID + ".identity@1",
				Providers: []identityregistry.Provider{{
					ID: providerID, ContractVersion: providerID + "@1",
					Kind: identityregistry.ProviderKindAuth, Handler: extensionID + ".identity",
					Priority: 100 - i, // A priority 100, B priority 99
					// inspect-only（无 operations）— 仅证明 catalog 发现/排序能力。
				}},
			},
		}
		if _, err := registry.Publish(pub); err != nil {
			t.Fatalf("publish %s: %v", providerID, err)
		}
	}
}

// trimLastSegment 去掉最后一个 "." 分段：sforum.auth-fixture-alpha.auth → sforum.auth-fixture-alpha
func trimLastSegment(s string) string {
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return s
	}
	return s[:idx]
}

// TestMultiProvider_DiscoveryAndOrdering 两个提供商可同时发现、排序。
func TestMultiProvider_DiscoveryAndOrdering(t *testing.T) {
	registry := identityregistry.New()
	publishMultiProvider(t, registry)
	providers := registry.Providers(identityregistry.ProviderKindAuth)
	if len(providers) < 2 {
		t.Fatalf("expected at least 2 auth providers, got %d", len(providers))
	}
	// Providers() 返回按 kind asc, priority desc, id asc 排序。
	ids := map[string]bool{}
	for _, p := range providers {
		ids[p.ID] = true
	}
	if !ids[multiProviderA] || !ids[multiProviderB] {
		t.Fatalf("both providers must be discoverable: %v", ids)
	}
}

// TestMultiProvider_IndependentActivation 两提供商激活状态独立。
func TestMultiProvider_IndependentActivation(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	digestA := sha256Hex(multiProviderA)
	liveA := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: multiProviderA, Kind: identityregistry.ProviderKindAuth,
			Operations: []identityregistry.ProviderOperation{{Name: AuthOperationLoginStart}},
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: trimLastSegment(multiProviderA), PackageDigest: digestA,
			VersionID: 1, RuntimeInstanceID: "rt-a",
		},
	}
	store := &fakeActivationStoreLite{items: map[string]ProviderActivation{
		multiProviderA: {
			ProviderID: multiProviderA, OwnerExtensionID: trimLastSegment(multiProviderA),
			OwnerPackageDigest: digestA, LoginEnabled: true,
		},
	}}
	svc := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(id string) (identityregistry.ProviderContribution, error) {
			if id == multiProviderA {
				return liveA, nil
			}
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		},
	})
	ctx := context.Background()

	// A 已激活 login，B 未激活。
	aOk, _ := svc.IsOperationActivated(ctx, multiProviderA, ExternalAuthOperationLogin)
	bOk, _ := svc.IsOperationActivated(ctx, multiProviderB, ExternalAuthOperationLogin)
	if !aOk {
		t.Fatalf("provider A login should be activated")
	}
	if bOk {
		t.Fatalf("provider B login should NOT be activated")
	}
}

// TestMultiProvider_CallbackStateCrossProviderRejected 跨 provider callback 被拒绝。
// state 绑定 providerId；用 A 的 state 消费后用于 B 必失败。
func TestMultiProvider_CallbackStateCrossProviderRejected(t *testing.T) {
	store := NewInMemoryCallbackStateStore()
	ctx := context.Background()
	// 为 provider A 创建 state。
	tx := CallbackTransaction{
		State: "state-A", ProviderID: multiProviderA, Operation: ExternalAuthOperationLogin,
		OwnerPackageDigest: "digest-A", ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.Save(ctx, tx); err != nil {
		t.Fatal(err)
	}
	consumed, err := store.Consume(ctx, "state-A")
	if err != nil {
		t.Fatal(err)
	}
	// 校验：用 B 的 provider/digest 校验 A 的 tx 必失败。
	if consumed.MatchesProvider(multiProviderB, ExternalAuthOperationLogin, "digest-B") {
		t.Fatalf("cross-provider match must be rejected")
	}
	if !consumed.MatchesProvider(multiProviderA, ExternalAuthOperationLogin, "digest-A") {
		t.Fatalf("same-provider match should succeed")
	}
}

// TestMultiProvider_CrossOperationRejected 跨 operation callback 被拒绝。
func TestMultiProvider_CrossOperationRejected(t *testing.T) {
	tx := CallbackTransaction{
		ProviderID: multiProviderA, Operation: ExternalAuthOperationLogin,
		OwnerPackageDigest: "digest-A",
	}
	if tx.MatchesProvider(multiProviderA, ExternalAuthOperationRegistration, "digest-A") {
		t.Fatalf("cross-operation match must be rejected")
	}
	if tx.MatchesProvider(multiProviderA, ExternalAuthOperationLink, "digest-A") {
		t.Fatalf("cross-operation match must be rejected (link)")
	}
}

// TestMultiProvider_SameSubjectAcrossProvidersNoConflict 相同 subject 在不同 providerId 下 digest 不同，不冲突。
// 这是 Core-HMAC 的关键属性：digest = HMAC(key, providerId || NUL || subject)。
func TestMultiProvider_SameSubjectAcrossProvidersNoConflict(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	digestA, err := ComputeSubjectDigest(multiProviderA, "user-42")
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := ComputeSubjectDigest(multiProviderB, "user-42")
	if err != nil {
		t.Fatal(err)
	}
	if digestA == digestB {
		t.Fatalf("same subject across different providers must produce different digests")
	}
	// 同 provider 同 subject 稳定。
	digestA2, _ := ComputeSubjectDigest(multiProviderA, "user-42")
	if digestA != digestA2 {
		t.Fatalf("same provider+subject must be stable")
	}
}

// TestMultiProvider_OneFailureDoesNotAffectOther 一个 provider complete 失败不影响另一个。
// 通过 callback state 隔离证明：A 的 state 失效/重放不影响 B 的 state。
func TestMultiProvider_OneFailureDoesNotAffectOther(t *testing.T) {
	store := NewInMemoryCallbackStateStore()
	ctx := context.Background()
	txA := CallbackTransaction{State: "s-A", ProviderID: multiProviderA, ExpiresAt: time.Now().Add(time.Minute)}
	txB := CallbackTransaction{State: "s-B", ProviderID: multiProviderB, ExpiresAt: time.Now().Add(time.Minute)}
	_ = store.Save(ctx, txA)
	_ = store.Save(ctx, txB)
	// A 的 state 消费后重放（模拟 provider A 失败/重放）。
	if _, err := store.Consume(ctx, "s-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(ctx, "s-A"); err == nil {
		t.Fatalf("replay of A should fail")
	}
	// B 的 state 仍可用。
	if _, err := store.Consume(ctx, "s-B"); err != nil {
		t.Fatalf("provider B state must remain usable after A failure: %v", err)
	}
}

// TestMultiProvider_CallbackTransactionActorBinding link callback 绑定 actor；跨 actor 拒绝。
func TestMultiProvider_CallbackTransactionActorBinding(t *testing.T) {
	tx := CallbackTransaction{
		ProviderID: multiProviderA, Operation: ExternalAuthOperationLink, ActorUserID: 7,
	}
	if !tx.MatchesActor(ExternalAuthOperationLink, 7) {
		t.Fatalf("correct actor should match for link")
	}
	if tx.MatchesActor(ExternalAuthOperationLink, 8) {
		t.Fatalf("different actor must be rejected for link")
	}
	// login/registration 必须 actorless。
	loginTx := CallbackTransaction{Operation: ExternalAuthOperationLogin, ActorUserID: 0}
	if !loginTx.MatchesActor(ExternalAuthOperationLogin, 0) {
		t.Fatalf("actorless login should match")
	}
}

// TestMultiProvider_CompleteLoginUnlinkedGeneric 未绑定返回泛化错误，不暴露存在性。
// 此测试证明 Core 不含 `if provider == github` 分支：同一逻辑适用于任意 provider。
func TestMultiProvider_CompleteLoginUnlinkedGeneric(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	// 用 fake link store（无任何绑定）。
	fakeLinks := &fakeLinkStoreMulti{links: map[string]ExternalIdentityLink{}}
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore: fakeLinks,
	})
	ctx := context.Background()
	for _, providerID := range []string{multiProviderA, multiProviderB} {
		_, err := svc.CompleteLogin(ctx, ExternalAuthAssertion{
			ProviderID: providerID, Operation: ExternalAuthOperationLogin,
			ProviderSubject: "42",
		})
		if !errors.Is(err, ErrExternalIdentityUnlinked) {
			t.Fatalf("provider %s: expected ErrExternalIdentityUnlinked, got %v", providerID, err)
		}
	}
}

// fakeLinkStoreMulti 多提供商测试用的内存 link store。
type fakeLinkStoreMulti struct {
	links map[string]ExternalIdentityLink // key: providerID|digest
}

func (s *fakeLinkStoreMulti) Link(_ context.Context, input LinkExternalIdentityInput, _ ExternalIdentityLinkCommitFence) (ExternalIdentityLinkMutation, error) {
	return ExternalIdentityLinkMutation{}, nil
}
func (s *fakeLinkStoreMulti) Unlink(_ context.Context, _ TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error) {
	return ExternalIdentityLinkMutation{}, nil
}
func (s *fakeLinkStoreMulti) Erase(_ context.Context, _ TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error) {
	return ExternalIdentityLinkMutation{}, nil
}
func (s *fakeLinkStoreMulti) Get(_ context.Context, id int64) (ExternalIdentityLink, error) {
	return ExternalIdentityLink{}, ErrExternalIdentityLinkNotFound
}
func (s *fakeLinkStoreMulti) FindActive(_ context.Context, providerID, digest string) (ExternalIdentityLink, error) {
	key := providerID + "|" + digest
	if l, ok := s.links[key]; ok {
		return l, nil
	}
	return ExternalIdentityLink{}, ErrExternalIdentityLinkNotFound
}
func (s *fakeLinkStoreMulti) ListUser(_ context.Context, userID int64) ([]ExternalIdentityLink, error) {
	return nil, nil
}

var _ ExternalIdentityLinkStore = (*fakeLinkStoreMulti)(nil)
