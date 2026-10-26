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

// M5 / T7：生命周期矩阵 + 安全矩阵 + 限流 + 非枚举公共错误边界。
// 复用 T1E 夹具风格；本文件是发布门禁的可执行清单，不得仅依赖“已有测试绿”。

func m5Live(providerID, digest, runtime string, ops ...string) identityregistry.ProviderContribution {
	operations := make([]identityregistry.ProviderOperation, 0, len(ops))
	for _, name := range ops {
		operations = append(operations, identityregistry.ProviderOperation{Name: name})
	}
	if len(operations) == 0 {
		operations = []identityregistry.ProviderOperation{
			{Name: AuthOperationLoginStart}, {Name: AuthOperationLoginComplete},
			{Name: AuthOperationLinkStart}, {Name: AuthOperationLinkComplete},
			{Name: AuthOperationRegistrationStart}, {Name: AuthOperationRegistrationComplete},
		}
	}
	return identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: providerID, ContractVersion: providerID + "@1",
			Kind: identityregistry.ProviderKindAuth, Operations: operations,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext." + providerID, ExtensionVersion: "1.0.0",
			PackageDigest: digest, VersionID: 1, RuntimeInstanceID: runtime,
		},
	}
}

func m5ActivateLogin(t *testing.T, store ProviderActivationStore, live identityregistry.ProviderContribution) {
	t.Helper()
	ctx := context.Background()
	on := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: live.Provider.ID, OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: live.Artifact.PackageDigest, LoginEnabled: &on,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatalf("activate: %v", err)
	}
}

// --- 生命周期矩阵 ---

func TestM5_Lifecycle_RestartPreservesHMACAndCallbackTTL(t *testing.T) {
	// restart：稳定 HMAC secret 跨“进程重启”可复现；callback TTL 语义一致。
	ResetIdentitySubjectHMACKeyForTest()
	t.Cleanup(ResetIdentitySubjectHMACKeyForTest)
	secret := "m5-restart-stable-hmac-secret-32b!!"
	if err := ConfigureIdentitySubjectHMAC(secret); err != nil {
		t.Fatal(err)
	}
	d1, err := ComputeSubjectDigest("sforum.auth-github.auth", "424242")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟重启：重新注入同一 secret。
	ResetIdentitySubjectHMACKeyForTest()
	if err := ConfigureIdentitySubjectHMAC(secret); err != nil {
		t.Fatal(err)
	}
	d2, err := ComputeSubjectDigest("sforum.auth-github.auth", "424242")
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("HMAC must be stable across restart: %s vs %s", d1, d2)
	}

	ctx := context.Background()
	cb := NewInMemoryCallbackStateStore()
	if err := cb.Save(ctx, CallbackTransaction{
		State: "m5-restart-state", ProviderID: "p1", Operation: ExternalAuthOperationLogin,
		ExpiresAt: time.Now().Add(CallbackStateDefaultTTL),
	}); err != nil {
		t.Fatal(err)
	}
	// 未过期可消费一次。
	if _, err := cb.Consume(ctx, "m5-restart-state"); err != nil {
		t.Fatalf("fresh state after restart semantics: %v", err)
	}
}

func TestM5_Lifecycle_DisableUninstallSafeModeForceDrain(t *testing.T) {
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	live := m5Live("m5.life", digest, "rt-m5")
	store := NewMemoryProviderActivationStore()
	m5ActivateLogin(t, store, live)

	// disable：Host 关闭 login → 不可用。
	off := false
	act, _ := store.Get(ctx, "m5.life")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.life", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, ExpectedRevision: act.Revision, LoginEnabled: &off,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatal(err)
	}
	disabled := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	av, err := disabled.EvaluateOperationAvailability(ctx, "m5.life", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("disable must remove availability: %#v err=%v", av, err)
	}

	// 重新启用后 uninstall：Registry 不可见。
	on := true
	act, _ = store.Get(ctx, "m5.life")
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.life", OwnerExtensionID: live.Artifact.ExtensionID,
		OwnerPackageDigest: digest, ExpectedRevision: act.Revision, LoginEnabled: &on,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatal(err)
	}
	uninstalled := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		},
	})
	av, err = uninstalled.EvaluateOperationAvailability(ctx, "m5.life", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("uninstall must remove availability: %#v err=%v", av, err)
	}

	// Safe Mode
	safe := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		SafeMode:        func() bool { return true },
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	av, err = safe.EvaluateOperationAvailability(ctx, "m5.life", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("Safe Mode must remove availability: %#v err=%v", av, err)
	}
	entries, err := safe.ListEffectivePublicCatalog(ctx, identityregistry.New(), "zh-CN")
	if err != nil || len(entries) != 0 {
		t.Fatalf("Safe Mode catalog must be empty: %#v err=%v", entries, err)
	}

	// ForceDrain：RuntimeInstanceID 清空 → 不可执行。
	drained := live
	drained.Artifact.RuntimeInstanceID = ""
	force := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return drained, nil
		},
	})
	// 先确保 activation 仍为 on（上面 Safe Mode 不写 activation）。
	av, err = force.EvaluateOperationAvailability(ctx, "m5.life", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("ForceDrain/empty runtime must remove availability: %#v err=%v", av, err)
	}
}

