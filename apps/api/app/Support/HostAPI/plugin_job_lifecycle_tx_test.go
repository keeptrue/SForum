package hostapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestPluginJobLifecycleCoordinatorMigratesByReplacementInOneTransaction(t *testing.T) {
	source := lifecycleTestRuntime("digest", "1.0.0", "sha256:v1", "1", "grant-v1")
	target := lifecycleTestRuntime("digest", "2.0.0", "sha256:v2", "2", "grant-v2")
	migration := supportjobs.PluginJobMigration{ID: "digest-v1-v2", From: source.Contract, To: target.Contract}
	row := lifecycleTestRow(t, 41, rivertype.JobStateScheduled, lifecycleTestArgs(source, map[string]any{
		"nested": map[string]any{"count": 1},
		"items":  []any{map[string]any{"name": "before"}},
	}))
	row.Queue = "plugin-maintenance"
	row.Priority = 3
	row.Tags = []string{"extension:digest", "migration:v2"}

	var migratorPayload map[string]any
	migrator := lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
		if input.MigrationID != migration.ID || !input.Source.Equal(source.Contract) || !input.Target.Equal(target.Contract) {
			t.Fatalf("migration input = %#v", input)
		}
		migratorPayload = input.Payload
		input.Payload["nested"].(map[string]any)["count"] = float64(2)
		input.Payload["items"].([]any)[0].(map[string]any)["name"] = "after"
		input.Payload["migrated"] = true
		return input.Payload, nil
	})
	tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{row}, insertJobID: 84}
	store := &lifecycleStoreStub{tx: tx}
	coordinator := &PluginJobLifecycleCoordinator{Store: store}

	result, err := coordinator.Reconcile(context.Background(), PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"digest": source},
		TargetContracts: map[string]PluginJobRuntimeContract{"digest": target},
		Migrations:      []supportjobs.PluginJobMigration{migration},
		Migrators:       map[string]PluginJobPayloadMigrator{migration.ID: migrator},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || !store.committed || store.rolledBack {
		t.Fatalf("transaction state: result=%v committed=%v rolledBack=%v", result.Committed, store.committed, store.rolledBack)
	}
	wantOperations := []string{"lock", "claim", "insert", "complete", "cancel"}
	if !reflect.DeepEqual(tx.operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", tx.operations, wantOperations)
	}
	if len(result.Executions) != 1 || result.Executions[0].ReplacementJobID != 84 || result.Executions[0].Action != supportjobs.PluginJobMigrate {
		t.Fatalf("executions = %#v", result.Executions)
	}
	if !tx.insertedArgs.Contract().Equal(target.Contract) || tx.insertedArgs.TrustGrantID != target.TrustGrantID || !tx.insertedArgs.validEnvelope() {
		t.Fatalf("replacement envelope = %#v", tx.insertedArgs)
	}
	if tx.insertedArgs.EnqueuedAt != lifecycleTestArgs(source, nil).EnqueuedAt {
		t.Fatalf("replacement enqueue time = %v", tx.insertedArgs.EnqueuedAt)
	}
	if got := tx.insertedArgs.Payload["nested"].(map[string]any)["count"]; got != float64(2) {
		t.Fatalf("replacement nested count = %#v", got)
	}
	if got := result.Plan.Entries[0].Args.Payload["nested"].(map[string]any)["count"]; got != float64(1) {
		t.Fatalf("migrator mutated immutable plan payload: %#v", got)
	}
	// Host 对 migrator 返回值再深拷贝，插件在返回后保留的引用不能污染入队信封。
	migratorPayload["nested"].(map[string]any)["count"] = float64(99)
	migratorPayload["items"].([]any)[0].(map[string]any)["name"] = "late-mutation"
	if got := tx.insertedArgs.Payload["nested"].(map[string]any)["count"]; got != float64(2) {
		t.Fatalf("late migrator mutation reached replacement: %#v", got)
	}
	if got := tx.insertedArgs.Payload["items"].([]any)[0].(map[string]any)["name"]; got != "after" {
		t.Fatalf("late nested slice mutation reached replacement: %#v", got)
	}

	if tx.insertOpts == nil || tx.insertOpts.MaxAttempts != 3 || tx.insertOpts.Queue != row.Queue ||
		tx.insertOpts.Priority != row.Priority || !tx.insertOpts.ScheduledAt.Equal(row.ScheduledAt) ||
		!reflect.DeepEqual(tx.insertOpts.Tags, row.Tags) {
		t.Fatalf("replacement insert options = %#v", tx.insertOpts)
	}
	wantLedger := PluginJobMigrationLedgerEntry{
		OldJobID: row.JobID, ExtensionID: lifecycleTestExtensionID, MigrationID: migration.ID,
		SourceContract: source.Contract, SourceTrustGrantID: source.TrustGrantID,
		TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
	}
	if !samePluginJobMigrationLedger(tx.claimedLedger, wantLedger) || !samePluginJobMigrationLedger(tx.completedLedger, wantLedger) || tx.completedJobID != 84 {
		t.Fatalf("ledger claim=%#v completion=%#v new=%d", tx.claimedLedger, tx.completedLedger, tx.completedJobID)
	}
}

