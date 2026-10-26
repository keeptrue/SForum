package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	publicstyles "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/publicstyles"
	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
)

const (
	PublicFrontendPolicySchemaV1         = "sforum.public-frontend-policy@1"
	PublicFrontendDocumentPolicySchemaV1 = "sforum.public-frontend-document-policy@1"
	PublicContentStylesSchemaV1          = "sforum.public-content-styles@1"
	PublicContentStyleScope              = "core.surface.forum-content"

	maxPublicPagePolicyComponents     = 256
	maxPublicPagePolicySources        = 256
	maxPublicPageDirectiveSources     = 128
	maxPublicPageExtensionPolicyBytes = 8 * 1024
	maxPublicPagePolicyHeaderBytes    = 8 * 1024
	publicPagePolicyHeaderPrefix      = "Content-Security-Policy: "
)

var ErrPublicPagePolicyUnavailable = errors.New("extensions: public page policy is unavailable")

type PublicPagePolicyErrorCode string

const (
	PublicPagePolicyInvalidInput         PublicPagePolicyErrorCode = "invalid_input"
	PublicPagePolicyRuntimeUnavailable   PublicPagePolicyErrorCode = "runtime_unavailable"
	PublicPagePolicyComponentUnavailable PublicPagePolicyErrorCode = "component_unavailable"
	PublicPagePolicyTrustUnavailable     PublicPagePolicyErrorCode = "trust_unavailable"
	PublicPagePolicyDependencyInvalid    PublicPagePolicyErrorCode = "dependency_invalid"
	PublicPagePolicyDirectiveInvalid     PublicPagePolicyErrorCode = "directive_invalid"
	PublicPagePolicyBoundsExceeded       PublicPagePolicyErrorCode = "bounds_exceeded"
	PublicPagePolicySnapshotChanged      PublicPagePolicyErrorCode = "snapshot_changed"
)

// PublicPagePolicyError lets the page owner fail all L2 closed without parsing
// internal error text. Controllers must not disclose Cause on a public route.
type PublicPagePolicyError struct {
	Code        PublicPagePolicyErrorCode
	ExtensionID string
	ComponentID string
	Cause       error
}

func (e *PublicPagePolicyError) Error() string {
	return ErrPublicPagePolicyUnavailable.Error()
}

func (e *PublicPagePolicyError) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrPublicPagePolicyUnavailable}
	}
	return []error{ErrPublicPagePolicyUnavailable, e.Cause}
}

// PublicFrontendComponentTuple is emitted by Host SSR composition. It contains
// no asset scope or CSP text: policy roots can only come from an exact rendered
// component and its Host-published entry handle.
type PublicFrontendComponentTuple struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	ComponentID      string `json:"componentId"`
	ContractVersion  string `json:"contractVersion"`
}

type PublicFrontendPolicyComponent struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	ImpactDigest     string `json:"impactDigest"`
	ComponentID      string `json:"componentId"`
	ContractVersion  string `json:"contractVersion"`
}

type PublicFrontendPolicyDirective struct {
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
}

// PublicFrontendPolicyContributor is Host-only audit evidence for every exact
// owner in the page asset closure, including asset-only dependency owners.
type PublicFrontendPolicyContributor struct {
	ExtensionID      string                          `json:"extensionId"`
	ExtensionVersion string                          `json:"extensionVersion"`
	PackageDigest    string                          `json:"packageDigest"`
	ImpactDigest     string                          `json:"impactDigest"`
	OwnerKind        string                          `json:"ownerKind"`
	AssetHandles     []string                        `json:"assetHandles"`
	Directives       []PublicFrontendPolicyDirective `json:"directives"`
}

// PublicFrontendDocumentPolicy is the exact Host-owned CSP result. HeaderValue
// excludes the header name, while the 8 KiB bound covers the complete header.
type PublicFrontendDocumentPolicy struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Digest        string                          `json:"digest"`
	Directives    []PublicFrontendPolicyDirective `json:"directives"`
	HeaderValue   string                          `json:"headerValue"`
}

