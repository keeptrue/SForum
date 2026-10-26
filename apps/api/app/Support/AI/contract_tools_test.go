package ai_test

import (
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func toolLoopRequest() ai.CompletionRequest {
	request := validRequest()
	request.Tools = []ai.ToolDefinition{{
		Name:        "forum-search",
		Description: "站内检索",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}}
	request.ToolChoice = "auto"
	request.Messages = []ai.Message{
		{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "有人聊过 channel 吗？"}}},
		{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "call_1", Name: "forum-search", Arguments: `{"query":"channel"}`}}},
		{Role: ai.RoleTool, ToolCallID: "call_1", Parts: []ai.Part{{Type: ai.PartText, Text: "命中 2 条"}}},
	}
	return request
}

func TestCompletionRequestAcceptsToolLoopMessages(t *testing.T) {
	if err := toolLoopRequest().Validate(); err != nil {
		t.Fatalf("expected valid tool loop request, got %v", err)
	}
	// 工具结果允许空正文：工具可能确实没有可说的内容。
	request := toolLoopRequest()
	request.Messages[2].Parts = nil
	if err := request.Validate(); err != nil {
		t.Fatalf("empty tool result must stay valid, got %v", err)
	}
}

func TestCompletionRequestRejectsToolContractViolations(t *testing.T) {
	cases := map[string]func(*ai.CompletionRequest){
		"invalid tool name":   func(r *ai.CompletionRequest) { r.Tools[0].Name = "Forum Search" },
		"duplicate tool name": func(r *ai.CompletionRequest) { r.Tools = append(r.Tools, r.Tools[0]) },
		"too many tools": func(r *ai.CompletionRequest) {
			tools := make([]ai.ToolDefinition, 0, ai.MaxToolsPerRequest+1)
			for i := 0; i <= ai.MaxToolsPerRequest; i++ {
				tools = append(tools, ai.ToolDefinition{Name: "tool." + strings.Repeat("x", i)})
			}
			r.Tools = tools
		},
		"description over ceiling": func(r *ai.CompletionRequest) {
			r.Tools[0].Description = strings.Repeat("x", ai.MaxToolDescriptionBytes+1)
		},
		"schema over ceiling": func(r *ai.CompletionRequest) {
			r.Tools[0].Parameters = json.RawMessage(`"` + strings.Repeat("x", ai.MaxToolSchemaBytes) + `"`)
		},
		"schema not json":     func(r *ai.CompletionRequest) { r.Tools[0].Parameters = json.RawMessage(`{"type":`) },
		"unknown tool choice": func(r *ai.CompletionRequest) { r.ToolChoice = "sometimes" },
		"tool result without call id": func(r *ai.CompletionRequest) {
			r.Messages[2].ToolCallID = ""
		},
		"tool result with calls": func(r *ai.CompletionRequest) {
			r.Messages[2].ToolCalls = []ai.ToolCall{{ID: "call_2", Name: "forum-search"}}
		},
		"assistant without content": func(r *ai.CompletionRequest) {
			r.Messages[1].ToolCalls = nil
		},
		"user message with call id": func(r *ai.CompletionRequest) {
			r.Messages[0].ToolCallID = "call_1"
		},
		"tool call without id": func(r *ai.CompletionRequest) { r.Messages[1].ToolCalls[0].ID = " " },
		"tool call invalid name": func(r *ai.CompletionRequest) {
			r.Messages[1].ToolCalls[0].Name = "forum search"
		},
		"tool call arguments over ceiling": func(r *ai.CompletionRequest) {
			r.Messages[1].ToolCalls[0].Arguments = strings.Repeat("x", ai.MaxToolArgumentsBytes+1)
		},
	}
	for name, mutate := range cases {
		request := toolLoopRequest()
		mutate(&request)
		if err := request.Validate(); err == nil {
			t.Fatalf("%s: expected rejection, got nil", name)
		}
	}
}

func TestToolNameAndChoiceValidation(t *testing.T) {
	// 工具名必须落在供应商实际接受的字符集内：`^[a-z0-9_-]{1,64}$`。
	valid := []string{"forum-search", "time-now", "user-profile-read", "tool_1"}
	for _, name := range valid {
		if !ai.ValidToolName(name) {
			t.Fatalf("%q must be a valid tool name", name)
		}
	}
	// 点号是真实失败案例：DeepSeek/OpenAI 会以 400 拒绝带点号的名字，
	// 因此契约层直接禁止，避免把不可用的名字留到运行期才暴露。
	invalid := []string{"", "Forum.Search", "forum search", "forum/search", "forum.search", strings.Repeat("x", ai.MaxToolNameLen+1)}
	for _, name := range invalid {
		if ai.ValidToolName(name) {
			t.Fatalf("%q must be rejected", name)
		}
	}
	for _, choice := range []string{"", "auto", "none", "required"} {
		if !ai.ValidToolChoice(choice) {
			t.Fatalf("%q must be a valid tool choice", choice)
		}
	}
	if ai.ValidToolChoice("always") {
		t.Fatal("unknown tool choice must be rejected")
	}
}

func TestInputBytesIncludeToolDeclarationsAndCalls(t *testing.T) {
	request := toolLoopRequest()
	expected := len(request.System) +
		len("有人聊过 channel 吗？") +
		len("forum-search") + len("站内检索") + len(`{"type":"object"}`) +
		len("call_1") + len("forum-search") + len(`{"query":"channel"}`) +
		len("命中 2 条")
	if got := request.InputBytes(); got != expected {
		t.Fatalf("input bytes = %d, want %d", got, expected)
	}
}

func TestSettingsToolBudgetValidation(t *testing.T) {
	settings := ai.RecommendedSettings()
	if settings.Gates.ResolvedToolCallsPerReply() != ai.DefaultToolCallsPerReply {
		t.Fatalf("recommended tool budget = %d", settings.Gates.ResolvedToolCallsPerReply())
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("recommended settings must validate: %v", err)
	}
	// 升级前保存的文档没有这个字段：缺省应获得推荐默认值，而不是被当作「关闭」。
	legacy := ai.RecommendedSettings()
	legacy.Gates.ToolCallsPerReply = nil
	if legacy.Gates.ResolvedToolCallsPerReply() != ai.DefaultToolCallsPerReply {
		t.Fatalf("missing budget must resolve to the default, got %d", legacy.Gates.ResolvedToolCallsPerReply())
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("missing budget must stay valid: %v", err)
	}
	over := ai.MaxToolCallsPerReply + 1
	settings.Gates.ToolCallsPerReply = &over
	if err := settings.Validate(); err == nil {
		t.Fatal("tool budget above the hard ceiling must be rejected")
	}
	// 显式 0 是运营者的选择：停用工具调用，必须被尊重。
	off := 0
	settings.Gates.ToolCallsPerReply = &off
	if err := settings.Validate(); err != nil {
		t.Fatalf("explicit zero (tools off) must stay valid: %v", err)
	}
	if settings.Gates.ResolvedToolCallsPerReply() != 0 {
		t.Fatalf("explicit zero must resolve to zero, got %d", settings.Gates.ResolvedToolCallsPerReply())
	}
}

func TestProfileToolsSupportedDefaultsOn(t *testing.T) {
	profile := deepseekProfile()
	if !profile.ToolsSupported() {
		t.Fatal("missing capability flag must default to supported")
	}
	disabled := false
	profile.SupportsTools = &disabled
	if profile.ToolsSupported() {
		t.Fatal("explicit false must disable tool support")
	}
	enabled := true
	profile.SupportsTools = &enabled
	if !profile.ToolsSupported() {
		t.Fatal("explicit true must enable tool support")
	}
}
