package aireply_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// outcomeRecorder 记录生成器写入的结果，并回答「这条触发回过没有」。
type outcomeRecorder struct {
	posted  map[[2]int64]bool
	records []supportai.ReplyOutcome
	err     error
	lookups int
}

func (f *outcomeRecorder) RecordReplyOutcome(_ context.Context, outcome supportai.ReplyOutcome) error {
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, outcome)
	return nil
}

func (f *outcomeRecorder) HasPostedReply(_ context.Context, topicID, triggerCommentID int64) (bool, error) {
	f.lookups++
	if f.posted == nil {
		return false, nil
	}
	return f.posted[[2]int64{topicID, triggerCommentID}], nil
}

// 一次成功的回复必须留下 posted + 评论 id。这条行是「回复有没有出现」的唯一答案：
// 执行记录只说明模型调用成功过。
func TestGenerateRecordsPostedOutcome(t *testing.T) {
	outcomes := &outcomeRecorder{}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()},
		&fakeCompleter{output: aireply.CompletionOutput{
			Text: "  建议从并发数的两倍起步。  ", ModelCalls: 1,
			Usage: supportai.Usage{InputTokens: 120, OutputTokens: 24}, RunID: "run-1",
		}},
		&fakePoster{},
		nil,
	).WithOutcomes(outcomes)

	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(outcomes.records) != 1 {
		t.Fatalf("outcomes = %+v", outcomes.records)
	}
	record := outcomes.records[0]
	if record.Outcome != supportai.ReplyOutcomePosted {
		t.Fatalf("outcome = %q", record.Outcome)
	}
	if record.CommentID != 9001 || record.TopicID != 9 || record.TriggerCommentID != 501 {
		t.Fatalf("record = %+v", record)
	}
	if record.RunID != "run-1" || record.InputTokens != 120 || record.OutputTokens != 24 {
		t.Fatalf("用量与 run 归属必须透出: %+v", record)
	}
}

// 空补全现在由网关判成失败。结果表必须把它记成 empty_reply + empty_completion，
// 而不是一条笼统的 unavailable——运营者要改的是模型与输出预算，不是网络。
func TestGenerateRecordsEmptyCompletionOutcome(t *testing.T) {
	outcomes := &outcomeRecorder{}
	completionErr := &supportai.UnavailableError{
		Posture: supportai.FailurePostureOpen,
		Reason:  supportai.ReasonEmptyCompletion,
		Err:     supportai.ErrEmptyCompletion,
	}
	recipient := &fakeCompleter{
		output: aireply.CompletionOutput{StopReason: supportai.StopReasonLength},
		err:    completionErr,
	}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()}, recipient, &fakePoster{}, nil,
	).WithOutcomes(outcomes)

	err := generator.Generate(context.Background(), jobInput())
	if err == nil {
		t.Fatal("empty completion must fail the generation")
	}
	if len(outcomes.records) != 1 {
		t.Fatalf("outcomes = %+v", outcomes.records)
	}
	record := outcomes.records[0]
	if record.Outcome != supportai.ReplyOutcomeEmptyReply {
		t.Fatalf("outcome = %q", record.Outcome)
	}
	if record.Reason != supportai.ReasonEmptyCompletion {
		t.Fatalf("reason = %q", record.Reason)
	}
	if !strings.Contains(record.Detail, supportai.StopReasonLength) {
		t.Fatalf("detail 必须带停止原因: %q", record.Detail)
	}
}

// 发布失败与生成失败是两个方向：前者查权限与主题状态，后者查模型。
func TestGenerateRecordsPublishFailure(t *testing.T) {
	outcomes := &outcomeRecorder{}
	generator := aireply.NewGenerator(
		&fakeThreads{thread: sampleThread()},
		&fakeCompleter{output: aireply.CompletionOutput{Text: "答案"}},
		&fakePoster{err: errors.New("identity: permission denied")},
		nil,
	).WithOutcomes(outcomes)

	if err := generator.Generate(context.Background(), jobInput()); err == nil {
		t.Fatal("publish failure must surface")
	}
	record := outcomes.records[0]
	if record.Outcome != supportai.ReplyOutcomePublishFailed {
		t.Fatalf("outcome = %q", record.Outcome)
	}
	if record.Reason != "publish_forbidden" {
		t.Fatalf("reason = %q", record.Reason)
	}
}

// 幂等：同一条触发已经发过回复时，不再出站、不再发帖。
func TestGenerateSkipsWhenTriggerAlreadyReplied(t *testing.T) {
	outcomes := &outcomeRecorder{posted: map[[2]int64]bool{{9, 501}: true}}
	completer := &fakeCompleter{output: aireply.CompletionOutput{Text: "第二条回复"}}
	poster := &fakePoster{}
	generator := aireply.NewGenerator(&fakeThreads{thread: sampleThread()}, completer, poster, nil).
		WithOutcomes(outcomes)

	if err := generator.Generate(context.Background(), jobInput()); err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if completer.calls != 0 || len(poster.inputs) != 0 {
		t.Fatalf("重复触发不得再次生成: completer=%d posted=%d", completer.calls, len(poster.inputs))
	}
	if len(outcomes.records) != 0 {
		t.Fatalf("跳过不得写新结果: %+v", outcomes.records)
	}
}