type PublicFrontendPolicy struct {
	SchemaVersion string `json:"schemaVersion"`
	GraphDigest   string `json:"graphDigest"`
	// ExtensionPolicyDigest binds only the exact extension graph and provenance.
	ExtensionPolicyDigest string                          `json:"extensionPolicyDigest"`
	Directives            []PublicFrontendPolicyDirective `json:"directives"`
	AdmittedComponents    []PublicFrontendPolicyComponent `json:"admittedComponents"`
	DocumentPolicy        PublicFrontendDocumentPolicy    `json:"documentPolicy"`
	// Styles carries the exact digest-bound stylesheets owned by the page's
	// admitted L2 assets. SSR preloads them into <head> so the first paint is
	// already styled; the client lease later adopts the same elements.
	Styles       []PublicFrontendAssetReference `json:"styles"`
	contributors []PublicFrontendPolicyContributor
}

// Contributors returns detached Host-only provenance. It is intentionally not
// serialized into the public page policy payload.
func (p PublicFrontendPolicy) Contributors() []PublicFrontendPolicyContributor {
	return clonePublicFrontendPolicyContributors(p.contributors)
}

// PublicPagePolicy resolves all exact L2 components rendered by one page from a
// single immutable Asset Registry snapshot, then merges their additions into
// the Host-owned restrictive document policy.
func (s *FrontendService) PublicPagePolicy(
	ctx context.Context,
	rendered []PublicFrontendComponentTuple,
) (PublicFrontendPolicy, error) {
	if err := s.publicPagePolicyGates(ctx); err != nil {
		return PublicFrontendPolicy{}, err
	}
	tuples, err := normalizePublicPageComponentTuples(rendered)
	if err != nil {
		return PublicFrontendPolicy{}, err
	}

	// Snapshot is a detached view captured from one atomic registry state. Every
	// root, dependency, owner, and graph digest below comes from this one value.
	snapshot := s.publicAssets.Snapshot()
	publications, assets, err := indexPublicPagePolicySnapshot(snapshot)
	if err != nil {
		return PublicFrontendPolicy{}, err
	}

	validated := make(map[string]assetregistry.Artifact)
	extensionsByID := make(map[string]Extension)
	roots := make([]string, 0, len(tuples))
	admitted := make([]PublicFrontendPolicyComponent, 0, len(tuples))
	for _, tuple := range tuples {
		publication, found := publications[tuple.ExtensionID]
		if !found || !publicPageTupleMatchesArtifact(tuple, publication.Artifact) {
			return PublicFrontendPolicy{}, publicPagePolicyError(PublicPagePolicyComponentUnavailable, tuple, ErrPublicFrontendUnavailable)
		}
		if err := s.validatePublicPagePolicyOwner(
			ctx, publication.Artifact, publications, extensionsByID, validated,
		); err != nil {
			return PublicFrontendPolicy{}, publicPagePolicyError(PublicPagePolicyTrustUnavailable, tuple, err)
		}
		extension := extensionsByID[tuple.ExtensionID]
		component, found := publicManifestComponent(extension.Manifest, tuple.ComponentID)
		if !found || component.ContractVersion != tuple.ContractVersion ||
			s.publicComponents == nil || !s.publicComponents.AdmitPublicComponent(extension, component) {
			return PublicFrontendPolicy{}, publicPagePolicyError(PublicPagePolicyComponentUnavailable, tuple, ErrPublicFrontendUnavailable)
		}
		entryHandle := publicL2EntryHandle(component)
		entry, found := assets[entryHandle]
		if !found || entry.Artifact != publication.Artifact || entry.Type != "script" || !entry.Module {
			return PublicFrontendPolicy{}, publicPagePolicyError(PublicPagePolicyComponentUnavailable, tuple, ErrPublicFrontendUnavailable)
		}
		roots = append(roots, entryHandle)
		admitted = append(admitted, PublicFrontendPolicyComponent{
			ExtensionID: tuple.ExtensionID, ExtensionVersion: tuple.ExtensionVersion,
			PackageDigest: tuple.PackageDigest, ImpactDigest: publication.Artifact.ImpactDigest,
			ComponentID: tuple.ComponentID, ContractVersion: tuple.ContractVersion,
		})
	}

	plan, err := planPublicPagePolicyAssets(assets, roots)
	if err != nil {
		return PublicFrontendPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid, Cause: err}
	}
	for _, asset := range plan {
		if err := s.validatePublicPagePolicyOwner(
			ctx, asset.Artifact, publications, extensionsByID, validated,
		); err != nil {
			return PublicFrontendPolicy{}, &PublicPagePolicyError{
				Code: PublicPagePolicyTrustUnavailable, ExtensionID: asset.Artifact.ExtensionID, Cause: err,
			}
		}
	}
	directives, contributors, err := aggregatePublicPagePolicyDirectives(plan)
	if err != nil {
		return PublicFrontendPolicy{}, err
	}

	// A concurrent publication cannot turn one page response into a mixture of
	// two snapshots. Compare the stable graph identity, never local revision.
	if current := s.publicAssets.Snapshot(); current.Digest != snapshot.Digest {
		return PublicFrontendPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicySnapshotChanged}
	}
	policy := PublicFrontendPolicy{
		SchemaVersion: PublicFrontendPolicySchemaV1, GraphDigest: snapshot.Digest,
		Directives: directives, AdmittedComponents: admitted, contributors: contributors,
	}
	for _, asset := range plan {
		if asset.Type == "style" {
			policy.Styles = append(policy.Styles, publicAssetReference(asset))
		}
	}
	policy.ExtensionPolicyDigest = publicPageExtensionPolicyDigest(policy)
	policy.DocumentPolicy, err = publicPageDocumentPolicy(policy)
	if err != nil {
		return PublicFrontendPolicy{}, err
	}
	return policy, nil
}

