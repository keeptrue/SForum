package bootstrap

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

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

const productionLifecycleCleanupProofSchema = "sforum.lifecycle.host-purge-proof@1"

type productionLifecycleRuntimeInspector interface {
	InspectRuntimeInstance(extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error)
}

type productionLifecycleCleanupPurger struct {
	pool          *pgxpool.Pool
	runtime       productionLifecycleRuntimeInspector
	extensionRoot string
	database      extensionsruntime.ExtensionDatabaseDisposition
}

func newProductionLifecycleCleanupPurger(
	pool *pgxpool.Pool,
	runtime productionLifecycleRuntimeInspector,
	extensionRoot string,
	database extensionsruntime.ExtensionDatabaseDisposition,
) (*productionLifecycleCleanupPurger, error) {
	if pool == nil || runtime == nil || strings.TrimSpace(extensionRoot) == "" || database == nil {
		return nil, errProductionLifecycleDependency
	}
	return &productionLifecycleCleanupPurger{
		pool: pool, runtime: runtime, extensionRoot: extensionRoot, database: database,
	}, nil
}

func (p *productionLifecycleCleanupPurger) PurgeLifecycleHostCleanup(
	ctx context.Context,
	request extensionsruntime.LifecycleCleanupPurgeRequest,
) (extensionsruntime.LifecycleCleanupPurgeReceipt, error) {
	if err := p.validateRequest(ctx, request); err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	identity, err := beginProductionLifecycleCleanupIdentity(ctx, p.pool, request)
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	defer identity.rollback()

	packageArtifact, err := inspectProductionLifecyclePackage(
		p.extensionRoot,
		request.RetainedPackagePath,
		request.RetainedPackageDigest,
	)
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	// 包先于 identity 删除；identity 已不存在时，重新出现的路径可能属于重装，必须拒绝。
	if !identity.present && packageArtifact.present {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, errProductionLifecycleCleanupConflict
	}
	if err := p.requireRuntimePurged(request.RetainedExtensionID, identity.runtimeInstanceID); err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}

	databaseReceipt, err := p.database.ApplyLifecycleDataDisposition(
		ctx,
		extensionsruntime.ExtensionDatabaseDispositionRequest{
			CleanupID: request.CleanupID, OperationID: request.OperationID,
			CleanupMode: request.CleanupMode,
			Artifact: extensionsruntime.ExtensionDatabaseArtifact{
				ExtensionID:   request.RetainedExtensionID,
				Version:       request.RetainedExtensionVersion,
				VersionID:     request.RetainedVersionID,
				PackageDigest: request.RetainedPackageDigest,
			},
			ExportArtifactID: request.ExportArtifactID, ExportDigest: request.ExportDigest,
		},
	)
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, fmt.Errorf("apply lifecycle database disposition: %w", err)
	}
	if err := validateProductionLifecycleDatabaseReceipt(request, databaseReceipt); err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	if identity.present {
		if err := packageArtifact.purge(); err != nil {
			return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
		}
	}
	if err := identity.commitPurge(ctx); err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, err
	}
	return productionLifecycleCleanupReceipt(request, databaseReceipt)
}

func (p *productionLifecycleCleanupPurger) validateRequest(
	ctx context.Context,
	request extensionsruntime.LifecycleCleanupPurgeRequest,
) error {
	if p == nil || p.pool == nil || p.runtime == nil || p.database == nil || ctx == nil ||
		strings.TrimSpace(p.extensionRoot) == "" || ctx.Err() != nil ||
		!validProductionLifecycleOpaqueID(request.CleanupID) || request.OperationID <= 0 ||
		request.RetainedExtensionID == "" ||
		request.RetainedExtensionID != strings.TrimSpace(request.RetainedExtensionID) ||
		request.RetainedExtensionVersion == "" ||
		request.RetainedExtensionVersion != strings.TrimSpace(request.RetainedExtensionVersion) ||
		request.RetainedVersionID <= 0 ||
		!validProductionLifecycleDigest(request.RetainedPackageDigest) ||
		request.RetainedPackagePath == "" ||
		request.RetainedPackagePath != strings.TrimSpace(request.RetainedPackagePath) {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return errProductionLifecycleCleanupConflict
	}
	switch request.CleanupMode {
	case extensionsruntime.LifecycleBoundaryCleanupPreserve:
		if !validProductionLifecycleOpaqueID(request.RetentionMarker) ||
			request.ExportArtifactID != "" || request.ExportDigest != "" {
			return errProductionLifecycleCleanupConflict
		}
	case extensionsruntime.LifecycleBoundaryCleanupExport:
		if request.RetentionMarker != "" || !validProductionLifecycleOpaqueID(request.ExportArtifactID) ||
			!validProductionLifecycleDigest(request.ExportDigest) {
			return errProductionLifecycleCleanupConflict
		}
	case extensionsruntime.LifecycleBoundaryCleanupComplete:
		if request.RetentionMarker != "" || request.ExportArtifactID != "" || request.ExportDigest != "" {
			return errProductionLifecycleCleanupConflict
		}
	default:
		return errProductionLifecycleCleanupConflict
	}
	return nil
}

func (p *productionLifecycleCleanupPurger) requireRuntimePurged(extensionID, instanceID string) error {
	_, err := p.runtime.InspectRuntimeInstance(extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: extensionID,
		InstanceID:  instanceID,
	})
	if errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect lifecycle runtime purge: %w", err)
	}
	return fmt.Errorf("%w: exact runtime instance is still present", errProductionLifecycleCleanupConflict)
}

