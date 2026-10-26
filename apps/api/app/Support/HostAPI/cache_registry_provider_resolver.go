package hostapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

const HostCacheProviderMaximumCallTimeout = 5 * time.Second

// HostCacheProviderArtifact is the exact executable artifact selected for one
// cache provider. VersionID keeps the durable extension-version row in the
// identity; RuntimeInstanceID keeps one physical process in the identity.
type HostCacheProviderArtifact struct {
	ExtensionID       string
	ExtensionVersion  string
	ArtifactDigest    string
	VersionID         int64
	RuntimeInstanceID string
}

// HostCacheProviderBinding is immutable input to every remote backend call.
// The remote transport must not resolve a provider again: doing so could send a
// mutation to a newer process after the Host admitted the selected process.
type HostCacheProviderBinding struct {
	SelectionRevision uint64
	ProviderID        string
	ProviderContract  string
	ProviderArtifact  HostCacheProviderArtifact
	CacheID           string
	CacheContract     string
	CacheOwner        cacheregistry.Artifact
	CallTimeout       time.Duration
}

type HostCacheProviderSelection struct {
	Binding  HostCacheProviderBinding
	Fallback string
}

// HostCacheProviderSelectionSource adapts the durable provider selection and
// immutable runtime catalog. Validate must compare the full selection,
// including both artifact identities and the source revision.
type HostCacheProviderSelectionSource interface {
	ResolveHostCacheProviderSelection(context.Context, HostCacheProviderRequest) (HostCacheProviderSelection, error)
	ValidateHostCacheProviderSelection(context.Context, HostCacheProviderRequest, HostCacheProviderSelection) error
}

// HostCacheProviderRemote is the semantic boundary for an exact Protocol V2
// cache provider. No implementation is supplied until the cache.provider wire
// schemas are frozen. Implementations must honor ctx cancellation and must use
// binding as-is instead of entering provider discovery again.
type HostCacheProviderRemote interface {
	Get(context.Context, HostCacheProviderBinding, string) (HostCacheStoredValue, bool, error)
	Set(context.Context, HostCacheProviderBinding, string, HostCacheStoredValue, time.Duration, string, string) error
	Delete(context.Context, HostCacheProviderBinding, string, string) (bool, error)
	InvalidateTags(context.Context, HostCacheProviderBinding, []string, string) (uint64, error)
	Increment(context.Context, HostCacheProviderBinding, string, int64, time.Duration) (int64, error)
	AcquireLock(context.Context, HostCacheProviderBinding, string, string, time.Duration) (bool, error)
	RenewLock(context.Context, HostCacheProviderBinding, string, string, time.Duration) (bool, error)
	ReleaseLock(context.Context, HostCacheProviderBinding, string, string) (bool, error)
	SetAndReleaseLock(context.Context, HostCacheProviderBinding, string, HostCacheStoredValue, time.Duration, string, string, string, string) error
}

// ExactHostCacheProviderResolver converts one immutable provider selection into
// a backend that can invoke only that exact selection. HostCacheService still
// owns Safe Mode, Core fallback, and both owner/provider admission leases.
type ExactHostCacheProviderResolver struct {
	source HostCacheProviderSelectionSource
	remote HostCacheProviderRemote
	token  *hostCacheProviderResolverToken
}

type hostCacheProviderResolverToken struct{ marker byte }

func NewExactHostCacheProviderResolver(
	source HostCacheProviderSelectionSource,
	remote HostCacheProviderRemote,
) (*ExactHostCacheProviderResolver, error) {
	if source == nil || remote == nil {
		return nil, ErrHostCacheProviderInvalid
	}
	return &ExactHostCacheProviderResolver{source: source, remote: remote, token: &hostCacheProviderResolverToken{}}, nil
}

