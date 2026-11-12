package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const badgeRulesMigration = "202611120002_badge_rules.sql"

// 静态契约测试：迁移是纯 SQL，跑不了「单元测试」，但「有没有把该写的东西写进文件」
// 是可以逐字断言的。真正的可逆性由 scripts/verify-migration-reversible.sh 在一次性
// PostgreSQL 上验证（含 down 后对象消失、再 up 重建）。两者职责不重叠：
// 这里挡住「漏写一个 DROP / 漏写 COMMENT / Down 段顺序颠倒」这类肉眼漏掉的错误。
func TestBadgeRulesMigrationDeclaresRuleDomain(t *testing.T) {
	up, down := readMigrationSections(t, badgeRulesMigration)

	for _, clause := range []string{
		"ALTER TABLE badges ADD COLUMN IF NOT EXISTS rule JSONB",
		"ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_zh TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE badges ADD COLUMN IF NOT EXISTS requirement_summary_en TEXT NOT NULL DEFAULT ''",
		// 旧 CHECK 必须先删再加：同时存在会让 rule 与 metric 互相误伤。
		"ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_rule_check",
		"ADD CONSTRAINT badges_rule_check CHECK ( (grant_mode = 'automatic' AND rule IS NOT NULL) OR (grant_mode = 'manual' AND rule IS NULL) )",
		// legacy 两列保留但必须写明「写路径不再维护」。
		"COMMENT ON COLUMN badges.metric IS 'LEGACY: not maintained by the write path since 202611120002",
		"COMMENT ON COLUMN badges.threshold IS 'LEGACY: not maintained by the write path since 202611120002",
		// 12 枚自动徽章转单条件规则。
		"UPDATE badges SET rule = jsonb_build_object( 'v', 1, 'root', jsonb_build_object( 'type', 'activity.count'",
		"'window', jsonb_build_object('kind', 'all')",
		"'cmp', jsonb_build_object('op', 'gte', 'value', threshold)",
		// 事实域三表 + 两个索引。
		"CREATE TABLE IF NOT EXISTS badge_facts",
		"fact_key TEXT NOT NULL CHECK (fact_key ~ '^[a-z0-9][a-z0-9._-]{1,120}$')",
		"delta BIGINT NOT NULL CHECK (delta <> 0)",
		"CREATE UNIQUE INDEX IF NOT EXISTS badge_facts_dedupe_idx ON badge_facts (user_id, fact_key, variant, dedupe_key) WHERE dedupe_key <> ''",
		"CREATE INDEX IF NOT EXISTS badge_facts_window_idx ON badge_facts (user_id, fact_key, variant, occurred_at DESC)",
		"CREATE TABLE IF NOT EXISTS user_fact_counters",
		"PRIMARY KEY (user_id, fact_key, variant)",
		"CREATE TABLE IF NOT EXISTS badge_rules_rollback",
		"legacy_metric TEXT",
		"legacy_threshold BIGINT",
		// 视图：追加列而不是改列序（CREATE OR REPLACE 只允许追加）。
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_badges WITH (security_barrier = true, security_invoker = false) AS",
		"badges.rule, badges.requirement_summary_zh, badges.requirement_summary_en",
		"CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badge_facts WITH (security_barrier = true, security_invoker = false) AS",
		"GREATEST(user_fact_counters.total, 0) AS total",
		"JOIN public.users ON users.id = user_fact_counters.user_id AND users.status = 'active'",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("badge rules Up 缺少：%q", clause)
		}
	}

	// 事实视图的公开面：写入流水与扩展身份都不许出现。
	// 切片只取视图定义本体，到 COMMENT 之前为止——注释里会写「不输出 delta」，
	// 把注释也算进来会让这条断言永远误报。
	factsView := up[strings.Index(up, "CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badge_facts"):]
	factsView = factsView[:strings.Index(factsView, "COMMENT ON VIEW sforum_core_v1.forum_user_badge_facts")]
	for _, forbidden := range []string{"dedupe_key", "source_extension_id", "delta"} {
		if strings.Contains(factsView, forbidden) {
			t.Fatalf("forum_user_badge_facts 不应输出 %q", forbidden)
		}
	}

	// Down：先拒绝不可反推的规则，再还原。
	for _, clause := range []string{
		"RAISE EXCEPTION 'Refusing to roll back badge rules",
		"USING HINT = 'This migration can only be rolled back while every automatic badge has a single activity.count leaf",
		// 规则与快照相同才还原 legacy，改过的按当前规则反推（计划 §4.3 / P-02）。
		"WHEN rollback.rule = badges.rule THEN rollback.legacy_metric",
		"ELSE badges.rule->'root'->'params'->>'metric'",
		"AND NOT EXISTS (SELECT 1 FROM public.badge_rules_rollback AS rollback WHERE rollback.badge_id = badges.id)",
		"SET metric = NULL, threshold = NULL WHERE badges.grant_mode = 'manual'",
		"DROP VIEW IF EXISTS sforum_core_v1.forum_user_badge_facts",
		"DROP VIEW IF EXISTS sforum_core_v1.forum_badges",
		// 旧 CHECK 必须原样回来，否则下一次 down 又会撞上新约束。
		"ADD CONSTRAINT badges_rule_check CHECK ( (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0)",
		"ALTER TABLE badges DROP COLUMN IF EXISTS rule",
		"DROP TABLE IF EXISTS badge_rules_rollback",
		"DROP TABLE IF EXISTS user_fact_counters",
		"DROP TABLE IF EXISTS badge_facts",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("badge rules Down 缺少：%q", clause)
		}
	}

	// 顺序断言：视图必须在删列之前重建（否则视图定义引用的列先消失，重建会失败）。
	if strings.Index(down, "DROP VIEW IF EXISTS sforum_core_v1.forum_badges") >
		strings.Index(down, "ALTER TABLE badges DROP COLUMN IF EXISTS rule") {
		t.Fatal("Down 段必须先重建 forum_badges 视图，再删除 badges.rule 列")
	}
	// 拒绝检查必须排在数据改写之前：先改写再拒绝，等于在报错前已经动了运营配置。
	if strings.Index(down, "RAISE EXCEPTION") > strings.Index(down, "UPDATE public.badges") {
		t.Fatal("Down 段的多条件拒绝检查必须排在还原 metric/threshold 之前")
	}
}

func readMigrationSections(t *testing.T, name string) (up string, down string) {
	t.Helper()
	body, err := fs.ReadFile(Files(), name)
	if err != nil {
		t.Fatalf("读取迁移 %s: %v", name, err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatalf("迁移 %s 没有 Down 段", name)
	}
	return strings.Join(strings.Fields(parts[0]), " "), strings.Join(strings.Fields(parts[1]), " ")
}
