package search

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"

	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// ProviderIDResolver 返回当前选中的搜索 provider。
// ResolvingSearchEngine 实现该接口；普通 Engine 视为默认站内搜索。
type ProviderIDResolver interface {
	SelectedID(ctx context.Context) (string, bool, error)
}

type ReconcileResult struct {
	ProviderID string
	IndexJobs  int
	DeleteJobs int
}

// Reconciler 只调度差异任务；实际传输仍由 IndexTopic/DeleteTopic worker 完成。
type Reconciler struct {
	providers  ProviderIDResolver
	store      IndexStateStore
	dispatcher *supportjobs.Dispatcher
	batchSize  int
	logger     *slog.Logger
}

func NewReconciler(providers ProviderIDResolver, store IndexStateStore, dispatcher *supportjobs.Dispatcher) *Reconciler {
	return &Reconciler{
		providers: providers, store: store, dispatcher: dispatcher,
		batchSize: RecommendedReconcileBatchSize,
	}
}

func (r *Reconciler) WithLogger(logger *slog.Logger) *Reconciler {
	if r != nil {
		r.logger = logger
	}
	return r
}

func (r *Reconciler) WithDispatcher(dispatcher *supportjobs.Dispatcher) *Reconciler {
	if r != nil {
		r.dispatcher = dispatcher
	}
	return r
}

func (r *Reconciler) WithBatchSize(batchSize int) *Reconciler {
	if r != nil {
		r.batchSize = normalizeReconcileLimit(batchSize)
	}
	return r
}

func (r *Reconciler) Reconcile(ctx context.Context) error {
	_, err := r.ReconcileOnce(ctx)
	return err
}

func (r *Reconciler) ReconcileOnce(ctx context.Context) (ReconcileResult, error) {
	if r == nil || r.providers == nil || r.store == nil || r.dispatcher == nil {
		return ReconcileResult{}, fmt.Errorf("search reconciler is not fully configured")
	}
	providerID, ok, err := r.providers.SelectedID(ctx)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("resolve search provider for reconciliation: %w", err)
	}
	if !ok || providerID == "" {
		return ReconcileResult{}, nil
	}
	result := ReconcileResult{ProviderID: providerID}
	stale, err := r.store.ListStaleTopicIDs(ctx, providerID, r.batchSize)
	if err != nil {
		return result, err
	}
	obsolete, err := r.store.ListObsoleteTopicIDs(ctx, providerID, r.batchSize)
	if err != nil {
		return result, err
	}
	if err := enqueueReconcileIndexJobs(ctx, r.dispatcher, stale); err != nil {
		return result, err
	}
	result.IndexJobs = len(stale)
	if err := enqueueReconcileDeleteJobs(ctx, r.dispatcher, obsolete); err != nil {
		return result, err
	}
	result.DeleteJobs = len(obsolete)
	if r.logger != nil && (result.IndexJobs > 0 || result.DeleteJobs > 0) {
		r.logger.InfoContext(ctx, "search reconciliation enqueued repairs",
			"providerId", providerID,
			"indexJobs", result.IndexJobs,
			"deleteJobs", result.DeleteJobs,
		)
	}
	return result, nil
}

func enqueueReconcileIndexJobs(ctx context.Context, dispatcher *supportjobs.Dispatcher, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]river.JobArgs, 0, len(ids))
	for _, id := range ids {
		args = append(args, searchjobs.IndexTopicArgs{TopicID: id})
	}
	if _, err := dispatcher.EnqueueMany(ctx, args, searchjobs.IndexTopicArgs{}.QueueOpts()); err != nil {
		return fmt.Errorf("enqueue search reconciliation index jobs: %w", err)
	}
	return nil
}

func enqueueReconcileDeleteJobs(ctx context.Context, dispatcher *supportjobs.Dispatcher, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]river.JobArgs, 0, len(ids))
	for _, id := range ids {
		args = append(args, searchjobs.DeleteTopicArgs{TopicID: id})
	}
	if _, err := dispatcher.EnqueueMany(ctx, args, searchjobs.DeleteTopicArgs{}.QueueOpts()); err != nil {
		return fmt.Errorf("enqueue search reconciliation delete jobs: %w", err)
	}
	return nil
}
