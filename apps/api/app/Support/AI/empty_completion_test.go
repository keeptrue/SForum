package ai_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 200 但没有正文不是成功。这条断言来自一次真实故障：控制台一整列 succeeded、
// 0 ms、0/0 token，社区一条回复都没有——推理模型把预算花在思考上，网关却照常
// 把空回答记成成功，还把它写进了结果缓存。
func TestGatewayTreatsEmptyCompletionAsFailure(t *testing.T) {
	gateway, usage, traces, invoker, cache := newGateway(t, enabledSettings())
	invoker.response = ai.WireResponse{
		Text:       "",
		Reasoning:  "我先想一下这个问题该怎么答……",
		StopReason: ai.StopReasonLength,
		Usage:      ai.Usage{InputTokens: 11, OutputTokens: 22},
	}

	_, err := gateway.Execute(context.Background(), validRequest())
	var unavailable *ai.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected an unavailable error, got %v", err)
	}
	if unavailable.Reason != ai.ReasonEmptyCompletion {
		t.Fatalf("reason = %q", unavailable.Reason)
	}
	if !errors.Is(err, ai.ErrEmptyCompletion) {
		t.Fatalf("err must wrap ErrEmptyCompletion: %v", err)
	}

	if len(traces.records) != 1 {
		t.Fatalf("traces = %+v", traces.records)
	}
	record := traces.records[0]
	if record.Status != ai.ExecutionStatusFailed {
		t.Fatalf("status = %q；空补全不得记成 succeeded", record.Status)
	}
	if !strings.Contains(record.ErrorSummary, ai.ReasonEmptyCompletion) {
		t.Fatalf("errorSummary = %q", record.ErrorSummary)
	}
	// 停止原因与「有没有思考」是同一个事实的唯一可诊断形态。
	if !strings.Contains(record.ErrorSummary, ai.StopReasonLength) || !strings.Contains(record.ErrorSummary, "reasoning=true") {
		t.Fatalf("errorSummary 缺少诊断事实: %q", record.ErrorSummary)
	}
	if record.InputTokens != 11 || record.OutputTokens != 22 {
		t.Fatalf("空回答同样消耗供应商 token，必须照实记账: %+v", record)
	}

	// 用量照记（真的花了钱），但缓存绝不能被写入：一条空结果进缓存，等于把一次
	// 坏运气变成十分钟的确定性失败。
	if len(usage.records) != 1 || usage.records[0].InputTokens != 11 {
		t.Fatalf("usage = %+v", usage.records)
	}
	if len(cache.items) != 0 {
		t.Fatalf("empty completion must never be cached: %+v", cache.items)
	}

	// 再来一次必须重新出站，而不是从缓存里读回同一个空正文。
	if _, err := gateway.Execute(context.Background(), validRequest()); err == nil {
		t.Fatal("second attempt must fail the same way")
	}
	if invoker.calls != 2 {
		t.Fatalf("invoker calls = %d；空结果不得被重放", invoker.calls)
	}
}

// 只要求工具的响应是工具循环的正常一步，不是空回答。
func TestGatewayKeepsToolCallOnlyCompletionSuccessful(t *testing.T) {
	gateway, _, traces, invoker, cache := newGateway(t, enabledSettings())
	invoker.response = ai.WireResponse{
		StopReason: ai.StopReasonTool,
		ToolCalls:  []ai.ToolCall{{ID: "call_1", Name: "time-now", Arguments: "{}"}},
		Usage:      ai.Usage{InputTokens: 30, OutputTokens: 8},
	}

	if _, err := gateway.Execute(context.Background(), validRequest()); err != nil {
		t.Fatalf("tool-call-only response must stay successful: %v", err)
	}
	if len(traces.records) != 1 || traces.records[0].Status != ai.ExecutionStatusSucceeded {
		t.Fatalf("traces = %+v", traces.records)
	}
	if len(cache.items) != 1 {
		t.Fatalf("tool-call step may be cached: %+v", cache.items)
	}
}

// 缓存命中的那一行必须自我说明：0 ms / 0 token 是正确值（本次没出站、没花钱），
// 而不是「供应商坏了」。分不清这两者正是这次排查的起点。
func TestCachedCompletionIsMarkedAndCostsNothing(t *testing.T) {
	gateway, _, traces, invoker, _ := newGateway(t, enabledSettings())
	if _, err := gateway.Execute(context.Background(), validRequest()); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := gateway.Execute(context.Background(), validRequest()); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if invoker.calls != 1 {
		t.Fatalf("invoker calls = %d；第二次必须命中缓存", invoker.calls)
	}
	if len(traces.records) != 2 {
		t.Fatalf("traces = %+v", traces.records)
	}
	hit := traces.records[1]
	if !hit.CacheHit {
		t.Fatal("cache hit must be flagged")
	}
	if hit.Status != ai.ExecutionStatusSucceeded {
		t.Fatalf("status = %q", hit.Status)
	}
	if hit.LatencyMS != 0 || hit.InputTokens != 0 || hit.OutputTokens != 0 || hit.SpendMicros != 0 {
		t.Fatalf("命中行必须记 0：本次没有出站、没有花 token、没有花钱: %+v", hit)
	}
}
