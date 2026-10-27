package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresAuditCleanupReportsProtectedAndDeletedRows(t *testing.T) {
	tests := []struct {
		name           string
		ordinary       int
		suggestionRefs int
		catalogRefs    int
		wantDeleted    int64
		wantRetained   int64
		wantRowsAfter  int
	}{
		{name: "unprotected", ordinary: 3, wantDeleted: 3},
		{name: "only protected", suggestionRefs: 1, catalogRefs: 1, wantRetained: 2, wantRowsAfter: 2},
		{name: "mixed", ordinary: 2, suggestionRefs: 1, catalogRefs: 1, wantDeleted: 2, wantRetained: 2, wantRowsAfter: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuditRetentionFixture(t)
			for range test.ordinary {
				fixture.seedEvent(t, 60*24*time.Hour)
			}
			for range test.suggestionRefs {
				fixture.protectBySuggestion(t, fixture.seedEvent(t, 60*24*time.Hour))
			}
			for range test.catalogRefs {
				fixture.protectByCatalog(t, fixture.seedEvent(t, 60*24*time.Hour))
			}
			fixture.seedEvent(t, time.Hour)

			result, err := fixture.writer.CleanupOlderThan(fixture.ctx, 30)
			if err != nil {
				t.Fatal(err)
			}
			if result.Deleted != test.wantDeleted || result.Retained != test.wantRetained {
				t.Fatalf("cleanup result=%#v, want deleted=%d retained=%d", result, test.wantDeleted, test.wantRetained)
			}
			// Recent event remains in addition to protected expired authority rows.
			if got := fixture.countEvents(t); got != test.wantRowsAfter+1 {
				t.Fatalf("events after cleanup=%d, want %d", got, test.wantRowsAfter+1)
			}
			deleted, err := fixture.writer.DeleteOlderThan(fixture.ctx, 30)
			if err != nil || deleted != 0 {
				t.Fatalf("legacy cleanup deleted=%d error=%v", deleted, err)
			}
		})
	}
}

func TestPostgresAuditCleanupSkipsConcurrentAuthorityBinding(t *testing.T) {
	fixture := newAuditRetentionFixture(t)
	protectedID := fixture.seedEvent(t, 60*24*time.Hour)
	fixture.seedEvent(t, 60*24*time.Hour)

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(fixture.ctx, `
		INSERT INTO extension_permission_role_suggestions (decision_audit_event_id)
		VALUES ($1)
	`, protectedID); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.writer.CleanupOlderThan(fixture.ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Retained != 1 {
		t.Fatalf("concurrent cleanup result=%#v", result)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	result, err = fixture.writer.CleanupOlderThan(fixture.ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 || result.Retained != 1 || fixture.countEvents(t) != 1 {
		t.Fatalf("committed authority cleanup result=%#v events=%d", result, fixture.countEvents(t))
	}
}

func TestPostgresAuditCleanupRetainsLegacyGrantOnlyApplyEvidence(t *testing.T) {
	fixture := newAuditRetentionFixture(t)
	decisionAuditID := fixture.seedEvent(t, 60*24*time.Hour)
	appliedAuditID := fixture.seedEvent(t, 60*24*time.Hour)
	fixture.seedEvent(t, 60*24*time.Hour)

	suggestionID := fixture.protectBySuggestion(t, decisionAuditID)
	fixture.protectByGrant(t, suggestionID, appliedAuditID)

	result, err := fixture.writer.CleanupOlderThan(fixture.ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Retained != 2 || fixture.countEvents(t) != 2 {
		t.Fatalf("legacy grant cleanup result=%#v events=%d", result, fixture.countEvents(t))
	}
	deleted, err := fixture.writer.DeleteOlderThan(fixture.ctx, 30)
	if err != nil || deleted != 0 {
		t.Fatalf("legacy cleaner deleted=%d error=%v", deleted, err)
	}
}

func TestPostgresAuditCleanupWorksBeforePermissionCatalogMigration(t *testing.T) {
	fixture := newAuditRetentionFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		DROP TABLE extension_permission_role_grants;
		DROP TABLE extension_permission_catalog;
	`); err != nil {
		t.Fatal(err)
	}
	protectedID := fixture.seedEvent(t, 60*24*time.Hour)
	fixture.protectBySuggestion(t, protectedID)
	fixture.seedEvent(t, 60*24*time.Hour)

	result, err := fixture.writer.CleanupOlderThan(fixture.ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Retained != 1 || fixture.countEvents(t) != 1 {
		t.Fatalf("pre-catalog cleanup result=%#v events=%d", result, fixture.countEvents(t))
	}
}

type auditRetentionFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	writer *PostgresWriter
}

func newAuditRetentionFixture(t *testing.T) *auditRetentionFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("audit_retention_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			actor_user_id BIGINT,
			target_user_id BIGINT,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE extension_permission_role_suggestions (
			id BIGSERIAL PRIMARY KEY,
			decision_audit_event_id BIGINT REFERENCES audit_events(id) ON DELETE RESTRICT
		);
		CREATE TABLE extension_permission_catalog (
			permission_key TEXT PRIMARY KEY,
			registered_audit_event_id BIGINT NOT NULL REFERENCES audit_events(id) ON DELETE RESTRICT
		);
		CREATE TABLE extension_permission_role_grants (
			suggestion_id BIGINT PRIMARY KEY
				REFERENCES extension_permission_role_suggestions(id) ON DELETE RESTRICT,
			applied_audit_event_id BIGINT NOT NULL
				REFERENCES audit_events(id) ON DELETE RESTRICT
		);
	`); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return &auditRetentionFixture{ctx: ctx, pool: pool, writer: NewPostgresWriter(pool)}
}

func (f *auditRetentionFixture) seedEvent(t *testing.T, age time.Duration) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO audit_events (action, created_at)
		VALUES ('fixture.audit', $1)
		RETURNING id
	`, time.Now().UTC().Add(-age)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *auditRetentionFixture) protectBySuggestion(t *testing.T, auditID int64) int64 {
	t.Helper()
	var suggestionID int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO extension_permission_role_suggestions (decision_audit_event_id)
		VALUES ($1)
		RETURNING id
	`, auditID).Scan(&suggestionID); err != nil {
		t.Fatal(err)
	}
	return suggestionID
}

func (f *auditRetentionFixture) protectByCatalog(t *testing.T, auditID int64) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_permission_catalog (permission_key, registered_audit_event_id)
		VALUES ($1, $2)
	`, fmt.Sprintf("fixture.permission.%d", auditID), auditID); err != nil {
		t.Fatal(err)
	}
}

func (f *auditRetentionFixture) protectByGrant(t *testing.T, suggestionID, auditID int64) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO extension_permission_role_grants (suggestion_id, applied_audit_event_id)
		VALUES ($1, $2)
	`, suggestionID, auditID); err != nil {
		t.Fatal(err)
	}
}

func (f *auditRetentionFixture) countEvents(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
