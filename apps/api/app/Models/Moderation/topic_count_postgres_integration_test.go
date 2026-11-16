package moderation_test

// P2-5 的跨包序列证据：create → lock → 编辑触发预审 → 审核通过 → 删除，
// 每一步都断言「categories.topic_count == 公开主题权威计数 == ListTopics.total」。
//
// 为什么要跨包：编辑触发预审（Forum.UpdateTopic + RequeuePending）与审核通过
// （Moderation.SubmitDecision → applyPendingTopicDecision 的 +1）分属两个包，
// 双计就发生在它们之间；只看一边的测试抓不到。
//
// fixture 的表结构与 Forum/revisions_postgres_integration_test.go 的
// revisionLedgerFixtureBaseSchemaSQL 同构（那份是权威镜像，这里复制一份是因为
// 它位于 package forum 的测试文件里，跨包不可引用）。moderation_decisions 的 DDL
// 与 Moderation/notification_fanout_postgres_integration_test.go 一致。

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type topicCountFixture struct {
	t     *testing.T
	ctx   context.Context
	admin *pgxpool.Pool
	pool  *pgxpool.Pool
}

func newTopicCountFixture(t *testing.T) *topicCountFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("moderation_topic_count_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cleanup := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if _, err := db.ExecContext(ctx, topicCountFixtureSchemaSQL); err != nil {
		db.Close()
		cleanup()
		t.Fatalf("create fixture schema: %v", err)
	}
	if err := applyTopicCountFixtureMigrations(ctx, db); err != nil {
		db.Close()
		cleanup()
		t.Fatalf("apply fixture migrations: %v", err)
	}
	if err := db.Close(); err != nil {
		cleanup()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	fixture := &topicCountFixture{t: t, ctx: ctx, admin: admin, pool: pool}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return fixture
}

// applyTopicCountFixtureMigrations 只补 revision ledger 需要的版本（与 Forum 侧
// fixture 一致）：它给 posts 加 current_revision，并建 topic_revision_snapshots。
func applyTopicCountFixtureMigrations(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return err
	}
	_, err = provider.ApplyVersion(ctx, 202607220052, true)
	return err
}

func (f *topicCountFixture) insertUser(username string) int64 {
	f.t.Helper()
	var id int64
	email := username + "@example.test"
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, lower($1), $2, lower($2), $1)
		RETURNING id
	`, username, email).Scan(&id); err != nil {
		f.t.Fatalf("insert user: %v", err)
	}
	return id
}

// counters 同时取冗余计数、权威公开计数与列表 total。
type topicCounters struct {
	redundant     int64
	authoritative int64
	listTotal     int64
}

func (f *topicCountFixture) counters(store *forum.PostgresStore) topicCounters {
	f.t.Helper()
	var counters topicCounters
	if err := f.pool.QueryRow(f.ctx, `
		SELECT topic_count FROM categories WHERE slug = 'general'
	`).Scan(&counters.redundant); err != nil {
		f.t.Fatalf("read category topic_count: %v", err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM topics
		JOIN categories ON categories.id = topics.category_id
		WHERE categories.slug = 'general' AND topics.status IN ('active', 'locked')
	`).Scan(&counters.authoritative); err != nil {
		f.t.Fatalf("count public topics: %v", err)
	}
	list, err := store.ListTopics(f.ctx, forum.TopicListInput{Page: 1, PerPage: 20})
	if err != nil {
		f.t.Fatalf("ListTopics: %v", err)
	}
	counters.listTotal = list.Total
	return counters
}

func (f *topicCountFixture) assertConsistent(store *forum.PostgresStore, step string) topicCounters {
	f.t.Helper()
	counters := f.counters(store)
	// 把每步的实际数字留在 -v 输出里：验收要求「每步计数断言的实际数字」可复核。
	f.t.Logf("%s: categories.topic_count=%d 公开主题数=%d ListTopics.total=%d",
		step, counters.redundant, counters.authoritative, counters.listTotal)
	if counters.redundant != counters.authoritative {
		f.t.Fatalf("%s: categories.topic_count=%d 与公开主题数=%d 不一致",
			step, counters.redundant, counters.authoritative)
	}
	if counters.listTotal != counters.authoritative {
		f.t.Fatalf("%s: ListTopics.total=%d 与公开主题数=%d 不一致",
			step, counters.listTotal, counters.authoritative)
	}
	return counters
}

