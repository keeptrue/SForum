package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestPostgresLifecycleCleanupStagesEveryModeAndRetainsRecoveryMaterial(t *testing.T) {
	tests := []struct {
		name        string
		operation   extensions.LifecycleMachineOperation
		mode        LifecycleBoundaryCleanupMode
		removalMode string
		status      string
	}{
		{"disable", extensions.LifecycleMachineDisable, LifecycleBoundaryCleanupDisable, "", lifecycleCleanupStatusRetained},
		{"upgrade retired source", extensions.LifecycleMachineUpgrade, LifecycleBoundaryCleanupRetiredSource, "", lifecycleCleanupStatusRetained},
		{"rollback retired source", extensions.LifecycleMachineRollback, LifecycleBoundaryCleanupRetiredSource, "", lifecycleCleanupStatusRetained},
		{"uninstall preserve", extensions.LifecycleMachineUninstall, LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve, lifecycleCleanupStatusPending},
		{"uninstall export", extensions.LifecycleMachineUninstall, LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove, lifecycleCleanupStatusPending},
		{"uninstall complete", extensions.LifecycleMachineUninstall, LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete, lifecycleCleanupStatusPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleCleanupIntegration(t, test.operation, test.mode, test.removalMode)
			const workers = 12
			var wg sync.WaitGroup
			results := make(chan LifecycleBoundaryCleanupResult, workers)
			errs := make(chan error, workers)
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
					results <- result
					errs <- err
				}()
			}
			wg.Wait()
			close(results)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			for result := range results {
				if !result.IdentityRetained || !result.PackageRetained || !result.RuntimeRecoveryRetained {
					t.Fatalf("retention result = %#v", result)
				}
				wantTombstone := test.operation == extensions.LifecycleMachineUninstall
				if result.DurableTombstone != wantTombstone || (result.TombstoneID != "") != wantTombstone {
					t.Fatalf("tombstone result = %#v", result)
				}
				if test.mode == LifecycleBoundaryCleanupPreserve && result.RetentionMarker == "" {
					t.Fatalf("preserve result = %#v", result)
				}
				if test.mode == LifecycleBoundaryCleanupExport &&
					(result.ExportArtifactID != h.exportArtifactID || result.ExportDigest != h.exportDigest) {
					t.Fatalf("export result = %#v", result)
				}
			}

			var status, recordKind, targetRuntime, snapshots, exportEvidence string
			var identityRetained, packageRetained, runtimeRetained bool
			var attempts, rows int
			if err := h.pool.QueryRow(h.ctx, `
				SELECT status, record_kind, target_runtime_instance_id,
				       identity_recovery_evidence_retained,
				       package_recovery_evidence_retained,
				       runtime_recovery_evidence_retained,
				       jsonb_array_length(runtime_recovery_attempts),
				       identity_snapshot::text || package_snapshot::text || runtime_recovery_snapshot::text,
				       COALESCE(export_evidence::text, ''), count(*) OVER ()
				FROM extension_lifecycle_cleanup_records
				WHERE operation_id = $1
			`, h.request.OperationID).Scan(
				&status, &recordKind, &targetRuntime,
				&identityRetained, &packageRetained, &runtimeRetained,
				&attempts, &snapshots, &exportEvidence, &rows,
			); err != nil {
				t.Fatal(err)
			}
			if status != test.status || attempts != 1 || rows != 1 ||
				!identityRetained || !packageRetained || !runtimeRetained {
				t.Fatalf("record status=%q attempts=%d rows=%d retained=%v/%v/%v",
					status, attempts, rows, identityRetained, packageRetained, runtimeRetained)
			}
			if (recordKind == lifecycleCleanupRecordUninstall) != (test.operation == extensions.LifecycleMachineUninstall) {
				t.Fatalf("record kind = %q", recordKind)
			}
			if (test.operation == extensions.LifecycleMachineDisable || test.operation == extensions.LifecycleMachineUninstall) && targetRuntime != "" {
				t.Fatalf("deactivation target runtime = %q", targetRuntime)
			}
			if strings.Contains(snapshots, "authoritySnapshot") || strings.Contains(snapshots, "secret-value") ||
				strings.Contains(exportEvidence, "secret-value") {
				t.Fatalf("cleanup record leaked non-allowlisted authority/secret data: %s %s", snapshots, exportEvidence)
			}
			if _, err := os.Stat(h.sourcePackagePath); err != nil {
				t.Fatalf("source package was not retained: %v", err)
			}
			var extensionCount, versionCount int
			if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extensions WHERE id = $1`, h.extensionID).Scan(&extensionCount); err != nil {
				t.Fatal(err)
			}
			if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, h.extensionID).Scan(&versionCount); err != nil {
				t.Fatal(err)
			}
			if extensionCount != 1 || versionCount != h.versionCount {
				t.Fatalf("staging removed identity/package rows: extensions=%d versions=%d", extensionCount, versionCount)
			}

			restarted := NewPostgresLifecycleBoundaryCleanup(h.pool)
			if _, err := restarted.StageLifecycleHostCleanup(h.ctx, h.request, h.mode); err != nil {
				t.Fatalf("restart replay: %v", err)
			}
		})
	}
}

func TestPostgresLifecycleCleanupAdvancesRuntimeRecoveryAttemptAndRejectsStaleReplay(t *testing.T) {
	h := newLifecycleCleanupIntegration(t, extensions.LifecycleMachineDisable, LifecycleBoundaryCleanupDisable, "")
	if _, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'succeeded', completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE operation_id = $1 AND step_id = $2 AND attempt = $3
	`, h.request.OperationID, h.request.StepID, h.request.Attempt); err != nil {
		t.Fatal(err)
	}
	next := cloneLifecycleBoundaryRequest(h.request)
	next.Attempt++
	next.SourceBinding.RuntimeInstanceID = "source-runtime-restarted"
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, started_at, actor_user_id, audit_event_id
		) VALUES ($1, $2, 'host.gate', 'cleanup.integration@1', $3,
		          'running', statement_timestamp(), $4, $5)
	`, next.OperationID, next.StepID, next.Attempt, next.ActorUserID, next.AuditEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, next, h.mode); err != nil {
		t.Fatal(err)
	}
	var lastAttempt, attempts int
	var runtimeID string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT last_attempt, jsonb_array_length(runtime_recovery_attempts), retained_runtime_instance_id
		FROM extension_lifecycle_cleanup_records WHERE operation_id = $1
	`, h.request.OperationID).Scan(&lastAttempt, &attempts, &runtimeID); err != nil {
		t.Fatal(err)
	}
	if lastAttempt != next.Attempt || attempts != 2 || runtimeID != next.SourceBinding.RuntimeInstanceID {
		t.Fatalf("recovery attempt = last:%d count:%d runtime:%q", lastAttempt, attempts, runtimeID)
	}
	if _, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode); !errors.Is(err, ErrLifecycleCleanupConflict) {
		t.Fatalf("stale replay error = %v", err)
	}
}

