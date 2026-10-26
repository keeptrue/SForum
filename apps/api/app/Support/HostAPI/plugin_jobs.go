package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/riverqueue/river"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// PluginJobKind 是 River 上的宿主统一 job kind；真实插件 kind 放在 payload 内。
const PluginJobKind = "extension.plugin_job"

// PluginJobArgs 入队载荷：仅允许插件 manifest 声明过的 JobName。
type PluginJobArgs struct {
	EnvelopeVersion      int            `json:"envelopeVersion,omitempty"`
	ExtensionID          string         `json:"extensionId"`
	ExtensionVersion     string         `json:"extensionVersion,omitempty"`
	ArtifactDigest       string         `json:"artifactDigest,omitempty"`
	TrustGrantID         string         `json:"trustGrantId,omitempty"`
	JobName              string         `json:"kind"`
	JobContractVersion   string         `json:"jobContractVersion,omitempty"`
	PayloadSchemaID      string         `json:"payloadSchemaId,omitempty"`
	PayloadSchemaVersion string         `json:"payloadSchemaVersion,omitempty"`
	RetryPolicy          string         `json:"retryPolicy,omitempty"`
	MaxAttempts          int            `json:"maxAttempts,omitempty"`
	RetryDelaySeconds    int            `json:"retryDelaySeconds,omitempty"`
	ConcurrencyLimit     int            `json:"concurrencyLimit,omitempty"`
	Payload              map[string]any `json:"payload,omitempty"`
	EnqueuedAt           time.Time      `json:"enqueuedAt"`
}

// Kind 返回 River 注册用的固定 kind（非插件声明的 JobName）。
func (PluginJobArgs) Kind() string { return PluginJobKind }

func (a PluginJobArgs) Contract() supportjobs.PluginJobContract {
	return supportjobs.PluginJobContract{
		ExtensionID: a.ExtensionID, ExtensionVersion: a.ExtensionVersion, ArtifactDigest: a.ArtifactDigest,
		JobName: a.JobName, JobContract: a.JobContractVersion,
		PayloadSchemaID: a.PayloadSchemaID, PayloadSchemaVersion: a.PayloadSchemaVersion,
		RetryPolicy: a.RetryPolicy, MaxAttempts: a.MaxAttempts,
		RetryDelaySeconds: a.RetryDelaySeconds, ConcurrencyLimit: a.ConcurrencyLimit,
	}
}

func (a PluginJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: a.Contract().Normalized().MaxAttempts}
}

func (a PluginJobArgs) validEnvelope() bool {
	return a.EnvelopeVersion == supportjobs.PluginJobEnvelopeVersion &&
		a.Contract().Valid() && a.TrustGrantID != "" && !a.EnqueuedAt.IsZero()
}

// RiverJobEnqueuer 把插件 job 写入 River 队列。默认是 default 队列；清单里
// 声明 queue: ai 的任务进入 AI 慢速通道。Contracts 可缺省（v1 路径的回退）。
type RiverJobEnqueuer struct {
	Dispatcher *supportjobs.Dispatcher
	Contracts  PluginJobContractSource
}

func (e *RiverJobEnqueuer) EnqueuePluginJob(ctx context.Context, extensionID, kind string, payload map[string]any) error {
	if e == nil || e.Dispatcher == nil {
		return fmt.Errorf("%w: job dispatcher missing", ErrUnavailable)
	}
	args := PluginJobArgs{
		ExtensionID: extensionID,
		JobName:     kind,
		Payload:     payload,
		EnqueuedAt:  time.Now().UTC(),
	}
	// v1 路径没有携带清单契约，因此尽力解析一次声明队列：解析失败时回退到默认
	// 队列（v1 服务在调用前已经校验过 kind 是否声明，这里只影响通道选择）。
	queue := supportjobs.QueueDefault
	if e.Contracts != nil {
		if contract, contractErr := e.Contracts.PluginJobContract(ctx, extensionID, kind); contractErr == nil {
			queue = supportjobs.QueueForPluginDeclaration(contract.Queue)
		}
	}
	_, err := e.Dispatcher.Enqueue(ctx, args, supportjobs.EnqueueOptions{Queue: queue})
	return err
}

// EnqueueVersionedPluginJob persists the exact runtime and manifest contract
// that authorized a protocol-v2 enqueue request.
func (e *RiverJobEnqueuer) EnqueueVersionedPluginJob(
	ctx context.Context,
	contract supportjobs.PluginJobContract,
	trustGrantID string,
	payload map[string]any,
) error {
	if e == nil || e.Dispatcher == nil {
		return fmt.Errorf("%w: job dispatcher missing", ErrUnavailable)
	}
	if !contract.Valid() || trustGrantID == "" {
		return fmt.Errorf("%w: exact plugin job contract is required", ErrInvalidRequest)
	}
	contract = contract.Normalized()
	args := PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		ArtifactDigest: contract.ArtifactDigest, TrustGrantID: trustGrantID,
		JobName: contract.JobName, JobContractVersion: contract.JobContract,
		PayloadSchemaID: contract.PayloadSchemaID, PayloadSchemaVersion: contract.PayloadSchemaVersion,
		RetryPolicy: contract.RetryPolicy, MaxAttempts: contract.MaxAttempts,
		RetryDelaySeconds: contract.RetryDelaySeconds, ConcurrencyLimit: contract.ConcurrencyLimit,
		Payload: payload, EnqueuedAt: time.Now().UTC(),
	}
	_, err := e.Dispatcher.Enqueue(ctx, args, supportjobs.EnqueueOptions{
		// 慢任务（例如要调模型的）声明 queue: ai 后进入站点给 AI 留的低并发通道，
		// 不再和 Core 的日常任务抢 default 队列的 worker。
		Queue: supportjobs.QueueForPluginDeclaration(contract.Queue), MaxAttempts: contract.MaxAttempts,
	})
	return err
}

