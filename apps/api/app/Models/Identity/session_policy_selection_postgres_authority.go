package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type identitySessionPolicyRegistryClaim struct {
	revision uint64
	digest   string
	provider identityregistry.ProviderContribution
}

type exactIdentitySessionPolicyProviderTip struct {
	revision          int64
	declarationDigest string
}

func resolveIdentitySessionPolicyCandidate(
	registry *identityregistry.Registry,
	policyID string,
) (identitySessionPolicyRegistryClaim, error) {
	if registry == nil {
		return identitySessionPolicyRegistryClaim{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	return resolveIdentitySessionPolicyCandidateFromSnapshot(registry.Snapshot(), policyID)
}

func resolveIdentitySessionPolicyCandidateFromSnapshot(
	snapshot identityregistry.Snapshot,
	policyID string,
) (identitySessionPolicyRegistryClaim, error) {
	if snapshot.SafeMode {
		return identitySessionPolicyRegistryClaim{}, ErrIdentitySessionPolicySafeMode
	}
	var provider identityregistry.ProviderContribution
	found := false
	for _, candidate := range snapshot.Providers {
		if candidate.ID == policyID {
			provider = candidate
			found = true
			break
		}
	}
	if !found || !identitySessionPolicyProviderHasEvaluate(provider) {
		return identitySessionPolicyRegistryClaim{}, ErrIdentitySessionPolicyDeclarationStale
	}
	bound := false
	for _, publication := range snapshot.Publications {
		if publication.Artifact != provider.Artifact || publication.Identity == nil ||
			publication.Identity.SessionPolicy != policyID {
			continue
		}
		for _, declared := range publication.Identity.Providers {
			if declared.ID == policyID &&
				identitySessionPolicyProviderDefinitionMatches(declared, provider.Provider) {
				bound = true
				break
			}
		}
		break
	}
	if !bound {
		return identitySessionPolicyRegistryClaim{}, ErrIdentitySessionPolicyDeclarationStale
	}
	return identitySessionPolicyRegistryClaim{
		revision: snapshot.Revision,
		digest:   snapshot.Digest,
		provider: provider,
	}, nil
}

func validateIdentitySessionPolicyRegistryClaim(
	registry *identityregistry.Registry,
	claim identitySessionPolicyRegistryClaim,
) error {
	current, err := resolveIdentitySessionPolicyCandidate(registry, claim.provider.ID)
	if errors.Is(err, ErrIdentitySessionPolicySafeMode) {
		return err
	}
	if err != nil || current.revision != claim.revision || current.digest != claim.digest ||
		!identitySessionPolicyProviderMatches(current.provider, claim.provider) {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	return nil
}

func lockExactIdentitySessionPolicyProvider(
	ctx context.Context,
	tx pgx.Tx,
	provider identityregistry.ProviderContribution,
) (exactIdentitySessionPolicyProviderTip, error) {
	artifact := provider.Artifact
	if !identitySessionPolicyProviderHasEvaluate(provider) {
		return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
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
			return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
		}
		return exactIdentitySessionPolicyProviderTip{}, err
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
			return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
		}
		return exactIdentitySessionPolicyProviderTip{}, err
	}
	if rootRevision <= 0 || rootState != identityregistry.RegistryStateActive ||
		rootVersionID != artifact.VersionID || rootVersion != artifact.ExtensionVersion ||
		rootDigest != artifact.PackageDigest {
		return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
	}

	var ownerID string
	if err := tx.QueryRow(ctx, `
		SELECT owner_extension_id
		FROM extension_identity_registry_owners
		WHERE identity_kind = 'provider' AND stable_id = $1
		FOR SHARE
	`, provider.ID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
		}
		return exactIdentitySessionPolicyProviderTip{}, err
	}

	var tip exactIdentitySessionPolicyProviderTip
	var state, tipOwner, tipVersion, tipDigest, tipContract string
	var tipVersionID int64
	if err := tx.QueryRow(ctx, `
		SELECT revision, registry_state, owner_extension_id, extension_version_id,
		       extension_version, package_digest, contract_version, declaration_digest
		FROM extension_identity_registry_declarations
		WHERE identity_kind = 'provider' AND stable_id = $1
		ORDER BY revision DESC LIMIT 1
		FOR SHARE
	`, provider.ID).Scan(
		&tip.revision, &state, &tipOwner, &tipVersionID,
		&tipVersion, &tipDigest, &tipContract, &tip.declarationDigest,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
		}
		return exactIdentitySessionPolicyProviderTip{}, err
	}
	if ownerID != artifact.ExtensionID || state != identityregistry.RegistryStateActive ||
		tipOwner != artifact.ExtensionID || tipVersionID != artifact.VersionID ||
		tipVersion != artifact.ExtensionVersion || tipDigest != artifact.PackageDigest ||
		tipContract != provider.ContractVersion || tip.revision <= 0 ||
		!validExternalIdentityDigest(tip.declarationDigest) {
		return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
	}

	var publication identityregistry.Publication
	if err := json.Unmarshal(publicationJSON, &publication); err != nil || publication.Identity == nil ||
		publication.Artifact.ExtensionID != artifact.ExtensionID ||
		publication.Artifact.VersionID != artifact.VersionID ||
		publication.Artifact.ExtensionVersion != artifact.ExtensionVersion ||
		publication.Artifact.PackageDigest != artifact.PackageDigest ||
		publication.Identity.SessionPolicy != provider.ID {
		return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
	}
	matched := false
	for _, declared := range publication.Identity.Providers {
		if declared.ID == provider.ID {
			matched = identitySessionPolicyProviderDefinitionMatches(declared, provider.Provider)
			break
		}
	}
	if !matched {
		return exactIdentitySessionPolicyProviderTip{}, ErrIdentitySessionPolicyDeclarationStale
	}
	return tip, nil
}

func authorizeIdentitySessionPolicyActor(ctx context.Context, tx pgx.Tx, actorUserID int64) error {
	actor, err := LoadEffectiveActorTx(ctx, tx, actorUserID)
	if errors.Is(err, ErrActorInactive) {
		return ErrIdentitySessionPolicyPermissionDenied
	}
	if err != nil {
		return err
	}
	if !actor.IsSuperAdmin() {
		return ErrIdentitySessionPolicyPermissionDenied
	}
	return nil
}

func lockIdentitySessionPolicySelection(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		identityregistry.IdentitySessionPolicySelectionLockKey,
	)
	return err
}

func lockIdentitySessionPolicyEffectSelection(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`,
		identityregistry.IdentitySessionPolicySelectionLockKey,
	)
	return err
}

func identitySessionPolicyEvidenceForProvider(
	provider identityregistry.ProviderContribution,
	declarationRevision int64,
) IdentitySessionPolicyEvidence {
	return IdentitySessionPolicyEvidence{
		PolicyID:                provider.ID,
		ProviderContractVersion: provider.ContractVersion,
		OwnerExtensionID:        provider.Artifact.ExtensionID,
		OwnerExtensionVersionID: provider.Artifact.VersionID,
		OwnerExtensionVersion:   provider.Artifact.ExtensionVersion,
		OwnerPackageDigest:      provider.Artifact.PackageDigest,
		DeclarationRevision:     declarationRevision,
	}
}
