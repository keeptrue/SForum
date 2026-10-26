package hostapi

import (
	"context"
	"strings"
	"testing"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeEventPublisher 记录最后一次发布，用来断言命名空间与 kind 是否被正确传递。
type fakeEventPublisher struct {
	lastExtensionID string
	lastEnvelope    appevents.Envelope
	// unknown 为真时模拟事件层拒绝（例如没有任何监听者）。
	unknown bool
	calls   int
}

func (p *fakeEventPublisher) EmitFromExtension(_ context.Context, extensionID string, envelope appevents.Envelope) appevents.Result {
	p.calls++
	p.lastExtensionID = extensionID
	p.lastEnvelope = envelope
	if p.unknown {
		return appevents.Result{OK: false, Reason: "extension.event_unknown", Message: "Unknown extension event."}
	}
	return appevents.Result{OK: true}
}

func protocolV2EventsRequest(t *testing.T, extensionID string, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	document, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			Extension:      &protocolv2.ExtensionIdentity{ExtensionId: extensionID},
			IdempotencyKey: "emit-1",
		},
		CommandId:      CommandEventsEmitID,
		CommandVersion: CommandEventsEmitVersion,
		IdempotencyKey: "emit-1",
		Input: &protocolv2.TypedDocument{
			SchemaId:      CommandEventsEmitInputSchema,
			SchemaVersion: CommandEventsEmitSchemaVersion,
			Value:         document,
		},
	}
}

// protocolV2EventsContext 把已验证的 runtime 身份放进 context —— 命令必须读它，
// 而不是读请求里自称的扩展 ID。
// protocolV2EventsRun 走完整的 Prepare → Execute 流程：真实调用总是先 Prepare，
// 校验也住在那里。直接调 Execute 会绕过命名空间判定，测不出真实行为。
func protocolV2EventsRun(t *testing.T, definition protocolV2CommandDefinition, ctx context.Context, request *hostv2.CommandRequest) (*protocolV2CommandExecution, error) {
	t.Helper()
	preparation, err := definition.Prepare(ctx, nil, request)
	if err != nil {
		return nil, err
	}
	return definition.Execute(ctx, nil, request, preparation)
}

func protocolV2EventsContext(extensionID string) context.Context {
	return ContextWithProtocolV2RuntimeIdentity(context.Background(),
		&protocolv2.ExtensionIdentity{ExtensionId: extensionID})
}

func TestProtocolV2EventsEmitDeclaresServiceAuthorityAndCapability(t *testing.T) {
	definition := newProtocolV2EventsEmitCommandDefinition(&fakeEventPublisher{})
	if definition.ID != CommandEventsEmitID || definition.Version != CommandEventsEmitVersion {
		t.Fatalf("command id = %#v", definition)
	}
	if definition.ActorMode != protocolV2CommandActorService || len(definition.RequiredPermissions) != 0 {
		t.Fatalf("events emit authority = %#v", definition)
	}
	if definition.RequiredCapability != "events.emit" {
		t.Fatalf("events emit capability = %q", definition.RequiredCapability)
	}
}

func TestProtocolV2EventsEmitPublishesOwnNamespace(t *testing.T) {
	publisher := &fakeEventPublisher{}
	definition := newProtocolV2EventsEmitCommandDefinition(publisher)
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe",
		"payload": map[string]any{"orderId": "42"},
	})
	execution, err := protocolV2EventsRun(t, definition, protocolV2EventsContext("fixture.events"), request)
	if err != nil {
		t.Fatal(err)
	}
	if publisher.calls != 1 || publisher.lastExtensionID != "fixture.events" {
		t.Fatalf("publisher calls = %d extension = %q", publisher.calls, publisher.lastExtensionID)
	}
	if publisher.lastEnvelope.Name != "fixture.events.order_ready" ||
		publisher.lastEnvelope.Kind != appevents.KindObserve {
		t.Fatalf("envelope = %#v", publisher.lastEnvelope)
	}
	// 输出不得回显 payload 内容：Plan 与审计会看到它。
	// planned=false 表示这是一次真实发布，不是 dry-run。
	values := execution.Output.GetValue().AsMap()
	if values["name"] != "fixture.events.order_ready" || values["planned"] != false {
		t.Fatalf("events output = %#v", values)
	}
	if values["deliveredCount"] != "1" || values["failedCount"] != "0" {
		t.Fatalf("events counts = %#v", values)
	}
	if strings.Contains(execution.Output.String(), "42") {
		t.Fatalf("events output leaked payload: %v", execution.Output)
	}
}

// 核心拒绝路径：插件不得发布别的插件命名空间、Core 事件名，或缺失身份。
func TestProtocolV2EventsEmitRejectsForeignAndCoreNamespaces(t *testing.T) {
	tests := []struct {
		name        string
		extensionID string
		eventName   string
	}{
		{"other plugin namespace", "fixture.events", "other.plugin.thing"},
		{"core event name", "fixture.events", "topic.created"},
		{"prefix without dot", "fixture.events", "fixture.eventsx.thing"},
		{"empty name", "fixture.events", ""},
	}
	for _, test := range tests {
		publisher := &fakeEventPublisher{}
		definition := newProtocolV2EventsEmitCommandDefinition(publisher)
		request := protocolV2EventsRequest(t, test.extensionID, map[string]any{
			"name": test.eventName, "kind": "observe", "payload": map[string]any{"a": "b"},
		})
		if _, err := protocolV2EventsRun(t, definition, protocolV2EventsContext(test.extensionID), request); err == nil {
			t.Fatalf("%s: foreign namespace accepted", test.name)
		}
		if publisher.calls != 0 {
			t.Fatalf("%s: published despite rejection", test.name)
		}
	}
}

