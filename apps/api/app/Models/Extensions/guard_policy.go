package extensions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// TrustGuardAuthorizationView 是「哪条路由的授权由哪个精确制品拥有」的派生视图（M5）。
//
// 它**不进入** TrustImpact 摘要：摘要覆盖的是权限本身，而这里只是把已经摘要过的
// 三处信息（Routes 的 guard/fallback/可变字段、GuardDeclarations 的 kind/entry/digest）
// 连接起来供操作员阅读。向 TrustImpact 增加字段会改变每个扩展的 impact_digest，
// 而 impact_digest 是 extension_trust_grants 的身份键（HasLiveGrant/ValidatePublishedIdentity
// 都按它匹配），也会让 legacy 身份采用（只接受 sforum.trust-impact@2）失配，
// 因此这类增强必须走派生视图，不能顺手加进摘要。
type TrustGuardAuthorizationView struct {
	RouteID               string   `json:"routeId"`
	RouteAction           string   `json:"routeAction"`
	Path                  string   `json:"path"`
	Methods               []string `json:"methods"`
	Access                string   `json:"access,omitempty"`
	Permission            string   `json:"permission,omitempty"`
	GuardID               string   `json:"guardId"`
	GuardContractVersion  string   `json:"guardContractVersion,omitempty"`
	GuardKind             string   `json:"guardKind"`
	GuardEntry            string   `json:"guardEntry,omitempty"`
	GuardDigest           string   `json:"guardDigest,omitempty"`
	GuardPermissions      []string `json:"guardPermissions,omitempty"`
	Risk                  string   `json:"risk"`
	RawRequest            bool     `json:"rawRequest"`
	RequiresExactTrust    bool     `json:"requiresExactTrust"`
	Fallback              string   `json:"fallback,omitempty"`
	MutableRequestFields  []string `json:"mutableRequestFields,omitempty"`
	MutableResponseFields []string `json:"mutableResponseFields,omitempty"`
}

// GuardAuthorizationViews 按清单顺序连接路由范围与守卫声明。函数只读取已披露的数据，
// 不查库、不读包内文件，因此可以安全地出现在状态/检视读取路径上。
func GuardAuthorizationViews(impact TrustImpact) []TrustGuardAuthorizationView {
	declarations := make(map[string]extensionmanifest.ManifestGuard, len(impact.GuardDeclarations))
	for _, declaration := range impact.GuardDeclarations {
		declarations[declaration.ID] = declaration
	}
	views := make([]TrustGuardAuthorizationView, 0, len(impact.Routes))
	for _, route := range impact.Routes {
		declaration, declared := declarations[route.Guard]
		kind := declaration.Kind
		view := TrustGuardAuthorizationView{
			RouteID: route.ID, RouteAction: route.Action,
			Path: route.Path, Methods: append([]string{}, route.Methods...),
			Access: route.Access, Permission: route.Permission,
			GuardID: route.Guard, GuardKind: kind, Fallback: route.Fallback,
			MutableRequestFields:  append([]string{}, route.MutableRequestFields...),
			MutableResponseFields: append([]string{}, route.MutableResponseFields...),
		}
		if declared {
			view.GuardContractVersion = declaration.ContractVersion
			view.GuardEntry = declaration.Entry
			view.GuardDigest = declaration.Digest
			view.GuardPermissions = append([]string{}, declaration.Permissions...)
		}
		view.Risk = extensionmanifest.GuardRuntimeRisk(route.Guard, kind)
		view.RawRequest = view.Risk == extensionmanifest.GuardRiskRawRequest
		view.RequiresExactTrust = extensionmanifest.GuardRequiresExactArtifactTrust(route.Guard, kind)
		views = append(views, view)
	}
	return views
}

const (
	defaultGuardPolicyTTL                 = 30 * time.Second
	RecommendedGuardPolicyRefreshInterval = 5 * time.Second
)

var errGuardPolicyArtifactInvalid = errors.New("extensions: invalid guard policy artifact")

