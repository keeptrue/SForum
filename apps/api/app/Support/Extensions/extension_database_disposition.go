package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const extensionDatabaseDispositionProofSchema = "sforum.extension-database-disposition-proof@1"

const (
	extensionDatabaseDispositionPreserved           = "preserved"
	extensionDatabaseDispositionExportedThenRemoved = "exported_then_removed"
	extensionDatabaseDispositionRemoved             = "removed"
)

var (
	ErrExtensionDatabaseDispositionInvalid  = errors.New("extension database disposition input is invalid")
	ErrExtensionDatabaseDispositionConflict = errors.New("extension database disposition exact fence conflict")
)

// ExtensionDatabaseDisposition is the only supported physical database cleanup
// boundary. Lifecycle purgers must not reproduce its private Registry SQL.
//
// ApplyLifecycleDataDisposition 的 exact-artifact 授予校验只回答「本次要抹除的
// artifact 自己还挂着 active grant 吗」，不再替调用方判定「有没有别的 artifact
// 的 grant 还活着」。调用方必须先完成 exact identity fence（例如 purger 的
// lockExactExtension 比对 extensions.active_version_id，以及 disposition 之后
// extension 级撤销 + active==0 断言），否则新的调用者可能跳过那道防线。
type ExtensionDatabaseDisposition interface {
	ApplyLifecycleDataDisposition(
		context.Context,
		ExtensionDatabaseDispositionRequest,
	) (ExtensionDatabaseDispositionReceipt, error)
}

type ExtensionDatabaseDispositionRequest struct {
	CleanupID        string
	OperationID      int64
	CleanupMode      LifecycleBoundaryCleanupMode
	Artifact         ExtensionDatabaseArtifact
	ExportArtifactID string
	ExportDigest     string
}

type ExtensionDatabaseDispositionReceipt struct {
	CleanupID            string
	OperationID          int64
	CleanupMode          LifecycleBoundaryCleanupMode
	Artifact             ExtensionDatabaseArtifact
	ExportArtifactID     string
	ExportDigest         string
	ExportEvidenceDigest string
	DataDisposition      string
	CredentialRevoked    bool
	SchemaRetained       bool
	// RolesRemoved means owner, runtime, and migration roles are all absent.
	// Preserve mode keeps the NOLOGIN owner with its retained schema.
	RolesRemoved    bool
	ResourceExisted bool
	ReceiptID       string
	Proof           json.RawMessage
	ProofDigest     string
}

type PostgresExtensionDatabaseDisposition struct {
	pool *pgxpool.Pool
}

func NewPostgresExtensionDatabaseDisposition(pool *pgxpool.Pool) *PostgresExtensionDatabaseDisposition {
	return &PostgresExtensionDatabaseDisposition{pool: pool}
}

