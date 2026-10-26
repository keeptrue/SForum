package hostapi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestHostCacheServiceBoundsAndNamespaceEscape(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "bounds.cache", cacheregistry.PolicyPublic, "")
	base := fixture.base
	base.CacheID = ""
	base.Namespace = fixture.cache.Namespace
	if _, err := fixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: base, Key: strings.Repeat("k", HostCacheMaximumKeyBytes+1), Schema: fixture.schema,
		Value: []byte("x"), TTL: time.Minute,
	}); !errors.Is(err, ErrHostCacheInvalid) {
		t.Fatalf("long key = %v", err)
	}
	if _, err := fixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: base, Key: "../../other:cache", Schema: fixture.schema,
		Value: make([]byte, HostCacheMaximumValueBytes+1), TTL: time.Minute,
	}); !errors.Is(err, ErrHostCacheInvalid) {
		t.Fatalf("large value = %v", err)
	}
	if _, err := fixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: base, Key: "ttl", Schema: fixture.schema,
		Value: []byte("x"), TTL: HostCacheMaximumTTL + time.Millisecond,
	}); !errors.Is(err, ErrHostCacheInvalid) {
		t.Fatalf("large ttl = %v", err)
	}
	if _, err := fixture.service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: base, Key: "../../other:cache", Schema: fixture.schema,
		Value: []byte("safe"), TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range fixture.backend.observedKeys() {
		if strings.Contains(key, "..") || strings.Contains(key, "other:cache") {
			t.Fatalf("namespace escape reached physical key: %q", key)
		}
	}
}

func TestHostCacheServiceRequiresInstallationNamespaceAndExactVersionID(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "version.cache", cacheregistry.PolicyPublic, "")
	if _, err := NewHostCacheService(fixture.registry, fixture.backend, nil); !errors.Is(err, ErrHostCacheInvalid) {
		t.Fatalf("missing installation/admission configuration = %v", err)
	}
	for _, versionID := range []int64{0, fixture.artifact.VersionID + 1} {
		base := fixture.base
		base.Caller.VersionID = versionID
		if _, err := fixture.service.Get(context.Background(), HostCacheGetRequest{
			HostCacheRequestBase: base, Key: "exact", Schema: fixture.schema,
		}); !errors.Is(err, ErrHostCacheDenied) {
			t.Fatalf("version id %d = %v", versionID, err)
		}
	}
}

func TestHostCacheMultiTraceSinkRejectsAuditlessConstruction(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "trace-required.cache", cacheregistry.PolicyPublic, "")
	for name, sink := range map[string]HostCacheTraceSink{
		"zero":    NewMultiHostCacheTraceSink(),
		"all_nil": NewMultiHostCacheTraceSink(nil, nil),
	} {
		t.Run(name, func(t *testing.T) {
			if sink != nil {
				t.Fatalf("auditless multi-sink=%#v", sink)
			}
			_, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
				WithHostCacheInstallationID("trace-required-installation"),
				WithHostCacheRuntimeAdmission(&testServiceProviderAdmission{}),
				WithHostCacheTraceSink(sink),
			)
			if !errors.Is(err, ErrHostCacheInvalid) {
				t.Fatalf("auditless service construction=%v", err)
			}
		})
	}
}

