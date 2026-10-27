package migrator

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const themeRuntimeSourceApprovalVersion = int64(202607150021)

func TestThemeRuntimeSourceApprovalMigrationConstraintsAndDownProtection(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.ApplyVersion(ctx, themeRuntimePublicationVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, themeRuntimeSourceApprovalVersion, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			source_theme_id, source_theme_version, source_package_digest,
			source_core_replacements_approved, reason
		) VALUES (
			'active', 'target.theme', '2.0.0', repeat('b', 64),
			'previous.theme', '1.0.0', repeat('a', 64), TRUE, 'activation'
		)
	`); err == nil {
		t.Fatal("source Core approval without its prior actor was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			source_core_replacements_approved, source_actor_user_id, reason
		) VALUES (
			'active', 'target.theme', '2.0.0', repeat('b', 64),
			TRUE, 42, 'activation'
		)
	`); err == nil {
		t.Fatal("source approval without an exact source theme was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			source_theme_id, source_theme_version, source_package_digest,
			source_core_replacements_approved, source_actor_user_id, reason
		) VALUES (
			'active', 'target.theme', '2.0.0', repeat('b', 64),
			'previous.theme', '1.0.0', repeat('a', 64), FALSE, 42, 'activation'
		)
	`); err == nil {
		t.Fatal("source approval actor without approval was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO theme_runtime_publications (
			desired_state, theme_id, theme_version, package_digest,
			source_theme_id, source_theme_version, source_package_digest,
			source_core_replacements_approved, source_actor_user_id, reason
		) VALUES (
			'active', 'target.theme', '2.0.0', repeat('b', 64),
			'previous.theme', '1.0.0', repeat('a', 64), TRUE, 42, 'activation'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, themeRuntimeSourceApprovalVersion, false); err == nil {
		t.Fatal("source approval Down removed durable history")
	}
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER theme_runtime_publication_immutable ON theme_runtime_publications;
		DROP TRIGGER theme_runtime_publication_no_truncate ON theme_runtime_publications;
		DELETE FROM theme_runtime_publications;
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, themeRuntimeSourceApprovalVersion, false); err != nil {
		t.Fatal(err)
	}
}
