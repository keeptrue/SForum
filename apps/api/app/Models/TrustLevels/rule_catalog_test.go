package trustlevels_test

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉住「发布面按候选过滤」的三个分支（A2-2）。
//
// 三条分支的差别只在**候选的可用性**上，而它们的后果完全不同：
//   - 读到为空 → 不发布（否则编辑器给一个空下拉，运营点进去是死路）；
//   - 取不到（unavailable）→ 照常发布（编辑器退回文本输入，条件仍可配）；
//   - 缺这类候选（调用方没查）→ 照常发布（无法判定，不能凭空砍功能）。

// catalogKeys 返回发布集合里的条件 key。
func catalogKeys(definitions []conditions.Definition) []string {
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		keys = append(keys, definition.Key)
	}
	return keys
}

func containsKey(keys []string, target string) bool {
	for _, key := range keys {
		if key == target {
			return true
		}
	}
	return false
}

// emptyGroupSet 造一份「五类齐全，但每一类都读到空」的候选集合。
func emptyGroupSet() conditions.RefCandidateSet {
	groups := make([]conditions.RefCandidateGroup, 0, 5)
	for _, kind := range conditions.RefKinds() {
		groups = append(groups, conditions.NewRefCandidateGroup(kind, nil))
	}
	return conditions.NormalizeRefCandidateSet(groups)
}

// TestPublishableRuleConditionsRefKindBranches 是三张表对应三条分支的判据。
func TestPublishableRuleConditionsRefKindBranches(t *testing.T) {
	subset := len(trustlevels.RuleConditions())

	t.Run("读到为空 → 用到该 refKind 的条件不发布", func(t *testing.T) {
		keys := catalogKeys(trustlevels.PublishableRuleConditions(nil, emptyGroupSet()))
		// 五类全空：badge.owned / object.authoring(category) / identity.role /
		// identity.oauth_link(provider) 都不该发布；tag 没有条件用到。
		for _, dropped := range []string{"badge.owned", "object.authoring", "identity.role", "identity.oauth_link"} {
			if containsKey(keys, dropped) {
				t.Fatalf("%q 的引用候选读到为空，却仍被发布（编辑器会给空下拉）", dropped)
			}
		}
		if containsKey(keys, "fact.count") {
			t.Fatal("没有事实候选时不该发布 fact.count")
		}
		// 剩下的是不依赖任何候选的条件：7 条。
		if len(keys) != 7 {
			t.Fatalf("应剩 7 条无候选依赖的条件，得到 %d：%v", len(keys), keys)
		}
		// 反向：校验面完全不受影响。
		if len(trustlevels.RuleConditions()) != subset {
			t.Fatalf("校验子集被过滤污染：%d ≠ %d", len(trustlevels.RuleConditions()), subset)
		}
		for _, key := range []string{"badge.owned", "object.authoring", "identity.role", "identity.oauth_link", "fact.count"} {
			if !trustlevels.RuleConditionAllowed(key) {
				t.Fatalf("%q 必须仍在校验白名单里（否则存量规则读不出来）", key)
			}
		}
	})

	t.Run("取不到候选（unavailable）→ 照常发布", func(t *testing.T) {
		keys := catalogKeys(trustlevels.PublishableRuleConditions(nil, conditions.UnavailableRefCandidateSet()))
		for _, kept := range []string{"badge.owned", "object.authoring", "identity.role", "identity.oauth_link"} {
			if !containsKey(keys, kept) {
				t.Fatalf("%q 在候选取不到时必须照常发布（编辑器会退回文本输入）", kept)
			}
		}
		// 事实候选是**另一个来源**（插件清单），它为空仍然要挡住 fact.count。
		if containsKey(keys, "fact.count") {
			t.Fatal("没有事实候选时不该发布 fact.count")
		}
		if len(keys) != subset-1 {
			t.Fatalf("应发布 %d 条（子集 − fact.count），得到 %d", subset-1, len(keys))
		}
	})

	t.Run("缺这类候选（调用方没查）→ 照常发布", func(t *testing.T) {
		// 只给 role 一类，且它读到为空；其它四类完全缺席。
		partial := conditions.NormalizeRefCandidateSet([]conditions.RefCandidateGroup{
			{Kind: conditions.RefKindRole, Candidates: []conditions.RefCandidate{}, Unavailable: false, Truncated: false},
		})
		keys := catalogKeys(trustlevels.PublishableRuleConditions(nil, partial))
		if containsKey(keys, "identity.role") {
			t.Fatal("role 候选读到为空，identity.role 不该发布")
		}
		for _, kept := range []string{"badge.owned", "object.authoring", "identity.oauth_link"} {
			if !containsKey(keys, kept) {
				t.Fatalf("%q 的候选类型缺席（无法判定），不该被砍掉", kept)
			}
		}
	})

	t.Run("有事实候选 → fact.count 发布且 options 已填充", func(t *testing.T) {
		candidates := []conditions.FactCandidate{{Key: "acme.wiki.pages"}}
		definitions := trustlevels.PublishableRuleConditions(candidates, conditions.UnavailableRefCandidateSet())
		var factKeys []string
		for _, definition := range definitions {
			if definition.Key != "fact.count" {
				continue
			}
			for _, spec := range definition.Params.Specs {
				if spec.Name == "factKey" {
					factKeys = spec.Options
				}
			}
		}
		if len(factKeys) != 1 || factKeys[0] != "acme.wiki.pages" {
			t.Fatalf("factKey 候选 = %v（只发布条件不填 options = 仍然是空下拉）", factKeys)
		}
		// 只读单例不得被写脏：再取一次 Core 的定义，options 必须还是 nil（开放集）。
		definition, ok := conditions.Core().Definition("fact.count")
		if !ok {
			t.Fatal("fact.count 不在共享目录里")
		}
		for _, spec := range definition.Params.Specs {
			if spec.Name == "factKey" && spec.Options != nil {
				t.Fatalf("共享目录单例被污染：factKey.Options = %v", spec.Options)
			}
		}
	})
}
