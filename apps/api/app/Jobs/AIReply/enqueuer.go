package aireplyjobs

import (
	"context"
	"time"

	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
)

// Enqueuer 实现 aireply.Enqueuer：把触发判定产生的任务放进持久队列。
// 它放在任务包里而不是领域包里，是为了让依赖保持单向——领域包不认识队列。
type Enqueuer struct {
	dispatcher *supportjobs.Dispatcher
}

func NewEnqueuer(dispatcher *supportjobs.Dispatcher) *Enqueuer {
	return &Enqueuer{dispatcher: dispatcher}
}

func (e *Enqueuer) EnqueueReply(ctx context.Context, input aireply.ReplyJobInput) error {
	if e == nil || e.dispatcher == nil {
		return aireply.ErrGeneratorUnavailable
	}
	args := GenerateReplyArgs{
		TopicID:          input.TopicID,
		ParentCommentID:  input.ParentCommentID,
		TriggerCommentID: input.TriggerCommentID,
		TriggerUserID:    input.TriggerUserID,
		BotUserID:        input.BotUserID,
	}
	_, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions())
	return err
}

// EnqueueTriage 实现 aireply.TriageEnqueuer：把自主参与判定任务放进持久队列。
// 去重窗口在入队时写进 TopicDebounce，同一主题窗口内的重复判定被 River 唯一
// 键挡下（入队冲突对调用方表现为 nil 错误——River 的 insert unique 语义是
// 返回已存在任务），恰好是「这条并入现有判定」想要的含义。
func (e *Enqueuer) EnqueueTriage(ctx context.Context, input aireply.TriageJobInput) error {
	if e == nil || e.dispatcher == nil {
		return aireply.ErrGeneratorUnavailable
	}
	args := TriageReplyArgs{
		TopicID:          input.TopicID,
		TriggerCommentID: input.TriggerCommentID,
		SubjectUserID:    input.SubjectUserID,
		TopicDebounce:    TriageDebounce(input.TopicID, time.Now()),
	}
	_, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions())
	return err
}
