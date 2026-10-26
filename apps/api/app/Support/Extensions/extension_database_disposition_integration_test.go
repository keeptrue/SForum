package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestPostgresExtensionDatabaseDispositionAppliesEveryModeAndReplays(t *testing.T) {
	tests := []struct {
		name           string
		mode           LifecycleBoundaryCleanupMode
		removalMode    string
		disposition    string
		schemaRetained bool
	}{
		{"preserve", LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve, extensionDatabaseDispositionPreserved, true},
		{"export then remove", LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove, extensionDatabaseDispositionExportedThenRemoved, false},
		{"complete removal", LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete, extensionDatabaseDispositionRemoved, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleCleanupIntegration(
				t, extensions.LifecycleMachineUninstall, test.mode, test.removalMode,
			)
			artifact := lifecycleCleanupDispositionArtifact(t, h)
			identifiers, credential, live := provisionExtensionDatabaseDispositionFixture(t, h, artifact)
			defer live.Close(context.Background())
			cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
			migrationRole := ""
			if test.mode == LifecycleBoundaryCleanupComplete {
				migrationRole = insertExtensionDatabaseDispositionMigrationRole(t, h, artifact, identifiers)
			}
			staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
			if err != nil {
				t.Fatal(err)
			}
			h.completeOperation(t, extensions.LifecycleTerminalSucceeded)

			request := ExtensionDatabaseDispositionRequest{
				CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
				CleanupMode: h.mode, Artifact: artifact,
				ExportArtifactID: h.exportArtifactID, ExportDigest: h.exportDigest,
			}
			wantRolesRemoved := !test.schemaRetained
			disposition := NewPostgresExtensionDatabaseDisposition(h.pool)
			receipt, err := disposition.ApplyLifecycleDataDisposition(h.ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.CleanupID != request.CleanupID || receipt.OperationID != request.OperationID ||
				receipt.Artifact != artifact || receipt.DataDisposition != test.disposition ||
				!receipt.CredentialRevoked || receipt.SchemaRetained != test.schemaRetained ||
				receipt.RolesRemoved != wantRolesRemoved || !receipt.ResourceExisted || receipt.ReceiptID == "" ||
				!validLifecycleCleanupDigest(receipt.ProofDigest) || len(receipt.Proof) == 0 {
				t.Fatalf("unexpected disposition receipt: %#v", receipt)
			}
			if test.mode == LifecycleBoundaryCleanupExport && receipt.ExportEvidenceDigest == "" {
				t.Fatalf("export evidence digest missing: %#v", receipt)
			}
			if _, err := live.Exec(context.Background(), `SELECT 1`); err == nil {
				t.Fatal("active plugin database session survived disposition")
			}
			if stale, staleErr := connectExtensionDatabaseCredential(h.ctx, h.pool, credential); staleErr == nil {
				_ = stale.Close(h.ctx)
				t.Fatal("disposed plugin database credential opened a new session")
			}

			assertExtensionDatabaseDispositionPhysicalState(
				t, h.pool, identifiers, test.schemaRetained,
			)
			if migrationRole != "" {
				var migrationRolePresent bool
				if err := h.pool.QueryRow(h.ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, migrationRole).Scan(&migrationRolePresent); err != nil {
					t.Fatal(err)
				}
				if migrationRolePresent {
					t.Fatalf("scoped migration role %q survived disposition", migrationRole)
				}
				var proof extensionDatabaseDispositionProof
				if err := json.Unmarshal(receipt.Proof, &proof); err != nil {
					t.Fatal(err)
				}
				if !containsSortedString(proof.Resource.MigrationRolesPresentBefore, migrationRole) ||
					len(proof.Resource.MigrationRolesPresentAfter) != 0 {
					t.Fatalf("migration role proof = %#v", proof.Resource)
				}
			}
			if test.schemaRetained {
				var retained string
				query := `SELECT note FROM ` + pgx.Identifier{identifiers.Schema, "disposition_probe"}.Sanitize() + ` WHERE id = 1`
				if err := h.pool.QueryRow(h.ctx, query).Scan(&retained); err != nil || retained != "retained" {
					t.Fatalf("preserved schema data = %q, %v", retained, err)
				}
			}
			var activeCredentials, activeGrants int
			if err := h.pool.QueryRow(h.ctx, `
				SELECT
				  (SELECT count(*) FROM extension_database_credentials WHERE extension_id = $1 AND status = 'active'),
				  (SELECT count(*) FROM extension_database_grants WHERE extension_id = $1 AND status = 'active')
			`, artifact.ExtensionID).Scan(&activeCredentials, &activeGrants); err != nil {
				t.Fatal(err)
			}
			if activeCredentials != 0 || activeGrants != 0 {
				t.Fatalf("active credential/grant rows remain: %d/%d", activeCredentials, activeGrants)
			}

			replayed, err := NewPostgresExtensionDatabaseDisposition(h.pool).
				ApplyLifecycleDataDisposition(h.ctx, request)
			if err != nil || replayed.ReceiptID != receipt.ReceiptID ||
				replayed.ProofDigest != receipt.ProofDigest || string(replayed.Proof) != string(receipt.Proof) {
				t.Fatalf("restart replay = %#v, %v", replayed, err)
			}
			var rows int
			if err := h.pool.QueryRow(h.ctx, `
				SELECT count(*) FROM extension_database_dispositions WHERE operation_id = $1
			`, request.OperationID).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("disposition rows = %d, %v", rows, err)
			}
		})
	}
}