type VersionedPluginJobEnqueuer interface {
	EnqueueVersionedPluginJob(context.Context, supportjobs.PluginJobContract, string, map[string]any) error
}

type PluginJobContractSource interface {
	PluginJobContract(context.Context, string, string) (supportjobs.PluginJobContract, error)
}

type PluginJobRuntimeContract struct {
	Contract     supportjobs.PluginJobContract
	TrustGrantID string
}

type PluginJobRuntimeResolver interface {
	ResolvePluginJobRuntime(context.Context, string, string) (PluginJobRuntimeContract, error)
}

type PluginJobExecutor interface {
	ExecutePluginJob(context.Context, supportjobs.PluginJobInvocation) error
}

type PluginJobCompatibilityError struct {
	Decision supportjobs.PluginJobDecision
}

func (e *PluginJobCompatibilityError) Error() string {
	if e == nil {
		return "plugin job contract is incompatible"
	}
	return fmt.Sprintf("plugin job %s: %s", e.Decision.Action, e.Decision.Reason)
}

// PluginJobWorker resolves the live exact contract before any plugin code is
// invoked. Legacy or incompatible rows are permanently cancelled.
type PluginJobWorker struct {
	river.WorkerDefaults[PluginJobArgs]
	Resolver    PluginJobRuntimeResolver
	Executor    PluginJobExecutor
	Limiter     *supportjobs.PluginJobConcurrencyLimiter
	limiterOnce sync.Once
}

func (w *PluginJobWorker) NextRetry(job *river.Job[PluginJobArgs]) time.Time {
	if job == nil || job.Args.Contract().Normalized().RetryPolicy != supportjobs.PluginJobRetryBounded {
		return time.Time{}
	}
	delay := time.Duration(job.Args.Contract().Normalized().RetryDelaySeconds) * time.Second
	return time.Now().UTC().Add(delay)
}

func (w *PluginJobWorker) Work(ctx context.Context, job *river.Job[PluginJobArgs]) error {
	if job == nil || !job.Args.validEnvelope() {
		return cancelPluginJob(supportjobs.PluginJobDecision{
			Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonEnvelopeInvalid,
		})
	}
	if job.Args.Payload != nil {
		if _, err := json.Marshal(job.Args.Payload); err != nil {
			return err
		}
	}
	if w == nil || w.Resolver == nil {
		return errors.New("plugin job runtime resolver is not configured")
	}
	target, err := w.Resolver.ResolvePluginJobRuntime(ctx, job.Args.ExtensionID, job.Args.JobName)
	if err != nil {
		if errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
			return cancelPluginJob(supportjobs.PluginJobDecision{
				Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonRuntimeChanged,
			})
		}
		return fmt.Errorf("resolve plugin job runtime: %w", err)
	}
	decision := supportjobs.DecidePluginJobExecution(job.Args.Contract(), target.Contract, nil)
	if decision.Action != supportjobs.PluginJobExecute {
		return cancelPluginJob(decision)
	}
	if target.TrustGrantID == "" || target.TrustGrantID != job.Args.TrustGrantID {
		return cancelPluginJob(supportjobs.PluginJobDecision{
			Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonTrustGrantStale,
		})
	}
	if w.Executor == nil {
		return errors.New("plugin job runtime executor is not configured")
	}
	w.limiterOnce.Do(func() {
		if w.Limiter == nil {
			w.Limiter = &supportjobs.PluginJobConcurrencyLimiter{}
		}
	})
	release, err := w.Limiter.Acquire(ctx, job.Args.Contract())
	if err != nil {
		return err
	}
	defer release()
	invocation := supportjobs.PluginJobInvocation{
		Contract: job.Args.Contract(), TrustGrantID: job.Args.TrustGrantID,
		Payload: job.Args.Payload, EnqueuedAt: job.Args.EnqueuedAt,
	}
	if job.JobRow != nil {
		invocation.JobID = job.ID
		invocation.Attempt = job.Attempt
	}
	err = w.Executor.ExecutePluginJob(ctx, invocation)
	if errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		return cancelPluginJob(supportjobs.PluginJobDecision{
			Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonRuntimeChanged,
		})
	}
	return err
}

func cancelPluginJob(decision supportjobs.PluginJobDecision) error {
	return river.JobCancel(&PluginJobCompatibilityError{Decision: decision})
}
