package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editorregistry "github.com/keeptrue/sforum/apps/api/app/Support/EditorRegistry"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	mediaregistry "github.com/keeptrue/sforum/apps/api/app/Support/MediaRegistry"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

type lifecycleRegistryDigestDocument struct {
	Schema             string                          `json:"schema"`
	ExtensionID        string                          `json:"extensionId"`
	ExtensionVersion   string                          `json:"extensionVersion"`
	PackageDigest      string                          `json:"packageDigest"`
	VersionID          int64                           `json:"versionId"`
	RuntimeInstanceID  string                          `json:"runtimeInstanceId"`
	Hooks              []extensions.ManifestEvent      `json:"hooks"`
	VersionedHooks     []extensions.ManifestHook       `json:"versionedHooks,omitempty"`
	VersionedProviders []extensions.ManifestProvider   `json:"versionedProviders,omitempty"`
	Services           []extensions.ManifestService    `json:"services"`
	Pages              []pages.PageContribution        `json:"pages"`
	Routes             routes.PluginRouteSet           `json:"routes"`
	Asset              *assetregistry.Publication      `json:"asset,omitempty"`
	AssetAdmitted      bool                            `json:"assetAdmitted,omitempty"`
	Query              *queryregistry.Publication      `json:"query,omitempty"`
	Cache              *cacheregistry.Publication      `json:"cache,omitempty"`
	SEO                *seoregistry.Publication        `json:"seo,omitempty"`
	Identity           *identityregistry.Publication   `json:"identity,omitempty"`
	Navigation         *navigationregistry.Publication `json:"navigation,omitempty"`
	Content            *contentregistry.Publication    `json:"content,omitempty"`
	Media              *mediaregistry.Publication      `json:"media,omitempty"`
	Editor             *editorregistry.Publication     `json:"editor,omitempty"`
	Entity             *entityregistry.Publication     `json:"entity,omitempty"`
	ProductionFamilies []string                        `json:"productionFamilies"`
	FoundationFamilies []string                        `json:"foundationFamilies"`
}

