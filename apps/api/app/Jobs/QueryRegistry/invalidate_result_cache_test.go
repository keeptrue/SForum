package queryregistryjobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestInvalidateResultCacheArgsCanonicalizeWithoutUniqueness(t *testing.T) {
	args, err := NewInvalidateResultCacheArgs(" OWNER.PLUGIN ", []string{
		"owner.plugin.topics", " OWNER.PLUGIN.MEMBERS ",
	})
	if err != nil {
		t.Fatalf("new args: %v", err)
	}
	if args.SchemaVersion != InvalidateResultCacheSchemaVersion || args.OwnerExtensionID != "owner.plugin" ||
		!slices.Equal(args.Tags, []string{"owner.plugin.members", "owner.plugin.topics"}) || !args.valid() {
		t.Fatalf("args=%#v", args)
	}
	opts := args.QueueOpts()
	if opts.Queue != supportjobs.QueueCritical || opts.MaxAttempts != 10 || opts.Unique.ByArgs ||
		opts.Unique.ByPeriod != 0 || opts.Unique.ByQueue || opts.Unique.ByState != nil || opts.Unique.ExcludeKind {
		t.Fatalf("queue opts=%#v", opts)
	}
}

func TestInvalidateResultCacheArgsRejectMalformedEnvelope(t *testing.T) {
	valid, err := NewInvalidateResultCacheArgs("owner.plugin", []string{
		"owner.plugin.members", "owner.plugin.topics",
	})
	if err != nil {
		t.Fatalf("valid args: %v", err)
	}
	tests := []InvalidateResultCacheArgs{
		{},
		{SchemaVersion: 2, OwnerExtensionID: valid.OwnerExtensionID, Tags: valid.Tags},
		{SchemaVersion: 1, OwnerExtensionID: "OWNER.PLUGIN", Tags: valid.Tags},
		{SchemaVersion: 1, OwnerExtensionID: valid.OwnerExtensionID, Tags: []string{"owner.plugin.topics", "owner.plugin.members"}},
		{SchemaVersion: 1, OwnerExtensionID: valid.OwnerExtensionID, Tags: []string{"other.plugin.topics"}},
		{SchemaVersion: 1, OwnerExtensionID: valid.OwnerExtensionID, Tags: []string{"owner.plugin.topics", "owner.plugin.topics"}},
	}
	for index, args := range tests {
		if args.valid() {
			t.Fatalf("case %d accepted: %#v", index, args)
		}
	}
}

type recordingInvalidator struct {
	calls int
	owner string
	tags  []string
	err   error
}

func (i *recordingInvalidator) InvalidateOwnerTags(_ context.Context, owner string, tags []string) (uint64, error) {
	i.calls++
	i.owner = owner
	i.tags = slices.Clone(tags)
	if len(tags) > 0 {
		tags[0] = "mutated"
	}
	return uint64(len(tags)), i.err
}

func TestInvalidateResultCacheWorkerInvalidatesCanonicalOwnerTags(t *testing.T) {
	args, err := NewInvalidateResultCacheArgs("owner.plugin", []string{"owner.plugin.topics"})
	if err != nil {
		t.Fatalf("new args: %v", err)
	}
	invalidator := &recordingInvalidator{}
	worker := &InvalidateResultCacheWorker{Invalidator: invalidator}
	if err := worker.Work(context.Background(), &river.Job[InvalidateResultCacheArgs]{Args: args}); err != nil {
		t.Fatalf("work: %v", err)
	}
	if invalidator.calls != 1 || invalidator.owner != args.OwnerExtensionID || !slices.Equal(invalidator.tags, args.Tags) ||
		args.Tags[0] != "owner.plugin.topics" {
		t.Fatalf("invalidator=%#v args=%#v", invalidator, args)
	}
}

