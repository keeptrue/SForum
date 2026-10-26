package hostapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

type hostCacheTestFixture struct {
	registry *cacheregistry.Registry
	backend  *fakeHostCacheBackend
	service  *HostCacheService
	artifact cacheregistry.Artifact
	cache    cacheregistry.Declaration
	caller   HostCacheCaller
	admitted *atomic.Bool
	base     HostCacheRequestBase
	schema   HostCacheSchema
}

type trackingHostCacheAdmission struct {
	active atomic.Int64
	seen   atomic.Int64
}

func (a *trackingHostCacheAdmission) AcquireServiceProvider(
	ctx context.Context,
	_ ServiceProviderIdentity,
) (ServiceProviderAdmissionLease, error) {
	a.active.Add(1)
	a.seen.Add(1)
	return &trackingHostCacheLease{ctx: ctx, admission: a}, nil
}

type trackingHostCacheLease struct {
	ctx       context.Context
	admission *trackingHostCacheAdmission
	once      sync.Once
}

func (l *trackingHostCacheLease) Context() context.Context { return l.ctx }
func (l *trackingHostCacheLease) Release() {
	l.once.Do(func() { l.admission.active.Add(-1) })
}

type delayedHostCacheTraceSink struct {
	delay time.Duration
}

func (s delayedHostCacheTraceSink) RecordHostCacheTrace(trace HostCacheTrace) {
	if trace.Operation == "lock_acquire" {
		time.Sleep(s.delay)
	}
}

func newHostCacheTestFixture(t *testing.T, extensionID, policy, provider string) hostCacheTestFixture {
	t.Helper()
	artifact := cacheregistry.Artifact{
		ExtensionID: extensionID, ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 7, RuntimeInstanceID: "runtime-" + strings.ReplaceAll(extensionID, ".", "-"),
	}
	declaration := cacheregistry.Declaration{
		ID: extensionID + ".records", ContractVersion: extensionID + ".records@1",
		Namespace: extensionID + ".records.namespace", Policy: policy, Provider: provider,
		Tags:         []string{extensionID + ".tag.records", extensionID + ".tag.summary"},
		Invalidators: []string{extensionID + ".invalidate.records"},
	}
	admitted := &atomic.Bool{}
	admitted.Store(true)
	registry := cacheregistry.New().WithPluginAdmission(func(candidate cacheregistry.Artifact) bool {
		return admitted.Load() && candidate == artifact
	})
	if _, err := registry.Publish(cacheregistry.Publication{Artifact: artifact, Caches: []cacheregistry.Declaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	backend := newFakeHostCacheBackend()
	service, err := NewHostCacheService(registry, backend, nil, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	caller := HostCacheCaller{
		ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
		ArtifactDigest: artifact.PackageDigest, VersionID: artifact.VersionID,
		RuntimeInstanceID: artifact.RuntimeInstanceID, Attested: true,
	}
	return hostCacheTestFixture{
		registry: registry, backend: backend, service: service, artifact: artifact, cache: declaration,
		caller: caller, admitted: admitted,
		base:   HostCacheRequestBase{Caller: caller, CacheID: declaration.ID, Scope: HostCacheScope{Locale: "zh-CN"}},
		schema: HostCacheSchema{ID: extensionID + ".value", Version: "1"},
	}
}

func TestHostCacheServiceCRUDCASIncrementTagsAndRemember(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "service.cache", cacheregistry.PolicyPublic, "")
	ctx := context.Background()
	set := HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "post:42", Schema: fixture.schema,
		Value: []byte(`{"title":"first"}`), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
	}
	revision, err := fixture.service.Set(ctx, set)
	if err != nil || revision == "" {
		t.Fatalf("set revision=%q err=%v", revision, err)
	}
	result, err := fixture.service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: set.Key, Schema: fixture.schema,
	})
	if err != nil || !result.Found || string(result.Value) != string(set.Value) || result.Revision != revision {
		t.Fatalf("get result=%#v err=%v", result, err)
	}

	stale := set
	stale.Value = []byte(`{"title":"stale"}`)
	stale.ExpectedRevision = strings.Repeat("b", 64)
	if _, err := fixture.service.Set(ctx, stale); !errors.Is(err, ErrHostCacheConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	set.ExpectedRevision = revision
	set.Value = []byte(`{"title":"updated"}`)
	if revision, err = fixture.service.Set(ctx, set); err != nil || revision == "" {
		t.Fatalf("exact CAS revision=%q err=%v", revision, err)
	}

	for index, expected := range []int64{1, 2, 3} {
		value, incrementErr := fixture.service.Increment(ctx, HostCacheIncrementRequest{
			HostCacheRequestBase: fixture.base, Key: "views", TTL: time.Minute,
		})
		if incrementErr != nil || value != expected {
			t.Fatalf("increment %d value=%d err=%v", index, value, incrementErr)
		}
	}
	invalidated, err := fixture.service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: fixture.base, Tags: []string{fixture.cache.Tags[0]},
	})
	if err != nil || invalidated != 1 {
		t.Fatalf("invalidate count=%d err=%v", invalidated, err)
	}
	if result, err = fixture.service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: set.Key, Schema: fixture.schema,
	}); err != nil || result.Found {
		t.Fatalf("invalidated get=%#v err=%v", result, err)
	}

	var loads atomic.Int64
	remember := HostCacheRememberRequest{
		HostCacheSetRequest: HostCacheSetRequest{
			HostCacheRequestBase: fixture.base, Key: "remembered", Schema: fixture.schema,
			TTL: time.Minute, Tags: []string{fixture.cache.Tags[1]},
		},
		LockTTL: time.Second, Wait: time.Second,
		Load: func(context.Context) ([]byte, error) {
			loads.Add(1)
			time.Sleep(20 * time.Millisecond)
			return []byte(`{"loaded":true}`), nil
		},
	}
	const workers = 16
	var group sync.WaitGroup
	errorsCh := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			value, rememberErr := fixture.service.Remember(ctx, remember)
			if rememberErr != nil || !value.Found || string(value.Value) != `{"loaded":true}` {
				errorsCh <- fmt.Errorf("remember=%#v err=%v", value, rememberErr)
			}
		}()
	}
	group.Wait()
	close(errorsCh)
	for rememberErr := range errorsCh {
		t.Error(rememberErr)
	}
	if loads.Load() != 1 {
		t.Fatalf("remember loader calls = %d", loads.Load())
	}

	deleted, err := fixture.service.Delete(ctx, HostCacheDeleteRequest{HostCacheRequestBase: fixture.base, Key: "remembered"})
	if err != nil || !deleted {
		t.Fatalf("delete=%t err=%v", deleted, err)
	}
}

