package pluginv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func aiToolTestDefinition(t *testing.T) AIToolDefinition {
	t.Helper()
	return AIToolDefinition{
		ID:              "plugin.ai-tool.reference.lookup",
		ContractVersion: "plugin.ai-tool.reference.lookup@1",
		Name:            "reference-order-lookup",
		Description:     "Look up an order by its public number.",
		Parameters:      `{"type":"object","properties":{"orderNumber":{"type":"string"}}}`,
		TimeoutMS:       1000,
		Execute: func(_ context.Context, call *AIToolCall) (*AIToolResult, error) {
			if call.Context.GetActor() != nil {
				t.Fatal("tool dispatch must not carry actor authority")
			}
			var arguments struct {
				OrderNumber string `json:"orderNumber"`
			}
			if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
				return &AIToolResult{Content: "参数无法解析", IsError: true}, nil
			}
			return &AIToolResult{Content: "order " + arguments.OrderNumber + " shipped"}, nil
		},
	}
}

func aiToolTestRequestContext() *protocolwire.RequestContext {
	return &protocolwire.RequestContext{
		RequestId: "ai-tool-1",
		Deadline:  timestamppb.New(time.Now().Add(time.Second)),
		Extension: &protocolwire.ExtensionIdentity{
			ExtensionId: "plugin.ai-tool.reference", ExtensionVersion: "1.0.0",
			ArtifactDigest: strings.Repeat("a", 64), InstanceId: "ai-tool-runtime",
		},
	}
}

