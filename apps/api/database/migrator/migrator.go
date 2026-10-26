package migrator

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

type Config struct {
	DatabaseURL string
	Logger      *slog.Logger
	// TargetCoreVersion pins the Core version this run migrates to. Leave it
	// empty to derive it from the build identity; that derivation also reports
	// whether the value is a real declaration or the untagged-development
	// fallback sentinel.
	TargetCoreVersion string
}

// coreCompatibilityDeclared resolves the build identity's compatibility version
// and its provenance. Tests replace it to simulate an untagged development
// checkout without depending on the git state of the working copy.
var coreCompatibilityDeclared = platformversion.CoreCompatibilityDeclared

// resolveCoreTargetVersion decides which Core version this run migrates to and
// whether that value is a real declaration.
//
// An undeclared target is the untagged-development fallback: it keeps the row
// parseable but says nothing about intent, so the compatibility and downgrade
// fences skip it instead of comparing it as a real (and therefore "older")
// version.
func resolveCoreTargetVersion(cfg Config) (string, bool) {
	if target := strings.TrimSpace(cfg.TargetCoreVersion); target != "" {
		// Naming a version is itself the declaration: a caller that writes
		// "1.0.0" means it, so "unknown" cannot apply and the downgrade fence
		// must still reject it.
		return target, true
	}
	return coreCompatibilityDeclared()
}

// warnUndeclaredCoreVersion explains a skipped version fence in one line. It is
// a warning rather than an error because an untagged development checkout is a
// legitimate state, but the operator still needs the actionable cause.
func warnUndeclaredCoreVersion(ctx context.Context, logger *slog.Logger, cfg Config, targetVersion string) {
	logger.WarnContext(ctx,
		"Core version is unknown for this build: git describe found no reachable v* tag, so the Core downgrade and raw-extension compatibility fences are skipped for this run",
		slog.String("derived_version", targetVersion),
		slog.Bool("caller_declared_target", strings.TrimSpace(cfg.TargetCoreVersion) != ""),
		slog.String("usual_cause", "shallow clone hiding the release tags"),
		slog.String("fix", "git fetch --unshallow, then confirm `git describe --tags --match 'v[0-9]*' --long --dirty --always` starts with v"),
	)
}

// IsUpToDate performs a read-only schema check for zero-downtime releases.
// Both Core and River migrations must exactly match the target binary; callers
// must fall back to the maintenance-window deploy path when this returns false.
func IsUpToDate(ctx context.Context, cfg Config) (bool, error) {
	db, err := openMigrationSQLDatabase(cfg.DatabaseURL, "")
	if err != nil {
		return false, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return false, fmt.Errorf("ping database: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return false, fmt.Errorf("create goose provider: %w", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect Core migration versions: %w", err)
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect pending Core migrations: %w", err)
	}
	if pending || current != target {
		return false, nil
	}

	return riverMigrationsExact(ctx, cfg)
}

func Up(ctx context.Context, cfg Config) error {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	targetVersion, targetDeclared := resolveCoreTargetVersion(cfg)
	if !targetDeclared {
		warnUndeclaredCoreVersion(ctx, logger, cfg, targetVersion)
	}

	adminDB, err := openMigrationSQLDatabase(cfg.DatabaseURL, "")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer adminDB.Close()

	// Goose 的 table lock 会持有连接并维护心跳；至少保留两个连接，避免未来 Go 迁移死锁。
	adminDB.SetMaxOpenConns(2)
	adminDB.SetMaxIdleConns(2)

	if err := adminDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return withCorePhysicalAuthoritySession(ctx, adminDB, func(adminConnection *sql.Conn) error {
		return runCoreMigrationsLocked(
			ctx, cfg, logger, adminDB, adminConnection, targetVersion, targetDeclared,
		)
	})
}

