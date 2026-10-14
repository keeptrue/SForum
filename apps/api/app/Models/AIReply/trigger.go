// Package aireply 是 AI 回复机器人的领域逻辑：判断一条评论是否应该唤起 AI
// 回应，并把生成任务交给后台。它不生成内容，也不直接写库——那些由 job 完成。
//
// 触发来自 comment.created 事件而不是论坛服务内部：论坛的写路径不该知道 AI 的
// 存在，而事件是同一个信息对所有订阅者的公开形态。代价是要自己补齐判定所需的
// 上下文，收益是论坛服务零改动、未来插件也能走同一条路。
package aireply

import (
	"context"
	"log/slog"
	"strings"

	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
)

// BotAccount 描述充当 AI 助手的账号。
type BotAccount struct {
	UserID   int64
	Username string
}

// AccountResolver 解析 AI 助手账号。没有机器人账号时返回 ok=false，回复功能
// 整体停用——一个还没配机器人的站点不该有任何 AI 行为。
type AccountResolver interface {
	ReplyBotAccount(ctx context.Context) (BotAccount, bool, error)
}

// CommentContext 是判定与生成所需的一条评论的快照。
type CommentContext struct {
	CommentID          int64
	TopicID            int64
	AuthorUserID       int64
	ParentID           int64
	ParentAuthorUserID int64
	Status             string
	RawContent         string
}

// TopicContext 是判定一条新主题是否唤起 AI 所需的快照。
type TopicContext struct {
	TopicID      int64
	AuthorUserID int64
	Status       string
	RawContent   string
}

// CommentReader 补齐事件载荷里没有的上下文：正文、父评论作者、账号类型。
type CommentReader interface {
	CommentContext(ctx context.Context, commentID int64) (CommentContext, error)
	TopicContext(ctx context.Context, topicID int64) (TopicContext, error)
	UserKind(ctx context.Context, userID int64) (identity.UserKind, error)
}

// ReplyJobInput 是一次待生成的 AI 回复。
type ReplyJobInput struct {
	TopicID int64
	// ParentCommentID 是 AI 回复要挂靠的评论：始终是触发它的那条评论，
	// 这样回答出现在提问下方，而不是另起一条无关的线。
	ParentCommentID int64
	// TriggerCommentID 是唤起回复的评论，供 job 读取上下文。
	TriggerCommentID int64
	// TriggerUserID 是触发者，用于按用户计量配额。
	TriggerUserID int64
	BotUserID     int64
}

// Enqueuer 把生成任务交给后台队列。
type Enqueuer interface {
	EnqueueReply(ctx context.Context, input ReplyJobInput) error
}

// TriggerModeSource 提供当前生效的回复触发模式。读失败按 mention（默认）处理：
// 配置不可用时回到最保守、成本最低的行为，而不是放开自主参与。
type TriggerModeSource interface {
	ReplyTriggerMode(ctx context.Context) (string, error)
}

// Trigger 把 comment.created 事件转换成一次待生成的回复。
type Trigger struct {
	accounts AccountResolver
	reader   CommentReader
	enqueue  Enqueuer
	// modes 可为 nil：nil 等价于永远返回 mention。旧的装配路径不必立即传入。
	modes TriggerModeSource
}

func NewTrigger(accounts AccountResolver, reader CommentReader, enqueue Enqueuer) *Trigger {
	return &Trigger{accounts: accounts, reader: reader, enqueue: enqueue}
}

// WithTriggerModes 注入触发模式来源。返回同一触发器以便链式装配。
func (t *Trigger) WithTriggerModes(modes TriggerModeSource) *Trigger {
	t.modes = modes
	return t
}

// triggerMode 返回当前生效的触发模式。任何异常都归一到 mention：这是默认行
// 为，也是故障时最安全的行为。
func (t *Trigger) triggerMode(ctx context.Context) string {
	if t == nil || t.modes == nil {
		return supportai.TriggerModeMention
	}
	mode, err := t.modes.ReplyTriggerMode(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load trigger mode failed; falling back to mention", "err", err)
		return supportai.TriggerModeMention
	}
	return supportai.ReplySettings{TriggerMode: mode}.NormalizedTriggerMode()
}

