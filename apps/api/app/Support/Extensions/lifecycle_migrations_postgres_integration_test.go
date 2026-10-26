package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestProductionLifecycleMigrationsConcurrentNoopAndRestart(t *testing.T) {
	ctx, pool, request := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineUpgrade, 4)
	adapter := NewProductionLifecycleBoundaryMigrations(pool, nil)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- adapter.ReconcileLifecycleMigrations(ctx, request, LifecycleBoundaryMigrationUpgrade)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var count int
	var status, proofKind string
	var targetReady, sourceSafe bool
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(status), max(proof_kind), bool_and(target_ready), bool_and(source_resume_safe)
		FROM extension_lifecycle_migration_proofs
		WHERE operation_id = $1
	`, request.OperationID).Scan(&count, &status, &proofKind, &targetReady, &sourceSafe); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != lifecycleMigrationStatusTargetReady ||
		proofKind != lifecycleMigrationProofHostNoop || !targetReady || !sourceSafe {
		t.Fatalf("proof count=%d status=%q kind=%q target=%v source=%v", count, status, proofKind, targetReady, sourceSafe)
	}
	restarted := NewProductionLifecycleBoundaryMigrations(pool, nil)
	allowed, err := restarted.CanResumeLifecycleSource(ctx, request, LifecycleBoundaryMigrationUpgrade)
	if err != nil || !allowed {
		t.Fatalf("source resume after restart = %v, %v", allowed, err)
	}
}

func TestProductionLifecycleMigrationsDeclaredSQLBlocksWithoutP5AndRejectsDrift(t *testing.T) {
	ctx, pool, request := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineUpgrade, 4)
	request.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "upgrade", strings.Repeat("d", 64)),
	}
	adapter := NewProductionLifecycleBoundaryMigrations(pool, nil)
	canPrepare, err := adapter.CanPrepareLifecycleMigrations(ctx, LifecycleStaticPreflightRequest{
		Operation: request.Operation, SourceExtension: request.SourceExtension,
		TargetExtension: request.TargetExtension,
	})
	if err != nil || canPrepare {
		t.Fatalf("declared migration static preflight = %v, %v", canPrepare, err)
	}
	err = adapter.ReconcileLifecycleMigrations(ctx, request, LifecycleBoundaryMigrationUpgrade)
	if !errors.Is(err, ErrLifecycleMigrationProofRequired) {
		t.Fatalf("missing P5 error = %v", err)
	}
	allowed, err := NewProductionLifecycleBoundaryMigrations(pool, nil).CanResumeLifecycleSource(
		ctx, request, LifecycleBoundaryMigrationUpgrade,
	)
	if err != nil || !allowed {
		t.Fatalf("source after pre-execution block = %v, %v", allowed, err)
	}
	ready, err := adapter.LifecycleArtifactMigrationReady(ctx, request.TargetExtension)
	if err != nil || ready {
		t.Fatalf("declared target readiness = %v, %v", ready, err)
	}

	drifted := cloneLifecycleBoundaryRequest(request)
	drifted.TargetExtension.Manifest.Migrations[0].Digest = strings.Repeat("e", 64)
	if err := adapter.ReconcileLifecycleMigrations(ctx, drifted, LifecycleBoundaryMigrationUpgrade); !errors.Is(err, ErrLifecycleMigrationsConflict) {
		t.Fatalf("declaration drift error = %v", err)
	}
}

func TestProductionLifecycleMigrationsUnknownExecutionKeepsSourceClosed(t *testing.T) {
	ctx, pool, request := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineRollback, 5)
	request.SourceExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "source", strings.Repeat("d", 64)),
	}
	request.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "target", strings.Repeat("e", 64)),
	}
	engine := &lifecycleMigrationIntegrationEngine{
		runErr:     errors.New("connection lost after migration dispatch"),
		inspectErr: errors.New("P5 ledger unavailable"),
	}
	adapter := NewProductionLifecycleBoundaryMigrations(pool, engine)
	if err := adapter.ReconcileLifecycleMigrations(ctx, request, LifecycleBoundaryMigrationRollback); err == nil {
		t.Fatal("expected unknown execution failure")
	}
	allowed, err := NewProductionLifecycleBoundaryMigrations(pool, nil).CanResumeLifecycleSource(
		ctx, request, LifecycleBoundaryMigrationRollback,
	)
	if err != nil || allowed {
		t.Fatalf("source after unknown execution = %v, %v", allowed, err)
	}
}

func TestProductionLifecycleMigrationsPersistsP5ProofAcrossRestart(t *testing.T) {
	ctx, pool, request := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineInstall, 2)
	request.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "install", strings.Repeat("d", 64)),
	}
	engine := &lifecycleMigrationIntegrationEngine{}
	adapter := NewProductionLifecycleBoundaryMigrations(pool, engine)
	if err := adapter.ReconcileLifecycleMigrations(ctx, request, LifecycleBoundaryMigrationInstall); err != nil {
		t.Fatal(err)
	}
	ready, err := NewProductionLifecycleBoundaryMigrations(pool, nil).LifecycleArtifactMigrationReady(
		ctx, request.TargetExtension,
	)
	if err != nil || !ready {
		t.Fatalf("P5 proof after restart = %v, %v", ready, err)
	}
	canPrepare, err := NewProductionLifecycleBoundaryMigrations(pool, nil).CanPrepareLifecycleMigrations(
		ctx, LifecycleStaticPreflightRequest{
			Operation: request.Operation, TargetExtension: request.TargetExtension,
		},
	)
	if err != nil || canPrepare {
		t.Fatalf("historical target proof static preflight = %v, %v", canPrepare, err)
	}
	if engine.runCalls != 1 || engine.inspectCalls != 1 {
		t.Fatalf("engine calls = run:%d inspect:%d", engine.runCalls, engine.inspectCalls)
	}
}

func TestProductionLifecycleMigrationsDoesNotReuseHistoricalTargetState(t *testing.T) {
	ctx, pool, rollback := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineRollback, 5)
	declaration := lifecycleMigrationTestDeclaration(rollback.TargetExtension.ID, "shared", strings.Repeat("d", 64))
	rollback.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{declaration}
	rollback.SourceExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(rollback.TargetExtension.ID, "newer", strings.Repeat("e", 64)),
	}
	_, targetDigest, err := lifecycleMigrationDeclarations(rollback.TargetExtension.Manifest.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	var historicalOperationID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, terminal_result, completed_at
		) VALUES ($1, $2, $3, '{}'::jsonb, 'install', 'enabled', 'historical@1',
		          $4, $5, 'builtin', '{}'::jsonb, 'succeeded', statement_timestamp())
		RETURNING id
	`, rollback.TargetExtension.ID, rollback.TargetExtension.Version,
		rollback.TargetExtension.PackageDigest, fmt.Sprintf("historical:%d", time.Now().UnixNano()),
		strings.Repeat("9", 64)).Scan(&historicalOperationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_migration_proofs WHERE operation_id = $1`, historicalOperationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, historicalOperationID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_migration_proofs (
			operation_id, operation, migration_mode, step_id, position,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_migrations_digest, plan_digest,
			first_attempt, last_attempt, status, target_ready, source_resume_safe,
			proof_kind, proof_id, proof_digest, proven_at
		) VALUES ($1, 'install', 'install', 'lifecycle.install.02.host.migrating', 2,
		          $2, $3, $4, $5, $6, $7, 1, 1, 'target_ready', TRUE, TRUE,
		          'p5_engine', 'historical-proof', $8, statement_timestamp())
	`, historicalOperationID, rollback.TargetExtension.ID, rollback.TargetExtension.Version,
		rollback.TargetExtension.PackageDigest, rollback.TargetExtension.ActiveVersionID,
		targetDigest, strings.Repeat("8", 64), strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}

	adapter := NewProductionLifecycleBoundaryMigrations(pool, nil)
	canPrepare, err := adapter.CanPrepareLifecycleMigrations(ctx, LifecycleStaticPreflightRequest{
		Operation: rollback.Operation, SourceExtension: rollback.SourceExtension,
		TargetExtension: rollback.TargetExtension,
	})
	if err != nil || canPrepare {
		t.Fatalf("historical target proof allowed rollback = %v, %v", canPrepare, err)
	}
	if err := adapter.ReconcileLifecycleMigrations(ctx, rollback, LifecycleBoundaryMigrationRollback); !errors.Is(err, ErrLifecycleMigrationProofRequired) {
		t.Fatalf("historical target proof reconciliation error = %v", err)
	}
}

