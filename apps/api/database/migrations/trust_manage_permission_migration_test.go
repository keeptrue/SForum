package migrations

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const trustManageMigration = "202611100004_trust_manage_permission.sql"

// TestTrustManagePermissionMigrationShape 固定 trust.manage 落库的结构契约。
//
// 背景：Identity/seeds.go 的 SeedPermissions 只在测试与 CLI 里被读取，启动期没有
// 任何权限目录 upsert。所以 seeds 里写了 trust.manage、库里没有，端点就永远 403，
// 且运行时零报错——只有 TestEverySeededPermissionReachesTheDatabase 会红。
func TestTrustManagePermissionMigrationShape(t *testing.T) {
	body, err := fs.ReadFile(Files(), trustManageMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("trust.manage migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout = '5s'",
		"INSERT INTO permissions (key, module, description) VALUES",
		"('trust.manage', 'identity',",
		// 幂等：重复执行（fresh install 之外的重跑、运维手工补跑）不得报唯一冲突。
		"ON CONFLICT (key) DO NOTHING",
		// 与 badge.manage 同一批岗位：站点策略归运营，超管全能。
		"SELECT roles.id, 'trust.manage' FROM roles WHERE roles.key IN ('operator', 'super_admin')",
		"ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("trust.manage migration missing %q", clause)
		}
	}
	// 版主只做内容审核，拿站点策略写权限属于越界（D9/D3 的岗位边界）。
	if strings.Contains(up, "roles.key IN ('moderator'") {
		t.Fatal("moderator must not receive trust.manage")
	}
	// 刻意不建 trust.view：后台读页复用 admin.access，第二个键就是死权限。
	if strings.Contains(up, "'trust.view'") {
		t.Fatal("trust.view is a dead permission; admin read pages reuse admin.access (D9)")
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"DELETE FROM role_permissions WHERE permission_key = 'trust.manage'",
		"DELETE FROM permissions WHERE key = 'trust.manage'",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("trust.manage Down missing %q", clause)
		}
	}
	// FK 顺序：role_permissions.permission_key 指向 permissions.key，先删授权再删权限。
	if strings.Index(down, "DELETE FROM role_permissions") > strings.Index(down, "DELETE FROM permissions") {
		t.Fatal("Down must delete role_permissions before permissions (FK)")
	}
	for _, forbidden := range []string{"CASCADE", "TRUNCATE", "DROP TABLE"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("trust.manage Down contains %q", forbidden)
		}
	}
}

// TestTrustManagePermissionMigrationIsIdempotent 证明「同一迁移连跑两次不报错」。
//
// 奇偶校验测试只验「seeds 有、迁移也有」，它不验 down 能不能回滚、也不验重复执行幂等。
// 这条补的正是那个缺口：up×2 → 1 行；down×2 → 0 行；再 up → 又 1 行。
// 全程在一个事务里跑完再回滚，不污染隔离库（其它包的测试还要用同一份权限目录）。
func TestTrustManagePermissionMigrationIsIdempotent(t *testing.T) {
	body, err := fs.ReadFile(Files(), trustManageMigration)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("trust.manage migration has no Down section")
	}
	upScript, downScript := parts[0], parts[1]

	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// up 连跑两次：第二次必须被 ON CONFLICT 吃掉，而不是报 duplicate key。
	for round := 1; round <= 2; round++ {
		if err := execScript(ctx, tx, upScript); err != nil {
			t.Fatalf("up round %d: %v", round, err)
		}
	}
	if got := countTrustManagePermission(t, ctx, tx); got != 1 {
		t.Fatalf("permissions rows for trust.manage after up×2 = %d, want 1", got)
	}
	if got := countTrustManageGrants(t, ctx, tx); got != 2 {
		t.Fatalf("role_permissions rows for trust.manage = %d, want 2 (operator + super_admin)", got)
	}
	// 版主不得拿到：这是「只读/审核岗位不能改站点策略」的种子侧保证。
	var moderator int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		WHERE rp.permission_key = 'trust.manage' AND r.key = 'moderator'`).Scan(&moderator); err != nil {
		t.Fatalf("count moderator grants: %v", err)
	}
	if moderator != 0 {
		t.Fatalf("moderator must not hold trust.manage, got %d grant(s)", moderator)
	}

	// down 连跑两次：第二次删 0 行也必须不报错（运维手工重跑 / 回滚脚本重入）。
	for round := 1; round <= 2; round++ {
		if err := execScript(ctx, tx, downScript); err != nil {
			t.Fatalf("down round %d: %v", round, err)
		}
	}
	if got := countTrustManagePermission(t, ctx, tx); got != 0 {
		t.Fatalf("permissions rows for trust.manage after down×2 = %d, want 0", got)
	}
	if got := countTrustManageGrants(t, ctx, tx); got != 0 {
		t.Fatalf("role_permissions rows for trust.manage after down×2 = %d, want 0", got)
	}

	// down 之后还能再 up：回滚不是单向闸门。
	if err := execScript(ctx, tx, upScript); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if got := countTrustManagePermission(t, ctx, tx); got != 1 {
		t.Fatalf("permissions rows for trust.manage after down→up = %d, want 1", got)
	}
}

// execScript 逐条执行迁移脚本。
//
// 不整段 Exec：pgx 默认走扩展协议，一条 Exec 里塞多条语句会被拒；
// 而 stdlib 的隐式协议行为又随驱动版本变化。逐条执行顺带让失败信息指向具体语句。
func execScript(ctx context.Context, tx pgx.Tx, script string) error {
	for _, statement := range splitStatements(script) {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements 按分号切分；跳过只含注释/空白的片段。
func splitStatements(script string) []string {
	var out []string
	for _, chunk := range strings.Split(script, ";") {
		if statementIsEmpty(chunk) {
			continue
		}
		out = append(out, chunk)
	}
	return out
}

func statementIsEmpty(chunk string) bool {
	for _, line := range strings.Split(chunk, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		return false
	}
	return true
}

func countTrustManagePermission(t *testing.T, ctx context.Context, tx pgx.Tx) int {
	t.Helper()
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key = 'trust.manage'`).Scan(&count); err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	return count
}

func countTrustManageGrants(t *testing.T, ctx context.Context, tx pgx.Tx) int {
	t.Helper()
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_permissions WHERE permission_key = 'trust.manage'`).Scan(&count); err != nil {
		t.Fatalf("count role_permissions: %v", err)
	}
	return count
}
