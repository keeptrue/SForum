package trustlevels

import (
	"context"
	"fmt"
	"sort"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RefCandidateSource 提供「可被规则引用的 Core 对象」候选集合。
//
// 为什么用窄接口而不是直接依赖 `*badges.PostgresStore`（D17）：
//   - `Models/TrustLevels` 一旦 import `Models/Badges`，两个域就硬耦合，之后每个
//     「等级也想要」的查询都会往 `badges.PostgresStore` 这个 God object 上堆方法，
//     直接撞 AGENTS.md 的非增长基线；
//   - 真实查询（`RefCandidates` / `LoadRefNames`）留在 `Badges` 侧，本包只声明
//     「我需要什么」，由 bootstrap 注入。这与既有的 `ActivityReader`、
//     `Forum.TrustPolicyResolver` 是同一套路，fakes 也可测。
//
// 方法刻意返回整个 `conditions.RefCandidateSet`（五类一次给全）：一次保存只查一次库，
// 而不是每个引用一次往返；同时候选集合天然就是「存在性」的真值——被真删掉的对象不在
// 候选里，被停用/隐藏的对象仍在（带 flag），与徽章 `rule_health` 同口径。
type RefCandidateSource interface {
	RefCandidates(ctx context.Context) (conditions.RefCandidateSet, error)
}

// FactKeySource 返回「当前已启用插件声明的事实 key」。
//
// 它是闭包而不是实例：装配顺序是 infra → extension → domains，事实目录在 domains
// 阶段才建好（D20）。未绑定（返回空切片）时 `fact.count` 一律按「未声明」处理，
// 与「未声明即无法判定」的既有语义一致，属 fail-closed。
type FactKeySource func() []string

// 失效种类。取值与徽章 `rule_health` **逐字相同**：前端已经有一套按 kind 渲染
// 「去哪修」的组件，另起一套词汇等于让运营在两个页面看到两种说法。
const (
	IssueMissingCategory  = "missing_category"
	IssueMissingTag       = "missing_tag"
	IssueMissingRole      = "missing_role"
	IssueMissingProvider  = "missing_provider"
	IssueMissingBadgeSlug = "missing_badge_slug"
	IssueMissingFactKey   = "missing_fact_key"
)

// RuleIssue 是规则里的一处失效引用。
//
// 形状对齐 `badges.RuleIssue`（kind / path / ref / label），`path` 与
// `ruleengine.Error.Path` 同一套坐标，前端可以直接定位到编辑器里的节点。
type RuleIssue struct {
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

// refKindIssueKind 把条件参数的 RefKind 映射成失效种类。
//
// 未知 RefKind 返回空串 = 不检查：宁可少报一条，也不要因为将来新增一种引用类型就
// 把所有旧规则判成失效（那会让一次升级把全站规则锁死）。
func refKindIssueKind(refKind string) string {
	switch refKind {
	case conditions.RefKindCategory:
		return IssueMissingCategory
	case conditions.RefKindTag:
		return IssueMissingTag
	case conditions.RefKindRole:
		return IssueMissingRole
	case conditions.RefKindProvider:
		return IssueMissingProvider
	case conditions.RefKindBadge:
		return IssueMissingBadgeSlug
	case "fact":
		return IssueMissingFactKey
	}
	return ""
}

// ValidateRuleReferences 检查一棵树里所有对象引用是否仍然存在。
//
// **只在写路径调用**：`DecodeConfig` 没有 ctx 也没有库，而它被资料页/论坛 provider
// 的读路径调用——把存在性检查塞进去会把读路径变成带 DB 往返的路径，一次慢查询就能
// 拖住整页渲染（计划 §4.2 的硬约束）。
//
// 返回的顺序是**树的书写顺序**：运营按编辑器从上往下看，报错也跟着走。
func ValidateRuleReferences(
	ctx context.Context,
	root ruleengine.Node,
	source RefCandidateSource,
	factKeys []string,
) ([]RuleIssue, error) {
	// 先看这棵树有没有引用型参数；没有就不必查库（绝大多数规则都不引用对象）。
	if !hasReferenceLeaves(root) {
		return nil, nil
	}
	if source == nil {
		// 装配错误要吵，不要静默放行：否则「引用已删除也不报错」会变成默认行为。
		return nil, fmt.Errorf("trustlevels: 引用校验缺少候选来源（RefCandidateSource 未注入）")
	}
	set, err := source.RefCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("trustlevels: 读取引用候选失败：%w", err)
	}
	known := make(map[string]map[string]struct{}, len(set))
	for _, group := range set {
		values := make(map[string]struct{}, len(group.Candidates))
		for _, candidate := range group.Candidates {
			values[candidate.Value] = struct{}{}
		}
		known[group.Kind] = values
	}
	declaredFacts := make(map[string]struct{}, len(factKeys))
	for _, key := range factKeys {
		declaredFacts[key] = struct{}{}
	}

	issues := make([]RuleIssue, 0, 2)
	walkRuleLeaves(root, "root", func(node ruleengine.Node, path string) {
		def, ok := ruleConditionSet().Definition(node.Type)
		if !ok {
			// 条件本身都不在子集里 → 那是 `rule_condition_not_available` /
			// `rule_unknown_condition` 的活（解码期已拦），这里不重复报。
			return
		}
		for _, spec := range def.Params.Specs {
			if spec.Kind != ruleengine.ParamObjectRef {
				continue
			}
			value, _ := node.Params[spec.Name].(string)
			if value == "" {
				// 缺参数由 `ruleengine.Validate` 报 `param_missing`，这里不抢。
				continue
			}
			kind := refKindIssueKind(spec.RefKind)
			if kind == "" {
				continue
			}
			exists := false
			if kind == IssueMissingFactKey {
				_, exists = declaredFacts[value]
			} else {
				_, exists = known[spec.RefKind][value]
			}
			if exists {
				continue
			}
			issues = append(issues, RuleIssue{
				Kind:  kind,
				Path:  fmt.Sprintf("%s.params.%s", path, spec.Name),
				Ref:   value,
				Label: def.LabelZh,
			})
		}
	})
	return issues, nil
}

// RuleReferenceIssues 扫描整份策略里所有带规则的启用档位，返回引用失效明细。
//
// 只查**启用档位**：停用档位的规则不参与判定，把它的失效引用做成 422 会挡住
// 「先停用、稍后再修」这条合规路径。
//
// 引用失效是保存期拒绝（`rule_reference_invalid`），不是解码期：主题/分类被删之后，
// 旧规则仍然要能被读出来（运行时该叶子返回确定值 false/0，与徽章一致），否则一次
// 误删就让整份 trust.levels 解码失败 → 全站等级退回默认策略。这条边界是这一刀最
// 容易破的地方，测试里两个方向都钉住了。
func RuleReferenceIssues(
	ctx context.Context,
	policy Policy,
	source RefCandidateSource,
	factKeys []string,
) ([]FieldError, error) {
	details := make([]FieldError, 0, 2)
	rank := Level(0)
	for _, tier := range policy.Tiers {
		// 停用档位整档跳过：它不参与判定，失效引用也不该挡住保存。软停用的合规路径
		// 是「先停用、稍后再修」，把它做成 422 会逼运营删掉整档配置才能保存无关改动。
		if tier.Retired {
			continue
		}
		rank++
		if len(tier.Rule) == 0 {
			continue
		}
		root, err := ruleengine.Decode(tier.Rule)
		if err != nil {
			// 解码期已经校验过；走到这里说明内存里的策略被别的路径改了。
			// 不在这里重复报解码错误（那会与解码期明细撞 ID），直接跳过。
			continue
		}
		issues, err := ValidateRuleReferences(ctx, root, source, factKeys)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			details = append(details, FieldError{
				Rank:   rank,
				TierID: tier.ID,
				Field:  "rule",
				Code:   CodeRuleReferenceInvalid,
				Params: map[string]any{"path": issue.Path, "ref": issue.Ref},
				Reason: referenceIssueReason(issue),
			})
		}
	}
	// 同一份配置里多个档位引用同一个已删对象时，明细顺序按档位 rank 升序（上面的
	// 遍历顺序即此），不需要再排序。
	return details, nil
}

