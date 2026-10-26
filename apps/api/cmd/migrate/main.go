package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

func main() {
	if platformversion.PrintIfRequested(os.Stdout, os.Args[1:]) {
		return
	}
	if err := validateArguments(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	if hasArgument(os.Args[1:], "--check-no-pending") {
		upToDate, err := migrator.IsUpToDate(context.Background(), migrator.Config{
			DatabaseURL: cfg.DatabaseURL,
			Logger:      logger.With("component", "database.migrator"),
		})
		if err != nil {
			logger.Error("migration compatibility check failed", "error", err)
			os.Exit(1)
		}
		if !upToDate {
			logger.Error("zero-downtime update refused: target has pending or mismatched migrations")
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, "schema-up-to-date")
		return
	}
	if hasArgument(os.Args[1:], "--check-online-safe") {
		plan, err := migrator.CheckOnlineSafe(context.Background(), migrator.Config{
			DatabaseURL: cfg.DatabaseURL,
			Logger:      logger.With("component", "database.migrator"),
		})
		if err != nil {
			if errors.Is(err, migrator.ErrOnlineMigrationUnsafe) {
				logger.Error("zero-downtime update refused: pending migrations are not online-safe", "error", err)
				os.Exit(3)
			}
			logger.Error("online migration compatibility check failed", "error", err)
			os.Exit(1)
		}
		if !plan.RequiresMigration() {
			fmt.Fprintln(os.Stdout, "online-migration-not-required")
			return
		}
		fmt.Fprintf(os.Stdout, "online-migration-safe pending=%d\n", len(plan.PendingCore))
		return
	}

	if err := migrator.Up(context.Background(), migrator.Config{
		DatabaseURL: cfg.DatabaseURL,
		Logger:      logger.With("component", "database.migrator"),
	}); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}
}

func hasArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func validateArguments(arguments []string) error {
	if len(arguments) == 0 {
		return nil
	}
	if len(arguments) == 1 && (arguments[0] == "--check-no-pending" || arguments[0] == "--check-online-safe") {
		return nil
	}
	return fmt.Errorf("unsupported migrator arguments: %v", arguments)
}