// GuardPolicyEntry 是管理路由 Guard 所需的最小 exact-artifact 视图。
// 该类型不暴露 Manifest，避免请求路径读到可变切片或 map。
type GuardPolicyEntry struct {
	ExtensionID             string
	ExtensionType           string
	Status                  string
	Source                  string
	Version                 string
	PackageDigest           string
	AdminFrontendDigest     string
	HasPrebuiltAdmin        bool
	FrontendArtifactTrusted bool
	HasMailProvider         bool
	HasExecutableBackend    bool
	LifecycleV2             bool
	CurrentTrustRequired    bool
	CurrentArtifactTrusted  bool
	currentTrustGrantID     string
	ReviewVersion           string
	ReviewPackageDigest     string
	ReviewTrustRequired     bool
	ReviewArtifactTrusted   bool
	reviewTrustGrantID      string
	HasStagedArtifact       bool
	StagedVersion           string
	StagedPackageDigest     string
	StagedTrustRequired     bool
	StagedArtifactTrusted   bool
	stagedTrustGrantID      string
	IsSystem                bool
	IsDeletable             bool
}

type GuardPolicyLookup struct {
	Revision               uint64
	SafeMode               bool
	TrustChallengesEnabled bool
	Entry                  GuardPolicyEntry
	Found                  bool
}

type DeclaredRouteGuardLookup struct {
	Revision         uint64
	ExtensionID      string
	ExtensionVersion string
	PackageDigest    string
	Access           string
	Permission       string
}

type declaredRouteGuardEntry struct {
	path       string
	methods    []string
	access     string
	permission string
}

type guardPolicyExtensionSource interface {
	List(context.Context) ([]Extension, error)
}

type guardPolicyExecutableTrust interface {
	TrustedArtifact(context.Context, Extension) (bool, error)
}

type guardPolicyExecutableTrustIdentity interface {
	RuntimeIdentity(context.Context, Extension) (RuntimeTrustIdentity, error)
}

type GuardPolicyConfig struct {
	SafeMode               bool
	TrustChallengesEnabled bool
	TTL                    time.Duration
}

type guardPolicySnapshot struct {
	entries        map[string]GuardPolicyEntry
	declaredRoutes map[string][]declaredRouteGuardEntry
	expiresAt      time.Time
	revision       uint64
}

type guardPolicyArtifactTrustKey struct {
	extensionID   string
	version       string
	packageDigest string
	trustGrantID  string
}

// GuardPolicyCatalog 在后台冻结扩展类型、制品和信任状态。Lookup 只读内存，
// 不得在 HTTP Guard 热路径触发 PostgreSQL 或文件系统访问。
type GuardPolicyCatalog struct {
	extensions      guardPolicyExtensionSource
	executableTrust guardPolicyExecutableTrust
	frontendTrust   FrontendTrustStore
	config          GuardPolicyConfig

	mu       sync.RWMutex
	snapshot *guardPolicySnapshot
	revision uint64
	// publicationEpoch fences refresh work that read trust before an in-memory
	// revocation. It changes even when no usable snapshot is currently present.
	publicationEpoch uint64
	// pendingRevocations blocks publication between the pre-durable exact capture
	// and its explicit completion. Tombstones keep an ambiguous old grant closed
	// until PostgreSQL reports it absent or a newer exact grant generation exists.
	pendingRevocations map[string]GuardPolicyEntry
	revokedArtifacts   map[guardPolicyArtifactTrustKey]struct{}
}

func NewGuardPolicyCatalog(
	extensions guardPolicyExtensionSource,
	executableTrust guardPolicyExecutableTrust,
	frontendTrust FrontendTrustStore,
	config GuardPolicyConfig,
) *GuardPolicyCatalog {
	if config.TTL <= 0 {
		config.TTL = defaultGuardPolicyTTL
	}
	return &GuardPolicyCatalog{
		extensions: extensions, executableTrust: executableTrust,
		frontendTrust: frontendTrust, config: config,
		pendingRevocations: make(map[string]GuardPolicyEntry),
		revokedArtifacts:   make(map[guardPolicyArtifactTrustKey]struct{}),
	}
}