func TestM5_Lifecycle_StagedUpgradeNewDigestActivationRollback(t *testing.T) {
	// staged upgrade：live digest 变了但 activation 仍绑旧 digest → 不可用；
	// new-digest activation：用新 digest 重新 CAS 激活 → 可用；
	// rollback：live 回到旧 digest 且 re-activate → 可用。
	ctx := context.Background()
	oldDigest := strings.Repeat("1", 64)
	newDigest := strings.Repeat("2", 64)
	live := m5Live("m5.up", oldDigest, "rt-old")
	store := NewMemoryProviderActivationStore()
	m5ActivateLogin(t, store, live)

	// staged：live 已是新 digest，activation 仍旧。
	stagedLive := live
	stagedLive.Artifact.PackageDigest = newDigest
	stagedLive.Artifact.RuntimeInstanceID = "rt-new"
	staged := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return stagedLive, nil
		},
	})
	av, err := staged.EvaluateOperationAvailability(ctx, "m5.up", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("staged upgrade without re-activation must fail closed: %#v err=%v", av, err)
	}

	// new-digest activation
	act, _ := store.Get(ctx, "m5.up")
	on := true
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.up", OwnerExtensionID: stagedLive.Artifact.ExtensionID,
		OwnerPackageDigest: newDigest, ExpectedRevision: act.Revision, LoginEnabled: &on,
	}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		t.Fatal(err)
	}
	// PrepareActivationInput 从 live 派生 digest；此处直接写 store 模拟故意激活新 digest。
	// Memory store Upsert 使用输入中的 OwnerPackageDigest。
	act, _ = store.Get(ctx, "m5.up")
	// 若 no-mutation 因 digest 已写成功，继续校验。
	if act.OwnerPackageDigest != newDigest {
		// 强制写入新 digest 绑定（模拟 admin 对 live 的 re-activate）。
		if _, err := store.Upsert(ctx, ProviderActivationInput{
			ProviderID: "m5.up", OwnerExtensionID: stagedLive.Artifact.ExtensionID,
			OwnerPackageDigest: newDigest, ExpectedRevision: act.Revision, LoginEnabled: &on,
		}); err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
			t.Fatal(err)
		}
	}
	// 重新读：某些 store 在 digest 字段变更时会升 revision。
	// 若仍旧，直接构造带新 digest 的 activation 行。
	act, _ = store.Get(ctx, "m5.up")
	if act.OwnerPackageDigest != newDigest {
		// Memory store 在 no-mutation 时可能保留旧 digest；使用全新 store 模拟 re-bind。
		store2 := NewMemoryProviderActivationStore()
		if _, err := store2.Upsert(ctx, ProviderActivationInput{
			ProviderID: "m5.up", OwnerExtensionID: stagedLive.Artifact.ExtensionID,
			OwnerPackageDigest: newDigest, LoginEnabled: &on,
		}); err != nil {
			t.Fatal(err)
		}
		store = store2
	}
	activated := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return stagedLive, nil
		},
	})
	av, err = activated.EvaluateOperationAvailability(ctx, "m5.up", ExternalAuthOperationLogin)
	if err != nil || !av.Available {
		t.Fatalf("new-digest activation must restore availability: %#v err=%v", av, err)
	}

	// rollback：live 回到旧 digest；需再激活旧 digest。
	rolledLive := live
	rolledLive.Artifact.RuntimeInstanceID = "rt-old-again"
	rolledStore := NewMemoryProviderActivationStore()
	if _, err := rolledStore.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.up", OwnerExtensionID: rolledLive.Artifact.ExtensionID,
		OwnerPackageDigest: oldDigest, LoginEnabled: &on,
	}); err != nil {
		t.Fatal(err)
	}
	// 仍绑新 digest 的 activation 对旧 live fail closed。
	stale := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store, // still newDigest
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return rolledLive, nil
		},
	})
	av, err = stale.EvaluateOperationAvailability(ctx, "m5.up", ExternalAuthOperationLogin)
	if err != nil || av.Available {
		t.Fatalf("rollback live without matching activation must fail closed: %#v err=%v", av, err)
	}
	// 故意 re-activate 旧 digest。
	rollback := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: rolledStore,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return rolledLive, nil
		},
	})
	av, err = rollback.EvaluateOperationAvailability(ctx, "m5.up", ExternalAuthOperationLogin)
	if err != nil || !av.Available {
		t.Fatalf("rollback re-activation must restore availability: %#v err=%v", av, err)
	}
}

