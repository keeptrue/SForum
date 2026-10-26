package extensionsruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type extensionDatabaseResourceRecord struct {
	ExtensionID     string
	SchemaName      string
	OwnerRoleName   string
	RuntimeRoleName string
	Status          string
	Revision        int64
}

func (r extensionDatabaseResourceRecord) matches(identifiers ExtensionDatabaseIdentifiers) bool {
	return r.SchemaName == identifiers.Schema && r.OwnerRoleName == identifiers.OwnerRole &&
		r.RuntimeRoleName == identifiers.RuntimeRole
}

type extensionDatabaseGrantRecord struct {
	ID                       int64
	ExtensionID              string
	VersionID                int64
	Version                  string
	PackageDigest            string
	DatabaseContractVersion  string
	Authority                string
	RequestedSchema          string
	RequestedRole            string
	Status                   string
	ActiveCredentialRevision int64
	Revision                 int64
	Powers                   []string
}

func (r extensionDatabaseGrantRecord) matchesDeclaration(declaration extensions.ManifestDatabase) bool {
	powers := extensionmanifest.DatabaseGrants(&declaration)
	return r.DatabaseContractVersion == declaration.ContractVersion &&
		extensionDatabaseStoredAuthorityMatches(r.Authority, powers) &&
		r.RequestedSchema == declaration.Schema && r.RequestedRole == declaration.Role &&
		reflect.DeepEqual(r.Powers, powers)
}

type extensionDatabaseRow interface {
	Scan(...any) error
}

type extensionDatabaseQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadExactExtensionDatabaseDeclaration(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	artifact ExtensionDatabaseArtifact,
) (extensions.ManifestDatabase, error) {
	if validateExtensionDatabaseArtifact(artifact) != nil {
		return extensions.ManifestDatabase{}, ErrExtensionDatabaseRegistryInvalid
	}
	var extensionID, version, packageDigest string
	var manifestJSON []byte
	err := querier.QueryRow(ctx, `
		SELECT extension_id, version, package_digest, manifest
		FROM extension_versions
		WHERE id = $1
	`, artifact.VersionID).Scan(&extensionID, &version, &packageDigest, &manifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return extensions.ManifestDatabase{}, ErrExtensionDatabaseArtifactConflict
	}
	if err != nil {
		return extensions.ManifestDatabase{}, fmt.Errorf("load exact extension database artifact: %w", err)
	}
	if extensionID != artifact.ExtensionID || version != artifact.Version || packageDigest != artifact.PackageDigest {
		return extensions.ManifestDatabase{}, ErrExtensionDatabaseArtifactConflict
	}
	var manifest extensions.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return extensions.ManifestDatabase{}, fmt.Errorf("decode exact extension database manifest: %w", err)
	}
	manifest = extensionmanifest.Normalize(manifest)
	if manifest.ID != artifact.ExtensionID || manifest.Version != artifact.Version || manifest.Database == nil {
		return extensions.ManifestDatabase{}, ErrExtensionDatabaseArtifactConflict
	}
	declaration := *manifest.Database
	if !extensionDatabaseContractPattern.MatchString(declaration.ContractVersion) ||
		len(extensionmanifest.DatabaseGrants(&declaration)) == 0 {
		return extensions.ManifestDatabase{}, ErrExtensionDatabaseArtifactConflict
	}
	return declaration, nil
}

func loadExtensionDatabaseResource(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	extensionID string,
	forUpdate bool,
) (extensionDatabaseResourceRecord, error) {
	query := `
		SELECT extension_id, schema_name, owner_role_name, runtime_role_name,
		       status, resource_revision
		FROM extension_database_resources
		WHERE extension_id = $1
	`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var record extensionDatabaseResourceRecord
	err := querier.QueryRow(ctx, query, extensionID).Scan(
		&record.ExtensionID, &record.SchemaName, &record.OwnerRoleName,
		&record.RuntimeRoleName, &record.Status, &record.Revision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return extensionDatabaseResourceRecord{}, ErrExtensionDatabaseGrantNotFound
	}
	if err != nil {
		return extensionDatabaseResourceRecord{}, fmt.Errorf("load extension database resource: %w", err)
	}
	return record, nil
}

