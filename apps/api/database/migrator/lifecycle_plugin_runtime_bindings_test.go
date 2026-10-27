package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecyclePluginRuntimeBindingsVersion = int64(202607160030)

func TestLifecyclePluginRuntimeBindingsMigrationAtomicEvidenceAndProtection(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedPluginRuntimeVersionTable(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, true); err != nil {
		t.Fatalf("apply plugin runtime publications: %v", err)
	}
	seedLifecyclePluginRuntimeBindingJournal(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingsVersion, true); err != nil {
		t.Fatalf("apply lifecycle plugin runtime binding migration: %v", err)
	}
	assertLifecyclePluginRuntimeBindingSchema(t, ctx, db, true)

	// Empty binding history may be downgraded and reapplied without touching either
	// retained publication table.
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingsVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle plugin runtime binding migration: %v", err)
	}
	assertLifecyclePluginRuntimeBindingSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingsVersion, true); err != nil {
		t.Fatalf("reapply lifecycle plugin runtime binding migration: %v", err)
	}

	publicationRevision := insertEmptyPluginRuntimePublication(t, ctx, db, "enable")
	journalID := insertLifecyclePluginRuntimeBindingJournal(t, ctx, db)

	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET plugin_runtime_publication_revision = $2
		WHERE id = $1
	`, journalID, publicationRevision); err == nil || !strings.Contains(err.Error(), "requires committed marker") {
		t.Fatalf("uncommitted lifecycle marker accepted runtime binding: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET commit_marker = TRUE,
		    committed_attempt = last_attempt,
		    committed_at = statement_timestamp(),
		    plugin_runtime_publication_revision = $2,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND commit_marker = FALSE
	`, journalID, publicationRevision); err != nil {
		t.Fatalf("atomically bind committed lifecycle marker: %v", err)
	}

	var committed bool
	var boundRevision sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT commit_marker, plugin_runtime_publication_revision
		FROM extension_lifecycle_publications
		WHERE id = $1
	`, journalID).Scan(&committed, &boundRevision); err != nil {
		t.Fatal(err)
	}
	if !committed || !boundRevision.Valid || boundRevision.Int64 != publicationRevision {
		t.Fatalf("committed lifecycle binding=(%v,%v), want (true,%d)", committed, boundRevision, publicationRevision)
	}

	otherRevision := insertEmptyPluginRuntimePublication(t, ctx, db, "recovery")
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET plugin_runtime_publication_revision = $2
		WHERE id = $1
	`, journalID, otherRevision); err == nil || !strings.Contains(err.Error(), "binding is immutable") {
		t.Fatalf("committed lifecycle runtime binding was rewritten: %v", err)
	}
	foreignJournalID := insertLifecyclePluginRuntimeBindingJournal(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET commit_marker = TRUE,
		    committed_attempt = last_attempt,
		    committed_at = statement_timestamp(),
		    plugin_runtime_publication_revision = 9223372036854775807
		WHERE id = $1
	`, foreignJournalID); err == nil {
		t.Fatal("lifecycle runtime binding accepted a missing desired revision")
	}

	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingsVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove lifecycle plugin runtime binding history") {
		t.Fatalf("binding migration Down removed retained evidence: %v", err)
	}
	assertLifecyclePluginRuntimeBindingSchema(t, ctx, db, true)
}

func seedLifecyclePluginRuntimeBindingJournal(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extension_lifecycle_publications (
			id BIGSERIAL PRIMARY KEY,
			last_attempt INTEGER NOT NULL DEFAULT 1,
			committed_attempt INTEGER,
			commit_marker BOOLEAN NOT NULL DEFAULT FALSE,
			revision BIGINT NOT NULL DEFAULT 1,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
			committed_at TIMESTAMPTZ
		)
	`); err != nil {
		t.Fatal(err)
	}
}

func insertEmptyPluginRuntimePublication(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	reason string,
) int64 {
	t.Helper()
	var revision int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (0, $1, $2)
		RETURNING revision
	`, pluginRuntimeTestMembersDigest(), reason).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func insertLifecyclePluginRuntimeBindingJournal(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) int64 {
	t.Helper()
	var journalID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_lifecycle_publications DEFAULT VALUES
		RETURNING id
	`).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	return journalID
}

func assertLifecyclePluginRuntimeBindingSchema(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	want bool,
) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'extension_lifecycle_publications'
			  AND column_name = 'plugin_runtime_publication_revision'
		)
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("lifecycle plugin runtime binding column exists=%v, want %v", exists, want)
	}
}