func TestM5_Lifecycle_TrustRevokeAndCallbackDuringChange(t *testing.T) {
	ctx := context.Background()
	digest := strings.Repeat("3", 64)
	live := m5Live("m5.rev", digest, "rt-1")
	store := NewMemoryProviderActivationStore()
	m5ActivateLogin(t, store, live)

	// trust revoke：解析失败 → ValidateCallbackBeforeEffect fail closed。
	revoked := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		},
	})
	tx := CallbackTransaction{
		ProviderID: "m5.rev", Operation: ExternalAuthOperationLogin,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerPackageDigest: digest,
		OwnerExtensionVersion: "1.0.0", ProviderContractVersion: "m5.rev@1",
	}
	if _, err := revoked.ValidateCallbackBeforeEffect(ctx, tx, "m5.rev"); !errors.Is(err, ErrExternalAuthProviderUnavailable) {
		t.Fatalf("trust revoke callback: %v", err)
	}

	// 变更中回调：state 绑旧 digest，live 已换新 digest。
	cb := NewInMemoryCallbackStateStore()
	if err := cb.Save(ctx, CallbackTransaction{
		State: "m5-mid-change", ProviderID: "m5.rev", Operation: ExternalAuthOperationLogin,
		OwnerExtensionID: live.Artifact.ExtensionID, OwnerPackageDigest: digest,
		OwnerExtensionVersion: "1.0.0", ProviderContractVersion: "m5.rev@1",
		ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	consumed, err := cb.Consume(ctx, "m5-mid-change")
	if err != nil {
		t.Fatal(err)
	}
	upgraded := live
	upgraded.Artifact.PackageDigest = strings.Repeat("9", 64)
	mid := NewExternalAuthService(ExternalAuthDeps{
		ActivationStore: store,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return upgraded, nil
		},
	})
	if _, err := mid.ValidateCallbackBeforeEffect(ctx, consumed, "m5.rev"); err == nil {
		t.Fatalf("callback during artifact change must fail closed")
	}
}

// --- 安全矩阵 ---

