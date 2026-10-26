package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestProductionLifecycleCleanupIdentityExactCASAndEvidenceRetention(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	// CI 的共享 PG 容器较慢，8 路并发抢同一把 advisory lock 时 30s 可能不够。
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	extensionID := fmt.Sprintf("bootstrap.cleanup.%d", time.Now().UnixNano())
	cleanupID := strings.ReplaceAll(extensionID, ".", "-")
	username := strings.ReplaceAll(extensionID, ".", "_")
	var actorUserID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, 'Bootstrap Cleanup')
		RETURNING id
	`, username, username+"@example.test").Scan(&actorUserID); err != nil {
		t.Fatal(err)
	}
	extensionRoot := t.TempDir()
	packagePath := filepath.Join(extensionRoot, extensionID, "artifact")
	if err := os.MkdirAll(packagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "plugin.json"), []byte("exact cleanup package"), 0o600); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"manifestVersion": 3, "id": extensionID, "type": "plugin",
		"name": "Bootstrap Cleanup", "version": "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', 'Bootstrap Cleanup', 'disabled', 'uploaded', false, true)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.0', $2::jsonb, $3, $4)
		RETURNING id
	`, extensionID, manifest, packagePath, packageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}
	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, removal_mode,
			terminal_result, started_at, completed_at
		) VALUES (
			$1, '1.0.0', $2, '{}'::jsonb,
			'uninstall', 'uninstalling', 'bootstrap.cleanup@1', $3, repeat('a', 64),
			'builtin', '{}'::jsonb, $4, 'preserve',
			'succeeded', statement_timestamp(), statement_timestamp()
		)
		RETURNING id
	`, extensionID, packageDigest, cleanupID, actorUserID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extension_lifecycle_cleanup_records (
			cleanup_id, operation_id, operation, step_id, position,
			first_attempt, last_attempt, cleanup_mode, record_kind, status,
			retained_extension_id, retained_extension_version, retained_package_digest,
			retained_version_id, retained_runtime_instance_id, retained_package_path,
			identity_snapshot, package_snapshot, runtime_recovery_snapshot,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_package_path,
			retention_marker
		) VALUES (
			$1, $2, 'uninstall', 'lifecycle.uninstall.06.host.uninstalling', 6,
			1, 1, 'uninstall_preserve', 'uninstall_tombstone', 'pending',
			$3, '1.0.0', $4,
			$5, 'runtime-old', $6,
			'{"extension":"retained"}'::jsonb,
			'{"package":"retained"}'::jsonb,
			'{"runtime":"retained"}'::jsonb,
			$3, '1.0.0', $4,
			$5, '', $6,
			'retained-data-bootstrap'
		)
	`, cleanupID, operationID, extensionID, packageDigest, versionID, packagePath); err != nil {
		t.Fatal(err)
	}
	var trustGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action,
			artifact_digests, impact_document, impact_digest, granted_by_user_id
		) VALUES ($1, '1.0.0', $2, 'enable', '{}'::jsonb, '{}'::jsonb, repeat('b', 64), $3)
		RETURNING id
	`, extensionID, packageDigest, actorUserID).Scan(&trustGrantID); err != nil {
		t.Fatal(err)
	}
	var frontendGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_frontend_trust_grants (
			extension_id, extension_version, package_digest, admin_frontend_digest,
			api_version, component_ids, granted_by_user_id
		) VALUES ($1, '1.0.0', $2, repeat('c', 64), 1, '[]'::jsonb, $3)
		RETURNING id
	`, extensionID, packageDigest, actorUserID).Scan(&frontendGrantID); err != nil {
		t.Fatal(err)
	}
	pageID := "bootstrap.cleanup.page." + cleanupID
	if _, err := pool.Exec(ctx, `
		INSERT INTO page_provider_bindings (
			page_id, extension_id, contribution_id, version, package_digest, approved_by
		) VALUES ($1, $2, 'cleanup-page', '1.0.0', $3, $4)
	`, pageID, extensionID, packageDigest, actorUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM page_provider_bindings WHERE page_id = $1`, pageID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_frontend_trust_grants WHERE id = $1`, frontendGrantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_trust_grants WHERE id = $1`, trustGrantID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_database_dispositions WHERE operation_id = $1`, operationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_cleanup_records WHERE operation_id = $1`, operationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, operationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, extensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actorUserID)
	})

	request := extensionsruntime.LifecycleCleanupPurgeRequest{
		CleanupID: cleanupID, OperationID: operationID,
		CleanupMode:         extensionsruntime.LifecycleBoundaryCleanupPreserve,
		RetainedExtensionID: extensionID, RetainedExtensionVersion: "1.0.0",
		RetainedPackageDigest: packageDigest, RetainedVersionID: versionID,
		RetainedPackagePath: packagePath, RetentionMarker: "retained-data-bootstrap",
	}
	drifted := request
	drifted.RetainedVersionID++
	if _, err := beginProductionLifecycleCleanupIdentity(ctx, pool, drifted); !errors.Is(err, errProductionLifecycleCleanupConflict) {
		t.Fatalf("drifted identity error = %v", err)
	}

	artifact, err := inspectProductionLifecyclePackage(t.TempDir(), packagePath, packageDigest)
	if err == nil || artifact.present {
		t.Fatal("unrelated root accepted the exact package")
	}
	artifact, err = inspectProductionLifecyclePackage(extensionRoot, packagePath, packageDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.present {
		t.Fatal("exact package was not present before finalization")
	}
	purger, err := newProductionLifecycleCleanupPurger(
		pool,
		lifecycleCleanupRuntimeInspector{err: extensionsruntime.ErrRuntimeInstanceNotFound},
		extensionRoot,
		extensionsruntime.NewPostgresExtensionDatabaseDisposition(pool),
	)
	if err != nil {
		t.Fatal(err)
	}
	finalizer := extensionsruntime.NewPostgresLifecycleBoundaryCleanupFinalizer(pool, purger)
	// 4 路并发足以覆盖 CAS 幂等路径，同时降低 CI 慢 PG 上的锁排队时间。
	const workers = 4
	results := make(chan extensionsruntime.LifecycleCleanupFinalizationResult, workers)
	errorsCh := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, finalizeErr := finalizer.FinalizeLifecycleHostCleanup(ctx, operationID)
			if finalizeErr != nil {
				errorsCh <- finalizeErr
				return
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for finalizeErr := range errorsCh {
		t.Fatalf("concurrent production lifecycle cleanup: %v", finalizeErr)
	}
	var result extensionsruntime.LifecycleCleanupFinalizationResult
	for item := range results {
		if result.OperationID == 0 {
			result = item
		}
		if item.OperationID != operationID || item.CleanupID != cleanupID ||
			item.Status != "finalized" || !item.PhysicalPurgeCompleted ||
			item.PurgeReceiptID == "" || item.PurgeProofDigest == "" ||
			(result.OperationID != 0 && (item.PurgeReceiptID != result.PurgeReceiptID ||
				item.PurgeProofDigest != result.PurgeProofDigest)) {
			t.Fatalf("unexpected concurrent cleanup finalization result: %#v", item)
		}
	}
	if result.OperationID == 0 {
		t.Fatal("concurrent cleanup returned no results")
	}
	replayed, err := finalizer.FinalizeLifecycleHostCleanup(ctx, operationID)
	if err != nil {
		t.Fatalf("replay production lifecycle cleanup: %v", err)
	}
	if replayed.PurgeReceiptID != result.PurgeReceiptID ||
		replayed.PurgeProofDigest != result.PurgeProofDigest || !replayed.PhysicalPurgeCompleted {
		t.Fatalf("cleanup replay drifted: first=%#v replay=%#v", result, replayed)
	}

	var retainedOperation, retainedCleanup bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM extension_lifecycle_operations WHERE id = $1)`, operationID).Scan(&retainedOperation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM extension_lifecycle_cleanup_records WHERE operation_id = $1)`, operationID).Scan(&retainedCleanup); err != nil {
		t.Fatal(err)
	}
	if !retainedOperation || !retainedCleanup {
		t.Fatalf("retained operation=%v cleanup=%v", retainedOperation, retainedCleanup)
	}
	var resourceExisted, credentialRevoked, schemaRetained, rolesRemoved bool
	var dispositionStatus string
	if err := pool.QueryRow(ctx, `
		SELECT resource_existed, credential_revoked, schema_retained, roles_removed, status
		FROM extension_database_dispositions WHERE operation_id = $1
	`, operationID).Scan(
		&resourceExisted, &credentialRevoked, &schemaRetained, &rolesRemoved, &dispositionStatus,
	); err != nil {
		t.Fatalf("load retained database disposition receipt: %v", err)
	}
	if resourceExisted || !credentialRevoked || schemaRetained || rolesRemoved || dispositionStatus != "applied" {
		t.Fatalf("unexpected no-resource disposition: existed=%v credential=%v schema=%v roles=%v status=%q",
			resourceExisted, credentialRevoked, schemaRetained, rolesRemoved, dispositionStatus)
	}
	var executableRevokedBy, frontendRevokedBy int64
	var executableReason string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(revoked_by_user_id, 0), revocation_reason
		FROM extension_trust_grants WHERE id = $1 AND revoked_at IS NOT NULL
	`, trustGrantID).Scan(&executableRevokedBy, &executableReason); err != nil {
		t.Fatalf("executable trust evidence was not retained: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(revoked_by_user_id, 0)
		FROM extension_frontend_trust_grants WHERE id = $1 AND revoked_at IS NOT NULL
	`, frontendGrantID).Scan(&frontendRevokedBy); err != nil {
		t.Fatalf("frontend trust evidence was not retained: %v", err)
	}
	if executableRevokedBy != actorUserID || frontendRevokedBy != actorUserID || executableReason != "uninstall" {
		t.Fatalf("revocation actor executable=%d frontend=%d reason=%q", executableRevokedBy, frontendRevokedBy, executableReason)
	}
	var liveExecutable, liveFrontend, pageBindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_trust_grants WHERE extension_id = $1 AND revoked_at IS NULL`, extensionID).Scan(&liveExecutable); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM extension_frontend_trust_grants WHERE extension_id = $1 AND revoked_at IS NULL`, extensionID).Scan(&liveFrontend); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM page_provider_bindings WHERE extension_id = $1`, extensionID).Scan(&pageBindings); err != nil {
		t.Fatal(err)
	}
	if liveExecutable != 0 || liveFrontend != 0 || pageBindings != 0 {
		t.Fatalf("live executable=%d frontend=%d page bindings=%d", liveExecutable, liveFrontend, pageBindings)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM extensions WHERE id = $1`, extensionID).Scan(new(string)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("extension identity still exists: %v", err)
	}
	if _, err := os.Stat(packagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package still exists: %v", err)
	}

}
