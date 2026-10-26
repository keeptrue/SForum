package hostapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestPostgresPluginJobLifecycleReplacementLedgerAndCancelAreAtomic(t *testing.T) {
	harness := newPluginJobLifecycleIntegrationHarness(t)
	extensionID := "sforum.lifecycle.atomic"
	source := integrationPluginJobRuntime(extensionID, "digest", "1.0.0", "sha256:atomic-v1", "1", "grant-atomic-v1")
	target := integrationPluginJobRuntime(extensionID, "digest", "2.0.0", "sha256:atomic-v2", "2", "grant-atomic-v2")
	migratorCalls := 0
	migrator := lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
		migratorCalls++
		return map[string]any{"migrated": true, "source": input.Payload["value"]}, nil
	})
	migration, input := integrationPluginJobMigrationInput(source, target, migrator)
	scheduledAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	tags := []string{"plugin-atomic", "schema-v1"}
	sourceRow := harness.insertJob(t, integrationPluginJobArgs(source, map[string]any{"value": "original"}), &river.InsertOpts{
		MaxAttempts: 7, Queue: "plugin-maintenance", Priority: 3,
		ScheduledAt: scheduledAt, Tags: tags,
	})
	if sourceRow.State != rivertype.JobStateScheduled {
		t.Fatalf("source state = %s, want scheduled", sourceRow.State)
	}

	result, err := (&PluginJobLifecycleCoordinator{Store: harness.store}).Reconcile(harness.ctx, input)
	if err != nil {
		t.Fatalf("reconcile plugin jobs: %v", err)
	}
	if !result.Committed || len(result.Executions) != 1 || result.Executions[0].Action != supportjobs.PluginJobMigrate {
		t.Fatalf("reconcile result = %#v", result)
	}
	replacementID := result.Executions[0].ReplacementJobID
	if replacementID <= 0 || migratorCalls != 1 {
		t.Fatalf("replacement=%d migrator calls=%d", replacementID, migratorCalls)
	}

	if old := harness.job(t, sourceRow.ID); old.State != rivertype.JobStateCancelled {
		t.Fatalf("source state after commit = %s", old.State)
	}
	replacement := harness.job(t, replacementID)
	if replacement.State != rivertype.JobStateScheduled || replacement.Attempt != 0 || replacement.MaxAttempts != 7 {
		t.Fatalf("replacement state/attempts = %s/%d/%d", replacement.State, replacement.Attempt, replacement.MaxAttempts)
	}
	if replacement.Queue != "plugin-maintenance" || replacement.Priority != 3 || !replacement.ScheduledAt.Equal(scheduledAt) {
		t.Fatalf("replacement scheduling = queue:%s priority:%d at:%s", replacement.Queue, replacement.Priority, replacement.ScheduledAt)
	}
	if !slices.Equal(replacement.Tags, tags) {
		t.Fatalf("replacement tags = %v", replacement.Tags)
	}
	var replacementArgs PluginJobArgs
	if err := json.Unmarshal(replacement.EncodedArgs, &replacementArgs); err != nil {
		t.Fatalf("decode replacement args: %v", err)
	}
	if !replacementArgs.Contract().Equal(target.Contract) || replacementArgs.TrustGrantID != target.TrustGrantID {
		t.Fatalf("replacement identity = %#v", replacementArgs)
	}
	if replacementArgs.Payload["migrated"] != true || replacementArgs.Payload["source"] != "original" {
		t.Fatalf("replacement payload = %#v", replacementArgs.Payload)
	}
	assertPluginJobLedgerLink(t, harness, sourceRow.ID, replacementID, migration, source, target)

	// 已提交事务的重试只会看到 cancelled source 与 exact target，不会重复迁移或插入。
	retry, err := (&PluginJobLifecycleCoordinator{Store: harness.store}).Reconcile(harness.ctx, input)
	if err != nil {
		t.Fatalf("retry reconcile: %v", err)
	}
	if !retry.Committed || retry.Plan.IgnoredFinalized != 1 || migratorCalls != 1 {
		t.Fatalf("retry result=%#v migrator calls=%d", retry, migratorCalls)
	}
	if harness.countJobs(t, extensionID, source.Contract.ArtifactDigest) != 1 ||
		harness.countJobs(t, extensionID, target.Contract.ArtifactDigest) != 1 ||
		harness.countLedger(t, sourceRow.ID) != 1 {
		t.Fatal("idempotent retry changed source, replacement, or ledger cardinality")
	}
}

