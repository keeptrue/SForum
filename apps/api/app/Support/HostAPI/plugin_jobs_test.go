package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type pluginJobResolverStub struct {
	target PluginJobRuntimeContract
	err    error
}

func (s pluginJobResolverStub) ResolvePluginJobRuntime(context.Context, string, string) (PluginJobRuntimeContract, error) {
	return s.target, s.err
}

type pluginJobExecutorStub struct {
	calls      int
	invocation supportjobs.PluginJobInvocation
	err        error
}

func (s *pluginJobExecutorStub) ExecutePluginJob(_ context.Context, invocation supportjobs.PluginJobInvocation) error {
	s.calls++
	s.invocation = invocation
	return s.err
}

func TestPluginJobWorkerExecutesExactContract(t *testing.T) {
	args := pluginJobArgsFixture()
	executor := &pluginJobExecutorStub{}
	worker := &PluginJobWorker{
		Resolver: pluginJobResolverStub{target: PluginJobRuntimeContract{Contract: args.Contract(), TrustGrantID: args.TrustGrantID}},
		Executor: executor,
	}
	err := worker.Work(context.Background(), &river.Job[PluginJobArgs]{
		JobRow: &rivertype.JobRow{ID: 41, Attempt: 2}, Args: args,
	})
	if err != nil || executor.calls != 1 {
		t.Fatalf("work = %v, calls = %d", err, executor.calls)
	}
	if executor.invocation.JobID != 41 || executor.invocation.Attempt != 2 || executor.invocation.Contract != args.Contract() ||
		executor.invocation.TrustGrantID != args.TrustGrantID || executor.invocation.Payload["page"] != 1 {
		t.Fatalf("invocation = %#v", executor.invocation)
	}
}

func TestPluginJobWorkerNeverExecutesIncompatibleContract(t *testing.T) {
	args := pluginJobArgsFixture()
	target := args.Contract()
	target.PayloadSchemaVersion = "2"
	executor := &pluginJobExecutorStub{}
	worker := &PluginJobWorker{
		Resolver: pluginJobResolverStub{target: PluginJobRuntimeContract{Contract: target, TrustGrantID: args.TrustGrantID}},
		Executor: executor,
	}
	err := worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args})
	assertPluginJobCancelled(t, err, supportjobs.PluginJobReasonIncompatible)
	if executor.calls != 0 {
		t.Fatalf("incompatible job reached executor %d times", executor.calls)
	}
}

func TestPluginJobWorkerCancelsLegacyAndStaleTrustRows(t *testing.T) {
	legacyJSON := []byte(`{"extensionId":"demo.plugin","kind":"demo.sync","payload":{"page":1}}`)
	var legacy PluginJobArgs
	if err := json.Unmarshal(legacyJSON, &legacy); err != nil {
		t.Fatal(err)
	}
	worker := &PluginJobWorker{}
	assertPluginJobCancelled(t, worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: legacy}), supportjobs.PluginJobReasonEnvelopeInvalid)

	args := pluginJobArgsFixture()
	worker.Resolver = pluginJobResolverStub{target: PluginJobRuntimeContract{Contract: args.Contract(), TrustGrantID: "new-grant"}}
	worker.Executor = &pluginJobExecutorStub{}
	assertPluginJobCancelled(t, worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args}), supportjobs.PluginJobReasonTrustGrantStale)
}

func TestPluginJobWorkerCancelsRuntimeChangedDuringResolutionOrDispatch(t *testing.T) {
	args := pluginJobArgsFixture()
	worker := &PluginJobWorker{Resolver: pluginJobResolverStub{err: supportjobs.ErrPluginJobRuntimeStale}}
	assertPluginJobCancelled(t, worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args}), supportjobs.PluginJobReasonRuntimeChanged)

	executor := &pluginJobExecutorStub{err: supportjobs.ErrPluginJobRuntimeStale}
	worker = &PluginJobWorker{
		Resolver: pluginJobResolverStub{target: PluginJobRuntimeContract{Contract: args.Contract(), TrustGrantID: args.TrustGrantID}},
		Executor: executor,
	}
	assertPluginJobCancelled(t, worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args}), supportjobs.PluginJobReasonRuntimeChanged)
	if executor.calls != 1 {
		t.Fatalf("dispatch race calls = %d", executor.calls)
	}
}

func TestPluginJobWorkerRetriesResolverAndExecutorFailures(t *testing.T) {
	args := pluginJobArgsFixture()
	temporary := errors.New("runtime temporarily unavailable")
	worker := &PluginJobWorker{Resolver: pluginJobResolverStub{err: temporary}}
	if err := worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args}); !errors.Is(err, temporary) {
		t.Fatalf("resolver error = %v", err)
	}
	executor := &pluginJobExecutorStub{err: temporary}
	worker = &PluginJobWorker{
		Resolver: pluginJobResolverStub{target: PluginJobRuntimeContract{Contract: args.Contract(), TrustGrantID: args.TrustGrantID}},
		Executor: executor,
	}
	if err := worker.Work(context.Background(), &river.Job[PluginJobArgs]{Args: args}); !errors.Is(err, temporary) {
		t.Fatalf("executor error = %v", err)
	}
}

