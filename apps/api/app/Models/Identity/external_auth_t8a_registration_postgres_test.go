package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// T8A：CompleteRegistration 授权提交边界与事务正确性。
// 必须直接调用 CompleteRegistration，覆盖 disable / artifact upgrade /
// policy-close race / rollback / event once / audit，以及拒绝路径零写入。
// 需要 SFORUM_TEST_DATABASE_URL；未设置时 skip。

type t8aEventCapture struct {
	names []string
	count atomic.Int32
}

func (c *t8aEventCapture) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	c.names = append(c.names, envelope.Name)
	if envelope.Name == appevents.UserRegistered {
		c.count.Add(1)
	}
	return appevents.Result{OK: true}
}

type t8aRegistrationHarness struct {
	t                    *testing.T
	fixture              *identityPersistencePGFixture
	ctx                  context.Context
	activation           *MemoryProviderActivationStore
	regEnabled           atomic.Bool
	safeMode             atomic.Bool
	live                 identityregistry.ProviderContribution
	liveOverride         func(string) (identityregistry.ProviderContribution, error)
	registrationPolicyTx func(context.Context, pgx.Tx) (bool, error)
	events               *t8aEventCapture
	svc                  *ExternalAuthService
	baselineUsers        int
	baselineLinks        int
	baselineRoles        int
	baselineAudits       int
}

func newT8ARegistrationHarness(t *testing.T) *t8aRegistrationHarness {
	t.Helper()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	t.Cleanup(cancel)

	prepareT8ARegistrationSchema(t, ctx, fixture)

	h := &t8aRegistrationHarness{
		t:          t,
		fixture:    fixture,
		ctx:        ctx,
		activation: NewMemoryProviderActivationStore(),
		events:     &t8aEventCapture{},
		live:       fixture.provider,
	}
	h.regEnabled.Store(true)
	h.safeMode.Store(false)
	h.svc = h.buildService()
	h.snapshotBaseline()
	h.activateRegistration(true)
	return h
}

