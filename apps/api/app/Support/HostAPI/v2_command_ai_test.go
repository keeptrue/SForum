package hostapi

import (
	"context"
	"errors"
	"testing"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeAIGateway 记录最后一次补全请求，并可按场景返回各类网关失败。
type fakeAIGateway struct {
	lastRequest supportai.CompletionRequest
	result      supportai.CompletionResult
	err         error
	calls       int
}

func (g *fakeAIGateway) Execute(_ context.Context, request supportai.CompletionRequest) (supportai.CompletionResult, error) {
	g.calls++
	g.lastRequest = request
	if g.err != nil {
		return supportai.CompletionResult{}, g.err
	}
	if g.result.Provider.Model == "" && g.result.Provider.Protocol == "" {
		g.result = supportai.CompletionResult{
			Text: "hello", StopReason: supportai.StopReasonEnd,
			Usage:         supportai.Usage{InputTokens: 11, OutputTokens: 7},
			Provider:      supportai.ProviderArtifact{ExtensionID: "sforum.ai-core", Protocol: "openai-chat", Model: "deepseek-chat"},
			PromptVersion: "fixture@1", ConfigRevision: 42, LatencyMS: 123,
		}
	}
	return g.result, nil
}

func protocolV2AIRequest(t *testing.T, extensionID string, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	document, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			Extension:      &protocolv2.ExtensionIdentity{ExtensionId: extensionID},
			IdempotencyKey: "ai-1",
		},
		CommandId:      CommandAICompleteID,
		CommandVersion: CommandAICompleteVersion,
		IdempotencyKey: "ai-1",
		Input: &protocolv2.TypedDocument{
			SchemaId:      CommandAICompleteInputSchema,
			SchemaVersion: CommandAICompleteSchemaVersion,
			Value:         document,
		},
	}
}

// protocolV2AIRun 走完整的 Prepare → Execute：真实调用总是先 Prepare，
// 身份与请求构造的校验都住在那里。
func protocolV2AIRun(t *testing.T, definition protocolV2CommandDefinition, ctx context.Context, request *hostv2.CommandRequest) (*protocolV2CommandExecution, error) {
	t.Helper()
	preparation, err := definition.Prepare(ctx, nil, request)
	if err != nil {
		return nil, err
	}
	return definition.Execute(ctx, nil, request, preparation)
}

func protocolV2AIContext(extensionID string) context.Context {
	return ContextWithProtocolV2RuntimeIdentity(context.Background(),
		&protocolv2.ExtensionIdentity{ExtensionId: extensionID})
}

func protocolV2AIValidValues() map[string]any {
	return map[string]any{
		"purpose": "fixture.summary", "costClass": "standard",
		"messages": []any{map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"type": "text", "text": "summarize this"}},
		}},
		"maxTokens": 512,
	}
}

func TestProtocolV2AICompleteDeclaresServiceAuthorityAndCapability(t *testing.T) {
	definition := newProtocolV2AICompleteCommandDefinition(&fakeAIGateway{})
	if definition.ID != CommandAICompleteID || definition.Version != CommandAICompleteVersion {
		t.Fatalf("command id = %#v", definition)
	}
	if definition.ActorMode != protocolV2CommandActorService || len(definition.RequiredPermissions) != 0 {
		t.Fatalf("ai complete authority = %#v", definition)
	}
	if definition.RequiredCapability != "ai.invoke" {
		t.Fatalf("ai complete capability = %q", definition.RequiredCapability)
	}
}

// 核心记账断言：调用方扩展 ID 必须来自已验证身份，ProfileRef 必须为空。
// 前者错则配额记到别人头上，后者错则插件可以绕过成本等级直接寻址供应商。
func TestProtocolV2AICompleteBindsCallerIdentityAndOmitsProfileRef(t *testing.T) {
	gateway := &fakeAIGateway{}
	definition := newProtocolV2AICompleteCommandDefinition(gateway)
	request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
	execution, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.calls != 1 {
		t.Fatalf("gateway calls = %d", gateway.calls)
	}
	if gateway.lastRequest.Metadata.CallerExtensionID != "fixture.ai" {
		t.Fatalf("caller extension = %q", gateway.lastRequest.Metadata.CallerExtensionID)
	}
	if gateway.lastRequest.ProfileRef != "" {
		t.Fatalf("plugin supplied profile ref = %q", gateway.lastRequest.ProfileRef)
	}
	if gateway.lastRequest.Purpose != "fixture.summary" || gateway.lastRequest.CostClass != "standard" {
		t.Fatalf("request = %#v", gateway.lastRequest)
	}
	if len(gateway.lastRequest.Messages) != 1 || len(gateway.lastRequest.Messages[0].Parts) != 1 ||
		gateway.lastRequest.Messages[0].Parts[0].Text != "summarize this" {
		t.Fatalf("messages = %#v", gateway.lastRequest.Messages)
	}
	values := execution.Output.GetValue().AsMap()
	if values["planned"] != false {
		t.Fatalf("planned flag = %#v", values)
	}
	usage, ok := values["usage"].(map[string]any)
	if !ok || usage["inputTokens"] != "11" || usage["outputTokens"] != "7" {
		t.Fatalf("usage = %#v", values["usage"])
	}
	if values["configRevision"] != "42" || values["latencyMs"] != "123" {
		t.Fatalf("trace values = %#v", values)
	}
}

