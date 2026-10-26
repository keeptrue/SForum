package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
)

func runStartupMigrations(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if !cfg.MigrateOnStartup {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	logger.InfoContext(ctx, "running startup database migrations")
	if err := migrator.Up(ctx, migrator.Config{
		DatabaseURL: cfg.DatabaseURL,
		Logger:      logger.With("component", "database.migrator"),
	}); err != nil {
		return fmt.Errorf("startup database migrations failed: %w", err)
	}
	return nil
}
