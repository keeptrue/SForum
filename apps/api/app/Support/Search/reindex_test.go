package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// fakeTopicIDSource 返回预设的 topic ID 列表。
type fakeTopicIDSource struct {
	ids []int64
	err error
}

func (f fakeTopicIDSource) ListAllTopicIDs(_ context.Context) ([]int64, error) {
	return f.ids, f.err
}

// fakeReindexStore 内存实现 ReindexStore。
type fakeReindexStore struct {
	runs         []ReindexRun
	pendingCount int64
	createErr    error
}

func (s *fakeReindexStore) CreateRun(_ context.Context, total int64, startedByUserID int64) (ReindexRun, error) {
	if s.createErr != nil {
		return ReindexRun{}, s.createErr
	}
	run := ReindexRun{ID: int64(len(s.runs) + 1), Total: total, Status: ReindexStatusRunning, StartedAt: time.Now().UTC(), StartedByUserID: startedByUserID}
	s.runs = append(s.runs, run)
	return run, nil
}

func (s *fakeReindexStore) GetCurrentRun(_ context.Context) (ReindexRun, error) {
	if len(s.runs) == 0 {
		return ReindexRun{}, ErrNoReindexRun
	}
	return s.runs[len(s.runs)-1], nil
}

func (s *fakeReindexStore) GetRun(_ context.Context, id int64) (ReindexRun, error) {
	for _, r := range s.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return ReindexRun{}, ErrNoReindexRun
}

func (s *fakeReindexStore) ListRuns(_ context.Context, limit int) ([]ReindexRun, error) {
	return s.runs, nil
}

func (s *fakeReindexStore) FinishRun(_ context.Context, id int64, status string, runErr string) error {
	for i := range s.runs {
		if s.runs[i].ID == id {
			s.runs[i].Status = status
			s.runs[i].Error = runErr
			now := time.Now().UTC()
			s.runs[i].FinishedAt = &now
		}
	}
	return nil
}

func (s *fakeReindexStore) CountPendingIndexJobs(_ context.Context) (int64, error) {
	return s.pendingCount, nil
}

// fakeReindexRiverClient 实现 RiverClient 接口，记录批量入队参数。
type fakeReindexRiverClient struct {
	insertManyCalls int
	insertManyArgs  []river.JobArgs
	insertManyOpts  []*river.InsertOpts
	err             error
}

func (f *fakeReindexRiverClient) Insert(_ context.Context, _ river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{}, f.err
}

func (f *fakeReindexRiverClient) InsertTx(_ context.Context, _ pgx.Tx, _ river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{}, f.err
}

func (f *fakeReindexRiverClient) InsertMany(_ context.Context, params []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	f.insertManyCalls++
	for _, p := range params {
		f.insertManyArgs = append(f.insertManyArgs, p.Args)
		f.insertManyOpts = append(f.insertManyOpts, p.InsertOpts)
	}
	if f.err != nil {
		return nil, f.err
	}
	results := make([]*rivertype.JobInsertResult, len(params))
	for i := range results {
		results[i] = &rivertype.JobInsertResult{}
	}
	return results, nil
}

func newReindexManagerForTest(topics TopicIDSource, store ReindexStore, client *fakeReindexRiverClient) *ReindexManager {
	dispatcher := supportjobs.NewDispatcher(client)
	return NewReindexManager(topics, store, dispatcher)
}

func TestReindexEnqueuesAllTopicIDsAndCreatesRun(t *testing.T) {
	ctx := context.Background()
	topics := fakeTopicIDSource{ids: []int64{1, 2, 3, 4, 5}}
	store := &fakeReindexStore{}
	client := &fakeReindexRiverClient{}
	mgr := newReindexManagerForTest(topics, store, client)

	run, err := mgr.Reindex(ctx, 42)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if run.Total != 5 {
		t.Fatalf("expected total 5, got %d", run.Total)
	}
	if run.Status != ReindexStatusRunning {
		t.Fatalf("expected running, got %s", run.Status)
	}
	if len(client.insertManyArgs) != 5 {
		t.Fatalf("expected 5 args enqueued, got %d", len(client.insertManyArgs))
	}
	for _, opts := range client.insertManyOpts {
		if opts.UniqueOpts.ByArgs || opts.UniqueOpts.ByState != nil || opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod != 0 {
			t.Fatalf("full reindex jobs must bypass historical uniqueness: %+v", opts.UniqueOpts)
		}
	}
	if client.insertManyCalls != 1 {
		t.Fatalf("expected 1 InsertMany call for 5 ids (< batch 1000), got %d", client.insertManyCalls)
	}
}

