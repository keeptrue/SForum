package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	themeRuntimePublicationVersion = int64(202607150020)
)

func TestThemeRuntimePublicationMigrationConstraintsHistoryAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	// Apply directly to avoid the intentionally global sforum_core_v1 schema
	// owned by migration 016. This publication ledger has no mutable FK owners.
	if _, err := provider.ApplyVersion(ctx, themeRuntimePublicationVersion, true); err != nil {
		t.Fatalf("apply theme runtime publications: %v", err)
	}
	assertThemeRuntimePublicationSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest, reason
		) VALUES ('active', 'invalid.theme', '1.0.0', 'not-a-digest', 'activation')
	`); err == nil {
		t.Fatal("invalid exact theme publication was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			core_replacements_approved, reason
		) VALUES ('active', 'invalid.theme', '1.0.0', repeat('a', 64), TRUE, 'activation')
	`); err == nil {
		t.Fatal("Core replacement approval without a retained actor was accepted")
	}

	var publicationRevision int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			core_replacements_approved, reason
		) VALUES ('active', 'fixture.theme', '1.0.0', repeat('a', 64), FALSE, 'activation')
		RETURNING revision
	`).Scan(&publicationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE theme_runtime_publications SET theme_version = '2.0.0'
		WHERE revision = $1
	`, publicationRevision); err == nil {
		t.Fatal("unacknowledged publication tuple was mutable")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM theme_runtime_publications WHERE revision = $1`, publicationRevision); err == nil {
		t.Fatal("unacknowledged publication history was deletable")
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE theme_runtime_publications`); err == nil {
		t.Fatal("unacknowledged publication history was truncatable")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_nodes (
			node_id, boot_id, lease_expires_at
		) VALUES ('api-1', 'boot-1', statement_timestamp() + interval '1 minute')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publication_acks (
			publication_revision, node_id, boot_id, status
		) VALUES ($1, 'api-1', 'boot-1', 'applying')
	`, publicationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE theme_runtime_publication_acks
		SET status = 'applied', applied_state = 'active',
		    applied_theme_id = 'fixture.theme', applied_theme_version = '1.0.0',
		    applied_package_digest = repeat('a', 64),
		    applied_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'api-1' AND boot_id = 'boot-1'
	`, publicationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM theme_runtime_publications WHERE revision = $1`, publicationRevision); err == nil {
		t.Fatal("acknowledged publication history was deletable")
	}
	if _, err := provider.ApplyVersion(ctx, themeRuntimePublicationVersion, false); err == nil {
		t.Fatal("theme runtime publication Down must refuse durable evidence")
	}
	assertThemeRuntimePublicationSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `DELETE FROM theme_runtime_publication_acks`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM theme_runtime_nodes`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER theme_runtime_publication_immutable ON theme_runtime_publications;
		DROP TRIGGER theme_runtime_publication_no_truncate ON theme_runtime_publications;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM theme_runtime_publications`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, themeRuntimePublicationVersion, false); err != nil {
		t.Fatalf("rollback empty theme runtime publications: %v", err)
	}
	assertThemeRuntimePublicationSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, themeRuntimePublicationVersion, true); err != nil {
		t.Fatalf("reapply theme runtime publications: %v", err)
	}
	assertThemeRuntimePublicationSchema(t, ctx, db, true)
}

func assertThemeRuntimePublicationSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"theme_runtime_publications", "theme_runtime_nodes", "theme_runtime_publication_acks",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("table %s exists=%t want=%t", table, exists, want)
		}
	}
	if !want {
		return
	}
	var triggerExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger
			WHERE tgname = 'theme_runtime_publication_notify' AND NOT tgisinternal
		)
	`).Scan(&triggerExists); err != nil {
		t.Fatal(err)
	}
	if !triggerExists {
		t.Fatal("theme runtime publication notification trigger is missing")
	}
}