func TestHostCacheServiceIsolatesActorLocaleNamespaceAndTags(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "isolate.cache", cacheregistry.PolicyActor, "")
	ctx := context.Background()
	write := func(actor, locale, value string) {
		t.Helper()
		base := fixture.base
		base.Scope = HostCacheScope{ActorFingerprint: actor, Locale: locale}
		if _, err := fixture.service.Set(ctx, HostCacheSetRequest{
			HostCacheRequestBase: base, Key: "feed", Schema: fixture.schema,
			Value: []byte(value), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(actor, locale string) string {
		t.Helper()
		base := fixture.base
		base.Scope = HostCacheScope{ActorFingerprint: actor, Locale: locale}
		result, err := fixture.service.Get(ctx, HostCacheGetRequest{
			HostCacheRequestBase: base, Key: "feed", Schema: fixture.schema,
		})
		if err != nil || !result.Found {
			t.Fatalf("read actor=%s locale=%s result=%#v err=%v", actor, locale, result, err)
		}
		return string(result.Value)
	}
	write("actor-1", "zh-CN", "actor-1-zh")
	write("actor-2", "zh-CN", "actor-2-zh")
	write("actor-1", "en-US", "actor-1-en")
	if read("actor-1", "zh-CN") != "actor-1-zh" || read("actor-2", "zh-CN") != "actor-2-zh" ||
		read("actor-1", "en-US") != "actor-1-en" {
		t.Fatal("actor/locale cache segments collided")
	}
	invalidatorBase := fixture.base
	invalidatorBase.Scope = HostCacheScope{ActorFingerprint: "actor-1", Locale: "zh-CN"}
	if count, err := fixture.service.InvalidateTags(context.Background(), HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: invalidatorBase, Tags: []string{fixture.cache.Tags[0]},
	}); err != nil || count != 3 {
		t.Fatalf("contract-wide actor variant invalidation count=%d err=%v", count, err)
	}
	for _, scope := range []HostCacheScope{
		{ActorFingerprint: "actor-1", Locale: "zh-CN"},
		{ActorFingerprint: "actor-2", Locale: "zh-CN"},
		{ActorFingerprint: "actor-1", Locale: "en-US"},
	} {
		base := fixture.base
		base.Scope = scope
		result, err := fixture.service.Get(context.Background(), HostCacheGetRequest{
			HostCacheRequestBase: base, Key: "feed", Schema: fixture.schema,
		})
		if err != nil || result.Found {
			t.Fatalf("tag retained actor variant scope=%#v result=%#v err=%v", scope, result, err)
		}
	}
	if _, err := fixture.service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: HostCacheRequestBase{Caller: fixture.caller, CacheID: fixture.cache.ID},
		Key:                  "feed", Schema: fixture.schema,
	}); !errors.Is(err, ErrHostCacheScopeRequired) {
		t.Fatalf("missing actor scope = %v", err)
	}
	tagBase := fixture.base
	tagBase.Scope = HostCacheScope{ActorFingerprint: "actor-1", Locale: "zh-CN"}
	if _, err := fixture.service.Set(ctx, HostCacheSetRequest{
		HostCacheRequestBase: tagBase, Key: "tag-escape", Schema: fixture.schema,
		Value: []byte("x"), TTL: time.Minute, Tags: []string{"other.cache.tag"},
	}); !errors.Is(err, ErrHostCacheDenied) {
		t.Fatalf("undeclared tag = %v", err)
	}
	for _, key := range fixture.backend.observedKeys() {
		if strings.Contains(key, "feed") || strings.Contains(key, "actor-1") {
			t.Fatalf("physical key exposed caller input: %q", key)
		}
	}
}

