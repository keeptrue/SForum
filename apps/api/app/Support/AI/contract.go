package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// CompletionContractVersion 是 Host 中立补全契约的稳定身份。provider 插件按此
// 契约实现 handler，openai-chat / anthropic-messages 的翻译由 Core 适配器负责。
const CompletionContractVersion = "sforum.ai.completion@1"

// 成本等级由用途声明，网关据此选择 provider profile。用途不得自行指定模型，
// 否则运营者无法用配置控制成本与质量。
const (
	CostClassEconomy  = "economy"
	CostClassStandard = "standard"
	CostClassPremium  = "premium"
)

// 响应格式。
const (
	ResponseFormatText = "text"
	ResponseFormatJSON = "json"
)

// 协议无关的停止原因。
const (
	StopReasonEnd      = "end"
	StopReasonLength   = "length"
	StopReasonFiltered = "filtered"
	StopReasonTool     = "tool"
	StopReasonError    = "error"
)

var (
	// ErrRequestInvalid 表示补全请求不满足契约约束。
	ErrRequestInvalid = errors.New("ai: completion request is invalid")
)

// Role 是消息角色。system 是请求级字段，不出现在 Messages 中，避免适配器
// 猜测各协议对 system 位置的约定。tool 是工具结果的角色，只在工具循环里出现。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartType 承认文本与图片两种内容块。图片只在站点开启多模态输入时出现；
// 运营者选择支持图片理解的模型，Core 不维护任何模型能力名单。
type PartType string

const (
	PartText  PartType = "text"
	PartImage PartType = "image"
)

// ImageContent 是一张以内联 base64 承载的图片。只支持内联而非 URL 引用：
// 附件可能来自私网存储，模型端点不一定抓得到站内地址；base64 让「图片是否
// 送达」与「附件是否公网可达」解耦。
type ImageContent struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

type Part struct {
	Type  PartType      `json:"type"`
	Text  string        `json:"text,omitempty"`
	Image *ImageContent `json:"image,omitempty"`
}

// ToolDefinition 是给模型看的工具声明。Parameters 是 JSON Schema 对象，原样
// 透传给供应商协议；Core 不解释它的语义，也不替工具校验参数。
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall 是模型发起的一次工具调用。Arguments 保留原始 JSON 字符串，由工具
// 实现自行解析——适配器不替它猜结构，解析失败由工具返回错误结果给模型。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts,omitempty"`
	// ToolCallID 仅 role=tool 时有值，指向它所回应的那次调用。
	ToolCallID string `json:"toolCallId,omitempty"`
	// ToolCalls 仅 role=assistant 时有值：模型在这一步要求调用的工具。
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
}

// Metadata 描述调用归属，用于记账、审计与 trace。用途不在这里：它只有
// CompletionRequest.Purpose 一个来源，避免两处不一致。
type Metadata struct {
	CallerExtensionID string `json:"callerExtensionId,omitempty"`
	ResourceType      string `json:"resourceType,omitempty"`
	ResourceID        string `json:"resourceId,omitempty"`
	Locale            string `json:"locale,omitempty"`
}

// CompletionRequest 是中立请求。ProfileRef 为空时由网关按成本等级选择。
type CompletionRequest struct {
	ProfileRef     string    `json:"profileRef,omitempty"`
	Purpose        string    `json:"purpose"`
	CostClass      string    `json:"costClass"`
	System         string    `json:"system,omitempty"`
	Messages       []Message `json:"messages"`
	MaxTokens      int       `json:"maxTokens"`
	Temperature    *float64  `json:"temperature,omitempty"`
	ResponseFormat string    `json:"responseFormat,omitempty"`
	// Tools 是本次调用可用的工具声明。为空表示纯文本补全，模型不应返回工具调用。
	Tools []ToolDefinition `json:"tools,omitempty"`
	// ToolChoice 控制工具使用策略：空（供应商默认，等同 auto）、auto、none、required。
	ToolChoice string   `json:"toolChoice,omitempty"`
	Metadata   Metadata `json:"metadata"`
	// PromptVersion 由调用方声明，参与缓存键与执行 trace。修改提示词必须递增它，
	// 否则历史决策无法与产出它的提示词对应。
	PromptVersion string `json:"promptVersion,omitempty"`
	// SubjectUserID 是触发本次调用的终端用户，用于用户级配额。0 表示系统发起。
	// 它不进入 Metadata，也不会出现在任何 provider 请求中。
	SubjectUserID int64 `json:"-"`
}

