package badges

import (
	"fmt"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// RequirementText 是公开面的「获得条件」自然语言（双语）。
//
// 只放运营填的文本，或由**服务端**从单条件规则生成的句子——**绝不把 AST 漏出去**：
// 公开响应里出现 `rule`/`cmp`/`params`/`children`/`op` 这类内部词汇会让规则结构
// 变成事实上的公开契约，之后改 AST 就是破坏性变更。
type RequirementText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// metricLabels 是生成句子用的指标名。只含业务词汇，不含条件 key。
var metricLabels = map[string][2]string{
	conditions.MetricTopicCount:        {"发布主题", "topics published"},
	conditions.MetricCommentCount:      {"发表评论", "comments posted"},
	conditions.MetricLikeReceivedCount: {"收到赞", "likes received"},
	conditions.MetricLikeGivenCount:    {"给出赞", "likes given"},
	conditions.MetricBookmarkCount:     {"收藏内容", "bookmarks"},
	conditions.MetricDaysVisited:       {"访问天数", "days visited"},
}

// BuildRequirementText 返回公开面的双语要求。
//
// 优先级：运营填的文本优先（逐语言独立回落）；某语言为空且规则是**单条件**
// （单 `activity.count` 叶子 + gte）时按模板生成；规则不是单条件时该语言留空——
// 宁可不出文案，也不要把内部结构拼成一句读不懂的话。
func BuildRequirementText(badge Badge) RequirementText {
	text := RequirementText{
		Zh: strings.TrimSpace(badge.RequirementSummaryZh),
		En: strings.TrimSpace(badge.RequirementSummaryEn),
	}
	metric, threshold, ok := fastPathRuleForText(badge.Rule)
	if !ok {
		return text
	}
	labels, ok := metricLabels[metric]
	if !ok {
		return text
	}
	if text.Zh == "" {
		text.Zh = fmt.Sprintf("%s ≥ %d", labels[0], threshold)
	}
	if text.En == "" {
		text.En = fmt.Sprintf("%s ≥ %d", labels[1], threshold)
	}
	return text
}

// fastPathRuleForText 只认「单 activity.count 叶子 + window.all + gte」这一种形状。
//
// 与求值快路径同一个判据（`fastPathRule`），但这里允许任意正阈值：文案只需要
// 「指标 + 阈值」，不需要参与授予决策。
func fastPathRuleForText(rule *ruleengine.Node) (string, int64, bool) {
	if rule == nil || !rule.IsLeaf() || rule.Type != conditions.KeyActivityCount || rule.Cmp == nil {
		return "", 0, false
	}
	if rule.Cmp.Op != ruleengine.CompareGTE || rule.Cmp.Value <= 0 {
		return "", 0, false
	}
	metric, _ := rule.Params["metric"].(string)
	window, ok := rule.Params["window"].(map[string]any)
	if !ok || window["kind"] != ruleengine.WindowKindAll {
		return "", 0, false
	}
	if _, hasDays := window["days"]; hasDays {
		return "", 0, false
	}
	return metric, rule.Cmp.Value, true
}
