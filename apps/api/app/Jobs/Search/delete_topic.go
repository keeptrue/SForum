package searchjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// DeleteTopicArgs 调度从搜索索引移除主题（删除/隐藏）。
type DeleteTopicArgs struct {
	TopicID int64 `json:"topic_id" river:"unique"`
}

func (DeleteTopicArgs) Kind() string {
	return "search.delete_topic"
}

func (a DeleteTopicArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return a.QueueOpts()
}

func (DeleteTopicArgs) QueueOpts() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueSearch,
		MaxAttempts: 10,
		Unique: river.UniqueOpts{
			ByArgs:  true,
			ByState: activeSearchJobStates(),
		},
	}
}

type DeleteTopicWorker struct {
	river.WorkerDefaults[DeleteTopicArgs]
	Indexer TopicIndexer
}

func (w *DeleteTopicWorker) Work(ctx context.Context, job *river.Job[DeleteTopicArgs]) error {
	if job.Args.TopicID <= 0 {
		return fmt.Errorf("delete topic job requires positive topic id: %d", job.Args.TopicID)
	}
	if w.Indexer == nil {
		return fmt.Errorf("delete topic worker requires indexer")
	}
	return w.Indexer.DeleteTopic(ctx, job.Args.TopicID)
}
