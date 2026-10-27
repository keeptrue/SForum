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

const githubAuthLegacyRuntimePublicationRepairMigration = "202607270061_github_auth_legacy_runtime_publication_repair.sql"

func TestR7_Migration061AppendsExactRuntimeRecoveryOnlyForAuditedPreLifecycleGitHub(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("migration061_%d", time.Now().UnixNano())
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
		CREATE TABLE extensions (id TEXT PRIMARY KEY, type TEXT NOT NULL, source TEXT NOT NULL, status TEXT NOT NULL, active_version_id BIGINT);
		CREATE TABLE extension_versions (id BIGINT PRIMARY KEY, extension_id TEXT NOT NULL, version TEXT NOT NULL, package_digest TEXT NOT NULL);
		CREATE TABLE extension_identity_registry_publications (
			owner_extension_id TEXT NOT NULL, revision BIGINT NOT NULL, registry_state TEXT NOT NULL,
			extension_version_id BIGINT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL, audit_event_id BIGINT NOT NULL
		);
		CREATE TABLE audit_events (id BIGINT PRIMARY KEY, action TEXT NOT NULL, metadata JSONB NOT NULL);
		CREATE TABLE extension_lifecycle_operations (extension_id TEXT NOT NULL, operation TEXT NOT NULL, state TEXT NOT NULL, terminal_result TEXT);
		CREATE TABLE plugin_runtime_publications (revision BIGSERIAL PRIMARY KEY, member_count INTEGER NOT NULL, members_digest TEXT NOT NULL, reason TEXT NOT NULL, actor_user_id BIGINT);
		CREATE TABLE plugin_runtime_publication_members (publication_revision BIGINT NOT NULL, extension_id TEXT NOT NULL, extension_version_id BIGINT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL);

		INSERT INTO extensions VALUES
			('sforum.auth-github', 'plugin', 'builtin', 'enabled', 301),
			('fixture.other', 'plugin', 'builtin', 'enabled', 303),
			('fixture.evidence-free', 'plugin', 'builtin', 'enabled', 304),
			('fixture.wrong-artifact', 'plugin', 'builtin', 'enabled', 305);
		INSERT INTO extension_versions VALUES
			(301, 'sforum.auth-github', '1.0.0', repeat('a', 64)),
			(302, 'sforum.auth-github', '1.0.0', repeat('b', 64)),
			(303, 'fixture.other', '1.0.0', repeat('c', 64)),
			(304, 'fixture.evidence-free', '1.0.0', repeat('d', 64)),
			(305, 'fixture.wrong-artifact', '1.0.0', repeat('e', 64)),
			(306, 'fixture.wrong-artifact', '1.0.0', repeat('f', 64));
		INSERT INTO audit_events VALUES (1, 'extension.enable', jsonb_build_object('extensionId', 'sforum.auth-github'));
		INSERT INTO extension_identity_registry_publications VALUES
			('sforum.auth-github', 1, 'active', 301, '1.0.0', repeat('a', 64), 1);
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason) VALUES (2, repeat('0', 64), 'upgrade');
		INSERT INTO plugin_runtime_publication_members VALUES
			(1, 'sforum.auth-github', 302, '1.0.0', repeat('b', 64)),
			(1, 'fixture.other', 303, '1.0.0', repeat('c', 64)),
			(1, 'fixture.wrong-artifact', 306, '1.0.0', repeat('f', 64));
	`); err != nil {
		t.Fatal(err)
	}

	body, err := fs.ReadFile(Files(), githubAuthLegacyRuntimePublicationRepairMigration)
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(body), "-- +goose Down")
	if !found {
		t.Fatal("migration 061 has no Down section")
	}
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("apply migration 061: %v", err)
	}
	assertMigration061RuntimePublication(t, ctx, pool, 2, 301, 303)

	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("rerun migration 061: %v", err)
	}
	assertMigration061RuntimePublication(t, ctx, pool, 2, 301, 303)
}

func TestR7_Migration061FailsClosedForTombstonedOrFreshState(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	body, err := fs.ReadFile(Files(), githubAuthLegacyRuntimePublicationRepairMigration)
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(body), "-- +goose Down")
	if !found {
		t.Fatal("migration 061 has no Down section")
	}

	for _, scenario := range []struct {
		name string
		seed string
	}{
		{name: "fresh_install", seed: ""},
		{name: "tombstoned_identity_root", seed: `
			INSERT INTO extensions VALUES ('sforum.auth-github', 'plugin', 'builtin', 'enabled', 301);
			INSERT INTO extension_versions VALUES (301, 'sforum.auth-github', '1.0.0', repeat('a', 64));
			INSERT INTO audit_events VALUES (1, 'extension.enable', jsonb_build_object('extensionId', 'sforum.auth-github'));
			INSERT INTO extension_identity_registry_publications VALUES ('sforum.auth-github', 1, 'tombstone', 301, '1.0.0', repeat('a', 64), 1);
			INSERT INTO plugin_runtime_publications (member_count, members_digest, reason) VALUES (1, repeat('0', 64), 'upgrade');
			INSERT INTO plugin_runtime_publication_members VALUES (1, 'sforum.auth-github', 999, 'old', repeat('b', 64));
		`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			schema := fmt.Sprintf("migration061_%d", time.Now().UnixNano())
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
				CREATE TABLE extensions (id TEXT PRIMARY KEY, type TEXT NOT NULL, source TEXT NOT NULL, status TEXT NOT NULL, active_version_id BIGINT);
				CREATE TABLE extension_versions (id BIGINT PRIMARY KEY, extension_id TEXT NOT NULL, version TEXT NOT NULL, package_digest TEXT NOT NULL);
				CREATE TABLE extension_identity_registry_publications (owner_extension_id TEXT NOT NULL, revision BIGINT NOT NULL, registry_state TEXT NOT NULL, extension_version_id BIGINT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL, audit_event_id BIGINT NOT NULL);
				CREATE TABLE audit_events (id BIGINT PRIMARY KEY, action TEXT NOT NULL, metadata JSONB NOT NULL);
				CREATE TABLE extension_lifecycle_operations (extension_id TEXT NOT NULL, operation TEXT NOT NULL, state TEXT NOT NULL, terminal_result TEXT);
				CREATE TABLE plugin_runtime_publications (revision BIGSERIAL PRIMARY KEY, member_count INTEGER NOT NULL, members_digest TEXT NOT NULL, reason TEXT NOT NULL, actor_user_id BIGINT);
				CREATE TABLE plugin_runtime_publication_members (publication_revision BIGINT NOT NULL, extension_id TEXT NOT NULL, extension_version_id BIGINT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL);
			`+scenario.seed); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_runtime_publications WHERE reason = 'recovery'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("migration 061 created recovery publication for %s", scenario.name)
			}
		})
	}
}

func assertMigration061RuntimePublication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantRevisions int, wantGitHub, wantOther int64) {
	t.Helper()
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_runtime_publications`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != wantRevisions {
		t.Fatalf("runtime publication revisions=%d, want %d", revisions, wantRevisions)
	}
	rows, err := pool.Query(ctx, `SELECT extension_id, extension_version_id FROM plugin_runtime_publication_members WHERE publication_revision = (SELECT max(revision) FROM plugin_runtime_publications) ORDER BY extension_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var id string
		var versionID int64
		if err := rows.Scan(&id, &versionID); err != nil {
			t.Fatal(err)
		}
		got[id] = versionID
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["sforum.auth-github"] != wantGitHub || got["fixture.other"] != wantOther || len(got) != 2 {
		t.Fatalf("latest runtime members=%#v", got)
	}
	if _, leaked := got["fixture.evidence-free"]; leaked {
		t.Fatalf("enabled artifact without durable runtime evidence was recovered: %#v", got)
	}
	if _, leaked := got["fixture.wrong-artifact"]; leaked {
		t.Fatalf("wrong exact artifact was recovered: %#v", got)
	}
}
