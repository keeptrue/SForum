package forum

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 分类/分组删除的真实 Postgres 契约（自带最小 schema + 真实 ON DELETE RESTRICT）：
//  1. includeHidden=false 只返回公开项；true 返回隐藏分组与隐藏分类（后台可维护）。
//  2. 非空分组、有主题的分类删除被数据库 RESTRICT 拒绝，并翻译成业务错误。
//  3. 空分组、无主题分类可删除；重复删除返回 not found。
func TestPostgresTaxonomyDeleteGuards(t *testing.T) {
	ctx, store := newTaxonomyPostgresHarness(t)

	publicGroup, err := store.CreateCategoryGroup(ctx, CreateCategoryGroupInput{Slug: "public-group", Name: "公开分组", Visibility: "public"})
	if err != nil {
		t.Fatalf("create public group: %v", err)
	}
	hiddenGroup, err := store.CreateCategoryGroup(ctx, CreateCategoryGroupInput{Slug: "hidden-group", Name: "隐藏分组", Visibility: "hidden"})
	if err != nil {
		t.Fatalf("create hidden group: %v", err)
	}
	topicGroup, err := store.CreateCategoryGroup(ctx, CreateCategoryGroupInput{Slug: "topic-group", Name: "有主题分组", Visibility: "public"})
	if err != nil {
		t.Fatalf("create topic group: %v", err)
	}
	hiddenCategory, err := store.CreateCategory(ctx, CreateCategoryInput{GroupID: publicGroup.ID, Slug: "hidden-board", Name: "隐藏版块", Visibility: "hidden", DefaultSort: "latest"})
	if err != nil {
		t.Fatalf("create hidden category: %v", err)
	}
	emptyCategory, err := store.CreateCategory(ctx, CreateCategoryInput{GroupID: publicGroup.ID, Slug: "empty-board", Name: "空版块", Visibility: "public", DefaultSort: "latest"})
	if err != nil {
		t.Fatalf("create empty category: %v", err)
	}
	topicCategory, err := store.CreateCategory(ctx, CreateCategoryInput{GroupID: topicGroup.ID, Slug: "topic-board", Name: "有主题版块", Visibility: "public", DefaultSort: "latest"})
	if err != nil {
		t.Fatalf("create topic category: %v", err)
	}

	publicCategories, err := store.ListCategories(ctx, false)
	if err != nil {
		t.Fatalf("list public categories: %v", err)
	}
	if categorySlugsInclude(publicCategories, "hidden-board") {
		t.Fatalf("public listing leaked a hidden category: %#v", publicCategories)
	}
	adminCategories, err := store.ListCategories(ctx, true)
	if err != nil {
		t.Fatalf("list admin categories: %v", err)
	}
	if !categorySlugsInclude(adminCategories, "hidden-board") {
		t.Fatalf("admin listing must include hidden categories: %#v", adminCategories)
	}

	publicGroups, err := store.ListCategoryGroups(ctx, false)
	if err != nil {
		t.Fatalf("list public groups: %v", err)
	}
	if groupSlugsInclude(publicGroups, "hidden-group") {
		t.Fatalf("public listing leaked a hidden group: %#v", publicGroups)
	}
	adminGroups, err := store.ListCategoryGroups(ctx, true)
	if err != nil {
		t.Fatalf("list admin groups: %v", err)
	}
	if findGroup(adminGroups, "hidden-group") == nil {
		t.Fatalf("admin listing must include hidden groups: %#v", adminGroups)
	}
	visibleListed := findGroup(adminGroups, "public-group")
	if visibleListed == nil || !categorySlugsInclude(visibleListed.Categories, "hidden-board") {
		t.Fatalf("admin group listing must include hidden child categories: %#v", visibleListed)
	}

	// 非空分组不可删除。
	if _, err := store.DeleteCategoryGroup(ctx, publicGroup.ID); !errors.Is(err, ErrCategoryGroupNotEmpty) {
		t.Fatalf("delete non-empty group error = %v, want ErrCategoryGroupNotEmpty", err)
	}
	// 无主题分类可删除；重复删除返回 not found。
	if _, err := store.DeleteCategory(ctx, emptyCategory.ID); err != nil {
		t.Fatalf("delete empty category: %v", err)
	}
	if _, err := store.DeleteCategory(ctx, emptyCategory.ID); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("delete missing category error = %v, want ErrCategoryNotFound", err)
	}

	// 有主题的分类不可删除（topics.category_id 是 ON DELETE RESTRICT）。
	if _, err := store.pool.Exec(ctx, `INSERT INTO topics (category_id) VALUES ($1)`, topicCategory.ID); err != nil {
		t.Fatalf("insert topic: %v", err)
	}
	if _, err := store.DeleteCategory(ctx, topicCategory.ID); !errors.Is(err, ErrCategoryNotEmpty) {
		t.Fatalf("delete category with topics error = %v, want ErrCategoryNotEmpty", err)
	}

	// 隐藏分类删除后分组清空，分组才可删除。
	if _, err := store.DeleteCategory(ctx, hiddenCategory.ID); err != nil {
		t.Fatalf("delete hidden category: %v", err)
	}
	if _, err := store.DeleteCategoryGroup(ctx, publicGroup.ID); err != nil {
		t.Fatalf("delete emptied group: %v", err)
	}
	// 有主题分类所在的分组仍不可删除。
	if _, err := store.DeleteCategoryGroup(ctx, topicGroup.ID); !errors.Is(err, ErrCategoryGroupNotEmpty) {
		t.Fatalf("delete group with topic category error = %v, want ErrCategoryGroupNotEmpty", err)
	}
	if _, err := store.DeleteCategoryGroup(ctx, hiddenGroup.ID); err != nil {
		t.Fatalf("delete hidden group: %v", err)
	}
	if _, err := store.DeleteCategoryGroup(ctx, hiddenGroup.ID); !errors.Is(err, ErrCategoryGroupNotFound) {
		t.Fatalf("delete missing group error = %v, want ErrCategoryGroupNotFound", err)
	}
}

