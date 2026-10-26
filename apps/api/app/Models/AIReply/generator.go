package aireply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// ReplyPurpose 是回复生成的用途标识。它与 Host 的用途登记表里那一条声明
	// 是同一个 id（supportai.CorePurposeForumReply）：成本等级、工具白名单、
	// 失败姿态都由那条声明与站点覆盖决定，这里不再自带常量。
	ReplyPurpose = supportai.CorePurposeForumReply
	// ReplyPromptVersion 是回复提示词的版本，用于缓存键与执行记录。它是用途声明
	// 里的值的别名，保持这个导出名是为了让调用方与历史记录读到同一个字符串。
	ReplyPromptVersion = supportai.CoreReplyPromptVersion
	// ReplyMaxTokens 是回复的输出下限。真正的预算来自该用途成本等级绑定的
	// profile（Profile.Defaults.MaxTokens，控制台可改）：推理类模型会先把预算
	// 消耗在思考上，写死一个小值等于让它们永远产出空回复。profile 解析不出来时
	// 用这个值兜底——那种情况下调用本身会在网关以 profile_unavailable 失败。
	ReplyMaxTokens = 512
)

// ReplyToolAllowlist 是回复用途声明的默认工具白名单。
//
// 它是 supportai 里那条用途声明的别名：白名单现在属于用途声明，不再是生成器的
// 私有常量。保留这个导出名是为了让「回复默认能用哪些工具」有一个显而易见的读法，
// 而不必记住登记表的键。
var ReplyToolAllowlist = supportai.CoreReplyToolAllowlist

var (
	ErrGeneratorUnavailable = errors.New("aireply: generator is not wired")
	ErrEmptyReply           = errors.New("aireply: model returned an empty reply")
)

// ReplyParameterSource 提供回复用途当前生效的参数：提示词、成本等级、工具
// 白名单。
//
// 三个值都来自同一个地方——用途声明 + 站点覆盖——因此它们是同一次解析的结果
// 而不是三个独立的读取：成本等级与工具边界必须对应同一份声明，分开读会出现
// 「用 A 声明的成本等级配 B 声明的工具」这种无法解释的组合。
type ReplyParameterSource interface {
	// ReplySystemPrompt 返回当前生效的系统指令（自定义或内置默认，含固定尾注）。
	ReplySystemPrompt(ctx context.Context) (string, error)
	// ReplyPurposeParameters 返回回复用途当前生效的参数。
	ReplyPurposeParameters(ctx context.Context) (ReplyPurposeParameters, error)
}

// LocaleSource 是可选能力：提供站点默认语言，作为工具调用的 locale 事实。
//
// 它不是 ReplyParameterSource 的必需部分：没有它的装配（单测、旧装配）里工具
// 拿到的 locale 是空值，插件按「未提供」处理。Identity 没有 per-user locale
// 字段，因此这里能给出的只有站点语言，不是提问者语言。
type LocaleSource interface {
	DefaultLocale(ctx context.Context) (string, error)
}

// ReplyPurposeParameters 是一次解析后的回复用途参数。
type ReplyPurposeParameters struct {
	CostClass     string
	ToolsAllowed  []string
	ToolsOverride bool
	PromptVersion string
	// MaxTokens 是本次回复的输出预算，取自该用途成本等级绑定 profile 的
	// Defaults.MaxTokens。0 表示解析不出 profile（回落到 ReplyMaxTokens）。
	MaxTokens int
}

// Generator 把一次待生成的回复变成真正的评论。
type Generator struct {
	threads   ThreadReader
	completer Completer
	poster    CommentPoster
	// parameters 可为 nil：那时使用内置默认提示词与用途声明的默认参数。
	parameters ReplyParameterSource
	// citations 可为 nil：那时引用能力整体关闭（不写格式要求、不校验、不落库）。
	// 它是 B4 的全部后端能力（校验 + 落库 + 事实复校验），由
	// PostgresCitationStore 实现。
	citations CitationStore
	// state 可为 nil：那时没有会话记忆，每次回复都从零开始（与引入前一致）。
	state TopicStateStore
	// toolCalls 可为 nil：那时工具轨迹只进日志（与引入前一致）。它只写不读——
	// 公开读走 Forum 侧的 ToolCallService，两边的可见性谓词不同源，不能合并。
	toolCalls ToolCallRecorder
	// toolSectioner 可为 nil：那时提示词里不出现「本次可调用的工具」段。
	// 它由 Host 的编排器实现，因为只有它同时知道用途白名单、登记表与本次是否
	// 真的会带上工具（步数预算、profile 是否支持 tools）。
	toolSectioner ToolSectioner
	// outcomes 可为 nil：那时「有没有真的回出一条评论」只留在日志里（与引入前
	// 一致）。装配后每次生成都在唯一出口写一行结果，并据它做幂等判定。
	outcomes OutcomeRecorder
}

