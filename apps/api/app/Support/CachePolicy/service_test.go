package cachepolicy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestPlanKeyIsolatesActorAndRevision(t *testing.T) {
	service := newTestService(t, "demo.cache.feed", cacheregistry.PolicyActor, []string{"demo.cache.tag.feed"})

	base := KeyRequest{
		CacheID: "demo.cache.feed", Namespace: "demo.cache.feed.namespace",
		RouteID: "route.topic.show", PageID: "page.topic",
		ActorFingerprint: "actor:1", LocaleFingerprint: "zh-CN",
		ThemeRevision: "theme-a", PluginRevision: "plugin-a",
	}
	planA, err := service.PlanKey(base)
	if err != nil {
		t.Fatal(err)
	}
	if planA.Key == "" || planA.Provider != ProviderMemory || planA.Directive != DirectiveStore {
		t.Fatalf("plan A = %#v", planA)
	}
	if len(planA.Tags) != 1 || planA.Tags[0] != "demo.cache.tag.feed" {
		t.Fatalf("tags = %#v", planA.Tags)
	}

	// Different actor must not share keys under actor policy.
	otherActor := base
	otherActor.ActorFingerprint = "actor:2"
	planB, err := service.PlanKey(otherActor)
	if err != nil {
		t.Fatal(err)
	}
	if planA.Key == planB.Key {
		t.Fatal("actor isolation failed: same key for different actors")
	}

	// Theme revision change must force a miss (different key material).
	themeB := base
	themeB.ThemeRevision = "theme-b"
	planC, err := service.PlanKey(themeB)
	if err != nil {
		t.Fatal(err)
	}
	if planA.Key == planC.Key {
		t.Fatal("theme revision did not change key material")
	}

	// Wrong namespace pin is rejected after Registry resolution.
	wrongNS := base
	wrongNS.Namespace = "demo.cache.other.namespace"
	if _, err := service.PlanKey(wrongNS); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong namespace = %v", err)
	}
}

func TestGetSetMissHitAndBypassNoStore(t *testing.T) {
	service := newTestService(t, "demo.cache.public", cacheregistry.PolicyPublic, []string{"demo.cache.tag.public"})
	ctx := context.Background()

	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.public", Namespace: "demo.cache.public.namespace",
		RouteID: "route.home", TTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("expected miss: found=%t err=%v", found, err)
	}
	if err := service.Set(ctx, plan, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	value, found, err := service.Get(ctx, plan)
	if err != nil || !found || string(value) != `{"ok":true}` {
		t.Fatalf("expected hit: found=%t value=%q err=%v", found, value, err)
	}

	bypass := plan
	bypass.Directive = DirectiveBypass
	if _, _, err := service.Get(ctx, bypass); !errors.Is(err, ErrBypass) {
		t.Fatalf("bypass get = %v", err)
	}
	if err := service.Set(ctx, bypass, []byte("x")); !errors.Is(err, ErrBypass) {
		t.Fatalf("bypass set = %v", err)
	}

	noStore := plan
	noStore.Directive = DirectiveNoStore
	if _, _, err := service.Get(ctx, noStore); !errors.Is(err, ErrNoStore) {
		t.Fatalf("no-store get = %v", err)
	}
	if err := service.Set(ctx, noStore, []byte("x")); !errors.Is(err, ErrNoStore) {
		t.Fatalf("no-store set = %v", err)
	}

	snap := service.Inspector()
	if snap.Metrics.Hits < 1 || snap.Metrics.Misses < 1 || snap.Metrics.Stores < 1 || snap.Metrics.Bypasses < 4 {
		t.Fatalf("metrics incomplete: %#v", snap.Metrics)
	}
}

