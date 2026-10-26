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

func TestPostgresForumCommentCreateGuardObservesCrossNodeTopicState(t *testing.T) {
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
	schema := fmt.Sprintf("comment_create_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	if _, err := root.Exec(ctx, `
		CREATE TABLE `+quotedSchema+`.topics (id BIGINT PRIMARY KEY, status TEXT NOT NULL);
		INSERT INTO `+quotedSchema+`.topics VALUES (7, 'active')
	`); err != nil {
		t.Fatal(err)
	}
	nodeA := openAttachmentGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openAttachmentGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	store := forum.NewPostgresStore(nodeA)
	authorizer := NewProductionRouteGuardAuthorizerWithPolicies(ProductionRouteGuardPolicies{ForumComments: store})
	plan, step := productionForumCommentCreatePlan(t, "7")
	request := productionGuardRequest(identity.PermissionPostCreate)
	request.Method, request.Path, request.Params = plan.Method(), plan.Path(), plan.Params()
	if err := authorizer.Authorize(ctx, plan, step, request); err != nil {
		t.Fatalf("active topic guard: %v", err)
	}
	for _, status := range []string{forum.TopicStatusLocked, forum.TopicStatusPending, forum.TopicStatusDeleted} {
		if _, err := nodeB.Exec(ctx, `UPDATE topics SET status = $1 WHERE id = 7`, status); err != nil {
			t.Fatal(err)
		}
		if err := authorizer.Authorize(ctx, plan, step, request); !errors.Is(err, ErrRoutePermissionDenied) {
			t.Fatalf("cross-node status %q error=%v", status, err)
		}
	}
	if _, err := nodeB.Exec(ctx, `UPDATE topics SET status = 'active' WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, plan, step, request); err != nil {
		t.Fatalf("restored active topic guard: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `DELETE FROM topics WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, plan, step, request); !errors.Is(err, ErrRouteGuardUnavailable) {
		t.Fatalf("deleted topic error=%v", err)
	}
}