// ToolSectioner 把「本次真的会发给模型的工具」渲染成提示词里的一段。
//
// 它必须是编排器的能力而不是登记表的：登记表只回答「站内有哪些工具」，而
// 提示词要说的是「这次你能调哪些」。两者在「用途白名单为空」「步数预算为 0」
// 「profile 不支持工具」这三种情况下不同，而恰恰是这三种情况最容易写出
// 「提示词说有、模型一调就是不可用」。
type ToolSectioner interface {
	ToolSection(ctx context.Context, costClass string, names []string) string
}

// CitationStore 是 B4 需要的全部引用能力。它把任务书里分开描述的三件事收进
// 一个接口，因为它们的实现必须是同一个可见性谓词（见 PostgresCitationStore）：
// 校验「这条依据可见吗」、写入「这条回复引用了什么」、复校验「上次的依据还在
// 吗」（B5 三重失效第 3 条）。
type CitationStore interface {
	CitationVerifier
	CitationRecorder
	FactVerifier
}

// TopicStateStore 是 B5 需要的全部状态能力。
//
// Load 的第二个返回值表示「有没有读到状态」：读到但内容无效（提示词版本不符、
// 超期）由 Effective 判，读不出来由 Load 判——把两者压成一个 bool 会让日志里
// 分不清「从没存过」与「存了但读不出来」。
type TopicStateStore interface {
	Load(ctx context.Context, topicID, botUserID int64) (TopicState, bool)
	Save(ctx context.Context, topicID, botUserID int64, state TopicState) error
}

func NewGenerator(threads ThreadReader, completer Completer, poster CommentPoster, parameters ReplyParameterSource) *Generator {
	return &Generator{threads: threads, completer: completer, poster: poster, parameters: parameters}
}

// WithCitations 装配 B4 的引用能力。返回同一生成器以便链式装配。
func (g *Generator) WithCitations(store CitationStore) *Generator {
	if g == nil {
		return g
	}
	g.citations = store
	return g
}

// WithToolCalls 装配工具轨迹的落库能力。返回同一生成器以便链式装配。
//
// 它不参与任何生成决策：装配了只是「多写一张表」，没装配回复照旧。这正是它与
// WithCitations 的区别——引用会改变提示词（要求模型写标记），轨迹只是记录。
func (g *Generator) WithToolCalls(store ToolCallRecorder) *Generator {
	if g == nil {
		return g
	}
	g.toolCalls = store
	return g
}

// WithToolSectioner 装配「本次可调用工具」的提示词段来源。
//
// 与 WithToolCalls 相反，它会改变模型读到的内容（system 多一段工具清单），
// 因此提示词版本必须与它一起演进：同一版号下出现两种 system 会让历史执行记录
// 无法解释产出它的到底是哪一种。
func (g *Generator) WithToolSectioner(sectioner ToolSectioner) *Generator {
	if g == nil {
		return g
	}
	g.toolSectioner = sectioner
	return g
}

// WithTopicState 装配 B5 的会话状态。返回同一生成器以便链式装配。
// 引用能力与会话状态必须成对装配：状态里的 VerifiedFacts 只能来自 B4 校验过的
// 引用，没有 citations 时状态永远采集不到任何事实（装配了也只是空转）。因此
// 缺 citations 时这里不装配——「配了状态却永远不记得任何事」比「没有状态」
// 更难排查。
func (g *Generator) WithTopicState(store TopicStateStore) *Generator {
	if g == nil {
		return g
	}
	if g.citations == nil {
		return g
	}
	g.state = store
	return g
}

