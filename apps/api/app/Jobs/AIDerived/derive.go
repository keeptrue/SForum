// Package aiderivedjobs 承载 AI 派生内容（话题蒸馏、长楼导览）的持久化任务。
//
// 它与 aireplyjobs 的结构相同（args + EnqueueOptions + worker），但触发面完全不同：
// 那边由事件驱动（@机器人、自主判定），这边**只有运营者手动触发**。因此这里没有
// 订阅器、没有去重窗口、也没有「产物再触发 AI」的路径——任务只做一次生成，产出
// 一份草稿就结束。
package aiderivedjobs

import (
	"context"
	"errors"

	"github.com/riverqueue/river"

	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// deriveRequest 是一次派生的内部载体，**不是** River 的 JobArgs。
//
// 它刻意不实现 river.JobArgs（没有 Kind/EnqueueOptions）：River 用 args 类型的
// 零值决定 kind，一个「Kind 取决于字段值」的类型会让同一份 payload 在不同注册
// 路径下绑定到不同 kind——真实事故形态是入队成功、worker 反序列化后 Purpose 为空、
// 产物被判定为非法用途而 cancel，而单测完全看不见。
//
// 对外的 JobArgs 只有 DigestArgs / TourArgs 两个，各自一个固定 kind。本类型只在
// 包内承载「两个 kind 共用的执行逻辑」。
//
// 它只带定位信息，不带正文：正文在任务执行时读取，排队期间的内容编辑才能被正确
// 反映（与 GenerateReplyArgs 同一条纪律）。RequestedBy 是发起这次派生的运营者，
// 写进产物的 created_by_user_id——它是审计归属，不是授权凭据。
type deriveRequest struct {
	// Purpose 是 forum.digest 或 forum.tour。
	Purpose string `json:"purpose"`
	TopicID int64  `json:"topicId"`
	// RequestedBy 是发起者，0 表示系统/CLI 未指定具体账号。
	RequestedBy int64 `json:"requestedBy"`
	// RequestID 是手动触发时由调用方生成的一次性标识，参与唯一键：同一次触发
	// 重复入队（CLI 重试、双击按钮）不会产生两份草稿，而运营者隔一会儿再点一次
	// 会得到新的一份。
	//
	// 它刻意不是 topicId+purpose 的组合去重：那会让运营者「重新生成一份」永远
	// 失败——而在模型输出不理想时重新生成正是这类功能最常用的动作。
	RequestID string `json:"requestId"`
}

// runDerive 承载两个 kind 共用的真实逻辑，让测试不必构造 river.Job。
//
// 三类确定性失败以 river.JobCancel 收尾：重试它们只会得到同样的结果并把账单
// 乘以 MaxAttempts，而任务列表里仍留下可查的原因（与 GenerateReplyWorker 处理
// ErrEmptyReply 同一条纪律）。
func runDerive(ctx context.Context, generator *aiderived.Generator, args deriveRequest) error {
	if generator == nil {
		return aiderived.ErrGeneratorUnavailable
	}
	_, err := generator.Generate(ctx, aiderived.GenerateInput{
		Purpose:     args.Purpose,
		TopicID:     args.TopicID,
		RequestedBy: args.RequestedBy,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, aiderived.ErrEmptyArtifact),
		errors.Is(err, aiderived.ErrTopicUnavailable),
		errors.Is(err, aiderived.ErrArtifactInvalid):
		// 话题在排队期间被删除/转为不可见也走这条：重试不会让它重新出现。
		return river.JobCancel(err)
	default:
		return err
	}
}

// KindDigest / KindTour 是两个稳定的 job kind。
const (
	KindDigest = "ai.forum_digest"
	KindTour   = "ai.forum_tour"
)

// DigestArgs / TourArgs 是两个**独立的** JobArgs 类型。
//
// 为什么不能只用一个带条件 Kind() 的结构：River 在注册 worker 时取 args 类型的
// **零值**来决定 kind（`AddWorkerSafely[T]` 里 `var jobArgs T`）。一个零值
// Purpose 为空的结构只能注册出 `ai.forum_digest`，`ai.forum_tour` 于是永远没有
// worker——实际表现是入队成功、随后 `river_job.state=discarded`，错误信息
// 「job kind is not registered in the client's Workers bundle: ai.forum_tour」。
// 一个 args 类型对应一个 kind 是 River 的硬约束，不是风格选择。
//
// 两个类型共享同一组字段（而不是嵌入一个私有结构）：River 会把 args 序列化进
// job 行，字段形状就是数据库里的事实，显式重复比隐式提升更容易核对。
type DigestArgs struct {
	TopicID     int64  `json:"topicId"`
	RequestedBy int64  `json:"requestedBy"`
	RequestID   string `json:"requestId"`
}

func (DigestArgs) Kind() string { return KindDigest }

func (DigestArgs) EnqueueOptions() supportjobs.EnqueueOptions { return deriveEnqueueOptions() }

func (a DigestArgs) deriveRequest() deriveRequest {
	return deriveRequest{Purpose: aiderived.PurposeDigest, TopicID: a.TopicID, RequestedBy: a.RequestedBy, RequestID: a.RequestID}
}

// TourArgs 见 DigestArgs 的说明。
type TourArgs struct {
	TopicID     int64  `json:"topicId"`
	RequestedBy int64  `json:"requestedBy"`
	RequestID   string `json:"requestId"`
}

func (TourArgs) Kind() string { return KindTour }

func (TourArgs) EnqueueOptions() supportjobs.EnqueueOptions { return deriveEnqueueOptions() }

func (a TourArgs) deriveRequest() deriveRequest {
	return deriveRequest{Purpose: aiderived.PurposeTour, TopicID: a.TopicID, RequestedBy: a.RequestedBy, RequestID: a.RequestID}
}

// DigestWorker 处理 ai.forum_digest。
type DigestWorker struct {
	river.WorkerDefaults[DigestArgs]
	generator *aiderived.Generator
}

func NewDigestWorker(generator *aiderived.Generator) *DigestWorker {
	return &DigestWorker{generator: generator}
}

func (w *DigestWorker) Work(ctx context.Context, job *river.Job[DigestArgs]) error {
	if w == nil || job == nil {
		return aiderived.ErrGeneratorUnavailable
	}
	return runDerive(ctx, w.generator, job.Args.deriveRequest())
}

// TourWorker 处理 ai.forum_tour。分开注册而不是复用同一个 worker：River 的运维
// 视图按 kind 聚合失败率，「导览一直超时」不该看起来像「蒸馏坏了」。
type TourWorker struct {
	river.WorkerDefaults[TourArgs]
	generator *aiderived.Generator
}

func NewTourWorker(generator *aiderived.Generator) *TourWorker {
	return &TourWorker{generator: generator}
}

func (w *TourWorker) Work(ctx context.Context, job *river.Job[TourArgs]) error {
	if w == nil || job == nil {
		return aiderived.ErrGeneratorUnavailable
	}
	return runDerive(ctx, w.generator, job.Args.deriveRequest())
}

// deriveEnqueueOptions 是两个 kind 共用的入队选项，保证「重试与去重策略只有一处」。
func deriveEnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueAI,
		MaxAttempts: 3,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

// AddWorkers 把两个 kind 的 worker 一起登记进 River 的 workers bundle。
//
// 它是「kind 覆盖」的唯一实现点：bootstrap 与测试都调它，避免出现「bootstrap 只
// 注册了一半、测试只测了另一半」这种谁都发现不了的分叉。generator 允许为 nil：
// 登记与装配无关，运行期缺依赖会由 worker 返回 ErrGeneratorUnavailable。
func AddWorkers(workers *river.Workers, generator *aiderived.Generator) error {
	if workers == nil {
		return aiderived.ErrGeneratorUnavailable
	}
	if err := river.AddWorkerSafely[DigestArgs](workers, NewDigestWorker(generator)); err != nil {
		return err
	}
	return river.AddWorkerSafely[TourArgs](workers, NewTourWorker(generator))
}
