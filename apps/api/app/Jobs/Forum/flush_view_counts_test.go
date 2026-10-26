package forumjobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

type flushDrainer struct {
	deltas map[int64]int64
	err    error
}

func (d *flushDrainer) DrainDeltas(context.Context) (map[int64]int64, error) {
	return d.deltas, d.err
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
		Drainer: &flushDrainer{deltas: map[int64]int64{10: 3, 11: 1}},
		Store:   store,
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
		Drainer: &flushDrainer{deltas: map[int64]int64{}},
		Store:   store,
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
	w := &FlushViewCountsWorker{Drainer: counter, Store: store}
	if err := w.Work(context.Background(), &river.Job[FlushViewCountsArgs]{}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.got[7] != 2 {
		t.Fatalf("got %v", store.got)
	}
}
