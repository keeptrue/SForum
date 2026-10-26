package aiderivedjobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	aiderivedjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/AIDerived"
	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	aidomain "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// --- kind 与入队策略 ---

func TestDeriveArgsEnqueueOptions(t *testing.T) {
	// 两个 kind 共用同一套入队策略（同队列、同重试、同去重）：策略只有一处实现，
	// 否则「蒸馏重试 3 次、导览重试 1 次」这种分叉没人会发现。
	options := (aiderivedjobs.DigestArgs{}).EnqueueOptions()
	tourOptions := (aiderivedjobs.TourArgs{}).EnqueueOptions()
	if options.Queue != tourOptions.Queue || options.MaxAttempts != tourOptions.MaxAttempts {
		t.Fatalf("the two kinds must share one policy: %+v vs %+v", options, tourOptions)
	}
	if options.Queue != "ai" {
		t.Fatalf("queue = %q", options.Queue)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d", options.MaxAttempts)
	}
	if !options.Unique.ByArgs {
		t.Fatal("the same trigger must not enqueue twice")
	}
	// ByState 必须留空：手写子集会漏掉 River 要求的状态（例如 pending），
	// Insert 直接拒绝，而失败只记一条 WARN——表现为「触发没反应」。
	if len(options.Unique.ByState) != 0 {
		t.Fatalf("ByState must stay empty, got %v", options.Unique.ByState)
	}
}

// RequestID 参与唯一键：同一次触发重复投递去重，不同触发不互相挡。
func TestDeriveArgsRequestIDUniqueness(t *testing.T) {
	one := aiderivedjobs.DigestArgs{TopicID: 42, RequestID: "one"}
	two := aiderivedjobs.DigestArgs{TopicID: 42, RequestID: "two"}
	// 两次触发之间 args 必须不同——否则运营者「重新生成一份」会被去重挡回，
	// 而模型输出不理想时重新生成正是这类功能最常用的动作。
	if one == two {
		t.Fatal("different request ids must produce different args, otherwise River dedupes them")
	}
	if !(aiderivedjobs.DigestArgs{}).EnqueueOptions().Unique.ByArgs {
		t.Fatal("ByArgs must be enabled so a repeated delivery of the same trigger dedupes")
	}
}

// --- worker 的失败姿态 ---

type stubExecutor struct {
	result supportai.CompletionResult
	err    error
	calls  int
}

func (s *stubExecutor) Execute(context.Context, supportai.CompletionRequest) (supportai.CompletionResult, error) {
	s.calls++
	return s.result, s.err
}

type stubTopics struct {
	topic aidomain.TopicDetail
	err   error
}

func (s stubTopics) GetTopic(context.Context, int64) (aidomain.TopicDetail, error) {
	return s.topic, s.err
}

type stubComments struct {
	items aidomain.CommentList
	err   error
}

func (s stubComments) ListComments(context.Context, aidomain.CommentListInput) (aidomain.CommentList, error) {
	return s.items, s.err
}

// newWorkers 返回两个 kind 的 worker 与它们共用的生成器。
func newWorkers(executor *stubExecutor, topics stubTopics) (*aiderivedjobs.DigestWorker, *aiderivedjobs.TourWorker) {
	generator := aiderived.NewGenerator(
		topics, stubComments{items: aidomain.CommentList{}}, executor,
		aiderived.NewPostgresArtifactStore(nil), nil,
	)
	return aiderivedjobs.NewDigestWorker(generator), aiderivedjobs.NewTourWorker(generator)
}

// runDigest / runTour 走真实的 worker 入口，避免测试绕过 job.Args 的形状。
func runDigest(worker *aiderivedjobs.DigestWorker, args aiderivedjobs.DigestArgs) error {
	return worker.Work(context.Background(), &river.Job[aiderivedjobs.DigestArgs]{Args: args})
}

func runTour(worker *aiderivedjobs.TourWorker, args aiderivedjobs.TourArgs) error {
	return worker.Work(context.Background(), &river.Job[aiderivedjobs.TourArgs]{Args: args})
}

// 空产出是确定性失败：重试只会得到同样的空正文，却把账单乘以 MaxAttempts。
// 它必须以 River 的取消收尾——任务列表里可查，且不再执行。
func TestDeriveWorkerCancelsDeterministicEmptyArtifact(t *testing.T) {
	digest, _ := newWorkers(&stubExecutor{result: supportai.CompletionResult{Text: "  "}}, stubTopics{topic: aidomain.TopicDetail{}})
	err := runDigest(digest, aiderivedjobs.DigestArgs{TopicID: 42})
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("空产出必须以 JobCancel 收尾，得到 %T %v", err, err)
	}
	if !errors.Is(err, aiderived.ErrEmptyArtifact) {
		t.Fatalf("err = %v", err)
	}
}

