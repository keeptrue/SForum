package conditions

import (
	"sort"
	"strings"
)

// 事实候选目录：`badgeConditions[]` → 编辑器下拉 + 保存期兜底。
//
// **它不是条件类型目录**（spec §0）：规则里引用插件事实的叶子恒为
// `type:"fact.count"`，这里声明的是那个条件的 `factKey` / `variant` **合法取值**。
// 所以本文件只负责「候选集合」，不参与 `ruleengine.Catalog`——往名单里塞条件会让
// 条件白名单随插件数量膨胀，而 `rule_health` 的 RefKind 检查也会丢掉统一入口。

// FactVariant 是某个事实 key 下的一个合法 variant 候选。
type FactVariant struct {
	Value   string
	LabelZh string
	LabelEn string
}

// FactCandidate 是一个已声明事实条件的完整候选。
//
// 双语 label/unit 进候选而不只是 key：插件在清单里声明了 `label{zh-CN,en-US}`，
// 若这里只留 key，编辑器下拉只能显示 key 原文，声明等于白做。
type FactCandidate struct {
	Key      string
	LabelZh  string
	LabelEn  string
	UnitZh   string
	UnitEn   string
	Compare  []string
	Variants []FactVariant
}

// FactDeclaration 是合并输入。
//
// 刻意不直接用清单结构体：`conditions` 属 Models 层，把 `Support/ExtensionManifest`
// 拉进来会让领域耦合到清单形状（且测试必须造整份清单）。装配层做一次转换即可。
type FactDeclaration struct {
	ExtensionID string
	Key         string
	Compare     []string
	LabelZh     string
	LabelEn     string
	UnitZh      string
	UnitEn      string
	Variants    []FactVariant
}

// FactConflict 是一条被拒绝的声明（进审计，不进目录）。
type FactConflict struct {
	Key         string
	ExtensionID string
	Reason      string
}

// 冲突原因。审计按字符串展示，改字面量即破坏审计可读性。
const (
	FactConflictCoreKey   = "core_condition_key"
	FactConflictNamespace = "namespace"
	FactConflictDuplicate = "duplicate_key"
)

// MergeFactDeclarations 把「当前已启用扩展」的事实声明合并成候选目录。
//
// 三条拒绝规则（spec §2.3 第 2 条 + 前缀纪律）：
//  1. key 与 Core 内置条件重名 → 拒绝（插件 id 理论上可以叫 `activity`，只靠前缀不够）；
//  2. key 不带 `<extensionID>.` 前缀 → 拒绝（清单校验已挡，这里是纵深防御：装配层
//     的输入可能来自历史数据或测试）；
//  3. 同一 key 被多处声明 → 保留第一个并记冲突（不静默覆盖，运营要看得见）。
//
// 输出按 Key 排序：目录是下发给编辑器的数组，顺序必须稳定，否则前端每次刷新都重排。
func MergeFactDeclarations(declarations []FactDeclaration) ([]FactCandidate, []FactConflict) {
	candidates := make([]FactCandidate, 0, len(declarations))
	conflicts := make([]FactConflict, 0)
	seen := make(map[string]struct{}, len(declarations))
	for _, declaration := range declarations {
		key := strings.TrimSpace(declaration.Key)
		extensionID := strings.TrimSpace(declaration.ExtensionID)
		if key == "" {
			conflicts = append(conflicts, FactConflict{Key: key, ExtensionID: extensionID, Reason: FactConflictNamespace})
			continue
		}
		if _, taken := Core().Definition(key); taken {
			conflicts = append(conflicts, FactConflict{Key: key, ExtensionID: extensionID, Reason: FactConflictCoreKey})
			continue
		}
		if extensionID == "" || !strings.HasPrefix(key, extensionID+".") || len(key) <= len(extensionID)+1 {
			conflicts = append(conflicts, FactConflict{Key: key, ExtensionID: extensionID, Reason: FactConflictNamespace})
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			conflicts = append(conflicts, FactConflict{Key: key, ExtensionID: extensionID, Reason: FactConflictDuplicate})
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, FactCandidate{
			Key:      key,
			LabelZh:  strings.TrimSpace(declaration.LabelZh),
			LabelEn:  strings.TrimSpace(declaration.LabelEn),
			UnitZh:   strings.TrimSpace(declaration.UnitZh),
			UnitEn:   strings.TrimSpace(declaration.UnitEn),
			Compare:  normalizeCompare(declaration.Compare),
			Variants: normalizeVariants(declaration.Variants),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Key < candidates[j].Key })
	return candidates, conflicts
}

// FactKeys 返回候选的 key 列表（给 `PublishableDefinitions` 与 resolver 用）。
func FactKeys(candidates []FactCandidate) []string {
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.Key)
	}
	return keys
}

// CandidatesByKey 建立 key → 候选 的索引。
func CandidatesByKey(candidates []FactCandidate) map[string]FactCandidate {
	index := make(map[string]FactCandidate, len(candidates))
	for _, candidate := range candidates {
		index[candidate.Key] = candidate
	}
	return index
}

// VariantDeclared 判定 (factKey, variant) 在给定候选下能否保存。
//
// 放行的三种情况（spec §8 Errata 2 第 1 条）：
//   - `factKey` 没有任何已声明候选（扩展停用/卸载）→ 放行，只在求值期 unknown。
//     拦住它会让「卸载插件后运营连规则都改不了」，而规则本身是运营资产；
//   - 该候选没有 variants（事实本来就没变体维度）→ 放行；
//   - `variant` 为空（省略 = 汇总所有变体）→ 放行。
//
// 只有「声明了变体候选、但值不在此列」才拒绝——那是拼写错误，放进来会静默永不满足。
func VariantDeclared(candidates []FactCandidate, factKey string, variant string) bool {
	variant = strings.TrimSpace(variant)
	if variant == "" {
		return true
	}
	candidate, ok := CandidatesByKey(candidates)[factKey]
	if !ok || len(candidate.Variants) == 0 {
		return true
	}
	for _, item := range candidate.Variants {
		if item.Value == variant {
			return true
		}
	}
	return false
}

func normalizeCompare(compare []string) []string {
	out := make([]string, 0, len(compare))
	seen := make(map[string]struct{}, len(compare))
	for _, op := range compare {
		value := strings.ToLower(strings.TrimSpace(op))
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeVariants(variants []FactVariant) []FactVariant {
	out := make([]FactVariant, 0, len(variants))
	seen := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		value := strings.TrimSpace(variant.Value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, FactVariant{Value: value, LabelZh: strings.TrimSpace(variant.LabelZh), LabelEn: strings.TrimSpace(variant.LabelEn)})
	}
	return out
}

// FactCandidateProvider 返回当前已启用扩展声明的事实候选（装配注入；nil = 无候选）。
//
// 返回切片而不是 map：`rule_health` 与编辑器都要**稳定顺序**；索引在各消费点按需建。
type FactCandidateProvider func() []FactCandidate