func refreshLifecycleRegistryMaterialDigest(material *lifecycleRegistryMaterial) error {
	if material == nil {
		return ErrLifecycleRegistryPublicationInvalid
	}
	legacyDigest, err := encodeLifecycleRegistryMaterialDigest(material, false, false)
	if err != nil {
		return err
	}
	material.legacyDigest = ""
	material.compatibleDigests = nil
	material.digest = legacyDigest
	assetDigest := ""
	queryDigest := ""
	if material.assetPublication != nil {
		assetDigest, err = encodeLifecycleRegistryMaterialDigest(material, true, false)
		if err != nil {
			return err
		}
		material.digest = assetDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = []string{legacyDigest}
	}
	if material.queryPublication != nil {
		var queryErr error
		queryDigest, queryErr = encodeLifecycleRegistryMaterialDigest(
			material, material.assetPublication != nil, true,
		)
		if queryErr != nil {
			return queryErr
		}
		material.digest = queryDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = []string{legacyDigest}
		if assetDigest != "" && assetDigest != legacyDigest {
			material.compatibleDigests = append(material.compatibleDigests, assetDigest)
		}
	}
	if material.cachePublication != nil {
		// @4 is additive, while a process may resume a row prepared by any of
		// the prior encoders that could actually have selected this exact family
		// set. A cache-only material was @1 before @4, so impossible @2/@3 digests
		// must not become recovery aliases merely because they can be recomputed.
		cacheDigest, cacheErr := encodeLifecycleRegistryMaterialDigestV4(material)
		if cacheErr != nil {
			return cacheErr
		}
		material.digest = cacheDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = []string{legacyDigest}
		if assetDigest != "" && assetDigest != legacyDigest {
			material.compatibleDigests = append(material.compatibleDigests, assetDigest)
		}
		if queryDigest != "" && queryDigest != legacyDigest && queryDigest != assetDigest {
			material.compatibleDigests = append(material.compatibleDigests, queryDigest)
		}
	}
	if material.seoPublication != nil {
		// SEO is the @5 additive family. Only digests emitted by an encoder that
		// could see this exact prior family set are valid recovery aliases.
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		seoDigest, seoErr := encodeLifecycleRegistryMaterialDigestV5(material)
		if seoErr != nil {
			return seoErr
		}
		material.digest = seoDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, seoDigest)
	}
	if material.identityPublication != nil {
		// Identity is additive @6. Keep only digests emitted by the exact prior
		// family set; plugins without Identity continue to emit their old bytes.
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		identityDigest, identityErr := encodeLifecycleRegistryMaterialDigestV6(material)
		if identityErr != nil {
			return identityErr
		}
		material.digest = identityDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, identityDigest)
	}
	if material.navigationPublication != nil {
		// Navigation/Region is additive @7. Only materials that freeze navigation
		// advance the primary digest; prior family digests remain recovery aliases.
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		navigationDigest, navigationErr := encodeLifecycleRegistryMaterialDigestV7(material)
		if navigationErr != nil {
			return navigationErr
		}
		material.digest = navigationDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, navigationDigest)
	}
	if material.contentPublication != nil {
		// Content Registry is additive @8 (P10). Only content-bearing materials
		// advance the primary digest; prior family digests remain recovery aliases.
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		contentDigest, contentErr := encodeLifecycleRegistryMaterialDigestV8(material)
		if contentErr != nil {
			return contentErr
		}
		material.digest = contentDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, contentDigest)
	}
	if material.mediaPublication != nil {
		// Media Pipeline Registry is additive @9 (P10). Only media-bearing materials
		// advance the primary digest; prior family digests remain recovery aliases.
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		mediaDigest, mediaErr := encodeLifecycleRegistryMaterialDigestV9(material)
		if mediaErr != nil {
			return mediaErr
		}
		material.digest = mediaDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, mediaDigest)
	}
	if material.editorPublication != nil {
		// Editor Registry is additive @10 (P10 Tiptap trusted L2).
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		editorDigest, editorErr := encodeLifecycleRegistryMaterialDigestV10(material)
		if editorErr != nil {
			return editorErr
		}
		material.digest = editorDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, editorDigest)
	}
	if material.entityPublication != nil {
		// Entity/Taxonomy/Field Registry is additive @11 (P10 data plane).
		priorDigest := material.digest
		priorAliases := append([]string(nil), material.compatibleDigests...)
		entityDigest, entityErr := encodeLifecycleRegistryMaterialDigestV11(material)
		if entityErr != nil {
			return entityErr
		}
		material.digest = entityDigest
		material.legacyDigest = legacyDigest
		material.compatibleDigests = appendLifecycleCompatibleDigest(priorAliases, priorDigest, entityDigest)
	}
	return nil
}