func TestPluginJobLifecycleCoordinatorReusesMatchingLedgerLink(t *testing.T) {
	source, target, migration, input, row := lifecycleMigrationFixture(t)
	migratorCalls := 0
	input.Migrators[migration.ID] = lifecycleMigratorFunc(func(PluginJobPayloadMigrationInput) (map[string]any, error) {
		migratorCalls++
		return nil, nil
	})
	tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{row}}
	tx.claim = func(ledger PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error) {
		return PluginJobMigrationClaim{Claimed: false, NewJobID: 901, Ledger: ledger}, nil
	}
	store := &lifecycleStoreStub{tx: tx}

	result, err := (&PluginJobLifecycleCoordinator{Store: store}).Reconcile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tx.operations, []string{"lock", "claim", "cancel"}) {
		t.Fatalf("operations = %v", tx.operations)
	}
	if migratorCalls != 0 || tx.insertedArgs.ExtensionID != "" || tx.completedJobID != 0 {
		t.Fatalf("existing link reran migration: calls=%d insert=%#v complete=%d", migratorCalls, tx.insertedArgs, tx.completedJobID)
	}
	if len(result.Executions) != 1 || result.Executions[0].ReplacementJobID != 901 {
		t.Fatalf("executions = %#v", result.Executions)
	}
	if !tx.claimedLedger.SourceContract.Equal(source.Contract) || !tx.claimedLedger.TargetContract.Equal(target.Contract) {
		t.Fatalf("claimed ledger = %#v", tx.claimedLedger)
	}
}

