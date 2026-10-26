package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 插件发起 AI 补全的边界。
//
// 契约刻意镜像 ai.CompletionRequest 但去掉 ProfileRef：插件只能声明用途与成本
// 等级，具体用哪个供应商与模型由站点配置决定。让插件寻址 profile 等于把成本与
// 质量的最终决定权交给插件代码，运营者将无法再用控制台约束它。
const (
	CommandAICompleteID            = "sforum.ai.complete"
	CommandAICompleteVersion       = "1"
	CommandAICompleteInputSchema   = "sforum.ai.complete.input"
	CommandAICompleteOutputSchema  = "sforum.ai.complete.result"
	CommandAICompleteSchemaVersion = "1"

	ProtocolV2AICompleteMaxPurposeLen = 128
	ProtocolV2AICompleteMaxLocaleLen  = 32
)

// ProtocolV2AIGateway 是命令与 AI 网关之间的窄接口。它刻意与
// *supportai.Gateway 的 Execute 同形：命令不需要知道闸门、记账与 trace 的
// 内部结构，只需要把一次中立请求交给它。
type ProtocolV2AIGateway interface {
	Execute(ctx context.Context, request supportai.CompletionRequest) (supportai.CompletionResult, error)
}

type protocolV2AICompleteInput struct {
	Purpose        string                        `json:"purpose"`
	CostClass      string                        `json:"costClass"`
	System         string                        `json:"system,omitempty"`
	Messages       []protocolV2AICompleteMessage `json:"messages"`
	MaxTokens      int                           `json:"maxTokens"`
	Temperature    *float64                      `json:"temperature,omitempty"`
	ResponseFormat string                        `json:"responseFormat,omitempty"`
	Tools          []protocolV2AICompleteTool    `json:"tools,omitempty"`
	ToolChoice     string                        `json:"toolChoice,omitempty"`
	PromptVersion  string                        `json:"promptVersion,omitempty"`
	SubjectUserID  int64                         `json:"subjectUserId,omitempty"`
	ResourceType   string                        `json:"resourceType,omitempty"`
	ResourceID     string                        `json:"resourceId,omitempty"`
	Locale         string                        `json:"locale,omitempty"`
}

type protocolV2AICompleteMessage struct {
	Role       string                         `json:"role"`
	Parts      []protocolV2AICompletePart     `json:"parts,omitempty"`
	ToolCallID string                         `json:"toolCallId,omitempty"`
	ToolCalls  []protocolV2AICompleteToolCall `json:"toolCalls,omitempty"`
}

type protocolV2AICompletePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// protocolV2AICompleteTool 的工具参数是字符串而不是 json.RawMessage：命令输入
// 来自 protobuf Struct，那里没有原始 JSON 字节。命令层校验它是合法 JSON 后再
// 转成 RawMessage 交给网关，网关侧的契约因此不受传输形态影响。
type protocolV2AICompleteTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters,omitempty"`
}

type protocolV2AICompleteToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

type protocolV2PreparedAIComplete struct {
	request     supportai.CompletionRequest
	extensionID string
	purpose     string
	costClass   string
}

// newProtocolV2AICompleteCommandDefinition 是插件发起 AI 调用的唯一入口。
//
// 插件今天已经可以自己开一个出站连接直接找模型供应商（net.outbound）。那正是
// 问题所在：调用不过闸门、不写执行记录、不进用量表，运营者无法解释钱花在哪。
// 这条命令不改变插件能做的事，只把同一件事收进宿主的记账、限流与审计里。
func newProtocolV2AICompleteCommandDefinition(gateway ProtocolV2AIGateway) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandAICompleteID, Version: CommandAICompleteVersion,
		InputSchemaID: CommandAICompleteInputSchema, InputSchemaVersion: CommandAICompleteSchemaVersion,
		OutputSchemaID: CommandAICompleteOutputSchema, OutputSchemaVersion: CommandAICompleteSchemaVersion,
		ActorMode:          protocolV2CommandActorService,
		RequiredCapability: capabilities.AIInvoke,
	}
	// Preview 只给用途、成本等级与规模。消息正文是社区内容或插件私有提示词，
	// 计划结果会进入 UI 与审计，不该借它外泄。
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AICompleteInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2AICompletePreparation(input, nil)
	}
	definition.Prepare = func(ctx context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2AICompleteInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if runtime == nil {
			return nil, protocolV2AICompleteError("ai.gateway_unavailable")
		}
		extensionID := strings.TrimSpace(runtime.GetExtensionId())
		// 请求里自称的扩展 ID 必须与已验证的 runtime 身份一致，否则配额会被记到
		// 别人头上 —— 调用方身份是这条命令唯一的记账依据，绝不能由请求携带。
		if request == nil || request.GetContext().GetExtension().GetExtensionId() != extensionID {
			return nil, protocolV2AICompleteError("ai.gateway_unavailable")
		}
		if gateway == nil {
			return nil, protocolV2AICompleteError("ai.gateway_unavailable")
		}
		subjectUserID, err := protocolV2AICompleteSubjectUser(ctx, input.SubjectUserID)
		if err != nil {
			return nil, err
		}
		completion, err := protocolV2AICompleteRequest(input, extensionID, subjectUserID, request.GetContext().GetLocale())
		if err != nil {
			return nil, err
		}
		prepared := &protocolV2PreparedAIComplete{
			request: completion, extensionID: extensionID,
			purpose: input.Purpose, costClass: input.CostClass,
		}
		return protocolV2AICompletePreparation(input, prepared)
	}
	definition.Execute = func(ctx context.Context, _ pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		prepared, ok := preparation.private.(*protocolV2PreparedAIComplete)
		if !ok || prepared == nil {
			return nil, protocolV2AICompleteError("ai.request_invalid")
		}
		result, err := gateway.Execute(ctx, prepared.request)
		if err != nil {
			return nil, protocolV2AICompleteFailure(err)
		}
		output, err := protocolV2AICompleteDocument(result)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandExecution{Output: output}, nil
	}
	return definition
}