// TestTopicCountStaysConsistentThroughModerationLifecycle 是 P2-5 的序列证据。
//
// 变异要求：把 requeue 或 DeleteTopic 的判定改回「只有 active」，第三步/第五步的
// 计数断言必须红。
func TestTopicCountStaysConsistentThroughModerationLifecycle(t *testing.T) {
	fixture := newTopicCountFixture(t)
	store := forum.NewPostgresStore(fixture.pool)
	moderationStore := moderation.NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser("count_sequence_author")
	reviewerID := fixture.insertUser("count_sequence_reviewer")

	content, err := forum.RenderContent(forum.ContentInput{
		RawContent: "序列测试正文", SourceFormat: forum.SourceFormatMarkdown,
	})
	if err != nil {
		t.Fatalf("render content: %v", err)
	}

	// 1) create（active）：计数 1。
	topic, err := store.CreateTopic(fixture.ctx, forum.CreateTopicRecord{
		CategorySlug: "general", AuthorUserID: authorID, Title: "计数序列主题",
		Slug: "count-sequence-topic", TagCreationMode: forum.TagCreationModeControlled,
		Content: content, Status: forum.TopicStatusActive,
	})
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "create"); got.redundant != 1 {
		t.Fatalf("create: topic_count=%d want 1", got.redundant)
	}

	// 2) lock：locked 仍在公开口径内，计数不变。
	if _, err := store.ApplyTopicAction(fixture.ctx, forum.TopicLifecycleInput{
		TopicID: topic.ID, Action: forum.TopicActionLock,
	}); err != nil {
		t.Fatalf("lock topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "lock"); got.redundant != 1 {
		t.Fatalf("lock: topic_count=%d want 1（locked 仍计入公开计数）", got.redundant)
	}

	// 3) 编辑触发预审：洗成 pending，必须先扣掉，否则下一步会双计。
	if _, err := store.UpdateTopic(fixture.ctx, forum.UpdateTopicRecord{
		TopicID: topic.ID, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 1,
		Operation: forum.RevisionOperationEdit, Origin: forum.RevisionOriginStaff,
		Title: "计数序列主题（改）", Slug: "count-sequence-topic-edited",
		TagCreationMode: forum.TagCreationModeControlled, RequeuePending: true,
		ModerationTriggers: []string{"link_spam"},
	}); err != nil {
		t.Fatalf("requeue topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "requeue"); got.redundant != 0 {
		t.Fatalf("requeue: topic_count=%d want 0", got.redundant)
	}

	// 4) 审核通过：回到 active，+1 正好回到 1。
	if _, err := moderationStore.SubmitDecision(fixture.ctx, moderation.DecisionInput{
		Source: moderation.SourcePrePublish, TargetType: moderation.TargetTypeTopic,
		TargetID: topic.ID, Action: moderation.ActionApprove, ReviewerUserID: reviewerID,
	}); err != nil {
		t.Fatalf("approve topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "approve"); got.redundant != 1 {
		t.Fatalf("approve: topic_count=%d want 1（双计会变成 2）", got.redundant)
	}

	// 5) 版主 hide：公开可见性再次变化，计数必须跟着 -1（P2-5 补刀：
	// ApplyTopicAction 以前完全不碰计数，与举报处置那条路径口径不一致）。
	if _, err := store.ApplyTopicAction(fixture.ctx, forum.TopicLifecycleInput{
		TopicID: topic.ID, Action: forum.TopicActionHide,
	}); err != nil {
		t.Fatalf("hide topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "hide"); got.redundant != 0 {
		t.Fatalf("hide: topic_count=%d want 0", got.redundant)
	}

	// 6) 删除：hidden 本来就不计入公开计数，删除不得再减一次（GREATEST 兜底 + 判定）。
	if _, err := store.DeleteTopic(fixture.ctx, topic.ID); err != nil {
		t.Fatalf("delete topic: %v", err)
	}
	if got := fixture.assertConsistent(store, "delete"); got.redundant != 0 {
		t.Fatalf("delete: topic_count=%d want 0", got.redundant)
	}
}

// topicCountFixtureSchemaSQL 与 Forum/revisions_postgres_integration_test.go 的
// revisionLedgerFixtureBaseSchemaSQL 同构，末尾追加 moderation_decisions。
const topicCountFixtureSchemaSQL = `CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  username_lower TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL,
  email_lower TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  top_badge_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE attachments (
  id BIGSERIAL PRIMARY KEY,
  public_id TEXT NOT NULL DEFAULT '',
  owner_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  content_type TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  visibility TEXT NOT NULL DEFAULT 'public',
  reference_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_profiles (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  avatar_attachment_id BIGINT REFERENCES attachments(id) ON DELETE SET NULL
);

-- 徽章定义（读路径 LEFT JOIN badges top_badge）：只保留读模型用到的列。
CREATE TABLE badges (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  tier TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT 'i-lucide-award',
  enabled BOOLEAN NOT NULL DEFAULT TRUE
);

-- 用户活动统计（写路径 hook）：真实迁移里的非负约束在这里省略，
-- fixture 只关心「写入是否落在同一事务、列是否存在」。
CREATE TABLE user_activity (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  like_received_count BIGINT NOT NULL DEFAULT 0,
  like_given_count BIGINT NOT NULL DEFAULT 0,
  bookmark_count BIGINT NOT NULL DEFAULT 0,
  days_visited INTEGER NOT NULL DEFAULT 0,
  posts_read_count BIGINT NOT NULL DEFAULT 0,
  read_time_seconds BIGINT NOT NULL DEFAULT 0,
  last_visited_on DATE,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_visits (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  visited_on DATE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, visited_on)
);

CREATE TABLE categories (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'public',
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE posts (
  id BIGSERIAL PRIMARY KEY,
  raw_content TEXT NOT NULL,
  html_content TEXT NOT NULL,
  plain_text TEXT NOT NULL,
  source_format TEXT NOT NULL DEFAULT 'markdown',
  editor_type TEXT NOT NULL DEFAULT 'markdown',
  editor_version TEXT NOT NULL DEFAULT '',
  render_version TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
  author_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  content_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE RESTRICT,
  title TEXT NOT NULL,
  slug TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  comment_count BIGINT NOT NULL DEFAULT 0,
  view_count BIGINT NOT NULL DEFAULT 0,
  like_count BIGINT NOT NULL DEFAULT 0,
  hot_score BIGINT NOT NULL DEFAULT 0,
  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- ApplyTopicAction 会写这两列；Forum 侧的共享 fixture 里没有，本文件按需补齐。
  locked_at TIMESTAMPTZ,
  pinned_at TIMESTAMPTZ,
  moderation_triggers JSONB NOT NULL DEFAULT '[]'::jsonb,
  ip_address TEXT NOT NULL DEFAULT '',
  last_edit_ip TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  content_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE RESTRICT,
  author_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  parent_comment_id BIGINT REFERENCES comments(id) ON DELETE SET NULL,
  root_comment_id BIGINT REFERENCES comments(id) ON DELETE SET NULL,
  path_key TEXT NOT NULL DEFAULT '',
  depth INTEGER NOT NULL DEFAULT 0,
  reply_count BIGINT NOT NULL DEFAULT 0,
  like_count BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  moderation_triggers JSONB NOT NULL DEFAULT '[]'::jsonb,
  ip_address TEXT NOT NULL DEFAULT '',
  last_edit_ip TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE TABLE post_revisions (
  id BIGSERIAL PRIMARY KEY,
  post_id BIGINT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  edited_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  raw_content TEXT NOT NULL,
  source_format TEXT NOT NULL,
  editor_type TEXT NOT NULL,
  editor_version TEXT NOT NULL DEFAULT '',
  render_version TEXT NOT NULL DEFAULT '',
  content_hash TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tags (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  topic_count BIGINT NOT NULL DEFAULT 0,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE topic_tags (
  topic_id BIGINT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (topic_id, tag_id)
);

CREATE TABLE attachment_references (
  attachment_id BIGINT NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  resource_type TEXT NOT NULL,
  resource_id BIGINT NOT NULL,
  context TEXT NOT NULL,
  created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

INSERT INTO categories (slug, name, visibility)
VALUES ('general', '综合讨论', 'public');
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
);`
