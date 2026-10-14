package aireplyjobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
)

// TriageReplyArgs 是一次自主参与判定。与 GenerateReplyArgs 同样只带定位信息：
// 判定执行时读取上下文，排队期间的编辑能被正确反映。
//
// TopicDebounce 参与 River 唯一键：同一主题在去重窗口（见 TriageDebounceWindow）
// 内只保留一个判定任务，窗口序号写入参数——窗口变了任务参数就变，旧任务照常
// 执行而新任务入队，不丢判定也不会堆积。触发评论只影响判定上下文，不影响唯一
// 性：一条热主题上连发十条，一条判定就够（判定读的是最近讨论窗口）。
type TriageReplyArgs struct {
	TopicID          int64 `json:"topicId"`
	TriggerCommentID int64 `json:"triggerCommentId"`
	SubjectUserID    int64 `json:"subjectUserId"`
	TopicDebounce    int64 `json:"topicDebounce" river:"unique"`
}

func (TriageReplyArgs) Kind() string {
	return "ai.reply_triage"
}

func (a TriageReplyArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue: supportjobs.QueueAI,
		// 判定失败几乎总是供应商侧故障；重试成本极低（economy、8 tokens），
		// 给两次重试让瞬时抖动不至于漏掉一条值得回的发言。
		MaxAttempts: 3,
		// 同一条触发评论 / 主题只排一个判定。
		Unique: river.UniqueOpts{ByArgs: true},
	}
}

// TriageReplyWorker 执行一次自主参与判定：判 REPLY 就地入队生成任务，判 SKIP
// 记日志结束。判定在 worker 里做而不是触发路径里做，是因为它是一次 LLM 调用
// ——慢、可失败、需要重试语义，这些 River 都有。
type TriageReplyWorker struct {
	river.WorkerDefaults[TriageReplyArgs]
	decider TriageDecider
	threads aireply.ThreadReader
	// generate 由 embed 路径注入：判 REPLY 后经它入队正式生成。
	generate func(ctx context.Context, input aireply.ReplyJobInput) error
}

// TriageDecider 是判定器的最小接口；具体实现见 aireply.TriageDecider。
type TriageDecider interface {
	Decide(ctx context.Context, input aireply.TriageInput) (aireply.TriageVerdict, string, error)
}

func NewTriageReplyWorker(decider TriageDecider, threads aireply.ThreadReader) *TriageReplyWorker {
	return &TriageReplyWorker{decider: decider, threads: threads}
}

// WithGenerate 注入生成入队函数。未注入时判定仍然运行并记录结论，但不生成
// ——装配不完整的部署行为可预期，而不是 panic。
func (w *TriageReplyWorker) WithGenerate(generate func(ctx context.Context, input aireply.ReplyJobInput) error) *TriageReplyWorker {
	w.generate = generate
	return w
}

func (w *TriageReplyWorker) Work(ctx context.Context, job *river.Job[TriageReplyArgs]) error {
	if job == nil {
		return aireply.ErrGeneratorUnavailable
	}
	return w.workArgs(ctx, job.Args)
}

// workArgs 承载真实逻辑，让测试可以不构造 river.Job 就验证行为。
func (w *TriageReplyWorker) workArgs(ctx context.Context, args TriageReplyArgs) error {
	if w == nil || w.decider == nil || w.threads == nil {
		return aireply.ErrGeneratorUnavailable
	}
	thread, err := w.threads.ReplyContext(ctx, args.TopicID, 0, args.TriggerCommentID)
	if err != nil {
		// 上下文不可读（主题被删/转私密）：任务没有可判定的对象，按已完成
		// 处理而不是重试——重试只会一次次撞上同一个错误。
		slog.InfoContext(ctx, "aireply: triage context unavailable; dropping job",
			"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID, "err", err)
		return nil
	}
	verdict, reason, err := w.decider.Decide(ctx, aireply.TriageInput{
		Thread:        thread,
		SubjectUserID: args.SubjectUserID,
	})
	if err != nil {
		// Decide 的错误语义是「这次没判成」：返回错误让 River 按重试策略处理。
		slog.WarnContext(ctx, "aireply: triage failed",
			"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID, "err", err)
		return err
	}
	if verdict != aireply.TriageReply {
		slog.InfoContext(ctx, "aireply: triage decided skip",
			"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID, "reason", reason)
		return nil
	}
	if w.generate == nil {
		slog.WarnContext(ctx, "aireply: triage decided reply but generate is not wired; dropping",
			"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID)
		return nil
	}
	input := aireply.ReplyJobInput{
		TopicID:          args.TopicID,
		TriggerCommentID: args.TriggerCommentID,
		TriggerUserID:    args.SubjectUserID,
	}
	if args.TriggerCommentID > 0 {
		// 评论触发：AI 回复挂在这条评论下。
		input.ParentCommentID = args.TriggerCommentID
	}
	if err := w.generate(ctx, input); err != nil {
		slog.WarnContext(ctx, "aireply: triage enqueue generate failed",
			"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID, "err", err)
		return err
	}
	slog.InfoContext(ctx, "aireply: triage decided reply; generation enqueued",
		"topicId", args.TopicID, "triggerCommentId", args.TriggerCommentID)
	return nil
}

// TriageDebounceWindow 是同一主题两条判定之间的最小间隔。自主模式下一条热
// 主题上的连发会一条条触发判定；每条都判是对配额的浪费——窗口内的后续发言
// 因唯一键冲突入队失败而合并，生效中的判定会读到它们的最新内容。
const TriageDebounceWindow = time.Minute

// TriageDebounce 计算主题在时刻 at 所处的时间桶序号。入队侧把它写进参数，
// 使 River 的唯一键成为「主题 × 窗口」：同窗口内后来的判定入队即被去重。
func TriageDebounce(topicID int64, at time.Time) int64 {
	return topicID*10_000_000 + at.Unix()/int64(TriageDebounceWindow.Seconds())
}