func TestPluginJobLifecycleCoordinatorRejectsInconsistentLedgerClaims(t *testing.T) {
	_, target, migration, input, row := lifecycleMigrationFixture(t)
	tests := []struct {
		name  string
		claim func(PluginJobMigrationLedgerEntry) PluginJobMigrationClaim
		want  error
	}{
		{
			name: "identity mismatch",
			claim: func(ledger PluginJobMigrationLedgerEntry) PluginJobMigrationClaim {
				ledger.TargetContract = target.Contract
				ledger.TargetContract.ArtifactDigest = "sha256:other"
				return PluginJobMigrationClaim{NewJobID: 88, Ledger: ledger}
			},
			want: ErrPluginJobMigrationConflict,
		},
		{
			name: "authority mismatch",
			claim: func(ledger PluginJobMigrationLedgerEntry) PluginJobMigrationClaim {
				ledger.TargetTrustGrantID = "other-grant"
				return PluginJobMigrationClaim{NewJobID: 88, Ledger: ledger}
			},
			want: ErrPluginJobMigrationConflict,
		},
		{
			name: "existing claim not linked",
			claim: func(ledger PluginJobMigrationLedgerEntry) PluginJobMigrationClaim {
				return PluginJobMigrationClaim{Ledger: ledger}
			},
			want: ErrPluginJobMigrationPending,
		},
		{
			name: "new claim already linked",
			claim: func(ledger PluginJobMigrationLedgerEntry) PluginJobMigrationClaim {
				return PluginJobMigrationClaim{Claimed: true, NewJobID: 88, Ledger: ledger}
			},
			want: ErrPluginJobMigrationConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{row}}
			tx.claim = func(ledger PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error) {
				return test.claim(ledger), nil
			}
			store := &lifecycleStoreStub{tx: tx}
			result, err := (&PluginJobLifecycleCoordinator{Store: store}).Reconcile(context.Background(), input)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if result.Committed || store.committed || !store.rolledBack {
				t.Fatalf("ledger conflict committed: result=%v store=%v rollback=%v", result.Committed, store.committed, store.rolledBack)
			}
			if !reflect.DeepEqual(tx.operations, []string{"lock", "claim"}) {
				t.Fatalf("operations after conflict = %v", tx.operations)
			}
		})
	}
	_ = migration
}

func TestPluginJobLifecycleCoordinatorRollsBackEveryReplacementBoundary(t *testing.T) {
	_, _, _, input, row := lifecycleMigrationFixture(t)
	failure := errors.New("injected transaction failure")
	for _, boundary := range []string{"claim", "insert", "complete", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			tx := &lifecycleTxStub{rows: []PluginJobLifecycleRow{row}, insertJobID: 84, failAt: boundary, failure: failure}
			store := &lifecycleStoreStub{tx: tx}
			result, err := (&PluginJobLifecycleCoordinator{Store: store}).Reconcile(context.Background(), input)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if result.Committed || store.committed || !store.rolledBack {
				t.Fatalf("boundary %s committed: result=%v store=%v rollback=%v", boundary, result.Committed, store.committed, store.rolledBack)
			}
			if tx.operationCount(boundary) != 1 {
				t.Fatalf("boundary %s operations = %v", boundary, tx.operations)
			}
		})
	}
}

func TestPluginJobLifecycleCoordinatorRollsBackCancelBatchAtomically(t *testing.T) {
	runtime := lifecycleTestRuntime("removed", "1.0.0", "sha256:v1", "1", "grant-v1")
	rows := []PluginJobLifecycleRow{
		lifecycleTestRow(t, 1, rivertype.JobStateAvailable, lifecycleTestArgs(runtime, nil)),
		lifecycleTestRow(t, 2, rivertype.JobStateAvailable, lifecycleTestArgs(runtime, nil)),
	}
	tx := &lifecycleTxStub{rows: rows, failCancelJobID: 2, failure: errors.New("cancel failed")}
	store := &lifecycleStoreStub{tx: tx}
	result, err := (&PluginJobLifecycleCoordinator{Store: store}).Reconcile(context.Background(), PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"removed": runtime},
		TargetContracts: map[string]PluginJobRuntimeContract{},
	})
	if err == nil || result.Committed || store.committed || !store.rolledBack {
		t.Fatalf("cancel batch result=%#v err=%v store=%#v", result, err, store)
	}
	if !reflect.DeepEqual(tx.cancelledJobIDs, []int64{1, 2}) {
		t.Fatalf("cancel attempts = %v", tx.cancelledJobIDs)
	}
}

