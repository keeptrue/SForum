package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// A2 的工具轨迹（ToolTrace）验收用例。
//
// 轨迹是运营者第一次能看见「模型到底调了什么工具、拿到了什么、为什么没拿到」的
// 数据源，因此这些用例断言的是**可解释性**而不是内部结构：五种结局必须能被区分，
// 参数必须已脱敏，原始字节数必须让「被截断」这件事本身可见。

// tracedRun 跑一次带工具的 run，返回结果与轨迹。
func tracedRun(t *testing.T, settings ai.Settings, invoker ai.ProviderInvoker, tools ...ai.Tool) (ai.RunResult, *fakeTraces) {
	t.Helper()
	orchestrator, _, traces := newOrchestrator(t, settings, invoker, tools...)
	result, err := orchestrator.Run(context.Background(), replyRunInput())
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	return result, traces
}

// T-A2-1：正常路径。一次成功的工具调用必须留下完整的一条记录：序号、工具名、
// 已脱敏参数、结果摘要与延迟。
func TestToolTraceRecordsSuccessfulCall(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "命中 1 条：主题 42《并发》"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("建议用带缓冲的 channel。"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	entry := result.ToolTrace[0]
	if entry.Step != 1 {
		t.Fatalf("step = %d, want the model-call ordinal 1", entry.Step)
	}
	if entry.Tool != "forum-search" {
		t.Fatalf("tool = %q", entry.Tool)
	}
	if entry.Arguments != `{"query":"并发"}` {
		t.Fatalf("arguments = %q", entry.Arguments)
	}
	if entry.Failure != "" {
		t.Fatalf("a successful call must not carry a failure: %q", entry.Failure)
	}
	if entry.ResultBytes <= 0 {
		t.Fatalf("resultBytes = %d", entry.ResultBytes)
	}
	if entry.LatencyMS < 0 {
		t.Fatalf("latencyMs = %d", entry.LatencyMS)
	}
	if entry.ResultDigest == "" {
		t.Fatal("a successful call must record what the model saw")
	}
}

// T-A2-2：未知工具。模型编了一个不存在的名字，轨迹必须标 unknown_tool，而整次
// run 仍然收尾（有正文）——这不是失败，是模型越界。
func TestToolTraceMarksUnknownTool(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-delete-everything"),
		finalResponse("没有这个工具，我直接回答。"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	if got := result.ToolTrace[0].Failure; got != ai.ToolFailureUnknownTool {
		t.Fatalf("failure = %q, want %q", got, ai.ToolFailureUnknownTool)
	}
	if result.Text != "没有这个工具，我直接回答。" {
		t.Fatalf("run must still finish with text: %q", result.Text)
	}
	if tool.invoked != 0 {
		t.Fatal("an unknown tool must never reach the registry")
	}
}

// T-A2-3：不在白名单。工具确实登记过，但用途没有点名它——这与「不存在」是
// 两件不同的事，必须能分开：前者是配置问题，后者是模型幻觉。
func TestToolTraceMarksToolOutsideAllowlist(t *testing.T) {
	registered := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	unlisted := &fakeTool{name: "user-search", result: ai.ToolResult{Content: "不该被调用"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "user-search"),
		finalResponse("我用不上那个工具。"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, registered, unlisted)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	if got := result.ToolTrace[0].Failure; got != ai.ToolFailureNotAllowed {
		t.Fatalf("failure = %q, want %q", got, ai.ToolFailureNotAllowed)
	}
	if unlisted.invoked != 0 {
		t.Fatal("a tool outside the allowlist must not be invoked")
	}
}

// T-A2-4：步数预算耗尽。剩余预算用完时模型仍要求调用，这类调用不经过 execute()，
// 必须由循环自己补一条 trace——否则轨迹解释不了「预算用完后模型还在乱撞」。
func TestToolTraceRecordsStepExhaustedCalls(t *testing.T) {
	settings := enabledSettings()
	budget := 2
	settings.Gates.ToolCallsPerReply = &budget
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	// 一次响应里要三个调用：前两个吃掉全部预算，第三个落在 remaining<=0 分支。
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		{
			StopReason: ai.StopReasonTool,
			ToolCalls: []ai.ToolCall{
				{ID: "call_1", Name: "forum-search", Arguments: `{"query":"并发"}`},
				{ID: "call_2", Name: "forum-search", Arguments: `{"query":"并发"}`},
				{ID: "call_3", Name: "forum-search", Arguments: `{"query":"并发"}`},
			},
			Usage: ai.Usage{InputTokens: 10, OutputTokens: 2},
		},
		finalResponse("预算用完了，这是基于已有信息的回答。"),
	}}
	result, _ := tracedRun(t, settings, invoker, tool)

	if result.ToolCalls != 2 {
		t.Fatalf("tool calls = %d", result.ToolCalls)
	}
	if len(result.ToolTrace) != 3 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	last := result.ToolTrace[2]
	if last.Failure != ai.ToolFailureStepExhausted {
		t.Fatalf("failure = %q, want %q", last.Failure, ai.ToolFailureStepExhausted)
	}
	if last.ArgumentsBytes == 0 {
		t.Fatalf("an exhausted call must still carry its arguments size: %+v", last)
	}
	// 被拒绝的调用与同批执行的调用共享同一个 Step（那次模型调用的序号）。
	if last.Step != 1 {
		t.Fatalf("step = %d, want the ordinal of the model call that produced it", last.Step)
	}
	// 前面两条是真实执行，没有失败标记。
	for index := 0; index < 2; index++ {
		if result.ToolTrace[index].Failure != "" {
			t.Fatalf("executed call %d must not carry a failure: %+v", index, result.ToolTrace[index])
		}
	}
}

// T-A2-5：结果预算耗尽。
//
// 运行期累计预算（MaxToolResultBytes 的倍数，见 MaxToolResultRunBytes）里没有
// 可注入的每-run 配置点，因此这里用「单条上限放到与累计预算同量级 + 一条超长
// 结果」让第一次调用就吃掉整个 run 预算，第二次自然落在 budget<=0 分支上。
// 断言的目标不变：第二次调用必须被标为 result_budget_exhausted。
func TestToolTraceMarksResultBudgetExhausted(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: strings.Repeat("长", ai.MaxToolResultRunBytes)}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		toolCallResponse("call_2", "forum-search"),
		finalResponse("基于已有信息回答。"),
	}}
	registry := ai.NewToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: enabledSettings()},
		Usage:    &fakeUsage{},
		Traces:   &fakeTraces{},
		Creds:    &fakeCreds{value: "sk-test"},
		Invoker:  invoker,
		Clock:    func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) },
	})
	orchestrator := ai.NewOrchestrator(ai.OrchestratorConfig{
		Gateway:        gateway,
		Registry:       registry,
		MaxResultBytes: ai.MaxToolResultRunBytes,
	})
	result, err := orchestrator.Run(context.Background(), replyRunInput())
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(result.ToolTrace) != 2 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	if result.ToolTrace[0].Failure != "" {
		t.Fatalf("first call must succeed: %+v", result.ToolTrace[0])
	}
	if got := result.ToolTrace[1].Failure; got != ai.ToolFailureBudgetExhausted {
		t.Fatalf("failure = %q, want %q", got, ai.ToolFailureBudgetExhausted)
	}
	if tool.invoked != 1 {
		t.Fatalf("the exhausted call must not reach the tool: invoked=%d", tool.invoked)
	}
}

