package hostapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

const (
	HostCacheCoreProviderID = "core.cache.redis"
	HostCacheFallbackClosed = "closed"
	// HostCacheFallbackNext remains a recognized compatibility value, but v1
	// execution rejects it until a durable cross-provider generation/tombstone
	// protocol can prevent deleted or invalidated data from resurfacing.
	HostCacheFallbackNext         = "next"
	HostCacheMaximumKeyBytes      = 512
	HostCacheMaximumValueBytes    = 1 << 20
	HostCacheMaximumTags          = 64
	HostCacheMinimumTTL           = time.Millisecond
	HostCacheMaximumTTL           = 24 * time.Hour
	HostCacheMinimumLockTTL       = 100 * time.Millisecond
	HostCacheMaximumLockTTL       = 30 * time.Second
	HostCacheDefaultRememberWait  = 2 * time.Second
	hostCacheMaximumProviderCount = 8
	hostCacheRevisionBytes        = 32
	hostCacheSchemaPartBytes      = 128
	hostCacheInstallationIDBytes  = 255
	defaultHostCacheKeyPrefix     = "sforum:host-cache:v1:installation:"
)

var (
	ErrHostCacheInvalid             = errors.New("host cache request is invalid")
	ErrHostCacheDenied              = errors.New("host cache caller is denied")
	ErrHostCacheScopeRequired       = errors.New("host cache requires Host-attested isolation scope")
	ErrHostCacheStale               = errors.New("host cache runtime or declaration is stale")
	ErrHostCacheProviderUnavailable = errors.New("host cache provider is unavailable")
	ErrHostCacheProviderInvalid     = errors.New("host cache provider resolution is invalid")
	ErrHostCacheConflict            = errors.New("host cache revision conflicts with the stored value")
	ErrHostCachePoisoned            = errors.New("host cache provider returned an invalid value")
	ErrHostCacheLockNotOwned        = errors.New("host cache lock is expired or owned by another caller")
)

// HostCacheStoredValue is the provider-neutral value envelope. Tags are
// Host-derived physical tag keys; callers never supply them directly.
type HostCacheStoredValue struct {
	Value         []byte   `json:"value"`
	SchemaID      string   `json:"schemaId"`
	SchemaVersion string   `json:"schemaVersion"`
	Revision      string   `json:"revision"`
	Tags          []string `json:"tags,omitempty"`
}

