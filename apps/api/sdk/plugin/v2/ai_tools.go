package pluginv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"time"
)

// AI 工具贡献点：插件把自己的能力交给模型的补全循环。
const (
	HostAIToolSlot              = "sforum.ai.tool"
	HostAIToolOperation         = "invoke"
	HostAIToolRequestSchemaRef  = "sforum.ai.tool.invoke.request@1"
	HostAIToolResponseSchemaRef = "sforum.ai.tool.invoke.response@1"

	// 与 Host 侧一致的上限。SDK 不 import Support/AI：插件只依赖契约，边界值
	// 重复声明一次，由 Host 的漂移测试钉住它们必须一致。
	HostAIToolNameMaximumLength    = 64
	HostAIToolDescriptionMaximum   = 1024
	HostAIToolSchemaMaximumBytes   = 8192
	HostAIToolMaximumTimeoutMS     = 30_000
	HostAIToolsMaximumPerExtension = 16
)

var (
	ErrInvalidAIToolDefinition = errors.New("invalid plugin AI tool definition")
	// ErrAIToolResultRequired 表示 handler 没有给出任何结果文档。
	ErrAIToolResultRequired = errors.New("plugin AI tool handler produced no result")
)

// AIToolDefinition 对齐一条 manifest `aiTools[]` 声明。
//
// Name 是模型看到的名字，字符集受供应商约束（[a-z0-9_-]，不含点号）；ID 是清单
// 侧的稳定标识。两者分开正是为了让「稳定标识」不受供应商命名规则牵制。
type AIToolDefinition struct {
	ID          string
	Name        string
	Description string
	Parameters  string
	TimeoutMS   int
	// ContractVersion 与 manifest 的 contractVersion 同源（id@1）。派发时用它
	// 校验 Host 与插件对同一声明的理解没有漂移。
	ContractVersion string
	// Execute 是作者侧业务 handler（非 Manifest 字段）。
	Execute AIToolHandler
}

// AIToolCall 是 Host 派发过来的一次调用。Arguments 是模型给出的原始 JSON 串，
// 解析失败应作为「工具可解释的失败」返回（ToolResult{IsError:true}），而不是让
// 整次补全失败。
type AIToolCall struct {
	Context   *protocolwire.RequestContext
	ID        string
	Name      string
	Arguments string
	// Locale / TopicID / ViewerUserID 是宿主按清单 aiTools[].context 声明传入的
	// 会话事实。未声明的项保持零值：插件不能假定它们总是存在。
	Locale       string
	TopicID      int64
	ViewerUserID int64
}

// AIToolResult 是给模型的回答。
//
// IsError=true 表示「工具可解释的失败」：参数不对、目标不存在、能力当前不可用。
// 模型会读到 Content 并可能换一种方式重试。它与 handler 返回 Go error 是两回
// 事——后者表示插件内部故障，Host 只给模型一句通用说明，不会把错误文案发布到
// 社区。
type AIToolResult struct {
	Content string
	IsError bool
}

type AIToolHandler func(context.Context, *AIToolCall) (*AIToolResult, error)

// AIToolRegistry 分发 Host → plugin 的 AI 工具调用。
//
// 它与 ProviderRegistry 是同一套传输（ProviderCall），只是固定了 slot 与
// operation：工具不是一个独立的协议能力，而是一个已知 slot 下的 invoke。
type AIToolRegistry struct {
	byID   map[string]AIToolDefinition
	byName map[string]string
	order  []string
}

func NewAIToolRegistry(definitions ...AIToolDefinition) (*AIToolRegistry, error) {
	registry := &AIToolRegistry{byID: map[string]AIToolDefinition{}, byName: map[string]string{}}
	for _, definition := range definitions {
		prepared, err := prepareAIToolDefinition(definition)
		if err != nil {
			return nil, err
		}
		if _, exists := registry.byID[prepared.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate tool id %q", ErrInvalidAIToolDefinition, prepared.ID)
		}
		registry.byID[prepared.ID] = prepared
		registry.byName[prepared.Name] = prepared.ID
		registry.order = append(registry.order, prepared.ID)
	}
	sort.Strings(registry.order)
	return registry, nil
}

// Definitions 返回声明视图（不含 handler），供握手/自检使用。
func (r *AIToolRegistry) Definitions() []AIToolDefinition {
	if r == nil {
		return nil
	}
	out := make([]AIToolDefinition, 0, len(r.order))
	for _, id := range r.order {
		definition := r.byID[id]
		definition.Execute = nil
		out = append(out, definition)
	}
	return out
}

