package profile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 公开用户检索的 SQL 只有在真实 Postgres 上才能证明三件事：活跃用户过滤真的生效、
// LIKE 元字符真的被转义（否则 `%` 查询会变回「列出全站成员」）、空查询不会落到库上。
func TestPostgresSearchPublicUsersFiltersEscapesAndOrders(t *testing.T) {
	ctx, store := newUserSearchPostgresHarness(t)

	t.Run("matches username and display name case-insensitively, exact first", func(t *testing.T) {
		items, err := store.SearchPublicUsers(ctx, "  ALICE  ", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 || items[0].Username != "alice" || items[1].Username != "alice2" {
			t.Fatalf("hits = %#v", items)
		}
		if items[0].DisplayName != "Alice" {
			t.Fatalf("display name = %q", items[0].DisplayName)
		}
	})

	t.Run("falls back to the username when the display name is blank", func(t *testing.T) {
		items, err := store.SearchPublicUsers(ctx, "ghost", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].DisplayName != "ghost" {
			t.Fatalf("hits = %#v", items)
		}
	})

	t.Run("excludes non-active accounts", func(t *testing.T) {
		items, err := store.SearchPublicUsers(ctx, "inactive", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("banned and disabled users must not be discoverable: %#v", items)
		}
	})

	t.Run("treats like wildcards as literals", func(t *testing.T) {
		// `%` 与 `_` 都是 LIKE 元字符：转义失效时它们会匹配全表，那正是「列出全站成员」
		// 这条路径必须被堵住的地方。
		items, err := store.SearchPublicUsers(ctx, "%", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Username != "100%pure" {
			t.Fatalf("a bare %% must only match a literal %%, got %#v", items)
		}
		items, err = store.SearchPublicUsers(ctx, "_", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Username != "li_wei" {
			t.Fatalf("a bare _ must only match a literal _, got %#v", items)
		}
		items, err = store.SearchPublicUsers(ctx, "100%pure", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Username != "100%pure" {
			t.Fatalf("a literal %% in the query must still match: %#v", items)
		}
	})

	t.Run("bounds the page size", func(t *testing.T) {
		items, err := store.SearchPublicUsers(ctx, "a", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 {
			t.Fatalf("limit was ignored: %#v", items)
		}
	})

	t.Run("an empty query never reaches the table", func(t *testing.T) {
		items, err := store.SearchPublicUsers(ctx, "   ", 8)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("an empty query must not return the directory: %#v", items)
		}
	})
}

func newUserSearchPostgresHarness(t *testing.T) (context.Context, *PostgresStore) {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("profile_user_search_%d", time.Now().UnixNano())
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
		`CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active'
		)`,
		`INSERT INTO users (id, username, username_lower, display_name, status) VALUES
			(1, 'alice', 'alice', 'Alice', 'active'),
			(2, 'alice2', 'alice2', 'Alice Wang', 'active'),
			(3, 'ghost', 'ghost', '', 'active'),
			(4, '100%pure', '100%pure', '100%pure', 'active'),
			(5, 'li_wei', 'li_wei', '李伟', 'active'),
			(6, 'inactive_banned', 'inactive_banned', 'Inactive banned', 'banned'),
			(7, 'inactive_disabled', 'inactive_disabled', 'Inactive disabled', 'disabled')`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup user search schema: %v\n%s", err, statement)
		}
	}
	return ctx, NewPostgresStore(pool)
}
