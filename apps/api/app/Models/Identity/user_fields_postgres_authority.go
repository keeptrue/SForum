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

type exactIdentityUserFieldTip struct {
	revision          int64
	declarationDigest string
}

func requireIdentityUserFieldSerializableTransaction(ctx context.Context, tx pgx.Tx) error {
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return err
	}
	if isolation != "serializable" {
		return ErrIdentityUserFieldTransactionIsolation
	}
	return nil
}

func lockExactIdentityUserField(
	ctx context.Context,
	tx pgx.Tx,
	field identityregistry.UserFieldContribution,
) (exactIdentityUserFieldTip, error) {
	artifact := field.Artifact
	if artifact.Core || artifact.VersionID <= 0 {
		return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
	}
	var versionID int64
	if err := tx.QueryRow(ctx, `
		SELECT version.id
		FROM extension_versions AS version
		JOIN extensions AS extension ON extension.id = version.extension_id
		WHERE extension.id = $1 AND extension.type = 'plugin'
		  AND extension.status = 'enabled' AND extension.active_version_id = version.id
		  AND version.id = $2 AND version.version = $3 AND version.package_digest = $4
		FOR SHARE OF version, extension
	`, artifact.ExtensionID, artifact.VersionID, artifact.ExtensionVersion, artifact.PackageDigest).Scan(&versionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
		}
		return exactIdentityUserFieldTip{}, err
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
		FOR SHARE
	`, artifact.ExtensionID).Scan(
		&rootRevision, &rootState, &rootVersionID, &rootVersion, &rootDigest, &publicationJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
		}
		return exactIdentityUserFieldTip{}, err
	}
	if rootRevision <= 0 || rootState != identityregistry.RegistryStateActive ||
		rootVersionID != artifact.VersionID || rootVersion != artifact.ExtensionVersion ||
		rootDigest != artifact.PackageDigest {
		return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
	}

	var ownerID string
	if err := tx.QueryRow(ctx, `
		SELECT owner_extension_id
		FROM extension_identity_registry_owners
		WHERE identity_kind = 'user_field' AND stable_id = $1
		FOR SHARE
	`, field.ID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
		}
		return exactIdentityUserFieldTip{}, err
	}

	var tip exactIdentityUserFieldTip
	var state, tipOwner, tipVersion, tipDigest, tipContract string
	var tipVersionID int64
	if err := tx.QueryRow(ctx, `
		SELECT revision, registry_state, owner_extension_id, extension_version_id,
		       extension_version, package_digest, contract_version, declaration_digest
		FROM extension_identity_registry_declarations
		WHERE identity_kind = 'user_field' AND stable_id = $1
		ORDER BY revision DESC LIMIT 1
		FOR SHARE
	`, field.ID).Scan(
		&tip.revision, &state, &tipOwner, &tipVersionID,
		&tipVersion, &tipDigest, &tipContract, &tip.declarationDigest,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
		}
		return exactIdentityUserFieldTip{}, err
	}
	if ownerID != artifact.ExtensionID || state != identityregistry.RegistryStateActive ||
		tipOwner != artifact.ExtensionID || tipVersionID != artifact.VersionID ||
		tipVersion != artifact.ExtensionVersion || tipDigest != artifact.PackageDigest ||
		tipContract != field.ContractVersion || tip.revision <= 0 ||
		!validIdentityUserFieldDigest(tip.declarationDigest) {
		return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
	}

	var publication identityregistry.Publication
	if err := json.Unmarshal(publicationJSON, &publication); err != nil || publication.Identity == nil ||
		publication.Artifact.ExtensionID != artifact.ExtensionID ||
		publication.Artifact.VersionID != artifact.VersionID ||
		publication.Artifact.ExtensionVersion != artifact.ExtensionVersion ||
		publication.Artifact.PackageDigest != artifact.PackageDigest {
		return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
	}
	matched := false
	for _, declared := range publication.Identity.UserFields {
		if declared.ID == field.ID {
			matched = reflect.DeepEqual(declared, field.UserField)
			break
		}
	}
	if !matched {
		return exactIdentityUserFieldTip{}, ErrIdentityUserFieldDeclarationStale
	}
	return tip, nil
}

func lockIdentityUserFieldUsers(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
	targetUserID int64,
) error {
	rows, err := tx.Query(ctx, `
		SELECT id, status
		FROM users
		WHERE id = $1 OR id = $2
		ORDER BY id
		FOR KEY SHARE
	`, actorUserID, targetUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	statuses := make(map[int64]string, 2)
	for rows.Next() {
		var userID int64
		var status string
		if err := rows.Scan(&userID, &status); err != nil {
			return err
		}
		statuses[userID] = status
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if statuses[actorUserID] != string(UserStatusActive) {
		return ErrIdentityUserFieldPermissionDenied
	}
	// Target status is not authority. An authorized live actor may manage a
	// disabled or banned account's retained field during moderation or privacy
	// workflows; only a missing target fails here.
	if _, found := statuses[targetUserID]; !found {
		return ErrIdentityUserFieldValueStateConflict
	}
	return nil
}

func lockIdentityUserFieldPrivacyUsers(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
	targetUserID int64,
) error {
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM users
		WHERE id = $1 OR id = $2
		ORDER BY id
		FOR KEY SHARE
	`, actorUserID, targetUserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	locked := make(map[int64]struct{}, 2)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return err
		}
		locked[userID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, found := locked[targetUserID]; !found {
		return ErrIdentityUserFieldValueNotFound
	}
	if actorUserID > 0 {
		if _, found := locked[actorUserID]; !found {
			return ErrIdentityUserFieldValueInvalid
		}
	}
	return nil
}

func authorizeIdentityUserFieldPermission(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
	permission string,
) error {
	if permission == "" {
		return ErrIdentityUserFieldPermissionDenied
	}
	actor, err := LoadEffectiveActorTx(ctx, tx, actorUserID)
	if errors.Is(err, ErrActorInactive) {
		return ErrIdentityUserFieldPermissionDenied
	}
	if err != nil {
		return err
	}
	if !actor.Can(permission) {
		return ErrIdentityUserFieldPermissionDenied
	}
	return nil
}

func lockIdentityUserFieldIdempotencyKey(ctx context.Context, tx pgx.Tx, key string) error {
	lockKey := "sforum:identity-user-field:idempotency:" + fmt.Sprintf("%d:%s", len([]byte(key)), key)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey)
	return err
}

func lockIdentityUserFieldValueKey(ctx context.Context, tx pgx.Tx, userID int64, fieldID string) error {
	lockKey := fmt.Sprintf(
		"sforum:identity-user-field:value:%d:%d:%s",
		userID, len([]byte(fieldID)), fieldID,
	)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey)
	return err
}
