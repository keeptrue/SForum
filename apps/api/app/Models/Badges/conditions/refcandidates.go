package conditions

import (
	"sort"
	"strings"
)

// 本文件是「objectRef 参数的候选来源」的形状定义。
//
// 为什么候选要单独一个面，而不是塞进 `Definition`：
//   - 条件是**静态目录**（12 条 Core 条件），候选是**数据**（分类/标签/角色/徽章/
//     登录方式随运营增删）。把数据放进只读单例会让 `Core()` 变成全局可变状态，
//     并在并发下产生竞争（同 `PublishableDefinitions` 的既有裁决）。
//   - 候选要按 kind 一批下发，编辑器按 `ParamSpec.RefKind` 查同一张表。它比
//     `fact.count` 的 `FactCandidate` 窄得多：只有值、展示名和一个「为什么不该选」
//     的标记，没有比较运算符、没有 variant、没有计量单位。
//
// 契约（2026-11-13，与 OpenAPI `BadgeRefCandidateGroup` 逐字对应）：
//   - 五个 kind **永远是五项、固定顺序** badge → category → tag → role → provider；
//   - `unavailable` 表示「取不到候选」，与「候选为空」是两件事：前者让编辑器退回
//     文本输入，后者只是没有可选项（运营仍该能手填）；
//   - `truncated` 表示还有更多，编辑器要提示「仅显示前 N 条」；
//   - `flags` 只做标记，**不过滤**——已隐藏的分类、已停用的角色仍然可以出现在
//     规则里（历史事实），也可以被继续选中，只是要让运营看见后果。

// RefCandidatesLimit 是单类候选的上限。
//
// 500 是编辑器可用性与查询成本的折中：分类/标签/角色/徽章都远小于这个量级，
// 登录方式更是只有个位数；真到了 500 说明该用搜索而不是下拉，编辑器有 truncated
// 提示兜底。
const RefCandidatesLimit = 500

// 候选标记。值是契约的一部分（OpenAPI enum），改名即破坏前端。
const (
	// RefFlagHidden 表示该对象当前不可公开访问（分类 visibility<>'public'、
	// 标签 status<>'active'）。规则引用它仍然合法：徽章记录的是历史事实。
	RefFlagHidden = "hidden"
	// RefFlagDisabled 表示该对象当前被停用（角色 is_enabled=false、徽章 enabled=false）。
	RefFlagDisabled = "disabled"
)

// 五类候选的 kind。顺序即下发顺序，见 `RefKinds`。
const (
	RefKindBadge    = "badge"
	RefKindCategory = "category"
	RefKindTag      = "tag"
	RefKindRole     = "role"
	RefKindProvider = "provider"
)

// refKindOrder 是唯一的顺序定义：下发、`NormalizeRefCandidateSet` 补齐、测试断言
// 都读它。写成变量而不是在三个地方各写一遍，是为了让「顺序固定」只有一处可漂移。
var refKindOrder = []string{
	RefKindBadge,
	RefKindCategory,
	RefKindTag,
	RefKindRole,
	RefKindProvider,
}

// RefKinds 返回五类的固定顺序（副本，调用方改它不影响本包）。
func RefKinds() []string {
	out := make([]string, len(refKindOrder))
	copy(out, refKindOrder)
	return out
}

// RefCandidate 是一个可选项。
type RefCandidate struct {
	// Value 是写进规则 params 的值（slug / roles.key / provider id）。
	Value string
	// Label 是展示名。数据库里大多只有单语名（categories.name 等），provider 有
	// 双语声明时由调用方挑一种（zh 优先、回落 en）。
	Label string
	// Flags 是 `RefFlagHidden` / `RefFlagDisabled` 的子集，空表示正常。
	Flags []string
}

// RefCandidateGroup 是一类候选。五个 kind 各一条，永远是五项。
type RefCandidateGroup struct {
	Kind       string
	Candidates []RefCandidate
	// Unavailable 表示**取不到**候选（查询失败 / 来源未注入），不是「没有候选」。
	Unavailable bool
	// Truncated 表示结果被 `RefCandidatesLimit` 截断。
	Truncated bool
}

// RefCandidateSet 是五类候选的集合，顺序由 `RefKinds` 固定。
type RefCandidateSet []RefCandidateGroup