// defaultReplyParameters 是没有任何配置来源时生效的参数：用途声明的成本等级与
// 工具白名单。它同时也是读配置失败时的回退值。
func defaultReplyParameters() ReplyPurposeParameters {
	declaration, ok := supportai.DefaultPurposeRegistry().Lookup(ReplyPurpose)
	if !ok {
		return ReplyPurposeParameters{CostClass: supportai.CostClassEconomy}
	}
	return ReplyPurposeParameters{
		CostClass:     declaration.CostClass,
		ToolsAllowed:  append([]string(nil), declaration.ToolAllowlist...),
		PromptVersion: declaration.PromptVersion,
	}
}

// resolveParameters 决定这次回复实际生效的参数。
//
// 读配置失败时回退到用途声明的默认值（成本等级 + Core 内置工具列表），而不是
// 变成「无白名单」或「全部工具」：插件工具必须由配置显式列名才能出现，Core
// 工具也不该因为一次设置读取失败而消失。
func (g *Generator) resolveParameters(ctx context.Context) (ReplyPurposeParameters, string) {
	fallback := defaultReplyParameters()
	if g == nil || g.parameters == nil {
		return fallback, ""
	}
	parameters, err := g.parameters.ReplyPurposeParameters(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load purpose parameters failed; using the declared default",
			"err", err)
		parameters = fallback
	}
	if strings.TrimSpace(parameters.CostClass) == "" {
		parameters.CostClass = fallback.CostClass
	}
	if parameters.ToolsAllowed == nil {
		parameters.ToolsAllowed = fallback.ToolsAllowed
	}
	if strings.TrimSpace(parameters.PromptVersion) == "" {
		parameters.PromptVersion = fallback.PromptVersion
	}
	// 读配置失败不该让整次回复失败：退回内置默认提示词仍然是有用的回答。
	systemOverride := ""
	if value, err := g.parameters.ReplySystemPrompt(ctx); err != nil {
		slog.WarnContext(ctx, "aireply: load configured system prompt failed; using the default",
			"err", err)
	} else {
		systemOverride = value
	}
	return parameters, systemOverride
}

// resolveMaxTokens 决定本次回复的输出预算。0 或负值回落到 ReplyMaxTokens：
// 参数来源没解析出 profile 时不该把预算变成 0（那会让请求在契约校验就被拒）。
func resolveMaxTokens(configured int) int {
	if configured <= 0 {
		return ReplyMaxTokens
	}
	return configured
}

// ImageAttachmentReader 是 ThreadReader 的可选扩展：能查出本次回复候选的
// 图片附件定位。PostgresReader 实现了它；测试 fake 不实现时回复退化为纯文本。
type ImageAttachmentReader interface {
	ReplyImageAttachments(ctx context.Context, topicID int64, parentCommentID int64) ([]ImageAttachment, error)
}

// resolveImageCandidates 查询候选图片附件。查询失败按「无图」处理：附件元
// 数据的一次读失败不该让整次回复失败。是否真正读取由 completer 按开关决定。
func (g *Generator) resolveImageCandidates(ctx context.Context, input ReplyJobInput) []ImageAttachment {
	reader, ok := g.threads.(ImageAttachmentReader)
	if !ok {
		return nil
	}
	attachments, err := reader.ReplyImageAttachments(ctx, input.TopicID, input.ParentCommentID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load image attachments failed; sending text only", "err", err)
		return nil
	}
	return attachments
}

