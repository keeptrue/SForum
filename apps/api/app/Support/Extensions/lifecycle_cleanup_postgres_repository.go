package extensionsruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func validateLifecycleCleanupOperation(ctx context.Context, tx pgx.Tx, fence lifecycleCleanupFence) error {
	var extensionID, extensionVersion, packageDigest, operation, removalMode, terminalResult string
	var completedAt sql.NullTime
	if err := tx.QueryRow(ctx, `
		SELECT extension_id, extension_version, package_digest, operation,
		       COALESCE(removal_mode, ''), COALESCE(terminal_result, ''), completed_at
		FROM extension_lifecycle_operations
		WHERE id = $1
		FOR UPDATE
	`, fence.OperationID).Scan(
		&extensionID, &extensionVersion, &packageDigest, &operation,
		&removalMode, &terminalResult, &completedAt,
	); err != nil {
		return mapLifecycleCleanupLoadError("lock lifecycle cleanup operation", err)
	}
	if extensionID != fence.Target.ExtensionID || extensionVersion != fence.Target.ExtensionVersion ||
		packageDigest != fence.Target.PackageDigest || operation != string(fence.Operation) ||
		completedAt.Valid || terminalResult != "" {
		return ErrLifecycleCleanupConflict
	}
	expectedRemoval := ""
	if fence.Operation == extensions.LifecycleMachineUninstall {
		switch fence.Mode {
		case LifecycleBoundaryCleanupPreserve:
			expectedRemoval = extensions.LifecycleRemovalPreserve
		case LifecycleBoundaryCleanupExport:
			expectedRemoval = extensions.LifecycleRemovalExportThenRemove
		case LifecycleBoundaryCleanupComplete:
			expectedRemoval = extensions.LifecycleRemovalComplete
		}
	}
	if removalMode != expectedRemoval {
		return ErrLifecycleCleanupConflict
	}

	var action, status string
	var actorUserID, auditEventID int64
	if err := tx.QueryRow(ctx, `
		SELECT lifecycle_action, status,
		       COALESCE(actor_user_id, 0), COALESCE(audit_event_id, 0)
		FROM extension_lifecycle_steps
		WHERE operation_id = $1 AND step_id = $2 AND attempt = $3
		FOR SHARE
	`, fence.OperationID, fence.StepID, fence.Attempt).Scan(
		&action, &status, &actorUserID, &auditEventID,
	); err != nil {
		return mapLifecycleCleanupLoadError("load lifecycle cleanup step", err)
	}
	if action != "host.gate" || (status != "running" && status != "waiting" && status != "succeeded") ||
		actorUserID != fence.ActorUserID || auditEventID != fence.AuditEventID {
		return ErrLifecycleCleanupConflict
	}
	return nil
}

func validateLifecycleCleanupRetainedArtifacts(ctx context.Context, tx pgx.Tx, fence lifecycleCleanupFence) error {
	for _, artifact := range []lifecycleCleanupArtifact{fence.Retained, fence.Target} {
		var extensionID, version, digest, packagePath string
		if err := tx.QueryRow(ctx, `
			SELECT extensions.id, extension_versions.version,
			       extension_versions.package_digest, extension_versions.package_path
			FROM extensions
			JOIN extension_versions
			  ON extension_versions.extension_id = extensions.id
			 AND extension_versions.id = $2
			WHERE extensions.id = $1
			FOR KEY SHARE OF extensions, extension_versions
		`, artifact.ExtensionID, artifact.VersionID).Scan(
			&extensionID, &version, &digest, &packagePath,
		); err != nil {
			return mapLifecycleCleanupLoadError("lock retained lifecycle artifact", err)
		}
		if extensionID != artifact.ExtensionID || version != artifact.ExtensionVersion ||
			digest != artifact.PackageDigest || packagePath != artifact.PackagePath {
			return ErrLifecycleCleanupConflict
		}
	}
	return nil
}

type lifecycleCleanupRecord struct {
	ID                        int64
	CleanupID                 string
	OperationID               int64
	Operation                 string
	StepID                    string
	Position                  int
	FirstAttempt              int
	LastAttempt               int
	Mode                      LifecycleBoundaryCleanupMode
	RecordKind                string
	Status                    string
	RetainedExtensionID       string
	RetainedExtensionVersion  string
	RetainedPackageDigest     string
	RetainedVersionID         int64
	RetainedRuntimeInstanceID string
	RetainedPackagePath       string
	TargetExtensionID         string
	TargetExtensionVersion    string
	TargetPackageDigest       string
	TargetVersionID           int64
	TargetRuntimeInstanceID   string
	TargetPackagePath         string
	RetentionMarker           sql.NullString
	ExportArtifactID          sql.NullString
	ExportDigest              sql.NullString
	ExportEvidenceAction      sql.NullString
	ExportEvidenceDigest      sql.NullString
	Revision                  int64
}

