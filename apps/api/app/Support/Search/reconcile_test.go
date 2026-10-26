package search

import (
	"context"
	"errors"
	"testing"
	"time"

	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type fakeProviderResolver struct {
	id  string
	ok  bool
	err error
}

func (r fakeProviderResolver) SelectedID(context.Context) (string, bool, error) {
	return r.id, r.ok, r.err
}

type fakeIndexStateStore struct {
	stale          []int64
	obsolete       []int64
	staleLimit     int
	obsoleteLimit  int
	staleProvider  string
	deleteProvider string
	err            error
}

func (*fakeIndexStateStore) MarkIndexed(context.Context, string, int64, time.Time) error {
	return nil
}

func (*fakeIndexStateStore) MarkDeleted(context.Context, string, int64) error {
	return nil
}

func (s *fakeIndexStateStore) ListStaleTopicIDs(_ context.Context, providerID string, limit int) ([]int64, error) {
	s.staleProvider = providerID
	s.staleLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.stale, nil
}

func (s *fakeIndexStateStore) ListObsoleteTopicIDs(_ context.Context, providerID string, limit int) ([]int64, error) {
	s.deleteProvider = providerID
	s.obsoleteLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.obsolete, nil
}

func TestReconcilerEnqueuesOnlyDifferencesForSelectedProvider(t *testing.T) {
	state := &fakeIndexStateStore{stale: []int64{1, 2}, obsolete: []int64{9}}
	client := &fakeReindexRiverClient{}
	reconciler := NewReconciler(
		fakeProviderResolver{id: "vendor.search", ok: true},
		state,
		supportjobs.NewDispatcher(client),
	).WithBatchSize(25)

	result, err := reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderID != "vendor.search" || result.IndexJobs != 2 || result.DeleteJobs != 1 {
		t.Fatalf("result = %+v", result)
	}
	if state.staleProvider != "vendor.search" || state.deleteProvider != "vendor.search" ||
		state.staleLimit != 25 || state.obsoleteLimit != 25 {
		t.Fatalf("state queries = %+v", state)
	}
	if len(client.insertManyArgs) != 3 || client.insertManyCalls != 2 {
		t.Fatalf("insert calls=%d args=%#v", client.insertManyCalls, client.insertManyArgs)
	}
	if arg, ok := client.insertManyArgs[0].(searchjobs.IndexTopicArgs); !ok || arg.TopicID != 1 {
		t.Fatalf("first job = %#v", client.insertManyArgs[0])
	}
	if arg, ok := client.insertManyArgs[2].(searchjobs.DeleteTopicArgs); !ok || arg.TopicID != 9 {
		t.Fatalf("delete job = %#v", client.insertManyArgs[2])
	}
	for _, opts := range client.insertManyOpts {
		if !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) == 0 {
			t.Fatalf("reconcile repair job must keep active-state uniqueness: %+v", opts.UniqueOpts)
		}
	}
}

func TestReconcilerNoProviderIsNoop(t *testing.T) {
	state := &fakeIndexStateStore{}
	client := &fakeReindexRiverClient{}
	result, err := NewReconciler(
		fakeProviderResolver{},
		state,
		supportjobs.NewDispatcher(client),
	).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (ReconcileResult{}) || client.insertManyCalls != 0 {
		t.Fatalf("result=%+v insertCalls=%d", result, client.insertManyCalls)
	}
}

func TestReconcilerStoreFailureIsRetryable(t *testing.T) {
	expected := errors.New("ledger unavailable")
	reconciler := NewReconciler(
		fakeProviderResolver{id: DefaultSiteSearchExtensionID, ok: true},
		&fakeIndexStateStore{err: expected},
		supportjobs.NewDispatcher(&fakeReindexRiverClient{}),
	)
	if err := reconciler.Reconcile(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("error = %v, want ledger error", err)
	}
}

func TestNormalizeReconcileLimitUsesBoundedDefault(t *testing.T) {
	if got := normalizeReconcileLimit(0); got != RecommendedReconcileBatchSize {
		t.Fatalf("zero limit = %d", got)
	}
	if got := normalizeReconcileLimit(5001); got != RecommendedReconcileBatchSize {
		t.Fatalf("oversized limit = %d", got)
	}
	if got := normalizeReconcileLimit(25); got != 25 {
		t.Fatalf("explicit limit = %d", got)
	}
}
