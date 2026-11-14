package conditions

import (
	"strconv"
	"strings"
)

// 本文件是「公开说明」里被引用对象的展示名。
//
// 为什么放在 `conditions` 而不是 `badges`：每个条件的 `Phrase` 都要用到名字，
// 而 `conditions` 刻意不 import `badges`（依赖方向单向，见 registry.go 文件头）。
// 把名字访问器放在条件目录这一侧，句子生成就不必反向依赖领域存储。
//
// 名字是**单串 + 泛称回落**，不是「真名或空串」：公开面永远不能因为运营删了一个
// 分类而变成空白或半句话（那正是手填时代的报障来源）。

// RefName 是一个被引用对象的双语展示名。
//
// 分类/标签/角色/徽章/主题的名字在库里只有**一个**串（不做双语），所以命中时
// Zh 与 En 同值；泛称回落才分语言（「指定分类」/「the selected category」）。
type RefName struct {
	Zh string
	En string
}

// Empty 报告名字是否为空（未命中）。
func (r RefName) Empty() bool { return strings.TrimSpace(r.Zh) == "" && strings.TrimSpace(r.En) == "" }

// 泛称回落。它们会直接印在公开面上，所以必须是「运营口吻的完整名词」，
// 且**不得**包含条件 key、参数名或任何 AST 词汇。
var (
	fallbackCategory = RefName{Zh: "指定分类", En: "the selected category"}
	fallbackTag      = RefName{Zh: "指定标签", En: "the selected tag"}
	fallbackRole     = RefName{Zh: "指定角色", En: "the selected role"}
	fallbackBadge    = RefName{Zh: "指定徽章", En: "the selected badge"}
	// fallbackProvider 在装配未注入 provider 真名时使用（见 `SetProviderNames`）：
	// 宁可写泛称，也不要让公开面出现 `github` 这类 provider id。
	fallbackProvider = RefName{Zh: "指定登录方式", En: "the selected sign-in method"}
	fallbackTopic    = RefName{Zh: "指定主题", En: "the selected topic"}
	// fallbackFact 的英文不能再带 "total"：`fact.count` 的英文模板是
	// `"%s" total %s`，两边都写会得到 `"a plugin fact total" total ≥ 3`。
	fallbackFact = RefName{Zh: "插件事实计数", En: "a plugin fact"}
)

// MaxRefNameRunes 是嵌进句子的引用名长度上限（超出截断并加省略号）。
//
// 句子有硬长度上限（见 requirement_text_render.go），而主题标题可以很长。
// 在名字这一层截断，句子模板就不必为「名字过长」再写一套分支；
// 截断只影响展示，真实数据一个字都不动。
const MaxRefNameRunes = 24

// RefNames 是一批已解析的引用名。零值是「什么都没解析到」，因此所有访问器
// 都会回落到泛称——调用方不需要先判空，也就不会漏判。
type RefNames struct {
	categories map[string]RefName
	tags       map[string]RefName
	roles      map[string]RefName
	badges     map[string]RefName
	providers  map[string]RefName
	topics     map[string]RefName
	facts      map[string]RefName
	factVars   map[string]RefName
}

// NewRefNames 返回可写的空集合。
func NewRefNames() RefNames {
	return RefNames{
		categories: map[string]RefName{},
		tags:       map[string]RefName{},
		roles:      map[string]RefName{},
		badges:     map[string]RefName{},
		providers:  map[string]RefName{},
		topics:     map[string]RefName{},
		facts:      map[string]RefName{},
		factVars:   map[string]RefName{},
	}
}

func normalized(key string) string { return strings.TrimSpace(key) }

func displayName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= MaxRefNameRunes {
		return trimmed
	}
	return string(runes[:MaxRefNameRunes]) + "…"
}

func (n RefNames) put(target map[string]RefName, key, name string) {
	cleanKey, cleanName := normalized(key), displayName(name)
	if cleanKey == "" || cleanName == "" {
		return
	}
	target[cleanKey] = RefName{Zh: cleanName, En: cleanName}
}

// AddCategory / AddTag / AddRole / AddBadge / AddProvider 记录一个真名。
// 空 key 或空名字一律忽略（回落逻辑比「空名字」更诚实）。
func (n *RefNames) AddCategory(slug, name string) { n.ensure(); n.put(n.categories, slug, name) }

// AddTag 记录标签真名。
func (n *RefNames) AddTag(slug, name string) { n.ensure(); n.put(n.tags, slug, name) }

// AddRole 记录角色真名（roles.alias）。
func (n *RefNames) AddRole(key, name string) { n.ensure(); n.put(n.roles, key, name) }