func revokeActiveExtensionDatabaseGrants(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	actorUserID int64,
	auditEventID int64,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE extension_database_grants
		SET status = 'revoked', revoked_by_user_id = $2, revoke_audit_event_id = $3,
		    revoked_at = statement_timestamp(), grant_revision = grant_revision + 1,
		    updated_at = statement_timestamp()
		WHERE extension_id = $1 AND status = 'active'
	`, extensionID, actorUserID, auditEventID)
	if err != nil {
		return fmt.Errorf("revoke previous extension database grants: %w", err)
	}
	return nil
}

func upsertExtensionDatabaseGrant(
	ctx context.Context,
	tx pgx.Tx,
	request ExtensionDatabaseGrantRequest,
	declaration extensions.ManifestDatabase,
) (extensionDatabaseGrantRecord, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO extension_database_grants (
			extension_id, extension_version_id, extension_version, package_digest,
			database_contract_version, authority, requested_schema, requested_role,
			granted_by_user_id, grant_audit_event_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (extension_id, extension_version_id, extension_version, package_digest)
		DO UPDATE SET
			database_contract_version = EXCLUDED.database_contract_version,
			authority = CASE
				WHEN extension_database_grants.authority = 'additive' THEN EXCLUDED.authority
				ELSE extension_database_grants.authority
			END,
			requested_schema = EXCLUDED.requested_schema,
			requested_role = EXCLUDED.requested_role,
			status = 'active', granted_by_user_id = EXCLUDED.granted_by_user_id,
			grant_audit_event_id = EXCLUDED.grant_audit_event_id,
			revoked_by_user_id = NULL, revoke_audit_event_id = NULL,
			revoked_at = NULL, failure_code = '',
			grant_revision = extension_database_grants.grant_revision + 1,
			updated_at = statement_timestamp()
		RETURNING id, extension_id, extension_version_id, extension_version,
		          package_digest, database_contract_version, authority,
		          requested_schema, requested_role, status,
		          active_credential_revision, grant_revision
	`, request.Artifact.ExtensionID, request.Artifact.VersionID, request.Artifact.Version,
		request.Artifact.PackageDigest, declaration.ContractVersion, extensionDatabaseStoredAuthority(declaration),
		declaration.Schema, declaration.Role, request.ActorUserID, request.AuditEventID)
	record, err := scanExtensionDatabaseGrant(row)
	if err != nil {
		return extensionDatabaseGrantRecord{}, fmt.Errorf("upsert exact extension database grant: %w", err)
	}
	if err := replaceExtensionDatabaseGrantPowers(ctx, tx, record.ID, declaration); err != nil {
		return extensionDatabaseGrantRecord{}, err
	}
	record.Powers = extensionmanifest.DatabaseGrants(&declaration)
	return record, nil
}