func (r *ExactHostCacheProviderResolver) ResolveHostCacheProvider(
	ctx context.Context,
	request HostCacheProviderRequest,
) (HostCacheProviderResolution, error) {
	if r == nil || r.source == nil || r.remote == nil || r.token == nil || ctx == nil ||
		request.SafeMode || strings.TrimSpace(request.DeclaredProvider) == "" ||
		request.DeclaredProvider == HostCacheCoreProviderID {
		return HostCacheProviderResolution{}, ErrHostCacheProviderInvalid
	}
	if err := ctx.Err(); err != nil {
		return HostCacheProviderResolution{}, context.Cause(ctx)
	}
	selection, err := r.source.ResolveHostCacheProviderSelection(ctx, request)
	if err != nil {
		return HostCacheProviderResolution{}, err
	}
	if err := validateExactHostCacheProviderSelection(request, selection); err != nil {
		return HostCacheProviderResolution{}, err
	}
	backend := &exactHostCacheProviderBackend{
		binding: selection.Binding,
		remote:  r.remote,
		token:   r.token,
	}
	artifact := selection.Binding.ProviderArtifact
	return HostCacheProviderResolution{
		Revision: selection.Binding.SelectionRevision,
		Fallback: HostCacheFallbackClosed,
		Candidates: []HostCacheProviderCandidate{{
			ProviderID:  selection.Binding.ProviderID,
			ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
			ArtifactDigest: artifact.ArtifactDigest, VersionID: artifact.VersionID,
			RuntimeInstance: artifact.RuntimeInstanceID, Backend: backend,
		}},
	}, nil
}

func (r *ExactHostCacheProviderResolver) ValidateHostCacheProvider(
	ctx context.Context,
	request HostCacheProviderRequest,
	resolution HostCacheProviderResolution,
	candidate HostCacheProviderCandidate,
) error {
	if r == nil || r.source == nil || r.remote == nil || r.token == nil || ctx == nil {
		return ErrHostCacheProviderInvalid
	}
	if err := validateHostCacheProviderResolution(request, resolution); err != nil {
		return err
	}
	if len(resolution.Candidates) != 1 || !sameHostCacheProviderCandidate(resolution.Candidates[0], candidate) {
		return ErrHostCacheProviderInvalid
	}
	backend, ok := candidate.Backend.(*exactHostCacheProviderBackend)
	if !ok || backend == nil || backend.token != r.token || candidate.Backend != resolution.Candidates[0].Backend {
		return ErrHostCacheProviderInvalid
	}
	selection := HostCacheProviderSelection{Binding: backend.binding, Fallback: resolution.Fallback}
	if err := validateExactHostCacheProviderSelection(request, selection); err != nil ||
		resolution.Revision != selection.Binding.SelectionRevision ||
		!candidateMatchesHostCacheProviderBinding(candidate, selection.Binding) {
		return ErrHostCacheProviderInvalid
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := r.source.ValidateHostCacheProviderSelection(ctx, request, selection); err != nil {
		return fmt.Errorf("%w: exact provider selection: %v", ErrHostCacheStale, err)
	}
	return nil
}

func validateExactHostCacheProviderSelection(
	request HostCacheProviderRequest,
	selection HostCacheProviderSelection,
) error {
	binding := selection.Binding
	artifact := binding.ProviderArtifact
	canonicalDigest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(artifact.ArtifactDigest)), "sha256:")
	if binding.SelectionRevision == 0 || selection.Fallback != HostCacheFallbackClosed ||
		binding.ProviderID != request.DeclaredProvider || strings.TrimSpace(binding.ProviderID) != binding.ProviderID ||
		!boundedHostCacheIdentity(binding.ProviderContract, 128) ||
		strings.TrimSpace(binding.ProviderContract) != binding.ProviderContract ||
		binding.CacheID != request.CacheID || binding.CacheContract != request.ContractVersion ||
		binding.CacheOwner != request.Owner || binding.CallTimeout < HostCacheMinimumTTL ||
		binding.CallTimeout > HostCacheProviderMaximumCallTimeout ||
		!boundedHostCacheIdentity(artifact.ExtensionID, 255) || strings.TrimSpace(artifact.ExtensionID) != artifact.ExtensionID ||
		!boundedHostCacheIdentity(artifact.ExtensionVersion, 128) || strings.TrimSpace(artifact.ExtensionVersion) != artifact.ExtensionVersion ||
		!validHostCacheDigest(artifact.ArtifactDigest) || artifact.ArtifactDigest != canonicalDigest || artifact.VersionID <= 0 ||
		!boundedHostCacheIdentity(artifact.RuntimeInstanceID, 512) ||
		strings.TrimSpace(artifact.RuntimeInstanceID) != artifact.RuntimeInstanceID {
		return ErrHostCacheProviderInvalid
	}
	return nil
}