func TestPostgresPluginJobLifecycleRollsBackEveryMutationBoundary(t *testing.T) {
	failure := errors.New("stop before transaction commit")
	for _, boundary := range []string{"claim", "insert", "complete", "cancel"} {
		t.Run(boundary, func(t *testing.T) {
			harness := newPluginJobLifecycleIntegrationHarness(t)
			extensionID := "sforum.lifecycle.rollback." + boundary
			source := integrationPluginJobRuntime(extensionID, "digest", "1.0.0", "sha256:rollback-v1-"+boundary, "1", "grant-v1")
			target := integrationPluginJobRuntime(extensionID, "digest", "2.0.0", "sha256:rollback-v2-"+boundary, "2", "grant-v2")
			migration, _ := integrationPluginJobMigrationInput(source, target, lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
				return input.Payload, nil
			}))
			sourceRow := harness.insertJob(t, integrationPluginJobArgs(source, map[string]any{"boundary": boundary}), &river.InsertOpts{
				MaxAttempts: 5, Queue: "plugin-rollback", Priority: 2, Tags: []string{"rollback-" + boundary},
			})
			ledger := PluginJobMigrationLedgerEntry{
				OldJobID: sourceRow.ID, ExtensionID: extensionID, MigrationID: migration.ID,
				SourceContract: source.Contract, SourceTrustGrantID: source.TrustGrantID,
				TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
			}
			err := harness.store.WithPluginJobLifecycleTx(harness.ctx, func(tx PluginJobLifecycleTx) error {
				rows, err := tx.LockPluginJobs(harness.ctx, extensionID)
				if err != nil || len(rows) != 1 || rows[0].JobID != sourceRow.ID {
					return fmt.Errorf("locked rows=%#v: %w", rows, err)
				}
				if _, err := tx.ClaimPluginJobMigration(harness.ctx, ledger); err != nil {
					return err
				}
				if boundary == "claim" {
					return failure
				}
				newJobID, err := tx.InsertPluginJob(harness.ctx, integrationPluginJobArgs(target, map[string]any{"boundary": boundary}), replacementPluginJobInsertOpts(rows[0]))
				if err != nil {
					return err
				}
				if boundary == "insert" {
					return failure
				}
				if err := tx.CompletePluginJobMigration(harness.ctx, ledger, newJobID); err != nil {
					return err
				}
				if boundary == "complete" {
					return failure
				}
				if err := tx.CancelPluginJob(harness.ctx, sourceRow.ID); err != nil {
					return err
				}
				return failure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("transaction error = %v", err)
			}
			if sourceAfter := harness.job(t, sourceRow.ID); sourceAfter.State != rivertype.JobStateAvailable {
				t.Fatalf("source state after %s rollback = %s", boundary, sourceAfter.State)
			}
			if harness.countLedger(t, sourceRow.ID) != 0 || harness.countJobs(t, extensionID, target.Contract.ArtifactDigest) != 0 {
				t.Fatalf("%s rollback retained ledger or replacement", boundary)
			}
		})
	}
}

func TestPostgresPluginJobLifecycleRetryReusesExistingExactLedgerLink(t *testing.T) {
	harness := newPluginJobLifecycleIntegrationHarness(t)
	extensionID := "sforum.lifecycle.linked-retry"
	source := integrationPluginJobRuntime(extensionID, "digest", "1.0.0", "sha256:linked-v1", "1", "grant-v1")
	target := integrationPluginJobRuntime(extensionID, "digest", "2.0.0", "sha256:linked-v2", "2", "grant-v2")
	migratorCalls := 0
	migration, input := integrationPluginJobMigrationInput(source, target, lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
		migratorCalls++
		return input.Payload, nil
	}))
	sourceRow := harness.insertJob(t, integrationPluginJobArgs(source, map[string]any{"value": "source"}), nil)
	ledger := PluginJobMigrationLedgerEntry{
		OldJobID: sourceRow.ID, ExtensionID: extensionID, MigrationID: migration.ID,
		SourceContract: source.Contract, SourceTrustGrantID: source.TrustGrantID,
		TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
	}
	var replacementID int64
	err := harness.store.WithPluginJobLifecycleTx(harness.ctx, func(tx PluginJobLifecycleTx) error {
		rows, err := tx.LockPluginJobs(harness.ctx, extensionID)
		if err != nil {
			return err
		}
		claim, err := tx.ClaimPluginJobMigration(harness.ctx, ledger)
		if err != nil {
			return err
		}
		if !claim.Claimed || !samePluginJobMigrationLedger(claim.Ledger, ledger) {
			return fmt.Errorf("unexpected initial ledger claim: %#v", claim)
		}
		replacementID, err = tx.InsertPluginJob(
			harness.ctx,
			integrationPluginJobArgs(target, map[string]any{"value": "replacement"}),
			replacementPluginJobInsertOpts(rows[0]),
		)
		if err != nil {
			return err
		}
		return tx.CompletePluginJobMigration(harness.ctx, ledger, replacementID)
	})
	if err != nil {
		t.Fatalf("seed exact linked ledger: %v", err)
	}
	if harness.job(t, sourceRow.ID).State != rivertype.JobStateAvailable {
		t.Fatal("recovery fixture unexpectedly cancelled source")
	}

	result, err := (&PluginJobLifecycleCoordinator{Store: harness.store}).Reconcile(harness.ctx, input)
	if err != nil {
		t.Fatalf("reconcile linked ledger: %v", err)
	}
	if !result.Committed || migratorCalls != 0 || len(result.Executions) != 2 {
		t.Fatalf("linked retry result=%#v migrator calls=%d", result, migratorCalls)
	}
	if result.Executions[0].ReplacementJobID != replacementID || harness.job(t, sourceRow.ID).State != rivertype.JobStateCancelled {
		t.Fatalf("linked retry did not reuse replacement %d: %#v", replacementID, result.Executions)
	}
	if harness.countJobs(t, extensionID, target.Contract.ArtifactDigest) != 1 || harness.countLedger(t, sourceRow.ID) != 1 {
		t.Fatal("linked retry inserted a duplicate replacement or ledger")
	}
}

