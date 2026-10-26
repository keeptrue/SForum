package hostapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestExactHostCacheProviderResolverBindsFullImmutableSelection(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "exact-provider.cache", cacheregistry.PolicyPublic, "provider.cache.remote")
	source := newExactHostCacheSelectionSource()
	remote := newExactHostCacheRemote()
	resolver, err := NewExactHostCacheProviderResolver(source, remote)
	if err != nil {
		t.Fatal(err)
	}
	request := exactHostCacheProviderRequest(fixture)
	resolution, err := resolver.ResolveHostCacheProvider(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Revision != source.revision || resolution.Fallback != HostCacheFallbackClosed ||
		len(resolution.Candidates) != 1 {
		t.Fatalf("resolution = %#v", resolution)
	}
	candidate := resolution.Candidates[0]
	if err := resolver.ValidateHostCacheProvider(context.Background(), request, resolution, candidate); err != nil {
		t.Fatalf("validate exact selection: %v", err)
	}

	stored := HostCacheStoredValue{
		Value: []byte(`{"provider":"exact"}`), SchemaID: "exact.value", SchemaVersion: "1",
		Revision: strings.Repeat("a", 64), Tags: []string{"physical-tag"},
	}
	if err := candidate.Backend.Set(context.Background(), "physical-key", stored, time.Minute, "", "tag-prefix"); err != nil {
		t.Fatal(err)
	}
	stored.Value[0] = 'x'
	stored.Tags[0] = "mutated"
	value, found, err := candidate.Backend.Get(context.Background(), "physical-key")
	if err != nil || !found || string(value.Value) != `{"provider":"exact"}` || value.Tags[0] != "physical-tag" {
		t.Fatalf("remote clone result=%#v found=%t err=%v", value, found, err)
	}
	call := remote.lastCall()
	want := source.selection(request).Binding
	if call.binding != want || call.operation != "get" || !call.hasDeadline {
		t.Fatalf("remote binding = %#v", call)
	}
	for name, mutate := range map[string]func(*HostCacheProviderSelection){
		"provider contract": func(value *HostCacheProviderSelection) { value.Binding.ProviderContract = "" },
		"cache contract":    func(value *HostCacheProviderSelection) { value.Binding.CacheContract += ".drift" },
		"cache owner":       func(value *HostCacheProviderSelection) { value.Binding.CacheOwner.VersionID++ },
		"call timeout":      func(value *HostCacheProviderSelection) { value.Binding.CallTimeout = 0 },
	} {
		t.Run("binding "+name, func(t *testing.T) {
			selection := source.selection(request)
			mutate(&selection)
			if err := validateExactHostCacheProviderSelection(request, selection); !errors.Is(err, ErrHostCacheProviderInvalid) {
				t.Fatalf("tampered binding passed: %v", err)
			}
		})
	}

	for name, mutate := range map[string]func(*HostCacheProviderResolution){
		"revision": func(value *HostCacheProviderResolution) { value.Revision++ },
		"artifact": func(value *HostCacheProviderResolution) {
			value.Candidates[0].ArtifactDigest = strings.Repeat("d", 64)
		},
		"version row": func(value *HostCacheProviderResolution) { value.Candidates[0].VersionID++ },
		"runtime":     func(value *HostCacheProviderResolution) { value.Candidates[0].RuntimeInstance = "replacement-runtime" },
		"backend":     func(value *HostCacheProviderResolution) { value.Candidates[0].Backend = newFakeHostCacheBackend() },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := cloneHostCacheProviderResolution(resolution)
			mutate(&tampered)
			if err := resolver.ValidateHostCacheProvider(
				context.Background(), request, tampered, tampered.Candidates[0],
			); !errors.Is(err, ErrHostCacheProviderInvalid) {
				t.Fatalf("tampered selection passed: %v", err)
			}
		})
	}

	source.mu.Lock()
	source.revision++
	source.mu.Unlock()
	if err := resolver.ValidateHostCacheProvider(context.Background(), request, resolution, candidate); !errors.Is(err, ErrHostCacheStale) {
		t.Fatalf("stale source selection = %v", err)
	}
}