func TestHostCacheServicePreventsCrossExtensionAccessAndInvalidation(t *testing.T) {
	first := newHostCacheTestFixture(t, "first.cache", cacheregistry.PolicyPublic, "")
	secondArtifact := cacheregistry.Artifact{
		ExtensionID: "second.cache", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("b", 64),
		VersionID: 8, RuntimeInstanceID: "runtime-second-cache",
	}
	secondCache := cacheregistry.Declaration{
		ID: "second.cache.records", ContractVersion: "second.cache.records@1",
		Namespace: "second.cache.records.namespace", Policy: cacheregistry.PolicyPublic,
		Tags: []string{"second.cache.tag.records"}, Invalidators: []string{"second.cache.invalidate.records"},
	}
	first.registry.WithPluginAdmission(func(candidate cacheregistry.Artifact) bool {
		return candidate == first.artifact || candidate == secondArtifact
	})
	if _, err := first.registry.Publish(cacheregistry.Publication{Artifact: secondArtifact, Caches: []cacheregistry.Declaration{secondCache}}); err != nil {
		t.Fatal(err)
	}
	secondCaller := HostCacheCaller{
		ExtensionID: secondArtifact.ExtensionID, ExtensionVersion: secondArtifact.ExtensionVersion,
		ArtifactDigest: secondArtifact.PackageDigest, VersionID: secondArtifact.VersionID,
		RuntimeInstanceID: secondArtifact.RuntimeInstanceID, Attested: true,
	}
	secondBase := HostCacheRequestBase{Caller: secondCaller, CacheID: secondCache.ID, Scope: HostCacheScope{Locale: "zh-CN"}}
	for _, request := range []HostCacheSetRequest{
		{HostCacheRequestBase: first.base, Key: "same", Schema: first.schema, Value: []byte("first"), TTL: time.Minute, Tags: []string{first.cache.Tags[0]}},
		{HostCacheRequestBase: secondBase, Key: "same", Schema: first.schema, Value: []byte("second"), TTL: time.Minute, Tags: []string{secondCache.Tags[0]}},
	} {
		if _, err := first.service.Set(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: HostCacheRequestBase{Caller: first.caller, CacheID: secondCache.ID, Scope: HostCacheScope{Locale: "zh-CN"}},
		Key:                  "same", Schema: first.schema,
	}); !errors.Is(err, ErrHostCacheDenied) {
		t.Fatalf("cross-extension read = %v", err)
	}
	if count, err := first.service.InvalidateTags(context.Background(), HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: first.base, Tags: []string{first.cache.Tags[0]},
	}); err != nil || count != 1 {
		t.Fatalf("first invalidation count=%d err=%v", count, err)
	}
	result, err := first.service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: secondBase, Key: "same", Schema: first.schema,
	})
	if err != nil || !result.Found || string(result.Value) != "second" {
		t.Fatalf("cross-extension invalidation reached second=%#v err=%v", result, err)
	}
}

