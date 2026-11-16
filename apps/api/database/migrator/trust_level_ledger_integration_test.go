package migrator_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/migrator"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 信任等级账本（`user_trust_levels` + `sforum_core_v1.forum_user_trust_levels`）的
// **真库**断言。
//
// 为什么放在 `migrator` 包而不是 `migrations` 包：`migrations` 的测试 import
// `migrator` 会形成 import cycle（migrator 已经 import migrations）。顺带这也合理——
// 这里要验的是「跑完整条链之后库长什么样」，本来就是 migrator 的视角。
//
// 三条断言各自对应一次踩坑记录：
//  1. 表与视图**真的存在**且可查（`DROP VIEW IF EXISTS` + `CREATE VIEW` 的形态只有在
//     重放整条链时才暴露问题，静态测试抓不到）；
//  2. **owner 与既有 Core 对象一致**：kernel 只收敛 owner 恰好等于运行时角色的对象，
//     不一致时运行期报 `extension database physical resource conflict`——那个错误完全
//     指不到 owner 头上；
//  3. 账本**只做变更检测**：视图对所有 active 用户可查，且没有授权语义。
func TestTrustLevelLedgerTableAndViewExistAfterFullChain(t *testing.T) {
	ctx := context.Background()
	url := testdb.IsolatedURL(t)
	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: url}); err != nil {
		t.Fatalf("full chain up: %v", err)
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse isolated url: %v", err)
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	scalarString := func(query string) string {
		var value string
		if err := conn.QueryRow(ctx, query).Scan(&value); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return value
	}

	if got := scalarString(`SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = 'user_trust_levels'`); got != "1" {
		t.Fatalf("user_trust_levels 不存在（count=%s）", got)
	}
	if got := scalarString(`SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'sforum_core_v1' AND c.relname = 'forum_user_trust_levels'`); got != "1" {
		t.Fatalf("视图 forum_user_trust_levels 不存在（count=%s）", got)
	}
	// owner 对齐：与 public.badges 同源。
	if got := scalarString(`SELECT pg_get_userbyid(t.relowner) FROM pg_class t
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public' AND t.relname = 'user_trust_levels'`); got == "" {
		t.Fatal("user_trust_levels 没有 owner")
	} else {
		t.Logf("user_trust_levels owner = %s", got)
	}
	// owner 比较必须钉住 namespace：同库里有别的包留下的私有 schema，里面同样有
	// `badges`（迁移链测试在自己 schema 里重放建表），不带 schema 条件的 join 会
	// 同时匹配到 public 与那些私有对象，返回多行、行序不定，断言随机红
	// （2026-11-17 覆盖率门就是这样红的：public 两个对象 owner 一致，却被
	// 私有 schema 里 owner=sforum 的 badges 比出 false）。
	if got := scalarString(`SELECT (pg_get_userbyid(t.relowner) = pg_get_userbyid(b.relowner))::text
		FROM pg_class t
		JOIN pg_namespace tn ON tn.oid = t.relnamespace AND tn.nspname = 'public'
		JOIN pg_class b ON b.relname = 'badges'
		JOIN pg_namespace bn ON bn.oid = b.relnamespace AND bn.nspname = 'public'
		WHERE t.relname = 'user_trust_levels'`); got != "true" {
		t.Fatalf("账本表 owner 与 public.badges 不一致（equal=%s）：kernel 会报 physical resource conflict", got)
	}
	if got := scalarString(`SELECT (pg_get_userbyid(v.relowner) = pg_get_userbyid(b.relowner))::text
		FROM pg_class v
		JOIN pg_namespace vn ON vn.oid = v.relnamespace AND vn.nspname = 'sforum_core_v1'
		JOIN pg_class b ON b.relname = 'badges'
		JOIN pg_namespace bn ON bn.oid = b.relnamespace AND bn.nspname = 'public'
		WHERE v.relname = 'forum_user_trust_levels'`); got != "true" {
		t.Fatalf("视图 owner 与 public.badges 不一致（equal=%s）", got)
	}
	// 视图可查（空表也要能查，证明列定义与 JOIN 都对）。
	if got := scalarString(`SELECT count(*)::text FROM sforum_core_v1.forum_user_trust_levels`); got != "0" {
		t.Fatalf("新视图应为空（count=%s）", got)
	}
	// 账本表的主键约束是 job 的唯一写入形态（同一用户一行）。这里只钉住约束存在：
	// 真正带用户行的 upsert 断言留到 job 落地时（那一刀才需要造完整 users 夹具）。
	if got := scalarString(`SELECT count(*)::text FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		WHERE t.relname = 'user_trust_levels' AND c.contype = 'p'`); got != "1" {
		t.Fatalf("user_trust_levels 缺主键（count=%s）：upsert 会变成重复行", got)
	}
}
