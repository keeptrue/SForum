// Package ai 是权限感知的 AI 网关服务层。Support/AI 保持纯基础设施：契约、
// 协议翻译、存储与执行；这里回答「谁能配置、谁能查看」。
package ai

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// BotAccountReader 读取充当 AI 助手的机器人账号。
type BotAccountReader interface {
	ReplyBotAccount(ctx context.Context) (int64, string, bool, error)
}

// CredentialStore 是密钥写入与状态查询的最小面。它以接口声明，让服务层不必
// 依赖 Secret Store 的具体实现。
type CredentialStore interface {
	// Put 写入新版本并返回版本号。
	Put(ctx context.Context, reference, plaintext, actor string) (int64, error)
	// Configured 报告该引用是否已有可用密钥。
	Configured(ctx context.Context, reference string) (bool, error)
}

// CredentialReader 把密钥引用解析为明文。它只服务于只读探测（列出供应商模型）：
// 那一步需要已保存的密钥，但明文不得进入任何回传路径，也不得被缓存。
type CredentialReader interface {
	ResolveAIKey(ctx context.Context, reference string) (string, error)
}

// ModelLister 执行一次只读的模型列表探测。
type ModelLister interface {
	ListModels(ctx context.Context, profile supportai.Profile, apiKey string) (supportai.ModelList, error)
}

type Service struct {
	settings supportai.SettingsStore
	gateway  *supportai.Gateway
	usage    supportai.UsageStore
	traces   supportai.ExecutionStore
	// replyOutcomes 读「这条触发最后有没有变成一条评论」。它与 traces 是两个
	// 问题：traces 记每一次模型调用，这里记一次生成的结果。
	replyOutcomes supportai.ReplyOutcomeStore
	credentials   CredentialStore
	reader        CredentialReader
	models        ModelLister
	botAccounts   BotAccountReader
	// purposes 是用途登记表：解析生效参数（成本等级、工具白名单、失败姿态）并
	// 供控制台枚举。它是进程级只读声明，不是可配置项。
	purposes *supportai.PurposeRegistry
	// defaultLocale 提供站点默认语言，作为工具调用的 locale 事实；缺省时为空。
	defaultLocale func(context.Context) (string, error)
	// toolCatalog 是站内已注册 AI 工具的只读枚举面；缺省时控制台退化为手动输入。
	toolCatalog supportai.ToolCatalog
}

// Config 汇总服务层依赖。除 Settings 外均可缺省，缺省时对应视图返回空结果
// 而不是报错，让一个尚未接入存储的部署仍能打开配置页。
type Config struct {
	Settings supportai.SettingsStore
	// Purposes 是用途登记表。缺省时使用 Core 内置用途，保证服务层不会因为少传
	// 一个可选依赖就失去用途语义。
	Purposes *supportai.PurposeRegistry
	Gateway  *supportai.Gateway
	Usage    supportai.UsageStore
	Traces   supportai.ExecutionStore
	// ReplyOutcomes 是回复结果的读面。缺省时控制台该区块返回空列表，而不是
	// 让整页打不开——一个还没跑过回复的部署本来就没有结果可读。
	ReplyOutcomes supportai.ReplyOutcomeStore
	Credentials   CredentialStore
	// CredentialReader 与 Models 只服务「获取模型列表」这一条只读路径；缺省时该
	// 能力报告不可用，其余配置功能不受影响。
	CredentialReader CredentialReader
	Models           ModelLister
	BotAccounts      BotAccountReader
	// DefaultLocale 返回站点默认语言（BCP-47）。缺省时工具调用的 locale 为空，
	// 插件按「未提供」处理——它是一项可选事实，不是必需依赖。
	DefaultLocale func(ctx context.Context) (string, error)
}

