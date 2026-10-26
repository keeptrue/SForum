package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// ProductionLifecycleBoundaryMigrations is deliberately fail-closed until P5
// installs a real engine. It never reads extension_migration_ledger because
// that v1 table did not execute SQL.
type ProductionLifecycleBoundaryMigrations struct {
	pool   *pgxpool.Pool
	engine LifecycleMigrationEngine
}

func NewProductionLifecycleBoundaryMigrations(
	pool *pgxpool.Pool,
	engine LifecycleMigrationEngine,
) *ProductionLifecycleBoundaryMigrations {
	return &ProductionLifecycleBoundaryMigrations{pool: pool, engine: engine}
}

func (m *ProductionLifecycleBoundaryMigrations) ReconcileLifecycleMigrations(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryMigrationMode,
) error {
	if m == nil || m.pool == nil || ctx == nil {
		return ErrLifecycleMigrationsInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := lifecycleMigrationPlanFor(request, mode, true)
	if err != nil {
		return err
	}
	callFence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return err
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin lifecycle migration decision: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, callFence, true); err != nil {
		return err
	}
	record, err := ensureLifecycleMigrationProof(ctx, tx, plan)
	if err != nil {
		return err
	}
	if plan.Attempt < record.LastAttempt {
		return ErrLifecycleMigrationsConflict
	}
	if record.TargetReady {
		return commitLifecycleMigrationDecision(ctx, tx, "existing target-ready proof")
	}

	hasDeclarations := len(plan.Target.Migrations) > 0 || (plan.Source != nil && len(plan.Source.Migrations) > 0)
	if !hasDeclarations {
		proof, proofErr := hostNoopLifecycleMigrationProof(plan)
		if proofErr != nil {
			return proofErr
		}
		if err := writeLifecycleMigrationProof(ctx, tx, record, plan.Attempt, proof); err != nil {
			return err
		}
		return commitLifecycleMigrationDecision(ctx, tx, "host no-op proof")
	}

	// Equal non-empty declarations are only a no-op after the source artifact
	// itself has a durable V3 target-ready proof. A v1 checksum row is ignored.
	if plan.Source != nil && plan.Source.MigrationsDigest == plan.Target.MigrationsDigest {
		if artifactProof, ok, err := findLifecycleMigrationArtifactProof(ctx, tx, *plan.Source, record.ID); err != nil {
			return err
		} else if ok {
			proof := reusedLifecycleMigrationProof(plan, artifactProof)
			if err := writeLifecycleMigrationProof(ctx, tx, record, plan.Attempt, proof); err != nil {
				return err
			}
			return commitLifecycleMigrationDecision(ctx, tx, "reused exact source proof")
		}
	}

	if m.engine == nil {
		// If a previous process crossed into engine execution, absence of the
		// engine cannot prove that SQL was untouched. Preserve the unsafe marker.
		sourceSafe := record.SourceResumeSafe
		if record.Status == lifecycleMigrationStatusNotStarted {
			sourceSafe = true
		}
		if err := blockLifecycleMigrationProof(ctx, tx, record, plan.Attempt, sourceSafe); err != nil {
			return err
		}
		if err := commitLifecycleMigrationDecision(ctx, tx, "missing P5 engine block"); err != nil {
			return err
		}
		return lifecycleMigrationBlockedError{
			reason: "lifecycle.migration_engine_unavailable",
			detail: "declared SQL migrations require the P5 exact-plan engine",
		}
	}

	if err := startLifecycleMigrationExecution(ctx, tx, record, plan.Attempt); err != nil {
		return err
	}
	if err := commitLifecycleMigrationDecision(ctx, tx, "migration execution fence"); err != nil {
		return err
	}

	enginePlan := plan.enginePlan()
	runErr := m.engine.ReconcileLifecycleMigration(ctx, enginePlan)
	inspectionCtx := ctx
	var cancel context.CancelFunc
	if runErr != nil || ctx.Err() != nil {
		inspectionCtx, cancel = lifecycleBoundaryCompensationContext(ctx)
		defer cancel()
	}
	proof, inspectErr := m.engine.InspectLifecycleMigration(inspectionCtx, enginePlan)
	if inspectErr != nil {
		if runErr != nil {
			return errors.Join(runErr, fmt.Errorf("inspect durable P5 migration proof: %w", inspectErr))
		}
		return fmt.Errorf("inspect durable P5 migration proof: %w", inspectErr)
	}
	if err := validateLifecycleMigrationEngineProof(plan, proof); err != nil {
		return err
	}
	if err := m.persistEngineProof(inspectionCtx, request, plan, proof); err != nil {
		return err
	}
	if proof.TargetReady {
		return nil
	}
	blocked := lifecycleMigrationBlockedError{
		reason: "lifecycle.migration_target_not_ready",
		detail: "P5 has not proven the exact target migration plan ready",
	}
	if runErr != nil {
		return errors.Join(runErr, blocked)
	}
	return blocked
}

