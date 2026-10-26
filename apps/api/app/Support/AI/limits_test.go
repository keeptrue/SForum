package ai_test

import (
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

// SDK 刻意不 import Support/AI（插件二进制不该拖进整个网关实现），边界值因此在
// 两处各写一遍。这份镜像没有编译期约束，只能靠测试钉住：任何一边单独调整都会让
// 「Host 放行、SDK 先拒」或反过来的分裂重新出现。
func TestSDKAICompletionLimitsMatchHost(t *testing.T) {
	if pluginv2.HostAICompleteMaxMessages != ai.MaxMessages {
		t.Fatalf("messages limit drifted: sdk=%d host=%d", pluginv2.HostAICompleteMaxMessages, ai.MaxMessages)
	}
	if pluginv2.HostAICompleteMaxTokens != ai.MaxCompletionTokens {
		t.Fatalf("token limit drifted: sdk=%d host=%d", pluginv2.HostAICompleteMaxTokens, ai.MaxCompletionTokens)
	}
	if pluginv2.HostAICompleteMaxTools != ai.MaxToolsPerRequest {
		t.Fatalf("tool limit drifted: sdk=%d host=%d", pluginv2.HostAICompleteMaxTools, ai.MaxToolsPerRequest)
	}
	if pluginv2.HostAICompleteMaxToolNameLen != ai.MaxToolNameLen {
		t.Fatalf("tool name limit drifted: sdk=%d host=%d", pluginv2.HostAICompleteMaxToolNameLen, ai.MaxToolNameLen)
	}
}

// 传输层必须装得下 Host 允许的最大输入：AI 补全请求经插件 gRPC 传输，若
// MaxTotalInputBytes 大于单条消息上限，大上下文请求会死在传输层而不是网关。
func TestTransportCarriesLargestAllowedInput(t *testing.T) {
	if pluginv2.DefaultMaxMessageBytes < ai.MaxTotalInputBytes {
		t.Fatalf("gRPC message limit (%d) must cover MaxTotalInputBytes (%d)",
			pluginv2.DefaultMaxMessageBytes, ai.MaxTotalInputBytes)
	}
}

// 上限的相互关系必须自洽：单条消息不能大于总输入预算，默认值必须落在范围内。
func TestLimitsAreSelfConsistent(t *testing.T) {
	if ai.MaxMessageBytes > ai.MaxTotalInputBytes {
		t.Fatalf("single message budget (%d) exceeds total input budget (%d)", ai.MaxMessageBytes, ai.MaxTotalInputBytes)
	}
	if ai.MaxSystemBytes > ai.MaxTotalInputBytes {
		t.Fatalf("system budget (%d) exceeds total input budget (%d)", ai.MaxSystemBytes, ai.MaxTotalInputBytes)
	}
	if ai.DefaultMaxCompletionTokens <= 0 || ai.DefaultMaxCompletionTokens > ai.MaxCompletionTokens {
		t.Fatalf("default completion tokens (%d) outside (0, %d]", ai.DefaultMaxCompletionTokens, ai.MaxCompletionTokens)
	}
	if ai.DefaultToolCallsPerReply < 0 || ai.DefaultToolCallsPerReply > ai.MaxToolCallsPerReply {
		t.Fatalf("default tool calls (%d) outside [0, %d]", ai.DefaultToolCallsPerReply, ai.MaxToolCallsPerReply)
	}
	if ai.MinTimeoutMS >= ai.MaxTimeoutMS {
		t.Fatalf("timeout range is empty: %d..%d", ai.MinTimeoutMS, ai.MaxTimeoutMS)
	}
	// 图片不计入单条文本预算，但要计入总输入预算，否则一张图就能把请求顶爆。
	if ai.MaxImageBase64Bytes > ai.MaxTotalInputBytes {
		t.Fatalf("one image (%d bytes encoded) exceeds the total input budget (%d)", ai.MaxImageBase64Bytes, ai.MaxTotalInputBytes)
	}
}

// 1M token 上下文是这一组上限存在的前提：输入预算必须显著大于旧的 400KB，
// 否则「不要卡那么紧」只是口头上的。按中文 3 字节/字约 1.3 字/token 折算，
// 12MiB 约等于 400 万汉字。
func TestInputBudgetSupportsMillionTokenContext(t *testing.T) {
	const legacyBudget = 400_000
	if ai.MaxTotalInputBytes <= legacyBudget {
		t.Fatalf("input budget must grow beyond the legacy ceiling, got %d", ai.MaxTotalInputBytes)
	}
	if ai.MaxTotalInputBytes < 4<<20 {
		t.Fatalf("input budget (%d) is below 4MiB and cannot hold a 1M token prompt", ai.MaxTotalInputBytes)
	}
	if ai.MaxCompletionTokens < 64_000 {
		t.Fatalf("output budget (%d) is below the 64k long-output tier", ai.MaxCompletionTokens)
	}
}

// CurrentLimits 是控制台渲染 min/max 的唯一来源：任何字段漏填都会让界面退回
// 「没有上下限」的自由输入。
func TestCurrentLimitsExposeEveryField(t *testing.T) {
	limits := ai.CurrentLimits()
	if limits.MinTimeoutMS != ai.MinTimeoutMS || limits.MaxTimeoutMS != ai.MaxTimeoutMS {
		t.Fatalf("timeout limits not exposed: %+v", limits)
	}
	if limits.MaxCompletionTokens != ai.MaxCompletionTokens || limits.DefaultCompletionTokens != ai.DefaultMaxCompletionTokens {
		t.Fatalf("completion limits not exposed: %+v", limits)
	}
	if limits.MaxToolCallsPerReply != ai.MaxToolCallsPerReply || limits.DefaultToolCallsPerReply != ai.DefaultToolCallsPerReply {
		t.Fatalf("tool call limits not exposed: %+v", limits)
	}
	if limits.MaxRateLimitPerMinute != ai.MaxRateLimitPerMinute ||
		limits.MaxExtensionDailyQuota != ai.MaxExtensionDailyQuota ||
		limits.MaxUserDailyQuota != ai.MaxUserDailyQuota ||
		limits.MaxMonthlyBudgetMicros != ai.MaxMonthlyBudgetMicros {
		t.Fatalf("gate limits not exposed: %+v", limits)
	}
	if limits.MaxTotalInputBytes != ai.MaxTotalInputBytes {
		t.Fatalf("input budget not exposed: %+v", limits)
	}
}
