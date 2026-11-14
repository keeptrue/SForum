package events_test

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// TestTrustLevelChangedDefinitionMatchesPublisherPayload 钉住事件载荷契约。
//
// 生产者（trustlevels.Recomputer）手写 payload map，订阅方按 definition 的
// PayloadFields 读。两处名字一旦漂移，订阅方拿到的是「字段缺失」而不是编译错误——
// 所以这里把定义侧钉死，另一侧由
// TrustLevels 的 recompute 集成测试逐键对照（见
// app/Models/TrustLevels/recompute_postgres_integration_test.go）。
func TestTrustLevelChangedDefinitionMatchesPublisherPayload(t *testing.T) {
	definition, ok := events.FindDefinition(events.TrustLevelChanged)
	if !ok {
		t.Fatalf("%s 没有进目录：插件订阅不到等级变更", events.TrustLevelChanged)
	}
	if definition.Name != "trust_level.changed" {
		t.Fatalf("name = %q", definition.Name)
	}
	if definition.Kind != events.KindObserve {
		t.Fatalf("kind = %q，等级变更必须 observe（异步、不阻断写路径）", definition.Kind)
	}
	if definition.FailurePolicy != events.FailurePolicyFailOpen {
		t.Fatalf("failure policy = %q，observe 必须 fail_open", definition.FailurePolicy)
	}
	want := []string{"userId", "fromLevel", "toLevel", "fromTierId", "toTierId", "computedAt"}
	if len(definition.PayloadFields) != len(want) {
		t.Fatalf("payload fields = %v", definition.PayloadFields)
	}
	for index, field := range want {
		if definition.PayloadFields[index] != field {
			t.Fatalf("payload fields = %v, want %v", definition.PayloadFields, want)
		}
	}
}