func TestReindexBatchesLargeTopicCount(t *testing.T) {
	ctx := context.Background()
	// 2500 个 ID 应分 3 批（1000/1000/500）。
	ids := make([]int64, 2500)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	topics := fakeTopicIDSource{ids: ids}
	store := &fakeReindexStore{}
	client := &fakeReindexRiverClient{}
	mgr := newReindexManagerForTest(topics, store, client)

	run, err := mgr.Reindex(ctx, 1)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if run.Total != 2500 {
		t.Fatalf("expected total 2500, got %d", run.Total)
	}
	if client.insertManyCalls != 3 {
		t.Fatalf("expected 3 batches, got %d", client.insertManyCalls)
	}
	if len(client.insertManyArgs) != 2500 {
		t.Fatalf("expected 2500 total args, got %d", len(client.insertManyArgs))
	}
}

func TestReindexRejectsConcurrentRunning(t *testing.T) {
	ctx := context.Background()
	topics := fakeTopicIDSource{ids: []int64{1, 2}}
	store := &fakeReindexStore{}
	// 预置一个 running run。
	store.runs = append(store.runs, ReindexRun{ID: 1, Total: 2, Status: ReindexStatusRunning, StartedAt: time.Now().UTC()})
	client := &fakeReindexRiverClient{}
	mgr := newReindexManagerForTest(topics, store, client)

	_, err := mgr.Reindex(ctx, 1)
	if !errors.Is(err, ErrReindexAlreadyRunning) {
		t.Fatalf("expected ErrReindexAlreadyRunning, got %v", err)
	}
	if client.insertManyCalls != 0 {
		t.Fatalf("expected no enqueue on rejected reindex, got %d calls", client.insertManyCalls)
	}
}

func TestReindexStatusReportsProgress(t *testing.T) {
	ctx := context.Background()
	topics := fakeTopicIDSource{ids: []int64{1, 2, 3, 4}}
	store := &fakeReindexStore{pendingCount: 1} // 4 total, 1 remaining
	client := &fakeReindexRiverClient{}
	mgr := newReindexManagerForTest(topics, store, client)

	_, _ = mgr.Reindex(ctx, 1)

	status, err := mgr.ReindexStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Total != 4 {
		t.Fatalf("expected total 4, got %d", status.Total)
	}
	if status.Remaining != 1 {
		t.Fatalf("expected remaining 1, got %d", status.Remaining)
	}
	if status.Processed != 3 {
		t.Fatalf("expected processed 3, got %d", status.Processed)
	}
	if status.Percent != 75 {
		t.Fatalf("expected percent 75, got %d", status.Percent)
	}
}

func TestReindexStatusAutoCompletesWhenNoPending(t *testing.T) {
	ctx := context.Background()
	topics := fakeTopicIDSource{ids: []int64{1, 2}}
	store := &fakeReindexStore{pendingCount: 0} // 全部处理完
	client := &fakeReindexRiverClient{}
	mgr := newReindexManagerForTest(topics, store, client)

	_, _ = mgr.Reindex(ctx, 1)

	status, err := mgr.ReindexStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != ReindexStatusCompleted {
		t.Fatalf("expected auto-completed, got %s", status.Status)
	}
	if status.Percent != 100 {
		t.Fatalf("expected 100%%, got %d", status.Percent)
	}
}

func TestReindexStatusNoRunReturnsError(t *testing.T) {
	ctx := context.Background()
	store := &fakeReindexStore{} // 无 run
	mgr := newReindexManagerForTest(fakeTopicIDSource{}, store, &fakeReindexRiverClient{})

	_, err := mgr.ReindexStatus(ctx)
	if !errors.Is(err, ErrNoReindexRun) {
		t.Fatalf("expected ErrNoReindexRun, got %v", err)
	}
}

func TestReindexEnqueueFailureMarksRunFailed(t *testing.T) {
	ctx := context.Background()
	topics := fakeTopicIDSource{ids: []int64{1, 2}}
	store := &fakeReindexStore{}
	client := &fakeReindexRiverClient{err: errors.New("river down")}
	mgr := newReindexManagerForTest(topics, store, client)

	_, err := mgr.Reindex(ctx, 1)
	if err == nil {
		t.Fatal("expected enqueue error")
	}
	// run 应被标记为 failed。
	run, _ := store.GetCurrentRun(ctx)
	if run.Status != ReindexStatusFailed {
		t.Fatalf("expected run failed, got %s", run.Status)
	}
}
