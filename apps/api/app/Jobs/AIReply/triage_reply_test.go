package aireplyjobs

import (
	"context"
	"testing"
	"time"

	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
)

func TestTriageDebounceBucketsByWindow(t *testing.T) {
	base := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	// 同一窗口内：桶号一致 → River 唯一键拦截重复判定。
	if TriageDebounce(9, base) != TriageDebounce(9, base.Add(30*time.Second)) {
		t.Fatal("two calls inside one window must share the bucket")
	}
	// 相邻窗口：桶号不同 → 新判定正常入队。
	if TriageDebounce(9, base) == TriageDebounce(9, base.Add(TriageDebounceWindow)) {
		t.Fatal("the next window must produce a new bucket")
	}
	// 不同主题绝不共享桶号。
	if TriageDebounce(9, base) == TriageDebounce(10, base) {
		t.Fatal("different topics must never share a bucket")
	}
}

type fakeThreads struct{ thread aireply.ReplyContext }

func (f fakeThreads) ReplyContext(context.Context, int64, int64, int64) (aireply.ReplyContext, error) {
	return f.thread, nil
}

type fakeDecider struct{ verdict aireply.TriageVerdict }

func (f fakeDecider) Decide(context.Context, aireply.TriageInput) (aireply.TriageVerdict, string, error) {
	return f.verdict, "", nil
}

func TestTriageWorkerReplyEnqueuesGeneration(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	worker := NewTriageReplyWorker(fakeDecider{verdict: aireply.TriageReply}, fakeThreads{}).WithGenerate(enqueuer.EnqueueReply)
	if err := worker.workArgs(context.Background(), TriageReplyArgs{TopicID: 9, TriggerCommentID: 501, SubjectUserID: 11}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if len(enqueuer.replies) != 1 {
		t.Fatalf("expected one generate enqueue, got %+v", enqueuer.replies)
	}
	job := enqueuer.replies[0]
	if job.TopicID != 9 || job.ParentCommentID != 501 || job.TriggerUserID != 11 {
		t.Fatalf("generate job = %+v", job)
	}
}

func TestTriageWorkerSkipEnqueuesNothing(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	worker := NewTriageReplyWorker(fakeDecider{verdict: aireply.TriageSkip}, fakeThreads{}).WithGenerate(enqueuer.EnqueueReply)
	if err := worker.workArgs(context.Background(), TriageReplyArgs{TopicID: 9, TriggerCommentID: 501, SubjectUserID: 11}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if len(enqueuer.replies) != 0 {
		t.Fatalf("skip must not enqueue generation: %+v", enqueuer.replies)
	}
}

func TestTriageWorkerTopicTriggerHasNoParentComment(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	args := TriageReplyArgs{TopicID: 9, TriggerCommentID: 0, SubjectUserID: 11}
	worker := NewTriageReplyWorker(fakeDecider{verdict: aireply.TriageReply}, fakeThreads{}).WithGenerate(enqueuer.EnqueueReply)
	// topic 触发时 TriggerCommentID=0：回复必须直接挂在主题下（ParentCommentID=0），
	// 而不是把 0 误当成评论 ID。
	if err := worker.workArgs(context.Background(), args); err != nil {
		t.Fatalf("work: %v", err)
	}
	if job := enqueuer.replies[0]; job.ParentCommentID != 0 {
		t.Fatalf("topic-triggered reply must hang under the topic: %+v", job)
	}
}

type recordingEnqueuer struct{ replies []aireply.ReplyJobInput }

func (r *recordingEnqueuer) EnqueueReply(_ context.Context, input aireply.ReplyJobInput) error {
	r.replies = append(r.replies, input)
	return nil
}