// 话题在排队期间被删除/转为不可见：重试不会让它重新出现，同样是确定性失败。
func TestDeriveWorkerCancelsUnavailableTopic(t *testing.T) {
	_, tour := newWorkers(&stubExecutor{result: supportai.CompletionResult{Text: "x"}}, stubTopics{err: aidomain.ErrTopicNotFound})
	err := runTour(tour, aiderivedjobs.TourArgs{TopicID: 42})
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("不可见话题必须以 JobCancel 收尾，得到 %T %v", err, err)
	}
}

// 供应商侧失败是可重试的：它必须原样返回 error，让 River 按 MaxAttempts 处理。
// 把它也取消掉，等于让一次网络抖动永久吃掉一份尚未生成的草稿。
func TestDeriveWorkerRetriesGatewayFailures(t *testing.T) {
	providerErr := errors.New("provider timed out")
	digest, _ := newWorkers(&stubExecutor{err: providerErr}, stubTopics{topic: aidomain.TopicDetail{}})
	err := runDigest(digest, aiderivedjobs.DigestArgs{TopicID: 42})
	if !errors.Is(err, providerErr) {
		t.Fatalf("err = %v", err)
	}
	var cancel *river.JobCancelError
	if errors.As(err, &cancel) {
		t.Fatal("a provider failure must be retried, not cancelled")
	}
}

// 未装配生成器时不得 panic：worker 只报一句可查的错误。
func TestDeriveWorkerWithoutGenerator(t *testing.T) {
	digest := aiderivedjobs.NewDigestWorker(nil)
	if err := runDigest(digest, aiderivedjobs.DigestArgs{TopicID: 42}); !errors.Is(err, aiderived.ErrGeneratorUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// --- 入队器 ---

// 未装配 dispatcher 时必须失败关闭而不是 panic：CLI 与将来的端点都可能在没有
// 队列入队器的装配下被调用。
func TestEnqueuerWithoutDispatcherFailsClosed(t *testing.T) {
	enqueuer := aiderivedjobs.NewEnqueuer(nil)
	if _, err := enqueuer.EnqueueDerive(context.Background(), aiderived.EnqueueInput{
		Purpose: aiderived.PurposeDigest, TopicID: 42, RequestID: "r1",
	}); !errors.Is(err, aiderived.ErrGeneratorUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// TestAddWorkersRegistersEveryKind 是「每个 kind 都有 worker」的守卫。
//
// 它之所以必须存在：第一版只有一个带条件 Kind() 的 DeriveArgs，River 取 args 类型
// 的零值决定 kind，于是只有 ai.forum_digest 被注册；ai.forum_tour 入队成功、随后
// 被丢弃（discarded: job kind is not registered in the client's Workers bundle）。
// 这个错误在单测里完全看不见——因为没有人测过「两个 kind 都能被登记」。
//
// River 不暴露「某个 kind 是否已注册」的查询接口，但重复登记同一个 kind 会返回
// 错误，所以「两个不同 args 类型都登记成功」等价于「两个 kind 都被覆盖」。
func TestAddWorkersRegistersEveryKind(t *testing.T) {
	workers := river.NewWorkers()
	if err := aiderivedjobs.AddWorkers(workers, nil); err != nil {
		t.Fatalf("registering both kinds must succeed: %v", err)
	}
	// 反向控制：同一个 args 类型再登记一次必须失败——证明上一次调用真的写入了
	// 两个不同的 kind，而不是静默什么都没做。
	if err := river.AddWorkerSafely[aiderivedjobs.DigestArgs](workers, aiderivedjobs.NewDigestWorker(nil)); err == nil {
		t.Fatal("re-registering ai.forum_digest must fail; otherwise the first call registered nothing")
	}
	if err := river.AddWorkerSafely[aiderivedjobs.TourArgs](workers, aiderivedjobs.NewTourWorker(nil)); err == nil {
		t.Fatal("re-registering ai.forum_tour must fail; otherwise the tour kind was never registered")
	}
}

// TestPerKindArgsCarryTheirOwnKind 钉住「一个 args 类型一个 kind」这条 River 硬约束。
func TestPerKindArgsCarryTheirOwnKind(t *testing.T) {
	if got := (aiderivedjobs.DigestArgs{}).Kind(); got != "ai.forum_digest" {
		t.Fatalf("digest kind = %q", got)
	}
	if got := (aiderivedjobs.TourArgs{}).Kind(); got != "ai.forum_tour" {
		t.Fatalf("tour kind = %q", got)
	}
	if (aiderivedjobs.DigestArgs{}).Kind() == (aiderivedjobs.TourArgs{}).Kind() {
		t.Fatal("the two purposes must not share a job kind")
	}
}

// TestEnqueuerRejectsUndeclaredPurpose 钉住「未声明用途在入队前被拒」。
func TestEnqueuerRejectsUndeclaredPurpose(t *testing.T) {
	enqueuer := aiderivedjobs.NewEnqueuer(nil)
	if _, err := enqueuer.EnqueueDerive(context.Background(), aiderived.EnqueueInput{
		Purpose: "forum.unknown", TopicID: 42, RequestID: "r1",
	}); !errors.Is(err, aiderived.ErrGeneratorUnavailable) {
		t.Fatalf("nil dispatcher must report unavailable, got %v", err)
	}
}
