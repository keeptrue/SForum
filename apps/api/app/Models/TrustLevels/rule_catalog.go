package trustlevels

import (
	"sync"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 信任等级的条件子集 = 共享条件目录 − 内容质量那两条。
//
// 为什么排除 `content.pinned` / `content.hidden`：
//   - 它们量的是「内容被管理员怎么处理过」，属审核信号而不是成就信号。把它当等级
//     门槛等于「被置顶过才配升级」，运营一眼就会反对；
//   - 负向使用（`not hidden`）更糟：它把「没被隐藏过」变成晋升优势，等于让一次审核
//     决定长期身份差异，而审核记录本身不公开、不可申诉；
//   - 计划 D4 已裁决：审核信号、内容质量、阅读行为都不进信任等级条件族。
//
// 这两条**仍在共享目录里**（徽章继续用），本包只做子集裁剪，不复制第二份条件定义
// ——复制就是两份口径的开始。
var ruleExcludedConditionKeys = []string{
	conditions.KeyContentPinned,
	conditions.KeyContentHidden,
}

// RuleConditionSet 是信任等级可用的条件子集（只读）。
//
// 它实现 `ruleengine.Catalog`，所以 `ruleengine.Validate` 直接拿它做白名单：子集外
// 的 key 一定校验不过，不存在「忘了在哪一处再判一次白名单」的第二道口子。
type RuleConditionSet struct {
	defs  map[string]conditions.Definition
	order []string
}

var (
	ruleConditionSetOnce sync.Once
	ruleConditionSetVal  *RuleConditionSet

	sharedDefinitionsOnce sync.Once
	sharedDefinitionsVal  map[string]conditions.Definition
)

// RuleConditions 返回子集里的条件定义（固定顺序，供后台目录端点）。
//
// 返回新切片：调用方（HTTP 层）不该能改到只读单例的内部状态。
func RuleConditions() []conditions.Definition {
	set := ruleConditionSet()
	out := make([]conditions.Definition, 0, len(set.order))
	for _, key := range set.order {
		out = append(out, set.defs[key])
	}
	return out
}

// RuleConditionAllowed 报告某条件是否属于信任等级子集。
//
// 与 `SharedCondition` 的分工是**修法不同**：
//   - 子集里没有、共享目录里有 → `rule_condition_not_available`（这条条件存在，
//     只是等级不能用；换成另一条即可）；
//   - 共享目录里根本没有 → `rule_unknown_condition`（key 拼错了或插件未启用）。
//
// 把两者合并不是省事，而是把「该换哪一条」这条信息从运营手里拿走。
func RuleConditionAllowed(key string) bool {
	_, ok := ruleConditionSet().defs[key]
	return ok
}

// SharedCondition 在**共享目录**（徽章与等级共用的全量条件）里查一条定义。
//
// 只用于区分「未知条件」与「已知但不可用」，不参与白名单判定。
func SharedCondition(key string) (conditions.Definition, bool) {
	def, ok := sharedDefinitions()[key]
	return def, ok
}

// ruleConditionCatalog 返回喂给 `ruleengine.Validate` 的 Catalog。
func ruleConditionCatalog() ruleengine.Catalog { return ruleConditionSet() }

// ConditionMeta 实现 `ruleengine.Catalog`。
func (s *RuleConditionSet) ConditionMeta(key string) (ruleengine.ConditionMeta, bool) {
	def, ok := s.defs[key]
	if !ok {
		return ruleengine.ConditionMeta{}, false
	}
	return ruleengine.ConditionMeta{
		Key:            def.Key,
		ValueKind:      def.ValueKind,
		AllowedCompare: def.Compare,
		Params:         def.Params,
	}, true
}

// Definitions 按注册顺序返回子集定义（副本）。
func (s *RuleConditionSet) Definitions() []conditions.Definition {
	out := make([]conditions.Definition, 0, len(s.order))
	for _, key := range s.order {
		out = append(out, s.defs[key])
	}
	return out
}

// Definition 返回单个子集条件。
func (s *RuleConditionSet) Definition(key string) (conditions.Definition, bool) {
	def, ok := s.defs[key]
	return def, ok
}

func ruleConditionSet() *RuleConditionSet {
	ruleConditionSetOnce.Do(func() { ruleConditionSetVal = buildRuleConditionSet() })
	return ruleConditionSetVal
}

func sharedDefinitions() map[string]conditions.Definition {
	sharedDefinitionsOnce.Do(func() {
		definitions := conditions.Core().Definitions()
		sharedDefinitionsVal = make(map[string]conditions.Definition, len(definitions))
		for _, def := range definitions {
			sharedDefinitionsVal[def.Key] = def
		}
	})
	return sharedDefinitionsVal
}

func buildRuleConditionSet() *RuleConditionSet {
	excluded := make(map[string]struct{}, len(ruleExcludedConditionKeys))
	for _, key := range ruleExcludedConditionKeys {
		excluded[key] = struct{}{}
	}
	definitions := conditions.Core().Definitions()
	set := &RuleConditionSet{
		defs:  make(map[string]conditions.Definition, len(definitions)),
		order: make([]string, 0, len(definitions)),
	}
	for _, def := range definitions {
		if _, skip := excluded[def.Key]; skip {
			continue
		}
		set.defs[def.Key] = def
		set.order = append(set.order, def.Key)
	}
	return set
}

// PublishableRuleConditions 返回可以下发给编辑器的条件子集（**发布面**）。
//
// 与 `RuleConditions` 的差别只有一条：**引用的候选全空时不发布该条件**。理由与徽章侧
// `conditions.Registry.PublishableDefinitions` / `hasCandidates` 逐字相同——一条
// `objectRef` 参数没有候选的条件，编辑器只能渲染出空下拉：运营点进去既选不出值、也存
// 不进去，等于在 UI 里摆一条死路。同一份共享目录，两个域必须同口径。
//
// 三种「空」必须分开，混起来就是两种事故：
//   - **候选读到但确实为空**（该 kind 组装好、`Candidates: []`）→ 不发布；
//   - **取不到候选**（`Unavailable: true`，读库失败或未注入）→ **照常发布**：编辑器
//     会按 `unavailable` 退回文本输入，条件仍然可配。把「取不到」当「空」会让一次 DB
//     抖动把运营的编辑器砍掉四条条件；
//   - **校验白名单**（`RuleConditions` / `RuleConditionAllowed` / `sharedCondition`）
//     → **永不受本函数影响**。否则插件停用期间存量规则会读不出来——那是把「暂时没候选」
//     当成了「条件不存在」，而这两件事的修法完全不同（启用插件 vs 换条件）。
func PublishableRuleConditions(
	factCandidates []conditions.FactCandidate,
	refCandidates conditions.RefCandidateSet,
) []conditions.Definition {
	factKeyCount := len(conditions.FactKeys(factCandidates))
	groups := make(map[string]conditions.RefCandidateGroup, len(refCandidates))
	for _, group := range refCandidates {
		if _, exists := groups[group.Kind]; exists {
			continue
		}
		groups[group.Kind] = group
	}
	factKeys := conditions.FactKeys(factCandidates)
	set := ruleConditionSet()
	out := make([]conditions.Definition, 0, len(set.order))
	for _, key := range set.order {
		def := set.defs[key]
		if !hasPublishableCandidates(def, factKeyCount, groups) {
			continue
		}
		out = append(out, withFactOptions(def, factKeys))
	}
	return out
}

// withFactOptions 把已声明的事实 key 填进 `factKey` 参数的 Options。
//
// 不填 = 条件发布了、下拉还是空的——比不发布更糟：运营看得见、点得进去、选不出来。
// 与徽章侧 `conditions.withFactOptions` 同一条裁决、同一套做法（**必须复制 specs**：
// `Core()` 是只读单例，原地写会污染校验用的全集，并在并发下产生数据竞争）。
func withFactOptions(def conditions.Definition, factKeys []string) conditions.Definition {
	if len(factKeys) == 0 {
		return def
	}
	specs := make([]ruleengine.ParamSpec, len(def.Params.Specs))
	copy(specs, def.Params.Specs)
	changed := false
	for index := range specs {
		if specs[index].Kind == ruleengine.ParamObjectRef && specs[index].RefKind == "fact" {
			specs[index].Options = append([]string(nil), factKeys...)
			changed = true
		}
	}
	if !changed {
		return def
	}
	def.Params = ruleengine.ParamsSchema{Specs: specs}
	return def
}

// hasPublishableCandidates 判一条条件的所有 objectRef 参数是否都有可用候选。
//
// 一个条件有多个 objectRef 参数时，**任一**参数的候选读到为空就不发布该条：只要有一格
// 是空下拉，这条条件在编辑器里就填不完整。
func hasPublishableCandidates(
	def conditions.Definition,
	factKeyCount int,
	groups map[string]conditions.RefCandidateGroup,
) bool {
	for _, spec := range def.Params.Specs {
		if spec.Kind != ruleengine.ParamObjectRef {
			continue
		}
		// `fact` 不是 Core 对象：候选来自插件清单（fact catalog），不在 refCandidates 里。
		if spec.RefKind == "fact" {
			if factKeyCount == 0 {
				return false
			}
			continue
		}
		group, present := groups[spec.RefKind]
		if !present {
			// 调用方没给这一类候选（例如只查了部分 kind）：无法判定，按「照常发布」处理。
			// 判成「空」会把调用方的省事变成运营的缺功能。
			continue
		}
		if group.Unavailable {
			continue
		}
		if len(group.Candidates) == 0 {
			return false
		}
	}
	return true
}
