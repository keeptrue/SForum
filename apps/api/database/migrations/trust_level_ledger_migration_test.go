package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

// 本文件钉住 M4b 的账本迁移：表 + 视图 + owner 对齐 + 可逆。
//
// 三条最贵的约束各有一条断言：
//  1. **只做变更检测**：表注释里必须写明「不是授权来源」，否则后人会拿它当权威；
//  2. **视图 DROP + CREATE**（不是 OR REPLACE）：schema 隔离测试库重放整条迁移链时，
//     OR REPLACE 在列形状变化后撞 42P16，而那条错误指不到根因；
//  3. **owner 自对齐**：kernel 只收敛 owner 恰好等于运行时角色的对象，漏了会报
//     `extension database physical resource conflict`（误导性极强）。

const trustLevelLedgerMigration = "202611140002_user_trust_levels.sql"

func readMigration(t *testing.T, name string) (up, down string) {
	t.Helper()
	body, err := fs.ReadFile(Files(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("%s has no Down section", name)
	}
	return parts[0], parts[1]
}

func TestTrustLevelLedgerMigrationDeclaresTableAsLedgerOnly(t *testing.T) {
	up, _ := readMigration(t, trustLevelLedgerMigration)
	flat := strings.Join(strings.Fields(up), " ")

	if !strings.Contains(flat, "CREATE TABLE IF NOT EXISTS public.user_trust_levels") {
		t.Fatal("缺少 user_trust_levels 建表")
	}
	if !strings.Contains(flat, "REFERENCES public.users (id) ON DELETE CASCADE") {
		t.Fatal("user_id 必须是外键且级联删除（否则删用户会留孤儿账本行）")
	}
	// 表注释必须写明「不是授权来源」——这是本单最容易在交接时丢失的一句。
	if !strings.Contains(flat, "NOT authoritative") || !strings.Contains(flat, "Resolver.Evaluate") {
		t.Fatal("表注释必须写明「只做变更检测、不是授权来源」并指出权威入口")
	}
}

func TestTrustLevelLedgerViewIsDropCreateAndOwnerAligned(t *testing.T) {
	up, _ := readMigration(t, trustLevelLedgerMigration)
	flat := strings.Join(strings.Fields(up), " ")

	if !strings.Contains(flat, "DROP VIEW IF EXISTS sforum_core_v1.forum_user_trust_levels") {
		t.Fatal("视图必须先 DROP IF EXISTS（重放整条链时 OR REPLACE 会 42P16）")
	}
	if strings.Contains(flat, "CREATE OR REPLACE VIEW sforum_core_v1.forum_user_trust_levels") {
		t.Fatal("视图不得使用 CREATE OR REPLACE（会撞 42P16）")
	}
	if !strings.Contains(flat, "CREATE VIEW sforum_core_v1.forum_user_trust_levels WITH (security_barrier = true, security_invoker = false) AS") {
		t.Fatal("视图必须带 security_barrier/security_invoker")
	}
	for _, column := range []string{"ledger.user_id, users.username, ledger.level, ledger.tier_id, ledger.computed_at"} {
		if !strings.Contains(flat, column) {
			t.Fatalf("视图列定义缺 %q", column)
		}
	}
	if !strings.Contains(flat, "AND users.status = 'active' OFFSET 0") {
		t.Fatal("视图只应输出 active 用户，且必须带 OFFSET 0（security_barrier 下冻结谓词）")
	}
	if !strings.Contains(flat, "COMMENT ON VIEW sforum_core_v1.forum_user_trust_levels IS") {
		t.Fatal("视图必须带 COMMENT 说明它不是权威判定")
	}
	// owner 对齐：必须从 public.badges 读 owner 再 ALTER，而不是写死角色名。
	if !strings.Contains(flat, "pg_get_userbyid(classes.relowner)") || !strings.Contains(flat, "classes.relname = 'badges'") {
		t.Fatal("owner 必须从 public.badges 自对齐（写死角色名会在不同部署上失败）")
	}
	for _, target := range []string{
		"ALTER TABLE public.user_trust_levels OWNER TO",
		"ALTER VIEW sforum_core_v1.forum_user_trust_levels OWNER TO",
	} {
		if !strings.Contains(flat, target) {
			t.Fatalf("owner 对齐缺 %q", target)
		}
	}
}

func TestTrustLevelLedgerMigrationIsReversible(t *testing.T) {
	_, down := readMigration(t, trustLevelLedgerMigration)
	flat := strings.Join(strings.Fields(down), " ")
	// 逆序：先视图后表。
	viewIndex := strings.Index(flat, "DROP VIEW IF EXISTS sforum_core_v1.forum_user_trust_levels")
	tableIndex := strings.Index(flat, "DROP TABLE IF EXISTS public.user_trust_levels")
	if viewIndex < 0 || tableIndex < 0 {
		t.Fatalf("Down 段缺少拆除语句：view=%d table=%d", viewIndex, tableIndex)
	}
	if viewIndex > tableIndex {
		t.Fatal("Down 必须先拆视图再拆表（逆序）")
	}
}
