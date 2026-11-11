package identity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// TestUserAccountsBatchReturnsOnlyExistingRows 钉住批量读 users 的唯一一条 SQL：
// 重复/非正/缺失 id 不产出条目；created_at 与 kind 原样带回；空集不报错。
//
// 这条 SQL 是作者信任等级的账号事实来源，fake 覆盖不到 ANY($1) 的真实类型行为。
func TestUserAccountsBatchReturnsOnlyExistingRows(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("user_accounts_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			kind TEXT NOT NULL DEFAULT 'human',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	humanCreated := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var humanID, botID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (kind, created_at) VALUES ('human', $1) RETURNING id`, humanCreated).Scan(&humanID); err != nil {
		t.Fatalf("insert human: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (kind, created_at) VALUES ('bot', now()) RETURNING id`).Scan(&botID); err != nil {
		t.Fatalf("insert bot: %v", err)
	}

	store := &PostgresStore{pool: pool}
	accounts, err := store.UserAccounts(ctx, []int64{humanID, botID, humanID, 0, -1, 9_999_999})
	if err != nil {
		t.Fatalf("UserAccounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("返回 %d 条，期望 2（重复/非法/缺失 id 都不产出）", len(accounts))
	}
	if got := accounts[humanID]; got.Kind != UserKindHuman || !got.CreatedAt.Equal(humanCreated) {
		t.Fatalf("human account = %+v，期望 kind=human createdAt=%v", got, humanCreated)
	}
	if got := accounts[botID]; got.Kind != UserKindBot {
		t.Fatalf("bot account = %+v，期望 kind=bot", got)
	}

	// 读取不得改动原行。
	var kind string
	if err := pool.QueryRow(ctx, `SELECT kind FROM users WHERE id = $1`, humanID).Scan(&kind); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if kind != "human" {
		t.Fatalf("读取后 kind = %q，期望 human", kind)
	}

	for name, ids := range map[string][]int64{"empty": nil, "invalid": {0, -1}} {
		got, err := store.UserAccounts(ctx, ids)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s 输入 => (%d rows, %v)，期望 (0, nil)", name, len(got), err)
		}
	}
}
