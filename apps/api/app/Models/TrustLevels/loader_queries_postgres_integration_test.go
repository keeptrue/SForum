package trustlevels_test

import (
	"strings"
	"testing"
)

// TestLoaderPathSQLCounts 是**常开**的反 stub 断言：不读任何 SFORUM_TRUST_PERF* 变量，
// 随数据库测试套件一起跑。它只断 SQL 条数与形状，**不测时延**。
//
// 为什么必须常开（反例即本断言存在的理由）：把 `bootstrap` 的 trustLevelActivityReader
// 换成缓存读——做性能优化时最自然的动作——SQL 条数会从 5 掉到 4、`FROM user_activity`
// 整条消失。若这条断言藏 `SFORUM_TRUST_PERF=1` 后面，整条 CI 都不会红：线上所有人的
// 等级会静默掉 TL0（统计读不到 = 未解析 = 无已挣得、限额回落到年龄两态）。保险丝不能
// 装进抽屉。
//
// 为什么 20 个用户就够：条数与形状是**常数**，与夹具规模无关（20 与 10k 逐字相同：
// A=5 / B=6 / B1=6 / 关闭态=4）。10k 只对分位有意义，属可选段
// （loader_latency_postgres_integration_test.go）。
func TestLoaderPathSQLCounts(t *testing.T) {
	fixture := newLoaderFixture(t, loaderSQLCountUsers)
	policyA, policyB, policyBSingle := loaderPolicies(t)
	probeUser := fixture.firstUserID()

	queriesA := fixture.countQueries(t, policyA, probeUser)
	queriesB := fixture.countQueries(t, policyB, probeUser)
	queriesBSingle := fixture.countQueries(t, policyBSingle, probeUser)
	queriesFlagOff := fixture.countQueriesFlagOff(t, policyA, probeUser)
	t.Logf("SQL 条数实测：A(无规则)=%d  B(全档规则)=%d  B1(仅 TL3 有规则)=%d  关闭态=%d",
		len(queriesA), len(queriesB), len(queriesBSingle), len(queriesFlagOff))

	assertLoaderQueries(t, "A(无规则)", queriesA, loaderBaselineSQL, "")
	assertLoaderQueries(t, "B(全档规则)", queriesB, loaderWithRulesSQL, loaderAccountAgeSQL)
	assertLoaderQueries(t, "B1(仅 TL3 规则)", queriesBSingle, loaderWithRulesSQL, loaderAccountAgeSQL)

	// 查询数 = 条件类型数，与档位数无关：3 档带规则 vs 1 档带规则必须同条数。
	// （本夹具只演示 type 数 = 1；type 数 = 4 由 Stub 环境的
	// TestPrepareIssuesOneQueryPerConditionType 钉着。）
	if len(queriesB) != len(queriesBSingle) {
		t.Fatalf("带规则的档位从 3 收到 1，SQL 条数却变了：%d → %d（应为同一条件类型数）",
			len(queriesB), len(queriesBSingle))
	}
	// 规则侧只多一条：本夹具只有一个条件类型（identity.account_age）。
	// 若规则路径多查一条，装配层或 Conditions 侧就变了，必须显式改基线而不是悄悄放过。
	if len(queriesB)-len(queriesA) != loaderRuleSQL {
		t.Fatalf("规则侧多出 %d 条 SQL，期望 %d 条（条件类型数）：\nA:\n%s\nB:\n%s",
			len(queriesB)-len(queriesA), loaderRuleSQL,
			strings.Join(queriesA, "\n---\n"), strings.Join(queriesB, "\n---\n"))
	}

	// 关闭态是「功能关闭」基线，不是侧 A：policy 与 activity 两段都短路，所以恰好
	// 比侧 A 少一条（activity 读）。这条关系是「开关真的短路了真读」的形状证据。
	if len(queriesA)-len(queriesFlagOff) != 1 {
		t.Fatalf("关闭态与侧 A 的条数差为 %d，期望 1（activity 读恰好消失）\nA:\n%s\n关闭态:\n%s",
			len(queriesA)-len(queriesFlagOff),
			strings.Join(queriesA, "\n---\n"), strings.Join(queriesFlagOff, "\n---\n"))
	}
	if strings.Contains(strings.Join(queriesFlagOff, "\n"), "FROM user_activity") {
		t.Fatalf("关闭态仍读了 user_activity：\n%s", strings.Join(queriesFlagOff, "\n---\n"))
	}
}
