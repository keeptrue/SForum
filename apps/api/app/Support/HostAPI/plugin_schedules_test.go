package hostapi

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type pluginScheduleJobEnqueuerStub struct {
	contract supportjobs.PluginJobContract
	grant    string
	ctx      context.Context
	calls    int
	err      error
}

func (s *pluginScheduleJobEnqueuerStub) EnqueueVersionedPluginJob(
	ctx context.Context,
	contract supportjobs.PluginJobContract,
	grant string,
	_ map[string]any,
) error {
	s.calls++
	s.ctx = ctx
	s.contract = contract
	s.grant = grant
	return s.err
}

func TestPluginScheduleTriggerWorkerEnqueuesUnderExactAdmission(t *testing.T) {
	registry := supportjobs.NewPluginScheduleAdmissionRegistry()
	runtime := pluginScheduleWorkerRuntime()
	if _, err := registry.PublishActive(runtime); err != nil {
		t.Fatal(err)
	}
	enqueuer := &pluginScheduleJobEnqueuerStub{}
	worker := &PluginScheduleTriggerWorker{Schedules: registry, Jobs: enqueuer}
	args := supportjobs.PluginScheduleTriggerArgs{
		ExtensionID: runtime.Identity.ExtensionID, ExtensionVersion: runtime.Identity.ExtensionVersion,
		ArtifactDigest: runtime.Identity.ArtifactDigest, InstanceID: runtime.Identity.InstanceID,
		ScheduleID: runtime.Schedules[0].ScheduleID,
	}
	if err := worker.Work(context.Background(), &river.Job[supportjobs.PluginScheduleTriggerArgs]{Args: args}); err != nil {
		t.Fatal(err)
	}
	if enqueuer.calls != 1 || !enqueuer.contract.Equal(runtime.Schedules[0].Contract) ||
		enqueuer.grant != runtime.Schedules[0].TrustGrantID || enqueuer.ctx == nil {
		t.Fatalf("enqueue calls=%d contract=%#v grant=%q", enqueuer.calls, enqueuer.contract, enqueuer.grant)
	}
}

func TestPluginScheduleTriggerWorkerCancelsDrainedOrStaleRuntime(t *testing.T) {
	registry := supportjobs.NewPluginScheduleAdmissionRegistry()
	runtime := pluginScheduleWorkerRuntime()
	if _, err := registry.PublishActive(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.BeginDrain(runtime.Identity); err != nil {
		t.Fatal(err)
	}
	enqueuer := &pluginScheduleJobEnqueuerStub{}
	worker := &PluginScheduleTriggerWorker{Schedules: registry, Jobs: enqueuer}
	args := supportjobs.PluginScheduleTriggerArgs{
		ExtensionID: runtime.Identity.ExtensionID, ExtensionVersion: runtime.Identity.ExtensionVersion,
		ArtifactDigest: runtime.Identity.ArtifactDigest, InstanceID: runtime.Identity.InstanceID,
		ScheduleID: runtime.Schedules[0].ScheduleID,
	}
	err := worker.Work(context.Background(), &river.Job[supportjobs.PluginScheduleTriggerArgs]{Args: args})
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) || enqueuer.calls != 0 {
		t.Fatalf("drained trigger error=%v calls=%d", err, enqueuer.calls)
	}
}

func pluginScheduleWorkerRuntime() supportjobs.PluginScheduleRuntime {
	contract := supportjobs.PluginJobContract{
		ExtensionID: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-a",
		JobName: "demo.sync", JobContract: "demo.plugin.job.sync@1",
		PayloadSchemaID: "demo.plugin.sync.payload", PayloadSchemaVersion: "1",
	}.Normalized()
	return supportjobs.PluginScheduleRuntime{
		Identity: supportjobs.PluginScheduleRuntimeIdentity{
			ExtensionID: contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
			ArtifactDigest: contract.ArtifactDigest, InstanceID: "instance-a",
		},
		Schedules: []supportjobs.PluginScheduleDeclaration{{
			ScheduleID: "demo.plugin.schedule.sync", JobName: contract.JobName,
			JobContract: contract.JobContract, Cron: "0 3 * * *", Timezone: "UTC",
			Contract: contract, TrustGrantID: "grant-a",
		}},
	}
}
