package hostapi

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
)

type protocolV2CacheLockHarness struct {
	fixture        hostCacheTestFixture
	server         *ProtocolV2CacheServiceServer
	identity       *protocolv2.ExtensionIdentity
	requestContext *protocolv2.RequestContext
	ctx            context.Context
}

func newProtocolV2CacheLockHarness(t *testing.T, extensionID string) protocolV2CacheLockHarness {
	t.Helper()
	fixture := newHostCacheTestFixture(t, extensionID, "public", "")
	server, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: fixture.artifact.ExtensionID, ExtensionVersion: fixture.artifact.ExtensionVersion,
		ArtifactDigest: fixture.artifact.PackageDigest, TrustGrantId: "grant-cache-lock",
		RuntimeEpoch: 7, InstanceId: fixture.artifact.RuntimeInstanceID,
	}
	requestContext := &protocolv2.RequestContext{
		RequestId: "cache-lock-request", Extension: identity, Locale: "zh-CN",
	}
	return protocolV2CacheLockHarness{
		fixture: fixture, server: server, identity: identity, requestContext: requestContext,
		ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), identity),
	}
}

func (h protocolV2CacheLockHarness) acquire(t *testing.T, key string, ttl time.Duration) *hostv2.CacheLockAcquireResponse {
	t.Helper()
	response, err := h.server.AcquireLock(h.ctx, &hostv2.CacheLockAcquireRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: key, Ttl: durationpb.New(ttl),
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestProtocolV2CacheLockAcquireContentionHasNoCapability(t *testing.T) {
	h := newProtocolV2CacheLockHarness(t, "protocol-lock-contention.cache")
	first := h.acquire(t, "rebuild", time.Second)
	if !first.GetAcquired() || first.GetError() != nil || len(first.GetLeaseToken()) != 64 ||
		first.GetExpiresAt() == nil || !first.GetExpiresAt().IsValid() {
		t.Fatalf("first acquire = %#v", first)
	}
	second := h.acquire(t, "rebuild", time.Second)
	if second.GetError() != nil || second.GetAcquired() || second.GetLeaseToken() != "" || second.GetExpiresAt() != nil {
		t.Fatalf("contended acquire = %#v", second)
	}
	released, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "rebuild", LeaseToken: first.GetLeaseToken(),
	})
	if err != nil || released.GetError() != nil || !released.GetReleased() {
		t.Fatalf("release = %#v, %v", released, err)
	}
}

func TestProtocolV2CacheLockRenewsAndAtomicallyStoresOnce(t *testing.T) {
	h := newProtocolV2CacheLockHarness(t, "protocol-lock-commit.cache")
	acquireCtx, cancelAcquire := context.WithCancel(h.ctx)
	acquired, err := h.server.AcquireLock(acquireCtx, &hostv2.CacheLockAcquireRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "post:42",
		Ttl: durationpb.New(300 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Returning from an RPC ends its context. A cross-RPC lease must instead
	// remain pinned to exact runtime admission until release, drain, or TTL.
	cancelAcquire()
	if !acquired.GetAcquired() || acquired.GetError() != nil {
		t.Fatalf("acquire = %#v", acquired)
	}
	originalExpiry := acquired.GetExpiresAt().AsTime()
	renewed, err := h.server.RenewLock(h.ctx, &hostv2.CacheLockRenewRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "post:42",
		LeaseToken: acquired.GetLeaseToken(), Ttl: durationpb.New(time.Second),
	})
	if err != nil || renewed.GetError() != nil || !renewed.GetRenewed() ||
		!renewed.GetExpiresAt().AsTime().After(originalExpiry) {
		t.Fatalf("renew = %#v, %v", renewed, err)
	}
	value, err := structpb.NewStruct(map[string]any{"title": "atomic"})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := h.server.SetAndReleaseLock(h.ctx, &hostv2.CacheSetAndReleaseLockRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "post:42",
		LeaseToken: acquired.GetLeaseToken(),
		Value: &protocolv2.TypedDocument{
			SchemaId: h.fixture.schema.ID, SchemaVersion: h.fixture.schema.Version, Value: value,
		},
		Ttl: durationpb.New(time.Minute), Tags: []string{h.fixture.cache.Tags[0]},
	})
	if err != nil || committed.GetError() != nil || len(committed.GetRevision()) != 64 {
		t.Fatalf("commit = %#v, %v", committed, err)
	}
	get, err := h.server.Get(h.ctx, &hostv2.CacheGetRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "post:42",
		ValueSchemaId: h.fixture.schema.ID, ValueSchemaVersion: h.fixture.schema.Version,
	})
	if err != nil || get.GetError() != nil || !get.GetFound() || get.GetRevision() != committed.GetRevision() ||
		get.GetValue().GetValue().AsMap()["title"] != "atomic" {
		t.Fatalf("get = %#v, %v", get, err)
	}
	replayed, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "post:42",
		LeaseToken: acquired.GetLeaseToken(),
	})
	if err != nil || replayed.GetError().GetReason() != "host.cache_lock_not_owned" || replayed.GetReleased() {
		t.Fatalf("replayed release = %#v, %v", replayed, err)
	}
}

