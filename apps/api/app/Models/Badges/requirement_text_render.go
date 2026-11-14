package badges

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件是规则树 → 公开说明的**唯一**拼接实现。
//
// 它只做树的遍历与连接，不查库、不认条件语义：每个叶子的措辞由
// `conditions.Definition.Phrase` 提供（那里才知道「这个条件的参数是什么意思」）。
// 这样公开目录、资料页徽章墙、后台预览端点三处用的是同一条链路，不可能分叉。
//
// 三条硬约束：
//   - **永远非空**：空串等于把「没有说明」重新带回公开面（手填已被移除）。
//   - 长度有上限：单句 ≤ 100、整段 ≤ 160（中英都按字符数），超限降级成概括句。
//   - 不含内部词汇：任何输出里都不能出现 AST 词汇或参数 slug（见测试的正则）。

const (
	// maxLeafRunes 是单个条件的句子上限。超限的叶子降级为「满足『条件名』条件」，
	// 保证长名字（例如超长主题标题）不会把整段撑爆。
	maxLeafRunes = 100
	// maxTextRunes 是公开说明整段的上限。
	maxTextRunes = 160
	// maxListedClauses 是组内最多列出的子句数，超出后只列前三条 + 总数。
	maxListedClauses = 4
	// listedHead 是压缩时保留的子句数。
	listedHead = 3
)

// 手工徽章与兜底句。它们会直接印在公开面上，所以是完整句子而不是标签。
const (
	manualRequirementZh = "由管理员手动授予"
	manualRequirementEn = "Granted manually by an admin"
	genericConditionZh  = "满足一个自定义条件"
	genericConditionEn  = "a custom condition is met"
	genericGroupZh      = "满足若干条件"
	genericGroupEn      = "some conditions are met"
)

// clause 是渲染中间结果：一句话 + 它覆盖的条件个数（压缩与降级时要用）。
type clause struct {
	zh     string
	en     string
	leaves int
	// negated 表示这句话来自 `not` 组。嵌进连接列表时要小写 `not:`——
	// 「A, and Not: B」是句中出现大写句首，读起来像两个句子粘在一起。
	negated bool
}

func (c clause) empty() bool { return strings.TrimSpace(c.zh) == "" || strings.TrimSpace(c.en) == "" }

func runeLen(value string) int { return utf8.RuneCountInString(value) }