func prepareT8ARegistrationSchema(t *testing.T, ctx context.Context, fixture *identityPersistencePGFixture) {
	t.Helper()
	// CompleteRegistration 需要与生产 createUserWithoutCredentialTx 对齐的列，
	// 以及 is_default 默认角色。
	if _, err := fixture.pool.Exec(ctx, `
		ALTER TABLE users
		  ADD COLUMN IF NOT EXISTS is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE,
		  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
		ALTER TABLE roles
		  ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT FALSE;
		UPDATE roles SET is_default = FALSE;
		UPDATE roles SET is_default = TRUE WHERE key = 'member';
	`); err != nil {
		t.Fatalf("prepare t8a schema: %v", err)
	}
	// 确认恰好一个默认角色，避免 assignDefaultRoleTx 失败掩盖其它路径。
	var defaults int
	if err := fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roles WHERE is_default = TRUE`).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default role, got %d", defaults)
	}
}

func (h *t8aRegistrationHarness) buildService() *ExternalAuthService {
	policyTx := h.registrationPolicyTx
	if policyTx == nil {
		policyTx = func(context.Context, pgx.Tx) (bool, error) {
			return h.regEnabled.Load(), nil
		}
	}
	return NewExternalAuthService(ExternalAuthDeps{
		Pool:            h.fixture.pool,
		LinkStore:       h.fixture.externalLinks,
		ActivationStore: h.activation,
		SafeMode:        func() bool { return h.safeMode.Load() },
		RegistrationEnabled: func(context.Context) (bool, error) {
			return h.regEnabled.Load(), nil
		},
		RegistrationEnabledTx: policyTx,
		AnyUserExists: func(ctx context.Context) (bool, error) {
			var count int
			err := h.fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
			return count > 0, err
		},
		ProviderContribution: func(providerID string) (identityregistry.ProviderContribution, error) {
			if h.liveOverride != nil {
				return h.liveOverride(providerID)
			}
			if providerID != h.live.ID {
				return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
			}
			if h.safeMode.Load() {
				return identityregistry.ProviderContribution{}, ErrExternalAuthProviderUnavailable
			}
			return h.live, nil
		},
		Events: h.events,
	})
}

func (h *t8aRegistrationHarness) activateRegistration(enabled bool) {
	h.t.Helper()
	on := enabled
	_, err := h.activation.Upsert(h.ctx, ProviderActivationInput{
		ProviderID:          h.live.ID,
		OwnerExtensionID:    h.live.Artifact.ExtensionID,
		OwnerPackageDigest:  h.live.Artifact.PackageDigest,
		OwnerExtensionVerID: h.live.Artifact.VersionID,
		RegistrationEnabled: &on,
		ExpectedRevision:    h.currentActivationRevision(),
	})
	if err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
		// 新建 revision=0；若已存在用当前 revision。
		act, getErr := h.activation.Get(h.ctx, h.live.ID)
		if getErr != nil {
			h.t.Fatalf("activate registration: %v (get: %v)", err, getErr)
		}
		_, err = h.activation.Upsert(h.ctx, ProviderActivationInput{
			ProviderID:          h.live.ID,
			OwnerExtensionID:    h.live.Artifact.ExtensionID,
			OwnerPackageDigest:  h.live.Artifact.PackageDigest,
			OwnerExtensionVerID: h.live.Artifact.VersionID,
			RegistrationEnabled: &on,
			ExpectedRevision:    act.Revision,
		})
		if err != nil && !errors.Is(err, ErrProviderActivationNoMutation) {
			h.t.Fatalf("activate registration retry: %v", err)
		}
	}
}

func (h *t8aRegistrationHarness) currentActivationRevision() int64 {
	act, err := h.activation.Get(h.ctx, h.live.ID)
	if err != nil {
		return 0
	}
	return act.Revision
}

func (h *t8aRegistrationHarness) snapshotBaseline() {
	h.t.Helper()
	h.baselineUsers = h.count(`SELECT COUNT(*) FROM users`)
	h.baselineLinks = h.count(`SELECT COUNT(*) FROM identity_external_links`)
	h.baselineRoles = h.count(`SELECT COUNT(*) FROM user_roles`)
	h.baselineAudits = h.count(`SELECT COUNT(*) FROM audit_events`)
}

func (h *t8aRegistrationHarness) count(query string) int {
	h.t.Helper()
	var n int
	if err := h.fixture.pool.QueryRow(h.ctx, query).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *t8aRegistrationHarness) assertZeroRegistrationEffects() {
	h.t.Helper()
	users := h.count(`SELECT COUNT(*) FROM users`)
	links := h.count(`SELECT COUNT(*) FROM identity_external_links`)
	roles := h.count(`SELECT COUNT(*) FROM user_roles`)
	// 拒绝路径不得新增 user / link / role；audit 亦不得留下 external_register 成功记录。
	if users != h.baselineUsers || links != h.baselineLinks || roles != h.baselineRoles {
		h.t.Fatalf(
			"denied path wrote state users=%d→%d links=%d→%d roles=%d→%d",
			h.baselineUsers, users, h.baselineLinks, links, h.baselineRoles, roles,
		)
	}
	var regAudits int
	if err := h.fixture.pool.QueryRow(h.ctx, `
		SELECT COUNT(*) FROM audit_events WHERE action = $1
	`, AuditActionExternalRegister).Scan(&regAudits); err != nil {
		h.t.Fatal(err)
	}
	if regAudits != 0 {
		h.t.Fatalf("denied path must not write registration audit, got %d", regAudits)
	}
	if h.events.count.Load() != 0 {
		h.t.Fatalf("denied path must not emit user.registered, got %d", h.events.count.Load())
	}
}

func (h *t8aRegistrationHarness) assertion(subject string) ExternalAuthAssertion {
	return ExternalAuthAssertion{
		ProviderID:              h.live.ID,
		ProviderContractVersion: h.live.ContractVersion,
		OwnerExtensionID:        h.live.Artifact.ExtensionID,
		OwnerExtensionVersion:   h.live.Artifact.ExtensionVersion,
		OwnerPackageDigest:      h.live.Artifact.PackageDigest,
		Operation:               ExternalAuthOperationRegistration,
		ProviderSubject:         subject,
		CorrelationID:           fmt.Sprintf("t8a-corr-%s", subject),
	}
}

func (h *t8aRegistrationHarness) input(username string) ExternalRegistrationInput {
	return ExternalRegistrationInput{
		Username:    username,
		Email:       username + "@example.test",
		DisplayName: username,
		Locale:      "zh-CN",
	}.Normalized()
}

func TestT8A_PostgresCompleteRegistrationSuccessEventAndAudit(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))
	t.Setenv("SFORUM_IN_PRODUCTION", "")

	rawSubject := "raw-github-subject-99"
	assertion := h.assertion(rawSubject)
	assertion.CorrelationID = "t8a-corr-success-ok"
	result, err := h.svc.CompleteRegistration(h.ctx, assertion, h.input("t8a_ok_user"))
	if err != nil {
		t.Fatalf("CompleteRegistration: %v", err)
	}
	if result.User.ID <= 0 || result.LinkID <= 0 {
		t.Fatalf("result incomplete: %#v", result)
	}
	if h.events.count.Load() != 1 {
		t.Fatalf("user.registered count=%d want 1 events=%v", h.events.count.Load(), h.events.names)
	}
	// 恰好一次：再次手动 emit 不应由 CompleteRegistration 重复。
	registered := 0
	for _, name := range h.events.names {
		if name == appevents.UserRegistered {
			registered++
		}
	}
	if registered != 1 {
		t.Fatalf("user.registered appearances=%d want 1", registered)
	}

	var regAudits, linkAudits int
	if err := h.fixture.pool.QueryRow(h.ctx, `
		SELECT COUNT(*) FROM audit_events WHERE action = $1 AND target_user_id = $2
	`, AuditActionExternalRegister, result.User.ID).Scan(&regAudits); err != nil {
		t.Fatal(err)
	}
	if regAudits != 1 {
		t.Fatalf("registration audit count=%d want 1", regAudits)
	}
	if err := h.fixture.pool.QueryRow(h.ctx, `
		SELECT COUNT(*) FROM audit_events
		WHERE action = 'identity.external_link.link' AND target_user_id = $1
	`, result.User.ID).Scan(&linkAudits); err != nil {
		t.Fatal(err)
	}
	if linkAudits != 1 {
		t.Fatalf("link audit count=%d want 1", linkAudits)
	}

	// 无密码凭据。
	var creds int
	if err := h.fixture.pool.QueryRow(h.ctx, `
		SELECT COUNT(*) FROM user_credentials WHERE user_id = $1
	`, result.User.ID).Scan(&creds); err != nil {
		// 隔离 fixture 可能无 user_credentials 表；仅在表存在时断言。
		if !strings.Contains(err.Error(), "user_credentials") {
			t.Fatal(err)
		}
	} else if creds != 0 {
		t.Fatalf("external registration must not create password credential, got %d", creds)
	}

	// 审计 metadata 仅保留 provider/owner/correlation；不得泄漏 raw subject、
	// Host digest、artifact digest 或 OAuth 材料。
	var meta string
	if err := h.fixture.pool.QueryRow(h.ctx, `
		SELECT metadata::text FROM audit_events
		WHERE action = $1 AND target_user_id = $2
	`, AuditActionExternalRegister, result.User.ID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		rawSubject,
		h.live.Artifact.PackageDigest,
		"providerSubject", "subjectDigest", "ownerPackageDigest",
		"state", "codeVerifier", "completionToken", "clientSecret",
	} {
		if strings.Contains(meta, forbidden) {
			t.Fatalf("forbidden registration audit material %q: %s", forbidden, meta)
		}
	}
}

func TestT8A_PostgresTicketAfterDisableZeroWrite(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	// 模拟 ticket 签发后 operator 关闭 registration 操作。
	h.activateRegistration(false)
	h.snapshotBaseline()

	_, err := h.svc.CompleteRegistration(h.ctx, h.assertion("2001"), h.input("t8a_disabled"))
	if !errors.Is(err, ErrExternalAuthOperationNotActivated) &&
		!errors.Is(err, ErrExternalAuthProviderUnavailable) {
		t.Fatalf("after disable err=%v", err)
	}
	h.assertZeroRegistrationEffects()
}

func TestT8A_PostgresArtifactUpgradeZeroWrite(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	// ticket 绑定旧 digest；live 已升级到新 digest。
	assertion := h.assertion("3001")
	upgraded := h.live
	upgraded.Artifact.PackageDigest = strings.Repeat("b", 64)
	h.liveOverride = func(providerID string) (identityregistry.ProviderContribution, error) {
		if providerID != upgraded.ID {
			return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
		}
		return upgraded, nil
	}
	// 激活目录仍指向旧 digest → 有效激活也会失败；或 live 比对失败。
	// 为隔离 artifact 比对，更新激活到新 digest，但 ticket 仍为旧 digest。
	on := true
	act, _ := h.activation.Get(h.ctx, h.live.ID)
	_, _ = h.activation.Upsert(h.ctx, ProviderActivationInput{
		ProviderID:          upgraded.ID,
		OwnerExtensionID:    upgraded.Artifact.ExtensionID,
		OwnerPackageDigest:  upgraded.Artifact.PackageDigest,
		OwnerExtensionVerID: upgraded.Artifact.VersionID,
		RegistrationEnabled: &on,
		ExpectedRevision:    act.Revision,
	})
	h.snapshotBaseline()

	_, err := h.svc.CompleteRegistration(h.ctx, assertion, h.input("t8a_artifact"))
	if !errors.Is(err, ErrExternalAuthArtifactMismatch) {
		t.Fatalf("artifact upgrade err=%v want artifact mismatch", err)
	}
	h.assertZeroRegistrationEffects()
}

func TestT8A_PostgresPolicyCloseRaceZeroWrite(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	// 第一次（事务外）返回 true，第二次（事务内）返回 false，模拟 policy-close race。
	var calls atomic.Int32
	h.svc = NewExternalAuthService(ExternalAuthDeps{
		Pool:                h.fixture.pool,
		LinkStore:           h.fixture.externalLinks,
		ActivationStore:     h.activation,
		RegistrationEnabled: func(context.Context) (bool, error) { return true, nil },
		RegistrationEnabledTx: func(context.Context, pgx.Tx) (bool, error) {
			calls.Add(1)
			return false, nil
		},
		AnyUserExists: func(ctx context.Context) (bool, error) {
			var count int
			err := h.fixture.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
			return count > 0, err
		},
		ProviderContribution: func(providerID string) (identityregistry.ProviderContribution, error) {
			if providerID != h.live.ID {
				return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
			}
			return h.live, nil
		},
		Events: h.events,
	})
	h.snapshotBaseline()

	_, err := h.svc.CompleteRegistration(h.ctx, h.assertion("4001"), h.input("t8a_policy_race"))
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("policy race err=%v want registration disabled", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("registration policy must be read through the transaction, calls=%d", calls.Load())
	}
	h.assertZeroRegistrationEffects()
}

func TestR2_PostgresPolicyCloseAfterFastCheckZeroWrite(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	if _, err := h.fixture.pool.Exec(h.ctx, `
		INSERT INTO web_options (name, value) VALUES ('identity.registration.enabled', 'enabled')
		ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value
	`); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	h.registrationPolicyTx = func(ctx context.Context, tx pgx.Tx) (bool, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		var value string
		err := tx.QueryRow(ctx, `SELECT value FROM web_options WHERE name = 'identity.registration.enabled'`).Scan(&value)
		if err != nil {
			return false, err
		}
		return value == "enabled", nil
	}
	h.svc = h.buildService()
	h.snapshotBaseline()

	result := make(chan error, 1)
	go func() {
		_, err := h.svc.CompleteRegistration(h.ctx, h.assertion("r2-policy-race"), h.input("r2_policy_race"))
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("registration transaction did not reach policy read")
	}
	if _, err := h.fixture.pool.Exec(h.ctx, `
		UPDATE web_options SET value = 'disabled' WHERE name = 'identity.registration.enabled'
	`); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrRegistrationDisabled) {
			t.Fatalf("closed policy err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("registration did not finish after policy close")
	}
	h.assertZeroRegistrationEffects()
}

func TestR2_PostgresBootstrapStillRejectsExternalRegistration(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	if _, err := h.fixture.pool.Exec(h.ctx, `DELETE FROM audit_events; DELETE FROM user_roles; DELETE FROM identity_external_links; DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	h.snapshotBaseline()
	_, err := h.svc.CompleteRegistration(h.ctx, h.assertion("r2-bootstrap"), h.input("r2_bootstrap"))
	if !errors.Is(err, ErrExternalAuthBootstrapRequired) {
		t.Fatalf("bootstrap external registration err=%v", err)
	}
	h.assertZeroRegistrationEffects()
}