func (m *ProductionLifecycleBoundaryMigrations) CanResumeLifecycleSource(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryMigrationMode,
) (bool, error) {
	if m == nil || m.pool == nil || ctx == nil {
		return false, ErrLifecycleMigrationsInvalid
	}
	if mode != LifecycleBoundaryMigrationUpgrade && mode != LifecycleBoundaryMigrationRollback {
		return false, ErrLifecycleMigrationsInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	plan, err := lifecycleMigrationPlanFor(request, mode, false)
	if err != nil {
		return false, err
	}
	callFence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return false, err
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin lifecycle source-resume proof: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, callFence, true); err != nil {
		return false, err
	}
	record, err := ensureLifecycleMigrationProof(ctx, tx, plan)
	if err != nil {
		return false, err
	}
	if record.LastObservedStepID == request.StepID && request.Attempt < record.LastObservedAttempt {
		return false, ErrLifecycleMigrationsConflict
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extension_lifecycle_migration_proofs
		SET last_observed_step_id = $2,
		    last_observed_attempt = $3,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND revision = $4
	`, record.ID, request.StepID, request.Attempt, record.Revision)
	if err != nil {
		return false, fmt.Errorf("record lifecycle source-resume observation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, ErrLifecycleMigrationsConflict
	}
	if err := commitLifecycleMigrationDecision(ctx, tx, "source-resume observation"); err != nil {
		return false, err
	}
	return record.SourceResumeSafe, nil
}

func (m *ProductionLifecycleBoundaryMigrations) LifecycleArtifactMigrationReady(
	ctx context.Context,
	artifact extensions.Extension,
) (bool, error) {
	if m == nil || m.pool == nil || ctx == nil {
		return false, ErrLifecycleMigrationsInvalid
	}
	if err := validateExactCoordinatorArtifact("migration readiness", artifact); err != nil ||
		artifact.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(artifact.PackageDigest) {
		return false, ErrLifecycleMigrationsInvalid
	}
	declarations, digest, err := lifecycleMigrationDeclarations(artifact.Manifest.Migrations)
	if err != nil {
		return false, err
	}
	if len(declarations) == 0 {
		return true, nil
	}
	var ready bool
	err = m.pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM extension_lifecycle_migration_proofs
		  WHERE target_extension_id = $1
		    AND target_extension_version = $2
		    AND target_package_digest = $3
		    AND target_version_id = $4
		    AND target_migrations_digest = $5
		    AND status = 'target_ready'
		    AND target_ready = TRUE
		    AND proof_kind IS NOT NULL
		    AND proof_id IS NOT NULL
		    AND proof_digest ~ '^[0-9a-f]{64}$'
		)
	`, artifact.ID, artifact.Version, artifact.PackageDigest, artifact.ActiveVersionID, digest).Scan(&ready)
	if err != nil {
		return false, fmt.Errorf("inspect exact artifact migration readiness: %w", err)
	}
	return ready, nil
}

