package pluginv2

import (
	"context"
	"errors"
	"testing"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestNotificationEmitRequestBindsExactRuntimeAndClearsActor(t *testing.T) {
	client := &notificationCommandClientStub{result: &hostwire.CommandResult{State: hostwire.CommandState_COMMAND_STATE_COMMITTED}}
	host := &Host{
		Commands: client,
		identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.notifications", ExtensionVersion: "1.0.0", ArtifactDigest: "digest", InstanceId: "instance"},
		instance: "instance",
	}
	parent := &protocolwire.RequestContext{
		Actor: &protocolwire.Actor{UserId: 99}, Locale: "zh-CN", IdempotencyKey: "emit-42",
		Deadline: timestamppb.New(time.Now().Add(time.Minute)),
	}
	request, err := host.NotificationEmitRequest(parent, NotificationEmitInput{
		Type: "fixture.notifications.order_ready", PayloadVersion: 1,
		Payload: map[string]any{"orderId": "42"}, RecipientUserIDs: []int64{42},
		Target:         &NotificationTarget{Kind: "extension_route", ID: "fixture.notifications.route.orders"},
		IdempotencyKey: "emit-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.GetContext().GetActor() != nil || request.GetActorDelegation() != "" ||
		request.GetContext().GetExtension().GetExtensionId() != "fixture.notifications" ||
		request.GetContext().GetLocale() != "zh-CN" || request.GetIdempotencyKey() != "emit-42" {
		t.Fatalf("notification request authority drifted: %#v", request)
	}
	values := request.GetInput().GetValue().AsMap()
	if values["recipientUserIds"].([]any)[0] != "42" || values["payload"].(map[string]any)["orderId"] != "42" {
		t.Fatalf("notification request values = %#v", values)
	}
	if _, err := host.EmitNotification(context.Background(), parent, NotificationEmitInput{
		Type: "fixture.notifications.order_ready", PayloadVersion: 1,
		Payload: map[string]any{"orderId": "42"}, RecipientUserIDs: []int64{42}, IdempotencyKey: "emit-42",
	}); err != nil || client.executeCalls != 1 {
		t.Fatalf("EmitNotification calls=%d err=%v", client.executeCalls, err)
	}
}

func TestNotificationEmitRequestRejectsForeignNamespaceAndUnsafeInput(t *testing.T) {
	host := &Host{Commands: &notificationCommandClientStub{}, identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.notifications"}, instance: "instance"}
	base := NotificationEmitInput{Type: "fixture.notifications.notice", PayloadVersion: 1, Payload: map[string]any{}, RecipientUserIDs: []int64{1}, IdempotencyKey: "emit-1"}
	tests := []NotificationEmitInput{base, base, base, base}
	tests[0].Type = "other.notifications.notice"
	tests[1].RecipientUserIDs = []int64{1, 1}
	tests[2].IdempotencyKey = "contains space"
	tests[3].Target = &NotificationTarget{Kind: "none", ID: "https://attacker.invalid"}
	for index, input := range tests {
		if _, err := host.NotificationEmitRequest(nil, input); !errors.Is(err, ErrHostNotificationEmitInvalid) {
			t.Fatalf("invalid SDK case %d error = %v", index, err)
		}
	}
}

type notificationCommandClientStub struct {
	result       *hostwire.CommandResult
	executeCalls int
}

func (c *notificationCommandClientStub) Plan(context.Context, *hostwire.CommandRequest, ...grpc.CallOption) (*hostwire.CommandPlan, error) {
	return nil, nil
}

func (c *notificationCommandClientStub) Execute(context.Context, *hostwire.CommandRequest, ...grpc.CallOption) (*hostwire.CommandResult, error) {
	c.executeCalls++
	return c.result, nil
}