func (c *GuardPolicyCatalog) Refresh(ctx context.Context) error {
	if c == nil || c.extensions == nil || ctx == nil {
		return errors.New("extensions: guard policy source is unavailable")
	}
	c.mu.RLock()
	publicationEpoch := c.publicationEpoch
	c.mu.RUnlock()
	items, err := c.extensions.List(ctx)
	if err != nil {
		return err
	}
	entries := make(map[string]GuardPolicyEntry, len(items))
	declaredRoutes := make(map[string][]declaredRouteGuardEntry, len(items))
	for _, extension := range items {
		entry, err := c.freezeEntry(ctx, extension)
		if err != nil {
			// Out-of-band disable must recover boot even when the retained artifact
			// cannot be decoded. Other failures still fail closed.
			if extension.Status == StatusDisabled &&
				(errors.Is(err, errGuardPolicyArtifactInvalid) || errors.Is(err, ErrFrontendPackageChanged)) {
				continue
			}
			return err
		}
		if _, duplicate := entries[entry.ExtensionID]; duplicate {
			return fmt.Errorf("extensions: duplicate guard policy extension %q", entry.ExtensionID)
		}
		entries[entry.ExtensionID] = entry
		declaredRoutes[entry.ExtensionID] = freezeDeclaredRouteGuards(extension.Manifest.Routes)
	}

	c.mu.Lock()
	if c.publicationEpoch != publicationEpoch || len(c.pendingRevocations) != 0 {
		c.mu.Unlock()
		return nil
	}
	c.applyExecutableTrustTombstonesLocked(entries)
	c.revision++
	c.snapshot = &guardPolicySnapshot{
		entries: entries, declaredRoutes: declaredRoutes,
		expiresAt: time.Now().Add(c.config.TTL), revision: c.revision,
	}
	c.mu.Unlock()
	return nil
}

