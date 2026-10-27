package hostapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestShortcodeProjectionPostgresVisibilityAndActorMatrix(t *testing.T) {
	ctx, pool := newShortcodeProjectionPostgresHarness(t)
	all := []int64{10, 11, 12, 13, 14, 15, 16, 17}

	users := executeShortcodeProjection(t, ctx, pool, QueryShortcodePublicUsersBatch, 0, "", []int64{1, 2, 3, 4, 5})
	assertProjectionIDs(t, users, "id", "1", "2", "3", "4")

	topics := executeShortcodeProjection(t, ctx, pool, QueryShortcodePublicTopicsBatch, 0, "", all)
	assertProjectionIDs(t, topics, "id", "10", "15")
	comments := executeShortcodeProjection(t, ctx, pool, QueryShortcodePublicCommentsBatch, 0, "", []int64{100, 101, 102, 103, 104, 105, 106})
	assertProjectionIDs(t, comments, "id", "100", "106")
	for _, row := range comments {
		if row["owning_topic_public"] != true {
			t.Fatalf("comment lacks owning-topic proof: %#v", row)
		}
	}
	categories := executeShortcodeProjection(t, ctx, pool, QueryShortcodePublicCategoriesBatch, 0, "", []int64{1, 2, 3})
	assertProjectionIDs(t, categories, "id", "1")
	links := executeShortcodeProjection(t, ctx, pool, QueryShortcodeFriendLinksList, 0, "", nil)
	assertProjectionIDs(t, links, "id", "2", "1")

	assertAuthorDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeAuthorDecisionsBatch, 0, "comment", []int64{100}), false, false, false)
	assertAuthorDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeAuthorDecisionsBatch, 1, "comment", []int64{100}), true, false, true)
	assertAuthorDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeAuthorDecisionsBatch, 2, "comment", []int64{100}), true, true, false)
	assertAuthorDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeAuthorDecisionsBatch, 3, "comment", []int64{100}), true, false, false)
	// User 4 represents a privileged administrator. Public-page policy has no staff bypass.
	assertAuthorDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeAuthorDecisionsBatch, 4, "comment", []int64{100}), true, false, false)

	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 0, "", []int64{10}), false, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 1, "", []int64{10}), true, true)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 2, "", []int64{10}), true, true)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 3, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 4, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 6, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 7, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 8, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 9, "", []int64{10}), true, false)
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 10, "", []int64{10}), true, false)
	// Pending/rejected comments never create reply eligibility; user 3 only has an accepted comment on topic 15.
	assertReplyDecision(t, executeShortcodeProjection(t, ctx, pool, QueryShortcodeReplyEligibilityBatch, 3, "", []int64{15}), true, true)
}

func executeShortcodeProjection(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryID string,
	actorUserID int64,
	resourceType string,
	ids []int64,
) []map[string]any {
	t.Helper()
	definition := shortcodeProjectionDefinition(t, queryID)
	filters := make([]protocolV2QueryFilter, 0, len(definition.Filters))
	for _, filter := range definition.Filters {
		switch filter.Field {
		case "resource_type":
			filters = append(filters, protocolV2QueryFilter{Definition: filter, Value: resourceType})
		case "ids":
			filters = append(filters, protocolV2QueryFilter{Definition: filter, Value: ids})
		}
	}
	plan := protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields, Filters: filters,
		Sorts: definition.DefaultSorts, Limit: ShortcodeProjectionMaxBatch,
		FetchLimit: ShortcodeProjectionMaxBatch + 1,
	}
	executionContext := ctx
	if definition.ActorScoped {
		executionContext = contextWithShortcodeProjectionActor(ctx, actorUserID)
	}
	rows, err := (&postgresProtocolV2QueryExecutor{pool: pool}).ExecuteProtocolV2Query(executionContext, plan)
	if err != nil {
		t.Fatalf("execute %s actor=%d: %v", queryID, actorUserID, err)
	}
	return rows
}

func shortcodeProjectionDefinition(t *testing.T, queryID string) protocolV2QueryDefinition {
	t.Helper()
	for _, definition := range shortcodeProjectionProtocolV2QueryDefinitions() {
		if definition.ID == queryID {
			return definition
		}
	}
	t.Fatalf("missing definition %s", queryID)
	return protocolV2QueryDefinition{}
}

func assertProjectionIDs(t *testing.T, rows []map[string]any, field string, want ...string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("rows=%#v want IDs=%v", rows, want)
	}
	for index, id := range want {
		if rows[index][field] != id {
			t.Fatalf("rows=%#v want IDs=%v", rows, want)
		}
	}
}

