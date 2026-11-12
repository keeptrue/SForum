package badges

import (
	"context"
	"strings"
	"testing"
)

// 回扫状态表的**真库**测试。
//
// fake 证明不了这几条：`UPDATE ... WHERE id = (SELECT ... ORDER BY started_at DESC, id DESC)`
// 到底命中哪一行（多行时选错行 = 进度写在历史 run 上）、成功是否真的把 failed 翻回
// running（SQL 里的 error=” / failed_cursor_user_id=0）、DISTINCT ON 是否真的每个
// 徽章只回一行、以及 FK CASCADE 删徽章后行是否消失。
//
// DDL 与迁移 `202611120003_badge_backfill_runs.sql` 同形。迁移本身（up/down/owner）
// 由 `scripts/verify-migration-reversible.sh` 物理验证，这里只验证查询语义。
const badgeBackfillRunsSchemaSQL = `
CREATE TABLE badge_rule_backfill_runs (
  id BIGSERIAL PRIMARY KEY,
  badge_id BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
  last_user_id BIGINT NOT NULL DEFAULT 0,
  failed_cursor_user_id BIGINT NOT NULL DEFAULT 0,
  batch INT,
  error TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ
);
CREATE INDEX badge_rule_backfill_runs_badge_started_idx
  ON badge_rule_backfill_runs (badge_id, started_at DESC, id DESC);
`

func newBackfillRunsFixture(t *testing.T) (*badgesPGFixture, *PostgresBackfillRuns) {
	t.Helper()
	fixture := newBadgesPGFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, badgeBackfillRunsSchemaSQL); err != nil {
		t.Fatalf("apply badge_rule_backfill_runs DDL: %v", err)
	}
	return fixture, NewPostgresBackfillRuns(fixture.pool)
}

func latestRun(t *testing.T, fixture *badgesPGFixture, runs *PostgresBackfillRuns) BackfillRunStatus {
	t.Helper()
	items, err := runs.ListLatestBackfillRuns(context.Background())
	if err != nil {
		t.Fatalf("ListLatestBackfillRuns: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("期望每个徽章一行，得到 %+v", items)
	}
	return items[0]
}

// TestBackfillRunsEmptyIsEmptySlice 断言没有任何 run 时返回空切片（不是 nil、不是错误）。
func TestBackfillRunsEmptyIsEmptySlice(t *testing.T) {
	_, runs := newBackfillRunsFixture(t)
	items, err := runs.ListLatestBackfillRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("空表必须回空切片，得到 %+v", items)
	}
}

// TestBackfillRunsProgressLifecycle 走完整状态机：running → running(新游标) →
// failed(带游标) → running(翻回，失败位置清零) → completed(finished_at 落值)。
func TestBackfillRunsProgressLifecycle(t *testing.T) {
	fixture, runs := newBackfillRunsFixture(t)
	badgeID := fixture.badge(t, "ten-topics", "gold", 0, "topic_count", 10)
	ctx := context.Background()

	if err := runs.StartBackfillRun(ctx, badgeID, 500); err != nil {
		t.Fatalf("StartBackfillRun: %v", err)
	}
	run := latestRun(t, fixture, runs)
	if run.Status != BackfillStatusRunning || run.BadgeID != badgeID {
		t.Fatalf("入队侧建行应为 running，得到 %+v", run)
	}
	// slug/name 来自 JOIN：没有它们运营只看到一串数字。
	if run.Slug != "ten-topics" || run.Name != "ten-topics" {
		t.Fatalf("必须 JOIN 出徽章 slug/name，得到 %q / %q", run.Slug, run.Name)
	}
	if run.LastUserID != 0 || run.FinishedAt != nil {
		t.Fatalf("新 run 不应有游标或结束时间，得到 %+v", run)
	}

	if err := runs.AdvanceBackfillRun(ctx, badgeID, 500, false); err != nil {
		t.Fatalf("AdvanceBackfillRun: %v", err)
	}
	run = latestRun(t, fixture, runs)
	if run.Status != BackfillStatusRunning || run.LastUserID != 500 || run.FinishedAt != nil {
		t.Fatalf("续页应保持 running 且推进游标，得到 %+v", run)
	}

	if err := runs.FailBackfillRun(ctx, badgeID, 500, "db down"); err != nil {
		t.Fatalf("FailBackfillRun: %v", err)
	}
	run = latestRun(t, fixture, runs)
	if run.Status != BackfillStatusFailed || run.FailedCursorUserID != 500 || run.Error != "db down" {
		t.Fatalf("失败页必须带游标与原因，得到 %+v", run)
	}
	// 失败不是结束：river 还会重试，所以 finished_at 必须留空。
	if run.FinishedAt != nil {
		t.Fatalf("failed 不应写 finished_at，得到 %v", run.FinishedAt)
	}

	if err := runs.AdvanceBackfillRun(ctx, badgeID, 900, false); err != nil {
		t.Fatalf("重试成功后 Advance: %v", err)
	}
	run = latestRun(t, fixture, runs)
	if run.Status != BackfillStatusRunning || run.LastUserID != 900 {
		t.Fatalf("重试成功必须翻回 running 并推进游标，得到 %+v", run)
	}
	if run.Error != "" || run.FailedCursorUserID != 0 {
		t.Fatalf("翻回 running 时 error/failedCursorUserId 必须清零，得到 %q / %d", run.Error, run.FailedCursorUserID)
	}

	if err := runs.AdvanceBackfillRun(ctx, badgeID, 1200, true); err != nil {
		t.Fatalf("末页 Advance: %v", err)
	}
	run = latestRun(t, fixture, runs)
	if run.Status != BackfillStatusCompleted || run.LastUserID != 1200 || run.FinishedAt == nil {
		t.Fatalf("末页必须落 completed + finished_at，得到 %+v", run)
	}
}

