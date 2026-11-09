package moderation_test

// 审核动作之后 hot_score 是否保住点赞贡献的真实 PostgreSQL 集成测试。
//
// fixture 与 notification_fanout_postgres_integration_test.go 同构：一次性集群
// + 每个测试独立 schema（search_path），DDL 逐表镜像迁移，而不是跑整条迁移链。
// 镜像的迁移：
//   - 基础论坛表（users/categories/posts/topics/comments）与 moderation_decisions
//     与 notification_fanout_postgres_integration_test.go 的 fixture 保持一致；
//   - topics.like_count / comments.like_count / content_reactions 镜像
//     database/migrations/202610280001_content_reactions.sql（第 9-38 行）。
// hot_score 的权威公式在 app/Models/Forum/hot_score.go：comment_count*5 + view_count + like_count*3。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type reactionHotScoreFixture struct {
	ctx   context.Context
	admin *pgx.Conn
	pool  *pgxpool.Pool
}

// reactionHotScoreCounters 是一次审核动作前后 topics 上的四个反规范化列。
type reactionHotScoreCounters struct {
	commentCount, viewCount, likeCount, hotScore int64
}

// formula 是 hot_score 的权威公式，测试用它同时校验「没有丢信号」和「没有重复计分」。
func (c reactionHotScoreCounters) formula() int64 {
	return c.commentCount*5 + c.viewCount + c.likeCount*3
}

func TestModerationApproveKeepsTopicLikeContribution(t *testing.T) {
	f := newReactionHotScoreFixture(t)
	authorID := f.insertUser(t, "hot_approve_author", "active")
	reviewerID := f.insertUser(t, "hot_approve_reviewer", "active")
	// comment_count=0, view_count=7, like_count=2 → hot_score=0*5+7+2*3=13。
	topicID := f.seedTopicWithLikeContribution(t, authorID, 0, 7, 2)
	commentID := f.insertPendingComment(t, topicID, authorID)

	before := f.topicCounters(t, topicID)
	if before.hotScore != 13 || before.likeCount != 2 {
		t.Fatalf("fixture counters=%+v want hot_score=13 like_count=2", before)
	}
	if got := f.reactionRowCount(t, topicID); got != 2 {
		t.Fatalf("fixture content_reactions rows=%d want 2", got)
	}

	store := moderation.NewPostgresStore(f.pool)
	if _, err := store.SubmitDecision(f.ctx, moderation.DecisionInput{
		Source:         moderation.SourcePrePublish,
		TargetType:     moderation.TargetTypeComment,
		TargetID:       commentID,
		Action:         moderation.ActionApprove,
		ReviewerUserID: reviewerID,
	}); err != nil {
		t.Fatalf("approve pending comment: %v", err)
	}
	f.assertCommentStatus(t, commentID, "active")
	if got := f.decisionCount(t, commentID); got != 1 {
		t.Fatalf("approve wrote %d decisions, want 1", got)
	}

	after := f.topicCounters(t, topicID)
	// 审核通过 +1 条评论（+5），view_count 不变（+7），点赞贡献必须保留（+6）。
	want := int64(1)*5 + 7 + 2*3
	if after.hotScore != want {
		t.Fatalf("approve rewrote hot_score=%d want=%d (like contribution %d dropped?)", after.hotScore, want, after.likeCount*3)
	}
	if after.commentCount != 1 || after.viewCount != 7 || after.likeCount != 2 {
		t.Fatalf("approve counters=%+v want comment_count=1 view_count=7 like_count=2", after)
	}
	if after.hotScore != after.formula() {
		t.Fatalf("approve left hot_score=%d inconsistent with formula %d (%+v)", after.hotScore, after.formula(), after)
	}
	if got := f.reactionRowCount(t, topicID); got != 2 {
		t.Fatalf("moderation approve touched content_reactions rows: %d want 2", got)
	}
}

func TestModerationRejectKeepsTopicLikeContribution(t *testing.T) {
	f := newReactionHotScoreFixture(t)
	authorID := f.insertUser(t, "hot_reject_author", "active")
	reviewerID := f.insertUser(t, "hot_reject_reviewer", "active")
	topicID := f.seedTopicWithLikeContribution(t, authorID, 0, 7, 2)
	commentID := f.insertPendingComment(t, topicID, authorID)

	store := moderation.NewPostgresStore(f.pool)
	if _, err := store.SubmitDecision(f.ctx, moderation.DecisionInput{
		Source:         moderation.SourcePrePublish,
		TargetType:     moderation.TargetTypeComment,
		TargetID:       commentID,
		Action:         moderation.ActionReject,
		ReviewerUserID: reviewerID,
		ReviewNote:     "not suitable for the forum",
	}); err != nil {
		t.Fatalf("reject pending comment: %v", err)
	}
	f.assertCommentStatus(t, commentID, "rejected")
	if got := f.decisionCount(t, commentID); got != 1 {
		t.Fatalf("reject wrote %d decisions, want 1", got)
	}

	// 驳回的是一条从未计入 comment_count 的评论，因此计数与热度必须逐列不变：
	// 0*5 + 7 + 2*3 = 13，点赞贡献 6 分不能被审核动作抹掉。
	after := f.topicCounters(t, topicID)
	want := int64(0)*5 + 7 + 2*3
	if after.hotScore != want {
		t.Fatalf("reject rewrote hot_score=%d want unchanged %d", after.hotScore, want)
	}
	if after.commentCount != 0 {
		t.Fatalf("reject changed comment_count to %d, want 0", after.commentCount)
	}
	if after.viewCount != 7 || after.likeCount != 2 {
		t.Fatalf("reject changed counters=%+v want view_count=7 like_count=2", after)
	}
	if after.hotScore != after.formula() {
		t.Fatalf("reject left hot_score=%d inconsistent with formula %d (%+v)", after.hotScore, after.formula(), after)
	}
	if got := f.reactionRowCount(t, topicID); got != 2 {
		t.Fatalf("moderation reject touched content_reactions rows: %d want 2", got)
	}
}

