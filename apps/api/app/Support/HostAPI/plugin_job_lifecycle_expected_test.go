package hostapi

import (
	"errors"
	"testing"

	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestPluginJobLifecycleExpectedPlanRejectsRowDriftBeforeMutation(t *testing.T) {
	_, target, migration, input, sourceRow := lifecycleMigrationFixture(t)
	targetRow := lifecycleTestRow(t, 42, rivertype.JobStateAvailable, lifecycleTestArgs(target, nil))
	tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{sourceRow, targetRow}, insertJobID: 84}
	store := &lifecycleStoreStub{tx: tx}
	coordinator := &PluginJobLifecycleCoordinator{Store: store}

	_, err := coordinator.ReconcileExpected(t.Context(), input, PluginJobLifecycleExpectedPlan{
		ExtensionID: lifecycleTestExtensionID,
		Entries: []PluginJobLifecycleExpectedDecision{{
			JobID: sourceRow.JobID, Action: supportjobs.PluginJobMigrate,
			Reason: supportjobs.PluginJobReasonMigrationDeclared, MigrationID: migration.ID,
		}},
	})
	if !errors.Is(err, ErrPluginJobLifecyclePlanDrift) {
		t.Fatalf("error = %v", err)
	}
	if !store.rolledBack || store.committed {
		t.Fatalf("store state = committed:%v rolledBack:%v", store.committed, store.rolledBack)
	}
	if tx.operationCount("claim") != 0 || tx.operationCount("insert") != 0 || tx.operationCount("cancel") != 0 {
		t.Fatalf("drift mutated queue: %v", tx.operations)
	}
}

func TestPluginJobLifecycleExpectedPlanProvesCommittedCancellation(t *testing.T) {
	runtime := lifecycleTestRuntime("removed", "1.0.0", "sha256:v1", "1", "grant-v1")
	row := lifecycleTestRow(t, 51, rivertype.JobStateCancelled, lifecycleTestArgs(runtime, nil))
	tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{row}}
	store := &lifecycleStoreStub{tx: tx}
	result, err := (&PluginJobLifecycleCoordinator{Store: store}).ReconcileExpected(
		t.Context(),
		PluginJobLifecycleInput{
			ExtensionID:     lifecycleTestExtensionID,
			SourceContracts: map[string]PluginJobRuntimeContract{"removed": runtime},
			TargetContracts: map[string]PluginJobRuntimeContract{},
		},
		PluginJobLifecycleExpectedPlan{
			ExtensionID: lifecycleTestExtensionID,
			Entries: []PluginJobLifecycleExpectedDecision{{
				JobID: row.JobID, Action: supportjobs.PluginJobCancel,
				Reason: supportjobs.PluginJobReasonTargetRemoved,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || len(result.Executions) != 1 || result.Executions[0].Action != supportjobs.PluginJobCancel {
		t.Fatalf("result = %#v", result)
	}
	if tx.operationCount("cancel") != 0 {
		t.Fatalf("recovery replayed cancellation: %v", tx.operations)
	}
}

func TestPluginJobLifecycleExpectedPlanProvesCommittedMigration(t *testing.T) {
	source, target, migration, input, sourceRow := lifecycleMigrationFixture(t)
	sourceRow.State = rivertype.JobStateCancelled
	replacement := lifecycleTestRow(t, 84, rivertype.JobStateAvailable, lifecycleTestArgs(target, map[string]any{"value": "new"}))
	ledger := PluginJobMigrationLedgerEntry{
		OldJobID: sourceRow.JobID, ExtensionID: lifecycleTestExtensionID, MigrationID: migration.ID,
		SourceContract: source.Contract, SourceTrustGrantID: source.TrustGrantID,
		TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
	}
	tx := &lifecycleTxStub{
		rows: []PluginJobLifecycleRow{sourceRow, replacement},
		claim: func(got PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error) {
			return PluginJobMigrationClaim{Claimed: false, NewJobID: replacement.JobID, Ledger: ledger}, nil
		},
	}
	result, err := (&PluginJobLifecycleCoordinator{Store: &lifecycleStoreStub{tx: tx}}).ReconcileExpected(
		t.Context(), input,
		PluginJobLifecycleExpectedPlan{
			ExtensionID: lifecycleTestExtensionID,
			Entries: []PluginJobLifecycleExpectedDecision{{
				JobID: sourceRow.JobID, Action: supportjobs.PluginJobMigrate,
				Reason: supportjobs.PluginJobReasonMigrationDeclared, MigrationID: migration.ID,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || len(result.Executions) != 1 ||
		result.Executions[0].ReplacementJobID != replacement.JobID {
		t.Fatalf("result = %#v", result)
	}
	if tx.operationCount("claim") != 1 || tx.operationCount("insert") != 0 || tx.operationCount("cancel") != 0 {
		t.Fatalf("migration recovery operations = %v", tx.operations)
	}
}
