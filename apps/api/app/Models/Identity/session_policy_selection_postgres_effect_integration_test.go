package identity

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentitySessionPolicyPostgresEffectDoesNotStarveHostPool(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	hostPool := fixture.newPool("identity-session-policy-effect-host")
	store, err := NewPostgresIdentitySessionPolicyStore(hostPool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	resolution := selectIdentitySessionPolicyStore(t, fixture, store)
	hostIdentity := NewPostgresStore(hostPool)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	entered := make(chan struct{})
	writeHostEffect, allowHostEffect := newIdentitySessionPolicyRelease(t)
	hostEffectWritten := make(chan struct{})
	release, releaseEffect := newIdentitySessionPolicyRelease(t)
	effectResult := make(chan error, 1)
	go func() {
		effectResult <- store.RunIfCurrent(ctx, resolution, identitySessionPolicyFixtureAuthority(fixture), func(effectCtx context.Context) error {
			close(entered)
			<-writeHostEffect
			var userID int64
			if err := hostPool.QueryRow(effectCtx, `SELECT id FROM users WHERE id = $1`, fixture.targetUserID).Scan(&userID); err != nil {
				return err
			}
			if err := hostIdentity.RecordLoginAudit(effectCtx, LoginAudit{
				UserID: fixture.targetUserID, Action: AuditActionLogin,
				IPAddress: "127.0.0.1", UserAgent: "effect-fixture", SessionHash: strings.Repeat("a", 64),
			}); err != nil {
				return err
			}
			close(hostEffectWritten)
			<-release
			return nil
		})
	}()
	awaitIdentitySessionPolicySignal(t, entered, "MaxConns=1 Host effect")

	hostIdentity.WithAuthorityMutationGate(store)
	updateResult := make(chan error, 1)
	go func() {
		updateResult <- hostIdentity.runIdentityAuthorityMutation(fixture.ctx, func() error {
			tx, beginErr := hostPool.Begin(fixture.ctx)
			if beginErr != nil {
				return beginErr
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			var userID int64
			if lockErr := tx.QueryRow(fixture.ctx, `
				SELECT id FROM users WHERE id = $1 FOR UPDATE
			`, fixture.targetUserID).Scan(&userID); lockErr != nil {
				return lockErr
			}
			return tx.Commit(fixture.ctx)
		})
	}()
	awaitIdentitySessionPolicyLocalWriter(t, store)
	if acquired := hostPool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("user authority writer borrowed %d MaxConns=1 Host connections", acquired)
	}
	allowHostEffect()
	select {
	case <-hostEffectWritten:
	case <-time.After(5 * time.Second):
		t.Fatal("Host effect could not reuse the MaxConns=1 main pool")
	}
	select {
	case err := <-updateResult:
		t.Fatalf("user authority writer crossed active Host effect: %v", err)
	default:
	}
	releaseEffect()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
		t.Fatalf("MaxConns=1 Host effect: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, updateResult); err != nil {
		t.Fatalf("user authority writer: %v", err)
	}
	var audits int
	if err := hostPool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM audit_events
		WHERE actor_user_id = $1 AND action = $2
	`, fixture.targetUserID, AuditActionLogin).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("Host effect audits=%d err=%v", audits, err)
	}
}

func TestIdentitySessionPolicyPostgresConcurrentEffectsDelayWriterUntilAllReturn(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution := selectIdentitySessionPolicyFixture(t, fixture)
	release0, releaseEffect0 := newIdentitySessionPolicyRelease(t)
	release1, releaseEffect1 := newIdentitySessionPolicyRelease(t)
	releases := []chan struct{}{release0, release1}
	entered := make(chan int, len(releases))
	effectResults := make(chan error, len(releases))
	for index := range releases {
		index := index
		go func() {
			effectResults <- fixture.sessionPolicy.RunIfCurrent(
				fixture.ctx,
				resolution,
				identitySessionPolicyFixtureAuthority(fixture),
				func(context.Context) error {
					entered <- index
					<-releases[index]
					return nil
				},
			)
		}()
	}
	for range releases {
		awaitIdentitySessionPolicyIndex(t, entered, "concurrent Host effect")
	}

	resetResult := make(chan error, 1)
	go func() {
		_, resetErr := fixture.sessionPolicy.Reset(fixture.ctx, ResetIdentitySessionPolicyInput{
			ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "all_effects_finished",
		})
		resetResult <- resetErr
	}()
	awaitIdentitySessionPolicyLocalWriter(t, fixture.sessionPolicy)
	releaseEffect0()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResults); err != nil {
		t.Fatalf("first effect result: %v", err)
	}
	select {
	case err := <-resetResult:
		t.Fatalf("writer crossed remaining shared effect: %v", err)
	default:
	}
	releaseEffect1()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResults); err != nil {
		t.Fatalf("second effect result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, resetResult); err != nil {
		t.Fatalf("writer after effects: %v", err)
	}
}

func TestIdentitySessionPolicyPostgresNonCooperativeEffectRetainsAdmissionAfterCancellation(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution, err := fixture.sessionPolicy.Resolve(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	entered := make(chan struct{})
	release, releaseEffect := newIdentitySessionPolicyRelease(t)
	effectResult := make(chan error, 1)
	go func() {
		effectResult <- fixture.sessionPolicy.RunIfCurrent(
			ctx,
			resolution,
			identitySessionPolicyFixtureAuthority(fixture),
			func(context.Context) error {
				close(entered)
				<-release
				return nil
			},
		)
	}()
	awaitIdentitySessionPolicySignal(t, entered, "non-cooperative Host effect")
	cancel()
	selectResult := make(chan error, 1)
	go func() {
		_, selectErr := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
			Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
		})
		selectResult <- selectErr
	}()
	awaitIdentitySessionPolicyLocalWriter(t, fixture.sessionPolicy)
	awaitIdentitySessionPolicySignal(t, ctx.Done(), "Host effect cancellation")
	select {
	case err := <-effectResult:
		t.Fatalf("non-cooperative callback returned at context cancellation: %v", err)
	default:
	}
	select {
	case err := <-selectResult:
		t.Fatalf("writer crossed non-cooperative callback at cancellation: %v", err)
	default:
	}
	releaseEffect()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
		t.Fatalf("accepted effect terminal result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, selectResult); err != nil {
		t.Fatalf("writer after non-cooperative effect: %v", err)
	}
}

func TestIdentitySessionPolicyPostgresEffectSerializesSelectionMutations(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution := selectIdentitySessionPolicyFixture(t, fixture)

	entered := make(chan struct{})
	release, releaseEffect := newIdentitySessionPolicyRelease(t)
	effectResult := make(chan error, 1)
	go func() {
		effectResult <- fixture.sessionPolicy.RunIfCurrent(
			fixture.ctx,
			resolution,
			identitySessionPolicyFixtureAuthority(fixture),
			func(context.Context) error {
				close(entered)
				<-release
				return nil
			},
		)
	}()
	awaitIdentitySessionPolicySignal(t, entered, "selection-serialized Host effect")

	resetPool := fixture.newPool("identity-session-policy-effect-reset")
	resetStore, err := NewPostgresIdentitySessionPolicyStore(resetPool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	var resetPID int32
	if err := resetPool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&resetPID); err != nil {
		t.Fatal(err)
	}
	resetResult := make(chan error, 1)
	go func() {
		_, resetErr := resetStore.Reset(fixture.ctx, ResetIdentitySessionPolicyInput{
			ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "effect_finished",
		})
		resetResult <- resetErr
	}()
	assertIdentityPersistenceBackendBlocked(t, fixture.admin, resetPID)

	releaseEffect()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
		t.Fatalf("effect result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, resetResult); err != nil {
		t.Fatalf("reset result: %v", err)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault || current.Revision != 2 || current.Implicit {
		t.Fatalf("selection after effect/reset = %#v, err=%v", current, err)
	}
}

func TestIdentitySessionPolicyPostgresEffectSerializesLifecycleInvalidation(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution := selectIdentitySessionPolicyFixture(t, fixture)

	entered := make(chan struct{})
	release, releaseEffect := newIdentitySessionPolicyRelease(t)
	effectResult := make(chan error, 1)
	go func() {
		effectResult <- fixture.sessionPolicy.RunIfCurrent(
			fixture.ctx,
			resolution,
			identitySessionPolicyFixtureAuthority(fixture),
			func(context.Context) error {
				close(entered)
				<-release
				return nil
			},
		)
	}()
	awaitIdentitySessionPolicySignal(t, entered, "lifecycle-serialized Host effect")

	lifecyclePool := fixture.newPool("identity-session-policy-effect-lifecycle")
	lifecycleSessionPolicy, err := NewPostgresIdentitySessionPolicyStore(lifecyclePool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleStore := identityregistry.NewPostgresStoreWithDependencies(
		lifecyclePool,
		identityregistry.PostgresStoreDependencies{SessionPolicyInvalidator: lifecycleSessionPolicy},
	)
	var lifecyclePID int32
	if err := lifecyclePool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&lifecyclePID); err != nil {
		t.Fatal(err)
	}
	lifecycleResult := make(chan error, 1)
	go func() {
		_, reconcileErr := lifecycleStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
			ExtensionID: fixture.extensionID, AllowedSource: &fixture.publication.Artifact,
			ActorUserID: fixture.actorUserID, AuditEventID: 1701,
		})
		lifecycleResult <- reconcileErr
	}()
	assertIdentityPersistenceBackendBlocked(t, fixture.admin, lifecyclePID)
	select {
	case err := <-lifecycleResult:
		t.Fatalf("lifecycle crossed active effect: %v", err)
	default:
	}

	releaseEffect()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
		t.Fatalf("effect result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, lifecycleResult); err != nil {
		t.Fatalf("lifecycle result: %v", err)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault || current.Revision != 2 || current.Implicit {
		t.Fatalf("selection after lifecycle = %#v, err=%v", current, err)
	}
}

func TestIdentitySessionPolicyPostgresEffectSerializesRegistryAuthorityMutations(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution := selectIdentitySessionPolicyFixture(t, fixture)

	entered := make(chan struct{})
	release, releaseEffect := newIdentitySessionPolicyRelease(t)
	effectResult := make(chan error, 1)
	go func() {
		effectResult <- fixture.sessionPolicy.RunIfCurrent(
			fixture.ctx,
			resolution,
			identitySessionPolicyFixtureAuthority(fixture),
			func(context.Context) error {
				close(entered)
				<-release
				return nil
			},
		)
	}()
	awaitIdentitySessionPolicySignal(t, entered, "Registry-serialized Host effect")

	writerStarted := make(chan struct{})
	writerResult := make(chan error, 1)
	go func() {
		close(writerStarted)
		_, removed, removeErr := fixture.registry.Remove(fixture.publication.Artifact)
		if removeErr == nil && !removed {
			removeErr = errors.New("exact Registry publication was not removed")
		}
		writerResult <- removeErr
	}()
	awaitIdentitySessionPolicySignal(t, writerStarted, "Registry writer start")

	releaseEffect()
	if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
		t.Fatalf("effect result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, writerResult); err != nil {
		t.Fatalf("Registry writer result: %v", err)
	}
	effectCalls := 0
	if err := fixture.sessionPolicy.RunIfCurrent(
		fixture.ctx,
		resolution,
		identitySessionPolicyFixtureAuthority(fixture),
		func(context.Context) error { effectCalls++; return nil },
	); !errors.Is(err, ErrIdentitySessionPolicyDeclarationStale) || effectCalls != 0 {
		t.Fatalf("stale effect calls=%d err=%v", effectCalls, err)
	}
}

func TestIdentitySessionPolicyPostgresEffectHoldsExactArtifactAndUserRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *pgxpool.Pool, *identityPersistencePGFixture) error
	}{
		{
			name: "extension status update",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture *identityPersistencePGFixture) error {
				_, err := pool.Exec(ctx, `UPDATE extensions SET status = 'disabled' WHERE id = $1`, fixture.extensionID)
				return err
			},
		},
		{
			name: "target user delete",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture *identityPersistencePGFixture) error {
				_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID)
				return err
			},
		},
		{
			name: "target user status update",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture *identityPersistencePGFixture) error {
				_, err := pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, fixture.targetUserID)
				return err
			},
		},
		{
			name: "target user token version update",
			mutate: func(ctx context.Context, pool *pgxpool.Pool, fixture *identityPersistencePGFixture) error {
				_, err := pool.Exec(ctx, `
					UPDATE users SET current_token_version = current_token_version + 1 WHERE id = $1
				`, fixture.targetUserID)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityPersistencePGFixture(t)
			resolution := selectIdentitySessionPolicyFixture(t, fixture)
			entered := make(chan struct{})
			release, releaseEffect := newIdentitySessionPolicyRelease(t)
			effectResult := make(chan error, 1)
			go func() {
				effectResult <- fixture.sessionPolicy.RunIfCurrent(
					fixture.ctx,
					resolution,
					identitySessionPolicyFixtureAuthority(fixture),
					func(context.Context) error {
						close(entered)
						<-release
						return nil
					},
				)
			}()
			awaitIdentitySessionPolicySignal(t, entered, test.name+" effect")

			writerPool := fixture.newPool("sp-effect-row-writer")
			var writerPID int32
			if err := writerPool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
			writerResult := make(chan error, 1)
			go func() { writerResult <- test.mutate(fixture.ctx, writerPool, fixture) }()
			assertIdentityPersistenceBackendBlocked(t, fixture.admin, writerPID)

			releaseEffect()
			if err := awaitIdentitySessionPolicyRaceResult(t, effectResult); err != nil {
				t.Fatalf("effect result: %v", err)
			}
			if err := awaitIdentitySessionPolicyRaceResult(t, writerResult); err != nil {
				t.Fatalf("writer result: %v", err)
			}
		})
	}
}

func TestIdentitySessionPolicyPostgresEffectRejectsInactiveUser(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution, err := fixture.sessionPolicy.Resolve(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE users SET status = 'disabled' WHERE id = $1
	`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	effectCalls := 0
	err = fixture.sessionPolicy.RunIfCurrent(
		fixture.ctx,
		resolution,
		identitySessionPolicyFixtureAuthority(fixture),
		func(context.Context) error { effectCalls++; return nil },
	)
	if !errors.Is(err, ErrIdentitySessionPolicyDeclarationStale) || effectCalls != 0 {
		t.Fatalf("inactive user effects=%d err=%v", effectCalls, err)
	}
}

func TestIdentitySessionPolicyPostgresEffectRejectsStaleResolutionWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *identityPersistencePGFixture)
	}{
		{
			name: "selection reset",
			mutate: func(t *testing.T, fixture *identityPersistencePGFixture) {
				if _, err := fixture.sessionPolicy.Reset(fixture.ctx, ResetIdentitySessionPolicyInput{
					ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "effect_stale",
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Safe Mode",
			mutate: func(t *testing.T, fixture *identityPersistencePGFixture) {
				snapshot := fixture.registry.Snapshot()
				if _, err := fixture.registry.ReplaceAllIfRevision(
					snapshot.Revision,
					snapshot.Publications,
					snapshot.Tombstones,
					true,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "same ID artifact replacement",
			mutate: func(t *testing.T, fixture *identityPersistencePGFixture) {
				target := fixture.insertSessionPolicyPublicationVersion("2.0.0", "b", "identity-runtime-v2")
				if _, err := fixture.registryStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
					ExtensionID: fixture.extensionID, AllowedSource: &fixture.publication.Artifact,
					AllowedTarget: &target.Artifact, Desired: &target,
					ActorUserID: fixture.actorUserID, AuditEventID: 1702,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.pool.Exec(fixture.ctx, `
					UPDATE extensions SET active_version_id = $2 WHERE id = $1
				`, fixture.extensionID, target.Artifact.VersionID); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.registry.ReplaceAll([]identityregistry.Publication{target}, false); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdentityPersistencePGFixture(t)
			resolution := selectIdentitySessionPolicyFixture(t, fixture)
			test.mutate(t, fixture)
			effectCalls := 0
			err := fixture.sessionPolicy.RunIfCurrent(
				fixture.ctx,
				resolution,
				identitySessionPolicyFixtureAuthority(fixture),
				func(context.Context) error { effectCalls++; return nil },
			)
			if !errors.Is(err, ErrIdentitySessionPolicyDeclarationStale) || effectCalls != 0 {
				t.Fatalf("stale effect calls=%d err=%v", effectCalls, err)
			}
		})
	}
}

func TestIdentitySessionPolicyPostgresEffectAllowsUnrelatedRegistryDrift(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution := selectIdentitySessionPolicyFixture(t, fixture)
	artifact, err := identityregistry.NewCoreArtifact(
		"core.identity.effect-unrelated",
		"1.0.0",
		strings.Repeat("f", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.registry.Publish(identityregistry.Publication{Artifact: artifact}); err != nil {
		t.Fatal(err)
	}

	effectCalls := 0
	if err := fixture.sessionPolicy.RunIfCurrent(
		fixture.ctx,
		resolution,
		identitySessionPolicyFixtureAuthority(fixture),
		func(context.Context) error { effectCalls++; return nil },
	); err != nil || effectCalls != 1 {
		t.Fatalf("unrelated Registry drift effects=%d err=%v", effectCalls, err)
	}
}

func TestIdentitySessionPolicyPostgresCanceledEffectWaitersReleaseAdmission(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	resolution, err := fixture.sessionPolicy.Resolve(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.sessionPolicy.effectGate.lockWrite(t.Context()); err != nil {
		t.Fatal(err)
	}
	var writerRelease sync.Once
	releaseWriter := func() { writerRelease.Do(fixture.sessionPolicy.effectGate.unlockWrite) }
	t.Cleanup(releaseWriter)

	const waiters = identitySessionPolicyEffectConnections
	results := make(chan error, waiters)
	var effectCalls atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	for index := 0; index < waiters; index++ {
		go func() {
			results <- fixture.sessionPolicy.RunIfCurrent(
				ctx,
				resolution,
				identitySessionPolicyFixtureAuthority(fixture),
				func(context.Context) error { effectCalls.Add(1); return nil },
			)
		}()
	}
	awaitIdentitySessionPolicyEffectSlots(t, fixture.sessionPolicy, waiters)
	cancel()
	for index := 0; index < waiters; index++ {
		if err := awaitIdentitySessionPolicyRaceResult(t, results); !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter %d error = %v", index, err)
		}
	}
	if effectCalls.Load() != 0 || len(fixture.sessionPolicy.effectConnectionSlots) != 0 {
		t.Fatalf("canceled waiters effects=%d slots=%d", effectCalls.Load(), len(fixture.sessionPolicy.effectConnectionSlots))
	}
	releaseWriter()
	if !fixture.sessionPolicy.effectGate.TryLock() {
		t.Fatal("canceled effect waiters leaked the local admission gate")
	}
	fixture.sessionPolicy.effectGate.Unlock()
}

func TestIdentitySessionPolicyPostgresEffectExitReleasesAdmission(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	applicationName := "sp-effect-exit-" + fixture.schema[len(fixture.schema)-12:]
	effectPool := fixture.newPool(applicationName)
	store, err := NewPostgresIdentitySessionPolicyStore(effectPool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := store.Resolve(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseline := identityPersistenceApplicationConnections(t, fixture.admin, applicationName)

	t.Run("error", func(t *testing.T) {
		wantErr := errors.New("Host session storage failed")
		if err := store.RunIfCurrent(
			fixture.ctx,
			resolution,
			identitySessionPolicyFixtureAuthority(fixture),
			func(context.Context) error {
				awaitIdentityPersistenceApplicationConnections(t, fixture.admin, applicationName, baseline+1)
				return wantErr
			},
		); !errors.Is(err, wantErr) {
			t.Fatalf("effect error = %v", err)
		}
		assertIdentitySessionPolicyEffectExit(t, fixture, store, applicationName, baseline)
	})

	t.Run("panic", func(t *testing.T) {
		panicValue := "Host effect panic"
		func() {
			defer func() {
				if recovered := recover(); recovered != panicValue {
					t.Fatalf("recovered panic = %#v", recovered)
				}
			}()
			_ = store.RunIfCurrent(
				fixture.ctx,
				resolution,
				identitySessionPolicyFixtureAuthority(fixture),
				func(context.Context) error {
					awaitIdentityPersistenceApplicationConnections(t, fixture.admin, applicationName, baseline+1)
					panic(panicValue)
				},
			)
		}()
		assertIdentitySessionPolicyEffectExit(t, fixture, store, applicationName, baseline)
	})

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		entered := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- store.RunIfCurrent(
				ctx,
				resolution,
				identitySessionPolicyFixtureAuthority(fixture),
				func(effectCtx context.Context) error {
					close(entered)
					<-effectCtx.Done()
					return context.Cause(effectCtx)
				},
			)
		}()
		awaitIdentitySessionPolicySignal(t, entered, "cancelable effect admission")
		awaitIdentityPersistenceApplicationConnections(t, fixture.admin, applicationName, baseline+1)
		cancel()
		if err := awaitIdentitySessionPolicyRaceResult(t, result); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled effect err=%v", err)
		}
		assertIdentitySessionPolicyEffectExit(t, fixture, store, applicationName, baseline)
	})
}

func identitySessionPolicyFixtureAuthority(fixture *identityPersistencePGFixture) IdentitySessionAuthority {
	return IdentitySessionAuthority{UserID: fixture.targetUserID, TokenVersion: 0}
}

func selectIdentitySessionPolicyFixture(
	t *testing.T,
	fixture *identityPersistencePGFixture,
) IdentitySessionPolicyResolution {
	return selectIdentitySessionPolicyStore(t, fixture, fixture.sessionPolicy)
}

func selectIdentitySessionPolicyStore(
	t *testing.T,
	fixture *identityPersistencePGFixture,
	store *PostgresIdentitySessionPolicyStore,
) IdentitySessionPolicyResolution {
	t.Helper()
	candidate, err := store.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
		Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
	}); err != nil {
		t.Fatal(err)
	}
	resolution, err := store.Resolve(fixture.ctx)
	if err != nil || resolution.Source != IdentitySessionPolicySourcePlugin || resolution.Provider == nil {
		t.Fatalf("plugin resolution=%#v err=%v", resolution, err)
	}
	return resolution
}

func awaitIdentitySessionPolicyLocalWriter(
	t *testing.T,
	store *PostgresIdentitySessionPolicyStore,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if !store.effectGate.TryRLock() {
			return
		}
		store.effectGate.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("session policy local writer did not queue before deadline")
		}
		runtime.Gosched()
	}
}

