package entityregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	maxPublications           = 512
	maxEntitiesTotal          = 4096
	maxEntitiesPerPublication = 256
	maxIDLength               = extensionmanifest.ManifestIDMaximumLength
	maxContractVersionLength  = extensionmanifest.ContractVersionMaximumLength
	maxSchemaRefLength        = extensionmanifest.SchemaReferenceMaximumLength
	maxExtensionVersionLen    = 128
	maxRuntimeInstanceIDLen   = 512
	maxLabelLength            = 128
	maxStorageKeyLength       = 128
	maxUIComponentLength      = 128
	maxUIModuleLength         = 512
	maxPermissionLength       = maxIDLength
	maxRefsPerDeclaration     = 64
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	schemaPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	// Storage keys are lower-snake package namespaces (extension_id.entity_key).
	storageKeyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)
	uiComponentPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,127}$`)
)

func normalizePublications(input []Publication) ([]Publication, error) {
	if len(input) > maxPublications {
		return nil, ErrInvalid
	}
	result := make([]Publication, 0, len(input))
	seen := map[string]bool{}
	entityCount := 0
	storageKeys := map[string]string{}
	for _, publication := range input {
		normalized, err := normalizePublication(publication)
		if err != nil {
			return nil, err
		}
		if seen[normalized.Artifact.ExtensionID] {
			return nil, fmt.Errorf("%w: duplicate publication %s", ErrConflict, normalized.Artifact.ExtensionID)
		}
		seen[normalized.Artifact.ExtensionID] = true
		entityCount += len(normalized.Entities)
		if entityCount > maxEntitiesTotal {
			return nil, ErrInvalid
		}
		for _, declaration := range normalized.Entities {
			if declaration.StorageKey == "" {
				continue
			}
			if owner, conflict := storageKeys[declaration.StorageKey]; conflict {
				return nil, fmt.Errorf("%w: storage key %s owned by %s and %s",
					ErrConflict, declaration.StorageKey, owner, normalized.Artifact.ExtensionID)
			}
			storageKeys[declaration.StorageKey] = normalized.Artifact.ExtensionID
		}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool {
		return artifactBefore(result[i].Artifact, result[j].Artifact)
	})
	return result, nil
}

func normalizePublication(input Publication) (Publication, error) {
	artifact, err := normalizeArtifact(input.Artifact)
	if err != nil {
		return Publication{}, ErrInvalid
	}
	if len(input.Entities) > maxEntitiesPerPublication {
		return Publication{}, ErrInvalid
	}
	result := Publication{Artifact: artifact}
	seen := map[string]bool{}
	localEntities := map[string]bool{}
	localTaxonomies := map[string]bool{}
	for _, raw := range input.Entities {
		declaration, declErr := normalizeDeclaration(artifact, raw)
		if declErr != nil {
			return Publication{}, declErr
		}
		if seen[declaration.ID] {
			return Publication{}, fmt.Errorf("%w: duplicate entity id %s in publication", ErrConflict, declaration.ID)
		}
		seen[declaration.ID] = true
		switch declaration.Kind {
		case KindEntity:
			localEntities[declaration.ID] = true
		case KindTaxonomy:
			localTaxonomies[declaration.ID] = true
		}
		result.Entities = append(result.Entities, declaration)
	}
	// Same-package refs are checked here. Cross-package field/taxonomy bindings
	// (plugin-extend-plugin) are validated at graph build once all publications
	// are visible; entity.taxonomyIds remain package-local owner declarations.
	for _, declaration := range result.Entities {
		switch declaration.Kind {
		case KindField:
			if strings.HasPrefix(declaration.EntityID, artifact.ExtensionID+".") &&
				!localEntities[declaration.EntityID] {
				return Publication{}, fmt.Errorf("%w: field %s references missing local entity %s",
					ErrInvalid, declaration.ID, declaration.EntityID)
			}
		case KindTaxonomy:
			for _, entityID := range declaration.EntityIDs {
				if strings.HasPrefix(entityID, artifact.ExtensionID+".") && !localEntities[entityID] {
					return Publication{}, fmt.Errorf("%w: taxonomy %s references missing local entity %s",
						ErrInvalid, declaration.ID, entityID)
				}
			}
		case KindEntity:
			for _, taxonomyID := range declaration.TaxonomyIDs {
				if !localTaxonomies[taxonomyID] {
					return Publication{}, fmt.Errorf("%w: entity %s references missing taxonomy %s",
						ErrInvalid, declaration.ID, taxonomyID)
				}
			}
		}
	}
	sort.Slice(result.Entities, func(i, j int) bool {
		if result.Entities[i].Kind != result.Entities[j].Kind {
			return result.Entities[i].Kind < result.Entities[j].Kind
		}
		if result.Entities[i].Order != result.Entities[j].Order {
			return result.Entities[i].Order < result.Entities[j].Order
		}
		return result.Entities[i].ID < result.Entities[j].ID
	})
	return result, nil
}

func normalizeArtifact(input Artifact) (Artifact, error) {
	input.ExtensionID = strings.ToLower(strings.TrimSpace(input.ExtensionID))
	input.ExtensionVersion = strings.TrimSpace(input.ExtensionVersion)
	input.PackageDigest = normalizeDigest(input.PackageDigest)
	input.RuntimeInstanceID = strings.TrimSpace(input.RuntimeInstanceID)
	isCoreNamespace := strings.HasPrefix(input.ExtensionID, "core.")
	isTrustedCore := validCoreArtifactSeal(input)
	if !idPattern.MatchString(input.ExtensionID) || input.ExtensionID == "core" ||
		len(input.ExtensionVersion) > maxExtensionVersionLen ||
		!digestPattern.MatchString(input.PackageDigest) {
		return Artifact{}, ErrInvalid
	}
	if _, err := semver.StrictNewVersion(input.ExtensionVersion); err != nil {
		return Artifact{}, ErrInvalid
	}
	if input.Core {
		if !isCoreNamespace || !isTrustedCore || input.VersionID != 0 || input.RuntimeInstanceID != "" {
			return Artifact{}, ErrInvalid
		}
	} else if input.coreSeal != [32]byte{} || input.VersionID <= 0 || isCoreNamespace ||
		input.RuntimeInstanceID != "" && !validBoundedOpaque(input.RuntimeInstanceID, maxRuntimeInstanceIDLen) {
		return Artifact{}, ErrInvalid
	}
	return input, nil
}

// NewCoreArtifact is the Host-only authority boundary for core entity catalogs.
func NewCoreArtifact(extensionID, extensionVersion, packageDigest string) (Artifact, error) {
	artifact := Artifact{
		ExtensionID:      strings.ToLower(strings.TrimSpace(extensionID)),
		ExtensionVersion: strings.TrimSpace(extensionVersion),
		PackageDigest:    normalizeDigest(packageDigest),
		Core:             true,
	}
	artifact.coreSeal = coreArtifactSeal(artifact)
	return normalizeArtifact(artifact)
}

func coreArtifactSeal(artifact Artifact) [32]byte {
	material := SchemaVersion + "\x00core-artifact\x00" + artifact.ExtensionID + "\x00" +
		artifact.ExtensionVersion + "\x00" + artifact.PackageDigest
	return sha256.Sum256([]byte(material))
}

func validCoreArtifactSeal(artifact Artifact) bool {
	return artifact.Core && artifact.coreSeal != [32]byte{} && artifact.coreSeal == coreArtifactSeal(artifact)
}

// IsHostCoreArtifact lets Host adapters distinguish Registry-issued Core.
func IsHostCoreArtifact(artifact Artifact) bool {
	normalized, err := normalizeArtifact(artifact)
	return err == nil && normalized.Core && validCoreArtifactSeal(normalized)
}

func IsExactArtifact(artifact Artifact) bool {
	normalized, err := normalizeArtifact(artifact)
	return err == nil && normalized == artifact
}

func normalizeDeclaration(artifact Artifact, input Declaration) (Declaration, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Label = strings.TrimSpace(input.Label)
	input.StorageKey = strings.ToLower(strings.TrimSpace(input.StorageKey))
	input.PermissionCreate = strings.TrimSpace(input.PermissionCreate)
	input.PermissionRead = strings.TrimSpace(input.PermissionRead)
	input.PermissionUpdate = strings.TrimSpace(input.PermissionUpdate)
	input.PermissionDelete = strings.TrimSpace(input.PermissionDelete)
	input.PermissionImport = strings.TrimSpace(input.PermissionImport)
	input.PermissionExport = strings.TrimSpace(input.PermissionExport)
	input.ImportExportPolicy = strings.ToLower(strings.TrimSpace(input.ImportExportPolicy))
	input.DeletionPolicy = strings.ToLower(strings.TrimSpace(input.DeletionPolicy))
	input.PermissionManage = strings.TrimSpace(input.PermissionManage)
	input.PermissionAssign = strings.TrimSpace(input.PermissionAssign)
	input.EntityID = strings.ToLower(strings.TrimSpace(input.EntityID))
	input.Schema = strings.TrimSpace(input.Schema)
	input.UIComponent = strings.TrimSpace(input.UIComponent)
	input.UIModule = strings.TrimSpace(input.UIModule)
	input.UIDigest = normalizeDigest(input.UIDigest)
	input.IndexKind = strings.ToLower(strings.TrimSpace(input.IndexKind))
	input.PermissionFieldRead = strings.TrimSpace(input.PermissionFieldRead)
	input.PermissionFieldWrite = strings.TrimSpace(input.PermissionFieldWrite)
	input.Validation = strings.TrimSpace(input.Validation)
	input.TaxonomyIDs = normalizeIDList(input.TaxonomyIDs)
	input.EntityIDs = normalizeIDList(input.EntityIDs)

	if !validContributionIdentity(artifact, input.ID, input.ContractVersion) || !validKind(input.Kind) {
		return Declaration{}, ErrInvalid
	}
	if input.Label != "" && (len(input.Label) > maxLabelLength || strings.ContainsRune(input.Label, '\x00')) {
		return Declaration{}, ErrInvalid
	}

	switch input.Kind {
	case KindEntity:
		if err := validateEntityDeclaration(artifact, &input); err != nil {
			return Declaration{}, err
		}
	case KindTaxonomy:
		if err := validateTaxonomyDeclaration(artifact, &input); err != nil {
			return Declaration{}, err
		}
	case KindField:
		if err := validateFieldDeclaration(artifact, &input); err != nil {
			return Declaration{}, err
		}
	default:
		return Declaration{}, ErrInvalid
	}
	return input, nil
}

func validateEntityDeclaration(artifact Artifact, input *Declaration) error {
	if input.StorageKey == "" || !storageKeyPattern.MatchString(input.StorageKey) ||
		len(input.StorageKey) > maxStorageKeyLength ||
		!strings.HasPrefix(input.StorageKey, artifact.ExtensionID+".") {
		return ErrInvalid
	}
	if input.Label == "" ||
		!validPermissionKey(input.PermissionCreate) ||
		!validPermissionKey(input.PermissionRead) ||
		!validPermissionKey(input.PermissionUpdate) ||
		!validPermissionKey(input.PermissionDelete) {
		return ErrInvalid
	}
	if !validImportExportPolicy(input.ImportExportPolicy) || !validDeletionPolicy(input.DeletionPolicy) {
		return ErrInvalid
	}
	// Import/export permission keys are required when policy admits the action.
	if policyAllowsImport(input.ImportExportPolicy) && !validPermissionKey(input.PermissionImport) {
		return ErrInvalid
	}
	if policyAllowsExport(input.ImportExportPolicy) && !validPermissionKey(input.PermissionExport) {
		return ErrInvalid
	}
	if input.ImportExportPolicy == ImportExportDeny {
		if input.PermissionImport != "" || input.PermissionExport != "" {
			return ErrInvalid
		}
	}
	// Entity-only fields must not carry taxonomy/field-only material.
	if input.EntityID != "" || input.Schema != "" || input.UIComponent != "" ||
		input.UIModule != "" || input.UIDigest != "" || input.Validation != "" ||
		input.PermissionManage != "" || input.PermissionAssign != "" ||
		input.PermissionFieldRead != "" || input.PermissionFieldWrite != "" ||
		input.IndexKind != "" || input.Indexed || input.Required || input.Hierarchical ||
		len(input.EntityIDs) > 0 {
		return ErrInvalid
	}
	for _, taxonomyID := range input.TaxonomyIDs {
		if !idPattern.MatchString(taxonomyID) || !strings.HasPrefix(taxonomyID, artifact.ExtensionID+".") {
			return ErrInvalid
		}
	}
	return nil
}

func validateTaxonomyDeclaration(artifact Artifact, input *Declaration) error {
	if input.StorageKey == "" || !storageKeyPattern.MatchString(input.StorageKey) ||
		len(input.StorageKey) > maxStorageKeyLength ||
		!strings.HasPrefix(input.StorageKey, artifact.ExtensionID+".") {
		return ErrInvalid
	}
	if input.Label == "" ||
		!validPermissionKey(input.PermissionManage) ||
		!validPermissionKey(input.PermissionAssign) ||
		len(input.EntityIDs) == 0 || len(input.EntityIDs) > maxRefsPerDeclaration {
		return ErrInvalid
	}
	// EntityIDs may be local or foreign (plugin-extend-plugin via required dep).
	for _, entityID := range input.EntityIDs {
		if !idPattern.MatchString(entityID) {
			return ErrInvalid
		}
	}
	// Taxonomy-only: no entity CRUD / field schema material.
	if input.PermissionCreate != "" || input.PermissionRead != "" ||
		input.PermissionUpdate != "" || input.PermissionDelete != "" ||
		input.PermissionImport != "" || input.PermissionExport != "" ||
		input.ImportExportPolicy != "" || input.DeletionPolicy != "" ||
		input.EntityID != "" || input.Schema != "" || input.UIComponent != "" ||
		input.UIModule != "" || input.UIDigest != "" || input.Validation != "" ||
		input.PermissionFieldRead != "" || input.PermissionFieldWrite != "" ||
		input.IndexKind != "" || input.Indexed || input.Required ||
		len(input.TaxonomyIDs) > 0 {
		return ErrInvalid
	}
	return nil
}

func validateFieldDeclaration(artifact Artifact, input *Declaration) error {
	// EntityID may be package-local or a foreign entity owned by a required
	// dependency (plugin-extend-plugin). Foreign IDs must still be stable ids.
	if input.EntityID == "" || !idPattern.MatchString(input.EntityID) ||
		!validSchemaRef(input.Schema) ||
		input.UIComponent == "" || !uiComponentPattern.MatchString(input.UIComponent) ||
		len(input.UIComponent) > maxUIComponentLength ||
		!validPermissionKey(input.PermissionFieldRead) ||
		!validPermissionKey(input.PermissionFieldWrite) {
		return ErrInvalid
	}
	if input.IndexKind == "" {
		if input.Indexed {
			return ErrInvalid
		}
		input.IndexKind = IndexNone
	}
	if !validIndexKind(input.IndexKind) {
		return ErrInvalid
	}
	if input.Indexed && input.IndexKind == IndexNone {
		return ErrInvalid
	}
	if !input.Indexed && input.IndexKind != IndexNone {
		// Explicit index kind without Indexed is invalid; force honesty.
		return ErrInvalid
	}
	if input.UIModule != "" || input.UIDigest != "" {
		if !validUIModule(input.UIModule) || !digestPattern.MatchString(input.UIDigest) {
			return ErrInvalid
		}
	}
	if input.Validation != "" && !validSchemaRef(input.Validation) {
		return ErrInvalid
	}
	// Field-only: no entity/taxonomy storage or CRUD material.
	if input.StorageKey != "" || input.PermissionCreate != "" || input.PermissionRead != "" ||
		input.PermissionUpdate != "" || input.PermissionDelete != "" ||
		input.PermissionImport != "" || input.PermissionExport != "" ||
		input.ImportExportPolicy != "" || input.DeletionPolicy != "" ||
		input.PermissionManage != "" || input.PermissionAssign != "" ||
		input.Hierarchical || len(input.EntityIDs) > 0 || len(input.TaxonomyIDs) > 0 {
		return ErrInvalid
	}
	return nil
}

func normalizeIDList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validContributionIdentity(artifact Artifact, id, contract string) bool {
	return idPattern.MatchString(id) && strings.HasPrefix(id, artifact.ExtensionID+".") &&
		contractPattern.MatchString(contract) &&
		len(id) <= maxIDLength && len(contract) <= maxContractVersionLength
}

func validKind(value string) bool {
	switch value {
	case KindEntity, KindTaxonomy, KindField:
		return true
	default:
		return false
	}
}

func validPermissionKey(value string) bool {
	return value != "" && idPattern.MatchString(value) && len(value) <= maxPermissionLength &&
		!strings.ContainsRune(value, '\x00')
}

func validImportExportPolicy(value string) bool {
	switch value {
	case ImportExportAllow, ImportExportDeny, ImportExportExportOnly, ImportExportImportOnly:
		return true
	default:
		return false
	}
}

func validDeletionPolicy(value string) bool {
	switch value {
	case DeletionSoft, DeletionHard, DeletionRetain:
		return true
	default:
		return false
	}
}

func validIndexKind(value string) bool {
	switch value {
	case IndexNone, IndexKeyword, IndexText, IndexNumeric, IndexBoolean:
		return true
	default:
		return false
	}
}

func policyAllowsImport(policy string) bool {
	return policy == ImportExportAllow || policy == ImportExportImportOnly
}

func policyAllowsExport(policy string) bool {
	return policy == ImportExportAllow || policy == ImportExportExportOnly
}

func validSchemaRef(value string) bool {
	if value == "" || len(value) > maxSchemaRefLength || strings.ContainsRune(value, '\x00') {
		return false
	}
	if schemaPattern.MatchString(value) {
		return true
	}
	clean, ok := extensionmanifest.SafeArchivePath(value)
	return ok && clean == value && strings.HasSuffix(value, ".json")
}

func validUIModule(value string) bool {
	if value == "" || len(value) > maxUIModuleLength {
		return false
	}
	clean, ok := extensionmanifest.SafeArchivePath(value)
	if !ok || clean != value {
		return false
	}
	return strings.HasSuffix(value, ".mjs") || strings.HasSuffix(value, ".js") ||
		strings.HasSuffix(value, ".vue")
}

func validBoundedOpaque(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func normalizeDigest(value string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
}

func computeGraphDigest(publications []Publication, safeMode bool) string {
	type digestDoc struct {
		Schema       string        `json:"schema"`
		SafeMode     bool          `json:"safeMode"`
		Publications []Publication `json:"publications"`
	}
	body, _ := json.Marshal(digestDoc{
		Schema: SchemaVersion, SafeMode: safeMode, Publications: publications,
	})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func artifactBefore(left, right Artifact) bool {
	if left.ExtensionID != right.ExtensionID {
		return left.ExtensionID < right.ExtensionID
	}
	if left.ExtensionVersion != right.ExtensionVersion {
		return left.ExtensionVersion < right.ExtensionVersion
	}
	return left.PackageDigest < right.PackageDigest
}

func contributionBefore(left, right Contribution) bool {
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Order != right.Order {
		return left.Order < right.Order
	}
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.ID != right.ID {
		return left.ID < right.ID
	}
	return left.Artifact.ExtensionID < right.Artifact.ExtensionID
}

func filterSafeModeInput(input []Publication, safeMode bool) []Publication {
	if !safeMode {
		return input
	}
	result := make([]Publication, 0, len(input))
	for _, publication := range input {
		if validCoreArtifactSeal(publication.Artifact) {
			result = append(result, publication)
		}
	}
	return result
}

func equalPublications(left, right Publication) bool {
	leftBody, _ := json.Marshal(left)
	rightBody, _ := json.Marshal(right)
	return string(leftBody) == string(rightBody)
}