func TestPostgresLifecycleCleanupFinalizerRequiresTerminalAndActualIdempotentPurge(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve,
	)
	staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
	if err != nil {
		t.Fatal(err)
	}
	withoutPurger := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, nil)
	if _, err := withoutPurger.FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID); !errors.Is(err, ErrLifecycleCleanupNotTerminal) {
		t.Fatalf("preterminal finalization error = %v", err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
	if _, err := withoutPurger.FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID); !errors.Is(err, ErrLifecycleCleanupPurgeUnavailable) {
		t.Fatalf("missing purger error = %v", err)
	}
	assertLifecycleCleanupStatus(t, h, lifecycleCleanupStatusPending)

	failing := lifecycleCleanupPurgerFunc(func(context.Context, LifecycleCleanupPurgeRequest) (LifecycleCleanupPurgeReceipt, error) {
		return LifecycleCleanupPurgeReceipt{}, errors.New("physical purge failed")
	})
	if _, err := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, failing).
		FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID); err == nil || !strings.Contains(err.Error(), "physical purge failed") {
		t.Fatalf("purge failure error = %v", err)
	}
	assertLifecycleCleanupStatus(t, h, lifecycleCleanupStatusPending)

	retainedData := filepath.Join(t.TempDir(), "retained-plugin-data.json")
	if err := os.WriteFile(retainedData, []byte(`{"retained":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	purger := &lifecycleCleanupIntegrationPurger{
		pool: h.pool, dataPath: retainedData,
		receipts: make(map[string]LifecycleCleanupPurgeReceipt),
	}
	finalizer := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, purger)
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	results := make(chan LifecycleCleanupFinalizationResult, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := finalizer.FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.CleanupID != staged.TombstoneID || result.Status != "finalized" ||
			!result.PhysicalPurgeCompleted || result.PurgeReceiptID == "" ||
			!validLifecycleCleanupDigest(result.PurgeProofDigest) || result.FinalizedAt == nil {
			t.Fatalf("finalization result = %#v", result)
		}
	}
	purger.mu.Lock()
	executions := purger.executions
	purger.mu.Unlock()
	if executions != 1 {
		t.Fatalf("physical purge executions = %d, want 1", executions)
	}

	restarted := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, purger)
	result, err := restarted.FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID)
	if err != nil || !result.PhysicalPurgeCompleted || result.Status != "finalized" {
		t.Fatalf("finalizer restart replay = %#v, %v", result, err)
	}
	purger.mu.Lock()
	executions = purger.executions
	purger.mu.Unlock()
	if executions != 1 {
		t.Fatalf("restart repeated physical purge: %d", executions)
	}

	var extensionCount, versionCount int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extensions WHERE id = $1`, h.extensionID).Scan(&extensionCount); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extension_versions WHERE extension_id = $1`, h.extensionID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if extensionCount != 0 || versionCount != 0 {
		t.Fatalf("test purger left identity/package rows: %d/%d", extensionCount, versionCount)
	}
	if _, err := os.Stat(h.sourcePackagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test purger left package path: %v", err)
	}
	if _, err := os.Stat(retainedData); err != nil {
		t.Fatalf("preserve-mode purger removed retained data: %v", err)
	}
	var disposition, proofDigest string
	var identityPresent, packagePresent, runtimePresent bool
	if err := h.pool.QueryRow(h.ctx, `
		SELECT purge_proof->>'dataDisposition', purge_proof_digest,
		       physical_identity_present, physical_package_present,
		       physical_runtime_recovery_present
		FROM extension_lifecycle_cleanup_records WHERE operation_id = $1
	`, h.request.OperationID).Scan(
		&disposition, &proofDigest, &identityPresent, &packagePresent, &runtimePresent,
	); err != nil {
		t.Fatal(err)
	}
	if disposition != "preserved" || !validLifecycleCleanupDigest(proofDigest) ||
		identityPresent || packagePresent || runtimePresent {
		t.Fatalf("purge proof disposition/digest/presence = %q/%q/%v/%v/%v",
			disposition, proofDigest, identityPresent, packagePresent, runtimePresent)
	}
}

func TestPostgresLifecycleCleanupFinalizerPersistsExactDispositionProofForEveryMode(t *testing.T) {
	tests := []struct {
		name        string
		mode        LifecycleBoundaryCleanupMode
		removalMode string
		disposition string
		dataRemains bool
	}{
		{"preserve", LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve, "preserved", true},
		{"export then remove", LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove, "exported_then_removed", false},
		{"complete removal", LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete, "removed", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleCleanupIntegration(t, extensions.LifecycleMachineUninstall, test.mode, test.removalMode)
			if _, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode); err != nil {
				t.Fatal(err)
			}
			h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
			dataPath := filepath.Join(t.TempDir(), "plugin-data.json")
			if err := os.WriteFile(dataPath, []byte(`{"pluginData":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			purger := &lifecycleCleanupIntegrationPurger{
				pool: h.pool, dataPath: dataPath, exportArtifactPath: h.exportArtifactPath,
				receipts: make(map[string]LifecycleCleanupPurgeReceipt),
			}
			result, err := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, purger).
				FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID)
			if err != nil || !result.PhysicalPurgeCompleted {
				t.Fatalf("finalize = %#v, %v", result, err)
			}
			var disposition, retentionMarker, exportArtifactID, exportDigest string
			var proof json.RawMessage
			if err := h.pool.QueryRow(h.ctx, `
				SELECT purge_proof->>'dataDisposition',
				       COALESCE(purge_proof->>'retentionMarker', ''),
				       COALESCE(purge_proof->>'exportArtifactId', ''),
				       COALESCE(purge_proof->>'exportDigest', ''), purge_proof
				FROM extension_lifecycle_cleanup_records WHERE operation_id = $1
			`, h.request.OperationID).Scan(
				&disposition, &retentionMarker, &exportArtifactID, &exportDigest, &proof,
			); err != nil {
				t.Fatal(err)
			}
			if disposition != test.disposition || !json.Valid(proof) {
				t.Fatalf("proof = %s disposition=%q", proof, disposition)
			}
			switch test.mode {
			case LifecycleBoundaryCleanupPreserve:
				if retentionMarker == "" || exportArtifactID != "" || exportDigest != "" {
					t.Fatalf("preserve proof fields = %q/%q/%q", retentionMarker, exportArtifactID, exportDigest)
				}
			case LifecycleBoundaryCleanupExport:
				if retentionMarker != "" || exportArtifactID != h.exportArtifactID || exportDigest != h.exportDigest {
					t.Fatalf("export proof fields = %q/%q/%q", retentionMarker, exportArtifactID, exportDigest)
				}
			case LifecycleBoundaryCleanupComplete:
				if retentionMarker != "" || exportArtifactID != "" || exportDigest != "" {
					t.Fatalf("complete proof fields = %q/%q/%q", retentionMarker, exportArtifactID, exportDigest)
				}
			}
			_, statErr := os.Stat(dataPath)
			if (statErr == nil) != test.dataRemains {
				t.Fatalf("data remains=%v, stat=%v", test.dataRemains, statErr)
			}
		})
	}
}