func NewService(cfg Config) *Service {
	purposes := cfg.Purposes
	if purposes == nil {
		purposes = supportai.DefaultPurposeRegistry()
	}
	return &Service{
		settings:      cfg.Settings,
		purposes:      purposes,
		gateway:       cfg.Gateway,
		usage:         cfg.Usage,
		traces:        cfg.Traces,
		replyOutcomes: cfg.ReplyOutcomes,
		credentials:   cfg.Credentials,
		reader:        cfg.CredentialReader,
		models:        cfg.Models,
		botAccounts:   cfg.BotAccounts,
		defaultLocale: cfg.DefaultLocale,
	}
}

// DefaultLocale 返回站点默认语言。它是 aireply.LocaleSource 的实现：工具调用
// 需要「现在用什么语言检索」时读这里，而不是从对话内容里猜。
func (s *Service) DefaultLocale(ctx context.Context) (string, error) {
	if s == nil || s.defaultLocale == nil {
		return "", nil
	}
	return s.defaultLocale(ctx)
}

// ReplyBot 返回当前充当 AI 助手的账号，供控制台显示。没有机器人账号时返回
// Configured=false——那意味着回复功能不会生效，而不是出错。
func (s *Service) ReplyBot(ctx context.Context, actor identity.Actor) (supportai.ReplyBotInfo, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.ReplyBotInfo{}, identity.ErrPermissionDenied
	}
	if s == nil || s.botAccounts == nil {
		return supportai.ReplyBotInfo{}, nil
	}
	userID, username, ok, err := s.botAccounts.ReplyBotAccount(ctx)
	if err != nil {
		return supportai.ReplyBotInfo{}, err
	}
	if !ok {
		return supportai.ReplyBotInfo{}, nil
	}
	return supportai.ReplyBotInfo{UserID: userID, Username: username, Configured: true}, nil
}

// Gateway 暴露底层网关，供 Core 用途（审核建议、摘要等）直接调用。它不经过
// 权限校验：调用方是服务端流程，已经完成了自己的授权。
func (s *Service) Gateway() *supportai.Gateway {
	if s == nil {
		return nil
	}
	return s.gateway
}

func (s *Service) GetSettings(ctx context.Context, actor identity.Actor) (supportai.Settings, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.Settings{}, identity.ErrPermissionDenied
	}
	if s == nil || s.settings == nil {
		return supportai.RecommendedSettings().Normalized(), nil
	}
	return s.settings.GetSettings(ctx)
}

// UpdateSettings 在写入前做策略校验。越界值直接拒绝，而不是截断到边界。
func (s *Service) UpdateSettings(ctx context.Context, actor identity.Actor, settings supportai.Settings) (supportai.Settings, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.Settings{}, identity.ErrPermissionDenied
	}
	if err := settings.Validate(); err != nil {
		return supportai.Settings{}, err
	}
	if s == nil || s.settings == nil {
		return supportai.Settings{}, supportai.ErrSettingsInvalid
	}
	return s.settings.SaveSettings(ctx, settings, actor.ID)
}

// ResetSettings 恢复推荐默认值。它不会清空 Secret Store 中的密钥：密钥值不在
// 配置文档里，重置只影响引用与策略。
//
// 供应商结构与成本等级绑定刻意保留：它们是运营者填过的资产（base_url、model、
// 单价），一键重置的承诺是「恢复推荐策略」，不是「清空我的配置」。只重置策略
// 字段，profiles 与 costClassProfiles 原样保留。
func (s *Service) ResetSettings(ctx context.Context, actor identity.Actor) (supportai.Settings, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.Settings{}, identity.ErrPermissionDenied
	}
	if s == nil || s.settings == nil {
		return supportai.RecommendedSettings().Normalized(), nil
	}
	current, err := s.settings.GetSettings(ctx)
	if err != nil {
		return supportai.Settings{}, err
	}
	reset := supportai.RecommendedSettings()
	if len(current.Profiles) > 0 {
		reset.Profiles = current.Profiles
		// 成本绑定必须跟着保留的 profiles 走，否则界面会指向一个不存在的供应商。
		reset.CostClassProfiles = current.CostClassProfiles
	}
	return s.settings.ResetSettings(ctx, reset, actor.ID)
}