type HostCacheBackend interface {
	Get(context.Context, string) (HostCacheStoredValue, bool, error)
	Set(context.Context, string, HostCacheStoredValue, time.Duration, string, string) error
	Delete(context.Context, string, string) (bool, error)
	InvalidateTags(context.Context, []string, string) (uint64, error)
	Increment(context.Context, string, int64, time.Duration) (int64, error)
	AcquireLock(context.Context, string, string, time.Duration) (bool, error)
	RenewLock(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLock(context.Context, string, string) (bool, error)
	SetAndReleaseLock(context.Context, string, HostCacheStoredValue, time.Duration, string, string, string, string) error
}

type HostCacheCaller struct {
	ExtensionID       string
	ExtensionVersion  string
	ArtifactDigest    string
	VersionID         int64
	RuntimeInstanceID string
	Attested          bool
	Core              bool
	// versionFromHostPlan is set only by the Host's transport adapter after its
	// runtime identity context and request identity match exactly. Plugin-facing
	// callers cannot use an omitted VersionID as a wildcard.
	versionFromHostPlan bool
}

type HostCacheScope struct {
	ActorFingerprint      string
	PermissionFingerprint string
	Locale                string
}

type HostCacheRequestBase struct {
	Caller    HostCacheCaller
	CacheID   string
	Namespace string
	Scope     HostCacheScope
}

type HostCacheSchema struct {
	ID      string
	Version string
}

type HostCacheGetRequest struct {
	HostCacheRequestBase
	Key    string
	Schema HostCacheSchema
}

type HostCacheGetResult struct {
	Found    bool
	Value    []byte
	Revision string
}

type HostCacheSetRequest struct {
	HostCacheRequestBase
	Key              string
	Schema           HostCacheSchema
	Value            []byte
	TTL              time.Duration
	Tags             []string
	ExpectedRevision string
}

type HostCacheDeleteRequest struct {
	HostCacheRequestBase
	Key string
}

type HostCacheInvalidateTagsRequest struct {
	HostCacheRequestBase
	Tags []string
}

type HostCacheInvalidatorRequest struct {
	HostCacheRequestBase
	InvalidatorID string
}

type HostCacheIncrementRequest struct {
	HostCacheRequestBase
	Key   string
	Delta int64
	TTL   time.Duration
}

type HostCacheRememberRequest struct {
	HostCacheSetRequest
	LockTTL time.Duration
	Wait    time.Duration
	Load    func(context.Context) ([]byte, error)
}

type HostCacheLockRequest struct {
	HostCacheRequestBase
	Key string
	TTL time.Duration
}

type HostCacheProviderRequest struct {
	DeclaredProvider string
	CacheID          string
	ContractVersion  string
	Owner            cacheregistry.Artifact
	SafeMode         bool
}

type HostCacheProviderCandidate struct {
	ProviderID       string
	ExtensionID      string
	ExtensionVersion string
	ArtifactDigest   string
	VersionID        int64
	RuntimeInstance  string
	Core             bool
	Backend          HostCacheBackend
}

type HostCacheProviderResolution struct {
	Revision   uint64
	Fallback   string
	Candidates []HostCacheProviderCandidate
}

// HostCacheProviderResolver adapts the existing durable provider-slot
// selection layer. Validate closes the selection/admission TOCTOU before a
// runtime lease is entered; that lease pins the in-flight provider call.
type HostCacheProviderResolver interface {
	ResolveHostCacheProvider(context.Context, HostCacheProviderRequest) (HostCacheProviderResolution, error)
	ValidateHostCacheProvider(context.Context, HostCacheProviderRequest, HostCacheProviderResolution, HostCacheProviderCandidate) error
}

type HostCacheServiceOption interface {
	applyHostCacheService(*HostCacheService)
}

type hostCacheServiceOptionFunc func(*HostCacheService)

func (f hostCacheServiceOptionFunc) applyHostCacheService(service *HostCacheService) { f(service) }

func WithHostCacheTraceSink(sink HostCacheTraceSink) HostCacheServiceOption {
	return hostCacheServiceOptionFunc(func(service *HostCacheService) { service.traceSink = sink })
}

// WithHostCacheInstallationID supplies the deployment-specific cache boundary.
// The raw id is never placed in Redis keys; only its SHA-256 digest is used.
func WithHostCacheInstallationID(installationID string) HostCacheServiceOption {
	return hostCacheServiceOptionFunc(func(service *HostCacheService) {
		service.installationID = strings.TrimSpace(installationID)
	})
}

// WithHostCacheRuntimeAdmission binds every plugin-owned operation and every
// executable provider call to the existing exact-instance runtime gate.
func WithHostCacheRuntimeAdmission(admission ServiceProviderAdmission) HostCacheServiceOption {
	return hostCacheServiceOptionFunc(func(service *HostCacheService) { service.admission = admission })
}

type HostCacheService struct {
	registry       *cacheregistry.Registry
	core           HostCacheBackend
	resolver       HostCacheProviderResolver
	traceSink      HostCacheTraceSink
	admission      ServiceProviderAdmission
	installationID string
	keyPrefix      string
}

func NewHostCacheService(
	registry *cacheregistry.Registry,
	core HostCacheBackend,
	resolver HostCacheProviderResolver,
	options ...HostCacheServiceOption,
) (*HostCacheService, error) {
	if registry == nil || core == nil {
		return nil, ErrHostCacheInvalid
	}
	service := &HostCacheService{registry: registry, core: core, resolver: resolver}
	for _, option := range options {
		if option != nil {
			option.applyHostCacheService(service)
		}
	}
	if !boundedHostCacheIdentity(service.installationID, hostCacheInstallationIDBytes) ||
		service.admission == nil || service.traceSink == nil {
		return nil, ErrHostCacheInvalid
	}
	installationDigest := sha256.Sum256([]byte(service.installationID))
	service.keyPrefix = defaultHostCacheKeyPrefix + hex.EncodeToString(installationDigest[:]) + ":contract:"
	return service, nil
}

type preparedHostCache struct {
	request          HostCacheRequestBase
	plan             cacheregistry.Plan
	providerReq      HostCacheProviderRequest
	providers        HostCacheProviderResolution
	external         bool
	contractRoot     string
	tagPrefix        string
	allowedTags      map[string]string
	ownerLease       ServiceProviderAdmissionLease
	executionCtx     context.Context
	traceTagDigest   string
	traceTagCount    int
	traceInvalidator string
}

func (s *HostCacheService) prepare(ctx context.Context, request HostCacheRequestBase) (preparedHostCache, error) {
	if s == nil || s.registry == nil || s.core == nil || ctx == nil {
		return preparedHostCache{}, ErrHostCacheInvalid
	}
	plan, err := s.registry.Plan(cacheregistry.PlanRequest{
		CacheID: request.CacheID, Namespace: request.Namespace,
		ActorFingerprint:      request.Scope.ActorFingerprint,
		PermissionFingerprint: request.Scope.PermissionFingerprint,
		LocaleFingerprint:     request.Scope.Locale,
	})
	if err != nil {
		switch {
		case errors.Is(err, cacheregistry.ErrIsolationRequired):
			return preparedHostCache{}, ErrHostCacheScopeRequired
		case errors.Is(err, cacheregistry.ErrArtifactUnavailable), errors.Is(err, cacheregistry.ErrPlanStale),
			errors.Is(err, cacheregistry.ErrArtifactConflict):
			return preparedHostCache{}, ErrHostCacheStale
		default:
			return preparedHostCache{}, fmt.Errorf("%w: cache declaration", ErrHostCacheInvalid)
		}
	}
	if err := validateHostCacheCaller(request.Caller, plan.Cache.Artifact); err != nil {
		return preparedHostCache{}, err
	}
	ownerLease, executionCtx, err := s.acquireHostCacheRuntime(ctx, plan.Cache.Artifact)
	if err != nil {
		return preparedHostCache{}, err
	}
	releaseOwner := true
	defer func() {
		if releaseOwner && ownerLease != nil {
			ownerLease.Release()
		}
	}()
	if err := s.validateHostCacheLease(ownerLease, executionCtx); err != nil {
		return preparedHostCache{}, err
	}
	if err := s.registry.ValidateLeasedPlan(plan); err != nil {
		return preparedHostCache{}, ErrHostCacheStale
	}
	providerReq := HostCacheProviderRequest{
		DeclaredProvider: plan.Cache.Provider, CacheID: plan.Cache.ID,
		ContractVersion: plan.Cache.ContractVersion, Owner: plan.Cache.Artifact, SafeMode: plan.SafeMode,
	}
	resolution, external, err := s.resolveProviders(executionCtx, providerReq)
	if err != nil {
		return preparedHostCache{}, err
	}
	contractParts := []string{
		plan.Cache.Artifact.ExtensionID, plan.Cache.Artifact.ExtensionVersion, plan.Cache.Artifact.PackageDigest,
		strconv.FormatInt(plan.Cache.Artifact.VersionID, 10),
		plan.Cache.ID, plan.Cache.ContractVersion, plan.Cache.Namespace,
	}
	if external {
		candidate := resolution.Candidates[0]
		contractParts = append(contractParts, "provider", strconv.FormatUint(resolution.Revision, 10),
			candidate.ProviderID, candidate.ExtensionID, candidate.ExtensionVersion, candidate.ArtifactDigest,
			strconv.FormatInt(candidate.VersionID, 10))
	}
	contractMaterial := strings.Join(contractParts, "\x00")
	contractDigest := sha256.Sum256([]byte(contractMaterial))
	contractRoot := s.keyPrefix + hex.EncodeToString(contractDigest[:])
	tagPrefix := contractRoot + ":tag:"
	allowedTags := make(map[string]string, len(plan.Cache.Tags))
	for _, tag := range plan.Cache.Tags {
		digest := sha256.Sum256([]byte(tag))
		allowedTags[tag] = tagPrefix + hex.EncodeToString(digest[:])
	}
	prepared := preparedHostCache{
		request: request, plan: plan, providerReq: providerReq, providers: resolution, external: external,
		contractRoot: contractRoot, tagPrefix: tagPrefix, allowedTags: allowedTags,
		ownerLease: ownerLease, executionCtx: executionCtx,
	}
	releaseOwner = false
	return prepared, nil
}

func validateHostCacheCaller(caller HostCacheCaller, owner cacheregistry.Artifact) error {
	if owner.Core {
		if caller.Core && caller.Attested {
			return nil
		}
		return ErrHostCacheDenied
	}
	if caller.versionFromHostPlan && caller.VersionID == 0 {
		caller.VersionID = owner.VersionID
	}
	digest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(caller.ArtifactDigest)), "sha256:")
	if !caller.Attested || caller.Core || strings.TrimSpace(caller.ExtensionID) != owner.ExtensionID ||
		strings.TrimSpace(caller.ExtensionVersion) != owner.ExtensionVersion || digest != owner.PackageDigest ||
		strings.TrimSpace(caller.RuntimeInstanceID) != owner.RuntimeInstanceID || caller.VersionID != owner.VersionID {
		return ErrHostCacheDenied
	}
	return nil
}

