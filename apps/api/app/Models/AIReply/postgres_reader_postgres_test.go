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

// 读取层是提示词里身份信息的唯一来源：昵称错了只是难看，@用户名错了模型就会查错人。
// 这些断言把「取哪些字段、过滤哪些行」钉在真实 Postgres 上。
func TestPostgresReaderCarriesPublicAuthorIdentity(t *testing.T) {
	ctx, reader := newReplyReaderPostgresHarness(t)

	thread, err := reader.ReplyContext(ctx, 10, 502, 502)
	if err != nil {
		t.Fatalf("ReplyContext error: %v", err)
	}
	if thread.TopicTitle != "连接池怎么配" || thread.TopicBody != "线上 QPS 很高" {
		t.Fatalf("topic = %q / %q", thread.TopicTitle, thread.TopicBody)
	}
	if thread.TopicAuthor.Username != "alice" || thread.TopicAuthor.DisplayName != "Alice" {
		t.Fatalf("topic author = %#v", thread.TopicAuthor)
	}
	if thread.ParentAuthor.Username != "ghost" || thread.ParentAuthor.DisplayName != "" {
		t.Fatalf("parent author = %#v", thread.ParentAuthor)
	}
	// 昵称缺失（用户没有设显示名）时不能编一个出来：Label 会用 @用户名兜底。
	if got := thread.ParentAuthor.Label(); got != "@ghost" {
		t.Fatalf("parent label = %q", got)
	}

	// 近期发言按时间正序，机器人自己的发言被标记出来，非 active 的发言不出现。
	if len(thread.RecentComments) != 4 {
		t.Fatalf("recent comments = %#v", thread.RecentComments)
	}
	if thread.RecentComments[0].Author.Username != "bob" || thread.RecentComments[0].IsBot {
		t.Fatalf("first recent comment = %#v", thread.RecentComments[0])
	}
	if thread.RecentComments[1].Author.Username != "sforum_ai" || !thread.RecentComments[1].IsBot {
		t.Fatalf("second recent comment = %#v", thread.RecentComments[1])
	}
	if thread.RecentComments[2].Author.Username != "ghost" {
		t.Fatalf("third recent comment = %#v", thread.RecentComments[2])
	}
}

func TestPostgresReaderPopulatesTheTriggerAuthorSeparately(t *testing.T) {
	ctx, reader := newReplyReaderPostgresHarness(t)

	thread, err := reader.ReplyContext(ctx, 10, 502, 503)
	if err != nil {
		t.Fatalf("ReplyContext error: %v", err)
	}
	if thread.ParentAuthor.Username != "ghost" || thread.TriggerAuthor.Username != "bob" {
		t.Fatalf("parent=%#v trigger=%#v", thread.ParentAuthor, thread.TriggerAuthor)
	}
	if thread.TriggerContent != "帮我看看这段代码" {
		t.Fatalf("trigger content = %q", thread.TriggerContent)
	}
}

// 机器人不得看到访客看不到的楼：非公开状态的主题、非公开分类、以及非 active 的评论。
func TestPostgresReaderRefusesNonPublicThreads(t *testing.T) {
	ctx, reader := newReplyReaderPostgresHarness(t)

	for _, topicID := range []int64{11, 12} {
		if _, err := reader.ReplyContext(ctx, topicID, 0, 0); err == nil {
			t.Fatalf("topic %d must not be readable by the bot", topicID)
		}
	}
	if _, err := reader.ReplyContext(ctx, 10, 504, 504); err == nil {
		t.Fatal("a non-active comment must not be readable by the bot")
	}
}

func newReplyReaderPostgresHarness(t *testing.T) (context.Context, *aireply.PostgresReader) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aireply_reader_%d", time.Now().UnixNano())
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
		`CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			username TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'human'
		)`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, visibility TEXT NOT NULL)`,
		`CREATE TABLE posts (id BIGINT PRIMARY KEY, plain_text TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE topics (
			id BIGINT PRIMARY KEY,
			category_id BIGINT NOT NULL,
			author_user_id BIGINT,
			content_id BIGINT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ
		)`,
		`CREATE TABLE comments (
			id BIGINT PRIMARY KEY,
			topic_id BIGINT NOT NULL,
			content_id BIGINT NOT NULL,
			author_user_id BIGINT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`INSERT INTO users (id, username, display_name, kind) VALUES
			(1, 'alice', 'Alice', 'human'),
			(2, 'bob', 'Bob', 'human'),
			(3, 'sforum_ai', 'sforum_ai', 'bot'),
			(4, 'ghost', '', 'human')`,
		`INSERT INTO categories (id, visibility) VALUES (1, 'public'), (2, 'hidden')`,
		`INSERT INTO posts (id, plain_text) VALUES
			(100, '线上 QPS 很高'),
			(101, '池子设 20 就行'),
			(102, '我的回答'),
			(103, '帮我看看这段代码'),
			(104, '待审发言')`,
		`INSERT INTO topics (id, category_id, author_user_id, content_id, title, status) VALUES
			(10, 1, 1, 100, '连接池怎么配', 'active'),
			(11, 1, 1, 100, '隐藏主题', 'hidden'),
			(12, 2, 1, 100, '隐藏分类主题', 'active')`,
		`INSERT INTO comments (id, topic_id, content_id, author_user_id, status, created_at) VALUES
			(500, 10, 101, 2, 'active', '2026-10-01T10:00:00Z'),
			(501, 10, 102, 3, 'active', '2026-10-01T10:05:00Z'),
			(502, 10, 103, 4, 'active', '2026-10-01T10:10:00Z'),
			(503, 10, 103, 2, 'active', '2026-10-01T10:15:00Z'),
			(504, 10, 104, 2, 'hidden', '2026-10-01T10:20:00Z')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup reply reader schema: %v\n%s", err, statement)
		}
	}
	return ctx, aireply.NewPostgresReader(pool)
}
