package aireply_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// GatewayCompleter 的 run id 透出契约。
//
// 这条链是 T5 的 B4/B5 的地基：`ai_citations.run_id` 与 `TopicState.LastRunID`
// 都取自 `CompletionOutput.RunID`。它若恒为空串，落库全是空值，B4/B5 等于白落，
// 而且不会有任何报错——所以必须有测试钉住两个分支各自的语义：
//
//   - 工具分支：编排器生成了 run id，必须原样透出（且等于执行记录的归属）；
//   - 非工具分支：单次 gateway.Execute 没有「run」概念，必须留空串，不许编。

// completerSettings 是带可用 profile 的启用配置。
func completerSettings() supportai.Settings {
	settings := supportai.RecommendedSettings()
	settings.Enabled = true
	settings.Revision = 3
	for index := range settings.Profiles {
		if settings.Profiles[index].ID == "deepseek" {
			settings.Profiles[index].Enabled = true
		}
	}
	return settings
}

type completerSettingsStore struct{ settings supportai.Settings }

func (s *completerSettingsStore) GetSettings(context.Context) (supportai.Settings, error) {
	return s.settings, nil
}

func (s *completerSettingsStore) SaveSettings(_ context.Context, settings supportai.Settings, _ int64) (supportai.Settings, error) {
	return settings, nil
}

func (s *completerSettingsStore) ResetSettings(_ context.Context, settings supportai.Settings, _ int64) (supportai.Settings, error) {
	return settings, nil
}

type completerUsage struct{}

func (completerUsage) Snapshot(context.Context, time.Time, string, int64) (supportai.UsageByScope, error) {
	return supportai.UsageByScope{}, nil
}

func (completerUsage) Record(context.Context, time.Time, supportai.UsageEntry) error { return nil }

// completerTraces 记录执行记录，供断言「结果的 run id == 记录的归属」。
type completerTraces struct{ records []supportai.ExecutionRecord }

func (c *completerTraces) RecordExecution(_ context.Context, entry supportai.ExecutionRecord) error {
	c.records = append(c.records, entry)
	return nil
}

func (c *completerTraces) ListExecutions(context.Context, int) ([]supportai.ExecutionRecord, error) {
	return c.records, nil
}

