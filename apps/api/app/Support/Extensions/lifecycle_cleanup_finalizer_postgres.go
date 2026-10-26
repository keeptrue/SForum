package extensionsruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

const lifecycleCleanupPurgeProofSchema = "sforum.lifecycle.cleanup-purge-proof@1"

var (
	ErrLifecycleCleanupNotStaged        = errors.New("extension lifecycle cleanup tombstone is not staged")
	ErrLifecycleCleanupNotTerminal      = errors.New("extension lifecycle cleanup operation has not succeeded")
	ErrLifecycleCleanupPurgeUnavailable = errors.New("extension lifecycle cleanup physical purger is unavailable")
	ErrLifecycleCleanupPurgeInvalid     = errors.New("extension lifecycle cleanup purge receipt is invalid")
)

// LifecycleCleanupPurgeRequest is an immutable exact-artifact instruction.
// Implementations must be idempotent by CleanupID and return the same durable
// receipt after a process or database-commit failure.
type LifecycleCleanupPurgeRequest struct {
	CleanupID                string
	OperationID              int64
	CleanupMode              LifecycleBoundaryCleanupMode
	RetainedExtensionID      string
	RetainedExtensionVersion string
	RetainedPackageDigest    string
	RetainedVersionID        int64
	RetainedPackagePath      string
	RetentionMarker          string
	ExportArtifactID         string
	ExportDigest             string
}

type LifecycleCleanupPurgeReceipt struct {
	CleanupID             string
	OperationID           int64
	CleanupMode           LifecycleBoundaryCleanupMode
	RetainedPackageDigest string
	ReceiptID             string
	IdentityPurged        bool
	PackagePurged         bool
	RuntimeRecoveryPurged bool
	DataDisposition       string
	Proof                 json.RawMessage
}

type LifecycleBoundaryCleanupPurger interface {
	PurgeLifecycleHostCleanup(context.Context, LifecycleCleanupPurgeRequest) (LifecycleCleanupPurgeReceipt, error)
}

type LifecycleCleanupFinalizationResult struct {
	CleanupID              string
	OperationID            int64
	Status                 string
	PhysicalPurgeCompleted bool
	PurgeReceiptID         string
	PurgeProofDigest       string
	FinalizedAt            *time.Time
}

// LifecycleBoundaryCleanupFinalizer may run only after the operation ledger is
// durably succeeded. "finalized" means the physical purger returned a valid
// exact receipt; terminal acknowledgement alone never advances the tombstone.
type LifecycleBoundaryCleanupFinalizer interface {
	FinalizeLifecycleHostCleanup(context.Context, int64) (LifecycleCleanupFinalizationResult, error)
}

type PostgresLifecycleBoundaryCleanupFinalizer struct {
	pool   *pgxpool.Pool
	purger LifecycleBoundaryCleanupPurger
}

func NewPostgresLifecycleBoundaryCleanupFinalizer(
	pool *pgxpool.Pool,
	purger LifecycleBoundaryCleanupPurger,
) *PostgresLifecycleBoundaryCleanupFinalizer {
	return &PostgresLifecycleBoundaryCleanupFinalizer{pool: pool, purger: purger}
}