func TestProtocolV2CacheRenewFencesOldTimerCallbackGeneration(t *testing.T) {
	h := newProtocolV2CacheLockHarness(t, "protocol-lock-timer-generation.cache")
	acquired := h.acquire(t, "old-callback", 2*time.Second)
	if !acquired.GetAcquired() {
		t.Fatalf("acquire = %#v", acquired)
	}
	binding, err := newProtocolV2CacheLeaseBinding(
		h.identity, h.requestContext, h.fixture.cache.Namespace, "old-callback",
	)
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration, err := h.server.leases.take(acquired.GetLeaseToken(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldGeneration.lock.Renew(h.ctx, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	newGeneration, err := h.server.leases.reinsert(oldGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if newGeneration == oldGeneration {
		t.Fatal("renew reused the timer generation pointer")
	}
	// Model a timer callback that passed Stop concurrently and runs only after
	// the renewed token has been published.
	h.server.leases.expire(oldGeneration)
	value, err := structpb.NewStruct(map[string]any{"generation": "new"})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := h.server.SetAndReleaseLock(h.ctx, &hostv2.CacheSetAndReleaseLockRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "old-callback",
		LeaseToken: acquired.GetLeaseToken(),
		Value: &protocolv2.TypedDocument{
			SchemaId: h.fixture.schema.ID, SchemaVersion: h.fixture.schema.Version, Value: value,
		},
		Ttl: durationpb.New(time.Minute),
	})
	if err != nil || committed.GetError() != nil || committed.GetRevision() == "" {
		t.Fatalf("old callback removed renewed lease: response=%#v err=%v", committed, err)
	}

	expiring := h.acquire(t, "new-callback", 2*time.Second)
	expiringBinding, err := newProtocolV2CacheLeaseBinding(
		h.identity, h.requestContext, h.fixture.cache.Namespace, "new-callback",
	)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := h.server.leases.take(expiring.GetLeaseToken(), expiringBinding)
	if err != nil {
		t.Fatal(err)
	}
	if err := taken.lock.Renew(h.ctx, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	currentGeneration, err := h.server.leases.reinsert(taken)
	if err != nil {
		t.Fatal(err)
	}
	h.server.leases.expire(currentGeneration)
	released, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "new-callback",
		LeaseToken: expiring.GetLeaseToken(),
	})
	if err != nil || released.GetError().GetReason() != "host.cache_lock_not_owned" {
		t.Fatalf("current callback did not expire lease: response=%#v err=%v", released, err)
	}
}

func TestProtocolV2CacheLockOwnerMismatchDoesNotConsumeLease(t *testing.T) {
	h := newProtocolV2CacheLockHarness(t, "protocol-lock-owner.cache")
	acquired := h.acquire(t, "owned", time.Second)
	if !acquired.GetAcquired() {
		t.Fatalf("acquire = %#v", acquired)
	}
	wrongIdentity := proto.Clone(h.identity).(*protocolv2.ExtensionIdentity)
	wrongIdentity.InstanceId = "runtime-other"
	wrongContext := proto.Clone(h.requestContext).(*protocolv2.RequestContext)
	wrongContext.Extension = wrongIdentity
	wrongRuntimeCtx := ContextWithProtocolV2RuntimeIdentity(context.Background(), wrongIdentity)
	wrongRuntime, err := h.server.ReleaseLock(wrongRuntimeCtx, &hostv2.CacheLockReleaseRequest{
		Context: wrongContext, Namespace: h.fixture.cache.Namespace, Key: "owned", LeaseToken: acquired.GetLeaseToken(),
	})
	if err != nil || wrongRuntime.GetError().GetReason() != "host.cache_lock_not_owned" {
		t.Fatalf("wrong runtime = %#v, %v", wrongRuntime, err)
	}
	wrongKey, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "other", LeaseToken: acquired.GetLeaseToken(),
	})
	if err != nil || wrongKey.GetError().GetReason() != "host.cache_lock_not_owned" {
		t.Fatalf("wrong key = %#v, %v", wrongKey, err)
	}
	wrongNamespace, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: "other.cache.namespace", Key: "owned", LeaseToken: acquired.GetLeaseToken(),
	})
	if err != nil || wrongNamespace.GetError().GetReason() != "host.cache_lock_not_owned" {
		t.Fatalf("wrong namespace = %#v, %v", wrongNamespace, err)
	}
	oversizedToken, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "owned",
		LeaseToken: strings.Repeat("a", 65),
	})
	if err != nil || oversizedToken.GetError().GetReason() != "host.cache_request_invalid" {
		t.Fatalf("oversized token = %#v, %v", oversizedToken, err)
	}
	canonicalLocale := proto.Clone(h.requestContext).(*protocolv2.RequestContext)
	canonicalLocale.Locale = "zh-cn"
	released, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
		Context: canonicalLocale, Namespace: strings.ToUpper(h.fixture.cache.Namespace),
		Key: "owned", LeaseToken: acquired.GetLeaseToken(),
	})
	if err != nil || released.GetError() != nil || !released.GetReleased() {
		t.Fatalf("canonical owner release = %#v, %v", released, err)
	}
}