func (c *completerTraces) ListRunExecutions(_ context.Context, resourceType, resourceID string, limit int) ([]supportai.ExecutionRecord, error) {
	out := []supportai.ExecutionRecord{}
	for _, record := range c.records {
		if record.ResourceType == resourceType && record.ResourceID == resourceID {
			out = append(out, record)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type completerCreds struct{}

func (completerCreds) ResolveAIKey(context.Context, string) (string, error) { return "sk-test", nil }

// completerInvoker 按调用次序返回脚本化响应。
type completerInvoker struct {
	responses []supportai.WireResponse
	calls     int
}

func (c *completerInvoker) Invoke(context.Context, supportai.Profile, supportai.WireRequest) (supportai.WireResponse, error) {
	index := c.calls
	c.calls++
	if index >= len(c.responses) {
		index = len(c.responses) - 1
	}
	return c.responses[index], nil
}

// completerTool 是一个恒成功的只读工具。
type completerTool struct{ name string }

func (c *completerTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name:        c.name,
		Description: "测试工具",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (c *completerTool) Invoke(context.Context, supportai.ToolCall, supportai.ToolContext) (supportai.ToolResult, error) {
	return supportai.ToolResult{Content: "命中 1 条"}, nil
}

// newCompleter 装配真实的 Gateway + Orchestrator（不起网络：invoker 是 fake）。
func newCompleter(t *testing.T, invoker supportai.ProviderInvoker, tools ...supportai.Tool) (*aireply.GatewayCompleter, *completerTraces) {
	t.Helper()
	traces := &completerTraces{}
	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: &completerSettingsStore{settings: completerSettings()},
		Usage:    completerUsage{},
		Traces:   traces,
		Creds:    completerCreds{},
		Invoker:  invoker,
		Purposes: supportai.DefaultPurposeRegistry(),
	})
	registry := supportai.NewToolRegistry()
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}
	orchestrator := supportai.NewOrchestrator(supportai.OrchestratorConfig{
		Gateway:  gateway,
		Registry: registry,
	})
	return aireply.NewGatewayCompleter(gateway, orchestrator), traces
}

func toolStepResponse(id, name string) supportai.WireResponse {
	return supportai.WireResponse{
		StopReason: supportai.StopReasonTool,
		ToolCalls:  []supportai.ToolCall{{ID: id, Name: name, Arguments: `{"query":"并发"}`}},
		Usage:      supportai.Usage{InputTokens: 10, OutputTokens: 2},
	}
}

func finalAnswer(text string) supportai.WireResponse {
	return supportai.WireResponse{Text: text, StopReason: supportai.StopReasonEnd, Usage: supportai.Usage{InputTokens: 5, OutputTokens: 3}}
}

func toolCompletionInput() aireply.CompletionInput {
	return aireply.CompletionInput{
		Purpose:       supportai.CorePurposeForumReply,
		CostClass:     supportai.CostClassEconomy,
		PromptVersion: supportai.CoreReplyPromptVersion,
		System:        "你是社区助手。",
		User:          "主题：Go 并发；问题：怎么用 channel？",
		MaxTokens:     512,
		SubjectUserID: 77,
		Tools:         []string{"forum-search"},
		ToolContext:   supportai.ToolContext{ViewerUserID: 77, TopicID: 42},
	}
}

// 工具分支：run id 必须原样透出，并与本次 run 的每条执行记录一致。
func TestGatewayCompleterPropagatesRunIDOnToolPath(t *testing.T) {
	invoker := &completerInvoker{responses: []supportai.WireResponse{
		toolStepResponse("call_1", "forum-search"),
		finalAnswer("建议用带缓冲的 channel。"),
	}}
	completer, traces := newCompleter(t, invoker, &completerTool{name: "forum-search"})

	output, err := completer.Complete(context.Background(), toolCompletionInput())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if output.RunID == "" {
		t.Fatal("the tool path must report the run id it used")
	}
	if len(traces.records) == 0 {
		t.Fatal("expected execution records for the tool run")
	}
	for _, record := range traces.records {
		if record.ResourceID != output.RunID {
			t.Fatalf("record carries %q but the completion reports %q", record.ResourceID, output.RunID)
		}
		if record.ResourceType != supportai.ResourceTypeRun {
			t.Fatalf("resourceType = %q", record.ResourceType)
		}
	}
}

// 非工具分支：没有 run，run id 必须是空串（不许编一个出来）。
func TestGatewayCompleterLeavesRunIDEmptyOnPlainPath(t *testing.T) {
	invoker := &completerInvoker{responses: []supportai.WireResponse{finalAnswer("直接回答，没有工具。")}}
	completer, traces := newCompleter(t, invoker, &completerTool{name: "forum-search"})

	input := toolCompletionInput()
	input.Tools = nil // 无工具 → 走单次 Execute 分支
	output, err := completer.Complete(context.Background(), input)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if output.RunID != "" {
		t.Fatalf("the plain path must not fabricate a run id, got %q", output.RunID)
	}
	if output.Text != "直接回答，没有工具。" {
		t.Fatalf("text = %q", output.Text)
	}
	// 非工具分支确实写了一条执行记录，但它不属于任何 run——这正是不该编 id 的理由：
	// 编了之后消费方会拿它去查一组并不存在的记录。
	for _, record := range traces.records {
		if record.ResourceID != "" {
			t.Fatalf("the plain path must not attribute its record to a run: %+v", record)
		}
	}
}

// 错误路径：run 中途失败时，已产生的 run id 与已完成的工具调用必须一起带回。
func TestGatewayCompleterPropagatesRunIDAndPartialTraceOnFailure(t *testing.T) {
	invoker := &failingSecondInvoker{first: toolStepResponse("call_1", "forum-search")}
	completer, traces := newCompleter(t, invoker, &completerTool{name: "forum-search"})

	output, err := completer.Complete(context.Background(), toolCompletionInput())
	if err == nil {
		t.Fatal("expected the second call to fail")
	}
	if output.RunID == "" {
		t.Fatal("a failed run must still report the run id it already used")
	}
	if output.ToolCalls != 1 || len(output.ToolTrace) != 1 {
		t.Fatalf("partial result lost the completed tool call: %+v", output)
	}
	for _, record := range traces.records {
		if record.ResourceID != output.RunID {
			t.Fatalf("record = %+v, result run id = %q", record, output.RunID)
		}
	}
}

// failingSecondInvoker 第一次成功、之后返回错误。
type failingSecondInvoker struct {
	first supportai.WireResponse
	calls int
}

func (f *failingSecondInvoker) Invoke(context.Context, supportai.Profile, supportai.WireRequest) (supportai.WireResponse, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return supportai.WireResponse{}, errors.New("provider exploded mid-run")
}

// 端到端：真实的 GatewayCompleter（工具分支）→ Generate。这是 B4/B5 能拿到非空
// run_id 的直接证明——它们落库用的就是链末端这个值。
func TestRunIDReachesGeneratorThroughGatewayCompleter(t *testing.T) {
	invoker := &completerInvoker{responses: []supportai.WireResponse{
		toolStepResponse("call_1", "forum-search"),
		finalAnswer("建议用带缓冲的 channel。"),
	}}
	completer, traces := newCompleter(t, invoker, &completerTool{name: "forum-search"})

	poster := &fakePoster{}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil)
	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(poster.inputs) != 1 {
		t.Fatalf("posted = %+v", poster.inputs)
	}
	if len(traces.records) == 0 {
		t.Fatal("expected execution records")
	}
	runID := traces.records[0].ResourceID
	if runID == "" {
		t.Fatal("the reply path must leave a non-empty run id in ai_executions")
	}
	for _, record := range traces.records {
		if record.ResourceID != runID {
			t.Fatalf("records disagree on the run id: %+v", traces.records)
		}
	}
}
