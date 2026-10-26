package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

const (
	ExtensionDatabaseLeaseIssuerActor = "actor"
	ExtensionDatabaseLeaseIssuerHost  = "host"

	ExtensionDatabaseLeaseActive                           = "active"
	ExtensionDatabaseLeaseDraining                         = "draining"
	ExtensionDatabaseLeaseRevoked                          = "revoked"
	ExtensionDatabaseLeaseFailed                           = "failed"
	extensionDatabaseRuntimeLeaseIssuedAudit               = "extension.database_runtime_lease.issued"
	extensionDatabaseRuntimeLeaseRevokedAudit              = "extension.database_runtime_lease.revoked"
	extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode  = coreauthority.KernelCleanupPendingRevokeCode
	extensionDatabaseRuntimeLeaseCleanupPendingExpiredCode = coreauthority.KernelCleanupPendingExpiredCode

	extensionDatabaseRuntimeLeaseTTL             = 2 * time.Minute
	extensionDatabaseRuntimeLeaseConnectionLimit = 1
	extensionDatabaseMaximumLiveRuntimeLeases    = 8
)

func isExtensionDatabaseRuntimeLeaseCleanupPending(code string) bool {
	return code == extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode ||
		code == extensionDatabaseRuntimeLeaseCleanupPendingExpiredCode
}

var (
	ErrExtensionDatabaseRuntimeLeaseNotFound = errors.New("extension database runtime lease not found")
	ErrExtensionDatabaseRuntimeLeaseConflict = errors.New("extension database runtime lease conflict")
)

type ExtensionDatabaseLeaseAuthority struct {
	Kind         string
	ActorUserID  int64
	AuditEventID int64
}

type ExtensionDatabaseRuntimeLeaseIssue struct {
	Artifact          ExtensionDatabaseArtifact
	RuntimeInstanceID string
	Authority         ExtensionDatabaseLeaseAuthority
}

type ExtensionDatabaseRuntimeLeaseRef struct {
	Artifact          ExtensionDatabaseArtifact
	RuntimeInstanceID string
	LeaseID           string
}

type ExtensionDatabaseRuntimeCredential struct {
	LeaseID           string
	GrantID           int64
	Artifact          ExtensionDatabaseArtifact
	RuntimeInstanceID string
	Powers            []string
	SchemaName        string
	OwnerRoleName     string
	RoleName          string
	DatabaseName      string
	SearchPath        string
	ConnectionURL     string
	Password          string
	ExpiresAt         time.Time
	Revision          int64
}

type ExtensionDatabaseRuntimeLeaseSnapshot struct {
	ID                int64
	LeaseID           string
	GrantID           int64
	Artifact          ExtensionDatabaseArtifact
	RuntimeInstanceID string
	RoleName          string
	Status            string
	IssuerKind        string
	IssuedByUserID    int64
	IssueAuditEventID int64
	IssuedAt          time.Time
	LastHeartbeatAt   time.Time
	ExpiresAt         time.Time
	DrainingAt        *time.Time
	RevokedAt         *time.Time
	FailureCode       string
	Revision          int64
}

