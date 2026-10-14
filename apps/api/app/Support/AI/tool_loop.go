package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

var (
	// ErrToolLoopUnavailable 表示工具循环缺少装配（没有登记表或没有网关）。
	ErrToolLoopUnavailable = errors.New("ai: tool loop is not wired")
	// ErrToolLoopStuck 表示模型在工具已撤下之后仍要求工具调用，无法产出回答。
	ErrToolLoopStuck = errors.New("ai: model kept requesting tools after the step budget was spent")
)

// ToolResult 是工具给模型的回答。
//
// 约定：工具「可以解释」的失败（参数非法、目标不存在、能力当前不可用）应作为
// IsError=true 的 ToolResult 返回，正文是给模型看的话；返回 error 表示工具内部
// 故障，编排层只把它记进日志，对模型给一句通用说明——因为模型的话会发布到
// 社区，内部错误串不该借它泄露。
type ToolResult struct {
	Content string
	IsError bool
}

// ToolContext 是一次工具调用可以依赖的会话事实。工具实现只能用它做判定，
// 不允许自己去猜「现在是谁在问」。
type ToolContext struct {
	// ViewerUserID 是提问者（触发本次回复的用户）。
	ViewerUserID int64
	// TopicID / CommentID 是本次对话发生的位置；0 表示不限定位置。
	TopicID   int64
	CommentID int64
	// Locale 是提问者的语言（可空），工具用它决定默认返回语言。
	Locale string
}

// Tool 是一个可被模型调用的受限能力。
type Tool interface {
	Definition() ToolDefinition
	Invoke(ctx context.Context, call ToolCall, env ToolContext) (ToolResult, error)
}

// ToolRegistry 是 Host 持有的工具登记表。插件贡献的工具同样登记在这里，
// 因此「站内有哪些工具」永远有一个可枚举、可审计的答案。
type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]Tool{}}
}

func (r *ToolRegistry) Register(tool Tool) error {
	if r == nil || tool == nil {
		return ErrToolLoopUnavailable
	}
	definition := tool.Definition()
	if !ValidToolName(definition.Name) {
		return fmt.Errorf("%w: invalid tool name %q", ErrToolLoopUnavailable, definition.Name)
	}
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	if _, exists := r.tools[definition.Name]; exists {
		return fmt.Errorf("%w: duplicate tool %s", ErrToolLoopUnavailable, definition.Name)
	}
	r.tools[definition.Name] = tool
	return nil
}

// Lookup 按名字取工具；未注册时返回 ok=false。
func (r *ToolRegistry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.tools[strings.TrimSpace(name)]
	return tool, ok
}

// Descriptors 返回白名单内已登记工具的声明，顺序与白名单一致，并受
// MaxToolsPerRequest 约束。未登记的名字被跳过而不是报错：插件卸载后，
// 历史白名单仍应该能跑，而不是把整个用途拖死。
func (r *ToolRegistry) Descriptors(names []string) []ToolDefinition {
	if r == nil || len(names) == 0 {
		return nil
	}
	descriptors := make([]ToolDefinition, 0, len(names))
	for _, name := range names {
		if len(descriptors) >= MaxToolsPerRequest {
			break
		}
		tool, ok := r.Lookup(name)
		if !ok {
			continue
		}
		descriptors = append(descriptors, tool.Definition())
	}
	return descriptors
}

// Orchestrator 在网关之上执行「模型 ↔ 工具」的循环。它归 Host 所有：步数预算、
// 闸门、执行记录与用户配额口径必须在同一处，否则每个用途都会长出自己的版本，
// 运营者也没法解释一次回复到底花了多少。
type Orchestrator struct {
	gateway        *Gateway
	registry       *ToolRegistry
	maxResultBytes int
}

type OrchestratorConfig struct {
	Gateway  *Gateway
	Registry *ToolRegistry
	// MaxResultBytes 为 0 时使用 MaxToolResultBytes。
	MaxResultBytes int
}

func NewOrchestrator(config OrchestratorConfig) *Orchestrator {
	limit := config.MaxResultBytes
	if limit <= 0 {
		limit = MaxToolResultBytes
	}
	return &Orchestrator{gateway: config.Gateway, registry: config.Registry, maxResultBytes: limit}
}