func TestHostCacheServiceLockExpiryAndOwnership(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "lock.cache", cacheregistry.PolicyPublic, "")
	request := HostCacheLockRequest{HostCacheRequestBase: fixture.base, Key: "rebuild", TTL: 120 * time.Millisecond}
	first, acquired, err := fixture.service.AcquireLock(context.Background(), request)
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err = fixture.service.AcquireLock(context.Background(), request); err != nil || acquired {
		t.Fatalf("contended lock acquired=%t err=%v", acquired, err)
	}
	time.Sleep(150 * time.Millisecond)
	second, acquired, err := fixture.service.AcquireLock(context.Background(), request)
	if err != nil || !acquired {
		t.Fatalf("replacement lock acquired=%t err=%v", acquired, err)
	}
	if err := first.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
		t.Fatalf("expired owner release = %v", err)
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatalf("current owner release = %v", err)
	}
	if err := second.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
		t.Fatalf("duplicate release = %v", err)
	}
}

func TestHostCacheLockUsesConservativeBackendLifetime(t *testing.T) {
	t.Run("acquire_subtracts_response_latency", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "lock-latency.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			acquireDelay:         100 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "delayed", TTL: 300 * time.Millisecond,
		})
		if err != nil || !acquired || lock == nil {
			t.Fatalf("delayed acquire lock=%#v acquired=%t err=%v", lock, acquired, err)
		}
		if remaining := time.Until(lock.expiresAt); remaining <= 0 || remaining >= 250*time.Millisecond {
			t.Fatalf("local remaining lifetime did not subtract response latency: %s", remaining)
		}
		if err := lock.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acquire_without_remaining_lifetime_cleans_token", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "lock-no-lifetime.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			acquireDelay:         130 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "expired-response", TTL: 100 * time.Millisecond,
		})
		if !errors.Is(err, ErrHostCacheLockNotOwned) || acquired || lock != nil {
			t.Fatalf("expired response lock=%#v acquired=%t err=%v", lock, acquired, err)
		}
		if backend.observedReleaseCalls() != 1 {
			t.Fatalf("expired response exact-token cleanup calls=%d", backend.observedReleaseCalls())
		}
		backend.fakeHostCacheBackend.mu.Lock()
		defer backend.fakeHostCacheBackend.mu.Unlock()
		if len(backend.locks) != 0 {
			t.Fatalf("expired response retained locks=%#v", backend.locks)
		}
	})

	t.Run("acquire_rechecks_lifetime_after_mandatory_trace", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "lock-trace-latency.cache", cacheregistry.PolicyPublic, "")
		service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
			hostCacheTestServiceOptions(WithHostCacheTraceSink(delayedHostCacheTraceSink{delay: 130 * time.Millisecond}))...)
		if err != nil {
			t.Fatal(err)
		}
		lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "delayed-audit", TTL: 100 * time.Millisecond,
		})
		if !errors.Is(err, ErrHostCacheLockNotOwned) || acquired || lock != nil {
			t.Fatalf("trace-expired lock=%#v acquired=%t err=%v", lock, acquired, err)
		}
		fixture.backend.mu.Lock()
		defer fixture.backend.mu.Unlock()
		if len(fixture.backend.locks) != 0 {
			t.Fatalf("trace-expired acquire retained locks=%#v", fixture.backend.locks)
		}
	})

	t.Run("acquire_canceled_after_apply_cleans_token", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "lock-canceled-response.cache", cacheregistry.PolicyPublic, "")
		applied := make(chan struct{})
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			acquireDelay:         time.Second,
			acquireApplied:       applied,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, _, acquireErr := service.AcquireLock(ctx, HostCacheLockRequest{
				HostCacheRequestBase: fixture.base, Key: "canceled-response", TTL: time.Second,
			})
			result <- acquireErr
		}()
		select {
		case <-applied:
		case <-time.After(time.Second):
			t.Fatal("backend did not apply lock")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled acquire=%v", err)
		}
		if backend.observedReleaseCalls() != 1 {
			t.Fatalf("canceled acquire cleanup calls=%d", backend.observedReleaseCalls())
		}
		backend.fakeHostCacheBackend.mu.Lock()
		defer backend.fakeHostCacheBackend.mu.Unlock()
		if len(backend.locks) != 0 {
			t.Fatalf("canceled acquire retained locks=%#v", backend.locks)
		}
	})

	t.Run("renew_subtracts_response_latency", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "renew-latency.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			renewDelay:           100 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "renew-delayed", TTL: time.Second,
		})
		if err != nil || !acquired || lock == nil {
			t.Fatalf("renew setup lock=%#v acquired=%t err=%v", lock, acquired, err)
		}
		if err := lock.Renew(context.Background(), 300*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if remaining := time.Until(lock.expiresAt); remaining <= 0 || remaining >= 250*time.Millisecond {
			t.Fatalf("renewed local lifetime did not subtract response latency: %s", remaining)
		}
		if err := lock.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("renew_canceled_after_apply_cleans_token", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "renew-canceled-response.cache", cacheregistry.PolicyPublic, "")
		applied := make(chan struct{})
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			renewDelay:           time.Second,
			renewApplied:         applied,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "renew-canceled", TTL: 2 * time.Second,
		})
		if err != nil || !acquired {
			t.Fatalf("setup acquired=%t err=%v", acquired, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- lock.Renew(ctx, 2*time.Second) }()
		select {
		case <-applied:
		case <-time.After(time.Second):
			t.Fatal("backend did not apply renewal")
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled renewal=%v", err)
		}
		backend.fakeHostCacheBackend.mu.Lock()
		if len(backend.locks) != 0 {
			t.Fatalf("canceled renewal retained locks=%#v", backend.locks)
		}
		backend.fakeHostCacheBackend.mu.Unlock()
		if err := lock.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
			t.Fatalf("closed renewed lock release=%v", err)
		}
	})
}

