package badges

import (
	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RequirementText 是公开面的「获得条件」自然语言（双语）。
//
// 自 2026-11 起它**只**由服务端从规则生成（`BuildRequirementText` /
// `BuildRuleRequirementText`），运营手填的 `requirement_summary_*` 不再参与。
// 宁可让句子来自规则，也不让公开面出现「一枚徽章没有任何说明」——手填时代的
// 真实故障就是：多条件/带窗口的规则没有自动文案，运营也填不准。
//
// **绝不把 AST 漏出去**：公开响应里出现 `rule`/`cmp`/`params`/`children`/`op`
// 这类内部词汇会让规则结构变成事实上的公开契约，之后改 AST 就是破坏性变更。
type RequirementText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// BuildRequirementText 返回一枚已保存徽章在公开面的双语文案。
//
// 名字由调用方一次批量解析后传入（见 `ref_names.go`）：渲染是纯函数，不做 IO，
// 所以「N 枚徽章 = N 次名字查询」这种 N+1 在结构上不可能发生。
//
// 手工徽章（`Rule == nil`）给固定句；规则解不开（`RuleBroken`）给泛称句，
// 不能谎称「由管理员手动授予」（那会把脏数据说成运营决定）。
func BuildRequirementText(badge Badge, names conditions.RefNames) RequirementText {
	if badge.Rule == nil && badge.RuleBroken {
		return RequirementText{Zh: genericConditionZh, En: genericConditionEn}
	}
	return renderRule(badge.Rule, names)
}

// BuildRuleRequirementText 是「规则 → 公开说明」的唯一公开入口。
//
// 后台预览端点（POST /admin/badges/rule-text）与公开面共用它：同一条规则、
// 同一份名字，两处必然得到同一句话（测试逐字断言）。
func BuildRuleRequirementText(rule *ruleengine.Node, names conditions.RefNames) RequirementText {
	return renderRule(rule, names)
}
