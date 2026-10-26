package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/config"
	platformversion "github.com/keeptrue/sforum/apps/api/version"
)

// stubUndeclaredCoreVersion 把构建身份钉成「dev 且无可达 v* tag」：走的是真实的
// 生产闸门策略，只是不依赖当前工作树的 git 状态。
func stubUndeclaredCoreVersion(t *testing.T) {
	t.Helper()
	original := requireDeclaredCoreVersion
	requireDeclaredCoreVersion = func(appEnv string) error {
		return platformversion.RequireDeclaredCoreVersion(appEnv, "1.0.0", false)
	}
	t.Cleanup(func() { requireDeclaredCoreVersion = original })
}

// 端口 1 上没有 Postgres：能走到迁移器就一定报数据库错误，而不是版本闸门错误。
const unreachableDatabaseURL = "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"

func quietBootstrapLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// 生产 + 未声明：无论本进程是否自己跑迁移都必须拒绝——生产模板写
// MIGRATE_ON_STARTUP=false，闸门是启动闸门而不是迁移闸门。
func TestRunStartupMigrationsRefusesUndeclaredProductionBuild(t *testing.T) {
	stubUndeclaredCoreVersion(t)
	for _, migrateOnStartup := range []bool{true, false} {
		err := runStartupMigrations(context.Background(), config.Config{
			AppEnv:           "production",
			MigrateOnStartup: migrateOnStartup,
			DatabaseURL:      unreachableDatabaseURL,
		}, quietBootstrapLogger())
		if err == nil || !strings.Contains(err.Error(), "SFORUM_VERSION") {
			t.Fatalf("production gate (migrate_on_startup=%t) error = %v", migrateOnStartup, err)
		}
	}
}

// 同一个未声明构建在 development 下必须放行到迁移器。
func TestRunStartupMigrationsLetsUndeclaredDevelopmentReachTheMigrator(t *testing.T) {
	stubUndeclaredCoreVersion(t)
	err := runStartupMigrations(context.Background(), config.Config{
		AppEnv:           "development",
		MigrateOnStartup: true,
		DatabaseURL:      unreachableDatabaseURL,
	}, quietBootstrapLogger())
	if err == nil {
		t.Fatal("expected the unreachable database to fail the migration")
	}
	if strings.Contains(err.Error(), "SFORUM_VERSION") {
		t.Fatalf("development run was stopped by the production gate: %v", err)
	}
	if !strings.Contains(err.Error(), "database") {
		t.Fatalf("development run did not reach the migrator: %v", err)
	}
}
