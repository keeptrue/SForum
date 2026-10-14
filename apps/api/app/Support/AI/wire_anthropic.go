package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropic-messages 适配器。与 OpenAI 的差异集中在五处：system 是顶层字段、
// 消息内容是 block 数组、鉴权用 x-api-key 加 anthropic-version 头、
// 用量字段名不同（input_tokens / output_tokens）、工具结果必须出现在 user 角色的
// 消息里（tool_result block）而不是独立的 tool 角色。

// AnthropicAPIVersion 是 Messages API 的必需版本头。
const AnthropicAPIVersion = "2023-06-01"

// anthropicBlock 同时承载 text / image / tool_use / tool_result 四种 block 形态。
// 各形态字段互斥，用一个结构体换来「Content 是同类数组」的简单性。
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// image 的字段：source 只支持 base64 内联（与契约的 ImageContent 对应）。
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source,omitempty"`
	// tool_use 与 tool_result 的字段。
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	// tool_result 的内容本身也是 block 数组。
	Content []anthropicBlock `json:"content,omitempty"`
	IsError bool             `json:"is_error,omitempty"`
}

// anthropicContentBlocks 把中立 parts 翻译成 Anthropic block 数组：文本拼接成
// 一个 text block，图片各自成为 image block。
func anthropicContentBlocks(message Message) []anthropicBlock {
	blocks := make([]anthropicBlock, 0, len(message.Parts)+1)
	if text := messageText(message); text != "" {
		blocks = append(blocks, anthropicBlock{Type: "text", Text: text})
	}
	for _, part := range message.Parts {
		if part.Type != PartImage || part.Image == nil {
			continue
		}
		block := anthropicBlock{Type: "image"}
		block.Source = &struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}{Type: "base64", MediaType: part.Image.MediaType, Data: part.Image.Data}
		blocks = append(blocks, block)
	}
	return blocks
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicMessagesPayload struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	// ToolChoice 是 Anthropic 的对象形态：auto / any / none。
	ToolChoice map[string]string `json:"tool_choice,omitempty"`
}

type anthropicMessagesResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func buildAnthropicMessagesRequest(profile Profile, apiKey string, request CompletionRequest) (WireRequest, error) {
	endpoint, err := joinAPIURL(profile.BaseURL, "/messages")
	if err != nil {
		return WireRequest{}, err
	}
	messages := make([]anthropicMessage, 0, len(request.Messages))
	// 连续的工具结果必须合并进同一条 user 消息：Anthropic 不接受同一角色连续出现。
	lastWasToolResult := false
	for _, message := range request.Messages {
		switch message.Role {
		case RoleTool:
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: message.ToolCallID,
				Content:   anthropicTextBlocks(messageText(message)),
			}
			if lastWasToolResult && len(messages) > 0 {
				last := &messages[len(messages)-1]
				last.Content = append(last.Content, block)
				continue
			}
			messages = append(messages, anthropicMessage{Role: "user", Content: []anthropicBlock{block}})
			lastWasToolResult = true
		case RoleAssistant:
			blocks := anthropicTextBlocks(messageText(message))
			for _, call := range message.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Name,
					Input: anthropicToolInput(call.Arguments),
				})
			}
			if len(blocks) == 0 {
				// 理论不可达（契约要求 assistant 至少有正文或工具调用），但空 block
				// 数组会被供应商拒绝；宁可跳过也不发出一个必然 4xx 的请求。
				continue
			}
			messages = append(messages, anthropicMessage{Role: "assistant", Content: blocks})
			lastWasToolResult = false
		default:
			messages = append(messages, anthropicMessage{Role: "user", Content: anthropicContentBlocks(message)})
			lastWasToolResult = false
		}
	}
	payload := anthropicMessagesPayload{
		Model:       profile.Model,
		System:      request.System,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		ToolChoice:  anthropicToolChoice(request.ToolChoice),
	}
	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}
	body, err := marshalWireBody(payload)
	if err != nil {
		return WireRequest{}, err
	}
	return WireRequest{
		Method: http.MethodPost,
		URL:    endpoint,
		Headers: map[string]string{
			"Content-Type":      "application/json",
			"Accept":            "application/json",
			"x-api-key":         apiKey,
			"anthropic-version": AnthropicAPIVersion,
		},
		Body: body,
	}, nil
}

func parseAnthropicMessagesResponse(statusCode int, body []byte) (WireResponse, error) {
	var payload anthropicMessagesResponse
	if err := unmarshalWireBody(body, &payload); err != nil {
		return WireResponse{}, err
	}
	if statusCode < 200 || statusCode > 299 {
		summary := ""
		if payload.Error != nil {
			summary = providerErrorSummary(payload.Error.Message)
		}
		return WireResponse{}, fmt.Errorf("%w: anthropic-messages status %d %s", ErrWire, statusCode, summary)
	}
	text := ""
	calls := make([]ToolCall, 0, 1)
	for _, block := range payload.Content {
		switch block.Type {
		case "text":
			text += block.Text
		// thinking 是推理模式下的思考 block。正文为空时它是唯一的线索：
		// 至少让调用方知道模型确实产出了内容，只是没落进 text block。
		case "thinking":
			if strings.TrimSpace(text) == "" {
				text = block.Text
			}
		case "tool_use":
			name := strings.TrimSpace(block.Name)
			if name == "" || strings.TrimSpace(block.ID) == "" {
				continue
			}
			arguments := "{}"
			if len(block.Input) > 0 {
				arguments = string(block.Input)
			}
			calls = append(calls, ToolCall{ID: block.ID, Name: name, Arguments: arguments})
		}
	}
	return WireResponse{
		Text:       text,
		ToolCalls:  calls,
		StopReason: MapAnthropicStopReason(payload.StopReason),
		Usage: Usage{
			InputTokens:  payload.Usage.InputTokens,
			OutputTokens: payload.Usage.OutputTokens,
			CachedTokens: payload.Usage.CacheReadInputTokens,
		},
		RawFinishReason: payload.StopReason,
	}, nil
}

func anthropicTextBlocks(text string) []anthropicBlock {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []anthropicBlock{{Type: "text", Text: text}}
}

// anthropicToolInput 把模型的参数串变成 tool_use.input 需要的对象。Anthropic
// 要求 input 是 JSON 对象：非法或非对象的参数包进 {"value": ...}，保证请求可解析。
func anthropicToolInput(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed)
	}
	wrapped, err := json.Marshal(map[string]string{"value": trimmed})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return wrapped
}

// anthropicToolChoice 把中立策略翻译成 Anthropic 的对象形态。
func anthropicToolChoice(choice string) map[string]string {
	switch strings.TrimSpace(choice) {
	case "auto":
		return map[string]string{"type": "auto"}
	case "required":
		return map[string]string{"type": "any"}
	case "none":
		return map[string]string{"type": "none"}
	default:
		return nil
	}
}
