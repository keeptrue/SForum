package identity

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

// GetCurrentUser 的「行不存在」必须是一条可判别的身份结论（ErrUserNotFound），
// 而不是驱动层的 pgx.ErrNoRows：上层据此答 401。若它退回原始驱动错误，一个已删除
// 账号的旧会话会被归到「未识别」→ 503，于是「账号没了」被答成「服务暂时不可用」。
//
// 这里只建读取路径需要的最小表结构（缺行时只会走到 SELECT，不会进
// loadCurrentUserAccess），不复制整份迁移，避免与 schema 演进重复维护。
func TestGetCurrentUserMapsMissingRowToNotFound(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	schema := fmt.Sprintf("identity_current_user_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")

	for _, ddl := range []string{
		`CREATE TABLE ` + quoted + `.users (
			id BIGINT PRIMARY KEY,
			username TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			locale TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			is_initial_super_admin BOOLEAN NOT NULL DEFAULT FALSE,
			current_token_version BIGINT NOT NULL DEFAULT 1,
			email_verified_at TIMESTAMPTZ
		)`,
		`CREATE TABLE ` + quoted + `.user_appearance_preferences (
			user_id BIGINT PRIMARY KEY,
			theme TEXT,
			light_background TEXT
		)`,
		`CREATE TABLE ` + quoted + `.user_profiles (
			user_id BIGINT PRIMARY KEY,
			avatar_attachment_id BIGINT
		)`,
		`CREATE TABLE ` + quoted + `.attachments (
			id BIGINT PRIMARY KEY,
			public_id TEXT NOT NULL DEFAULT '',
			owner_user_id BIGINT,
			content_type TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT ''
		)`,
	} {
		if _, err := admin.Exec(ctx, ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	_, err = NewPostgresStore(pool).GetCurrentUser(ctx, 424242)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("a missing user row must be ErrUserNotFound, got %v", err)
	}
	if errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a missing user row must not be reported as a dependency failure: %v", err)
	}
}
