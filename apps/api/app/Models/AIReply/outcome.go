package aireply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// OutcomeRecorder 记录一条回复的最终裁决，并回答「这条触发是不是已经回过」。
//
// 它是生成器唯一需要的写面：一次回复可以包含多次模型调用，也可能一次都没调用
// 就被闸门拒绝——「有没有生成出一条评论」这个事实此前只存在于日志里，控制台
// 因此只能看到一片 succeeded。两个方法必须来自同一个存储（同一条幂等判据），
// 分成两个接口会让「写入的结果」与「判定已回过」用上两套口径。
type OutcomeRecorder interface {
	RecordReplyOutcome(ctx context.Context, outcome supportai.ReplyOutcome) error
	HasPostedReply(ctx context.Context, topicID, triggerCommentID int64) (bool, error)
}

// WithOutcomes 装配回复结果记录与幂等判定。返回同一生成器以便链式装配。
//
// 未装配时行为与引入前完全一致：回复照常，只是「为什么没回」仍然只能去读日志。
func (g *Generator) WithOutcomes(recorder OutcomeRecorder) *Generator {
	if g == nil {
		return g
	}
	g.outcomes = recorder
	return g
}

// alreadyPosted 判定这条触发是否已经产出过评论。读失败一律按「没有」处理：
// 结果表是诊断与幂等增强，不是发布的正确性依赖——一次存储抖动不该让回复消失。
// 代价是这种情况下可能重复回复一次，由 River 的 unique 键作为第一道防线。
func (g *Generator) alreadyPosted(ctx context.Context, input ReplyJobInput) bool {
	if g == nil || g.outcomes == nil {
		return false
	}
	posted, err := g.outcomes.HasPostedReply(ctx, input.TopicID, input.TriggerCommentID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: check existing reply failed; generating anyway",
			"topicId", input.TopicID, "triggerCommentId", input.TriggerCommentID, "err", err)
		return false
	}
	return posted
}

// replyOutcome 把一次生成的结果折叠成一行可读的结论。
//
// stage 说明失败发生在哪一段（"complete" / "publish"）：生成失败与发布失败的
// 运营者动作完全不同（前者查模型与预算，后者查权限与主题状态），因此不能都写成
// 一个笼统的 failed。
func replyOutcome(
	input ReplyJobInput,
	output CompletionOutput,
	commentID int64,
	stage string,
	err error,
) supportai.ReplyOutcome {
	outcome := supportai.ReplyOutcome{
		TopicID:          input.TopicID,
		ParentCommentID:  input.ParentCommentID,
		TriggerCommentID: input.TriggerCommentID,
		BotUserID:        input.BotUserID,
		RunID:            output.RunID,
		CommentID:        commentID,
		ModelCalls:       output.ModelCalls,
		ToolCalls:        output.ToolCalls,
		InputTokens:      output.Usage.InputTokens,
		OutputTokens:     output.Usage.OutputTokens,
	}
	if err == nil && commentID > 0 {
		outcome.Outcome = supportai.ReplyOutcomePosted
		return outcome
	}
	if err == nil {
		// 没有错误也没有评论 id：调用方契约被违反，如实记一条兜底而不是伪造成 posted。
		outcome.Outcome = supportai.ReplyOutcomeFailed
		outcome.Reason = "missing_comment_id"
		return outcome
	}

	code, reason, detail := classifyReplyError(stage, output, err)
	outcome.Outcome = code
	outcome.Reason = reason
	outcome.Detail = detail
	return outcome
}

// classifyReplyError 把错误翻译成稳定结果码 + 原因 + 可读补充事实。
func classifyReplyError(stage string, output CompletionOutput, err error) (code, reason, detail string) {
	var unavailable *supportai.UnavailableError
	switch {
	case errors.As(err, &unavailable) && unavailable.Reason == supportai.ReasonEmptyCompletion:
		// 网关把空补全判成了失败（它不再是 succeeded）。结果码仍然是
		// empty_reply：运营者要看的是「模型没产出」，不是「网关不可用」。
		// 停止原因与思考有无来自网关带回的部分结果。
		return supportai.ReplyOutcomeEmptyReply, supportai.ReasonEmptyCompletion,
			fmt.Sprintf("finish_reason=%s reasoning=%t",
				strings.TrimSpace(output.StopReason), strings.TrimSpace(output.Reasoning) != "")
	case errors.As(err, &unavailable):
		// 其余网关不可用：关闭、闸门、用途未声明、供应商失败。原因码直接透传，
		// 控制台文案与执行记录因此同源。
		return supportai.ReplyOutcomeUnavailable, unavailable.Reason, ""
	case errors.Is(err, ErrEmptyReply):
		return supportai.ReplyOutcomeEmptyReply, supportai.ReasonEmptyCompletion,
			fmt.Sprintf("finish_reason=%s reasoning=%t",
				strings.TrimSpace(output.StopReason), strings.TrimSpace(output.Reasoning) != "")
	case errors.Is(err, supportai.ErrToolLoopStuck):
		return supportai.ReplyOutcomeToolLoopStuck, "tool_loop_stuck",
			fmt.Sprintf("model_calls=%d tool_calls=%d", output.ModelCalls, output.ToolCalls)
	case stage == "publish":
		return supportai.ReplyOutcomePublishFailed, publishReason(err), supportai.TruncateBytes(err.Error(), 160)
	default:
		return supportai.ReplyOutcomeFailed, "generation_failed", supportai.TruncateBytes(err.Error(), 160)
	}
}

// publishReason 把发布错误归到两个可执行的方向：权限/可见性（要改配置）与
// 内容规则（要改内容或审核策略）。其余保持原样，由 detail 说明。
func publishReason(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "permission"), strings.Contains(message, "forbidden"):
		return "publish_forbidden"
	case strings.Contains(message, "closed"), strings.Contains(message, "not found"):
		return "publish_target_unavailable"
	default:
		return "publish_failed"
	}
}

// recordOutcome 写入唯一出口的那一行结果。失败只记 Warn：结果是诊断面，
// 一次存储失败不该把已经发布的评论变成失败的重试任务（River 重跑会重复发帖）。
func (g *Generator) recordOutcome(ctx context.Context, input ReplyJobInput, outcome supportai.ReplyOutcome) {
	if g == nil || g.outcomes == nil {
		return
	}
	if err := g.outcomes.RecordReplyOutcome(ctx, outcome); err != nil {
		slog.WarnContext(ctx, "aireply: record reply outcome failed",
			"topicId", input.TopicID, "triggerCommentId", input.TriggerCommentID,
			"outcome", outcome.Outcome, "err", err)
	}
}
