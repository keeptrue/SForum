package pluginv2

import (
	"context"
	"testing"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

type eventsCommandClientStub struct {
	request *hostwire.CommandRequest
	result  *hostwire.CommandResult
}

func (s *eventsCommandClientStub) Plan(
	context.Context,
	*hostwire.CommandRequest,
	...grpc.CallOption,
) (*hostwire.CommandPlan, error) {
	return nil, nil
}

func (s *eventsCommandClientStub) Execute(
	_ context.Context,
	request *hostwire.CommandRequest,
	_ ...grpc.CallOption,
) (*hostwire.CommandResult, error) {
	s.request = request
	return s.result, nil
}

func eventsTestHost(stub *eventsCommandClientStub) *Host {
	return &Host{
		Commands: stub,
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: "fixture.events", ExtensionVersion: "1.0.0",
			ArtifactDigest: "digest", InstanceId: "instance",
		},
		instance: "instance",
	}
}

// 发布请求必须绑定已验证 runtime 身份、清掉 actor，并把幂等键同时写进 context 与
// 请求顶层：Host 的命令回执按这三者做归属与去重。
func TestEventEmitRequestBindsExactRuntimeAndClearsActor(t *testing.T) {
	stub := &eventsCommandClientStub{}
	host := eventsTestHost(stub)
	parent := &protocolwire.RequestContext{Locale: "zh-CN", IdempotencyKey: "emit-1"}
	request, err := host.EventEmitRequest(parent, EventEmitInput{
		Name: "fixture.events.topic-archived", Kind: HostEventsEmitKindObserve,
		Payload: map[string]any{"topicId": "42"}, IdempotencyKey: "emit-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.GetCommandId() != HostEventsEmitCommandID || request.GetCommandVersion() != HostEventsEmitCommandVersion {
		t.Fatalf("command identity = %#v", request)
	}
	if request.GetContext().GetActor() != nil || request.GetActorDelegation() != "" ||
		request.GetContext().GetExtension().GetExtensionId() != "fixture.events" ||
		request.GetIdempotencyKey() != "emit-1" ||
		request.GetContext().GetIdempotencyKey() != "emit-1" {
		t.Fatalf("event request authority drifted: %#v", request)
	}
	if !DocumentMatchesSchema(request.GetInput(), HostEventsEmitInputSchemaRef) {
		t.Fatalf("input schema = %#v", request.GetInput())
	}
	values := request.GetInput().GetValue().AsMap()
	if values["name"] != "fixture.events.topic-archived" || values["kind"] != "observe" {
		t.Fatalf("input values = %#v", values)
	}
	payload, ok := values["payload"].(map[string]any)
	if !ok || payload["topicId"] != "42" {
		t.Fatalf("payload = %#v", values["payload"])
	}
}

func TestEventEmitRequestRejectsAuthorSideInvalidInput(t *testing.T) {
	host := eventsTestHost(&eventsCommandClientStub{})
	cases := map[string]EventEmitInput{
		"empty name": {
			Name: "", Kind: HostEventsEmitKindObserve, Payload: map[string]any{}, IdempotencyKey: "k",
		},
		"name with whitespace": {
			Name: "fixture.events.bad name", Kind: HostEventsEmitKindObserve, Payload: map[string]any{}, IdempotencyKey: "k",
		},
		"unsupported kind": {
			Name: "fixture.events.x", Kind: "validate", Payload: map[string]any{}, IdempotencyKey: "k",
		},
		"missing payload": {
			Name: "fixture.events.x", Kind: HostEventsEmitKindObserve, IdempotencyKey: "k",
		},
		"missing idempotency key": {
			Name: "fixture.events.x", Kind: HostEventsEmitKindObserve, Payload: map[string]any{},
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := host.EventEmitRequest(nil, input); err == nil {
				t.Fatalf("%s must be rejected before the round trip", name)
			}
		})
	}
}

func TestEventEmitDeliveryReadsDecimalCounters(t *testing.T) {
	document, err := NewTypedDocument(HostEventsEmitResultSchemaRef, map[string]any{
		"planned": false, "name": "fixture.events.x",
		"deliveredCount": "2", "failedCount": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	delivered, failed, ok := EventEmitDelivery(&hostwire.CommandResult{Output: document})
	if !ok || delivered != 2 || failed != 0 {
		t.Fatalf("delivery = %d/%d ok=%v", delivered, failed, ok)
	}
	// 结果文档缺失或形状不对时必须报告不可读，而不是把 0 当成功投递。
	if _, _, ok := EventEmitDelivery(&hostwire.CommandResult{}); ok {
		t.Fatal("empty result must not read as a successful delivery")
	}
}