func TestPostgresLifecycleCleanupFinalizerReplaysBothPurgeCommitWindows(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete,
	)
	if _, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode); err != nil {
		t.Fatal(err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
	dataPath := filepath.Join(t.TempDir(), "plugin-data.json")
	if err := os.WriteFile(dataPath, []byte(`{"pluginData":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	purger := &lifecycleCleanupIntegrationPurger{
		pool: h.pool, dataPath: dataPath,
		receipts: make(map[string]LifecycleCleanupPurgeReceipt),
	}
	candidate, err := loadLifecycleCleanupFinalizationCandidate(h.ctx, h.pool, h.request.OperationID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Crash window 1: physical purge completed, but no tombstone transaction was
	// committed. Retry must reuse the purger's durable receipt.
	if _, err := purger.PurgeLifecycleHostCleanup(h.ctx, candidate.purgeRequest()); err != nil {
		t.Fatal(err)
	}
	assertLifecycleCleanupStatus(t, h, lifecycleCleanupStatusPending)
	result, err := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, purger).
		FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID)
	if err != nil || !result.PhysicalPurgeCompleted {
		t.Fatalf("post-purge replay = %#v, %v", result, err)
	}
	purger.mu.Lock()
	executions := purger.executions
	purger.mu.Unlock()
	if executions != 1 {
		t.Fatalf("post-purge replay executions = %d", executions)
	}

	// Crash window 2: finalization committed but the caller lost the response.
	// A restarted finalizer must return the marker without invoking a purger.
	mustNotRun := lifecycleCleanupPurgerFunc(func(context.Context, LifecycleCleanupPurgeRequest) (LifecycleCleanupPurgeReceipt, error) {
		return LifecycleCleanupPurgeReceipt{}, errors.New("purger must not run after committed finalization")
	})
	replayed, err := NewPostgresLifecycleBoundaryCleanupFinalizer(h.pool, mustNotRun).
		FinalizeLifecycleHostCleanup(h.ctx, h.request.OperationID)
	if err != nil || replayed.PurgeProofDigest != result.PurgeProofDigest || !replayed.PhysicalPurgeCompleted {
		t.Fatalf("post-commit replay = %#v, %v", replayed, err)
	}
}

type lifecycleCleanupIntegration struct {
	ctx                context.Context
	pool               *pgxpool.Pool
	cleanup            *PostgresLifecycleBoundaryCleanup
	request            LifecycleBoundaryRequest
	mode               LifecycleBoundaryCleanupMode
	extensionID        string
	sourcePackagePath  string
	targetPackagePath  string
	exportArtifactID   string
	exportArtifactPath string
	exportDigest       string
	versionCount       int
	actorUserID        int64
	auditEventID       int64
}

func newLifecycleCleanupIntegration(
	t *testing.T,
	operation extensions.LifecycleMachineOperation,
	mode LifecycleBoundaryCleanupMode,
	removalMode string,
) *lifecycleCleanupIntegration {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	position, err := lifecycleCleanupPosition(operation, mode, removalMode)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	request := lifecycleCleanupTestRequest(t, operation, position)
	request.RemovalMode = removalMode
	extensionID := fmt.Sprintf("cleanup.integration.%d", time.Now().UnixNano())
	request.TargetExtension.ID = extensionID
	request.TargetExtension.Manifest.ID = extensionID
	request.TargetExtension.Name = "Cleanup Integration"
	request.TargetBinding.ExtensionID = extensionID
	request.TargetExtension.PackagePath = filepath.Join(t.TempDir(), "target-package")
	if err := writeLifecycleCleanupPackage(request.TargetExtension.PackagePath); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if request.SourceExtension == nil {
		pool.Close()
		t.Fatal("cleanup integration request has no source")
	}
	request.SourceExtension.ID = extensionID
	request.SourceExtension.Manifest.ID = extensionID
	request.SourceExtension.Name = "Cleanup Integration Source"
	request.SourceBinding.ExtensionID = extensionID
	if request.SourceExtension.PackageDigest == request.TargetExtension.PackageDigest {
		request.SourceExtension.PackagePath = request.TargetExtension.PackagePath
	} else {
		request.SourceExtension.PackagePath = filepath.Join(t.TempDir(), "source-package")
		if err := writeLifecycleCleanupPackage(request.SourceExtension.PackagePath); err != nil {
			pool.Close()
			t.Fatal(err)
		}
	}
	// Ensure arbitrary lifecycle result data is never copied into cleanup rows.
	request.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(`{"secret":"secret-value"}`)
	exportArtifactID, exportArtifactPath, exportDigest := "", "", ""
	if mode == LifecycleBoundaryCleanupExport {
		exportArtifactID = "export-" + strings.ReplaceAll(extensionID, ".", "-")
		exportBytes := []byte("durable export artifact for " + extensionID)
		exportArtifactPath = filepath.Join(t.TempDir(), "export-artifact.bin")
		if err := os.WriteFile(exportArtifactPath, exportBytes, 0o600); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		digest := sha256Bytes(exportBytes)
		exportDigest = digest
		request.actionResults[extensions.LifecycleMachineUninstallAfter] = json.RawMessage(fmt.Sprintf(
			`{"exportArtifactId":%q,"exportDigest":%q,"secret":"secret-value"}`,
			exportArtifactID, exportDigest,
		))
	}

	username := fmt.Sprintf("cleanup_%d", time.Now().UnixNano())
	actorUserID, err := insertCleanupActor(ctx, pool, username)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	var auditEventID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'extension.lifecycle.cleanup.integration', '{}'::jsonb)
		RETURNING id
	`, actorUserID).Scan(&auditEventID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, actorUserID)
		pool.Close()
		t.Fatal(err)
	}
	status := "enabled"
	if operation == extensions.LifecycleMachineDisable || operation == extensions.LifecycleMachineUninstall {
		status = "disabled"
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Cleanup Integration', $2, 'uploaded', false, true)
	`, extensionID, status); err != nil {
		pool.Close()
		t.Fatal(err)
	}

	insertVersion := func(extension extensions.Extension) int64 {
		t.Helper()
		manifest, marshalErr := json.Marshal(extension.Manifest)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var id int64
		if insertErr := pool.QueryRow(ctx, `
			INSERT INTO extension_versions (
				extension_id, version, manifest, package_path, package_digest
			) VALUES ($1, $2, $3::jsonb, $4, $5)
			RETURNING id
		`, extensionID, extension.Version, manifest, extension.PackagePath, extension.PackageDigest).Scan(&id); insertErr != nil {
			t.Fatal(insertErr)
		}
		return id
	}
	var sourceVersionID int64
	targetVersionID := insertVersion(request.TargetExtension)
	versionCount := 1
	if request.SourceExtension.PackageDigest == request.TargetExtension.PackageDigest {
		sourceVersionID = targetVersionID
	} else {
		sourceVersionID = insertVersion(*request.SourceExtension)
		versionCount++
	}
	request.TargetExtension.ActiveVersionID = targetVersionID
	request.TargetBinding.VersionID = targetVersionID
	request.SourceExtension.ActiveVersionID = sourceVersionID
	request.SourceBinding.VersionID = sourceVersionID
	request.SourceBinding.RuntimeInstanceID = "source-runtime"
	request.TargetBinding.RuntimeInstanceID = "target-runtime"
	if operation == extensions.LifecycleMachineDisable || operation == extensions.LifecycleMachineUninstall {
		request.TargetBinding.RuntimeInstanceID = ""
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, extensionID, targetVersionID); err != nil {
		t.Fatal(err)
	}
	request.ActorUserID = actorUserID
	request.AuditEventID = auditEventID
	path, err := extensions.RecommendedLifecyclePath(operation)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, audit_event_id,
			removal_mode
		) VALUES (
			$1, $2, $3, '{}'::jsonb,
			$4, $5, 'cleanup.integration@1', $6, $7,
			'builtin', '{}'::jsonb, $8, $9, $10
		)
		RETURNING id
	`, extensionID, request.TargetExtension.Version, request.TargetExtension.PackageDigest,
		operation, path[position].State,
		"cleanup:"+extensionID, strings.Repeat("c", 64), actorUserID, auditEventID,
		nullableLifecycleCleanupString(removalMode)).Scan(&operationID); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	request.OperationID = operationID
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_steps (
			operation_id, step_id, lifecycle_action, plan_version, attempt,
			status, started_at, actor_user_id, audit_event_id
		) VALUES ($1, $2, 'host.gate', 'cleanup.integration@1', $3,
		          'running', statement_timestamp(), $4, $5)
	`, operationID, request.StepID, request.Attempt, actorUserID, auditEventID); err != nil {
		pool.Close()
		t.Fatal(err)
	}

	h := &lifecycleCleanupIntegration{
		ctx: ctx, pool: pool, cleanup: NewPostgresLifecycleBoundaryCleanup(pool),
		request: request, mode: mode, extensionID: extensionID,
		sourcePackagePath: request.SourceExtension.PackagePath,
		targetPackagePath: request.TargetExtension.PackagePath,
		exportArtifactID:  exportArtifactID, exportArtifactPath: exportArtifactPath,
		exportDigest: exportDigest,
		versionCount: versionCount, actorUserID: actorUserID, auditEventID: auditEventID,
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_cleanup_records WHERE operation_id = $1`, operationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, operationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_events WHERE id = $1`, auditEventID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actorUserID)
		pool.Close()
	})
	return h
}

func (h *lifecycleCleanupIntegration) completeOperation(t *testing.T, terminal string) {
	t.Helper()
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_lifecycle_steps
		SET status = 'succeeded', completed_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE operation_id = $1 AND step_id = $2 AND attempt = $3
	`, h.request.OperationID, h.request.StepID, h.request.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_lifecycle_operations
		SET terminal_result = $2, completed_at = statement_timestamp(),
		    revision = revision + 1, updated_at = statement_timestamp()
		WHERE id = $1
	`, h.request.OperationID, terminal); err != nil {
		t.Fatal(err)
	}
}

func assertLifecycleCleanupStatus(t *testing.T, h *lifecycleCleanupIntegration, want string) {
	t.Helper()
	var status string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT status FROM extension_lifecycle_cleanup_records WHERE operation_id = $1
	`, h.request.OperationID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("cleanup status = %q, want %q", status, want)
	}
}

