package badges

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
)

// refCandidateStoreFake 在 fakeStore 之上只加一个读口，用来验「有实现 / 没实现」
// 两条分支，同时不逼 `Store` 的所有 fake 都实现候选查询（那正是窄接口的理由）。
type refCandidateStoreFake struct {
	*fakeStore
	set conditions.RefCandidateSet
	err error
}

func (f refCandidateStoreFake) RefCandidates(context.Context) (conditions.RefCandidateSet, error) {
	return f.set, f.err
}

func refCandidateKinds(t *testing.T, set conditions.RefCandidateSet) []string {
	t.Helper()
	kinds := make([]string, 0, len(set))
	for _, group := range set {
		kinds = append(kinds, group.Kind)
	}
	return kinds
}

// TestBadgeConditionsRefCandidatesUnavailableWithoutStorePort 断言：store 不实现
// `RefCandidateStore` 时五项全 unavailable，**且不返回错误**。
//
// 这是本刀的核心失败路径——候选取不到只该让编辑器退回文本输入，绝不能让
// `GET /admin/badges/conditions` 失败（那会把整个规则编辑器一起打死）。
func TestBadgeConditionsRefCandidatesUnavailableWithoutStorePort(t *testing.T) {
	admin := NewAdminService(newFakeStore())
	catalog, err := admin.BadgeConditions(context.Background(), viewActor())
	if err != nil {
		t.Fatalf("候选不可用不该让条件目录失败：%v", err)
	}
	if got := refCandidateKinds(t, catalog.RefCandidates); len(got) != 5 {
		t.Fatalf("必须恒下发 5 个 kind，得到 %v", got)
	}
	want := conditions.RefKinds()
	for index, group := range catalog.RefCandidates {
		if group.Kind != want[index] {
			t.Fatalf("第 %d 项应为 %s，得到 %s", index, want[index], group.Kind)
		}
		if !group.Unavailable {
			t.Fatalf("%s 应 unavailable：%+v", group.Kind, group)
		}
	}
}

// TestBadgeConditionsRefCandidatesKeepsPartialResultsOnError 断言：store 回传
// 「部分成功 + 第一条错误」时，领域层保留返回值、只记 warn。
//
// 整体丢弃会把一次局部失败（比如 tags 查询挂了）放大成五项全空，运营连本来能选的
// 分类都选不了。
func TestBadgeConditionsRefCandidatesKeepsPartialResultsOnError(t *testing.T) {
	partial := conditions.NormalizeRefCandidateSet([]conditions.RefCandidateGroup{
		conditions.NewRefCandidateGroup(conditions.RefKindCategory, []conditions.RefCandidate{
			{Value: "news", Label: "News"},
		}),
		conditions.NewUnavailableRefCandidateGroup(conditions.RefKindTag),
	})
	store := refCandidateStoreFake{
		fakeStore: newFakeStore(),
		set:       partial,
		err:       errors.New("load badge ref candidates (tag): boom"),
	}
	admin := NewAdminService(store)
	catalog, err := admin.BadgeConditions(context.Background(), viewActor())
	if err != nil {
		t.Fatalf("部分失败不该让条件目录失败：%v", err)
	}
	if len(catalog.RefCandidates) != 5 {
		t.Fatalf("必须恒下发 5 个 kind，得到 %d", len(catalog.RefCandidates))
	}
	category := catalog.RefCandidates[1]
	if category.Kind != conditions.RefKindCategory || category.Unavailable || len(category.Candidates) != 1 {
		t.Fatalf("成功的类必须保留候选，得到 %+v", category)
	}
	if category.Candidates[0].Value != "news" || category.Candidates[0].Label != "News" {
		t.Fatalf("候选值级内容不对：%+v", category.Candidates[0])
	}
	if !catalog.RefCandidates[2].Unavailable {
		t.Fatalf("失败的类必须标 unavailable，得到 %+v", catalog.RefCandidates[2])
	}
}
