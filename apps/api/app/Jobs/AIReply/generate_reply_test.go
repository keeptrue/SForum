package aireplyjobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	aireplyjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/AIReply"
	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
)

// stubThreads 提供一次回复所需的最小上下文。
type stubThreads struct{}

func (stubThreads) ReplyContext(context.Context, int64, int64, int64) (aireply.ReplyContext, error) {
	return aireply.ReplyContext{TopicTitle: "如何配置连接池"}, nil
}

// reasoningOnlyCompleter 模拟推理类模型把输出预算全花在思考上：有思考，没有正文。
type reasoningOnlyCompleter struct{}

func (reasoningOnlyCompleter) Complete(context.Context, aireply.CompletionInput) (aireply.CompletionOutput, error) {
	return aireply.CompletionOutput{Reasoning: "先回忆一下默认值……", StopReason: "length"}, nil
}

// failIfPosted 在回复被发布时让测试失败：思考正文出现在这里就是本次要修的缺陷。
type failIfPosted struct{ t *testing.T }

func (p failIfPosted) PostBotComment(context.Context, int64, aireply.BotCommentInput) (int64, error) {
	p.t.Error("思考正文被发布成了评论")
	return 0, nil
}

func TestGenerateReplyKind(t *testing.T) {
	if kind := (aireplyjobs.GenerateReplyArgs{}).Kind(); kind != "ai.reply_generate" {
		t.Fatalf("kind = %q", kind)
	}
}

// Unique.ByState 必须留空。手写一个子集会漏掉 River 要求的状态（例如 pending），
// 而 Insert 会直接拒绝——入队失败只记一条 WARN，对外表现为「AI 完全不回复」，
// 从现象上完全看不出是任务配置问题。
func TestGenerateReplyLeavesUniqueStatesToRiverDefaults(t *testing.T) {
	options := (aireplyjobs.GenerateReplyArgs{}).EnqueueOptions()
	if !options.Unique.ByArgs {
		t.Fatal("the same trigger comment must not enqueue twice")
	}
	if len(options.Unique.ByState) != 0 {
		t.Fatalf("ByState must stay empty so River supplies the full active set, got %v", options.Unique.ByState)
	}
	if options.Queue != "ai" {
		t.Fatalf("queue = %q", options.Queue)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d", options.MaxAttempts)
	}
}

// 空回复是确定性失败：同样的提示词配同样的模型，重试只会得到同样的空回复，却把
// 账单乘以 MaxAttempts。它必须以 River 的取消收尾——任务列表里可查，且不再执行。
func TestGenerateReplyCancelsDeterministicEmptyReply(t *testing.T) {
	generator := aireply.NewGenerator(stubThreads{}, reasoningOnlyCompleter{}, failIfPosted{t: t}, nil)
	worker := aireplyjobs.NewGenerateReplyWorker(generator)

	err := worker.Work(context.Background(), &river.Job[aireplyjobs.GenerateReplyArgs]{
		Args: aireplyjobs.GenerateReplyArgs{TopicID: 9, ParentCommentID: 501, TriggerCommentID: 501, TriggerUserID: 11, BotUserID: 77},
	})
	if !errors.Is(err, aireply.ErrEmptyReply) {
		t.Fatalf("err = %v", err)
	}
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("空回复必须以 JobCancel 收尾，得到 %T", err)
	}
}

// 供应商侧失败仍按重试策略处理：网络抖动与「模型没给答案」是两件事，前者重试有意义。
func TestGenerateReplyRetriesProviderFailures(t *testing.T) {
	generator := aireply.NewGenerator(stubThreads{}, failingCompleter{}, failIfPosted{t: t}, nil)
	worker := aireplyjobs.NewGenerateReplyWorker(generator)

	err := worker.Work(context.Background(), &river.Job[aireplyjobs.GenerateReplyArgs]{})
	var cancel *river.JobCancelError
	if errors.As(err, &cancel) {
		t.Fatal("供应商失败不该被取消，它应当按重试策略处理")
	}
	if !errors.Is(err, errProviderDown) {
		t.Fatalf("err = %v", err)
	}
}

var errProviderDown = errors.New("provider is down")

type failingCompleter struct{}

func (failingCompleter) Complete(context.Context, aireply.CompletionInput) (aireply.CompletionOutput, error) {
	return aireply.CompletionOutput{}, errProviderDown
}