func (s *FrontendService) publicPagePolicyGates(ctx context.Context) error {
	if s == nil || ctx == nil || s.safeMode || !s.v3TrustChallenges ||
		s.extensions == nil || s.executableTrust == nil || s.publicAssets == nil || s.publicComponents == nil {
		return &PublicPagePolicyError{Code: PublicPagePolicyRuntimeUnavailable, Cause: ErrPublicFrontendUnavailable}
	}
	return nil
}

func normalizePublicPageComponentTuples(
	input []PublicFrontendComponentTuple,
) ([]PublicFrontendComponentTuple, error) {
	if len(input) > maxPublicPagePolicyComponents {
		return nil, &PublicPagePolicyError{Code: PublicPagePolicyBoundsExceeded}
	}
	result := make([]PublicFrontendComponentTuple, 0, len(input))
	seen := make(map[string]PublicFrontendComponentTuple, len(input))
	for _, tuple := range input {
		canonical := tuple
		canonical.ExtensionID = normalizeID(tuple.ExtensionID)
		canonical.ComponentID = normalizeID(tuple.ComponentID)
		canonical.ExtensionVersion = strings.TrimSpace(tuple.ExtensionVersion)
		canonical.PackageDigest = normalizedPublicDigest(tuple.PackageDigest)
		canonical.ContractVersion = strings.TrimSpace(tuple.ContractVersion)
		if canonical.ExtensionID == "" || canonical.ComponentID == "" || canonical.ExtensionVersion == "" ||
			canonical.PackageDigest == "" || canonical.ContractVersion == "" || tuple != canonical {
			return nil, publicPagePolicyError(PublicPagePolicyInvalidInput, tuple, nil)
		}
		key := canonical.ExtensionID + "\x00" + canonical.ComponentID
		if previous, duplicate := seen[key]; duplicate {
			if previous != canonical {
				return nil, publicPagePolicyError(PublicPagePolicyInvalidInput, tuple, nil)
			}
			continue
		}
		seen[key] = canonical
		result = append(result, canonical)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.ExtensionID != right.ExtensionID {
			return left.ExtensionID < right.ExtensionID
		}
		if left.ComponentID != right.ComponentID {
			return left.ComponentID < right.ComponentID
		}
		if left.ExtensionVersion != right.ExtensionVersion {
			return left.ExtensionVersion < right.ExtensionVersion
		}
		if left.PackageDigest != right.PackageDigest {
			return left.PackageDigest < right.PackageDigest
		}
		return left.ContractVersion < right.ContractVersion
	})
	return result, nil
}

