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
	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestPostgresEntityMetaValueGuardObservesCrossNodeAuthority(t *testing.T) {
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
	schema := fmt.Sprintf("entity_meta_guard_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	for _, statement := range []string{
		`CREATE TABLE ` + quotedSchema + `.users (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE ` + quotedSchema + `.topics (id BIGINT PRIMARY KEY, author_user_id BIGINT NOT NULL)`,
		`CREATE TABLE ` + quotedSchema + `.entity_field_definitions (
			field_key TEXT PRIMARY KEY, entity_type TEXT NOT NULL, visibility TEXT NOT NULL, enabled BOOLEAN NOT NULL
		)`,
		`INSERT INTO ` + quotedSchema + `.users (id) VALUES (7), (42)`,
		`INSERT INTO ` + quotedSchema + `.topics (id, author_user_id) VALUES (7, 42)`,
		`INSERT INTO ` + quotedSchema + `.entity_field_definitions VALUES ('profile.note', 'user', 'owner', true), ('topic.note', 'topic', 'owner', true)`,
	} {
		if _, err := root.Exec(ctx, statement); err != nil {
			t.Fatalf("install entity-meta guard fixture: %v\n%s", err, statement)
		}
	}
	nodeA := openEntityMetaGuardNode(t, ctx, databaseURL, schema)
	defer nodeA.Close()
	nodeB := openEntityMetaGuardNode(t, ctx, databaseURL, schema)
	defer nodeB.Close()
	store := entitymeta.NewPostgresStore(nodeA)
	authorizer := entityMetaValueAuthorizer(store)
	userSubject, err := store.LoadValueGuardSubject(ctx, entitymeta.EntityUser, 7, []string{"profile.note"})
	if err != nil {
		t.Fatalf("load user guard subject: %v", err)
	}
	if !userSubject.Exists || userSubject.OwnerUserID != 7 || len(userSubject.Fields) != 1 {
		t.Fatalf("user guard subject = %#v", userSubject)
	}

	userPlan, userStep := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", entitymeta.EntityUser)
	userRequest := productionGuardRequest()
	userRequest.ActorID = 7
	userRequest.Method, userRequest.Path, userRequest.Params = userPlan.Method(), userPlan.Path(), userPlan.Params()
	userRequest.Body = []byte(`{"values":[{"fieldKey":"profile.note","value":"x"}]}`)
	if err := authorizer.Authorize(ctx, userPlan, userStep, userRequest); err != nil {
		t.Fatalf("user owner guard: %v", err)
	}

	topicPlan, topicStep := productionEntityMetaValuePlan(t, "core.route.entity_meta.upsert_values", entitymeta.EntityTopic)
	topicRequest := productionGuardRequest(identity.PermissionTopicEditOwn)
	topicRequest.Method, topicRequest.Path, topicRequest.Params = topicPlan.Method(), topicPlan.Path(), topicPlan.Params()
	topicRequest.Body = []byte(`{"values":[{"fieldKey":"topic.note","value":"x"}]}`)
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicRequest); err != nil {
		t.Fatalf("topic owner guard: %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE topics SET author_user_id = 99 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicRequest); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("cross-node topic owner error = %v", err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE topics SET author_user_id = 42 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeB.Exec(ctx, `UPDATE entity_field_definitions SET visibility = 'admin' WHERE field_key = 'topic.note'`); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Authorize(ctx, topicPlan, topicStep, topicRequest); !errors.Is(err, ErrRoutePermissionDenied) {
		t.Fatalf("cross-node field visibility error = %v", err)
	}
}

func openEntityMetaGuardNode(t *testing.T, ctx context.Context, databaseURL, schema string) *pgxpool.Pool {
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
