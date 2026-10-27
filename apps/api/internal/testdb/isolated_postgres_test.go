package testdb_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	"github.com/keeptrue/sforum/apps/api/internal/testdb/guard"
)

const (
	denyHelperEnv           = "SFORUM_TESTDB_DENY_HELPER"
	requireMissingHelperEnv = "SFORUM_TESTDB_REQUIRE_MISSING_HELPER"
)

// TestInspectReportsSentinelOnThrowawayDatabase 在一次性集群里新建一个临时库，
// 证明 EnsureSentinel 写入的哨兵确实会被 Inspect 读到，并且重复写入幂等。
// 它必须用临时库：哨兵一旦落在共享测试库的 public 上，整轮数据库测试都会拒绝连接。
func TestInspectReportsSentinelOnThrowawayDatabase(t *testing.T) {
	base := testdb.IsolatedURL(t)
	name := fmt.Sprintf("sforum_test_testdb_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin := openDatabase(t, ctx, base)
	requireDisposableCluster(t, ctx, admin)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create throwaway database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop throwaway database %s: %v", name, err)
		}
		admin.Close()
	})

	target := databaseURLWithName(t, base, name)
	db := openDatabase(t, ctx, target)
	t.Cleanup(func() { db.Close() })

	before, err := guard.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("inspect throwaway database: %v", err)
	}
	if before.Sentinel {
		t.Fatalf("fresh database already reports %s", guard.SentinelTable)
	}
	if !before.Disposable {
		t.Fatalf("throwaway database must belong to a disposable cluster, got %#v", before)
	}

	if err := guard.EnsureSentinel(ctx, db); err != nil {
		t.Fatalf("ensure sentinel: %v", err)
	}
	after, err := guard.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("inspect guarded database: %v", err)
	}
	if !after.Sentinel {
		t.Fatalf("sentinel %s was not detected after EnsureSentinel", guard.SentinelTable)
	}
	// 幂等：开发启动脚本每次都会执行，不能因为哨兵已存在而失败。
	if err := guard.EnsureSentinel(ctx, db); err != nil {
		t.Fatalf("EnsureSentinel is not idempotent: %v", err)
	}
}

// TestIsolatedURLRefusesSentinelDatabase 用子进程验证拒绝分支：IsolatedURL 必须
// 让测试进程失败，而不是安静地跳过。
func TestIsolatedURLRefusesSentinelDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a guarded test process")
	}
	base := testdb.IsolatedURL(t)
	name := fmt.Sprintf("sforum_test_testdb_deny_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin := openDatabase(t, ctx, base)
	requireDisposableCluster(t, ctx, admin)
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create guarded database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop guarded database %s: %v", name, err)
		}
		admin.Close()
	})
	target := databaseURLWithName(t, base, name)
	guarded := openDatabase(t, ctx, target)
	if err := guard.EnsureSentinel(ctx, guarded); err != nil {
		t.Fatalf("ensure sentinel: %v", err)
	}
	guarded.Close()

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIsolatedURLDenyHelper$")
	command.Env = append(os.Environ(),
		denyHelperEnv+"=1",
		testdb.EnvTestDatabaseURL+"="+target,
		testdb.EnvDevelopmentDatabaseURL+"=",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("IsolatedURL accepted a database carrying %s:\n%s", guard.SentinelTable, output)
	}
	if !strings.Contains(string(output), guard.SentinelTable) {
		t.Fatalf("refusal must name %s, got:\n%s", guard.SentinelTable, output)
	}
}

func TestIsolatedURLDenyHelper(t *testing.T) {
	if os.Getenv(denyHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	testdb.RequireIsolatedURL(t)
	t.Fatalf("IsolatedURL must refuse the guarded database")
}

func openDatabase(t *testing.T, ctx context.Context, databaseURL string) *sql.DB {
	t.Helper()
	db, err := guard.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open %s: %v", guard.Redact(databaseURL), err)
	}
	return db
}

// requireDisposableCluster 保证建库/删库只发生在用完即删的集群里。
// 非脚本环境（没有一次性集群标记）直接跳过，绝不在共享库上建临时库。
func requireDisposableCluster(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	state, err := guard.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("inspect cluster: %v", err)
	}
	if !state.Disposable {
		t.Skip("creating throwaway databases requires the disposable cluster marker; run ./scripts/test-api-isolated.sh")
	}
}

func databaseURLWithName(t *testing.T, base, name string) string {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", guard.Redact(base), err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// RequireIsolatedURL 在未设置时必须让测试进程失败（不是跳过）：被显式开关打开、
// 或子进程复用的场景里，静默跳过会被父测试误判成通过。
func TestRequireIsolatedURLFailsWithoutTestDatabaseURL(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a failing test process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRequireIsolatedURLMissingHelper$")
	command.Env = append(os.Environ(),
		requireMissingHelperEnv+"=1",
		testdb.EnvTestDatabaseURL+"=",
		testdb.EnvDevelopmentDatabaseURL+"=",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("RequireIsolatedURL accepted a missing test database URL:\n%s", output)
	}
	if !strings.Contains(string(output), testdb.EnvTestDatabaseURL+" is required") {
		t.Fatalf("failure must name the missing variable, got:\n%s", output)
	}
}

func TestRequireIsolatedURLMissingHelper(t *testing.T) {
	if os.Getenv(requireMissingHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	testdb.RequireIsolatedURL(t)
	t.Fatalf("RequireIsolatedURL must fail when %s is unset", testdb.EnvTestDatabaseURL)
}
