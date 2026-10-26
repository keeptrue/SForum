package migrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// 现场复刻 2026-10-27 事故：库里记录的是带 tag 的更高版本，而当前构建
// （shallow clone）派生不出 v* tag。未声明 target 必须按「不知道」处理，
// 显式钉住同一个 1.0.0 则必须继续被当成真实降级拒绝。
func TestUpTreatsUndeclaredTargetAsUnknownInsteadOfDowngrade(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := runCoreKernelTestUp(ctx, fixture, "3.0.18"); err != nil {
		t.Fatalf("prepare schema at 3.0.18: %v", err)
	}
	db := openCoreAuthorityFixtureDB(t, fixture)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		UPDATE public.sforum_core_runtime_state
		SET current_version = $1, target_version = $1, status = 'ready'
		WHERE singleton = TRUE
	`, "3.0.18+4.ga4e91267b.dirty"); err != nil {
		t.Fatalf("replay the migrated Core state: %v", err)
	}

	// 显式钉住 1.0.0 是真实降级意图：护栏照旧拒绝。
	if err := runCoreKernelTestUp(ctx, fixture, "1.0.0"); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("pinned downgrade error = %v, want ErrCoreUpgradeIncompatible", err)
	}

	// 未声明（探针为空）的 1.0.0 只是「不知道我是谁」，不是降级意图。
	original := coreCompatibilityDeclared
	coreCompatibilityDeclared = func() (string, bool) { return "1.0.0", false }
	t.Cleanup(func() { coreCompatibilityDeclared = original })

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := Up(ctx, Config{DatabaseURL: fixture.databaseURL, Logger: logger}); err != nil {
		t.Fatalf("undeclared target was rejected as a downgrade: %v", err)
	}
	if !strings.Contains(logs.String(), "Core version is unknown for this build") {
		t.Fatalf("undeclared run did not warn about the skipped fences: %s", logs.String())
	}
	// 诊断字段由 TargetCoreVersion 就地算出，Config 上没有第二个「是否声明」开关。
	if !strings.Contains(logs.String(), "caller_declared_target=false") {
		t.Fatalf("warning did not report the pinned-target provenance: %s", logs.String())
	}

	var status, currentVersion, targetVersion string
	if err := db.QueryRowContext(ctx, `
		SELECT status, current_version, target_version
		FROM public.sforum_core_runtime_state WHERE singleton = TRUE
	`).Scan(&status, &currentVersion, &targetVersion); err != nil {
		t.Fatalf("read Core runtime state after the undeclared run: %v", err)
	}
	if status != "ready" {
		t.Fatalf("Core runtime state status after the undeclared run = %q, want ready", status)
	}
	// 未声明运行不得把哨兵写进 current_version：那会让库记录低于真实 schema，
	// 削弱下一次降级围栏。target_version 收敛回 current_version（本轮不表达版本意见）。
	if currentVersion != "3.0.18+4.ga4e91267b.dirty" {
		t.Fatalf("undeclared run rewrote current_version to %q", currentVersion)
	}
	if targetVersion != currentVersion {
		t.Fatalf("undeclared run left target_version=%q, want it converged to current_version=%q",
			targetVersion, currentVersion)
	}
}

// 全新库：运行时状态表还没有行，未声明运行没有更高记录可保，按 F3 的例外写入哨兵。
func TestUpUndeclaredTargetOnFreshDatabasePublishesSentinel(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	original := coreCompatibilityDeclared
	coreCompatibilityDeclared = func() (string, bool) { return "1.0.0", false }
	t.Cleanup(func() { coreCompatibilityDeclared = original })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := Up(ctx, Config{DatabaseURL: fixture.databaseURL, Logger: logger}); err != nil {
		t.Fatalf("undeclared run on a fresh database failed: %v", err)
	}

	db := openCoreAuthorityFixtureDB(t, fixture)
	t.Cleanup(func() { _ = db.Close() })
	var status, currentVersion, targetVersion string
	if err := db.QueryRowContext(ctx, `
		SELECT status, current_version, target_version
		FROM public.sforum_core_runtime_state WHERE singleton = TRUE
	`).Scan(&status, &currentVersion, &targetVersion); err != nil {
		t.Fatalf("read Core runtime state after the fresh undeclared run: %v", err)
	}
	if status != "ready" || currentVersion != "1.0.0" || targetVersion != "1.0.0" {
		t.Fatalf(
			"fresh undeclared run published status=%q current=%q target=%q, want ready/1.0.0/1.0.0",
			status, currentVersion, targetVersion,
		)
	}
}
