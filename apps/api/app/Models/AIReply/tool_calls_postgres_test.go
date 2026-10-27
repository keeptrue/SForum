package aireply_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这一组把工具轨迹的落库形态钉在真实 PostgreSQL 上。内存 fake 证明不了三件事：
// 幂等键真的生效（River 重试不产生重复行）、顺序真的是 seq、评论删除真的级联
// 清掉记录——而这三件正是「用户看到的那条记录」可信的全部依据。
//
// 与 citations_postgres_test.go 同一套隔离策略：独立 schema + search_path，
// 测试结束整体 DROP，不触碰开发库的公开表。
func TestPostgresToolCallsRoundTrip(t *testing.T) {
	ctx, pool := newToolCallPostgresHarness(t)
	store := aireply.NewPostgresToolCallStore(pool)

	calls := []aireply.ToolCall{
		{Seq: 0, Step: 1, Tool: "web-search", Arguments: `{"query":"SForum"}`, ArgumentsBytes: 20, ResultBytes: 900, ResultDigest: "摘要", LatencyMS: 120},
		{Seq: 1, Step: 1, Tool: "forum-search", Arguments: `{"query":"SForum"}`, Failure: "not_allowed", LatencyMS: 3},
	}
	if err := store.Record(ctx, 501, 10, "run-abc", calls); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// 重放同一批（River 重试的形态）：幂等键必须吞掉重复。
	if err := store.Record(ctx, 501, 10, "run-abc", calls); err != nil {
		t.Fatalf("Record (replay): %v", err)
	}

	items, err := store.ListBySourceComment(ctx, 501)
	if err != nil {
		t.Fatalf("ListBySourceComment: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("重放不得产生重复行：%+v", items)
	}
	if items[0].Seq != 0 || items[0].Tool != "web-search" || items[1].Seq != 1 {
		t.Fatalf("顺序必须是 seq：%+v", items)
	}
	if items[0].ResultDigest != "摘要" || items[0].LatencyMS != 120 {
		t.Fatalf("字段未完整回读：%+v", items[0])
	}
	// 被拒绝的调用也落库：它是运营者判断「模型在撞白名单」的唯一证据。
	if items[1].Failure != "not_allowed" {
		t.Fatalf("拒绝结局必须保留：%+v", items[1])
	}

	// 按 run 回放拿到同一批。
	byRun, err := store.ListByRun(ctx, "run-abc")
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(byRun) != 2 {
		t.Fatalf("byRun = %+v", byRun)
	}
}

// 评论被真删时记录必须一起消失：否则公开读会拿到指向不存在评论的行，或者更糟，
// 一个删掉的楼层在别的查询路径上重新出现。
func TestPostgresToolCallsCascadeWithTheComment(t *testing.T) {
	ctx, pool := newToolCallPostgresHarness(t)
	store := aireply.NewPostgresToolCallStore(pool)

	if err := store.Record(ctx, 501, 10, "run-abc", []aireply.ToolCall{{Seq: 0, Tool: "web-search"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM comments WHERE id = 501`); err != nil {
		t.Fatalf("delete comment: %v", err)
	}
	items, err := store.ListBySourceComment(ctx, 501)
	if err != nil {
		t.Fatalf("ListBySourceComment: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("级联删除后不该留下孤儿行：%+v", items)
	}
}

// 没有工具名的行不该落库：它解释不了任何事。
func TestPostgresToolCallsSkipUnnamedEntries(t *testing.T) {
	ctx, pool := newToolCallPostgresHarness(t)
	store := aireply.NewPostgresToolCallStore(pool)
	if err := store.Record(ctx, 501, 10, "run-abc", []aireply.ToolCall{{Seq: 0, Tool: "  "}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	items, err := store.ListBySourceComment(ctx, 501)
	if err != nil {
		t.Fatalf("ListBySourceComment: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v", items)
	}
}

// 非法产出评论 id 是调用方的编程错误，必须报错而不是静默丢弃。
func TestPostgresToolCallsRequireTheProducedComment(t *testing.T) {
	ctx, pool := newToolCallPostgresHarness(t)
	store := aireply.NewPostgresToolCallStore(pool)
	if err := store.Record(ctx, 0, 10, "run-abc", []aireply.ToolCall{{Seq: 0, Tool: "web-search"}}); err == nil {
		t.Fatal("commentID=0 must be rejected")
	}
}

func newToolCallPostgresHarness(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aireply_tool_calls_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	statements := []string{
		`CREATE TABLE comments (
			id BIGINT PRIMARY KEY,
			topic_id BIGINT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active'
		)`,
		`INSERT INTO comments (id) VALUES (501)`,
		// 与迁移 202610190005 同形（含幂等约束与级联）。
		`CREATE TABLE ai_tool_calls (
			id BIGSERIAL PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			run_id TEXT NOT NULL DEFAULT '',
			topic_id BIGINT NOT NULL DEFAULT 0,
			comment_id BIGINT NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
			seq INT NOT NULL,
			step INT NOT NULL DEFAULT 0,
			tool TEXT NOT NULL,
			arguments TEXT NOT NULL DEFAULT '',
			arguments_bytes INT NOT NULL DEFAULT 0,
			result_bytes INT NOT NULL DEFAULT 0,
			result_digest TEXT NOT NULL DEFAULT '',
			is_error BOOLEAN NOT NULL DEFAULT false,
			failure TEXT NOT NULL DEFAULT '',
			latency_ms INT NOT NULL DEFAULT 0,
			CONSTRAINT ai_tool_calls_comment_seq_key UNIQUE (comment_id, seq)
		)`,
		`CREATE INDEX ai_tool_calls_comment_idx ON ai_tool_calls (comment_id, seq)`,
		`CREATE INDEX ai_tool_calls_run_idx ON ai_tool_calls (run_id, created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}
	return ctx, pool
}
