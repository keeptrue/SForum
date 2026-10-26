package contentregistry

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

// Bounds keep publication graphs finite. Individually valid declarations can
// otherwise explode startup work or snapshot cost.
const (
	maxPublications            = 512
	maxContentTotal            = 4096
	maxContentPerPublication   = extensionmanifest.ContentDeclarationsMaximum
	maxIDLength                = extensionmanifest.ManifestIDMaximumLength
	maxContractVersionLength   = extensionmanifest.ContractVersionMaximumLength
	maxSchemaRefLength         = extensionmanifest.SchemaReferenceMaximumLength
	maxHandlerLength           = extensionmanifest.HandlerReferenceMaximumLength
	maxTombstonesTotal         = 65536
	maxPublicationHistoryTotal = 16384
	maxExtensionVersionLength  = 128
	maxRuntimeInstanceIDLength = 512
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	schemaPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
)

func normalizePublications(input []Publication) ([]Publication, error) {
	if len(input) > maxPublications {
		return nil, ErrInvalid
	}
	result := make([]Publication, 0, len(input))
	seen := map[string]bool{}
	contentCount := 0
	for _, publication := range input {
		normalized, err := normalizePublication(publication)
		if err != nil {
			return nil, err
		}
		if seen[normalized.Artifact.ExtensionID] {
			return nil, fmt.Errorf("%w: duplicate publication %s", ErrConflict, normalized.Artifact.ExtensionID)
		}
		seen[normalized.Artifact.ExtensionID] = true
		contentCount += len(normalized.Content)
		if contentCount > maxContentTotal {
			return nil, ErrInvalid
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
	if len(input.Content) > maxContentPerPublication {
		return Publication{}, ErrInvalid
	}
	result := Publication{Artifact: artifact}
	seen := map[string]bool{}
	for _, raw := range input.Content {
		declaration, declErr := normalizeDeclaration(artifact, raw)
		if declErr != nil {
			return Publication{}, declErr
		}
		if seen[declaration.ID] {
			return Publication{}, fmt.Errorf("%w: duplicate content id %s in publication", ErrConflict, declaration.ID)
		}
		seen[declaration.ID] = true
		result.Content = append(result.Content, declaration)
	}
	if !artifact.Core && artifact.RuntimeInstanceID == "" {
		for _, declaration := range result.Content {
			if declaration.Handler != "" {
				return Publication{}, ErrInvalid
			}
		}
	}
	sort.Slice(result.Content, func(i, j int) bool {
		if result.Content[i].Kind != result.Content[j].Kind {
			return result.Content[i].Kind < result.Content[j].Kind
		}
		return result.Content[i].ID < result.Content[j].ID
	})
	return result, nil
}

func normalizeTombstone(input Tombstone) (Tombstone, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.OwnerExtensionID = strings.ToLower(strings.TrimSpace(input.OwnerExtensionID))
	input.DefinitionDigest = normalizeDigest(input.DefinitionDigest)
	hostPostBodyAlias := input.OwnerExtensionID == hostPostBodyCoreExtensionID && input.ID == HostPostBodyTargetID
	if !idPattern.MatchString(input.ID) || len(input.ID) > maxIDLength ||
		!contractPattern.MatchString(input.ContractVersion) || len(input.ContractVersion) > maxContractVersionLength ||
		!idPattern.MatchString(input.OwnerExtensionID) || input.OwnerExtensionID == "core" ||
		(!strings.HasPrefix(input.ID, input.OwnerExtensionID+".") && !hostPostBodyAlias) ||
		!digestPattern.MatchString(input.DefinitionDigest) {
		return Tombstone{}, ErrInvalid
	}
	return input, nil
}

func normalizePublicationRecord(input PublicationRecord) (PublicationRecord, error) {
	input.ExtensionID = strings.ToLower(strings.TrimSpace(input.ExtensionID))
	input.ExtensionVersion = strings.TrimSpace(input.ExtensionVersion)
	input.PackageDigest = normalizeDigest(input.PackageDigest)
	input.ContentDigest = normalizeDigest(input.ContentDigest)
	if !idPattern.MatchString(input.ExtensionID) || input.ExtensionID == "core" ||
		len(input.ExtensionVersion) > maxExtensionVersionLength ||
		!digestPattern.MatchString(input.PackageDigest) || !digestPattern.MatchString(input.ContentDigest) {
		return PublicationRecord{}, ErrInvalid
	}
	if _, err := semver.StrictNewVersion(input.ExtensionVersion); err != nil {
		return PublicationRecord{}, ErrInvalid
	}
	return input, nil
}

func normalizeArtifact(input Artifact) (Artifact, error) {
	input.ExtensionID = strings.ToLower(strings.TrimSpace(input.ExtensionID))
	input.ExtensionVersion = strings.TrimSpace(input.ExtensionVersion)
	input.PackageDigest = normalizeDigest(input.PackageDigest)
	input.RuntimeInstanceID = strings.TrimSpace(input.RuntimeInstanceID)
	isCoreNamespace := strings.HasPrefix(input.ExtensionID, "core.")
	isTrustedCore := validCoreArtifactSeal(input)
	if !idPattern.MatchString(input.ExtensionID) || input.ExtensionID == "core" ||
		len(input.ExtensionVersion) > maxExtensionVersionLength ||
		!digestPattern.MatchString(input.PackageDigest) {
		return Artifact{}, ErrInvalid
	}
	if _, err := semver.StrictNewVersion(input.ExtensionVersion); err != nil {
		return Artifact{}, ErrInvalid
	}
	if input.Core {
		// Host-sealed Core only: no VersionID/runtime, private seal required.
		if !isCoreNamespace || !isTrustedCore || input.VersionID != 0 || input.RuntimeInstanceID != "" {
			return Artifact{}, ErrInvalid
		}
	} else if input.coreSeal != [32]byte{} || input.VersionID <= 0 || isCoreNamespace ||
		input.RuntimeInstanceID != "" && !validBoundedOpaque(input.RuntimeInstanceID, maxRuntimeInstanceIDLength) {
		// Third-party packages need an exact VersionID and cannot claim the Host
		// namespace. Runtime is conditionally required after declarations parse.
		return Artifact{}, ErrInvalid
	}
	return input, nil
}

// NewCoreArtifact is the explicit Host-only authority boundary for core content
// catalogs. Callers must never feed extension-controlled values into it. The
// private seal prevents JSON or an Artifact literal in another package from
// promoting a publication by setting Core=true or choosing a core.* ID.
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

// IsHostCoreArtifact lets Host adapters distinguish a Registry-issued Core
// identity from a caller-constructed Core flag without exposing seal material.
func IsHostCoreArtifact(artifact Artifact) bool {
	normalized, err := normalizeArtifact(artifact)
	return err == nil && normalized.Core && validCoreArtifactSeal(normalized)
}

// IsExactArtifact reports whether an Artifact is already in the canonical,
// validated form emitted by Registry snapshots. Host adapters use it to reject
// caller-constructed identities before consulting a runtime backend.
func IsExactArtifact(artifact Artifact) bool {
	normalized, err := normalizeArtifact(artifact)
	return err == nil && normalized == artifact
}

func normalizeDeclaration(artifact Artifact, input Declaration) (Declaration, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Handler = strings.TrimSpace(input.Handler)
	input.Schema = strings.TrimSpace(input.Schema)
	input.Renderer = strings.TrimSpace(input.Renderer)
	input.Migration = strings.TrimSpace(input.Migration)

	if !validContributionIdentity(artifact, input.ID, input.ContractVersion) ||
		!validKind(input.Kind) || !validSchemaRef(input.Schema) ||
		// Manifest requires at least one of handler/renderer; both may coexist.
		(input.Handler == "" && input.Renderer == "") {
		return Declaration{}, ErrInvalid
	}
	if input.Handler != "" && !validHandler(input.Handler) {
		return Declaration{}, ErrInvalid
	}
	// Renderer/migration remain opaque identity references only. Cross-registry
	// existence checks belong to Manifest validation, not this leaf registry.
	if input.Renderer != "" && !validOpaqueRef(input.Renderer) {
		return Declaration{}, ErrInvalid
	}
	if input.Migration != "" && !validOpaqueRef(input.Migration) {
		return Declaration{}, ErrInvalid
	}
	return input, nil
}

func validContributionIdentity(artifact Artifact, id, contract string) bool {
	hostPostBodyAlias := IsHostCoreArtifact(artifact) && artifact.ExtensionID == hostPostBodyCoreExtensionID && id == HostPostBodyTargetID
	return idPattern.MatchString(id) && (strings.HasPrefix(id, artifact.ExtensionID+".") || hostPostBodyAlias) &&
		contractPattern.MatchString(contract) &&
		len(id) <= maxIDLength && len(contract) <= maxContractVersionLength
}

func validKind(value string) bool {
	switch value {
	case KindBlock, KindShortcode, KindEmbed, KindNode, KindMark, KindRenderFilter, KindSanitizer:
		return true
	default:
		return false
	}
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

func validHandler(value string) bool {
	if value == "" || len(value) > maxHandlerLength {
		return false
	}
	if strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

// validOpaqueRef accepts stable identity tokens only. Renderer points at a
// template id and migration at a migration id; neither is a free-form path.
func validOpaqueRef(value string) bool {
	return len(value) <= maxSchemaRefLength && idPattern.MatchString(value)
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

func declarationDigest(declaration Declaration) string {
	body, _ := json.Marshal(declaration)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func publicationContentDigest(content []Declaration) string {
	if content == nil {
		content = []Declaration{}
	}
	body, _ := json.Marshal(content)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func computeGraphDigest(publications []Publication, tombstones []Tombstone, history []PublicationRecord, safeMode bool) string {
	if publications == nil {
		publications = []Publication{}
	}
	if tombstones == nil {
		tombstones = []Tombstone{}
	}
	if history == nil {
		history = []PublicationRecord{}
	}
	document := struct {
		SchemaVersion string              `json:"schemaVersion"`
		SafeMode      bool                `json:"safeMode"`
		Publications  []Publication       `json:"publications"`
		Tombstones    []Tombstone         `json:"tombstones"`
		History       []PublicationRecord `json:"history"`
	}{
		SchemaVersion: SchemaVersion, SafeMode: safeMode, Publications: publications,
		Tombstones: tombstones, History: history,
	}
	body, _ := json.Marshal(document)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func tombstoneKey(value Tombstone) string {
	return value.ID + "\x00" + value.ContractVersion
}

func publicationHistoryKey(value PublicationRecord) string {
	return value.ExtensionID + "\x00" + value.ExtensionVersion + "\x00" + value.PackageDigest
}

func artifactBefore(left, right Artifact) bool {
	if left.Core != right.Core {
		return left.Core
	}
	if left.ExtensionID != right.ExtensionID {
		return left.ExtensionID < right.ExtensionID
	}
	if left.ExtensionVersion != right.ExtensionVersion {
		return left.ExtensionVersion < right.ExtensionVersion
	}
	if left.PackageDigest != right.PackageDigest {
		return left.PackageDigest < right.PackageDigest
	}
	if left.VersionID != right.VersionID {
		return left.VersionID < right.VersionID
	}
	return left.RuntimeInstanceID < right.RuntimeInstanceID
}

// filterSafeModeInput drops non-core publications before validation so a
// corrupt third-party declaration cannot block Host Safe Mode recovery.
func filterSafeModeInput(publications []Publication, safeMode bool) []Publication {
	if !safeMode {
		return publications
	}
	result := make([]Publication, 0, len(publications))
	for _, publication := range publications {
		if validCoreArtifactSeal(publication.Artifact) {
			result = append(result, publication)
		}
	}
	return result
}