func awaitIdentitySessionPolicyEffectSlots(
	t *testing.T,
	store *PostgresIdentitySessionPolicyStore,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if len(store.effectConnectionSlots) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session policy effect slots did not reach %d", want)
		}
		runtime.Gosched()
	}
}

func newIdentitySessionPolicyRelease(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	return release, closeRelease
}

func awaitIdentitySessionPolicySignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitIdentitySessionPolicyIndex(t *testing.T, signal <-chan int, label string) int {
	t.Helper()
	select {
	case index := <-signal:
		return index
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return 0
	}
}

func assertIdentityPersistenceBackendBlocked(t *testing.T, pool *pgxpool.Pool, pid int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blockers []int32
		if err := pool.QueryRow(t.Context(), `SELECT pg_blocking_pids($1)`, pid).Scan(&blockers); err != nil {
			t.Fatal(err)
		}
		if len(blockers) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d was not blocked before deadline", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertIdentitySessionPolicyExclusiveAdmissionAvailable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var acquired bool
	if err := tx.QueryRow(t.Context(), `
		SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))
	`, identityregistry.IdentitySessionPolicySelectionLockKey).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("exclusive session-policy admission remained locked after effect exit")
	}
}

func identityPersistenceApplicationConnections(t *testing.T, pool *pgxpool.Pool, applicationName string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_stat_activity WHERE application_name = $1
	`, applicationName).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func awaitIdentityPersistenceApplicationConnections(
	t *testing.T,
	pool *pgxpool.Pool,
	applicationName string,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := identityPersistenceApplicationConnections(t, pool, applicationName); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("application %q connection count did not become %d", applicationName, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertIdentitySessionPolicyEffectExit(
	t *testing.T,
	fixture *identityPersistencePGFixture,
	store *PostgresIdentitySessionPolicyStore,
	applicationName string,
	baseline int,
) {
	t.Helper()
	awaitIdentityPersistenceApplicationConnections(t, fixture.admin, applicationName, baseline)
	if slots := len(store.effectConnectionSlots); slots != 0 {
		t.Fatalf("effect connection slots still held = %d", slots)
	}
	if !store.effectGate.TryLock() {
		t.Fatal("local effect gate remained held after effect exit")
	}
	store.effectGate.Unlock()
	assertIdentitySessionPolicyExclusiveAdmissionAvailable(t, store.pool)
}
