// Package trustgate 为「信任等级基线门」（Z3-b″）的集成测试提供一次性库与用户构造。
//
// 它只建装载器真正读到的六张表，不跑全量迁移：`LoadEffectiveActorTx` 与
// `loadCurrentUserAccess` 的权限解析只碰 users / roles / user_roles / permissions /
// role_permissions / user_permission_overrides。
//
// 为什么单独成包：forum、identity、profile 三个包的验收门都要同一套库表，
// 三份 DDL 就是本仓反复出现的「两份」失败（换一处漏一处）。这里只此一份。
package trustgate

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// Fixture 是一次性 schema 上的最小身份库。
type Fixture struct {
	Pool *pgxpool.Pool

	ctx        context.Context
	admin      *pgxpool.Pool
	schema     string
	roleSeeded bool
}

// New 在隔离数据库里建一个独立 schema，并建最小身份表。
func New(t testing.TB) *Fixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("trustgate: connect admin pool: %v", err)
	}
	schema := fmt.Sprintf("trust_gate_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatalf("trustgate: create schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("trustgate: parse config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("trustgate: connect schema pool: %v", err)
	}

	fixture := &Fixture{Pool: pool, ctx: ctx, admin: admin, schema: schema}
	for _, ddl := range []string{
		// users 的列覆盖两条 loader 读到的全部字段：LoadEffectiveActorTx 读
		// status/kind/created_at，LoadActor→GetCurrentUser 还读 username 等展示列。
		`CREATE TABLE users (
			id bigint PRIMARY KEY,
			username text NOT NULL DEFAULT 'member',
			display_name text NOT NULL DEFAULT 'Member',
			email text NOT NULL DEFAULT 'member@example.test',
			locale text NOT NULL DEFAULT 'zh-CN',
			status text NOT NULL DEFAULT 'active',
			kind text NOT NULL DEFAULT 'human',
			is_initial_super_admin boolean NOT NULL DEFAULT FALSE,
			current_token_version bigint NOT NULL DEFAULT 1,
			email_verified_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE TABLE user_appearance_preferences (user_id bigint PRIMARY KEY, theme text, light_background text)`,
		`CREATE TABLE user_profiles (user_id bigint PRIMARY KEY, avatar_attachment_id bigint)`,
		`CREATE TABLE attachments (id bigint PRIMARY KEY, public_id text, owner_user_id bigint, content_type text, status text)`,
		`CREATE TABLE roles (id bigint PRIMARY KEY, key text NOT NULL, is_enabled boolean NOT NULL DEFAULT TRUE)`,
		`CREATE TABLE user_roles (user_id bigint NOT NULL, role_id bigint NOT NULL)`,
		`CREATE TABLE permissions (key text PRIMARY KEY)`,
		`CREATE TABLE role_permissions (role_id bigint NOT NULL, permission_key text NOT NULL)`,
		`CREATE TABLE user_permission_overrides (user_id bigint NOT NULL, permission_key text NOT NULL, effect text NOT NULL, updated_by_user_id bigint)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			fixture.Close()
			t.Fatalf("trustgate: ddl: %v", err)
		}
	}
	t.Cleanup(fixture.Close)
	return fixture
}

// Close 删除 schema 并关闭连接池。
func (f *Fixture) Close() {
	if f == nil {
		return
	}
	if f.Pool != nil {
		f.Pool.Close()
	}
	if f.admin != nil {
		if f.schema != "" {
			_, _ = f.admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE")
		}
		f.admin.Close()
	}
}

// InsertMemberUser 建一个「默认安装的普通会员」：human/bot 二选一、指定注册时间，
// 并授予 member 角色（权限键取 identity.SeedMemberPermissions，与迁移落库的行一致）。
func (f *Fixture) InsertMemberUser(t testing.TB, id int64, kind string, createdAt time.Time) {
	t.Helper()
	f.seedMemberRole(t)
	if _, err := f.Pool.Exec(f.ctx,
		`INSERT INTO users (id, status, kind, created_at) VALUES ($1, 'active', $2, $3)`, id, kind, createdAt); err != nil {
		t.Fatalf("trustgate: insert user: %v", err)
	}
	if _, err := f.Pool.Exec(f.ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, 1)`, id); err != nil {
		t.Fatalf("trustgate: insert user role: %v", err)
	}
}

// seedMemberRole 幂等：同一个 fixture 里多建几个用户只落一次角色行。
func (f *Fixture) seedMemberRole(t testing.TB) {
	t.Helper()
	if f.roleSeeded {
		return
	}
	if _, err := f.Pool.Exec(f.ctx, `INSERT INTO roles (id, key, is_enabled) VALUES (1, 'member', TRUE)`); err != nil {
		t.Fatalf("trustgate: insert role: %v", err)
	}
	for _, key := range identity.SeedMemberPermissions {
		if _, err := f.Pool.Exec(f.ctx,
			`INSERT INTO permissions (key) VALUES ($1) ON CONFLICT DO NOTHING`, key); err != nil {
			t.Fatalf("trustgate: insert permission %q: %v", key, err)
		}
		if _, err := f.Pool.Exec(f.ctx,
			`INSERT INTO role_permissions (role_id, permission_key) VALUES (1, $1)`, key); err != nil {
			t.Fatalf("trustgate: insert role permission %q: %v", key, err)
		}
	}
	f.roleSeeded = true
}

// AllowOverride 给用户加一条 effect='allow' 的个人权限覆盖。
func (f *Fixture) AllowOverride(t testing.TB, id int64, key string) {
	t.Helper()
	if _, err := f.Pool.Exec(f.ctx,
		`INSERT INTO user_permission_overrides (user_id, permission_key, effect) VALUES ($1, $2, 'allow')`, id, key); err != nil {
		t.Fatalf("trustgate: insert allow override: %v", err)
	}
}

// BeginTx 在 fixture 的池上开一个事务（事务路径装载用）。
func (f *Fixture) BeginTx(t testing.TB) pgx.Tx {
	t.Helper()
	tx, err := f.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatalf("trustgate: begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}
