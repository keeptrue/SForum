package events_test

import (
	"testing"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// 声明侧（Manifest V3 knownOrNamespacedContract）与发布侧的尺子必须一致，
// 否则插件能声明一个事件却发不出去。
func TestResolveDefinitionAcceptsOwnNamespace(t *testing.T) {
	definition, ok := appevents.ResolveDefinition("fixture.events.order_ready", "fixture.events")
	if !ok {
		t.Fatal("own namespace event rejected")
	}
	if definition.Name != "fixture.events.order_ready" || definition.Kind != appevents.KindObserve {
		t.Fatalf("custom definition = %#v", definition)
	}
	// 自定义事件不应阻断业务写路径。
	if definition.FailurePolicy != appevents.FailurePolicyFailOpen {
		t.Fatalf("custom failure policy = %q", definition.FailurePolicy)
	}
	if definition.TimeoutMS <= 0 {
		t.Fatalf("custom timeout = %d", definition.TimeoutMS)
	}
}

func TestResolveDefinitionRejectsForeignNamespaces(t *testing.T) {
	tests := []struct {
		name        string
		extensionID string
		eventName   string
	}{
		{"other plugin", "fixture.events", "other.plugin.thing"},
		{"prefix without dot", "fixture.events", "fixture.eventsx.thing"},
		{"empty extension", "", "fixture.events.thing"},
		{"empty name", "fixture.events", ""},
	}
	for _, test := range tests {
		if _, ok := appevents.ResolveDefinition(test.eventName, test.extensionID); ok {
			t.Fatalf("%s: foreign namespace resolved", test.name)
		}
	}
}

// Core 事件名即使不在任何插件命名空间下也必须照旧工作。
func TestResolveDefinitionKeepsCoreEvents(t *testing.T) {
	definition, ok := appevents.ResolveDefinition("topic.created", "")
	if !ok {
		t.Fatal("core event rejected")
	}
	if definition.Kind != appevents.KindObserve {
		t.Fatalf("core kind = %q", definition.Kind)
	}
	// Core 事件名由目录命中，与调用方命名空间无关：任何插件都能发布 Core 事件
	// 这条路径不做命名空间限制，因为 Emit 的无身份入口本来就服务于宿主代码。
	// 命令层会另外要求前缀属于调用方，两道门各自独立。
	if _, ok := appevents.ResolveDefinition("topic.created", "fixture.events"); !ok {
		t.Fatal("core event rejected when an extension id is supplied")
	}
}

func TestOwnsEventName(t *testing.T) {
	if !appevents.OwnsEventName("a.b", "a.b.thing") {
		t.Fatal("own prefix rejected")
	}
	if appevents.OwnsEventName("a.b", "a.bc.thing") {
		t.Fatal("partial prefix accepted")
	}
	if appevents.OwnsEventName("", "a.b.thing") {
		t.Fatal("empty owner accepted")
	}
	if appevents.OwnsEventName("a.b", "") {
		t.Fatal("empty name accepted")
	}
}
