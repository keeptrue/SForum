package entitymeta

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// entity-meta 的 value 路由用 topics.author_user_id 作为 topic 的所有者。
// 这一列是 BIGINT REFERENCES users(id) ON DELETE SET NULL
// （database/migrations/202607060003_forum_topics_comments_posts.sql:38），
// 作者注销后为 NULL；把它直接扫进 int64 会让「作者已注销」的主题在读取
// entity-meta 时整条失败，而不是被上层按 OwnerUserID == 0 fail-closed。
//
// 修法与 c6d6d53ba 同源：SELECT 里 COALESCE(author_user_id, 0)。
func TestPostgresValueGuardSubjectToleratesDeletedTopicAuthor(t *testing.T) {
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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	authorName := "entity_meta_author_" + suffix
	var authorID, groupID, categoryID, postID, topicID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $1) RETURNING id
	`, authorName, authorName+"@example.test").Scan(&authorID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM topics WHERE id = $1`, topicID)
		_, _ = pool.Exec(cleanup, `DELETE FROM posts WHERE id = $1`, postID)
		_, _ = pool.Exec(cleanup, `DELETE FROM categories WHERE id = $1`, categoryID)
		_, _ = pool.Exec(cleanup, `DELETE FROM category_groups WHERE id = $1`, groupID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, authorID)
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO category_groups (slug, name) VALUES ($1, $2) RETURNING id
	`, "entity-meta-group-"+suffix, "Entity Meta Group "+suffix).Scan(&groupID); err != nil {
		t.Fatalf("insert category group: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO categories (group_id, slug, name) VALUES ($1, $2, $3) RETURNING id
	`, groupID, "entity-meta-author-"+suffix, "Entity Meta "+suffix).Scan(&categoryID); err != nil {
		t.Fatalf("insert category: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO posts (raw_content, html_content, plain_text, content_hash)
		VALUES ('body', '<p>body</p>', 'body', $1) RETURNING id
	`, "entity-meta-hash-"+suffix).Scan(&postID); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, categoryID, authorID, postID, "Entity Meta Topic "+suffix, "entity-meta-topic-"+suffix).Scan(&topicID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}

	// 删掉作者：FK 的 ON DELETE SET NULL 把 topics.author_user_id 置空。
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, authorID); err != nil {
		t.Fatalf("delete author: %v", err)
	}
	var authorNull bool
	if err := pool.QueryRow(ctx, `SELECT author_user_id IS NULL FROM topics WHERE id = $1`, topicID).Scan(&authorNull); err != nil {
		t.Fatalf("verify ON DELETE SET NULL: %v", err)
	}
	if !authorNull {
		t.Fatal("fixture must leave a NULL topic author")
	}

	subject, err := NewPostgresStore(pool).LoadValueGuardSubject(ctx, EntityTopic, topicID, nil)
	if err != nil {
		t.Fatalf("load entity-meta guard subject whose author was deleted: %v", err)
	}
	if !subject.Exists || subject.EntityType != EntityTopic || subject.EntityID != topicID {
		t.Fatalf("subject = %#v", subject)
	}
	if subject.OwnerUserID != 0 {
		t.Fatalf("ownerUserID = %d, want 0 for a deleted author", subject.OwnerUserID)
	}
}