func TestPostgresExtensionDatabaseDispositionPersistsTruthfulNoResourceReceipts(t *testing.T) {
	tests := []struct {
		name        string
		mode        LifecycleBoundaryCleanupMode
		removalMode string
	}{
		{"preserve", LifecycleBoundaryCleanupPreserve, extensions.LifecycleRemovalPreserve},
		{"export", LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove},
		{"complete", LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLifecycleCleanupIntegration(t, extensions.LifecycleMachineUninstall, test.mode, test.removalMode)
			artifact := lifecycleCleanupDispositionArtifact(t, h)
			identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
			if err != nil {
				t.Fatal(err)
			}
			cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
			staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
			if err != nil {
				t.Fatal(err)
			}
			h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
			receipt, err := NewPostgresExtensionDatabaseDisposition(h.pool).ApplyLifecycleDataDisposition(
				h.ctx,
				ExtensionDatabaseDispositionRequest{
					CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
					CleanupMode: h.mode, Artifact: artifact,
					ExportArtifactID: h.exportArtifactID, ExportDigest: h.exportDigest,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ResourceExisted || receipt.SchemaRetained || receipt.RolesRemoved ||
				!receipt.CredentialRevoked || receipt.ReceiptID == "" {
				t.Fatalf("no-resource receipt claimed physical work: %#v", receipt)
			}
			var resourceExisted, schemaRetained, rolesRemoved bool
			if err := h.pool.QueryRow(h.ctx, `
				SELECT resource_existed, schema_retained, roles_removed
				FROM extension_database_dispositions WHERE operation_id = $1
			`, h.request.OperationID).Scan(&resourceExisted, &schemaRetained, &rolesRemoved); err != nil {
				t.Fatal(err)
			}
			if resourceExisted || schemaRetained || rolesRemoved {
				t.Fatalf("persisted no-resource facts = %v/%v/%v", resourceExisted, schemaRetained, rolesRemoved)
			}
		})
	}
}

func TestPostgresExtensionDatabaseDispositionResumesPreparedAndConvergesConcurrentCalls(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete,
	)
	artifact := lifecycleCleanupDispositionArtifact(t, h)
	identifiers, _, live := provisionExtensionDatabaseDispositionFixture(t, h, artifact)
	defer live.Close(context.Background())
	cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
	staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
	if err != nil {
		t.Fatal(err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
	request := ExtensionDatabaseDispositionRequest{
		CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
		CleanupMode: h.mode, Artifact: artifact,
	}

	connection, err := acquireExtensionDatabaseSessionLock(h.ctx, h.pool, identifiers.LockKey)
	if err != nil {
		t.Fatal(err)
	}
	prepared, applied, err := prepareExtensionDatabaseDisposition(h.ctx, connection, request, identifiers)
	releaseExtensionDatabaseSessionLock(connection, identifiers.LockKey)
	if err != nil || applied || prepared.Status != "prepared" {
		t.Fatalf("prepared crash window = %#v/%v/%v", prepared, applied, err)
	}

	const workers = 8
	var wg sync.WaitGroup
	receipts := make(chan ExtensionDatabaseDispositionReceipt, workers)
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, applyErr := NewPostgresExtensionDatabaseDisposition(h.pool).
				ApplyLifecycleDataDisposition(h.ctx, request)
			receipts <- receipt
			errs <- applyErr
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var receiptID, proofDigest string
	for receipt := range receipts {
		if receiptID == "" {
			receiptID, proofDigest = receipt.ReceiptID, receipt.ProofDigest
		}
		if receipt.ReceiptID != receiptID || receipt.ProofDigest != proofDigest {
			t.Fatalf("concurrent receipts diverged: %#v", receipt)
		}
	}
	assertExtensionDatabaseDispositionPhysicalState(t, h.pool, identifiers, false)
}

func TestPostgresExtensionDatabaseDispositionRejectsUnexpectedRoleMemberBeforeMutation(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupComplete, extensions.LifecycleRemovalComplete,
	)
	artifact := lifecycleCleanupDispositionArtifact(t, h)
	identifiers, _, live := provisionExtensionDatabaseDispositionFixture(t, h, artifact)
	defer live.Close(context.Background())
	cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
	staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
	if err != nil {
		t.Fatal(err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
	highRole := fmt.Sprintf("sforum_ext_disposition_high_%x", time.Now().UnixNano())
	if len(highRole) > 63 {
		highRole = highRole[:63]
	}
	quotedHigh := pgx.Identifier{highRole}.Sanitize()
	quotedRuntime := pgx.Identifier{identifiers.RuntimeRole}.Sanitize()
	if _, err := h.pool.Exec(h.ctx, `CREATE ROLE `+quotedHigh+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `GRANT `+quotedRuntime+` TO `+quotedHigh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `REVOKE `+quotedRuntime+` FROM `+quotedHigh)
		_, _ = h.pool.Exec(context.Background(), `DROP OWNED BY `+quotedHigh)
		_, _ = h.pool.Exec(context.Background(), `DROP ROLE IF EXISTS `+quotedHigh)
	})

	_, err = NewPostgresExtensionDatabaseDisposition(h.pool).ApplyLifecycleDataDisposition(
		h.ctx,
		ExtensionDatabaseDispositionRequest{
			CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
			CleanupMode: h.mode, Artifact: artifact,
		},
	)
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("unexpected role member error = %v", err)
	}
	var canLogin bool
	if err := h.pool.QueryRow(h.ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, identifiers.RuntimeRole).Scan(&canLogin); err != nil {
		t.Fatal(err)
	}
	if !canLogin {
		t.Fatal("failed disposition mutated runtime credential before role validation")
	}
	var status string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT status FROM extension_database_dispositions WHERE operation_id = $1
	`, h.request.OperationID).Scan(&status); err != nil || status != "prepared" {
		t.Fatalf("failed disposition status = %q, %v", status, err)
	}
}

func TestPostgresExtensionDatabaseDispositionRejectsTamperedExportEvidence(t *testing.T) {
	h := newLifecycleCleanupIntegration(
		t, extensions.LifecycleMachineUninstall,
		LifecycleBoundaryCleanupExport, extensions.LifecycleRemovalExportThenRemove,
	)
	artifact := lifecycleCleanupDispositionArtifact(t, h)
	identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	cleanupExtensionDatabaseDispositionFixture(t, h, artifact, identifiers)
	staged, err := h.cleanup.StageLifecycleHostCleanup(h.ctx, h.request, h.mode)
	if err != nil {
		t.Fatal(err)
	}
	h.completeOperation(t, extensions.LifecycleTerminalSucceeded)
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_lifecycle_cleanup_records
		SET export_evidence_digest = $2
		WHERE operation_id = $1
	`, h.request.OperationID, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	_, err = NewPostgresExtensionDatabaseDisposition(h.pool).ApplyLifecycleDataDisposition(
		h.ctx,
		ExtensionDatabaseDispositionRequest{
			CleanupID: staged.TombstoneID, OperationID: h.request.OperationID,
			CleanupMode: h.mode, Artifact: artifact,
			ExportArtifactID: h.exportArtifactID, ExportDigest: h.exportDigest,
		},
	)
	if !errors.Is(err, ErrExtensionDatabaseDispositionConflict) {
		t.Fatalf("tampered export evidence error = %v", err)
	}
	var rows int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM extension_database_dispositions WHERE operation_id = $1
	`, h.request.OperationID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("tampered export created disposition rows = %d, %v", rows, err)
	}
}

func lifecycleCleanupDispositionArtifact(t *testing.T, h *lifecycleCleanupIntegration) ExtensionDatabaseArtifact {
	t.Helper()
	if h.request.SourceExtension == nil {
		t.Fatal("cleanup source artifact is missing")
	}
	return ExtensionDatabaseArtifact{
		ExtensionID:   h.request.SourceExtension.ID,
		Version:       h.request.SourceExtension.Version,
		VersionID:     h.request.SourceExtension.ActiveVersionID,
		PackageDigest: h.request.SourceExtension.PackageDigest,
	}
}

func provisionExtensionDatabaseDispositionFixture(
	t *testing.T,
	h *lifecycleCleanupIntegration,
	artifact ExtensionDatabaseArtifact,
) (ExtensionDatabaseIdentifiers, ExtensionDatabaseCredential, *pgx.Conn) {
	t.Helper()
	identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	databaseName, err := ensureExtensionDatabaseResources(h.ctx, tx, artifact.ExtensionID, identifiers)
	if err != nil {
		t.Fatal(err)
	}
	request := ExtensionDatabaseGrantRequest{
		Artifact: artifact, ActorUserID: h.actorUserID, AuditEventID: h.auditEventID,
	}
	grant, err := upsertExtensionDatabaseGrant(h.ctx, tx, request, extensions.ManifestDatabase{
		ContractVersion: "fixture.database@1", Authority: "own_schema",
		Schema: "fixture", Role: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	password := strings.Repeat("A", 43)
	if err := activateExtensionDatabaseRuntimeRole(h.ctx, tx, identifiers, databaseName, password); err != nil {
		t.Fatal(err)
	}
	if err := insertExtensionDatabaseCredential(
		h.ctx, tx, grant.ID, request, identifiers.RuntimeRole, 1, strings.Repeat("b", 64),
	); err != nil {
		t.Fatal(err)
	}
	if err := activateExtensionDatabaseGrantCredential(h.ctx, tx, grant, 1); err != nil {
		t.Fatal(err)
	}
	if err := markExtensionDatabaseResourceProvisioned(h.ctx, tx, artifact.ExtensionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(h.ctx); err != nil {
		t.Fatal(err)
	}
	credential := ExtensionDatabaseCredential{
		GrantID: grant.ID, ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.Version,
		PackageDigest: artifact.PackageDigest, SchemaName: identifiers.Schema,
		OwnerRoleName: identifiers.OwnerRole, RoleName: identifiers.RuntimeRole,
		DatabaseName: databaseName, SearchPath: identifiers.Schema + ",pg_catalog",
		Password: password, CredentialRevision: 1,
	}
	live, err := connectExtensionDatabaseCredential(h.ctx, h.pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(h.ctx, `CREATE TABLE disposition_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(h.ctx, `INSERT INTO disposition_probe (id, note) VALUES (1, 'retained')`); err != nil {
		t.Fatal(err)
	}
	return identifiers, credential, live
}