// T-A2-6：内部故障。工具返回 error 时轨迹标 internal_error，而回填给模型的文本
// 不得包含错误原文——那段话会被发布到社区。
func TestToolTraceMarksInternalErrorWithoutLeakingIt(t *testing.T) {
	tool := &fakeTool{name: "forum-search", err: errors.New("boom")}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("工具挂了，我直接回答。"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	if got := result.ToolTrace[0].Failure; got != ai.ToolFailureInternal {
		t.Fatalf("failure = %q, want %q", got, ai.ToolFailureInternal)
	}
	body := wireBody(t, invoker.wires[1])
	if strings.Contains(body, "boom") {
		t.Fatalf("internal error text leaked to the model: %s", body)
	}
}

// T-A2-7：参数截断。2KB 参数必须被截到上限内、保持合法 UTF-8，同时保留截断前的
// 原始字节数——「被截了」这件事本身要可见。
func TestToolTraceTruncatesArgumentsButKeepsOriginalSize(t *testing.T) {
	const size = 2048
	longArguments := `{"query":"` + strings.Repeat("并", size/3) + `"}`
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		{
			StopReason: ai.StopReasonTool,
			ToolCalls:  []ai.ToolCall{{ID: "call_1", Name: "forum-search", Arguments: longArguments}},
			Usage:      ai.Usage{InputTokens: 10, OutputTokens: 2},
		},
		finalResponse("好"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	entry := result.ToolTrace[0]
	if entry.ArgumentsBytes != len(longArguments) {
		t.Fatalf("argumentsBytes = %d, want the pre-truncation size %d", entry.ArgumentsBytes, len(longArguments))
	}
	if len(entry.Arguments) > ai.MaxToolTraceArgumentsBytes {
		t.Fatalf("arguments not truncated: %d bytes", len(entry.Arguments))
	}
	if !utf8.ValidString(entry.Arguments) {
		t.Fatal("truncation must not split a multi-byte character")
	}
}

// T-A2-8：脱敏。工具参数是模型输出，从未经过网关的 ApplyRedaction；轨迹侧必须
// 自己脱敏，否则邮箱与 IP 会以明文进日志。
func TestToolTraceRedactsArguments(t *testing.T) {
	settings := enabledSettings()
	settings.Redaction = ai.RedactionSettings{Enabled: true, RedactEmail: true, RedactIP: true, RedactPhone: true}
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "联系人 a@b.com 在 10.1.2.3"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		{
			StopReason: ai.StopReasonTool,
			ToolCalls:  []ai.ToolCall{{ID: "call_1", Name: "forum-search", Arguments: `{"query":"a@b.com"}`}},
			Usage:      ai.Usage{InputTokens: 10, OutputTokens: 2},
		},
		finalResponse("好"),
	}}
	result, _ := tracedRun(t, settings, invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	entry := result.ToolTrace[0]
	if strings.Contains(entry.Arguments, "a@b.com") {
		t.Fatalf("arguments must be redacted: %q", entry.Arguments)
	}
	if !strings.Contains(entry.Arguments, ai.RedactedEmailPlaceholder) {
		t.Fatalf("arguments = %q", entry.Arguments)
	}
	if strings.Contains(entry.ResultDigest, "a@b.com") || strings.Contains(entry.ResultDigest, "10.1.2.3") {
		t.Fatalf("result digest must be redacted: %q", entry.ResultDigest)
	}
}

// T-A2-9：顺序。多次调用必须按 Step 单调不减，否则运营者无法还原时间线。
func TestToolTraceKeepsStepOrder(t *testing.T) {
	settings := enabledSettings()
	budget := 3
	settings.Gates.ToolCallsPerReply = &budget
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		toolCallResponse("call_2", "forum-search"),
		toolCallResponse("call_3", "forum-search"),
		finalResponse("总结。"),
	}}
	result, _ := tracedRun(t, settings, invoker, tool)

	if len(result.ToolTrace) != 3 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	for index := 1; index < len(result.ToolTrace); index++ {
		if result.ToolTrace[index].Step < result.ToolTrace[index-1].Step {
			t.Fatalf("steps must be monotonic: %+v", result.ToolTrace)
		}
	}
	if result.ToolTrace[0].Step != 1 || result.ToolTrace[2].Step != 3 {
		t.Fatalf("steps must be the model-call ordinals: %+v", result.ToolTrace)
	}
}