func validateProductionLifecycleDatabaseReceipt(
	request extensionsruntime.LifecycleCleanupPurgeRequest,
	receipt extensionsruntime.ExtensionDatabaseDispositionReceipt,
) error {
	artifact := receipt.Artifact
	if receipt.CleanupID != request.CleanupID || receipt.OperationID != request.OperationID ||
		receipt.CleanupMode != request.CleanupMode ||
		artifact.ExtensionID != request.RetainedExtensionID ||
		artifact.Version != request.RetainedExtensionVersion ||
		artifact.VersionID != request.RetainedVersionID ||
		artifact.PackageDigest != request.RetainedPackageDigest ||
		receipt.ExportArtifactID != request.ExportArtifactID || receipt.ExportDigest != request.ExportDigest ||
		!receipt.CredentialRevoked || !validProductionLifecycleOpaqueID(receipt.ReceiptID) ||
		!validProductionLifecycleDigest(receipt.ProofDigest) ||
		len(bytes.TrimSpace(receipt.Proof)) == 0 || !json.Valid(receipt.Proof) {
		return errProductionLifecycleCleanupConflict
	}
	var proofObject map[string]json.RawMessage
	if err := json.Unmarshal(receipt.Proof, &proofObject); err != nil || len(proofObject) == 0 {
		return errProductionLifecycleCleanupConflict
	}
	proofDigest := sha256.Sum256(receipt.Proof)
	if hex.EncodeToString(proofDigest[:]) != receipt.ProofDigest {
		return errProductionLifecycleCleanupConflict
	}
	wantDisposition := ""
	wantSchemaRetained := false
	wantRolesRemoved := false
	switch request.CleanupMode {
	case extensionsruntime.LifecycleBoundaryCleanupPreserve:
		wantDisposition = "preserved"
		wantSchemaRetained = receipt.ResourceExisted
	case extensionsruntime.LifecycleBoundaryCleanupExport:
		wantDisposition = "exported_then_removed"
		wantRolesRemoved = receipt.ResourceExisted
	case extensionsruntime.LifecycleBoundaryCleanupComplete:
		wantDisposition = "removed"
		wantRolesRemoved = receipt.ResourceExisted
	default:
		return errProductionLifecycleCleanupConflict
	}
	if receipt.DataDisposition != wantDisposition || receipt.SchemaRetained != wantSchemaRetained ||
		receipt.RolesRemoved != wantRolesRemoved {
		return errProductionLifecycleCleanupConflict
	}
	if request.CleanupMode == extensionsruntime.LifecycleBoundaryCleanupExport {
		if !validProductionLifecycleDigest(receipt.ExportEvidenceDigest) {
			return errProductionLifecycleCleanupConflict
		}
	} else if receipt.ExportEvidenceDigest != "" {
		return errProductionLifecycleCleanupConflict
	}
	return nil
}

func productionLifecycleCleanupReceipt(
	request extensionsruntime.LifecycleCleanupPurgeRequest,
	database extensionsruntime.ExtensionDatabaseDispositionReceipt,
) (extensionsruntime.LifecycleCleanupPurgeReceipt, error) {
	proof := struct {
		Schema                string          `json:"schema"`
		CleanupID             string          `json:"cleanupId"`
		OperationID           int64           `json:"operationId"`
		ExtensionID           string          `json:"extensionId"`
		ExtensionVersion      string          `json:"extensionVersion"`
		VersionID             int64           `json:"versionId"`
		PackageDigest         string          `json:"packageDigest"`
		DatabaseReceiptID     string          `json:"databaseReceiptId"`
		DatabaseProofDigest   string          `json:"databaseProofDigest"`
		DatabaseProof         json.RawMessage `json:"databaseProof"`
		IdentityPurged        bool            `json:"identityPurged"`
		PackagePurged         bool            `json:"packagePurged"`
		RuntimeRecoveryPurged bool            `json:"runtimeRecoveryPurged"`
		DataDisposition       string          `json:"dataDisposition"`
	}{
		Schema:    productionLifecycleCleanupProofSchema,
		CleanupID: request.CleanupID, OperationID: request.OperationID,
		ExtensionID:      request.RetainedExtensionID,
		ExtensionVersion: request.RetainedExtensionVersion,
		VersionID:        request.RetainedVersionID, PackageDigest: request.RetainedPackageDigest,
		DatabaseReceiptID: database.ReceiptID, DatabaseProofDigest: database.ProofDigest,
		DatabaseProof:  database.Proof,
		IdentityPurged: true, PackagePurged: true, RuntimeRecoveryPurged: true,
		DataDisposition: database.DataDisposition,
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return extensionsruntime.LifecycleCleanupPurgeReceipt{}, fmt.Errorf("encode lifecycle host purge proof: %w", err)
	}
	receiptMaterial := sha256.Sum256([]byte(strings.Join([]string{
		request.CleanupID,
		fmt.Sprintf("%d", request.OperationID),
		request.RetainedPackageDigest,
		database.ReceiptID,
		database.ProofDigest,
	}, "\x00")))
	return extensionsruntime.LifecycleCleanupPurgeReceipt{
		CleanupID: request.CleanupID, OperationID: request.OperationID,
		CleanupMode: request.CleanupMode, RetainedPackageDigest: request.RetainedPackageDigest,
		ReceiptID:      "host-purge-" + hex.EncodeToString(receiptMaterial[:]),
		IdentityPurged: true, PackagePurged: true, RuntimeRecoveryPurged: true,
		DataDisposition: database.DataDisposition, Proof: encoded,
	}, nil
}

func validProductionLifecycleDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validProductionLifecycleOpaqueID(value string) bool {
	if value == "" || len(value) > 200 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' || char == ':' {
			continue
		}
		return false
	}
	return true
}

var _ extensionsruntime.LifecycleBoundaryCleanupPurger = (*productionLifecycleCleanupPurger)(nil)