func indexPublicPagePolicySnapshot(
	snapshot assetregistry.Snapshot,
) (map[string]assetregistry.Publication, map[string]assetregistry.Asset, error) {
	if normalizedPublicDigest(snapshot.Digest) == "" {
		return nil, nil, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid}
	}
	publications := make(map[string]assetregistry.Publication, len(snapshot.Publications))
	for _, publication := range snapshot.Publications {
		if _, duplicate := publications[publication.Artifact.ExtensionID]; duplicate {
			return nil, nil, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid}
		}
		publications[publication.Artifact.ExtensionID] = publication
	}
	assets := make(map[string]assetregistry.Asset, len(snapshot.Assets))
	for _, asset := range snapshot.Assets {
		publication, found := publications[asset.Artifact.ExtensionID]
		if !found || publication.Artifact != asset.Artifact {
			return nil, nil, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid}
		}
		if _, duplicate := assets[asset.Handle]; duplicate {
			return nil, nil, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid}
		}
		assets[asset.Handle] = asset
	}
	return publications, assets, nil
}

func publicPageTupleMatchesArtifact(tuple PublicFrontendComponentTuple, artifact assetregistry.Artifact) bool {
	return tuple.ExtensionID == artifact.ExtensionID &&
		tuple.ExtensionVersion == artifact.ExtensionVersion &&
		tuple.PackageDigest == artifact.PackageDigest
}

func (s *FrontendService) validatePublicPagePolicyOwner(
	ctx context.Context,
	artifact assetregistry.Artifact,
	publications map[string]assetregistry.Publication,
	extensionsByID map[string]Extension,
	validated map[string]assetregistry.Artifact,
) error {
	if previous, found := validated[artifact.ExtensionID]; found {
		if previous != artifact {
			return ErrPublicFrontendUnavailable
		}
		return nil
	}
	publication, found := publications[artifact.ExtensionID]
	if !found || publication.Artifact != artifact {
		return ErrPublicFrontendUnavailable
	}
	if artifact.OwnerKind == assetregistry.OwnerKindCore {
		if err := s.executableTrust.ValidatePublishedIdentity(ctx, Extension{}, artifact); err != nil {
			return s.failClosedPublicIdentity(artifact, err)
		}
		validated[artifact.ExtensionID] = artifact
		return nil
	}
	extension, found := extensionsByID[artifact.ExtensionID]
	if !found {
		var err error
		extension, err = s.extensions.Get(ctx, artifact.ExtensionID)
		if err != nil {
			return s.failClosedPublicIdentity(artifact, err)
		}
		extensionsByID[artifact.ExtensionID] = extension
	}
	if err := s.executableTrust.ValidatePublishedIdentity(ctx, extension, artifact); err != nil {
		return s.failClosedPublicIdentity(artifact, err)
	}
	validated[artifact.ExtensionID] = artifact
	return nil
}