type lifecycleCleanupPurgerFunc func(context.Context, LifecycleCleanupPurgeRequest) (LifecycleCleanupPurgeReceipt, error)

func (f lifecycleCleanupPurgerFunc) PurgeLifecycleHostCleanup(
	ctx context.Context,
	request LifecycleCleanupPurgeRequest,
) (LifecycleCleanupPurgeReceipt, error) {
	return f(ctx, request)
}

type lifecycleCleanupIntegrationPurger struct {
	mu                 sync.Mutex
	pool               *pgxpool.Pool
	dataPath           string
	exportArtifactPath string
	receipts           map[string]LifecycleCleanupPurgeReceipt
	executions         int
}

func (p *lifecycleCleanupIntegrationPurger) PurgeLifecycleHostCleanup(
	ctx context.Context,
	request LifecycleCleanupPurgeRequest,
) (LifecycleCleanupPurgeReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if receipt, ok := p.receipts[request.CleanupID]; ok {
		return receipt, nil
	}
	disposition := ""
	switch request.CleanupMode {
	case LifecycleBoundaryCleanupPreserve:
		if request.RetentionMarker == "" {
			return LifecycleCleanupPurgeReceipt{}, errors.New("preserve marker is missing")
		}
		if _, err := os.Stat(p.dataPath); err != nil {
			return LifecycleCleanupPurgeReceipt{}, fmt.Errorf("retained data missing: %w", err)
		}
		disposition = "preserved"
	case LifecycleBoundaryCleanupExport:
		exported, err := os.ReadFile(p.exportArtifactPath)
		if err != nil || sha256Bytes(exported) != request.ExportDigest || request.ExportArtifactID == "" {
			return LifecycleCleanupPurgeReceipt{}, fmt.Errorf("export artifact is not durable or exact: %w", err)
		}
		if err := os.Remove(p.dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return LifecycleCleanupPurgeReceipt{}, err
		}
		disposition = "exported_then_removed"
	case LifecycleBoundaryCleanupComplete:
		if err := os.Remove(p.dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return LifecycleCleanupPurgeReceipt{}, err
		}
		disposition = "removed"
	default:
		return LifecycleCleanupPurgeReceipt{}, errors.New("unexpected test purge mode")
	}
	if err := os.RemoveAll(request.RetainedPackagePath); err != nil {
		return LifecycleCleanupPurgeReceipt{}, err
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, request.RetainedExtensionID); err != nil {
		return LifecycleCleanupPurgeReceipt{}, err
	}
	if _, err := os.Stat(request.RetainedPackagePath); !errors.Is(err, os.ErrNotExist) {
		return LifecycleCleanupPurgeReceipt{}, fmt.Errorf("package path still exists: %v", err)
	}
	if request.CleanupMode != LifecycleBoundaryCleanupPreserve {
		if _, err := os.Stat(p.dataPath); !errors.Is(err, os.ErrNotExist) {
			return LifecycleCleanupPurgeReceipt{}, fmt.Errorf("removed data still exists: %v", err)
		}
	}
	receipt := LifecycleCleanupPurgeReceipt{
		CleanupID: request.CleanupID, OperationID: request.OperationID,
		CleanupMode: request.CleanupMode, RetainedPackageDigest: request.RetainedPackageDigest,
		ReceiptID:      "purge-" + request.CleanupID,
		IdentityPurged: true, PackagePurged: true, RuntimeRecoveryPurged: true,
		DataDisposition: disposition,
		Proof: json.RawMessage(fmt.Sprintf(
			`{"extensionRemoved":true,"packageRemoved":true,"dataDisposition":%q}`,
			disposition,
		)),
	}
	p.executions++
	p.receipts[request.CleanupID] = receipt
	return receipt, nil
}

func writeLifecycleCleanupPackage(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "package.marker"), []byte("retained exact package"), 0o600)
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// insertCleanupActor 插入一个清理流程集成测试用的真实账号并返回 id。
// 序列重试的原因见 insertLifecycleActorFixture：共享测试库里的 users 序列
// 可能落后于别的包显式插入的 id。
func insertCleanupActor(ctx context.Context, pool *pgxpool.Pool, username string) (int64, error) {
	return insertLifecycleActorFixture(ctx, pool, username, "Cleanup Integration")
}
