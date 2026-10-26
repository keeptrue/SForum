package extensionsruntime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// 设置页动作 `ai_tool_probe` 的传输验证：宿主用草稿值起一个短命 runtime，
// 再按 aiTools[] 声明把一次调用派发到真实子进程，并把它给出的结果映射成
// 设置页能显示的成功/失败。
//
// 为什么必须有这条测试：provider_probe 走的是 ProviderProbe 这条老通道，
// 而 AI 工具探测复用的是保留槽 sforum.ai.tool/invoke。两者只在「读 s.protocols
// 拿客户端」这一步之前共享代码，之后分叉。单测里的假 runtime 永远走不到
// ProtocolStarter 上，所以只有真实子进程能证明这条路是通的。
func TestSettingsActionAIToolProbeDispatchesThroughRealRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ai tool probe subprocess build in short mode")
	}
	extension := buildReferenceFixtureExtension(t, "sforum-ai-tool-reference", "sforum.ai-tool-reference", 1201, nil)
	if len(extension.Manifest.AITools) != 1 {
		t.Fatalf("fixture ai tool declarations = %#v", extension.Manifest.AITools)
	}
	declaration := extension.Manifest.AITools[0]

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "1201", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})

	// 探测不受插件的 enabled 开关影响，也不要求插件此刻正在运行：它要回答的是
	// 「按这份草稿配置能不能跑通」，所以这里刻意没有先 Start 一次正式 runtime。
	probe, err := manager.ProbeSettingsAction(ctx, extension, extensions.SettingsActionProbeRequest{
		AIToolID: declaration.ID,
		Values:   map[string]string{"orderNumber": "probe-1"},
	})
	if err != nil {
		t.Fatalf("probe against the real subprocess failed: %v", err)
	}
	if !probe.OK {
		t.Fatalf("probe reported failure: %+v", probe)
	}
	if probe.Message != "order probe-1 shipped" {
		t.Fatalf("probe message = %q, want the tool's own content", probe.Message)
	}
	if probe.Reason != "extension.settings_action_ai_tool_ok" {
		t.Fatalf("probe reason = %q", probe.Reason)
	}
	// 结果只承载 content/isError，因此 Details/Suggestions 必须为空：
	// 任何给人看的说明都得由插件写进 message，不能指望有第二条通道。
	if len(probe.Details) != 0 || len(probe.Suggestions) != 0 {
		t.Fatalf("ai tool probe must not invent details/suggestions: %+v", probe)
	}

	// 「工具可解释的失败」映射成 OK=false，但仍然是正常返回而不是 error：
	// 运营者要看到的是插件给出的原因，而不是一句「操作暂不可用」。
	explainable, err := manager.ProbeSettingsAction(ctx, extension, extensions.SettingsActionProbeRequest{
		AIToolID: declaration.ID,
		Values:   map[string]string{"orderNumber": ""},
	})
	if err != nil {
		t.Fatalf("explainable probe failure escalated to an error: %v", err)
	}
	if explainable.OK || explainable.Message != "orderNumber is required" {
		t.Fatalf("explainable probe failure = %+v", explainable)
	}
	if explainable.Reason != "extension.settings_action_ai_tool_failed" {
		t.Fatalf("explainable probe reason = %q", explainable.Reason)
	}

	// 未声明的工具 id 必须失败关闭，而不是被派发到一个同名 handler。
	undeclared, err := manager.ProbeSettingsAction(ctx, extension, extensions.SettingsActionProbeRequest{
		AIToolID: "sforum.ai-tool-reference.not_declared",
		Values:   map[string]string{"orderNumber": "1"},
	})
	if !errors.Is(err, extensions.ErrSettingsActionUnavailable) {
		t.Fatalf("undeclared tool id: probe=%+v err=%v, want ErrSettingsActionUnavailable", undeclared, err)
	}

	// 探测的 arguments 恒为草稿值（map[string]string），所以插件的布尔型参数只能
	// 以字符串形态出现。这里钉住这条边界的行为：形状不符时插件给的是「工具可解释
	// 的失败」，而不是让整次探测以内部错误消失——运营者需要看到原因。
	shapeMismatch, err := manager.ProbeSettingsAction(ctx, extension, extensions.SettingsActionProbeRequest{
		AIToolID: declaration.ID,
		Values:   map[string]string{"orderNumber": "1", "fail": "true"},
	})
	if err != nil {
		t.Fatalf("shape mismatch escalated to an error: %v", err)
	}
	if shapeMismatch.OK || shapeMismatch.Message != "arguments must be a JSON object" {
		t.Fatalf("shape mismatch result = %+v", shapeMismatch)
	}
}
