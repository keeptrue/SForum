package providers

import (
	"context"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	toolplugin "github.com/keeptrue/sforum/apps/api/app/Support/AI/toolplugin"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type aiToolInvokerStarter struct {
	request extensionsruntime.VersionedAIToolRequest
}

func (*aiToolInvokerStarter) Start(context.Context, extensions.Extension) (extensionsruntime.RouteTarget, error) {
	return extensionsruntime.RouteTarget{}, nil
}

func (*aiToolInvokerStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *aiToolInvokerStarter) InvokeVersionedAITool(
	_ context.Context,
	_ extensions.Extension,
	input extensionsruntime.VersionedAIToolRequest,
) (extensionsruntime.VersionedAIToolResponse, error) {
	s.request = input
	return extensionsruntime.VersionedAIToolResponse{Content: "order 42 shipped", IsError: true}, nil
}

// 适配器必须逐字段翻译，且在没有运行时时显式失败：任何一侧新增字段都要在这里
// 同时出现，否则「宿主以为发出去的」与「插件实际收到的」会悄悄分叉。
func TestAIToolInvokerTranslatesEveryFieldAndFailsClosed(t *testing.T) {
	if invoker := NewAIToolInvoker(nil); invoker != nil {
		t.Fatalf("nil runtime must not produce an invoker: %#v", invoker)
	}
	starter := &aiToolInvokerStarter{}
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	extension := extensions.Extension{
		ID: "plugin.ai-tool.valid", Name: "valid", Version: "1.0.0", Type: extensions.TypePlugin,
	}
	result, err := NewAIToolInvoker(manager).InvokeVersionedAITool(context.Background(), extension, toolplugin.PluginToolCall{
		DeclarationID:   "plugin.ai-tool.valid.lookup",
		ContractVersion: "plugin.ai-tool.valid.lookup@1",
		Handler:         "plugin.ai-tool.valid.lookup",
		Timeout:         1000_000_000,
		Name:            "valid-order-lookup",
		Arguments:       `{"orderNumber":"42"}`,
	})
	if err != nil || result.Content != "order 42 shipped" || !result.IsError {
		t.Fatalf("adapter result=%#v err=%v", result, err)
	}
	if starter.request.DeclarationID != "plugin.ai-tool.valid.lookup" ||
		starter.request.ContractVersion != "plugin.ai-tool.valid.lookup@1" ||
		starter.request.Handler != "plugin.ai-tool.valid.lookup" ||
		starter.request.Name != "valid-order-lookup" ||
		starter.request.Arguments != `{"orderNumber":"42"}` ||
		starter.request.Timeout != 1000_000_000 {
		t.Fatalf("translated request drifted: %#v", starter.request)
	}
}
