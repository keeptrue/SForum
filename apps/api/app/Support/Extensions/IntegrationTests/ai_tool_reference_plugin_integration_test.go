package extensionsruntime_test

import (
	"context"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	providers "github.com/keeptrue/sforum/apps/api/app/Providers"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	toolplugin "github.com/keeptrue/sforum/apps/api/app/Support/AI/toolplugin"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// M2 的端到端验证：一个只贡献 AI 工具的真实子进程，从模型侧工具接口一路走到
// Host → plugin 的 versioned ProviderCall，再回到模型可读的结果。
//
// 它同时覆盖两条「不依赖插件自觉」的 Host 侧拒绝：声明没有冻结在当前 runtime 的
// 清单里，以及单次 deadline 与声明超时不一致。
func TestAIToolReferencePluginDispatchesThroughRealRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ai tool reference plugin subprocess build in short mode")
	}
	extension := buildReferenceFixtureExtension(t, "sforum-ai-tool-reference", "sforum.ai-tool-reference", 1101, nil)
	if len(extension.Manifest.AITools) != 1 {
		t.Fatalf("fixture ai tool declarations = %#v", extension.Manifest.AITools)
	}
	declaration := extension.Manifest.AITools[0]

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "1101", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start ai tool fixture: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	// 模型侧工具接口：声明必须通过运行期登记规则，声明视图必须与清单一致。
	tool, err := toolplugin.NewPluginTool(extension, declaration, providers.NewAIToolInvoker(manager))
	if err != nil {
		t.Fatalf("fixture declaration rejected by the runtime registry: %v", err)
	}
	definition := tool.Definition()
	if definition.Name != declaration.Name || definition.Description != declaration.Description ||
		string(definition.Parameters) != string(declaration.Parameters) {
		t.Fatalf("tool definition drifted from the manifest: %#v", definition)
	}

	result, err := tool.Invoke(ctx, supportai.ToolCall{
		ID: "call-1", Name: declaration.Name, Arguments: `{"orderNumber":"42"}`,
	}, supportai.ToolContext{ViewerUserID: 7})
	if err != nil || result.IsError || result.Content != "order 42 shipped" {
		t.Fatalf("real runtime dispatch result=%#v err=%v", result, err)
	}

	// 工具可解释的失败：原样交给模型，插件里的业务判断不需要宿主参与。
	explainable, err := tool.Invoke(ctx, supportai.ToolCall{
		Name: declaration.Name, Arguments: `{"orderNumber":""}`,
	}, supportai.ToolContext{})
	if err != nil || !explainable.IsError || explainable.Content == "" {
		t.Fatalf("explainable tool failure result=%#v err=%v", explainable, err)
	}

	// 插件内部故障：必须是 error（宿主只给模型一句通用说明），不能变成模型可见内容。
	internal, err := tool.Invoke(ctx, supportai.ToolCall{
		Name: declaration.Name, Arguments: `{"fail":true}`,
	}, supportai.ToolContext{})
	if err == nil {
		t.Fatalf("plugin internal failure must surface as an error: result=%#v", internal)
	}
	if internal.Content != "" || internal.IsError {
		t.Fatalf("plugin internal failure leaked a model-visible result: %#v", internal)
	}

	invoker := providers.NewAIToolInvoker(manager)
	base := toolplugin.PluginToolCall{
		DeclarationID: declaration.ID, ContractVersion: declaration.ContractVersion,
		Handler: declaration.Handler, Name: declaration.Name,
		Arguments: `{"orderNumber":"1"}`, Timeout: time.Duration(declaration.TimeoutMS) * time.Millisecond,
	}
	notFrozen := base
	notFrozen.DeclarationID = "sforum.ai-tool-reference.not_declared"
	if _, err := invoker.InvokeVersionedAITool(ctx, extension, notFrozen); err == nil {
		t.Fatal("a declaration that is not frozen in the runtime manifest must be refused")
	}
	drifted := base
	drifted.Timeout = base.Timeout + time.Second
	if _, err := invoker.InvokeVersionedAITool(ctx, extension, drifted); err == nil {
		t.Fatal("a deadline that drifted from the declared timeout must be refused")
	}
}
