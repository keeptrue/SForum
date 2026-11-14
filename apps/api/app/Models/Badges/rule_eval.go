package badges

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// EvaluateRule 在条件解析器上求值一棵规则树。
//
// 三步是分开的，因为每一步的失败语义不同：
//   - 建 resolver / Prepare 失败 → 返回 error（调用方按「这次评估不可用」处理）；
//   - 单条件求值失败 → 进 Result.Errors，该叶子按 unknown 计（fail-closed）；
//   - 结论依赖了 unknown → Matched=false + Incomplete=true，不授予但也不报错。
//
// 这里**不做**任何引用存在性检查：引用对象被删时条件返回确定的 0/false，那属于
// 「不满足」而不是「无法判定」。把后者当 error 会让整棵树变 unknown，徽章永远不发。
// FactKeyProvider 返回「当前已声明的插件事实 key」。
//
// M1 不注入（恒为空集）：`fact.count` 叶子一律 unknown，fail-closed。M3 由扩展
// registry 注入真实集合——不接通这条链路，M3 上线后所有插件事实条件都会
// 「规则存得下、永远不满足」，而且零报错。
type FactKeyProvider func() []string

// SetFactKeyProvider 注入事实 key 来源（bootstrap 装配；M1 不注入）。
func (s *PostgresStore) SetFactKeyProvider(provider FactKeyProvider) {
	if s == nil {
		return
	}
	s.factKeys = provider
}

func (s *PostgresStore) EvaluateRule(ctx context.Context, userID int64, rule ruleengine.Node) (ruleengine.Result, error) {
	var factKeys []string
	if s.factKeys != nil {
		factKeys = s.factKeys()
	}
	resolver, err := conditions.NewResolver(s.pool, userID, factKeys...)
	if err != nil {
		return ruleengine.Result{}, fmt.Errorf("badge rule resolver: %w", err)
	}
	if err := resolver.Prepare(ctx, rule); err != nil {
		return ruleengine.Result{}, fmt.Errorf("prepare badge rule: %w", err)
	}
	result, err := ruleengine.Eval(ctx, rule, resolver)
	if err != nil {
		return ruleengine.Result{}, fmt.Errorf("evaluate badge rule: %w", err)
	}
	return result, nil
}

// SetFactCandidates 注入事实候选来源（bootstrap 装配；nil = 没有扩展声明）。
//
// 与 `SetFactKeyProvider` 同源但用途不同：key 集合喂 resolver 与 rule_health，
// 候选（带 variants）喂保存期校验。两处都设成同一个 provider，避免「健康检查说
// 声明了、保存却说 variant 没声明」这种自相矛盾。
func (s *PostgresStore) SetFactCandidates(provider conditions.FactCandidateProvider) {
	if s == nil {
		return
	}
	s.factCandidates = provider
}
