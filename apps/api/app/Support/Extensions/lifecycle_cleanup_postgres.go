package extensionsruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const (
	lifecycleCleanupRecordRetention = "retention"
	lifecycleCleanupRecordUninstall = "uninstall_tombstone"
	lifecycleCleanupStatusRetained  = "retained"
	lifecycleCleanupStatusPending   = "pending"
)

var (
	ErrLifecycleCleanupInvalid               = errors.New("extension lifecycle cleanup input is invalid")
	ErrLifecycleCleanupConflict              = errors.New("extension lifecycle cleanup exact fence conflict")
	ErrLifecycleCleanupExportEvidenceMissing = errors.New("extension lifecycle cleanup export evidence is missing")
)

// PostgresLifecycleBoundaryCleanup only persists cleanup intent and recovery
// evidence. It deliberately has no filesystem remover or extension store
// dependency, so staging can never delete an extension row or package.
type PostgresLifecycleBoundaryCleanup struct {
	pool *pgxpool.Pool
}

func NewPostgresLifecycleBoundaryCleanup(pool *pgxpool.Pool) *PostgresLifecycleBoundaryCleanup {
	return &PostgresLifecycleBoundaryCleanup{pool: pool}
}

func (c *PostgresLifecycleBoundaryCleanup) StageLifecycleHostCleanup(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryCleanupMode,
) (LifecycleBoundaryCleanupResult, error) {
	fence, err := lifecycleCleanupFenceFor(request, mode)
	if err != nil {
		return LifecycleBoundaryCleanupResult{}, err
	}
	if c == nil || c.pool == nil || ctx == nil {
		return LifecycleBoundaryCleanupResult{}, ErrLifecycleCleanupInvalid
	}
	if err := ctx.Err(); err != nil {
		return LifecycleBoundaryCleanupResult{}, err
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return LifecycleBoundaryCleanupResult{}, fmt.Errorf("begin lifecycle cleanup stage: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := validateLifecycleCleanupOperation(ctx, tx, fence); err != nil {
		return LifecycleBoundaryCleanupResult{}, err
	}
	if err := validateLifecycleCleanupRetainedArtifacts(ctx, tx, fence); err != nil {
		return LifecycleBoundaryCleanupResult{}, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extension_lifecycle_cleanup_records (
			cleanup_id, operation_id, operation, step_id, position,
			first_attempt, last_attempt, cleanup_mode, record_kind, status,
			retained_extension_id, retained_extension_version, retained_package_digest,
			retained_version_id, retained_runtime_instance_id, retained_package_path,
			identity_snapshot, package_snapshot, runtime_recovery_snapshot,
			runtime_recovery_attempts,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_package_path,
			retention_marker, export_artifact_id, export_digest,
			export_evidence_action, export_evidence, export_evidence_digest
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $6, $7, $8, $9,
			$10, $11, $12,
			$13, $14, $15,
			$16::jsonb, $17::jsonb, $18::jsonb,
			$19::jsonb,
			$20, $21, $22,
			$23, $24, $25,
			$26, $27, $28,
			$29, $30::jsonb, $31
		)
		ON CONFLICT (operation_id, step_id, cleanup_mode) DO NOTHING
	`, fence.CleanupID, fence.OperationID, fence.Operation, fence.StepID, fence.Position,
		fence.Attempt, fence.Mode, fence.RecordKind, fence.Status,
		fence.Retained.ExtensionID, fence.Retained.ExtensionVersion, fence.Retained.PackageDigest,
		fence.Retained.VersionID, fence.Retained.RuntimeInstanceID, fence.Retained.PackagePath,
		string(fence.IdentitySnapshot), string(fence.PackageSnapshot), string(fence.RuntimeRecoverySnapshot),
		string(fence.RuntimeAttempt),
		fence.Target.ExtensionID, fence.Target.ExtensionVersion, fence.Target.PackageDigest,
		fence.Target.VersionID, fence.Target.RuntimeInstanceID, fence.Target.PackagePath,
		nullableLifecycleCleanupString(fence.RetentionMarker),
		nullableLifecycleCleanupString(fence.ExportArtifactID),
		nullableLifecycleCleanupString(fence.ExportDigest),
		nullableLifecycleCleanupString(string(fence.ExportEvidenceAction)),
		nullableLifecycleCleanupJSON(fence.ExportEvidence),
		nullableLifecycleCleanupString(fence.ExportEvidenceDigest),
	)
	if err != nil {
		return LifecycleBoundaryCleanupResult{}, fmt.Errorf("insert lifecycle cleanup record: %w", err)
	}

	record, err := loadLifecycleCleanupRecord(ctx, tx, fence.OperationID, fence.StepID, fence.Mode, true)
	if err != nil {
		return LifecycleBoundaryCleanupResult{}, mapLifecycleCleanupLoadError("load staged lifecycle cleanup", err)
	}
	if !record.matchesFence(fence) || fence.Attempt < record.LastAttempt {
		return LifecycleBoundaryCleanupResult{}, ErrLifecycleCleanupConflict
	}
	if record.Status == lifecycleCleanupStatusPending || record.Status == lifecycleCleanupStatusRetained {
		if fence.Attempt > record.LastAttempt ||
			record.RetainedRuntimeInstanceID != fence.Retained.RuntimeInstanceID ||
			record.TargetRuntimeInstanceID != fence.Target.RuntimeInstanceID {
			tag, updateErr := tx.Exec(ctx, `
				UPDATE extension_lifecycle_cleanup_records
				SET last_attempt = $2,
				    retained_runtime_instance_id = $3,
				    target_runtime_instance_id = $4,
				    runtime_recovery_snapshot = $5::jsonb,
				    runtime_recovery_attempts = runtime_recovery_attempts || $6::jsonb,
				    revision = revision + 1,
				    updated_at = statement_timestamp()
				WHERE id = $1 AND revision = $7 AND last_attempt = $8
			`, record.ID, fence.Attempt, fence.Retained.RuntimeInstanceID,
				fence.Target.RuntimeInstanceID, string(fence.RuntimeRecoverySnapshot),
				string(fence.RuntimeAttempt), record.Revision, record.LastAttempt)
			if updateErr != nil {
				return LifecycleBoundaryCleanupResult{}, fmt.Errorf("advance lifecycle cleanup recovery attempt: %w", updateErr)
			}
			if tag.RowsAffected() != 1 {
				return LifecycleBoundaryCleanupResult{}, ErrLifecycleCleanupConflict
			}
			record.LastAttempt = fence.Attempt
			record.RetainedRuntimeInstanceID = fence.Retained.RuntimeInstanceID
			record.TargetRuntimeInstanceID = fence.Target.RuntimeInstanceID
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleBoundaryCleanupResult{}, fmt.Errorf("commit lifecycle cleanup stage: %w", err)
	}
	return lifecycleCleanupBoundaryResult(record), nil
}

type lifecycleCleanupArtifact struct {
	ExtensionID       string
	ExtensionVersion  string
	PackageDigest     string
	VersionID         int64
	RuntimeInstanceID string
	PackagePath       string
}

type lifecycleCleanupFence struct {
	CleanupID               string
	OperationID             int64
	Operation               extensions.LifecycleMachineOperation
	StepID                  string
	Position                int
	Attempt                 int
	Mode                    LifecycleBoundaryCleanupMode
	RecordKind              string
	Status                  string
	Retained                lifecycleCleanupArtifact
	Target                  lifecycleCleanupArtifact
	IdentitySnapshot        []byte
	PackageSnapshot         []byte
	RuntimeRecoverySnapshot []byte
	RuntimeAttempt          []byte
	RetentionMarker         string
	ExportArtifactID        string
	ExportDigest            string
	ExportEvidenceAction    extensions.LifecycleMachineAction
	ExportEvidence          []byte
	ExportEvidenceDigest    string
	ActorUserID             int64
	AuditEventID            int64
}

func lifecycleCleanupFenceFor(
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryCleanupMode,
) (lifecycleCleanupFence, error) {
	if request.OperationID <= 0 || request.Attempt <= 0 || request.ActorUserID <= 0 ||
		request.AuditEventID <= 0 || request.StepID == "" || request.StepID != strings.TrimSpace(request.StepID) ||
		len(request.StepID) > 512 {
		return lifecycleCleanupFence{}, ErrLifecycleCleanupInvalid
	}
	position, err := lifecycleCleanupPosition(request.Operation, mode, request.RemovalMode)
	if err != nil || request.Position != position {
		return lifecycleCleanupFence{}, ErrLifecycleCleanupInvalid
	}
	path, err := extensions.RecommendedLifecyclePath(request.Operation)
	if err != nil || position >= len(path) || path[position].Action != "" {
		return lifecycleCleanupFence{}, ErrLifecycleCleanupInvalid
	}
	expectedStepID := fmt.Sprintf("lifecycle.%s.%02d.host.%s", request.Operation, position, path[position].State)
	if request.StepID != expectedStepID || request.SourceExtension == nil {
		return lifecycleCleanupFence{}, ErrLifecycleCleanupInvalid
	}

	retained, err := lifecycleCleanupExactArtifact("retained source", *request.SourceExtension, request.SourceBinding, true)
	if err != nil {
		return lifecycleCleanupFence{}, err
	}
	target, err := lifecycleCleanupExactArtifact("target", request.TargetExtension, request.TargetBinding, false)
	if err != nil {
		return lifecycleCleanupFence{}, err
	}
	if retained.ExtensionID != target.ExtensionID {
		return lifecycleCleanupFence{}, fmt.Errorf("%w: source and target extension identities differ", ErrLifecycleCleanupInvalid)
	}

	cleanupID := lifecycleCleanupStableID(request.OperationID, request.StepID, mode)
	fence := lifecycleCleanupFence{
		CleanupID: cleanupID, OperationID: request.OperationID, Operation: request.Operation,
		StepID: request.StepID, Position: request.Position, Attempt: request.Attempt, Mode: mode,
		Retained: retained, Target: target, ActorUserID: request.ActorUserID, AuditEventID: request.AuditEventID,
	}
	if request.Operation == extensions.LifecycleMachineUninstall {
		fence.RecordKind, fence.Status = lifecycleCleanupRecordUninstall, lifecycleCleanupStatusPending
	} else {
		fence.RecordKind, fence.Status = lifecycleCleanupRecordRetention, lifecycleCleanupStatusRetained
	}
	if mode == LifecycleBoundaryCleanupPreserve {
		fence.RetentionMarker = "retained-data-" + cleanupID
	}
	if mode == LifecycleBoundaryCleanupExport {
		fence.ExportArtifactID, fence.ExportDigest, fence.ExportEvidenceAction,
			fence.ExportEvidence, fence.ExportEvidenceDigest, err = lifecycleCleanupExportEvidence(request)
		if err != nil {
			return lifecycleCleanupFence{}, err
		}
	}

	identitySnapshot := struct {
		ExtensionID string `json:"extensionId"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Status      string `json:"status"`
		Source      string `json:"source"`
		IsSystem    bool   `json:"isSystem"`
		IsDeletable bool   `json:"isDeletable"`
	}{retained.ExtensionID, request.SourceExtension.Name, request.SourceExtension.Type,
		request.SourceExtension.Status, request.SourceExtension.Source,
		request.SourceExtension.IsSystem, request.SourceExtension.IsDeletable}
	packageSnapshot := struct {
		ExtensionID              string `json:"extensionId"`
		Version                  string `json:"version"`
		PackageDigest            string `json:"packageDigest"`
		VersionID                int64  `json:"versionId"`
		PackagePath              string `json:"packagePath"`
		BackendProtocolVersion   int    `json:"backendProtocolVersion"`
		LifecycleContractVersion string `json:"lifecycleContractVersion"`
	}{retained.ExtensionID, retained.ExtensionVersion, retained.PackageDigest,
		retained.VersionID, retained.PackagePath, request.SourceExtension.Manifest.Backend.ProtocolVersion,
		request.SourceExtension.Manifest.Lifecycle.ContractVersion}
	runtimeSnapshot := struct {
		Operation extensions.LifecycleMachineOperation `json:"operation"`
		Mode      LifecycleBoundaryCleanupMode         `json:"cleanupMode"`
		Source    extensions.LifecycleRuntimeBinding   `json:"source"`
		Target    extensions.LifecycleRuntimeBinding   `json:"target"`
	}{request.Operation, mode, request.SourceBinding, request.TargetBinding}
	runtimeAttempt := []struct {
		Attempt int                                `json:"attempt"`
		Source  extensions.LifecycleRuntimeBinding `json:"source"`
		Target  extensions.LifecycleRuntimeBinding `json:"target"`
	}{{request.Attempt, request.SourceBinding, request.TargetBinding}}
	if fence.IdentitySnapshot, err = json.Marshal(identitySnapshot); err != nil {
		return lifecycleCleanupFence{}, fmt.Errorf("%w: encode identity snapshot", ErrLifecycleCleanupInvalid)
	}
	if fence.PackageSnapshot, err = json.Marshal(packageSnapshot); err != nil {
		return lifecycleCleanupFence{}, fmt.Errorf("%w: encode package snapshot", ErrLifecycleCleanupInvalid)
	}
	if fence.RuntimeRecoverySnapshot, err = json.Marshal(runtimeSnapshot); err != nil {
		return lifecycleCleanupFence{}, fmt.Errorf("%w: encode runtime recovery snapshot", ErrLifecycleCleanupInvalid)
	}
	if fence.RuntimeAttempt, err = json.Marshal(runtimeAttempt); err != nil {
		return lifecycleCleanupFence{}, fmt.Errorf("%w: encode runtime recovery attempt", ErrLifecycleCleanupInvalid)
	}
	return fence, nil
}

func lifecycleCleanupPosition(
	operation extensions.LifecycleMachineOperation,
	mode LifecycleBoundaryCleanupMode,
	removalMode string,
) (int, error) {
	switch {
	case operation == extensions.LifecycleMachineDisable && mode == LifecycleBoundaryCleanupDisable && removalMode == "":
		return 3, nil
	case operation == extensions.LifecycleMachineUpgrade && mode == LifecycleBoundaryCleanupRetiredSource && removalMode == "":
		return 10, nil
	case operation == extensions.LifecycleMachineRollback && mode == LifecycleBoundaryCleanupRetiredSource && removalMode == "":
		return 6, nil
	case operation == extensions.LifecycleMachineUninstall:
		expected, err := lifecycleBoundaryUninstallCleanupMode(removalMode)
		if err == nil && expected == mode {
			return 6, nil
		}
	}
	return 0, ErrLifecycleCleanupInvalid
}

func lifecycleCleanupExactArtifact(
	label string,
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
	requireRuntime bool,
) (lifecycleCleanupArtifact, error) {
	if err := validateExactCoordinatorArtifact(label, extension); err != nil ||
		extension.ActiveVersionID <= 0 || !validLifecycleCleanupDigest(extension.PackageDigest) ||
		extension.PackagePath == "" || extension.PackagePath != strings.TrimSpace(extension.PackagePath) {
		return lifecycleCleanupArtifact{}, fmt.Errorf("%w: %s artifact is incomplete", ErrLifecycleCleanupInvalid, label)
	}
	if err := validateExactCoordinatorBinding(label, binding, extension, requireRuntime); err != nil ||
		len(binding.RuntimeInstanceID) > 512 {
		return lifecycleCleanupArtifact{}, fmt.Errorf("%w: %s runtime binding is incomplete", ErrLifecycleCleanupInvalid, label)
	}
	return lifecycleCleanupArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: binding.RuntimeInstanceID, PackagePath: extension.PackagePath,
	}, nil
}

