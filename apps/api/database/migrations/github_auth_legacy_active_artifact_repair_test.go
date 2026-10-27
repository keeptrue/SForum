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

const githubAuthLegacyActiveArtifactRepairMigration = "202607270060_github_auth_legacy_active_artifact_repair.sql"

func TestR7_Migration060RestoresOnlyAuditedPreLifecycleGitHubArtifact(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("migration060_%d", time.Now().UnixNano())
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
			active_version_id BIGINT, staged_version_id BIGINT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY, extension_id TEXT NOT NULL, version TEXT NOT NULL, package_digest TEXT NOT NULL
		);
		CREATE TABLE extension_identity_registry_publications (
			owner_extension_id TEXT NOT NULL, revision BIGINT NOT NULL, registry_state TEXT NOT NULL,
			extension_version_id BIGINT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL,
			audit_event_id BIGINT NOT NULL
		);
		CREATE TABLE extension_lifecycle_operations (
			extension_id TEXT NOT NULL, operation TEXT NOT NULL, state TEXT NOT NULL, terminal_result TEXT
		);
		CREATE TABLE audit_events (id BIGINT PRIMARY KEY, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb);

		INSERT INTO extensions (id, type, source, status, active_version_id, staged_version_id) VALUES
			('sforum.auth-github', 'plugin', 'builtin', 'enabled', 202, 203),
			('sforum.auth-github-lifecycle', 'plugin', 'builtin', 'enabled', 205, 206),
			('sforum.auth-github-uploaded', 'plugin', 'uploaded', 'enabled', 208, 209);
		INSERT INTO extension_versions VALUES
			(201, 'sforum.auth-github', '1.0.0', repeat('a', 64)),
			(202, 'sforum.auth-github', '1.0.0', repeat('b', 64)),
			(203, 'sforum.auth-github', '1.0.0', repeat('c', 64)),
			(204, 'sforum.auth-github-lifecycle', '1.0.0', repeat('d', 64)),
			(205, 'sforum.auth-github-lifecycle', '1.0.0', repeat('e', 64)),
			(206, 'sforum.auth-github-lifecycle', '1.0.0', repeat('f', 64)),
			(207, 'sforum.auth-github-uploaded', '1.0.0', repeat('0', 64)),
			(208, 'sforum.auth-github-uploaded', '1.0.0', repeat('1', 64)),
			(209, 'sforum.auth-github-uploaded', '1.0.0', repeat('2', 64));
		INSERT INTO audit_events VALUES
			(1, 'extension.enable', jsonb_build_object('extensionId', 'sforum.auth-github')),
			(2, 'extension.enable', jsonb_build_object('extensionId', 'sforum.auth-github-lifecycle')),
			(3, 'extension.enable', jsonb_build_object('extensionId', 'sforum.auth-github-uploaded'));
		INSERT INTO extension_identity_registry_publications VALUES
			('sforum.auth-github', 1, 'active', 201, '1.0.0', repeat('a', 64), 1),
			('sforum.auth-github-lifecycle', 1, 'active', 204, '1.0.0', repeat('d', 64), 2),
			('sforum.auth-github-uploaded', 1, 'active', 207, '1.0.0', repeat('0', 64), 3);
		INSERT INTO extension_lifecycle_operations VALUES
			('sforum.auth-github-lifecycle', 'upgrade', 'enabled', 'succeeded');
	`); err != nil {
		t.Fatal(err)
	}

	body, err := fs.ReadFile(Files(), githubAuthLegacyActiveArtifactRepairMigration)
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(body), "-- +goose Down")
	if !found {
		t.Fatal("migration 060 has no Down section")
	}
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("apply migration 060: %v", err)
	}
	assertMigration060Versions(t, ctx, pool, "sforum.auth-github", 201, 203)
	assertMigration060Versions(t, ctx, pool, "sforum.auth-github-lifecycle", 205, 206)
	assertMigration060Versions(t, ctx, pool, "sforum.auth-github-uploaded", 208, 209)

	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("rerun migration 060: %v", err)
	}
	assertMigration060Versions(t, ctx, pool, "sforum.auth-github", 201, 203)
}

func assertMigration060Versions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, wantActive, wantStaged int64) {
	t.Helper()
	var active, staged int64
	if err := pool.QueryRow(ctx, `SELECT active_version_id, staged_version_id FROM extensions WHERE id = $1`, id).Scan(&active, &staged); err != nil {
		t.Fatal(err)
	}
	if active != wantActive || staged != wantStaged {
		t.Fatalf("extension %s = active=%d staged=%d, want active=%d staged=%d", id, active, staged, wantActive, wantStaged)
	}
}