func TestHostCacheLockBackendHonorsCallerAndRuntimeCancellation(t *testing.T) {
	for _, operation := range []string{"release", "renew"} {
		t.Run(operation+"_caller_deadline", func(t *testing.T) {
			fixture := newHostCacheTestFixture(t, "caller-"+operation+".cache", cacheregistry.PolicyPublic, "")
			started := make(chan struct{})
			backend := &blockingHostCacheLockBackend{fakeHostCacheBackend: newFakeHostCacheBackend()}
			if operation == "release" {
				backend.releaseStarted = started
			} else {
				backend.renewStarted = started
			}
			service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
			if err != nil {
				t.Fatal(err)
			}
			lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
				HostCacheRequestBase: fixture.base, Key: operation, TTL: 2 * time.Second,
			})
			if err != nil || !acquired {
				t.Fatalf("setup acquired=%t err=%v", acquired, err)
			}
			callerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if operation == "release" {
				err = lock.Release(callerCtx)
			} else {
				err = lock.Renew(callerCtx, 2*time.Second)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocking backend caller deadline=%v", err)
			}
			select {
			case <-started:
			default:
				t.Fatal("blocking backend was not entered")
			}
			backend.fakeHostCacheBackend.mu.Lock()
			remainingLocks := len(backend.locks)
			backend.fakeHostCacheBackend.mu.Unlock()
			if remainingLocks != 0 {
				t.Fatalf("caller cancellation retained %d locks", remainingLocks)
			}
			if operation == "renew" {
				if err := lock.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
					t.Fatalf("closed renewed lock release=%v", err)
				}
			}
		})

		t.Run(operation+"_runtime_cancel", func(t *testing.T) {
			fixture := newHostCacheTestFixture(t, "runtime-"+operation+".cache", cacheregistry.PolicyPublic, "")
			started := make(chan struct{})
			backend := &blockingHostCacheLockBackend{fakeHostCacheBackend: newFakeHostCacheBackend()}
			if operation == "release" {
				backend.releaseStarted = started
			} else {
				backend.renewStarted = started
			}
			admission := &cancelableHostCacheAdmission{}
			service, err := NewHostCacheService(fixture.registry, backend, nil,
				hostCacheTestServiceOptions(WithHostCacheRuntimeAdmission(admission))...)
			if err != nil {
				t.Fatal(err)
			}
			lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
				HostCacheRequestBase: fixture.base, Key: operation, TTL: 2 * time.Second,
			})
			if err != nil || !acquired {
				t.Fatalf("setup acquired=%t err=%v", acquired, err)
			}
			errCh := make(chan error, 1)
			go func() {
				if operation == "release" {
					errCh <- lock.Release(context.Background())
					return
				}
				errCh <- lock.Renew(context.Background(), 2*time.Second)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("blocking backend was not entered")
			}
			admission.cancelRuntime()
			select {
			case err := <-errCh:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("blocking backend runtime cancellation=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("blocking backend ignored runtime cancellation")
			}
			backend.fakeHostCacheBackend.mu.Lock()
			remainingLocks := len(backend.locks)
			backend.fakeHostCacheBackend.mu.Unlock()
			if remainingLocks != 0 {
				t.Fatalf("runtime cancellation retained %d locks", remainingLocks)
			}
		})
	}
}

