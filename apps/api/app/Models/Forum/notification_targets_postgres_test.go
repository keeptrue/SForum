package forum_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// TestResolveNotificationTargetCommentPageSegment 验证评论通知深链携带 flat 分页页码：
// page 1 目标保持旧链接形态 /t/{topic}#comment-{id}（兼容既有通知与测试断言），
// page>1 目标输出 /t/{topic}/page/{N}#comment-{id}，与前端 forumTopicPath 同形。
func TestResolveNotificationTargetCommentPageSegment(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	stamp := time.Now().UnixNano()
	var userID, categoryGroupID, categoryID, topicID int64
	t.Cleanup(func() {
		if topicID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM comments WHERE topic_id=$1`, topicID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM topics WHERE id=$1`, topicID)
		}
		if categoryID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM categories WHERE id=$1`, categoryID)
		}
		if categoryGroupID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM category_groups WHERE id=$1`, categoryGroupID)
		}
		if userID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM posts WHERE created_by_user_id=$1`, userID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		}
	})

	username := fmt.Sprintf("notif_page_%d", stamp)
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO category_groups (slug,name,visibility) VALUES ($1,$1,'public') RETURNING id`,
		fmt.Sprintf("notif-page-group-%d", stamp)).Scan(&categoryGroupID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO categories (group_id,slug,name,visibility) VALUES ($1,$2,$2,'public') RETURNING id`,
		categoryGroupID, fmt.Sprintf("notif-page-%d", stamp)).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}

	insertPost := func(label string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO posts (raw_content,html_content,plain_text,content_hash,created_by_user_id)
VALUES ($1,$1,$1,$2,$3) RETURNING id`, label, fmt.Sprintf("hash-%d-%s", stamp, label), userID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	postID := insertPost("notif-page-topic")
	if err := pool.QueryRow(ctx, `INSERT INTO topics (category_id,author_user_id,content_id,title,slug,status)
VALUES ($1,$2,$3,$4,$5,'active') RETURNING id`, categoryID, userID, postID, "notif-page-topic", fmt.Sprintf("notif-page-topic-%d", stamp)).Scan(&topicID); err != nil {
		t.Fatal(err)
	}

	// 与 commentsPerPageOption 同源读取运营配置；无配置时用默认 20。
	perPage := 20
	var raw string
	if err := pool.QueryRow(ctx, `SELECT value FROM web_options WHERE name=$1`, options.NameForumCommentsPerPage).Scan(&raw); err == nil {
		if n, convErr := strconv.Atoi(strings.TrimSpace(raw)); convErr == nil && n >= 1 {
			perPage = n
			if perPage > 100 {
				perPage = 100
			}
		}
	}

	insertComment := func(index int) int64 {
		var id int64
		pID := insertPost(fmt.Sprintf("notif-page-comment-%d", index))
		if err := pool.QueryRow(ctx, `INSERT INTO comments (topic_id,content_id,author_user_id,path_key,status,created_at)
VALUES ($1,$2,$3,$4,'active',$5) RETURNING id`, topicID, pID, userID, fmt.Sprintf("%020d", pID),
			time.Unix(stamp/1e9, 0).Add(time.Duration(index)*time.Second)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 第 1 条评论：page 1，链接保持旧形态。
	firstCommentID := insertComment(0)
	// 填满第一页后再插入一条：该评论位于 page 2。
	lastIndex := perPage
	var lastCommentID int64
	for i := 1; i <= lastIndex; i++ {
		lastCommentID = insertComment(i)
	}

	store := forum.NewPostgresStore(pool)

	available, path, err := store.ResolveNotificationTarget(ctx, 0, "comment", firstCommentID)
	if err != nil || !available {
		t.Fatalf("first comment target: available=%v path=%q err=%v", available, path, err)
	}
	if want := fmt.Sprintf("/t/%d#comment-%d", topicID, firstCommentID); path != want {
		t.Fatalf("page-1 target path = %q, want %q", path, want)
	}

	available, path, err = store.ResolveNotificationTarget(ctx, 0, "comment", lastCommentID)
	if err != nil || !available {
		t.Fatalf("last comment target: available=%v path=%q err=%v", available, path, err)
	}
	if want := fmt.Sprintf("/t/%d/page/2#comment-%d", topicID, lastCommentID); path != want {
		t.Fatalf("page-2 target path = %q, want %q", path, want)
	}
}