func planPublicPagePolicyAssets(
	assets map[string]assetregistry.Asset,
	roots []string,
) ([]assetregistry.Asset, error) {
	sort.Strings(roots)
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	result := make([]assetregistry.Asset, 0, len(roots))
	var visit func(string) error
	visit = func(handle string) error {
		if visited[handle] {
			return nil
		}
		if visiting[handle] {
			return assetregistry.ErrDependency
		}
		asset, found := assets[handle]
		if !found {
			if strings.HasPrefix(handle, "core.asset.") {
				return nil
			}
			return assetregistry.ErrDependency
		}
		visiting[handle] = true
		for _, dependency := range asset.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, handle)
		visited[handle] = true
		result = append(result, asset)
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func aggregatePublicPagePolicyDirectives(
	plan []assetregistry.Asset,
) ([]PublicFrontendPolicyDirective, []PublicFrontendPolicyContributor, error) {
	sets := make(map[string]map[string]struct{})
	contributors := make(map[string]*publicPagePolicyContributorAccumulator)
	for _, asset := range plan {
		contributor := contributors[asset.Artifact.ExtensionID]
		if contributor == nil {
			contributor = &publicPagePolicyContributorAccumulator{
				artifact: asset.Artifact, handles: make(map[string]struct{}),
				directives: make(map[string]map[string]struct{}),
			}
			contributors[asset.Artifact.ExtensionID] = contributor
		} else if contributor.artifact != asset.Artifact {
			return nil, nil, &PublicPagePolicyError{Code: PublicPagePolicyDependencyInvalid}
		}
		contributor.handles[asset.Handle] = struct{}{}
		for _, declaration := range asset.CSP {
			fields := strings.Fields(declaration)
			if len(fields) < 2 || strings.Join(fields, " ") != declaration || !publicPagePolicyDirectiveName(fields[0]) {
				return nil, nil, &PublicPagePolicyError{
					Code: PublicPagePolicyDirectiveInvalid, ExtensionID: asset.Artifact.ExtensionID,
				}
			}
			for _, raw := range fields[1:] {
				source, ok := strictPublicPagePolicySource(fields[0], raw)
				if !ok {
					return nil, nil, &PublicPagePolicyError{
						Code: PublicPagePolicyDirectiveInvalid, ExtensionID: asset.Artifact.ExtensionID,
					}
				}
				addPublicPagePolicySource(sets, fields[0], source)
				addPublicPagePolicySource(contributor.directives, fields[0], source)
			}
		}
	}

	directives, err := publicPagePolicyDirectivesFromSets(sets, true)
	if err != nil {
		return nil, nil, err
	}
	ownerIDs := make([]string, 0, len(contributors))
	for ownerID := range contributors {
		ownerIDs = append(ownerIDs, ownerID)
	}
	sort.Strings(ownerIDs)
	evidence := make([]PublicFrontendPolicyContributor, 0, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		contributor := contributors[ownerID]
		handles := make([]string, 0, len(contributor.handles))
		for handle := range contributor.handles {
			handles = append(handles, handle)
		}
		sort.Strings(handles)
		ownerDirectives, err := publicPagePolicyDirectivesFromSets(contributor.directives, false)
		if err != nil {
			return nil, nil, err
		}
		evidence = append(evidence, PublicFrontendPolicyContributor{
			ExtensionID: contributor.artifact.ExtensionID, ExtensionVersion: contributor.artifact.ExtensionVersion,
			PackageDigest: contributor.artifact.PackageDigest, ImpactDigest: contributor.artifact.ImpactDigest,
			OwnerKind: contributor.artifact.OwnerKind, AssetHandles: handles, Directives: ownerDirectives,
		})
	}
	return directives, evidence, nil
}

type publicPagePolicyContributorAccumulator struct {
	artifact   assetregistry.Artifact
	handles    map[string]struct{}
	directives map[string]map[string]struct{}
}

func addPublicPagePolicySource(sets map[string]map[string]struct{}, directive, source string) {
	set := sets[directive]
	if set == nil {
		set = make(map[string]struct{})
		sets[directive] = set
	}
	set[source] = struct{}{}
}

func publicPagePolicyDirectivesFromSets(
	sets map[string]map[string]struct{},
	enforceExtensionBounds bool,
) ([]PublicFrontendPolicyDirective, error) {
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	sort.Strings(names)
	directives := make([]PublicFrontendPolicyDirective, 0, len(names))
	totalSources := 0
	for _, name := range names {
		sources := make([]string, 0, len(sets[name]))
		for source := range sets[name] {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		if len(sources) > 1 && sources[0] == "'none'" {
			return nil, &PublicPagePolicyError{Code: PublicPagePolicyDirectiveInvalid}
		}
		if enforceExtensionBounds && len(sources) > maxPublicPageDirectiveSources {
			return nil, &PublicPagePolicyError{Code: PublicPagePolicyBoundsExceeded}
		}
		totalSources += len(sources)
		directives = append(directives, PublicFrontendPolicyDirective{Name: name, Sources: sources})
	}
	// This is only the extension-policy fragment. publicPageDocumentPolicy applies
	// the authoritative bound again after adding the complete Host baseline.
	if enforceExtensionBounds && (totalSources > maxPublicPagePolicySources ||
		publicPageExtensionPolicySerializedBytes(directives) > maxPublicPageExtensionPolicyBytes) {
		return nil, &PublicPagePolicyError{Code: PublicPagePolicyBoundsExceeded}
	}
	return directives, nil
}

func publicPageDocumentPolicy(policy PublicFrontendPolicy) (PublicFrontendDocumentPolicy, error) {
	sets := make(map[string]map[string]struct{})
	for _, directive := range publicPageHostBaselinePolicy() {
		for _, source := range directive.Sources {
			addPublicPagePolicySource(sets, directive.Name, source)
		}
	}
	for _, directive := range policy.Directives {
		if !publicPagePolicyDirectiveName(directive.Name) || len(directive.Sources) == 0 {
			return PublicFrontendDocumentPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyDirectiveInvalid}
		}
		for _, source := range directive.Sources {
			canonical, ok := strictPublicPagePolicySource(directive.Name, source)
			if !ok || canonical != source {
				return PublicFrontendDocumentPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyDirectiveInvalid}
			}
			addPublicPagePolicySource(sets, directive.Name, source)
		}
	}
	directives, err := publicPagePolicyDirectivesFromSets(sets, false)
	if err != nil {
		return PublicFrontendDocumentPolicy{}, err
	}
	for _, directive := range directives {
		if publicPagePolicyExecutableDirective(directive.Name) &&
			(len(directive.Sources) != 1 || directive.Sources[0] != "'self'") {
			return PublicFrontendDocumentPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyDirectiveInvalid}
		}
	}
	headerValue := publicPagePolicyHeaderValue(directives)
	if len(publicPagePolicyHeaderPrefix)+len(headerValue) > maxPublicPagePolicyHeaderBytes {
		return PublicFrontendDocumentPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyBoundsExceeded}
	}
	document := PublicFrontendDocumentPolicy{
		SchemaVersion: PublicFrontendDocumentPolicySchemaV1,
		Directives:    directives,
		HeaderValue:   headerValue,
	}
	document.Digest = publicPageDocumentPolicyDigest(policy, document)
	return document, nil
}