// LookupDeclaredRoute 在同一不可变目录内解析 legacy proxy route。V3 custom/raw/
// inherited guards 不进入该视图，必须由独立可信 Route Registry 执行。
func (c *GuardPolicyCatalog) LookupDeclaredRoute(extensionID, method, routePath string) (DeclaredRouteGuardLookup, bool) {
	if c == nil {
		return DeclaredRouteGuardLookup{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil || c.snapshot.revision == 0 || !time.Now().Before(c.snapshot.expiresAt) || c.config.SafeMode {
		return DeclaredRouteGuardLookup{}, false
	}
	id := normalizeID(extensionID)
	entry, found := c.snapshot.entries[id]
	if !found || entry.ExtensionID != extensionID || entry.ExtensionType != TypePlugin || entry.Status != StatusEnabled ||
		entry.Version == "" || entry.PackageDigest == "" ||
		(entry.CurrentTrustRequired && !entry.CurrentArtifactTrusted) {
		return DeclaredRouteGuardLookup{}, false
	}
	wantedPath := normalizeRoutePath(routePath)
	wantedMethod := strings.ToUpper(strings.TrimSpace(method))
	var matched *declaredRouteGuardEntry
	for index := range c.snapshot.declaredRoutes[id] {
		route := &c.snapshot.declaredRoutes[id][index]
		if route.path != wantedPath || !slices.Contains(route.methods, wantedMethod) {
			continue
		}
		if matched != nil {
			return DeclaredRouteGuardLookup{}, false
		}
		matched = route
	}
	if matched == nil {
		return DeclaredRouteGuardLookup{}, false
	}
	return DeclaredRouteGuardLookup{
		Revision: c.snapshot.revision, ExtensionID: entry.ExtensionID,
		ExtensionVersion: entry.Version, PackageDigest: entry.PackageDigest,
		Access: matched.access, Permission: matched.permission,
	}, true
}

func (c *GuardPolicyCatalog) Lookup(extensionID string) (GuardPolicyLookup, bool) {
	if c == nil {
		return GuardPolicyLookup{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.snapshot == nil || c.snapshot.revision == 0 || !time.Now().Before(c.snapshot.expiresAt) {
		return GuardPolicyLookup{}, false
	}
	id := normalizeID(extensionID)
	entry, found := c.snapshot.entries[id]
	return GuardPolicyLookup{
		Revision: c.snapshot.revision, SafeMode: c.config.SafeMode,
		TrustChallengesEnabled: c.config.TrustChallengesEnabled,
		Entry:                  entry, Found: found,
	}, true
}

// CaptureExecutableTrustExact starts the process-local half of a durable
// revocation. It deliberately ignores snapshot TTL: an expired entry is still
// the exact pre-revoke artifact that must be fenced. Refresh publication stays
// paused until the caller either releases or invalidates this capture.
func (c *GuardPolicyCatalog) CaptureExecutableTrustExact(extensionID string) (GuardPolicyEntry, bool) {
	return c.CaptureExecutableTrustExactWithFallback(
		extensionID,
		GuardPolicyEntry{ExtensionID: normalizeID(extensionID)},
	)
}

// CaptureExecutableTrustExactWithFallback uses a caller-held runtime artifact
// when the policy snapshot has not observed the extension yet. The fallback is
// stored as the capture token too, so rollback release and unknown-outcome
// tombstones operate on the same exact value.
func (c *GuardPolicyCatalog) CaptureExecutableTrustExactWithFallback(
	extensionID string,
	fallback GuardPolicyEntry,
) (GuardPolicyEntry, bool) {
	if c == nil {
		return GuardPolicyEntry{}, false
	}
	id := normalizeID(extensionID)
	if id == "" || id != extensionID || fallback.ExtensionID != id ||
		(fallback.CurrentTrustRequired &&
			(strings.TrimSpace(fallback.Version) == "" || strings.TrimSpace(fallback.PackageDigest) == "")) {
		return GuardPolicyEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publicationEpoch++
	captured := fallback
	found := false
	if c.snapshot != nil {
		if entry, ok := c.snapshot.entries[id]; ok && entry.ExtensionID == extensionID {
			captured, found = entry, true
		}
	}
	if c.pendingRevocations == nil {
		c.pendingRevocations = make(map[string]GuardPolicyEntry)
	}
	c.pendingRevocations[id] = captured
	return captured, found
}

// ReleaseExecutableTrustCaptureExact ends a capture after PostgreSQL proved
// that COMMIT did not take effect. Exact comparison prevents stale cleanup from
// releasing a newer capture for the same extension.
func (c *GuardPolicyCatalog) ReleaseExecutableTrustCaptureExact(
	extensionID string,
	captured GuardPolicyEntry,
) bool {
	if c == nil {
		return false
	}
	id := normalizeID(extensionID)
	if id == "" || id != extensionID || captured.ExtensionID != extensionID {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending, found := c.pendingRevocations[id]
	if !found || pending != captured {
		return false
	}
	delete(c.pendingRevocations, id)
	c.publicationEpoch++
	return true
}

// InvalidateExecutableTrustExact publishes a new in-memory revision without
// I/O. Each current/review/staged slot is closed only when it still names the
// artifact captured before the durable revoke; a concurrent reauthorization
// may therefore publish a new exact artifact without being erased by stale work.
func (c *GuardPolicyCatalog) InvalidateExecutableTrustExact(
	extensionID string,
	captured GuardPolicyEntry,
) bool {
	if c == nil {
		return false
	}
	id := normalizeID(extensionID)
	if id == "" || id != extensionID || captured.ExtensionID != extensionID {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publicationEpoch++
	if pending, found := c.pendingRevocations[id]; found && pending == captured {
		delete(c.pendingRevocations, id)
	}
	c.rememberExecutableTrustRevocationLocked(captured)
	if c.snapshot == nil {
		return false
	}
	entry, found := c.snapshot.entries[id]
	if !found {
		return false
	}
	changed := false
	if captured.CurrentTrustRequired && entry.CurrentTrustRequired && sameGuardPolicyTrustIdentity(
		captured.Version, captured.PackageDigest, captured.currentTrustGrantID,
		entry.Version, entry.PackageDigest, entry.currentTrustGrantID,
	) {
		if entry.CurrentArtifactTrusted {
			entry.CurrentArtifactTrusted = false
			changed = true
		}
		if c.config.TrustChallengesEnabled && entry.FrontendArtifactTrusted {
			entry.FrontendArtifactTrusted = false
			changed = true
		}
	}
	if captured.ReviewTrustRequired && entry.ReviewTrustRequired && sameGuardPolicyTrustIdentity(
		captured.ReviewVersion, captured.ReviewPackageDigest, captured.reviewTrustGrantID,
		entry.ReviewVersion, entry.ReviewPackageDigest, entry.reviewTrustGrantID,
	) &&
		entry.ReviewArtifactTrusted {
		entry.ReviewArtifactTrusted = false
		changed = true
	}
	if captured.HasStagedArtifact && captured.StagedTrustRequired &&
		entry.HasStagedArtifact && entry.StagedTrustRequired && sameGuardPolicyTrustIdentity(
		captured.StagedVersion, captured.StagedPackageDigest, captured.stagedTrustGrantID,
		entry.StagedVersion, entry.StagedPackageDigest, entry.stagedTrustGrantID,
	) &&
		entry.StagedArtifactTrusted {
		entry.StagedArtifactTrusted = false
		changed = true
	}
	if !changed {
		return false
	}
	entries := make(map[string]GuardPolicyEntry, len(c.snapshot.entries))
	for key, value := range c.snapshot.entries {
		entries[key] = value
	}
	entries[id] = entry
	c.revision++
	c.snapshot = &guardPolicySnapshot{
		entries: entries, declaredRoutes: c.snapshot.declaredRoutes,
		expiresAt: c.snapshot.expiresAt, revision: c.revision,
	}
	return true
}

func sameGuardPolicyTrustIdentity(
	capturedVersion string,
	capturedDigest string,
	capturedGrantID string,
	currentVersion string,
	currentDigest string,
	currentGrantID string,
) bool {
	if capturedVersion != currentVersion || capturedDigest != currentDigest {
		return false
	}
	// Older/custom trust readers expose only the artifact tuple. Production
	// readers expose grant IDs so a same-artifact reauthorization is distinguishable.
	return capturedGrantID == "" || currentGrantID == "" || capturedGrantID == currentGrantID
}

func (c *GuardPolicyCatalog) rememberExecutableTrustRevocationLocked(captured GuardPolicyEntry) {
	if c.revokedArtifacts == nil {
		c.revokedArtifacts = make(map[guardPolicyArtifactTrustKey]struct{})
	}
	remember := func(required bool, version, digest, grantID string) {
		if !required || strings.TrimSpace(version) == "" || strings.TrimSpace(digest) == "" {
			return
		}
		c.revokedArtifacts[guardPolicyArtifactTrustKey{
			extensionID: captured.ExtensionID, version: version,
			packageDigest: digest, trustGrantID: grantID,
		}] = struct{}{}
	}
	remember(captured.CurrentTrustRequired, captured.Version, captured.PackageDigest, captured.currentTrustGrantID)
	remember(captured.ReviewTrustRequired, captured.ReviewVersion, captured.ReviewPackageDigest, captured.reviewTrustGrantID)
	if captured.HasStagedArtifact {
		remember(captured.StagedTrustRequired, captured.StagedVersion, captured.StagedPackageDigest, captured.stagedTrustGrantID)
	}
}

func (c *GuardPolicyCatalog) applyExecutableTrustTombstonesLocked(entries map[string]GuardPolicyEntry) {
	if len(c.revokedArtifacts) == 0 {
		return
	}
	for id, entry := range entries {
		if entry.CurrentTrustRequired {
			entry.CurrentArtifactTrusted = c.applyExecutableTrustTombstoneLocked(
				id, entry.Version, entry.PackageDigest, entry.currentTrustGrantID,
				entry.CurrentArtifactTrusted,
			)
			if c.config.TrustChallengesEnabled && !entry.CurrentArtifactTrusted {
				entry.FrontendArtifactTrusted = false
			}
		}
		if entry.ReviewTrustRequired {
			entry.ReviewArtifactTrusted = c.applyExecutableTrustTombstoneLocked(
				id, entry.ReviewVersion, entry.ReviewPackageDigest, entry.reviewTrustGrantID,
				entry.ReviewArtifactTrusted,
			)
		}
		if entry.HasStagedArtifact && entry.StagedTrustRequired {
			entry.StagedArtifactTrusted = c.applyExecutableTrustTombstoneLocked(
				id, entry.StagedVersion, entry.StagedPackageDigest, entry.stagedTrustGrantID,
				entry.StagedArtifactTrusted,
			)
		}
		entries[id] = entry
	}
}

func (c *GuardPolicyCatalog) applyExecutableTrustTombstoneLocked(
	extensionID string,
	version string,
	digest string,
	grantID string,
	trusted bool,
) bool {
	sameArtifact := func(key guardPolicyArtifactTrustKey) bool {
		return key.extensionID == extensionID && key.version == version && key.packageDigest == digest
	}
	if !trusted {
		// A durable negative read resolves both known and ambiguous COMMIT outcomes.
		for key := range c.revokedArtifacts {
			if sameArtifact(key) {
				delete(c.revokedArtifacts, key)
			}
		}
		return false
	}
	exact := guardPolicyArtifactTrustKey{
		extensionID: extensionID, version: version, packageDigest: digest, trustGrantID: grantID,
	}
	for key := range c.revokedArtifacts {
		if sameArtifact(key) && key.trustGrantID == "" {
			// Capture may precede the refresh that learns the live grant generation.
			// An unknown generation is therefore a wildcard, not evidence that any
			// subsequently observed non-empty ID is a new authorization. Only a
			// durable negative read above may resolve this ambiguity.
			return false
		}
	}
	if _, revoked := c.revokedArtifacts[exact]; revoked {
		return false
	}
	if grantID == "" {
		// Without a source generation, fail closed against every tombstone for the
		// same artifact. Production uses grant IDs and does not take this fallback.
		for key := range c.revokedArtifacts {
			if sameArtifact(key) {
				return false
			}
		}
		return true
	}
	// A different live grant ID is an explicit same-artifact reauthorization.
	for key := range c.revokedArtifacts {
		if sameArtifact(key) {
			delete(c.revokedArtifacts, key)
		}
	}
	return true
}

func (c *GuardPolicyCatalog) RunRefresh(ctx context.Context, interval time.Duration) {
	if c == nil || ctx == nil {
		return
	}
	if interval <= 0 {
		interval = RecommendedGuardPolicyRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Refresh(ctx)
		}
	}
}

func (c *GuardPolicyCatalog) freezeEntry(ctx context.Context, extension Extension) (GuardPolicyEntry, error) {
	id := normalizeID(extension.ID)
	if id == "" || id != extension.ID || (extension.Type != TypePlugin && extension.Type != TypeTheme) ||
		(extension.Status != StatusInstalled && extension.Status != StatusEnabled && extension.Status != StatusDisabled) ||
		strings.TrimSpace(extension.Version) == "" || strings.TrimSpace(extension.PackageDigest) == "" ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version || extension.Manifest.Type != extension.Type {
		return GuardPolicyEntry{}, fmt.Errorf("%w %q", errGuardPolicyArtifactInvalid, extension.ID)
	}

	currentTrusted, currentTrustGrantID, err := c.artifactTrusted(ctx, extension)
	if err != nil {
		return GuardPolicyEntry{}, fmt.Errorf("extensions: resolve current guard trust for %s: %w", extension.ID, err)
	}
	review := trustReviewArtifact(extension)
	reviewTrusted := currentTrusted
	reviewTrustGrantID := currentTrustGrantID
	if review.Version != extension.Version || review.PackageDigest != extension.PackageDigest {
		reviewTrusted, reviewTrustGrantID, err = c.artifactTrusted(ctx, review)
		if err != nil {
			return GuardPolicyEntry{}, fmt.Errorf("extensions: resolve review guard trust for %s: %w", extension.ID, err)
		}
	}
	frontendTrusted := currentTrusted
	if !c.config.TrustChallengesEnabled {
		frontendTrusted, err = c.legacyFrontendTrusted(ctx, extension)
		if err != nil {
			return GuardPolicyEntry{}, fmt.Errorf("extensions: resolve frontend guard trust for %s: %w", extension.ID, err)
		}
	}
	entry := GuardPolicyEntry{
		ExtensionID: extension.ID, ExtensionType: extension.Type, Status: extension.Status,
		Source: extension.Source, Version: extension.Version, PackageDigest: extension.PackageDigest,
		AdminFrontendDigest:     extension.AdminFrontendDigest,
		HasPrebuiltAdmin:        prebuiltSettingsComponent(extension) != nil,
		FrontendArtifactTrusted: frontendTrusted,
		HasMailProvider:         manifestHasProvider(extension.Manifest, "mail.provider"),
		HasExecutableBackend:    hasExecutableBackend(extension.Manifest), LifecycleV2: usesLifecycleV2(extension),
		CurrentTrustRequired: RequiresExecutableTrust(extension), CurrentArtifactTrusted: currentTrusted,
		currentTrustGrantID: currentTrustGrantID,
		ReviewVersion:       review.Version, ReviewPackageDigest: review.PackageDigest,
		ReviewTrustRequired: RequiresExecutableTrust(review), ReviewArtifactTrusted: reviewTrusted,
		reviewTrustGrantID: reviewTrustGrantID,
		IsSystem:           extension.IsSystem, IsDeletable: extension.IsDeletable,
	}
	if staged, ok := extension.StagedArtifact(); ok {
		stagedTrusted := reviewTrusted
		stagedTrustGrantID := reviewTrustGrantID
		if staged.Version != review.Version || staged.PackageDigest != review.PackageDigest {
			stagedTrusted, stagedTrustGrantID, err = c.artifactTrusted(ctx, staged)
			if err != nil {
				return GuardPolicyEntry{}, fmt.Errorf("extensions: resolve staged guard trust for %s: %w", extension.ID, err)
			}
		}
		entry.HasStagedArtifact = true
		entry.StagedVersion = staged.Version
		entry.StagedPackageDigest = staged.PackageDigest
		entry.StagedTrustRequired = RequiresExecutableTrust(staged)
		entry.StagedArtifactTrusted = stagedTrusted
		entry.stagedTrustGrantID = stagedTrustGrantID
	}
	return entry, nil
}

func (c *GuardPolicyCatalog) artifactTrusted(ctx context.Context, extension Extension) (bool, string, error) {
	if !RequiresExecutableTrust(extension) {
		return true, "", nil
	}
	if c.executableTrust == nil {
		return false, "", errors.New("executable trust source is unavailable")
	}
	if source, ok := c.executableTrust.(guardPolicyExecutableTrustIdentity); ok {
		identity, err := source.RuntimeIdentity(ctx, extension)
		if errors.Is(err, ErrTrustGrantNotFound) {
			return false, "", nil
		}
		if err != nil {
			return false, "", err
		}
		if strings.TrimSpace(identity.TrustGrantID) == "" {
			return false, "", errors.New("executable trust source returned an empty grant identity")
		}
		return true, identity.TrustGrantID, nil
	}
	trusted, err := c.executableTrust.TrustedArtifact(ctx, extension)
	return trusted, "", err
}

func manifestHasProvider(manifest Manifest, slot string) bool {
	for _, provider := range manifest.Providers {
		if provider.Slot == slot {
			return true
		}
	}
	return false
}

func freezeDeclaredRouteGuards(routes []ManifestRoute) []declaredRouteGuardEntry {
	result := make([]declaredRouteGuardEntry, 0, len(routes))
	for _, route := range routes {
		access, permission, ok := declaredRouteGuardAccess(route)
		if !ok || route.Path == "" || !strings.HasPrefix(route.Path, "/") || strings.Contains(route.Path, "..") {
			continue
		}
		methods := make([]string, 0, len(route.Methods))
		for _, method := range route.Methods {
			method = strings.ToUpper(strings.TrimSpace(method))
			if !validDeclaredRouteGuardMethod(method) ||
				(access == RouteAccessPublic && method != "GET" && method != "HEAD" && method != "OPTIONS") {
				methods = nil
				break
			}
			if !slices.Contains(methods, method) {
				methods = append(methods, method)
			}
		}
		if len(methods) == 0 {
			continue
		}
		result = append(result, declaredRouteGuardEntry{
			path: normalizeRoutePath(route.Path), methods: methods,
			access: access, permission: permission,
		})
	}
	return result
}

func validDeclaredRouteGuardMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func declaredRouteGuardAccess(route ManifestRoute) (access string, permission string, ok bool) {
	access = strings.TrimSpace(route.Access)
	guard := strings.TrimSpace(route.Guard)
	if guard == "" {
		if access == "" {
			access = RouteAccessLogin
		}
	} else {
		switch guard {
		case extensionmanifest.GuardCorePublic:
			if access != RouteAccessPublic {
				return "", "", false
			}
		case extensionmanifest.GuardCoreLogin:
			if access == "" {
				access = RouteAccessLogin
			}
			if access != RouteAccessLogin {
				return "", "", false
			}
		case extensionmanifest.GuardCorePermission:
			if access != RouteAccessPermission {
				return "", "", false
			}
		default:
			return "", "", false
		}
	}
	switch access {
	case RouteAccessPublic, RouteAccessLogin:
		return access, "", true
	case RouteAccessPermission:
		permission = strings.TrimSpace(route.Permission)
		return access, permission, permission != ""
	default:
		return "", "", false
	}
}

func (c *GuardPolicyCatalog) legacyFrontendTrusted(ctx context.Context, extension Extension) (bool, error) {
	component := prebuiltSettingsComponent(extension)
	if component == nil {
		return false, nil
	}
	if extension.Source == SourceBuiltin && extension.IsSystem && !extension.IsDeletable {
		return true, nil
	}
	if c.frontendTrust == nil {
		return false, errors.New("frontend trust source is unavailable")
	}
	grant, err := c.frontendTrust.FrontendGrant(ctx, extension.ID, extension.Version, extension.AdminFrontendDigest)
	if err != nil {
		if errors.Is(err, ErrFrontendGrantNotFound) {
			return false, nil
		}
		return false, err
	}
	return grant.APIVersion == component.APIVersion && slices.Contains(grant.ComponentIDs, component.ID), nil
}
