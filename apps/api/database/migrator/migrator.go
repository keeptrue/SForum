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
	DatabaseURL       string
	Logger            *slog.Logger
	TargetCoreVersion string
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
	targetVersion := strings.TrimSpace(cfg.TargetCoreVersion)
	if targetVersion == "" {
		targetVersion = platformversion.CoreCompatibilityVersion()
	}
	return withCorePhysicalAuthoritySession(ctx, adminDB, func(adminConnection *sql.Conn) error {
		return runCoreMigrationsLocked(ctx, cfg, logger, adminDB, adminConnection, targetVersion)
	})
}

func runCoreMigrationsLocked(
	ctx context.Context,
	cfg Config,
	logger *slog.Logger,
	adminDB *sql.DB,
	adminConnection *sql.Conn,
	targetVersion string,
) error {
	if err := ensureCoreDatabaseExtensions(ctx, adminConnection); err != nil {
		return fmt.Errorf("ensure Core database extensions: %w", err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, adminConnection, targetVersion); err != nil {
		return fmt.Errorf("check core upgrade compatibility: %w", err)
	}
	authority, err := prepareCoreMigrationAuthorityForVersion(ctx, adminConnection, targetVersion)
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
		ctx, adminConnection, targetVersion, false,
	)
	if err != nil {
		return err
	}

	results, err := runWithCoreMigrationDatabaseCreate(ctx, adminDB, authority, provider.Up)
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if !runtimeStateExisted {
		_, runtimeStateMarked, err = markCoreMigrationStarted(ctx, adminConnection, targetVersion, true)
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
	return publishCoreRuntimeVersion(ctx, adminConnection, targetVersion)
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
