package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
)

func TestRestoreCachePublicationsFailsClosedWithoutManager(t *testing.T) {
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Caches: cacheregistry.New(),
	})
	if err := boundary.RestoreCachePublications(context.Background(), nil, false); !errors.Is(err, ErrLifecycleRegistryPublicationUnavailable) {
		t.Fatalf("missing manager error = %v", err)
	}
}

func TestRestoreCachePublicationsSafeModeKeepsOnlySealedCore(t *testing.T) {
	registry := cacheregistry.New()
	coreArtifact, err := cacheregistry.NewCoreArtifact(
		"core.cache.bootstrap", "1.0.0", strings.Repeat("a", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	core := cacheregistry.Publication{Artifact: coreArtifact, Caches: []cacheregistry.Declaration{{
		ID: "core.cache.bootstrap.items", ContractVersion: "core.cache.bootstrap.items@1",
		Namespace: "core.cache.bootstrap.items", Policy: cacheregistry.PolicyPublic,
	}}}
	plugin := cacheregistry.Publication{Artifact: cacheregistry.Artifact{
		ExtensionID: "third.party.cache", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("b", 64), VersionID: 1, RuntimeInstanceID: "runtime-a",
	}, Caches: []cacheregistry.Declaration{{
		ID: "third.party.cache.items", ContractVersion: "third.party.cache.items@1",
		Namespace: "third.party.cache.items", Policy: cacheregistry.PolicyPrivate,
	}}}
	if _, err := registry.ReplaceAllIfRevision(0, []cacheregistry.Publication{core, plugin}, false); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{}), Caches: registry,
	})
	if err := boundary.RestoreCachePublications(context.Background(), nil, true); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if !snapshot.SafeMode || len(snapshot.Publications) != 1 ||
		snapshot.Publications[0].Artifact != coreArtifact || len(snapshot.Caches) != 1 {
		t.Fatalf("Safe Mode Cache snapshot = %#v", snapshot)
	}
	if _, err := registry.Resolve(core.Caches[0].ID); err != nil {
		t.Fatalf("resolve sealed Core cache: %v", err)
	}
}
