package hostapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	"google.golang.org/protobuf/proto"
)

type commandInvalidationRiverClient struct {
	mu    sync.Mutex
	calls int
	tx    pgx.Tx
	args  river.JobArgs
	opts  *river.InsertOpts
	err   error
}

const commandInvalidationJobTestKey = "query-invalidation-job"

func (*commandInvalidationRiverClient) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected non-transactional insert")
}

func (c *commandInvalidationRiverClient) InsertTx(
	ctx context.Context,
	tx pgx.Tx,
	args river.JobArgs,
	opts *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.tx, c.args, c.opts = tx, args, opts
	if c.err != nil {
		return nil, c.err
	}
	// 模拟 River 使用同一 pgx.Tx 暂存 job，后续 audit/receipt/commit 失败时
	// fake transaction 必须像 PostgreSQL 一样同时丢弃业务写入与 job。
	if _, err := tx.Exec(ctx, "test.write", commandInvalidationJobTestKey, "enqueued"); err != nil {
		return nil, err
	}
	return &rivertype.JobInsertResult{}, nil
}

func (*commandInvalidationRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected bulk insert")
}

func (c *commandInvalidationRiverClient) snapshot() (int, pgx.Tx, river.JobArgs, *river.InsertOpts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.tx, c.args, c.opts
}