// Generate 执行一次完整的生成：读上下文 → 组装提示词 → 调用模型 → 以机器人
// 身份发布。任何一步失败都返回错误，由队列按重试策略处理。
//
// 它刻意不做「先插一条占位评论再替换」：那需要额外的创建与更新两步，还要抑制
// 中间态的通知，而生成通常只要几秒。等真实体验证明有必要再加。
func (g *Generator) Generate(ctx context.Context, input ReplyJobInput) (err error) {
	if g == nil || g.threads == nil || g.completer == nil || g.poster == nil {
		return ErrGeneratorUnavailable
	}
	// 幂等：同一条触发只应产出一次回复。缓存命中或事件重复投递都会把同一个
	// prompt 再送一遍，而缓存里存的是上一次的正文——没有这条判定，重复投递会
	// 变成一条一模一样的第二条回复。
	if g.alreadyPosted(ctx, input) {
		slog.InfoContext(ctx, "aireply: reply already posted for this trigger; skipping",
			"topicId", input.TopicID, "triggerCommentId", input.TriggerCommentID)
		return nil
	}
	// completion 与 stage 提到函数作用域，是为了让唯一出口的 defer 能看见这次
	// 生成到底走到了哪一步、花了多少。没有它们的 defer 只能写「失败了」。
	var (
		completion       CompletionOutput
		publishedComment int64
		stage            = "complete"
	)
	defer func() {
		g.recordOutcome(ctx, input, replyOutcome(input, completion, publishedComment, stage, err))
	}()
	thread, err := g.threads.ReplyContext(ctx, input.TopicID, input.ParentCommentID, input.TriggerCommentID)
	if err != nil {
		// 读上下文失败发生在任何模型调用之前：单独标一个 stage，结果表才能把
		// 「帖子读不出来」与「模型没产出」分开，而不是都落在 generation_failed 上。
		stage = "context"
		return fmt.Errorf("load reply context: %w", err)
	}
	parameters, systemOverride := g.resolveParameters(ctx)
	// B5：先读状态、按三重失效裁决、渲染成提示词片段。任何一步失败都降级为
	// 「没有状态」——记忆缺失不该让回复失败（任务书易错点 3）。
	previousState, hadState := g.loadTopicState(ctx, input)
	stateFacts, invalidation := g.effectiveState(ctx, previousState, hadState, parameters.PromptVersion)
	stateBlock, _ := RenderTopicState(stateFacts)
	logStateInjection(ctx, input, invalidation)
	system, user := buildReplyPrompt(thread, systemOverride, replyPromptExtras{
		stateBlock:       stateBlock,
		citationsEnabled: g.citations != nil,
		toolSection:      g.resolveToolSection(ctx, parameters),
	})
	// 工具以提问者视角取数：可见性判定用触发者身份，位置信息用于限定
	// 「当前这栋楼」。语言来自站点默认值——读失败留空，工具按「不知道语言」
	// 工作，不因为一句可选事实而让整次回复失败。
	toolContext := supportai.ToolContext{
		ViewerUserID: input.TriggerUserID,
		// BotUserID 只让工具认出「机器人自己说过什么」（ai-self-history）；
		// 可见性仍然逐字用 ViewerUserID，不得拿它放宽任何读取范围。
		BotUserID: input.BotUserID,
		TopicID:   input.TopicID,
		CommentID: input.TriggerCommentID,
	}
	if source, ok := g.parameters.(LocaleSource); ok {
		if locale, err := source.DefaultLocale(ctx); err == nil {
			toolContext.Locale = strings.TrimSpace(locale)
		}
	}
	completion, err = g.completer.Complete(ctx, CompletionInput{
		Purpose:       ReplyPurpose,
		CostClass:     parameters.CostClass,
		PromptVersion: parameters.PromptVersion,
		System:        system,
		User:          user,
		MaxTokens:     resolveMaxTokens(parameters.MaxTokens),
		SubjectUserID: input.TriggerUserID,
		Images:        g.resolveImageCandidates(ctx, input),
		ActorUserID:   input.BotUserID,
		Tools:         append([]string(nil), parameters.ToolsAllowed...),
		ToolContext:   toolContext,
	})
	if err != nil {
		// 部分结果必须可观察：这次 run 已经执行过的工具与花掉的 token 是事实，
		// 重试的一方需要知道上次做到哪。断点续跑不做（River 的重试是整任务重跑），
		// 但「被丢弃」与「被观察到」是两回事。
		annotated := fmt.Errorf("complete reply (tool_calls=%d usage=%d/%d): %w",
			completion.ToolCalls, completion.Usage.InputTokens, completion.Usage.OutputTokens, err)
		logGeneratedReply(ctx, input, completion, parameters.PromptVersion, annotated)
		return annotated
	}
	// B4：先解析标记、剥离正文，再判空。顺序不能反——「整段正文只有标记」在剥离
	// 之前是非空的，剥离之后才是空回复。颠倒顺序会发出一条正文里只剩噪声的评论
	// （任务书易错点 2）。
	raw := completion.Text
	body, refs := parseCitationMarkers(raw)
	content := strings.TrimSpace(body)
	if len(refs) > 0 {
		content = normalizeCitationBody(body)
	}
	if content == "" {
		// 空回复不发布：一条空评论比没有回复更糟。
		//
		// 错误里带上停止原因与「模型是否产出了思考」：这是同一句话的唯一可诊断
		// 形态——finish_reason=length + 有思考＝预算被思考吃光（该调 profile 的
		// 最大输出 token），没有思考＝模型确实什么都没说。
		empty := fmt.Errorf("%w: stop_reason=%s reasoning=%t", ErrEmptyReply, completion.StopReason, strings.TrimSpace(completion.Reasoning) != "")
		logGeneratedReply(ctx, input, completion, parameters.PromptVersion, empty)
		return empty
	}
	// B4：逐条校验引用。校验失败的单条丢弃、零引用是正常结果，都不阻断发布；
	// 校验整体失败（存储不可用）同样降级为「无引用」——依据缺失不该让一条有用
	// 的回答发不出去。
	citations := g.verifyCitations(ctx, refs)
	stage = "publish"
	commentID, err := g.poster.PostBotComment(ctx, input.BotUserID, BotCommentInput{
		TopicID:   input.TopicID,
		ParentID:  input.ParentCommentID,
		Content:   content,
		Citations: citations,
	})
	if err != nil {
		postErr := fmt.Errorf("post bot comment: %w", err)
		logGeneratedReply(ctx, input, completion, parameters.PromptVersion, postErr)
		return postErr
	}
	publishedComment = commentID
	// 发布成功之后才落库引用、才写会话状态（记忆连续性 · 层 2 第 3 条）。
	// 发布失败却写状态，下一轮会引用一段没人看过的结论。
	runID := completion.RunID
	g.recordCitations(ctx, commentID, runID, citations)
	// 工具轨迹与引用同一时机：发布成功之后。失败不阻断（见 recordToolCalls）。
	g.recordToolCalls(ctx, commentID, input.TopicID, runID, completion.ToolTrace)
	g.saveTopicState(ctx, input, parameters.PromptVersion, runID, commentID, citations, previousState, stateFacts, hadState, invalidation)
	logGeneratedReply(ctx, input, completion, parameters.PromptVersion, nil)
	return nil
}

