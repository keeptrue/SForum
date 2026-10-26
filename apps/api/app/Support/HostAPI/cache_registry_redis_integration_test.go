package hostapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestHostRedisCacheBackendIntegration(t *testing.T) {
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
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	extensionID := fmt.Sprintf("redis.cache.%d", time.Now().UnixNano())
	fixture := newHostCacheTestFixture(t, extensionID, "public", "")
	backend, err := NewHostRedisCacheBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHostCacheService(fixture.registry, backend, nil, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.prepare(ctx, fixture.base)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.release()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		var cursor uint64
		for {
			keys, next, scanErr := client.Scan(cleanupCtx, cursor, prepared.contractRoot+"*", 100).Result()
			if scanErr != nil {
				return
			}
			if len(keys) > 0 {
				_ = client.Del(cleanupCtx, keys...).Err()
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})

	set := func(key, value string) string {
		t.Helper()
		revision, setErr := service.Set(ctx, HostCacheSetRequest{
			HostCacheRequestBase: fixture.base, Key: key, Schema: fixture.schema,
			Value: []byte(value), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
		})
		if setErr != nil || revision == "" {
			t.Fatalf("set %s revision=%q err=%v", key, revision, setErr)
		}
		return revision
	}
	firstRevision := set("../../post:1", `{"id":1}`)
	set("post:2", `{"id":2}`)
	result, err := service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "../../post:1", Schema: fixture.schema,
	})
	if err != nil || !result.Found || result.Revision != firstRevision || string(result.Value) != `{"id":1}` {
		t.Fatalf("Redis get=%#v err=%v", result, err)
	}
	if strings.Contains(prepared.valueKey("../../post:1"), "..") || strings.Contains(prepared.valueKey("../../post:1"), "post:1") {
		t.Fatal("Redis physical key exposed user key")
	}
	if _, err := service.Set(ctx, HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "../../post:1", Schema: fixture.schema,
		Value: []byte(`{"id":3}`), TTL: time.Minute, ExpectedRevision: strings.Repeat("f", 64),
	}); !errors.Is(err, ErrHostCacheConflict) {
		t.Fatalf("Redis CAS conflict = %v", err)
	}
	for expected := int64(1); expected <= 2; expected++ {
		value, incrementErr := service.Increment(ctx, HostCacheIncrementRequest{
			HostCacheRequestBase: fixture.base, Key: "views", TTL: time.Minute,
		})
		if incrementErr != nil || value != expected {
			t.Fatalf("Redis increment=%d err=%v", value, incrementErr)
		}
	}
	invalidated, err := service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: fixture.base, Tags: []string{fixture.cache.Tags[0]},
	})
	if err != nil || invalidated != 2 {
		t.Fatalf("Redis invalidate=%d err=%v", invalidated, err)
	}
	set("protected", `{"id":3}`)
	physicalTags, err := prepared.physicalTags([]string{fixture.cache.Tags[0]})
	if err != nil || len(physicalTags) != 1 {
		t.Fatalf("physical Redis tags=%#v err=%v", physicalTags, err)
	}
	foreignKey := "sforum:host-cache:v1:foreign:" + extensionID
	if err := client.Set(ctx, foreignKey, "do-not-delete", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), foreignKey).Err() })
	if err := client.SAdd(ctx, physicalTags[0], foreignKey).Err(); err != nil {
		t.Fatal(err)
	}
	invalidated, err = service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: fixture.base, Tags: []string{fixture.cache.Tags[0]},
	})
	if !errors.Is(err, ErrHostCachePoisoned) || invalidated != 0 {
		t.Fatalf("Redis poisoned tag set invalidate=%d err=%v", invalidated, err)
	}
	if value, getErr := client.Get(ctx, foreignKey).Result(); getErr != nil || value != "do-not-delete" {
		t.Fatalf("foreign Redis key was changed: value=%q err=%v", value, getErr)
	}
	protected, getErr := service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "protected", Schema: fixture.schema,
	})
	if getErr != nil || !protected.Found || string(protected.Value) != `{"id":3}` {
		t.Fatalf("owned Redis key changed on poisoned invalidation: result=%#v err=%v", protected, getErr)
	}
	if err := client.SRem(ctx, physicalTags[0], foreignKey).Err(); err != nil {
		t.Fatal(err)
	}
	poisonedLockKey := prepared.lockKey("poisoned-member")
	poisonedCounterKey := prepared.counterKey("poisoned-member")
	lockToken := strings.Repeat("c", 64)
	if err := client.Set(ctx, poisonedLockKey, lockToken, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, poisonedCounterKey, "41", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.SAdd(ctx, physicalTags[0], poisonedLockKey, poisonedCounterKey).Err(); err != nil {
		t.Fatal(err)
	}
	invalidated, err = service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: fixture.base, Tags: []string{fixture.cache.Tags[0]},
	})
	if !errors.Is(err, ErrHostCachePoisoned) || invalidated != 0 {
		t.Fatalf("Redis non-value member invalidate=%d err=%v", invalidated, err)
	}
	if value, getErr := client.Get(ctx, poisonedLockKey).Result(); getErr != nil || value != lockToken {
		t.Fatalf("poisoned tag deleted lock: value=%q err=%v", value, getErr)
	}
	if value, getErr := client.Get(ctx, poisonedCounterKey).Result(); getErr != nil || value != "41" {
		t.Fatalf("poisoned tag deleted counter: value=%q err=%v", value, getErr)
	}
	protected, getErr = service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "protected", Schema: fixture.schema,
	})
	if getErr != nil || !protected.Found || string(protected.Value) != `{"id":3}` {
		t.Fatalf("owned Redis value changed on non-value poison: result=%#v err=%v", protected, getErr)
	}
	if err := client.SRem(ctx, physicalTags[0], poisonedLockKey, poisonedCounterKey).Err(); err != nil {
		t.Fatal(err)
	}

	lockRequest := HostCacheLockRequest{HostCacheRequestBase: fixture.base, Key: "rebuild", TTL: 120 * time.Millisecond}
	first, acquired, err := service.AcquireLock(ctx, lockRequest)
	if err != nil || !acquired {
		t.Fatalf("Redis first lock=%t err=%v", acquired, err)
	}
	if _, acquired, err = service.AcquireLock(ctx, lockRequest); err != nil || acquired {
		t.Fatalf("Redis contended lock=%t err=%v", acquired, err)
	}
	time.Sleep(150 * time.Millisecond)
	second, acquired, err := service.AcquireLock(ctx, lockRequest)
	if err != nil || !acquired {
		t.Fatalf("Redis replacement lock=%t err=%v", acquired, err)
	}
	if err := first.Release(ctx); !errors.Is(err, ErrHostCacheLockNotOwned) {
		t.Fatalf("Redis expired owner release=%v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("Redis current owner release=%v", err)
	}

	renewRequest := HostCacheLockRequest{HostCacheRequestBase: fixture.base, Key: "renew", TTL: 120 * time.Millisecond}
	renewedLock, acquired, err := service.AcquireLock(ctx, renewRequest)
	if err != nil || !acquired {
		t.Fatalf("Redis renew lock=%t err=%v", acquired, err)
	}
	time.Sleep(80 * time.Millisecond)
	if err := renewedLock.Renew(ctx, 250*time.Millisecond); err != nil {
		t.Fatalf("Redis renew = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if contender, acquired, err := service.AcquireLock(ctx, renewRequest); err != nil || acquired || contender != nil {
		t.Fatalf("Redis renewed lock replacement=%#v acquired=%t err=%v", contender, acquired, err)
	}
	if err := renewedLock.Release(ctx); err != nil {
		t.Fatalf("Redis renewed lock release=%v", err)
	}

	remembered, err := service.Remember(ctx, HostCacheRememberRequest{
		HostCacheSetRequest: HostCacheSetRequest{
			HostCacheRequestBase: fixture.base, Key: "remember-renew", Schema: fixture.schema, TTL: time.Minute,
		},
		LockTTL: 100 * time.Millisecond, Wait: time.Second,
		Load: func(context.Context) ([]byte, error) {
			time.Sleep(250 * time.Millisecond)
			return []byte(`{"renewed":true}`), nil
		},
	})
	if err != nil || !remembered.Found || string(remembered.Value) != `{"renewed":true}` {
		t.Fatalf("Redis remember renewal=%#v err=%v", remembered, err)
	}

	if err := client.Set(ctx, prepared.valueKey("poisoned"), "not-json-and-secret", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: fixture.base, Key: "poisoned", Schema: fixture.schema,
	}); !errors.Is(err, ErrHostCachePoisoned) {
		t.Fatalf("Redis poisoned value = %v", err)
	}
}

