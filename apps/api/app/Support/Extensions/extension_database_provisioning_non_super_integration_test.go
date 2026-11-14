package extensionsruntime

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 生产 provisioning 由非超级用户宿主执行：先用 CREATEROLE 建出扩展 owner，
// 再 CREATE SCHEMA ... AUTHORIZATION owner。PG 16 起后者要求调用者能 SET ROLE 到
// owner（17.12 起 REASSIGN OWNED 同规则，CVE-2026-120146），而 CREATEROLE 建 role
// 只留下 admin=true, inherit=false, set=false 的 creator 行。
//
// 这个测试刻意不手工补任何 GRANT：宿主全程没有 SET 权限，只能靠生产 helper 在事务内
// 临时取得再归还。把 ensureExtensionDatabaseSchema 里的 acquire 注释掉，它必须变红。
func TestPostgresExtensionDatabaseSchemaProvisioningWorksForNonSuperCreateRoleHost(t *testing.T) {
	baseURL := testdb.IsolatedURL(t)
	baseConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig := baseConfig.Copy()
	adminConfig.Database = "postgres"
	delete(adminConfig.RuntimeParams, "role")
	delete(adminConfig.RuntimeParams, "search_path")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect provisioning administrator: %v", err)
	}
	defer admin.Close(context.Background())
	var canCreateDatabase bool
	if err := admin.QueryRow(ctx, `SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&canCreateDatabase); err != nil {
		t.Fatal(err)
	}
	if !canCreateDatabase {
		t.Skip("provisioning non-super integration test requires a database-creating test administrator")
	}

	nonce := time.Now().UnixNano()
	databaseName := fmt.Sprintf("sforum_p6_provision_%d", nonce)
	hostRoleName := fmt.Sprintf("sforum_p6_provision_host_%d", nonce)
	ownerRoleName := fmt.Sprintf("sforum_p6_provision_owner_%d", nonce)
	schemaName := fmt.Sprintf("sforum_p6_provision_schema_%d", nonce)
	const hostPassword = "sforum_provision_host_test"
	database := pgx.Identifier{databaseName}.Sanitize()
	hostRole := pgx.Identifier{hostRoleName}.Sanitize()
	ownerRole := pgx.Identifier{ownerRoleName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupAdmin, connectErr := pgx.ConnectConfig(cleanupCtx, adminConfig)
		if connectErr != nil {
			t.Errorf("connect provisioning cleanup administrator: %v", connectErr)
			return
		}
		defer cleanupAdmin.Close(context.Background())
		if _, err := cleanupAdmin.Exec(cleanupCtx, `DROP DATABASE IF EXISTS `+database+` WITH (FORCE)`); err != nil {
			t.Errorf("drop isolated provisioning database: %v", err)
			return
		}
		for _, role := range []string{ownerRole, hostRole} {
			if _, err := cleanupAdmin.Exec(cleanupCtx, `DROP ROLE IF EXISTS `+role); err != nil {
				t.Errorf("drop isolated provisioning role %s: %v", role, err)
			}
		}
	})
	if _, err := admin.Exec(ctx,
		`CREATE ROLE `+hostRole+` LOGIN INHERIT NOSUPERUSER NOCREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '`+hostPassword+`'`,
	); err != nil {
		t.Fatalf("create non-super provisioning Host: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+database+` OWNER `+hostRole); err != nil {
		t.Fatalf("create isolated provisioning database: %v", err)
	}
	targetURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	targetURL.Path = "/" + databaseName
	targetURL.User = url.UserPassword(hostRoleName, hostPassword)
	targetQuery := targetURL.Query()
	targetQuery.Del("role")
	targetQuery.Del("search_path")
	targetURL.RawQuery = targetQuery.Encode()
	hostPool, err := pgxpool.New(ctx, targetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer hostPool.Close()
	var superuser, createRole bool
	if err := hostPool.QueryRow(ctx, `SELECT rolsuper, rolcreaterole FROM pg_roles WHERE rolname = current_user`).Scan(
		&superuser, &createRole,
	); err != nil {
		t.Fatal(err)
	}
	if superuser || !createRole {
		t.Fatalf("non-super provisioning Host attributes = super:%t createrole:%t", superuser, createRole)
	}

	// 角色先独立提交：CREATE ROLE 在未提交事务里对其他连接不可见，
	// 而下面的权限断言必须从另一条连接观察宿主的真实授权状态。
	roleSetup, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roleSetup.Rollback(ctx)
	if err := ensureExtensionDatabaseRole(ctx, roleSetup, ownerRoleName, false, ""); err != nil {
		t.Fatalf("provision extension database owner role: %v", err)
	}
	if err := roleSetup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, ownerRoleName, "before schema provisioning",
	)

	provisioning, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer provisioning.Rollback(ctx)
	if err := ensureExtensionDatabaseSchema(ctx, provisioning, schemaName, ownerRoleName); err != nil {
		t.Fatalf("provision extension database schema: %v", err)
	}
	if err := provisioning.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var schemaOwner string
	if err := hostPool.QueryRow(ctx, `
		SELECT roles.rolname
		FROM pg_namespace
		JOIN pg_roles AS roles ON roles.oid = pg_namespace.nspowner
		WHERE pg_namespace.nspname = $1
	`, schemaName).Scan(&schemaOwner); err != nil {
		t.Fatalf("inspect provisioned extension database schema: %v", err)
	}
	if schemaOwner != ownerRoleName {
		t.Fatalf("provisioned extension database schema owner = %q, want %q", schemaOwner, ownerRoleName)
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, ownerRoleName, "after schema provisioning",
	)
}