func TestProductionLifecycleMigrationsRecordsNotStartedSourceProof(t *testing.T) {
	ctx, pool, request := newLifecycleMigrationIntegration(t, extensions.LifecycleMachineUpgrade, 2)
	request.TargetExtension.Manifest.Migrations = []extensions.ManifestMigration{
		lifecycleMigrationTestDeclaration(request.TargetExtension.ID, "upgrade", strings.Repeat("d", 64)),
	}
	adapter := NewProductionLifecycleBoundaryMigrations(pool, nil)
	allowed, err := adapter.CanResumeLifecycleSource(ctx, request, LifecycleBoundaryMigrationUpgrade)
	if err != nil || !allowed {
		t.Fatalf("not-started source proof = %v, %v", allowed, err)
	}
	var status, observedStep string
	var observedAttempt int
	if err := pool.QueryRow(ctx, `
		SELECT status, last_observed_step_id, last_observed_attempt
		FROM extension_lifecycle_migration_proofs
		WHERE operation_id = $1
	`, request.OperationID).Scan(&status, &observedStep, &observedAttempt); err != nil {
		t.Fatal(err)
	}
	if status != lifecycleMigrationStatusNotStarted || observedStep != request.StepID || observedAttempt != request.Attempt {
		t.Fatalf("not-started proof = %q %q %d", status, observedStep, observedAttempt)
	}
}