func TestInvalidateResultCacheWorkerCancelsMalformedAndSnoozesAuthorityFailures(t *testing.T) {
	valid, err := NewInvalidateResultCacheArgs("owner.plugin", []string{"owner.plugin.topics"})
	if err != nil {
		t.Fatalf("new args: %v", err)
	}
	var typedNil *queryregistry.RedisQueryResultCache
	tests := []struct {
		name        string
		args        InvalidateResultCacheArgs
		invalidator queryregistry.SemanticCacheInvalidator
		cancel      bool
	}{
		{name: "malformed", args: InvalidateResultCacheArgs{}, invalidator: &recordingInvalidator{}, cancel: true},
		{name: "nil invalidator", args: valid},
		{name: "typed nil invalidator", args: valid, invalidator: typedNil},
		{name: "invalid at authority", args: valid, invalidator: &recordingInvalidator{err: queryregistry.ErrInvalid}, cancel: true},
		{name: "runtime invalid", args: valid, invalidator: &recordingInvalidator{err: queryregistry.ErrExecutionInvalid}},
		{name: "capability", args: valid, invalidator: &recordingInvalidator{err: queryregistry.ErrCacheCapability}},
		{name: "poison", args: valid, invalidator: &recordingInvalidator{err: queryregistry.ErrCachePoisoned}},
		{name: "durability", args: valid, invalidator: &recordingInvalidator{err: queryregistry.ErrCacheDurability}},
		{name: "transport", args: valid, invalidator: &recordingInvalidator{err: errors.New("redis unavailable")}},
		{name: "canceled", args: valid, invalidator: &recordingInvalidator{err: context.Canceled}},
		{name: "deadline", args: valid, invalidator: &recordingInvalidator{err: context.DeadlineExceeded}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := &InvalidateResultCacheWorker{Invalidator: test.invalidator}
			err := worker.Work(context.Background(), &river.Job[InvalidateResultCacheArgs]{Args: test.args})
			if test.cancel {
				var cancelErr *river.JobCancelError
				if !errors.As(err, &cancelErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			var snoozeErr *river.JobSnoozeError
			if !errors.As(err, &snoozeErr) || snoozeErr.Duration != queryInvalidationSnooze {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestInvalidateResultCacheWorkerSnoozesNilContextWithoutCallingInvalidator(t *testing.T) {
	args, err := NewInvalidateResultCacheArgs("owner.plugin", []string{"owner.plugin.topics"})
	if err != nil {
		t.Fatal(err)
	}
	invalidator := &recordingInvalidator{}
	err = (&InvalidateResultCacheWorker{Invalidator: invalidator}).Work(nil, &river.Job[InvalidateResultCacheArgs]{Args: args})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || invalidator.calls != 0 {
		t.Fatalf("err=%v invalidator=%#v", err, invalidator)
	}
}

func TestInvalidateResultCacheWorkerLogsOnlyStableFailureClass(t *testing.T) {
	args, err := NewInvalidateResultCacheArgs("owner.plugin", []string{"owner.plugin.topics"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	secret := "redis://credential@sforum:query-result:value owner.plugin.topics"
	worker := &InvalidateResultCacheWorker{
		Invalidator: &recordingInvalidator{err: errors.New(secret)}, Logger: logger,
	}
	if err := worker.Work(context.Background(), &river.Job[InvalidateResultCacheArgs]{Args: args}); err == nil {
		t.Fatal("transport failure must be deferred")
	}
	logged := output.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, "owner.plugin.topics") ||
		!strings.Contains(logged, "error_class=transport") || !strings.Contains(logged, "tags=1") {
		t.Fatalf("unsafe invalidation log: %q", logged)
	}
}

type recordingRiverClient struct {
	tx   pgx.Tx
	args river.JobArgs
	opts *river.InsertOpts
	err  error
}

func (c *recordingRiverClient) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected non-transactional insert")
}

func (c *recordingRiverClient) InsertTx(
	_ context.Context,
	tx pgx.Tx,
	args river.JobArgs,
	opts *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	c.tx, c.args, c.opts = tx, args, opts
	return &rivertype.JobInsertResult{}, c.err
}

func (*recordingRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected bulk insert")
}

type stubTx struct{}

func (*stubTx) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("unused") }
func (*stubTx) Commit(context.Context) error          { return errors.New("unused") }
func (*stubTx) Rollback(context.Context) error        { return errors.New("unused") }
func (*stubTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unused")
}
func (*stubTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (*stubTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (*stubTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unused")
}
func (*stubTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unused")
}
func (*stubTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (*stubTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (*stubTx) Conn() *pgx.Conn                                  { return nil }

func TestEnqueueInvalidationTxUsesCanonicalCriticalEnvelope(t *testing.T) {
	client := &recordingRiverClient{}
	dispatcher := supportjobs.NewDispatcher(client)
	tx := &stubTx{}
	if _, err := EnqueueInvalidationTx(context.Background(), dispatcher, tx, " OWNER.PLUGIN ", []string{
		"owner.plugin.topics", "owner.plugin.members",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	args, ok := client.args.(InvalidateResultCacheArgs)
	if !ok || client.tx != tx || !args.valid() || client.opts.Queue != supportjobs.QueueCritical ||
		client.opts.MaxAttempts != 10 || client.opts.UniqueOpts.ByArgs {
		t.Fatalf("tx=%#v args=%#v opts=%#v", client.tx, client.args, client.opts)
	}

	expected := errors.New("insert failed")
	client.err = expected
	if _, err := EnqueueInvalidationTx(context.Background(), dispatcher, tx, "owner.plugin", []string{"owner.plugin.topics"}); !errors.Is(err, expected) {
		t.Fatalf("insert err=%v", err)
	}
}

func TestEnqueueInvalidationTxRejectsMissingAuthority(t *testing.T) {
	if _, err := EnqueueInvalidationTx(context.Background(), nil, &stubTx{}, "owner.plugin", []string{"owner.plugin.tag"}); err == nil {
		t.Fatal("expected nil dispatcher rejection")
	}
	if _, err := EnqueueInvalidationTx(context.Background(), supportjobs.NewDispatcher(&recordingRiverClient{}), nil, "owner.plugin", []string{"owner.plugin.tag"}); err == nil {
		t.Fatal("expected nil transaction rejection")
	}
}

func TestRegisterInvalidateResultCacheWorker(t *testing.T) {
	registry := supportjobs.NewRegistry()
	Register(registry, &recordingInvalidator{}, nil)
	workers, err := registry.Build()
	if err != nil || workers == nil || registry.IsEmpty() {
		t.Fatalf("workers=%#v err=%v empty=%t", workers, err, registry.IsEmpty())
	}
}

func TestRegisterInvalidateResultCacheWorkerRejectsDuplicateKind(t *testing.T) {
	registry := supportjobs.NewRegistry()
	Register(registry, nil, nil)
	Register(registry, nil, nil)
	if _, err := registry.Build(); err == nil || !strings.Contains(err.Error(), InvalidateResultCacheKind) {
		t.Fatalf("duplicate registration err=%v", err)
	}
}

func TestQueryInvalidationSnoozeIsBounded(t *testing.T) {
	if queryInvalidationSnooze < time.Second || queryInvalidationSnooze > 5*time.Minute {
		t.Fatalf("snooze=%s", queryInvalidationSnooze)
	}
}