func (s *HostCacheService) resolveProviders(
	ctx context.Context,
	request HostCacheProviderRequest,
) (HostCacheProviderResolution, bool, error) {
	core := HostCacheProviderResolution{
		Fallback:   HostCacheFallbackClosed,
		Candidates: []HostCacheProviderCandidate{{ProviderID: HostCacheCoreProviderID, Core: true, Backend: s.core}},
	}
	// Safe Mode is Host-owned and always bypasses executable provider code.
	if request.SafeMode || request.DeclaredProvider == "" || request.DeclaredProvider == HostCacheCoreProviderID {
		return core, false, nil
	}
	if s.resolver == nil {
		return HostCacheProviderResolution{}, false, ErrHostCacheProviderUnavailable
	}
	resolution, err := s.resolver.ResolveHostCacheProvider(ctx, request)
	if err != nil {
		return HostCacheProviderResolution{}, false, fmt.Errorf("%w: %v", ErrHostCacheProviderUnavailable, err)
	}
	if err := validateHostCacheProviderResolution(request, resolution); err != nil {
		return HostCacheProviderResolution{}, false, err
	}
	return cloneHostCacheProviderResolution(resolution), true, nil
}

func validateHostCacheProviderResolution(request HostCacheProviderRequest, resolution HostCacheProviderResolution) error {
	if resolution.Revision == 0 || len(resolution.Candidates) != 1 ||
		resolution.Fallback != HostCacheFallbackClosed ||
		resolution.Candidates[0].ProviderID != request.DeclaredProvider {
		return ErrHostCacheProviderInvalid
	}
	seen := make(map[string]struct{}, len(resolution.Candidates))
	for index, candidate := range resolution.Candidates {
		candidate.ProviderID = strings.TrimSpace(candidate.ProviderID)
		if candidate.ProviderID == "" || len(candidate.ProviderID) > 255 || candidate.Backend == nil {
			return ErrHostCacheProviderInvalid
		}
		if _, duplicate := seen[candidate.ProviderID]; duplicate {
			return ErrHostCacheProviderInvalid
		}
		seen[candidate.ProviderID] = struct{}{}
		if candidate.Core {
			if candidate.ProviderID != HostCacheCoreProviderID || index == 0 || candidate.ExtensionID != "" ||
				candidate.ExtensionVersion != "" || candidate.ArtifactDigest != "" || candidate.RuntimeInstance != "" {
				return ErrHostCacheProviderInvalid
			}
			continue
		}
		if !boundedHostCacheIdentity(candidate.ExtensionID, 255) ||
			!boundedHostCacheIdentity(candidate.ExtensionVersion, 128) ||
			!validHostCacheDigest(candidate.ArtifactDigest) || candidate.VersionID <= 0 ||
			!boundedHostCacheIdentity(candidate.RuntimeInstance, 512) {
			return ErrHostCacheProviderInvalid
		}
	}
	return nil
}

