package badges

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件锁定「公开目录不因名字解析退化成 N+1」这条性能口径。
//
// 判据不是墙钟时间（那受 CI 机器影响），而是**调用形状**：
// 一次目录扫描只能触发**一次** loader 调用，且一次调用里每类引用只出现一次。
// 墙钟只用 benchmark 记录，作为汇报数字，不作为门禁。

type countingLoader struct {
	calls int
	refs  []RefSet
	err   error
	names conditions.RefNames
}

func (l *countingLoader) LoadRefNames(_ context.Context, refs RefSet) (conditions.RefNames, error) {
	l.calls++
	l.refs = append(l.refs, refs)
	return l.names, l.err
}

func catalogRules(count int) []Badge {
	items := make([]Badge, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, Badge{
			Slug: "badge-" + string(rune('a'+index%26)),
			Name: "徽章",
			Rule: &ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{
				{Type: conditions.KeyObjectAuthoring, Params: map[string]any{
					"targetKind": "category", "targetSlug": "general",
					"window": map[string]any{"kind": "all"},
				}, Cmp: &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 3}},
				{Type: conditions.KeyIdentityRole, Params: map[string]any{"roleKey": "moderator"}},
				{Type: conditions.KeyBadgeOwned, Params: map[string]any{"slug": "veteran"}},
			}},
		})
	}
	return items
}

// 500 枚徽章 → 一次 loader 调用，且同类引用已去重。
func TestResolveRefNamesIsBatchedAndDeduped(t *testing.T) {
	loader := &countingLoader{names: conditions.NewRefNames()}
	items := catalogRules(500)

	names := BadgeRefNames(context.Background(), loader, items)
	if loader.calls != 1 {
		t.Fatalf("名字解析必须批量：loader 调用 %d 次", loader.calls)
	}
	refs := loader.refs[0]
	if len(refs.Categories) != 1 {
		t.Fatalf("同一分类被 500 条规则引用，应为 1 个去重后的 slug，得到 %d", len(refs.Categories))
	}
	if len(refs.Roles) != 1 {
		t.Fatalf("角色引用未去重：%d", len(refs.Roles))
	}
	// `badge.owned` 的目标不在本批 seed 里（seed 只有 badge-a..z），所以会被问一次。
	if len(refs.Badges) != 1 {
		t.Fatalf("徽章引用未去重：%d", len(refs.Badges))
	}
	if names.Category("general").Zh == "" {
		t.Fatal("解析后的名字不应为空（有泛称回落）")
	}
}

// seed 里已有的徽章名不再回表（公开目录「同一次扫描」复用）。
func TestResolveRefNamesSkipsSeededBadges(t *testing.T) {
	rule := &ruleengine.Node{Type: conditions.KeyBadgeOwned, Params: map[string]any{"slug": "veteran"}}
	loader := &countingLoader{}
	seed := conditions.NewRefNames()
	seed.AddBadge("veteran", "老用户")

	names := ResolveRefNames(context.Background(), loader, CollectRefs(rule), seed)
	if loader.calls != 0 {
		t.Fatalf("已解析的徽章名不应再查询，loader 调用 %d 次", loader.calls)
	}
	if got := names.Badge("veteran").Zh; got != "老用户" {
		t.Fatalf("seed 名字应直接可用，得到 %q", got)
	}
}

// loader 失败：fail-soft —— 用 seed 渲染，公开面不报错、不空白。
func TestResolveRefNamesFailsSoft(t *testing.T) {
	loader := &countingLoader{err: errors.New("categories table down")}
	items := catalogRules(3)

	names := BadgeRefNames(context.Background(), loader, items)
	if names.Category("general").Zh == "" {
		t.Fatal("解析失败也必须回落泛称，不得为空")
	}
	text := BuildRequirementText(items[0], names)
	if text.Zh == "" || text.En == "" {
		t.Fatalf("解析失败不得让公开说明变空：%+v", text)
	}
}

// BenchmarkRenderPublicCatalogText 记录「500 枚徽章一次扫描」的渲染开销（无 DB）。
//
// 两次批量解析（seed 收集 + 一次 loader 调用）都算在内；真正的 SQL 由
// `LoadRefNames` 每类一条 `= ANY($1)`，与该数量无关。
func BenchmarkRenderPublicCatalogText(b *testing.B) {
	loader := &countingLoader{names: conditions.NewRefNames()}
	items := catalogRules(500)
	nameSet := BadgeRefNames(context.Background(), loader, items)

	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		for itemIndex := range items {
			_ = items[itemIndex].Public(nameSet)
		}
	}
}

// 登录方式真名：注入前回落泛称、注入后出真名（证明 `SetProviderNames` 接线生效）。
//
// 用一个没有 pool 的 store：登录方式名来自 registry 快照（不查库），所以这条测试
// 不需要数据库也能验证「句子真的变了」。
func TestProviderNameInjectionChangesSentence(t *testing.T) {
	rule := &ruleengine.Node{
		Type:   conditions.KeyIdentityOAuthLink,
		Params: map[string]any{"providerId": "github"},
	}
	refs := CollectRefs(rule)

	before := BuildRuleRequirementText(rule, conditions.NewRefNames())
	if !strings.Contains(before.Zh, "指定登录方式") || !strings.Contains(before.En, "the selected sign-in method") {
		t.Fatalf("未注入时应回落泛称，得到 %+v", before)
	}

	store := &PostgresStore{}
	if names, err := store.LoadRefNames(context.Background(), refs); err != nil {
		t.Fatalf("LoadRefNames without injection: %v", err)
	} else if got := BuildRuleRequirementText(rule, names); got != before {
		t.Fatalf("未注入时不应改变句子：%+v vs %+v", got, before)
	}

	store.SetProviderNames(func() map[string][2]string {
		return map[string][2]string{"github": {"GitHub", "GitHub"}}
	})
	names, err := store.LoadRefNames(context.Background(), refs)
	if err != nil {
		t.Fatalf("LoadRefNames: %v", err)
	}
	after := BuildRuleRequirementText(rule, names)
	if after == before {
		t.Fatalf("注入真名后句子必须变化，仍是 %+v", after)
	}
	if !strings.Contains(after.Zh, "GitHub") || !strings.Contains(after.En, "GitHub") {
		t.Fatalf("注入后应出现真名，得到 %+v", after)
	}
}
