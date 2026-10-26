package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// aiToolTransportStarter 同时实现 Starter 与 AI 工具派发面，用来验证协作者只做
// 「把调用转发给当前进程的 Starter」这一件事。
type aiToolTransportStarter struct {
	calls    atomic.Int32
	requests chan VersionedAIToolRequest
}

func (*aiToolTransportStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{}, nil
}

func (*aiToolTransportStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *aiToolTransportStarter) InvokeVersionedAITool(
	_ context.Context,
	_ extensions.Extension,
	input VersionedAIToolRequest,
) (VersionedAIToolResponse, error) {
	s.calls.Add(1)
	if s.requests != nil {
		s.requests <- input
	}
	return VersionedAIToolResponse{Content: "order 42 shipped", IsError: false}, nil
}

// noAIToolStarter 是「这个进程角色没有 Protocol V2 传输」的最小实现。
type noAIToolStarter struct{}

func (noAIToolStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{}, nil
}

func (noAIToolStarter) Stop(context.Context, extensions.Extension) error { return nil }

func TestAIToolRuntimeInvokerFailsClosedWithoutTransport(t *testing.T) {
	extension := managerRuntimeExtension("plugin.ai-tool.reference", "1.0.0", strings.Repeat("a", 64))
	request := VersionedAIToolRequest{DeclarationID: extension.ID + ".lookup", ContractVersion: extension.ID + ".lookup@1"}

	if _, err := NewAIToolRuntimeInvoker(nil).InvokeVersionedAITool(context.Background(), extension, request); !errors.Is(err, extensions.ErrRuntimeUnavailable) {
		t.Fatalf("nil manager must report unavailable, got %v", err)
	}
	// 默认 localStarter 不实现 AI 工具派发：必须显式失败，而不是静默变成成功。
	manager := NewManager(ManagerConfig{Starter: noAIToolStarter{}})
	if _, err := NewAIToolRuntimeInvoker(manager).InvokeVersionedAITool(context.Background(), extension, request); err == nil {
		t.Fatal("starter without the AI tool face must fail explicitly")
	}
}

func TestAIToolRuntimeInvokerDelegatesToTheCurrentProcessStarter(t *testing.T) {
	starter := &aiToolTransportStarter{requests: make(chan VersionedAIToolRequest, 1)}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("plugin.ai-tool.reference", "1.0.0", strings.Repeat("a", 64))
	request := VersionedAIToolRequest{
		DeclarationID: extension.ID + ".lookup", ContractVersion: extension.ID + ".lookup@1",
		Handler: extension.ID + ".lookup", Name: "reference-order-lookup",
		Arguments: `{"orderNumber":"42"}`, Timeout: 1000_000_000,
	}
	response, err := NewAIToolRuntimeInvoker(manager).InvokeVersionedAITool(context.Background(), extension, request)
	if err != nil || response.Content != "order 42 shipped" || response.IsError {
		t.Fatalf("delegation response=%#v err=%v", response, err)
	}
	if starter.calls.Load() != 1 {
		t.Fatalf("starter calls = %d", starter.calls.Load())
	}
	forwarded := <-starter.requests
	if forwarded != request {
		t.Fatalf("request drifted in translation: %#v", forwarded)
	}
}

// 线上契约的字面量固定在这里：Host 传输与插件 SDK 各自钉住同一组 slot/operation/
// schema，单侧改名会让工具调用变成「发得出去、认不出来」。
func TestProtocolV2AIToolWireContractIsPinned(t *testing.T) {
	if ProtocolV2AIToolProviderSlot != "sforum.ai.tool" ||
		ProtocolV2AIToolProviderOperation != "invoke" ||
		ProtocolV2AIToolRequestSchema != "sforum.ai.tool.invoke.request@1" ||
		ProtocolV2AIToolResponseSchema != "sforum.ai.tool.invoke.response@1" {
		t.Fatalf("ai tool transport contract drifted: slot=%q op=%q req=%q res=%q",
			ProtocolV2AIToolProviderSlot, ProtocolV2AIToolProviderOperation,
			ProtocolV2AIToolRequestSchema, ProtocolV2AIToolResponseSchema)
	}
}