func publicPageHostBaselinePolicy() []PublicFrontendPolicyDirective {
	// Host 基线不可被扩展移除；可执行脚本、样式和 Worker 始终只允许同源制品。
	return []PublicFrontendPolicyDirective{
		{Name: "base-uri", Sources: []string{"'none'"}},
		{Name: "connect-src", Sources: []string{"'self'"}},
		{Name: "default-src", Sources: []string{"'none'"}},
		{Name: "font-src", Sources: []string{"'self'"}},
		{Name: "form-action", Sources: []string{"'self'"}},
		{Name: "frame-ancestors", Sources: []string{"'none'"}},
		{Name: "img-src", Sources: []string{"'self'"}},
		{Name: "media-src", Sources: []string{"'self'"}},
		{Name: "object-src", Sources: []string{"'none'"}},
		{Name: "script-src", Sources: []string{"'self'"}},
		{Name: "style-src", Sources: []string{"'self'"}},
		{Name: "worker-src", Sources: []string{"'self'"}},
	}
}

func publicPagePolicyDirectiveName(value string) bool {
	switch value {
	case "connect-src", "font-src", "img-src", "media-src", "script-src", "style-src", "worker-src":
		return true
	default:
		return false
	}
}

func strictPublicPagePolicySource(directive, source string) (string, bool) {
	if source == "'self'" || source == "'none'" {
		return source, true
	}
	if publicPagePolicyExecutableDirective(directive) {
		// Remote executable code and CSS are not bound to the trusted package digest.
		// They require a separate authority that the current platform does not expose.
		return "", false
	}
	if source == "" || strings.ContainsAny(source, "*;,\"'<>\\?#%\r\n\x00") {
		return "", false
	}
	for index := 0; index < len(source); index++ {
		if source[index] < 0x21 || source[index] > 0x7e {
			return "", false
		}
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.ForceQuery {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Scheme != scheme || (scheme != "https" && (scheme != "wss" || directive != "connect-src")) {
		return "", false
	}
	port := parsed.Port()
	portNumber := 0
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false
		}
		portNumber = value
	}
	hostname := strings.ToLower(parsed.Hostname())
	host := hostname
	if address, err := netip.ParseAddr(hostname); err == nil {
		host = address.String()
		if address.Is6() {
			host = "[" + host + "]"
		}
	} else if !validPublicPagePolicyDNSName(hostname) {
		return "", false
	}
	rawHost := parsed.Host
	if port != "" {
		suffix := ":" + port
		if !strings.HasSuffix(rawHost, suffix) {
			return "", false
		}
		rawHost = strings.TrimSuffix(rawHost, suffix)
	}
	if strings.Contains(host, ":") {
		if len(rawHost) < 2 || rawHost[0] != '[' || rawHost[len(rawHost)-1] != ']' {
			return "", false
		}
	} else if !strings.EqualFold(rawHost, parsed.Hostname()) {
		return "", false
	}
	if portNumber != 0 && portNumber != 443 {
		host += ":" + strconv.Itoa(portNumber)
	}
	canonical := scheme + "://" + host
	return canonical, true
}

