package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// A3 的关联 id（correlation id）验收用例。
//
// 目标不是「多了一个字段」，而是「一次 run 的每一次模型调用都能被聚合成一条
// 时间线」。因此这些用例断言的是聚合关系与四条执行路径的覆盖，而不是字段存在性。

// runRecordRequest 返回带 run 归属的请求。
func runRecordRequest(runID string) ai.CompletionRequest {
	request := validRequest()
	request.Metadata.ResourceType = ai.ResourceTypeRun
	request.Metadata.ResourceID = runID
	return request
}

// T-A3-1：一次 2 步 run 的每一次调用都带着同一个非空 run id。
// 这是整条任务的地基：漏一步，回放里就会出现「孤立记录」。
func TestRunIDReachesEveryModelCall(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "命中 1 条"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("建议用带缓冲的 channel。"),
	}}
	_, traces := tracedRun(t, enabledSettings(), invoker, tool)

	if len(traces.records) != 2 {
		t.Fatalf("records = %d, want one per model call", len(traces.records))
	}
	first, second := traces.records[0], traces.records[1]
	if first.ResourceID == "" || second.ResourceID == "" {
		t.Fatalf("every record must carry a run id: %+v", traces.records)
	}
	if first.ResourceID != second.ResourceID {
		t.Fatalf("a single run must share one id: %q vs %q", first.ResourceID, second.ResourceID)
	}
	for _, record := range traces.records {
		if record.ResourceType != ai.ResourceTypeRun {
			t.Fatalf("resourceType = %q, want %q", record.ResourceType, ai.ResourceTypeRun)
		}
	}
	// 聚合查询必须真能取回这两条（fake 按归属过滤）。
	timeline, err := traces.ListRunExecutions(context.Background(), ai.ResourceTypeRun, first.ResourceID, 50)
	if err != nil {
		t.Fatalf("list run executions: %v", err)
	}
	if len(timeline) != 2 {
		t.Fatalf("timeline = %+v", timeline)
	}
}

// T-A3-2：四条执行路径各一条记录，每条都必须带 ai.run 归属。
// 漏掉任何一条，那一类失败（例如 denied）在回放里就是不可见的。
func TestRunIDCoversAllExecutionPaths(t *testing.T) {
	type path struct {
		name   string
		build  func(t *testing.T) (*ai.Gateway, *fakeTraces, *fakeInvoker, ai.CompletionRequest)
		status string
	}
	paths := []path{
		{
			name: "denied: gateway disabled",
			build: func(t *testing.T) (*ai.Gateway, *fakeTraces, *fakeInvoker, ai.CompletionRequest) {
				gateway, _, traces, invoker, _ := newGateway(t, ai.RecommendedSettings())
				return gateway, traces, invoker, runRecordRequest("run-disabled")
			},
			status: ai.ExecutionStatusDenied,
		},
		{
			name: "denied: gate",
			build: func(t *testing.T) (*ai.Gateway, *fakeTraces, *fakeInvoker, ai.CompletionRequest) {
				// 把该扩展的当日调用打满，让它撞上扩展作用域闸门。
				gateway, _, traces, invoker, _ := newGatewayWithUsage(t, enabledSettings(), ai.UsageByScope{
					Extension: ai.UsageSnapshot{DayCalls: 500},
				})
				request := runRecordRequest("run-gated")
				request.Metadata.CallerExtensionID = "vendor.plugin"
				return gateway, traces, invoker, request
			},
			status: ai.ExecutionStatusDenied,
		},
		{
			name: "failed: provider",
			build: func(t *testing.T) (*ai.Gateway, *fakeTraces, *fakeInvoker, ai.CompletionRequest) {
				gateway, _, traces, invoker, _ := newGateway(t, enabledSettings())
				invoker.err = errors.New("provider exploded")
				return gateway, traces, invoker, runRecordRequest("run-failed")
			},
			status: ai.ExecutionStatusFailed,
		},
		{
			name: "succeeded",
			build: func(t *testing.T) (*ai.Gateway, *fakeTraces, *fakeInvoker, ai.CompletionRequest) {
				gateway, _, traces, invoker, _ := newGateway(t, enabledSettings())
				return gateway, traces, invoker, runRecordRequest("run-ok")
			},
			status: ai.ExecutionStatusSucceeded,
		},
	}

	for _, item := range paths {
		t.Run(item.name, func(t *testing.T) {
			gateway, traces, _, request := item.build(t)
			_, _ = gateway.Execute(context.Background(), request)
			if len(traces.records) != 1 {
				t.Fatalf("records = %+v", traces.records)
			}
			record := traces.records[0]
			if record.Status != item.status {
				t.Fatalf("status = %q, want %q", record.Status, item.status)
			}
			if record.ResourceType != ai.ResourceTypeRun {
				t.Fatalf("resourceType = %q", record.ResourceType)
			}
			if record.ResourceID != request.Metadata.ResourceID {
				t.Fatalf("resourceId = %q, want %q", record.ResourceID, request.Metadata.ResourceID)
			}
		})
	}
}

