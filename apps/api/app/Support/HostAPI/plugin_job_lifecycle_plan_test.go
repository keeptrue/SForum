package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

const lifecycleTestExtensionID = "sforum.lifecycle-test"

func TestPlanPluginJobLifecycleOwnsEveryDeclaredJobName(t *testing.T) {
	alphaSource := lifecycleTestRuntime("alpha", "1.0.0", "sha256:alpha-v1", "1", "grant-alpha-v1")
	alphaTarget := lifecycleTestRuntime("alpha", "2.0.0", "sha256:alpha-v2", "2", "grant-alpha-v2")
	betaSource := lifecycleTestRuntime("beta", "1.0.0", "sha256:beta-v1", "1", "grant-beta-v1")
	betaTarget := lifecycleTestRuntime("beta", "2.0.0", "sha256:beta-v2", "2", "grant-beta-v2")
	removedSource := lifecycleTestRuntime("removed", "1.0.0", "sha256:removed-v1", "1", "grant-removed-v1")
	unknown := lifecycleTestRuntime("unknown", "1.0.0", "sha256:unknown", "1", "grant-unknown")

	rows := []PluginJobLifecycleRow{
		lifecycleTestRow(t, 10, rivertype.JobStateAvailable, lifecycleTestArgs(alphaSource, nil)),
		lifecycleTestRow(t, 4, rivertype.JobStateRunning, lifecycleTestArgs(removedSource, nil)),
		lifecycleTestRow(t, 9, rivertype.JobStateAvailable, lifecycleTestArgsWithTrust(betaTarget, "stale-grant", nil)),
		lifecycleTestRow(t, 3, rivertype.JobStateAvailable, lifecycleTestArgs(betaTarget, nil)),
		lifecycleTestRow(t, 2, rivertype.JobStateRunning, lifecycleTestArgs(alphaSource, nil)),
		lifecycleTestRow(t, 1, rivertype.JobStateAvailable, lifecycleTestArgs(alphaTarget, nil)),
		lifecycleTestRow(t, 5, rivertype.JobStateAvailable, lifecycleTestArgs(unknown, nil)),
		{JobID: 6, Kind: PluginJobKind, State: rivertype.JobStateAvailable, EncodedArgs: json.RawMessage("{")},
		lifecycleTestRow(t, 7, rivertype.JobState("future"), lifecycleTestArgs(alphaTarget, nil)),
		lifecycleTestRow(t, 8, rivertype.JobStateCompleted, lifecycleTestArgs(alphaTarget, nil)),
	}
	legacy := lifecycleTestArgs(alphaSource, nil)
	legacy.EnvelopeVersion = 0
	rows[0] = lifecycleTestRow(t, 10, rivertype.JobStateAvailable, legacy)
	originalFirstID := rows[0].JobID

	plan, err := PlanPluginJobLifecycle(PluginJobLifecycleInput{
		ExtensionID: lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{
			"alpha": alphaSource, "beta": betaSource, "removed": removedSource,
		},
		TargetContracts:        map[string]PluginJobRuntimeContract{"alpha": alphaTarget, "beta": betaTarget},
		SourceRuntimeAvailable: true,
	}, rows)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].JobID != originalFirstID {
		t.Fatalf("planner reordered caller rows: first id = %d", rows[0].JobID)
	}
	if plan.ExtensionID != lifecycleTestExtensionID || plan.IgnoredFinalized != 1 {
		t.Fatalf("plan metadata = %#v", plan)
	}

	want := map[int64]supportjobs.PluginJobDecision{
		1:  {Action: supportjobs.PluginJobExecute, Reason: supportjobs.PluginJobReasonExactMatch},
		2:  {Action: supportjobs.PluginJobDrain, Reason: supportjobs.PluginJobReasonSourceCompatible},
		3:  {Action: supportjobs.PluginJobExecute, Reason: supportjobs.PluginJobReasonExactMatch},
		4:  {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonTargetRemoved},
		5:  {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonJobUnknown},
		6:  {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonEnvelopeInvalid},
		7:  {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonStateUnknown},
		9:  {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonTrustGrantStale},
		10: {Action: supportjobs.PluginJobCancel, Reason: supportjobs.PluginJobReasonEnvelopeInvalid},
	}
	if len(plan.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(plan.Entries), len(want))
	}
	lastID := int64(0)
	for _, entry := range plan.Entries {
		if entry.Row.JobID <= lastID {
			t.Fatalf("entries are not stably ordered: %d after %d", entry.Row.JobID, lastID)
		}
		lastID = entry.Row.JobID
		if expected, ok := want[entry.Row.JobID]; !ok || entry.Decision != expected {
			t.Fatalf("job %d decision = %#v, want %#v", entry.Row.JobID, entry.Decision, expected)
		}
	}
}

