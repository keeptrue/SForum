package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type extensionDatabaseDispositionFence struct {
	RecordID                  int64
	RecordRevision            int64
	CleanupID                 string
	OperationID               int64
	Mode                      LifecycleBoundaryCleanupMode
	CleanupStatus             string
	RetainedExtensionID       string
	RetainedExtensionVersion  string
	RetainedPackageDigest     string
	RetainedVersionID         int64
	TargetExtensionID         string
	TargetExtensionVersion    string
	TargetPackageDigest       string
	ExportArtifactID          sql.NullString
	ExportDigest              sql.NullString
	ExportEvidence            []byte
	ExportEvidenceDigest      sql.NullString
	Operation                 string
	OperationExtensionID      string
	OperationExtensionVersion string
	OperationPackageDigest    string
	OperationRemovalMode      string
	OperationTerminalResult   string
	OperationRevision         int64
	OperationCompletedAt      sql.NullTime
	ActorUserID               sql.NullInt64
	AuditEventID              sql.NullInt64
}

func loadExtensionDatabaseDispositionFence(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	operationID int64,
	forUpdate bool,
) (extensionDatabaseDispositionFence, error) {
	query := `
		SELECT cleanup.id, cleanup.revision, cleanup.cleanup_id, cleanup.operation_id,
		       cleanup.cleanup_mode, cleanup.status,
		       cleanup.retained_extension_id, cleanup.retained_extension_version,
		       cleanup.retained_package_digest, cleanup.retained_version_id,
		       cleanup.target_extension_id, cleanup.target_extension_version,
		       cleanup.target_package_digest,
		       cleanup.export_artifact_id, cleanup.export_digest,
		       cleanup.export_evidence, cleanup.export_evidence_digest,
		       operation.operation, operation.extension_id, operation.extension_version,
		       operation.package_digest, COALESCE(operation.removal_mode, ''),
		       COALESCE(operation.terminal_result, ''), operation.revision,
		       operation.completed_at, operation.requested_by_user_id, operation.audit_event_id
		FROM extension_lifecycle_cleanup_records AS cleanup
		JOIN extension_lifecycle_operations AS operation ON operation.id = cleanup.operation_id
		WHERE cleanup.operation_id = $1 AND cleanup.record_kind = 'uninstall_tombstone'
	`
	if forUpdate {
		query += ` FOR UPDATE OF cleanup, operation`
	}
	var fence extensionDatabaseDispositionFence
	err := querier.QueryRow(ctx, query, operationID).Scan(
		&fence.RecordID, &fence.RecordRevision, &fence.CleanupID, &fence.OperationID,
		&fence.Mode, &fence.CleanupStatus,
		&fence.RetainedExtensionID, &fence.RetainedExtensionVersion,
		&fence.RetainedPackageDigest, &fence.RetainedVersionID,
		&fence.TargetExtensionID, &fence.TargetExtensionVersion, &fence.TargetPackageDigest,
		&fence.ExportArtifactID, &fence.ExportDigest,
		&fence.ExportEvidence, &fence.ExportEvidenceDigest,
		&fence.Operation, &fence.OperationExtensionID, &fence.OperationExtensionVersion,
		&fence.OperationPackageDigest, &fence.OperationRemovalMode,
		&fence.OperationTerminalResult, &fence.OperationRevision,
		&fence.OperationCompletedAt, &fence.ActorUserID, &fence.AuditEventID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return extensionDatabaseDispositionFence{}, ErrLifecycleCleanupNotStaged
	}
	if err != nil {
		return extensionDatabaseDispositionFence{}, fmt.Errorf("load extension database disposition fence: %w", err)
	}
	return fence, nil
}

func (f extensionDatabaseDispositionFence) validate(request ExtensionDatabaseDispositionRequest) error {
	if f.CleanupID != request.CleanupID || f.OperationID != request.OperationID || f.Mode != request.CleanupMode ||
		f.RetainedExtensionID != request.Artifact.ExtensionID ||
		f.RetainedExtensionVersion != request.Artifact.Version ||
		f.RetainedPackageDigest != request.Artifact.PackageDigest ||
		f.RetainedVersionID != request.Artifact.VersionID {
		return ErrExtensionDatabaseDispositionConflict
	}
	if f.Operation != extensions.LifecycleOperationUninstall ||
		f.OperationTerminalResult != extensions.LifecycleTerminalSucceeded ||
		!f.OperationCompletedAt.Valid {
		return ErrLifecycleCleanupNotTerminal
	}
	if f.CleanupStatus != lifecycleCleanupStatusPending && f.CleanupStatus != "finalized" {
		return ErrExtensionDatabaseDispositionConflict
	}
	if f.TargetExtensionID != f.OperationExtensionID ||
		f.TargetExtensionVersion != f.OperationExtensionVersion ||
		f.TargetPackageDigest != f.OperationPackageDigest ||
		f.RetainedExtensionID != f.TargetExtensionID {
		return ErrExtensionDatabaseDispositionConflict
	}
	expectedMode, err := lifecycleBoundaryUninstallCleanupMode(f.OperationRemovalMode)
	if err != nil || expectedMode != f.Mode {
		return ErrExtensionDatabaseDispositionConflict
	}
	return f.validateExportEvidence(request)
}

