package moderation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
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
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusResolved,
		ReviewNote:       "handled",
		ExpectedRevision: report.Revision,
	})
	if err != nil {
		t.Fatalf("update report: %v", err)
	}
	if updated.ReporterName != "Reporter" || updated.ReviewerUserID == nil ||
		*updated.ReviewerUserID != reviewerID || updated.ReviewerName != "Reviewer" ||
		updated.Status != moderation.StatusResolved || updated.ReviewNote != "handled" || updated.ResolvedAt == nil {
		t.Fatalf("updated report projection = %#v", updated)
	}
	if report.Revision != 1 || updated.Revision != 2 {
		t.Fatalf("revision did not advance exactly once: created=%d updated=%d", report.Revision, updated.Revision)
	}
}

// 终态是审计事实：resolved/rejected 之后再更新，任何目标状态都必须被拒。
func TestPostgresStoreUpdateReportRejectsTerminalReport(t *testing.T) {
	ctx, pool := newReportStorePool(t)
	reporterID := insertReportStoreUser(t, ctx, pool, "reporter", "Reporter")
	reviewerID := insertReportStoreUser(t, ctx, pool, "reviewer", "Reviewer")
	secondReviewerID := insertReportStoreUser(t, ctx, pool, "reviewer2", "Reviewer Two")
	store := moderation.NewPostgresStore(pool)

	report := createReportStoreFixture(t, ctx, store, reporterID)
	closed, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusResolved,
		ReviewNote:       "handled",
		ExpectedRevision: report.Revision,
	})
	if err != nil {
		t.Fatalf("resolve report: %v", err)
	}
	closedAt := *closed.ResolvedAt

	for _, status := range []string{
		moderation.StatusOpen,
		moderation.StatusReviewing,
		moderation.StatusResolved,
		moderation.StatusRejected,
	} {
		_, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
			ReportID:         closed.ID,
			ReviewerUserID:   secondReviewerID,
			Status:           status,
			ReviewNote:       "rewritten",
			ExpectedRevision: closed.Revision,
		})
		if !errors.Is(err, moderation.ErrReportTerminal) {
			t.Fatalf("update closed report to %q = %v, want ErrReportTerminal", status, err)
		}
	}

	stored, err := store.GetReport(ctx, report.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if stored.Status != moderation.StatusResolved || stored.ReviewNote != "handled" ||
		stored.ReviewerUserID == nil || *stored.ReviewerUserID != reviewerID ||
		stored.ResolvedAt == nil || !stored.ResolvedAt.Equal(closedAt) ||
		stored.Revision != closed.Revision {
		t.Fatalf("closed report was rewritten: %#v", stored)
	}
}

// 版本 CAS：过期版本写不进去，并发两次同版本写只有一次命中。
func TestPostgresStoreUpdateReportRevisionIsCompareAndSwap(t *testing.T) {
	ctx, pool := newReportStorePool(t)
	reporterID := insertReportStoreUser(t, ctx, pool, "reporter", "Reporter")
	reviewerID := insertReportStoreUser(t, ctx, pool, "reviewer", "Reviewer")
	store := moderation.NewPostgresStore(pool)

	report := createReportStoreFixture(t, ctx, store, reporterID)
	claimed, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusReviewing,
		ReviewNote:       "claimed",
		ExpectedRevision: report.Revision,
	})
	if err != nil {
		t.Fatalf("claim report: %v", err)
	}
	if claimed.Revision != report.Revision+1 {
		t.Fatalf("claim revision = %d, want %d", claimed.Revision, report.Revision+1)
	}

	// 用上一版版本再写：必须冲突，且不能把已提交的备注洗掉。
	_, err = store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusOpen,
		ReviewNote:       "stale",
		ExpectedRevision: report.Revision,
	})
	if !errors.Is(err, moderation.ErrReportConflict) {
		t.Fatalf("stale revision update = %v, want ErrReportConflict", err)
	}
	stale, err := store.GetReport(ctx, report.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if stale.Status != moderation.StatusReviewing || stale.ReviewNote != "claimed" || stale.Revision != claimed.Revision {
		t.Fatalf("stale write leaked into the row: %#v", stale)
	}

	// 并发：两个写入者持有同一版本，只有一个能提交。
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 2)
	for index := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
				ReportID:         stale.ID,
				ReviewerUserID:   reviewerID,
				Status:           moderation.StatusReviewing,
				ReviewNote:       fmt.Sprintf("writer-%d", index),
				ExpectedRevision: stale.Revision,
			})
			results[index] = err
		}(index)
	}
	close(start)
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, moderation.ErrReportConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update returned %v, want nil or ErrReportConflict", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS = %d success / %d conflict, want 1/1", successes, conflicts)
	}
	final, err := store.GetReport(ctx, report.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if final.Revision != stale.Revision+1 {
		t.Fatalf("revision advanced %d times, want exactly 1", final.Revision-stale.Revision)
	}
}

