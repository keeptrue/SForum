package queryregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// Bounds keep publication graphs and plan inputs finite. Individually valid
// declarations can otherwise explode startup work or plan cost.
const (
	maxPublications                = 512
	maxQueriesTotal                = 4096
	maxQueriesPerPublication       = 512
	maxResultFiltersPerPublication = 64
	maxResultFiltersTotal          = 1024
	maxFieldsPerQuery              = 128
	maxRelationsPerQuery           = 32
	maxFiltersPerQuery             = 64
	maxSortsPerQuery               = 16
	maxIdentityFieldsPerQuery      = 8
	maxCacheTagsPerQuery           = 32
	maxIDLength                    = 121
	maxSchemaRefLength             = 256
	maxOpaqueNameLength            = 256
	maxFilterValueLength           = 512
	maxLocaleLength                = 32
	maxScopeLength                 = 128
	maxFingerprintLength           = 128
	maxExtensionVersionLength      = 128
	maxRuntimeInstanceIDLength     = 512
	maxHandlerLength               = 256
	maxResultFilterPriority        = 1_000_000
	// Cost values cross JSON/protocol boundaries. This representational bound is
	// not the still-open product cost model; it prevents architecture-dependent
	// int ranges and pathological policy output from entering a durable plan.
	maxCostValue     = 1<<31 - 1
	maxPlanFields    = maxFieldsPerQuery
	maxPlanRelations = maxRelationsPerQuery
	maxPlanFilters   = maxFiltersPerQuery
	maxPlanSorts     = maxSortsPerQuery

	defaultPageLimit = 20
	maximumPageLimit = 100
	maximumOffset    = 1_000_000
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,120}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contractPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	schemaPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	localePattern   = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8}){0,3}$`)
)

func normalizePublications(input []Publication) ([]Publication, error) {
	if len(input) > maxPublications {
		return nil, ErrInvalid
	}
	result := make([]Publication, 0, len(input))
	seen := map[string]bool{}
	queryCount := 0
	filterCount := 0
	for _, publication := range input {
		normalized, err := normalizePublication(publication)
		if err != nil {
			return nil, err
		}
		if seen[normalized.Artifact.ExtensionID] {
			return nil, fmt.Errorf("%w: duplicate publication %s", ErrConflict, normalized.Artifact.ExtensionID)
		}
		seen[normalized.Artifact.ExtensionID] = true
		queryCount += len(normalized.Queries)
		filterCount += len(normalized.ResultFilters)
		if queryCount > maxQueriesTotal || filterCount > maxResultFiltersTotal {
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
	if len(input.Queries) > maxQueriesPerPublication ||
		len(input.ResultFilters) > maxResultFiltersPerPublication {
		return Publication{}, ErrInvalid
	}
	result := Publication{Artifact: artifact}
	seen := map[string]bool{}
	for _, raw := range input.Queries {
		declaration, queryErr := normalizeQueryDeclaration(artifact, raw)
		if queryErr != nil {
			return Publication{}, queryErr
		}
		if seen["query\x00"+declaration.ID] {
			return Publication{}, fmt.Errorf("%w: duplicate query %s in publication", ErrConflict, declaration.ID)
		}
		seen["query\x00"+declaration.ID] = true
		result.Queries = append(result.Queries, declaration)
	}
	sort.Slice(result.Queries, func(i, j int) bool {
		return result.Queries[i].ID < result.Queries[j].ID
	})
	for _, raw := range input.ResultFilters {
		declaration, filterErr := normalizeResultFilterDeclaration(artifact, raw, result.Queries)
		if filterErr != nil {
			return Publication{}, filterErr
		}
		if seen["filter\x00"+declaration.ID] {
			return Publication{}, fmt.Errorf("%w: duplicate result filter %s in publication", ErrConflict, declaration.ID)
		}
		seen["filter\x00"+declaration.ID] = true
		result.ResultFilters = append(result.ResultFilters, declaration)
	}
	sort.Slice(result.ResultFilters, func(i, j int) bool {
		left, right := result.ResultFilters[i], result.ResultFilters[j]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		return left.ID < right.ID
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
		len(input.ExtensionVersion) > maxExtensionVersionLength ||
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
	} else if input.coreSeal != [32]byte{} || input.VersionID <= 0 ||
		!validBoundedOpaque(input.RuntimeInstanceID, maxRuntimeInstanceIDLength) {
		return Artifact{}, ErrInvalid
	}
	return input, nil
}

// NewCoreArtifact is the explicit Host-only authority boundary for core query
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

func normalizeQueryDeclaration(artifact Artifact, input QueryDeclaration) (QueryDeclaration, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.Entity = strings.ToLower(strings.TrimSpace(input.Entity))
	input.PlanVersion = strings.TrimSpace(input.PlanVersion)
	input.Pagination = strings.ToLower(strings.TrimSpace(input.Pagination))
	input.ResultSchema = strings.TrimSpace(input.ResultSchema)
	input.PermissionPolicy = strings.ToLower(strings.TrimSpace(input.PermissionPolicy))
	input.Handler = strings.TrimSpace(input.Handler)
	input.ProviderDigest = normalizeDigest(input.ProviderDigest)
	input.ResultSchemaDigest = normalizeDigest(input.ResultSchemaDigest)

	fields, err := normalizeNameList(input.Fields, maxFieldsPerQuery, true)
	if err != nil {
		return QueryDeclaration{}, err
	}
	relations, err := normalizeNameList(input.Relations, maxRelationsPerQuery, false)
	if err != nil {
		return QueryDeclaration{}, err
	}
	filters, err := normalizeNameList(input.Filters, maxFiltersPerQuery, false)
	if err != nil {
		return QueryDeclaration{}, err
	}
	sorts, err := normalizeNameList(input.Sort, maxSortsPerQuery, false)
	if err != nil {
		return QueryDeclaration{}, err
	}
	cacheTags, err := normalizeOwnedCacheTags(artifact.ExtensionID, input.CacheTags)
	if err != nil {
		return QueryDeclaration{}, err
	}
	identityFields, err := normalizeNameList(input.IdentityFields, maxIdentityFieldsPerQuery, false)
	if err != nil {
		return QueryDeclaration{}, err
	}
	defaultSort, err := normalizeDefaultSort(input.DefaultSort, maxSortsPerQuery)
	if err != nil {
		return QueryDeclaration{}, err
	}
	if !validContributionIdentity(artifact, input.ID, input.ContractVersion) ||
		!idPattern.MatchString(input.Entity) || !contractPattern.MatchString(input.PlanVersion) ||
		len(input.PlanVersion) > maxSchemaRefLength ||
		!validSchemaRef(input.ResultSchema) || len(fields) == 0 ||
		!validPagination(input.Pagination) || !validPermissionPolicy(input.PermissionPolicy) {
		return QueryDeclaration{}, ErrInvalid
	}
	input.Fields = fields
	input.Relations = relations
	input.Filters = filters
	input.Sort = sorts
	input.CacheTags = cacheTags
	input.IdentityFields = identityFields
	input.DefaultSort = defaultSort
	if err := validateExecutableQueryMetadata(artifact, input); err != nil {
		return QueryDeclaration{}, err
	}
	if _, _, err := publicationExecutableProvider(artifact, input); err != nil {
		return QueryDeclaration{}, err
	}
	return input, nil
}

func normalizeResultFilterDeclaration(
	artifact Artifact,
	input ResultFilterDeclaration,
	queries []QueryDeclaration,
) (ResultFilterDeclaration, error) {
	input.ID = strings.ToLower(strings.TrimSpace(input.ID))
	input.ContractVersion = strings.TrimSpace(input.ContractVersion)
	input.QueryID = strings.ToLower(strings.TrimSpace(input.QueryID))
	input.QueryContractVersion = strings.TrimSpace(input.QueryContractVersion)
	input.QueryPlanVersion = strings.TrimSpace(input.QueryPlanVersion)
	input.Handler = strings.TrimSpace(input.Handler)
	input.FailurePolicy = strings.ToLower(strings.TrimSpace(input.FailurePolicy))
	input.FilterDigest = normalizeDigest(input.FilterDigest)
	if input.FailurePolicy == "" {
		input.FailurePolicy = ResultFilterFailClosed
	}
	if input.TimeoutMS == 0 {
		input.TimeoutMS = int(defaultFilterTimeout / time.Millisecond)
	}
	if input.Dependency != nil {
		dependency := *input.Dependency
		dependency.ExtensionID = strings.ToLower(strings.TrimSpace(dependency.ExtensionID))
		dependency.VersionConstraint = strings.TrimSpace(dependency.VersionConstraint)
		input.Dependency = &dependency
	}
	// IdentityFields are always Host-derived from the target query in the
	// complete graph. Never retain a caller-supplied decorative identity.
	input.IdentityFields = nil
	if !validContributionIdentity(artifact, input.ID, input.ContractVersion) ||
		!idPattern.MatchString(input.QueryID) || !contractPattern.MatchString(input.QueryContractVersion) ||
		!contractPattern.MatchString(input.QueryPlanVersion) ||
		!validExecutableHandler(artifact.ExtensionID, input.Handler) ||
		(input.FailurePolicy != ResultFilterFailClosed && input.FailurePolicy != ResultFilterFailOpen) ||
		input.TimeoutMS < 1 || input.TimeoutMS > int(maximumFilterTimeout/time.Millisecond) ||
		input.Priority < -maxResultFilterPriority || input.Priority > maxResultFilterPriority {
		return ResultFilterDeclaration{}, ErrInvalid
	}
	if input.Dependency != nil {
		if !idPattern.MatchString(input.Dependency.ExtensionID) ||
			input.Dependency.ExtensionID == artifact.ExtensionID ||
			input.Dependency.VersionConstraint == "" {
			return ResultFilterDeclaration{}, ErrInvalid
		}
		if _, err := semver.NewConstraint(input.Dependency.VersionConstraint); err != nil {
			return ResultFilterDeclaration{}, ErrInvalid
		}
	}
	// 同 publication 内的目标 query：Host 复制 identity，并拒绝 self-dependency。
	for _, query := range queries {
		if query.ID != input.QueryID {
			continue
		}
		if input.Dependency != nil || query.Handler == "" ||
			query.ContractVersion != input.QueryContractVersion ||
			query.PlanVersion != input.QueryPlanVersion ||
			len(query.IdentityFields) == 0 {
			return ResultFilterDeclaration{}, ErrInvalid
		}
		input.IdentityFields = slices.Clone(query.IdentityFields)
		break
	}
	if _, _, err := publicationExecutableFilter(artifact, input); err != nil {
		return ResultFilterDeclaration{}, err
	}
	return input, nil
}

func normalizeDefaultSort(input []SortValue, limit int) ([]SortValue, error) {
	if len(input) > limit {
		return nil, ErrInvalid
	}
	result := make([]SortValue, 0, len(input))
	seen := map[string]bool{}
	for _, item := range input {
		field := strings.TrimSpace(item.Field)
		if !validOpaqueName(field) || seen[field] {
			return nil, ErrInvalid
		}
		seen[field] = true
		result = append(result, SortValue{Field: field, Descending: item.Descending})
	}
	return result, nil
}

func validateExecutableQueryMetadata(artifact Artifact, input QueryDeclaration) error {
	if input.Handler == "" {
		if len(input.IdentityFields) != 0 || len(input.DefaultSort) != 0 ||
			input.ProviderDigest != "" || input.boundProvider != nil {
			return ErrInvalid
		}
		return nil
	}
	if !validExecutableHandler(artifact.ExtensionID, input.Handler) ||
		len(input.IdentityFields) == 0 || len(input.DefaultSort) == 0 {
		return ErrInvalid
	}
	fields := map[string]struct{}{}
	for _, field := range input.Fields {
		fields[field] = struct{}{}
	}
	sorts := map[string]struct{}{}
	for _, field := range input.Sort {
		sorts[field] = struct{}{}
	}
	for _, identity := range input.IdentityFields {
		if _, ok := fields[identity]; !ok {
			return ErrInvalid
		}
		if _, ok := sorts[identity]; !ok {
			return ErrInvalid
		}
	}
	for _, item := range input.DefaultSort {
		if _, ok := sorts[item.Field]; !ok {
			return ErrInvalid
		}
	}
	if len(input.DefaultSort) < len(input.IdentityFields) {
		return ErrInvalid
	}
	offset := len(input.DefaultSort) - len(input.IdentityFields)
	for index, identity := range input.IdentityFields {
		if input.DefaultSort[offset+index].Field != identity {
			return ErrInvalid
		}
	}
	return nil
}

func normalizeNameList(input []string, limit int, required bool) ([]string, error) {
	if len(input) > limit {
		return nil, ErrInvalid
	}
	if required && len(input) == 0 {
		return nil, ErrInvalid
	}
	result := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, value := range input {
		value = strings.TrimSpace(value)
		if !validOpaqueName(value) || seen[value] {
			return nil, ErrInvalid
		}
		seen[value] = true
		result = append(result, value)
	}
	// Stable declaration order is declaration order after lowercasing; do not
	// re-sort names so plan selection can preserve request order later.
	return result, nil
}

func normalizeOwnedCacheTags(owner string, input []string) ([]string, error) {
	if len(input) > maxCacheTagsPerQuery {
		return nil, ErrInvalid
	}
	result := make([]string, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, raw := range input {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if !idPattern.MatchString(tag) || !strings.HasPrefix(tag, owner+".") {
			return nil, ErrInvalid
		}
		if _, duplicate := seen[tag]; duplicate {
			return nil, ErrInvalid
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result, nil
}

func validContributionIdentity(artifact Artifact, id, contract string) bool {
	return idPattern.MatchString(id) && strings.HasPrefix(id, artifact.ExtensionID+".") &&
		contractPattern.MatchString(contract) &&
		len(id) <= maxIDLength && len(contract) <= maxSchemaRefLength
}

func validPagination(value string) bool {
	switch value {
	case PaginationNone, PaginationOffset, PaginationCursor:
		return true
	default:
		return false
	}
}

func validPermissionPolicy(value string) bool {
	if value == PermissionPolicyPublic || value == PermissionPolicyLogin {
		return true
	}
	return idPattern.MatchString(value)
}

func validSchemaRef(value string) bool {
	if len(value) > maxSchemaRefLength {
		return false
	}
	if schemaPattern.MatchString(value) {
		return true
	}
	clean, ok := extensionmanifest.SafeArchivePath(value)
	return ok && clean == value && strings.HasSuffix(value, ".json")
}

func validOpaqueName(value string) bool {
	return validBoundedOpaque(value, maxOpaqueNameLength)
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
	if publications == nil {
		publications = []Publication{}
	}
	document := struct {
		SchemaVersion string        `json:"schemaVersion"`
		SafeMode      bool          `json:"safeMode"`
		Publications  []Publication `json:"publications"`
	}{SchemaVersion: SchemaVersion, SafeMode: safeMode, Publications: publications}
	body, _ := json.Marshal(document)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
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
// corrupt third-party query cannot block Host Safe Mode recovery.
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
