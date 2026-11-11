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

// TestListUserPermissionsReturnsDenyOnlyKeys 是 D28 的 SQL 层证据。
//
// 构造：一条**只有 deny override、没有任何角色授予**的键。这类键不在候选集里，
// 如果 ListUserPermissions 只对候选集求 effective，它根本不会出现在结果中，
// applyTrustLevel 的 denied 集合就看不到它 —— 信任等级挣得该键时 deny 被绕过。
//
// 变异要求：删掉 identity.sql 的 `UNION SELECT denied_permission_keys.key, FALSE`，
// 这条测试必须红。
func TestListUserPermissionsReturnsDenyOnlyKeys(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("trust_levels_deny_only_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		admin.Close()
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// 最小 schema：只建这条查询读到的五张表。
	for _, ddl := range []string{
		`CREATE TABLE users (id bigint PRIMARY KEY)`,
		`CREATE TABLE roles (id bigint PRIMARY KEY, key text NOT NULL, is_enabled boolean NOT NULL DEFAULT TRUE)`,
		`CREATE TABLE user_roles (user_id bigint NOT NULL, role_id bigint NOT NULL)`,
		`CREATE TABLE permissions (key text PRIMARY KEY)`,
		`CREATE TABLE role_permissions (role_id bigint NOT NULL, permission_key text NOT NULL)`,
		`CREATE TABLE user_permission_overrides (user_id bigint NOT NULL, permission_key text NOT NULL, effect text NOT NULL)`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	// 只有 deny，没有任何角色授予：这就是「deny-only 键」。
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_permission_overrides (user_id, permission_key, effect) VALUES (7, 'topic.create', 'deny')`,
	); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	decisions, err := store.queries.ListUserPermissions(ctx, 7)
	if err != nil {
		t.Fatalf("ListUserPermissions: %v", err)
	}
	found := false
	for _, decision := range decisions {
		if decision.Key != "topic.create" {
			continue
		}
		found = true
		if decision.Effective {
			t.Fatal("deny-only 键被标成 effective=true")
		}
	}
	if !found {
		t.Fatal("deny-only 键没有出现在 ListUserPermissions 结果里（D28 的 UNION 被删了？）")
	}
	if !deniedPermissionKeys(decisions)["topic.create"] {
		t.Fatal("deny-only 键没有进 denied 集合")
	}

	// 对照组（T10）：同一用户另一条只 allow 的覆盖必须 effective=true。
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_permission_overrides (user_id, permission_key, effect) VALUES (7, 'post.create', 'allow')`,
	); err != nil {
		t.Fatal(err)
	}
	decisions, err = store.queries.ListUserPermissions(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	allowEffective := false
	for _, decision := range decisions {
		if decision.Key == "post.create" && decision.Effective {
			allowEffective = true
		}
	}
	if !allowEffective {
		t.Fatal("allow 覆盖没有生效：查询形状坏了")
	}
}