func runCoreMigrationsLocked(
	ctx context.Context,
	cfg Config,
	logger *slog.Logger,
	adminDB *sql.DB,
	adminConnection *sql.Conn,
	targetVersion string,
	targetDeclared bool,
) error {
	if err := ensureCoreDatabaseExtensions(ctx, adminConnection); err != nil {
		return fmt.Errorf("ensure Core database extensions: %w", err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, adminConnection, targetVersion, targetDeclared); err != nil {
		return fmt.Errorf("check core upgrade compatibility: %w", err)
	}
	// An undeclared target still reaches the kernel reconciliation: the
	// exactness validation of physical declarations does not depend on the
	// build identity, only the version comparison does.
	authority, err := prepareCoreMigrationAuthorityForVersion(
		ctx, adminConnection, targetVersion, targetDeclared,
	)
	if err != nil {
		return fmt.Errorf("prepare Core migration authority: %w", err)
	}
	db, err := openMigrationSQLDatabase(cfg.DatabaseURL, authority.OwnerRole)
	if err != nil {
		return fmt.Errorf("open Core-owner migration database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping Core-owner migration database: %w", err)
	}
	if err := validateCoreMigrationConnection(ctx, db, authority); err != nil {
		return err
	}

	locker, err := lock.NewPostgresTableLocker(lock.WithTableLogger(logger))
	if err != nil {
		return fmt.Errorf("create migration lock: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
		goose.WithLocker(locker),
		goose.WithSlog(logger),
	)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	runtimeStateExisted, runtimeStateMarked, err := markCoreMigrationStarted(
		ctx, adminConnection, targetVersion, false, targetDeclared,
	)
	if err != nil {
		return err
	}

	results, err := runWithCoreMigrationDatabaseCreate(ctx, adminDB, authority, provider.Up)
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if !runtimeStateExisted {
		_, runtimeStateMarked, err = markCoreMigrationStarted(
			ctx, adminConnection, targetVersion, true, targetDeclared,
		)
		if err != nil {
			return err
		}
	}
	if len(results) == 0 {
		logger.InfoContext(ctx, "database migrations already up to date")
	} else {
		for _, result := range results {
			logger.InfoContext(ctx, "database migration applied",
				slog.Int64("version", result.Source.Version),
				slog.String("path", result.Source.Path),
				slog.Duration("duration", result.Duration),
			)
		}
		logger.InfoContext(ctx, "database migrations complete", slog.Int("applied", len(results)))
	}
	if err := runRiverMigrations(ctx, cfg.DatabaseURL, logger); err != nil {
		return err
	}
	if !runtimeStateMarked {
		return nil
	}
	return publishCoreRuntimeVersion(ctx, adminConnection, targetVersion, targetDeclared)
}

func withCorePhysicalAuthoritySession(
	ctx context.Context,
	db *sql.DB,
	operation func(*sql.Conn) error,
) (returnErr error) {
	if ctx == nil || db == nil || operation == nil {
		return fmt.Errorf("%w: physical authority session inputs are required", ErrCoreAuthorityConflict)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire Core physical authority connection: %w", err)
	}
	discard := false
	defer func() {
		if discard {
			// ErrBadConn prevents database/sql from returning an uncertain lock owner to the idle pool.
			_ = connection.Raw(func(any) error { return driver.ErrBadConn })
		}
		returnErr = errors.Join(returnErr, connection.Close())
	}()
	if err := lockCorePhysicalAuthoritySession(ctx, connection); err != nil {
		discard = true
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := unlockCorePhysicalAuthoritySession(unlockCtx, connection); err != nil {
			discard = true
			returnErr = errors.Join(returnErr, err)
		}
	}()
	return operation(connection)
}

func runWithCoreMigrationDatabaseCreate(
	ctx context.Context,
	adminDB *sql.DB,
	authority coreMigrationAuthority,
	operation func(context.Context) ([]*goose.MigrationResult, error),
) ([]*goose.MigrationResult, error) {
	if err := configureCoreMigrationDatabaseCreate(ctx, adminDB, authority, true); err != nil {
		return nil, err
	}
	results, operationErr := operation(ctx)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cleanupErr := configureCoreMigrationDatabaseCreate(cleanupCtx, adminDB, authority, false)
	return results, errors.Join(operationErr, cleanupErr)
}

func openMigrationSQLDatabase(databaseURL string, roleName string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if roleName != "" {
		config.RuntimeParams["role"] = roleName
	}
	return stdlib.OpenDB(*config), nil
}

func runRiverMigrations(ctx context.Context, databaseURL string, logger *slog.Logger) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open river migration pool: %w", err)
	}
	defer pool.Close()

	// River 自带迁移维护队列表结构，避免项目手写第三方内部 schema。
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: logger})
	if err != nil {
		return fmt.Errorf("create river migrator: %w", err)
	}
	results, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("run river migrations: %w", err)
	}
	if len(results.Versions) == 0 {
		logger.InfoContext(ctx, "river migrations already up to date")
		return nil
	}
	logger.InfoContext(ctx, "river migrations complete", slog.Int("applied", len(results.Versions)))
	return nil
}