func appendLifecycleCompatibleDigest(values []string, candidate, primary string) []string {
	if candidate == "" || candidate == primary {
		return values
	}
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func encodeLifecycleRegistryMaterialDigest(
	material *lifecycleRegistryMaterial,
	includeAsset bool,
	includeQuery bool,
) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, includeAsset, includeQuery, false, false, false, false, false, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV4(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, false, false, false, false, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV5(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, false, false, false, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV6(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, false, false, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV7(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, true, false, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV8(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, true, true, false, false, false)
}

func encodeLifecycleRegistryMaterialDigestV9(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, true, true, true, false, false)
}

func encodeLifecycleRegistryMaterialDigestV10(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, true, true, true, true, false)
}

func encodeLifecycleRegistryMaterialDigestV11(material *lifecycleRegistryMaterial) (string, error) {
	return encodeLifecycleRegistryMaterialDigestVersion(material, true, true, true, true, true, true, true, true, true, true)
}

func encodeLifecycleRegistryMaterialDigestVersion(
	material *lifecycleRegistryMaterial,
	includeAsset bool,
	includeQuery bool,
	includeCache bool,
	includeSEO bool,
	includeIdentity bool,
	includeNavigation bool,
	includeContent bool,
	includeMedia bool,
	includeEditor bool,
	includeEntity bool,
) (string, error) {
	extension := material.extension
	binding := material.binding
	productionFamilies := []string{"hooks.v1", "pages.runtime", "services.v2"}
	if hasVersionedPluginHooks(extension) {
		productionFamilies = append(productionFamilies, "hooks.v2")
	}
	for _, provider := range extension.Manifest.Providers {
		if isVersionedProviderSlot(provider) {
			productionFamilies = append(productionFamilies, "providers.v2")
			break
		}
	}
	schema := "sforum.lifecycle.registry-plan@1"
	var asset *assetregistry.Publication
	assetAdmitted := false
	if includeAsset {
		schema = "sforum.lifecycle.registry-plan@2"
		asset = material.assetPublication
		assetAdmitted = material.assetAdmitted
	}
	var query *queryregistry.Publication
	if includeQuery {
		schema = "sforum.lifecycle.registry-plan@3"
		query = material.queryPublication
		if query != nil {
			productionFamilies = append(productionFamilies, "queries.v1")
		}
	}
	var cache *cacheregistry.Publication
	if includeCache {
		schema = "sforum.lifecycle.registry-plan@4"
		cache = material.cachePublication
		if cache != nil {
			productionFamilies = append(productionFamilies, "caches.v1")
		}
	}
	var seo *seoregistry.Publication
	if includeSEO {
		schema = "sforum.lifecycle.registry-plan@5"
		seo = material.seoPublication
		if seo != nil {
			productionFamilies = append(productionFamilies, "seo.v1")
		}
	}
	var identity *identityregistry.Publication
	if includeIdentity {
		schema = "sforum.lifecycle.registry-plan@6"
		identity = material.identityPublication
		if identity != nil {
			productionFamilies = append(productionFamilies, "identity.v1")
		}
	}
	var navigation *navigationregistry.Publication
	if includeNavigation {
		schema = "sforum.lifecycle.registry-plan@7"
		navigation = material.navigationPublication
		if navigation != nil {
			productionFamilies = append(productionFamilies, "navigation.v1")
		}
	}
	var content *contentregistry.Publication
	if includeContent {
		schema = "sforum.lifecycle.registry-plan@8"
		content = material.contentPublication
		if content != nil {
			productionFamilies = append(productionFamilies, "content.v1")
		}
	}
	var media *mediaregistry.Publication
	if includeMedia {
		schema = "sforum.lifecycle.registry-plan@9"
		media = material.mediaPublication
		if media != nil {
			productionFamilies = append(productionFamilies, "media.v1")
		}
	}
	var editor *editorregistry.Publication
	if includeEditor {
		schema = "sforum.lifecycle.registry-plan@10"
		editor = material.editorPublication
		if editor != nil {
			productionFamilies = append(productionFamilies, "editor.v1")
		}
	}
	var entity *entityregistry.Publication
	if includeEntity {
		schema = "sforum.lifecycle.registry-plan@11"
		entity = material.entityPublication
		if entity != nil {
			productionFamilies = append(productionFamilies, "entity.v1")
		}
	}
	// @1 remains byte-for-byte compatible with pre-P9 in-flight rows. New
	// asset-bearing operations persist @2. Query-bearing operations persist @3;
	// cache-bearing operations persist @4 and SEO-bearing operations persist @5.
	// Identity-bearing operations persist @6; navigation-bearing operations persist @7;
	// content-bearing operations persist @8; media-bearing operations persist @9;
	// editor-bearing operations persist @10; entity-bearing operations persist @11.
	// Earlier versions are accepted only as explicit recovery aliases computed
	// from the same exact material.
	document := lifecycleRegistryDigestDocument{
		Schema: schema, ExtensionID: extension.ID,
		ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: binding.RuntimeInstanceID,
		Hooks:              append([]extensions.ManifestEvent(nil), extensions.DeclaredManifestEvents(extension.Manifest)...),
		VersionedHooks:     cloneManifestHooks(extension.Manifest.Hooks),
		VersionedProviders: append([]extensions.ManifestProvider(nil), extension.Manifest.Providers...),
		Services:           append([]extensions.ManifestService(nil), extension.Manifest.Services...),
		Pages:              append([]pages.PageContribution(nil), material.pages...), Routes: material.routes,
		Asset: asset, AssetAdmitted: assetAdmitted,
		Query:              query,
		Cache:              cache,
		SEO:                seo,
		Identity:           identity,
		Navigation:         navigation,
		Content:            content,
		Media:              media,
		Editor:             editor,
		Entity:             entity,
		ProductionFamilies: productionFamilies,
		FoundationFamilies: []string{"routes.v1-foundation"},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("encode lifecycle registry plan: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// LifecycleAssetAuthority supplies an impact digest from durable Host state.
// Implementations must never rebuild trust impact by scanning package bytes.
type LifecycleAssetAuthority interface {
	OperationImpactDigest(context.Context, int64, extensions.Extension) (string, error)
	RestoreImpactDigest(context.Context, extensions.Extension) (string, error)
}

// LifecycleAssetAdmission checks the frozen publication against current trust.
// The production implementation performs an exact grant lookup without package I/O.
type LifecycleAssetAdmission interface {
	ValidatePublishedIdentity(context.Context, extensions.Extension, assetregistry.Artifact) error
}

// LifecycleAssetOperationRepository is the durable operation/authority subset
// needed to reconstruct exact registry material after a process restart.
type LifecycleAssetOperationRepository interface {
	Operation(context.Context, string, int64) (extensions.LifecycleOperation, error)
	LastSuccessfulLifecycleAuthority(
		context.Context,
		extensions.ExactExtensionVersionInput,
	) (extensions.LifecycleAuthoritySnapshot, error)
}

// PostgresLifecycleAssetAuthority reads immutable lifecycle authority first and
// falls back to an unambiguous exact live grant for asset-only extensions.
type PostgresLifecycleAssetAuthority struct {
	pool       *pgxpool.Pool
	operations LifecycleAssetOperationRepository
}

func NewPostgresLifecycleAssetAuthority(
	pool *pgxpool.Pool,
	operations LifecycleAssetOperationRepository,
) *PostgresLifecycleAssetAuthority {
	return &PostgresLifecycleAssetAuthority{pool: pool, operations: operations}
}

func (a *PostgresLifecycleAssetAuthority) OperationImpactDigest(
	ctx context.Context,
	operationID int64,
	extension extensions.Extension,
) (string, error) {
	if a == nil || a.operations == nil || ctx == nil || operationID <= 0 {
		return "", ErrLifecycleRegistryPublicationUnavailable
	}
	operation, err := a.operations.Operation(ctx, extension.ID, operationID)
	if err != nil {
		return "", err
	}
	if operation.ID != operationID || operation.ExtensionID != extension.ID ||
		operation.ExtensionVersion != extension.Version || operation.PackageDigest != extension.PackageDigest {
		return "", ErrLifecycleRegistryPublicationConflict
	}
	var authority extensions.LifecycleAuthoritySnapshot
	if json.Unmarshal(operation.AuthoritySnapshot, &authority) != nil ||
		operation.AuthorityType != authority.AuthorityType ||
		(authority.Grant == nil && operation.TrustGrantID != 0) ||
		(authority.Grant != nil && operation.TrustGrantID != authority.Grant.ID) {
		return "", ErrLifecycleRegistryPublicationConflict
	}
	return lifecycleAssetAuthorityImpact(extension, operation.AuthoritySnapshot)
}

func (a *PostgresLifecycleAssetAuthority) RestoreImpactDigest(
	ctx context.Context,
	extension extensions.Extension,
) (string, error) {
	if a == nil || a.operations == nil || ctx == nil {
		return "", ErrLifecycleRegistryPublicationUnavailable
	}
	authority, err := a.operations.LastSuccessfulLifecycleAuthority(ctx, extensions.ExactExtensionVersionInput{
		ExtensionID: extension.ID, Version: extension.Version, PackageDigest: extension.PackageDigest,
	})
	if err == nil {
		document, marshalErr := json.Marshal(authority)
		if marshalErr != nil {
			return "", fmt.Errorf("encode lifecycle asset authority: %w", marshalErr)
		}
		return lifecycleAssetAuthorityImpact(extension, document)
	}
	if !errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
		return "", err
	}
	if extension.Source == extensions.SourceBuiltin {
		// Built-ins require no grant. Their immutable package digest is the Host
		// authority identity used by other startup runtime contracts as well.
		if !validLifecycleCleanupDigest(extension.PackageDigest) {
			return "", ErrLifecycleRegistryPublicationConflict
		}
		return extension.PackageDigest, nil
	}
	return a.exactLiveGrantImpact(ctx, extension)
}

func (a *PostgresLifecycleAssetAuthority) exactLiveGrantImpact(
	ctx context.Context,
	extension extensions.Extension,
) (string, error) {
	if a == nil || a.pool == nil {
		return "", ErrLifecycleRegistryPublicationUnavailable
	}
	rows, err := a.pool.Query(ctx, `
		SELECT impact_digest
		FROM extension_trust_grants
		WHERE extension_id = $1
		  AND extension_version = $2
		  AND package_digest = $3
		  AND action = $4
		  AND revoked_at IS NULL
		ORDER BY granted_at DESC, id DESC
		LIMIT 2
	`, extension.ID, extension.Version, extension.PackageDigest, extensions.TrustActionEnable)
	if err != nil {
		return "", fmt.Errorf("load lifecycle asset grant authority: %w", err)
	}
	defer rows.Close()
	impacts := make([]string, 0, 2)
	for rows.Next() {
		var impact string
		if scanErr := rows.Scan(&impact); scanErr != nil {
			return "", fmt.Errorf("scan lifecycle asset grant authority: %w", scanErr)
		}
		impacts = append(impacts, impact)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read lifecycle asset grant authority: %w", err)
	}
	if len(impacts) == 0 {
		return "", extensions.ErrTrustGrantNotFound
	}
	if len(impacts) != 1 || !validLifecycleCleanupDigest(impacts[0]) {
		// More than one live impact for one package has no durable selected
		// provider. Startup must not guess which browser authority to publish.
		return "", ErrLifecycleRegistryPublicationConflict
	}
	return impacts[0], nil
}

func lifecycleAssetAuthorityImpact(extension extensions.Extension, document json.RawMessage) (string, error) {
	var authority extensions.LifecycleAuthoritySnapshot
	if len(document) == 0 || json.Unmarshal(document, &authority) != nil {
		return "", ErrLifecycleRegistryPublicationConflict
	}
	impact := authority.Impact
	if authority.SchemaVersion != extensions.LifecycleAuthoritySnapshotSchemaV1 || authority.ActorUserID <= 0 ||
		impact.SchemaVersion != extensions.TrustImpactSchemaV2 || impact.Action != extensions.TrustActionEnable ||
		impact.ExtensionID != extension.ID || impact.ExtensionVersion != extension.Version ||
		impact.ExtensionType != extension.Type || impact.Source != extension.Source ||
		impact.PackageDigest != extension.PackageDigest || impact.ArtifactDigests["package"] != extension.PackageDigest ||
		!validLifecycleCleanupDigest(impact.Digest) {
		return "", ErrLifecycleRegistryPublicationConflict
	}
	switch authority.AuthorityType {
	case extensions.LifecycleAuthorityBuiltin:
		if extension.Source != extensions.SourceBuiltin || authority.Grant != nil {
			return "", ErrLifecycleRegistryPublicationConflict
		}
	case extensions.LifecycleAuthorityTrustGrant:
		grant := authority.Grant
		if grant == nil || grant.ID <= 0 || grant.ExtensionID != extension.ID ||
			grant.ExtensionVersion != extension.Version || grant.PackageDigest != extension.PackageDigest ||
			grant.Action != extensions.TrustActionEnable || grant.ImpactDigest != impact.Digest || grant.RevokedAt != nil {
			return "", ErrLifecycleRegistryPublicationConflict
		}
	default:
		return "", ErrLifecycleRegistryPublicationConflict
	}
	return impact.Digest, nil
}

func (b *PostgresLifecycleBoundaryRegistries) AssetRegistry() *assetregistry.Registry {
	if b == nil {
		return nil
	}
	return b.assets
}

// restoreAssetPublications captures the caller-visible revision before any
// authority lookup. A concurrent watcher/revoke therefore wins and restoration
// fails closed instead of replacing its newer graph.
func (b *PostgresLifecycleBoundaryRegistries) restoreAssetPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if b == nil || b.assets == nil {
		return nil
	}
	if ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot := b.assets.Snapshot()
	publications := coreAssetPublications(snapshot.Publications)
	if !safeMode {
		if b.assetAuthority == nil || b.assetAdmission == nil {
			return ErrLifecycleRegistryPublicationUnavailable
		}
		for _, item := range items {
			if item.Status != extensions.StatusEnabled || !extensionHasPublicAssets(item) {
				continue
			}
			impactDigest, err := b.assetAuthority.RestoreImpactDigest(ctx, item)
			if errors.Is(err, extensions.ErrTrustGrantNotFound) ||
				errors.Is(err, extensions.ErrLifecycleAuthorityNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("restore asset authority for %s: %w", item.ID, err)
			}
			publication, err := extensions.BuildPublicAssetPublication(item, impactDigest)
			if err != nil {
				return fmt.Errorf("restore asset registry for %s: %w", item.ID, err)
			}
			if err := b.assetAdmission.ValidatePublishedIdentity(ctx, enabledAssetExtension(item), publication.Artifact); err != nil {
				if errors.Is(err, extensions.ErrTrustGrantNotFound) ||
					errors.Is(err, extensions.ErrFrontendPackageChanged) {
					continue
				}
				return fmt.Errorf("restore asset admission for %s: %w", item.ID, err)
			}
			publications = append(publications, publication)
		}
	}
	var err error
	publications, err = convergedLifecycleAssetRestoreGraph(publications)
	if err != nil {
		return wrapLifecycleAssetError("converge restored asset graph", err)
	}
	if _, err := b.assets.ReplaceAllIfRevision(snapshot.Revision, publications); err != nil {
		return wrapLifecycleAssetError("restore asset registry publication", err)
	}
	return nil
}

// convergedLifecycleAssetRestoreGraph applies the same transitive hard-
// dependency closure as QuarantineExact when a revoked owner was excluded
// before startup. Cycles and declaration conflicts still fail validation.
func convergedLifecycleAssetRestoreGraph(
	publications []assetregistry.Publication,
) ([]assetregistry.Publication, error) {
	remaining := append([]assetregistry.Publication(nil), publications...)
	for {
		available := make(map[string]struct{})
		for _, publication := range remaining {
			for _, asset := range publication.Assets {
				available[strings.ToLower(strings.TrimSpace(asset.Handle))] = struct{}{}
			}
		}
		invalid := make(map[string]struct{})
		for _, publication := range remaining {
			for _, asset := range publication.Assets {
				for _, dependency := range asset.Dependencies {
					dependency = strings.ToLower(strings.TrimSpace(dependency))
					if strings.HasPrefix(dependency, "core.asset.") {
						continue
					}
					if _, ok := available[dependency]; !ok {
						if publication.Artifact.Core {
							return nil, assetregistry.ErrDependency
						}
						invalid[publication.Artifact.ExtensionID] = struct{}{}
						break
					}
				}
			}
		}
		if len(invalid) == 0 {
			break
		}
		next := make([]assetregistry.Publication, 0, len(remaining)-len(invalid))
		for _, publication := range remaining {
			if _, remove := invalid[publication.Artifact.ExtensionID]; !remove {
				next = append(next, publication)
			}
		}
		remaining = next
	}
	probe := assetregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, remaining); err != nil {
		return nil, err
	}
	return probe.Snapshot().Publications, nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeAssetMaterials(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.assets == nil {
		return nil
	}
	if b.assetAuthority == nil || b.assetAdmission == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	if source != nil && extensionHasPublicAssets(source.extension) {
		impact, err := b.assetAuthority.RestoreImpactDigest(ctx, source.extension)
		if err != nil {
			return fmt.Errorf("freeze source asset authority: %w", err)
		}
		if err := b.freezeAssetMaterial(ctx, source, impact, false); err != nil {
			return err
		}
	}
	if target != nil && extensionHasPublicAssets(target.extension) {
		impact, err := b.assetAuthority.OperationImpactDigest(ctx, request.OperationID, target.extension)
		if err != nil {
			return fmt.Errorf("freeze target asset authority: %w", err)
		}
		if err := b.freezeAssetMaterial(ctx, target, impact, true); err != nil {
			return err
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) freezeAssetMaterial(
	ctx context.Context,
	material *lifecycleRegistryMaterial,
	impactDigest string,
	requireAdmission bool,
) error {
	publication, err := extensions.BuildPublicAssetPublication(material.extension, impactDigest)
	if err != nil {
		return wrapLifecycleAssetError("build frozen asset publication", err)
	}
	material.assetPublication = &publication
	material.assetAdmitted = true
	if err := b.assetAdmission.ValidatePublishedIdentity(
		ctx,
		enabledAssetExtension(material.extension),
		publication.Artifact,
	); err != nil {
		if !requireAdmission && (errors.Is(err, extensions.ErrTrustGrantNotFound) ||
			errors.Is(err, extensions.ErrFrontendPackageChanged)) {
			material.assetAdmitted = false
		} else {
			return fmt.Errorf("validate frozen asset admission: %w", err)
		}
	}
	return refreshLifecycleRegistryMaterialDigest(material)
}

// lifecycleAssetPlanAttempts bounds how often one apply re-derives the graph
// after a concurrent asset registry publication moved the revision. Registering
// (prepare) and activation (apply) are separate steps, so any other publication
// in between would otherwise fail the whole lifecycle operation.
const lifecycleAssetPlanAttempts = 16

type lifecycleAssetPlan struct {
	mu       sync.Mutex
	revision uint64
	source   []assetregistry.Publication
	target   []assetregistry.Publication
}

func (b *PostgresLifecycleBoundaryRegistries) prepareAssetPlan(
	source, target *lifecycleRegistryMaterial,
) (*lifecycleAssetPlan, error) {
	if b == nil || b.assets == nil {
		return nil, nil
	}
	plan := &lifecycleAssetPlan{}
	if err := b.rebuildAssetPlan(plan, source, target); err != nil {
		return nil, err
	}
	return plan, nil
}

// rebuildAssetPlan re-derives both graphs from one live registry snapshot. The
// caller must hold plan.mu, or own a plan that is not published yet.
func (b *PostgresLifecycleBoundaryRegistries) rebuildAssetPlan(
	plan *lifecycleAssetPlan,
	source, target *lifecycleRegistryMaterial,
) error {
	snapshot := b.assets.Snapshot()
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	allowed := lifecycleAssetAllowedPublications(source, target)
	sourceDesired := admittedAssetPublication(source)
	targetDesired := admittedAssetPublication(target)
	sourceGraph, err := lifecycleAssetGraph(snapshot.Publications, extensionID, sourceDesired, allowed)
	if err != nil {
		return wrapLifecycleAssetError("prepare source asset graph", err)
	}
	targetGraph, err := lifecycleAssetGraph(snapshot.Publications, extensionID, targetDesired, allowed)
	if err != nil {
		return wrapLifecycleAssetError("prepare target asset graph", err)
	}
	plan.revision = snapshot.Revision
	plan.source = sourceGraph
	plan.target = targetGraph
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) applyAssetPlan(
	ctx context.Context,
	plan *lifecycleAssetPlan,
	phase LifecycleRegistryPublicationPhase,
) error {
	if b == nil || b.assets == nil || plan == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	desired := plan.source
	if phase == LifecycleRegistryPublicationTarget {
		desired = plan.target
	}
	revision, err := b.assets.ReplaceAllIfRevision(plan.revision, desired)
	if err != nil {
		return wrapLifecycleAssetError("apply asset registry graph", err)
	}
	plan.revision = revision
	return nil
}

// applyAssetPlanRetrying applies one prepared plan and re-derives it from the
// live registry snapshot whenever a concurrent publication moved the revision.
// Registering (prepare) and activation (apply) are separate lifecycle steps, so
// any unrelated publication in between would otherwise fail the whole exact
// operation even though its own graph is still consistent.
func (b *PostgresLifecycleBoundaryRegistries) applyAssetPlanRetrying(
	ctx context.Context,
	plan *lifecycleAssetPlan,
	phase LifecycleRegistryPublicationPhase,
	source, target *lifecycleRegistryMaterial,
) error {
	if b == nil || b.assets == nil || plan == nil {
		return nil
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	for attempt := 0; attempt < lifecycleAssetPlanAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		desired := plan.source
		if phase == LifecycleRegistryPublicationTarget {
			desired = plan.target
		}
		revision, err := b.assets.ReplaceAllIfRevision(plan.revision, desired)
		if err == nil {
			plan.revision = revision
			return nil
		}
		if !errors.Is(err, assetregistry.ErrRevisionConflict) {
			return wrapLifecycleAssetError("apply asset registry graph", err)
		}
		// Re-derive from the live snapshot; lifecycleAssetGraph still fails
		// closed when this owner's publication is not the exact source/target.
		if rebuildErr := b.rebuildAssetPlan(plan, source, target); rebuildErr != nil {
			return rebuildErr
		}
	}
	return wrapLifecycleAssetError("apply asset registry graph", assetregistry.ErrRevisionConflict)
}

func lifecycleAssetGraph(
	base []assetregistry.Publication,
	extensionID string,
	desired *assetregistry.Publication,
	allowed map[assetregistry.Artifact]struct{},
) ([]assetregistry.Publication, error) {
	current, found := findAssetPublication(base, extensionID)
	if found {
		if _, ok := allowed[current.Artifact]; !ok {
			return nil, assetregistry.ErrArtifactConflict
		}
	}
	probe := assetregistry.New()
	if _, err := probe.ReplaceAllIfRevision(0, base); err != nil {
		return nil, err
	}
	if desired == nil {
		if found {
			if _, _, err := probe.QuarantineExact(current.Artifact); err != nil {
				return nil, err
			}
		}
		return probe.Snapshot().Publications, nil
	}
	publications := make([]assetregistry.Publication, 0, len(base)+1)
	for _, publication := range base {
		if publication.Artifact.ExtensionID != extensionID {
			publications = append(publications, publication)
		}
	}
	publications = append(publications, *desired)
	if _, err := probe.ReplaceAllIfRevision(probe.Revision(), publications); err != nil {
		return nil, err
	}
	return probe.Snapshot().Publications, nil
}

func lifecycleAssetAllowedPublications(
	materials ...*lifecycleRegistryMaterial,
) map[assetregistry.Artifact]struct{} {
	allowed := make(map[assetregistry.Artifact]struct{}, len(materials))
	for _, material := range materials {
		if material != nil && material.assetPublication != nil {
			allowed[material.assetPublication.Artifact] = struct{}{}
		}
	}
	return allowed
}

func admittedAssetPublication(material *lifecycleRegistryMaterial) *assetregistry.Publication {
	if material == nil || material.assetPublication == nil || !material.assetAdmitted {
		return nil
	}
	value := *material.assetPublication
	value.Assets = append([]assetregistry.Declaration(nil), material.assetPublication.Assets...)
	return &value
}

func findAssetPublication(
	publications []assetregistry.Publication,
	extensionID string,
) (assetregistry.Publication, bool) {
	for _, publication := range publications {
		if publication.Artifact.ExtensionID == extensionID {
			return publication, true
		}
	}
	return assetregistry.Publication{}, false
}

func coreAssetPublications(publications []assetregistry.Publication) []assetregistry.Publication {
	result := make([]assetregistry.Publication, 0, len(publications))
	for _, publication := range publications {
		if publication.Artifact.OwnerKind == assetregistry.OwnerKindCore && publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Artifact.ExtensionID < result[j].Artifact.ExtensionID
	})
	return result
}

func extensionHasPublicAssets(extension extensions.Extension) bool {
	manifest := extensionmanifest.Normalize(extension.Manifest)
	for _, asset := range manifest.Assets {
		switch strings.ToLower(strings.TrimSpace(asset.Type)) {
		case "script", "style":
			return true
		}
	}
	for _, component := range manifest.Components {
		if strings.TrimSpace(component.L2Component) != "" && component.Action != extensionmanifest.ComponentActionHide {
			return true
		}
	}
	for _, editor := range manifest.Editor {
		if strings.TrimSpace(editor.L2Module) != "" && strings.TrimSpace(editor.L2Digest) != "" {
			return true
		}
	}
	return false
}

func enabledAssetExtension(extension extensions.Extension) extensions.Extension {
	extension.Status = extensions.StatusEnabled
	return extension
}

func wrapLifecycleAssetError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, assetregistry.ErrArtifactConflict) || errors.Is(err, assetregistry.ErrRevisionConflict) ||
		errors.Is(err, assetregistry.ErrConflict) || errors.Is(err, assetregistry.ErrDependency) ||
		errors.Is(err, assetregistry.ErrInvalid) {
		return fmt.Errorf("%w: %s: %w", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

var _ LifecycleAssetAuthority = (*PostgresLifecycleAssetAuthority)(nil)