func TestProtocolV2CacheLockFailsClosedOnExpiryStaleAndCancellation(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		h := newProtocolV2CacheLockHarness(t, "protocol-lock-expiry.cache")
		acquired := h.acquire(t, "short", HostCacheMinimumLockTTL)
		if !acquired.GetAcquired() {
			t.Fatalf("acquire = %#v", acquired)
		}
		time.Sleep(2 * HostCacheMinimumLockTTL)
		response, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
			Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "short", LeaseToken: acquired.GetLeaseToken(),
		})
		if err != nil || response.GetError().GetReason() != "host.cache_lock_not_owned" {
			t.Fatalf("expired release = %#v, %v", response, err)
		}
	})

	t.Run("stale declaration", func(t *testing.T) {
		h := newProtocolV2CacheLockHarness(t, "protocol-lock-stale.cache")
		acquired := h.acquire(t, "stale", time.Second)
		if _, removed, err := h.fixture.registry.Remove(h.fixture.artifact); err != nil || !removed {
			t.Fatalf("remove declaration: removed=%t err=%v", removed, err)
		}
		response, err := h.server.RenewLock(h.ctx, &hostv2.CacheLockRenewRequest{
			Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "stale",
			LeaseToken: acquired.GetLeaseToken(), Ttl: durationpb.New(time.Second),
		})
		if err != nil || response.GetError().GetReason() != "host.cache_runtime_stale" || response.GetRenewed() {
			t.Fatalf("stale renew = %#v, %v", response, err)
		}
	})

	t.Run("runtime drain", func(t *testing.T) {
		h := newProtocolV2CacheLockHarness(t, "protocol-lock-drain.cache")
		admission := &protocolV2CacheCancelableAdmission{}
		service, err := NewHostCacheService(h.fixture.registry, h.fixture.backend, nil,
			WithHostCacheInstallationID("protocol-v2-cache-drain"), WithHostCacheRuntimeAdmission(admission),
			WithHostCacheTraceSink(NewHostCacheInspector(32)))
		if err != nil {
			t.Fatal(err)
		}
		h.server, err = NewProtocolV2CacheServiceServer(service)
		if err != nil {
			t.Fatal(err)
		}
		acquired := h.acquire(t, "drain", time.Second)
		if !acquired.GetAcquired() {
			t.Fatalf("acquire = %#v", acquired)
		}
		admission.drain(errors.New("runtime draining"))
		response, err := h.server.RenewLock(h.ctx, &hostv2.CacheLockRenewRequest{
			Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "drain",
			LeaseToken: acquired.GetLeaseToken(), Ttl: durationpb.New(time.Second),
		})
		if err != nil || response.GetError().GetReason() != "host.cache_runtime_stale" || response.GetRenewed() {
			t.Fatalf("drained renew = %#v, %v", response, err)
		}
	})

	t.Run("cancelled release", func(t *testing.T) {
		h := newProtocolV2CacheLockHarness(t, "protocol-lock-cancel.cache")
		acquired := h.acquire(t, "cancel", time.Second)
		caller, cancel := context.WithCancel(h.ctx)
		cancel()
		response, err := h.server.ReleaseLock(caller, &hostv2.CacheLockReleaseRequest{
			Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "cancel",
			LeaseToken: acquired.GetLeaseToken(),
		})
		if err != nil || response.GetError().GetReason() != "host.cache_cancelled" || response.GetReleased() {
			t.Fatalf("cancelled release = %#v, %v", response, err)
		}
		replay, err := h.server.ReleaseLock(h.ctx, &hostv2.CacheLockReleaseRequest{
			Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "cancel",
			LeaseToken: acquired.GetLeaseToken(),
		})
		if err != nil || replay.GetError().GetReason() != "host.cache_lock_not_owned" {
			t.Fatalf("cancel replay = %#v, %v", replay, err)
		}
	})
}

