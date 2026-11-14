package extensionsruntime

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresExtensionDatabaseKernelCleanupWorksForNonSuperCreateRoleHost(t *testing.T) {
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
		t.Fatalf("connect kernel cleanup administrator: %v", err)
	}
	defer admin.Close(context.Background())
	var canCreateDatabase bool
	if err := admin.QueryRow(ctx, `SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&canCreateDatabase); err != nil {
		t.Fatal(err)
	}
	if !canCreateDatabase {
		t.Skip("kernel non-super integration test requires a database-creating test administrator")
	}

	nonce := time.Now().UnixNano()
	databaseName := fmt.Sprintf("sforum_p5_kernel_ns_%d", nonce)
	hostRoleName := fmt.Sprintf("sforum_p5_kernel_host_%d", nonce)
	extensionOwnerName := fmt.Sprintf("sforum_p5_kernel_ext_%d", nonce)
	leaseRoleName := fmt.Sprintf("sforum_p5_kernel_lease_%d", nonce)
	coreOwnerName, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	const hostPassword = "sforum_kernel_host_test"
	const leasePassword = "sforum_kernel_lease_test"
	database := pgx.Identifier{databaseName}.Sanitize()
	hostRole := pgx.Identifier{hostRoleName}.Sanitize()
	extensionOwner := pgx.Identifier{extensionOwnerName}.Sanitize()
	leaseRole := pgx.Identifier{leaseRoleName}.Sanitize()
	coreOwner := pgx.Identifier{coreOwnerName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupAdmin, connectErr := pgx.ConnectConfig(cleanupCtx, adminConfig)
		if connectErr != nil {
			t.Errorf("connect kernel non-super cleanup administrator: %v", connectErr)
			return
		}
		defer cleanupAdmin.Close(context.Background())
		if _, err := cleanupAdmin.Exec(cleanupCtx, `DROP DATABASE IF EXISTS `+database+` WITH (FORCE)`); err != nil {
			t.Errorf("drop isolated kernel database: %v", err)
			return
		}
		for _, role := range []string{leaseRole, extensionOwner, coreOwner, hostRole} {
			if _, err := cleanupAdmin.Exec(cleanupCtx, `DROP ROLE IF EXISTS `+role); err != nil {
				t.Errorf("drop isolated kernel role %s: %v", role, err)
			}
		}
	})
	if _, err := admin.Exec(ctx,
		`CREATE ROLE `+hostRole+` LOGIN INHERIT NOSUPERUSER NOCREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '`+hostPassword+`'`,
	); err != nil {
		t.Fatalf("create non-super kernel Host: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+database+` OWNER `+hostRole); err != nil {
		t.Fatalf("create isolated kernel database: %v", err)
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
		t.Fatalf("non-super kernel Host attributes = super:%t createrole:%t", superuser, createRole)
	}

	extensionSchemaName := fmt.Sprintf("sforum_p5_kernel_schema_%d", nonce)
	extensionSchema := pgx.Identifier{extensionSchemaName}.Sanitize()
	setup, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, setup); err != nil {
		t.Fatal(err)
	}
	setupQueries := []string{
		`CREATE ROLE ` + coreOwner + ` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`GRANT ` + coreOwner + ` TO ` + hostRole + ` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
		`REVOKE CREATE ON DATABASE ` + database + ` FROM ` + coreOwner,
		`ALTER SCHEMA public OWNER TO ` + coreOwner,
		`CREATE SCHEMA sforum_core_v1 AUTHORIZATION ` + coreOwner,
		`CREATE ROLE ` + extensionOwner + ` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`GRANT ` + extensionOwner + ` TO ` + hostRole + ` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
		`CREATE SCHEMA ` + extensionSchema + ` AUTHORIZATION ` + extensionOwner,
		`REVOKE ` + extensionOwner + ` FROM ` + hostRole + ` GRANTED BY CURRENT_USER`,
		`CREATE ROLE ` + leaseRole + ` LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '` + leasePassword + `'`,
		`GRANT ` + coreOwner + ` TO ` + leaseRole + ` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		`GRANT ` + extensionOwner + ` TO ` + leaseRole + ` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
	}
	for _, query := range setupQueries {
		if _, err := setup.Exec(ctx, query); err != nil {
			t.Fatalf("prepare non-super kernel authority %q: %v", query, err)
		}
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, extensionOwnerName, "before lease retirement",
	)

	leaseURL := *targetURL
	leaseURL.User = url.UserPassword(leaseRoleName, leasePassword)
	leaseConnection, err := pgx.Connect(ctx, leaseURL.String())
	if err != nil {
		t.Fatalf("connect non-super kernel lease: %v", err)
	}
	coreTableName := fmt.Sprintf("kernel_non_super_core_%d", nonce)
	ownTableName := fmt.Sprintf("kernel_non_super_own_%d", nonce)
	coreTable := pgx.Identifier{extensionDatabaseCoreSchema, coreTableName}.Sanitize()
	ownTable := pgx.Identifier{extensionSchemaName, ownTableName}.Sanitize()
	for _, query := range []string{
		`CREATE TABLE ` + coreTable + ` (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE ` + ownTable + ` (id BIGINT PRIMARY KEY)`,
	} {
		if _, err := leaseConnection.Exec(ctx, query); err != nil {
			leaseConnection.Close(context.Background())
			t.Fatalf("create non-super kernel object: %v", err)
		}
	}
	if err := leaseConnection.Close(ctx); err != nil {
		t.Fatal(err)
	}

	retirement, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer retirement.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, retirement); err != nil {
		t.Fatal(err)
	}
	if err := acquireExtensionDatabaseKernelOwnerAuthority(
		ctx, retirement, map[string]struct{}{leaseRoleName: {}},
	); err != nil {
		t.Fatalf("acquire non-super kernel retirement authority: %v", err)
	}
	if err := reassignExtensionDatabaseKernelCoreObjects(ctx, retirement, leaseRoleName); err != nil {
		t.Fatalf("transfer non-super kernel Core objects: %v", err)
	}
	// 生产回收路径用同一对 helper 临时获得「把对象交给扩展 owner」的 SET ROLE
	// 权限（PG 17.12 起 REASSIGN OWNED 要求，CVE-2026-120146），用完立刻归还。
	if err := acquireExtensionDatabaseObjectRecipientAuthority(ctx, retirement, extensionOwnerName); err != nil {
		t.Fatalf("acquire non-super kernel object recipient authority: %v", err)
	}
	for _, query := range []string{
		`REASSIGN OWNED BY ` + leaseRole + ` TO ` + extensionOwner,
		`DROP OWNED BY ` + leaseRole,
		`DROP ROLE ` + leaseRole,
	} {
		if _, err := retirement.Exec(ctx, query); err != nil {
			t.Fatalf("retire non-super kernel lease: %v", err)
		}
	}
	if err := releaseExtensionDatabaseObjectRecipientAuthority(ctx, retirement, extensionOwnerName); err != nil {
		t.Fatalf("release non-super kernel object recipient authority: %v", err)
	}
	if err := retirement.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, hostPool, "relation", extensionDatabaseCoreSchema, coreTableName, coreOwnerName,
	)
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, hostPool, "relation", extensionSchemaName, ownTableName, extensionOwnerName,
	)
	var roleExists bool
	if err := hostPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, leaseRoleName,
	).Scan(&roleExists); err != nil {
		t.Fatal(err)
	}
	if roleExists {
		t.Fatal("non-super kernel lease role survived exact retirement")
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, extensionOwnerName, "after lease retirement",
	)
}

// CREATEROLE 建 role 时 PostgreSQL 会留下一条 admin=true, inherit=false, set=false
// 的 creator 成员行，因此单查 USAGE 恒为 false，证明不了「没有残留授权」。
// USAGE（可继承）与 SET（可 SET ROLE）才是 CVE-2026-120146 收紧的那两个权限位，
// 也是宿主能拿扩展 owner 身份动手的全部前提。
func assertNonSuperHostCannotActAsExtensionOwner(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	hostRoleName string,
	extensionOwnerName string,
	stage string,
) {
	t.Helper()
	var usable, settable bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_has_role($1, $2, 'USAGE'), pg_has_role($1, $2, 'SET')
	`, hostRoleName, extensionOwnerName).Scan(&usable, &settable); err != nil {
		t.Fatal(err)
	}
	if usable || settable {
		t.Fatalf(
			"non-super Host could act as the extension owner %s (usage=%t set=%t)",
			stage, usable, settable,
		)
	}
}