func (r lifecycleCleanupRecord) matchesFence(f lifecycleCleanupFence) bool {
	return r.CleanupID == f.CleanupID && r.OperationID == f.OperationID &&
		r.Operation == string(f.Operation) && r.StepID == f.StepID && r.Position == f.Position &&
		r.Mode == f.Mode && r.RecordKind == f.RecordKind &&
		r.RetainedExtensionID == f.Retained.ExtensionID &&
		r.RetainedExtensionVersion == f.Retained.ExtensionVersion &&
		r.RetainedPackageDigest == f.Retained.PackageDigest && r.RetainedVersionID == f.Retained.VersionID &&
		r.RetainedPackagePath == f.Retained.PackagePath &&
		r.TargetExtensionID == f.Target.ExtensionID && r.TargetExtensionVersion == f.Target.ExtensionVersion &&
		r.TargetPackageDigest == f.Target.PackageDigest && r.TargetVersionID == f.Target.VersionID &&
		r.TargetPackagePath == f.Target.PackagePath &&
		r.RetentionMarker.String == f.RetentionMarker && r.RetentionMarker.Valid == (f.RetentionMarker != "") &&
		r.ExportArtifactID.String == f.ExportArtifactID && r.ExportArtifactID.Valid == (f.ExportArtifactID != "") &&
		r.ExportDigest.String == f.ExportDigest && r.ExportDigest.Valid == (f.ExportDigest != "") &&
		r.ExportEvidenceAction.String == string(f.ExportEvidenceAction) &&
		r.ExportEvidenceAction.Valid == (f.ExportEvidenceAction != "") &&
		r.ExportEvidenceDigest.String == f.ExportEvidenceDigest &&
		r.ExportEvidenceDigest.Valid == (f.ExportEvidenceDigest != "")
}

func loadLifecycleCleanupRecord(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	operationID int64,
	stepID string,
	mode LifecycleBoundaryCleanupMode,
	forUpdate bool,
) (lifecycleCleanupRecord, error) {
	query := `
		SELECT id, cleanup_id, operation_id, operation, step_id, position,
		       first_attempt, last_attempt, cleanup_mode, record_kind, status,
		       retained_extension_id, retained_extension_version, retained_package_digest,
		       retained_version_id, retained_runtime_instance_id, retained_package_path,
		       target_extension_id, target_extension_version, target_package_digest,
		       target_version_id, target_runtime_instance_id, target_package_path,
		       retention_marker, export_artifact_id, export_digest,
		       export_evidence_action, export_evidence_digest, revision
		FROM extension_lifecycle_cleanup_records
		WHERE operation_id = $1 AND step_id = $2 AND cleanup_mode = $3
	`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var record lifecycleCleanupRecord
	err := querier.QueryRow(ctx, query, operationID, stepID, mode).Scan(
		&record.ID, &record.CleanupID, &record.OperationID, &record.Operation,
		&record.StepID, &record.Position, &record.FirstAttempt, &record.LastAttempt,
		&record.Mode, &record.RecordKind, &record.Status,
		&record.RetainedExtensionID, &record.RetainedExtensionVersion,
		&record.RetainedPackageDigest, &record.RetainedVersionID,
		&record.RetainedRuntimeInstanceID, &record.RetainedPackagePath,
		&record.TargetExtensionID, &record.TargetExtensionVersion,
		&record.TargetPackageDigest, &record.TargetVersionID,
		&record.TargetRuntimeInstanceID, &record.TargetPackagePath,
		&record.RetentionMarker, &record.ExportArtifactID, &record.ExportDigest,
		&record.ExportEvidenceAction, &record.ExportEvidenceDigest, &record.Revision,
	)
	return record, err
}

func lifecycleCleanupBoundaryResult(record lifecycleCleanupRecord) LifecycleBoundaryCleanupResult {
	return LifecycleBoundaryCleanupResult{
		DurableTombstone: record.RecordKind == lifecycleCleanupRecordUninstall,
		TombstoneID: func() string {
			if record.RecordKind == lifecycleCleanupRecordUninstall {
				return record.CleanupID
			}
			return ""
		}(),
		// IdentityRetained 在这里是墓碑创建时的语义（"还没 purge"），与 purge
		// receipt 的 identityPurged（"这次 purge 物理身份被删了没有"）不是同一个
		// 字段；两者在 retained 卸载的时序上互为镜像，不要合并。
		IdentityRetained: true, PackageRetained: true, RuntimeRecoveryRetained: true,
		RetentionMarker:  record.RetentionMarker.String,
		ExportArtifactID: record.ExportArtifactID.String, ExportDigest: record.ExportDigest.String,
	}
}

func mapLifecycleCleanupLoadError(action string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrLifecycleCleanupConflict, action)
	}
	return fmt.Errorf("%s: %w", action, err)
}
