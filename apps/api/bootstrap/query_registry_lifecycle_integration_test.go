package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionQueryLifecycleUpgradeJoined(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production Query subprocess lifecycle upgrade gate in short mode")
	}

	source := enableProductionQueryLifecycleFixture(t, buildProductionQueryForceOwner(t))
	target := buildProductionQueryLifecycleTarget(t, source)
	settings := productionQueryForceSettings{
		source.ID: filepath.Join(t.TempDir(), "query.ready"),
	}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: productionQueryForceTrust{}, Settings: settings,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), source); err != nil {
		t.Fatalf("start source Query runtime: %v", err)
	}
	sourceRuntime, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	var targetRuntime extensionsruntime.RuntimeInstanceSnapshot
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, identity := range []extensionsruntime.RuntimeInstanceIdentity{
			targetRuntime.Identity, sourceRuntime.Identity,
		} {
			if identity.ExtensionID == "" || identity.InstanceID == "" {
				continue
			}
			if err := manager.StopRuntimeInstance(ctx, identity); err != nil {
				_ = starter.StopInstance(ctx, identity)
			}
		}
	})

	registry, catalog, err := hostapi.NewQueryRegistryCoreRegistry(hostapi.QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	routeSchemas, err := extensionopenapi.NewRouteSchemaContractPublication(nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := &productionQueryLifecyclePublicationRepository{}
	boundary := extensionsruntime.NewPostgresLifecycleBoundaryRegistries(
		extensionsruntime.LifecycleRegistryBoundaryConfig{
			Repository:   repository,
			Manager:      manager,
			Pages:        pages.NewRegistry(nil),
			Routes:       routes.NewRegistry(),
			RouteSchemas: routeSchemas,
			Services:     hostapi.NewServiceRegistry(),
			Queries:      registry,
		},
	)
	if err := boundary.RestoreRoutePublications(t.Context(), []extensions.Extension{source}, false); err != nil {
		t.Fatalf("restore source lifecycle publication: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig("postgres://postgres:postgres@127.0.0.1:1/sforum?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	stableRuntime, err := hostapi.NewPostgresProtocolV2QueryRuntime(pool, productionQueryAuthorityResolverStub{})
	if err != nil {
		t.Fatal(err)
	}
	cache := newProductionQueryLifecycleCache()
	gateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = gateway.Close() })
	bound, err := bindProductionQueryRegistryWithCache(
		registry, catalog, stableRuntime, &productionQueryActorStoreStub{}, manager, gateway,
		productionQueryTraceSinkStub{}, cache,
	)
	if err != nil {
		t.Fatal(err)
	}

	request := queryregistry.PlanRequest{
		QueryID:    productionQueryForceQueryID,
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
	}
	first := executeProductionQueryLifecycle(t, bound.Execution, request)
	second := executeProductionQueryLifecycle(t, bound.Execution, request)
	if first.CacheHit || !second.CacheHit || first.CacheKey == "" || second.CacheKey != first.CacheKey ||
		len(first.Rows) != 1 || first.Rows[0]["title"] != "item-5" ||
		len(second.Rows) != 1 || second.Rows[0]["title"] != "item-5" {
		t.Fatalf("source cache results first=%#v second=%#v", first, second)
	}
	if loads, stores, hits, entries := cache.stats(); loads != 2 || stores != 1 || hits != 1 || entries != 1 {
		t.Fatalf("source cache stats loads=%d stores=%d hits=%d entries=%d", loads, stores, hits, entries)
	}

	targetRuntime, err = manager.StageRuntimeInstance(t.Context(), target)
	if err != nil {
		t.Fatalf("stage target Query runtime: %v", err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), targetRuntime.Identity); err != nil {
		t.Fatalf("health target Query runtime: %v", err)
	}

	const publicationPosition = 8
	path, err := extensions.RecommendedLifecyclePath(extensions.LifecycleMachineUpgrade)
	if err != nil || publicationPosition >= len(path) {
		t.Fatalf("resolve upgrade publication step: %v", err)
	}
	requestBoundary := extensionsruntime.LifecycleBoundaryRequest{
		OperationID: 911,
		Operation:   extensions.LifecycleMachineUpgrade,
		Position:    publicationPosition,
		StepID: fmt.Sprintf(
			"lifecycle.%s.%02d.host.%s",
			extensions.LifecycleMachineUpgrade,
			publicationPosition,
			path[publicationPosition].State,
		),
		Attempt:         1,
		SourceExtension: &source,
		TargetExtension: target,
		SourceBinding:   productionQueryLifecycleBinding(source, sourceRuntime),
		TargetBinding:   productionQueryLifecycleBinding(target, targetRuntime),
	}
	transaction, err := boundary.PrepareLifecycleRegistryPublication(
		t.Context(), requestBoundary, extensionsruntime.LifecycleBoundaryActivate,
	)
	if err != nil {
		t.Fatalf("prepare Query lifecycle publication: %v", err)
	}
	if _, err := manager.BeginDrainContext(t.Context(), sourceRuntime.Identity); err != nil {
		t.Fatalf("drain source Query runtime: %v", err)
	}
	if err := manager.WaitDrain(t.Context(), sourceRuntime.Identity); err != nil {
		t.Fatalf("wait source Query drain: %v", err)
	}
	if _, err := manager.PublishDrainedRuntimeInstance(t.Context(), targetRuntime.Identity); err != nil {
		t.Fatalf("publish drained target Query runtime: %v", err)
	}
	if err := transaction.Publish(t.Context()); err != nil {
		t.Fatalf("publish target Query registry snapshot: %v", err)
	}
	if _, err := bound.Execution.Execute(t.Context(), request); !errors.Is(err, queryregistry.ErrArtifactUnavailable) {
		t.Fatalf("drained target Query execution error=%v", err)
	}
	if loads, stores, hits, entries := cache.stats(); loads != 2 || stores != 1 || hits != 1 || entries != 1 {
		t.Fatalf("drained target touched cache loads=%d stores=%d hits=%d entries=%d", loads, stores, hits, entries)
	}
	if _, err := manager.ResumeRuntimeInstanceContext(t.Context(), targetRuntime.Identity); err != nil {
		t.Fatalf("resume target Query runtime: %v", err)
	}

	publication, found := registry.SnapshotPublication(target.ID)
	wantArtifact := queryregistry.Artifact{
		ExtensionID: target.ID, ExtensionVersion: target.Version,
		PackageDigest: target.PackageDigest, VersionID: target.ActiveVersionID,
		RuntimeInstanceID: targetRuntime.Identity.InstanceID,
	}
	if !found || publication.Artifact != wantArtifact {
		t.Fatalf("target Query publication found=%v got=%#v want=%#v", found, publication.Artifact, wantArtifact)
	}
	if manager.RuntimeInstanceAvailable(sourceRuntime.Identity) || !manager.RuntimeInstanceAvailable(targetRuntime.Identity) {
		t.Fatalf(
			"exact runtime availability source=%v target=%v",
			manager.RuntimeInstanceAvailable(sourceRuntime.Identity),
			manager.RuntimeInstanceAvailable(targetRuntime.Identity),
		)
	}

	third := executeProductionQueryLifecycle(t, bound.Execution, request)
	if third.CacheHit || third.CacheKey == "" || third.CacheKey == first.CacheKey ||
		len(third.Rows) != 1 || third.Rows[0]["title"] != "item-5" {
		t.Fatalf("target cache result=%#v source cache key=%q", third, first.CacheKey)
	}
	fourth := executeProductionQueryLifecycle(t, bound.Execution, request)
	if !fourth.CacheHit || fourth.CacheKey != third.CacheKey ||
		len(fourth.Rows) != 1 || fourth.Rows[0]["title"] != "item-5" {
		t.Fatalf("target cache hit result=%#v", fourth)
	}
	if loads, stores, hits, entries := cache.stats(); loads != 4 || stores != 2 || hits != 2 || entries != 2 {
		t.Fatalf("upgraded cache stats loads=%d stores=%d hits=%d entries=%d", loads, stores, hits, entries)
	}
}

func buildProductionQueryLifecycleTarget(t *testing.T, source extensions.Extension) extensions.Extension {
	t.Helper()
	root := filepath.Join(t.TempDir(), source.ID+"-v2")
	if err := os.CopyFS(root, os.DirFS(source.PackagePath)); err != nil {
		t.Fatalf("copy target Query package: %v", err)
	}
	manifest := source.Manifest
	manifest.Version = "2.0.0"
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("validate target Query manifest: %v", err)
	}
	writeProductionQueryForceManifest(t, root, manifest)
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	target := source
	target.Version = manifest.Version
	target.PackagePath = root
	target.PackageDigest = digest
	target.Manifest = manifest
	target.ActiveVersionID = source.ActiveVersionID + 1
	return target
}

