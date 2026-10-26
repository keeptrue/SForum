package main

import (
	"context"
	"errors"
	"log"
	"strconv"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	emitHookID   = "sforum.notification-reference.hook.emit"
	emitHookName = "sforum.notification-reference.emit-requested"
)

func main() {
	server := pluginv2.NewServer()
	hooks, err := pluginv2.NewHookRegistry(pluginv2.HookDefinition{
		ID: emitHookID, Name: emitHookName, Kind: "observe",
		ContractVersion: emitHookID + "@1", Handler: emitHookID,
		InputSchema: "sforum.notification-reference.hook.emit.input@1",
		Execution:   "sync", FailurePolicy: "fail_closed", TimeoutMS: 2000,
		Execute: func(ctx context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
			host, hostErr := server.Host()
			if hostErr != nil || call == nil {
				return nil, errors.New("notification Host broker is unavailable")
			}
			values := pluginv2.TypedDocumentValues(call.Payload)
			recipient, parseErr := strconv.ParseInt(stringValue(values["recipientUserId"]), 10, 64)
			orderID := stringValue(values["orderId"])
			idempotencyKey := stringValue(values["idempotencyKey"])
			if parseErr != nil || recipient <= 0 || orderID == "" || idempotencyKey == "" {
				return nil, errors.New("notification fixture input is invalid")
			}
			result, emitErr := host.EmitNotification(ctx, call.Context, pluginv2.NotificationEmitInput{
				Type: "sforum.notification-reference.order_ready", PayloadVersion: 1,
				Payload: map[string]any{"orderId": orderID}, RecipientUserIDs: []int64{recipient},
				Target:         &pluginv2.NotificationTarget{Kind: "extension_route", ID: "sforum.notification-reference.route.orders"},
				IdempotencyKey: idempotencyKey,
			})
			if emitErr != nil || result.GetError() != nil {
				return nil, errors.New("Host rejected notification emission")
			}
			return &pluginv2.HookResult{Accepted: true}, nil
		},
	})
	if err != nil {
		log.Fatalf("configure notification fixture: %v", err)
	}
	pluginv2.Serve(server.WithHookRegistry(hooks))
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
