package queryregistryjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const (
	InvalidateResultCacheKind          = "query.invalidate_result_cache"
	InvalidateResultCacheSchemaVersion = 1
	queryInvalidationSnooze            = time.Minute
)

// InvalidateResultCacheArgs carries only stable owner identity and canonical
// logical tags. Physical Redis material and exact runtime identity are derived
// by the Host at execution time and must never enter River rows.
type InvalidateResultCacheArgs struct {
	SchemaVersion    int      `json:"schema_version"`
	OwnerExtensionID string   `json:"owner_extension_id"`
	Tags             []string `json:"tags"`
}

func (InvalidateResultCacheArgs) Kind() string { return InvalidateResultCacheKind }

func NewInvalidateResultCacheArgs(owner string, tags []string) (InvalidateResultCacheArgs, error) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	canonical, err := queryregistry.CanonicalSemanticCacheTags(owner, tags)
	if err != nil {
		return InvalidateResultCacheArgs{}, err
	}
	return InvalidateResultCacheArgs{
		SchemaVersion: InvalidateResultCacheSchemaVersion, OwnerExtensionID: owner,
		Tags: canonical,
	}, nil
}

func (InvalidateResultCacheArgs) QueueOpts() supportjobs.EnqueueOptions {
	// Do not add River uniqueness here. A second mutation while an identical job
	// is running still requires a later epoch rotation of its own.
	// MaxAttempts bounds accidental ordinary errors only. River v0.40 snoozes
	// deliberately restore the attempt count so Safe Mode/outages never discard
	// an already committed invalidation.
	return supportjobs.EnqueueOptions{Queue: supportjobs.QueueCritical, MaxAttempts: 10}
}

func (a InvalidateResultCacheArgs) valid() bool {
	if a.SchemaVersion != InvalidateResultCacheSchemaVersion ||
		a.OwnerExtensionID != strings.ToLower(strings.TrimSpace(a.OwnerExtensionID)) {
		return false
	}
	canonical, err := queryregistry.CanonicalSemanticCacheTags(a.OwnerExtensionID, a.Tags)
	return err == nil && slices.Equal(canonical, a.Tags)
}

type InvalidateResultCacheWorker struct {
	river.WorkerDefaults[InvalidateResultCacheArgs]
	Invalidator queryregistry.SemanticCacheInvalidator
	Logger      *slog.Logger
}

func (w *InvalidateResultCacheWorker) Work(
	ctx context.Context,
	job *river.Job[InvalidateResultCacheArgs],
) error {
	if job == nil || !job.Args.valid() {
		return river.JobCancel(errors.New("invalid Query result cache invalidation envelope"))
	}
	if ctx == nil || w == nil || w.Invalidator == nil {
		return river.JobSnooze(queryInvalidationSnooze)
	}
	if _, err := w.Invalidator.InvalidateOwnerTags(ctx, job.Args.OwnerExtensionID, slices.Clone(job.Args.Tags)); err != nil {
		if errors.Is(err, queryregistry.ErrInvalid) {
			return river.JobCancel(fmt.Errorf("invalid Query result cache invalidation: %w", err))
		}
		if w.Logger != nil {
			w.Logger.Warn("Query result cache invalidation deferred",
				"owner", job.Args.OwnerExtensionID, "tags", len(job.Args.Tags),
				"error_class", queryInvalidationErrorClass(err))
		}
		return river.JobSnooze(queryInvalidationSnooze)
	}
	return nil
}

func queryInvalidationErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, queryregistry.ErrExecutionInvalid):
		return "runtime_invalid"
	case errors.Is(err, queryregistry.ErrCacheCapability):
		return "capability"
	case errors.Is(err, queryregistry.ErrCachePoisoned):
		return "poisoned"
	case errors.Is(err, queryregistry.ErrCacheDurability):
		return "durability"
	default:
		return "transport"
	}
}

func EnqueueInvalidationTx(
	ctx context.Context,
	dispatcher *supportjobs.Dispatcher,
	tx pgx.Tx,
	owner string,
	tags []string,
) (*rivertype.JobInsertResult, error) {
	if dispatcher == nil || tx == nil {
		return nil, errors.New("Query result cache invalidation dispatcher is unavailable")
	}
	args, err := NewInvalidateResultCacheArgs(owner, tags)
	if err != nil {
		return nil, err
	}
	return dispatcher.EnqueueTx(ctx, tx, args, args.QueueOpts())
}

func Register(
	registry *supportjobs.Registry,
	invalidator queryregistry.SemanticCacheInvalidator,
	logger *slog.Logger,
) {
	if registry == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[InvalidateResultCacheArgs](workers, &InvalidateResultCacheWorker{
			Invalidator: invalidator,
			Logger:      logger,
		})
	})
}