func TestHostCacheReleaseWithCanceledCallerStillCleansExactToken(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "release-canceled.cache", cacheregistry.PolicyPublic, "")
	backend := &delayedResponseHostCacheBackend{fakeHostCacheBackend: newFakeHostCacheBackend()}
	service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
		HostCacheRequestBase: fixture.base, Key: "release-canceled", TTL: time.Second,
	})
	if err != nil || !acquired {
		t.Fatalf("setup acquired=%t err=%v", acquired, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lock.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("release canceled caller=%v", err)
	}
	if backend.observedReleaseCalls() != 1 {
		t.Fatalf("release canceled cleanup calls=%d", backend.observedReleaseCalls())
	}
	backend.fakeHostCacheBackend.mu.Lock()
	defer backend.fakeHostCacheBackend.mu.Unlock()
	if len(backend.locks) != 0 {
		t.Fatalf("release canceled retained locks=%#v", backend.locks)
	}
}

func TestHostCacheExplicitCoreProviderUsesHostBackendWithoutResolver(t *testing.T) {
	fixture := newHostCacheTestFixture(
		t, "explicit-core.cache", cacheregistry.PolicyPublic, HostCacheCoreProviderID,
	)
	revision, err := fixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "core", Schema: fixture.schema,
		Value: []byte(`{"provider":"core"}`), TTL: time.Minute,
	})
	if err != nil || revision == "" || len(fixture.backend.observedKeys()) != 1 {
		t.Fatalf("explicit Core provider revision=%q keys=%v error=%v",
			revision, fixture.backend.observedKeys(), err)
	}
}