// resolved_at 只写一次：已存在的结案时间不会被再次进入终态时覆盖。
func TestPostgresStoreUpdateReportResolvedAtIsWriteOnce(t *testing.T) {
	ctx, pool := newReportStorePool(t)
	reporterID := insertReportStoreUser(t, ctx, pool, "reporter", "Reporter")
	reviewerID := insertReportStoreUser(t, ctx, pool, "reviewer", "Reviewer")
	store := moderation.NewPostgresStore(pool)

	report := createReportStoreFixture(t, ctx, store, reporterID)
	firstResolved := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)
	// 历史脏行：活跃态却已有结案时间（旧代码把 resolved_at 双向摆动过）。
	if _, err := pool.Exec(ctx, `
		UPDATE moderation_reports SET resolved_at = $2 WHERE id = $1
	`, report.ID, firstResolved); err != nil {
		t.Fatalf("seed legacy resolved_at: %v", err)
	}

	closed, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusResolved,
		ReviewNote:       "handled",
		ExpectedRevision: report.Revision,
	})
	if err != nil {
		t.Fatalf("resolve report: %v", err)
	}
	if closed.ResolvedAt == nil || !closed.ResolvedAt.Equal(firstResolved) {
		t.Fatalf("resolved_at was rewritten on close: %v, want %v", closed.ResolvedAt, firstResolved)
	}

	if _, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   reviewerID,
		Status:           moderation.StatusRejected,
		ReviewNote:       "second opinion",
		ExpectedRevision: closed.Revision,
	}); !errors.Is(err, moderation.ErrReportTerminal) {
		t.Fatalf("second close = %v, want ErrReportTerminal", err)
	}
	stored, err := store.GetReport(ctx, report.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if stored.ResolvedAt == nil || !stored.ResolvedAt.Equal(firstResolved) {
		t.Fatalf("resolved_at changed after terminal rewrite attempt: %v", stored.ResolvedAt)
	}
}

// reviewer 归属：审核员只能填充空缺，改派是 moderation.manage 的动作。
func TestPostgresStoreUpdateReportKeepsFirstReviewerUnlessOverridden(t *testing.T) {
	ctx, pool := newReportStorePool(t)
	reporterID := insertReportStoreUser(t, ctx, pool, "reporter", "Reporter")
	firstReviewerID := insertReportStoreUser(t, ctx, pool, "reviewer", "Reviewer")
	secondReviewerID := insertReportStoreUser(t, ctx, pool, "reviewer2", "Reviewer Two")
	store := moderation.NewPostgresStore(pool)

	report := createReportStoreFixture(t, ctx, store, reporterID)
	claimed, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   firstReviewerID,
		Status:           moderation.StatusReviewing,
		ReviewNote:       "claimed",
		ExpectedRevision: report.Revision,
	})
	if err != nil {
		t.Fatalf("claim report: %v", err)
	}
	if claimed.ReviewerUserID == nil || *claimed.ReviewerUserID != firstReviewerID {
		t.Fatalf("claim reviewer = %v, want %d", claimed.ReviewerUserID, firstReviewerID)
	}

	annotated, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:         report.ID,
		ReviewerUserID:   secondReviewerID,
		Status:           moderation.StatusReviewing,
		ReviewNote:       "second look",
		ExpectedRevision: claimed.Revision,
	})
	if err != nil {
		t.Fatalf("annotate report: %v", err)
	}
	if annotated.ReviewerUserID == nil || *annotated.ReviewerUserID != firstReviewerID {
		t.Fatalf("review-only update overwrote reviewer: %v, want %d", annotated.ReviewerUserID, firstReviewerID)
	}
	if annotated.ReviewNote != "second look" {
		t.Fatalf("review note not applied: %q", annotated.ReviewNote)
	}

	reassigned, err := store.UpdateReport(ctx, moderation.UpdateReportInput{
		ReportID:              report.ID,
		ReviewerUserID:        secondReviewerID,
		Status:                moderation.StatusReviewing,
		ReviewNote:            "reassigned",
		ExpectedRevision:      annotated.Revision,
		AllowReviewerOverride: true,
	})
	if err != nil {
		t.Fatalf("reassign report: %v", err)
	}
	if reassigned.ReviewerUserID == nil || *reassigned.ReviewerUserID != secondReviewerID {
		t.Fatalf("manage override did not reassign reviewer: %v, want %d", reassigned.ReviewerUserID, secondReviewerID)
	}
}

func createReportStoreFixture(t *testing.T, ctx context.Context, store *moderation.PostgresStore, reporterID int64) moderation.Report {
	t.Helper()
	report, err := store.CreateReport(ctx, moderation.CreateReportInput{
		ReporterUserID: reporterID,
		TargetType:     moderation.TargetTypeTopic,
		TargetID:       4242,
		ReasonCode:     moderation.ReasonSpam,
		Body:           "fixture",
	})
	if err != nil {
		t.Fatalf("create report fixture: %v", err)
	}
	return report
}

func newReportStorePool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)

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

// 与 202607070006_moderation_reports.sql 及 202611150001_moderation_report_revision.sql
// 保持一致：fixture 复刻真实列集，避免投影字段与 schema 漂移时测试仍然绿。
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
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
  reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  review_note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX moderation_reports_open_unique_idx
  ON moderation_reports (reporter_user_id, target_type, target_id)
  WHERE status = 'open' AND reporter_user_id IS NOT NULL;`