func TestProtocolV2EventsEmitRejectsForgedRequestIdentity(t *testing.T) {
	publisher := &fakeEventPublisher{}
	definition := newProtocolV2EventsEmitCommandDefinition(publisher)
	// 请求自称 fixture.events，但已验证的 runtime 身份是别的插件。
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe", "payload": map[string]any{"a": "b"},
	})
	if _, err := protocolV2EventsRun(t, definition, protocolV2EventsContext("fixture.other"), request); err == nil {
		t.Fatal("forged request identity accepted")
	}
	if publisher.calls != 0 {
		t.Fatal("published despite identity mismatch")
	}
}

// 无 runtime 身份意味着调用没经过 broker 认证。
func TestProtocolV2EventsEmitRejectsUnauthenticatedContext(t *testing.T) {
	publisher := &fakeEventPublisher{}
	definition := newProtocolV2EventsEmitCommandDefinition(publisher)
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe", "payload": map[string]any{"a": "b"},
	})
	if _, err := protocolV2EventsRun(t, definition, context.Background(), request); err == nil {
		t.Fatal("unauthenticated context accepted")
	}
	if publisher.calls != 0 {
		t.Fatal("published without runtime identity")
	}
}

func TestProtocolV2EventsEmitRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
	}{
		{"missing kind", map[string]any{"name": "fixture.events.a", "payload": map[string]any{"a": "b"}}},
		{"validate kind", map[string]any{"name": "fixture.events.a", "kind": "validate", "payload": map[string]any{"a": "b"}}},
		{"nil payload", map[string]any{"name": "fixture.events.a", "kind": "observe"}},
		{"empty name", map[string]any{"name": "", "kind": "observe", "payload": map[string]any{"a": "b"}}},
	}
	for _, test := range tests {
		publisher := &fakeEventPublisher{}
		definition := newProtocolV2EventsEmitCommandDefinition(publisher)
		request := protocolV2EventsRequest(t, "fixture.events", test.values)
		if _, err := protocolV2EventsRun(t, definition, protocolV2EventsContext("fixture.events"), request); err == nil {
			t.Fatalf("%s: invalid input accepted", test.name)
		}
		if publisher.calls != 0 {
			t.Fatalf("%s: published despite invalid input", test.name)
		}
	}
}

func TestProtocolV2EventsEmitReportsUnavailablePublisher(t *testing.T) {
	definition := newProtocolV2EventsEmitCommandDefinition(nil)
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe", "payload": map[string]any{"a": "b"},
	})
	if _, err := protocolV2EventsRun(t, definition, protocolV2EventsContext("fixture.events"), request); err == nil {
		t.Fatal("nil publisher accepted")
	}
}

func TestProtocolV2EventsEmitPreviewOmitsPayload(t *testing.T) {
	definition := newProtocolV2EventsEmitCommandDefinition(&fakeEventPublisher{})
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe",
		"payload": map[string]any{"secret": "private-order"},
	})
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Impact) != 1 || strings.Contains(plan.Impact[0].GetSummary(), "private-order") {
		t.Fatalf("preview leaked payload: %#v", plan.Impact)
	}
	if plan.ProjectedResult.GetValue().AsMap()["planned"] != true {
		t.Fatalf("preview projection = %#v", plan.ProjectedResult.GetValue().AsMap())
	}
}

// 命令真正可用性取决于两个阶段的产物都被当成计划校验：Preview 与 Prepare 都必须
// 带上 Host 策略决定与匹配声明 schema 的投影结果。少了任何一项，真实 broker 调用
// 会在「prepare 后重新 finalize」这一步被拒（host.command_policy_required /
// host.command_schema_mismatch），而只调 Prepare→Execute 的单测看不出来。
func TestProtocolV2EventsEmitStagesProducePlanValidPreparations(t *testing.T) {
	definition := newProtocolV2EventsEmitCommandDefinition(&fakeEventPublisher{})
	request := protocolV2EventsRequest(t, "fixture.events", map[string]any{
		"name": "fixture.events.order_ready", "kind": "observe",
		"payload": map[string]any{"orderId": "42"},
	})
	ctx := protocolV2EventsContext("fixture.events")

	preview, err := definition.Preview(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := definition.Prepare(ctx, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	for name, preparation := range map[string]*protocolV2CommandPreparation{"preview": preview, "prepare": prepared} {
		if preparation == nil {
			t.Fatalf("%s returned no preparation", name)
		}
		if len(preparation.Policy) == 0 {
			t.Fatalf("%s preparation carries no policy decision", name)
		}
		if detail := validateProtocolV2CommandDocument(
			preparation.ProjectedResult, definition.OutputSchemaID, definition.OutputSchemaVersion, "projected result",
		); detail != nil {
			t.Fatalf("%s projected result does not match the declared schema: %v", name, detail.GetReason())
		}
	}
	if prepared.private == nil {
		t.Fatal("prepare must carry the validated input for Execute")
	}
}
