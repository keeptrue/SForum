package identity

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// TestRoleTemplateGrantsMatchDatabase 是角色模板的「奇偶校验」闸门。
//
// 事实：SeedRoleTemplates / SeedMemberPermissions / SeedPermissions 只在测试与 CLI 里被读，
// 权限与角色授权的真值落在迁移 SQL 里。所以「seeds 改了、迁移没跟」在运行时零报错：
// 后台「应用模板」预填的权限比库里多（新建组凭空多权限）或比库里少（新组建完就缺权限），
// 都不会有人发现。这条测试把两边钉死：**严格相等**，不是子集。
//
// 出现意外多余授权时，正确做法是在新迁移里显式收敛并记账，而不是放宽这里的断言
// 或加例外名单——例外名单会让漂移重新变成静默问题。
func TestRoleTemplateGrantsMatchDatabase(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	grants := readRolePermissionGrants(t, ctx, conn)

	for _, template := range SeedRoleTemplates {
		assertSamePermissionSet(t, template.Key, grants[template.Key], template.PermissionKeys)
	}
	assertSamePermissionSet(t, RoleMember, grants[RoleMember], SeedMemberPermissions)

	// super_admin 是全能角色：库里的集合必须与权限目录逐键一致。
	all := make([]string, 0, len(SeedPermissions))
	for _, permission := range SeedPermissions {
		all = append(all, permission.Key)
	}
	assertSamePermissionSet(t, RoleSuperAdmin, grants[RoleSuperAdmin], all)
}

// readRolePermissionGrants 读出每个角色的权限集合。
func readRolePermissionGrants(t *testing.T, ctx context.Context, conn *pgx.Conn) map[string][]string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT roles.key, role_permissions.permission_key
		FROM role_permissions
		JOIN roles ON roles.id = role_permissions.role_id`)
	if err != nil {
		t.Fatalf("query role permissions: %v", err)
	}
	defer rows.Close()

	grants := map[string][]string{}
	for rows.Next() {
		var roleKey, permissionKey string
		if err := rows.Scan(&roleKey, &permissionKey); err != nil {
			t.Fatalf("scan role permission: %v", err)
		}
		grants[roleKey] = append(grants[roleKey], permissionKey)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate role permissions: %v", err)
	}
	return grants
}

// assertSamePermissionSet 双向比对，失败时把两侧差集都打出来（只报一边会让人猜）。
func assertSamePermissionSet(t *testing.T, roleKey string, database, seeded []string) {
	t.Helper()
	inDB := toSet(database)
	inSeeds := toSet(seeded)
	var onlyDB, onlySeeds []string
	for key := range inDB {
		if !inSeeds[key] {
			onlyDB = append(onlyDB, key)
		}
	}
	for key := range inSeeds {
		if !inDB[key] {
			onlySeeds = append(onlySeeds, key)
		}
	}
	sort.Strings(onlyDB)
	sort.Strings(onlySeeds)
	if len(onlyDB) > 0 || len(onlySeeds) > 0 {
		t.Fatalf("role %q permission set differs from seeds\n  only in database: %v\n  only in seeds:    %v\n"+
			"（用一条只加不改的新迁移收敛库侧；不要放宽断言或加例外名单）", roleKey, onlyDB, onlySeeds)
	}
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}