type protocolV2CacheCancelableAdmission struct {
	mu     sync.Mutex
	cancel context.CancelCauseFunc
}

func (a *protocolV2CacheCancelableAdmission) AcquireServiceProvider(
	ctx context.Context,
	_ ServiceProviderIdentity,
) (ServiceProviderAdmissionLease, error) {
	leaseCtx, cancel := context.WithCancelCause(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	return &testServiceProviderLease{ctx: leaseCtx}, nil
}

func (a *protocolV2CacheCancelableAdmission) drain(cause error) {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
}

func TestProtocolV2CacheSetAndReleaseHasSingleConcurrentWinner(t *testing.T) {
	h := newProtocolV2CacheLockHarness(t, "protocol-lock-race.cache")
	acquired := h.acquire(t, "winner", 2*time.Second)
	if !acquired.GetAcquired() {
		t.Fatalf("acquire = %#v", acquired)
	}
	value, err := structpb.NewStruct(map[string]any{"winner": true})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 64
	start := make(chan struct{})
	results := make(chan *hostv2.CacheSetAndReleaseLockResponse, callers)
	errorsCh := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response, callErr := h.server.SetAndReleaseLock(h.ctx, &hostv2.CacheSetAndReleaseLockRequest{
				Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "winner",
				LeaseToken: acquired.GetLeaseToken(),
				Value: &protocolv2.TypedDocument{
					SchemaId: h.fixture.schema.ID, SchemaVersion: h.fixture.schema.Version, Value: value,
				},
				Ttl: durationpb.New(time.Minute),
			})
			results <- response
			errorsCh <- callErr
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsCh)
	for callErr := range errorsCh {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	winners, replayed := 0, 0
	for response := range results {
		switch {
		case response.GetError() == nil && len(response.GetRevision()) == 64:
			winners++
		case response.GetError().GetReason() == "host.cache_lock_not_owned":
			replayed++
		default:
			t.Fatalf("unexpected result = %#v", response)
		}
	}
	if winners != 1 || replayed != callers-1 {
		t.Fatalf("winners=%d replayed=%d", winners, replayed)
	}
}

func TestProtocolV2CacheLockRedisAtomicRoundTripIntegration(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("SFORUM_TEST_REDIS_ADDR is required for Host cache Redis integration tests")
	}
	database := 15
	if raw := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_DB")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			t.Fatalf("invalid SFORUM_TEST_REDIS_DB %q", raw)
		}
		database = value
	}
	client := redis.NewClient(&redis.Options{
		Addr: address, Password: os.Getenv("SFORUM_TEST_REDIS_PASSWORD"), DB: database,
		DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis is unavailable: %v", err)
	}

	h := newProtocolV2CacheLockHarness(t, "protocol-lock-redis.cache")
	backend, err := NewHostRedisCacheBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHostCacheService(h.fixture.registry, backend, nil,
		WithHostCacheInstallationID("protocol-v2-cache-redis"),
		WithHostCacheRuntimeAdmission(&testServiceProviderAdmission{}),
		WithHostCacheTraceSink(NewHostCacheInspector(64)))
	if err != nil {
		t.Fatal(err)
	}
	h.server, err = NewProtocolV2CacheServiceServer(service)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.prepare(ctx, HostCacheRequestBase{
		Caller: h.fixture.caller, Namespace: h.fixture.cache.Namespace, Scope: HostCacheScope{Locale: "zh-CN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared.release()
	cleanupProtocolV2CacheRedisKeys(t, client, prepared.contractRoot)
	t.Cleanup(func() { cleanupProtocolV2CacheRedisKeys(t, client, prepared.contractRoot) })

	acquired := h.acquire(t, "redis-atomic", 300*time.Millisecond)
	if !acquired.GetAcquired() || acquired.GetError() != nil {
		t.Fatalf("acquire = %#v", acquired)
	}
	renewed, err := h.server.RenewLock(h.ctx, &hostv2.CacheLockRenewRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "redis-atomic",
		LeaseToken: acquired.GetLeaseToken(), Ttl: durationpb.New(time.Second),
	})
	if err != nil || renewed.GetError() != nil || !renewed.GetRenewed() {
		t.Fatalf("renew = %#v, %v", renewed, err)
	}
	value, err := structpb.NewStruct(map[string]any{"redis": "atomic"})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := h.server.SetAndReleaseLock(h.ctx, &hostv2.CacheSetAndReleaseLockRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "redis-atomic",
		LeaseToken: acquired.GetLeaseToken(),
		Value: &protocolv2.TypedDocument{
			SchemaId: h.fixture.schema.ID, SchemaVersion: h.fixture.schema.Version, Value: value,
		},
		Ttl: durationpb.New(time.Minute), Tags: []string{h.fixture.cache.Tags[0]},
	})
	if err != nil || committed.GetError() != nil || len(committed.GetRevision()) != 64 {
		t.Fatalf("commit = %#v, %v", committed, err)
	}
	if exists, err := client.Exists(ctx, prepared.lockKey("redis-atomic")).Result(); err != nil || exists != 0 {
		t.Fatalf("atomic commit retained lock: exists=%d err=%v", exists, err)
	}
	if exists, err := client.Exists(ctx, prepared.valueKey("redis-atomic")).Result(); err != nil || exists != 1 {
		t.Fatalf("atomic commit missing value: exists=%d err=%v", exists, err)
	}
	replayed, err := h.server.SetAndReleaseLock(h.ctx, &hostv2.CacheSetAndReleaseLockRequest{
		Context: h.requestContext, Namespace: h.fixture.cache.Namespace, Key: "redis-atomic",
		LeaseToken: acquired.GetLeaseToken(),
		Value: &protocolv2.TypedDocument{
			SchemaId: h.fixture.schema.ID, SchemaVersion: h.fixture.schema.Version, Value: value,
		},
		Ttl: durationpb.New(time.Minute),
	})
	if err != nil || replayed.GetError().GetReason() != "host.cache_lock_not_owned" {
		t.Fatalf("replayed commit = %#v, %v", replayed, err)
	}
}

func cleanupProtocolV2CacheRedisKeys(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			t.Fatalf("scan Redis cache keys: %v", err)
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Fatalf("delete Redis cache keys: %v", err)
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}