// T-A3-3：缓存命中。命中的记录带 CacheHit=true，且与首次记录共享同一个 run id
// ——但这是两次不同的 run（各自生成 id），因此这里断言的是「同一请求的第一次与
// 第二次调用各自成 run，且命中那次仍然记录了归属」。
func TestCacheHitRecordCarriesItsOwnRunID(t *testing.T) {
	gateway, _, traces, invoker, _ := newGateway(t, enabledSettings())

	first := runRecordRequest("run-first")
	first.System = "缓存命中的判定基准"
	if _, err := gateway.Execute(context.Background(), first); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	second := runRecordRequest("run-second")
	second.System = first.System
	if _, err := gateway.Execute(context.Background(), second); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if invoker.calls != 1 {
		t.Fatalf("invoker calls = %d, want 1 (second call must hit the cache)", invoker.calls)
	}
	if len(traces.records) != 2 {
		t.Fatalf("records = %+v", traces.records)
	}
	hit := traces.records[1]
	if !hit.CacheHit {
		t.Fatalf("second record must be marked as a cache hit: %+v", hit)
	}
	if hit.ResourceID != "run-second" {
		t.Fatalf("resourceId = %q, want the id of the run that hit the cache", hit.ResourceID)
	}
	if hit.ResourceID == traces.records[0].ResourceID {
		t.Fatal("two separate runs must not share an id")
	}
}

// T-A3-4：不同 run 不串。连续两次 Run 必须得到两个不同的 id。
func TestConsecutiveRunsDoNotShareARunID(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("回答一。"),
		toolCallResponse("call_2", "forum-search"),
		finalResponse("回答二。"),
	}}
	orchestrator, _, traces := newOrchestrator(t, enabledSettings(), invoker, tool)

	if _, err := orchestrator.Run(context.Background(), replyRunInput()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := orchestrator.Run(context.Background(), replyRunInput()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(traces.records) < 2 {
		t.Fatalf("records = %+v", traces.records)
	}
	ids := map[string]bool{}
	for _, record := range traces.records {
		if record.ResourceID == "" {
			t.Fatalf("record without a run id: %+v", record)
		}
		ids[record.ResourceID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("two runs produced %d distinct ids: %v", len(ids), ids)
	}
}

// T-A3-5（安全带）：缓存键不含 run id。有人「顺手」把 metadata 整个写进 CacheKey
// 时，这条会立刻失败——否则每次 run 都是新键，缓存永久失效且不报错，只有账单上涨。
func TestCacheKeyIgnoresResourceIdentity(t *testing.T) {
	settings := enabledSettings()
	profile, err := settings.ResolveProfile(ai.CostClassEconomy, "")
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}

	base := validRequest()
	base.Metadata.CallerExtensionID = "vendor.plugin"

	withRunA := base
	withRunA.Metadata.ResourceType = ai.ResourceTypeRun
	withRunA.Metadata.ResourceID = "run-a"

	withRunB := base
	withRunB.Metadata.ResourceType = ai.ResourceTypeRun
	withRunB.Metadata.ResourceID = "run-b"

	keyBase := ai.CacheKey(base, profile, 7)
	if got := ai.CacheKey(withRunA, profile, 7); got != keyBase {
		t.Fatalf("run id leaked into the cache key: %s vs %s", got, keyBase)
	}
	if got := ai.CacheKey(withRunB, profile, 7); got != keyBase {
		t.Fatalf("different run ids must not change the key: %s vs %s", got, keyBase)
	}

	// 反向控制：真正应当影响键的维度仍然影响它，否则这条测试会因为「键退化成常量」而假绿。
	changed := withRunA
	changed.Messages = []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "另一段正文"}}}}
	if ai.CacheKey(changed, profile, 7) == keyBase {
		t.Fatal("message body must still affect the cache key")
	}
	if ai.CacheKey(withRunA, profile, 8) == keyBase {
		t.Fatal("config revision must still affect the cache key")
	}
}

// runID 必须是 32 位 hex（16 字节），且相邻两次不同。格式进契约是因为它会被
// 写进 URL（A4 的 /runs/:runId），长度与字符集必须可判定。
func TestRunIDShapeIsStable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
		invoker := &scriptedInvoker{responses: []ai.WireResponse{finalResponse("回答。")}}
		_, traces := tracedRun(t, enabledSettings(), invoker, tool)
		if len(traces.records) != 1 {
			t.Fatalf("records = %+v", traces.records)
		}
		id := traces.records[0].ResourceID
		if len(id) != 32 {
			t.Fatalf("run id %q is %d chars, want 32 hex chars", id, len(id))
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("run id %q is not lowercase hex", id)
		}
		if seen[id] {
			t.Fatalf("duplicate run id %q within 32 runs", id)
		}
		seen[id] = true
	}
}

