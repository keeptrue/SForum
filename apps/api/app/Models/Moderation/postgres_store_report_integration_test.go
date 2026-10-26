package moderation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
)

func TestPostgresStoreReportMutationsReturnStoredProjection(t *testing.T) {
	ctx, pool := newReportStorePool(t)
	reporterID := insertReportStoreUser(t, ctx, pool, "reporter", "Reporter")
	reviewerID := insertReportStoreUser(t, ctx, pool, "reviewer", "Reviewer")
	store := moderation.NewPostgresStore(pool)

	report, err := store.CreateReport(ctx, moderation.CreateReportInput{
		ReporterUserID: reporterID,
		TargetType:     moderation.TargetTypeTopic,
		TargetID:       42,
		ReasonCode:     moderation.ReasonSpam,
		Body:           "duplicate content",
	})
	if err != nil {
		t.Fatalf("create report: %v", err)
	}
	if report.ReporterUserID != reporterID || report.ReporterName != "Reporter" || report.Status != moderation.StatusOpen {
		t.Fatalf("created report projection = %#v", report)
	}

	_, err = store.CreateReport(ctx, moderation.CreateReportInput{
		ReporterUserID: reporterID,
		TargetType:     moderation.TargetTypeTopic,
		TargetID:       42,
		ReasonCode:     moderation.ReasonSpam,
	})
	if !errors.Is(err, moderation.ErrReportDuplicate) {
		t.Fatalf("duplicate create error = %v, want ErrReportDuplicate", err)
	}

	updated, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:       report.ID,
		ReviewerUserID: reviewerID,
		Status:         moderation.StatusResolved,
		ReviewNote:     "handled",
	})
	if err != nil {
		t.Fatalf("update report: %v", err)
	}
	if updated.ReporterName != "Reporter" || updated.ReviewerUserID == nil ||
		*updated.ReviewerUserID != reviewerID || updated.ReviewerName != "Reviewer" ||
		updated.Status != moderation.StatusResolved || updated.ReviewNote != "handled" || updated.ResolvedAt == nil {
		t.Fatalf("updated report projection = %#v", updated)
	}
}

func newReportStorePool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	schema := "moderation_report_" + hex.EncodeToString(random)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create fixture schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, reportStoreSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
		t.Fatalf("create fixture tables: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	return ctx, pool
}

func insertReportStoreUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username, displayName string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, display_name) VALUES ($1, $2) RETURNING id
	`, username, displayName).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

const reportStoreSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  display_name TEXT
);
CREATE TABLE moderation_reports (
  id BIGSERIAL PRIMARY KEY,
  reporter_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  target_type TEXT NOT NULL CHECK (target_type IN ('topic', 'comment')),
  target_id BIGINT NOT NULL,
  reason_code TEXT NOT NULL CHECK (reason_code IN ('spam', 'abuse', 'illegal', 'off_topic', 'other')),
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'reviewing', 'resolved', 'rejected')),
  reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  review_note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX moderation_reports_open_unique_idx
  ON moderation_reports (reporter_user_id, target_type, target_id)
  WHERE status = 'open' AND reporter_user_id IS NOT NULL;`