func TestHostCacheServiceSeparatesInstallationsSharingBackend(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "installation.cache", cacheregistry.PolicyPublic, "")
	admission := &testServiceProviderAdmission{}
	first, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		WithHostCacheInstallationID("installation-a"), WithHostCacheRuntimeAdmission(admission),
		WithHostCacheTraceSink(NewHostCacheInspector(16)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		WithHostCacheInstallationID("installation-b"), WithHostCacheRuntimeAdmission(admission),
		WithHostCacheTraceSink(NewHostCacheInspector(16)))
	if err != nil {
		t.Fatal(err)
	}
	for service, value := range map[*HostCacheService]string{first: "first", second: "second"} {
		if _, err := service.Set(context.Background(), HostCacheSetRequest{
			HostCacheRequestBase: fixture.base, Key: "same", Schema: fixture.schema,
			Value: []byte(value), TTL: time.Minute,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for service, expected := range map[*HostCacheService]string{first: "first", second: "second"} {
		result, err := service.Get(context.Background(), HostCacheGetRequest{
			HostCacheRequestBase: fixture.base, Key: "same", Schema: fixture.schema,
		})
		if err != nil || !result.Found || string(result.Value) != expected {
			t.Fatalf("installation value=%#v expected=%q err=%v", result, expected, err)
		}
	}
	keys := fixture.backend.observedKeys()
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("installation physical keys = %#v", keys)
	}
}

func TestHostCacheMutationsHoldAdmissionLeaseThroughCommit(t *testing.T) {
	for _, operation := range []string{"set", "delete", "invalidate", "increment"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newHostCacheTestFixture(t, "lease-"+operation+".cache", cacheregistry.PolicyPublic, "")
			admission := &trackingHostCacheAdmission{}
			service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
				WithHostCacheInstallationID("lease-installation"), WithHostCacheRuntimeAdmission(admission),
				WithHostCacheTraceSink(NewHostCacheInspector(16)))
			if err != nil {
				t.Fatal(err)
			}
			if operation == "delete" || operation == "invalidate" {
				if _, err := service.Set(context.Background(), HostCacheSetRequest{
					HostCacheRequestBase: fixture.base, Key: "commit", Schema: fixture.schema,
					Value: []byte("committed"), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
				}); err != nil {
					t.Fatal(err)
				}
			}
			var activeDuringCommit int64
			after := func() {
				activeDuringCommit = admission.active.Load()
				fixture.admitted.Store(false)
			}
			switch operation {
			case "set":
				fixture.backend.afterSet = after
				_, err = service.Set(context.Background(), HostCacheSetRequest{
					HostCacheRequestBase: fixture.base, Key: "commit", Schema: fixture.schema,
					Value: []byte("committed"), TTL: time.Minute,
				})
			case "delete":
				fixture.backend.afterDelete = after
				_, err = service.Delete(context.Background(), HostCacheDeleteRequest{
					HostCacheRequestBase: fixture.base, Key: "commit",
				})
			case "invalidate":
				fixture.backend.afterInvalidate = after
				_, err = service.InvalidateTags(context.Background(), HostCacheInvalidateTagsRequest{
					HostCacheRequestBase: fixture.base, Tags: []string{fixture.cache.Tags[0]},
				})
			case "increment":
				fixture.backend.afterIncrement = after
				_, err = service.Increment(context.Background(), HostCacheIncrementRequest{
					HostCacheRequestBase: fixture.base, Key: "commit", TTL: time.Minute,
				})
			}
			if err != nil {
				t.Fatalf("leased %s commit reported stale = %v", operation, err)
			}
			if activeDuringCommit != 1 || admission.active.Load() != 0 {
				t.Fatalf("%s admission during=%d active=%d seen=%d", operation, activeDuringCommit, admission.active.Load(), admission.seen.Load())
			}
		})
	}
}

func TestHostCacheExternalProviderCallHoldsOwnerAndProviderLeases(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "provider-lease.cache", cacheregistry.PolicyPublic, "provider-lease.cache.selected")
	provider := newFakeHostCacheBackend()
	resolver := &fakeHostCacheProviderResolver{resolution: HostCacheProviderResolution{
		Revision: 1, Fallback: HostCacheFallbackClosed,
		Candidates: []HostCacheProviderCandidate{{
			ProviderID: fixture.cache.Provider, ExtensionID: "provider.runtime", ExtensionVersion: "1.0.0",
			ArtifactDigest: strings.Repeat("e", 64), VersionID: 19,
			RuntimeInstance: "provider-runtime", Backend: provider,
		}},
	}}
	admission := &trackingHostCacheAdmission{}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, resolver,
		WithHostCacheInstallationID("provider-lease-installation"), WithHostCacheRuntimeAdmission(admission),
		WithHostCacheTraceSink(NewHostCacheInspector(16)))
	if err != nil {
		t.Fatal(err)
	}
	var activeDuringProvider int64
	provider.afterSet = func() { activeDuringProvider = admission.active.Load() }
	if _, err := service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "provider", Schema: fixture.schema,
		Value: []byte("provider"), TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	if activeDuringProvider != 2 || admission.seen.Load() != 2 || admission.active.Load() != 0 {
		t.Fatalf("provider leases during=%d seen=%d active=%d", activeDuringProvider, admission.seen.Load(), admission.active.Load())
	}
}