func TestExactHostCacheProviderResolverFailsClosedWithoutCoreLeakage(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "closed-provider.cache", cacheregistry.PolicyPublic, "provider.cache.remote")
	source := newExactHostCacheSelectionSource()
	remote := newExactHostCacheRemote()
	remote.failure = errors.New("remote provider credential must stay private")
	resolver, err := NewExactHostCacheProviderResolver(source, remote)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, resolver, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "closed", Schema: fixture.schema,
		Value: []byte(`{"value":"remote"}`), TTL: time.Minute,
	})
	if !errors.Is(err, ErrHostCacheProviderUnavailable) || len(fixture.backend.observedKeys()) != 0 {
		t.Fatalf("provider failure err=%v core keys=%v", err, fixture.backend.observedKeys())
	}

	missingSource := newExactHostCacheSelectionSource()
	missingSource.resolveErr = errors.New("selected provider is absent")
	missingResolver, err := NewExactHostCacheProviderResolver(missingSource, newExactHostCacheRemote())
	if err != nil {
		t.Fatal(err)
	}
	missingService, err := NewHostCacheService(fixture.registry, fixture.backend, missingResolver, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = missingService.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "unknown", Schema: fixture.schema,
	})
	if !errors.Is(err, ErrHostCacheProviderUnavailable) || len(fixture.backend.observedKeys()) != 0 {
		t.Fatalf("unknown provider err=%v core keys=%v", err, fixture.backend.observedKeys())
	}
}

func TestExactHostCacheProviderBackendHonorsTimeoutAndRuntimeRevocation(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "revoke-provider.cache", cacheregistry.PolicyPublic, "provider.cache.remote")
	source := newExactHostCacheSelectionSource()
	source.timeout = 30 * time.Millisecond
	remote := newExactHostCacheRemote()
	remote.blockGet.Store(true)
	resolver, err := NewExactHostCacheProviderResolver(source, remote)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := resolver.ResolveHostCacheProvider(context.Background(), exactHostCacheProviderRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, _, err = resolution.Candidates[0].Backend.Get(context.Background(), "timeout")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("remote timeout err=%v elapsed=%v", err, time.Since(started))
	}

	remote.blockGet.Store(true)
	remote.resetStarted()
	admission := &cancelableHostCacheAdmission{}
	service, err := NewHostCacheService(
		fixture.registry, fixture.backend, resolver,
		WithHostCacheInstallationID("revoke-provider-installation"),
		WithHostCacheRuntimeAdmission(admission), WithHostCacheTraceSink(NewHostCacheInspector(16)),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, callErr := service.Get(context.Background(), HostCacheGetRequest{
			HostCacheRequestBase: fixture.base, Key: "revoke", Schema: fixture.schema,
		})
		result <- callErr
	}()
	<-remote.startedChannel()
	admission.cancelRuntime()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("revoked provider call = %v", err)
	}
	if len(fixture.backend.observedKeys()) != 0 {
		t.Fatalf("revoked provider leaked to Core: %v", fixture.backend.observedKeys())
	}
}

func TestExactHostCacheProviderResolverConcurrentSnapshots(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "race-provider.cache", cacheregistry.PolicyPublic, "provider.cache.remote")
	source := newExactHostCacheSelectionSource()
	remote := newExactHostCacheRemote()
	resolver, err := NewExactHostCacheProviderResolver(source, remote)
	if err != nil {
		t.Fatal(err)
	}
	request := exactHostCacheProviderRequest(fixture)
	var failures atomic.Int64
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for call := 0; call < 32; call++ {
				resolution, resolveErr := resolver.ResolveHostCacheProvider(context.Background(), request)
				if resolveErr != nil || len(resolution.Candidates) != 1 {
					failures.Add(1)
					return
				}
				candidate := resolution.Candidates[0]
				if validateErr := resolver.ValidateHostCacheProvider(context.Background(), request, resolution, candidate); validateErr != nil {
					failures.Add(1)
					return
				}
				if _, incrementErr := candidate.Backend.Increment(context.Background(), "counter", 1, time.Minute); incrementErr != nil {
					failures.Add(1)
					return
				}
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("concurrent resolver failures = %d", failures.Load())
	}
}

