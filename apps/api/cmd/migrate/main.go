package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
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
	// 形态检查是**离线**的：它只读嵌入式迁移文件，所以必须排在配置加载与
	// 生产版本门之前——没有数据库、没有 DATABASE_URL 的环境同样要能判断形态。
	if hasArgument(os.Args[1:], "--check-online-safe-form") {
		os.Exit(runOnlineSafeFormCheck(os.Stdout, migrations.Files()))
	}
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	// The separate migrate binary is the production entry point (the deploy
	// templates write MIGRATE_ON_STARTUP=false), so the same fail-closed gate
	// applies here: a build that cannot name its Core version would skip both
	// version fences instead of reporting a downgrade.
	if err := platformversion.RequireDeclaredForProduction(cfg.AppEnv); err != nil {
		logger.Error("migrations refused: undeclared Core version in production", "error", err)
		os.Exit(1)
	}
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

// runOnlineSafeFormCheck 报告全部迁移的在线安全声明形态，返回进程退出码。
//
// 退出码语义：0 = 没有畸形声明（未声明是合法的，只报告）；3 = 存在畸形声明
// （含 `NO TRANSACTION` 或缺少 `SET LOCAL` 超时）；1 = 读不到迁移文件。
func runOnlineSafeFormCheck(stdout io.Writer, fsys fs.FS) int {
	report, err := migrator.CheckOnlineSafeForm(fsys)
	if err != nil {
		fmt.Fprintf(stdout, "online-safe-form check failed: %v\n", err)
		return 1
	}
	for _, declaration := range report.Malformed() {
		fmt.Fprintf(stdout, "malformed %s: %s\n", declaration.Path, declaration.Reason)
	}
	declared, undeclared, malformed := report.Counts()
	fmt.Fprintf(
		stdout,
		"online-safe-form scanned=%d declared=%d undeclared=%d malformed=%d\n",
		len(report.Declarations), declared, undeclared, malformed,
	)
	if malformed > 0 {
		return 3
	}
	return 0
}

func validateArguments(arguments []string) error {
	if len(arguments) == 0 {
		return nil
	}
	if len(arguments) == 1 && (arguments[0] == "--check-no-pending" ||
		arguments[0] == "--check-online-safe" ||
		arguments[0] == "--check-online-safe-form") {
		return nil
	}
	return fmt.Errorf("unsupported migrator arguments: %v", arguments)
}