// Usage 返回站点级用量，供成本看板展示。它不带发起方维度：那是 trace 的职责。
func (s *Service) Usage(ctx context.Context, actor identity.Actor, now time.Time) (supportai.UsageByScope, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.UsageByScope{}, identity.ErrPermissionDenied
	}
	if s == nil || s.usage == nil {
		return supportai.UsageByScope{}, nil
	}
	return s.usage.Snapshot(ctx, now, "", 0)
}

// SaveProfileCredential 为指定 profile 写入新密钥。明文只在这一次请求里存在：
// 它被写进 Secret Store 后立即丢弃，设置文档与后续读取都只看到引用。
func (s *Service) SaveProfileCredential(ctx context.Context, actor identity.Actor, profileID, plaintext string) (int64, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return 0, identity.ErrPermissionDenied
	}
	if s == nil || s.credentials == nil || s.settings == nil {
		return 0, supportai.ErrCredentialMissing
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	profile, ok := settings.Profile(profileID)
	if !ok {
		return 0, supportai.ErrProfileUnavailable
	}
	return s.credentials.Put(ctx, profile.APIKeyRef, plaintext, actorDisplayName(actor))
}

// resolveDiagnoseProfile 选出诊断要探测的 profile，顺序与成本等级的推荐使用
// 顺序一致，并返回实际命中的等级。
//
// 只用 economy 会让「只绑定了 standard/premium」的站点得到一个与事实不符的
// 结论：配置其实是能用的，诊断却说没有可用提供商。
func resolveDiagnoseProfile(settings supportai.Settings) (supportai.Profile, string, error) {
	if profile, err := settings.ResolveProfile(supportai.CostClassEconomy, ""); err == nil {
		return profile, supportai.CostClassEconomy, nil
	}
	if profile, err := settings.ResolveProfile(supportai.CostClassStandard, ""); err == nil {
		return profile, supportai.CostClassStandard, nil
	}
	if profile, err := settings.ResolveProfile(supportai.CostClassPremium, ""); err == nil {
		return profile, supportai.CostClassPremium, nil
	}
	return supportai.Profile{}, "", supportai.ErrProfileUnavailable
}

