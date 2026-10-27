package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const githubAuthLegacyEnabledStateRepairMigration = "202607270058_github_auth_legacy_enabled_state_repair.sql"

func TestR5_Migration058QuarantinesOnlyEvidenceFreeLegacyGitHubBuiltin(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("migration058_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY, type TEXT NOT NULL, source TEXT NOT NULL, status TEXT NOT NULL,
			active_version_id BIGINT,
			staged_version_id BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY, extension_id TEXT NOT NULL, version TEXT NOT NULL, package_digest TEXT NOT NULL
		);
		CREATE TABLE extension_identity_registry_publications (
			owner_extension_id TEXT NOT NULL, revision BIGINT NOT NULL, registry_state TEXT NOT NULL
		);
		CREATE TABLE extension_lifecycle_operations (
			extension_id TEXT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL,
			operation TEXT NOT NULL, state TEXT NOT NULL, terminal_result TEXT
		);
		CREATE TABLE audit_events (action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb);

		INSERT INTO extensions (id, type, source, status, active_version_id) VALUES
			('sforum.auth-github', 'plugin', 'builtin', 'enabled', 101),
			('fixture.lifecycle-evidence', 'plugin', 'builtin', 'enabled', 102),
			('fixture.audit-evidence', 'plugin', 'builtin', 'enabled', 103),
			('fixture.partial-publication', 'plugin', 'builtin', 'enabled', 104),
			('fixture.unrelated', 'plugin', 'builtin', 'enabled', 105),
			('sforum.auth-github-uploaded', 'plugin', 'uploaded', 'enabled', 106),
			('sforum.auth-github-theme', 'theme', 'builtin', 'enabled', 107);
		INSERT INTO extension_versions (id, extension_id, version, package_digest) VALUES
			(101, 'sforum.auth-github', '1.0.0', repeat('a', 64)),
			(102, 'fixture.lifecycle-evidence', '1.0.0', repeat('b', 64)),
			(103, 'fixture.audit-evidence', '1.0.0', repeat('c', 64)),
			(104, 'fixture.partial-publication', '1.0.0', repeat('d', 64)),
			(105, 'fixture.unrelated', '1.0.0', repeat('e', 64)),
			(106, 'sforum.auth-github-uploaded', '1.0.0', repeat('f', 64)),
			(107, 'sforum.auth-github-theme', '1.0.0', repeat('0', 64));
		INSERT INTO extension_lifecycle_operations VALUES
			('fixture.lifecycle-evidence', '1.0.0', repeat('b', 64), 'enable', 'enabled', 'succeeded'),
			('fixture.partial-publication', '1.0.0', repeat('d', 64), 'install', 'enabled', 'succeeded');
		INSERT INTO audit_events VALUES
			('extension.enable', jsonb_build_object('extensionId', 'fixture.audit-evidence'));
		INSERT INTO extension_identity_registry_publications VALUES
			('fixture.partial-publication', 1, 'active'),
			('fixture.partial-publication', 2, 'tombstone');
	`); err != nil {
		t.Fatal(err)
	}

	body, err := fs.ReadFile(Files(), githubAuthLegacyEnabledStateRepairMigration)
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(body), "-- +goose Down")
	if !found {
		t.Fatal("migration 058 has no Down section")
	}
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("apply migration 058: %v", err)
	}

	assertMigration058Extension(t, ctx, pool, "sforum.auth-github", "installed", nil, ptrInt64(101))
	for _, id := range []string{
		"fixture.lifecycle-evidence", "fixture.audit-evidence", "fixture.partial-publication",
		"fixture.unrelated", "sforum.auth-github-uploaded", "sforum.auth-github-theme",
	} {
		assertMigration058Extension(t, ctx, pool, id, "enabled", ptrInt64(101+int64(indexOfMigration058ID(id))), nil)
	}

	// Re-running is idempotent: the now-installed legacy row does not receive a
	// second mutation and every preserved state remains enabled.
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("rerun migration 058: %v", err)
	}
	assertMigration058Extension(t, ctx, pool, "sforum.auth-github", "installed", nil, ptrInt64(101))
}

func assertMigration058Extension(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, wantStatus string, wantActive, wantStaged *int64) {
	t.Helper()
	var status string
	var active, staged *int64
	if err := pool.QueryRow(ctx, `SELECT status, active_version_id, staged_version_id FROM extensions WHERE id = $1`, id).Scan(&status, &active, &staged); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || !equalMigration058Int64(active, wantActive) || !equalMigration058Int64(staged, wantStaged) {
		t.Fatalf("extension %s = status=%q active=%v staged=%v, want status=%q active=%v staged=%v", id, status, active, staged, wantStatus, wantActive, wantStaged)
	}
}

func equalMigration058Int64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func ptrInt64(value int64) *int64 { return &value }

func indexOfMigration058ID(id string) int {
	for index, candidate := range []string{
		"fixture.lifecycle-evidence", "fixture.audit-evidence", "fixture.partial-publication",
		"fixture.unrelated", "sforum.auth-github-uploaded", "sforum.auth-github-theme",
	} {
		if candidate == id {
			return index + 1
		}
	}
	panic("unknown migration 058 fixture id")
}