func newCommandInvalidationTestEngine(
	t *testing.T,
	backend *fakeProtocolV2CommandBackend,
	client *commandInvalidationRiverClient,
) *protocolV2CommandEngine {
	t.Helper()
	definition := testProtocolV2CommandDefinition(t, func(
		ctx context.Context,
		tx pgx.Tx,
		_ *hostv2.CommandRequest,
		_ *protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		if _, err := tx.Exec(ctx, "test.write", "query-invalidation", "committed"); err != nil {
			return nil, err
		}
		return testProtocolV2CommandExecution(t), nil
	})
	engine, err := newProtocolV2CommandEngineWithInvalidationJobs(
		backend, nil, supportjobs.NewDispatcher(client), definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestProtocolV2CommandQueryInvalidationEnqueuesOnceAndReplaysReceipt(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	client := &commandInvalidationRiverClient{}
	engine := newCommandInvalidationTestEngine(t, backend, client)
	request := testProtocolV2CommandRequest(t, "query-invalidation-1", "value")
	request.QueryInvalidationTags = []string{"demo.plugin.members", "demo.plugin.topics"}

	result, err := engine.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	calls, tx, rawArgs, opts := client.snapshot()
	args, ok := rawArgs.(queryregistryjobs.InvalidateResultCacheArgs)
	if calls != 1 || !ok || tx == nil || args.OwnerExtensionID != "demo.plugin" ||
		!slices.Equal(args.Tags, request.GetQueryInvalidationTags()) ||
		opts == nil || opts.Queue != supportjobs.QueueCritical || opts.UniqueOpts.ByArgs {
		t.Fatalf("calls=%d tx=%#v args=%#v opts=%#v", calls, tx, rawArgs, opts)
	}
	if got := backend.value(commandInvalidationJobTestKey); got != "enqueued" {
		t.Fatalf("committed invalidation job=%q", got)
	}

	replay, err := engine.execute(context.Background(), request)
	if err != nil || replay.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if calls, _, _, _ := client.snapshot(); calls != 1 {
		t.Fatalf("replay enqueued %d invalidations", calls)
	}

	changed := proto.Clone(request).(*hostv2.CommandRequest)
	changed.QueryInvalidationTags = []string{"demo.plugin.other"}
	conflict, err := engine.execute(context.Background(), changed)
	if err != nil || conflict.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		conflict.GetError().GetReason() != "host.command_idempotency_conflict" {
		t.Fatalf("conflict=%#v err=%v", conflict, err)
	}
	if calls, _, _, _ := client.snapshot(); calls != 1 {
		t.Fatalf("conflict enqueued %d invalidations", calls)
	}
}

func TestProtocolV2CommandQueryInvalidationInsertFailureRollsBackDomainWrite(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	client := &commandInvalidationRiverClient{err: errors.New("river unavailable")}
	engine := newCommandInvalidationTestEngine(t, backend, client)
	request := testProtocolV2CommandRequest(t, "query-invalidation-rollback", "value")
	request.QueryInvalidationTags = []string{"demo.plugin.topics"}

	result, err := engine.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		result.GetError().GetReason() != "host.command_rolled_back" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if backend.commitCount() != 0 || backend.rollbackCount() != 1 || backend.receiptCount() != 0 ||
		backend.value("query-invalidation") != "" || backend.value(commandInvalidationJobTestKey) != "" {
		t.Fatalf("commits=%d rollbacks=%d receipts=%d value=%q job=%q",
			backend.commitCount(), backend.rollbackCount(), backend.receiptCount(),
			backend.value("query-invalidation"), backend.value(commandInvalidationJobTestKey))
	}
}

func TestProtocolV2CommandQueryInvalidationValidatesOutputBeforeEnqueue(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	client := &commandInvalidationRiverClient{}
	definition := testProtocolV2CommandDefinition(t, func(
		ctx context.Context,
		tx pgx.Tx,
		_ *hostv2.CommandRequest,
		_ *protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		if _, err := tx.Exec(ctx, "test.write", "query-invalidation", "leaked"); err != nil {
			return nil, err
		}
		result := testProtocolV2CommandExecution(t)
		result.Output.SchemaId = "sforum.test.wrong-output"
		return result, nil
	})
	engine, err := newProtocolV2CommandEngineWithInvalidationJobs(
		backend, nil, supportjobs.NewDispatcher(client), definition,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := testProtocolV2CommandRequest(t, "query-invalidation-output", "value")
	request.QueryInvalidationTags = []string{"demo.plugin.topics"}

	result, err := engine.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		result.GetError().GetReason() != "host.command_schema_mismatch" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if calls, _, _, _ := client.snapshot(); calls != 0 {
		t.Fatalf("invalid output enqueued %d jobs", calls)
	}
	if backend.value("query-invalidation") != "" || backend.receiptCount() != 0 ||
		len(backend.auditSnapshot()) != 0 || backend.rollbackCount() != 1 {
		t.Fatalf("invalid output leaked state: value=%q receipts=%d audits=%d rollbacks=%d",
			backend.value("query-invalidation"), backend.receiptCount(),
			len(backend.auditSnapshot()), backend.rollbackCount())
	}
}

func TestProtocolV2CommandQueryInvalidationPostEnqueueFailuresRollbackJob(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeProtocolV2CommandBackend)
		reason    string
	}{
		{
			name: "audit",
			configure: func(backend *fakeProtocolV2CommandBackend) {
				backend.auditErr = errors.New("audit unavailable")
			},
			reason: "host.command_rolled_back",
		},
		{
			name: "receipt",
			configure: func(backend *fakeProtocolV2CommandBackend) {
				backend.saveErr = errors.New("receipt unavailable")
			},
			reason: "host.command_rolled_back",
		},
		{
			name: "commit rollback",
			configure: func(backend *fakeProtocolV2CommandBackend) {
				backend.commitErr = pgx.ErrTxCommitRollback
			},
			reason: "host.command_commit_rolled_back",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2CommandBackend()
			test.configure(backend)
			client := &commandInvalidationRiverClient{}
			engine := newCommandInvalidationTestEngine(t, backend, client)
			request := testProtocolV2CommandRequest(t, "query-invalidation-post-enqueue", "value")
			request.QueryInvalidationTags = []string{"demo.plugin.topics"}

			result, err := engine.execute(context.Background(), request)
			if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
				result.GetError().GetReason() != test.reason {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if calls, tx, _, _ := client.snapshot(); calls != 1 || tx == nil {
				t.Fatalf("enqueue calls=%d tx=%#v", calls, tx)
			}
			if backend.value("query-invalidation") != "" || backend.value(commandInvalidationJobTestKey) != "" ||
				backend.receiptCount() != 0 || len(backend.auditSnapshot()) != 0 || backend.commitCount() != 0 {
				t.Fatalf("failed transaction leaked state: value=%q job=%q receipts=%d audits=%d commits=%d",
					backend.value("query-invalidation"), backend.value(commandInvalidationJobTestKey),
					backend.receiptCount(), len(backend.auditSnapshot()), backend.commitCount())
			}
		})
	}
}

func TestProtocolV2CommandQueryInvalidationRejectsNonCanonicalAndUnavailableTags(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	client := &commandInvalidationRiverClient{}
	engine := newCommandInvalidationTestEngine(t, backend, client)
	overLimit := make([]string, 33)
	for index := range overLimit {
		overLimit[index] = fmt.Sprintf("demo.plugin.tag.%02d", index)
	}
	tests := [][]string{
		{"DEMO.PLUGIN.TOPICS"},
		{"other.plugin.topics"},
		{"demo.plugin.topics", "demo.plugin.topics"},
		{"demo.plugin.topics", "demo.plugin.members"},
		overLimit,
	}
	for index, tags := range tests {
		request := testProtocolV2CommandRequest(t, "query-invalidation-invalid", "value")
		request.QueryInvalidationTags = tags
		result, err := engine.execute(context.Background(), request)
		if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED ||
			result.GetError().GetReason() != "host.command_query_invalidation_invalid" {
			t.Fatalf("case %d result=%#v err=%v", index, result, err)
		}
	}
	if backend.beginCount() != 0 {
		t.Fatalf("invalid tags began %d transactions", backend.beginCount())
	}
	if calls, _, _, _ := client.snapshot(); calls != 0 {
		t.Fatalf("invalid tags enqueued %d jobs", calls)
	}

	withoutJobs := newTestProtocolV2CommandEngine(t, backend, func(
		context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	request := testProtocolV2CommandRequest(t, "query-invalidation-unavailable", "value")
	request.QueryInvalidationTags = []string{"demo.plugin.topics"}
	result, err := withoutJobs.execute(context.Background(), request)
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED ||
		result.GetError().GetReason() != "host.command_query_invalidation_unavailable" {
		t.Fatalf("unavailable result=%#v err=%v", result, err)
	}
}

func TestProtocolV2CommandFingerprintBindsQueryInvalidationTags(t *testing.T) {
	request := testProtocolV2CommandRequest(t, "query-invalidation-fingerprint", "value")
	request.QueryInvalidationTags = []string{"demo.plugin.topics", "demo.plugin.members"}
	first, err := protocolV2CommandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	request.QueryInvalidationTags = []string{"demo.plugin.members", "demo.plugin.topics"}
	reordered, err := protocolV2CommandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if first != reordered {
		t.Fatal("query invalidation tag order changed command fingerprint")
	}
	request.QueryInvalidationTags = []string{"demo.plugin.members"}
	second, err := protocolV2CommandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("query invalidation tags did not change command fingerprint")
	}
}