// Diagnose 发一次真实的最小调用，回答「现在这套配置能不能用」。前三类问题
// （开关关闭、没有可用提供商、密钥缺失）在本地就能判定，不必花钱发请求；
// 剩下的一律走完整的网关路径，因此延迟、用量、失败原因都是真的。
//
// 它刻意不绕过闸门：被配额拦住本身就是一条诊断结论，而且绕过会让「测试」
// 成为配额系统的一个后门。
func (s *Service) Diagnose(ctx context.Context, actor identity.Actor, message string) (supportai.DiagnoseResult, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.DiagnoseResult{}, identity.ErrPermissionDenied
	}
	if s == nil || s.settings == nil || s.gateway == nil {
		return supportai.DiagnoseResult{
			Reason: supportai.DiagnoseReasonDisabled,
			Error:  "AI gateway is not wired in this deployment",
		}, nil
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return supportai.DiagnoseResult{}, err
	}
	if !settings.Enabled {
		return supportai.DiagnoseResult{Reason: supportai.DiagnoseReasonDisabled}, nil
	}
	profile, costClass, err := resolveDiagnoseProfile(settings)
	if err != nil {
		return supportai.DiagnoseResult{Reason: supportai.DiagnoseReasonNoProvider}, nil
	}
	result := supportai.DiagnoseResult{
		ProviderID: profile.ID,
		Model:      profile.Model,
		Protocol:   profile.Protocol,
		CostClass:  costClass,
	}
	configured := false
	if s.credentials != nil {
		configured, err = s.credentials.Configured(ctx, profile.APIKeyRef)
		if err != nil {
			return supportai.DiagnoseResult{}, err
		}
	}
	if !configured {
		result.Reason = supportai.DiagnoseReasonNoCredential
		return result, nil
	}
	probe := strings.TrimSpace(message)
	if probe == "" {
		probe = supportai.DefaultDiagnoseMessage
	}
	completion, err := s.gateway.Execute(ctx, supportai.CompletionRequest{
		Purpose:       supportai.DiagnosePurpose,
		CostClass:     costClass,
		System:        "这是一次连通性探测。严格按用户的要求回复，不要添加任何其它内容。",
		Messages:      []supportai.Message{{Role: supportai.RoleUser, Parts: []supportai.Part{{Type: supportai.PartText, Text: probe}}}},
		MaxTokens:     supportai.DiagnoseMaxTokens,
		PromptVersion: supportai.DiagnosePromptVersion,
	})
	if err != nil {
		var unavailable *supportai.UnavailableError
		if errors.As(err, &unavailable) {
			// 空补全是「连接正常、模型没产出」，不是传输失败：诊断的 ok 描述的是
			// 往返是否成立，reason 描述的是模型给了什么。把它报成 provider_failed
			// 会让运营者去查网络与密钥，而真正要改的是输出预算或模型选择。
			if unavailable.Reason == supportai.ReasonEmptyCompletion {
				result.OK = true
				result.LatencyMS = completion.LatencyMS
				result.InputTokens = completion.Usage.InputTokens
				result.OutputTokens = completion.Usage.OutputTokens
				result.StopReason = completion.StopReason
				result.Reason = supportai.DiagnoseReasonEmptyReply
				result.Error = diagnoseEmptyReplyHint(completion.StopReason, strings.TrimSpace(completion.Reasoning) != "")
				return result, nil
			}
			result.Reason = unavailable.Reason
			result.Error = unavailable.Error()
			return result, nil
		}
		result.Reason = supportai.DiagnoseReasonProviderFailed
		result.Error = err.Error()
		return result, nil
	}
	result.OK = true
	result.Reply = completion.Text
	result.LatencyMS = completion.LatencyMS
	result.InputTokens = completion.Usage.InputTokens
	result.OutputTokens = completion.Usage.OutputTokens
	result.StopReason = completion.StopReason
	// 调用成功但没有正文，仍然不是「连接正常」：界面上会出现一条空白的回复条，
	// 运营者只会以为功能坏了。这里把它变成一条可解释的结论——停止原因说明
	// 预算是否被推理消耗完，运营者据此调整预算而不是反复重试。
	if strings.TrimSpace(completion.Text) == "" && len(completion.ToolCalls) == 0 {
		result.Reason = supportai.DiagnoseReasonEmptyReply
		result.Error = diagnoseEmptyReplyHint(completion.StopReason, strings.TrimSpace(completion.Reasoning) != "")
	}
	return result, nil
}

// diagnoseEmptyReplyHint 把「成功的空回复」翻译成一句可执行的建议。
//
// 它区分两种事实：模型产出了思考但没有正文（reasoningOnly），与模型什么都没产出。
// 前者的动作是调预算或换模型，后者才要怀疑协议兼容性——把两者说成同一句话，运营者
// 只能靠试。
func diagnoseEmptyReplyHint(stopReason string, reasoningOnly bool) string {
	if reasoningOnly {
		return "模型只产出了思考内容（reasoning / thinking），没有产出正文：推理类模型会先把输出预算消耗在思考上。请提高该供应商的最大输出 token 后重试，或为该成本等级绑定一个非推理模型。"
	}
	switch stopReason {
	case supportai.StopReasonLength:
		return "模型在输出预算内结束了（finish_reason=length）：推理类模型通常先把预算消耗在思考上，因此没有产出正文。请提高该供应商的最大输出 token 后重试。"
	case supportai.StopReasonFiltered:
		return "模型输出被内容过滤拦下（finish_reason=content_filter），没有产生正文。"
	default:
		return "供应商返回了一次成功的调用，但没有正文内容。可能是该模型不兼容当前协议解析，或返回了空 completion。"
	}
}

// Purposes 返回用途登记表，供控制台枚举站内已声明的 AI 用途。
func (s *Service) Purposes() []supportai.PurposeDeclaration {
	if s == nil || s.purposes == nil {
		return supportai.BuiltinPurposes()
	}
	return s.purposes.Purposes()
}