// ProviderCall 处理一次 Host 派发。它只接受本 slot 的 invoke；其它 slot 应交回
// 通用 ProviderRegistry，避免一个注册表暗中承接不属于它的能力。
func (r *AIToolRegistry) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{Context: responseContext(providerRequestContext(request), time.Now().UTC())}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	definition, detail := r.resolve(request)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	handlerCtx, cancel := bindRequestContextDeadline(ctx, request.GetContext())
	defer cancel()
	values := TypedDocumentValues(request.GetInput())
	result, err := definition.Execute(handlerCtx, &AIToolCall{
		Context:      cloneRequestContext(request.GetContext()),
		ID:           definition.ID,
		Name:         definition.Name,
		Arguments:    aiToolArguments(values),
		Locale:       aiToolString(values, "locale"),
		TopicID:      aiToolInt64(values, "topicId"),
		ViewerUserID: aiToolInt64(values, "viewerUserId"),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		response.Error = familyErrorDetail(err, "ai_tool.handler_failed", "Plugin AI tool handler failed.")
		return response, nil
	}
	if result == nil {
		response.Error = familyErrorDetail(ErrAIToolResultRequired, "ai_tool.result_required", "Plugin AI tool handler produced no result.")
		return response, nil
	}
	output, err := NewTypedDocument(HostAIToolResponseSchemaRef, map[string]any{
		"content": result.Content, "isError": result.IsError,
	})
	if err != nil {
		response.Error = familyErrorDetail(err, "ai_tool.result_invalid", "Plugin AI tool result is invalid.")
		return response, nil
	}
	response.Output = output
	return response, nil
}

func (r *AIToolRegistry) resolve(request *pluginwire.ProviderCallRequest) (AIToolDefinition, *protocolwire.ErrorDetail) {
	if r == nil {
		return AIToolDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			Reason: "ai_tool.registry_unavailable", Message: "Plugin AI tool registry is unavailable.",
		}
	}
	if request == nil {
		return AIToolDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "ai_tool.request_required", Message: "An AI tool invocation request is required.",
		}
	}
	if detail := validateFamilyRequestContext(request.GetContext(), "ai_tool"); detail != nil {
		return AIToolDefinition{}, detail
	}
	if strings.TrimSpace(request.GetSlotId()) != HostAIToolSlot ||
		strings.TrimSpace(request.GetOperation()) != HostAIToolOperation {
		return AIToolDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "ai_tool.slot_mismatch", Message: "AI tool registry accepts only the sforum.ai.tool invoke operation.",
		}
	}
	// 派发只认声明 ID：工具名可能重复（跨插件），ID 才是稳定的派发目标。
	definition, ok := r.byID[strings.TrimSpace(request.GetDeclarationId())]
	if !ok {
		return AIToolDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			Reason: "ai_tool.not_found", Message: "The requested AI tool is not registered.",
		}
	}
	if contract := strings.TrimSpace(request.GetContractVersion()); contract != "" && contract != definition.ContractVersion {
		return AIToolDefinition{}, &protocolwire.ErrorDetail{
			Code:   protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason: "ai_tool.contract_mismatch", Message: "AI tool contract version does not match the registered declaration.",
		}
	}
	return definition, nil
}

func prepareAIToolDefinition(definition AIToolDefinition) (AIToolDefinition, error) {
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.Parameters = strings.TrimSpace(definition.Parameters)
	definition.ContractVersion = strings.TrimSpace(definition.ContractVersion)
	if definition.ContractVersion == "" {
		definition.ContractVersion = definition.ID + "@1"
	}
	if definition.ID == "" || !validContractVersion(definition.ContractVersion) ||
		!validHostAIToolName(definition.Name) || definition.Description == "" ||
		len(definition.Description) > HostAIToolDescriptionMaximum || definition.Execute == nil {
		return AIToolDefinition{}, fmt.Errorf("%w: id, name, description, and execute are required", ErrInvalidAIToolDefinition)
	}
	if len(definition.Parameters) == 0 || len(definition.Parameters) > HostAIToolSchemaMaximumBytes ||
		!json.Valid([]byte(definition.Parameters)) {
		return AIToolDefinition{}, fmt.Errorf("%w: parameters must be a valid JSON object", ErrInvalidAIToolDefinition)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(definition.Parameters), &probe); err != nil || probe == nil {
		return AIToolDefinition{}, fmt.Errorf("%w: parameters must be a JSON object", ErrInvalidAIToolDefinition)
	}
	if definition.TimeoutMS <= 0 || definition.TimeoutMS > HostAIToolMaximumTimeoutMS {
		return AIToolDefinition{}, fmt.Errorf("%w: timeout must be 1..%d", ErrInvalidAIToolDefinition, HostAIToolMaximumTimeoutMS)
	}
	return definition, nil
}

// aiToolArguments 安全读取参数串。模型可能给出非字符串内容，直接断言会 panic，
// 而插件进程崩溃的代价远大于一次「参数无法解析」的工具错误。
func aiToolArguments(values map[string]any) string {
	raw, ok := values["arguments"].(string)
	if !ok {
		return ""
	}
	return raw
}

// aiToolString 读取一个可选字符串事实。缺失或类型不符一律返回空串：会话事实
// 是加项，读不到时工具应当按「没有这个事实」工作，而不是让整次调用失败。
func aiToolString(values map[string]any, key string) string {
	raw, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

// aiToolInt64 读取一个可选整数事实。structpb 把数字解成 float64，这里只接受
// 能无损表示的整数值，其余情况返回 0。
func aiToolInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0
		}
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}