func TestT8A_PostgresRollbackOnDefaultRoleFailure(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	// 清除默认角色 → assignDefaultRoleTx 失败 → 整事务回滚。
	if _, err := h.fixture.pool.Exec(h.ctx, `UPDATE roles SET is_default = FALSE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.fixture.pool.Exec(context.Background(), `UPDATE roles SET is_default = TRUE WHERE key = 'member'`)
	})
	h.snapshotBaseline()

	_, err := h.svc.CompleteRegistration(h.ctx, h.assertion("5001"), h.input("t8a_rollback"))
	if !errors.Is(err, ErrExternalAuthDefaultRoleFailed) {
		t.Fatalf("rollback err=%v want default role failed", err)
	}
	h.assertZeroRegistrationEffects()
}

func TestT8A_PostgresSafeModeUninstallRevokeZeroWrite(t *testing.T) {
	h := newT8ARegistrationHarness(t)
	ResetIdentitySubjectHMACKeyForTest()
	t.Setenv(IdentitySubjectHMACSecretEnv, strings.Repeat("k", 32))

	cases := []struct {
		name string
		prep func()
	}{
		{
			name: "safe_mode",
			prep: func() { h.safeMode.Store(true) },
		},
		{
			name: "uninstall_or_revoke",
			prep: func() {
				h.liveOverride = func(string) (identityregistry.ProviderContribution, error) {
					return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
				}
			},
		},
		{
			name: "version_drift",
			prep: func() {
				assertionVersion := h.live
				// live 版本漂移，ticket 仍带 1.0.0。
				assertionVersion.Artifact.ExtensionVersion = "9.9.9"
				h.liveOverride = func(providerID string) (identityregistry.ProviderContribution, error) {
					if providerID != assertionVersion.ID {
						return identityregistry.ProviderContribution{}, ErrAuthProviderNotFound
					}
					return assertionVersion, nil
				}
				// 激活跟随 live 新版本 digest/id，仅 version 字符串漂移由 MatchesLiveContribution 捕获。
				on := true
				act, _ := h.activation.Get(h.ctx, h.live.ID)
				_, _ = h.activation.Upsert(h.ctx, ProviderActivationInput{
					ProviderID:          assertionVersion.ID,
					OwnerExtensionID:    assertionVersion.Artifact.ExtensionID,
					OwnerPackageDigest:  assertionVersion.Artifact.PackageDigest,
					OwnerExtensionVerID: assertionVersion.Artifact.VersionID,
					RegistrationEnabled: &on,
					ExpectedRevision:    act.Revision,
				})
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 每 case 重置可变状态。
			h.safeMode.Store(false)
			h.liveOverride = nil
			h.events = &t8aEventCapture{}
			h.activateRegistration(true)
			tc.prep()
			h.svc = h.buildService()
			h.snapshotBaseline()

			subject := fmt.Sprintf("6%03d", i+1)
			_, err := h.svc.CompleteRegistration(h.ctx, h.assertion(subject), h.input("t8a_"+tc.name))
			if err == nil {
				t.Fatalf("%s must fail closed", tc.name)
			}
			h.assertZeroRegistrationEffects()
		})
	}
}

func TestT8A_MatchesLiveContributionStrict(t *testing.T) {
	live := identityregistry.ProviderContribution{
		Provider: identityregistry.Provider{
			ID: "prov.a", ContractVersion: "prov.a@1", Kind: identityregistry.ProviderKindAuth,
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "ext.a", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), VersionID: 1, RuntimeInstanceID: "rt-a",
		},
	}
	ok := ExternalAuthAssertion{
		ProviderID: "prov.a", ProviderContractVersion: "prov.a@1",
		OwnerExtensionID: "ext.a", OwnerExtensionVersion: "1.0.0",
		OwnerPackageDigest: strings.Repeat("a", 64),
	}
	if !ok.MatchesLiveContribution(live) {
		t.Fatal("exact match must succeed")
	}
	badDigest := ok
	badDigest.OwnerPackageDigest = strings.Repeat("b", 64)
	if badDigest.MatchesLiveContribution(live) {
		t.Fatal("digest drift must fail")
	}
	badVersion := ok
	badVersion.OwnerExtensionVersion = "2.0.0"
	if badVersion.MatchesLiveContribution(live) {
		t.Fatal("version drift must fail")
	}
	badContract := ok
	badContract.ProviderContractVersion = "prov.a@2"
	if badContract.MatchesLiveContribution(live) {
		t.Fatal("contract drift must fail")
	}
	core := live
	core.Artifact.Core = true
	if ok.MatchesLiveContribution(core) {
		t.Fatal("core artifact must fail")
	}
}
