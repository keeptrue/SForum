package badgescontroller

import (
	"encoding/json"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
)

// TestRefCandidateGroupWireShape 钉住 `refCandidates[]` 的键集合与值级内容。
//
// 契约漂移的典型形态是「多一个键」或「漏赋值变成缺键」（计划 §13#20：键存在
// 不等于值正确），所以两组断言都要有：键集合精确相等 + 值级比对。
func TestRefCandidateGroupWireShape(t *testing.T) {
	set := conditions.NormalizeRefCandidateSet([]conditions.RefCandidateGroup{
		conditions.NewRefCandidateGroup(conditions.RefKindCategory, []conditions.RefCandidate{
			{Value: "news", Label: "News"},
			{Value: "archive", Label: "Archive", Flags: []string{conditions.RefFlagHidden}},
		}),
	})
	raw, err := json.Marshal(toRefCandidateSetDTO(set))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire) != 5 {
		t.Fatalf("refCandidates 必须恒为 5 项，得到 %d", len(wire))
	}
	for _, group := range wire {
		assertKeys(t, "ref candidate group", group, []string{"kind", "candidates", "unavailable", "truncated"})
	}

	category := wire[1]
	if category["kind"] != "category" {
		t.Fatalf("第 2 项必须是 category（固定顺序 badge→category→tag→role→provider），得到 %v", category["kind"])
	}
	if category["unavailable"] != false || category["truncated"] != false {
		t.Fatalf("成功取到候选时应是 false/false，得到 %v/%v", category["unavailable"], category["truncated"])
	}
	candidates, ok := category["candidates"].([]any)
	if !ok || len(candidates) != 2 {
		t.Fatalf("category.candidates 应是 2 项数组，得到 %v", category["candidates"])
	}
	// 值级断言：展示名与值都要真的写进 wire（同 §13#20 的假绿形态）。
	// 排序按展示名：Archive < News，所以 [0] 是隐藏项。
	first, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("候选元素形状不对：%v", candidates[0])
	}
	assertKeys(t, "ref candidate", first, []string{"value", "label", "flags"})
	if first["value"] != "archive" || first["label"] != "Archive" {
		t.Fatalf("候选值级内容不对：%v", first)
	}
	flags, ok := first["flags"].([]any)
	if !ok || len(flags) != 1 || flags[0] != "hidden" {
		t.Fatalf("隐藏对象的 flags 应为 [\"hidden\"]，得到 %v", first["flags"])
	}
}

// TestRefCandidateEmptyCollectionsAreArrays 断言「没有候选 / 取不到候选」两种情形
// 都不会把数组渲染成 null、也不会带 omitempty 缺键。
func TestRefCandidateEmptyCollectionsAreArrays(t *testing.T) {
	raw, err := json.Marshal(toRefCandidateSetDTO(conditions.UnavailableRefCandidateSet()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire []map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire) != 5 {
		t.Fatalf("兜底集合必须仍下发 5 项，得到 %d", len(wire))
	}
	for _, group := range wire {
		candidates, ok := group["candidates"].([]any)
		if !ok || len(candidates) != 0 {
			t.Fatalf("%v 的 candidates 必须是 []，得到 %v", group["kind"], group["candidates"])
		}
		if group["unavailable"] != true {
			t.Fatalf("%v 应 unavailable=true，得到 %v", group["kind"], group["unavailable"])
		}
	}

	// 单条候选没有 flags 时也必须是 []。
	rawCandidate, err := json.Marshal(toRefCandidateDTO(conditions.RefCandidate{Value: "v", Label: "L"}))
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	var candidate map[string]any
	if err := json.Unmarshal(rawCandidate, &candidate); err != nil {
		t.Fatalf("unmarshal candidate: %v", err)
	}
	flags, ok := candidate["flags"].([]any)
	if !ok || len(flags) != 0 {
		t.Fatalf("无标记的候选 flags 必须是 []，得到 %v", candidate["flags"])
	}
}
