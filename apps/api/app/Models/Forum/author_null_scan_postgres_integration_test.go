package forum

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// 本文件钉住「作者已注销」这条真实 schema 路径。topics.author_user_id 与
// comments.author_user_id 都挂着 ON DELETE SET NULL
// （database/migrations/202607060003_forum_topics_comments_posts.sql:38,64），
// 删掉作者后列变成 NULL，但记录本身仍然存在。
//
// 把 NULL 直接扫进 int64 会让所有读这两列的路径整条失败：
// resource guard 的 own 权限判定，以及修订抹除的 lockRedactionTarget。
// 修法与 c6d6d53ba 同源：SELECT 里 COALESCE(<col>, 0)，让上层按「作者已注销」
// 处理（AuthorUserID == 0），而不是退化成 NotFound、panic 或 500。

type authorNullGraph struct {
	AuthorID   int64
	ActorID    int64
	GroupID    int64
	CategoryID int64
	PostID     int64
	TopicID    int64
	CommentID  int64
}

// openAuthorNullPool 连真实迁移过的库；SFORUM_TEST_DATABASE_URL 未设置时跳过。
func openAuthorNullPool(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, fmt.Sprintf("%d", time.Now().UnixNano())
}

func insertAuthorNullUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix, suffix string) int64 {
	t.Helper()
	var id int64
	name := prefix + "_" + suffix
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1) RETURNING id
	`, name, name+"@example.test").Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}

// newAuthorNullGraph 建一条 topic + comment 的最小真实行，按 FK 顺序清理。
// 分类单独建，避免计数器漂移落到 dev 库的 general 上。
func newAuthorNullGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) authorNullGraph {
	t.Helper()
	graph := authorNullGraph{
		AuthorID: insertAuthorNullUser(t, ctx, pool, "author_null", suffix),
		ActorID:  insertAuthorNullUser(t, ctx, pool, "author_null_staff", suffix),
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO category_groups (slug, name) VALUES ($1, $2) RETURNING id
	`, "author-null-group-"+suffix, "Author Null Group "+suffix).Scan(&graph.GroupID); err != nil {
		t.Fatalf("insert category group: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO categories (group_id, slug, name) VALUES ($1, $2, $3) RETURNING id
	`, graph.GroupID, "author-null-"+suffix, "Author Null "+suffix).Scan(&graph.CategoryID); err != nil {
		t.Fatalf("insert category: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO posts (raw_content, html_content, plain_text, content_hash)
		VALUES ('body', '<p>body</p>', 'body', $1) RETURNING id
	`, "author-null-hash-"+suffix).Scan(&graph.PostID); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, graph.CategoryID, graph.AuthorID, graph.PostID, "Author Null Topic "+suffix, "author-null-topic-"+suffix).Scan(&graph.TopicID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO comments (topic_id, content_id, author_user_id, path_key, depth)
		VALUES ($1, $2, $3, '000001', 0) RETURNING id
	`, graph.TopicID, graph.PostID, graph.AuthorID).Scan(&graph.CommentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// post_revisions / topic_revision_snapshots / comments 随 post / topic 级联删除。
		_, _ = pool.Exec(cleanup, `DELETE FROM comments WHERE id = $1`, graph.CommentID)
		_, _ = pool.Exec(cleanup, `DELETE FROM topics WHERE id = $1`, graph.TopicID)
		_, _ = pool.Exec(cleanup, `DELETE FROM posts WHERE id = $1`, graph.PostID)
		_, _ = pool.Exec(cleanup, `DELETE FROM categories WHERE id = $1`, graph.CategoryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM category_groups WHERE id = $1`, graph.GroupID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = ANY($1::bigint[])`, []int64{graph.AuthorID, graph.ActorID})
	})
	return graph
}

// deleteAuthor 触发 FK 的 ON DELETE SET NULL，并断言两列确实已经变成 NULL。
func deleteAuthor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, graph authorNullGraph) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, graph.AuthorID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	var topicAuthorNull, commentAuthorNull bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT author_user_id IS NULL FROM topics WHERE id = $1),
		  (SELECT author_user_id IS NULL FROM comments WHERE id = $2)
	`, graph.TopicID, graph.CommentID).Scan(&topicAuthorNull, &commentAuthorNull); err != nil {
		t.Fatalf("verify ON DELETE SET NULL: %v", err)
	}
	if !topicAuthorNull || !commentAuthorNull {
		t.Fatalf("fixture must leave NULL authors: topic=%v comment=%v", topicAuthorNull, commentAuthorNull)
	}
}

// TestPostgresTopicResourceGuardSubjectToleratesDeletedAuthor 固化
// LoadTopicResourceGuardSubject 对 NULL 作者的行为：记录存在、作者为 0。
// 上层 guard 依据 AuthorUserID <= 0 走 fail-closed 的 own 路径，这是「作者已注销」，
// 不是「主题不存在」。
func TestPostgresTopicResourceGuardSubjectToleratesDeletedAuthor(t *testing.T) {
	ctx, pool, suffix := openAuthorNullPool(t)
	graph := newAuthorNullGraph(t, ctx, pool, suffix)
	deleteAuthor(t, ctx, pool, graph)

	subject, err := NewPostgresStore(pool).LoadTopicResourceGuardSubject(ctx, graph.TopicID)
	if err != nil {
		t.Fatalf("load topic guard subject whose author was deleted: %v", err)
	}
	if !subject.Exists || subject.TopicID != graph.TopicID || subject.Status != TopicStatusActive {
		t.Fatalf("subject = %#v", subject)
	}
	if subject.AuthorUserID != 0 {
		t.Fatalf("authorUserID = %d, want 0 for a deleted author", subject.AuthorUserID)
	}
}

