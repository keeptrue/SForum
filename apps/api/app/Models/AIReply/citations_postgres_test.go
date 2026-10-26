package aireply_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
)

// 这一组把 B4/B5 的落库形态钉在真实 PostgreSQL 上：内存 fake 证明不了
// 「run_id 真的写进去了」「乐观锁真的放弃了写入」「引文真的来自数据库正文」——
// 而这三件事正是运营者事后能解释一次回复的全部依据。
//
// 与 postgres_reader_postgres_test.go 同一套隔离策略：独立 schema + search_path，
// 测试结束整体 DROP，不触碰开发库的公开表。

func TestPostgresCitationsRoundTripWithRunID(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)

	// 校验：评论引用的引文取自 posts.plain_text，不是模型输出。
	verified, err := store.Verify(ctx, []aireply.CitationRef{
		{CommentID: 502},
		{TopicID: 10},
		{CommentID: 999}, // 不存在
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 2 {
		t.Fatalf("verified = %+v (an unknown floor must be dropped)", verified)
	}
	// 评论引用把所属主题补全出来，前端才能拼出可点地址。
	if verified[0].TopicID != 10 || verified[0].CommentID != 502 {
		t.Fatalf("comment citation = %+v", verified[0])
	}
	if verified[0].Excerpt != "池子设 20 就行" {
		t.Fatalf("excerpt must come from the stored body, got %q", verified[0].Excerpt)
	}
	// 主题级引用保留 CommentID=0，前端据此不加 #comment- 锚点。
	if verified[1].TopicID != 10 || verified[1].CommentID != 0 {
		t.Fatalf("topic citation = %+v", verified[1])
	}

	// 落库：带 run id 与产出评论 id。
	if err := store.Record(ctx, 501, "run-deadbeef", verified); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// 读回：顺序即写入顺序，字段完整。
	items, err := store.ListBySourceComment(ctx, 501)
	if err != nil {
		t.Fatalf("ListBySourceComment: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].CommentID != 502 || items[1].CommentID != 0 {
		t.Fatalf("order = %+v", items)
	}
	// run_id 真的落了：这是 A3 与 B4 的连接点，也是「这次回复对应哪次 run」的唯一证据。
	var runID string
	if err := pool.QueryRow(ctx, `SELECT run_id FROM ai_citations WHERE source_comment_id = 501 ORDER BY id LIMIT 1`).Scan(&runID); err != nil {
		t.Fatalf("read run_id: %v", err)
	}
	if runID != "run-deadbeef" {
		t.Fatalf("run_id = %q, want the run that produced this reply", runID)
	}

	// 零引用不写行：一条普通回复不该在表里留下任何痕迹。
	if err := store.Record(ctx, 501, "run-deadbeef", nil); err != nil {
		t.Fatalf("Record(nil): %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_citations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("a nil citation list must not insert anything: count=%d", count)
	}
}

// 引用的可见性谓词必须与主题/评论读路径一致：隐藏主题、隐藏分类、非 active 评论
// 一律不可引用（并且不可区分「不存在」与「不可见」）。
func TestPostgresCitationsRefuseInvisibleTargets(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)

	verified, err := store.Verify(ctx, []aireply.CitationRef{
		{TopicID: 11},    // status=hidden
		{TopicID: 12},    // 分类不可见
		{CommentID: 504}, // status=hidden
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(verified) != 0 {
		t.Fatalf("invisible targets must not be citable: %+v", verified)
	}
}

// B5 三重失效第 3 条的存储侧：楼层被转不可见后，复校验必须判定它失效。
func TestPostgresVerifyFactsDropsOnlyTheHiddenFloor(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresCitationStore(pool)

	facts := []aireply.Fact{
		{TopicID: 10, CommentID: 500, Excerpt: "可见楼层"},
		{TopicID: 10, CommentID: 504, Excerpt: "待隐藏楼层"},
	}
	alive, err := store.VerifyFacts(ctx, facts)
	if err != nil {
		t.Fatalf("VerifyFacts: %v", err)
	}
	if len(alive) != 2 {
		t.Fatalf("alive = %+v", alive)
	}
	if !alive[0] {
		t.Fatal("a visible floor must stay valid")
	}
	if alive[1] {
		t.Fatal("a hidden floor must be invalidated")
	}
}

// 乐观锁的两种路径都在真库上验证：新写入走 insert-only，冲突时放弃而不是覆盖。
func TestPostgresTopicStateUsesOptimisticLocking(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresTopicStateStore(pool)

	// 首次写入：revision=0 → INSERT。
	first := aireply.TopicState{
		LastRunID:     "run-1",
		PromptVersion: "forum-reply@8",
		VerifiedFacts: []aireply.Fact{{TopicID: 10, CommentID: 500, Excerpt: "事实", VerifiedAt: time.Now()}},
		UpdatedAt:     time.Now(),
	}
	if err := store.Save(ctx, 10, 77, first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	loaded, ok := store.Load(ctx, 10, 77)
	if !ok {
		t.Fatal("the state must be readable after the first write")
	}
	if loaded.Revision != 1 {
		t.Fatalf("revision = %d, want 1", loaded.Revision)
	}
	if loaded.LastRunID != "run-1" || len(loaded.VerifiedFacts) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}

	// 第二次写入：带着读到的 revision 更新成功。
	loaded.LastRunID = "run-2"
	if err := store.Save(ctx, 10, 77, loaded); err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	again, _ := store.Load(ctx, 10, 77)
	if again.Revision != 2 || again.LastRunID != "run-2" {
		t.Fatalf("after update: %+v", again)
	}

	// 陈旧 revision 写入必须被放弃（后写者不一定是更正确的那个）。
	stale := again
	stale.Revision = 1
	stale.LastRunID = "run-stale"
	if err := store.Save(ctx, 10, 77, stale); err != nil {
		t.Fatalf("a lost write must not surface as an error: %v", err)
	}
	final, _ := store.Load(ctx, 10, 77)
	if final.LastRunID != "run-2" {
		t.Fatalf("a stale revision must not overwrite: lastRunId=%q", final.LastRunID)
	}
	if final.Revision != 2 {
		t.Fatalf("revision moved on a lost write: %d", final.Revision)
	}

	// 并发插入冲突（revision=0 但行已存在）同样放弃。
	duplicate := aireply.TopicState{LastRunID: "run-dup", PromptVersion: "forum-reply@8", UpdatedAt: time.Now()}
	if err := store.Save(ctx, 10, 77, duplicate); err != nil {
		t.Fatalf("Save(duplicate): %v", err)
	}
	kept, _ := store.Load(ctx, 10, 77)
	if kept.LastRunID != "run-2" {
		t.Fatalf("an insert conflict must not overwrite: lastRunId=%q", kept.LastRunID)
	}
}

// 读取容错：document 不是合法 JSON 时按「没有状态」处理，不 panic、不返回错误。
func TestPostgresTopicStateTreatsCorruptDocumentAsStateless(t *testing.T) {
	ctx, pool := newCitationPostgresHarness(t)
	store := aireply.NewPostgresTopicStateStore(pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_topic_state (topic_id, bot_user_id, document, revision, updated_at)
		VALUES (10, 77, '{"verifiedFacts": "not-an-array"}'::jsonb, 4, now())
	`); err != nil {
		t.Fatalf("seed corrupt document: %v", err)
	}
	state, ok := store.Load(ctx, 10, 77)
	if ok {
		t.Fatalf("a corrupt document must read as stateless: %+v", state)
	}
	// 容错不等于删数据：坏行仍在，下次写入会覆盖它（这里只断言读取行为）。
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM ai_topic_state WHERE topic_id = 10 AND bot_user_id = 77`).Scan(&revision); err != nil {
		t.Fatalf("the corrupt row must not be deleted on read: %v", err)
	}
	if revision != 4 {
		t.Fatalf("revision = %d", revision)
	}

	// 重建：读取失败后 revision 未知，写入走 insert-only → 与既有行冲突 → 放弃。
	// 「宁可这一轮不写，也不覆盖别人」是精确的行为，不是缺陷。
	rebuild := aireply.TopicState{LastRunID: "run-rebuild", PromptVersion: "forum-reply@8", UpdatedAt: time.Now()}
	if err := store.Save(ctx, 10, 77, rebuild); err != nil {
		t.Fatalf("Save(rebuild): %v", err)
	}
	var lastRunID string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(document->>'lastRunId', '') FROM ai_topic_state WHERE topic_id = 10 AND bot_user_id = 77
	`).Scan(&lastRunID); err != nil {
		t.Fatal(err)
	}
	if lastRunID != "" {
		t.Fatalf("an insert-only write must not overwrite a readable-but-corrupt row: %q", lastRunID)
	}
}

// 文档形状契约：TopicState / Fact / ReplyRef 的 JSON 字段名进了 JSONB 列，
// 改名会让已有行读不出来。
func TestTopicStateJSONContractIsStable(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	state := aireply.TopicState{
		LastRunID:     "run-1",
		PromptVersion: "forum-reply@8",
		VerifiedFacts: []aireply.Fact{{TopicID: 10, CommentID: 500, Excerpt: "事实", VerifiedAt: now}},
		LastReplies:   []aireply.ReplyRef{{CommentID: 501, RunID: "run-1", PostedAt: now}},
		UpdatedAt:     now,
		Revision:      3,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"lastRunId":"run-1"`, `"promptVersion":"forum-reply@8"`,
		`"verifiedFacts":[`, `"topicId":10`, `"commentId":500`, `"excerpt":"事实"`, `"verifiedAt"`,
		`"lastReplies":[`, `"postedAt"`, `"updatedAt"`, `"revision":3`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("topic state JSON %s is missing %s", encoded, want)
		}
	}
}

// newCitationPostgresHarness 建一个隔离 schema，只包含 B4/B5 需要的表。
//
// 它刻意手工建表而不跑迁移：迁移文件的存在性由 `go run ./cmd/migrate` 与
// goose 的 Up/Down 往返单独验证；这里要验证的是**读写代码**与表结构契约一致。
func newCitationPostgresHarness(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aireply_citations_%d", time.Now().UnixNano())
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
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'human')`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, visibility TEXT NOT NULL)`,
		`CREATE TABLE posts (id BIGINT PRIMARY KEY, plain_text TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE topics (
			id BIGINT PRIMARY KEY, category_id BIGINT NOT NULL, author_user_id BIGINT,
			content_id BIGINT NOT NULL, title TEXT NOT NULL, status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ
		)`,
		// 与生产表一致地使用序列：端到端用例要真的插评论（ai_citations 的外键
		// 指向 comments），没有序列就插不进去。
		`CREATE SEQUENCE comments_id_seq START 9000`,
		`CREATE TABLE comments (
			id BIGINT PRIMARY KEY DEFAULT nextval('comments_id_seq'),
			topic_id BIGINT NOT NULL, content_id BIGINT NOT NULL,
			author_user_id BIGINT, status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL
		)`,
		// 与迁移 202610190002 同形（含 source_comment_id）。
		`CREATE TABLE ai_citations (
			id BIGSERIAL PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			run_id TEXT NOT NULL DEFAULT '',
			topic_id BIGINT NOT NULL DEFAULT 0,
			comment_id BIGINT NOT NULL DEFAULT 0,
			excerpt TEXT NOT NULL DEFAULT '',
			source_comment_id BIGINT REFERENCES comments(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX ai_citations_run_idx ON ai_citations (run_id, created_at DESC)`,
		`CREATE INDEX ai_citations_target_idx ON ai_citations (topic_id, comment_id)`,
		`CREATE INDEX ai_citations_source_idx ON ai_citations (source_comment_id, id)`,
		// 与迁移 202610190003 同形。
		`CREATE TABLE ai_topic_state (
			topic_id BIGINT NOT NULL, bot_user_id BIGINT NOT NULL,
			document JSONB NOT NULL, revision BIGINT NOT NULL DEFAULT 1,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (topic_id, bot_user_id)
		)`,
		`INSERT INTO users (id, username, display_name, kind) VALUES
			(1, 'alice', 'Alice', 'human'), (2, 'bob', 'Bob', 'human'),
			(3, 'sforum_ai', 'sforum_ai', 'bot')`,
		`INSERT INTO categories (id, visibility) VALUES (1, 'public'), (2, 'hidden')`,
		`INSERT INTO posts (id, plain_text) VALUES
			(100, '线上 QPS 很高'), (101, '池子设 20 就行'), (102, '待隐藏楼层'), (103, '待审发言')`,
		`INSERT INTO topics (id, category_id, author_user_id, content_id, title, status) VALUES
			(10, 1, 1, 100, '连接池怎么配', 'active'),
			(11, 1, 1, 100, '隐藏主题', 'hidden'),
			(12, 2, 1, 100, '隐藏分类主题', 'active')`,
		`INSERT INTO comments (id, topic_id, content_id, author_user_id, status, created_at) VALUES
			(500, 10, 101, 2, 'active', '2026-10-01T10:00:00Z'),
			(501, 10, 100, 3, 'active', '2026-10-01T10:05:00Z'),
			(502, 10, 101, 2, 'active', '2026-10-01T10:10:00Z'),
			(504, 10, 102, 2, 'hidden', '2026-10-01T10:20:00Z')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup citation schema: %v\n%s", err, statement)
		}
	}
	return ctx, pool
}