// clipRunes 按字符截断并加省略号（只在超限降级里用）。
func clipRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if runeLen(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

// renderRule 把一棵规则树渲染成双语公开说明。
//
// `rule == nil` 表示手工徽章（运营手动授予），给固定句；这是「非空」约束里
// 唯一不需要遍历规则的分支。
func renderRule(rule *ruleengine.Node, names conditions.RefNames) RequirementText {
	if rule == nil {
		return RequirementText{Zh: manualRequirementZh, En: manualRequirementEn}
	}
	rendered := renderNode(rule, names)
	text := RequirementText{Zh: strings.TrimSpace(rendered.zh), En: strings.TrimSpace(rendered.en)}
	if text.Zh == "" {
		text.Zh = genericConditionZh
	}
	if text.En == "" {
		text.En = genericConditionEn
	}
	if runeLen(text.Zh) > maxTextRunes || runeLen(text.En) > maxTextRunes {
		return summarizeRule(rule, names, rendered.leaves)
	}
	return text
}

// summarizeRule 是整段超限时的降级：给总数 + 最关键（第一条）条件。
//
// 为什么保留一条而不是全部丢掉：公开面需要回答「怎么拿」，只给「满足 12 个条件」
// 等于把问题原样退回给用户；保留第一条至少给出方向。
func summarizeRule(rule *ruleengine.Node, names conditions.RefNames, leaves int) RequirementText {
	head := firstLeaf(rule, names)
	zhPrefix := fmt.Sprintf("满足 %d 个条件，其中最关键的是：", leaves)
	enPrefix := fmt.Sprintf("Meets %d conditions; the most important one is: ", leaves)
	return RequirementText{
		Zh: zhPrefix + clipRunes(head.zh, maxTextRunes-runeLen(zhPrefix)),
		En: enPrefix + clipRunes(head.en, maxTextRunes-runeLen(enPrefix)),
	}
}

// renderNode 递归渲染一个节点。
func renderNode(node *ruleengine.Node, names conditions.RefNames) clause {
	if node == nil {
		return clause{zh: genericConditionZh, en: genericConditionEn, leaves: 1}
	}
	if node.IsGroup() {
		return renderGroup(node, names)
	}
	return renderLeaf(node, names)
}

// renderLeaf 渲染一个叶子；措辞来自条件目录的 Phrase。
func renderLeaf(node *ruleengine.Node, names conditions.RefNames) clause {
	definition, ok := conditions.Core().Definition(node.Type)
	if !ok || definition.Phrase == nil {
		// 未知条件（插件卸载后留下的规则）：不写条件 key，只给一句完整的话。
		return clause{zh: genericConditionZh, en: genericConditionEn, leaves: 1}
	}
	zh, en := definition.Phrase(node.Params, node.Cmp, names)
	zh, en = strings.TrimSpace(zh), strings.TrimSpace(en)
	if zh == "" || en == "" || runeLen(zh) > maxLeafRunes || runeLen(en) > maxLeafRunes {
		zh = fmt.Sprintf("满足「%s」条件", definition.LabelZh)
		en = fmt.Sprintf("the \"%s\" condition is met", definition.LabelEn)
	}
	return clause{zh: zh, en: en, leaves: 1}
}

// renderGroup 渲染 all/any/not 组。
func renderGroup(node *ruleengine.Node, names conditions.RefNames) clause {
	children := make([]clause, 0, len(node.Children))
	total := 0
	for index := range node.Children {
		child := renderNode(&node.Children[index], names)
		children = append(children, child)
		total += child.leaves
	}
	if len(children) == 0 {
		return clause{zh: genericGroupZh, en: genericGroupEn, leaves: 1}
	}
	switch node.Op {
	case ruleengine.OpNot:
		// `not` 恰好一个子节点（由 Validate 保证）；多出来的一律只看第一个，
		// 不把非法形状拼成半句话。
		return clause{
			zh:      "不满足：" + children[0].zh,
			en:      "Not: " + children[0].en,
			leaves:  children[0].leaves,
			negated: true,
		}
	case ruleengine.OpAny:
		return joinClauses(children, total, "，或者", ", or ", true)
	default:
		return joinClauses(children, total, "，并且", ", and ", false)
	}
}

// joinClauses 连接子句；超过 maxListedClauses 时改成「满足全部 N 个条件，例如：…」。
//
// 中文一律用**标点**连接，且连接符**不带尾空格**（「A，并且B」）：`≥ 5并且` 是运营
// 第一眼能看到的粘连，而「并且 账户年龄」又像多打了一个空白（见 task-13 及其跟进）。
// 英文保留 `", and "`（英文需要空格）。
func joinClauses(children []clause, total int, zhSep, enSep string, disjunction bool) clause {
	if len(children) > maxListedClauses {
		return compressClauses(children, total, disjunction)
	}
	zhParts := make([]string, 0, len(children))
	enParts := make([]string, 0, len(children))
	for _, child := range children {
		zhParts = append(zhParts, child.zh)
		enParts = append(enParts, joinedEnglish(child))
	}
	return clause{
		zh:     strings.Join(zhParts, zhSep),
		en:     strings.Join(enParts, enSep),
		leaves: total,
	}
}

// compressClauses 把超宽组压成「总数 + 三个例子」。
//
// 为什么不是「A并且B…等 N 个条件」：那句话在中文里没有断句点，读完不知道 8 是
// 条件总数还是第 8 条；「满足全部 8 个条件，例如：A、B、C」把结论放在前面。
func compressClauses(children []clause, total int, disjunction bool) clause {
	zhHead := fmt.Sprintf("满足全部 %d 个条件，例如：", total)
	enHead := fmt.Sprintf("Meets all %d conditions, e.g. ", total)
	if disjunction {
		// `any` 压成「全部」会改变语义（只要一条成立就够），所以这里明说「任一」。
		zhHead = fmt.Sprintf("满足其中任一条件（共 %d 个），例如：", total)
		enHead = fmt.Sprintf("Meets any of the %d conditions, e.g. ", total)
	}
	zhParts := make([]string, 0, listedHead)
	enParts := make([]string, 0, listedHead)
	for _, child := range children[:listedHead] {
		zhParts = append(zhParts, child.zh)
		enParts = append(enParts, joinedEnglish(child))
	}
	return clause{
		zh:     zhHead + strings.Join(zhParts, "、"),
		en:     enHead + joinEnglishList(enParts),
		leaves: total,
	}
}

// joinedEnglish 把子句放进连接列表时的英文形态：嵌套 `not` 统一小写。
func joinedEnglish(child clause) string {
	if child.negated {
		return "not:" + strings.TrimPrefix(child.en, "Not:")
	}
	return child.en
}

// joinEnglishList 产出 `A, B and C`（最后一项用 and，不是逗号）。
func joinEnglishList(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// firstLeaf 返回树里第一个叶子的渲染结果（超限降级时用作「最关键的一条」）。
func firstLeaf(node *ruleengine.Node, names conditions.RefNames) clause {
	if node == nil {
		return clause{zh: genericConditionZh, en: genericConditionEn, leaves: 1}
	}
	if !node.IsGroup() {
		return renderLeaf(node, names)
	}
	for index := range node.Children {
		if child := firstLeaf(&node.Children[index], names); !child.empty() {
			return child
		}
	}
	return clause{zh: genericConditionZh, en: genericConditionEn, leaves: 1}
}