// 调用方传入的 Metadata 不得被改写：Run 拷贝后再注入 run 归属。
func TestRunDoesNotMutateCallerMetadata(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{finalResponse("回答。")}}
	orchestrator, _, _ := newOrchestrator(t, enabledSettings(), invoker, tool)

	input := replyRunInput()
	input.Metadata = ai.Metadata{CallerExtensionID: "core.reply"}
	if _, err := orchestrator.Run(context.Background(), input); err != nil {
		t.Fatalf("run: %v", err)
	}
	if input.Metadata.ResourceID != "" || input.Metadata.ResourceType != "" {
		t.Fatalf("caller metadata was mutated: %+v", input.Metadata)
	}
}

// 时钟注入不得影响 run id 的唯一性（run id 不走可注入 clock）。
func TestRunIDIsIndependentOfInjectedClock(t *testing.T) {
	registry := ai.NewToolRegistry()
	if err := registry.Register(&fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	fixed := func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	traces := &fakeTraces{}
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: enabledSettings()},
		Usage:    &fakeUsage{},
		Traces:   traces,
		Creds:    &fakeCreds{value: "sk-test"},
		Invoker:  &scriptedInvoker{responses: []ai.WireResponse{finalResponse("回答。")}},
		Clock:    fixed,
	})
	orchestrator := ai.NewOrchestrator(ai.OrchestratorConfig{Gateway: gateway, Registry: registry})
	if _, err := orchestrator.Run(context.Background(), replyRunInput()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := orchestrator.Run(context.Background(), replyRunInput()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(traces.records) != 2 {
		t.Fatalf("records = %+v", traces.records)
	}
	if traces.records[0].ResourceID == traces.records[1].ResourceID {
		t.Fatal("a frozen clock must not collapse two runs into one id")
	}
}

// RunResult.RunID 是本条链的枢纽：T5 的 B4（ai_citations.run_id）与 B5
// （TopicState.LastRunID）都以它为键。它若只存在于内部 metadata，回复与执行记录
// 就永远串不起来——落库全是空串，B4/B5 等于白落。
//
// 三条断言对应三条路径：成功、失败（早返回）、以及「与执行记录同一个 id」。
func TestRunResultCarriesRunIDOnSuccess(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "命中 1 条"}}
	invoker := &scriptedInvoker{responses: []ai.WireResponse{
		toolCallResponse("call_1", "forum-search"),
		finalResponse("建议用带缓冲的 channel。"),
	}}
	result, traces := tracedRun(t, enabledSettings(), invoker, tool)

	if result.RunID == "" {
		t.Fatal("a tool run must report its run id on the result")
	}
	// 与执行记录的归属逐条相等：两个 id 就是两个真相。
	if len(traces.records) == 0 {
		t.Fatal("expected execution records")
	}
	for _, record := range traces.records {
		if record.ResourceID != result.RunID {
			t.Fatalf("record %d carries %q but the result reports %q", record.ID, record.ResourceID, result.RunID)
		}
	}
	// 聚合查询用结果里的 id 必须能取回这一次 run 的全部记录。
	timeline, err := traces.ListRunExecutions(context.Background(), ai.ResourceTypeRun, result.RunID, 50)
	if err != nil {
		t.Fatalf("list run executions: %v", err)
	}
	if len(timeline) != len(traces.records) {
		t.Fatalf("timeline for run %q = %d records, want %d", result.RunID, len(timeline), len(traces.records))
	}
}

// 错误路径也必须带 run id。Run 有多条早返回路径（网关失败、工具循环卡死），
// 它们都 `return result, err`；回填若留在循环之后，失败那次 run 的真实调用记录
// 就变成查不回来的孤立行。
func TestRunResultCarriesRunIDOnEarlyFailure(t *testing.T) {
	tool := &fakeTool{name: "forum-search", result: ai.ToolResult{Content: "结果"}}
	// 第一次调用要求工具，第二次网关失败。
	invoker := &failingAfterInvoker{first: toolCallResponse("call_1", "forum-search")}
	orchestrator, _, traces := newOrchestrator(t, enabledSettings(), invoker, tool)

	result, err := orchestrator.Run(context.Background(), replyRunInput())
	if err == nil {
		t.Fatal("expected the gateway failure to surface")
	}
	if result.RunID == "" {
		t.Fatal("a failed run must still report the run id it already used")
	}
	// 失败前那一步是真实发生过的调用，它的记录必须与结果的 id 一致。
	if len(traces.records) == 0 {
		t.Fatal("the step before the failure must have left an execution record")
	}
	for _, record := range traces.records {
		if record.ResourceID != result.RunID {
			t.Fatalf("record %s = %q, result = %q", record.ResourceID, record.ResourceID, result.RunID)
		}
	}
}

// failingAfterInvoker 第一次返回给定响应，之后返回错误：用来制造「run 中途网关失败」。
type failingAfterInvoker struct {
	first ai.WireResponse
	calls int
}

func (f *failingAfterInvoker) Invoke(context.Context, ai.Profile, ai.WireRequest) (ai.WireResponse, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return ai.WireResponse{}, errors.New("provider exploded mid-run")
}
