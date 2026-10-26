package pluginv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	HostAICompleteCommandID       = "sforum.ai.complete"
	HostAICompleteCommandVersion  = "1"
	HostAICompleteInputSchemaRef  = "sforum.ai.complete.input@1"
	HostAICompleteResultSchemaRef = "sforum.ai.complete.result@1"

	// 与 Host 侧 ai.* 常量同值。SDK 刻意不 import Support/AI：插件 SDK 只应依赖
	// 契约，边界值在这里重复声明一次，避免把整个网关实现拖进插件二进制。
	// 两者必须同步：Host 侧测试 TestSDKAICompletionLimitsMatchHost 钉住了这层镜像，
	// 否则会出现「Host 放行、SDK 先拒」的分裂。
	HostAICompleteMaxMessages    = 256
	HostAICompleteMaxTokens      = 128_000
	HostAICompleteMaxPurposeLen  = 128
	HostAICompleteMaxTools       = 16
	HostAICompleteMaxToolNameLen = 64
)

var (
	ErrHostAICompleteInvalid = errors.New("protocol v2 AI completion request is invalid")
)

// AICompletionMessage 是一条中立消息。system 是请求级字段，不在这里出现：
// 各协议对 system 位置的约定不同，由 Host 的适配器决定。
type AICompletionMessage struct {
	Role       string
	Text       string
	ToolCallID string
	ToolCalls  []AICompletionToolCall
}

// AICompletionToolCall 是模型要求的一次工具调用，或（role=tool 时）它所回应的
// 那次调用的标识。Arguments 是原始 JSON 字符串，由插件自行解析。
type AICompletionToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// AICompletionTool 是给模型看的工具声明。Parameters 是 JSON Schema 对象。
type AICompletionTool struct {
	Name        string
	Description string
	Parameters  string
}

// AICompletionInput 是作者侧的补全请求。它刻意没有 profile 或 model 字段：
// 用途声明成本等级，用哪个供应商与模型由站点配置决定。让插件选模型等于把
// 成本与质量的最终决定权从运营者手里拿走。
type AICompletionInput struct {
	Purpose        string
	CostClass      string
	System         string
	Messages       []AICompletionMessage
	MaxTokens      int
	Temperature    *float64
	ResponseFormat string
	Tools          []AICompletionTool
	ToolChoice     string
	PromptVersion  string
	ResourceType   string
	ResourceID     string
	IdempotencyKey string
}