func TestHostCacheProviderFallbackFailsClosedSafeModeAndStaleRuntime(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "provider.cache", cacheregistry.PolicyPublic, "provider.cache.selected")
	failing := newFakeHostCacheBackend()
	failing.failure = errors.New("provider credential secret")
	resolver := &fakeHostCacheProviderResolver{resolution: HostCacheProviderResolution{
		Revision: 9, Fallback: HostCacheFallbackNext,
		Candidates: []HostCacheProviderCandidate{
			{ProviderID: fixture.cache.Provider, ExtensionID: "provider.extension", ExtensionVersion: "1.0.0",
				ArtifactDigest: strings.Repeat("c", 64), VersionID: 91, RuntimeInstance: "provider-runtime", Backend: failing},
			{ProviderID: HostCacheCoreProviderID, Core: true, Backend: fixture.backend},
		},
	}}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, resolver, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	set := HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "fallback", Schema: fixture.schema,
		Value: []byte("core"), TTL: time.Minute,
	}
	if _, err := service.Set(context.Background(), set); !errors.Is(err, ErrHostCacheProviderInvalid) {
		t.Fatalf("unsafe cross-provider fallback = %v", err)
	}
	if len(fixture.backend.observedKeys()) != 0 {
		t.Fatal("unsafe fallback wrote data that could later be resurrected")
	}

	resolver.resolution.Fallback = HostCacheFallbackClosed
	resolver.resolution.Candidates = resolver.resolution.Candidates[:1]
	if _, err := service.Set(context.Background(), set); !errors.Is(err, ErrHostCacheProviderUnavailable) {
		t.Fatalf("closed provider failure = %v", err)
	}

	coreArtifact, err := cacheregistry.NewCoreArtifact("core.safe-cache", "1.0.0", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	coreCache := cacheregistry.Declaration{
		ID: "core.safe-cache.records", ContractVersion: "core.safe-cache.records@1",
		Namespace: "core.safe-cache.records.namespace", Policy: cacheregistry.PolicyPublic,
		Provider: "core.safe-cache.external", Tags: []string{"core.safe-cache.tag.records"},
	}
	coreRegistry := cacheregistry.New()
	if _, err := coreRegistry.ReplaceAll([]cacheregistry.Publication{{Artifact: coreArtifact, Caches: []cacheregistry.Declaration{coreCache}}}, true); err != nil {
		t.Fatal(err)
	}
	coreBackend := newFakeHostCacheBackend()
	coreService, err := NewHostCacheService(coreRegistry, coreBackend, &fakeHostCacheProviderResolver{resolveErr: errors.New("must not run")}, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coreService.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: HostCacheRequestBase{
			Caller: HostCacheCaller{Core: true, Attested: true}, CacheID: coreCache.ID,
		},
		Key: "safe", Schema: fixture.schema, Value: []byte("safe"), TTL: time.Minute,
	}); err != nil {
		t.Fatalf("safe mode core fallback = %v", err)
	}

	publicFixture := newHostCacheTestFixture(t, "stale.cache", cacheregistry.PolicyPublic, "")
	if _, err := publicFixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: publicFixture.base, Key: "stale", Schema: publicFixture.schema,
		Value: []byte("must-not-escape"), TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	publicFixture.backend.afterGet = func() { publicFixture.admitted.Store(false) }
	if result, err := publicFixture.service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: publicFixture.base, Key: "stale", Schema: publicFixture.schema,
	}); err != nil || !result.Found {
		t.Fatalf("already-leased read was invalidated by graceful drain: result=%#v err=%v", result, err)
	}
	if _, err := publicFixture.service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: publicFixture.base, Key: "stale", Schema: publicFixture.schema,
	}); !errors.Is(err, ErrHostCacheStale) {
		t.Fatalf("new read entered after drain = %v", err)
	}
}