func (r *PostgresExtensionDatabaseRegistry) HeartbeatRuntimeLease(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	expectedRevision int64,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	if err := r.validateRuntimeLeaseRef(ctx, ref); err != nil || expectedRevision <= 0 {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(ref.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("begin runtime lease heartbeat: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	lease, err := loadExtensionDatabaseRuntimeLease(ctx, tx, ref, true)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if lease.Status != ExtensionDatabaseLeaseActive || lease.Revision != expectedRevision {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	powers, err := loadExtensionDatabaseGrantPowers(ctx, tx, lease.GrantID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) ||
		containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
			return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
		}
		declaration, err := loadExactExtensionDatabaseDeclaration(ctx, tx, lease.Artifact)
		if err != nil {
			return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
		}
		if !slices.Equal(extensionmanifest.DatabaseGrants(&declaration), powers) {
			return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseArtifactConflict
		}
		if err := validateExtensionDatabaseCoreCompatibility(ctx, tx, declaration, powers); err != nil {
			return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
		}
	}
	heartbeatAt, expiresAt, err := extensionDatabaseRuntimeLeaseWindow(ctx, tx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if !heartbeatAt.Before(lease.ExpiresAt) {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	if err := extendExtensionDatabaseRuntimeLeaseRole(ctx, tx, lease.RoleName, expiresAt); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	lease, err = heartbeatExtensionDatabaseRuntimeLease(ctx, tx, lease, heartbeatAt, expiresAt)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("commit runtime lease heartbeat: %w", err)
	}
	return lease, nil
}

func (r *PostgresExtensionDatabaseRegistry) BeginRuntimeLeaseDrain(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	expectedRevision int64,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	if err := r.validateRuntimeLeaseRef(ctx, ref); err != nil || expectedRevision <= 0 {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(ref.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("begin runtime lease drain: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	lease, err := loadExtensionDatabaseRuntimeLease(ctx, tx, ref, true)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if lease.Status == ExtensionDatabaseLeaseDraining && lease.Revision == expectedRevision {
		return lease, nil
	}
	if lease.Status != ExtensionDatabaseLeaseActive || lease.Revision != expectedRevision {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	lease, err = drainExtensionDatabaseRuntimeLease(ctx, tx, lease)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("commit runtime lease drain: %w", err)
	}
	return lease, nil
}

func (r *PostgresExtensionDatabaseRegistry) IssueRuntimeLease(
	ctx context.Context,
	request ExtensionDatabaseRuntimeLeaseIssue,
) (ExtensionDatabaseRuntimeCredential, error) {
	if err := r.validateRuntimeLeaseIssue(ctx, request); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	leaseID, err := r.newRuntimeLeaseID()
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("%w: generate lease id: %v", ErrExtensionDatabaseCredential, err)
	}
	password, fingerprint, err := r.newCredentialSecret()
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("%w: generate lease secret: %v", ErrExtensionDatabaseCredential, err)
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(request.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, ErrExtensionDatabaseRegistryInvalid
	}
	if _, err := r.reapExpiredRuntimeLeasesForExtension(
		ctx, request.Artifact.ExtensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("reconcile runtime leases before issue: %w", err)
	}
	roleName, err := ExtensionDatabaseRuntimeLeaseRoleFor(
		request.Artifact.ExtensionID, request.RuntimeInstanceID, leaseID,
	)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, ErrExtensionDatabaseRegistryInvalid
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("begin extension database runtime lease: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	declaration, err := loadExactExtensionDatabaseDeclaration(ctx, tx, request.Artifact)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	powers := extensionmanifest.DatabaseGrants(&declaration)
	if len(powers) == 0 {
		return ExtensionDatabaseRuntimeCredential{}, ErrExtensionDatabaseAuthority
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) ||
		containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
			return ExtensionDatabaseRuntimeCredential{}, err
		}
		if err := validateExtensionDatabaseCoreCompatibility(ctx, tx, declaration, powers); err != nil {
			return ExtensionDatabaseRuntimeCredential{}, err
		}
	}
	databaseName, err := ensureExtensionDatabaseResources(ctx, tx, request.Artifact.ExtensionID, identifiers)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("ensure runtime lease database resources: %w", err)
	}
	if err := ensureExtensionDatabaseRuntimeLeaseCleanupReady(ctx, tx, request.Artifact.ExtensionID); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	grant, err := r.resolveRuntimeLeaseGrant(ctx, tx, request, declaration)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("resolve runtime lease exact grant: %w", err)
	}
	if err := ensureExtensionDatabaseRuntimeLeaseCapacity(ctx, tx, request.Artifact.ExtensionID); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("reserve runtime lease capacity: %w", err)
	}
	issuedAt, expiresAt, err := extensionDatabaseRuntimeLeaseWindow(ctx, tx)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("read runtime lease window: %w", err)
	}
	searchPath, err := createExtensionDatabaseRuntimeLeaseRole(
		ctx, tx, identifiers, roleName, databaseName, powers, password, expiresAt,
	)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("create runtime lease role: %w", err)
	}
	request.Authority, err = materializeExtensionDatabaseRuntimeLeaseHostAudit(
		ctx, tx, request.Authority, extensionDatabaseRuntimeLeaseIssuedAudit,
		ExtensionDatabaseRuntimeLeaseRef{
			Artifact: request.Artifact, RuntimeInstanceID: request.RuntimeInstanceID, LeaseID: leaseID,
		},
	)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	lease, err := insertExtensionDatabaseRuntimeLease(
		ctx, tx, request, grant.ID, leaseID, roleName, fingerprint, issuedAt, expiresAt,
	)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	if err := markExtensionDatabaseResourceProvisioned(ctx, tx, request.Artifact.ExtensionID); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	credential := ExtensionDatabaseRuntimeCredential{
		LeaseID: lease.LeaseID, GrantID: grant.ID, Artifact: request.Artifact,
		RuntimeInstanceID: request.RuntimeInstanceID, Powers: append([]string(nil), powers...),
		SchemaName: identifiers.Schema, OwnerRoleName: identifiers.OwnerRole, RoleName: roleName,
		DatabaseName: databaseName, SearchPath: searchPath, Password: password,
		ExpiresAt: expiresAt, Revision: lease.Revision,
	}
	credential.ConnectionURL, err = extensionDatabaseRuntimeConnectionURL(r.pool.Config().ConnConfig, credential)
	if err != nil {
		return ExtensionDatabaseRuntimeCredential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseRuntimeCredential{}, fmt.Errorf("commit extension database runtime lease: %w", err)
	}
	return credential, nil
}

