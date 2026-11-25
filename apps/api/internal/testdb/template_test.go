package testdb_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const missingTemplateHelperEnv = "SFORUM_TESTDB_MISSING_TEMPLATE_HELPER"

func TestMigratedDatabaseURLClonesMigratedTemplateWithIsolation(t *testing.T) {
	firstURL := testdb.MigratedDatabaseURL(t, "template_isolation_a")
	secondURL := testdb.MigratedDatabaseURL(t, "template_isolation_b")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first := openPool(t, ctx, firstURL)
	second := openPool(t, ctx, secondURL)
	for label, pool := range map[string]*pgxpool.Pool{"first": first, "second": second} {
		var version int64
		if err := pool.QueryRow(ctx,
			`SELECT COALESCE(max(version_id), 0) FROM goose_db_version`,
		).Scan(&version); err != nil {
			t.Fatalf("%s clone has no migration ledger: %v", label, err)
		}
		if version <= 0 {
			t.Fatalf("%s clone migration version = %d", label, version)
		}
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	username := "template_isolation_" + suffix
	if _, err := first.Exec(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Template Isolation')
	`, username, username+"@example.test"); err != nil {
		t.Fatalf("insert into first clone: %v", err)
	}
	var count int
	if err := second.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE username = $1`, username,
	).Scan(&count); err != nil {
		t.Fatalf("read second clone: %v", err)
	}
	if count != 0 {
		t.Fatalf("second clone observed %d rows from first clone", count)
	}
}

func TestDatabaseTemplatePreparesExactlyOnceAndClones(t *testing.T) {
	var calls atomic.Int32
	template := testdb.NewTemplate("prepare_once", func(ctx context.Context, databaseURL string) error {
		calls.Add(1)
		conn, err := pgx.Connect(ctx, databaseURL)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		_, err = conn.Exec(ctx, `CREATE TABLE template_probe (id INTEGER PRIMARY KEY)`)
		return err
	})

	firstURL := template.Clone(t, "prepared_a")
	secondURL := template.Clone(t, "prepared_b")
	if got := calls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for label, databaseURL := range map[string]string{"first": firstURL, "second": secondURL} {
		pool := openPool(t, ctx, databaseURL)
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('template_probe') IS NOT NULL`,
		).Scan(&exists); err != nil {
			t.Fatalf("%s clone: %v", label, err)
		}
		if !exists {
			t.Fatalf("%s clone is missing prepared table", label)
		}
		var currentSchema string
		if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil {
			t.Fatalf("%s clone current_schema: %v", label, err)
		}
		if !strings.HasPrefix(currentSchema, "fixture_prepare_once_") {
			t.Fatalf("%s clone current_schema = %q", label, currentSchema)
		}
		var coreTableExists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass('public.users') IS NOT NULL`,
		).Scan(&coreTableExists); err != nil {
			t.Fatalf("%s clone public schema: %v", label, err)
		}
		if !coreTableExists {
			t.Fatalf("%s clone lost the full public schema fallback", label)
		}
	}
}

func TestMigratedDatabaseURLFailsClosedWhenTemplateIsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a failing test process")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigratedDatabaseMissingTemplateHelper$")
	command.Env = append(os.Environ(),
		missingTemplateHelperEnv+"=1",
		testdb.EnvFullTemplateDatabase+"=sforum_test_missing_template",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("MigratedDatabaseURL accepted a missing template:\n%s", output)
	}
	if !strings.Contains(string(output), "sforum_test_missing_template") {
		t.Fatalf("failure must name the missing template, got:\n%s", output)
	}
}

func TestMigratedDatabaseMissingTemplateHelper(t *testing.T) {
	if os.Getenv(missingTemplateHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	testdb.MigratedDatabaseURL(t, "missing_template")
	t.Fatal("MigratedDatabaseURL must fail closed without a template")
}

func openPool(t *testing.T, ctx context.Context, databaseURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open cloned database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