func (d *PostgresExtensionDatabaseDisposition) ApplyLifecycleDataDisposition(
	ctx context.Context,
	request ExtensionDatabaseDispositionRequest,
) (ExtensionDatabaseDispositionReceipt, error) {
	if d == nil || d.pool == nil || ctx == nil {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionInvalid
	}
	if err := ctx.Err(); err != nil {
		return ExtensionDatabaseDispositionReceipt{}, err
	}
	if err := validateExtensionDatabaseDispositionRequest(request); err != nil {
		return ExtensionDatabaseDispositionReceipt{}, err
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(request.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionInvalid
	}

	connection, err := acquireExtensionDatabaseSessionLock(ctx, d.pool, identifiers.LockKey)
	if err != nil {
		return ExtensionDatabaseDispositionReceipt{}, fmt.Errorf("lock extension database disposition: %w", err)
	}
	defer releaseExtensionDatabaseSessionLock(connection, identifiers.LockKey)

	record, applied, err := prepareExtensionDatabaseDisposition(ctx, connection, request, identifiers)
	if err != nil {
		return ExtensionDatabaseDispositionReceipt{}, err
	}
	if applied {
		return extensionDatabaseDispositionReceipt(record)
	}
	record, err = applyPreparedExtensionDatabaseDisposition(ctx, connection, request, identifiers, record)
	if err != nil {
		return ExtensionDatabaseDispositionReceipt{}, err
	}
	return extensionDatabaseDispositionReceipt(record)
}

func validateExtensionDatabaseDispositionRequest(request ExtensionDatabaseDispositionRequest) error {
	if !validLifecycleCleanupOpaqueID(request.CleanupID) || request.OperationID <= 0 ||
		validateExtensionDatabaseArtifact(request.Artifact) != nil {
		return ErrExtensionDatabaseDispositionInvalid
	}
	switch request.CleanupMode {
	case LifecycleBoundaryCleanupPreserve, LifecycleBoundaryCleanupComplete:
		if request.ExportArtifactID != "" || request.ExportDigest != "" {
			return ErrExtensionDatabaseDispositionInvalid
		}
	case LifecycleBoundaryCleanupExport:
		if !validLifecycleCleanupOpaqueID(request.ExportArtifactID) ||
			!validLifecycleCleanupDigest(request.ExportDigest) {
			return ErrExtensionDatabaseDispositionInvalid
		}
	default:
		return ErrExtensionDatabaseDispositionInvalid
	}
	return nil
}

func extensionDatabaseDispositionForMode(mode LifecycleBoundaryCleanupMode) (string, error) {
	switch mode {
	case LifecycleBoundaryCleanupPreserve:
		return extensionDatabaseDispositionPreserved, nil
	case LifecycleBoundaryCleanupExport:
		return extensionDatabaseDispositionExportedThenRemoved, nil
	case LifecycleBoundaryCleanupComplete:
		return extensionDatabaseDispositionRemoved, nil
	default:
		return "", ErrExtensionDatabaseDispositionInvalid
	}
}

type extensionDatabaseDispositionProofArtifact struct {
	ExtensionID   string `json:"extensionId"`
	Version       string `json:"version"`
	VersionID     int64  `json:"versionId"`
	PackageDigest string `json:"packageDigest"`
}

type extensionDatabaseDispositionProofOperation struct {
	Revision     int64     `json:"revision"`
	CompletedAt  time.Time `json:"completedAt"`
	ActorUserID  *int64    `json:"actorUserId"`
	AuditEventID *int64    `json:"auditEventId"`
}

type extensionDatabaseDispositionProofExport struct {
	ArtifactID     string `json:"artifactId"`
	Digest         string `json:"digest"`
	EvidenceDigest string `json:"evidenceDigest"`
}

type extensionDatabaseDispositionProofResource struct {
	Existed                     bool     `json:"existed"`
	SchemaName                  string   `json:"schemaName"`
	OwnerRoleName               string   `json:"ownerRoleName"`
	RuntimeRoleName             string   `json:"runtimeRoleName"`
	SchemaPresentBefore         bool     `json:"schemaPresentBefore"`
	SchemaPresentAfter          bool     `json:"schemaPresentAfter"`
	OwnerRolePresentBefore      bool     `json:"ownerRolePresentBefore"`
	OwnerRolePresentAfter       bool     `json:"ownerRolePresentAfter"`
	RuntimeRolePresentBefore    bool     `json:"runtimeRolePresentBefore"`
	RuntimeRolePresentAfter     bool     `json:"runtimeRolePresentAfter"`
	MigrationRoles              []string `json:"migrationRoles"`
	MigrationRolesPresentBefore []string `json:"migrationRolesPresentBefore"`
	MigrationRolesPresentAfter  []string `json:"migrationRolesPresentAfter"`
}

type extensionDatabaseDispositionProof struct {
	Schema            string                                     `json:"schema"`
	ReceiptID         string                                     `json:"receiptId"`
	CleanupID         string                                     `json:"cleanupId"`
	OperationID       int64                                      `json:"operationId"`
	CleanupMode       LifecycleBoundaryCleanupMode               `json:"cleanupMode"`
	Artifact          extensionDatabaseDispositionProofArtifact  `json:"artifact"`
	Operation         extensionDatabaseDispositionProofOperation `json:"operation"`
	Resource          extensionDatabaseDispositionProofResource  `json:"resource"`
	Export            *extensionDatabaseDispositionProofExport   `json:"export,omitempty"`
	DataDisposition   string                                     `json:"dataDisposition"`
	CredentialRevoked bool                                       `json:"credentialRevoked"`
	SchemaRetained    bool                                       `json:"schemaRetained"`
	RolesRemoved      bool                                       `json:"rolesRemoved"`
}

func extensionDatabaseDispositionReceiptID(request ExtensionDatabaseDispositionRequest) string {
	material := strings.Join([]string{
		"sforum:extension-database-disposition-receipt@1",
		request.CleanupID,
		strconv.FormatInt(request.OperationID, 10),
		string(request.CleanupMode),
		request.Artifact.ExtensionID,
		request.Artifact.Version,
		strconv.FormatInt(request.Artifact.VersionID, 10),
		request.Artifact.PackageDigest,
		request.ExportArtifactID,
		request.ExportDigest,
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "database-disposition-" + hex.EncodeToString(digest[:])
}

func canonicalExtensionDatabaseDispositionProof(
	proof extensionDatabaseDispositionProof,
) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(proof)
	if err != nil {
		return nil, "", fmt.Errorf("encode extension database disposition proof: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func extensionDatabaseDispositionReceipt(
	record extensionDatabaseDispositionRecord,
) (ExtensionDatabaseDispositionReceipt, error) {
	if record.Status != "applied" || !record.ReceiptID.Valid || !record.ProofDigest.Valid ||
		len(record.Proof) == 0 {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionConflict
	}
	var proof extensionDatabaseDispositionProof
	if err := json.Unmarshal(record.Proof, &proof); err != nil {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionConflict
	}
	canonical, digest, err := canonicalExtensionDatabaseDispositionProof(proof)
	if err != nil || digest != record.ProofDigest.String || proof.Schema != extensionDatabaseDispositionProofSchema ||
		proof.ReceiptID != record.ReceiptID.String || proof.CleanupID != record.CleanupID ||
		proof.OperationID != record.OperationID || proof.CleanupMode != record.Mode ||
		proof.DataDisposition != record.DataDisposition.String ||
		proof.CredentialRevoked != record.CredentialRevoked ||
		proof.SchemaRetained != record.SchemaRetained.Bool ||
		proof.RolesRemoved != record.RolesRemoved || proof.Resource.Existed != record.ResourceExisted {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionConflict
	}
	artifact := ExtensionDatabaseArtifact{
		ExtensionID: record.ExtensionID, Version: record.ExtensionVersion,
		VersionID: record.ExtensionVersionID, PackageDigest: record.PackageDigest,
	}
	if proof.Artifact != (extensionDatabaseDispositionProofArtifact{
		ExtensionID: artifact.ExtensionID, Version: artifact.Version,
		VersionID: artifact.VersionID, PackageDigest: artifact.PackageDigest,
	}) {
		return ExtensionDatabaseDispositionReceipt{}, ErrExtensionDatabaseDispositionConflict
	}
	return ExtensionDatabaseDispositionReceipt{
		CleanupID: record.CleanupID, OperationID: record.OperationID, CleanupMode: record.Mode,
		Artifact: artifact, ExportArtifactID: record.ExportArtifactID.String,
		ExportDigest: record.ExportDigest.String, ExportEvidenceDigest: record.ExportEvidenceDigest.String,
		DataDisposition: record.DataDisposition.String, CredentialRevoked: record.CredentialRevoked,
		SchemaRetained: record.SchemaRetained.Bool, RolesRemoved: record.RolesRemoved,
		ResourceExisted: record.ResourceExisted, ReceiptID: record.ReceiptID.String,
		Proof: canonical, ProofDigest: digest,
	}, nil
}

var _ ExtensionDatabaseDisposition = (*PostgresExtensionDatabaseDisposition)(nil)