// protocolV2AICompleteRequest 把命令输入翻译成网关的中立请求。调用方扩展 ID 由
// 已验证身份注入，ProfileRef 恒为空：插件不能寻址 profile。
func protocolV2AICompleteRequest(
	input protocolV2AICompleteInput,
	extensionID string,
	subjectUserID int64,
	contextLocale string,
) (supportai.CompletionRequest, error) {
	locale := strings.TrimSpace(input.Locale)
	if locale == "" {
		locale = strings.TrimSpace(contextLocale)
	}
	request := supportai.CompletionRequest{
		Purpose:        input.Purpose,
		CostClass:      input.CostClass,
		System:         input.System,
		MaxTokens:      input.MaxTokens,
		Temperature:    input.Temperature,
		ResponseFormat: input.ResponseFormat,
		ToolChoice:     input.ToolChoice,
		PromptVersion:  input.PromptVersion,
		SubjectUserID:  subjectUserID,
		Metadata: supportai.Metadata{
			CallerExtensionID: extensionID,
			ResourceType:      input.ResourceType,
			ResourceID:        input.ResourceID,
			Locale:            locale,
		},
	}
	messages := make([]supportai.Message, 0, len(input.Messages))
	for _, message := range input.Messages {
		parts := make([]supportai.Part, 0, len(message.Parts))
		for _, part := range message.Parts {
			parts = append(parts, supportai.Part{Type: supportai.PartType(part.Type), Text: part.Text})
		}
		calls := make([]supportai.ToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, supportai.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		messages = append(messages, supportai.Message{
			Role: supportai.Role(message.Role), Parts: parts,
			ToolCallID: message.ToolCallID, ToolCalls: calls,
		})
	}
	request.Messages = messages
	tools := make([]supportai.ToolDefinition, 0, len(input.Tools))
	for _, tool := range input.Tools {
		definition := supportai.ToolDefinition{Name: tool.Name, Description: tool.Description}
		if parameters := strings.TrimSpace(tool.Parameters); parameters != "" {
			definition.Parameters = json.RawMessage(parameters)
		}
		tools = append(tools, definition)
	}
	request.Tools = tools
	// 网关入口还会再校验一次；这里是同样的契约，只是为了让命令能给出比
	// request_invalid 更精确的用途/成本等级原因。
	return request.Normalized(), nil
}

// protocolV2AICompleteSubjectUser 决定本次调用挂在哪个终端用户配额上。
//
// 插件不能报一个用户 ID 就把别人的日配额花掉：只有当命令上下文里存在宿主已验证
// 的委派 actor、且它与请求声明一致时才接受。本命令是 service 模式，永远拿不到
// 委派 actor，因此 subjectUserId 今天恒被拒绝 —— 这个判断留在这里，是为了让
// 将来若放开委派模式时规则已经是显式的，而不是靠调用方自觉。
func protocolV2AICompleteSubjectUser(ctx context.Context, requested int64) (int64, error) {
	if requested <= 0 {
		return 0, nil
	}
	actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
	if !ok || actorUserID != requested {
		return 0, protocolV2AICompleteError("ai.subject_user_unavailable")
	}
	return actorUserID, nil
}

func protocolV2AICompleteInputFromRequest(request *hostv2.CommandRequest) (protocolV2AICompleteInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2AICompleteInput](request)
	if err != nil {
		return input, protocolV2AICompleteError("ai.request_invalid")
	}
	input.Purpose = strings.TrimSpace(input.Purpose)
	input.CostClass = strings.TrimSpace(input.CostClass)
	input.ResponseFormat = strings.TrimSpace(input.ResponseFormat)
	input.ToolChoice = strings.TrimSpace(input.ToolChoice)
	input.PromptVersion = strings.TrimSpace(input.PromptVersion)
	input.ResourceType = strings.TrimSpace(input.ResourceType)
	input.ResourceID = strings.TrimSpace(input.ResourceID)
	input.Locale = strings.TrimSpace(input.Locale)
	// 用途与成本等级各自报错：它们是两个不同的配置错误，合成一个
	// request_invalid 会让插件作者无法定位是哪一项写错了。
	if input.Purpose == "" || len(input.Purpose) > ProtocolV2AICompleteMaxPurposeLen {
		return input, protocolV2AICompleteError("ai.purpose_invalid")
	}
	if !supportai.ValidCostClass(input.CostClass) {
		return input, protocolV2AICompleteError("ai.cost_class_invalid")
	}
	if len(input.Locale) > ProtocolV2AICompleteMaxLocaleLen ||
		len(input.ResourceType) > ProtocolV2AICompleteMaxPurposeLen ||
		len(input.ResourceID) > ProtocolV2AICompleteMaxPurposeLen {
		return input, protocolV2AICompleteError("ai.request_invalid")
	}
	if len(input.System) > supportai.MaxSystemBytes || len(input.Messages) == 0 ||
		len(input.Messages) > supportai.MaxMessages ||
		input.MaxTokens <= 0 || input.MaxTokens > supportai.MaxCompletionTokens ||
		input.Temperature != nil && (*input.Temperature < supportai.MinTemperature || *input.Temperature > supportai.MaxTemperature) ||
		!supportai.ValidResponseFormat(input.ResponseFormat) || !supportai.ValidToolChoice(input.ToolChoice) ||
		len(input.Tools) > supportai.MaxToolsPerRequest {
		return input, protocolV2AICompleteError("ai.request_invalid")
	}
	for _, tool := range input.Tools {
		if len(tool.Parameters) > supportai.MaxToolSchemaBytes ||
			len(tool.Description) > supportai.MaxToolDescriptionBytes {
			return input, protocolV2AICompleteError("ai.request_invalid")
		}
		if parameters := strings.TrimSpace(tool.Parameters); parameters != "" && !json.Valid([]byte(parameters)) {
			return input, protocolV2AICompleteError("ai.request_invalid")
		}
	}
	return input, nil
}