func aiToolTestRequest(t *testing.T, declarationID, contractVersion, arguments string) *pluginwire.ProviderCallRequest {
	t.Helper()
	document, err := NewTypedDocument(HostAIToolRequestSchemaRef, map[string]any{
		"name": "reference-order-lookup", "arguments": arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pluginwire.ProviderCallRequest{
		Context: aiToolTestRequestContext(), SlotId: HostAIToolSlot, Operation: HostAIToolOperation,
		ContractVersion: contractVersion, DeclarationId: declarationID, Input: document,
	}
}

func TestAIToolRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := aiToolTestDefinition(t)
	if _, err := NewAIToolRegistry(valid); err != nil {
		t.Fatalf("valid definition must register: %v", err)
	}
	if _, err := NewAIToolRegistry(valid, valid); err == nil {
		t.Fatal("duplicate tool ids must be rejected")
	}
	cases := map[string]func(*AIToolDefinition){
		"dotted name":      func(d *AIToolDefinition) { d.Name = "reference.order.lookup" },
		"missing name":     func(d *AIToolDefinition) { d.Name = "" },
		"missing execute":  func(d *AIToolDefinition) { d.Execute = nil },
		"parameters array": func(d *AIToolDefinition) { d.Parameters = `["x"]` },
		"parameters json":  func(d *AIToolDefinition) { d.Parameters = `{"a":` },
		"timeout zero":     func(d *AIToolDefinition) { d.TimeoutMS = 0 },
		"timeout above":    func(d *AIToolDefinition) { d.TimeoutMS = HostAIToolMaximumTimeoutMS + 1 },
		"contract version": func(d *AIToolDefinition) { d.ContractVersion = "not-a-contract" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			definition := aiToolTestDefinition(t)
			mutate(&definition)
			if _, err := NewAIToolRegistry(definition); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

func TestAIToolRegistryDispatchesExactDeclarationThroughReservedSlot(t *testing.T) {
	definition := aiToolTestDefinition(t)
	registry, err := NewAIToolRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithAIToolRegistry(registry)
	server.started = true
	server.identity = cloneV2IdentityForSEOTest(aiToolTestRequestContext().GetExtension())

	response, err := server.ProviderCall(context.Background(), aiToolTestRequest(
		t, definition.ID, definition.ContractVersion, `{"orderNumber":"42"}`,
	))
	if err != nil || response.GetError() != nil {
		t.Fatalf("dispatch response=%#v err=%v", response, err)
	}
	if !DocumentMatchesSchema(response.GetOutput(), HostAIToolResponseSchemaRef) {
		t.Fatalf("output schema = %#v", response.GetOutput())
	}
	values := TypedDocumentValues(response.GetOutput())
	if values["content"] != "order 42 shipped" || values["isError"] != false {
		t.Fatalf("output values = %#v", values)
	}
	if definitions := registry.Definitions(); len(definitions) != 1 || definitions[0].Execute != nil ||
		definitions[0].Name != definition.Name {
		t.Fatalf("definitions = %#v", definitions)
	}
}

// 保留 slot 不得落进通用 provider 命名空间：否则一个只声明了 providers[] 的插件
// 也可能被当成 AI 工具执行者，模型调用的执行者就不再是清单声明的那一个。
func TestAIToolSlotNeverFallsThroughToTheGenericProviderNamespace(t *testing.T) {
	definition := aiToolTestDefinition(t)
	registry, err := NewAIToolRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	generic, err := NewProviderRegistry(ProviderDefinition{
		ID: "plugin.generic.claim", Slot: HostAIToolSlot, ContractVersion: "plugin.generic.claim@1",
		Label: "Generic claim", Handler: "generic.claim",
		RequestSchema:  "plugin.generic.claim.request@1",
		ResponseSchema: "plugin.generic.claim.response@1", TimeoutMS: 1000,
		Execute: func(context.Context, *ProviderCall) (*protocolwire.TypedDocument, error) {
			t.Fatal("the reserved ai tool slot must never dispatch to the generic provider registry")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithProviderRegistry(generic)
	server.started = true
	server.identity = cloneV2IdentityForSEOTest(aiToolTestRequestContext().GetExtension())
	if _, err := server.ProviderCall(context.Background(), aiToolTestRequest(
		t, definition.ID, definition.ContractVersion, `{}`,
	)); status.Code(err) != codes.Unimplemented {
		t.Fatalf("without an AI tool registry the reserved slot must be unimplemented, got %v", err)
	}

	routed := NewServer().WithProviderRegistry(generic).WithAIToolRegistry(registry)
	routed.started = true
	routed.identity = cloneV2IdentityForSEOTest(aiToolTestRequestContext().GetExtension())
	response, err := routed.ProviderCall(context.Background(), aiToolTestRequest(
		t, definition.ID, definition.ContractVersion, `{"orderNumber":"7"}`,
	))
	if err != nil || response.GetError() != nil || TypedDocumentValues(response.GetOutput())["content"] != "order 7 shipped" {
		t.Fatalf("reserved slot routing drifted: response=%#v err=%v", response, err)
	}
}

func TestAIToolRegistryDeniesWrongSlotUnknownToolAndContractDrift(t *testing.T) {
	definition := aiToolTestDefinition(t)
	registry, err := NewAIToolRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	// 直接在注册表上验证拒绝姿态：经过 Server 的非保留 slot 会落到通用 provider
	// 命名空间，那是另一条路径（由上一个测试单独覆盖）。
	wrongSlot := aiToolTestRequest(t, definition.ID, definition.ContractVersion, `{}`)
	wrongSlot.SlotId = "sforum.seo.page"
	response, err := registry.ProviderCall(context.Background(), wrongSlot)
	if err != nil || response.GetError().GetReason() != "ai_tool.slot_mismatch" {
		t.Fatalf("wrong slot response=%#v err=%v", response, err)
	}

	unknown := aiToolTestRequest(t, "plugin.ai-tool.reference.missing", definition.ContractVersion, `{}`)
	response, err = registry.ProviderCall(context.Background(), unknown)
	if err != nil || response.GetError().GetReason() != "ai_tool.not_found" {
		t.Fatalf("unknown declaration response=%#v err=%v", response, err)
	}

	drifted := aiToolTestRequest(t, definition.ID, "plugin.ai-tool.reference.lookup@2", `{}`)
	response, err = registry.ProviderCall(context.Background(), drifted)
	if err != nil || response.GetError().GetReason() != "ai_tool.contract_mismatch" {
		t.Fatalf("contract drift response=%#v err=%v", response, err)
	}

	// 参数不是字符串时不得 panic：模型给出的参数是它自己编的 JSON。
	lenient := aiToolTestRequest(t, definition.ID, definition.ContractVersion, `{`)
	response, err = registry.ProviderCall(context.Background(), lenient)
	if err != nil || response.GetError() != nil {
		t.Fatalf("unparsable arguments must stay a tool-level failure: response=%#v err=%v", response, err)
	}
	if values := TypedDocumentValues(response.GetOutput()); values["isError"] != true {
		t.Fatalf("unparsable arguments must return an explainable tool error: %#v", values)
	}
}

// SDK 侧的线上契约与边界值字面量：Host 侧有对应的漂移测试，两侧一起把同一组数字
// 钉死，任何单侧改动都会在本地测试里暴露。
func TestAIToolSDKContractIsPinned(t *testing.T) {
	if HostAIToolSlot != "sforum.ai.tool" || HostAIToolOperation != "invoke" ||
		HostAIToolRequestSchemaRef != "sforum.ai.tool.invoke.request@1" ||
		HostAIToolResponseSchemaRef != "sforum.ai.tool.invoke.response@1" {
		t.Fatalf("sdk ai tool wire contract drifted: slot=%q op=%q req=%q res=%q",
			HostAIToolSlot, HostAIToolOperation, HostAIToolRequestSchemaRef, HostAIToolResponseSchemaRef)
	}
	if HostAIToolNameMaximumLength != 64 || HostAIToolDescriptionMaximum != 1024 ||
		HostAIToolSchemaMaximumBytes != 8192 || HostAIToolMaximumTimeoutMS != 30_000 ||
		HostAIToolsMaximumPerExtension != 16 {
		t.Fatalf("sdk ai tool limits drifted: name=%d description=%d schema=%d timeout=%d count=%d",
			HostAIToolNameMaximumLength, HostAIToolDescriptionMaximum,
			HostAIToolSchemaMaximumBytes, HostAIToolMaximumTimeoutMS, HostAIToolsMaximumPerExtension)
	}
}

// 宿主按清单声明写入的会话事实必须能被插件读到；没写的事实保持零值，插件不能
// 假定它们总是存在。
func TestAIToolRegistryDeliversDeclaredContextFacts(t *testing.T) {
	var captured *AIToolCall
	definition := AIToolDefinition{
		ID:              "plugin.ai-tool.reference.context",
		ContractVersion: "plugin.ai-tool.reference.context@1",
		Name:            "reference-context",
		Description:     "Capture context facts.",
		Parameters:      `{"type":"object"}`,
		TimeoutMS:       1000,
		Execute: func(_ context.Context, call *AIToolCall) (*AIToolResult, error) {
			captured = call
			return &AIToolResult{Content: "ok"}, nil
		},
	}
	registry, err := NewAIToolRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}

	dispatch := func(values map[string]any) {
		t.Helper()
		document, err := NewTypedDocument(HostAIToolRequestSchemaRef, values)
		if err != nil {
			t.Fatal(err)
		}
		response, err := registry.ProviderCall(context.Background(), &pluginwire.ProviderCallRequest{
			Context: aiToolTestRequestContext(), SlotId: HostAIToolSlot, Operation: HostAIToolOperation,
			ContractVersion: definition.ContractVersion, DeclarationId: definition.ID, Input: document,
		})
		if err != nil || response.GetError() != nil {
			t.Fatalf("dispatch failed: err=%v detail=%v", err, response.GetError())
		}
	}

	dispatch(map[string]any{
		"name": definition.Name, "arguments": "{}",
		"locale": "zh-CN", "topicId": 42, "viewerUserId": 7,
	})
	if captured.Locale != "zh-CN" || captured.TopicID != 42 || captured.ViewerUserID != 7 {
		t.Fatalf("context facts drifted: %#v", captured)
	}

	dispatch(map[string]any{"name": definition.Name, "arguments": "{}"})
	if captured.Locale != "" || captured.TopicID != 0 || captured.ViewerUserID != 0 {
		t.Fatalf("absent facts must stay zero: %#v", captured)
	}
}