func enableProductionQueryLifecycleFixture(t *testing.T, source extensions.Extension) extensions.Extension {
	t.Helper()
	manifest := source.Manifest
	manifest.Lifecycle = &extensions.ManifestLifecycle{
		ContractVersion: source.ID + ".lifecycle@1",
	}
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("validate source Query lifecycle manifest: %v", err)
	}
	writeProductionQueryForceManifest(t, source.PackagePath, manifest)
	digest, err := extensionpackage.DigestTree(source.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	source.Manifest = manifest
	source.PackageDigest = digest
	return source
}

func productionQueryLifecycleBinding(
	extension extensions.Extension,
	runtime extensionsruntime.RuntimeInstanceSnapshot,
) extensions.LifecycleRuntimeBinding {
	return extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
}

func executeProductionQueryLifecycle(
	t *testing.T,
	runtime *queryregistry.ExecutionRuntime,
	request queryregistry.PlanRequest,
) queryregistry.QueryResult {
	t.Helper()
	result, err := runtime.Execute(t.Context(), request)
	if err != nil {
		t.Fatalf("execute production Query lifecycle request: %v", err)
	}
	return result
}

type productionQueryLifecyclePublicationRepository struct {
	mu       sync.Mutex
	prepared bool
	ref      extensionsruntime.LifecycleRegistryPublicationRef
	phase    extensionsruntime.LifecycleRegistryPublicationPhase
}