// TestPostgresCommentResourceGuardSubjectToleratesDeletedAuthor 同上，
// 覆盖 LoadCommentResourceGuardSubject 的 comments.author_user_id。
func TestPostgresCommentResourceGuardSubjectToleratesDeletedAuthor(t *testing.T) {
	ctx, pool, suffix := openAuthorNullPool(t)
	graph := newAuthorNullGraph(t, ctx, pool, suffix)
	deleteAuthor(t, ctx, pool, graph)

	subject, err := NewPostgresStore(pool).LoadCommentResourceGuardSubject(ctx, graph.CommentID)
	if err != nil {
		t.Fatalf("load comment guard subject whose author was deleted: %v", err)
	}
	if !subject.Exists || subject.CommentID != graph.CommentID || subject.Status != CommentStatusActive {
		t.Fatalf("subject = %#v", subject)
	}
	if subject.AuthorUserID != 0 {
		t.Fatalf("authorUserID = %d, want 0 for a deleted author", subject.AuthorUserID)
	}
}

// TestPostgresRedactTopicRevisionToleratesDeletedAuthor 固化 lockRedactionTarget
// 的 topics.author_user_id：超级管理员抹除一个「作者已注销」主题的历史修订时，
// 抹除必须照常成功，审计里的 target_user_id 落 NULL（0 表示无目标用户）。
func TestPostgresRedactTopicRevisionToleratesDeletedAuthor(t *testing.T) {
	ctx, pool, suffix := openAuthorNullPool(t)
	graph := newAuthorNullGraph(t, ctx, pool, suffix)
	deleteAuthor(t, ctx, pool, graph)
	seedRedactionRevision(t, ctx, pool, graph.PostID, 1)
	if _, err := pool.Exec(ctx, `UPDATE posts SET current_revision = 2 WHERE id = $1`, graph.PostID); err != nil {
		t.Fatalf("bump current revision: %v", err)
	}

	store := NewPostgresStore(pool).WithAuditor(audit.NewPostgresWriter(pool))
	if err := store.RedactTopicRevision(ctx, RevisionRedactionRecord{
		TargetID: graph.TopicID, TargetType: "topic", RevisionNo: 1,
		ExpectedRevision: 2, ActorUserID: graph.ActorID, Reason: "author_deleted",
	}); err != nil {
		t.Fatalf("redact topic revision whose author was deleted: %v", err)
	}
	assertRedactedAuditTarget(t, ctx, pool, audit.ActionForumTopicRevisionRedact, graph.TopicID, graph)
}

// TestPostgresRedactCommentRevisionToleratesDeletedAuthor 同上，覆盖
// lockRedactionTarget 的 comments 分支（另一条 SQL）。
func TestPostgresRedactCommentRevisionToleratesDeletedAuthor(t *testing.T) {
	ctx, pool, suffix := openAuthorNullPool(t)
	graph := newAuthorNullGraph(t, ctx, pool, suffix)
	deleteAuthor(t, ctx, pool, graph)
	seedRedactionRevision(t, ctx, pool, graph.PostID, 1)
	if _, err := pool.Exec(ctx, `UPDATE posts SET current_revision = 2 WHERE id = $1`, graph.PostID); err != nil {
		t.Fatalf("bump current revision: %v", err)
	}

	store := NewPostgresStore(pool).WithAuditor(audit.NewPostgresWriter(pool))
	if err := store.RedactCommentRevision(ctx, RevisionRedactionRecord{
		TargetID: graph.CommentID, TargetType: "comment", RevisionNo: 1,
		ExpectedRevision: 2, ActorUserID: graph.ActorID, Reason: "author_deleted",
	}); err != nil {
		t.Fatalf("redact comment revision whose author was deleted: %v", err)
	}
	assertRedactedAuditTarget(t, ctx, pool, audit.ActionForumCommentRevisionRedact, graph.CommentID, graph)
}

func seedRedactionRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, postID, revisionNo int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO post_revisions (post_id, revision_no, raw_content, source_format, editor_type, content_hash)
		VALUES ($1, $2, 'old body', 'markdown', 'markdown', $3)
	`, postID, revisionNo, fmt.Sprintf("redaction-revision-%d-%d", postID, revisionNo)); err != nil {
		t.Fatalf("insert post revision: %v", err)
	}
}

// assertRedactedAuditTarget 同时证明「抹除已落库」与「NULL 作者写审计不炸 FK」。
func assertRedactedAuditTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action string, targetID int64, graph authorNullGraph) {
	t.Helper()
	var redacted bool
	if err := pool.QueryRow(ctx, `
		SELECT redacted_at IS NOT NULL FROM post_revisions WHERE post_id = $1 AND revision_no = 1
	`, graph.PostID).Scan(&redacted); err != nil {
		t.Fatalf("load redacted revision: %v", err)
	}
	if !redacted {
		t.Fatal("revision payload was not redacted")
	}

	var auditID int64
	var target *int64
	if err := pool.QueryRow(ctx, `
		SELECT id, target_user_id FROM audit_events
		WHERE action = $1 AND metadata->>'targetId' = $2
		ORDER BY id DESC LIMIT 1
	`, action, fmt.Sprintf("%d", targetID)).Scan(&auditID, &target); err != nil {
		t.Fatalf("load redaction audit event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE id = $1`, auditID)
	})
	if target != nil {
		t.Fatalf("audit target_user_id = %d, want NULL for a deleted author", *target)
	}
}
