package forumjobs

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

type flushSource struct {
	deltas map[int64]int64
	err    error
}

func (d *flushSource) ReadViewDeltas(context.Context) (map[int64]int64, error) {
	return d.deltas, d.err
}

// flushAcker 记录「落库成功后扣减」的调用顺序。
type flushAcker struct {
	acked map[int64]int64
	calls int
}

func (a *flushAcker) AckViewDeltas(_ context.Context, applied map[int64]int64) error {
	a.calls++
	a.acked = applied
	return nil
}

type flushStore struct {
	got map[int64]int64
	n   int
	err error
}

func (s *flushStore) ApplyViewCountDeltas(_ context.Context, deltas map[int64]int64) (int, error) {
	s.got = deltas
	if s.err != nil {
		return 0, s.err
	}
	return s.n, nil
}

func TestFlushViewCountsWorkerAppliesDrain(t *testing.T) {
	store := &flushStore{n: 2}
	w := &FlushViewCountsWorker{
		Source: &flushSource{deltas: map[int64]int64{10: 3, 11: 1}},
		Acker:  &flushAcker{},
		Store:  store,
	}
	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.got[10] != 3 || store.got[11] != 1 {
		t.Fatalf("applied deltas: %v", store.got)
	}
}

func TestFlushViewCountsWorkerEmptyDrain(t *testing.T) {
	store := &flushStore{}
	w := &FlushViewCountsWorker{
		Source: &flushSource{deltas: map[int64]int64{}},
		Acker:  &flushAcker{},
		Store:  store,
	}
	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.got != nil {
		t.Fatalf("should not apply empty: %v", store.got)
	}
}

func TestFlushViewCountsWorkerUsesMemoryCounter(t *testing.T) {
	counter := forum.NewMemoryTopicViewCounter()
	counter.RecordView(context.Background(), 7, "u:1")
	counter.RecordView(context.Background(), 7, "u:2")
	store := &flushStore{n: 1}
	w := &FlushViewCountsWorker{Source: counter, Acker: counter, Store: store}
	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.got[7] != 2 {
		t.Fatalf("got %v", store.got)
	}
	if counter.Delta(7) != 0 {
		t.Fatalf("applied delta was not acked: %d", counter.Delta(7))
	}
}

// 落库失败时增量必须留在 Redis：先扣减再落库会把这段浏览永久丢掉（RV-52）。
func TestFlushViewCountsKeepsDeltasWhenApplyFails(t *testing.T) {
	counter := forum.NewMemoryTopicViewCounter()
	counter.RecordView(context.Background(), 7, "u:1")
	store := &flushStore{err: errors.New("pg down")}
	acker := &flushAcker{}
	w := &FlushViewCountsWorker{Source: counter, Acker: acker, Store: store}

	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err == nil {
		t.Fatal("apply failure must surface as a retryable error")
	}
	if got := counter.Delta(7); got != 1 {
		t.Fatalf("deltas were consumed before the database write succeeded: delta(7)=%d, want 1", got)
	}
	if acker.calls != 0 {
		t.Fatalf("acker ran %d times after a failed apply, want 0", acker.calls)
	}
}

// 成功路径：扣减紧跟在落库之后，且只扣一次。
func TestFlushViewCountsAcksOnlyAfterApply(t *testing.T) {
	store := &flushStore{n: 1}
	acker := &flushAcker{}
	w := &FlushViewCountsWorker{
		Source: &flushSource{deltas: map[int64]int64{10: 2}},
		Acker:  acker,
		Store:  store,
	}
	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if acker.calls != 1 || acker.acked[10] != 2 {
		t.Fatalf("ack = %#v (calls=%d)", acker.acked, acker.calls)
	}
}
