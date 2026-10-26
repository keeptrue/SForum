package ai_test

import (
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func toolRequest() ai.CompletionRequest {
	request := validRequest()
	request.Tools = []ai.ToolDefinition{{
		Name:        "forum-search",
		Description: "站内检索主题",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
	}}
	request.ToolChoice = "auto"
	request.Messages = []ai.Message{
		{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "站内有人聊过 channel 吗？"}}},
		{Role: ai.RoleAssistant, Parts: []ai.Part{{Type: ai.PartText, Text: "我先查一下。"}}, ToolCalls: []ai.ToolCall{
			{ID: "call_1", Name: "forum-search", Arguments: `{"query":"channel"}`},
		}},
		{Role: ai.RoleTool, ToolCallID: "call_1", Parts: []ai.Part{{Type: ai.PartText, Text: "命中 2 条：主题 12、主题 40"}}},
	}
	return request
}

func TestOpenAIRequestCarriesToolsAndToolMessages(t *testing.T) {
	wire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", toolRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var payload struct {
		ToolChoice string `json:"tool_choice"`
		Tools      []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.ToolChoice != "auto" || len(payload.Tools) != 1 || payload.Tools[0].Type != "function" {
		t.Fatalf("tools = %+v choice=%q", payload.Tools, payload.ToolChoice)
	}
	if payload.Tools[0].Function.Name != "forum-search" || len(payload.Tools[0].Function.Parameters) == 0 {
		t.Fatalf("tool function = %+v", payload.Tools[0].Function)
	}
	// system 之后依次是 user / assistant(tool_calls) / tool(tool_call_id)。
	if len(payload.Messages) != 4 {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	assistant := payload.Messages[2]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", assistant)
	}
	if assistant.ToolCalls[0].ID != "call_1" || assistant.ToolCalls[0].Function.Name != "forum-search" ||
		assistant.ToolCalls[0].Function.Arguments != `{"query":"channel"}` {
		t.Fatalf("assistant tool call = %+v", assistant.ToolCalls[0])
	}
	result := payload.Messages[3]
	if result.Role != "tool" || result.ToolCallID != "call_1" || result.Content != "命中 2 条：主题 12、主题 40" {
		t.Fatalf("tool message = %+v", result)
	}
}

func TestOpenAIArgumentsFallBackToEmptyObject(t *testing.T) {
	request := toolRequest()
	request.Messages[1].ToolCalls[0].Arguments = "   "
	wire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", request)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if !containsJSON(t, wire.Body, `"arguments":"{}"`) {
		t.Fatalf("empty arguments must become {}: %s", wire.Body)
	}
}

func TestOpenAIParsesToolCallResponse(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"forum-topic-read","arguments":"{\"topicId\":42}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":8}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.StopReason != ai.StopReasonTool {
		t.Fatalf("stop reason = %q", response.StopReason)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "forum-topic-read" ||
		response.ToolCalls[0].Arguments != `{"topicId":42}` {
		t.Fatalf("tool calls = %+v", response.ToolCalls)
	}
}

func TestOpenAIParsesLegacyFunctionCallFinishReason(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"function_call"}],"usage":{}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.StopReason != ai.StopReasonTool {
		t.Fatalf("stop reason = %q", response.StopReason)
	}
}

func TestAnthropicRequestCarriesToolsAndMergesToolResults(t *testing.T) {
	request := toolRequest()
	// 两条工具结果必须合并进同一条 user 消息：Anthropic 不接受同角色连续出现。
	request.Messages = append(request.Messages[:2],
		ai.Message{Role: ai.RoleTool, ToolCallID: "call_1", Parts: []ai.Part{{Type: ai.PartText, Text: "第一条结果"}}},
		ai.Message{Role: ai.RoleTool, ToolCallID: "call_2", Parts: []ai.Part{{Type: ai.PartText, Text: "第二条结果"}}},
	)
	wire, err := ai.BuildWireRequest(claudeProfile(), "sk-ant", request)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	var payload struct {
		ToolChoice map[string]string `json:"tool_choice"`
		Tools      []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "forum-search" || len(payload.Tools[0].InputSchema) == 0 {
		t.Fatalf("tools = %+v", payload.Tools)
	}
	if payload.ToolChoice["type"] != "auto" {
		t.Fatalf("tool choice = %+v", payload.ToolChoice)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	assistant := payload.Messages[1]
	if assistant.Role != "assistant" || len(assistant.Content) != 2 {
		t.Fatalf("assistant message = %+v", assistant)
	}
	if assistant.Content[1].Type != "tool_use" || assistant.Content[1].ID != "call_1" ||
		assistant.Content[1].Name != "forum-search" || string(assistant.Content[1].Input) != `{"query":"channel"}` {
		t.Fatalf("tool_use block = %+v", assistant.Content[1])
	}
	results := payload.Messages[2]
	if results.Role != "user" || len(results.Content) != 2 {
		t.Fatalf("merged tool results = %+v", results)
	}
	if results.Content[0].Type != "tool_result" || results.Content[0].ToolUseID != "call_1" ||
		results.Content[0].Content[0].Text != "第一条结果" {
		t.Fatalf("first tool_result = %+v", results.Content[0])
	}
	if results.Content[1].ToolUseID != "call_2" || results.Content[1].Content[0].Text != "第二条结果" {
		t.Fatalf("second tool_result = %+v", results.Content[1])
	}
}

func TestAnthropicWrapsNonObjectArguments(t *testing.T) {
	request := toolRequest()
	request.Messages[1].ToolCalls[0].Arguments = "不是 JSON"
	wire, err := ai.BuildWireRequest(claudeProfile(), "sk-ant", request)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if !containsJSON(t, wire.Body, `"input":{"value":"不是 JSON"}`) {
		t.Fatalf("invalid arguments must be wrapped: %s", wire.Body)
	}
}

func TestAnthropicParsesToolUseResponse(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"我先查一下。"},{"type":"tool_use","id":"toolu_1","name":"forum-search","input":{"query":"channel"}}],"stop_reason":"tool_use","usage":{"input_tokens":40,"output_tokens":12}}`)
	response, err := ai.ParseWireResponse(claudeProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.StopReason != ai.StopReasonTool || response.Text != "我先查一下。" {
		t.Fatalf("response = %+v", response)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "toolu_1" ||
		response.ToolCalls[0].Name != "forum-search" || response.ToolCalls[0].Arguments != `{"query":"channel"}` {
		t.Fatalf("tool calls = %+v", response.ToolCalls)
	}
}

func TestAnthropicToolChoiceMapping(t *testing.T) {
	cases := map[string]string{"auto": "auto", "required": "any", "none": "none"}
	for choice, want := range cases {
		request := toolRequest()
		request.ToolChoice = choice
		wire, err := ai.BuildWireRequest(claudeProfile(), "sk-ant", request)
		if err != nil {
			t.Fatalf("%s: build failed: %v", choice, err)
		}
		if !containsJSON(t, wire.Body, `"tool_choice":{"type":"`+want+`"}`) {
			t.Fatalf("%s: tool choice not mapped to %s: %s", choice, want, wire.Body)
		}
	}
}

func containsJSON(t *testing.T, body []byte, fragment string) bool {
	t.Helper()
	return json.Valid(body) && strings.Contains(string(body), fragment)
}
