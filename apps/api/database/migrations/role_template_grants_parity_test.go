package migrations

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const roleTemplateGrantsParityMigration = "202611140003_role_template_grants_parity.sql"

// TestRoleTemplateGrantsParityMigrationShape 固定「只加不改、Down 只拆自己那一把」的结构契约。
//
// 背景：内置角色模板的权威定义在 Identity/seeds.go，但权限与授权真值在迁移 SQL 里。
// 收敛 operator / tech_admin 的漂移必须用一条新迁移，且：
//   - Up 只 INSERT（改历史授权会让已上线站点的自定义调整被悄悄覆盖）；
//   - Down 只删本迁移唯一来源的 operator × entity_meta.manage，其余三条各有更早的属主。
func TestRoleTemplateGrantsParityMigrationShape(t *testing.T) {
	body, err := fs.ReadFile(Files(), roleTemplateGrantsParityMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("role template grants parity migration has no Down section")
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
		"CROSS JOIN (VALUES",
		"('operator', 'entity_meta.manage')",
		"('operator', 'trust.manage')",
		"('operator', 'attachment.upload_policy.manage')",
		"('tech_admin', 'ai.manage')",
		// 幂等：重复执行（全新库之外的补跑、运维手工重放）不得报唯一冲突。
		"ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("migration Up missing %q", clause)
		}
	}
	// 只加不改：不覆盖既有别名、不删既有授权。
	for _, forbidden := range []string{"UPDATE ", "DELETE FROM", "TRUNCATE", "DROP TABLE", "CASCADE"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration Up must only insert, found %q", forbidden)
		}
	}

	down := strings.Join(strings.Fields(stripSQLLineComments(downRaw)), " ")
	if !strings.Contains(down, "DELETE FROM role_permissions") {
		t.Fatal("Down must delete the operator grant")
	}
	if !strings.Contains(down, "'entity_meta.manage'") {
		t.Fatal("Down must target entity_meta.manage")
	}
	// 只拆本迁移自己的那一把：其余三条各有更早的属主迁移。
	for _, owned := range []string{"'trust.manage'", "'attachment.upload_policy.manage'", "'ai.manage'"} {
		if strings.Contains(down, owned) {
			t.Fatalf("Down must not touch %s (owned by an earlier migration)", owned)
		}
	}
	// 只删 operator 那一行：super_admin 的 entity_meta.manage 属于 202607120009。
	if !strings.Contains(down, "key = 'operator'") {
		t.Fatal("Down must scope to the operator role")
	}
	if strings.Contains(down, "DELETE FROM permissions") {
		t.Fatal("Down must not delete permission catalog rows")
	}
	for _, forbidden := range []string{"CASCADE", "TRUNCATE", "DROP TABLE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("Down contains %q", forbidden)
		}
	}
}

// TestRoleTemplateGrantsParityMigrationIsIdempotent 证明 up/down 可重复执行。
//
// 奇偶校验只验「seeds 与库一致」，不验回滚能不能重放。这条补缺口：
// up×2 → 每个授权仍是 1 行；down×2 → 只掉 operator × entity_meta.manage，其它三条不动；
// 再 up → 回到 1 行。全程在一个事务里跑完再回滚，不污染隔离库。
func TestRoleTemplateGrantsParityMigrationIsIdempotent(t *testing.T) {
	body, err := fs.ReadFile(Files(), roleTemplateGrantsParityMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("role template grants parity migration has no Down section")
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
	for _, grant := range []struct{ role, permission string }{
		{"operator", "entity_meta.manage"},
		{"operator", "trust.manage"},
		{"operator", "attachment.upload_policy.manage"},
		{"tech_admin", "ai.manage"},
	} {
		if got := countRoleGrant(t, ctx, tx, grant.role, grant.permission); got != 1 {
			t.Fatalf("%s × %s after up×2 = %d rows, want 1", grant.role, grant.permission, got)
		}
	}

	for round := 1; round <= 2; round++ {
		if err := execScript(ctx, tx, downScript); err != nil {
			t.Fatalf("down round %d: %v", round, err)
		}
	}
	if got := countRoleGrant(t, ctx, tx, "operator", "entity_meta.manage"); got != 0 {
		t.Fatalf("operator × entity_meta.manage after down×2 = %d rows, want 0", got)
	}
	// Down 只拆自己那一把：另外三条必须原样保留。
	for _, grant := range []struct{ role, permission string }{
		{"operator", "trust.manage"},
		{"operator", "attachment.upload_policy.manage"},
		{"tech_admin", "ai.manage"},
		{"super_admin", "entity_meta.manage"},
	} {
		if got := countRoleGrant(t, ctx, tx, grant.role, grant.permission); got != 1 {
			t.Fatalf("%s × %s after down×2 = %d rows, want 1 (Down over-deleted)", grant.role, grant.permission, got)
		}
	}

	if err := execScript(ctx, tx, upScript); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if got := countRoleGrant(t, ctx, tx, "operator", "entity_meta.manage"); got != 1 {
		t.Fatalf("operator × entity_meta.manage after re-up = %d rows, want 1", got)
	}
}

func countRoleGrant(t *testing.T, ctx context.Context, tx pgx.Tx, roleKey, permissionKey string) int {
	t.Helper()
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.key = $1 AND rp.permission_key = $2`, roleKey, permissionKey).Scan(&count)
	if err != nil {
		t.Fatalf("count %s × %s: %v", roleKey, permissionKey, err)
	}
	return count
}

// stripSQLLineComments 去掉整行 `--` 注释：结构断言只看可执行 SQL，
// 否则 Down 里解释「为什么不动 trust.manage」的注释会把它自己判成违规。
func stripSQLLineComments(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