// AddBadge 记录徽章真名。
func (n *RefNames) AddBadge(slug, name string) { n.ensure(); n.put(n.badges, slug, name) }

// AddProvider 记录第三方登录方式真名。
func (n *RefNames) AddProvider(id, name string) { n.ensure(); n.put(n.providers, id, name) }

// AddTopic 记录主题标题。
func (n *RefNames) AddTopic(id int64, title string) {
	if id <= 0 {
		return
	}
	n.ensure()
	n.put(n.topics, strconv.FormatInt(id, 10), title)
}

// AddFact 记录插件事实的真名。
func (n *RefNames) AddFact(key, zh, en string) {
	cleanKey := normalized(key)
	if cleanKey == "" || strings.TrimSpace(zh) == "" {
		return
	}
	n.ensure()
	n.facts[cleanKey] = RefName{Zh: displayName(zh), En: displayName(en)}
}

// AddFactVariant 记录插件事实变体的真名。
func (n *RefNames) AddFactVariant(key, variant, name string) {
	cleanKey, cleanVariant := normalized(key), normalized(variant)
	if cleanKey == "" || cleanVariant == "" || strings.TrimSpace(name) == "" {
		return
	}
	n.ensure()
	n.factVars[cleanKey+"\x00"+cleanVariant] = RefName{Zh: displayName(name), En: displayName(name)}
}

func (n *RefNames) ensure() {
	if n.categories == nil {
		*n = NewRefNames()
	}
}

func lookup(source map[string]RefName, key string, fallback RefName) RefName {
	if name, ok := source[normalized(key)]; ok && !name.Empty() {
		return name
	}
	return fallback
}

// Category 返回分类真名，未命中回落「指定分类」。
func (n RefNames) Category(slug string) RefName {
	return lookup(n.categories, slug, fallbackCategory)
}

// Tag 返回标签真名，未命中回落「指定标签」。
func (n RefNames) Tag(slug string) RefName { return lookup(n.tags, slug, fallbackTag) }

// Role 返回角色真名，未命中回落「指定角色」。
func (n RefNames) Role(key string) RefName { return lookup(n.roles, key, fallbackRole) }

// Badge 返回徽章真名，未命中回落「指定徽章」。
func (n RefNames) Badge(slug string) RefName { return lookup(n.badges, slug, fallbackBadge) }

// Provider 返回登录方式真名，未命中回落「指定登录方式」。
func (n RefNames) Provider(id string) RefName { return lookup(n.providers, id, fallbackProvider) }

// Topic 返回主题标题，未命中回落「指定主题」。
func (n RefNames) Topic(id int64) RefName {
	return lookup(n.topics, strconv.FormatInt(id, 10), fallbackTopic)
}

// Fact 返回插件事实真名（命中 variant 时附在括号里），未命中回落「插件事实计数」。
func (n RefNames) Fact(key, variant string) RefName {
	base := lookup(n.facts, key, fallbackFact)
	if base.Empty() || base == fallbackFact {
		return base
	}
	if label, ok := n.factVars[normalized(key)+"\x00"+normalized(variant)]; ok && !label.Empty() {
		return RefName{Zh: base.Zh + "（" + label.Zh + "）", En: base.En + " (" + label.En + ")"}
	}
	return base
}

// HasBadge 报告某个徽章名是否已解析（公开目录用它跳过重复的徽章名查询）。
func (n RefNames) HasBadge(slug string) bool {
	_, ok := n.badges[normalized(slug)]
	return ok
}

// Merge 返回「以 over 的非空项覆盖 base」的新集合。
//
// 只做浅合并、不修改任一入参：`RefNames` 可能在一次扫描里被多处共享，
// 原地写会让并发请求互相污染（与 `withFactOptions` 复制 specs 同一条理由）。
func (n RefNames) Merge(over RefNames) RefNames {
	out := NewRefNames()
	mergeInto := func(dst, src map[string]RefName) {
		for key, name := range src {
			if !name.Empty() {
				dst[key] = name
			}
		}
	}
	mergeInto(out.categories, n.categories)
	mergeInto(out.tags, n.tags)
	mergeInto(out.roles, n.roles)
	mergeInto(out.badges, n.badges)
	mergeInto(out.providers, n.providers)
	mergeInto(out.topics, n.topics)
	mergeInto(out.facts, n.facts)
	mergeInto(out.factVars, n.factVars)
	mergeInto(out.categories, over.categories)
	mergeInto(out.tags, over.tags)
	mergeInto(out.roles, over.roles)
	mergeInto(out.badges, over.badges)
	mergeInto(out.providers, over.providers)
	mergeInto(out.topics, over.topics)
	mergeInto(out.facts, over.facts)
	mergeInto(out.factVars, over.factVars)
	return out
}