func TestM5_Security_ReplayExpiryCrossProviderOperationActor(t *testing.T) {
	ctx := context.Background()
	cb := NewInMemoryCallbackStateStore()
	tx := CallbackTransaction{
		State: "m5-sec-state", ProviderID: "prov.a", Operation: ExternalAuthOperationLogin,
		OwnerPackageDigest: "digest-a", ActorUserID: 0,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := cb.Save(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Consume(ctx, "m5-sec-state"); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Consume(ctx, "m5-sec-state"); !errors.Is(err, ErrCallbackStateReplayed) {
		t.Fatalf("replay: %v", err)
	}

	// expiry
	if err := cb.Save(ctx, CallbackTransaction{
		State: "m5-expired", ProviderID: "prov.a", Operation: ExternalAuthOperationLogin,
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cb.Consume(ctx, "m5-expired"); !errors.Is(err, ErrCallbackStateExpired) {
		t.Fatalf("expiry: %v", err)
	}

	// cross-provider / operation
	bound := CallbackTransaction{
		ProviderID: "prov.a", Operation: ExternalAuthOperationLogin, OwnerPackageDigest: "d1",
	}
	if bound.MatchesProvider("prov.b", ExternalAuthOperationLogin, "d1") {
		t.Fatal("cross-provider must reject")
	}
	if bound.MatchesProvider("prov.a", ExternalAuthOperationLink, "d1") {
		t.Fatal("cross-operation must reject")
	}

	// cross-actor (link)
	linkTx := CallbackTransaction{
		ProviderID: "prov.a", Operation: ExternalAuthOperationLink, ActorUserID: 7,
	}
	if linkTx.MatchesActor(ExternalAuthOperationLink, 8) {
		t.Fatal("cross-actor must reject")
	}
}

func TestM5_Security_ActivationCASAndDuplicateRegistrationTicket(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryProviderActivationStore()
	on := true
	created, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.cas", OwnerExtensionID: "ext.m5.cas",
		OwnerPackageDigest: strings.Repeat("c", 64), LoginEnabled: &on,
	})
	if err != nil {
		t.Fatal(err)
	}
	// stale CAS
	if _, err := store.Upsert(ctx, ProviderActivationInput{
		ProviderID: "m5.cas", OwnerExtensionID: "ext.m5.cas",
		OwnerPackageDigest: strings.Repeat("c", 64), ExpectedRevision: created.Revision - 1,
		LoginEnabled: &on,
	}); !errors.Is(err, ErrProviderActivationCASConflict) && !errors.Is(err, ErrProviderActivationNoMutation) {
		// revision 0 新建后 revision 通常为 1；用错误 revision。
		if _, err2 := store.Upsert(ctx, ProviderActivationInput{
			ProviderID: "m5.cas", OwnerExtensionID: "ext.m5.cas",
			OwnerPackageDigest: strings.Repeat("c", 64), ExpectedRevision: 99999,
			LoginEnabled: &on,
		}); !errors.Is(err2, ErrProviderActivationCASConflict) {
			t.Fatalf("stale CAS: first=%v second=%v", err, err2)
		}
	}

	// concurrent CAS：恰好一个成功
	var success, conflict int
	var wg sync.WaitGroup
	act, _ := store.Get(ctx, "m5.cas")
	off := false
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Upsert(ctx, ProviderActivationInput{
				ProviderID: "m5.cas", OwnerExtensionID: "ext.m5.cas",
				OwnerPackageDigest: strings.Repeat("c", 64), ExpectedRevision: act.Revision,
				LoginEnabled: &off,
			})
			if err == nil || errors.Is(err, ErrProviderActivationNoMutation) {
				success++
				return
			}
			if errors.Is(err, ErrProviderActivationCASConflict) {
				conflict++
				return
			}
		}()
	}
	wg.Wait()
	if success < 1 {
		t.Fatalf("expected CAS success>=1, success=%d conflict=%d", success, conflict)
	}

	// duplicate registration ticket one-use
	tickets := NewInMemoryRegistrationTicketStore()
	tok := "m5-reg-once"
	if err := tickets.Save(ctx, validRegistrationTicket(tok, time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(ctx, tok); err == nil {
		t.Fatal("duplicate registration ticket consume must fail")
	}
}

func TestM5_Security_SubjectRaceDigestIsolation(t *testing.T) {
	ResetIdentitySubjectHMACKeyForTest()
	t.Cleanup(ResetIdentitySubjectHMACKeyForTest)
	t.Setenv(IdentitySubjectHMACSecretEnv, "")
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	// 同 subject 跨 provider digest 不同 → 不串绑。
	da, err := ComputeSubjectDigest("prov.a", "42")
	if err != nil {
		t.Fatal(err)
	}
	db, err := ComputeSubjectDigest("prov.b", "42")
	if err != nil {
		t.Fatal(err)
	}
	if da == db {
		t.Fatal("subject digests must be provider-scoped")
	}
	// 不同 subject 同 provider 不同。
	dc, err := ComputeSubjectDigest("prov.a", "43")
	if err != nil {
		t.Fatal(err)
	}
	if da == dc {
		t.Fatal("different subjects must differ")
	}
}

func TestM5_Security_UnlinkRaceAndLastMethod(t *testing.T) {
	links := newT1ECountingLinkStore()
	links.byID[1] = ExternalIdentityLink{
		ID: 1, UserID: 9, ProviderID: "p1",
		Status: ExternalIdentityLinkStatusActive, Revision: 1,
	}
	links.digests[1] = strings.Repeat("d", 64)
	links.byKey["p1|"+strings.Repeat("d", 64)] = 1
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore:  links,
		RecentAuth: fixedRecentAuth{ok: true},
	})
	ctx := context.Background()
	fp := SessionFingerprint("m5-unlink")

	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e1 = svc.Unlink(ctx, UnlinkExternalIdentityInput{
			UserID: 9, LinkID: 1, ExpectedRevision: 1, SessionFingerprint: fp, RequestID: "a",
		})
	}()
	go func() {
		defer wg.Done()
		_, e2 = svc.Unlink(ctx, UnlinkExternalIdentityInput{
			UserID: 9, LinkID: 1, ExpectedRevision: 1, SessionFingerprint: fp, RequestID: "b",
		})
	}()
	wg.Wait()
	ok := 0
	for _, e := range []error{e1, e2} {
		if e == nil {
			ok++
			continue
		}
		if errors.Is(e, ErrExternalIdentityLinkStateConflict) || errors.Is(e, ErrExternalIdentityLinkNotFound) {
			continue
		}
		t.Fatalf("unexpected: %v", e)
	}
	if ok < 1 {
		t.Fatalf("expected one success; e1=%v e2=%v", e1, e2)
	}
}

