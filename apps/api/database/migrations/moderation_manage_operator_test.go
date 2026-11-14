package migrations

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const moderationManageOperatorMigration = "202611140004_moderation_manage_operator.sql"

// TestModerationManageOperatorMigrationShape 固定「只加不改、Down 只拆自己那一把」的结构契约。
//
// moderation.manage 的授予此前只有 super_admin（202607110010）。本迁移把运营岗补上，
// 因此 Down 只能摘 operator 那一行：按 permission_key 全删会连带撤掉 202607110010 的授予。
func TestModerationManageOperatorMigrationShape(t *testing.T) {
	body, err := fs.ReadFile(Files(), moderationManageOperatorMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("moderation.manage operator migration has no Down section")
	}
	upRaw, downRaw := parts[0], parts[1]
	if !strings.Contains(upRaw, "-- +sforum OnlineSafe") {
		t.Fatal("Up must declare -- +sforum OnlineSafe")
	}

	up := strings.Join(strings.Fields(stripSQLLineComments(upRaw)), " ")
	for _, clause := range []string{
		"SET LOCAL lock_timeout = '5s'",
		"SET LOCAL statement_timeout = '1min'",
		"INSERT INTO role_permissions (role_id, permission_key)",
		"SELECT roles.id, 'moderation.manage' FROM roles",
		"WHERE roles.key = 'operator'",
		// 幂等：重复执行不得报唯一冲突。
		"ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("migration Up missing %q", clause)
		}
	}
	// 只加不改：不动别名、不删既有授权、不动权限目录。
	for _, forbidden := range []string{"UPDATE ", "DELETE FROM", "TRUNCATE", "DROP TABLE", "CASCADE", "INSERT INTO permissions"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration Up must only grant, found %q", forbidden)
		}
	}
	// super_admin 的授予属于 202607110010，本迁移不重复声明。
	if strings.Contains(up, "super_admin") {
		t.Fatal("Up must not re-grant super_admin (owned by 202607110010)")
	}

	down := strings.Join(strings.Fields(stripSQLLineComments(downRaw)), " ")
	if !strings.Contains(down, "DELETE FROM role_permissions") {
		t.Fatal("Down must delete the operator grant")
	}
	if !strings.Contains(down, "'moderation.manage'") {
		t.Fatal("Down must target moderation.manage")
	}
	if !strings.Contains(down, "key = 'operator'") {
		t.Fatal("Down must scope to the operator role")
	}
	if strings.Contains(down, "super_admin") {
		t.Fatal("Down must not touch the super_admin grant")
	}
	if strings.Contains(down, "DELETE FROM permissions") {
		t.Fatal("Down must not delete the permission catalog row")
	}
	for _, forbidden := range []string{"CASCADE", "TRUNCATE", "DROP TABLE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("Down contains %q", forbidden)
		}
	}
}

// TestModerationManageOperatorMigrationIsIdempotent 证明 up/down 可重复执行。
//
// up×2 → operator 与 super_admin 各 1 行；down×2 → 只掉 operator 那一行，super_admin 保留；
// 再 up → 回到 1 行。全程在一个事务里跑完再回滚，不污染隔离库。
func TestModerationManageOperatorMigrationIsIdempotent(t *testing.T) {
	body, err := fs.ReadFile(Files(), moderationManageOperatorMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("moderation.manage operator migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for round := 1; round <= 2; round++ {
		if err := execScript(ctx, tx, upScript); err != nil {
			t.Fatalf("up round %d: %v", round, err)
		}
	}
	if got := countRoleGrant(t, ctx, tx, "operator", "moderation.manage"); got != 1 {
		t.Fatalf("operator × moderation.manage after up×2 = %d rows, want 1", got)
	}
	if got := countRoleGrant(t, ctx, tx, "super_admin", "moderation.manage"); got != 1 {
		t.Fatalf("super_admin × moderation.manage after up×2 = %d rows, want 1", got)
	}

	for round := 1; round <= 2; round++ {
		if err := execScript(ctx, tx, downScript); err != nil {
			t.Fatalf("down round %d: %v", round, err)
		}
	}
	if got := countRoleGrant(t, ctx, tx, "operator", "moderation.manage"); got != 0 {
		t.Fatalf("operator × moderation.manage after down×2 = %d rows, want 0", got)
	}
	// Down 只拆自己那一把：super_admin 的授予必须原样保留。
	if got := countRoleGrant(t, ctx, tx, "super_admin", "moderation.manage"); got != 1 {
		t.Fatalf("super_admin × moderation.manage after down×2 = %d rows, want 1 (Down over-deleted)", got)
	}

	if err := execScript(ctx, tx, upScript); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if got := countRoleGrant(t, ctx, tx, "operator", "moderation.manage"); got != 1 {
		t.Fatalf("operator × moderation.manage after re-up = %d rows, want 1", got)
	}
}