type lifecycleMigrationIntegrationEngine struct {
	mu           sync.Mutex
	runCalls     int
	inspectCalls int
	runErr       error
	inspectErr   error
}

func (e *lifecycleMigrationIntegrationEngine) ReconcileLifecycleMigration(
	_ context.Context,
	_ LifecycleMigrationEnginePlan,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runCalls++
	return e.runErr
}

func (e *lifecycleMigrationIntegrationEngine) InspectLifecycleMigration(
	_ context.Context,
	plan LifecycleMigrationEnginePlan,
) (LifecycleMigrationEngineProof, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inspectCalls++
	if e.inspectErr != nil {
		return LifecycleMigrationEngineProof{}, e.inspectErr
	}
	return LifecycleMigrationEngineProof{
		ProofID:     "p5-proof:" + fmt.Sprint(plan.OperationID),
		ProofDigest: strings.Repeat("f", 64), PlanDigest: plan.PlanDigest,
		TargetReady: true, SourceResumeSafe: false,
	}, nil
}

func newLifecycleMigrationIntegration(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	position int,
) (context.Context, *pgxpool.Pool, LifecycleBoundaryRequest) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	request := lifecyclePublicationTestRequest(t, operation, position)
	unique := fmt.Sprintf("migration-proof-%d", time.Now().UnixNano())
	actorID, err := insertLifecycleActorFixture(ctx, pool, unique, "Lifecycle Migration Proof")
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	request.ActorUserID = actorID
	request.AuditEventID = time.Now().UnixNano()
	path, err := extensions.RecommendedLifecyclePath(operation)
	if err != nil || position < 0 || position >= len(path) {
		pool.Close()
		t.Fatalf("resolve lifecycle migration fixture state: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, current_step_id,
			plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, audit_event_id
		) VALUES ($1, $2, $3, '{}'::jsonb, $4, $5, $6,
		          'migration-proof@1', $7, $8,
		          'builtin', '{}'::jsonb, $9, $10)
		RETURNING id
	`, request.TargetExtension.ID, request.TargetExtension.Version,
		request.TargetExtension.PackageDigest, operation, path[position].State, request.StepID,
		unique, strings.Repeat("c", 64), actorID, request.AuditEventID).Scan(&request.OperationID)
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID)
		pool.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, actor_user_id, audit_event_id, started_at
		) VALUES ($1, $2, 'host.gate', 'migration-proof@1', $3,
		          'running', $4, $5, statement_timestamp())
	`, request.OperationID, request.StepID, request.Attempt, actorID, request.AuditEventID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM extension_lifecycle_operations WHERE id = $1`, request.OperationID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorID)
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_migration_proofs WHERE operation_id = $1`, request.OperationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, request.OperationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actorID)
		pool.Close()
	})
	return ctx, pool, request
}