func TestPostgresPluginJobLifecycleLinkRequiresExactLedgerIdentity(t *testing.T) {
	harness := newPluginJobLifecycleIntegrationHarness(t)
	extensionID := "sforum.lifecycle.identity"
	source := integrationPluginJobRuntime(extensionID, "digest", "1.0.0", "sha256:identity-v1", "1", "grant-v1")
	target := integrationPluginJobRuntime(extensionID, "digest", "2.0.0", "sha256:identity-v2", "2", "grant-v2")
	migration, _ := integrationPluginJobMigrationInput(source, target, lifecycleMigratorFunc(func(input PluginJobPayloadMigrationInput) (map[string]any, error) {
		return input.Payload, nil
	}))
	sourceRow := harness.insertJob(t, integrationPluginJobArgs(source, nil), nil)
	ledger := PluginJobMigrationLedgerEntry{
		OldJobID: sourceRow.ID, ExtensionID: extensionID, MigrationID: migration.ID,
		SourceContract: source.Contract, SourceTrustGrantID: source.TrustGrantID,
		TargetContract: target.Contract, TargetTrustGrantID: target.TrustGrantID,
	}
	err := harness.store.WithPluginJobLifecycleTx(harness.ctx, func(tx PluginJobLifecycleTx) error {
		if _, err := tx.ClaimPluginJobMigration(harness.ctx, ledger); err != nil {
			return err
		}
		mismatch := ledger
		mismatch.TargetTrustGrantID = "grant-other"
		return tx.CompletePluginJobMigration(harness.ctx, mismatch, sourceRow.ID+1000)
	})
	if !errors.Is(err, ErrPluginJobMigrationConflict) {
		t.Fatalf("mismatched ledger link error = %v", err)
	}
	if harness.countLedger(t, sourceRow.ID) != 0 {
		t.Fatal("failed exact-identity link committed its claim")
	}
}

func assertPluginJobLedgerLink(
	t *testing.T,
	harness *pluginJobLifecycleIntegrationHarness,
	oldJobID int64,
	newJobID int64,
	migration supportjobs.PluginJobMigration,
	source PluginJobRuntimeContract,
	target PluginJobRuntimeContract,
) {
	t.Helper()
	var extensionID, migrationID, sourceGrant, targetGrant string
	var persistedNewJobID int64
	var completed bool
	var sourceDocument, targetDocument []byte
	err := harness.pool.QueryRow(harness.ctx, `
		SELECT extension_id, migration_id, source_trust_grant_id, target_trust_grant_id,
		       new_job_id, completed_at IS NOT NULL, source_contract, target_contract
		FROM extension_plugin_job_migrations WHERE old_job_id = $1
	`, oldJobID).Scan(
		&extensionID, &migrationID, &sourceGrant, &targetGrant,
		&persistedNewJobID, &completed, &sourceDocument, &targetDocument,
	)
	if err != nil {
		t.Fatalf("read plugin job ledger: %v", err)
	}
	var persistedSource, persistedTarget supportjobs.PluginJobContract
	if err := json.Unmarshal(sourceDocument, &persistedSource); err != nil {
		t.Fatalf("decode source ledger contract: %v", err)
	}
	if err := json.Unmarshal(targetDocument, &persistedTarget); err != nil {
		t.Fatalf("decode target ledger contract: %v", err)
	}
	if extensionID != source.Contract.ExtensionID || migrationID != migration.ID ||
		sourceGrant != source.TrustGrantID || targetGrant != target.TrustGrantID ||
		persistedNewJobID != newJobID || !completed ||
		!persistedSource.Equal(source.Contract) || !persistedTarget.Equal(target.Contract) {
		t.Fatalf("persisted ledger identity changed: source=%#v target=%#v new=%d", persistedSource, persistedTarget, persistedNewJobID)
	}
}