// ProviderArtifact 标识产出结果的确切插件产物，供审计、归因与卸载后的降级展示。
type ProviderArtifact struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	Protocol         string `json:"protocol"`
	Model            string `json:"model"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	CachedTokens int `json:"cachedTokens,omitempty"`
}

func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// CompletionResult 是中立结果。PromptVersion 与 ConfigRevision 一并记录，
// 使历史决策可解释、可回放；配置后续变更不得反推历史结论。
type CompletionResult struct {
	Text       string           `json:"text"`
	StopReason string           `json:"stopReason"`
	Usage      Usage            `json:"usage"`
	Provider   ProviderArtifact `json:"provider"`
	// ToolCalls 非空时表示模型的这一步要求先执行工具，Text 通常为空。
	ToolCalls      []ToolCall `json:"toolCalls,omitempty"`
	PromptVersion  string     `json:"promptVersion,omitempty"`
	ConfigRevision int64      `json:"configRevision,omitempty"`
	LatencyMS      int64      `json:"latencyMs"`
	CacheHit       bool       `json:"cacheHit,omitempty"`
}

func ValidRole(role Role) bool {
	return role == RoleUser || role == RoleAssistant || role == RoleTool
}

// ValidToolName 限定工具名的字符集：与供应商实际接受的模式保持一致
// （`^[a-zA-Z0-9_-]{1,64}$`，OpenAI 与 Anthropic 都是这套约束）。点号刻意不在
// 允许集合内——DeepSeek/OpenAI 会在请求层直接以 400 拒绝带点号的名字，用连字符
// 表达命名空间（forum-topic-read）才是真正可用的写法。
func ValidToolName(name string) bool {
	if name == "" || len(name) > MaxToolNameLen {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// ValidToolChoice 校验工具策略。空字符串表示交给供应商默认（等同 auto）。
func ValidToolChoice(choice string) bool {
	switch strings.TrimSpace(choice) {
	case "", "auto", "none", "required":
		return true
	default:
		return false
	}
}

func ValidCostClass(class string) bool {
	switch class {
	case CostClassEconomy, CostClassStandard, CostClassPremium:
		return true
	default:
		return false
	}
}

func ValidResponseFormat(format string) bool {
	switch format {
	case "", ResponseFormatText, ResponseFormatJSON:
		return true
	default:
		return false
	}
}

// Normalized 去除标识类字段的空白，供存储与比较使用。
func (r CompletionRequest) Normalized() CompletionRequest {
	r.ProfileRef = strings.TrimSpace(r.ProfileRef)
	r.Purpose = strings.TrimSpace(r.Purpose)
	r.CostClass = strings.TrimSpace(r.CostClass)
	r.ResponseFormat = strings.TrimSpace(r.ResponseFormat)
	r.ToolChoice = strings.TrimSpace(r.ToolChoice)
	r.Metadata.CallerExtensionID = strings.TrimSpace(r.Metadata.CallerExtensionID)
	r.Metadata.ResourceType = strings.TrimSpace(r.Metadata.ResourceType)
	r.Metadata.ResourceID = strings.TrimSpace(r.Metadata.ResourceID)
	r.Metadata.Locale = strings.TrimSpace(r.Metadata.Locale)
	for i := range r.Tools {
		r.Tools[i].Name = strings.TrimSpace(r.Tools[i].Name)
	}
	for i := range r.Messages {
		r.Messages[i].ToolCallID = strings.TrimSpace(r.Messages[i].ToolCallID)
		for j := range r.Messages[i].ToolCalls {
			r.Messages[i].ToolCalls[j].ID = strings.TrimSpace(r.Messages[i].ToolCalls[j].ID)
			r.Messages[i].ToolCalls[j].Name = strings.TrimSpace(r.Messages[i].ToolCalls[j].Name)
		}
	}
	return r
}

// Validate 在网关入口处执行。它只校验契约自身的约束；profile 与闸门分别由
// Profile.Validate 与 EvaluateGates 负责，避免把三类失败混成一个错误。
func (r CompletionRequest) Validate() error {
	if strings.TrimSpace(r.Purpose) == "" {
		return ErrRequestInvalid
	}
	if !ValidCostClass(r.CostClass) {
		return ErrRequestInvalid
	}
	if !ValidResponseFormat(r.ResponseFormat) {
		return ErrRequestInvalid
	}
	if !ValidToolChoice(r.ToolChoice) {
		return ErrRequestInvalid
	}
	if len(r.Messages) == 0 || len(r.Messages) > MaxMessages {
		return ErrRequestInvalid
	}
	if len(r.System) > MaxSystemBytes {
		return ErrRequestInvalid
	}
	if r.MaxTokens <= 0 || r.MaxTokens > MaxCompletionTokens {
		return ErrRequestInvalid
	}
	if r.Temperature != nil && (*r.Temperature < MinTemperature || *r.Temperature > MaxTemperature) {
		return ErrRequestInvalid
	}
	if len(r.Tools) > MaxToolsPerRequest {
		return ErrRequestInvalid
	}
	seenTools := make(map[string]bool, len(r.Tools))
	total := len(r.System)
	// 工具声明同样进入供应商输入，必须计入规模与配额。
	for _, tool := range r.Tools {
		if !ValidToolName(tool.Name) || seenTools[tool.Name] {
			return ErrRequestInvalid
		}
		seenTools[tool.Name] = true
		if len(tool.Description) > MaxToolDescriptionBytes {
			return ErrRequestInvalid
		}
		if len(tool.Parameters) > MaxToolSchemaBytes {
			return ErrRequestInvalid
		}
		if len(tool.Parameters) > 0 && !json.Valid(tool.Parameters) {
			return ErrRequestInvalid
		}
		total += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	for _, message := range r.Messages {
		if !ValidRole(message.Role) {
			return ErrRequestInvalid
		}
		switch message.Role {
		case RoleTool:
			// 工具结果必须指向它所回应的那次调用；允许空正文（工具可能无话可说），
			// 但绝不允许再携带工具调用。
			if strings.TrimSpace(message.ToolCallID) == "" || len(message.ToolCallID) > MaxToolCallIDLen {
				return ErrRequestInvalid
			}
			if len(message.ToolCalls) > 0 {
				return ErrRequestInvalid
			}
		case RoleAssistant:
			// 助手消息可以只带工具调用而没有正文——那正是「要求先执行工具」的形态。
			if len(message.Parts) == 0 && len(message.ToolCalls) == 0 {
				return ErrRequestInvalid
			}
		default:
			// 用户消息是唯一允许承载社区内容的角色：必须有非空正文。
			if len(message.Parts) == 0 || len(message.ToolCalls) > 0 || message.ToolCallID != "" {
				return ErrRequestInvalid
			}
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || len(call.ID) > MaxToolCallIDLen || !ValidToolName(call.Name) {
				return ErrRequestInvalid
			}
			if len(call.Arguments) > MaxToolArgumentsBytes {
				return ErrRequestInvalid
			}
			total += len(call.Name) + len(call.Arguments)
		}
		imageCount := 0
		for _, part := range message.Parts {
			switch part.Type {
			case PartText:
				if strings.TrimSpace(part.Text) == "" {
					return ErrRequestInvalid
				}
				if len(part.Text) > MaxMessageBytes {
					return ErrRequestInvalid
				}
				total += len(part.Text)
			case PartImage:
				// 图片只允许出现在用户消息：assistant/tool 角色的历史消息带图
				// 没有语义，只会放大出站体积。
				if message.Role != RoleUser {
					return ErrRequestInvalid
				}
				if part.Image == nil || !ValidImageMediaType(part.Image.MediaType) {
					return ErrRequestInvalid
				}
				if part.Image.Data == "" || len(part.Image.Data) > MaxImageBase64Bytes {
					return ErrRequestInvalid
				}
				imageCount++
				if imageCount > MaxImagesPerMessage {
					return ErrRequestInvalid
				}
				total += len(part.Image.Data)
			default:
				return ErrRequestInvalid
			}
		}
	}
	if total > MaxTotalInputBytes {
		return ErrRequestInvalid
	}
	return nil
}

// InputBytes 返回请求正文的 UTF-8 字节规模，供记账与 trace 使用。
func (r CompletionRequest) InputBytes() int {
	total := len(r.System)
	for _, tool := range r.Tools {
		total += len(tool.Name) + len(tool.Description) + len(tool.Parameters)
	}
	for _, message := range r.Messages {
		total += len(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += len(call.Name) + len(call.Arguments)
		}
		for _, part := range message.Parts {
			total += len(part.Text)
			if part.Image != nil {
				total += len(part.Image.Data)
			}
		}
	}
	return total
}

// ValidImageMediaType 限定可出站的图片类型。集合刻意保守：jpeg/png/webp/gif
// 是主流视觉端点都接受的公约数；svg 是文本可注入载体，永不进入。
func ValidImageMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

// TruncateBytes 按 UTF-8 字符边界截断，避免截出半个多字节字符。
func TruncateBytes(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	cut := value[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