// CanPrepareLifecycleMigrations is safe to call before coordinator.Run. It
// never invokes the engine. Declared SQL can proceed only when the exact target
// already has a durable V3 proof or a real P5 engine is configured for the
// later fenced boundary step.
func (m *ProductionLifecycleBoundaryMigrations) CanPrepareLifecycleMigrations(
	ctx context.Context,
	request LifecycleStaticPreflightRequest,
) (bool, error) {
	if m == nil || m.pool == nil || ctx == nil {
		return false, ErrLifecycleMigrationsInvalid
	}
	candidate, _, err := lifecyclePreflightArtifactFor(
		request.Operation, request.SourceExtension, request.TargetExtension,
	)
	if err != nil {
		return false, err
	}
	if request.Operation != extensions.LifecycleMachineInstall &&
		request.Operation != extensions.LifecycleMachineUpgrade &&
		request.Operation != extensions.LifecycleMachineRollback {
		return false, ErrLifecycleMigrationsInvalid
	}
	targetDeclarations, _, err := lifecycleMigrationDeclarations(candidate.Manifest.Migrations)
	if err != nil {
		return false, err
	}
	hasDeclarations := len(targetDeclarations) > 0
	if request.SourceExtension != nil {
		sourceDeclarations, _, declarationErr := lifecycleMigrationDeclarations(request.SourceExtension.Manifest.Migrations)
		if declarationErr != nil {
			return false, declarationErr
		}
		hasDeclarations = hasDeclarations || len(sourceDeclarations) > 0
	}
	if !hasDeclarations {
		return true, nil
	}
	if m.engine != nil {
		return true, nil
	}
	// A historical target proof does not describe the database's current
	// state after another artifact may have migrated it. Without P5, the only
	// reusable transition is an unchanged declaration set whose current source
	// artifact itself has an exact durable proof.
	if request.SourceExtension == nil {
		return false, nil
	}
	_, targetDigest, err := lifecycleMigrationDeclarations(candidate.Manifest.Migrations)
	if err != nil {
		return false, err
	}
	_, sourceDigest, err := lifecycleMigrationDeclarations(request.SourceExtension.Manifest.Migrations)
	if err != nil {
		return false, err
	}
	if sourceDigest != targetDigest {
		return false, nil
	}
	return m.LifecycleArtifactMigrationReady(ctx, *request.SourceExtension)
}

func (m *ProductionLifecycleBoundaryMigrations) persistEngineProof(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	plan lifecycleMigrationPlan,
	proof LifecycleMigrationEngineProof,
) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin P5 migration proof persistence: %w", err)
	}
	defer tx.Rollback(ctx)
	callFence, err := lifecycleBoundaryCallFenceFor(request)
	if err != nil {
		return err
	}
	if err := validateLifecycleBoundaryPostgresFence(ctx, tx, callFence, true); err != nil {
		return err
	}
	record, err := loadLifecycleMigrationProof(ctx, tx, plan.OperationID, plan.Mode, true)
	if err != nil {
		return mapLifecycleMigrationProofError("load executing migration proof", err)
	}
	if !record.matches(plan) || plan.Attempt < record.LastAttempt {
		return ErrLifecycleMigrationsConflict
	}
	durable := lifecycleMigrationDurableProof{
		Kind: lifecycleMigrationProofP5, ID: proof.ProofID, Digest: proof.ProofDigest,
		TargetReady: proof.TargetReady, SourceResumeSafe: proof.SourceResumeSafe,
	}
	if err := writeLifecycleMigrationProof(ctx, tx, record, plan.Attempt, durable); err != nil {
		return err
	}
	return commitLifecycleMigrationDecision(ctx, tx, "P5 migration proof")
}

func commitLifecycleMigrationDecision(ctx context.Context, tx pgx.Tx, label string) error {
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", label, err)
	}
	return nil
}

var _ LifecycleBoundaryMigrations = (*ProductionLifecycleBoundaryMigrations)(nil)
var _ LifecycleBoundaryMigrationFacts = (*ProductionLifecycleBoundaryMigrations)(nil)