// 工具自身报告 IsError 时不算编排层失败：IsError 记录事实，Failure 留空。
// 混同两者会让「参数非法」看起来像「宿主故障」。
func TestToolTraceRecordsToolReportedErrorAsResultNotFailure(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "query 参数缺失", IsError: true}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("我换个查法。"),
	}}
	result, _ := tracedRun(t, enabledSettings(), invoker, tool)

	if len(result.ToolTrace) != 1 {
		t.Fatalf("tool trace = %+v", result.ToolTrace)
	}
	entry := result.ToolTrace[0]
	if !entry.IsError {
		t.Fatalf("isError = false, want true: %+v", entry)
	}
	if entry.Failure != "" {
		t.Fatalf("a tool-reported error is not an orchestration failure: %q", entry.Failure)
	}
}

// 轨迹必须能被序列化进日志（A2 步骤 6 依赖它），且字段名是运营者可见契约。
func TestToolTraceEntryJSONShape(t *testing.T) {
	entry := ai.ToolTraceEntry{
		Step: 2, Tool: "forum-search", Arguments: `{"query":"x"}`, ArgumentsBytes: 13,
		ResultBytes: 42, ResultDigest: "摘要", IsError: true,
		Failure: ai.ToolFailureNotAllowed, LatencyMS: 7,
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"step":2`, `"tool":"forum-search"`, `"argumentsBytes":13`,
		`"resultBytes":42`, `"isError":true`, `"failure":"not_allowed"`, `"latencyMs":7`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("%s missing from %s", key, encoded)
		}
	}
}
