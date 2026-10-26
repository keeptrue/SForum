package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// reindexBatchSize 单次 InsertMany 的批量大小。
// River 批量插入性能远优于循环单条；分批避免单次 SQL 过大。
const reindexBatchSize = 1000

// ErrReindexAlreadyRunning 表示已有重建正在进行，拒绝并发触发。
// 保证进度统计精确，并避免同一主题在两个全量 run 中重复调度。
var ErrReindexAlreadyRunning = errors.New("search: reindex already running")

// ReindexManager 协调搜索索引批量重建：
//   - Reindex：扫描全部 topic ID → 创建 run → 分批 EnqueueMany。
//   - ReindexStatus：读当前 run + 实时统计剩余 job 数算进度。
//   - ListReindexRuns：历史 run。
//
// 它不直接执行索引（IndexTopicWorker 负责），只负责"入队 + 进度追踪"。
type ReindexManager struct {
	topics     TopicIDSource
	store      ReindexStore
	dispatcher *supportjobs.Dispatcher
}

func NewReindexManager(topics TopicIDSource, store ReindexStore, dispatcher *supportjobs.Dispatcher) *ReindexManager {
	return &ReindexManager{topics: topics, store: store, dispatcher: dispatcher}
}

// Reindex 触发一次全量重建。若已有 running run 则拒绝。
func (m *ReindexManager) Reindex(ctx context.Context, startedByUserID int64) (ReindexRun, error) {
	// 拒绝并发重建：保证进度精确。
	if current, err := m.store.GetCurrentRun(ctx); err == nil && current.Status == ReindexStatusRunning {
		return current, ErrReindexAlreadyRunning
	}

	ids, err := m.topics.ListAllTopicIDs(ctx)
	if err != nil {
		return ReindexRun{}, fmt.Errorf("list topic ids for reindex: %w", err)
	}

	run, err := m.store.CreateRun(ctx, int64(len(ids)), startedByUserID)
	if err != nil {
		return ReindexRun{}, err
	}

	// 全量重建必须为每个主题创建新任务，不能被历史 completed unique job 阻塞。
	// Index 是幂等 upsert，且上方已拒绝并发重建；日常增量任务仍使用活跃态去重。
	opts := searchjobs.IndexTopicArgs{}.QueueOpts()
	opts.Unique = river.UniqueOpts{}
	for start := 0; start < len(ids); start += reindexBatchSize {
		end := start + reindexBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		argsList := make([]river.JobArgs, 0, end-start)
		for _, id := range ids[start:end] {
			argsList = append(argsList, searchjobs.IndexTopicArgs{TopicID: id})
		}
		if _, err := m.dispatcher.EnqueueMany(ctx, argsList, opts); err != nil {
			// 入队失败：标记 run 失败，已入队的 job 仍会执行（幂等），可重新触发。
			_ = m.store.FinishRun(ctx, run.ID, ReindexStatusFailed, err.Error())
			slog.ErrorContext(ctx, "search: reindex enqueue failed", "runId", run.ID, "batchStart", start, "err", err)
			return run, fmt.Errorf("enqueue reindex batch: %w", err)
		}
	}

	slog.InfoContext(ctx, "search: reindex enqueued", "runId", run.ID, "total", run.Total)
	return run, nil
}

// ReindexStatus 返回当前 run 的实时进度。
// processed/remaining/percent 由查询 river_job 剩余数实时计算。
func (m *ReindexManager) ReindexStatus(ctx context.Context) (ReindexStatusOutput, error) {
	run, err := m.store.GetCurrentRun(ctx)
	if err != nil {
		return ReindexStatusOutput{}, err
	}

	out := ReindexStatusOutput{ReindexRun: run}

	// 已完成的 run 直接算 100%（或按已有数据），不再查 river_job。
	if run.Status == ReindexStatusCompleted || run.Status == ReindexStatusFailed {
		if run.Status == ReindexStatusCompleted {
			out.Processed = run.Total
			out.Percent = 100
		}
		return out, nil
	}

	// running：查剩余 job 数算进度。
	remaining, err := m.store.CountPendingIndexJobs(ctx)
	if err != nil {
		// 查询失败不阻断，降级为不显示进度数字。
		slog.WarnContext(ctx, "search: count pending index jobs failed", "err", err)
		return out, nil
	}
	out.Remaining = remaining
	if run.Total > 0 {
		processed := run.Total - remaining
		if processed < 0 {
			processed = 0
		}
		if processed > run.Total {
			processed = run.Total
		}
		out.Processed = processed
		out.Percent = int(processed * 100 / run.Total)
	}

	// 剩余归零且仍 running：自动标记完成（worker 已处理完所有 job）。
	if remaining == 0 && run.Status == ReindexStatusRunning {
		if err := m.store.FinishRun(ctx, run.ID, ReindexStatusCompleted, ""); err != nil {
			slog.WarnContext(ctx, "search: mark reindex completed failed", "runId", run.ID, "err", err)
		} else {
			out.Status = ReindexStatusCompleted
			out.Processed = run.Total
			out.Percent = 100
			now := time.Now().UTC()
			out.FinishedAt = &now
		}
	}

	return out, nil
}

// ListReindexRuns 返回最近的历史 run（默认 20 条）。
func (m *ReindexManager) ListReindexRuns(ctx context.Context) ([]ReindexRun, error) {
	return m.store.ListRuns(ctx, 20)
}
