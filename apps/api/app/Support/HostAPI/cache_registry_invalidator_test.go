package hostapi

import (
	"context"
	"errors"
	"testing"
	"time"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestHostCacheDeclaredInvalidatorProducesTagAudit(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "audit.cache", cacheregistry.PolicyPublic, "")
	inspector := NewHostCacheInspector(16)
	service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		hostCacheTestServiceOptions(WithHostCacheTraceSink(inspector))...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "tagged", Schema: fixture.schema,
		Value: []byte("tagged"), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
	}); err != nil {
		t.Fatal(err)
	}
	count, err := service.InvalidateDeclared(context.Background(), HostCacheInvalidatorRequest{
		HostCacheRequestBase: fixture.base, InvalidatorID: fixture.cache.Invalidators[0],
	})
	if err != nil || count != 1 {
		t.Fatalf("declared invalidator count=%d err=%v", count, err)
	}
	entries := inspector.Snapshot()
	last := entries[len(entries)-1]
	if last.Operation != "invalidate_declared" || last.InvalidatorID != fixture.cache.Invalidators[0] ||
		last.TagCount != len(fixture.cache.Tags) || len(last.TagDigest) != 64 || last.Affected != 1 {
		t.Fatalf("invalidator trace = %#v", last)
	}
	if _, err := service.InvalidateDeclared(context.Background(), HostCacheInvalidatorRequest{
		HostCacheRequestBase: fixture.base, InvalidatorID: "other.cache.invalidate",
	}); !errors.Is(err, ErrHostCacheDenied) {
		t.Fatalf("undeclared invalidator = %v", err)
	}
}

func TestHostCacheDeclaredInvalidatorAuditsCommittedCountAfterFinalFence(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "audit-fence.cache", cacheregistry.PolicyPublic, "")
	inspector := NewHostCacheInspector(16)
	service, err := NewHostCacheService(fixture.registry, fixture.backend, nil,
		hostCacheTestServiceOptions(WithHostCacheTraceSink(inspector))...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Set(context.Background(), HostCacheSetRequest{
		HostCacheRequestBase: fixture.base, Key: "tagged", Schema: fixture.schema,
		Value: []byte("tagged"), TTL: time.Minute, Tags: []string{fixture.cache.Tags[0]},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.backend.afterInvalidate = func() {
		if _, replaceErr := fixture.registry.ReplaceAll(nil, true); replaceErr != nil {
			t.Errorf("switch safe mode: %v", replaceErr)
		}
	}
	count, err := service.InvalidateDeclared(context.Background(), HostCacheInvalidatorRequest{
		HostCacheRequestBase: fixture.base, InvalidatorID: fixture.cache.Invalidators[0],
	})
	if !errors.Is(err, ErrHostCacheStale) || count != 0 {
		t.Fatalf("final-fenced invalidator count=%d err=%v", count, err)
	}
	entries := inspector.Snapshot()
	last := entries[len(entries)-1]
	if last.Operation != "invalidate_declared" || last.Outcome != HostCacheTraceStale || last.Affected != 1 {
		t.Fatalf("final-fenced invalidator trace = %#v", last)
	}
}