// protocolV2AICompletePreparation 同时服务 Preview 与 Prepare。planned 为真时
// 表示这是一次 dry-run：没有真实调用，也不产生用量。
func protocolV2AICompletePreparation(
	input protocolV2AICompleteInput,
	prepared *protocolV2PreparedAIComplete,
) (*protocolV2CommandPreparation, error) {
	document, err := protocolV2AICompleteDocumentFromInput(input, prepared == nil)
	if err != nil {
		return nil, err
	}
	resourceID := input.Purpose
	if prepared != nil {
		resourceID = prepared.extensionID + "/" + input.Purpose
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.ai.complete@1", ResourceId: resourceID, Allowed: true,
			Reason: "The Host resolves the profile from the cost class and applies the site budget, rate, and per-extension quota gates before any outbound call.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "ai", Action: "complete", ResourceType: "ai.completion", ResourceId: input.Purpose,
			Summary: "Request one " + input.CostClass + " completion with " +
				strconv.Itoa(len(input.Messages)) + " message(s), billed to this extension.",
			Reversible: false,
		}},
		ProjectedResult: document,
		private:         prepared,
	}, nil
}

func protocolV2AICompleteDocumentFromInput(input protocolV2AICompleteInput, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandAICompleteOutputSchema, CommandAICompleteSchemaVersion, map[string]any{
		"planned": planned, "purpose": input.Purpose, "costClass": input.CostClass,
		"messageCount":  strconv.Itoa(len(input.Messages)),
		"maxTokens":     strconv.Itoa(input.MaxTokens),
		"promptVersion": input.PromptVersion,
	})
}