func (r *PostgresExtensionDatabaseRegistry) RevokeRuntimeLease(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	authority ExtensionDatabaseLeaseAuthority,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	if err := r.validateRuntimeLeaseRef(ctx, ref); err != nil || !validExtensionDatabaseLeaseAuthority(authority) {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	lease, err := r.fenceRuntimeLeaseForRevoke(ctx, ref, authority)
	if err != nil || lease.Status == ExtensionDatabaseLeaseRevoked {
		return lease, err
	}
	if !isExtensionDatabaseRuntimeLeaseCleanupPending(lease.FailureCode) {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	lease, _, err = r.cleanupRuntimeLease(ctx, ref)
	return lease, err
}

func (r *PostgresExtensionDatabaseRegistry) fenceRuntimeLeaseForRevoke(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
	authority ExtensionDatabaseLeaseAuthority,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	identifiers, err := ExtensionDatabaseIdentifiersFor(ref.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("begin runtime lease revoke: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	lease, err := loadExtensionDatabaseRuntimeLease(ctx, tx, ref, true)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if lease.Status == ExtensionDatabaseLeaseRevoked {
		return lease, nil
	}
	if lease.Status == ExtensionDatabaseLeaseFailed {
		if isExtensionDatabaseRuntimeLeaseCleanupPending(lease.FailureCode) {
			return lease, nil
		}
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	authority, err = materializeExtensionDatabaseRuntimeLeaseHostAudit(
		ctx, tx, authority, extensionDatabaseRuntimeLeaseRevokedAudit, ref,
	)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if err := disableExtensionDatabaseRuntimeLeaseRole(ctx, tx, lease.RoleName, databaseName); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	lease, err = markExtensionDatabaseRuntimeLeaseRevokeCleanupPending(ctx, tx, lease, authority)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, fmt.Errorf("commit runtime lease authority fence: %w", err)
	}
	return lease, nil
}

func (r *PostgresExtensionDatabaseRegistry) cleanupRuntimeLease(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
) (ExtensionDatabaseRuntimeLeaseSnapshot, bool, error) {
	identifiers, err := ExtensionDatabaseIdentifiersFor(ref.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, ErrExtensionDatabaseRegistryInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, fmt.Errorf("begin runtime lease cleanup: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, err
	}
	lease, err := loadExtensionDatabaseRuntimeLease(ctx, tx, ref, true)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, err
	}
	if lease.Status == ExtensionDatabaseLeaseRevoked ||
		lease.Status == ExtensionDatabaseLeaseFailed && lease.FailureCode == extensionDatabaseRuntimeLeaseExpiredCode {
		return lease, false, nil
	}
	if lease.Status != ExtensionDatabaseLeaseFailed || !isExtensionDatabaseRuntimeLeaseCleanupPending(lease.FailureCode) {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, ErrExtensionDatabaseRuntimeLeaseConflict
	}
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, err
	}
	powers, err := loadExtensionDatabaseGrantPowers(ctx, tx, lease.GrantID)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, err
	}
	if err := cleanupExtensionDatabaseRuntimeLeaseRole(
		ctx, tx, lease.RoleName, identifiers.OwnerRole, identifiers.Schema, databaseName, powers,
	); err != nil {
		return lease, false, err
	}
	lease, err = finalizeExtensionDatabaseRuntimeLeaseCleanup(ctx, tx, lease)
	if err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, false, fmt.Errorf("commit runtime lease cleanup: %w", err)
	}
	return lease, true, nil
}

