package identityregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const (
	maxPublications            = 512
	maxPermissionsTotal        = 4096
	maxUserFieldsTotal         = 4096
	maxProvidersTotal          = 4096
	maxPermissions             = 512
	maxUserFields              = 256
	maxProviders               = 128
	maxRecommendedRoles        = 64
	maxRiskHooks               = 128
	maxRuntimeInstanceIDLength = 512
	maxSchemaReferenceLength   = extensionmanifest.SchemaReferenceMaximumLength
	maxProviderOperations      = extensionmanifest.ManifestIdentityProviderMaximumOperations
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
	contractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func NewCoreArtifact(extensionID, extensionVersion, packageDigest string) (Artifact, error) {
	artifact := Artifact{
		ExtensionID: strings.ToLower(strings.TrimSpace(extensionID)), ExtensionVersion: strings.TrimSpace(extensionVersion),
		PackageDigest: strings.ToLower(strings.TrimSpace(packageDigest)), Core: true,
	}
	if !strings.HasPrefix(artifact.ExtensionID, "core.") || !idPattern.MatchString(artifact.ExtensionID) ||
		!strictSemVer(artifact.ExtensionVersion) || !digestPattern.MatchString(artifact.PackageDigest) {
		return Artifact{}, ErrInvalid
	}
	artifact.coreSeal = coreArtifactSeal(artifact)
	return artifact, nil
}

func normalizeArtifact(input Artifact) (Artifact, error) {
	input.ExtensionID = strings.ToLower(strings.TrimSpace(input.ExtensionID))
	input.ExtensionVersion = strings.TrimSpace(input.ExtensionVersion)
	input.PackageDigest = strings.ToLower(strings.TrimSpace(input.PackageDigest))
	input.RuntimeInstanceID = strings.TrimSpace(input.RuntimeInstanceID)
	if !idPattern.MatchString(input.ExtensionID) || !strictSemVer(input.ExtensionVersion) ||
		!digestPattern.MatchString(input.PackageDigest) {
		return Artifact{}, ErrInvalid
	}
	if input.Core {
		if !strings.HasPrefix(input.ExtensionID, "core.") || input.VersionID != 0 || input.RuntimeInstanceID != "" ||
			input.coreSeal != coreArtifactSeal(input) {
			return Artifact{}, ErrInvalid
		}
	} else if input.VersionID <= 0 || input.coreSeal != ([32]byte{}) || strings.HasPrefix(input.ExtensionID, "core.") ||
		len(input.RuntimeInstanceID) > maxRuntimeInstanceIDLength {
		return Artifact{}, ErrInvalid
	}
	return input, nil
}

func strictSemVer(value string) bool {
	_, err := semver.StrictNewVersion(value)
	return err == nil
}

func coreArtifactSeal(artifact Artifact) [32]byte {
	return sha256.Sum256([]byte("sforum.identity-registry.core\x00" + artifact.ExtensionID + "\x00" + artifact.ExtensionVersion + "\x00" + artifact.PackageDigest))
}

func validCoreArtifactSeal(artifact Artifact) bool {
	return artifact.Core && strings.HasPrefix(artifact.ExtensionID, "core.") &&
		artifact.VersionID == 0 && artifact.RuntimeInstanceID == "" &&
		artifact.coreSeal == coreArtifactSeal(artifact)
}

func normalizePublication(input Publication) (Publication, error) {
	return normalizePublicationMode(input, false)
}

// normalizeHistoricalDurablePublication only decodes exact root evidence that
// predates executable session providers. Live publication never uses this mode.
func normalizeHistoricalDurablePublication(input Publication) (Publication, error) {
	return normalizePublicationMode(input, true)
}

func normalizePublicationMode(input Publication, allowLegacyUnboundSessionPolicy bool) (Publication, error) {
	artifact, err := normalizeArtifact(input.Artifact)
	if err != nil || len(input.Permissions) > maxPermissions {
		return Publication{}, ErrInvalid
	}
	result := Publication{Artifact: artifact}
	permissionKeys := make(map[string]struct{}, len(input.Permissions))
	for _, permission := range input.Permissions {
		normalized, normalizeErr := normalizePermission(artifact, permission)
		if normalizeErr != nil {
			return Publication{}, normalizeErr
		}
		if _, duplicate := permissionKeys[normalized.Key]; duplicate {
			return Publication{}, ErrConflict
		}
		permissionKeys[normalized.Key] = struct{}{}
		result.Permissions = append(result.Permissions, normalized)
	}
	sort.Slice(result.Permissions, func(i, j int) bool { return result.Permissions[i].Key < result.Permissions[j].Key })
	if input.Identity != nil {
		identity, normalizeErr := normalizeIdentity(
			artifact, *input.Identity, permissionKeys, allowLegacyUnboundSessionPolicy,
		)
		if normalizeErr != nil {
			return Publication{}, normalizeErr
		}
		result.Identity = &identity
	}
	return result, nil
}

func normalizePermission(artifact Artifact, input PermissionDefinition) (PermissionDefinition, error) {
	input.Key = strings.ToLower(strings.TrimSpace(input.Key))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Label = strings.TrimSpace(input.Label)
	input.Description = strings.TrimSpace(input.Description)
	input.LabelLocales = normalizePermissionLocales(input.LabelLocales)
	input.DescriptionLocales = normalizePermissionLocales(input.DescriptionLocales)
	input.AssignmentPolicy = strings.ToLower(strings.TrimSpace(input.AssignmentPolicy))
	if !ownedID(artifact, input.Key) || !contractPattern.MatchString(input.ContractVersion) ||
		input.Label == "" || input.Description == "" || input.AssignmentPolicy != "host" ||
		len(input.RecommendedRoles) > maxRecommendedRoles {
		return PermissionDefinition{}, ErrInvalid
	}
	roles := make([]string, 0, len(input.RecommendedRoles))
	seen := map[string]struct{}{}
	for _, role := range input.RecommendedRoles {
		role = strings.ToLower(strings.TrimSpace(role))
		// super_admin is an invariant, not a recommendation target. The Host
		// already grants it all permissions in policy code.
		if !idPattern.MatchString(role) || role == "super_admin" {
			return PermissionDefinition{}, ErrInvalid
		}
		if _, duplicate := seen[role]; duplicate {
			return PermissionDefinition{}, ErrInvalid
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	input.RecommendedRoles = roles
	return input, nil
}

func normalizePermissionLocales(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for locale, value := range input {
		locale = strings.TrimSpace(locale)
		value = strings.TrimSpace(value)
		if locale != "" && value != "" {
			result[locale] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeIdentity(
	artifact Artifact,
	input IdentityDeclaration,
	permissions map[string]struct{},
	allowLegacyUnboundSessionPolicy bool,
) (IdentityDeclaration, error) {
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.SessionPolicy = strings.ToLower(strings.TrimSpace(input.SessionPolicy))
	if !contractPattern.MatchString(input.ContractVersion) || len(input.UserFields) > maxUserFields ||
		len(input.Providers) > maxProviders || len(input.RiskHooks) > maxRiskHooks {
		return IdentityDeclaration{}, ErrInvalid
	}
	if input.SessionPolicy != "" && input.SessionPolicy != "core.session.default" && !ownedID(artifact, input.SessionPolicy) {
		return IdentityDeclaration{}, ErrInvalid
	}
	result := IdentityDeclaration{ContractVersion: input.ContractVersion, SessionPolicy: input.SessionPolicy}
	fieldIDs := map[string]struct{}{}
	for _, field := range input.UserFields {
		field.ID = strings.ToLower(strings.TrimSpace(field.ID))
		field.ContractVersion = strings.TrimSpace(field.ContractVersion)
		field.Type = strings.ToLower(strings.TrimSpace(field.Type))
		field.Schema = strings.TrimSpace(field.Schema)
		field.SchemaWireReference = strings.TrimSpace(field.SchemaWireReference)
		field.SchemaDigest = strings.ToLower(strings.TrimSpace(field.SchemaDigest))
		field.ReadPermission = strings.ToLower(strings.TrimSpace(field.ReadPermission))
		field.WritePermission = strings.ToLower(strings.TrimSpace(field.WritePermission))
		if !ownedID(artifact, field.ID) || !contractPattern.MatchString(field.ContractVersion) ||
			!validSchemaReference(field.Schema) || !validFieldType(field.Type) ||
			!declaredPermission(field.ReadPermission, permissions) || !declaredPermission(field.WritePermission, permissions) {
			return IdentityDeclaration{}, ErrInvalid
		}
		if _, duplicate := fieldIDs[field.ID]; duplicate {
			return IdentityDeclaration{}, ErrConflict
		}
		fieldIDs[field.ID] = struct{}{}
		result.UserFields = append(result.UserFields, cloneUserField(field))
	}
	providerIDs := map[string]struct{}{}
	for _, provider := range input.Providers {
		provider.ID = strings.ToLower(strings.TrimSpace(provider.ID))
		provider.ContractVersion = strings.TrimSpace(provider.ContractVersion)
		provider.Kind = strings.ToLower(strings.TrimSpace(provider.Kind))
		provider.Handler = strings.TrimSpace(provider.Handler)
		if !ownedID(artifact, provider.ID) || !contractPattern.MatchString(provider.ContractVersion) ||
			!validProviderKind(provider.Kind) || !validHandler(provider.Handler) ||
			len(provider.Operations) > maxProviderOperations {
			return IdentityDeclaration{}, ErrInvalid
		}
		if _, duplicate := providerIDs[provider.ID]; duplicate {
			return IdentityDeclaration{}, ErrConflict
		}
		providerIDs[provider.ID] = struct{}{}
		var operations []ProviderOperation
		if len(provider.Operations) > 0 {
			operations = make([]ProviderOperation, 0, len(provider.Operations))
		}
		seenOperations := make(map[string]struct{}, len(provider.Operations))
		for _, operation := range provider.Operations {
			normalized, operationErr := normalizeProviderOperation(provider.Kind, operation)
			if operationErr != nil {
				return IdentityDeclaration{}, operationErr
			}
			if _, duplicate := seenOperations[normalized.Name]; duplicate {
				return IdentityDeclaration{}, ErrConflict
			}
			seenOperations[normalized.Name] = struct{}{}
			operations = append(operations, normalized)
		}
		sort.Slice(operations, func(i, j int) bool { return operations[i].Name < operations[j].Name })
		provider.Operations = operations
		result.Providers = append(result.Providers, cloneProvider(provider))
	}
	if !allowLegacyUnboundSessionPolicy && !validIdentitySessionPolicy(result) {
		return IdentityDeclaration{}, ErrInvalid
	}
	riskHooks := make([]string, 0, len(input.RiskHooks))
	seenHooks := map[string]struct{}{}
	for _, hook := range input.RiskHooks {
		hook = strings.ToLower(strings.TrimSpace(hook))
		if !ownedID(artifact, hook) {
			return IdentityDeclaration{}, ErrInvalid
		}
		if _, duplicate := seenHooks[hook]; duplicate {
			return IdentityDeclaration{}, ErrInvalid
		}
		seenHooks[hook] = struct{}{}
		riskHooks = append(riskHooks, hook)
	}
	sort.Slice(result.UserFields, func(i, j int) bool { return result.UserFields[i].ID < result.UserFields[j].ID })
	sort.Slice(result.Providers, func(i, j int) bool {
		if result.Providers[i].Kind != result.Providers[j].Kind {
			return result.Providers[i].Kind < result.Providers[j].Kind
		}
		if result.Providers[i].Priority != result.Providers[j].Priority {
			return result.Providers[i].Priority > result.Providers[j].Priority
		}
		return result.Providers[i].ID < result.Providers[j].ID
	})
	sort.Strings(riskHooks)
	result.RiskHooks = riskHooks
	// Provider declarations are an inspect-only catalog until they carry an
	// executable operation contract. Legacy providers must not require a process.
	requiresRuntime := len(result.RiskHooks) > 0 ||
		result.SessionPolicy != "" && result.SessionPolicy != "core.session.default"
	if !requiresRuntime {
		for _, provider := range result.Providers {
			if len(provider.Operations) > 0 {
				requiresRuntime = true
				break
			}
		}
	}
	if !artifact.Core && requiresRuntime && artifact.RuntimeInstanceID == "" {
		return IdentityDeclaration{}, ErrInvalid
	}
	return result, nil
}

func validIdentitySessionPolicy(identity IdentityDeclaration) bool {
	if identity.SessionPolicy == "" || identity.SessionPolicy == "core.session.default" {
		return true
	}
	for _, provider := range identity.Providers {
		if provider.ID != identity.SessionPolicy || provider.Kind != ProviderKindSession {
			continue
		}
		for _, operation := range provider.Operations {
			if operation.Name == "session.evaluate" {
				return true
			}
		}
		return false
	}
	return false
}

func normalizeProviderOperation(kind string, input ProviderOperation) (ProviderOperation, error) {
	input.Name = strings.ToLower(strings.TrimSpace(input.Name))
	input.InputSchema = strings.TrimSpace(input.InputSchema)
	input.InputSchemaWireReference = strings.TrimSpace(input.InputSchemaWireReference)
	input.InputSchemaDigest = strings.ToLower(strings.TrimSpace(input.InputSchemaDigest))
	input.OutputSchema = strings.TrimSpace(input.OutputSchema)
	input.OutputSchemaWireReference = strings.TrimSpace(input.OutputSchemaWireReference)
	input.OutputSchemaDigest = strings.ToLower(strings.TrimSpace(input.OutputSchemaDigest))
	input.FailurePolicy = strings.ToLower(strings.TrimSpace(input.FailurePolicy))
	expectedPolicy, known := providerOperationPolicy(kind, input.Name)
	if !known || !validSchemaReference(input.InputSchema) || !validSchemaReference(input.OutputSchema) ||
		input.TimeoutMS <= 0 || input.TimeoutMS > extensionmanifest.ManifestIdentityProviderMaximumTimeoutMS ||
		input.FailurePolicy != expectedPolicy {
		return ProviderOperation{}, ErrInvalid
	}
	return cloneProviderOperation(input), nil
}

func providerOperationPolicy(kind, name string) (string, bool) {
	switch kind + ":" + name {
	case ProviderKindProfile + ":sections.list", ProviderKindProfile + ":section.read":
		return ProviderFailureOmit, true
	case ProviderKindAuth + ":registration.start", ProviderKindAuth + ":registration.complete",
		ProviderKindAuth + ":login.start", ProviderKindAuth + ":login.complete",
		ProviderKindAuth + ":link.start", ProviderKindAuth + ":link.complete",
		// provider.probe：Host 管理端有界探测；fail_closed，无账户/会话效应。
		ProviderKindAuth + ":provider.probe",
		ProviderKindProfile + ":section.update", ProviderKindProfile + ":account.read",
		ProviderKindProfile + ":account.update",
		ProviderKindRecovery + ":recovery.start", ProviderKindRecovery + ":recovery.complete",
		ProviderKindSession + ":session.evaluate", ProviderKindRisk + ":risk.evaluate":
		return ProviderFailureFailClosed, true
	default:
		return "", false
	}
}

func normalizeTombstone(input Tombstone) (Tombstone, error) {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.OwnerExtensionID = strings.ToLower(strings.TrimSpace(input.OwnerExtensionID))
	if !validTombstoneKind(input.Kind) || !idPattern.MatchString(input.ID) ||
		!contractPattern.MatchString(input.ContractVersion) || !idPattern.MatchString(input.OwnerExtensionID) ||
		!strings.HasPrefix(input.ID, input.OwnerExtensionID+".") {
		return Tombstone{}, ErrInvalid
	}
	return input, nil
}

func ownedID(artifact Artifact, id string) bool {
	if !idPattern.MatchString(id) {
		return false
	}
	return strings.HasPrefix(id, artifact.ExtensionID+".")
}

func declaredPermission(permission string, permissions map[string]struct{}) bool {
	if permission == "" {
		return true
	}
	_, ok := permissions[permission]
	return ok
}

func validFieldType(value string) bool {
	return slices.Contains([]string{"string", "number", "boolean", "object", "array"}, value)
}

func validSchemaReference(value string) bool {
	if value == "" || len(value) > maxSchemaReferenceLength {
		return false
	}
	if contractPattern.MatchString(value) {
		return true
	}
	clean, ok := extensionmanifest.SafeArchivePath(value)
	return ok && clean == value && strings.HasSuffix(value, ".json")
}

func validProviderKind(value string) bool {
	return slices.Contains([]string{ProviderKindAuth, ProviderKindProfile, ProviderKindRecovery, ProviderKindSession, ProviderKindRisk}, value)
}

func validHandler(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, "://") && !strings.Contains(value, "..")
}

func validTombstoneKind(value string) bool {
	return slices.Contains([]string{TombstoneKindPermission, TombstoneKindUserField, TombstoneKindProvider}, value)
}

func publicationDigest(snapshot Snapshot) string {
	stable := snapshot
	stable.Revision = 0
	stable.Digest = ""
	raw, _ := json.Marshal(stable)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func tombstoneKey(value Tombstone) string {
	return value.Kind + "\x00" + value.ID + "\x00" + value.ContractVersion
}

func ownershipKey(kind, id string) string {
	return fmt.Sprintf("%s\x00%s", kind, id)
}