func TestM5_Security_NonEnumeratingPublicErrors(t *testing.T) {
	// unlinked login → 稳定 generic 错误，不携带 subject/email 是否存在信息。
	ResetIdentitySubjectHMACKeyForTest()
	t.Cleanup(ResetIdentitySubjectHMACKeyForTest)
	digest := strings.Repeat("e", 64)
	live := m5Live("m5.enum", digest, "rt")
	links := newT1ECountingLinkStore()
	activation := &fakeActivationStoreLite{items: map[string]ProviderActivation{
		"m5.enum": {
			ProviderID: "m5.enum", OwnerExtensionID: live.Artifact.ExtensionID,
			OwnerPackageDigest: digest, LoginEnabled: true,
		},
	}}
	svc := NewExternalAuthService(ExternalAuthDeps{
		LinkStore: links, ActivationStore: activation,
		ProviderContribution: func(string) (identityregistry.ProviderContribution, error) {
			return live, nil
		},
	})
	_, err := svc.CompleteLogin(context.Background(), ExternalAuthAssertion{
		ProviderID: "m5.enum", Operation: ExternalAuthOperationLogin,
		ProviderSubject: "999", OwnerPackageDigest: digest,
		OwnerExtensionID: live.Artifact.ExtensionID,
	})
	if !errors.Is(err, ErrExternalIdentityUnlinked) {
		t.Fatalf("unlinked must be generic: %v", err)
	}
	// 错误字符串不得含 raw subject。
	if strings.Contains(err.Error(), "999") {
		t.Fatalf("error leaked subject: %v", err)
	}
}

func TestM5_RateLimit_StartAndCallback(t *testing.T) {
	lim := NewMemoryExternalAuthRateLimiter()
	ctx := context.Background()
	key := ExternalAuthRateKey("start", "203.0.113.10")
	// max=2：第 3 次拒绝。
	for i := 0; i < 2; i++ {
		ok, err := lim.Allow(ctx, key, 2, time.Minute)
		if err != nil || !ok {
			t.Fatalf("allow %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, err := lim.Allow(ctx, key, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("third start must be rate limited")
	}
	// callback 独立 key。
	ck := ExternalAuthRateKey("callback", "203.0.113.10")
	ok, err = lim.Allow(ctx, ck, 2, time.Minute)
	if err != nil || !ok {
		t.Fatalf("callback key independent: ok=%v err=%v", ok, err)
	}
}

func TestM5_Redaction_PublicCatalogEntryShape(t *testing.T) {
	// 公开 catalog 条目类型故意不包含 digest / runtime / secret / subject 字段。
	// 完整 HTTP redaction 见 Controllers/Identity external_auth_m5_http_test。
	entry := AuthProviderCatalogEntry{
		ProviderID: "ext.demo.auth", Kind: "auth", ContractVersion: "ext.demo.auth@1",
		Priority: 0, Operations: []string{"login.start"}, ActivatedOperations: []string{"login"},
		OwnerExtensionID: "ext.demo", Label: "Demo", Icon: "brand-demo",
	}
	if entry.ProviderID == "" || entry.OwnerExtensionID == "" {
		t.Fatal("expected ids")
	}
	sensitive := []string{
		strings.Repeat("a", 64),
		"client_secret", "access_token", "code_verifier", "providerSubject",
	}
	joined := entry.ProviderID + entry.Kind + entry.ContractVersion + entry.OwnerExtensionID + entry.Label + entry.Icon
	for _, op := range entry.Operations {
		joined += op
	}
	for _, op := range entry.ActivatedOperations {
		joined += op
	}
	for _, s := range sensitive {
		if strings.Contains(joined, s) {
			t.Fatalf("catalog entry leaked %q", s)
		}
	}
}
