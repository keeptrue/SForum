package search

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真实 PostgreSQL 路径：Index → Search → Delete，覆盖 shipped PostgresSiteEngine。
func TestPostgresSiteEngineIndexSearchDelete(t *testing.T) {
	dsn := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	defer pool.Close()

	// 确保表存在（migration 或临时建表，避免依赖 migrate 是否已跑）。
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS search_documents (
		  topic_id BIGINT PRIMARY KEY,
		  title TEXT NOT NULL DEFAULT '',
		  excerpt TEXT NOT NULL DEFAULT '',
		  plain_text TEXT NOT NULL DEFAULT '',
		  category_id BIGINT NOT NULL DEFAULT 0,
		  category_slug TEXT NOT NULL DEFAULT '',
		  category_name TEXT NOT NULL DEFAULT '',
		  author_user_id BIGINT NOT NULL DEFAULT 0,
		  author_username TEXT NOT NULL DEFAULT '',
		  author_display_name TEXT NOT NULL DEFAULT '',
		  slug TEXT NOT NULL DEFAULT '',
		  status TEXT NOT NULL DEFAULT '',
		  is_pinned BOOLEAN NOT NULL DEFAULT false,
		  comment_count BIGINT NOT NULL DEFAULT 0,
		  view_count BIGINT NOT NULL DEFAULT 0,
		  tag_slugs TEXT[] NOT NULL DEFAULT '{}',
		  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	// tsv 可能已存在于正式 migration；若无则尝试添加 generated column（失败则跳过 FTS 用 plain 路径测 upsert）。
	var hasTSV bool
	_ = pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM information_schema.columns
		  WHERE table_name = 'search_documents' AND column_name = 'tsv'
		)
	`).Scan(&hasTSV)
	if !hasTSV {
		_, err = pool.Exec(ctx, `
			ALTER TABLE search_documents ADD COLUMN IF NOT EXISTS tsv tsvector
			GENERATED ALWAYS AS (
			  setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
			  setweight(to_tsvector('simple', coalesce(excerpt, '')), 'B') ||
			  setweight(to_tsvector('simple', coalesce(plain_text, '')), 'C')
			) STORED
		`)
		if err != nil {
			t.Fatalf("add tsv: %v", err)
		}
		_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS search_documents_tsv_idx ON search_documents USING GIN (tsv)`)
	}
	_, err = pool.Exec(ctx, `ALTER TABLE search_documents ADD COLUMN IF NOT EXISTS cjk_tsv tsvector NOT NULL DEFAULT ''::tsvector`)
	if err != nil {
		t.Fatalf("add cjk_tsv: %v", err)
	}
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS search_documents_cjk_tsv_idx ON search_documents USING GIN (cjk_tsv)`)
	_, err = pool.Exec(ctx, `ALTER TABLE search_documents ADD COLUMN IF NOT EXISTS metadata_tsv tsvector NOT NULL DEFAULT ''::tsvector`)
	if err != nil {
		t.Fatalf("add metadata_tsv: %v", err)
	}
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS search_documents_metadata_tsv_idx ON search_documents USING GIN (metadata_tsv)`)
	_, err = pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS sforum_host_extensions`)
	if err != nil {
		t.Fatalf("create Host extension schema: %v", err)
	}
	_, err = pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA sforum_host_extensions`)
	if err != nil {
		t.Fatalf("create pg_trgm: %v", err)
	}
	_, err = pool.Exec(ctx, `
		ALTER TABLE search_documents ADD COLUMN IF NOT EXISTS fuzzy_text TEXT
		GENERATED ALWAYS AS (title || E'\n' || excerpt) STORED
	`)
	if err != nil {
		t.Fatalf("add fuzzy_text: %v", err)
	}
	_, _ = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS search_documents_fuzzy_text_trgm_idx ON search_documents USING GIN (fuzzy_text sforum_host_extensions.gin_trgm_ops)`)

	engine := NewPostgresSiteEngine(pool)
	if err := engine.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := engine.EnsureIndex(ctx); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}

	unique := "site-engine-unique-" + time.Now().Format("150405.000")
	topicID := int64(900000000 + time.Now().Unix()%1000000)
	now := time.Now().UTC()
	doc := TopicSearchDoc{
		ID: topicID, Title: "PG FTS " + unique, Excerpt: "ex", PlainText: "body " + unique,
		CategorySlug: "dev", CategoryName: "开发交流", Status: "active",
		AuthorUsername: "dalao", AuthorDisplayName: "小明",
		TagSlugs: []string{"go"}, CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}
	// cleanup
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, topicID)
	})

	if err := engine.Index(ctx, doc); err != nil {
		t.Fatalf("Index: %v", err)
	}
	// 非公开状态不应命中
	if err := engine.Index(ctx, TopicSearchDoc{
		ID: topicID + 1, Title: unique + " draft", Status: "draft", PlainText: unique,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("Index draft: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, topicID+1)
	})

	res, err := engine.Search(ctx, SearchInput{Query: unique, Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total < 1 {
		t.Fatalf("expected hits for %q, total=%d items=%+v", unique, res.Total, res.Items)
	}
	found := false
	for _, item := range res.Items {
		if item.ID == topicID {
			found = true
		}
		if item.ID == topicID+1 {
			t.Fatalf("draft leaked into results: %+v", item)
		}
		if item.PlainText != "" {
			t.Fatalf("plainText not stripped: %+v", item)
		}
		if !IsPublicSearchStatus(item.Status) {
			t.Fatalf("non-public: %+v", item)
		}
	}
	if !found {
		t.Fatalf("active topic %d not in results %+v", topicID, res.Items)
	}
	for _, metadataQuery := range []string{"小明", "dalao", "开发交流", "go"} {
		metadataResult, err := engine.Search(ctx, SearchInput{Query: metadataQuery, Page: 1, PerPage: 20})
		if err != nil {
			t.Fatalf("Search metadata %q: %v", metadataQuery, err)
		}
		if !containsSearchDoc(metadataResult.Items, topicID) {
			t.Fatalf("expected metadata %q to match topic %d: %+v", metadataQuery, topicID, metadataResult.Items)
		}
	}

	chineseTopicID := topicID + 2
	if err := engine.Index(ctx, TopicSearchDoc{
		ID: chineseTopicID, Title: "分享一段我写的糟糕代码，求重构建议", PlainText: "正文",
		CategorySlug: "dev", CategoryName: "Dev", Status: "active",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("Index Chinese topic: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, chineseTopicID)
	})
	chineseResult, err := engine.Search(ctx, SearchInput{Query: "分享", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search Chinese prefix: %v", err)
	}
	foundChinese := false
	for _, item := range chineseResult.Items {
		if item.ID == chineseTopicID {
			foundChinese = true
			break
		}
	}
	if !foundChinese {
		t.Fatalf("expected 分享 to match Chinese topic %d: %+v", chineseTopicID, chineseResult.Items)
	}
	insideResult, err := engine.Search(ctx, SearchInput{Query: "代码", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search Chinese infix: %v", err)
	}
	foundInside := false
	for _, item := range insideResult.Items {
		if item.ID == chineseTopicID {
			foundInside = true
			break
		}
	}
	if !foundInside {
		t.Fatalf("expected 代码 to match inside Chinese title %d: %+v", chineseTopicID, insideResult.Items)
	}
	oneCharacterResult, err := engine.Search(ctx, SearchInput{Query: "码", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search one Chinese character: %v", err)
	}
	if !containsSearchDoc(oneCharacterResult.Items, chineseTopicID) {
		t.Fatalf("expected single Han character to match topic %d: %+v", chineseTopicID, oneCharacterResult.Items)
	}
	fuzzyResult, err := engine.Search(ctx, SearchInput{Query: "分享一断", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search Chinese typo: %v", err)
	}
	if !containsSearchDoc(fuzzyResult.Items, chineseTopicID) {
		t.Fatalf("expected typo-tolerant query to match topic %d: %+v", chineseTopicID, fuzzyResult.Items)
	}
	distractorTopicID := topicID + 5
	if err := engine.Index(ctx, TopicSearchDoc{
		ID: distractorTopicID, Title: "分享一份我刚整理的面试题集", PlainText: "相似开头",
		CategorySlug: "dev", CategoryName: "Dev", Status: "active",
		CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour), LastActivityAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Index fuzzy distractor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, distractorTopicID)
	})
	fuzzyRanked, err := engine.Search(ctx, SearchInput{Query: "分享一断", CategorySlug: "dev", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search fuzzy relevance ordering: %v", err)
	}
	if !containsSearchDoc(fuzzyRanked.Items, chineseTopicID) || !containsSearchDoc(fuzzyRanked.Items, distractorTopicID) {
		t.Fatalf("expected both fuzzy candidates: %+v", fuzzyRanked.Items)
	}
	if searchDocPosition(fuzzyRanked.Items, chineseTopicID) >= searchDocPosition(fuzzyRanked.Items, distractorTopicID) {
		t.Fatalf("closer typo match must outrank newer distractor: %+v", fuzzyRanked.Items)
	}

	bodyTopicID := topicID + 3
	if err := engine.Index(ctx, TopicSearchDoc{
		ID: bodyTopicID, Title: "数据库经验", PlainText: "这里讨论高并发场景下的全文检索优化方案",
		CategorySlug: "dev", CategoryName: "Dev", Status: "active", IsPinned: true,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("Index Chinese body topic: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, bodyTopicID)
	})
	bodyResult, err := engine.Search(ctx, SearchInput{Query: "全文检索", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search Chinese body: %v", err)
	}
	if !containsSearchDoc(bodyResult.Items, bodyTopicID) {
		t.Fatalf("expected Chinese body match for topic %d: %+v", bodyTopicID, bodyResult.Items)
	}
	exactTopicID := topicID + 4
	if err := engine.Index(ctx, TopicSearchDoc{
		ID: exactTopicID, Title: "全文检索", PlainText: "精确标题",
		CategorySlug: "dev", CategoryName: "Dev", Status: "active",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), LastActivityAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Index exact-title topic: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM search_documents WHERE topic_id = $1`, exactTopicID)
	})
	rankedResult, err := engine.Search(ctx, SearchInput{Query: "全文检索", Page: 1, PerPage: 20})
	if err != nil {
		t.Fatalf("Search relevance ordering: %v", err)
	}
	if len(rankedResult.Items) < 2 || rankedResult.Items[0].ID != exactTopicID {
		t.Fatalf("exact title must outrank newer pinned body hit: %+v", rankedResult.Items)
	}

	if err := engine.Delete(ctx, topicID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	res, err = engine.Search(ctx, SearchInput{Query: unique, Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	for _, item := range res.Items {
		if item.ID == topicID {
			t.Fatalf("deleted topic still returned")
		}
	}
}

func containsSearchDoc(items []TopicSearchDoc, topicID int64) bool {
	for _, item := range items {
		if item.ID == topicID {
			return true
		}
	}
	return false
}

func searchDocPosition(items []TopicSearchDoc, topicID int64) int {
	for index, item := range items {
		if item.ID == topicID {
			return index
		}
	}
	return len(items) + 1
}
