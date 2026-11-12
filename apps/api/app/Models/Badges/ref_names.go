package badges

import (
	"context"
	"log/slog"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件是「规则里引用了谁」的收集与名字解析入口。
//
// 两段式是刻意的（也是公开目录性能的全部依据）：
//  1. `CollectRefs` 扫一遍规则树，得到一个去重的 `RefSet`（纯内存）；
//  2. `ResolveRefNames` 用**一次** loader 调用把每一类引用批量查出来；
//  3. 渲染时才把真名嵌进句子（`renderRule`）。
//
// 一段式（扫描时逐条渲染）会让 500 枚徽章的目录变成几百次单行查询——那正是
// 「加名字解析之后公开目录变慢」的典型事故形状。

// FactRef 指向一个插件事实（可选变体）。
type FactRef struct {
	Key     string
	Variant string
}

// RefSet 是一条规则树里出现过的引用集合（去重）。
//
// 用 map 而不是 slice：同一枚分类被 20 条规则引用时，SQL 只该收到一个元素。
type RefSet struct {
	Categories map[string]struct{}
	Tags       map[string]struct{}
	Roles      map[string]struct{}
	Badges     map[string]struct{}
	Providers  map[string]struct{}
	Topics     map[int64]struct{}
	Facts      map[FactRef]struct{}
}

// NewRefSet 返回可写的空集合。
func NewRefSet() RefSet {
	return RefSet{
		Categories: map[string]struct{}{},
		Tags:       map[string]struct{}{},
		Roles:      map[string]struct{}{},
		Badges:     map[string]struct{}{},
		Providers:  map[string]struct{}{},
		Topics:     map[int64]struct{}{},
		Facts:      map[FactRef]struct{}{},
	}
}

func (r *RefSet) ensure() {
	if r.Categories == nil {
		*r = NewRefSet()
	}
}

func (r *RefSet) addString(target map[string]struct{}, value string) {
	if value == "" {
		return
	}
	target[value] = struct{}{}
}

// CollectRefs 扫一遍规则树，收集全部引用。
//
// 引用语义必须与求值一致（`rule_store.go` 的保存期校验是同一口径）：
// `object.authoring` 的 `targetSlug` 到底是分类还是标签由 `targetKind` 决定，
// 只看 RefKind 会把它标成分类，于是标签名永远解析不到（回落泛称）。
func CollectRefs(rules ...*ruleengine.Node) RefSet {
	refs := NewRefSet()
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		for _, leaf := range collectRuleLeaves(*rule) {
			switch leaf.Type {
			case conditions.KeyObjectEngagement:
				if topicID, ok := intParamOf(leaf, "topicId"); ok && topicID > 0 {
					refs.Topics[topicID] = struct{}{}
				}
			case conditions.KeyObjectAuthoring:
				slug := stringParamOf(leaf, "targetSlug")
				if stringParamOf(leaf, "targetKind") == conditions.TargetKindTag {
					refs.addString(refs.Tags, slug)
				} else {
					refs.addString(refs.Categories, slug)
				}
			case conditions.KeyIdentityRole:
				refs.addString(refs.Roles, stringParamOf(leaf, "roleKey"))
			case conditions.KeyIdentityOAuthLink:
				refs.addString(refs.Providers, stringParamOf(leaf, "providerId"))
			case conditions.KeyBadgeOwned:
				refs.addString(refs.Badges, stringParamOf(leaf, "slug"))
			case conditions.KeyFactCount:
				fact := FactRef{Key: stringParamOf(leaf, "factKey"), Variant: stringParamOf(leaf, "variant")}
				if fact.Key != "" {
					refs.Facts[fact] = struct{}{}
				}
			}
		}
	}
	return refs
}

// Empty 报告集合里没有任何引用（此时不需要 loader）。
func (r RefSet) Empty() bool {
	return len(r.Categories) == 0 && len(r.Tags) == 0 && len(r.Roles) == 0 &&
		len(r.Badges) == 0 && len(r.Providers) == 0 && len(r.Topics) == 0 && len(r.Facts) == 0
}

// WithoutResolvedBadges 返回去掉「名字已知的徽章」后的集合。
//
// 公开目录那一次扫描已经拿到了整页徽章的 slug/name，再为它们查一遍 badges 表
// 是纯浪费；`badge.owned` 因此零额外查询（未启用的目标徽章仍会被查到）。
func (r RefSet) WithoutResolvedBadges(known conditions.RefNames) RefSet {
	if len(r.Badges) == 0 {
		return r
	}
	out := r
	out.Badges = make(map[string]struct{}, len(r.Badges))
	for slug := range r.Badges {
		if known.HasBadge(slug) {
			continue
		}
		out.Badges[slug] = struct{}{}
	}
	return out
}

// RefNameLoader 是「一次把一批引用解析成真名」的窄接口。
//
// 单独一个接口而不是加到 `Store`：假 store（单测）不该被逼实现一个它不关心的
// 能力；未实现即视为「没有真名」，公开面按泛称渲染（fail-soft），不会报错。
type RefNameLoader interface {
	LoadRefNames(ctx context.Context, refs RefSet) (conditions.RefNames, error)
}

// RefNameLoaderOf 从任意 store 取出 loader（不存在时返回 nil）。
func RefNameLoaderOf(store any) RefNameLoader {
	if loader, ok := store.(RefNameLoader); ok {
		return loader
	}
	return nil
}

// ResolveRefNames 一次批量解析引用名；失败按泛称回落（fail-soft）并记一次 warn。
//
// fail-soft 是硬要求：名称解析只是「把句子写得更具体」，它挂了不该让公开页 500，
// 也不该让句子变空（`seed` 至少含徽章自身名字）。
func ResolveRefNames(ctx context.Context, loader RefNameLoader, refs RefSet, seed conditions.RefNames) conditions.RefNames {
	refs = refs.WithoutResolvedBadges(seed)
	if loader == nil || refs.Empty() {
		return seed
	}
	names, err := loader.LoadRefNames(ctx, refs)
	if err != nil {
		// 降级必须可观测：否则「分类名变成『指定分类』」永远查不到原因。
		slog.Default().Warn("badge requirement text: resolve reference names failed", "error", err)
	}
	return seed.Merge(names)
}

// BadgeRefNames 给一批 admin/公开徽章一次性解析引用名。
//
// seed 里先放进这批徽章自己的 slug/name：`badge.owned` 引用同一批里的徽章时
// 不必回表（公开目录与后台列表都是整页扫描，天然自足）。
func BadgeRefNames(ctx context.Context, loader RefNameLoader, items []Badge) conditions.RefNames {
	seed := conditions.NewRefNames()
	rules := make([]*ruleengine.Node, 0, len(items))
	for _, item := range items {
		seed.AddBadge(item.Slug, item.Name)
		rules = append(rules, item.Rule)
	}
	return ResolveRefNames(ctx, loader, CollectRefs(rules...), seed)
}