// NewRefCandidateGroup 规范化一类候选：排序、去重、截断到上限。
//
// 排序口径刻意是「展示名 → 值」而不是 SQL 的 ORDER BY：候选来自两种来源（Core 表
// 查询与 identity registry 快照），后者是 map，Go 的遍历顺序随机。把最终顺序交给
// 这一个函数，SQL 的 ORDER BY 只负责让 `LIMIT` 截掉的是「排在后面的那一批」。
//
// 去重按 Value 保留第一条：数据库中 slug/key/provider id 都唯一，去重真正防的是
// 「两种来源合并」时的重复项（未来若把已声明 provider 与已有绑定拼在一起）。
func NewRefCandidateGroup(kind string, rows []RefCandidate) RefCandidateGroup {
	normalized := make([]RefCandidate, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		value := strings.TrimSpace(row.Value)
		if value == "" {
			// 空值是脏数据（slug 有 NOT NULL + UNIQUE，正常不会出现）。静默丢弃而
			// 不是返回错误：一个坏行不该让整类候选变 unavailable。
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, RefCandidate{
			Value: value,
			Label: strings.TrimSpace(row.Label),
			Flags: normalizeRefCandidateFlags(row.Flags),
		})
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		// 先按展示名（大小写不敏感）再按值：中文名按 UTF-8 字节序，与 SQL 的
		// 本地化排序可能差一两条——只影响被截断的最后一项，`truncated` 仍为真。
		left := strings.ToLower(normalized[i].Label)
		right := strings.ToLower(normalized[j].Label)
		if left != right {
			return left < right
		}
		return normalized[i].Value < normalized[j].Value
	})
	truncated := len(normalized) > RefCandidatesLimit
	if truncated {
		normalized = normalized[:RefCandidatesLimit]
	}
	return RefCandidateGroup{
		Kind:        kind,
		Candidates:  normalized,
		Unavailable: false,
		Truncated:   truncated,
	}
}

// NewUnavailableRefCandidateGroup 构造「取不到候选」的一类。
func NewUnavailableRefCandidateGroup(kind string) RefCandidateGroup {
	return RefCandidateGroup{Kind: kind, Candidates: []RefCandidate{}, Unavailable: true}
}

// UnavailableRefCandidateSet 返回五项全部 unavailable 的集合。
//
// 调用方拿不到来源时用它兜底：编辑器会退回文本输入，而不是渲染一个只有占位项的
// 空下拉（那正是本刀要消灭的死路）。
func UnavailableRefCandidateSet() RefCandidateSet {
	return NormalizeRefCandidateSet(nil)
}

// NormalizeRefCandidateSet 把散装的分组按固定顺序补齐成五项。
//
// 缺项补 unavailable（不是「空候选」）：调用方没给这一 kind 的证据，编辑器就不该
// 假装「这里确实没有可选项」。未知 kind 直接丢弃——多出来的项会让前端按未知
// refKind 渲染。
func NormalizeRefCandidateSet(groups []RefCandidateGroup) RefCandidateSet {
	index := make(map[string]RefCandidateGroup, len(groups))
	for _, group := range groups {
		if _, ok := index[group.Kind]; ok {
			continue
		}
		index[group.Kind] = group
	}
	out := make(RefCandidateSet, 0, len(refKindOrder))
	for _, kind := range refKindOrder {
		group, ok := index[kind]
		if !ok {
			out = append(out, NewUnavailableRefCandidateGroup(kind))
			continue
		}
		if group.Candidates == nil {
			group.Candidates = []RefCandidate{}
		}
		group.Kind = kind
		out = append(out, group)
	}
	return out
}

// normalizeRefCandidateFlags 去重并只保留已知标记。
//
// 未知标记丢弃而不是透传：它是一个 enum 契约，透传会让前端按未知后缀渲染，
// 运营看到的是「（已）」这类半截文案。
func normalizeRefCandidateFlags(flags []string) []string {
	if len(flags) == 0 {
		return nil
	}
	out := make([]string, 0, len(flags))
	seen := make(map[string]struct{}, len(flags))
	for _, flag := range flags {
		switch flag {
		case RefFlagHidden, RefFlagDisabled:
		default:
			continue
		}
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		out = append(out, flag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
