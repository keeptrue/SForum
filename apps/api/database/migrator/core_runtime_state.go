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
		if err := validateCoreMigrationTarget(state, target); err != nil {
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

func validateCoreMigrationTarget(state coreRuntimeState, target *semver.Version) error {
	if state.Status != "ready" && state.Status != "migrating" {
		return fmt.Errorf("%w: Core runtime version state is invalid", ErrCoreAuthorityConflict)
	}
	if state.CurrentVersion != "" {
		current, err := semver.StrictNewVersion(state.CurrentVersion)
		if err != nil {
			return fmt.Errorf("%w: stored Core runtime version is invalid", ErrCoreAuthorityConflict)
		}
		if target.LessThan(current) {
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
		if target.LessThan(pending) {
			return fmt.Errorf(
				"%w: target=%s is older than pending Core migration=%s",
				ErrCoreUpgradeIncompatible, target, pending,
			)
		}
	}
	return nil
}

func publishCoreRuntimeVersion(ctx context.Context, connection *sql.Conn, targetVersion string) error {
	target, err := semver.StrictNewVersion(targetVersion)
	if err != nil {
		return fmt.Errorf("invalid target SForum version %q: %w", targetVersion, err)
	}
	result, err := connection.ExecContext(ctx, `
		UPDATE public.`+pgx.Identifier{coreauthority.RuntimeStateTable}.Sanitize()+`
		SET current_version = $1, target_version = $1, status = 'ready',
		    migrated_at = statement_timestamp(), revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE singleton = TRUE AND status = 'migrating' AND target_version = $1
	`, target.String())
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
