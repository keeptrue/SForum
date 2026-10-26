// Package aireplyjobs 承载 AI 回复的持久化任务定义与执行器。
package aireplyjobs

import (
	"context"
	"errors"

	"github.com/riverqueue/river"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// GenerateReplyArgs 是一次待生成的 AI 回复。它只带定位信息，不携带正文本体：
// 正文在任务执行时读取，这样排队期间的内容编辑能被正确反映。
type GenerateReplyArgs struct {
	TopicID          int64 `json:"topicId"`
	ParentCommentID  int64 `json:"parentCommentId"`
	TriggerCommentID int64 `json:"triggerCommentId"`
	TriggerUserID    int64 `json:"triggerUserId"`
	BotUserID        int64 `json:"botUserId"`
}

func (GenerateReplyArgs) Kind() string {
	return "ai.reply_generate"
}

func (a GenerateReplyArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue: supportjobs.QueueAI,
		// 生成失败通常来自供应商侧：重试两三次足够，再多只是重复计费。
		MaxAttempts: 3,
		// 同一条触发评论只应产生一次生成。
		//
		// ByState 必须留空以使用 River 的默认活跃状态集合。手写一个子集会让
		// Insert 直接报错（例如漏掉 pending），而入队失败只会记一条 WARN——
		// 表现为「AI 完全不回复」，看不出是配置问题。
		Unique: river.UniqueOpts{ByArgs: true},
	}
}

// GenerateReplyWorker 执行一次回复生成。
type GenerateReplyWorker struct {
	river.WorkerDefaults[GenerateReplyArgs]
	generator *aireply.Generator
}

func NewGenerateReplyWorker(generator *aireply.Generator) *GenerateReplyWorker {
	return &GenerateReplyWorker{generator: generator}
}

func (w *GenerateReplyWorker) Work(ctx context.Context, job *river.Job[GenerateReplyArgs]) error {
	if w == nil || w.generator == nil {
		return aireply.ErrGeneratorUnavailable
	}
	err := w.generator.Generate(ctx, aireply.ReplyJobInput{
		TopicID:          job.Args.TopicID,
		ParentCommentID:  job.Args.ParentCommentID,
		TriggerCommentID: job.Args.TriggerCommentID,
		TriggerUserID:    job.Args.TriggerUserID,
		BotUserID:        job.Args.BotUserID,
	})
	if errors.Is(err, aireply.ErrEmptyReply) {
		// 确定性失败：同样的提示词配同样的模型，重试只会得到同样的空回复，却把
		// 账单乘以 MaxAttempts。取消任务并留下错误，让「机器人没回」在任务列表
		// 里可查，而不是变成三次付费的静默失败。
		return river.JobCancel(err)
	}
	return err
}
