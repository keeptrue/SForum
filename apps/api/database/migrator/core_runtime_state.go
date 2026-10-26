package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	semver "github.com/Masterminds/semver/v3"
	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

type coreRuntimeState struct {
	CurrentVersion string
	TargetVersion  string
	Status         string
}

func markCoreMigrationStarted(
	ctx context.Context,
	connection *sql.Conn,
	targetVersion string,
	required bool,
	declared bool,
) (bool, bool, error) {
	target, err := semver.StrictNewVersion(targetVersion)
	if err != nil {
		return false, false, fmt.Errorf("invalid target SForum version %q: %w", targetVersion, err)
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("begin Core runtime version fence: %w", err)
	}
	defer tx.Rollback()

	var tableExists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`,
		coreauthority.PublicSchema+"."+coreauthority.RuntimeStateTable,
	).Scan(&tableExists); err != nil {
		return false, false, fmt.Errorf("inspect Core runtime version table: %w", err)
	}
	if !tableExists {
		if required {
			return false, false, fmt.Errorf("%w: Core runtime version table is missing after migrations", ErrCoreAuthorityConflict)
		}
		return false, false, nil
	}

	var state coreRuntimeState
	err = tx.QueryRowContext(ctx, `
		SELECT current_version, target_version, status
		FROM public.`+pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize()+`
		WHERE singleton = TRUE
		FOR UPDATE
	`).Scan(&state.CurrentVersion, &state.TargetVersion, &state.Status)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO public.`+pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize()+` (
				singleton, current_version, target_version, status
			) VALUES (TRUE, '', $1, 'migrating')
		`, target.String()); err != nil {
			return false, false, fmt.Errorf("initialize Core runtime version fence: %w", err)
		}
	} else if err != nil {
		return false, false, fmt.Errorf("load Core runtime version fence: %w", err)
	} else {
		if err := validateCoreMigrationTarget(state, target, declared); err != nil {
			return false, false, err
		}
		if state.Status == "ready" && state.CurrentVersion == target.String() &&
			state.TargetVersion == target.String() {
			if err := tx.Commit(); err != nil {
				return false, false, fmt.Errorf("commit unchanged Core runtime version fence: %w", err)
			}
			return true, false, nil
		}
		if state.Status == "migrating" && state.TargetVersion == target.String() {
			if err := tx.Commit(); err != nil {
				return false, false, fmt.Errorf("commit existing Core runtime version fence: %w", err)
			}
			return true, true, nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE public.`+pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize()+`
			SET target_version = $1, status = 'migrating', migrated_at = NULL,
			    migration_started_at = statement_timestamp(),
			    revision = revision + 1, updated_at = statement_timestamp()
			WHERE singleton = TRUE
		`, target.String()); err != nil {
			return false, false, fmt.Errorf("mark Core runtime migration started: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("commit Core runtime version fence: %w", err)
	}
	return true, true, nil
}

// validateCoreMigrationTarget fences the runtime-state row against a Core
// downgrade.
//
// declared reports whether target is a real declaration. An undeclared target
// is the untagged-development fallback, and "unknown" must never be read as
// "lower": skipping the two ordering comparisons is what stops a shallow clone
// from looking like a downgrade request. The structural checks (status, stored
// versions) describe the row rather than the build identity, so they apply in
// both cases.
func validateCoreMigrationTarget(state coreRuntimeState, target *semver.Version, declared bool) error {
	if state.Status != "ready" && state.Status != "migrating" {
		return fmt.Errorf("%w: Core runtime version state is invalid", ErrCoreAuthorityConflict)
	}
	if state.CurrentVersion != "" {
		current, err := semver.StrictNewVersion(state.CurrentVersion)
		if err != nil {
			return fmt.Errorf("%w: stored Core runtime version is invalid", ErrCoreAuthorityConflict)
		}
		if declared && target.LessThan(current) {
			return fmt.Errorf(
				"%w: target=%s is older than migrated Core=%s",
				ErrCoreUpgradeIncompatible, target, current,
			)
		}
	}
	if state.Status == "migrating" && state.TargetVersion != "" {
		pending, err := semver.StrictNewVersion(state.TargetVersion)
		if err != nil {
			return fmt.Errorf("%w: pending Core runtime version is invalid", ErrCoreAuthorityConflict)
		}
		if declared && target.LessThan(pending) {
			return fmt.Errorf(
				"%w: target=%s is older than pending Core migration=%s",
				ErrCoreUpgradeIncompatible, target, pending,
			)
		}
	}
	return nil
}

// publishCoreRuntimeVersion closes the migration fence.
//
// declared reports whether targetVersion is a real declaration. An undeclared
// run must not rewrite current_version: the sentinel would claim the schema is
// older than it really is and weaken the next downgrade fence. It converges
// target_version onto the stored current_version instead ("this run expresses
// no version opinion") and only writes the target when the row has no version
// yet, which is the fresh-database case.
func publishCoreRuntimeVersion(
	ctx context.Context,
	connection *sql.Conn,
	targetVersion string,
	declared bool,
) error {
	target, err := semver.StrictNewVersion(targetVersion)
	if err != nil {
		return fmt.Errorf("invalid target SForum version %q: %w", targetVersion, err)
	}
	statement := `
		UPDATE public.` + pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize() + `
		SET current_version = $1, target_version = $1, status = 'ready',
		    migrated_at = statement_timestamp(), revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE singleton = TRUE AND status = 'migrating' AND target_version = $1
	`
	if !declared {
		// SET expressions read the pre-update row, so both CASE branches see the
		// stored current_version and keep it untouched.
		statement = `
		UPDATE public.` + pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize() + `
		SET current_version = CASE WHEN current_version = '' THEN $1 ELSE current_version END,
		    target_version = CASE WHEN current_version = '' THEN $1 ELSE current_version END,
		    status = 'ready', migrated_at = statement_timestamp(),
		    revision = revision + 1, updated_at = statement_timestamp()
		WHERE singleton = TRUE AND status = 'migrating' AND target_version = $1
	`
	}
	result, err := connection.ExecContext(ctx, statement, target.String())
	if err != nil {
		return fmt.Errorf("publish Core runtime version: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect Core runtime version publication: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: Core runtime version publication lost its fence", ErrCoreAuthorityConflict)
	}
	return nil
}