func publicPagePolicyExecutableDirective(value string) bool {
	return value == "script-src" || value == "style-src" || value == "worker-src"
}

func validPublicPagePolicyDNSName(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || !publicPagePolicyDNSAlphaNumeric(label[0]) ||
			!publicPagePolicyDNSAlphaNumeric(label[len(label)-1]) {
			return false
		}
		for index := 1; index < len(label)-1; index++ {
			if !publicPagePolicyDNSAlphaNumeric(label[index]) && label[index] != '-' {
				return false
			}
		}
	}
	return true
}

func publicPagePolicyDNSAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func publicPageExtensionPolicySerializedBytes(directives []PublicFrontendPolicyDirective) int {
	return len(publicPagePolicyHeaderValue(directives))
}

func publicPagePolicyHeaderValue(directives []PublicFrontendPolicyDirective) string {
	var value strings.Builder
	for index, directive := range directives {
		if index != 0 {
			value.WriteString("; ")
		}
		value.WriteString(directive.Name)
		for _, source := range directive.Sources {
			value.WriteByte(' ')
			value.WriteString(source)
		}
	}
	return value.String()
}

func publicPageExtensionPolicyDigest(policy PublicFrontendPolicy) string {
	canonical := struct {
		SchemaVersion      string                            `json:"schemaVersion"`
		GraphDigest        string                            `json:"graphDigest"`
		Directives         []PublicFrontendPolicyDirective   `json:"directives"`
		AdmittedComponents []PublicFrontendPolicyComponent   `json:"admittedComponents"`
		Contributors       []PublicFrontendPolicyContributor `json:"contributors"`
	}{policy.SchemaVersion, policy.GraphDigest, policy.Directives, policy.AdmittedComponents, policy.contributors}
	body, _ := json.Marshal(canonical)
	digest := sha256.New()
	_, _ = digest.Write([]byte("sforum.public-frontend-extension-policy.digest@1\n"))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil))
}

func publicPageDocumentPolicyDigest(
	policy PublicFrontendPolicy,
	document PublicFrontendDocumentPolicy,
) string {
	canonical := struct {
		SchemaVersion          string                          `json:"schemaVersion"`
		ExtensionSchemaVersion string                          `json:"extensionSchemaVersion"`
		GraphDigest            string                          `json:"graphDigest"`
		ExtensionPolicyDigest  string                          `json:"extensionPolicyDigest"`
		Directives             []PublicFrontendPolicyDirective `json:"directives"`
		HeaderValue            string                          `json:"headerValue"`
	}{
		document.SchemaVersion, policy.SchemaVersion, policy.GraphDigest,
		policy.ExtensionPolicyDigest, document.Directives, document.HeaderValue,
	}
	body, _ := json.Marshal(canonical)
	digest := sha256.New()
	_, _ = digest.Write([]byte("sforum.public-frontend-document-policy.digest@1\n"))
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil))
}

func clonePublicFrontendPolicyContributors(
	input []PublicFrontendPolicyContributor,
) []PublicFrontendPolicyContributor {
	result := make([]PublicFrontendPolicyContributor, len(input))
	for index, contributor := range input {
		contributor.AssetHandles = append([]string(nil), contributor.AssetHandles...)
		contributor.Directives = append([]PublicFrontendPolicyDirective(nil), contributor.Directives...)
		for directiveIndex := range contributor.Directives {
			contributor.Directives[directiveIndex].Sources = append(
				[]string(nil), contributor.Directives[directiveIndex].Sources...,
			)
		}
		result[index] = contributor
	}
	return result
}

func publicPagePolicyError(
	code PublicPagePolicyErrorCode,
	tuple PublicFrontendComponentTuple,
	cause error,
) *PublicPagePolicyError {
	return &PublicPagePolicyError{
		Code: code, ExtensionID: tuple.ExtensionID, ComponentID: tuple.ComponentID, Cause: cause,
	}
}