func candidateMatchesHostCacheProviderBinding(candidate HostCacheProviderCandidate, binding HostCacheProviderBinding) bool {
	artifact := binding.ProviderArtifact
	return !candidate.Core && candidate.ProviderID == binding.ProviderID &&
		candidate.ExtensionID == artifact.ExtensionID && candidate.ExtensionVersion == artifact.ExtensionVersion &&
		candidate.ArtifactDigest == artifact.ArtifactDigest && candidate.VersionID == artifact.VersionID &&
		candidate.RuntimeInstance == artifact.RuntimeInstanceID
}

func sameHostCacheProviderCandidate(left, right HostCacheProviderCandidate) bool {
	return left.ProviderID == right.ProviderID && left.ExtensionID == right.ExtensionID &&
		left.ExtensionVersion == right.ExtensionVersion && left.ArtifactDigest == right.ArtifactDigest &&
		left.VersionID == right.VersionID && left.RuntimeInstance == right.RuntimeInstance &&
		left.Core == right.Core && left.Backend == right.Backend
}

type exactHostCacheProviderBackend struct {
	binding HostCacheProviderBinding
	remote  HostCacheProviderRemote
	token   *hostCacheProviderResolverToken
}

func (b *exactHostCacheProviderBackend) operationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if b == nil || b.remote == nil || b.token == nil || ctx == nil ||
		b.binding.CallTimeout < HostCacheMinimumTTL || b.binding.CallTimeout > HostCacheProviderMaximumCallTimeout {
		return nil, nil, ErrHostCacheProviderInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, context.Cause(ctx)
	}
	callCtx, cancel := context.WithTimeout(ctx, b.binding.CallTimeout)
	return callCtx, cancel, nil
}

func (b *exactHostCacheProviderBackend) Get(ctx context.Context, key string) (HostCacheStoredValue, bool, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return HostCacheStoredValue{}, false, err
	}
	defer cancel()
	value, found, err := b.remote.Get(callCtx, b.binding, key)
	return cloneHostCacheStoredValue(value), found, err
}

func (b *exactHostCacheProviderBackend) Set(
	ctx context.Context, key string, value HostCacheStoredValue, ttl time.Duration, expectedRevision, tagPrefix string,
) error {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return b.remote.Set(callCtx, b.binding, key, cloneHostCacheStoredValue(value), ttl, expectedRevision, tagPrefix)
}

func (b *exactHostCacheProviderBackend) Delete(ctx context.Context, key, tagPrefix string) (bool, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	return b.remote.Delete(callCtx, b.binding, key, tagPrefix)
}

func (b *exactHostCacheProviderBackend) InvalidateTags(
	ctx context.Context, tags []string, tagPrefix string,
) (uint64, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	return b.remote.InvalidateTags(callCtx, b.binding, slices.Clone(tags), tagPrefix)
}

func (b *exactHostCacheProviderBackend) Increment(
	ctx context.Context, key string, delta int64, ttl time.Duration,
) (int64, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	return b.remote.Increment(callCtx, b.binding, key, delta, ttl)
}

func (b *exactHostCacheProviderBackend) AcquireLock(
	ctx context.Context, key, owner string, ttl time.Duration,
) (bool, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	return b.remote.AcquireLock(callCtx, b.binding, key, owner, ttl)
}

func (b *exactHostCacheProviderBackend) RenewLock(
	ctx context.Context, key, owner string, ttl time.Duration,
) (bool, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	return b.remote.RenewLock(callCtx, b.binding, key, owner, ttl)
}

func (b *exactHostCacheProviderBackend) ReleaseLock(ctx context.Context, key, owner string) (bool, error) {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	return b.remote.ReleaseLock(callCtx, b.binding, key, owner)
}

func (b *exactHostCacheProviderBackend) SetAndReleaseLock(
	ctx context.Context,
	key string,
	value HostCacheStoredValue,
	ttl time.Duration,
	expectedRevision string,
	tagPrefix string,
	lockKey string,
	owner string,
) error {
	callCtx, cancel, err := b.operationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return b.remote.SetAndReleaseLock(
		callCtx, b.binding, key, cloneHostCacheStoredValue(value), ttl,
		expectedRevision, tagPrefix, lockKey, owner,
	)
}

var _ HostCacheProviderResolver = (*ExactHostCacheProviderResolver)(nil)
var _ HostCacheBackend = (*exactHostCacheProviderBackend)(nil)
