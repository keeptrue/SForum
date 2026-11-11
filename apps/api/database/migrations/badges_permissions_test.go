package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const badgesPermissionsMigration = "202611100003_badges_permissions.sql"

// TestBadgesPermissionsMigrationSeedsCatalogAndRoleGrants 固定权限落库的结构契约。
//
// 这条迁移存在的理由本身就是要被固定下来的事实：seeds.go 只在测试与 CLI 里生效，
// 启动期不会 upsert 权限目录。少了这条迁移，代码里写着 badge.view 而库里没有，
// 角色挂不上、端点永远 403，且没有任何报错提示。
func TestBadgesPermissionsMigrationSeedsCatalogAndRoleGrants(t *testing.T) {
	body, err := fs.ReadFile(Files(), badgesPermissionsMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("badges permissions migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"INSERT INTO permissions (key, module, description) VALUES",
		"('badge.view', 'forum',",
		"('badge.manage', 'forum',",
		"ON CONFLICT (key) DO NOTHING",
		// 只读给 moderator + operator + super_admin；可写只给 operator + super_admin。
		"SELECT roles.id, 'badge.view' FROM roles WHERE roles.key IN ('moderator', 'operator', 'super_admin')",
		"SELECT roles.id, 'badge.manage' FROM roles WHERE roles.key IN ('operator', 'super_admin')",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("badges permissions migration missing %q", clause)
		}
	}
	// 版主不得拿到写权限：这条是「只读管理员打写端点必须 403」的种子侧保证。
	if strings.Contains(up, "SELECT roles.id, 'badge.manage' FROM roles WHERE roles.key IN ('moderator'") {
		t.Fatal("moderator must not receive badge.manage")
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"DELETE FROM role_permissions WHERE permission_key IN ('badge.view', 'badge.manage')",
		"DELETE FROM permissions WHERE key IN ('badge.view', 'badge.manage')",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("badges permissions Down missing %q", clause)
		}
	}
	// Down 顺序：先删角色授权（有 FK 指向 permissions），再删权限行。
	if strings.Index(down, "DELETE FROM role_permissions") > strings.Index(down, "DELETE FROM permissions") {
		t.Fatal("Down must delete role_permissions before permissions (FK)")
	}
	for _, forbidden := range []string{"CASCADE", "TRUNCATE", "DROP TABLE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("badges permissions Down contains %q", forbidden)
		}
	}
}
