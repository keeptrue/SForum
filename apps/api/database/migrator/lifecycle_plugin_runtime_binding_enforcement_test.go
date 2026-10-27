package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const lifecyclePluginRuntimeBindingEnforcementVersion = int64(202607160031)

func TestLifecyclePluginRuntimeBindingEnforcementMigrationSameCASAndHistory(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedPluginRuntimeVersionTable(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, true); err != nil {
		t.Fatalf("apply plugin runtime publications: %v", err)
	}
	seedLifecyclePluginRuntimeBindingJournal(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingsVersion, true); err != nil {
		t.Fatalf("apply lifecycle plugin runtime bindings: %v", err)
	}

	historicalID := insertCommittedLifecycleMarkerWithoutRuntimeBinding(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingEnforcementVersion, true); err != nil {
		t.Fatalf("apply lifecycle runtime binding enforcement: %v", err)
	}

	// Historical committed/NULL evidence remains readable, but an operator cannot
	// invent a desired revision after the original transaction.
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET plugin_runtime_publication_revision = $2
		WHERE id = $1
	`, historicalID, insertEmptyPluginRuntimePublication(t, ctx, db, "recovery")); err == nil ||
		!strings.Contains(err.Error(), "cannot be backfilled") {
		t.Fatalf("historical lifecycle marker accepted runtime backfill: %v", err)
	}

	// With no binding evidence, 031 may be rolled back and reapplied while the
	// nullable historical marker remains intact.
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingEnforcementVersion, false); err != nil {
		t.Fatalf("rollback empty lifecycle runtime enforcement: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingEnforcementVersion, true); err != nil {
		t.Fatalf("reapply lifecycle runtime enforcement: %v", err)
	}

	journalID := insertLifecyclePluginRuntimeBindingJournal(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET commit_marker = TRUE,
		    committed_attempt = last_attempt,
		    committed_at = statement_timestamp()
		WHERE id = $1
	`, journalID); err == nil || !strings.Contains(err.Error(), "requires plugin runtime binding") {
		t.Fatalf("new lifecycle marker committed without runtime binding: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_lifecycle_publications (
			commit_marker, committed_attempt, committed_at
		) VALUES (TRUE, 1, statement_timestamp())
	`); err == nil || !strings.Contains(err.Error(), "requires plugin runtime binding") {
		t.Fatalf("inserted committed lifecycle marker without runtime binding: %v", err)
	}

	publicationRevision := insertEmptyPluginRuntimePublication(t, ctx, db, "enable")
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
		t.Fatalf("commit lifecycle marker with same-CAS runtime binding: %v", err)
	}
	assertCommittedLifecycleRuntimeBinding(t, ctx, db, journalID, publicationRevision)

	if _, err := db.ExecContext(ctx, `
		UPDATE extension_lifecycle_publications
		SET commit_marker = FALSE
		WHERE id = $1
	`, journalID); err == nil || !strings.Contains(err.Error(), "committed lifecycle marker is immutable") {
		t.Fatalf("committed lifecycle marker was reopened: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, lifecyclePluginRuntimeBindingEnforcementVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot weaken lifecycle plugin runtime binding enforcement") {
		t.Fatalf("binding enforcement Down weakened retained evidence: %v", err)
	}
}

func insertCommittedLifecycleMarkerWithoutRuntimeBinding(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_lifecycle_publications (
			commit_marker, committed_attempt, committed_at
		) VALUES (TRUE, 1, statement_timestamp())
		RETURNING id
	`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertCommittedLifecycleRuntimeBinding(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	journalID int64,
	wantRevision int64,
) {
	t.Helper()
	var committed bool
	var revision sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT commit_marker, plugin_runtime_publication_revision
		FROM extension_lifecycle_publications
		WHERE id = $1
	`, journalID).Scan(&committed, &revision); err != nil {
		t.Fatal(err)
	}
	if !committed || !revision.Valid || revision.Int64 != wantRevision {
		t.Fatalf("lifecycle marker committed=%v runtime revision=%v, want %d", committed, revision, wantRevision)
	}
}
