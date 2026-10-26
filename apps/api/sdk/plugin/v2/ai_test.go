package pluginv2

import (
	"context"
	"errors"
	"testing"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

// 插件作者侧的构造必须绑定精确 runtime 身份并清掉 actor：与 notifications.emit
// 同一条纪律，AI 调用也不能代表某个用户。
func TestAICompletionRequestBindsExactRuntimeAndClearsActor(t *testing.T) {
	client := &aiCommandClientStub{result: &hostwire.CommandResult{
		State:  hostwire.CommandState_COMMAND_STATE_COMMITTED,
		Output: aiResultDocument(t, "hello"),
	}}
	host := &Host{
		Commands: client,
		identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.ai", ExtensionVersion: "1.0.0", ArtifactDigest: "digest", InstanceId: "instance"},
		instance: "instance",
	}
	parent := &protocolwire.RequestContext{Actor: nil, Locale: "zh-CN", IdempotencyKey: "ai-42"}
	request, err := host.AICompletionRequest(parent, AICompletionInput{
		Purpose: "fixture.ai.summary", CostClass: "standard", MaxTokens: 512,
		Messages:      []AICompletionMessage{{Role: "user", Text: "summarize this"}},
		PromptVersion: "fixture@1", IdempotencyKey: "ai-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.GetCommandId() != HostAICompleteCommandID || request.GetCommandVersion() != HostAICompleteCommandVersion {
		t.Fatalf("command identity = %#v", request)
	}
	if request.GetContext().GetActor() != nil || request.GetActorDelegation() != "" ||
		request.GetContext().GetExtension().GetExtensionId() != "fixture.ai" ||
		request.GetIdempotencyKey() != "ai-42" {
		t.Fatalf("ai request authority drifted: %#v", request)
	}
	if !DocumentMatchesSchema(request.GetInput(), HostAICompleteInputSchemaRef) {
		t.Fatalf("input schema = %#v", request.GetInput())
	}
	values := request.GetInput().GetValue().AsMap()
	if values["purpose"] != "fixture.ai.summary" || values["costClass"] != "standard" {
		t.Fatalf("input values = %#v", values)
	}
	// Proto Struct 只有数字类型，maxTokens 通过数值传递是安全的（远小于 2^53）。
	if values["maxTokens"] != float64(512) {
		t.Fatalf("maxTokens = %#v", values["maxTokens"])
	}
	// 输入里绝不能出现 profile / model：插件不许寻址供应商。
	for _, forbidden := range []string{"profileRef", "profile", "model"} {
		if _, present := values[forbidden]; present {
			t.Fatalf("input carries %s: %#v", forbidden, values)
		}
	}
	result, err := host.Complete(context.Background(), parent, AICompletionInput{
		Purpose: "fixture.ai.summary", CostClass: "standard", MaxTokens: 512,
		Messages:       []AICompletionMessage{{Role: "user", Text: "summarize this"}},
		IdempotencyKey: "ai-42",
	})
	if err != nil || client.executeCalls != 1 {
		t.Fatalf("Complete calls=%d err=%v", client.executeCalls, err)
	}
	if AICompletionText(result) != "hello" {
		t.Fatalf("completion text = %q", AICompletionText(result))
	}
}

// 拒绝路径：越界值、非法成本等级、不合法工具名、重复工具调用 id、坏 JSON Schema。
// 这些都能在作者侧立刻发现，不该浪费一次必然失败的往返。
func TestAICompletionRequestRejectsInvalidInput(t *testing.T) {
	host := &Host{Commands: &aiCommandClientStub{}, identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.ai"}, instance: "instance"}
	temperature := 5.0
	tests := []struct {
		name  string
		input AICompletionInput
	}{
		{"empty purpose", AICompletionInput{CostClass: "standard", MaxTokens: 16, Messages: []AICompletionMessage{{Role: "user", Text: "hi"}}, IdempotencyKey: "k"}},
		{"unknown cost class", AICompletionInput{Purpose: "p", CostClass: "free", MaxTokens: 16, Messages: []AICompletionMessage{{Role: "user", Text: "hi"}}, IdempotencyKey: "k"}},
		{"zero tokens", AICompletionInput{Purpose: "p", CostClass: "standard", MaxTokens: 0, Messages: []AICompletionMessage{{Role: "user", Text: "hi"}}, IdempotencyKey: "k"}},
		{"no messages", AICompletionInput{Purpose: "p", CostClass: "standard", MaxTokens: 16, IdempotencyKey: "k"}},
		{"bad role", AICompletionInput{Purpose: "p", CostClass: "standard", MaxTokens: 16, Messages: []AICompletionMessage{{Role: "system", Text: "hi"}}, IdempotencyKey: "k"}},
		{"temperature out of range", AICompletionInput{Purpose: "p", CostClass: "standard", MaxTokens: 16, Temperature: &temperature, Messages: []AICompletionMessage{{Role: "user", Text: "hi"}}, IdempotencyKey: "k"}},
		{"missing idempotency key", AICompletionInput{Purpose: "p", CostClass: "standard", MaxTokens: 16, Messages: []AICompletionMessage{{Role: "user", Text: "hi"}}}},
		{"dotted tool name", AICompletionInput{
			Purpose: "p", CostClass: "standard", MaxTokens: 16, IdempotencyKey: "k",
			Messages: []AICompletionMessage{{Role: "user", Text: "hi"}},
			Tools:    []AICompletionTool{{Name: "fixture.lookup", Description: "d"}},
		}},
		{"duplicate tool call id", AICompletionInput{
			Purpose: "p", CostClass: "standard", MaxTokens: 16, IdempotencyKey: "k",
			Messages: []AICompletionMessage{{Role: "assistant", ToolCalls: []AICompletionToolCall{
				{ID: "call-1", Name: "fixture-lookup"}, {ID: "call-1", Name: "fixture-lookup"},
			}}},
		}},
		{"invalid tool schema", AICompletionInput{
			Purpose: "p", CostClass: "standard", MaxTokens: 16, IdempotencyKey: "k",
			Messages: []AICompletionMessage{{Role: "user", Text: "hi"}},
			Tools:    []AICompletionTool{{Name: "fixture-lookup", Description: "d", Parameters: "{not json"}},
		}},
	}
	for _, test := range tests {
		if _, err := host.AICompletionRequest(nil, test.input); !errors.Is(err, ErrHostAICompleteInvalid) {
			t.Fatalf("%s: error = %v", test.name, err)
		}
	}
}

// 工具声明是插件真正能带来价值的输入：它们必须逐字到达 Host。
func TestAICompletionRequestForwardsToolDeclarations(t *testing.T) {
	host := &Host{Commands: &aiCommandClientStub{}, identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.ai"}, instance: "instance"}
	request, err := host.AICompletionRequest(nil, AICompletionInput{
		Purpose: "p", CostClass: "premium", MaxTokens: 16, IdempotencyKey: "k",
		Messages:   []AICompletionMessage{{Role: "user", Text: "hi"}},
		Tools:      []AICompletionTool{{Name: "fixture-lookup", Description: "look up", Parameters: `{"type":"object"}`}},
		ToolChoice: "auto", ResponseFormat: "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := request.GetInput().GetValue().AsMap()
	tools, ok := values["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", values["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "fixture-lookup" || tool["parameters"] != `{"type":"object"}` {
		t.Fatalf("tool = %#v", tool)
	}
	if values["toolChoice"] != "auto" || values["responseFormat"] != "json" {
		t.Fatalf("strategy values = %#v", values)
	}
}

func TestAICompletionResultValuesHandlesNilResult(t *testing.T) {
	if AICompletionText(nil) != "" {
		t.Fatal("nil result produced text")
	}
	if len(AICompletionResultValues(nil)) != 0 {
		t.Fatal("nil result produced values")
	}
}

type aiCommandClientStub struct {
	result       *hostwire.CommandResult
	executeCalls int
}

func (c *aiCommandClientStub) Plan(context.Context, *hostwire.CommandRequest, ...grpc.CallOption) (*hostwire.CommandPlan, error) {
	return nil, nil
}

func (c *aiCommandClientStub) Execute(context.Context, *hostwire.CommandRequest, ...grpc.CallOption) (*hostwire.CommandResult, error) {
	c.executeCalls++
	return c.result, nil
}

func aiResultDocument(t *testing.T, text string) *protocolwire.TypedDocument {
	t.Helper()
	document, err := NewTypedDocument(HostAICompleteResultSchemaRef, map[string]any{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	return document
}
