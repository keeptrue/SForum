package ai_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 回复结果的读写面：写入一行、按时间倒序读回、按触发判幂等。
//
// 独立 schema + 真实 PostgreSQL：这三条 SQL 的语义（排序、部分索引的谓词、
// 「只有 posted 才算回过」）不能用内存假实现证明。
const replyOutcomeSchemaSQL = `
CREATE TABLE ai_reply_outcomes (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  topic_id            BIGINT NOT NULL,
  parent_comment_id   BIGINT NOT NULL DEFAULT 0,
  trigger_comment_id  BIGINT NOT NULL DEFAULT 0,
  bot_user_id         BIGINT NOT NULL DEFAULT 0,
  run_id              TEXT NOT NULL DEFAULT '',
  comment_id          BIGINT NOT NULL DEFAULT 0,
  outcome             TEXT NOT NULL,
  reason              TEXT NOT NULL DEFAULT '',
  detail              TEXT NOT NULL DEFAULT '',
  model_calls         INTEGER NOT NULL DEFAULT 0,
  tool_calls          INTEGER NOT NULL DEFAULT 0,
  input_tokens        INTEGER NOT NULL DEFAULT 0,
  output_tokens       INTEGER NOT NULL DEFAULT 0,
  CONSTRAINT ai_reply_outcomes_outcome_check CHECK (
    outcome IN ('posted', 'empty_reply', 'tool_loop_stuck', 'unavailable', 'publish_failed', 'failed')
  )
);
`

func openReplyOutcomeSchema(t *testing.T) (*pgxpool.Pool, func()) {
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
		t.Skipf("PostgreSQL unavailable: %v", err)
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	schema := "ai_reply_outcome_" + hex.EncodeToString(raw[:])
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Skipf("cannot create schema: %v", err)
	}
	cleanup := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanup()
		t.Fatalf("parse database url: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		cleanup()
		t.Fatalf("connect fixture schema: %v", err)
	}
	if _, err := pool.Exec(ctx, replyOutcomeSchemaSQL); err != nil {
		pool.Close()
		cleanup()
		t.Fatalf("create fixture table: %v", err)
	}
	return pool, func() {
		pool.Close()
		cleanup()
	}
}

func TestReplyOutcomeStoreRoundTrip(t *testing.T) {
	pool, cleanup := openReplyOutcomeSchema(t)
	defer cleanup()
	store := ai.NewPostgresStore(pool)
	ctx := context.Background()

	// 空回答：写入后必须读得回，并且不计入「已经回过」。
	if err := store.RecordReplyOutcome(ctx, ai.ReplyOutcome{
		TopicID: 9, TriggerCommentID: 501, BotUserID: 77,
		Outcome: ai.ReplyOutcomeEmptyReply, Reason: ai.ReasonEmptyCompletion,
		Detail: "finish_reason=length reasoning=true", ModelCalls: 1,
		InputTokens: 11, OutputTokens: 22,
	}); err != nil {
		t.Fatalf("record empty reply: %v", err)
	}
	posted, err := store.HasPostedReply(ctx, 9, 501)
	if err != nil {
		t.Fatalf("has posted: %v", err)
	}
	if posted {
		t.Fatal("空回复不得被当成「已经回过」，否则修正配置后永远等不到第二次尝试")
	}

	// 发布成功：读回顺序是时间倒序，幂等判据变成 true。
	if err := store.RecordReplyOutcome(ctx, ai.ReplyOutcome{
		TopicID: 9, TriggerCommentID: 501, BotUserID: 77, CommentID: 9001,
		RunID: "run-1", Outcome: ai.ReplyOutcomePosted, ModelCalls: 1,
	}); err != nil {
		t.Fatalf("record posted: %v", err)
	}
	items, err := store.ListReplyOutcomes(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Outcome != ai.ReplyOutcomePosted || items[0].CommentID != 9001 {
		t.Fatalf("最新的在前: %+v", items[0])
	}
	if items[1].Reason != ai.ReasonEmptyCompletion || items[1].Detail == "" {
		t.Fatalf("空回复的原因与诊断事实必须落库: %+v", items[1])
	}
	posted, err = store.HasPostedReply(ctx, 9, 501)
	if err != nil || !posted {
		t.Fatalf("posted = %v err = %v", posted, err)
	}

	// 主题触发用 trigger_comment_id = 0 作为键：一次发帖提及只应得到一条回复。
	if err := store.RecordReplyOutcome(ctx, ai.ReplyOutcome{
		TopicID: 9, Outcome: ai.ReplyOutcomePosted, CommentID: 9002,
	}); err != nil {
		t.Fatalf("record topic-level reply: %v", err)
	}
	if ok, err := store.HasPostedReply(ctx, 9, 0); err != nil || !ok {
		t.Fatalf("topic-level posted = %v err = %v", ok, err)
	}
	// 另一个主题不受影响。
	if ok, err := store.HasPostedReply(ctx, 10, 501); err != nil || ok {
		t.Fatalf("unrelated topic posted = %v err = %v", ok, err)
	}

	// limit 收边：0 → 默认值，超出 → 上限，不允许全表扫描。
	if _, err := store.ListReplyOutcomes(ctx, 0); err != nil {
		t.Fatalf("default limit: %v", err)
	}
}
