package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresExecutableTrustStoreConsumesChallengeOnce(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	extensionID := "trust.integration." + suffix
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Trust Integration') RETURNING id
	`, "trust_"+suffix, "trust_"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID)
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ($1, 'plugin', 'Trust Integration')`, extensionID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)

	identity := TrustIdentity{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), Action: TrustActionEnable,
		ImpactDigest: strings.Repeat("b", 64),
	}
	store := NewPostgresExecutableTrustStore(pool)
	if err := store.CreateChallenge(ctx, TrustChallengeRecord{
		TokenHash: strings.Repeat("c", 64), ActorUserID: actorID, Identity: identity,
		ArtifactDigests: map[string]string{"package": identity.PackageDigest},
		Impact: TrustImpact{
			SchemaVersion: TrustImpactSchemaV1, Action: TrustActionEnable,
			ExtensionID: extensionID, ExtensionVersion: "1.0.0",
			PackageDigest: identity.PackageDigest, ArtifactDigests: map[string]string{"package": identity.PackageDigest},
			Digest: identity.ImpactDigest,
		},
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	input := TrustConsumeInput{TokenHash: strings.Repeat("c", 64), ActorUserID: actorID, Identity: identity}
	type consumeResult struct {
		grant TrustGrant
		err   error
	}
	results := make(chan consumeResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			grant, err := store.ConsumeChallenge(ctx, input)
			results <- consumeResult{grant: grant, err: err}
		}()
	}
	wait.Wait()
	close(results)
	succeeded := 0
	replayed := 0
	var createdGrant TrustGrant
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
			createdGrant = result.grant
		case errors.Is(result.err, ErrTrustChallengeReplayed):
			replayed++
		default:
			t.Fatalf("unexpected consume result: %v", result.err)
		}
	}
	if succeeded != 1 || replayed != 1 || !createdGrant.created {
		t.Fatalf("consume results succeeded=%d replayed=%d grant=%#v", succeeded, replayed, createdGrant)
	}
	granted, err := store.HasLiveGrant(ctx, identity)
	if err != nil || !granted {
		t.Fatalf("live grant=%v err=%v", granted, err)
	}
	liveGrant, err := store.LiveGrant(ctx, identity)
	if err != nil || liveGrant.ID <= 0 || liveGrant.RevokedAt != nil || liveGrant.RevokedByUserID != 0 {
		t.Fatalf("loaded live grant=%#v err=%v", liveGrant, err)
	}
	mismatched := createdGrant
	mismatched.ImpactDigest = strings.Repeat("d", 64)
	if err := store.revokeExactGrant(ctx, mismatched, actorID, "wrong_identity"); !errors.Is(err, ErrTrustGrantNotFound) {
		t.Fatalf("mismatched exact revoke error=%v", err)
	}
	if granted, err := store.HasLiveGrant(ctx, identity); err != nil || !granted {
		t.Fatalf("mismatched revoke changed live grant=%t err=%v", granted, err)
	}
	// An activation publication holds this extension advisory lock from its
	// final live-grant check through commit. Compensation revoke must wait rather
	// than flipping revoked_at inside that proof window.
	activationTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := LockExecutableTrustExtensionTx(ctx, activationTx, extensionID); err != nil {
		_ = activationTx.Rollback(ctx)
		t.Fatal(err)
	}
	defer activationTx.Rollback(context.Background())
	var activationPID int32
	if err := activationTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&activationPID); err != nil {
		t.Fatal(err)
	}
	waiterApplicationName := "sforum_trust_revoke_waiter_" + suffix
	waiterConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	waiterConfig.ConnConfig.RuntimeParams["application_name"] = waiterApplicationName
	waiterConfig.MaxConns = 1
	waiterPool, err := pgxpool.NewWithConfig(ctx, waiterConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterPool.Close()
	waiterStore := NewPostgresExecutableTrustStore(waiterPool)
	revokeCtx, cancelRevoke := context.WithCancel(ctx)
	defer cancelRevoke()
	exactRevokeDone := make(chan error, 1)
	go func() {
		exactRevokeDone <- waiterStore.revokeExactGrant(revokeCtx, createdGrant, actorID, "activation_failed")
	}()
	waitForExecutableTrustAdvisoryLockWaiter(
		t, ctx, pool, activationPID, waiterApplicationName, exactRevokeDone,
	)
	if err := activationTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-exactRevokeDone; err != nil {
		t.Fatal(err)
	}
	if err := store.revokeExactGrant(ctx, createdGrant, actorID, "activation_failed_replay"); err != nil {
		t.Fatalf("exact revoke must be idempotent: %v", err)
	}
	granted, err = store.HasLiveGrant(ctx, identity)
	if err != nil || granted {
		t.Fatalf("exact revoked grant=%v err=%v", granted, err)
	}

	// 重新授权后保留 RevokeAll 的生产覆盖。
	secondTokenHash := strings.Repeat("e", 64)
	if err := store.CreateChallenge(ctx, TrustChallengeRecord{
		TokenHash: secondTokenHash, ActorUserID: actorID, Identity: identity,
		ArtifactDigests: map[string]string{"package": identity.PackageDigest},
		Impact: TrustImpact{
			SchemaVersion: TrustImpactSchemaV1, Action: TrustActionEnable,
			ExtensionID: extensionID, ExtensionVersion: identity.ExtensionVersion,
			PackageDigest: identity.PackageDigest, ArtifactDigests: map[string]string{"package": identity.PackageDigest},
			Digest: identity.ImpactDigest,
		},
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	startRace := make(chan struct{})
	consumeDone := make(chan error, 1)
	revokeDone := make(chan error, 1)
	go func() {
		<-startRace
		_, err := store.ConsumeChallenge(ctx, TrustConsumeInput{
			TokenHash: secondTokenHash, ActorUserID: actorID, Identity: identity,
		})
		consumeDone <- err
	}()
	go func() {
		<-startRace
		revokeDone <- store.RevokeAll(ctx, extensionID, actorID, "integration_test")
	}()
	close(startRace)
	consumeErr := <-consumeDone
	if consumeErr != nil && !errors.Is(consumeErr, ErrTrustChallengeStale) {
		t.Fatalf("concurrent consume error=%v", consumeErr)
	}
	if err := <-revokeDone; err != nil {
		t.Fatalf("concurrent revoke error=%v", err)
	}
	granted, err = store.HasLiveGrant(ctx, identity)
	if err != nil || granted {
		t.Fatalf("revoked grant=%v err=%v", granted, err)
	}
}

func waitForExecutableTrustAdvisoryLockWaiter(
	t *testing.T,
	ctx context.Context,
	observer *pgxpool.Pool,
	blockerPID int32,
	applicationName string,
	result <-chan error,
) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var waiting bool
		if err := observer.QueryRow(waitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks AS held
				JOIN pg_locks AS waiter
				  ON waiter.locktype = held.locktype
				 AND waiter.database IS NOT DISTINCT FROM held.database
				 AND waiter.classid IS NOT DISTINCT FROM held.classid
				 AND waiter.objid IS NOT DISTINCT FROM held.objid
				 AND waiter.objsubid IS NOT DISTINCT FROM held.objsubid
				JOIN pg_stat_activity AS activity ON activity.pid = waiter.pid
				WHERE held.pid = $1
				  AND held.locktype = 'advisory'
				  AND held.granted
				  AND NOT waiter.granted
				  AND activity.application_name = $2
				  AND $1 = ANY(pg_blocking_pids(waiter.pid))
			)
		`, blockerPID, applicationName).Scan(&waiting); err != nil {
			t.Fatalf("inspect exact revoke advisory-lock waiter: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("exact revoke returned before reaching the advisory lock: %v", err)
		case <-waitCtx.Done():
			t.Fatalf("exact revoke did not reach the advisory lock: %v", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresActivationAttemptsDriveBootLoopSkip(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	extension := Extension{
		ID:   "activation.integration." + fmt.Sprintf("%d", time.Now().UnixNano()),
		Name: "Activation Integration", Version: "1.0.0", Type: TypePlugin,
		Status: StatusEnabled, Source: SourceUploaded, PackageDigest: strings.Repeat("d", 64),
		Manifest: Manifest{Backend: ManifestBackend{Entry: "backend/plugin"}},
	}
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name, status) VALUES ($1, 'plugin', $2, 'enabled')`, extension.ID, extension.Name); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extension.ID)
	store := NewPostgresStore(pool)
	attempt, err := store.BeginActivationAttempt(ctx, extension, ActivationTriggerStartup, "boot-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteActivationAttempt(ctx, attempt.ID, ActivationStatusFailed, "startup_crash"); err != nil {
		t.Fatal(err)
	}
	coordinator := NewActivationCoordinator(store)
	skip, err := coordinator.ShouldSkipStartup(ctx, extension, "boot-2")
	if err != nil || !skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
	latest, err := store.LatestActivationAttempt(ctx, extension.ID, extension.PackageDigest)
	if err != nil || latest.Status != ActivationStatusSkipped || latest.FailureReason != "boot_loop_guard" {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
}

