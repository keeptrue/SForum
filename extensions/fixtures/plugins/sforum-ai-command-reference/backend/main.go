// sforum-ai-command-reference 是一个只做一件事的 Protocol V2 fixture：在一次同步
// hook 里通过 SDK 调用宿主 AI 补全命令，并把命令结果（状态、稳定原因码、正文）
// 作为 hook 结果文档回给宿主。
//
// 之所以把命令结果回传而不是把失败伪装成成功：命令层的拒绝（能力、闸门、预算）
// 是宿主权威判定，fixture 需要让测试能从 hook 结果里读到确切原因。
package main

import (
	"context"
	"errors"
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	completeHookID       = "sforum.ai-command-reference.hook.complete"
	completeHookName     = "sforum.ai-command-reference.complete-requested"
	completeInputSchema  = "sforum.ai-command-reference.hook.complete.input@1"
	completeResultSchema = "sforum.ai-command-reference.hook.complete.result@1"
)

func main() {
	server := pluginv2.NewServer()
	hooks, err := pluginv2.NewHookRegistry(pluginv2.HookDefinition{
		ID: completeHookID, Name: completeHookName, Kind: "observe",
		ContractVersion: completeHookID + "@1", Handler: completeHookID,
		InputSchema: completeInputSchema, ResultSchema: completeResultSchema,
		Execution: "sync", FailurePolicy: "fail_closed", TimeoutMS: 5000,
		Execute: func(ctx context.Context, call *pluginv2.HookCall) (*pluginv2.HookResult, error) {
			host, hostErr := server.Host()
			if hostErr != nil || call == nil {
				return nil, errors.New("ai command fixture Host broker is unavailable")
			}
			values := pluginv2.TypedDocumentValues(call.Payload)
			result, completeErr := host.Complete(ctx, call.Context, pluginv2.AICompletionInput{
				Purpose:        stringValue(values["purpose"]),
				CostClass:      stringValue(values["costClass"]),
				MaxTokens:      256,
				Messages:       []pluginv2.AICompletionMessage{{Role: "user", Text: stringValue(values["prompt"])}},
				PromptVersion:  "sforum.ai-command-reference@1",
				IdempotencyKey: stringValue(values["idempotencyKey"]),
			})
			document, docErr := pluginv2.NewTypedDocument(completeResultSchema, map[string]any{
				"state":  result.GetState().String(),
				"reason": result.GetError().GetReason(),
				"text":   pluginv2.AICompletionText(result),
			})
			if docErr != nil {
				return nil, docErr
			}
			return &pluginv2.HookResult{
				Accepted: completeErr == nil && result.GetError() == nil,
				Result:   document,
			}, nil
		},
	})
	if err != nil {
		log.Fatalf("configure ai command fixture: %v", err)
	}
	pluginv2.Serve(server.WithHookRegistry(hooks))
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