func TestHostCacheRememberPinsProviderAcrossSelectionSwitch(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "pin.cache", cacheregistry.PolicyPublic, "pin.cache.selected")
	first, second := newFakeHostCacheBackend(), newFakeHostCacheBackend()
	firstCandidate := HostCacheProviderCandidate{
		ProviderID: fixture.cache.Provider, ExtensionID: "provider.first", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("c", 64), VersionID: 11, RuntimeInstance: "provider-first-runtime", Backend: first,
	}
	secondCandidate := HostCacheProviderCandidate{
		ProviderID: fixture.cache.Provider, ExtensionID: "provider.second", ExtensionVersion: "1.0.0",
		ArtifactDigest: strings.Repeat("d", 64), VersionID: 12, RuntimeInstance: "provider-second-runtime", Backend: second,
	}
	resolver := &fakeHostCacheProviderResolver{resolution: HostCacheProviderResolution{
		Revision: 1, Fallback: HostCacheFallbackClosed, Candidates: []HostCacheProviderCandidate{firstCandidate},
	}}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, resolver, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	loaderStarted := make(chan struct{})
	loaderContinue := make(chan struct{})
	resultCh := make(chan error, 1)
	go func() {
		_, rememberErr := service.Remember(context.Background(), HostCacheRememberRequest{
			HostCacheSetRequest: HostCacheSetRequest{
				HostCacheRequestBase: fixture.base, Key: "provider-switch", Schema: fixture.schema, TTL: time.Minute,
			},
			LockTTL: time.Second, Wait: time.Second,
			Load: func(context.Context) ([]byte, error) {
				close(loaderStarted)
				<-loaderContinue
				return []byte("first-provider"), nil
			},
		})
		resultCh <- rememberErr
	}()
	<-loaderStarted
	resolver.mu.Lock()
	resolver.resolution = HostCacheProviderResolution{
		Revision: 2, Fallback: HostCacheFallbackClosed, Candidates: []HostCacheProviderCandidate{secondCandidate},
	}
	resolver.mu.Unlock()
	close(loaderContinue)
	if err := <-resultCh; err != nil {
		t.Fatalf("in-flight pinned provider = %v", err)
	}
	first.mu.Lock()
	firstItems := len(first.items)
	first.mu.Unlock()
	second.mu.Lock()
	secondItems := len(second.items)
	second.mu.Unlock()
	if firstItems != 1 || secondItems != 0 {
		t.Fatalf("provider switch wrote first=%d second=%d", firstItems, secondItems)
	}
	result, err := service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "provider-switch", Schema: fixture.schema,
	})
	if err != nil || result.Found {
		t.Fatalf("new provider resurrected old provider data: result=%#v err=%v", result, err)
	}
	resolver.mu.Lock()
	resolver.resolution = HostCacheProviderResolution{
		Revision: 3, Fallback: HostCacheFallbackClosed, Candidates: []HostCacheProviderCandidate{firstCandidate},
	}
	resolver.mu.Unlock()
	result, err = service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "provider-switch", Schema: fixture.schema,
	})
	if err != nil || result.Found {
		t.Fatalf("provider switch-back resurrected old revision: result=%#v err=%v", result, err)
	}
}

func TestHostCacheRejectsPoisonedProviderAndNeverLogsSecrets(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "poison.cache", cacheregistry.PolicyPublic, "")
	fixture.backend.poison = &HostCacheStoredValue{
		Value: []byte("secret-result"), SchemaID: "other.schema", SchemaVersion: "1",
		Revision: strings.Repeat("a", 64), Tags: []string{"foreign:tag"},
	}
	if _, err := fixture.service.Get(context.Background(), HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "secret-key", Schema: fixture.schema,
	}); !errors.Is(err, ErrHostCachePoisoned) {
		t.Fatalf("poisoned result = %v", err)
	}

	fixture.backend.poison = nil
	var logs bytes.Buffer
	inspector := NewHostCacheInspector(16)
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		hostCacheTestServiceOptions(WithHostCacheTraceSink(NewMultiHostCacheTraceSink(NewSlogHostCacheTraceSink(logger), inspector)))...)
	if err != nil {
		t.Fatal(err)
	}
	secretKey, secretValue := "password=trace-secret", "credential=trace-secret"
	if _, err := service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: secretKey, Schema: fixture.schema,
		Value: []byte(secretValue), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "trace-secret") || strings.Contains(logs.String(), fixture.cache.Tags[0]) {
		t.Fatalf("cache trace leaked secret input: %s", logs.String())
	}
	entries := inspector.Snapshot()
	if len(entries) != 1 || entries[0].ExtensionID != fixture.artifact.ExtensionID ||
		entries[0].CacheID != fixture.cache.ID || entries[0].ProviderID != HostCacheCoreProviderID ||
		entries[0].Outcome != HostCacheTraceAllowed {
		t.Fatalf("inspector attribution = %#v", entries)
	}
}
