package searchjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// TopicIndexer 由 search 支持包实现，供 worker 执行实际索引/删除操作。
// 相比原 v1 增加了 DeleteTopic，覆盖主题删除/隐藏场景。
type TopicIndexer interface {
	IndexTopic(ctx context.Context, topicID int64) error
	DeleteTopic(ctx context.Context, topicID int64) error
}

// IndexTopicArgs 调度主题重新索引（创建/更新/评论/恢复/置顶等）。
type IndexTopicArgs struct {
	TopicID int64 `json:"topic_id" river:"unique"`
}

func (IndexTopicArgs) Kind() string {
	return "search.index_topic"
}

func (a IndexTopicArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return a.QueueOpts()
}

// QueueOpts 导出队列配置，供 search.Indexer 在不同包内调度复用。
func (IndexTopicArgs) QueueOpts() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueSearch,
		MaxAttempts: 10,
		Unique: river.UniqueOpts{
			ByArgs:  true,
			ByState: activeSearchJobStates(),
		},
	}
}

// activeSearchJobStates 只合并仍可能执行的同主题任务。
// completed 若参与唯一性，会让后续编辑和全量重建在 River 清理历史任务前永久跳过。
func activeSearchJobStates() []rivertype.JobState {
	return []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRetryable,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
	}
}

type IndexTopicWorker struct {
	river.WorkerDefaults[IndexTopicArgs]
	Indexer TopicIndexer
}

func (w *IndexTopicWorker) Work(ctx context.Context, job *river.Job[IndexTopicArgs]) error {
	if job.Args.TopicID <= 0 {
		return fmt.Errorf("index topic job requires positive topic id: %d", job.Args.TopicID)
	}
	if w.Indexer == nil {
		return fmt.Errorf("index topic worker requires indexer")
	}
	return w.Indexer.IndexTopic(ctx, job.Args.TopicID)
}

// Register 注册搜索相关 worker（索引 + 删除）。indexer 实现 TopicIndexer。
func Register(registry *supportjobs.Registry, indexer TopicIndexer) {
	registry.Add(func(workers *river.Workers) error {
		if err := river.AddWorkerSafely[IndexTopicArgs](workers, &IndexTopicWorker{Indexer: indexer}); err != nil {
			return err
		}
		return river.AddWorkerSafely[DeleteTopicArgs](workers, &DeleteTopicWorker{Indexer: indexer})
	})
}
