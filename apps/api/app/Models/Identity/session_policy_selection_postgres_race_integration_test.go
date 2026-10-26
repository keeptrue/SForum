package identity

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentitySessionPolicyPostgresConcurrentCASHasOneWinner(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}

	selectResults := runIdentitySessionPolicyRace(2, func() error {
		_, err := fixture.sessionPolicy.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
			Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
		})
		return err
	})
	assertIdentitySessionPolicyRaceResult(t, selectResults)
	fixture.assertSessionPolicyCounts(1, 1, 1)
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.Revision != 1 || current.IdentitySessionPolicyEvidence != candidate {
		t.Fatalf("selection after race = %#v, %v", current, err)
	}

	resetResults := runIdentitySessionPolicyRace(2, func() error {
		_, err := fixture.sessionPolicy.Reset(fixture.ctx, ResetIdentitySessionPolicyInput{
			ExpectedRevision: 1, ActorUserID: fixture.adminUserID, ReasonCode: "operator_reset",
		})
		return err
	})
	assertIdentitySessionPolicyRaceResult(t, resetResults)
	fixture.assertSessionPolicyCounts(1, 2, 2)
	current, err = fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.Revision != 2 || current.PolicyID != IdentitySessionPolicyCoreDefault || current.Implicit {
		t.Fatalf("Core selection after reset race = %#v, %v", current, err)
	}
}

func TestIdentitySessionPolicyPostgresLifecycleSelectRace(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}

	lockConnection, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := lockConnection.Begin(fixture.ctx)
	if err != nil {
		lockConnection.Release()
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = lockTx.Rollback(fixture.ctx)
			lockConnection.Release()
		}
	}()
	if _, err := lockTx.Exec(
		fixture.ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		identityregistry.IdentitySessionPolicySelectionLockKey,
	); err != nil {
		t.Fatal(err)
	}
	var lockPID int32
	if err := lockTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&lockPID); err != nil {
		t.Fatal(err)
	}

	lifecyclePool := fixture.newPool("identity-session-policy-lifecycle-race")
	selectPool := fixture.newPool("identity-session-policy-select-race")
	lifecycleStore := identityregistry.NewPostgresStoreWithDependencies(
		lifecyclePool,
		identityregistry.PostgresStoreDependencies{SessionPolicyInvalidator: fixture.sessionPolicy},
	)
	selectStore, err := NewPostgresIdentitySessionPolicyStore(selectPool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	var lifecyclePID, selectPID int32
	if err := lifecyclePool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&lifecyclePID); err != nil {
		t.Fatal(err)
	}
	if err := selectPool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&selectPID); err != nil {
		t.Fatal(err)
	}

	lifecycleResult := make(chan error, 1)
	go func() {
		_, reconcileErr := lifecycleStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
			ExtensionID: fixture.extensionID, AllowedSource: &fixture.publication.Artifact,
			ActorUserID: fixture.actorUserID, AuditEventID: 1201,
		})
		lifecycleResult <- reconcileErr
	}()
	assertIdentityPersistenceBackendBlockedBy(t, fixture.admin, lifecyclePID, lockPID)

	selectResult := make(chan error, 1)
	go func() {
		_, selectErr := selectStore.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
			Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
		})
		selectResult <- selectErr
	}()
	assertIdentityPersistenceBackendBlockedBy(t, fixture.admin, selectPID, lifecyclePID)

	if err := lockTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	released = true
	lockConnection.Release()

	if err := awaitIdentitySessionPolicyRaceResult(t, lifecycleResult); err != nil {
		t.Fatalf("lifecycle race result: %v", err)
	}
	selectErr := awaitIdentitySessionPolicyRaceResult(t, selectResult)
	if !errors.Is(selectErr, ErrIdentitySessionPolicyDeclarationStale) {
		t.Fatalf("select race result: %v", selectErr)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault ||
		current.Revision != 0 || !current.Implicit {
		t.Fatalf("race final selection = %#v, err=%v", current, err)
	}
	fixture.assertSessionPolicyCounts(0, 0, 0)
}