func TestHostCacheRedisInvalidationDeduplicatesPhysicalEntriesIntegration(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}

	extensionID := fmt.Sprintf("redis.unique.%d", time.Now().UnixNano())
	artifact := cacheregistry.Artifact{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("b", 64),
		VersionID: 17, RuntimeInstanceID: "runtime-" + strconv.FormatInt(time.Now().UnixNano(), 10),
	}
	tags := make([]string, HostCacheMaximumTags)
	for index := range tags {
		tags[index] = fmt.Sprintf("%s.tag.%02d", extensionID, index)
	}
	declaration := cacheregistry.Declaration{
		ID: extensionID + ".records", ContractVersion: extensionID + ".records@1",
		Namespace: extensionID + ".records.namespace", Policy: cacheregistry.PolicyPublic, Tags: tags,
	}
	registry := cacheregistry.New().WithPluginAdmission(func(candidate cacheregistry.Artifact) bool {
		return candidate == artifact
	})
	if _, err := registry.Publish(cacheregistry.Publication{
		Artifact: artifact, Caches: []cacheregistry.Declaration{declaration},
	}); err != nil {
		t.Fatal(err)
	}
	backend, err := NewHostRedisCacheBackend(client)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewHostCacheService(registry, backend, nil, hostCacheTestServiceOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	base := HostCacheRequestBase{
		Caller: HostCacheCaller{
			ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
			ArtifactDigest: artifact.PackageDigest, VersionID: artifact.VersionID,
			RuntimeInstanceID: artifact.RuntimeInstanceID, Attested: true,
		},
		CacheID: declaration.ID,
		Scope:   HostCacheScope{Locale: "zh-CN"},
	}
	prepared, err := service.prepare(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.release()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		var cursor uint64
		for {
			keys, next, scanErr := client.Scan(cleanupCtx, cursor, prepared.contractRoot+"*", 100).Result()
			if scanErr != nil {
				return
			}
			if len(keys) > 0 {
				_ = client.Del(cleanupCtx, keys...).Err()
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})

	schema := HostCacheSchema{ID: extensionID + ".value", Version: "1"}
	const entryCount = 256
	for index := range entryCount {
		if _, err := service.Set(ctx, HostCacheSetRequest{
			HostCacheRequestBase: base, Key: fmt.Sprintf("shared-%d", index), Schema: schema,
			Value: []byte(strconv.Itoa(index)), TTL: time.Minute, Tags: tags,
		}); err != nil {
			t.Fatalf("set shared entry %d: %v", index, err)
		}
	}
	invalidated, err := service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: base, Tags: tags,
	})
	if err != nil || invalidated != entryCount {
		t.Fatalf("64-tag shared invalidation=%d err=%v", invalidated, err)
	}

	physicalTags, err := prepared.physicalTags(tags[:1])
	if err != nil || len(physicalTags) != 1 {
		t.Fatalf("physical tag=%#v err=%v", physicalTags, err)
	}
	members := make([]any, hostCacheRedisMaximumTagMembers+1)
	for index := range members {
		members[index] = prepared.valueKey(fmt.Sprintf("unique-limit-%d", index))
	}
	if err := client.SAdd(ctx, physicalTags[0], members...).Err(); err != nil {
		t.Fatalf("seed unique-entry safety cap: %v", err)
	}
	if _, err := backend.InvalidateTags(ctx, physicalTags, prepared.tagPrefix); err == nil {
		t.Fatal("Redis invalidation accepted more than 10,000 unique physical entries")
	}
	if count, err := client.SCard(ctx, physicalTags[0]).Result(); err != nil || count != int64(len(members)) {
		t.Fatalf("unique-entry cap mutated tag count=%d err=%v", count, err)
	}

	unionTags, err := prepared.physicalTags(tags[1:3])
	if err != nil || len(unionTags) != 2 {
		t.Fatalf("union physical tags=%#v err=%v", unionTags, err)
	}
	const membersPerUnionTag = hostCacheRedisMaximumTagMembers/2 + 1
	for tagIndex, tag := range unionTags {
		unionMembers := make([]any, membersPerUnionTag)
		for index := range unionMembers {
			unionMembers[index] = prepared.valueKey(fmt.Sprintf("union-limit-%d-%d", tagIndex, index))
		}
		if err := client.SAdd(ctx, tag, unionMembers...).Err(); err != nil {
			t.Fatalf("seed union tag %d: %v", tagIndex, err)
		}
	}
	if _, err := backend.InvalidateTags(ctx, unionTags, prepared.tagPrefix); err == nil {
		t.Fatal("Redis invalidation accepted a union above 10,000 unique physical entries")
	}
	for index, tag := range unionTags {
		if count, err := client.SCard(ctx, tag).Result(); err != nil || count != membersPerUnionTag {
			t.Fatalf("union cap mutated tag %d count=%d err=%v", index, count, err)
		}
	}
}