func (p preparedHostCache) valueKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return p.contractRoot + ":segment:" + p.plan.Isolation.SegmentDigest + ":value:" + hex.EncodeToString(digest[:])
}

func (p preparedHostCache) counterKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return p.contractRoot + ":segment:" + p.plan.Isolation.SegmentDigest + ":counter:" + hex.EncodeToString(digest[:])
}

func (p preparedHostCache) lockKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return p.contractRoot + ":segment:" + p.plan.Isolation.SegmentDigest + ":lock:" + hex.EncodeToString(digest[:])
}

func (p preparedHostCache) physicalTags(tags []string) ([]string, error) {
	if len(tags) == 0 || len(tags) > HostCacheMaximumTags {
		return nil, ErrHostCacheInvalid
	}
	// Tags are contract-wide on purpose: one declared entity event must evict all
	// actor/permission/locale variants. Only the exact cache owner can invoke
	// invalidation, and physical members remain installation/contract scoped.
	result := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		physical, declared := p.allowedTags[tag]
		if !declared {
			return nil, ErrHostCacheDenied
		}
		if _, duplicate := seen[tag]; duplicate {
			return nil, ErrHostCacheInvalid
		}
		seen[tag] = struct{}{}
		result = append(result, physical)
	}
	sort.Strings(result)
	return result, nil
}