// RunInput 是一次「带工具的补全」的全部输入。
type RunInput struct {
	Purpose       string
	CostClass     string
	System        string
	User          string
	// Images 是首条用户消息要携带的图片部件（已编码）。只进首条消息：
	// 后续工具步的历史消息带图只会重复计费。
	Images    []Part
	MaxTokens int
	PromptVersion string
	Metadata      Metadata
	// SubjectUserID 只计入首次调用：用户配额按「一次回复」结算，而不是按步数。
	// 站点与扩展口径仍在每一步真实计量。
	SubjectUserID int64
	// ToolNames 是本次运行允许调用的工具白名单；空表示纯文本补全。
	ToolNames []string
	// ToolContext 传给每次工具调用的会话事实。
	ToolContext ToolContext
}

// RunResult 汇总本次运行的全部产出与成本。
type RunResult struct {
	Text           string
	StopReason     string
	Usage          Usage
	ModelCalls     int
	ToolCalls      int
	PromptVersion  string
	ConfigRevision int64
}

// Run 执行循环：调用模型 → 若要求工具则执行并把结果回填 → 直到得到最终回答。
//
// 边界都在这里定死：
//   - 工具调用次数不超过站点闸门 toolCallsPerReply（0 表示停用工具）；
//   - 每一步都是一次独立的网关调用，因此逐步过闸门、逐步留执行记录；
//   - 用户配额只在首次调用计入，工具步数不重复扣提问者的每日额度；
//   - 工具结果按单条与单次累计双重截断，避免一次检索塞满上下文；
//   - 模型在工具撤下后仍要求调用且没有正文时，返回 ErrToolLoopStuck 而不是
//     发出一个无法满足的请求。
func (o *Orchestrator) Run(ctx context.Context, input RunInput) (RunResult, error) {
	if o == nil || o.gateway == nil {
		return RunResult{}, ErrToolLoopUnavailable
	}
	settings, err := o.gateway.Settings(ctx)
	if err != nil {
		return RunResult{}, err
	}
	steps := settings.Gates.ResolvedToolCallsPerReply()
	if steps > MaxToolCallsPerReply {
		steps = MaxToolCallsPerReply
	}
	descriptors := o.registry.Descriptors(input.ToolNames)
	if steps <= 0 || len(descriptors) == 0 || !o.toolsSupported(settings, input.CostClass) {
		descriptors = nil
	}
	remaining := 0
	if descriptors != nil {
		remaining = steps
	}

	userText := input.User
	if len(input.Images) > 0 {
		userText += fmt.Sprintf("\n\n（本条发言附带 %d 张图片，请结合图片内容回答。）", len(input.Images))
	}
	firstParts := make([]Part, 0, len(input.Images)+1)
	firstParts = append(firstParts, Part{Type: PartText, Text: userText})
	firstParts = append(firstParts, input.Images...)
	messages := []Message{{Role: RoleUser, Parts: firstParts}}
	result := RunResult{PromptVersion: input.PromptVersion}
	budget := MaxToolResultRunBytes
	firstCall := true
	for {
		request := CompletionRequest{
			Purpose:       input.Purpose,
			CostClass:     input.CostClass,
			System:        input.System,
			Messages:      messages,
			MaxTokens:     input.MaxTokens,
			PromptVersion: input.PromptVersion,
			Metadata:      input.Metadata,
			Tools:         descriptors,
		}
		if descriptors != nil {
			request.ToolChoice = "auto"
		}
		if firstCall {
			request.SubjectUserID = input.SubjectUserID
		}
		completion, err := o.gateway.Execute(ctx, request)
		if err != nil {
			// 部分结果仍然返回：调用方需要知道这次回复已经花掉了几次调用。
			return result, err
		}
		firstCall = false
		result.ModelCalls++
		result.Usage = addUsage(result.Usage, completion.Usage)
		result.ConfigRevision = completion.ConfigRevision
		if len(completion.ToolCalls) == 0 {
			result.Text = completion.Text
			result.StopReason = completion.StopReason
			return result, nil
		}
		if descriptors == nil {
			// 工具已经不在请求里，模型仍然要求调用。有正文就用正文收尾，否则拒绝
			// 编造工具结果。
			if strings.TrimSpace(completion.Text) != "" {
				result.Text = completion.Text
				result.StopReason = completion.StopReason
				return result, nil
			}
			return result, ErrToolLoopStuck
		}
		assistant := Message{Role: RoleAssistant, ToolCalls: completion.ToolCalls}
		if strings.TrimSpace(completion.Text) != "" {
			assistant.Parts = []Part{{Type: PartText, Text: completion.Text}}
		}
		messages = append(messages, assistant)
		for _, call := range completion.ToolCalls {
			if remaining <= 0 {
				// 超出预算的调用也必须给出一条结果，否则 assistant 消息里的
				// tool_calls 没有配对，供应商会直接拒绝下一次请求。
				messages = append(messages, Message{
					Role:       RoleTool,
					ToolCallID: call.ID,
					Parts:      []Part{{Type: PartText, Text: "超出本次回复的工具调用上限，未执行。"}},
				})
				continue
			}
			remaining--
			result.ToolCalls++
			content, used := o.execute(ctx, call, input.ToolContext, input.ToolNames, budget)
			budget -= used
			messages = append(messages, Message{
				Role:       RoleTool,
				ToolCallID: call.ID,
				Parts:      []Part{{Type: PartText, Text: content}},
			})
		}
		if remaining <= 0 {
			// 预算用完：撤下工具声明，最后一步只能产出回答。
			descriptors = nil
		}
		if result.ModelCalls > MaxToolCallsPerReply+2 {
			// 理论不可达（每轮至少消耗一个工具预算），留作最后一道保险。
			return result, ErrToolLoopStuck
		}
	}
}

