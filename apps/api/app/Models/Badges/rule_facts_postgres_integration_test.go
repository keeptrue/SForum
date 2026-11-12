package badges

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 真库用例：插件事实条件在「已声明」与「未声明」两种世界里的求值结论。
//
// 这是 spec §5.1 的验收断言：未声明 → `Matched=false, Incomplete=true` 且 Errors
// 里带 `resolve_failed`。**不是** false/Incomplete=false（那等于「确定不满足」，
// `not` 会把它反转成成立），更不是 true。
func TestEvaluateRuleMarksUndeclaredFactUnknown(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	ctx := context.Background()

	store := NewPostgresStore(fixture.pool)
	// 模拟「扩展已卸载」：候选集合为空。
	store.SetFactKeyProvider(func() []string { return []string{} })

	result, err := store.EvaluateRule(ctx, userID, factVariantRule("acme.wiki.pages", ""))
	if err != nil {
		t.Fatalf("未知事实不该让整次评估报错（应进 Errors 且转 unknown），得到 %v", err)
	}
	if result.Matched {
		t.Fatal("未声明的事实条件绝不能判成满足（`lte` 会把 0 判成成立）")
	}
	if !result.Incomplete {
		t.Fatal("必须标记 Incomplete=true：结论依赖了无法判定的条件")
	}
	if len(result.Errors) != 1 || ruleengine.CodeOf(result.Errors[0]) != ruleengine.CodeResolveFailed {
		t.Fatalf("Errors 必须有一条 resolve_failed，得到 %v", result.Errors)
	}
}

// TestEvaluateRuleDeclaredFactReadsCounters 断言「同一个集合」把链路接通：
// 声明之后同一条规则读到真实计数值，且不再是 Incomplete。
func TestEvaluateRuleDeclaredFactReadsCounters(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_fact_counters (user_id, fact_key, variant, total) VALUES ($1,'acme.wiki.pages','',3)`,
		userID); err != nil {
		t.Fatalf("seed counters: %v", err)
	}

	store := NewPostgresStore(fixture.pool)
	store.SetFactKeyProvider(func() []string { return conditions.FactKeys([]conditions.FactCandidate{{Key: "acme.wiki.pages"}}) })

	result, err := store.EvaluateRule(ctx, userID, factVariantRule("acme.wiki.pages", ""))
	if err != nil {
		t.Fatalf("求值失败：%v", err)
	}
	if !result.Matched || result.Incomplete {
		t.Fatalf("声明后计数 3 ≥ 1 应确定成立，得到 Matched=%v Incomplete=%v", result.Matched, result.Incomplete)
	}
}
