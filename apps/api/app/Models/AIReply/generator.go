package aireply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

const (
	// ReplyPurpose 是回复生成的用途标识。它与 Host 的用途登记表里那一条声明
	// 是同一个 id（supportai.CorePurposeForumReply）：成本等级、工具白名单、
	// 失败姿态都由那条声明与站点覆盖决定，这里不再自带常量。
	ReplyPurpose = supportai.CorePurposeForumReply
	// ReplyPromptVersion 是回复提示词的版本，用于缓存键与执行记录。它是用途声明
	// 里的值的别名，保持这个导出名是为了让调用方与历史记录读到同一个字符串。
	ReplyPromptVersion = supportai.CoreReplyPromptVersion
	// ReplyMaxTokens 是单次回复的输出上限。
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

// ReplyPurposeParameters 是一次解析后的回复用途参数。
type ReplyPurposeParameters struct {
	CostClass     string
	ToolsAllowed  []string
	ToolsOverride bool
	PromptVersion string
}

// Generator 把一次待生成的回复变成真正的评论。
type Generator struct {
	threads   ThreadReader
	completer Completer
	poster    CommentPoster
	// parameters 可为 nil：那时使用内置默认提示词与用途声明的默认参数。
	parameters ReplyParameterSource
}

func NewGenerator(threads ThreadReader, completer Completer, poster CommentPoster, parameters ReplyParameterSource) *Generator {
	return &Generator{threads: threads, completer: completer, poster: poster, parameters: parameters}
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
func (g *Generator) Generate(ctx context.Context, input ReplyJobInput) error {
	if g == nil || g.threads == nil || g.completer == nil || g.poster == nil {
		return ErrGeneratorUnavailable
	}
	thread, err := g.threads.ReplyContext(ctx, input.TopicID, input.ParentCommentID, input.TriggerCommentID)
	if err != nil {
		return fmt.Errorf("load reply context: %w", err)
	}
	parameters, systemOverride := g.resolveParameters(ctx)
	system, user := buildReplyPrompt(thread, systemOverride)
	completion, err := g.completer.Complete(ctx, CompletionInput{
		Purpose:       ReplyPurpose,
		CostClass:     parameters.CostClass,
		PromptVersion: parameters.PromptVersion,
		System:        system,
		User:          user,
		MaxTokens:     ReplyMaxTokens,
		SubjectUserID: input.TriggerUserID,
		Images:        g.resolveImageCandidates(ctx, input),
		ActorUserID:   input.BotUserID,
		Tools:         append([]string(nil), parameters.ToolsAllowed...),
		// 工具以提问者视角取数：可见性判定用触发者身份，位置信息用于限定
		// 「当前这栋楼」。
		ToolContext: supportai.ToolContext{
			ViewerUserID: input.TriggerUserID,
			TopicID:      input.TopicID,
			CommentID:    input.TriggerCommentID,
		},
	})
	if err != nil {
		return fmt.Errorf("complete reply: %w", err)
	}
	content := strings.TrimSpace(completion.Text)
	if content == "" {
		// 空回复不发布：一条空评论比没有回复更糟。
		return ErrEmptyReply
	}
	if _, err := g.poster.PostBotComment(ctx, input.BotUserID, BotCommentInput{
		TopicID:  input.TopicID,
		ParentID: input.ParentCommentID,
		Content:  content,
	}); err != nil {
		return fmt.Errorf("post bot comment: %w", err)
	}
	return nil
}