func (f extensionDatabaseDispositionFence) validateExportEvidence(
	request ExtensionDatabaseDispositionRequest,
) error {
	if f.Mode != LifecycleBoundaryCleanupExport {
		if f.ExportArtifactID.Valid || f.ExportDigest.Valid ||
			f.ExportEvidenceDigest.Valid || len(f.ExportEvidence) != 0 ||
			request.ExportArtifactID != "" || request.ExportDigest != "" {
			return ErrExtensionDatabaseDispositionConflict
		}
		return nil
	}
	if !f.ExportArtifactID.Valid || !f.ExportDigest.Valid || !f.ExportEvidenceDigest.Valid ||
		f.ExportArtifactID.String != request.ExportArtifactID ||
		f.ExportDigest.String != request.ExportDigest || len(f.ExportEvidence) == 0 {
		return ErrExtensionDatabaseDispositionConflict
	}
	evidence := struct {
		ExportArtifactID string `json:"exportArtifactId"`
		ExportDigest     string `json:"exportDigest"`
	}{}
	if err := json.Unmarshal(f.ExportEvidence, &evidence); err != nil ||
		evidence.ExportArtifactID != request.ExportArtifactID ||
		evidence.ExportDigest != request.ExportDigest {
		return ErrExtensionDatabaseDispositionConflict
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return ErrExtensionDatabaseDispositionConflict
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != f.ExportEvidenceDigest.String {
		return ErrExtensionDatabaseDispositionConflict
	}
	return nil
}

type extensionDatabaseDispositionRecord struct {
	ID                   int64
	CleanupID            string
	OperationID          int64
	Mode                 LifecycleBoundaryCleanupMode
	ExtensionID          string
	ExtensionVersionID   int64
	ExtensionVersion     string
	PackageDigest        string
	SchemaName           string
	OwnerRoleName        string
	RuntimeRoleName      string
	ExportArtifactID     sql.NullString
	ExportDigest         sql.NullString
	ExportEvidenceDigest sql.NullString
	ResourceExisted      bool
	Status               string
	DataDisposition      sql.NullString
	CredentialRevoked    bool
	SchemaRetained       sql.NullBool
	RolesRemoved         bool
	ReceiptID            sql.NullString
	Proof                []byte
	ProofDigest          sql.NullString
	Revision             int64
}

func (r extensionDatabaseDispositionRecord) matchesRequest(
	request ExtensionDatabaseDispositionRequest,
	identifiers ExtensionDatabaseIdentifiers,
) bool {
	return r.CleanupID == request.CleanupID && r.OperationID == request.OperationID &&
		r.Mode == request.CleanupMode && r.ExtensionID == request.Artifact.ExtensionID &&
		r.ExtensionVersionID == request.Artifact.VersionID &&
		r.ExtensionVersion == request.Artifact.Version && r.PackageDigest == request.Artifact.PackageDigest &&
		r.SchemaName == identifiers.Schema && r.OwnerRoleName == identifiers.OwnerRole &&
		r.RuntimeRoleName == identifiers.RuntimeRole &&
		r.ExportArtifactID.String == request.ExportArtifactID &&
		r.ExportArtifactID.Valid == (request.ExportArtifactID != "") &&
		r.ExportDigest.String == request.ExportDigest && r.ExportDigest.Valid == (request.ExportDigest != "")
}

func loadExtensionDatabaseDispositionRecord(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	operationID int64,
	forUpdate bool,
) (extensionDatabaseDispositionRecord, error) {
	query := `
		SELECT id, cleanup_id, operation_id, cleanup_mode,
		       extension_id, extension_version_id, extension_version, package_digest,
		       schema_name, owner_role_name, runtime_role_name,
		       export_artifact_id, export_digest, export_evidence_digest,
		       resource_existed, status, data_disposition, credential_revoked,
		       schema_retained, roles_removed, receipt_id, proof, proof_digest, revision
		FROM extension_database_dispositions
		WHERE operation_id = $1
	`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var record extensionDatabaseDispositionRecord
	err := querier.QueryRow(ctx, query, operationID).Scan(
		&record.ID, &record.CleanupID, &record.OperationID, &record.Mode,
		&record.ExtensionID, &record.ExtensionVersionID, &record.ExtensionVersion,
		&record.PackageDigest, &record.SchemaName, &record.OwnerRoleName, &record.RuntimeRoleName,
		&record.ExportArtifactID, &record.ExportDigest, &record.ExportEvidenceDigest,
		&record.ResourceExisted, &record.Status, &record.DataDisposition,
		&record.CredentialRevoked, &record.SchemaRetained, &record.RolesRemoved,
		&record.ReceiptID, &record.Proof, &record.ProofDigest, &record.Revision,
	)
	return record, err
}

func prepareExtensionDatabaseDisposition(
	ctx context.Context,
	connection *pgxpool.Conn,
	request ExtensionDatabaseDispositionRequest,
	identifiers ExtensionDatabaseIdentifiers,
) (extensionDatabaseDispositionRecord, bool, error) {
	tx, err := connection.Begin(ctx)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("begin extension database disposition preparation: %w", err)
	}
	defer tx.Rollback(ctx)
	fence, err := loadExtensionDatabaseDispositionFence(ctx, tx, request.OperationID, true)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, false, err
	}
	if err := fence.validate(request); err != nil {
		return extensionDatabaseDispositionRecord{}, false, err
	}

	record, err := loadExtensionDatabaseDispositionRecord(ctx, tx, request.OperationID, true)
	if err == nil {
		if !record.matchesRequest(request, identifiers) ||
			record.ExportEvidenceDigest.String != fence.ExportEvidenceDigest.String ||
			record.ExportEvidenceDigest.Valid != fence.ExportEvidenceDigest.Valid {
			return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
		}
		if record.Status != "prepared" && record.Status != "applied" {
			return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
		}
		if fence.CleanupStatus == "finalized" && record.Status != "applied" {
			return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("commit existing extension database disposition: %w", err)
		}
		return record, record.Status == "applied", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("load extension database disposition: %w", err)
	}
	if fence.CleanupStatus != lifecycleCleanupStatusPending {
		return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
	}

	resource, resourceExisted, err := loadExtensionDatabaseDispositionResource(ctx, tx, request.Artifact.ExtensionID, true)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, false, err
	}
	if resourceExisted {
		if !resource.matches(identifiers) {
			return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseResourceConflict
		}
	} else if err := validateAbsentExtensionDatabaseResources(ctx, tx, identifiers); err != nil {
		return extensionDatabaseDispositionRecord{}, false, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extension_database_dispositions (
			cleanup_id, operation_id, cleanup_mode,
			extension_id, extension_version_id, extension_version, package_digest,
			schema_name, owner_role_name, runtime_role_name,
			export_artifact_id, export_digest, export_evidence_digest, resource_existed
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7,
			$8, $9, $10,
			$11, $12, $13, $14
		)
		ON CONFLICT DO NOTHING
	`, request.CleanupID, request.OperationID, request.CleanupMode,
		request.Artifact.ExtensionID, request.Artifact.VersionID,
		request.Artifact.Version, request.Artifact.PackageDigest,
		identifiers.Schema, identifiers.OwnerRole, identifiers.RuntimeRole,
		nullableLifecycleCleanupString(request.ExportArtifactID),
		nullableLifecycleCleanupString(request.ExportDigest),
		nullableLifecycleCleanupString(fence.ExportEvidenceDigest.String), resourceExisted)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("prepare extension database disposition: %w", err)
	}
	record, err = loadExtensionDatabaseDispositionRecord(ctx, tx, request.OperationID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
	}
	if err != nil {
		return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("load prepared extension database disposition: %w", err)
	}
	if !record.matchesRequest(request, identifiers) || record.ResourceExisted != resourceExisted ||
		record.ExportEvidenceDigest.String != fence.ExportEvidenceDigest.String ||
		record.ExportEvidenceDigest.Valid != fence.ExportEvidenceDigest.Valid || record.Status != "prepared" {
		return extensionDatabaseDispositionRecord{}, false, ErrExtensionDatabaseDispositionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return extensionDatabaseDispositionRecord{}, false, fmt.Errorf("commit extension database disposition preparation: %w", err)
	}
	return record, false, nil
}

func applyPreparedExtensionDatabaseDisposition(
	ctx context.Context,
	connection *pgxpool.Conn,
	request ExtensionDatabaseDispositionRequest,
	identifiers ExtensionDatabaseIdentifiers,
	prepared extensionDatabaseDispositionRecord,
) (extensionDatabaseDispositionRecord, error) {
	tx, err := connection.Begin(ctx)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, fmt.Errorf("begin extension database disposition application: %w", err)
	}
	defer tx.Rollback(ctx)
	fence, err := loadExtensionDatabaseDispositionFence(ctx, tx, request.OperationID, true)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, err
	}
	if err := fence.validate(request); err != nil {
		return extensionDatabaseDispositionRecord{}, err
	}
	record, err := loadExtensionDatabaseDispositionRecord(ctx, tx, request.OperationID, true)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, fmt.Errorf("lock prepared extension database disposition: %w", err)
	}
	if !record.matchesRequest(request, identifiers) || record.ID != prepared.ID ||
		record.ResourceExisted != prepared.ResourceExisted {
		return extensionDatabaseDispositionRecord{}, ErrExtensionDatabaseDispositionConflict
	}
	if record.Status == "applied" {
		if err := tx.Commit(ctx); err != nil {
			return extensionDatabaseDispositionRecord{}, err
		}
		return record, nil
	}
	if record.Status != "prepared" || fence.CleanupStatus != lifecycleCleanupStatusPending {
		return extensionDatabaseDispositionRecord{}, ErrExtensionDatabaseDispositionConflict
	}

	resourceProof, err := applyExtensionDatabasePhysicalDisposition(
		ctx, tx, request, identifiers, record.ResourceExisted, fence,
	)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, err
	}
	disposition, err := extensionDatabaseDispositionForMode(request.CleanupMode)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, err
	}
	receiptID := extensionDatabaseDispositionReceiptID(request)
	proof := extensionDatabaseDispositionProof{
		Schema: extensionDatabaseDispositionProofSchema, ReceiptID: receiptID,
		CleanupID: request.CleanupID, OperationID: request.OperationID, CleanupMode: request.CleanupMode,
		Artifact: extensionDatabaseDispositionProofArtifact{
			ExtensionID: request.Artifact.ExtensionID, Version: request.Artifact.Version,
			VersionID: request.Artifact.VersionID, PackageDigest: request.Artifact.PackageDigest,
		},
		Operation: extensionDatabaseDispositionProofOperation{
			Revision: fence.OperationRevision, CompletedAt: fence.OperationCompletedAt.Time,
			ActorUserID: nullableInt64Pointer(fence.ActorUserID), AuditEventID: nullableInt64Pointer(fence.AuditEventID),
		},
		Resource: resourceProof, DataDisposition: disposition, CredentialRevoked: true,
		SchemaRetained: request.CleanupMode == LifecycleBoundaryCleanupPreserve && record.ResourceExisted,
		RolesRemoved:   record.ResourceExisted && request.CleanupMode != LifecycleBoundaryCleanupPreserve,
	}
	if request.CleanupMode == LifecycleBoundaryCleanupExport {
		proof.Export = &extensionDatabaseDispositionProofExport{
			ArtifactID: request.ExportArtifactID, Digest: request.ExportDigest,
			EvidenceDigest: fence.ExportEvidenceDigest.String,
		}
	}
	canonicalProof, proofDigest, err := canonicalExtensionDatabaseDispositionProof(proof)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_dispositions
		SET status = 'applied', data_disposition = $2, credential_revoked = TRUE,
		    schema_retained = $3, roles_removed = $4,
		    receipt_id = $5, proof = $6::jsonb, proof_digest = $7,
		    applied_at = statement_timestamp(), revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND revision = $8 AND status = 'prepared'
	`, record.ID, disposition, proof.SchemaRetained, proof.RolesRemoved,
		receiptID, string(canonicalProof), proofDigest, record.Revision)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, fmt.Errorf("apply extension database disposition receipt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return extensionDatabaseDispositionRecord{}, ErrExtensionDatabaseDispositionConflict
	}
	record, err = loadExtensionDatabaseDispositionRecord(ctx, tx, request.OperationID, false)
	if err != nil {
		return extensionDatabaseDispositionRecord{}, fmt.Errorf("load applied extension database disposition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return extensionDatabaseDispositionRecord{}, fmt.Errorf("commit extension database disposition application: %w", err)
	}
	return record, nil
}

func loadExtensionDatabaseDispositionResource(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	extensionID string,
	forUpdate bool,
) (extensionDatabaseResourceRecord, bool, error) {
	record, err := loadExtensionDatabaseResource(ctx, querier, extensionID, forUpdate)
	if errors.Is(err, ErrExtensionDatabaseGrantNotFound) {
		return extensionDatabaseResourceRecord{}, false, nil
	}
	return record, err == nil, err
}

func nullableInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func dispositionRevocationActor(fence extensionDatabaseDispositionFence) (any, any) {
	if fence.ActorUserID.Valid && fence.ActorUserID.Int64 > 0 &&
		fence.AuditEventID.Valid && fence.AuditEventID.Int64 > 0 {
		return fence.ActorUserID.Int64, fence.AuditEventID.Int64
	}
	return nil, nil
}
