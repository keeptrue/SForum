package extensionsruntime

import (
	"context"
	"fmt"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AI 工具调用复用既有的 versioned ProviderCall 传输。
//
// 为什么不新增一个 proto 服务：ProviderCall 已经带有精确声明绑定、契约版本、
// 超时与错误信封，新增 RPC 会把这套边界复制一遍并引入第二处漂移。工具只是
// 一个 slot 下的 operation，与 SEO 的做法完全一致。
const (
	ProtocolV2AIToolProviderSlot      = "sforum.ai.tool"
	ProtocolV2AIToolProviderOperation = "invoke"
	ProtocolV2AIToolRequestSchema     = "sforum.ai.tool.invoke.request@1"
	ProtocolV2AIToolResponseSchema    = "sforum.ai.tool.invoke.response@1"
)

// VersionedAIToolRequest freezes one tool call to an exact declaration carried
// by this runtime. The Host never asks the plugin "which tools do you have or
// what is your timeout" — both come from the frozen manifest declaration.
type VersionedAIToolRequest struct {
	DeclarationID   string
	ContractVersion string
	Handler         string
	Timeout         time.Duration
	// Name 是模型要求的工具名。它必须与声明一致：宿主不会把一次调用改派给
	// 同一个 handler 下的另一个工具名。
	Name      string
	Arguments string
	// Locale / TopicID / ViewerUserID 是清单 aiTools[].context 声明过的会话事实。
	// 未声明的项保持零值，不会出现在请求文档里——声明即授权，宿主不做隐式披露。
	Locale       string
	TopicID      int64
	ViewerUserID int64
}

// VersionedAIToolResponse 是插件给出的结果。
//
// IsError 区分「工具可解释的失败」与「调用本身失败」：前者原样给模型（它可以
// 据此换参数重试），后者由宿主替换成一句通用说明，避免插件内部错误串借模型之口
// 发布到社区。
type VersionedAIToolResponse struct {
	Content string
	IsError bool
}

// InvokeVersionedAITool executes one tool call against the exact runtime.
//
// 超时是唯一的运行期自由度，且必须逐字等于声明里的 TimeoutMS：插件不能在调用
// 时要求一个比声明更长的窗口，否则一个慢工具就能拖住整个补全循环，而运营者在
// 清单里看到的超时将不再是真实上界。
func (c *protocolV2Client) InvokeVersionedAITool(
	parent context.Context,
	declaredTools []extensions.ManifestAITool,
	input VersionedAIToolRequest,
) (VersionedAIToolResponse, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return VersionedAIToolResponse{}, extensions.ErrRuntimeUnavailable
	}
	var declared *extensions.ManifestAITool
	for index := range declaredTools {
		candidate := &declaredTools[index]
		if candidate.ID == input.DeclarationID && candidate.ContractVersion == input.ContractVersion &&
			candidate.Handler == input.Handler && candidate.Name == input.Name {
			declared = candidate
			break
		}
	}
	if declared == nil {
		return VersionedAIToolResponse{}, fmt.Errorf("protocol v2 AI tool declaration %q is not frozen", input.DeclarationID)
	}
	declaredTimeout := time.Duration(declared.TimeoutMS) * time.Millisecond
	// 上限用工具声明自己的边界，而不是传输默认超时：后者是「调用方没给 deadline
	// 时的兜底」，工具调用永远自带 deadline。两者混用会把 5 秒的传输默认值变成
	// 工具能力的硬顶。
	maxTimeout := time.Duration(extensionmanifest.AIToolMaximumTimeoutMS) * time.Millisecond
	if input.Timeout != declaredTimeout || declaredTimeout <= 0 || declaredTimeout > maxTimeout {
		return VersionedAIToolResponse{}, fmt.Errorf("protocol v2 AI tool declaration %q timeout drifted", input.DeclarationID)
	}
	requestSchemaID, requestSchemaVersion, err := protocolV2SchemaRef(ProtocolV2AIToolRequestSchema)
	if err != nil {
		return VersionedAIToolResponse{}, err
	}
	// 会话事实是加项：未声明的字段根本不写进文档，旧插件读不到也不受影响。
	payload := map[string]any{
		"name": input.Name, "arguments": input.Arguments,
	}
	if input.Locale != "" {
		payload["locale"] = input.Locale
	}
	if input.TopicID > 0 {
		payload["topicId"] = input.TopicID
	}
	if input.ViewerUserID > 0 {
		payload["viewerUserId"] = input.ViewerUserID
	}
	document, err := protocolV2Document(requestSchemaID, requestSchemaVersion, payload)
	if err != nil {
		return VersionedAIToolResponse{}, err
	}
	ctx, cancel := protocolV2Deadline(parent, declaredTimeout)
	defer cancel()
	response, err := c.client.ProviderCall(ctx, &pluginwire.ProviderCallRequest{
		Context:         c.requestContext(ctx, input.DeclarationID),
		SlotId:          ProtocolV2AIToolProviderSlot,
		Operation:       ProtocolV2AIToolProviderOperation,
		ContractVersion: input.ContractVersion,
		DeclarationId:   input.DeclarationID,
		Input:           document,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.DeadlineExceeded:
			return VersionedAIToolResponse{}, context.DeadlineExceeded
		case codes.Canceled:
			return VersionedAIToolResponse{}, context.Canceled
		}
		if ctx.Err() != nil {
			return VersionedAIToolResponse{}, ctx.Err()
		}
		return VersionedAIToolResponse{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return VersionedAIToolResponse{}, err
	}
	if err := validateProtocolV2DocumentRef(response.GetOutput(), ProtocolV2AIToolResponseSchema, "AI tool output"); err != nil {
		return VersionedAIToolResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return VersionedAIToolResponse{
		Content: stringValue(values, "content"),
		IsError: booleanValue(values, "isError"),
	}, nil
}

// aiToolTransportInvoker 是 AI 工具派发在 Starter 上的能力面。
//
// Starter 是接口，不同进程角色注入的实现不同（API 是 *ProtocolStarter，Local
// 是 localStarter）。用能力断言而不是往 Starter 接口上加方法，可以让「没有
// Protocol V2 的进程角色」显式失败，而不是得到一个编译期就必须实现的假方法。
type aiToolTransportInvoker interface {
	InvokeVersionedAITool(context.Context, extensions.Extension, VersionedAIToolRequest) (VersionedAIToolResponse, error)
}

// AIToolRuntimeInvoker 是 AI 工具派发的聚焦协作者。
//
// 为什么单独一类：Manager(72) 与 RuntimeInvoker(28) 的接收者方法数都是不可增长
// 的架构基线，而 AI 工具派发是独立职责。与 events_publish.go 的
// ExtensionEventPublisher 同一手法：新职责进新协作者，不往 God object 上堆。
//
// 它自己不做超时、schema 或身份判定——那些都在 protocolV2Client 的冻结声明校验
// 里；这里只负责从 Manager 拿到当前进程的 Starter 并转发一次。
type AIToolRuntimeInvoker struct {
	starter Starter
}

// NewAIToolRuntimeInvoker 绑定 Manager 持有的 Starter。
// manager 为 nil 时构造出的协作者一律报告不可用，而不是静默成功。
func NewAIToolRuntimeInvoker(manager *Manager) *AIToolRuntimeInvoker {
	if manager == nil {
		return &AIToolRuntimeInvoker{}
	}
	return &AIToolRuntimeInvoker{starter: manager.starter}
}

func (i *AIToolRuntimeInvoker) InvokeVersionedAITool(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedAIToolRequest,
) (VersionedAIToolResponse, error) {
	if i == nil || i.starter == nil {
		return VersionedAIToolResponse{}, extensions.ErrRuntimeUnavailable
	}
	invoker, ok := i.starter.(aiToolTransportInvoker)
	if !ok {
		return VersionedAIToolResponse{}, fmt.Errorf("versioned AI tool requires Protocol V2")
	}
	return invoker.InvokeVersionedAITool(ctx, extension, input)
}

// InvokeVersionedAITool 把一次工具调用派发到精确 runtime。
// 与 SEO 同一手法：先在运行时表里取到该插件的 Protocol V2 客户端，再调它。
func (s *ProtocolStarter) InvokeVersionedAITool(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedAIToolRequest,
) (VersionedAIToolResponse, error) {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return VersionedAIToolResponse{}, fmt.Errorf("versioned AI tool requires Protocol V2")
	}
	return client.InvokeVersionedAITool(ctx, extension.Manifest.AITools, input)
}
