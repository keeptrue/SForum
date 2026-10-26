package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type exactExternalIdentityProviderTip struct {
	revision          int64
	declarationDigest string
}

func lockExactExternalIdentityProvider(
	ctx context.Context,
	tx pgx.Tx,
	provider identityregistry.ProviderContribution,
) (exactExternalIdentityProviderTip, error) {
	artifact := provider.Artifact
	var versionID int64
	if err := tx.QueryRow(ctx, `
		SELECT version.id
		FROM extension_versions AS version
		JOIN extensions AS extension ON extension.id = version.extension_id
		WHERE extension.id = $1 AND extension.type = 'plugin'
		  AND extension.status = 'enabled' AND extension.active_version_id = version.id
		  AND version.id = $2 AND version.version = $3 AND version.package_digest = $4
		FOR NO KEY UPDATE OF version, extension
	`, artifact.ExtensionID, artifact.VersionID, artifact.ExtensionVersion, artifact.PackageDigest).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
		}
		return exactExternalIdentityProviderTip{}, err
	}

	var rootRevision, rootVersionID int64
	var rootState, rootVersion, rootDigest string
	var publicationJSON []byte
	if err := tx.QueryRow(ctx, `
		SELECT revision, registry_state, extension_version_id, extension_version,
		       package_digest, publication_json
		FROM extension_identity_registry_publications
		WHERE owner_extension_id = $1
		ORDER BY revision DESC LIMIT 1
		FOR UPDATE
	`, artifact.ExtensionID).Scan(
		&rootRevision, &rootState, &rootVersionID, &rootVersion, &rootDigest, &publicationJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
		}
		return exactExternalIdentityProviderTip{}, err
	}
	if rootRevision <= 0 || rootState != "active" || rootVersionID != artifact.VersionID ||
		rootVersion != artifact.ExtensionVersion || rootDigest != artifact.PackageDigest {
		return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
	}

	var ownerID string
	if err := tx.QueryRow(ctx, `
		SELECT owner_extension_id
		FROM extension_identity_registry_owners
		WHERE identity_kind = 'provider' AND stable_id = $1
		FOR UPDATE
	`, provider.ID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
		}
		return exactExternalIdentityProviderTip{}, err
	}

	var tip exactExternalIdentityProviderTip
	var state, tipOwner, tipVersion, tipDigest, tipContract string
	var tipVersionID int64
	if err := tx.QueryRow(ctx, `
		SELECT revision, registry_state, owner_extension_id, extension_version_id,
		       extension_version, package_digest, contract_version, declaration_digest
		FROM extension_identity_registry_declarations
		WHERE identity_kind = 'provider' AND stable_id = $1
		ORDER BY revision DESC LIMIT 1
		FOR UPDATE
	`, provider.ID).Scan(
		&tip.revision, &state, &tipOwner, &tipVersionID,
		&tipVersion, &tipDigest, &tipContract, &tip.declarationDigest,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
		}
		return exactExternalIdentityProviderTip{}, err
	}
	if ownerID != artifact.ExtensionID || state != "active" || tipOwner != artifact.ExtensionID ||
		tipVersionID != artifact.VersionID || tipVersion != artifact.ExtensionVersion ||
		tipDigest != artifact.PackageDigest || tipContract != provider.ContractVersion ||
		tip.revision <= 0 || !validExternalIdentityDigest(tip.declarationDigest) {
		return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
	}
	var publication identityregistry.Publication
	if err := json.Unmarshal(publicationJSON, &publication); err != nil || publication.Identity == nil ||
		publication.Artifact.ExtensionID != artifact.ExtensionID ||
		publication.Artifact.VersionID != artifact.VersionID ||
		publication.Artifact.ExtensionVersion != artifact.ExtensionVersion ||
		publication.Artifact.PackageDigest != artifact.PackageDigest {
		return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
	}
	matched := false
	for _, declared := range publication.Identity.Providers {
		if declared.ID == provider.ID {
			matched = reflect.DeepEqual(declared, provider.Provider)
			break
		}
	}
	if !matched {
		return exactExternalIdentityProviderTip{}, ErrExternalIdentityProviderStale
	}
	return tip, nil
}

func lockActiveExternalIdentityUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM users WHERE id = $1 FOR KEY SHARE
	`, userID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrExternalIdentityLinkInvalid
		}
		return err
	}
	if status != string(UserStatusActive) {
		return ErrExternalIdentityLinkStateConflict
	}
	return nil
}

func lockExternalIdentityIdempotencyKey(ctx context.Context, tx pgx.Tx, key string) error {
	lockKey := externalIdentityLinkLockNamespace + fmt.Sprintf("%d:%s", len([]byte(key)), key)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey)
	return err
}
