package http

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestPostgresForumResourceGuardObservesCrossNodeOwnershipAndDeletion(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	schema := fmt.Sprintf("forum_resource_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	for _, statement := range []string{
		`CREATE TABLE ` + quotedSchema + `.topics (
			id BIGINT PRIMARY KEY, author_user_id BIGINT NOT NULL, status TEXT NOT NULL
		)`,
		`CREATE TABLE ` + quotedSchema + `.comments (
			id BIGINT PRIMARY KEY, author_user_id BIGINT NOT NULL, status TEXT NOT NULL
		)`,
		`INSERT INTO ` + quotedSchema + `.topics (id, author_user_id, status) VALUES (7, 42, 'active')`,
		`INSERT INTO ` + quotedSchema + `.comments (id, author_user_id, status) VALUES (9, 42, 'active')`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install forum resource fixture: %v\n%s", err, statement)
		}
	}

	nodeA := openForumResourceGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openForumResourceGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	store := forum.NewPostgresStore(nodeA)
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumResources: store})

	topicPlan, topicStep := productionForumResourcePlan(t, "core.route.forum.update_topic", "7")
	topicOwner := productionGuardRequest(identity.PermissionTopicEditOwn)
	topicOwner.Method, topicOwner.Path, topicOwner.Params = topicPlan.Method(), topicPlan.Path(), topicPlan.Params()
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicOwner); err != nil {
		t.Fatalf("initial topic owner: %v", err)
	}

	topicGlobal := productionGuardRequest(identity.PermissionTopicEditAny)
	topicGlobal.ActorID = 99
	topicGlobal.Method, topicGlobal.Path, topicGlobal.Params = topicPlan.Method(), topicPlan.Path(), topicPlan.Params()
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicGlobal); err != nil {
		t.Fatalf("topic global authority: %v", err)
	}

	if _, err := nodeB.Exec(ctx, `UPDATE topics SET author_user_id = 99 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicOwner); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("cross-node topic ownership transfer error=%v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE topics SET author_user_id = 42 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicOwner); err != nil {
		t.Fatalf("restored topic owner: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE topics SET status = 'deleted' WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicOwner); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("cross-node topic soft-delete error=%v", err)
	}
	if _, err := nodeB.Exec(ctx, `DELETE FROM topics WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicOwner); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("cross-node topic hard-delete error=%v", err)
	}

	commentPlan, commentStep := productionForumResourcePlan(t, "core.route.forum.delete_comment", "9")
	commentOwner := productionGuardRequest(identity.PermissionPostDeleteOwn)
	commentOwner.Method, commentOwner.Path, commentOwner.Params = commentPlan.Method(), commentPlan.Path(), commentPlan.Params()
	if err := authorizer.Authorize(ctx, commentPlan, commentStep, commentOwner); err != nil {
		t.Fatalf("initial comment owner: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE comments SET author_user_id = 7 WHERE id = 9`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, commentPlan, commentStep, commentOwner); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("cross-node comment ownership transfer error=%v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE comments SET author_user_id = 42, status = 'deleted' WHERE id = 9`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, commentPlan, commentStep, commentOwner); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("cross-node comment soft-delete error=%v", err)
	}
	if _, err := nodeB.Exec(ctx, `DELETE FROM comments WHERE id = 9`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, commentPlan, commentStep, commentOwner); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("cross-node comment hard-delete error=%v", err)
	}

	// 不存在的资源 id 必须 fail-closed，且与 topic/comment 路径隔离。
	missingTopicPlan, missingTopicStep := productionForumResourcePlan(t, "core.route.forum.delete_topic", "404")
	missingTopic := productionGuardRequest(identity.PermissionTopicDeleteOwn)
	missingTopic.Method, missingTopic.Path, missingTopic.Params = missingTopicPlan.Method(), missingTopicPlan.Path(), missingTopicPlan.Params()
	if err := authorizer.Authorize(ctx, missingTopicPlan, missingTopicStep, missingTopic); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("nonexistent topic error=%v", err)
	}
	missingCommentPlan, missingCommentStep := productionForumResourcePlan(t, "core.route.forum.update_comment", "404")
	missingComment := productionGuardRequest(identity.PermissionPostEditOwn)
	missingComment.Method, missingComment.Path, missingComment.Params = missingCommentPlan.Method(), missingCommentPlan.Path(), missingCommentPlan.Params()
	if err := authorizer.Authorize(ctx, missingCommentPlan, missingCommentStep, missingComment); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("nonexistent comment error=%v", err)
	}
}

func openForumResourceGuardNode(t *testing.T, ctx context.Context, databaseURL, schema string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