func insertExtensionDatabaseDispositionMigrationRole(
	t *testing.T,
	h *lifecycleCleanupIntegration,
	artifact ExtensionDatabaseArtifact,
	identifiers ExtensionDatabaseIdentifiers,
) string {
	t.Helper()
	planDigest := sha256Bytes([]byte("disposition migration role " + artifact.ExtensionID))
	targetMigrationsDigest := sha256Bytes([]byte("target migrations " + artifact.ExtensionID))
	dryRunDigest := sha256Bytes([]byte("dry run " + artifact.ExtensionID))
	requestFingerprint := sha256Bytes([]byte("install operation " + artifact.ExtensionID))
	var operationID int64
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, operation, state,
			plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot, requested_by_user_id, audit_event_id,
			terminal_result, completed_at
		) VALUES (
			$1, $2, $3, 'install', 'enabled',
			'disposition.migration-role@1', $4, $5,
			'builtin', '{}'::jsonb, $6, $7,
			'succeeded', statement_timestamp()
		)
		RETURNING id
	`, artifact.ExtensionID, artifact.Version, artifact.PackageDigest,
		"disposition:migration-role:"+artifact.ExtensionID, requestFingerprint,
		h.actorUserID, h.auditEventID).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO extension_database_migration_plans (
			operation_id, operation, migration_mode, step_id, attempt, plan_digest,
			extension_id, target_extension_version_id, target_extension_version,
			target_package_digest, target_migrations_digest,
			schema_name, owner_role_name, dry_run_digest,
			status, total_steps, target_ready, completed_at
		) VALUES (
			$1, 'install', 'install', 'disposition.migration-role', 1, $2,
			$3, $4, $5,
			$6, $7,
			$8, $9, $10,
			'succeeded', 0, true, statement_timestamp()
		)
	`, operationID, planDigest, artifact.ExtensionID, artifact.VersionID, artifact.Version,
		artifact.PackageDigest, targetMigrationsDigest, identifiers.Schema, identifiers.OwnerRole,
		dryRunDigest); err != nil {
		t.Fatal(err)
	}
	roleName, err := ExtensionDatabaseMigrationRoleFor(artifact.ExtensionID, planDigest)
	if err != nil {
		t.Fatal(err)
	}
	quotedRole := pgx.Identifier{roleName}.Sanitize()
	quotedOwner := pgx.Identifier{identifiers.OwnerRole}.Sanitize()
	if _, err := h.pool.Exec(h.ctx, `CREATE ROLE `+quotedRole+` NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `GRANT `+quotedOwner+` TO `+quotedRole); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = h.pool.Exec(ctx, `REVOKE `+quotedOwner+` FROM `+quotedRole)
		_, _ = h.pool.Exec(ctx, `DROP OWNED BY `+quotedRole)
		_, _ = h.pool.Exec(ctx, `DROP ROLE IF EXISTS `+quotedRole)
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_database_migration_plans WHERE operation_id = $1`, operationID)
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_lifecycle_operations WHERE id = $1`, operationID)
	})
	return roleName
}

func cleanupExtensionDatabaseDispositionFixture(
	t *testing.T,
	h *lifecycleCleanupIntegration,
	artifact ExtensionDatabaseArtifact,
	identifiers ExtensionDatabaseIdentifiers,
) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_database_dispositions WHERE operation_id = $1`, h.request.OperationID)
		_, _ = h.pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, identifiers.RuntimeRole)
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_database_credentials WHERE extension_id = $1`, artifact.ExtensionID)
		// grant_powers.grant_id 是 ON DELETE RESTRICT：不先删 powers，
		// 下面两条 DELETE 会静默失败并留下 grants/resources 残留。
		_, _ = h.pool.Exec(ctx, `
			DELETE FROM extension_database_grant_powers
			WHERE grant_id IN (SELECT id FROM extension_database_grants WHERE extension_id = $1)
		`, artifact.ExtensionID)
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_database_grants WHERE extension_id = $1`, artifact.ExtensionID)
		_, _ = h.pool.Exec(ctx, `DELETE FROM extension_database_resources WHERE extension_id = $1`, artifact.ExtensionID)
		_, _ = h.pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{identifiers.Schema}.Sanitize()+` CASCADE`)
		for _, roleName := range []string{identifiers.RuntimeRole, identifiers.OwnerRole} {
			quoted := pgx.Identifier{roleName}.Sanitize()
			_, _ = h.pool.Exec(ctx, `DROP OWNED BY `+quoted)
			_, _ = h.pool.Exec(ctx, `DROP ROLE IF EXISTS `+quoted)
		}
	})
}

func assertExtensionDatabaseDispositionPhysicalState(
	t *testing.T,
	pool *pgxpool.Pool,
	identifiers ExtensionDatabaseIdentifiers,
	retained bool,
) {
	t.Helper()
	var schema, owner, runtime bool
	if err := pool.QueryRow(context.Background(), `
		SELECT
		  EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1),
		  EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2),
		  EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $3)
	`, identifiers.Schema, identifiers.OwnerRole, identifiers.RuntimeRole).Scan(&schema, &owner, &runtime); err != nil {
		t.Fatal(err)
	}
	if schema != retained || owner != retained || runtime {
		t.Fatalf("physical state schema/owner/runtime = %v/%v/%v, retained=%v", schema, owner, runtime, retained)
	}
}