func exactHostCacheProviderRequest(fixture hostCacheTestFixture) HostCacheProviderRequest {
	return HostCacheProviderRequest{
		DeclaredProvider: fixture.cache.Provider, CacheID: fixture.cache.ID,
		ContractVersion: fixture.cache.ContractVersion, Owner: fixture.artifact,
	}
}

type exactHostCacheSelectionSource struct {
	mu         sync.Mutex
	revision   uint64
	timeout    time.Duration
	provider   HostCacheProviderArtifact
	resolveErr error
}

func newExactHostCacheSelectionSource() *exactHostCacheSelectionSource {
	return &exactHostCacheSelectionSource{
		revision: 17, timeout: 500 * time.Millisecond,
		provider: HostCacheProviderArtifact{
			ExtensionID: "cache.provider", ExtensionVersion: "2.1.0",
			ArtifactDigest: strings.Repeat("c", 64), VersionID: 41,
			RuntimeInstanceID: "cache-provider-runtime",
		},
	}
}

func (s *exactHostCacheSelectionSource) selection(request HostCacheProviderRequest) HostCacheProviderSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectionLocked(request)
}

func (s *exactHostCacheSelectionSource) selectionLocked(request HostCacheProviderRequest) HostCacheProviderSelection {
	return HostCacheProviderSelection{
		Binding: HostCacheProviderBinding{
			SelectionRevision: s.revision, ProviderID: request.DeclaredProvider,
			ProviderContract: "sforum.cache.provider@1", ProviderArtifact: s.provider,
			CacheID: request.CacheID, CacheContract: request.ContractVersion, CacheOwner: request.Owner,
			CallTimeout: s.timeout,
		},
		Fallback: HostCacheFallbackClosed,
	}
}

func (s *exactHostCacheSelectionSource) ResolveHostCacheProviderSelection(
	_ context.Context,
	request HostCacheProviderRequest,
) (HostCacheProviderSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolveErr != nil {
		return HostCacheProviderSelection{}, s.resolveErr
	}
	return s.selectionLocked(request), nil
}

func (s *exactHostCacheSelectionSource) ValidateHostCacheProviderSelection(
	_ context.Context,
	request HostCacheProviderRequest,
	selection HostCacheProviderSelection,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolveErr != nil || selection != s.selectionLocked(request) {
		return errors.New("selection is stale")
	}
	return nil
}

type exactHostCacheRemoteCall struct {
	operation   string
	binding     HostCacheProviderBinding
	hasDeadline bool
}

type exactHostCacheRemote struct {
	backend  *fakeHostCacheBackend
	failure  error
	mu       sync.Mutex
	calls    []exactHostCacheRemoteCall
	started  chan struct{}
	startOne sync.Once
	blockGet atomic.Bool
}

func newExactHostCacheRemote() *exactHostCacheRemote {
	return &exactHostCacheRemote{backend: newFakeHostCacheBackend(), started: make(chan struct{})}
}

func (r *exactHostCacheRemote) record(ctx context.Context, binding HostCacheProviderBinding, operation string) error {
	_, hasDeadline := ctx.Deadline()
	r.mu.Lock()
	r.calls = append(r.calls, exactHostCacheRemoteCall{operation: operation, binding: binding, hasDeadline: hasDeadline})
	failure := r.failure
	r.mu.Unlock()
	return failure
}

func (r *exactHostCacheRemote) lastCall() exactHostCacheRemoteCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func (r *exactHostCacheRemote) resetStarted() {
	r.mu.Lock()
	r.started = make(chan struct{})
	r.startOne = sync.Once{}
	r.mu.Unlock()
}

