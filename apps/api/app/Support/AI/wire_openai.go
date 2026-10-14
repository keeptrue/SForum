package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openai-chat 适配器。它覆盖 ChatGPT、DeepSeek、Moonshot、Qwen、Ollama、vLLM
// 以及任何 OpenAI 兼容网关；差异全部落在 profile 配置里。
//
// 关于 max_tokens：OpenAI 新模型推荐 max_completion_tokens，但 DeepSeek 与
// 绝大多数兼容实现只认 max_tokens。为了「一个适配器覆盖最多端点」，这里使用
// max_tokens，并在 profile 层用 MaxCompletionTokens 约束取值范围。
//
// 工具调用的翻译同样按最保守的公共子集：tools[].function、assistant.tool_calls、
// role=tool + tool_call_id。这是 OpenAI 兼容实现的公约数。

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

// openAIContentPart 是 OpenAI 混合内容数组里的一项：text 或 image_url。
// 只有用户消息会用数组形态；system/assistant/tool 仍是纯字符串——这是
// OpenAI 兼容实现的公约数，多数端点对 assistant 数组形态支持并不一致。
type openAIContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type openAIChatMessage struct {
	Role string `json:"role"`
	// Content 是 string（纯文本）或 []openAIContentPart（含图片）；nil 表示
	// 该消息没有正文（assistant 只发工具调用的形态）。
	Content any `json:"content,omitempty"`
	// ToolCalls 只出现在 assistant 消息上：模型要求先执行这些工具。
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 只出现在 role=tool 的消息上，指向它所回应的调用。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// openAIMessageContent 把中立 parts 翻译成 OpenAI 的 content 形态：纯文本
// 保持字符串（最兼容），含图片时变成数组。
func openAIMessageContent(message Message) any {
	hasImage := false
	for _, part := range message.Parts {
		if part.Type == PartImage && part.Image != nil {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return messageText(message)
	}
	parts := make([]openAIContentPart, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch part.Type {
		case PartText:
			parts = append(parts, openAIContentPart{Type: "text", Text: part.Text})
		case PartImage:
			if part.Image == nil {
				continue
			}
			imagePart := openAIContentPart{Type: "image_url"}
			imagePart.ImageURL = &struct {
				URL string `json:"url"`
			}{URL: "data:" + part.Image.MediaType + ";base64," + part.Image.Data}
			parts = append(parts, imagePart)
		}
	}
	return parts
}

type openAIFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAITool struct {
	Type     string                   `json:"type"`
	Function openAIFunctionDefinition `json:"function"`
}

type openAIChatRequestPayload struct {
	Model          string              `json:"model"`
	Messages       []openAIChatMessage `json:"messages"`
	MaxTokens      int                 `json:"max_tokens"`
	Temperature    *float64            `json:"temperature,omitempty"`
	ResponseFormat map[string]string   `json:"response_format,omitempty"`
	Tools          []openAITool        `json:"tools,omitempty"`
	// ToolChoice 采用 OpenAI 原生的 auto / none / required 字符串形态。
	ToolChoice string `json:"tool_choice,omitempty"`
}

type openAIChatResponsePayload struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// ReasoningContent 是推理类模型的思考正文。DeepSeek-R1、Qwen3、
			// 各类兼容网关都会把它与 content 分开返回；模型把答案写进这个字段
			// 时，只读 content 就会拿到一个「调用成功但空回复」的假故障。
			ReasoningContent string `json:"reasoning_content"`
			// Reasoning 是部分网关（含 Claude 兼容层）使用的同类字段名。
			Reasoning string `json:"reasoning"`
			// ToolCalls 只出现在 assistant 消息上：模型要求先执行这些工具。
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func buildOpenAIChatRequest(profile Profile, apiKey string, request CompletionRequest) (WireRequest, error) {
	endpoint, err := joinAPIURL(profile.BaseURL, "/chat/completions")
	if err != nil {
		return WireRequest{}, err
	}
	messages := make([]openAIChatMessage, 0, len(request.Messages)+1)
	// OpenAI 协议把 system 放在 messages[0]；Anthropic 是顶层字段。
	if system := request.System; system != "" {
		messages = append(messages, openAIChatMessage{Role: "system", Content: system})
	}
	for _, message := range request.Messages {
		built := openAIChatMessage{Role: string(message.Role)}
		switch message.Role {
		case RoleTool:
			built.ToolCallID = message.ToolCallID
			built.Content = messageText(message)
		case RoleAssistant:
			built.Content = messageText(message)
			for _, call := range message.ToolCalls {
				built.ToolCalls = append(built.ToolCalls, openAIToolCall{
					ID:   call.ID,
					Type: "function",
					Function: openAIFunctionCall{
						Name: call.Name,
						// OpenAI 要求 arguments 是 JSON 字符串；空参用 {} 而不是空串。
						Arguments: openAIArguments(call.Arguments),
					},
				})
			}
		default:
			// 用户消息可能带图片：纯文本保持字符串形态（兼容面最大），
			// 含图片时切换为混合内容数组。
			built.Content = openAIMessageContent(message)
		}
		messages = append(messages, built)
	}
	payload := openAIChatRequestPayload{
		Model:       profile.Model,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Temperature: request.Temperature,
		ToolChoice:  request.ToolChoice,
	}
	if request.ResponseFormat == ResponseFormatJSON {
		payload.ResponseFormat = map[string]string{"type": "json_object"}
	}
	for _, tool := range request.Tools {
		payload.Tools = append(payload.Tools, openAITool{
			Type: "function",
			Function: openAIFunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
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
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			"Authorization": "Bearer " + apiKey,
		},
		Body: body,
	}, nil
}

func parseOpenAIChatResponse(statusCode int, body []byte) (WireResponse, error) {
	var payload openAIChatResponsePayload
	if err := unmarshalWireBody(body, &payload); err != nil {
		return WireResponse{}, err
	}
	if statusCode < 200 || statusCode > 299 {
		summary := ""
		if payload.Error != nil {
			summary = providerErrorSummary(payload.Error.Message)
		}
		return WireResponse{}, fmt.Errorf("%w: openai-chat status %d %s", ErrWire, statusCode, summary)
	}
	if len(payload.Choices) == 0 {
		return WireResponse{}, fmt.Errorf("%w: openai-chat returned no choices", ErrWire)
	}
	choice := payload.Choices[0]
	calls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	for index, call := range choice.Message.ToolCalls {
		if strings.TrimSpace(call.Function.Name) == "" {
			continue
		}
		id := strings.TrimSpace(call.ID)
		if id == "" {
			// 少数兼容网关省略 id；补一个稳定值，否则工具结果无法对上调用。
			id = fmt.Sprintf("call_%d", index)
		}
		calls = append(calls, ToolCall{
			ID:        id,
			Name:      strings.TrimSpace(call.Function.Name),
			Arguments: call.Function.Arguments,
		})
	}
	return WireResponse{
		Text:       firstNonEmpty(choice.Message.Content, choice.Message.ReasoningContent, choice.Message.Reasoning),
		ToolCalls:  calls,
		StopReason: MapOpenAIFinishReason(choice.FinishReason),
		Usage: Usage{
			InputTokens:  payload.Usage.PromptTokens,
			OutputTokens: payload.Usage.CompletionTokens,
			CachedTokens: payload.Usage.PromptTokensDetails.CachedTokens,
		},
		RawFinishReason: choice.FinishReason,
	}, nil
}

// firstNonEmpty 返回第一个非空白的候选正文。推理类模型可能把答案写在
// reasoning_content 而不是 content，空 content 不能被当成「没有回复」。
func firstNonEmpty(candidates ...string) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// openAIArguments 保证 assistant 消息里的 arguments 始终是可解析的 JSON 字符串。
// 模型偶尔给出空串或散文：空串补 {}，非法内容原样透传（由工具返回解析失败），
// 但不制造一个连 JSON.parse 都过不了的载荷。
func openAIArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	if !json.Valid([]byte(trimmed)) {
		if encoded, err := json.Marshal(trimmed); err == nil {
			return string(encoded)
		}
		return "{}"
	}
	return trimmed
}

// messageText 把一条消息的文本部件拼成一个字符串。适配器不解释结构，
// 只负责把中立的 parts 形态翻译成协议要求的单一 content 字段。
func messageText(message Message) string {
	if len(message.Parts) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, part := range message.Parts {
		builder.WriteString(part.Text)
	}
	return builder.String()
}