func referenceIssueReason(issue RuleIssue) string {
	switch issue.Kind {
	case IssueMissingCategory:
		return fmt.Sprintf("引用的分类 %q 已不存在（被删除或改名）；请重新选择分类", issue.Ref)
	case IssueMissingTag:
		return fmt.Sprintf("引用的标签 %q 已不存在；请重新选择标签", issue.Ref)
	case IssueMissingRole:
		return fmt.Sprintf("引用的角色 %q 已不存在；请重新选择角色", issue.Ref)
	case IssueMissingProvider:
		return fmt.Sprintf("引用的登录方式 %q 已不存在；请重新选择", issue.Ref)
	case IssueMissingBadgeSlug:
		return fmt.Sprintf("引用的徽章 %q 已不存在；请重新选择徽章", issue.Ref)
	case IssueMissingFactKey:
		return fmt.Sprintf("插件事实 %q 没有被任何已启用插件声明；请启用对应插件或换一个事实", issue.Ref)
	}
	return fmt.Sprintf("引用的对象 %q 已不存在", issue.Ref)
}

// hasReferenceLeaves 判断树里是否有任何 objectRef / fact 参数需要查库。
func hasReferenceLeaves(root ruleengine.Node) bool {
	found := false
	walkRuleLeaves(root, "root", func(node ruleengine.Node, _ string) {
		if found {
			return
		}
		def, ok := ruleConditionSet().Definition(node.Type)
		if !ok {
			return
		}
		for _, spec := range def.Params.Specs {
			if spec.Kind == ruleengine.ParamObjectRef {
				found = true
				return
			}
		}
	})
	return found
}

// SortedIssueKinds 返回失效种类的稳定顺序（供契约/测试枚举）。
func SortedIssueKinds() []string {
	kinds := []string{
		IssueMissingBadgeSlug, IssueMissingCategory, IssueMissingFactKey,
		IssueMissingProvider, IssueMissingRole, IssueMissingTag,
	}
	sort.Strings(kinds)
	return kinds
}