func newTaxonomyPostgresHarness(t *testing.T) (context.Context, *PostgresStore) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("forum_taxonomy_%d", time.Now().UnixNano())
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

	// 与真实迁移一致的可见性与外键约束：隐藏项过滤、RESTRICT 删除保护都在数据库层生效。
	for _, statement := range []string{
		`CREATE TABLE category_groups (
		  id BIGSERIAL PRIMARY KEY,
		  slug TEXT NOT NULL UNIQUE,
		  name TEXT NOT NULL,
		  description TEXT NOT NULL DEFAULT '',
		  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'hidden')),
		  position INTEGER NOT NULL DEFAULT 0,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE categories (
		  id BIGSERIAL PRIMARY KEY,
		  group_id BIGINT NOT NULL REFERENCES category_groups(id) ON DELETE RESTRICT,
		  slug TEXT NOT NULL UNIQUE,
		  name TEXT NOT NULL,
		  description TEXT NOT NULL DEFAULT '',
		  icon TEXT NOT NULL DEFAULT '',
		  icon_color TEXT NOT NULL DEFAULT '',
		  visibility TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'hidden')),
		  position INTEGER NOT NULL DEFAULT 0,
		  default_sort TEXT NOT NULL DEFAULT 'latest',
		  topic_count BIGINT NOT NULL DEFAULT 0,
		  comment_count BIGINT NOT NULL DEFAULT 0,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE topics (
		  id BIGSERIAL PRIMARY KEY,
		  category_id BIGINT NOT NULL REFERENCES categories(id) ON DELETE RESTRICT
		)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("apply taxonomy fixture schema: %v", err)
		}
	}
	return ctx, NewPostgresStore(pool)
}

func categorySlugsInclude(items []Category, slug string) bool {
	for _, item := range items {
		if item.Slug == slug {
			return true
		}
	}
	return false
}

func groupSlugsInclude(items []CategoryGroup, slug string) bool {
	return findGroup(items, slug) != nil
}

func findGroup(items []CategoryGroup, slug string) *CategoryGroup {
	for index := range items {
		if items[index].Slug == slug {
			return &items[index]
		}
	}
	return nil
}
