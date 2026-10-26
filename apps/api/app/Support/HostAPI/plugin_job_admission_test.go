package hostapi

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type testPluginJobAdmission struct {
	identity PluginJobEnqueueIdentity
	lease    PluginJobEnqueueLease
	err      error
	calls    int
}

func (a *testPluginJobAdmission) AcquirePluginJobEnqueue(_ context.Context, identity PluginJobEnqueueIdentity) (PluginJobEnqueueLease, error) {
	a.calls++
	a.identity = identity
	if a.err != nil {
		return nil, a.err
	}
	if a.lease == nil {
		a.lease = &testPluginJobLease{ctx: context.Background()}
	}
	return a.lease, nil
}

type testPluginJobLease struct {
	ctx      context.Context
	released bool
}

func (l *testPluginJobLease) Context() context.Context { return l.ctx }
func (l *testPluginJobLease) Release()                 { l.released = true }

type pluginJobLeaseContextKey struct{}

func TestProtocolV2JobEnqueueHoldsExactRuntimeAdmissionThroughQueueWrite(t *testing.T) {
	leaseContext := context.WithValue(context.Background(), pluginJobLeaseContextKey{}, "lease")
	lease := &testPluginJobLease{ctx: leaseContext}
	admission := &testPluginJobAdmission{lease: lease}
	jobs := &fakeJobs{}
	server := newProtocolV2JobTestServer(jobs, admission)

	response, err := server.Enqueue(context.Background(), protocolV2JobTestRequest(testProtocolV2RequestContext()))
	if err != nil || response.GetError() != nil {
		t.Fatalf("enqueue = %#v, %v", response, err)
	}
	wantIdentity := PluginJobEnqueueIdentity{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact", InstanceID: "instance",
	}
	if admission.calls != 1 || admission.identity != wantIdentity {
		t.Fatalf("admission calls=%d identity=%#v", admission.calls, admission.identity)
	}
	if jobs.ctx.Value(pluginJobLeaseContextKey{}) != "lease" || !lease.released {
		t.Fatalf("queue context=%#v released=%v", jobs.ctx, lease.released)
	}
	if jobs.contract.ExtensionVersion != "1.0.0" || jobs.contract.ArtifactDigest != "artifact" ||
		jobs.contract.JobContract != "demo.plugin.job.sync@1" || jobs.contract.PayloadSchemaID != "demo.sync.payload" {
		t.Fatalf("persisted contract = %#v", jobs.contract)
	}
}

func TestProtocolV2JobEnqueueRejectsAdmissionBeforeQueueWrite(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		admission *testPluginJobAdmission
		reason    string
	}{
		{name: "stale", admission: &testPluginJobAdmission{err: ErrPluginJobEnqueueStale}, reason: "host.job_runtime_stale"},
		{name: "draining", admission: &testPluginJobAdmission{err: ErrPluginJobEnqueueDraining}, reason: "host.job_runtime_draining"},
		{name: "lease canceled", admission: &testPluginJobAdmission{lease: &testPluginJobLease{ctx: canceled}}, reason: "host.job_enqueue_cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeJobs{}
			response, err := newProtocolV2JobTestServer(jobs, tt.admission).Enqueue(
				context.Background(), protocolV2JobTestRequest(testProtocolV2RequestContext()),
			)
			if err != nil || response.GetError().GetReason() != tt.reason {
				t.Fatalf("enqueue = %#v, %v", response, err)
			}
			wantCode := protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
			if tt.name == "lease canceled" {
				wantCode = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
			}
			if response.GetError().GetCode() != wantCode {
				t.Fatalf("error code = %s", response.GetError().GetCode())
			}
			if jobs.lastKind != "" {
				t.Fatalf("queue write reached for %s", tt.name)
			}
		})
	}
}

func TestProtocolV2JobEnqueueFailsClosedWithoutAdmission(t *testing.T) {
	jobs := &fakeJobs{}
	response, err := newProtocolV2JobTestServer(jobs, nil).Enqueue(
		context.Background(), protocolV2JobTestRequest(testProtocolV2RequestContext()),
	)
	if err != nil || response.GetError().GetReason() != "host.job_admission_unavailable" || jobs.lastKind != "" {
		t.Fatalf("enqueue = %#v jobs=%#v err=%v", response, jobs, err)
	}
}

func newProtocolV2JobTestServer(jobs *fakeJobs, admission PluginJobEnqueueAdmission) *protocolV2JobServer {
	service := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.JobsEnqueue}), jobs: []string{"demo.sync"}},
		Jobs:         jobs, JobAdmission: admission,
	})
	return &protocolV2JobServer{core: &protocolV2Core{service: service}}
}

func protocolV2JobTestRequest(requestContext *protocolv2.RequestContext) *hostv2.JobEnqueueRequest {
	return &hostv2.JobEnqueueRequest{
		Context: requestContext, JobKind: "demo.sync", PayloadVersion: "1",
		Payload: &protocolv2.TypedDocument{SchemaId: "demo.sync.payload", SchemaVersion: "1"},
	}
}

type recordingPluginJobRiverClient struct {
	ctx  context.Context
	args river.JobArgs
	opts *river.InsertOpts
}

func (c *recordingPluginJobRiverClient) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c.ctx = ctx
	c.args = args
	c.opts = opts
	return &rivertype.JobInsertResult{}, nil
}

func (*recordingPluginJobRiverClient) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected InsertTx")
}

func (*recordingPluginJobRiverClient) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, errors.New("unexpected InsertMany")
}

func TestRiverJobEnqueuerForwardsLeaseContextAndExactEnvelope(t *testing.T) {
	client := &recordingPluginJobRiverClient{}
	enqueuer := &RiverJobEnqueuer{Dispatcher: supportjobs.NewDispatcher(client)}
	ctx := context.WithValue(context.Background(), pluginJobLeaseContextKey{}, "river-lease")
	contract := supportjobs.PluginJobContract{
		ExtensionID: "demo.plugin", ExtensionVersion: "2.0.0", ArtifactDigest: "sha256:exact",
		JobName: "demo.sync", JobContract: "demo.plugin.job.sync@2",
		PayloadSchemaID: "demo.sync.payload", PayloadSchemaVersion: "2",
		RetryPolicy: supportjobs.PluginJobRetryBounded, MaxAttempts: 7,
		RetryDelaySeconds: 45, ConcurrencyLimit: 2,
	}
	if err := enqueuer.EnqueueVersionedPluginJob(ctx, contract, "grant-2", map[string]any{"page": 2}); err != nil {
		t.Fatal(err)
	}
	args, ok := client.args.(PluginJobArgs)
	if !ok || !args.Contract().Equal(contract) || args.TrustGrantID != "grant-2" {
		t.Fatalf("args = %#v", client.args)
	}
	if client.opts == nil || client.opts.MaxAttempts != 7 || args.InsertOpts().MaxAttempts != 7 {
		t.Fatalf("insert opts = %#v args opts = %#v", client.opts, args.InsertOpts())
	}
	if client.ctx.Value(pluginJobLeaseContextKey{}) != "river-lease" {
		t.Fatalf("dispatcher context = %#v", client.ctx)
	}
}