// ReplySystemPrompt 返回运营者配置的回复系统指令（自定义或内置默认），**不含**
// 固定安全尾注。它实现 aireply.ReplyParameterSource：生成器在每次回复时读取，
// 配置改完立即生效。
//
// 尾注与「本次可调用工具」段由生成器用 supportai.ComposeReplySystemPrompt 组装：
// 工具清单只有运行期才知道（用途白名单 ∩ 登记表 ∩ 本次真的会带工具），而尾注
// 必须永远排在它之后。把两段分开，是这个顺序唯一可靠的实现方式。
func (s *Service) ReplySystemPrompt(ctx context.Context) (string, error) {
	if s == nil || s.settings == nil {
		return "", nil
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.Reply.BaseSystemPrompt(), nil
}

// ReplyPurposeParameters 返回回复用途当前生效的参数（成本等级、工具白名单、提示词
// 版本）。它实现 aireply.ReplyParameterSource：生成器在每次回复时读取，配置改完
// 立即生效。
//
// 解析顺序由用途解析器统一决定：站点按用途覆盖 > 用途声明的默认值。回复不再是
// 特例——它的工具覆盖来自 Purposes["forum.reply"]，与其它用途同一条路径。
func (s *Service) ReplyPurposeParameters(ctx context.Context) (aireply.ReplyPurposeParameters, error) {
	if s == nil || s.settings == nil {
		return aireply.ReplyPurposeParameters{}, nil
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return aireply.ReplyPurposeParameters{}, err
	}
	declaration, ok := s.purposes.Lookup(aireply.ReplyPurpose)
	if !ok {
		// 登记表必定包含回复用途（DefaultPurposeRegistry 是编译期常量）。真缺失
		// 时如实报错，由生成器回退到内置默认参数，而不是伪造一份声明。
		return aireply.ReplyPurposeParameters{}, supportai.ErrPurposeUnknown
	}
	allowed, overridden := declaration.ToolAllowlist, false
	if configured, exists := settings.Purposes[declaration.ID]; exists {
		if names, over := configured.ToolAllowlist(); over {
			allowed, overridden = names, true
		}
	}
	return aireply.ReplyPurposeParameters{
		CostClass:     declaration.CostClass,
		ToolsAllowed:  append([]string(nil), allowed...),
		ToolsOverride: overridden,
		PromptVersion: declaration.PromptVersion,
		MaxTokens:     replyMaxTokens(settings, declaration.CostClass),
	}, nil
}

// replyMaxTokens 返回回复用途的输出预算：该用途成本等级绑定 profile 的
// Defaults.MaxTokens（控制台 providers 页签可改）。
//
// 它必须来自 profile 而不是一个写死的常量：推理类模型会把预算先花在思考上，
// 上限太小就永远写不出正文（表现为「调用成功但没有回答」，修复前更糟——思维链
// 会被当成回答发布）。解析不出 profile 时返回 0，由生成器回落到内置下限。
func replyMaxTokens(settings supportai.Settings, costClass string) int {
	profile, err := settings.ResolveProfile(costClass, "")
	if err != nil {
		return 0
	}
	return profile.Defaults.MaxTokens
}

// ReplyTriggerMode 返回当前生效的回复触发模式。它实现 aireply.TriggerModeSource：
// 触发器在每次事件到达时读取，配置改完立即生效。
func (s *Service) ReplyTriggerMode(ctx context.Context) (string, error) {
	if s == nil || s.settings == nil {
		return supportai.TriggerModeMention, nil
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return "", err
	}
	return settings.Reply.NormalizedTriggerMode(), nil
}

// TriagePurposeParameters 返回判定用途当前生效的参数（成本等级与提示词版本）。
// 它实现 aireply.TriageParameterSource。判定不允许工具，也没有按用途的站点工具
// 覆盖——唯一性就是这条边界的一部分，配置面不提供打开它的入口。
func (s *Service) TriagePurposeParameters(ctx context.Context) (aireply.TriagePurposeParameters, error) {
	if s == nil || s.purposes == nil {
		return aireply.TriagePurposeParameters{}, nil
	}
	declaration, ok := s.purposes.Lookup(supportai.CorePurposeForumReplyTriage)
	if !ok {
		return aireply.TriagePurposeParameters{}, supportai.ErrPurposeUnknown
	}
	return aireply.TriagePurposeParameters{
		CostClass:     declaration.CostClass,
		PromptVersion: declaration.PromptVersion,
	}, nil
}

// PurposeDetails 返回按用途的生效参数，供控制台按用途展示与配置。
func (s *Service) PurposeDetails(ctx context.Context, actor identity.Actor) ([]supportai.ValidatedPurpose, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.purposes == nil {
		return nil, nil
	}
	settings, err := s.GetSettings(ctx, actor)
	if err != nil {
		return nil, err
	}
	// 解析与生成器走同一个解析器：控制台看到的「生效值」与运行期实际使用的
	// 值不可能分叉。
	resolver := supportai.NewPurposeResolver(s.purposes, func() supportai.Settings { return settings })
	declarations := s.purposes.Purposes()
	out := make([]supportai.ValidatedPurpose, 0, len(declarations))
	for _, declaration := range declarations {
		resolution, err := resolver.Resolve(declaration.ID)
		if err != nil {
			continue
		}
		out = append(out, resolution)
	}
	return out, nil
}

// CredentialStatus 返回每个 profile 的密钥配置状态，供控制台显示「已配置」。
// 它永远不返回密钥值本身。
func (s *Service) CredentialStatus(ctx context.Context, actor identity.Actor) (map[string]bool, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	status := map[string]bool{}
	if s == nil || s.credentials == nil || s.settings == nil {
		return status, nil
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	for _, profile := range settings.Profiles {
		configured, err := s.credentials.Configured(ctx, profile.APIKeyRef)
		if err != nil {
			return nil, err
		}
		status[profile.ID] = configured
	}
	return status, nil
}

// actorDisplayName 用于 Secret Store 审计。Actor 只记录谁改过，不含密钥值。
func actorDisplayName(actor identity.Actor) string {
	if actor.ID <= 0 {
		return ""
	}
	return "user:" + strconv.FormatInt(actor.ID, 10)
}

func (s *Service) Executions(ctx context.Context, actor identity.Actor, limit int) ([]supportai.ExecutionRecord, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.traces == nil {
		return []supportai.ExecutionRecord{}, nil
	}
	return s.traces.ListExecutions(ctx, limit)
}

// RunTimeline 返回一次 AI run 的全部执行记录，按时间正序。
//
// 它是 A4 的唯一新查询面，形状与 Executions 一致：权限判定留在服务层（控制器
// 只做解码/编码），因此「谁能读回放」与「谁能读执行记录」不可能是两个答案。
//
// 空 runId 返回空列表而不是错误：历史记录（A3 之前）没有 run 归属，界面据此
// 不显示展开入口——那是正常状态，不是失败。查询不会退化成全表扫描，判定在
// 存储层（ListRunExecutions 对空 id 直接短路）。
func (s *Service) RunTimeline(ctx context.Context, actor identity.Actor, runID string, limit int) ([]supportai.ExecutionRecord, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.traces == nil {
		return []supportai.ExecutionRecord{}, nil
	}
	return s.traces.ListRunExecutions(ctx, supportai.ResourceTypeRun, runID, limit)
}

// ReplyOutcomes 返回最近的回复结果，按时间倒序。
//
// 权限沿用「谁能读执行记录」：两处都是运营诊断面，分成两个权限只会让一个管理员
// 看得见调用却看不见它有没有产出。缺省依赖时返回空列表而不是错误——没跑过回复
// 的部署读到空是正确行为。
func (s *Service) ReplyOutcomes(ctx context.Context, actor identity.Actor, limit int) ([]supportai.ReplyOutcome, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return nil, identity.ErrPermissionDenied
	}
	if s == nil || s.replyOutcomes == nil {
		return []supportai.ReplyOutcome{}, nil
	}
	return s.replyOutcomes.ListReplyOutcomes(ctx, limit)
}
