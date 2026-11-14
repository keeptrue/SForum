package conditions

import (
	"fmt"
	"testing"
)

// TestRefKindsAreFixedOrder 钉住「五个 kind、固定顺序」这条契约。
//
// 前端按这个顺序渲染分组，也是「补齐缺项」的依据；顺序一变就是 breaking change。
func TestRefKindsAreFixedOrder(t *testing.T) {
	want := []string{"badge", "category", "tag", "role", "provider"}
	got := RefKinds()
	if len(got) != len(want) {
		t.Fatalf("kind 数量应为 %d，得到 %d: %v", len(want), len(got), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("kind[%d] 应为 %q，得到 %q（完整顺序 %v）", index, want[index], got[index], got)
		}
	}
}

// TestUnavailableRefCandidateSetIsFiveEmptyGroups 断言兜底集合的形状：
// 五项、全 unavailable、候选是空切片而不是 nil。
func TestUnavailableRefCandidateSetIsFiveEmptyGroups(t *testing.T) {
	set := UnavailableRefCandidateSet()
	if len(set) != 5 {
		t.Fatalf("兜底集合应有 5 项，得到 %d", len(set))
	}
	for index, group := range set {
		if group.Kind != RefKinds()[index] {
			t.Fatalf("第 %d 项 kind 应为 %q，得到 %q", index, RefKinds()[index], group.Kind)
		}
		if !group.Unavailable {
			t.Fatalf("%s 应标记为 unavailable（取不到候选 ≠ 没有候选）", group.Kind)
		}
		if group.Truncated {
			t.Fatalf("%s 不该标记 truncated", group.Kind)
		}
		if group.Candidates == nil {
			t.Fatalf("%s 的 Candidates 必须是空切片而不是 nil（JSON 会变 null）", group.Kind)
		}
	}
}

// TestNewRefCandidateGroupNormalizes 覆盖排序 / 去重 / 丢弃脏行与未知标记。
func TestNewRefCandidateGroupNormalizes(t *testing.T) {
	group := NewRefCandidateGroup(RefKindCategory, []RefCandidate{
		{Value: "b", Label: "Bravo"},
		{Value: "a", Label: "alpha"},
		{Value: "b", Label: "Bravo duplicated"},
		{Value: "  ", Label: "empty value"},
		{Value: "c", Label: "  Charlie  ", Flags: []string{RefFlagHidden, "unknown", RefFlagHidden}},
	})
	if group.Unavailable || group.Truncated {
		t.Fatalf("正常用例不该 unavailable/truncated：%+v", group)
	}
	if len(group.Candidates) != 3 {
		t.Fatalf("去重 + 丢弃空值后应为 3 项，得到 %d: %+v", len(group.Candidates), group.Candidates)
	}
	// 大小写不敏感的展示名排序：alpha < Bravo < Charlie。
	wantOrder := []string{"a", "b", "c"}
	for index, value := range wantOrder {
		if group.Candidates[index].Value != value {
			t.Fatalf("排序后第 %d 项应为 %q，得到 %q（完整 %+v）", index, value, group.Candidates[index].Value, group.Candidates)
		}
	}
	if label := group.Candidates[2].Label; label != "Charlie" {
		t.Fatalf("展示名应去掉首尾空白，得到 %q", label)
	}
	// 重复值保留第一条。
	if label := group.Candidates[1].Label; label != "Bravo" {
		t.Fatalf("重复值应保留第一条，得到 %q", label)
	}
	flags := group.Candidates[2].Flags
	if len(flags) != 1 || flags[0] != RefFlagHidden {
		t.Fatalf("未知标记应被丢弃、已知标记去重，得到 %v", flags)
	}
	if group.Candidates[0].Flags != nil {
		t.Fatalf("没有标记时 Flags 应为 nil（DTO 会渲染成 []），得到 %v", group.Candidates[0].Flags)
	}
}

// TestNewRefCandidateGroupTruncates 断言截断阈值与标记：上限 500，多取的一行只
// 用来判断「还有更多」，不进返回集合。
func TestNewRefCandidateGroupTruncates(t *testing.T) {
	rows := make([]RefCandidate, 0, RefCandidatesLimit+1)
	for index := 0; index <= RefCandidatesLimit; index++ {
		rows = append(rows, RefCandidate{
			// 值补零保证字典序与插入顺序一致，这样能断言「被截掉的是最后一条」。
			Value: fmt.Sprintf("v%04d", index),
			Label: fmt.Sprintf("n%04d", index),
		})
	}
	group := NewRefCandidateGroup(RefKindBadge, rows)
	if !group.Truncated {
		t.Fatalf("%d 条候选必须标记 truncated", len(rows))
	}
	if len(group.Candidates) != RefCandidatesLimit {
		t.Fatalf("截断后应为 %d 条，得到 %d", RefCandidatesLimit, len(group.Candidates))
	}
	if last := group.Candidates[len(group.Candidates)-1].Value; last != fmt.Sprintf("v%04d", RefCandidatesLimit-1) {
		t.Fatalf("被截掉的应是最后一条 v%04d，实际末尾是 %q", RefCandidatesLimit, last)
	}

	// 恰好 500 条不算截断。
	exact := NewRefCandidateGroup(RefKindBadge, rows[:RefCandidatesLimit])
	if exact.Truncated {
		t.Fatalf("恰好 %d 条不该标记 truncated", RefCandidatesLimit)
	}
	if len(exact.Candidates) != RefCandidatesLimit {
		t.Fatalf("恰好 %d 条应全部保留，得到 %d", RefCandidatesLimit, len(exact.Candidates))
	}
}

// TestNormalizeRefCandidateSetFillsMissingKinds 断言缺项补 unavailable、未知 kind 丢弃。
//
// 「缺项」不能补成空候选：调用方没提供证据时，编辑器应退回文本输入，而不是宣称
// 「这里确实没有可选项」——后者会让运营以为系统里真的没有分类。
func TestNormalizeRefCandidateSetFillsMissingKinds(t *testing.T) {
	set := NormalizeRefCandidateSet([]RefCandidateGroup{
		NewRefCandidateGroup(RefKindTag, []RefCandidate{{Value: "go", Label: "Go"}}),
		NewRefCandidateGroup("unknown-kind", []RefCandidate{{Value: "x", Label: "X"}}),
		{Kind: RefKindRole, Candidates: nil},
	})
	if len(set) != 5 {
		t.Fatalf("规范化后应恒为 5 项，得到 %d", len(set))
	}
	for index, group := range set {
		if group.Kind != RefKinds()[index] {
			t.Fatalf("第 %d 项 kind 应为 %q，得到 %q", index, RefKinds()[index], group.Kind)
		}
		if group.Candidates == nil {
			t.Fatalf("%s 的 Candidates 必须是空切片而不是 nil", group.Kind)
		}
	}
	tag := set[2]
	if tag.Unavailable || len(tag.Candidates) != 1 || tag.Candidates[0].Value != "go" {
		t.Fatalf("tag 组应保留唯一候选，得到 %+v", tag)
	}
	// 显式给了 role 组但没有候选 = 「能取到、确实为空」，不是 unavailable。
	if set[3].Unavailable {
		t.Fatalf("给了 role 组（候选为空）时不该被改写成 unavailable")
	}
	// 未出现的 badge / category / provider 都是 unavailable。
	for _, index := range []int{0, 1, 4} {
		if !set[index].Unavailable {
			t.Fatalf("%s 缺项应补 unavailable", set[index].Kind)
		}
	}
}
