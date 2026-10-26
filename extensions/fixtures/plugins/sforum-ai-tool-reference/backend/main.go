// sforum-ai-tool-reference 是一个只贡献 AI 工具、不接任何其它能力的 Protocol V2
// fixture：它证明「精确制品 + 冻结声明」的工具调用能真正走到子进程再回来。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const lookupToolID = "sforum.ai-tool-reference.order_lookup"

func main() {
	server := pluginv2.NewServer()
	registry, err := pluginv2.NewAIToolRegistry(pluginv2.AIToolDefinition{
		ID:              lookupToolID,
		ContractVersion: lookupToolID + "@1",
		Name:            "sforum-order-lookup",
		Description:     "Look up an order by its public number.",
		Parameters:      `{"type":"object","properties":{"orderNumber":{"type":"string"}},"required":["orderNumber"]}`,
		TimeoutMS:       1000,
		Execute:         lookupOrder,
	})
	if err != nil {
		log.Fatalf("configure ai tool fixture: %v", err)
	}
	pluginv2.Serve(server.WithAIToolRegistry(registry))
}

// lookupOrder 刻意区分两种失败：参数问题是「工具可解释的失败」（模型可以换一种
// 方式重试），而内部故障返回 Go error，由宿主替换成一句通用说明——插件内部错误串
// 不该借模型之口发布到社区，因此调用方能断言它从未出现在结果内容里。
func lookupOrder(_ context.Context, call *pluginv2.AIToolCall) (*pluginv2.AIToolResult, error) {
	var arguments struct {
		OrderNumber string `json:"orderNumber"`
		Fail        bool   `json:"fail"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		return &pluginv2.AIToolResult{Content: "arguments must be a JSON object", IsError: true}, nil
	}
	if arguments.Fail {
		return nil, errors.New("internal failure text that must never reach the model")
	}
	if arguments.OrderNumber == "" {
		return &pluginv2.AIToolResult{Content: "orderNumber is required", IsError: true}, nil
	}
	return &pluginv2.AIToolResult{Content: "order " + arguments.OrderNumber + " shipped"}, nil
}