func TestHostCacheRememberRenewsLockAndRejectsOwnerReplacement(t *testing.T) {
	t.Run("renews", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "renew.cache", cacheregistry.PolicyPublic, "")
		started := make(chan struct{})
		resultCh := make(chan error, 1)
		go func() {
			_, err := fixture.service.Remember(context.Background(), HostCacheRememberRequest{
				HostCacheSetRequest: HostCacheSetRequest{HostCacheRequestBase: fixture.base, Key: "slow", Schema: fixture.schema, TTL: time.Minute},
				LockTTL:             120 * time.Millisecond, Wait: time.Second,
				Load: func(context.Context) ([]byte, error) {
					close(started)
					time.Sleep(350 * time.Millisecond)
					return []byte("renewed"), nil
				},
			})
			resultCh <- err
		}()
		<-started
		time.Sleep(180 * time.Millisecond)
		if lock, acquired, err := fixture.service.AcquireLock(context.Background(), HostCacheLockRequest{
			HostCacheRequestBase: fixture.base, Key: "slow", TTL: 120 * time.Millisecond,
		}); err != nil || acquired || lock != nil {
			t.Fatalf("renewed lock replacement lock=%#v acquired=%t err=%v", lock, acquired, err)
		}
		if err := <-resultCh; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("replacement_fences_commit", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "replace-lock.cache", cacheregistry.PolicyPublic, "")
		_, err := fixture.service.Remember(context.Background(), HostCacheRememberRequest{
			HostCacheSetRequest: HostCacheSetRequest{HostCacheRequestBase: fixture.base, Key: "slow", Schema: fixture.schema, TTL: time.Minute},
			LockTTL:             120 * time.Millisecond, Wait: time.Second,
			Load: func(context.Context) ([]byte, error) {
				fixture.backend.mu.Lock()
				for key := range fixture.backend.locks {
					fixture.backend.locks[key] = fakeHostCacheLock{owner: strings.Repeat("f", 64), expiresAt: time.Now().Add(time.Second)}
				}
				fixture.backend.mu.Unlock()
				time.Sleep(80 * time.Millisecond)
				return []byte("stale-loader"), nil
			},
		})
		if !errors.Is(err, ErrHostCacheLockNotOwned) {
			t.Fatalf("replacement owner commit = %v", err)
		}
		if len(fixture.backend.items) != 0 {
			t.Fatal("stale loader committed after lock replacement")
		}
	})
}

func TestHostCacheRememberUsesConservativeLockLifetime(t *testing.T) {
	t.Run("acquire_without_remaining_lifetime_never_loads", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "remember-acquire-latency.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			acquireDelay:         130 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		var loads atomic.Int64
		result, err := service.Remember(context.Background(), HostCacheRememberRequest{
			HostCacheSetRequest: HostCacheSetRequest{
				HostCacheRequestBase: fixture.base, Key: "expired-acquire", Schema: fixture.schema, TTL: time.Minute,
			},
			LockTTL: 100 * time.Millisecond,
			Wait:    time.Second,
			Load: func(context.Context) ([]byte, error) {
				loads.Add(1)
				return []byte("must-not-load"), nil
			},
		})
		if !errors.Is(err, ErrHostCacheLockNotOwned) || result.Found || loads.Load() != 0 {
			t.Fatalf("expired acquire result=%#v loads=%d err=%v", result, loads.Load(), err)
		}
		if backend.observedReleaseCalls() != 1 {
			t.Fatalf("expired Remember acquire cleanup calls=%d", backend.observedReleaseCalls())
		}
	})

	t.Run("delayed_renewal_cancels_loader_before_commit", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "remember-renew-latency.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			renewDelay:           100 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Remember(context.Background(), HostCacheRememberRequest{
			HostCacheSetRequest: HostCacheSetRequest{
				HostCacheRequestBase: fixture.base, Key: "delayed-renew", Schema: fixture.schema, TTL: time.Minute,
			},
			LockTTL: 120 * time.Millisecond,
			Wait:    time.Second,
			Load: func(ctx context.Context) ([]byte, error) {
				<-ctx.Done()
				return nil, context.Cause(ctx)
			},
		})
		if !errors.Is(err, ErrHostCacheLockNotOwned) || result.Found {
			t.Fatalf("delayed renewal result=%#v err=%v", result, err)
		}
		backend.fakeHostCacheBackend.mu.Lock()
		defer backend.fakeHostCacheBackend.mu.Unlock()
		if len(backend.items) != 0 {
			t.Fatalf("delayed renewal committed stale loader=%#v", backend.items)
		}
	})

	t.Run("successful_commit_remains_success_after_local_deadline", func(t *testing.T) {
		fixture := newHostCacheTestFixture(t, "remember-commit-latency.cache", cacheregistry.PolicyPublic, "")
		backend := &delayedResponseHostCacheBackend{
			fakeHostCacheBackend: newFakeHostCacheBackend(),
			commitDelay:          130 * time.Millisecond,
		}
		service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Remember(context.Background(), HostCacheRememberRequest{
			HostCacheSetRequest: HostCacheSetRequest{
				HostCacheRequestBase: fixture.base, Key: "late-success", Schema: fixture.schema, TTL: time.Minute,
			},
			LockTTL: 100 * time.Millisecond,
			Wait:    time.Second,
			Load:    func(context.Context) ([]byte, error) { return []byte("committed"), nil },
		})
		if err != nil || !result.Found || string(result.Value) != "committed" {
			t.Fatalf("late successful commit result=%#v err=%v", result, err)
		}
		backend.fakeHostCacheBackend.mu.Lock()
		defer backend.fakeHostCacheBackend.mu.Unlock()
		if len(backend.items) != 1 || len(backend.locks) != 0 {
			t.Fatalf("late successful commit items=%d locks=%d", len(backend.items), len(backend.locks))
		}
	})
}