// 请求里自称的身份必须被已验证身份压过。
func TestProtocolV2AICompleteRejectsForgedRequestIdentity(t *testing.T) {
	gateway := &fakeAIGateway{}
	definition := newProtocolV2AICompleteCommandDefinition(gateway)
	request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
	if _, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.other"), request); err == nil {
		t.Fatal("forged request identity accepted")
	}
	if gateway.calls != 0 {
		t.Fatal("completed despite identity mismatch")
	}
}

func TestProtocolV2AICompleteRejectsUnauthenticatedContext(t *testing.T) {
	gateway := &fakeAIGateway{}
	definition := newProtocolV2AICompleteCommandDefinition(gateway)
	request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
	if _, err := protocolV2AIRun(t, definition, context.Background(), request); err == nil {
		t.Fatal("unauthenticated context accepted")
	}
	if gateway.calls != 0 {
		t.Fatal("completed without runtime identity")
	}
}

// 插件不得借 subjectUserId 花掉某个成员的日配额：本命令是 service 模式，
// 永远拿不到委派 actor，因此任何非零 subjectUserId 都必须被拒绝。
func TestProtocolV2AICompleteRejectsSubjectUserWithoutDelegation(t *testing.T) {
	gateway := &fakeAIGateway{}
	definition := newProtocolV2AICompleteCommandDefinition(gateway)
	values := protocolV2AIValidValues()
	values["subjectUserId"] = 4242
	request := protocolV2AIRequest(t, "fixture.ai", values)
	if _, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request); err == nil {
		t.Fatal("subject user accepted without delegated actor")
	}
	if gateway.calls != 0 {
		t.Fatal("completed despite unattested subject user")
	}
}

func TestProtocolV2AICompleteRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
	}{
		{"missing purpose", map[string]any{
			"costClass": "standard", "maxTokens": 16,
			"messages": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "text": "hi"}}}},
		}},
		{"unknown cost class", map[string]any{
			"purpose": "fixture.summary", "costClass": "free", "maxTokens": 16,
			"messages": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "text": "hi"}}}},
		}},
		{"missing cost class", map[string]any{
			"purpose": "fixture.summary", "maxTokens": 16,
			"messages": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "text": "hi"}}}},
		}},
		{"zero tokens", map[string]any{
			"purpose": "fixture.summary", "costClass": "standard", "maxTokens": 0,
			"messages": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"type": "text", "text": "hi"}}}},
		}},
		{"empty messages", map[string]any{
			"purpose": "fixture.summary", "costClass": "standard", "maxTokens": 16, "messages": []any{},
		}},
	}
	for _, test := range tests {
		gateway := &fakeAIGateway{}
		definition := newProtocolV2AICompleteCommandDefinition(gateway)
		request := protocolV2AIRequest(t, "fixture.ai", test.values)
		if _, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request); err == nil {
			t.Fatalf("%s: invalid input accepted", test.name)
		}
		if gateway.calls != 0 {
			t.Fatalf("%s: completed despite invalid input", test.name)
		}
	}
}

// 每个失败原因都必须映射到契约表里的稳定原因码：运营者按它决定动作。
func TestProtocolV2AICompleteMapsGatewayFailuresToStableReasons(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"disabled", &supportai.UnavailableError{Reason: supportai.GateReasonDisabled}, "ai.gateway_disabled"},
		{"budget", &supportai.UnavailableError{Reason: supportai.GateReasonBudgetExceeded}, "ai.budget_exceeded"},
		{"rate", &supportai.UnavailableError{Reason: supportai.GateReasonRateLimited}, "ai.rate_limited"},
		{"quota", &supportai.UnavailableError{Reason: supportai.GateReasonQuotaExceeded}, "ai.quota_exceeded"},
		{"profile", supportai.ErrProfileUnavailable, "ai.profile_unavailable"},
		{"credential", supportai.ErrCredentialMissing, "ai.credential_missing"},
		{"unsafe target", &supportai.UnavailableError{Reason: supportai.ReasonUnsafeTarget}, "ai.unsafe_target"},
		{"provider failed", &supportai.UnavailableError{Reason: supportai.ReasonProviderFailed}, "ai.provider_failed"},
		{"request invalid", supportai.ErrRequestInvalid, "ai.request_invalid"},
		{"gateway unwired", supportai.ErrGatewayUnavailable, "ai.gateway_unavailable"},
	}
	for _, test := range tests {
		gateway := &fakeAIGateway{err: test.err}
		definition := newProtocolV2AICompleteCommandDefinition(gateway)
		request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
		_, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request)
		if err == nil {
			t.Fatalf("%s: failure accepted", test.name)
		}
		var commandErr *protocolV2CommandError
		if !errors.As(err, &commandErr) || commandErr.detail.GetReason() != test.want {
			t.Fatalf("%s: reason = %v, want %s", test.name, commandErr.detail.GetReason(), test.want)
		}
	}
}

