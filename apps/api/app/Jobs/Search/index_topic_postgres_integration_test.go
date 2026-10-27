package searchjobs

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真实 River 唯一索引回归：历史 completed 不能阻塞重建，活跃任务仍必须去重。
func TestIndexTopicUniqueStatesAllowReindexAfterCompletion(t *testing.T) {
	dsn := testdb.IsolatedURL(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: "public"})
	if err != nil {
		t.Fatalf("create River client: %v", err)
	}
	args := IndexTopicArgs{TopicID: time.Now().UnixNano()}
	opts := args.QueueOpts().RiverInsertOpts()

	first, err := client.Insert(ctx, args, opts)
	if err != nil {
		t.Fatalf("insert first job: %v", err)
	}
	jobIDs := []int64{first.Job.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM river_job WHERE id = ANY($1::bigint[])`, jobIDs)
	})
	if first.UniqueSkippedAsDuplicate {
		t.Fatal("first job unexpectedly skipped")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1
	`, first.Job.ID); err != nil {
		t.Fatalf("complete first job: %v", err)
	}

	second, err := client.Insert(ctx, args, opts)
	if err != nil {
		t.Fatalf("insert after completion: %v", err)
	}
	jobIDs = append(jobIDs, second.Job.ID)
	if second.UniqueSkippedAsDuplicate || second.Job.ID == first.Job.ID {
		t.Fatalf("completed job blocked reindex: first=%d second=%d skipped=%v", first.Job.ID, second.Job.ID, second.UniqueSkippedAsDuplicate)
	}

	duplicate, err := client.Insert(ctx, args, opts)
	if err != nil {
		t.Fatalf("insert while active: %v", err)
	}
	if !duplicate.UniqueSkippedAsDuplicate || duplicate.Job.ID != second.Job.ID {
		t.Fatalf("active job was not deduplicated: active=%d duplicate=%d skipped=%v", second.Job.ID, duplicate.Job.ID, duplicate.UniqueSkippedAsDuplicate)
	}
}