func (r *exactHostCacheRemote) startedChannel() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

func (r *exactHostCacheRemote) waitIfBlocked(ctx context.Context) error {
	if !r.blockGet.Load() {
		return nil
	}
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	r.startOne.Do(func() { close(started) })
	<-ctx.Done()
	return context.Cause(ctx)
}

func (r *exactHostCacheRemote) Get(
	ctx context.Context, binding HostCacheProviderBinding, key string,
) (HostCacheStoredValue, bool, error) {
	if err := r.record(ctx, binding, "get"); err != nil {
		return HostCacheStoredValue{}, false, err
	}
	if err := r.waitIfBlocked(ctx); err != nil {
		return HostCacheStoredValue{}, false, err
	}
	return r.backend.Get(ctx, key)
}

func (r *exactHostCacheRemote) Set(
	ctx context.Context, binding HostCacheProviderBinding, key string, value HostCacheStoredValue,
	ttl time.Duration, expectedRevision, tagPrefix string,
) error {
	if err := r.record(ctx, binding, "set"); err != nil {
		return err
	}
	return r.backend.Set(ctx, key, value, ttl, expectedRevision, tagPrefix)
}

func (r *exactHostCacheRemote) Delete(
	ctx context.Context, binding HostCacheProviderBinding, key, tagPrefix string,
) (bool, error) {
	if err := r.record(ctx, binding, "delete"); err != nil {
		return false, err
	}
	return r.backend.Delete(ctx, key, tagPrefix)
}

func (r *exactHostCacheRemote) InvalidateTags(
	ctx context.Context, binding HostCacheProviderBinding, tags []string, tagPrefix string,
) (uint64, error) {
	if err := r.record(ctx, binding, "invalidate_tags"); err != nil {
		return 0, err
	}
	return r.backend.InvalidateTags(ctx, slices.Clone(tags), tagPrefix)
}

func (r *exactHostCacheRemote) Increment(
	ctx context.Context, binding HostCacheProviderBinding, key string, delta int64, ttl time.Duration,
) (int64, error) {
	if err := r.record(ctx, binding, "increment"); err != nil {
		return 0, err
	}
	return r.backend.Increment(ctx, key, delta, ttl)
}

func (r *exactHostCacheRemote) AcquireLock(
	ctx context.Context, binding HostCacheProviderBinding, key, owner string, ttl time.Duration,
) (bool, error) {
	if err := r.record(ctx, binding, "lock_acquire"); err != nil {
		return false, err
	}
	return r.backend.AcquireLock(ctx, key, owner, ttl)
}

func (r *exactHostCacheRemote) RenewLock(
	ctx context.Context, binding HostCacheProviderBinding, key, owner string, ttl time.Duration,
) (bool, error) {
	if err := r.record(ctx, binding, "lock_renew"); err != nil {
		return false, err
	}
	return r.backend.RenewLock(ctx, key, owner, ttl)
}

func (r *exactHostCacheRemote) ReleaseLock(
	ctx context.Context, binding HostCacheProviderBinding, key, owner string,
) (bool, error) {
	if err := r.record(ctx, binding, "lock_release"); err != nil {
		return false, err
	}
	return r.backend.ReleaseLock(ctx, key, owner)
}

func (r *exactHostCacheRemote) SetAndReleaseLock(
	ctx context.Context,
	binding HostCacheProviderBinding,
	key string,
	value HostCacheStoredValue,
	ttl time.Duration,
	expectedRevision string,
	tagPrefix string,
	lockKey string,
	owner string,
) error {
	if err := r.record(ctx, binding, "set_and_release_lock"); err != nil {
		return err
	}
	return r.backend.SetAndReleaseLock(ctx, key, value, ttl, expectedRevision, tagPrefix, lockKey, owner)
}

var _ HostCacheProviderSelectionSource = (*exactHostCacheSelectionSource)(nil)
var _ HostCacheProviderRemote = (*exactHostCacheRemote)(nil)