// 网关未接线必须是显式的不可用，而不是「成功但什么都没做」：后者会让插件
// 静默退回自建出站，成本重新回到账外。
func TestProtocolV2AICompleteReportsUnavailableGateway(t *testing.T) {
	definition := newProtocolV2AICompleteCommandDefinition(nil)
	request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
	if _, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request); err == nil {
		t.Fatal("nil gateway accepted")
	}
}

// Plan 结果会进入 UI 与审计：它只能携带用途、成本等级与规模，不能把提示词
// 或社区正文带出去。
func TestProtocolV2AICompletePreviewOmitsMessageContent(t *testing.T) {
	definition := newProtocolV2AICompleteCommandDefinition(&fakeAIGateway{})
	request := protocolV2AIRequest(t, "fixture.ai", map[string]any{
		"purpose": "fixture.summary", "costClass": "standard", "maxTokens": 512,
		"messages": []any{map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"type": "text", "text": "private community text"}},
		}},
	})
	plan, err := definition.Preview(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Impact) != 1 {
		t.Fatalf("impact = %#v", plan.Impact)
	}
	for _, field := range []string{plan.Impact[0].GetSummary(), plan.Impact[0].GetResourceId()} {
		if field == "" {
			continue
		}
		if contains := field; contains == "private community text" {
			t.Fatalf("preview leaked content: %q", field)
		}
	}
	if plan.ProjectedResult.GetValue().AsMap()["planned"] != true {
		t.Fatalf("preview projection = %#v", plan.ProjectedResult.GetValue().AsMap())
	}
}

// 工具声明由插件提供：它们参与缓存键与成本，必须逐字到达网关。
func TestProtocolV2AICompleteForwardsToolDeclarations(t *testing.T) {
	gateway := &fakeAIGateway{}
	definition := newProtocolV2AICompleteCommandDefinition(gateway)
	values := protocolV2AIValidValues()
	values["tools"] = []any{map[string]any{
		"name": "fixture-lookup", "description": "look up a thing",
		"parameters": `{"type":"object","properties":{"id":{"type":"string"}}}`,
	}}
	request := protocolV2AIRequest(t, "fixture.ai", values)
	if _, err := protocolV2AIRun(t, definition, protocolV2AIContext("fixture.ai"), request); err != nil {
		t.Fatal(err)
	}
	if len(gateway.lastRequest.Tools) != 1 || gateway.lastRequest.Tools[0].Name != "fixture-lookup" {
		t.Fatalf("tools = %#v", gateway.lastRequest.Tools)
	}
	if string(gateway.lastRequest.Tools[0].Parameters) == "" {
		t.Fatal("tool parameters dropped")
	}
}

// 能力门禁住在命令引擎层（definition 层测不到）：未声明 ai.invoke 的插件必须
// 在 Plan 阶段就被拒，且不产生任何出站调用。
func TestProtocolV2AICompleteEngineEnforcesCapability(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakeAIGateway{}
	engine, err := newProtocolV2CommandEngineWithActorDelegation(
		nil, authority, newProtocolV2AICompleteCommandDefinition(gateway),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := protocolV2AIRequest(t, "fixture.ai", protocolV2AIValidValues())
	// 无能力源：拒绝。
	plan, err := engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without capability source = %#v err=%v", plan.GetError(), err)
	}
	// 有能力源但缺 ai.invoke：仍拒绝。
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without ai.invoke = %#v err=%v", plan.GetError(), err)
	}
	// 授予 ai.invoke 后能力门禁通过（Preview 仍会给出计划，而不是权限错误）。
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.AIInvoke})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError() != nil {
		t.Fatalf("plan after ai.invoke grant = %#v err=%v", plan.GetError(), err)
	}
	if gateway.calls != 0 {
		t.Fatal("Plan produced an outbound completion")
	}
}