// TestBackfillRunsKeepsOnlyLatestRowPerBadge 断言同一徽章重跑会新增历史行，
// 但读端点只回最新一行——否则运营看到的是「同一枚徽章三条 running」。
func TestBackfillRunsKeepsOnlyLatestRowPerBadge(t *testing.T) {
	fixture, runs := newBackfillRunsFixture(t)
	badgeID := fixture.badge(t, "ten-topics", "gold", 0, "topic_count", 10)
	other := fixture.badge(t, "first-topic", "bronze", 1, "topic_count", 1)
	ctx := context.Background()

	if err := runs.StartBackfillRun(ctx, badgeID, 500); err != nil {
		t.Fatal(err)
	}
	if err := runs.AdvanceBackfillRun(ctx, badgeID, 500, true); err != nil {
		t.Fatal(err)
	}
	if err := runs.StartBackfillRun(ctx, badgeID, 500); err != nil {
		t.Fatal(err)
	}

	var total int64
	if err := fixture.pool.QueryRow(ctx,
		`SELECT count(*) FROM badge_rule_backfill_runs WHERE badge_id = $1`, badgeID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("两次回扫应留两行历史，得到 %d", total)
	}

	items, err := runs.ListLatestBackfillRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != BackfillStatusRunning || items[0].LastUserID != 0 {
		t.Fatalf("读端点必须只回最新一行且是新 run，得到 %+v", items)
	}

	// 不同徽章各一行。
	if err := runs.StartBackfillRun(ctx, other, 500); err != nil {
		t.Fatal(err)
	}
	items, err = runs.ListLatestBackfillRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("两枚徽章应各回一行，得到 %+v", items)
	}
}

// TestBackfillRunsVanishWithBadge 断言 FK CASCADE：徽章被删后进度行一起消失，
// 读端点不会回孤儿行（JOIN badges 也会滤掉它，但行本身必须没）。
func TestBackfillRunsVanishWithBadge(t *testing.T) {
	fixture, runs := newBackfillRunsFixture(t)
	badgeID := fixture.badge(t, "ten-topics", "gold", 0, "topic_count", 10)
	ctx := context.Background()

	if err := runs.StartBackfillRun(ctx, badgeID, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM badges WHERE id = $1`, badgeID); err != nil {
		t.Fatal(err)
	}
	items, err := runs.ListLatestBackfillRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("徽章删除后进度行应消失，得到 %+v", items)
	}
}

// TestBackfillRunsClampStoredError 断言入库的 error 按字符截断到 500。
func TestBackfillRunsClampStoredError(t *testing.T) {
	fixture, runs := newBackfillRunsFixture(t)
	badgeID := fixture.badge(t, "ten-topics", "gold", 0, "topic_count", 10)
	ctx := context.Background()

	if err := runs.StartBackfillRun(ctx, badgeID, 500); err != nil {
		t.Fatal(err)
	}
	if err := runs.FailBackfillRun(ctx, badgeID, 500, strings.Repeat("错", MaxBackfillErrorLength+10)); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT error FROM badge_rule_backfill_runs WHERE badge_id = $1`, badgeID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len([]rune(stored)) != MaxBackfillErrorLength {
		t.Fatalf("入库 error 长度 = %d，want %d", len([]rune(stored)), MaxBackfillErrorLength)
	}
}