func validateHostCacheKey(key string) error {
	if key == "" || len(key) > HostCacheMaximumKeyBytes || !utf8.ValidString(key) {
		return ErrHostCacheInvalid
	}
	for _, value := range key {
		if value < 0x20 || value == 0x7f {
			return ErrHostCacheInvalid
		}
	}
	return nil
}

func validateHostCacheSchema(schema HostCacheSchema) error {
	schema.ID = strings.TrimSpace(schema.ID)
	schema.Version = strings.TrimSpace(schema.Version)
	if !boundedHostCacheIdentity(schema.ID, hostCacheSchemaPartBytes) ||
		!boundedHostCacheIdentity(schema.Version, hostCacheSchemaPartBytes) {
		return ErrHostCacheInvalid
	}
	return nil
}

func validateHostCacheTTL(ttl time.Duration) error {
	if ttl < HostCacheMinimumTTL || ttl > HostCacheMaximumTTL {
		return ErrHostCacheInvalid
	}
	return nil
}

func validateHostCacheRevision(value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if len(value) != hostCacheRevisionBytes*2 {
		return ErrHostCacheInvalid
	}
	_, err := hex.DecodeString(value)
	if err != nil {
		return ErrHostCacheInvalid
	}
	return nil
}

func validateHostCacheStoredValue(prepared preparedHostCache, value HostCacheStoredValue, schema HostCacheSchema) error {
	if len(value.Value) == 0 || len(value.Value) > HostCacheMaximumValueBytes || value.SchemaID != schema.ID ||
		value.SchemaVersion != schema.Version || validateHostCacheRevision(value.Revision, false) != nil ||
		len(value.Tags) > HostCacheMaximumTags {
		return ErrHostCachePoisoned
	}
	allowed := make(map[string]struct{}, len(prepared.allowedTags))
	for _, physical := range prepared.allowedTags {
		allowed[physical] = struct{}{}
	}
	seen := make(map[string]struct{}, len(value.Tags))
	for _, tag := range value.Tags {
		if _, ok := allowed[tag]; !ok {
			return ErrHostCachePoisoned
		}
		if _, duplicate := seen[tag]; duplicate {
			return ErrHostCachePoisoned
		}
		seen[tag] = struct{}{}
	}
	return nil
}

func newHostCacheRevision() (string, error) {
	buffer := make([]byte, hostCacheRevisionBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func newHostCacheLockToken() (string, error) { return newHostCacheRevision() }

func boundedHostCacheIdentity(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validHostCacheDigest(value string) bool {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneHostCacheProviderResolution(value HostCacheProviderResolution) HostCacheProviderResolution {
	value.Candidates = slices.Clone(value.Candidates)
	return value
}

func cloneHostCacheStoredValue(value HostCacheStoredValue) HostCacheStoredValue {
	value.Value = slices.Clone(value.Value)
	value.Tags = slices.Clone(value.Tags)
	return value
}