// verifyCitations 把解析出的指向变成已校验的引用。
//
// 三种情况都返回 nil 而不是错误：
//   - 没有装配引用能力（部署缺能力，记 Info 一次）；
//   - 模型一个标记都没写（最常见，不是异常）；
//   - 校验整体失败（存储不可用——回复本身可能仍然有用）。
//
// 逐条丢弃发生在存储层的 Verify 里，并自带 Warn 日志；这里只补一条汇总，
// 因为「模型在编引用」是一个需要运营者看见的行为模式。
func (g *Generator) verifyCitations(ctx context.Context, refs []CitationRef) []Citation {
	if len(refs) == 0 {
		return nil
	}
	if g.citations == nil {
		// 解析出了标记但没装配校验：正文已被剥离，标记不会泄漏到评论里，
		// 但运营者需要知道「这台部署丢弃了模型给出的依据」。
		slog.InfoContext(ctx, "aireply: citation markers stripped without a verifier",
			"markers", len(refs), "reason", "citations_unavailable")
		return nil
	}
	verified, err := g.citations.Verify(ctx, refs)
	if err != nil {
		slog.WarnContext(ctx, "aireply: citation verification failed; publishing without citations",
			"markers", len(refs), "err", err)
		return nil
	}
	verified = dedupeCitations(verified)
	if len(verified) > MaxCitationsPerReply {
		// 超出渲染上限：保留前 N 条（模型标注的顺序＝它在正文里提到的顺序）。
		slog.WarnContext(ctx, "aireply: citations truncated to the rendering limit",
			"verified", len(verified), "kept", MaxCitationsPerReply)
		verified = verified[:MaxCitationsPerReply]
	}
	if len(verified) < len(refs) {
		slog.WarnContext(ctx, "aireply: some citations were dropped as unverifiable",
			"markers", len(refs), "verified", len(verified))
	}
	return verified
}

// recordCitations 把已校验的引用落库。它只在发布成功之后被调用。
//
// 失败只记 Warn：引用是展示层的增强，一次落库失败不该让一条已发布的评论变成
// 失败的重试任务（那会重复发布）。
func (g *Generator) recordCitations(ctx context.Context, commentID int64, runID string, citations []Citation) {
	if g.citations == nil || len(citations) == 0 || commentID <= 0 {
		return
	}
	if err := g.citations.Record(ctx, commentID, runID, citations); err != nil {
		slog.WarnContext(ctx, "aireply: record citations failed",
			"commentId", commentID, "citations", len(citations), "err", err)
	}
}