func lifecycleMigrationFixture(t *testing.T) (
	PluginJobRuntimeContract,
	PluginJobRuntimeContract,
	supportjobs.PluginJobMigration,
	PluginJobLifecycleInput,
	PluginJobLifecycleRow,
) {
	t.Helper()
	source := lifecycleTestRuntime("digest", "1.0.0", "sha256:v1", "1", "grant-v1")
	target := lifecycleTestRuntime("digest", "2.0.0", "sha256:v2", "2", "grant-v2")
	migration := supportjobs.PluginJobMigration{ID: "digest-v1-v2", From: source.Contract, To: target.Contract}
	input := PluginJobLifecycleInput{
		ExtensionID:     lifecycleTestExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{"digest": source},
		TargetContracts: map[string]PluginJobRuntimeContract{"digest": target},
		Migrations:      []supportjobs.PluginJobMigration{migration},
		Migrators: map[string]PluginJobPayloadMigrator{
			migration.ID: lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) { return input.Payload, nil }),
		},
	}
	return source, target, migration, input, lifecycleTestRow(t, 41, rivertype.JobStateAvailable, lifecycleTestArgs(source, map[string]any{"value": "old"}))
}

type lifecycleStoreStub struct {
	tx         *lifecycleTxStub
	committed  bool
	rolledBack bool
}

func (s *lifecycleStoreStub) WithPluginJobLifecycleTx(ctx context.Context, fn func(PluginJobLifecycleTx) error) error {
	if err := fn(s.tx); err != nil {
		s.rolledBack = true
		return err
	}
	s.committed = true
	return nil
}

type lifecycleTxStub struct {
	rows            []PluginJobLifecycleRow
	operations      []string
	claim           func(PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error)
	insertJobID     int64
	failAt          string
	failCancelJobID int64
	failure         error
	claimedLedger   PluginJobMigrationLedgerEntry
	completedLedger PluginJobMigrationLedgerEntry
	completedJobID  int64
	insertedArgs    PluginJobArgs
	insertOpts      *river.InsertOpts
	cancelledJobIDs []int64
}

func (tx *lifecycleTxStub) LockPluginJobs(_ context.Context, extensionID string) ([]PluginJobLifecycleRow, error) {
	tx.operations = append(tx.operations, "lock")
	if extensionID != lifecycleTestExtensionID {
		return nil, fmt.Errorf("unexpected extension scope %q", extensionID)
	}
	return append([]PluginJobLifecycleRow(nil), tx.rows...), nil
}

func (tx *lifecycleTxStub) ClaimPluginJobMigration(_ context.Context, ledger PluginJobMigrationLedgerEntry) (PluginJobMigrationClaim, error) {
	tx.operations = append(tx.operations, "claim")
	tx.claimedLedger = ledger
	if tx.failAt == "claim" {
		return PluginJobMigrationClaim{}, tx.failure
	}
	if tx.claim != nil {
		return tx.claim(ledger)
	}
	return PluginJobMigrationClaim{Claimed: true, Ledger: ledger}, nil
}

func (tx *lifecycleTxStub) InsertPluginJob(_ context.Context, args PluginJobArgs, opts *river.InsertOpts) (int64, error) {
	tx.operations = append(tx.operations, "insert")
	tx.insertedArgs = args
	tx.insertOpts = opts
	if tx.failAt == "insert" {
		return 0, tx.failure
	}
	return tx.insertJobID, nil
}

func (tx *lifecycleTxStub) CompletePluginJobMigration(_ context.Context, ledger PluginJobMigrationLedgerEntry, newJobID int64) error {
	tx.operations = append(tx.operations, "complete")
	tx.completedLedger = ledger
	tx.completedJobID = newJobID
	if tx.failAt == "complete" {
		return tx.failure
	}
	return nil
}

func (tx *lifecycleTxStub) CancelPluginJob(_ context.Context, jobID int64) error {
	tx.operations = append(tx.operations, "cancel")
	tx.cancelledJobIDs = append(tx.cancelledJobIDs, jobID)
	if tx.failAt == "cancel" || tx.failCancelJobID == jobID {
		return tx.failure
	}
	return nil
}

func (tx *lifecycleTxStub) operationCount(operation string) int {
	count := 0
	for _, current := range tx.operations {
		if current == operation {
			count++
		}
	}
	return count
}