func (f *PostgresLifecycleBoundaryCleanupFinalizer) FinalizeLifecycleHostCleanup(
	ctx context.Context,
	operationID int64,
) (LifecycleCleanupFinalizationResult, error) {
	if f == nil || f.pool == nil || ctx == nil || operationID <= 0 {
		return LifecycleCleanupFinalizationResult{}, ErrLifecycleCleanupInvalid
	}
	if err := ctx.Err(); err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}
	candidate, err := loadLifecycleCleanupFinalizationCandidate(ctx, f.pool, operationID, false)
	if err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}
	if err := candidate.validateTerminalSuccess(); err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}
	if candidate.Status == "finalized" {
		return candidate.result(), nil
	}
	if f.purger == nil {
		return LifecycleCleanupFinalizationResult{}, ErrLifecycleCleanupPurgeUnavailable
	}

	request := candidate.purgeRequest()
	receipt, err := f.purger.PurgeLifecycleHostCleanup(ctx, request)
	if err != nil {
		return LifecycleCleanupFinalizationResult{}, fmt.Errorf("purge lifecycle cleanup %s: %w", candidate.CleanupID, err)
	}
	proof, proofDigest, err := validateLifecycleCleanupPurgeReceipt(request, receipt)
	if err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return LifecycleCleanupFinalizationResult{}, fmt.Errorf("begin lifecycle cleanup finalization: %w", err)
	}
	defer tx.Rollback(ctx)
	locked, err := loadLifecycleCleanupFinalizationCandidate(ctx, tx, operationID, true)
	if err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}
	if err := locked.validateTerminalSuccess(); err != nil {
		return LifecycleCleanupFinalizationResult{}, err
	}
	if !locked.samePurgeFence(candidate) {
		return LifecycleCleanupFinalizationResult{}, ErrLifecycleCleanupConflict
	}
	if locked.Status == "finalized" {
		if locked.PurgeReceiptID.String != receipt.ReceiptID ||
			locked.PurgeProofDigest.String != proofDigest {
			return LifecycleCleanupFinalizationResult{}, ErrLifecycleCleanupConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return LifecycleCleanupFinalizationResult{}, fmt.Errorf("commit existing lifecycle cleanup finalization: %w", err)
		}
		return locked.result(), nil
	}

	var finalizedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE extension_lifecycle_cleanup_records
		SET status = 'finalized',
		    physical_identity_present = FALSE,
		    physical_package_present = FALSE,
		    physical_runtime_recovery_present = FALSE,
		    finalized_at = statement_timestamp(),
		    finalized_operation_revision = $2,
		    finalized_operation_completed_at = $3,
		    purge_receipt_id = $4,
		    purge_proof = $5::jsonb,
		    purge_proof_digest = $6,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND revision = $7 AND status = 'pending'
		RETURNING finalized_at
	`, locked.RecordID, locked.OperationRevision, locked.OperationCompletedAt.Time,
		receipt.ReceiptID, string(proof), proofDigest, locked.RecordRevision).Scan(&finalizedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LifecycleCleanupFinalizationResult{}, ErrLifecycleCleanupConflict
		}
		return LifecycleCleanupFinalizationResult{}, fmt.Errorf("finalize lifecycle cleanup tombstone: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		// The purger is idempotent. A retry can inspect the marker and either
		// return it or replay the same receipt after an unknown commit result.
		return LifecycleCleanupFinalizationResult{}, fmt.Errorf("commit lifecycle cleanup finalization: %w", err)
	}
	return LifecycleCleanupFinalizationResult{
		CleanupID: locked.CleanupID, OperationID: locked.OperationID, Status: "finalized",
		PhysicalPurgeCompleted: true, PurgeReceiptID: receipt.ReceiptID,
		PurgeProofDigest: proofDigest, FinalizedAt: &finalizedAt,
	}, nil
}

type lifecycleCleanupFinalizationCandidate struct {
	RecordID                  int64
	RecordRevision            int64
	CleanupID                 string
	OperationID               int64
	Mode                      LifecycleBoundaryCleanupMode
	Status                    string
	RetainedExtensionID       string
	RetainedExtensionVersion  string
	RetainedPackageDigest     string
	RetainedVersionID         int64
	RetainedPackagePath       string
	TargetExtensionID         string
	TargetExtensionVersion    string
	TargetPackageDigest       string
	RetentionMarker           sql.NullString
	ExportArtifactID          sql.NullString
	ExportDigest              sql.NullString
	FinalizedAt               sql.NullTime
	PurgeReceiptID            sql.NullString
	PurgeProofDigest          sql.NullString
	Operation                 string
	OperationExtensionID      string
	OperationExtensionVersion string
	OperationPackageDigest    string
	OperationRemovalMode      string
	OperationTerminalResult   string
	OperationRevision         int64
	OperationCompletedAt      sql.NullTime
}

func loadLifecycleCleanupFinalizationCandidate(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	operationID int64,
	forUpdate bool,
) (lifecycleCleanupFinalizationCandidate, error) {
	query := `
		SELECT cleanup.id, cleanup.revision, cleanup.cleanup_id, cleanup.operation_id,
		       cleanup.cleanup_mode, cleanup.status,
		       cleanup.retained_extension_id, cleanup.retained_extension_version,
		       cleanup.retained_package_digest, cleanup.retained_version_id,
		       cleanup.retained_package_path,
		       cleanup.target_extension_id, cleanup.target_extension_version,
		       cleanup.target_package_digest,
		       cleanup.retention_marker, cleanup.export_artifact_id, cleanup.export_digest,
		       cleanup.finalized_at, cleanup.purge_receipt_id, cleanup.purge_proof_digest,
		       operation.operation, operation.extension_id, operation.extension_version,
		       operation.package_digest, COALESCE(operation.removal_mode, ''),
		       COALESCE(operation.terminal_result, ''), operation.revision, operation.completed_at
		FROM extension_lifecycle_cleanup_records AS cleanup
		JOIN extension_lifecycle_operations AS operation ON operation.id = cleanup.operation_id
		WHERE cleanup.operation_id = $1 AND cleanup.record_kind = 'uninstall_tombstone'
	`
	if forUpdate {
		query += " FOR UPDATE OF cleanup, operation"
	}
	var item lifecycleCleanupFinalizationCandidate
	err := querier.QueryRow(ctx, query, operationID).Scan(
		&item.RecordID, &item.RecordRevision, &item.CleanupID, &item.OperationID,
		&item.Mode, &item.Status,
		&item.RetainedExtensionID, &item.RetainedExtensionVersion,
		&item.RetainedPackageDigest, &item.RetainedVersionID,
		&item.RetainedPackagePath,
		&item.TargetExtensionID, &item.TargetExtensionVersion,
		&item.TargetPackageDigest,
		&item.RetentionMarker, &item.ExportArtifactID, &item.ExportDigest,
		&item.FinalizedAt, &item.PurgeReceiptID, &item.PurgeProofDigest,
		&item.Operation, &item.OperationExtensionID, &item.OperationExtensionVersion,
		&item.OperationPackageDigest, &item.OperationRemovalMode,
		&item.OperationTerminalResult, &item.OperationRevision, &item.OperationCompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleCleanupFinalizationCandidate{}, ErrLifecycleCleanupNotStaged
	}
	if err != nil {
		return lifecycleCleanupFinalizationCandidate{}, fmt.Errorf("load lifecycle cleanup finalization candidate: %w", err)
	}
	return item, nil
}

func (c lifecycleCleanupFinalizationCandidate) validateTerminalSuccess() error {
	if c.Operation != extensions.LifecycleOperationUninstall ||
		c.OperationTerminalResult != extensions.LifecycleTerminalSucceeded ||
		!c.OperationCompletedAt.Valid {
		return ErrLifecycleCleanupNotTerminal
	}
	if c.Status != lifecycleCleanupStatusPending && c.Status != "finalized" {
		return ErrLifecycleCleanupConflict
	}
	if c.TargetExtensionID != c.OperationExtensionID ||
		c.TargetExtensionVersion != c.OperationExtensionVersion ||
		c.TargetPackageDigest != c.OperationPackageDigest {
		return ErrLifecycleCleanupConflict
	}
	expectedMode, err := lifecycleBoundaryUninstallCleanupMode(c.OperationRemovalMode)
	if err != nil || expectedMode != c.Mode {
		return ErrLifecycleCleanupConflict
	}
	if c.Status == "finalized" && (!c.FinalizedAt.Valid || !c.PurgeReceiptID.Valid || !c.PurgeProofDigest.Valid) {
		return ErrLifecycleCleanupConflict
	}
	return nil
}

func (c lifecycleCleanupFinalizationCandidate) purgeRequest() LifecycleCleanupPurgeRequest {
	return LifecycleCleanupPurgeRequest{
		CleanupID: c.CleanupID, OperationID: c.OperationID, CleanupMode: c.Mode,
		RetainedExtensionID:      c.RetainedExtensionID,
		RetainedExtensionVersion: c.RetainedExtensionVersion,
		RetainedPackageDigest:    c.RetainedPackageDigest, RetainedVersionID: c.RetainedVersionID,
		RetainedPackagePath: c.RetainedPackagePath, RetentionMarker: c.RetentionMarker.String,
		ExportArtifactID: c.ExportArtifactID.String, ExportDigest: c.ExportDigest.String,
	}
}

func (c lifecycleCleanupFinalizationCandidate) samePurgeFence(other lifecycleCleanupFinalizationCandidate) bool {
	return c.CleanupID == other.CleanupID && c.OperationID == other.OperationID &&
		c.Mode == other.Mode && c.RetainedExtensionID == other.RetainedExtensionID &&
		c.RetainedExtensionVersion == other.RetainedExtensionVersion &&
		c.RetainedPackageDigest == other.RetainedPackageDigest &&
		c.RetainedVersionID == other.RetainedVersionID && c.RetainedPackagePath == other.RetainedPackagePath &&
		c.TargetExtensionID == other.TargetExtensionID &&
		c.TargetExtensionVersion == other.TargetExtensionVersion &&
		c.TargetPackageDigest == other.TargetPackageDigest &&
		c.OperationRevision == other.OperationRevision &&
		c.OperationCompletedAt.Time.Equal(other.OperationCompletedAt.Time)
}

func (c lifecycleCleanupFinalizationCandidate) result() LifecycleCleanupFinalizationResult {
	finalizedAt := c.FinalizedAt.Time
	return LifecycleCleanupFinalizationResult{
		CleanupID: c.CleanupID, OperationID: c.OperationID, Status: c.Status,
		PhysicalPurgeCompleted: c.Status == "finalized",
		PurgeReceiptID:         c.PurgeReceiptID.String, PurgeProofDigest: c.PurgeProofDigest.String,
		FinalizedAt: &finalizedAt,
	}
}

func validateLifecycleCleanupPurgeReceipt(
	request LifecycleCleanupPurgeRequest,
	receipt LifecycleCleanupPurgeReceipt,
) ([]byte, string, error) {
	if receipt.CleanupID != request.CleanupID || receipt.OperationID != request.OperationID ||
		receipt.CleanupMode != request.CleanupMode ||
		receipt.RetainedPackageDigest != request.RetainedPackageDigest ||
		!validLifecycleCleanupOpaqueID(receipt.ReceiptID) ||
		!receipt.IdentityPurged || !receipt.PackagePurged || !receipt.RuntimeRecoveryPurged {
		return nil, "", ErrLifecycleCleanupPurgeInvalid
	}
	expectedDisposition := ""
	switch request.CleanupMode {
	case LifecycleBoundaryCleanupPreserve:
		expectedDisposition = "preserved"
	case LifecycleBoundaryCleanupExport:
		expectedDisposition = "exported_then_removed"
	case LifecycleBoundaryCleanupComplete:
		expectedDisposition = "removed"
	default:
		return nil, "", ErrLifecycleCleanupPurgeInvalid
	}
	if receipt.DataDisposition != expectedDisposition ||
		len(bytes.TrimSpace(receipt.Proof)) == 0 || !json.Valid(receipt.Proof) {
		return nil, "", ErrLifecycleCleanupPurgeInvalid
	}
	var proofObject map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Proof, &proofObject); err != nil || len(proofObject) == 0 {
		return nil, "", ErrLifecycleCleanupPurgeInvalid
	}
	providerProofDigestValue := sha256.Sum256(receipt.Proof)
	providerProofDigest := hex.EncodeToString(providerProofDigestValue[:])
	proof, err := json.Marshal(struct {
		Schema                string                       `json:"schema"`
		CleanupID             string                       `json:"cleanupId"`
		OperationID           int64                        `json:"operationId"`
		CleanupMode           LifecycleBoundaryCleanupMode `json:"cleanupMode"`
		RetainedPackageDigest string                       `json:"retainedPackageDigest"`
		ReceiptID             string                       `json:"receiptId"`
		IdentityPurged        bool                         `json:"identityPurged"`
		PackagePurged         bool                         `json:"packagePurged"`
		RuntimeRecoveryPurged bool                         `json:"runtimeRecoveryPurged"`
		DataDisposition       string                       `json:"dataDisposition"`
		RetentionMarker       string                       `json:"retentionMarker,omitempty"`
		ExportArtifactID      string                       `json:"exportArtifactId,omitempty"`
		ExportDigest          string                       `json:"exportDigest,omitempty"`
		ProviderProofDigest   string                       `json:"providerProofDigest"`
	}{
		lifecycleCleanupPurgeProofSchema, request.CleanupID, request.OperationID,
		request.CleanupMode, request.RetainedPackageDigest, receipt.ReceiptID,
		receipt.IdentityPurged, receipt.PackagePurged, receipt.RuntimeRecoveryPurged,
		receipt.DataDisposition, request.RetentionMarker,
		request.ExportArtifactID, request.ExportDigest, providerProofDigest,
	})
	if err != nil {
		return nil, "", ErrLifecycleCleanupPurgeInvalid
	}
	digest := sha256.Sum256(proof)
	return proof, hex.EncodeToString(digest[:]), nil
}

var _ LifecycleBoundaryCleanupFinalizer = (*PostgresLifecycleBoundaryCleanupFinalizer)(nil)