func TestTagInvalidationAuditAndActorRequired(t *testing.T) {
	service := newTestService(t, "demo.cache.items", cacheregistry.PolicyPublic, []string{"demo.cache.tag.items"})
	ctx := context.Background()

	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.items", Namespace: "demo.cache.items.namespace",
		PageID: "page.list",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || !found {
		t.Fatalf("pre-invalidate hit failed: found=%t err=%v", found, err)
	}

	if _, err := service.Invalidate(ctx, InvalidateRequest{
		Tags: []string{"demo.cache.tag.items"},
	}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("empty actor = %v", err)
	}

	result, err := service.Invalidate(ctx, InvalidateRequest{
		Tags:   []string{"demo.cache.tag.items"},
		Actor:  "admin:1",
		Reason: "entity.updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedKeys != 1 || result.AuditID == "" || result.Actor != "admin:1" {
		t.Fatalf("invalidate result = %#v", result)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("expected miss after tag invalidate: found=%t err=%v", found, err)
	}

	snap := service.Inspector()
	if snap.Metrics.Invalidations != 1 || len(snap.RecentAudit) != 1 {
		t.Fatalf("inspector audit = %#v", snap)
	}
	if snap.RecentAudit[0].Reason != "entity.updated" || snap.RecentAudit[0].DeletedKeys != 1 {
		t.Fatalf("audit row = %#v", snap.RecentAudit[0])
	}
}

func TestProviderSwitchAndSetRevisions(t *testing.T) {
	service := newTestService(t, "demo.cache.rev", cacheregistry.PolicyPublic, nil)
	ctx := context.Background()

	service.SetRevisions("theme-1", "plugin-1")
	plan1, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.rev", Namespace: "demo.cache.rev.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan1, []byte("old")); err != nil {
		t.Fatal(err)
	}

	// Process-local revision bump changes planned keys without caller override.
	service.SetRevisions("theme-2", "plugin-1")
	plan2, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.rev", Namespace: "demo.cache.rev.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan1.Key == plan2.Key {
		t.Fatal("SetRevisions did not change key material")
	}
	if _, found, err := service.Get(ctx, plan2); err != nil || found {
		t.Fatalf("new revision should miss: found=%t err=%v", found, err)
	}

	// Provider switch is Host-owned.
	if err := service.SelectProvider(ProviderNoop, cache.NoopCache{}); err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan2, []byte("noop")); err != nil {
		t.Fatal(err)
	}
	// Noop always misses.
	if _, found, err := service.Get(ctx, plan2); err != nil || found {
		t.Fatalf("noop get: found=%t err=%v", found, err)
	}
	snap := service.Inspector()
	if snap.Provider.Provider != ProviderNoop || snap.Metrics.ThemeRevision != "theme-2" {
		t.Fatalf("inspector provider/revision = %#v", snap)
	}

	if err := service.SelectProvider("postgres", cache.NewMemoryCache()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown provider = %v", err)
	}
}

func TestDeleteExactKeys(t *testing.T) {
	service := newTestService(t, "demo.cache.del", cacheregistry.PolicyPublic, nil)
	ctx := context.Background()
	plan, err := service.PlanKey(KeyRequest{
		CacheID: "demo.cache.del", Namespace: "demo.cache.del.namespace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Set(ctx, plan, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, plan.Key); err != nil {
		t.Fatal(err)
	}
	if _, found, err := service.Get(ctx, plan); err != nil || found {
		t.Fatalf("delete miss: found=%t err=%v", found, err)
	}
}

func TestNewRejectsNilRegistryAndInvalidProvider(t *testing.T) {
	if _, err := New(nil, cache.NewMemoryCache(), ProviderMemory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil registry = %v", err)
	}
	reg := cacheregistry.New()
	if _, err := New(reg, cache.NewMemoryCache(), "disk"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad provider = %v", err)
	}
	// nil backend forces noop provider.
	svc, err := New(reg, nil, ProviderMemory)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Inspector().Provider.Provider != ProviderNoop {
		t.Fatalf("nil backend provider = %#v", svc.Inspector().Provider)
	}
}

func newTestService(t *testing.T, cacheID, policy string, tags []string) *Service {
	t.Helper()
	publication := testPublication("demo.cache", false, 'c')
	// Host exact-runtime admission is required for third-party Plan/Resolve.
	registry := cacheregistry.New().WithPluginAdmission(func(artifact cacheregistry.Artifact) bool {
		return artifact == publication.Artifact
	})
	decl := cacheregistry.Declaration{
		ID: cacheID, ContractVersion: cacheID + "@1",
		Namespace: cacheID + ".namespace", Policy: policy, Tags: tags,
	}
	if len(tags) == 0 {
		decl.Tags = nil
	}
	publication.Caches = []cacheregistry.Declaration{decl}
	if _, err := registry.Publish(publication); err != nil {
		t.Fatalf("publish cache declaration: %v", err)
	}
	service, err := New(registry, cache.NewMemoryCache(), ProviderMemory)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testPublication(extensionID string, core bool, digest byte) cacheregistry.Publication {
	artifact := cacheregistry.Artifact{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat(string(digest), 64),
	}
	if core {
		var err error
		artifact, err = cacheregistry.NewCoreArtifact(artifact.ExtensionID, artifact.ExtensionVersion, artifact.PackageDigest)
		if err != nil {
			panic(err)
		}
	} else {
		artifact.VersionID = 1
		artifact.RuntimeInstanceID = "runtime-" + strings.ReplaceAll(extensionID, ".", "-")
	}
	return cacheregistry.Publication{Artifact: artifact}
}