func TestIdentitySessionPolicyPostgresSelectFirstLifecycleRace(t *testing.T) {
	fixture := newIdentityPersistencePGFixture(t)
	candidate, err := fixture.sessionPolicy.Candidate(fixture.ctx, fixture.sessionProvider.ID)
	if err != nil {
		t.Fatal(err)
	}

	lockConnection, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	lockTx, err := lockConnection.Begin(fixture.ctx)
	if err != nil {
		lockConnection.Release()
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = lockTx.Rollback(fixture.ctx)
			lockConnection.Release()
		}
	}()
	if _, err := lockTx.Exec(
		fixture.ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		identityregistry.IdentitySessionPolicySelectionLockKey,
	); err != nil {
		t.Fatal(err)
	}
	var lockPID int32
	if err := lockTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&lockPID); err != nil {
		t.Fatal(err)
	}

	selectPool := fixture.newPool("identity-session-policy-select-first-race")
	lifecyclePool := fixture.newPool("identity-session-policy-lifecycle-second-race")
	selectStore, err := NewPostgresIdentitySessionPolicyStore(selectPool, fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleStore := identityregistry.NewPostgresStoreWithDependencies(
		lifecyclePool,
		identityregistry.PostgresStoreDependencies{SessionPolicyInvalidator: fixture.sessionPolicy},
	)
	var selectPID, lifecyclePID int32
	if err := selectPool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&selectPID); err != nil {
		t.Fatal(err)
	}
	if err := lifecyclePool.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&lifecyclePID); err != nil {
		t.Fatal(err)
	}

	selectResult := make(chan error, 1)
	go func() {
		_, selectErr := selectStore.Select(fixture.ctx, SelectIdentitySessionPolicyInput{
			Candidate: candidate, ExpectedRevision: 0, ActorUserID: fixture.adminUserID,
		})
		selectResult <- selectErr
	}()
	assertIdentityPersistenceBackendBlockedBy(t, fixture.admin, selectPID, lockPID)

	lifecycleResult := make(chan error, 1)
	go func() {
		_, reconcileErr := lifecycleStore.Reconcile(fixture.ctx, identityregistry.ReconcilePublicationInput{
			ExtensionID: fixture.extensionID, AllowedSource: &fixture.publication.Artifact,
			ActorUserID: fixture.actorUserID, AuditEventID: 1202,
		})
		lifecycleResult <- reconcileErr
	}()
	assertIdentityPersistenceBackendBlockedBy(t, fixture.admin, lifecyclePID, selectPID)

	if err := lockTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	released = true
	lockConnection.Release()

	if err := awaitIdentitySessionPolicyRaceResult(t, selectResult); err != nil {
		t.Fatalf("select-first race result: %v", err)
	}
	if err := awaitIdentitySessionPolicyRaceResult(t, lifecycleResult); err != nil {
		t.Fatalf("lifecycle-second race result: %v", err)
	}
	current, err := fixture.sessionPolicy.Current(fixture.ctx)
	if err != nil || current.PolicyID != IdentitySessionPolicyCoreDefault ||
		current.Revision != 2 || current.Implicit {
		t.Fatalf("select-first race final selection = %#v, err=%v", current, err)
	}
	fixture.assertSessionPolicyCounts(1, 2, 2)
	events, err := fixture.sessionPolicy.ListEvents(fixture.ctx, 10)
	if err != nil || len(events) != 2 || events[0].Action != IdentitySessionPolicyActionInvalidate ||
		events[0].PreviousSelection == nil || *events[0].PreviousSelection != candidate {
		t.Fatalf("select-first race events = %#v, err=%v", events, err)
	}
}

func assertIdentityPersistenceBackendBlockedBy(
	t *testing.T,
	pool *pgxpool.Pool,
	pid int32,
	wantBlocker int32,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blockers []int32
		if err := pool.QueryRow(t.Context(), `
			SELECT pg_blocking_pids($1)
		`, pid).Scan(&blockers); err != nil {
			t.Fatal(err)
		}
		if len(blockers) == 1 && blockers[0] == wantBlocker {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d blockers=%v want [%d]", pid, blockers, wantBlocker)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitIdentitySessionPolicyRaceResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("session policy race did not complete before deadline")
		return nil
	}
}

func runIdentitySessionPolicyRace(count int, operation func() error) []error {
	start := make(chan struct{})
	results := make(chan error, count)
	var group sync.WaitGroup
	group.Add(count)
	for range count {
		go func() {
			defer group.Done()
			<-start
			results <- operation()
		}()
	}
	close(start)
	group.Wait()
	close(results)
	errorsFound := make([]error, 0, count)
	for err := range results {
		errorsFound = append(errorsFound, err)
	}
	return errorsFound
}

func assertIdentitySessionPolicyRaceResult(t *testing.T, results []error) {
	t.Helper()
	winners, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrIdentitySessionPolicyRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if winners != 1 || conflicts != len(results)-1 {
		t.Fatalf("race results winners=%d conflicts=%d all=%v", winners, conflicts, results)
	}
}