func newReactionHotScoreFixture(t *testing.T) *reactionHotScoreFixture {
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
	schema := "reaction_hot_score_" + hex.EncodeToString(random)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create fixture schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, reactionHotScoreSchemaSQL); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
		t.Fatalf("create fixture tables: %v", err)
	}
	f := &reactionHotScoreFixture{ctx: ctx, admin: admin, pool: pool}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})
	return f
}

func (f *reactionHotScoreFixture) insertUser(t *testing.T, username, status string) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `INSERT INTO users (username, username_lower, email, locale, display_name, status)
		VALUES ($1, lower($1), lower($1) || '@example.test', 'en-US', $1, $2) RETURNING id`, username, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedTopicWithLikeContribution 预置一个自洽的主题：hot_score 由权威公式算出，
// 并按 like_count 插入同样数量的 content_reactions 关系行，保证计数列与关系表一致。
func (f *reactionHotScoreFixture) seedTopicWithLikeContribution(t *testing.T, authorID, commentCount, viewCount, likeCount int64) int64 {
	t.Helper()
	var postID, topicID int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO posts (raw_content) VALUES ('hot score topic body') RETURNING id`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	hotScore := commentCount*5 + viewCount + likeCount*3
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, status,
		  comment_count, view_count, like_count, hot_score)
		VALUES (1, $1, $2, 'hot score topic', 'active', $3, $4, $5, $6)
		RETURNING id`, authorID, postID, commentCount, viewCount, likeCount, hotScore).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	for index := int64(0); index < likeCount; index++ {
		likerID := f.insertUser(t, fmt.Sprintf("hot_liker_%d_%d", topicID, index), "active")
		if _, err := f.pool.Exec(f.ctx, `
			INSERT INTO content_reactions (user_id, target_type, target_id, reaction)
			VALUES ($1, 'topic', $2, 'like')`, likerID, topicID); err != nil {
			t.Fatal(err)
		}
	}
	return topicID
}

func (f *reactionHotScoreFixture) insertPendingComment(t *testing.T, topicID, authorID int64) int64 {
	t.Helper()
	var postID, commentID int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO posts (raw_content) VALUES ('pending comment body') RETURNING id`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO comments (topic_id, content_id, author_user_id, status)
		VALUES ($1, $2, $3, 'pending') RETURNING id`, topicID, postID, authorID).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	return commentID
}

func (f *reactionHotScoreFixture) topicCounters(t *testing.T, topicID int64) reactionHotScoreCounters {
	t.Helper()
	var counters reactionHotScoreCounters
	if err := f.pool.QueryRow(f.ctx, `
		SELECT comment_count, view_count, like_count, hot_score FROM topics WHERE id = $1`, topicID).
		Scan(&counters.commentCount, &counters.viewCount, &counters.likeCount, &counters.hotScore); err != nil {
		t.Fatalf("load topic counters %d: %v", topicID, err)
	}
	return counters
}

func (f *reactionHotScoreFixture) reactionRowCount(t *testing.T, topicID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM content_reactions WHERE target_type = 'topic' AND target_id = $1`, topicID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *reactionHotScoreFixture) decisionCount(t *testing.T, commentID int64) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM moderation_decisions WHERE target_type = 'comment' AND target_id = $1`, commentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *reactionHotScoreFixture) assertCommentStatus(t *testing.T, commentID int64, want string) {
	t.Helper()
	var got string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM comments WHERE id = $1`, commentID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("comment status=%q want=%q", got, want)
	}
}

const reactionHotScoreSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  locale TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active'
);
CREATE TABLE categories (
  id BIGSERIAL PRIMARY KEY,
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO categories DEFAULT VALUES;
CREATE TABLE posts (
  id BIGSERIAL PRIMARY KEY,
  raw_content TEXT NOT NULL,
  source_format TEXT NOT NULL DEFAULT 'markdown'
);
CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL REFERENCES categories(id),
  author_user_id BIGINT REFERENCES users(id),
  content_id BIGINT NOT NULL REFERENCES posts(id),
  title TEXT NOT NULL,
  status TEXT NOT NULL,
  moderation_triggers JSONB NOT NULL DEFAULT '[]',
  comment_count BIGINT NOT NULL DEFAULT 0,
  view_count BIGINT NOT NULL DEFAULT 0,
  like_count BIGINT NOT NULL DEFAULT 0,
  hot_score BIGINT NOT NULL DEFAULT 0,
  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL REFERENCES topics(id),
  content_id BIGINT NOT NULL REFERENCES posts(id),
  author_user_id BIGINT REFERENCES users(id),
  parent_comment_id BIGINT REFERENCES comments(id),
  reply_count BIGINT NOT NULL DEFAULT 0,
  like_count BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  moderation_triggers JSONB NOT NULL DEFAULT '[]',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE moderation_decisions (
  id BIGSERIAL PRIMARY KEY,
  source TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  report_id BIGINT,
  action TEXT NOT NULL,
  reviewer_user_id BIGINT NOT NULL REFERENCES users(id),
  review_note TEXT NOT NULL DEFAULT '',
  trigger_snapshot JSONB NOT NULL DEFAULT '[]',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_reactions_target_type_check CHECK (target_type IN ('topic', 'comment')),
  CONSTRAINT content_reactions_reaction_check CHECK (reaction IN ('like')),
  CONSTRAINT content_reactions_target_positive CHECK (target_id > 0),
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);`
