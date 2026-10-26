package extensionsruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

var (
	ErrExtensionDatabaseRegistryInvalid  = errors.New("extension database registry input is invalid")
	ErrExtensionDatabaseArtifactConflict = errors.New("extension database exact artifact conflict")
	ErrExtensionDatabaseGrantNotFound    = errors.New("extension database exact grant not found")
	ErrExtensionDatabaseAuthority        = errors.New("extension database authority is not supported")
	ErrExtensionDatabaseCoreIncompatible = errors.New("extension database authority is incompatible with migrated Core")
	ErrExtensionDatabaseResourceConflict = errors.New("extension database physical resource conflict")
	ErrExtensionDatabaseCredential       = errors.New("extension database credential operation failed")
)

var (
	extensionDatabasePasswordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	extensionDatabaseContractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

type ExtensionDatabaseArtifact struct {
	ExtensionID   string
	Version       string
	VersionID     int64
	PackageDigest string
}

type ExtensionDatabaseGrantRequest struct {
	Artifact     ExtensionDatabaseArtifact
	ActorUserID  int64
	AuditEventID int64
}

type ExtensionDatabaseCredential struct {
	GrantID            int64
	ExtensionID        string
	ExtensionVersion   string
	PackageDigest      string
	SchemaName         string
	OwnerRoleName      string
	RoleName           string
	DatabaseName       string
	SearchPath         string
	Password           string
	CredentialRevision int64
}

type ExtensionDatabaseGrantSnapshot struct {
	GrantID                  int64
	Artifact                 ExtensionDatabaseArtifact
	DatabaseContractVersion  string
	Authority                string
	SchemaName               string
	OwnerRoleName            string
	RuntimeRoleName          string
	Status                   string
	ActiveCredentialRevision int64
	GrantRevision            int64
}

type PostgresExtensionDatabaseRegistry struct {
	pool   *pgxpool.Pool
	random io.Reader
}

func NewPostgresExtensionDatabaseRegistry(
	pool *pgxpool.Pool,
	random io.Reader,
) *PostgresExtensionDatabaseRegistry {
	if random == nil {
		random = rand.Reader
	}
	return &PostgresExtensionDatabaseRegistry{pool: pool, random: random}
}

func (r *PostgresExtensionDatabaseRegistry) ProvisionOwnSchema(
	ctx context.Context,
	request ExtensionDatabaseGrantRequest,
) (ExtensionDatabaseCredential, error) {
	return r.issueOwnSchemaCredential(ctx, request, true)
}

func (r *PostgresExtensionDatabaseRegistry) RotateOwnSchemaCredential(
	ctx context.Context,
	request ExtensionDatabaseGrantRequest,
) (ExtensionDatabaseCredential, error) {
	return r.issueOwnSchemaCredential(ctx, request, false)
}

func (r *PostgresExtensionDatabaseRegistry) issueOwnSchemaCredential(
	ctx context.Context,
	request ExtensionDatabaseGrantRequest,
	provision bool,
) (ExtensionDatabaseCredential, error) {
	if err := r.validateRequest(ctx, request); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(request.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseCredential{}, ErrExtensionDatabaseRegistryInvalid
	}
	password, fingerprint, err := r.newCredentialSecret()
	if err != nil {
		return ExtensionDatabaseCredential{}, fmt.Errorf("%w: generate secret: %v", ErrExtensionDatabaseCredential, err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ExtensionDatabaseCredential{}, fmt.Errorf("begin extension database credential issue: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	declaration, err := loadExactExtensionDatabaseDeclaration(ctx, tx, request.Artifact)
	if err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if !extensionmanifest.HasDatabaseGrant(&declaration, extensionmanifest.DatabaseGrantOwnSchema) {
		return ExtensionDatabaseCredential{}, ErrExtensionDatabaseAuthority
	}
	databaseName, err := ensureExtensionDatabaseResources(ctx, tx, request.Artifact.ExtensionID, identifiers)
	if err != nil {
		return ExtensionDatabaseCredential{}, err
	}

	var grant extensionDatabaseGrantRecord
	if provision {
		grant, err = upsertExtensionDatabaseGrant(ctx, tx, request, declaration)
	} else {
		grant, err = loadExtensionDatabaseGrant(ctx, tx, request.Artifact, true, true)
		if err == nil && !grant.matchesDeclaration(declaration) {
			err = ErrExtensionDatabaseArtifactConflict
		}
	}
	if err != nil {
		return ExtensionDatabaseCredential{}, err
	}

	revision, err := nextExtensionDatabaseCredentialRevision(ctx, tx, request.Artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := activateExtensionDatabaseRuntimeRole(
		ctx, tx, identifiers, databaseName, password,
	); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := revokeActiveExtensionDatabaseCredentials(
		ctx, tx, request.Artifact.ExtensionID, request.ActorUserID, request.AuditEventID,
	); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := insertExtensionDatabaseCredential(
		ctx, tx, grant.ID, request, identifiers.RuntimeRole, revision, fingerprint,
	); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := activateExtensionDatabaseGrantCredential(ctx, tx, grant, revision); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := markExtensionDatabaseResourceProvisioned(ctx, tx, request.Artifact.ExtensionID); err != nil {
		return ExtensionDatabaseCredential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExtensionDatabaseCredential{}, fmt.Errorf("commit extension database credential issue: %w", err)
	}
	return ExtensionDatabaseCredential{
		GrantID: grant.ID, ExtensionID: request.Artifact.ExtensionID,
		ExtensionVersion: request.Artifact.Version, PackageDigest: request.Artifact.PackageDigest,
		SchemaName: identifiers.Schema, OwnerRoleName: identifiers.OwnerRole,
		RoleName: identifiers.RuntimeRole, DatabaseName: databaseName,
		SearchPath: identifiers.Schema + ",pg_catalog", Password: password,
		CredentialRevision: revision,
	}, nil
}

func (r *PostgresExtensionDatabaseRegistry) RevokeOwnSchema(
	ctx context.Context,
	request ExtensionDatabaseGrantRequest,
) error {
	if err := r.validateRequest(ctx, request); err != nil {
		return err
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(request.Artifact.ExtensionID)
	if err != nil {
		return ErrExtensionDatabaseRegistryInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin extension database credential revoke: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabaseResource(ctx, tx, identifiers.LockKey); err != nil {
		return err
	}
	resource, err := loadExtensionDatabaseResource(ctx, tx, request.Artifact.ExtensionID, true)
	if err != nil {
		return err
	}
	if !resource.matches(identifiers) {
		return ErrExtensionDatabaseResourceConflict
	}
	grant, err := loadExtensionDatabaseGrant(ctx, tx, request.Artifact, false, true)
	if err != nil {
		return err
	}
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		return err
	}
	if err := disableExtensionDatabaseRuntimeRole(ctx, tx, identifiers.RuntimeRole, databaseName); err != nil {
		return err
	}
	if err := revokeActiveExtensionDatabaseCredentials(
		ctx, tx, request.Artifact.ExtensionID, request.ActorUserID, request.AuditEventID,
	); err != nil {
		return err
	}
	if grant.Status == "active" {
		if err := revokeExtensionDatabaseGrant(ctx, tx, grant, request.ActorUserID, request.AuditEventID); err != nil {
			return err
		}
	}
	if err := markExtensionDatabaseResourceRevoked(ctx, tx, request.Artifact.ExtensionID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit extension database credential revoke: %w", err)
	}
	return nil
}

func (r *PostgresExtensionDatabaseRegistry) InspectOwnSchemaGrant(
	ctx context.Context,
	artifact ExtensionDatabaseArtifact,
) (ExtensionDatabaseGrantSnapshot, error) {
	if r == nil || r.pool == nil || ctx == nil || validateExtensionDatabaseArtifact(artifact) != nil {
		return ExtensionDatabaseGrantSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	identifiers, err := ExtensionDatabaseIdentifiersFor(artifact.ExtensionID)
	if err != nil {
		return ExtensionDatabaseGrantSnapshot{}, ErrExtensionDatabaseRegistryInvalid
	}
	grant, err := loadExtensionDatabaseGrant(ctx, r.pool, artifact, false, false)
	if err != nil {
		return ExtensionDatabaseGrantSnapshot{}, err
	}
	resource, err := loadExtensionDatabaseResource(ctx, r.pool, artifact.ExtensionID, false)
	if err != nil {
		return ExtensionDatabaseGrantSnapshot{}, err
	}
	if !resource.matches(identifiers) {
		return ExtensionDatabaseGrantSnapshot{}, ErrExtensionDatabaseResourceConflict
	}
	return ExtensionDatabaseGrantSnapshot{
		GrantID: grant.ID, Artifact: artifact,
		DatabaseContractVersion: grant.DatabaseContractVersion, Authority: grant.Authority,
		SchemaName: resource.SchemaName, OwnerRoleName: resource.OwnerRoleName,
		RuntimeRoleName: resource.RuntimeRoleName, Status: grant.Status,
		ActiveCredentialRevision: grant.ActiveCredentialRevision, GrantRevision: grant.Revision,
	}, nil
}

func (r *PostgresExtensionDatabaseRegistry) validateRequest(
	ctx context.Context,
	request ExtensionDatabaseGrantRequest,
) error {
	if r == nil || r.pool == nil || r.random == nil || ctx == nil ||
		request.ActorUserID <= 0 || request.AuditEventID <= 0 {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return validateExtensionDatabaseArtifact(request.Artifact)
}

func validateExtensionDatabaseArtifact(artifact ExtensionDatabaseArtifact) error {
	if !extensionDatabaseIDPattern.MatchString(artifact.ExtensionID) ||
		artifact.Version == "" || artifact.Version != strings.TrimSpace(artifact.Version) ||
		artifact.VersionID <= 0 || !validLifecycleCleanupDigest(artifact.PackageDigest) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	return nil
}

func (r *PostgresExtensionDatabaseRegistry) newCredentialSecret() (string, string, error) {
	material := make([]byte, 32)
	if _, err := io.ReadFull(r.random, material); err != nil {
		return "", "", err
	}
	password := base64.RawURLEncoding.EncodeToString(material)
	if !extensionDatabasePasswordPattern.MatchString(password) {
		return "", "", ErrExtensionDatabaseCredential
	}
	digest := sha256.Sum256([]byte(password))
	return password, hex.EncodeToString(digest[:]), nil
}

func lockExtensionDatabaseResource(ctx context.Context, tx pgx.Tx, lockKey int64) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("lock extension database resource: %w", err)
	}
	return nil
}

// 物理角色与数据库 ACL 共用 PostgreSQL catalog。生产调用先持有扩展级或迁移 session 锁，
// 再获取此数据库级事务锁；持有本锁的路径不得反向获取扩展锁。
func lockExtensionDatabasePhysicalAuthority(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtext(current_database()),
			hashtext($1)
		)
	`, coreauthority.PhysicalAuthorityLockName); err != nil {
		return fmt.Errorf("lock extension database physical authority: %w", err)
	}
	return nil
}
