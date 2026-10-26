// sforum-events-command-reference 是一个只做一件事的 Protocol V2 fixture：在一次
// 同步 hook 里通过 SDK 的 EmitEvent 发布一个事件，并把命令判定（状态、原因码、
// 投递计数）作为 hook 结果文档回给宿主。
//
// 事件名、kind 与幂等键都由 hook 输入给出，因此同一个 fixture 既能验证「自己命名
// 空间可以发布」，也能验证「别的插件命名空间与 Core 事件名会被拒」——这正是 SDK
// 无法在作者侧判定的部分（命名空间归属只有 Host 的已验证身份能决定）。
package main

import (
	"context"
	"errors"
	"log"
	"strconv"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	emitHookID       = "sforum.events-command-reference.hook.emit"
	emitHookName     = "sforum.events-command-reference.emit-requested"
	emitInputSchema  = "sforum.events-command-reference.hook.emit.input@1"
	emitResultSchema = "sforum.events-command-reference.hook.emit.result@1"
)

const (
	customEventID = "sforum.events-command-reference.event.custom"
	customEvent   = "sforum.events-command-reference.custom-requested"
)

func main() {
	server := pluginv2.NewServer()
	hooks, err := pluginv2.NewHookRegistry(pluginv2.HookDefinition{
		// 声明过的事件必须有人应答：没有这条 listener，宿主投递会失败，发布本身
		// 就会被报成失败——那正是「能声明、不能投递」另一种形态。
		ID: customEventID, Name: customEvent, Kind: "observe",
		ContractVersion: customEventID + "@1", Handler: customEventID,
		InputSchema: "sforum.events-command-reference.event.custom.input@1",
		Execution:   "sync", FailurePolicy: "fail_open", TimeoutMS: 2000,
		Execute: func(context.Context, *pluginv2.HookCall) (*pluginv2.HookResult, error) {
			return &pluginv2.HookResult{Accepted: true}, nil
		},
	}, pluginv2.HookDefinition{
		ID: emitHookID, Name: emitHookName, Kind: "observe",
		ContractVersion: emitHookID + "@1", Handler: emitHookID,
		InputSchema: emitInputSchema, ResultSchema: emitResultSchema,
		Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 5000,
		Execute: func(ctx context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
			host, hostErr := server.Host()
			if hostErr != nil || call == nil {
				return nil, errors.New("events command fixture Host broker is unavailable")
			}
			values := pluginv2.TypedDocumentValues(call.Payload)
			payload := map[string]any{}
			if raw, ok := values["payload"].(map[string]any); ok {
				payload = raw
			}
			result, emitErr := host.EmitEvent(ctx, call.Context, pluginv2.EventEmitInput{
				Name:           stringValue(values["name"]),
				Kind:           stringValue(values["kind"]),
				Payload:        payload,
				IdempotencyKey: stringValue(values["idempotencyKey"]),
			})
			delivered, failed, _ := pluginv2.EventEmitDelivery(result)
			document, docErr := pluginv2.NewTypedDocument(emitResultSchema, map[string]any{
				"state":     result.GetState().String(),
				"reason":    result.GetError().GetReason(),
				"name":      stringValue(pluginv2.EventEmitResultValues(result)["name"]),
				"delivered": strconv.Itoa(delivered),
				"failed":    strconv.Itoa(failed),
			})
			if docErr != nil {
				return nil, docErr
			}
			return &pluginv2.HookResult{
				Accepted: emitErr == nil && result.GetError() == nil,
				Result:   document,
			}, nil
		},
	})
	if err != nil {
		log.Fatalf("configure events command fixture: %v", err)
	}
	pluginv2.Serve(server.WithHookRegistry(hooks))
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