func assertAuthorDecision(t *testing.T, rows []map[string]any, authenticated, resourceAuthor, topicAuthor bool) {
	t.Helper()
	if len(rows) != 1 || rows[0]["authenticated"] != authenticated ||
		rows[0]["is_resource_author"] != resourceAuthor || rows[0]["is_topic_author"] != topicAuthor {
		t.Fatalf("author decision=%#v", rows)
	}
}

func assertReplyDecision(t *testing.T, rows []map[string]any, authenticated, eligible bool) {
	t.Helper()
	if len(rows) != 1 || rows[0]["authenticated"] != authenticated || rows[0]["eligible"] != eligible {
		t.Fatalf("reply decision=%#v", rows)
	}
}

func newShortcodeProjectionPostgresHarness(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("shortcode_projection_%d", time.Now().UnixNano())
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
	setupShortcodeProjectionSchema(t, ctx, pool)
	return ctx, pool
}

func setupShortcodeProjectionSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT, display_name TEXT, status TEXT)`,
		`CREATE TABLE category_groups (id BIGINT PRIMARY KEY, visibility TEXT)`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, group_id BIGINT, slug TEXT, name TEXT, description TEXT, icon TEXT, icon_color TEXT, visibility TEXT)`,
		`CREATE TABLE posts (id BIGINT PRIMARY KEY, plain_text TEXT)`,
		`CREATE TABLE topics (id BIGINT PRIMARY KEY, category_id BIGINT, content_id BIGINT, author_user_id BIGINT, title TEXT, slug TEXT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE comments (id BIGINT PRIMARY KEY, topic_id BIGINT, content_id BIGINT, author_user_id BIGINT, status TEXT, deleted_at TIMESTAMPTZ, created_at TIMESTAMPTZ)`,
		`CREATE TABLE site_friend_links (id BIGINT PRIMARY KEY, name TEXT, url TEXT, description TEXT, logo_url TEXT, position INTEGER, enabled BOOLEAN)`,
		`INSERT INTO users VALUES (1,'author','Author','active'),(2,'commenter','Commenter','active'),(3,'other','Other','active'),(4,'admin','Admin','active'),(5,'blocked','Blocked','banned'),(6,'pending','Pending','active'),(7,'rejected','Rejected','active'),(8,'hidden-comment','Hidden comment','active'),(9,'deleted-comment','Deleted comment','active'),(10,'other-topic','Other topic','active')`,
		`INSERT INTO category_groups VALUES (1,'public'),(2,'hidden')`,
		`INSERT INTO categories VALUES (1,1,'general','General','','','', 'public'),(2,1,'hidden','Hidden','','','', 'hidden'),(3,2,'private-group','Private Group','','','', 'public')`,
		`INSERT INTO posts SELECT value, 'public excerpt ' || value FROM generate_series(1,30) AS value`,
		`INSERT INTO topics VALUES
			(10,1,1,1,'Public','public','active',NULL),
			(11,1,2,1,'Hidden','hidden','hidden',NULL),
			(12,1,3,1,'Deleted','deleted','deleted',now()),
			(13,1,4,1,'Pending','pending','pending',NULL),
			(14,1,5,1,'Rejected','rejected','rejected',NULL),
			(15,1,6,2,'Locked','locked','locked',NULL),
			(16,2,7,1,'Hidden category','hidden-category','active',NULL),
			(17,1,8,1,'Deleted marker','deleted-marker','active',now())`,
		`INSERT INTO comments VALUES
			(100,10,10,2,'active',NULL,now()),
			(101,10,11,2,'hidden',NULL,now()),
			(102,10,12,2,'deleted',now(),now()),
			(103,10,13,3,'pending',NULL,now()),
			(104,10,14,3,'rejected',NULL,now()),
			(105,11,15,3,'active',NULL,now()),
			(106,15,16,3,'active',NULL,now()),
			(107,10,17,6,'pending',NULL,now()),
			(108,10,18,7,'rejected',NULL,now()),
			(109,10,19,8,'hidden',NULL,now()),
			(110,10,20,9,'deleted',now(),now()),
			(111,15,21,10,'active',NULL,now())`,
		`INSERT INTO site_friend_links VALUES
			(1,'Later','https://later.example','','',20,TRUE),
			(2,'First','https://first.example','','',10,TRUE),
			(3,'Disabled','https://disabled.example','','',0,FALSE)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup shortcode projection schema: %v\n%s", err, statement)
		}
	}
}
