package hostapi

import (
	"testing"

	"github.com/riverqueue/river"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestPostgresPluginJobLifecycleExpectedPlanRecoversCommittedMigration(t *testing.T) {
	harness := newPluginJobLifecycleIntegrationHarness(t)
	source := integrationPluginJobRuntime(
		lifecycleTestExtensionID, "digest", "1.0.0", "sha256:v1", "1", "grant-v1",
	)
	target := integrationPluginJobRuntime(
		lifecycleTestExtensionID, "digest", "2.0.0", "sha256:v2", "2", "grant-v2",
	)
	migrator := lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
		input.Payload["migrated"] = true
		return input.Payload, nil
	})
	migration, input := integrationPluginJobMigrationInput(source, target, migrator)
	old := harness.insertJob(t, integrationPluginJobArgs(source, map[string]any{"value": "old"}), &river.InsertOpts{Queue: "default"})
	expected := PluginJobLifecycleExpectedPlan{
		ExtensionID: lifecycleTestExtensionID,
		Entries: []PluginJobLifecycleExpectedDecision{{
			JobID: old.ID, Action: supportjobs.PluginJobMigrate,
			Reason: supportjobs.PluginJobReasonMigrationDeclared, MigrationID: migration.ID,
		}},
	}
	coordinator := &PluginJobLifecycleCoordinator{Store: harness.store}
	first, err := coordinator.ReconcileExpected(harness.ctx, input, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Committed || len(first.Executions) != 1 || first.Executions[0].ReplacementJobID <= 0 {
		t.Fatalf("first result = %#v", first)
	}
	replacementID := first.Executions[0].ReplacementJobID

	// The River transaction committed, but the lifecycle evidence write is
	// assumed lost. Replaying the same expected plan must prove the exact ledger
	// link and replacement row without inserting or cancelling a second copy.
	recovered, err := coordinator.ReconcileExpected(harness.ctx, input, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Committed || len(recovered.Executions) != 1 ||
		recovered.Executions[0].ReplacementJobID != replacementID {
		t.Fatalf("recovered result = %#v", recovered)
	}
	if got := harness.countJobs(t, lifecycleTestExtensionID, target.Contract.ArtifactDigest); got != 1 {
		t.Fatalf("target replacement jobs = %d", got)
	}
	if got := harness.countLedger(t, old.ID); got != 1 {
		t.Fatalf("migration ledger rows = %d", got)
	}
}