func TestPlanPluginJobLifecycleMigrationAndRunningSourcePolicy(t *testing.T) {
	source := lifecycleTestRuntime("digest", "1.0.0", "sha256:v1", "1", "grant-v1")
	target := lifecycleTestRuntime("digest", "2.0.0", "sha256:v2", "2", "grant-v2")
	migration := supportjobs.PluginJobMigration{ID: "digest-v1-v2", From: source.Contract, To: target.Contract}
	migrator := lifecycleMigratorFunc(func(_ PluginJobPayloadMigrationInput) (map[string]any, error) { return nil, nil })
	input := PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"digest": source},
		TargetContracts: map[string]PluginJobRuntimeContract{"digest": target},
		Migrations:      []supportjobs.PluginJobMigration{migration},
		Migrators:       map[string]PluginJobPayloadMigrator{migration.ID: migrator},
	}

	plan, err := PlanPluginJobLifecycle(input, []PluginJobLifecycleRow{
		lifecycleTestRow(t, 1, rivertype.JobStateAvailable, lifecycleTestArgs(source, nil)),
		lifecycleTestRow(t, 2, rivertype.JobStateRunning, lifecycleTestArgs(source, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Entries[0].Decision; got.Action != supportjobs.PluginJobMigrate || got.MigrationID != migration.ID {
		t.Fatalf("queued source decision = %#v", got)
	}
	if got := plan.Entries[1].Decision; got.Action != supportjobs.PluginJobCancel || got.Reason != supportjobs.PluginJobReasonRunningMigration {
		t.Fatalf("running source without runtime decision = %#v", got)
	}

	input.SourceRuntimeAvailable = true
	plan, err = PlanPluginJobLifecycle(input, []PluginJobLifecycleRow{
		lifecycleTestRow(t, 3, rivertype.JobStateRunning, lifecycleTestArgs(source, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Entries[0].Decision; got.Action != supportjobs.PluginJobDrain || got.Reason != supportjobs.PluginJobReasonSourceCompatible {
		t.Fatalf("running source with runtime decision = %#v", got)
	}

	input.SourceRuntimeAvailable = false
	input.Migrators = nil
	plan, err = PlanPluginJobLifecycle(input, []PluginJobLifecycleRow{
		lifecycleTestRow(t, 4, rivertype.JobStateAvailable, lifecycleTestArgs(source, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Entries[0].Decision; got.Action != supportjobs.PluginJobCancel || got.Reason != supportjobs.PluginJobReasonMigratorMissing {
		t.Fatalf("missing migrator decision = %#v", got)
	}
}

func TestPlanPluginJobLifecycleRejectsInvalidRuntimeMapsBeforePlanningRows(t *testing.T) {
	source := lifecycleTestRuntime("alpha", "1.0.0", "sha256:v1", "1", "grant-v1")
	target := lifecycleTestRuntime("alpha", "2.0.0", "sha256:v2", "2", "grant-v2")
	validInput := PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"alpha": source},
		TargetContracts: map[string]PluginJobRuntimeContract{"alpha": target},
	}
	row := lifecycleTestRow(t, 1, rivertype.JobStateAvailable, lifecycleTestArgs(source, nil))

	tests := []struct {
		name  string
		input PluginJobLifecycleInput
		want  error
	}{
		{name: "blank extension", input: func() PluginJobLifecycleInput { value := validInput; value.ExtensionID = " "; return value }(), want: ErrInvalidRequest},
		{name: "source snapshot missing", input: func() PluginJobLifecycleInput { value := validInput; value.SourceContracts = nil; return value }(), want: ErrInvalidRequest},
		{name: "target snapshot missing", input: func() PluginJobLifecycleInput { value := validInput; value.TargetContracts = nil; return value }(), want: ErrInvalidRequest},
		{name: "map key mismatch", input: func() PluginJobLifecycleInput {
			value := validInput
			value.TargetContracts = map[string]PluginJobRuntimeContract{"other": target}
			return value
		}(), want: ErrInvalidRequest},
		{name: "invalid source contract", input: func() PluginJobLifecycleInput {
			value := validInput
			broken := source
			broken.Contract.ArtifactDigest = ""
			value.SourceContracts = map[string]PluginJobRuntimeContract{"alpha": broken}
			return value
		}(), want: ErrInvalidRequest},
		{name: "foreign target", input: func() PluginJobLifecycleInput {
			value := validInput
			broken := target
			broken.Contract.ExtensionID = "other"
			value.TargetContracts = map[string]PluginJobRuntimeContract{"alpha": broken}
			return value
		}(), want: ErrPluginJobLifecycleIsolation},
		{name: "target trust missing", input: func() PluginJobLifecycleInput {
			value := validInput
			broken := target
			broken.TrustGrantID = ""
			value.TargetContracts = map[string]PluginJobRuntimeContract{"alpha": broken}
			return value
		}(), want: ErrInvalidRequest},
		{name: "available source trust missing", input: func() PluginJobLifecycleInput {
			value := validInput
			value.SourceRuntimeAvailable = true
			broken := source
			broken.TrustGrantID = ""
			value.SourceContracts = map[string]PluginJobRuntimeContract{"alpha": broken}
			return value
		}(), want: ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanPluginJobLifecycle(test.input, []PluginJobLifecycleRow{row})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if len(plan.Entries) != 0 {
				t.Fatalf("invalid runtime planned queue mutations: %#v", plan.Entries)
			}
		})
	}
}

func TestPlanPluginJobLifecycleRejectsRowsOutsideLockedScope(t *testing.T) {
	runtime := lifecycleTestRuntime("alpha", "1.0.0", "sha256:v1", "1", "grant-v1")
	input := PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"alpha": runtime},
		TargetContracts: map[string]PluginJobRuntimeContract{"alpha": runtime},
	}
	foreignRuntime := runtime
	foreignRuntime.Contract.ExtensionID = "sforum.foreign"
	foreign := lifecycleTestRow(t, 1, rivertype.JobStateAvailable, lifecycleTestArgs(foreignRuntime, nil))
	wrongKind := lifecycleTestRow(t, 2, rivertype.JobStateAvailable, lifecycleTestArgs(runtime, nil))
	wrongKind.Kind = "other.kind"

	for _, row := range []PluginJobLifecycleRow{foreign, wrongKind} {
		plan, err := PlanPluginJobLifecycle(input, []PluginJobLifecycleRow{row})
		if !errors.Is(err, ErrPluginJobLifecycleIsolation) {
			t.Fatalf("row %#v error = %v", row, err)
		}
		if len(plan.Entries) != 0 {
			t.Fatalf("out-of-scope row planned mutations: %#v", plan.Entries)
		}
	}
}

func lifecycleTestRuntime(jobName, version, digest, schemaVersion, trustGrantID string) PluginJobRuntimeContract {
	return PluginJobRuntimeContract{
		Contract: supportjobs.PluginJobContract{
			ExtensionID: lifecycleTestExtensionID, ExtensionVersion: version, ArtifactDigest: digest,
			JobName: jobName, JobContract: "1", PayloadSchemaID: "schema." + jobName,
			PayloadSchemaVersion: schemaVersion,
		},
		TrustGrantID: trustGrantID,
	}
}

func lifecycleTestArgs(runtime PluginJobRuntimeContract, payload map[string]any) PluginJobArgs {
	return lifecycleTestArgsWithTrust(runtime, runtime.TrustGrantID, payload)
}

func lifecycleTestArgsWithTrust(runtime PluginJobRuntimeContract, trustGrantID string, payload map[string]any) PluginJobArgs {
	contract := runtime.Contract
	return PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		ArtifactDigest: contract.ArtifactDigest, TrustGrantID: trustGrantID,
		JobName: contract.JobName, JobContractVersion: contract.JobContract,
		PayloadSchemaID: contract.PayloadSchemaID, PayloadSchemaVersion: contract.PayloadSchemaVersion,
		Payload: payload, EnqueuedAt: time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
	}
}

func lifecycleTestRow(t *testing.T, id int64, state rivertype.JobState, args PluginJobArgs) PluginJobLifecycleRow {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return PluginJobLifecycleRow{
		JobID: id, Kind: PluginJobKind, State: state, EncodedArgs: encoded,
		Attempt: 2, MaxAttempts: 5, Queue: "extensions", Priority: 2,
		ScheduledAt: time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC), Tags: []string{"extension:test"},
	}
}

type lifecycleMigratorFunc func(PluginJobPayloadMigrationInput) (map[string]any, error)

func (f lifecycleMigratorFunc) MigratePluginJobPayload(_ context.Context, input PluginJobPayloadMigrationInput) (map[string]any, error) {
	return f(input)
}
