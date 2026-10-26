package editorregistry

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
	maxPublications          = 512
	maxEditorTotal           = 4096
	maxEditorPerPublication  = 256
	maxIDLength              = extensionmanifest.ManifestIDMaximumLength
	maxContractVersionLength = extensionmanifest.ContractVersionMaximumLength
	maxSchemaRefLength       = extensionmanifest.SchemaReferenceMaximumLength
	maxExtensionVersionLen   = 128
	maxRuntimeInstanceIDLen  = 512
	maxLabelLength           = 128
	maxIconLength            = 128
	maxGroupLength           = 64
	maxCommandKeyLength      = 128
	maxExtensionNameLength   = 128
	maxL2ModuleLength        = 512
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	schemaPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	// Tiptap extension/command names are camelCase or dotted identifiers.
	extensionNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,127}$`)
	commandKeyPattern    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,127}$`)
)

func normalizePublications(input []Publication) ([]Publication, error) {
	if len(input) > maxPublications {
		return nil, ErrInvalid
	}
	result := make([]Publication, 0, len(input))
	seen := map[string]bool{}
	editorCount := 0
	for _, publication := range input {
		normalized, err := normalizePublication(publication)
		if err != nil {
			return nil, err
		}
		if seen[normalized.Artifact.ExtensionID] {
			return nil, fmt.Errorf("%w: duplicate publication %s", ErrConflict, normalized.Artifact.ExtensionID)
		}
		seen[normalized.Artifact.ExtensionID] = true
		editorCount += len(normalized.Editor)
		if editorCount > maxEditorTotal {
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
	if len(input.Editor) > maxEditorPerPublication {
		return Publication{}, ErrInvalid
	}
	result := Publication{Artifact: artifact}
	seen := map[string]bool{}
	commands := map[string]bool{}
	for _, raw := range input.Editor {
		declaration, declErr := normalizeDeclaration(artifact, raw)
		if declErr != nil {
			return Publication{}, declErr
		}
		if seen[declaration.ID] {
			return Publication{}, fmt.Errorf("%w: duplicate editor id %s in publication", ErrConflict, declaration.ID)
		}
		seen[declaration.ID] = true
		if declaration.Kind == KindCommand {
			commands[declaration.ID] = true
		}
		result.Editor = append(result.Editor, declaration)
	}
	// Toolbar must reference a command declared in the same publication.
	// Cross-package command wiring is intentionally out of scope for @1.
	for _, declaration := range result.Editor {
		if declaration.Kind != KindToolbar {
			continue
		}
		if !commands[declaration.CommandID] {
			return Publication{}, fmt.Errorf("%w: toolbar %s references missing command %s",
				ErrInvalid, declaration.ID, declaration.CommandID)
		}
	}
	sort.Slice(result.Editor, func(i, j int) bool {
		if result.Editor[i].Kind != result.Editor[j].Kind {
			return result.Editor[i].Kind < result.Editor[j].Kind
		}
		if result.Editor[i].Order != result.Editor[j].Order {
			return result.Editor[i].Order < result.Editor[j].Order
		}
		return result.Editor[i].ID < result.Editor[j].ID
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

// NewCoreArtifact is the Host-only authority boundary for core editor catalogs.
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
	input.Schema = strings.TrimSpace(input.Schema)
	input.ExtensionName = strings.TrimSpace(input.ExtensionName)
	input.L2Module = strings.TrimSpace(input.L2Module)
	input.L2Digest = normalizeDigest(input.L2Digest)
	input.CommandKey = strings.TrimSpace(input.CommandKey)
	input.CommandID = strings.ToLower(strings.TrimSpace(input.CommandID))
	input.Label = strings.TrimSpace(input.Label)
	input.Icon = strings.TrimSpace(input.Icon)
	input.Group = strings.ToLower(strings.TrimSpace(input.Group))
	input.Permission = strings.TrimSpace(input.Permission)

	if !validContributionIdentity(artifact, input.ID, input.ContractVersion) || !validKind(input.Kind) {
		return Declaration{}, ErrInvalid
	}
	if input.Permission != "" && !validBoundedOpaque(input.Permission, maxIDLength) {
		return Declaration{}, ErrInvalid
	}
	switch input.Kind {
	case KindNode, KindMark:
		if !validSchemaRef(input.Schema) ||
			!extensionNamePattern.MatchString(input.ExtensionName) ||
			len(input.ExtensionName) > maxExtensionNameLength ||
			!validL2Module(input.L2Module) ||
			!digestPattern.MatchString(input.L2Digest) ||
			input.CommandKey != "" || input.CommandID != "" {
			return Declaration{}, ErrInvalid
		}
	case KindCommand:
		if !commandKeyPattern.MatchString(input.CommandKey) ||
			len(input.CommandKey) > maxCommandKeyLength ||
			input.Schema != "" || input.CommandID != "" {
			return Declaration{}, ErrInvalid
		}
		// Commands may ship inside a node/mark L2 module or a dedicated module.
		if input.L2Module != "" || input.L2Digest != "" {
			if !validL2Module(input.L2Module) || !digestPattern.MatchString(input.L2Digest) {
				return Declaration{}, ErrInvalid
			}
		}
		if input.ExtensionName != "" &&
			(!extensionNamePattern.MatchString(input.ExtensionName) || len(input.ExtensionName) > maxExtensionNameLength) {
			return Declaration{}, ErrInvalid
		}
	case KindToolbar:
		if input.CommandID == "" || !idPattern.MatchString(input.CommandID) ||
			!strings.HasPrefix(input.CommandID, artifact.ExtensionID+".") ||
			input.Label == "" || len(input.Label) > maxLabelLength ||
			input.Schema != "" || input.ExtensionName != "" ||
			input.L2Module != "" || input.L2Digest != "" || input.CommandKey != "" {
			return Declaration{}, ErrInvalid
		}
		if input.Icon != "" && (len(input.Icon) > maxIconLength || strings.ContainsRune(input.Icon, '\x00')) {
			return Declaration{}, ErrInvalid
		}
		if input.Group != "" && (len(input.Group) > maxGroupLength || !idPattern.MatchString(input.Group)) {
			return Declaration{}, ErrInvalid
		}
	default:
		return Declaration{}, ErrInvalid
	}
	return input, nil
}

func validContributionIdentity(artifact Artifact, id, contract string) bool {
	return idPattern.MatchString(id) && strings.HasPrefix(id, artifact.ExtensionID+".") &&
		contractPattern.MatchString(contract) &&
		len(id) <= maxIDLength && len(contract) <= maxContractVersionLength
}

func validKind(value string) bool {
	switch value {
	case KindNode, KindMark, KindCommand, KindToolbar:
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

func validL2Module(value string) bool {
	if value == "" || len(value) > maxL2ModuleLength {
		return false
	}
	clean, ok := extensionmanifest.SafeArchivePath(value)
	if !ok || clean != value {
		return false
	}
	// Prebuilt trusted L2 editor modules are ESM only.
	return strings.HasSuffix(value, ".mjs") || strings.HasSuffix(value, ".js")
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

func publicationEditorDigest(editor []Declaration) string {
	body, _ := json.Marshal(editor)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
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
	return reflectDeepEqual(left, right)
}

func reflectDeepEqual(left, right any) bool {
	leftBody, _ := json.Marshal(left)
	rightBody, _ := json.Marshal(right)
	return string(leftBody) == string(rightBody)
}