func TestPluginJobWorkerUsesDeclaredRetryPolicy(t *testing.T) {
	args := pluginJobArgsFixture()
	args.RetryPolicy = supportjobs.PluginJobRetryBounded
	args.MaxAttempts = 3
	args.RetryDelaySeconds = 12
	args.ConcurrencyLimit = 1
	worker := &PluginJobWorker{}
	before := time.Now().UTC().Add(12 * time.Second)
	next := worker.NextRetry(&river.Job[PluginJobArgs]{Args: args})
	after := time.Now().UTC().Add(12 * time.Second)
	if next.Before(before) || next.After(after) {
		t.Fatalf("next retry = %s, expected within [%s, %s]", next, before, after)
	}
	args.RetryPolicy = supportjobs.PluginJobRetryExponential
	args.RetryDelaySeconds = 0
	if got := worker.NextRetry(&river.Job[PluginJobArgs]{Args: args}); !got.IsZero() {
		t.Fatalf("exponential policy must delegate to River, got %s", got)
	}
}

func assertPluginJobCancelled(t *testing.T, err error, reason string) {
	t.Helper()
	var cancelErr *river.JobCancelError
	if !errors.As(err, &cancelErr) {
		t.Fatalf("expected River cancellation, got %v", err)
	}
	var compatibility *PluginJobCompatibilityError
	if !errors.As(err, &compatibility) || compatibility.Decision.Reason != reason {
		t.Fatalf("compatibility error = %#v", compatibility)
	}
}

func pluginJobArgsFixture() PluginJobArgs {
	return PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1", TrustGrantID: "grant-1",
		JobName: "demo.sync", JobContractVersion: "demo.job.sync@1",
		PayloadSchemaID: "demo.payload", PayloadSchemaVersion: "1",
		Payload: map[string]any{"page": 1}, EnqueuedAt: time.Now().UTC(),
	}
}

type stubPluginJobContractSource struct {
	contract supportjobs.PluginJobContract
	err      error
}

func (s stubPluginJobContractSource) PluginJobContract(context.Context, string, string) (supportjobs.PluginJobContract, error) {
	return s.contract, s.err
}

// 声明 queue: ai 的任务必须真的落在 AI 慢速通道上：这是「插件 AI 工作不再和
// Core 日常任务抢 default worker」的唯一依据。
func TestRiverJobEnqueuerRoutesDeclaredAIQueue(t *testing.T) {
	client := &recordingPluginJobRiverClient{}
	enqueuer := &RiverJobEnqueuer{Dispatcher: supportjobs.NewDispatcher(client)}
	contract := supportjobs.PluginJobContract{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
		JobName: "demo.sync", JobContract: "demo.plugin.job.sync@1",
		PayloadSchemaID: "demo.sync.payload", PayloadSchemaVersion: "1",
		Queue: supportjobs.PluginJobQueueAI,
	}
	if err := enqueuer.EnqueueVersionedPluginJob(context.Background(), contract, "grant-1", map[string]any{"page": 1}); err != nil {
		t.Fatal(err)
	}
	if client.opts == nil || client.opts.Queue != supportjobs.QueueAI {
		t.Fatalf("river insert queue = %#v", client.opts)
	}

	// 未声明队列时保持既有行为（default）。
	client = &recordingPluginJobRiverClient{}
	enqueuer = &RiverJobEnqueuer{Dispatcher: supportjobs.NewDispatcher(client)}
	contract.Queue = ""
	if err := enqueuer.EnqueueVersionedPluginJob(context.Background(), contract, "grant-1", nil); err != nil {
		t.Fatal(err)
	}
	if client.opts == nil || client.opts.Queue != supportjobs.QueueDefault {
		t.Fatalf("undeclared river insert queue = %#v", client.opts)
	}
}

// v1 路径不携带契约，因此尽力解析一次声明；解析失败必须安全回退到 default，
// 而不是让一次 job 入队失败或者落到未知队列。
func TestRiverJobEnqueuerLegacyPathResolvesDeclaredQueue(t *testing.T) {
	client := &recordingPluginJobRiverClient{}
	source := stubPluginJobContractSource{contract: supportjobs.PluginJobContract{Queue: supportjobs.PluginJobQueueAI}}
	enqueuer := &RiverJobEnqueuer{Dispatcher: supportjobs.NewDispatcher(client), Contracts: source}
	if err := enqueuer.EnqueuePluginJob(context.Background(), "demo.plugin", "demo.sync", map[string]any{"page": 1}); err != nil {
		t.Fatal(err)
	}
	if client.opts == nil || client.opts.Queue != supportjobs.QueueAI {
		t.Fatalf("legacy path river insert queue = %#v", client.opts)
	}

	client = &recordingPluginJobRiverClient{}
	enqueuer = &RiverJobEnqueuer{
		Dispatcher: supportjobs.NewDispatcher(client),
		Contracts:  stubPluginJobContractSource{err: errors.New("extension not found")},
	}
	if err := enqueuer.EnqueuePluginJob(context.Background(), "demo.plugin", "demo.sync", nil); err != nil {
		t.Fatal(err)
	}
	if client.opts == nil || client.opts.Queue != supportjobs.QueueDefault {
		t.Fatalf("fallback river insert queue = %#v", client.opts)
	}
}