// execute 运行一次工具调用并返回给模型看的正文，以及它消耗的结果预算。
//
// 「工具不存在」与「不在白名单」都不返回错误：那是模型自己的越界，应当被如实
// 告知并继续回答，而不是让整次回复失败。
func (o *Orchestrator) execute(ctx context.Context, call ToolCall, env ToolContext, allowlist []string, budget int) (string, int) {
	name := strings.TrimSpace(call.Name)
	tool, ok := o.registry.Lookup(name)
	if !ok || !toolAllowed(allowlist, name) {
		content := "工具 " + name + " 不可用。"
		return content, len(content)
	}
	if budget <= 0 {
		content := "本次回复的工具结果预算已用尽，请基于已有信息直接回答。"
		return content, len(content)
	}
	limit := o.maxResultBytes
	if budget < limit {
		limit = budget
	}
	result, err := tool.Invoke(ctx, call, env)
	if err != nil {
		// 内部故障：日志留全貌，模型只拿到一句不含实现细节的说明。
		slog.WarnContext(ctx, "ai: tool invocation failed", "tool", name, "err", err)
		content := "工具 " + name + " 执行失败，请基于已有信息继续，不要重复同样的调用。"
		return content, len(content)
	}
	content := strings.TrimSpace(result.Content)
	if content == "" {
		content = "（" + name + " 没有返回内容）"
	}
	if result.IsError {
		content = "工具报告错误：" + content
	}
	truncated := TruncateBytes(content, limit)
	if truncated != content {
		truncated += "\n…（结果过长，已截断）"
	}
	return truncated, len(truncated)
}

// toolsSupported 判断当前成本等级解析出的 profile 是否支持工具调用。
// 解析失败时照常带上工具：真正的失败原因（没有可用提供商、密钥缺失）由网关在
// 调用时给出，这里不制造第二个失败来源。
func (o *Orchestrator) toolsSupported(settings Settings, costClass string) bool {
	profile, err := settings.ResolveProfile(costClass, "")
	if err != nil {
		return true
	}
	return profile.ToolsSupported()
}

func toolAllowed(allowlist []string, name string) bool {
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

func addUsage(a, b Usage) Usage {
	return Usage{
		InputTokens:  a.InputTokens + b.InputTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens,
		CachedTokens: a.CachedTokens + b.CachedTokens,
	}
}