// CommentCreated 处理一条已提交的评论。它只做判定与入队，绝不等待生成，
// 也不返回错误：回复是评论之外的事情，失败只能记日志。
func (t *Trigger) CommentCreated(ctx context.Context, envelope appevents.Envelope) {
	if t == nil || t.accounts == nil || t.reader == nil || t.enqueue == nil {
		slog.WarnContext(ctx, "aireply: trigger is not wired; reply generation is disabled")
		return
	}
	commentID := payloadID(envelope.Payload, "commentId")
	topicID := payloadID(envelope.Payload, "topicId")
	authorUserID := payloadID(envelope.Payload, "authorUserId")
	if commentID <= 0 || topicID <= 0 || authorUserID <= 0 {
		slog.WarnContext(ctx, "aireply: trigger skipped", "reason", "incomplete_payload",
			"commentId", commentID, "topicId", topicID, "authorUserId", authorUserID)
		return
	}
	// 从这一刻起每个出口都留下原因。「AI 为什么不回复」必须能从日志直接回答，
	// 否则无法区分「事件没到」和「判定跳过」——两者在外部完全一样。
	skip := func(reason string, extra ...any) {
		args := append([]any{"reason", reason, "commentId", commentID}, extra...)
		slog.InfoContext(ctx, "aireply: trigger skipped", args...)
	}

	// 防循环的第一道闸：机器人自己的发言永远不触发新的生成。它只需要一次
	// 账号类型查询，比解析正文便宜，所以放在最前面。
	authorKind, err := t.reader.UserKind(ctx, authorUserID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: resolve author kind failed", "userId", authorUserID, "err", err)
		return
	}
	if authorKind.IsBot() {
		skip("author_is_bot", "authorUserId", authorUserID)
		return
	}

	account, ok, err := t.accounts.ReplyBotAccount(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: resolve bot account failed", "err", err)
		return
	}
	if !ok {
		skip("no_bot_account")
		return
	}
	if authorUserID == account.UserID {
		skip("author_is_the_assistant", "botUserId", account.UserID)
		return
	}

	snapshot, err := t.reader.CommentContext(ctx, commentID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load comment context failed", "commentId", commentID, "err", err)
		return
	}
	// 待审或其他非公开状态的评论不触发：等它真的可见了再说。
	if snapshot.Status != "" && snapshot.Status != "active" {
		skip("comment_not_public", "status", snapshot.Status)
		return
	}

	mentioned := mentionsBot(mentionedUsernames(snapshot.RawContent), account.Username)
	repliedToBot := false
	if !mentioned {
		repliedToBot = t.repliedToBot(ctx, snapshot.ParentAuthorUserID, account)
	}

	// off 模式在 mention 判定之后才短路：被 @ 也不回。放在防循环之后、入队
	// 之前，日志仍然能回答「为什么不回」。
	if mode := t.triggerMode(ctx); mode == supportai.TriggerModeOff {
		skip("trigger_mode_off", "wasDirectedAtBot", mentioned || repliedToBot)
		return
	}

	if mentioned || repliedToBot {
		t.enqueueReply(ctx, ReplyJobInput{
			TopicID:          topicID,
			ParentCommentID:  commentID,
			TriggerCommentID: commentID,
			TriggerUserID:    authorUserID,
			BotUserID:        account.UserID,
		}, map[string]any{"commentId": commentID}, mentioned, repliedToBot)
		return
	}

	// 自主模式：未被 @ / 不是回复机器人时，交给 triage 判定值不值得回。判定
	// 失败按 SKIP 落日志，绝不静默吞掉——「AI 为什么没回」必须可从日志回答。
	if t.triggerMode(ctx) == supportai.TriggerModeAutonomous {
		t.enqueueTriage(ctx, TriageJobInput{
			TopicID:          topicID,
			TriggerCommentID: commentID,
			SubjectUserID:    authorUserID,
		}, map[string]any{"commentId": commentID})
		return
	}

	// 最重要的一条：它同时给出「期望被 @ 的用户名」和「被回复者是谁」，
	// 运营者据此立刻能看出是没 @ 对，还是回复的对象不是机器人。
	skip("not_directed_at_bot",
		"assistantUsername", account.Username,
		"parentAuthorUserId", snapshot.ParentAuthorUserID,
		"mentions", mentionedUsernames(snapshot.RawContent))
}

// TopicCreated 处理一条已提交的主题。发帖 @ 机器人与评论 @ 机器人是同一个
// 用户预期：主题正文里的提及同样应该唤起回复。与评论路径的差别只有两点：
// 没有父评论可查（AI 回复直接挂在主题下，ParentCommentID=0），也没有
// 「回复机器人」这条触发途径。
func (t *Trigger) TopicCreated(ctx context.Context, envelope appevents.Envelope) {
	if t == nil || t.accounts == nil || t.reader == nil || t.enqueue == nil {
		slog.WarnContext(ctx, "aireply: trigger is not wired; reply generation is disabled")
		return
	}
	topicID := payloadID(envelope.Payload, "topicId")
	authorUserID := payloadID(envelope.Payload, "authorUserId")
	if topicID <= 0 || authorUserID <= 0 {
		slog.WarnContext(ctx, "aireply: trigger skipped", "reason", "incomplete_payload",
			"topicId", topicID, "authorUserId", authorUserID)
		return
	}
	skip := func(reason string, extra ...any) {
		args := append([]any{"reason", reason, "topicId", topicID}, extra...)
		slog.InfoContext(ctx, "aireply: trigger skipped", args...)
	}

	authorKind, err := t.reader.UserKind(ctx, authorUserID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: resolve author kind failed", "userId", authorUserID, "err", err)
		return
	}
	if authorKind.IsBot() {
		skip("author_is_bot", "authorUserId", authorUserID)
		return
	}

	account, ok, err := t.accounts.ReplyBotAccount(ctx)
	if err != nil {
		slog.WarnContext(ctx, "aireply: resolve bot account failed", "err", err)
		return
	}
	if !ok {
		skip("no_bot_account")
		return
	}
	if authorUserID == account.UserID {
		skip("author_is_the_assistant", "botUserId", account.UserID)
		return
	}

	snapshot, err := t.reader.TopicContext(ctx, topicID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: load topic context failed", "topicId", topicID, "err", err)
		return
	}
	// 待审主题不触发：触发评论的审核通过事件由评论路径自行覆盖，主题这里
	// 只接受立即可见的内容。
	if snapshot.Status != "" && snapshot.Status != "active" {
		skip("topic_not_public", "status", snapshot.Status)
		return
	}

	mentioned := mentionsBot(mentionedUsernames(snapshot.RawContent), account.Username)

	// off 模式：@ 了也不回。
	if t.triggerMode(ctx) == supportai.TriggerModeOff {
		skip("trigger_mode_off", "wasMentioned", mentioned)
		return
	}

	if mentioned {
		t.enqueueReply(ctx, ReplyJobInput{
			TopicID:       topicID,
			TriggerUserID: authorUserID,
			BotUserID:     account.UserID,
		}, map[string]any{"topicId": topicID}, true, false)
		return
	}

	// 自主模式：正文没有 @ 机器人时交给 triage 判定。
	if t.triggerMode(ctx) == supportai.TriggerModeAutonomous {
		t.enqueueTriage(ctx, TriageJobInput{
			TopicID:       topicID,
			SubjectUserID: authorUserID,
		}, map[string]any{"topicId": topicID})
		return
	}

	skip("not_directed_at_bot",
		"assistantUsername", account.Username,
		"mentions", mentionedUsernames(snapshot.RawContent))
}

