package migrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

const onlineSafeDirective = "-- +sforum OnlineSafe"

var ErrOnlineMigrationUnsafe = errors.New("pending migrations are not safe for an online update")

type OnlineMigrationPlan struct {
	PendingCore []string
}

func (plan OnlineMigrationPlan) RequiresMigration() bool {
	return len(plan.PendingCore) > 0
}

// CheckOnlineSafe permits only explicitly declared, backward-compatible Core
// migrations. River must already match because its internal migrations do not
// carry SForum's old/new binary compatibility declaration.
func CheckOnlineSafe(ctx context.Context, cfg Config) (OnlineMigrationPlan, error) {
	var plan OnlineMigrationPlan
	db, err := openMigrationSQLDatabase(cfg.DatabaseURL, "")
	if err != nil {
		return plan, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return plan, fmt.Errorf("ping database: %w", err)
	}

	targetVersion := strings.TrimSpace(cfg.TargetCoreVersion)
	if targetVersion == "" {
		targetVersion = platformversion.CoreCompatibilityVersion()
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, targetVersion); err != nil {
		return plan, fmt.Errorf("check core upgrade compatibility: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return plan, fmt.Errorf("create goose provider: %w", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return plan, fmt.Errorf("inspect Core migration versions: %w", err)
	}
	hasPending, err := provider.HasPending(ctx)
	if err != nil {
		return plan, fmt.Errorf("inspect pending Core migrations: %w", err)
	}
	if !hasPending && current != target {
		return plan, fmt.Errorf(
			"%w: Core migration version mismatch current=%d target=%d",
			ErrOnlineMigrationUnsafe,
			current,
			target,
		)
	}
	if hasPending {
		statuses, statusErr := provider.Status(ctx)
		if statusErr != nil {
			return plan, fmt.Errorf("inspect Core migration status: %w", statusErr)
		}
		plan.PendingCore, err = onlineSafePendingMigrations(migrations.Files(), statuses)
		if err != nil {
			return OnlineMigrationPlan{}, err
		}
	}

	riverExact, err := riverMigrationsExact(ctx, cfg)
	if err != nil {
		return OnlineMigrationPlan{}, err
	}
	if !riverExact {
		return OnlineMigrationPlan{}, fmt.Errorf(
			"%w: River migration versions differ from the live database",
			ErrOnlineMigrationUnsafe,
		)
	}
	return plan, nil
}

func onlineSafePendingMigrations(fsys fs.FS, statuses []*goose.MigrationStatus) ([]string, error) {
	pending := make([]string, 0)
	for _, status := range statuses {
		if status == nil || status.State != goose.StatePending {
			continue
		}
		if status.Source == nil || status.Source.Type != goose.TypeSQL || strings.TrimSpace(status.Source.Path) == "" {
			return nil, fmt.Errorf("%w: pending migration has no declarative SQL source", ErrOnlineMigrationUnsafe)
		}
		declared, err := migrationDeclaresOnlineSafe(fsys, status.Source.Path)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: pending migration %s has an invalid online declaration: %v",
				ErrOnlineMigrationUnsafe,
				status.Source.Path,
				err,
			)
		}
		if !declared {
			return nil, fmt.Errorf(
				"%w: %s does not declare %s",
				ErrOnlineMigrationUnsafe,
				status.Source.Path,
				onlineSafeDirective,
			)
		}
		pending = append(pending, status.Source.Path)
	}
	if len(pending) == 0 {
		return nil, fmt.Errorf("%w: Core reports pending migrations without a matching source", ErrOnlineMigrationUnsafe)
	}
	return pending, nil
}

func migrationDeclaresOnlineSafe(fsys fs.FS, path string) (bool, error) {
	body, err := fs.ReadFile(fsys, path)
	if err != nil {
		return false, err
	}
	upLines := make([]string, 0)
	declared := false
	for _, line := range strings.Split(string(body), "\n") {
		directive := strings.TrimSpace(line)
		if directive == "-- +goose Down" {
			break
		}
		upLines = append(upLines, line)
		if directive == onlineSafeDirective {
			declared = true
		}
	}
	if !declared {
		return false, nil
	}
	upBody := strings.Join(upLines, "\n")
	if strings.Contains(upBody, "-- +goose NO TRANSACTION") {
		return false, errors.New("online migrations must run transactionally")
	}
	for _, required := range []string{"SET LOCAL lock_timeout", "SET LOCAL statement_timeout"} {
		if !strings.Contains(upBody, required) {
			return false, fmt.Errorf("missing %s", required)
		}
	}
	return true, nil
}

func riverMigrationsExact(ctx context.Context, cfg Config) (bool, error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return false, fmt.Errorf("open river migration pool: %w", err)
	}
	defer pool.Close()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: cfg.Logger})
	if err != nil {
		return false, fmt.Errorf("create river migrator: %w", err)
	}
	existing, err := migrator.ExistingVersions(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect River migration versions: %w", err)
	}
	all := migrator.AllVersions()
	if len(existing) != len(all) {
		return false, nil
	}
	for index := range all {
		if existing[index].Version != all[index].Version {
			return false, nil
		}
	}
	return true, nil
}