func loadExtensionDatabaseGrant(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	artifact ExtensionDatabaseArtifact,
	activeOnly bool,
	forUpdate bool,
) (extensionDatabaseGrantRecord, error) {
	query := `
		SELECT id, extension_id, extension_version_id, extension_version,
		       package_digest, database_contract_version, authority,
		       requested_schema, requested_role, status,
		       active_credential_revision, grant_revision
		FROM extension_database_grants
		WHERE extension_id = $1 AND extension_version_id = $2
		  AND extension_version = $3 AND package_digest = $4
	`
	if activeOnly {
		query += ` AND status = 'active'`
	}
	if forUpdate {
		query += ` FOR UPDATE`
	}
	record, err := scanExtensionDatabaseGrant(querier.QueryRow(
		ctx, query, artifact.ExtensionID, artifact.VersionID, artifact.Version, artifact.PackageDigest,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return extensionDatabaseGrantRecord{}, ErrExtensionDatabaseGrantNotFound
	}
	if err != nil {
		return extensionDatabaseGrantRecord{}, fmt.Errorf("load exact extension database grant: %w", err)
	}
	record.Powers, err = loadExtensionDatabaseGrantPowers(ctx, querier, record.ID)
	if err != nil {
		return extensionDatabaseGrantRecord{}, err
	}
	return record, nil
}

func extensionDatabaseStoredAuthority(declaration extensions.ManifestDatabase) string {
	if len(declaration.Grants) > 0 {
		return "additive"
	}
	return declaration.Authority
}

func extensionDatabaseStoredAuthorityMatches(stored string, powers []string) bool {
	if stored == "additive" {
		return len(powers) > 0
	}
	legacy := extensions.ManifestDatabase{Authority: stored}
	return reflect.DeepEqual(extensionmanifest.DatabaseGrants(&legacy), powers)
}

func replaceExtensionDatabaseGrantPowers(
	ctx context.Context,
	tx pgx.Tx,
	grantID int64,
	declaration extensions.ManifestDatabase,
) error {
	if grantID <= 0 {
		return ErrExtensionDatabaseRegistryInvalid
	}
	desired := extensionmanifest.DatabaseGrants(&declaration)
	existing, err := loadExtensionDatabaseGrantPowersOptional(ctx, tx, grantID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if !reflect.DeepEqual(existing, desired) {
			return ErrExtensionDatabaseArtifactConflict
		}
		return nil
	}
	source := "manifest_grants"
	if len(declaration.Grants) == 0 {
		source = "legacy_authority"
	}
	for _, power := range desired {
		ordinal := extensionDatabaseGrantOrdinal(power)
		if ordinal == 0 {
			return ErrExtensionDatabaseAuthority
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO extension_database_grant_powers (grant_id, power, source, ordinal)
			VALUES ($1, $2, $3, $4)
		`, grantID, power, source, ordinal); err != nil {
			return fmt.Errorf("insert extension database grant power: %w", err)
		}
	}
	return nil
}

func loadExtensionDatabaseGrantPowers(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	grantID int64,
) ([]string, error) {
	powers, err := loadExtensionDatabaseGrantPowersOptional(ctx, querier, grantID)
	if err != nil {
		return nil, err
	}
	if len(powers) == 0 {
		return nil, ErrExtensionDatabaseAuthority
	}
	return powers, nil
}

func loadExtensionDatabaseGrantPowersOptional(
	ctx context.Context,
	querier extensionDatabaseQuerier,
	grantID int64,
) ([]string, error) {
	type rowQuerier interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}
	rowsProvider, ok := querier.(rowQuerier)
	if !ok || grantID <= 0 {
		return nil, ErrExtensionDatabaseRegistryInvalid
	}
	rows, err := rowsProvider.Query(ctx, `
		SELECT power FROM extension_database_grant_powers
		WHERE grant_id = $1 ORDER BY ordinal
	`, grantID)
	if err != nil {
		return nil, fmt.Errorf("load extension database grant powers: %w", err)
	}
	defer rows.Close()
	powers := make([]string, 0, 5)
	for rows.Next() {
		var power string
		if err := rows.Scan(&power); err != nil {
			return nil, err
		}
		powers = append(powers, power)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return powers, nil
}

func extensionDatabaseGrantOrdinal(power string) int {
	switch power {
	case extensionmanifest.DatabaseGrantOwnSchema:
		return 1
	case extensionmanifest.DatabaseGrantCoreViews:
		return 2
	case extensionmanifest.DatabaseGrantHostCommands:
		return 3
	case extensionmanifest.DatabaseGrantRawCore:
		return 4
	case extensionmanifest.DatabaseGrantKernel:
		return 5
	default:
		return 0
	}
}

func scanExtensionDatabaseGrant(row extensionDatabaseRow) (extensionDatabaseGrantRecord, error) {
	var record extensionDatabaseGrantRecord
	err := row.Scan(
		&record.ID, &record.ExtensionID, &record.VersionID, &record.Version,
		&record.PackageDigest, &record.DatabaseContractVersion, &record.Authority,
		&record.RequestedSchema, &record.RequestedRole, &record.Status,
		&record.ActiveCredentialRevision, &record.Revision,
	)
	return record, err
}

func nextExtensionDatabaseCredentialRevision(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
) (int64, error) {
	var revision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(credential_revision), 0) + 1
		FROM extension_database_credentials
		WHERE extension_id = $1
	`, extensionID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("allocate extension database credential revision: %w", err)
	}
	if revision <= 0 {
		return 0, ErrExtensionDatabaseCredential
	}
	return revision, nil
}

func revokeActiveExtensionDatabaseCredentials(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	actorUserID int64,
	auditEventID int64,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE extension_database_credentials
		SET status = 'revoked', revoked_by_user_id = $2, revoke_audit_event_id = $3,
		    revoked_at = statement_timestamp()
		WHERE extension_id = $1 AND status = 'active'
	`, extensionID, actorUserID, auditEventID)
	if err != nil {
		return fmt.Errorf("revoke previous extension database credentials: %w", err)
	}
	return nil
}

func insertExtensionDatabaseCredential(
	ctx context.Context,
	tx pgx.Tx,
	grantID int64,
	request ExtensionDatabaseGrantRequest,
	roleName string,
	revision int64,
	fingerprint string,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO extension_database_credentials (
			grant_id, extension_id, credential_revision, role_name,
			credential_fingerprint, issued_by_user_id, issue_audit_event_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, grantID, request.Artifact.ExtensionID, revision, roleName, fingerprint,
		request.ActorUserID, request.AuditEventID)
	if err != nil {
		return fmt.Errorf("insert extension database credential revision: %w", err)
	}
	return nil
}

func activateExtensionDatabaseGrantCredential(
	ctx context.Context,
	tx pgx.Tx,
	grant extensionDatabaseGrantRecord,
	revision int64,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_grants
		SET active_credential_revision = $2,
		    grant_revision = grant_revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND grant_revision = $3 AND status = 'active'
	`, grant.ID, revision, grant.Revision)
	if err != nil {
		return fmt.Errorf("activate extension database credential revision: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func revokeExtensionDatabaseGrant(
	ctx context.Context,
	tx pgx.Tx,
	grant extensionDatabaseGrantRecord,
	actorUserID int64,
	auditEventID int64,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_grants
		SET status = 'revoked', revoked_by_user_id = $2, revoke_audit_event_id = $3,
		    revoked_at = statement_timestamp(), grant_revision = grant_revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1 AND grant_revision = $4 AND status = 'active'
	`, grant.ID, actorUserID, auditEventID, grant.Revision)
	if err != nil {
		return fmt.Errorf("revoke exact extension database grant: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func markExtensionDatabaseResourceProvisioned(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_resources
		SET status = 'provisioned', failure_code = '', revoked_at = NULL,
		    resource_revision = resource_revision + 1,
		    updated_at = statement_timestamp()
		WHERE extension_id = $1
	`, extensionID)
	if err != nil {
		return fmt.Errorf("mark extension database resource provisioned: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func markExtensionDatabaseResourceRevoked(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE extension_database_resources
		SET status = 'revoked', revoked_at = statement_timestamp(),
		    schema_retained = TRUE, resource_revision = resource_revision + 1,
		    updated_at = statement_timestamp()
		WHERE extension_id = $1
	`, extensionID)
	if err != nil {
		return fmt.Errorf("mark extension database resource revoked: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}