// TriageEnqueuer 把判定任务交给后台队列。
type TriageEnqueuer interface {
	EnqueueTriage(ctx context.Context, input TriageJobInput) error
}

// TriageJobInput 是一次自主参与判定任务。
type TriageJobInput struct {
	TopicID int64
	// TriggerCommentID 是触发判定的评论；主题触发时为 0，判定以主楼正文为对象。
	TriggerCommentID int64
	// SubjectUserID 是触发者，判定调用按他的每日配额计量。
	SubjectUserID int64
}

// enqueueTriage 入队一次判定。triage 入队器在装配层包装，避免 Trigger 直接依
// 赖 River 细节；没有装配判定入队器（旧部署）时按「未启用自主模式」处理。
func (t *Trigger) enqueueTriage(ctx context.Context, input TriageJobInput, logFields map[string]any) {
	enqueuer, ok := t.enqueue.(TriageEnqueuer)
	if !ok {
		args := append([]any{"reason", "triage_enqueuer_missing"}, logFieldsToArgs(logFields)...)
		slog.InfoContext(ctx, "aireply: trigger skipped", args...)
		return
	}
	if err := enqueuer.EnqueueTriage(ctx, input); err != nil {
		args := append([]any{"err", err}, logFieldsToArgs(logFields)...)
		slog.WarnContext(ctx, "aireply: enqueue triage failed", args...)
		return
	}
	args := append([]any{"topicId", input.TopicID, "queued", "triage"}, logFieldsToArgs(logFields)...)
	slog.InfoContext(ctx, "aireply: triage enqueued", args...)
}

func logFieldsToArgs(fields map[string]any) []any {
	args := make([]any, 0, len(fields)*2)
	for key, value := range fields {
		args = append(args, key, value)
	}
	return args
}

func (t *Trigger) enqueueReply(ctx context.Context, input ReplyJobInput, logFields map[string]any, viaMention, viaReply bool) {
	if err := t.enqueue.EnqueueReply(ctx, input); err != nil {
		args := []any{"err", err}
		for key, value := range logFields {
			args = append(args, key, value)
		}
		slog.WarnContext(ctx, "aireply: enqueue reply failed", args...)
		return
	}
	args := []any{"botUserId", input.BotUserID, "topicId", input.TopicID,
		"viaMention", viaMention, "viaReply", viaReply}
	for key, value := range logFields {
		args = append(args, key, value)
	}
	slog.InfoContext(ctx, "aireply: reply enqueued", args...)
}

func (t *Trigger) repliedToBot(ctx context.Context, parentAuthorUserID int64, account BotAccount) bool {
	if parentAuthorUserID <= 0 {
		return false
	}
	if parentAuthorUserID == account.UserID {
		return true
	}
	// 站点可能有多个机器人账号；回复任何一个都算在跟机器人说话。
	kind, err := t.reader.UserKind(ctx, parentAuthorUserID)
	if err != nil {
		slog.WarnContext(ctx, "aireply: resolve parent author kind failed",
			"userId", parentAuthorUserID, "err", err)
		return false
	}
	return kind.IsBot()
}

func mentionsBot(names []string, botUsername string) bool {
	botUsername = strings.TrimSpace(botUsername)
	if botUsername == "" {
		return false
	}
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), botUsername) {
			return true
		}
	}
	return false
}

// payloadID 兼容事件载荷里的整型与 JSON 数字。
func payloadID(payload map[string]any, key string) int64 {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