// TestPostgresExecutableTrustStoreSupersedesStaleLiveGrantForSamePackage 固化启动期
// 不变量：同一 (extension, version, package_digest, action) 只能有一条 live grant。
//
// dev watcher 每次构建都按当前声明重新签发 exact-artifact grant；声明变化会派生
// 新的 impact_digest 而 package_digest 不变，于是「按完整 identity 幂等」会把新旧
// 两行都留成 live，启动恢复公开权威时
// (PostgresLifecycleAssetAuthority.exactLiveGrantImpact) 只能 fail closed。
func TestPostgresExecutableTrustStoreSupersedesStaleLiveGrantForSamePackage(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	extensionID := "trust.supersede." + suffix
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Trust Supersede') RETURNING id
	`, "trust_supersede_"+suffix, "trust_supersede_"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID)
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ($1, 'plugin', 'Trust Supersede')`, extensionID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)

	store := NewPostgresExecutableTrustStore(pool)
	packageDigest := strings.Repeat("a", 64)
	base := TrustIdentity{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		PackageDigest: packageDigest, Action: TrustActionEnable,
	}
	grantFor := func(identity TrustIdentity) TrustEnsureGrantInput {
		return TrustEnsureGrantInput{
			ActorUserID: actorID, Identity: identity,
			ArtifactDigests: map[string]string{"package": packageDigest},
			Impact: TrustImpact{
				SchemaVersion: TrustImpactSchemaV1, Action: TrustActionEnable,
				ExtensionID: identity.ExtensionID, ExtensionVersion: identity.ExtensionVersion,
				PackageDigest: packageDigest, ArtifactDigests: map[string]string{"package": packageDigest},
				Digest: identity.ImpactDigest,
			},
		}
	}
	liveImpacts := func() []string {
		rows, err := pool.Query(ctx, `
			SELECT impact_digest FROM extension_trust_grants
			WHERE extension_id = $1 AND extension_version = $2 AND package_digest = $3
			  AND action = $4 AND revoked_at IS NULL
			ORDER BY id
		`, extensionID, "1.0.0", packageDigest, TrustActionEnable)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var impacts []string
		for rows.Next() {
			var impact string
			if err := rows.Scan(&impact); err != nil {
				t.Fatal(err)
			}
			impacts = append(impacts, impact)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return impacts
	}

	// 首次签发：建立唯一 live 权威。
	first := base
	first.ImpactDigest = strings.Repeat("b", 64)
	if _, err := store.EnsureLiveGrant(ctx, grantFor(first)); err != nil {
		t.Fatal(err)
	}
	if impacts := liveImpacts(); len(impacts) != 1 || impacts[0] != first.ImpactDigest {
		t.Fatalf("first grant must be the only live authority: %v", impacts)
	}

	// 同包新声明（impact 变了、package digest 没变）：旧的 live grant 必须被撤销，
	// 只保留新的一条，且保留审计行（不物理删除）。
	second := base
	second.ImpactDigest = strings.Repeat("c", 64)
	if _, err := store.EnsureLiveGrant(ctx, grantFor(second)); err != nil {
		t.Fatal(err)
	}
	if impacts := liveImpacts(); len(impacts) != 1 || impacts[0] != second.ImpactDigest {
		t.Fatalf("newer impact must replace the stale live grant: %v", impacts)
	}
	var revokedAt *time.Time
	var reason string
	if err := pool.QueryRow(ctx, `
		SELECT revoked_at, revocation_reason FROM extension_trust_grants
		WHERE extension_id = $1 AND impact_digest = $2
	`, extensionID, first.ImpactDigest).Scan(&revokedAt, &reason); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil || reason != executableTrustSupersededGrantReason {
		t.Fatalf("stale grant must stay as a revoked audit row: revokedAt=%v reason=%q", revokedAt, reason)
	}

	// 幂等：同一 exact identity 重复签发不新增行。
	if _, err := store.EnsureLiveGrant(ctx, grantFor(second)); err != nil {
		t.Fatal(err)
	}
	if impacts := liveImpacts(); len(impacts) != 1 || impacts[0] != second.ImpactDigest {
		t.Fatalf("repeated grant must be idempotent: %v", impacts)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_trust_grants WHERE extension_id = $1`, extensionID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("grant history must keep both rows, got %d", total)
	}

	// 人工挑战路径同样收敛：消费新挑战后仍然只有一条 live grant。
	third := base
	third.ImpactDigest = strings.Repeat("d", 64)
	tokenHash := strings.Repeat("e", 64)
	if err := store.CreateChallenge(ctx, TrustChallengeRecord{
		TokenHash: tokenHash, ActorUserID: actorID, Identity: third,
		ArtifactDigests: map[string]string{"package": packageDigest},
		Impact:          grantFor(third).Impact,
		ExpiresAt:       time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeChallenge(ctx, TrustConsumeInput{
		TokenHash: tokenHash, ActorUserID: actorID, Identity: third,
	}); err != nil {
		t.Fatal(err)
	}
	if impacts := liveImpacts(); len(impacts) != 1 || impacts[0] != third.ImpactDigest {
		t.Fatalf("challenge consumption must also converge to one live grant: %v", impacts)
	}
}

// TestPostgresExecutableTrustStoreLoadsGrantWhoseGrantorWasDeleted 固化
// granted_by_user_id 可空这一事实：列上挂着 ON DELETE SET NULL，删掉曾授予
// 可执行信任的管理员后，live grant 仍然有效，但授予人变成 NULL。
//
// 启动期 refresh extension guard policy 会读这条 grant；把 NULL 扫进 int64
// 会让整个 API 起不来（cannot scan NULL into *int64），所以 LiveGrant 必须
// 与 revoked_by_user_id 一样把缺失的授予人归一成 0（未知）。
func TestPostgresExecutableTrustStoreLoadsGrantWhoseGrantorWasDeleted(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	extensionID := "trust.deleted-grantor." + suffix
	var actorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Deleted Grantor') RETURNING id
	`, "trust_grantor_"+suffix, "trust_grantor_"+suffix+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID)
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id, type, name) VALUES ($1, 'plugin', 'Deleted Grantor')`, extensionID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)

	identity := TrustIdentity{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), Action: TrustActionEnable,
		ImpactDigest: strings.Repeat("b", 64),
	}
	store := NewPostgresExecutableTrustStore(pool)
	if _, err := store.EnsureLiveGrant(ctx, TrustEnsureGrantInput{
		ActorUserID: actorID, Identity: identity,
		ArtifactDigests: map[string]string{"package": identity.PackageDigest},
		Impact: TrustImpact{
			SchemaVersion: TrustImpactSchemaV1, Action: TrustActionEnable,
			ExtensionID: extensionID, ExtensionVersion: identity.ExtensionVersion,
			PackageDigest:   identity.PackageDigest,
			ArtifactDigests: map[string]string{"package": identity.PackageDigest},
			Digest:          identity.ImpactDigest,
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 删掉授予人：FK 的 ON DELETE SET NULL 把列置空，grant 本身仍是 live。
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID); err != nil {
		t.Fatal(err)
	}
	var grantorNull, live bool
	if err := pool.QueryRow(ctx, `
		SELECT granted_by_user_id IS NULL, revoked_at IS NULL
		FROM extension_trust_grants
		WHERE extension_id = $1
	`, extensionID).Scan(&grantorNull, &live); err != nil {
		t.Fatal(err)
	}
	if !grantorNull || !live {
		t.Fatalf("fixture must leave one live grant with a NULL grantor: null=%v live=%v", grantorNull, live)
	}

	grant, err := store.LiveGrant(ctx, identity)
	if err != nil {
		t.Fatalf("load live grant whose grantor was deleted: %v", err)
	}
	if grant.GrantedByUserID != 0 {
		t.Fatalf("granted_by_user_id = %d, want 0 for a deleted grantor", grant.GrantedByUserID)
	}
	if grant.ID <= 0 || grant.ExtensionID != extensionID || grant.RevokedAt != nil {
		t.Fatalf("loaded grant = %#v", grant)
	}
}
