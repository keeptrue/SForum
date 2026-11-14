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

// 生产 provisioning 由非超级用户宿主执行：CREATEROLE 建出扩展 owner/runtime 后，
// CREATE SCHEMA ... AUTHORIZATION owner（PG 16 起要求 SET ROLE 到 owner）与
// configureExtensionDatabaseOwnership（改 schema ACL 与 owner 的默认权限）都要以
// owner 身份执行，而宿主只有 CREATEROLE 留下的 admin=true / inherit=false /
// set=false creator 行。运行时租约那半边同理：ALTER DEFAULT PRIVILEGES FOR ROLE
// <lease> 要以租约 role 的身份执行。
//
// 本测试走真正的生产入口 ensureExtensionDatabaseResources，宿主全程不手工补任何
// 接收者 GRANT；只有「部署权限」由管理员代授（数据库 CREATE 与注册表读写），
// 真实部署里 API 用户是数据库属主，本来就具备这两项。
func TestPostgresExtensionDatabaseProvisioningWorksForNonSuperCreateRoleHost(t *testing.T) {
	baseURL := testdb.IsolatedURL(t)
	baseConfig, err := pgx.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig := baseConfig.Copy()
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
	extensionID := fmt.Sprintf("p6.provision.%d", nonce)
	hostRoleName := fmt.Sprintf("sforum_p6_provision_host_%d", nonce)
	leaseRoleName := fmt.Sprintf("sforum_p6_provision_lease_%d", nonce)
	const hostPassword = "sforum_provision_host_test"
	hostRole := pgx.Identifier{hostRoleName}.Sanitize()
	leaseRole := pgx.Identifier{leaseRoleName}.Sanitize()
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		cleanupAdmin, connectErr := pgx.ConnectConfig(cleanupCtx, adminConfig)
		if connectErr != nil {
			t.Errorf("connect provisioning cleanup administrator: %v", connectErr)
			return
		}
		defer cleanupAdmin.Close(context.Background())
		if _, err := cleanupAdmin.Exec(cleanupCtx,
			`DROP SCHEMA IF EXISTS `+pgx.Identifier{identifiers.Schema}.Sanitize()+` CASCADE`,
		); err != nil {
			t.Errorf("drop provisioned extension schema: %v", err)
		}
		if _, err := cleanupAdmin.Exec(cleanupCtx,
			`DELETE FROM extension_database_resources WHERE extension_id = $1`, extensionID,
		); err != nil {
			t.Errorf("delete provisioned extension resource row: %v", err)
		}
		for _, role := range []string{leaseRoleName, identifiers.RuntimeRole, identifiers.OwnerRole, hostRoleName} {
			var exists bool
			if err := cleanupAdmin.QueryRow(cleanupCtx,
				`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role,
			).Scan(&exists); err != nil {
				t.Errorf("inspect provisioned role %s: %v", role, err)
				continue
			}
			if exists {
				if _, err := cleanupAdmin.Exec(cleanupCtx,
					`DROP OWNED BY `+pgx.Identifier{role}.Sanitize(),
				); err != nil {
					t.Errorf("drop provisioned role ownership %s: %v", role, err)
				}
			}
			if _, err := cleanupAdmin.Exec(cleanupCtx,
				`DROP ROLE IF EXISTS `+pgx.Identifier{role}.Sanitize(),
			); err != nil {
				t.Errorf("drop provisioned role %s: %v", role, err)
			}
		}
	})
	if _, err := admin.Exec(ctx,
		`CREATE ROLE `+hostRole+` LOGIN INHERIT NOSUPERUSER NOCREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '`+hostPassword+`'`,
	); err != nil {
		t.Fatalf("create non-super provisioning Host: %v", err)
	}
	// 部署权限：真实部署里 API 用户是数据库属主，天然拥有这两项。
	for _, query := range []string{
		`GRANT CREATE ON DATABASE ` + pgx.Identifier{baseConfig.Database}.Sanitize() + ` TO ` + hostRole,
		// 共享测试库的 public ACL 会被同包其它测试的物理态操作改动（实测整包跑时
		// has_schema_privilege('public','USAGE') 为 false，单跑为 true），这里显式
		// 补上部署侧权限，让本测试与执行顺序无关；被测的接收者权限一条都不补。
		`GRANT USAGE ON SCHEMA public TO ` + hostRole,
		// FOR UPDATE 读回资源行，因此除读写外还需要 UPDATE。
		`GRANT SELECT, INSERT, UPDATE ON extension_database_resources TO ` + hostRole,
	} {
		if _, err := admin.Exec(ctx, query); err != nil {
			t.Fatalf("grant deployment privilege %q: %v", query, err)
		}
	}
	targetURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
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

	provisioning, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer provisioning.Rollback(ctx)
	if _, err := ensureExtensionDatabaseResources(ctx, provisioning, extensionID, identifiers); err != nil {
		t.Fatalf("provision extension database resources: %v", err)
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
	`, identifiers.Schema).Scan(&schemaOwner); err != nil {
		t.Fatalf("inspect provisioned extension database schema: %v", err)
	}
	if schemaOwner != identifiers.OwnerRole {
		t.Fatalf("provisioned extension database schema owner = %q, want %q", schemaOwner, identifiers.OwnerRole)
	}
	var runtimeInheritsOwner bool
	if err := hostPool.QueryRow(ctx,
		`SELECT pg_has_role($1, $2, 'USAGE')`, identifiers.RuntimeRole, identifiers.OwnerRole,
	).Scan(&runtimeInheritsOwner); err != nil {
		t.Fatal(err)
	}
	if !runtimeInheritsOwner {
		t.Fatal("provisioned runtime role does not inherit the extension owner")
	}
	var resourceRows int
	if err := hostPool.QueryRow(ctx,
		`SELECT count(*) FROM extension_database_resources WHERE extension_id = $1`, extensionID,
	).Scan(&resourceRows); err != nil {
		t.Fatal(err)
	}
	if resourceRows != 1 {
		t.Fatalf("provisioned extension resource rows = %d, want 1", resourceRows)
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, identifiers.OwnerRole, "after provisioning",
	)

	// 运行时租约那半边：ALTER DEFAULT PRIVILEGES FOR ROLE <lease> 同样需要租约 role 的
	// 特权。按生产顺序建 role → 授予 owner → 配置默认权限，宿主不补任何接收者权限。
	if _, err := hostPool.Exec(ctx,
		`CREATE ROLE `+leaseRole+` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
	); err != nil {
		t.Fatalf("create runtime lease role: %v", err)
	}
	if _, err := hostPool.Exec(ctx,
		`GRANT `+pgx.Identifier{identifiers.OwnerRole}.Sanitize()+` TO `+leaseRole+` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
	); err != nil {
		t.Fatalf("grant extension owner to runtime lease role: %v", err)
	}
	leaseSetup, err := hostPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer leaseSetup.Rollback(ctx)
	if err := configureExtensionDatabaseRuntimeLeaseDefaultPrivileges(
		ctx, leaseSetup, leaseRoleName, identifiers.Schema, identifiers.OwnerRole, true, false,
	); err != nil {
		t.Fatalf("configure runtime lease default privileges: %v", err)
	}
	if err := leaseSetup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var defaultACLs int
	if err := hostPool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_default_acl AS defaults
		JOIN pg_roles AS roles ON roles.oid = defaults.defaclrole
		JOIN pg_namespace AS namespaces ON namespaces.oid = defaults.defaclnamespace
		WHERE roles.rolname = $1 AND namespaces.nspname = $2
	`, leaseRoleName, identifiers.Schema).Scan(&defaultACLs); err != nil {
		t.Fatal(err)
	}
	if defaultACLs != 4 {
		t.Fatalf("runtime lease default ACL entries = %d, want 4", defaultACLs)
	}
	assertNonSuperHostCannotActAsExtensionOwner(
		t, ctx, hostPool, hostRoleName, leaseRoleName, "after runtime lease default privileges",
	)
}