func (r *PostgresExtensionDatabaseRegistry) InspectRuntimeLease(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
) (ExtensionDatabaseRuntimeLeaseSnapshot, error) {
	if err := r.validateRuntimeLeaseRef(ctx, ref); err != nil {
		return ExtensionDatabaseRuntimeLeaseSnapshot{}, err
	}
	return loadExtensionDatabaseRuntimeLease(ctx, r.pool, ref, false)
}

func (r *PostgresExtensionDatabaseRegistry) resolveRuntimeLeaseGrant(
	ctx context.Context,
	tx pgx.Tx,
	request ExtensionDatabaseRuntimeLeaseIssue,
	declaration extensionmanifest.ManifestDatabase,
) (extensionDatabaseGrantRecord, error) {
	grant, err := loadExtensionDatabaseGrant(ctx, tx, request.Artifact, true, true)
	if err == nil {
		if !grant.matchesDeclaration(declaration) {
			return extensionDatabaseGrantRecord{}, ErrExtensionDatabaseArtifactConflict
		}
		return grant, nil
	}
	if !errors.Is(err, ErrExtensionDatabaseGrantNotFound) || request.Authority.Kind != ExtensionDatabaseLeaseIssuerActor {
		return extensionDatabaseGrantRecord{}, err
	}
	return upsertExtensionDatabaseGrant(ctx, tx, ExtensionDatabaseGrantRequest{
		Artifact: request.Artifact, ActorUserID: request.Authority.ActorUserID,
		AuditEventID: request.Authority.AuditEventID,
	}, declaration)
}

func (r *PostgresExtensionDatabaseRegistry) validateRuntimeLeaseIssue(
	ctx context.Context,
	request ExtensionDatabaseRuntimeLeaseIssue,
) error {
	if r == nil || r.pool == nil || r.random == nil || ctx == nil ||
		validateExtensionDatabaseArtifact(request.Artifact) != nil ||
		request.RuntimeInstanceID == "" || request.RuntimeInstanceID != strings.TrimSpace(request.RuntimeInstanceID) ||
		len(request.RuntimeInstanceID) > 512 || !validExtensionDatabaseLeaseAuthority(request.Authority) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	return ctx.Err()
}

func (r *PostgresExtensionDatabaseRegistry) validateRuntimeLeaseRef(
	ctx context.Context,
	ref ExtensionDatabaseRuntimeLeaseRef,
) error {
	if r == nil || r.pool == nil || ctx == nil || validateExtensionDatabaseArtifact(ref.Artifact) != nil ||
		ref.RuntimeInstanceID == "" || ref.RuntimeInstanceID != strings.TrimSpace(ref.RuntimeInstanceID) ||
		len(ref.RuntimeInstanceID) > 512 || !validLifecycleCleanupDigest(ref.LeaseID) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	return ctx.Err()
}

func validExtensionDatabaseLeaseAuthority(authority ExtensionDatabaseLeaseAuthority) bool {
	switch authority.Kind {
	case ExtensionDatabaseLeaseIssuerActor:
		return authority.ActorUserID > 0 && authority.AuditEventID > 0
	case ExtensionDatabaseLeaseIssuerHost:
		return authority.ActorUserID == 0 && authority.AuditEventID >= 0
	default:
		return false
	}
}

func (r *PostgresExtensionDatabaseRegistry) newRuntimeLeaseID() (string, error) {
	material := make([]byte, 32)
	if _, err := io.ReadFull(r.random, material); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", material), nil
}
