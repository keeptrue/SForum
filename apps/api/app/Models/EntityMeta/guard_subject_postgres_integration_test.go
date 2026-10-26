package entitymeta

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// entity-meta 用 topics.author_user_id 作为 topic 的所有者。这一列是
// `BIGINT REFERENCES users(id) ON DELETE SET NULL`
// （database/migrations/202607060003_forum_topics_comments_posts.sql:38），
// 作者注销后为 NULL；把它直接扫进 int64 会让「作者已注销」的主题在读取
// entity-meta 时整条失败，而不是被上层按 OwnerUserID == 0 fail-closed。
//
// 修法与 c6d6d53ba 同源：SELECT 里 COALESCE(author_user_id, 0)。
func TestPostgresValueGuardSubjectToleratesDeletedTopicAuthor(t *testing.T) {
	ctx, pool, suffix := openEntityMetaPool(t)
	graph := newEntityMetaTopicGraph(t, ctx, pool, suffix)
	deleteEntityMetaTopicAuthor(t, ctx, pool, graph)

	subject, err := NewPostgresStore(pool).LoadValueGuardSubject(ctx, EntityTopic, graph.TopicID, nil)
	if err != nil {
		t.Fatalf("load entity-meta guard subject whose author was deleted: %v", err)
	}
	if !subject.Exists || subject.EntityType != EntityTopic || subject.EntityID != graph.TopicID {
		t.Fatalf("subject = %#v", subject)
	}
	if subject.OwnerUserID != 0 {
		t.Fatalf("ownerUserID = %d, want 0 for a deleted author", subject.OwnerUserID)
	}
}

// TestPostgresServiceListValuesToleratesDeletedTopicAuthor 钉住核心直连路径：
// `GET /api/v1/entity-meta/:entityType/:entityID` 不经过 route guard
// （core-only plan 在 dispatcher 里 early-return，ProviderCore 不评估 guard），
// 直接走 Service.ListValues → entityOwnerID → TopicAuthorID。
//
// 同时钉住语义边界：作者注销后 ownerID 是 0，**不等于**放行 ——
// owner 可见性的字段对匿名/非 owner 仍然不可见，只有 public 字段照旧返回。
func TestPostgresServiceListValuesToleratesDeletedTopicAuthor(t *testing.T) {
	ctx, pool, suffix := openEntityMetaPool(t)
	graph := newEntityMetaTopicGraph(t, ctx, pool, suffix)
	publicKey := "em.public-probe." + suffix
	ownerKey := "em.owner-probe." + suffix
	insertEntityMetaProbeField(t, ctx, pool, graph, publicKey, VisibilityPublic, "public-value")
	insertEntityMetaProbeField(t, ctx, pool, graph, ownerKey, VisibilityOwner, "owner-secret")
	deleteEntityMetaTopicAuthor(t, ctx, pool, graph)

	values, err := NewService(NewPostgresStore(pool)).
		ListValues(ctx, identity.Actor{}, EntityTopic, graph.TopicID)
	if err != nil {
		t.Fatalf("list entity-meta values whose topic author was deleted: %v", err)
	}
	byKey := make(map[string]MetaValue, len(values))
	for _, value := range values {
		byKey[value.FieldKey] = value
	}
	if _, ok := byKey[ownerKey]; ok {
		t.Fatalf("owner-visible field leaked to an inactive actor: %#v", byKey[ownerKey])
	}
	public, ok := byKey[publicKey]
	if !ok {
		t.Fatalf("public field missing after author deletion: %#v", values)
	}
	if public.Value != "public-value" {
		t.Fatalf("public field value = %#v", public.Value)
	}
}

type entityMetaTopicGraph struct {
	AuthorID   int64
	GroupID    int64
	CategoryID int64
	PostID     int64
	TopicID    int64
}

// openEntityMetaPool 连真实迁移过的库；SFORUM_TEST_DATABASE_URL 未设置时跳过。
func openEntityMetaPool(t *testing.T) (context.Context, *pgxpool.Pool, string) {
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

// newEntityMetaTopicGraph 建一条真实 topic 行（含分类组），按 FK 顺序清理。
func newEntityMetaTopicGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) entityMetaTopicGraph {
	t.Helper()
	graph := entityMetaTopicGraph{}
	authorName := "entity_meta_author_" + suffix
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1) RETURNING id
	`, authorName, authorName+"@example.test").Scan(&graph.AuthorID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM topics WHERE id = $1`, graph.TopicID)
		_, _ = pool.Exec(cleanup, `DELETE FROM posts WHERE id = $1`, graph.PostID)
		_, _ = pool.Exec(cleanup, `DELETE FROM categories WHERE id = $1`, graph.CategoryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM category_groups WHERE id = $1`, graph.GroupID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, graph.AuthorID)
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO category_groups (slug, name) VALUES ($1, $2) RETURNING id
	`, "entity-meta-group-"+suffix, "Entity Meta Group "+suffix).Scan(&graph.GroupID); err != nil {
		t.Fatalf("insert category group: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO categories (group_id, slug, name) VALUES ($1, $2, $3) RETURNING id
	`, graph.GroupID, "entity-meta-author-"+suffix, "Entity Meta "+suffix).Scan(&graph.CategoryID); err != nil {
		t.Fatalf("insert category: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO posts (raw_content, html_content, plain_text, content_hash)
		VALUES ('body', '<p>body</p>', 'body', $1) RETURNING id
	`, "entity-meta-hash-"+suffix).Scan(&graph.PostID); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, graph.CategoryID, graph.AuthorID, graph.PostID, "Entity Meta Topic "+suffix, "entity-meta-topic-"+suffix).Scan(&graph.TopicID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}
	return graph
}

// deleteEntityMetaTopicAuthor 删掉作者，触发 FK 的 ON DELETE SET NULL 并复核列已为空。
func deleteEntityMetaTopicAuthor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, graph entityMetaTopicGraph) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, graph.AuthorID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	var authorNull bool
	if err := pool.QueryRow(ctx, `SELECT author_user_id IS NULL FROM topics WHERE id = $1`, graph.TopicID).Scan(&authorNull); err != nil {
		t.Fatalf("verify ON DELETE SET NULL: %v", err)
	}
	if !authorNull {
		t.Fatal("fixture must leave a NULL topic author")
	}
}

func insertEntityMetaProbeField(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	graph entityMetaTopicGraph, fieldKey, visibility, value string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO entity_field_definitions (field_key, entity_type, value_type, visibility, label_zh_cn)
		VALUES ($1, $2, 'string', $3, $1)
	`, fieldKey, EntityTopic, visibility); err != nil {
		t.Fatalf("insert field definition %s: %v", fieldKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entity_field_definitions WHERE field_key = $1`, fieldKey)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO entity_meta_values (entity_type, entity_id, field_key, value_text)
		VALUES ($1, $2, $3, $4)
	`, EntityTopic, graph.TopicID, fieldKey, value); err != nil {
		t.Fatalf("insert entity meta value %s: %v", fieldKey, err)
	}
}
