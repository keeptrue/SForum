package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// TestAPITokenVersionMigrationRoundTrip 真实跑一遍 up 与 down（P2-2）。
//
// 固定三件事：
//  1. up 之后 api_tokens 多了 token_version / revoke_reason，且默认值可用；
//  2. 回填把存量 PAT 对齐到 users.current_token_version —— 升级瞬间不能把所有
//     机器客户端一起踢下线；
//  3. down 之后两列消失，且不影响 users 表。
func TestAPITokenVersionMigrationRoundTrip(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("api_token_version_migration_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		_ = admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	defer db.Close()

	// 迁移前的形状：users + api_tokens（没有新列）。
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
		  id BIGSERIAL PRIMARY KEY,
		  current_token_version BIGINT NOT NULL DEFAULT 0
		);
		CREATE TABLE api_tokens (
		  id BIGSERIAL PRIMARY KEY,
		  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		  public_id TEXT NOT NULL UNIQUE,
		  token_hash TEXT NOT NULL,
		  name TEXT NOT NULL,
		  scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
		  revoked_at TIMESTAMPTZ,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		INSERT INTO users (current_token_version) VALUES (7);
		INSERT INTO api_tokens (user_id, public_id, token_hash, name) VALUES (1, 'pub-1', 'hash-1', 'legacy');
	`); err != nil {
		t.Fatalf("seed pre-migration schema: %v", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202611160001, true); err != nil {
		t.Fatalf("apply migration up: %v", err)
	}

	// up：列存在、默认值可用、回填对齐到 users 的当前版本。
	var tokenVersion int64
	var revokeReason string
	if err := db.QueryRowContext(ctx, `
		SELECT token_version, revoke_reason FROM api_tokens WHERE public_id = 'pub-1'
	`).Scan(&tokenVersion, &revokeReason); err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if tokenVersion != 7 {
		t.Fatalf("回填后 token_version=%d want 7（存量 PAT 必须与当前版本一致）", tokenVersion)
	}
	if revokeReason != "" {
		t.Fatalf("revoke_reason=%q want 空字符串默认值", revokeReason)
	}

	// down：两列消失，users 不受影响。
	if _, err := provider.DownTo(ctx, 202611160000); err != nil {
		t.Fatalf("apply migration down: %v", err)
	}
	if columnExists(t, ctx, db, "api_tokens", "token_version") {
		t.Fatal("down 之后 api_tokens.token_version 仍然存在")
	}
	if columnExists(t, ctx, db, "api_tokens", "revoke_reason") {
		t.Fatal("down 之后 api_tokens.revoke_reason 仍然存在")
	}
	if !columnExists(t, ctx, db, "users", "current_token_version") {
		t.Fatal("down 不应影响 users.current_token_version")
	}
}

func columnExists(t *testing.T, ctx context.Context, db *sql.DB, table, column string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Fatalf("check column %s.%s: %v", table, column, err)
	}
	return exists
}
