// TriageDecider 是自主参与模式的核心：判断一条新发言是否值得 AI 回复。它只
// 输出「回 / 不回」两种结论，绝不产生用户可见内容——「回什么」仍然属于
// Generator。把判定从生成里拆出来，是因为两者的失败语义相反：回复失败应当
// 重试或显式报错，判定失败的正确行为是沉默（SKIP），fail-open 一次网关故障
// 就会放大成一轮意外刷屏。
package aireply

import (
	"context"
	"log/slog"
	"strings"

	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

// TriageVerdict 是一次判定的结论。
type TriageVerdict string

const (
	// TriageReply 表示值得回复：进入正式生成。
	TriageReply TriageVerdict = "REPLY"
	// TriageSkip 表示不值得回复：静默放弃，理由只进日志。
	TriageSkip TriageVerdict = "SKIP"
)

// TriageInput 是一次判定所需的全部信息。
type TriageInput struct {
	// Thread 是触发发言的上下文（主楼 + 最近评论 + 触发发言）。判定复用生成
	// 的上下文读取：两者需要的「这段对话在说什么」是同一份事实。
	Thread ReplyContext
	// SubjectUserID 是触发者，判定调用按他的每日配额计量。
	SubjectUserID int64
}

// TriagePurposeParameters 是判定用途当前生效的参数：成本等级与提示词版本。
// 判定不允许工具，所以没有工具字段——这不是遗漏而是边界。
type TriagePurposeParameters struct {
	CostClass     string
	PromptVersion string
}

// TriageParameterSource 提供判定用途当前生效的参数。
type TriageParameterSource interface {
	TriagePurposeParameters(ctx context.Context) (TriagePurposeParameters, error)
}

// defaultTriageParameters 是没有配置来源时的判定参数：用途声明的成本等级。
func defaultTriageParameters() TriagePurposeParameters {
	declaration, ok := supportai.DefaultPurposeRegistry().Lookup(supportai.CorePurposeForumReplyTriage)
	if !ok {
		return TriagePurposeParameters{CostClass: supportai.CostClassEconomy}
	}
	return TriagePurposeParameters{CostClass: declaration.CostClass, PromptVersion: declaration.PromptVersion}
}

// TriageDecider 把一段对话变成一个「回 / 不回」的结论。
type TriageDecider struct {
	completer  Completer
	parameters TriageParameterSource
}

func NewTriageDecider(completer Completer, parameters TriageParameterSource) *TriageDecider {
	return &TriageDecider{completer: completer, parameters: parameters}
}

// resolveTriageParameters 决定本次判定实际生效的参数。读配置失败回退到用途
// 声明默认值，与 Generator.resolveParameters 同一纪律。
func (d *TriageDecider) resolveTriageParameters(ctx context.Context) TriagePurposeParameters {
	fallback := defaultTriageParameters()
	if d == nil || d.parameters == nil {
		return fallback
	}
	parameters, err := d.parameters.TriagePurposeParameters(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load triage parameters failed; using the declared default", "err", err)
		return fallback
	}
	if !supportai.ValidCostClass(parameters.CostClass) {
		parameters.CostClass = fallback.CostClass
	}
	if strings.TrimSpace(parameters.PromptVersion) == "" {
		parameters.PromptVersion = fallback.PromptVersion
	}
	return parameters
}

// Decide 执行一次判定。任何失败都返回 SKIP + 原因：判定的失败姿态是沉默。
func (d *TriageDecider) Decide(ctx context.Context, input TriageInput) (TriageVerdict, string, error) {
	if d == nil || d.completer == nil {
		return TriageSkip, "triage_unavailable", ErrGeneratorUnavailable
	}
	parameters := d.resolveTriageParameters(ctx)
	system := supportai.DefaultTriageSystemPrompt + "\n\n" + supportai.ReplyPromptSafetyAppendix
	user := buildTriagePrompt(input.Thread)
	// MaxTokens 压到 8：模型只需要输出一个词。更大的预算买不来更好的判定，
	// 只会让提示词注入有地方展开。
	completion, err := d.completer.Complete(ctx, CompletionInput{
		Purpose:       supportai.CorePurposeForumReplyTriage,
		PromptVersion: parameters.PromptVersion,
		System:        system,
		User:          user,
		MaxTokens:     8,
		CostClass:     parameters.CostClass,
		SubjectUserID: input.SubjectUserID,
	})
	if err != nil {
		return TriageSkip, "triage_call_failed", err
	}
	if parseTriageVerdict(completion.Text) == TriageReply {
		return TriageReply, "", nil
	}
	return TriageSkip, "triage_verdict_skip", nil
}

// parseTriageVerdict 解析模型输出。只认 REPLY：包含 REPLY（不区分大小写）才算
// 要回，其余一切输出——SKIP、空串、解释性文字、被注入诱导的长篇大论——都按
// SKIP 处理。判定是高门槛动作，模糊输出一律不回。
func parseTriageVerdict(text string) TriageVerdict {
	if strings.Contains(strings.ToUpper(text), string(TriageReply)) {
		return TriageReply
	}
	return TriageSkip
}

// buildTriagePrompt 组装判定的用户消息。复用回复上下文的字段，但措辞面向
// 「判断」而不是「回答」；触发内容缺失时用占位措辞，判定的输入是完整对话。
func buildTriagePrompt(thread ReplyContext) string {
	var builder strings.Builder
	builder.WriteString("主题：" + strings.TrimSpace(thread.TopicTitle) + "\n")
	if body := strings.TrimSpace(thread.TopicBody); body != "" {
		builder.WriteString("\n主楼正文：\n" + truncateForPrompt(body, TopicBodyRuneLimit) + "\n")
	}
	if len(thread.RecentComments) > 0 {
		builder.WriteString("\n最近的讨论（按时间顺序）：\n")
		for _, comment := range thread.RecentComments {
			label := commentLabel(comment.Author, "某位成员")
			if comment.IsBot {
				label += "（AI 助手自己）"
			}
			builder.WriteString(label + "：" + truncateForPrompt(comment.Content, 600) + "\n")
		}
	}
	// 触发发言优先于父评论：自主模式下每条新主题/评论都可能触发，触发内容
	// 就是「现在要不要回」的对象；被回复场景两者相同，只展示一次。
	trigger := strings.TrimSpace(thread.TriggerContent)
	if trigger == "" {
		trigger = strings.TrimSpace(thread.ParentContent)
	}
	if trigger != "" {
		builder.WriteString("\n最新发言（判断对象）：\n" + truncateForPrompt(trigger, 1200) + "\n")
	}
	builder.WriteString("\nAI 助手应该回复这条最新发言吗？只回答 REPLY 或 SKIP。")
	return builder.String()
}
