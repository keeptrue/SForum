package forum

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresReferenceSelectorFiltersNonPublicResources(t *testing.T) {
	ctx, store := newReferenceSelectorPostgresHarness(t)
	tests := []struct {
		kind string
		want []int64
	}{
		{kind: ReferenceKindUser, want: []int64{1}},
		{kind: ReferenceKindTopic, want: []int64{10, 11}},
		{kind: ReferenceKindComment, want: []int64{100, 101}},
		{kind: ReferenceKindCategory, want: []int64{1}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			items, err := store.ListReferenceOptions(ctx, ReferenceSelectorInput{Kind: test.kind, Limit: 20})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(items))
			for _, item := range items {
				got = append(got, item.ID)
				if strings.TrimSpace(item.Label) == "" {
					t.Fatalf("empty recognizable label: %#v", item)
				}
				if test.kind == ReferenceKindUser && item.ID == 1 {
					if item.Avatar == nil || item.Avatar.Kind != "uploaded" || item.Avatar.URL != "/media/avatars/avatar-public" {
						t.Fatalf("user avatar preview = %#v", item.Avatar)
					}
				}
				if test.kind == ReferenceKindCategory && item.ID == 1 {
					if item.Icon != "i-lucide-palette" || item.IconColor != "#7c3aed" {
						t.Fatalf("category visual preview = %#v", item)
					}
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("ids = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPostgresReferenceSelectorSearchesRecognizableFields(t *testing.T) {
	ctx, store := newReferenceSelectorPostgresHarness(t)
	tests := []struct {
		kind  string
		query string
		want  []int64
	}{
		{kind: ReferenceKindUser, query: "ali", want: []int64{1}},
		{kind: ReferenceKindTopic, query: "locked", want: []int64{11}},
		{kind: ReferenceKindComment, query: "alice", want: []int64{100, 101}},
		{kind: ReferenceKindCategory, query: "public", want: []int64{1}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			items, err := store.ListReferenceOptions(ctx, ReferenceSelectorInput{
				Kind: test.kind, Query: test.query, Limit: 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(items))
			for _, item := range items {
				got = append(got, item.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("ids = %v, want %v", got, test.want)
			}
		})
	}
}

func newReferenceSelectorPostgresHarness(t *testing.T) (context.Context, *PostgresStore) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("forum_reference_selector_%d", time.Now().UnixNano())
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
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT, display_name TEXT, email TEXT NOT NULL DEFAULT '', status TEXT)`,
		`CREATE TABLE user_profiles (user_id BIGINT PRIMARY KEY, avatar_attachment_id BIGINT)`,
		`CREATE TABLE attachments (id BIGINT PRIMARY KEY, public_id TEXT, owner_user_id BIGINT, content_type TEXT, status TEXT)`,
		`CREATE TABLE category_groups (id BIGINT PRIMARY KEY, name TEXT, visibility TEXT)`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, group_id BIGINT, slug TEXT, name TEXT, icon TEXT NOT NULL DEFAULT '', icon_color TEXT NOT NULL DEFAULT '', visibility TEXT)`,
		`CREATE TABLE topics (id BIGINT PRIMARY KEY, category_id BIGINT, title TEXT, status TEXT)`,
		`CREATE TABLE comments (id BIGINT PRIMARY KEY, topic_id BIGINT, author_user_id BIGINT, status TEXT)`,
		`INSERT INTO users (id,username,display_name,status) VALUES (1,'alice','Alice','active'),(2,'blocked','Blocked','banned')`,
		`INSERT INTO attachments (id,public_id,owner_user_id,content_type,status) VALUES (9,'avatar-public',1,'image/png','active')`,
		`INSERT INTO user_profiles (user_id,avatar_attachment_id) VALUES (1,9)`,
		`INSERT INTO category_groups VALUES (1,'Public group','public'),(2,'Hidden group','hidden')`,
		`INSERT INTO categories (id,group_id,slug,name,visibility,icon,icon_color) VALUES (1,1,'public','Public category','public','i-lucide-palette','#7c3aed'),(2,1,'hidden','Hidden category','hidden','',''),(3,2,'private-group','Private group category','public','','')`,
		`INSERT INTO topics VALUES (10,1,'Active topic','active'),(11,1,'Locked topic','locked'),(12,1,'Hidden topic','hidden'),(13,2,'Hidden category topic','active'),(14,3,'Hidden group topic','active')`,
		`INSERT INTO comments VALUES (100,10,1,'active'),(101,11,1,'active'),(102,10,1,'deleted'),(103,12,1,'active'),(104,13,1,'active'),(105,14,1,'active')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup reference selector schema: %v\n%s", err, statement)
		}
	}
	return ctx, NewPostgresStore(pool)
}