// recordToolCalls 把本次 run 的工具轨迹落到产出评论名下。
//
// 失败只记 Warn：轨迹是展示层的增强，一次落库失败不该让一条已发布的评论变成
// 失败的重试任务（那会重复发布）。这与 recordCitations 是同一条纪律。
//
// 被拒绝的调用（unknown_tool / not_allowed / step_exhausted /
// budget_exhausted）一并写入：它们是运营者判断「模型在撞配置」的唯一证据，
// 公开面会在读取时过滤掉（见 Forum 的 ToolCallService）。
func (g *Generator) recordToolCalls(ctx context.Context, commentID, topicID int64, runID string, trace []supportai.ToolTraceEntry) {
	if g.toolCalls == nil || len(trace) == 0 || commentID <= 0 {
		return
	}
	calls := make([]ToolCall, 0, len(trace))
	for index, entry := range trace {
		calls = append(calls, ToolCall{
			// Seq 是切片下标而不是 entry.Step：并行调用共享同一个 Step。
			Seq:            index,
			Step:           entry.Step,
			Tool:           entry.Tool,
			Arguments:      entry.Arguments,
			ArgumentsBytes: entry.ArgumentsBytes,
			ResultBytes:    entry.ResultBytes,
			ResultDigest:   entry.ResultDigest,
			IsError:        entry.IsError,
			Failure:        entry.Failure,
			LatencyMS:      entry.LatencyMS,
		})
	}
	if err := g.toolCalls.Record(ctx, commentID, topicID, runID, calls); err != nil {
		slog.WarnContext(ctx, "aireply: record tool calls failed",
			"commentId", commentID, "calls", len(calls), "err", err)
	}
}

// resolveToolSection 取「本次真的会发给模型的工具」的提示词段。
//
// 读失败一律降级为空段：工具清单缺失只会让模型保守作答，而不该让整次回复失败。
func (g *Generator) resolveToolSection(ctx context.Context, parameters ReplyPurposeParameters) string {
	if g.toolSectioner == nil || len(parameters.ToolsAllowed) == 0 {
		return ""
	}
	return g.toolSectioner.ToolSection(ctx, parameters.CostClass, parameters.ToolsAllowed)
}

// logGeneratedReply 把一次回复的规模事实写进结构化日志：花了多少次模型调用、
// 多少次工具调用、多少 token，以及逐次工具轨迹。
//
// 级别规则：成功且无异常为 Info；发布失败、空回复、或轨迹里出现任何 Failure
// 一律 Warn——让「配置漂移导致模型在撞工具预算」自己浮出来，而不是等运营者
// 去猜为什么回答变差了。
//
// toolCalls 是真正执行的次数，toolCallsPerRun 是本次 run 记录到的调用次数
// （含被拒绝的）。两者不等本身就是结论：模型在要求它拿不到的工具。
func logGeneratedReply(ctx context.Context, input ReplyJobInput, output CompletionOutput, promptVersion string, failure error) {
	level := slog.LevelInfo
	if failure != nil {
		level = slog.LevelWarn
	}
	for _, entry := range output.ToolTrace {
		if entry.Failure != "" {
			level = slog.LevelWarn
			break
		}
	}
	attrs := []any{
		"topicId", input.TopicID,
		"purpose", ReplyPurpose,
		"modelCalls", output.ModelCalls,
		"toolCalls", output.ToolCalls,
		"toolCallsPerRun", len(output.ToolTrace),
		"inputTokens", output.Usage.InputTokens,
		"outputTokens", output.Usage.OutputTokens,
		"promptVersion", promptVersion,
		"tool_trace", toolTraceJSON(output.ToolTrace),
	}
	if failure != nil {
		attrs = append(attrs, "err", failure)
	}
	slog.Log(ctx, level, "aireply: reply generated", attrs...)
}

// toolTraceJSON 序列化轨迹供日志阅读。序列化失败不阻断任何路径：这是诊断信息，
// 不是回复的一部分。
func toolTraceJSON(trace []supportai.ToolTraceEntry) string {
	if len(trace) == 0 {
		return ""
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		return ""
	}
	return string(encoded)
}