func (r *productionQueryLifecyclePublicationRepository) PrepareLifecycleRegistryPublication(
	_ context.Context,
	_ extensionsruntime.PrepareLifecycleRegistryPublicationInput,
) (extensionsruntime.LifecycleRegistryPublicationRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.prepared {
		r.prepared = true
		r.ref = extensionsruntime.LifecycleRegistryPublicationRef{
			OperationID: 911,
			StepID:      "lifecycle.upgrade.08.host.enabled",
			Mode:        extensionsruntime.LifecycleBoundaryActivate,
			Attempt:     1,
		}
		r.phase = extensionsruntime.LifecycleRegistryPublicationSource
	}
	return r.ref, nil
}

func (r *productionQueryLifecyclePublicationRepository) InspectLifecycleRegistryPublication(
	_ context.Context,
	ref extensionsruntime.LifecycleRegistryPublicationRef,
) (extensionsruntime.LifecycleRegistryPublicationPhase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.prepared || ref != r.ref {
		return "", extensionsruntime.ErrLifecycleRegistryPublicationConflict
	}
	return r.phase, nil
}

func (r *productionQueryLifecyclePublicationRepository) MoveLifecycleRegistryPublication(
	_ context.Context,
	ref extensionsruntime.LifecycleRegistryPublicationRef,
	phase extensionsruntime.LifecycleRegistryPublicationPhase,
	move func() error,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.prepared || ref != r.ref {
		return extensionsruntime.ErrLifecycleRegistryPublicationConflict
	}
	if r.phase == phase {
		return nil
	}
	if err := move(); err != nil {
		return err
	}
	r.phase = phase
	return nil
}

type productionQueryLifecycleCacheFence struct {
	key string
}

func (*productionQueryLifecycleCacheFence) QueryResultCacheFenceToken() {}

type productionQueryLifecycleCache struct {
	mu      sync.Mutex
	entries map[string]queryregistry.CachedQueryResult
	loads   int
	stores  int
	hits    int
}

func newProductionQueryLifecycleCache() *productionQueryLifecycleCache {
	return &productionQueryLifecycleCache{entries: make(map[string]queryregistry.CachedQueryResult)}
}

func (c *productionQueryLifecycleCache) LoadQueryResult(
	_ context.Context,
	key string,
	_ []string,
) (queryregistry.CachedQueryResult, queryregistry.QueryResultCacheFence, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	entry, found := c.entries[key]
	if found {
		c.hits++
		return cloneProductionQueryLifecycleCacheEntry(entry), nil, true, nil
	}
	return queryregistry.CachedQueryResult{}, &productionQueryLifecycleCacheFence{key: key}, false, nil
}

func (c *productionQueryLifecycleCache) StoreQueryResult(
	_ context.Context,
	key string,
	entry queryregistry.CachedQueryResult,
	_ []string,
	fence queryregistry.QueryResultCacheFence,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	token, ok := fence.(*productionQueryLifecycleCacheFence)
	if !ok || token == nil || token.key != key || entry.CacheKey != key {
		return queryregistry.ErrCacheFenceConflict
	}
	c.stores++
	c.entries[key] = cloneProductionQueryLifecycleCacheEntry(entry)
	return nil
}

func (c *productionQueryLifecycleCache) stats() (loads, stores, hits, entries int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads, c.stores, c.hits, len(c.entries)
}

func cloneProductionQueryLifecycleCacheEntry(input queryregistry.CachedQueryResult) queryregistry.CachedQueryResult {
	result := input
	result.CacheTags = append([]string(nil), input.CacheTags...)
	result.Rows = make([]queryregistry.QueryRow, len(input.Rows))
	for index, row := range input.Rows {
		result.Rows[index] = make(queryregistry.QueryRow, len(row))
		for key, value := range row {
			result.Rows[index][key] = value
		}
	}
	return result
}
