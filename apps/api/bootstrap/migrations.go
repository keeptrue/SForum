package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

// requireDeclaredCoreVersion is the production fail-closed gate. Tests replace
// it to exercise the wiring without depending on the git state of the checkout;
// the policy itself is covered in the version package.
var requireDeclaredCoreVersion = platformversion.RequireDeclaredForProduction

func runStartupMigrations(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	// Startup gate, not a migration gate: production writes
	// MIGRATE_ON_STARTUP=false and migrates from a separate process, so this
	// must run even when the current process does not migrate. A production
	// build that cannot name its Core version would silently skip both version
	// fences, and a fence is fail-closed on the same footing as safe mode.
	if err := requireDeclaredCoreVersion(cfg.AppEnv); err != nil {
		return err
	}
	if !cfg.MigrateOnStartup {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	logger.InfoContext(ctx, "running startup database migrations")
	// TargetCoreVersion stays empty on purpose: the migrator resolves it from
	// the build identity, which is the single version authority and also knows
	// whether the value is a real declaration or the untagged-development
	// fallback. Pinning it here would create a second authority.
	if err := migrator.Up(ctx, migrator.Config{
		DatabaseURL: cfg.DatabaseURL,
		Logger:      logger.With("component", "database.migrator"),
	}); err != nil {
		return fmt.Errorf("startup database migrations failed: %w", err)
	}
	return nil
}