// PublicFrontendComponentRef is a soft page-local L2 reference. Host expands it
// through the live PublicComponent admission path before CSP aggregation.
type PublicFrontendComponentRef struct {
	ExtensionID string `json:"extensionId"`
	ComponentID string `json:"componentId"`
}

// PublicContentStyleCatalog exposes exact plugin-owned CSS for the generic
// forum content surface. Core owns loading and trust checks, not selectors.
type PublicContentStyleCatalog struct {
	SchemaVersion string                         `json:"schemaVersion"`
	GraphDigest   string                         `json:"graphDigest"`
	Styles        []PublicFrontendAssetReference `json:"styles"`
}

func (s *FrontendService) PublicContentStyles(ctx context.Context) (PublicContentStyleCatalog, error) {
	if s == nil || ctx == nil || s.safeMode || !s.v3TrustChallenges ||
		s.extensions == nil || s.executableTrust == nil || s.publicAssets == nil {
		return PublicContentStyleCatalog{}, ErrPublicFrontendUnavailable
	}
	snapshot := s.publicAssets.Snapshot()
	publications, _, err := indexPublicPagePolicySnapshot(snapshot)
	if err != nil {
		return PublicContentStyleCatalog{}, ErrPublicFrontendUnavailable
	}
	plan, err := publicstyles.Plan(snapshot, PublicContentStyleScope)
	if err != nil {
		return PublicContentStyleCatalog{}, ErrPublicFrontendUnavailable
	}
	validated := make(map[string]assetregistry.Artifact)
	extensionsByID := make(map[string]Extension)
	styles := make([]PublicFrontendAssetReference, 0, len(plan))
	for _, asset := range plan {
		if err := s.validatePublicPagePolicyOwner(
			ctx, asset.Artifact, publications, extensionsByID, validated,
		); err != nil {
			return PublicContentStyleCatalog{}, ErrPublicFrontendUnavailable
		}
		styles = append(styles, publicAssetReference(asset))
	}
	if current := s.publicAssets.Snapshot(); current.Digest != snapshot.Digest {
		return PublicContentStyleCatalog{}, ErrPublicFrontendUnavailable
	}
	return PublicContentStyleCatalog{
		SchemaVersion: PublicContentStylesSchemaV1,
		GraphDigest:   snapshot.Digest,
		Styles:        styles,
	}, nil
}

// PublicPagePolicyForComponents expands soft component refs into exact tuples and
// aggregates the Host-owned document CSP for one page response.
func (s *FrontendService) PublicPagePolicyForComponents(
	ctx context.Context,
	refs []PublicFrontendComponentRef,
) (PublicFrontendPolicy, error) {
	if err := s.publicPagePolicyGates(ctx); err != nil {
		return PublicFrontendPolicy{}, err
	}
	if len(refs) > maxPublicPagePolicyComponents {
		return PublicFrontendPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyBoundsExceeded}
	}
	tuples := make([]PublicFrontendComponentTuple, 0, len(refs))
	for _, ref := range refs {
		extensionID := normalizeID(ref.ExtensionID)
		componentID := normalizeID(ref.ComponentID)
		if extensionID == "" || componentID == "" {
			return PublicFrontendPolicy{}, &PublicPagePolicyError{Code: PublicPagePolicyInvalidInput}
		}
		descriptor, err := s.PublicComponent(ctx, extensionID, componentID)
		if err != nil {
			return PublicFrontendPolicy{}, publicPagePolicyError(
				PublicPagePolicyComponentUnavailable,
				PublicFrontendComponentTuple{ExtensionID: extensionID, ComponentID: componentID},
				err,
			)
		}
		tuples = append(tuples, PublicFrontendComponentTuple{
			ExtensionID:      descriptor.ExtensionID,
			ExtensionVersion: descriptor.ExtensionVersion,
			PackageDigest:    descriptor.PackageDigest,
			ComponentID:      descriptor.ComponentID,
			ContractVersion:  descriptor.ContractVersion,
		})
	}
	return s.PublicPagePolicy(ctx, tuples)
}