func TestHostCacheLockFinalFenceCleansAcquiredToken(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "lock-fence.cache", cacheregistry.PolicyPublic, "")
	fixture.backend.afterLock = func() {
		if _, err := fixture.registry.ReplaceAll(nil, true); err != nil {
			t.Errorf("switch safe mode: %v", err)
		}
	}
	lock, acquired, err := fixture.service.AcquireLock(context.Background(), HostCacheLockRequest{
		HostCacheRequestBase: fixture.base, Key: "fenced", TTL: time.Second,
	})
	if !errors.Is(err, ErrHostCacheStale) || acquired || lock != nil {
		t.Fatalf("final-fenced lock=%#v acquired=%t err=%v", lock, acquired, err)
	}
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	if len(fixture.backend.locks) != 0 {
		t.Fatalf("final fence leaked locks = %#v", fixture.backend.locks)
	}
}

func TestHostCacheRememberFinalFenceCleansAcquiredToken(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "remember-fence.cache", cacheregistry.PolicyPublic, "")
	fixture.backend.afterLock = func() {
		if _, err := fixture.registry.ReplaceAll(nil, true); err != nil {
			t.Errorf("switch safe mode: %v", err)
		}
	}
	result, err := fixture.service.Remember(context.Background(), HostCacheRememberRequest{
		HostCacheSetRequest: HostCacheSetRequest{
			HostCacheRequestBase: fixture.base, Key: "fenced", Schema: fixture.schema, TTL: time.Minute,
		},
		LockTTL: time.Second,
		Wait:    time.Second,
		Load:    func(context.Context) ([]byte, error) { return []byte("must-not-run"), nil },
	})
	if !errors.Is(err, ErrHostCacheStale) || result.Found {
		t.Fatalf("final-fenced remember=%#v err=%v", result, err)
	}
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	if len(fixture.backend.locks) != 0 {
		t.Fatalf("final fence leaked remember locks = %#v", fixture.backend.locks)
	}
}

func TestHostCacheExpiredLockReleasesRuntimeAdmission(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "lock-admission.cache", cacheregistry.PolicyPublic, "")
	admission := &trackingHostCacheAdmission{}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		WithHostCacheInstallationID("lock-admission-installation"), WithHostCacheRuntimeAdmission(admission),
		WithHostCacheTraceSink(NewHostCacheInspector(16)))
	if err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := service.AcquireLock(context.Background(), HostCacheLockRequest{
		HostCacheRequestBase: fixture.base, Key: "expires", TTL: 100 * time.Millisecond,
	})
	if err != nil || !acquired || admission.active.Load() != 1 {
		t.Fatalf("lock=%#v acquired=%t active=%d err=%v", lock, acquired, admission.active.Load(), err)
	}
	deadline := time.Now().Add(time.Second)
	for admission.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if admission.active.Load() != 0 {
		t.Fatalf("expired lock retained runtime admission = %d", admission.active.Load())
	}
	fixture.backend.mu.Lock()
	remainingLocks := len(fixture.backend.locks)
	fixture.backend.mu.Unlock()
	if remainingLocks != 0 {
		t.Fatalf("expired lock retained %d backend locks", remainingLocks)
	}
	if err := lock.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
		t.Fatalf("expired lock release = %v", err)
	}
}

func TestHostCacheCanceledLockReleasesRuntimeAdmission(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "lock-cancel.cache", cacheregistry.PolicyPublic, "")
	admission := &trackingHostCacheAdmission{}
	service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		WithHostCacheInstallationID("lock-cancel-installation"), WithHostCacheRuntimeAdmission(admission),
		WithHostCacheTraceSink(NewHostCacheInspector(16)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lock, acquired, err := service.AcquireLock(ctx, HostCacheLockRequest{
		HostCacheRequestBase: fixture.base, Key: "cancel", TTL: 100 * time.Millisecond,
	})
	if err != nil || !acquired || admission.active.Load() != 1 {
		t.Fatalf("lock=%#v acquired=%t active=%d err=%v", lock, acquired, admission.active.Load(), err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for admission.active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if admission.active.Load() != 0 {
		t.Fatalf("canceled lock retained runtime admission = %d", admission.active.Load())
	}
	fixture.backend.mu.Lock()
	remainingLocks := len(fixture.backend.locks)
	fixture.backend.mu.Unlock()
	if remainingLocks != 0 {
		t.Fatalf("canceled lock retained %d backend locks", remainingLocks)
	}
	if err := lock.Release(context.Background()); !errors.Is(err, ErrHostCacheLockNotOwned) {
		t.Fatalf("canceled lock release = %v", err)
	}
}