// protocolV2AICompleteDocument 输出中立结果。整数一律编码成十进制字符串，与
// notifications / events 两条命令保持一致：protobuf Struct 只有 double，直接
// 放大整数会丢精度，而 token 与 revision 都必须逐位可核对。
func protocolV2AICompleteDocument(result supportai.CompletionResult) (*protocolv2.TypedDocument, error) {
	calls := make([]any, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		calls = append(calls, map[string]any{
			"id": call.ID, "name": call.Name, "arguments": call.Arguments,
		})
	}
	return protocolV2Document(CommandAICompleteOutputSchema, CommandAICompleteSchemaVersion, map[string]any{
		"planned": false,
		"text":    result.Text, "stopReason": result.StopReason,
		"toolCalls": calls,
		"usage": map[string]any{
			"inputTokens":  strconv.Itoa(result.Usage.InputTokens),
			"outputTokens": strconv.Itoa(result.Usage.OutputTokens),
			"cachedTokens": strconv.Itoa(result.Usage.CachedTokens),
		},
		"provider": map[string]any{
			"extensionId":      result.Provider.ExtensionID,
			"extensionVersion": result.Provider.ExtensionVersion,
			"packageDigest":    result.Provider.PackageDigest,
			"protocol":         result.Provider.Protocol,
			"model":            result.Provider.Model,
		},
		"promptVersion":  result.PromptVersion,
		"configRevision": strconv.FormatInt(result.ConfigRevision, 10),
		"latencyMs":      strconv.FormatInt(result.LatencyMS, 10),
		"cacheHit":       result.CacheHit,
	})
}

// protocolV2AICompleteFailure 把网关错误翻译成稳定原因码。契约见
// plans/2026-10-11-ai-plugin-host-api-m1.md：运营者按原因码决定动作，因此这里
// 的取值是接口的一部分，不能随手改。
func protocolV2AICompleteFailure(err error) error {
	// 网关未接线与「调用发出后失败」是两件不同的事：前者运营者无能为力，后者
	// 才是查网络或密钥。bootstrap 用可后填的网关目标，所以这条路径是真实存在的。
	if errors.Is(err, supportai.ErrGatewayUnavailable) {
		return protocolV2AICompleteError("ai.gateway_unavailable")
	}
	if errors.Is(err, supportai.ErrCredentialMissing) {
		return protocolV2AICompleteError("ai.credential_missing")
	}
	if errors.Is(err, supportai.ErrProfileUnavailable) {
		return protocolV2AICompleteError("ai.profile_unavailable")
	}
	var unavailable *supportai.UnavailableError
	if errors.As(err, &unavailable) {
		switch unavailable.Reason {
		case supportai.GateReasonDisabled:
			return protocolV2AICompleteError("ai.gateway_disabled")
		case supportai.GateReasonBudgetExceeded:
			return protocolV2AICompleteError("ai.budget_exceeded")
		case supportai.GateReasonRateLimited:
			return protocolV2AICompleteError("ai.rate_limited")
		case supportai.GateReasonQuotaExceeded:
			return protocolV2AICompleteError("ai.quota_exceeded")
		case "profile_unavailable":
			return protocolV2AICompleteError("ai.profile_unavailable")
		case supportai.ReasonUnsafeTarget:
			return protocolV2AICompleteError("ai.unsafe_target")
		case supportai.ReasonProviderFailed:
			return protocolV2AICompleteError("ai.provider_failed")
		}
	}
	if errors.Is(err, supportai.ErrRequestInvalid) {
		return protocolV2AICompleteError("ai.request_invalid")
	}
	return protocolV2AICompleteError("ai.provider_failed")
}

func protocolV2AICompleteError(reason string) error {
	code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	message := "The AI completion request is invalid."
	retryable := false
	switch reason {
	case "ai.gateway_disabled":
		code, message = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"The site AI gateway is disabled; no provider call was made."
	case "ai.purpose_invalid":
		message = "The AI completion purpose is required and must be at most 128 characters."
	case "ai.cost_class_invalid":
		message = "The cost class must be economy, standard, or premium."
	case "ai.profile_unavailable":
		code, message = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"No enabled provider profile is bound to this cost class."
	case "ai.credential_missing":
		code, message = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"The provider credential could not be resolved."
	case "ai.quota_exceeded":
		code, message = protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED,
			"The daily AI quota for this extension or user is exhausted."
	case "ai.budget_exceeded":
		code, message = protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED,
			"The site monthly AI budget is exhausted."
	case "ai.rate_limited":
		code, message = protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED,
			"The site AI rate limit is reached."
	case "ai.provider_failed":
		code, message = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"The provider call failed. The attempt is already accounted for; retry with the same idempotency key."
	case "ai.unsafe_target":
		code, message = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"The outbound guard rejected the provider endpoint address."
	case "ai.subject_user_unavailable":
		code, message = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"A subject user requires a Host-attested delegated actor for the same user."
	case "ai.gateway_unavailable":
		code, message = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"The Host AI gateway is not available for this exact runtime."
		retryable = true
	}
	return newProtocolV2CommandError(code, reason, message, retryable)
}
