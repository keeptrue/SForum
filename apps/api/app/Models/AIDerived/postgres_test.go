package aiderived_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 这一组把 B3 的落库形态钉在真实 PostgreSQL 上：内存 fake 证明不了「status 真的
// 是 draft」「CHECK 约束真的拦住了野用途」「列表顺序真的是 created_at DESC」——
// 而这三件事正是「产物落成预期位置」这条验收的可验证形态。
//
// 与 citations_postgres_test.go 同一套隔离策略：独立 schema + search_path，
// 测试结束整体 DROP，不触碰开发库的公开表。

func TestPostgresArtifactsRoundTripDraftOnly(t *testing.T) {
	ctx, pool := newDerivedPostgresHarness(t)
	store := aiderived.NewPostgresArtifactStore(pool)

	created, err := store.Insert(ctx, aiderived.Artifact{
		Purpose:   aiderived.PurposeDigest,
		TopicID:   42,
		PromptVer: supportai.CoreDigestPromptVersion,
		Status:    aiderived.StatusDraft,
		Content:   "一句话结论：设 20。\n\n关键要点：\n- 池子上限按并发算",
		CreatedBy: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if created.ID <= 0 || created.CreatedAt.IsZero() {
		t.Fatalf("insert must return generated id and timestamp: %+v", created)
	}

	read, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Status != aiderived.StatusDraft {
		t.Fatalf("status = %q, want draft", read.Status)
	}
	if read.PromptVer != supportai.CoreDigestPromptVersion {
		t.Fatalf("promptVersion = %q", read.PromptVer)
	}
	if read.CreatedBy != 1 || read.TopicID != 42 {
		t.Fatalf("read = %+v", read)
	}
	// 正文必须原样读回：它是运营者要审阅的东西，任何裁剪都是内容丢失。
	if !strings.Contains(read.Content, "关键要点") {
		t.Fatalf("content = %q", read.Content)
	}

	if _, err := store.Get(ctx, 999_999); err == nil {
		t.Fatal("a missing artifact must not be readable")
	}
}

// CHECK 约束是第二道防线：NormalizeArtifact 已经拦住野用途，但直接写库的路径
// （将来的批量脚本、运维 SQL）也必须被数据库拒绝。
func TestPostgresArtifactsRejectUndeclaredPurposeAtDatabaseLevel(t *testing.T) {
	ctx, pool := newDerivedPostgresHarness(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_derived_artifacts (purpose, topic_id, prompt_version, content)
		VALUES ('forum.summarize', 42, 'x@1', '正文')
	`); err == nil {
		t.Fatal("the table must refuse a purpose that is not a derived purpose")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_derived_artifacts (purpose, topic_id, prompt_version, content)
		VALUES ('forum.digest', 42, 'x@1', '')
	`); err == nil {
		t.Fatal("the table must refuse an empty content row")
	}
}

func TestPostgresArtifactsListOrdersNewestFirst(t *testing.T) {
	ctx, pool := newDerivedPostgresHarness(t)
	store := aiderived.NewPostgresArtifactStore(pool)

	for index, purpose := range []string{aiderived.PurposeDigest, aiderived.PurposeTour, aiderived.PurposeDigest} {
		if _, err := store.Insert(ctx, aiderived.Artifact{
			Purpose:   purpose,
			TopicID:   int64(42 + index%2),
			PromptVer: "p@" + string(rune('1'+index)),
			Content:   fmt.Sprintf("草稿 %d", index),
		}); err != nil {
			t.Fatalf("insert %d: %v", index, err)
		}
	}
	all, err := store.List(ctx, aiderived.ArtifactFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len = %d", len(all))
	}
	for index := 1; index < len(all); index++ {
		if all[index].CreatedAt.After(all[index-1].CreatedAt) {
			t.Fatalf("list must be newest first: %+v", all)
		}
	}

	digests, err := store.List(ctx, aiderived.ArtifactFilter{Purpose: aiderived.PurposeDigest})
	if err != nil {
		t.Fatal(err)
	}
	if len(digests) != 2 {
		t.Fatalf("purpose filter = %d", len(digests))
	}
	tours, err := store.List(ctx, aiderived.ArtifactFilter{TopicID: 43})
	if err != nil {
		t.Fatal(err)
	}
	if len(tours) != 1 || tours[0].TopicID != 43 {
		t.Fatalf("topic filter = %+v", tours)
	}
	drafts, err := store.List(ctx, aiderived.ArtifactFilter{Status: aiderived.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 3 {
		t.Fatalf("status filter = %d", len(drafts))
	}
}

// 未指定发起者时写 NULL，且账号被删除后产物仍可读：外键 SET NULL 的语义必须
// 在读取侧被正确处理——否则「发起者已不存在」会让一份历史草稿永远打不开。
func TestPostgresArtifactsSurviveCreatorDeletion(t *testing.T) {
	ctx, pool := newDerivedPostgresHarness(t)
	store := aiderived.NewPostgresArtifactStore(pool)

	anonymous, err := store.Insert(ctx, aiderived.Artifact{
		Purpose:   aiderived.PurposeDigest,
		TopicID:   42,
		PromptVer: supportai.CoreDigestPromptVersion,
		Content:   "系统触发的草稿",
	})
	if err != nil {
		t.Fatalf("Insert without creator: %v", err)
	}
	if anonymous.CreatedBy != 0 {
		t.Fatalf("createdBy = %d, want 0 for an absent creator", anonymous.CreatedBy)
	}

	owned, err := store.Insert(ctx, aiderived.Artifact{
		Purpose:   aiderived.PurposeTour,
		TopicID:   42,
		PromptVer: supportai.CoreTourPromptVersion,
		Content:   "由运营者触发的草稿",
		CreatedBy: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if owned.CreatedBy != 1 {
		t.Fatalf("createdBy = %d", owned.CreatedBy)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = 1`); err != nil {
		t.Fatalf("delete creator: %v", err)
	}
	read, err := store.Get(ctx, owned.ID)
	if err != nil {
		t.Fatalf("an artifact must stay readable after its creator is deleted: %v", err)
	}
	if read.CreatedBy != 0 {
		t.Fatalf("createdBy = %d, want 0 once the account is gone", read.CreatedBy)
	}
}

// 派生任务不写 comments / topics：这是 B3 的硬边界，用真库断言而不是靠注释。
func TestPostgresArtifactsDoNotTouchCommentsOrTopics(t *testing.T) {
	ctx, pool := newDerivedPostgresHarness(t)
	store := aiderived.NewPostgresArtifactStore(pool)

	beforeComments, beforeTopics := countRows(t, ctx, pool, "comments"), countRows(t, ctx, pool, "topics")
	if _, err := store.Insert(ctx, aiderived.Artifact{
		Purpose:   aiderived.PurposeTour,
		TopicID:   42,
		PromptVer: supportai.CoreTourPromptVersion,
		Content:   "主线：#500 → #501",
	}); err != nil {
		t.Fatal(err)
	}
	if after := countRows(t, ctx, pool, "comments"); after != beforeComments {
		t.Fatalf("comments changed: %d → %d", beforeComments, after)
	}
	if after := countRows(t, ctx, pool, "topics"); after != beforeTopics {
		t.Fatalf("topics changed: %d → %d", beforeTopics, after)
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// newDerivedPostgresHarness 建一个隔离 schema，只包含迁移 202610190004 的表。
func newDerivedPostgresHarness(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aiderived_%d", time.Now().UnixNano())
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
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT NOT NULL)`,
		// 两个空表只为「派生不写公共内容」这条断言存在。
		`CREATE TABLE topics (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE comments (id BIGINT PRIMARY KEY)`,
		// 与迁移 202610190004 同形：CHECK 约束逐条照抄，否则这条测试证明的不是
		// 生产表的形状。
		`CREATE TABLE ai_derived_artifacts (
			id BIGSERIAL PRIMARY KEY,
			purpose TEXT NOT NULL CHECK (purpose IN ('forum.digest', 'forum.tour')),
			topic_id BIGINT NOT NULL CHECK (topic_id > 0),
			run_id TEXT NOT NULL DEFAULT '',
			prompt_version TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'discarded')),
			content TEXT NOT NULL CHECK (content <> ''),
			created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX ai_derived_artifacts_purpose_idx ON ai_derived_artifacts (purpose, created_at DESC, id DESC)`,
		`CREATE INDEX ai_derived_artifacts_topic_idx ON ai_derived_artifacts (topic_id, created_at DESC, id DESC)`,
		`CREATE INDEX ai_derived_artifacts_status_idx ON ai_derived_artifacts (status, created_at DESC)`,
		`INSERT INTO users (id, username) VALUES (1, 'alice')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup derived schema: %v\n%s", err, statement)
		}
	}
	return ctx, pool
}