func lifecycleCleanupExportEvidence(
	request LifecycleBoundaryRequest,
) (string, string, extensions.LifecycleMachineAction, []byte, string, error) {
	for _, action := range []extensions.LifecycleMachineAction{
		extensions.LifecycleMachineUninstallAfter,
		extensions.LifecycleMachineUninstallStep,
	} {
		document, ok := request.ActionResult(action)
		if !ok || len(bytes.TrimSpace(document)) == 0 {
			continue
		}
		var evidence struct {
			ExportArtifactID string `json:"exportArtifactId"`
			ExportDigest     string `json:"exportDigest"`
		}
		if err := json.Unmarshal(document, &evidence); err != nil {
			return "", "", "", nil, "", fmt.Errorf("%w: decode %s result", ErrLifecycleCleanupInvalid, action)
		}
		if evidence.ExportArtifactID == "" && evidence.ExportDigest == "" {
			continue
		}
		if !validLifecycleCleanupOpaqueID(evidence.ExportArtifactID) ||
			!validLifecycleCleanupDigest(evidence.ExportDigest) {
			return "", "", "", nil, "", fmt.Errorf("%w: malformed %s export evidence", ErrLifecycleCleanupInvalid, action)
		}
		allowlisted, err := json.Marshal(struct {
			ExportArtifactID string `json:"exportArtifactId"`
			ExportDigest     string `json:"exportDigest"`
		}{evidence.ExportArtifactID, evidence.ExportDigest})
		if err != nil {
			return "", "", "", nil, "", ErrLifecycleCleanupInvalid
		}
		digest := sha256.Sum256(allowlisted)
		return evidence.ExportArtifactID, evidence.ExportDigest, action,
			allowlisted, hex.EncodeToString(digest[:]), nil
	}
	return "", "", "", nil, "", ErrLifecycleCleanupExportEvidenceMissing
}

func lifecycleCleanupStableID(operationID int64, stepID string, mode LifecycleBoundaryCleanupMode) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", operationID, stepID, mode)))
	return "lifecycle-cleanup-" + hex.EncodeToString(digest[:])
}

func validLifecycleCleanupOpaqueID(value string) bool {
	if !validLifecycleCleanupReference(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func nullableLifecycleCleanupString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableLifecycleCleanupJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

var _ LifecycleBoundaryCleanup = (*PostgresLifecycleBoundaryCleanup)(nil)