// AICompletionRequest 构造 actorless 的 sforum.ai.complete@1 请求。
// Host 仍然对用途、成本等级、规模、工具与配额做权威校验；这里只做能在作者侧
// 立刻发现的形状错误，避免一次必然失败的往返。
func (h *Host) AICompletionRequest(parent *protocolwire.RequestContext, input AICompletionInput) (*hostwire.CommandRequest, error) {
	if h == nil || h.Commands == nil {
		return nil, ErrHostAICompleteInvalid
	}
	input.Purpose = strings.TrimSpace(input.Purpose)
	input.CostClass = strings.TrimSpace(input.CostClass)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	switch input.CostClass {
	case "economy", "standard", "premium":
	default:
		return nil, ErrHostAICompleteInvalid
	}
	if input.Purpose == "" || len(input.Purpose) > HostAICompleteMaxPurposeLen ||
		input.MaxTokens <= 0 || input.MaxTokens > HostAICompleteMaxTokens ||
		len(input.Messages) == 0 || len(input.Messages) > HostAICompleteMaxMessages ||
		len(input.Tools) > HostAICompleteMaxTools ||
		!validHostAIIdempotencyKey(input.IdempotencyKey) {
		return nil, ErrHostAICompleteInvalid
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		return nil, ErrHostAICompleteInvalid
	}
	messages := make([]any, 0, len(input.Messages))
	seenToolCalls := make(map[string]struct{})
	for _, message := range input.Messages {
		switch message.Role {
		case "user", "assistant", "tool":
		default:
			return nil, ErrHostAICompleteInvalid
		}
		parts := []any{}
		if strings.TrimSpace(message.Text) != "" {
			parts = append(parts, map[string]any{"type": "text", "text": message.Text})
		}
		calls := make([]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if !validHostAIToolName(call.Name) || strings.TrimSpace(call.ID) == "" {
				return nil, ErrHostAICompleteInvalid
			}
			if _, duplicate := seenToolCalls[call.ID]; duplicate {
				return nil, ErrHostAICompleteInvalid
			}
			seenToolCalls[call.ID] = struct{}{}
			calls = append(calls, map[string]any{
				"id": call.ID, "name": call.Name, "arguments": call.Arguments,
			})
		}
		entry := map[string]any{"role": message.Role}
		if len(parts) > 0 {
			entry["parts"] = parts
		}
		if message.ToolCallID != "" {
			entry["toolCallId"] = message.ToolCallID
		}
		if len(calls) > 0 {
			entry["toolCalls"] = calls
		}
		// 角色语义由 Host 权威判定（tool 必须带 toolCallId 且不带正文以外的内容、
		// user 必须有正文等）；这里只保证不构造出明显空壳的消息。
		if len(parts) == 0 && len(calls) == 0 {
			return nil, ErrHostAICompleteInvalid
		}
		messages = append(messages, entry)
	}
	tools := make([]any, 0, len(input.Tools))
	seenTools := make(map[string]struct{}, len(input.Tools))
	for _, tool := range input.Tools {
		if !validHostAIToolName(tool.Name) {
			return nil, ErrHostAICompleteInvalid
		}
		if _, duplicate := seenTools[tool.Name]; duplicate {
			return nil, ErrHostAICompleteInvalid
		}
		seenTools[tool.Name] = struct{}{}
		if parameters := strings.TrimSpace(tool.Parameters); parameters != "" && !json.Valid([]byte(parameters)) {
			return nil, ErrHostAICompleteInvalid
		}
		tools = append(tools, map[string]any{
			"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
		})
	}
	values := map[string]any{
		"purpose": input.Purpose, "costClass": input.CostClass,
		"messages": messages, "maxTokens": input.MaxTokens,
	}
	if input.System != "" {
		values["system"] = input.System
	}
	if input.Temperature != nil {
		values["temperature"] = *input.Temperature
	}
	if format := strings.TrimSpace(input.ResponseFormat); format != "" {
		values["responseFormat"] = format
	}
	if len(tools) > 0 {
		values["tools"] = tools
	}
	if choice := strings.TrimSpace(input.ToolChoice); choice != "" {
		values["toolChoice"] = choice
	}
	if version := strings.TrimSpace(input.PromptVersion); version != "" {
		values["promptVersion"] = version
	}
	if resourceType := strings.TrimSpace(input.ResourceType); resourceType != "" {
		values["resourceType"] = resourceType
	}
	if resourceID := strings.TrimSpace(input.ResourceID); resourceID != "" {
		values["resourceId"] = resourceID
	}
	document, err := NewTypedDocument(HostAICompleteInputSchemaRef, values)
	if err != nil {
		return nil, ErrHostAICompleteInvalid
	}
	requestContext := h.RequestContext(parent)
	if requestContext == nil || parent != nil && parent.GetIdempotencyKey() != "" && parent.GetIdempotencyKey() != input.IdempotencyKey {
		return nil, ErrHostAICompleteInvalid
	}
	requestContext.IdempotencyKey = input.IdempotencyKey
	return &hostwire.CommandRequest{
		Context: requestContext, CommandId: HostAICompleteCommandID,
		CommandVersion: HostAICompleteCommandVersion,
		IdempotencyKey: input.IdempotencyKey, Input: document,
	}, nil
}

// Complete executes sforum.ai.complete@1 through the runtime-scoped Host broker.
// A rejected command returns as a normal CommandResult with a stable ErrorDetail
// reason, matching every other Host Command family.
//
// 调用前请确认本插件声明了 ai.invoke 能力；未声明时 Host 在 Plan 阶段就拒绝，
// 不会产生任何出站调用。
func (h *Host) Complete(ctx context.Context, parent *protocolwire.RequestContext, input AICompletionInput) (*hostwire.CommandResult, error) {
	request, err := h.AICompletionRequest(parent, input)
	if err != nil {
		return nil, err
	}
	return h.Commands.Execute(ctx, request)
}

// AICompletionResultValues 读取结果文档。Host 把整数编码成十进制字符串
// （protobuf Struct 只有 double，放大整数会丢精度），这里只做形状断言，
// 不做数值解析——插件自己决定如何解释 usage。
func AICompletionResultValues(result *hostwire.CommandResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	return TypedDocumentValues(result.GetOutput())
}

// AICompletionText 是纯文本补全的便捷读取路径。需要工具调用结果时请直接读
// AICompletionResultValues：那时 Text 通常为空。
func AICompletionText(result *hostwire.CommandResult) string {
	values := AICompletionResultValues(result)
	text, _ := values["text"].(string)
	return text
}

func validHostAIToolName(name string) bool {
	if name == "" || len(name) > HostAICompleteMaxToolNameLen {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func validHostAIIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}
