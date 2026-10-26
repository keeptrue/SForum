package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	componentcatalog "github.com/keeptrue/sforum/apps/api/app/Support/ComponentCatalog"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type lifecycleFeatureFacts struct{}

func (lifecycleFeatureFacts) MissingRequiredFeatures(context.Context, []string) ([]string, error) {
	return nil, nil
}

type lifecycleRiverClient struct{}

func (lifecycleRiverClient) InsertTx(
	context.Context,
	pgx.Tx,
	river.JobArgs,
	*river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	return nil, nil
}

func (lifecycleRiverClient) JobCancelTx(context.Context, pgx.Tx, int64) (*rivertype.JobRow, error) {
	return nil, nil
}

type lifecycleMigrationEngine struct{}

func (lifecycleMigrationEngine) ReconcileLifecycleMigration(
	context.Context,
	extensionsruntime.LifecycleMigrationEnginePlan,
) error {
	return nil
}

func (lifecycleMigrationEngine) InspectLifecycleMigration(
	context.Context,
	extensionsruntime.LifecycleMigrationEnginePlan,
) (extensionsruntime.LifecycleMigrationEngineProof, error) {
	return extensionsruntime.LifecycleMigrationEngineProof{}, nil
}

type lifecycleDatabaseDisposition struct{}

func (lifecycleDatabaseDisposition) ApplyLifecycleDataDisposition(
	context.Context,
	extensionsruntime.ExtensionDatabaseDispositionRequest,
) (extensionsruntime.ExtensionDatabaseDispositionReceipt, error) {
	return extensionsruntime.ExtensionDatabaseDispositionReceipt{}, nil
}

type bootstrapIdentityPublicationStore struct{}

func (bootstrapIdentityPublicationStore) Reconcile(
	context.Context,
	identityregistry.ReconcilePublicationInput,
) (identityregistry.DurableState, error) {
	return identityregistry.DurableState{}, nil
}

func (bootstrapIdentityPublicationStore) LoadDurableState(context.Context) (identityregistry.DurableState, error) {
	return identityregistry.DurableState{}, nil
}

func newBootstrapLifecycleStack(t *testing.T) (*productionLifecycleStack, *extensionsruntime.Manager, *extensions.PostgresStore) {
	return newBootstrapLifecycleStackWithSafeMode(t, false)
}

func TestProductionLifecycleStackDefaultIdentityStoreBindsTrustImpactValidator(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := extensions.NewPostgresStore(pool)
	trust := extensions.NewExecutableTrustService(store, extensions.NewPostgresExecutableTrustStore(pool))
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{}),
	})
	// Leave IdentityStore nil so production constructs the default PostgreSQL store.
	stack, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: store, Features: lifecycleFeatureFacts{}, Trust: trust,
		Runtime: manager, Pages: pages.NewRegistry(nil), Services: hostapi.NewServiceRegistry(),
		Caches: cacheregistry.New(), River: lifecycleRiverClient{},
		MigrationEngine: lifecycleMigrationEngine{}, ExtensionRoot: t.TempDir(), QueryCursorSecret: bootstrapQueryCursorSecret(),
		Database: lifecycleDatabaseDisposition{},
	})
	if err != nil {
		t.Fatalf("new production lifecycle stack: %v", err)
	}
	postgresStore, ok := stack.IdentityStore.(*identityregistry.PostgresStore)
	if !ok || postgresStore == nil {
		t.Fatalf("default IdentityStore type=%T want *identityregistry.PostgresStore", stack.IdentityStore)
	}
	if !postgresStore.HasStoredTrustImpactValidator() {
		t.Fatal("production default IdentityStore must bind ValidateStoredTrustImpact")
	}
	if !postgresStore.HasSessionPolicyLifecycleInvalidator() ||
		!postgresStore.HasSessionPolicyLifecycleMutationGate() || stack.SessionPolicyStore == nil {
		t.Fatal("production default IdentityStore must bind the shared Session Policy lifecycle invalidator")
	}
	if seam := identityregistry.NewPostgresStore(pool); seam.HasSessionPolicyLifecycleInvalidator() ||
		seam.HasSessionPolicyLifecycleMutationGate() {
		t.Fatal("ordinary NewPostgresStore must not invent a cross-module lifecycle dependency")
	}
	if identityregistry.NewPostgresStore(pool).HasStoredTrustImpactValidator() {
		t.Fatal("ordinary NewPostgresStore must remain adoption-fail-closed")
	}

	// Injected test seams stay untouched (no forced Postgres adopter).
	seam := bootstrapIdentityPublicationStore{}
	seamStack, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: store, Features: lifecycleFeatureFacts{}, Trust: trust,
		Runtime: manager, Pages: pages.NewRegistry(nil), Services: hostapi.NewServiceRegistry(),
		Caches: cacheregistry.New(), IdentityStore: seam, River: lifecycleRiverClient{},
		MigrationEngine: lifecycleMigrationEngine{}, ExtensionRoot: t.TempDir(), QueryCursorSecret: bootstrapQueryCursorSecret(),
		Database: lifecycleDatabaseDisposition{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seamStack.IdentityStore.(bootstrapIdentityPublicationStore); !ok {
		t.Fatalf("injected IdentityStore replaced: %T", seamStack.IdentityStore)
	}
	if seamStack.SessionPolicyStore != nil {
		t.Fatal("injected IdentityStore must not be paired with an unrelated Session Policy store")
	}
}

func newBootstrapLifecycleStackWithSafeMode(
	t *testing.T,
	safeMode bool,
) (*productionLifecycleStack, *extensionsruntime.Manager, *extensions.PostgresStore) {
	t.Helper()
	pool := &pgxpool.Pool{}
	store := extensions.NewPostgresStore(pool)
	trust := extensions.NewExecutableTrustService(store, extensions.NewPostgresExecutableTrustStore(pool))
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{}),
	})
	stack, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: store, Features: lifecycleFeatureFacts{}, Trust: trust,
		Runtime: manager, Pages: pages.NewRegistry(nil), Services: hostapi.NewServiceRegistry(),
		Caches: cacheregistry.New(), IdentityStore: bootstrapIdentityPublicationStore{},
		NotificationRegistry: notifications.NewRegistry(),
		River:                lifecycleRiverClient{}, MigrationEngine: lifecycleMigrationEngine{},
		ExtensionRoot: t.TempDir(), QueryCursorSecret: bootstrapQueryCursorSecret(), Database: lifecycleDatabaseDisposition{}, SafeMode: safeMode,
	})
	if err != nil {
		t.Fatalf("new production lifecycle stack: %v", err)
	}
	return stack, manager, store
}

func TestProductionLifecycleStackConstructsEveryRequiredDependency(t *testing.T) {
	stack, manager, _ := newBootstrapLifecycleStack(t)

	checks := map[string]bool{
		"repository": stack.Repository != nil, "runtime": stack.Runtime != nil,
		"preflight":        stack.Preflight != nil && stack.StaticPreflight != nil,
		"migration engine": stack.MigrationEngine != nil, "migrations": stack.Migrations != nil,
		"schedules": stack.Schedules != nil,
		"job store": stack.JobStore != nil, "job coordinator": stack.JobCoordinator != nil,
		"jobs": stack.Jobs != nil, "route registry": stack.RouteRegistry != nil,
		"route schemas":         stack.RouteSchemas != nil,
		"component registry":    stack.ComponentRegistry != nil,
		"component composition": stack.ComponentComposition != nil,
		"asset registry":        stack.AssetRegistry != nil,
		"cache registry":        stack.CacheRegistry != nil,
		"identity registry":     stack.IdentityRegistry != nil,
		"identity store":        stack.IdentityStore != nil,
		"query registry":        stack.QueryRegistry != nil,
		"query core catalog":    stack.QueryCoreCatalog != nil,
		"seo registry":          stack.SEORegistry != nil,
		"navigation registry":   stack.NavigationRegistry != nil,
		"content registry":      stack.ContentRegistry != nil,
		"media registry":        stack.MediaRegistry != nil,
		"editor registry":       stack.EditorRegistry != nil,
		"entity registry":       stack.EntityRegistry != nil,
		"route providers":       stack.RouteProviders != nil,
		"registry repository":   stack.RegistryRepository != nil, "registries": stack.Registries != nil,
		"state": stack.State != nil, "journal": stack.PublicationJournal != nil,
		"cleanup": stack.Cleanup != nil, "cleanup purger": stack.CleanupPurger != nil,
		"database disposition": stack.Database != nil,
		"cleanup finalizer":    stack.CleanupFinalizer != nil, "boundary": stack.Boundary != nil,
		"host": stack.Host != nil, "coordinator": stack.Coordinator != nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Fatalf("production lifecycle dependency %q is nil", name)
		}
	}
	if stack.RuntimeManager != manager {
		t.Fatal("coordinator stack did not retain the exact API runtime Manager")
	}
	if stack.Registries.ComponentRegistry() != stack.ComponentRegistry {
		t.Fatal("lifecycle boundary and production stack use different Component Registry instances")
	}
	if stack.Registries.AssetRegistry() != stack.AssetRegistry {
		t.Fatal("lifecycle boundary and production stack use different Asset Registry instances")
	}
	if stack.Registries.QueryRegistry() != stack.QueryRegistry {
		t.Fatal("lifecycle boundary and production stack use different Query Registry instances")
	}
	if stack.Registries.CacheRegistry() != stack.CacheRegistry {
		t.Fatal("lifecycle boundary and production stack use different Cache Registry instances")
	}
	if stack.Registries.IdentityRegistry() != stack.IdentityRegistry {
		t.Fatal("lifecycle boundary and production stack use different Identity Registry instances")
	}
	if stack.Registries.NavigationRegistry() != stack.NavigationRegistry {
		t.Fatal("lifecycle boundary and production stack use different Navigation Registry instances")
	}
	if stack.Registries.ContentRegistry() != stack.ContentRegistry {
		t.Fatal("lifecycle boundary and production stack use different Content Registry instances")
	}
	if stack.Registries.MediaRegistry() != stack.MediaRegistry {
		t.Fatal("lifecycle boundary and production stack use different Media Registry instances")
	}
	if stack.Registries.EditorRegistry() != stack.EditorRegistry {
		t.Fatal("lifecycle boundary and production stack use different Editor Registry instances")
	}
	if stack.Registries.EntityRegistry() != stack.EntityRegistry {
		t.Fatal("lifecycle boundary and production stack use different Entity Registry instances")
	}
	navigationSnapshot := stack.NavigationRegistry.Snapshot()
	if navigationSnapshot.SafeMode || len(navigationSnapshot.Publications) != 1 ||
		!navigationSnapshot.Publications[0].Artifact.Core {
		t.Fatalf("production core navigation snapshot = %#v", navigationSnapshot)
	}
	contentSnapshot := stack.ContentRegistry.Snapshot()
	if contentSnapshot.SafeMode || len(contentSnapshot.Publications) != 1 ||
		!contentSnapshot.Publications[0].Artifact.Core || len(contentSnapshot.Content) != 1 ||
		contentSnapshot.Content[0].ID != contentregistry.HostPostBodyTargetID {
		t.Fatalf("production content registry snapshot = %#v", contentSnapshot)
	}
	mediaSnapshot := stack.MediaRegistry.Snapshot()
	if mediaSnapshot.SafeMode || len(mediaSnapshot.Publications) != 0 {
		t.Fatalf("production media registry snapshot = %#v", mediaSnapshot)
	}
	editorSnapshot := stack.EditorRegistry.Snapshot()
	if editorSnapshot.SafeMode || len(editorSnapshot.Publications) != 0 {
		t.Fatalf("production editor registry snapshot = %#v", editorSnapshot)
	}
	entitySnapshot := stack.EntityRegistry.Snapshot()
	if entitySnapshot.SafeMode || len(entitySnapshot.Publications) != 0 {
		t.Fatalf("production entity registry snapshot = %#v", entitySnapshot)
	}
	snapshot := stack.RouteRegistry.Snapshot()
	if snapshot.Revision != 1 || snapshot.SafeMode || len(snapshot.Routes) != len(routes.CoreRouteCatalog()) ||
		len(snapshot.Conflicts) != 0 {
		t.Fatalf("production core route snapshot = revision %d safe=%t routes=%d conflicts=%#v",
			snapshot.Revision, snapshot.SafeMode, len(snapshot.Routes), snapshot.Conflicts)
	}
	for _, routeID := range []string{"core.route.system.health", "core.route.system.ready"} {
		found := false
		for _, route := range snapshot.Routes {
			if route.ID == routeID && route.Provider.Kind == routes.ProviderCore {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production core catalog omitted Host route %q", routeID)
		}
	}

	// lifecycle boundary 必须持有 stack 暴露的同一个 Registry，否则激活发布与
	// 请求解析会落到两份不同的快照。
	boundaryRoutes := reflect.ValueOf(stack.Registries).Elem().FieldByName("routes")
	if boundaryRoutes.IsNil() || boundaryRoutes.Pointer() != reflect.ValueOf(stack.RouteRegistry).Pointer() {
		t.Fatal("lifecycle boundary and production stack use different Route Registry instances")
	}
	boundarySchemas := reflect.ValueOf(stack.Registries).Elem().FieldByName("routeSchemas")
	if boundarySchemas.IsNil() || boundarySchemas.Pointer() != reflect.ValueOf(stack.RouteSchemas).Pointer() {
		t.Fatal("lifecycle boundary and production stack use different Route Schema Publication instances")
	}
}

func TestProductionLifecycleStackRestoresComponentsThroughSharedRegistry(t *testing.T) {
	stack, _, _ := newBootstrapLifecycleStack(t)
	coreArtifact := stack.QueryCoreCatalog.Artifact()
	extension := bootstrapLifecycleComponentExtension(t, "bootstrap.component.normal")
	if err := stack.Registries.RestoreRoutePublications(
		context.Background(), []extensions.Extension{extension}, false,
	); err != nil {
		t.Fatalf("restore production component publication: %v", err)
	}
	runtime, ok := stack.ComponentRegistry.RuntimeSnapshot(extension.ID)
	if !ok || runtime.Extension.PackageDigest != extension.PackageDigest ||
		!strings.HasPrefix(runtime.InstanceID, "host-component-package:") {
		t.Fatalf("restored production component runtime = %#v, %t", runtime, ok)
	}
	if snapshot := stack.ComponentRegistry.Snapshot(); snapshot.Revision != 1 || len(snapshot.Contributions) != 1 {
		t.Fatalf("restored production component snapshot = %#v", snapshot)
	}
	// restore 不得删除或篡改进程启动密封的 Core Query artifact。
	assertProductionLifecycleCoreQueryPreserved(t, stack, coreArtifact, false)
}

func TestProductionLifecycleStackPublishesSealedCoreQueryCatalog(t *testing.T) {
	stack, _, _ := newBootstrapLifecycleStack(t)
	if stack.QueryCoreCatalog == nil || stack.QueryRegistry == nil {
		t.Fatal("production stack missing Query Core catalog or registry")
	}
	if stack.Registries.QueryRegistry() != stack.QueryRegistry {
		t.Fatal("shared Query Registry pointer must equal Registries.QueryRegistry")
	}
	// 推荐默认 cost max 500；空 Options 不发明 cursor secret。
	if stack.QueryCoreCatalog.CostMaximum() != 500 || stack.QueryCoreCatalog.CostPolicy() == nil {
		t.Fatalf("core cost defaults = max %d policy %v",
			stack.QueryCoreCatalog.CostMaximum(), stack.QueryCoreCatalog.CostPolicy() != nil)
	}

	artifact := stack.QueryCoreCatalog.Artifact()
	if !artifact.Core || artifact.ExtensionID != hostapi.QueryRegistryCoreExtensionID {
		t.Fatalf("core artifact = %#v", artifact)
	}
	publication, found := stack.QueryRegistry.SnapshotPublication(hostapi.QueryRegistryCoreExtensionID)
	// 稳定目录条数 tripwire：新增 stable query 必须显式抬这个数（与目录密封摘要互为双保险）。
	coreQueryCount := 10 + len(hostapi.ShortcodeProjectionQueryIDs())
	if !found || publication.Artifact != artifact || len(publication.Queries) != coreQueryCount {
		t.Fatalf("core publication = %#v found=%t", publication, found)
	}
	snapshot := stack.QueryRegistry.Snapshot()
	if snapshot.Revision == 0 || snapshot.SafeMode || len(snapshot.Publications) != 1 || len(snapshot.Queries) != coreQueryCount {
		t.Fatalf("startup query snapshot = %#v", snapshot)
	}

	want := map[string]struct {
		pagination string
	}{
		"core.query.safe_user.by_id":                {pagination: queryregistry.PaginationNone},
		"core.query.public_topics.list":             {pagination: queryregistry.PaginationOffset},
		"core.query.public_topic.by_id":             {pagination: queryregistry.PaginationNone},
		"core.query.public_attachment.by_public_id": {pagination: queryregistry.PaginationNone},
		"core.query.site_stats.current":             {pagination: queryregistry.PaginationNone},
		"core.query.public_comments.list":           {pagination: queryregistry.PaginationOffset},
		"core.query.safe_user.by_username":          {pagination: queryregistry.PaginationNone},
		"core.query.safe_users.batch":               {pagination: queryregistry.PaginationOffset},
		"core.query.public_categories.list":         {pagination: queryregistry.PaginationOffset},
	}
	for _, queryID := range hostapi.ShortcodeProjectionQueryIDs() {
		want[queryID] = struct{ pagination string }{pagination: queryregistry.PaginationOffset}
	}
	for queryID, expect := range want {
		resolved, err := stack.QueryRegistry.Resolve(queryID)
		if err != nil {
			t.Fatalf("resolve %s: %v", queryID, err)
		}
		if resolved.Artifact != artifact ||
			(resolved.PermissionPolicy != queryregistry.PermissionPolicyPublic &&
				!hostapi.IsShortcodeProjectionActorPolicy(resolved.PermissionPolicy)) ||
			resolved.Pagination != expect.pagination {
			t.Fatalf("resolved %s = %#v", queryID, resolved)
		}
		// 尚无 entity-event invalidator：CacheTags 必须保持空，避免虚假缓存命中。
		if resolved.CacheTags != nil && len(resolved.CacheTags) != 0 {
			t.Fatalf("cache tags must stay empty for %s: %#v", queryID, resolved.CacheTags)
		}
	}

	// cost policy 必须实际可 plan；caller MaxCost 只能收紧，不得抬高 site maximum。
	listPlan, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID:    "core.query.public_topics.list",
		Fields:     []string{"id", "title"},
		Filters:    []queryregistry.FilterValue{{Field: "category_id", Value: "1"}},
		Sorts:      []queryregistry.SortValue{{Field: "last_activity_at", Descending: true}},
		Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	if err != nil {
		t.Fatalf("plan list core query: %v", err)
	}
	if listPlan.Pagination.Mode != queryregistry.PaginationOffset ||
		listPlan.Cost.Maximum != 500 || listPlan.Cost.Units <= 0 ||
		listPlan.Query.PermissionPolicy != queryregistry.PermissionPolicyPublic {
		t.Fatalf("list plan = %#v", listPlan)
	}
	raised, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID:    "core.query.public_topics.list",
		Fields:     []string{"id", "title"},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
		MaxCost:    2000,
	})
	if err != nil || raised.Cost.Maximum != 500 {
		t.Fatalf("caller raised cost maximum = %#v err=%v", raised, err)
	}
	capped, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID:    "core.query.public_topics.list",
		Fields:     []string{"id"},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
		MaxCost:    12,
	})
	if err != nil || capped.Cost.Maximum != 12 {
		t.Fatalf("caller lowered cost maximum = %#v err=%v", capped, err)
	}
	single, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID: "core.query.safe_user.by_id",
		Fields:  []string{"id", "username"},
		Filters: []queryregistry.FilterValue{{Field: "id", Value: "1"}},
	})
	if err != nil || single.Pagination.Mode != queryregistry.PaginationNone {
		t.Fatalf("single plan = %#v err=%v", single, err)
	}
	// offset 语义保持：列表声明不得接受 cursor payload。
	if _, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID:    "core.query.public_topics.list",
		Pagination: queryregistry.PaginationRequest{Limit: 2, Cursor: "not-a-cursor"},
	}); err == nil {
		t.Fatal("offset list query accepted cursor payload")
	}
}

func assertProductionLifecycleCoreQueryPreserved(
	t *testing.T,
	stack *productionLifecycleStack,
	wantArtifact queryregistry.Artifact,
	safeMode bool,
) {
	t.Helper()
	publication, found := stack.QueryRegistry.SnapshotPublication(hostapi.QueryRegistryCoreExtensionID)
	if !found || publication.Artifact != wantArtifact || len(publication.Queries) != 10+len(hostapi.ShortcodeProjectionQueryIDs()) {
		t.Fatalf("core query publication after restore = %#v found=%t want=%#v", publication, found, wantArtifact)
	}
	if stack.QueryCoreCatalog.Artifact() != wantArtifact {
		t.Fatalf("stack catalog artifact drifted after restore: %#v", stack.QueryCoreCatalog.Artifact())
	}
	snapshot := stack.QueryRegistry.Snapshot()
	if snapshot.SafeMode != safeMode {
		t.Fatalf("query safe mode after restore = %t want %t", snapshot.SafeMode, safeMode)
	}
	for _, item := range snapshot.Publications {
		if !item.Artifact.Core {
			t.Fatalf("third-party query publication leaked after restore: %#v", item.Artifact)
		}
	}
	if safeMode && len(snapshot.Publications) != 1 {
		t.Fatalf("safe mode query publications = %#v", snapshot.Publications)
	}
}

func TestProductionLifecycleStackSafeModeKeepsHostRoutesAndSkipsThirdParty(t *testing.T) {
	stack, _, _ := newBootstrapLifecycleStackWithSafeMode(t, true)
	if cacheSnapshot := stack.CacheRegistry.Snapshot(); !cacheSnapshot.SafeMode || len(cacheSnapshot.Publications) != 0 {
		t.Fatalf("initial Safe Mode Cache snapshot = %#v", cacheSnapshot)
	}
	coreArtifact := stack.QueryCoreCatalog.Artifact()
	// Safe Mode 进程首个 Query snapshot 必须已保留全部稳定 Core queries。
	assertProductionLifecycleCoreQueryPreserved(t, stack, coreArtifact, true)
	component := bootstrapLifecycleComponentExtension(t, "bootstrap.component.safe-mode")
	if err := stack.ComponentRegistry.ReplaceRuntime(component, "pre-safe-mode-component"); err != nil {
		t.Fatalf("seed pre-Safe-Mode component: %v", err)
	}
	if err := stack.Registries.RestoreRoutePublications(
		context.Background(), []extensions.Extension{component}, true,
	); err != nil {
		t.Fatalf("restore Safe Mode component publication: %v", err)
	}
	if _, ok := stack.ComponentRegistry.RuntimeSnapshot(component.ID); ok {
		t.Fatal("Safe Mode restore retained a third-party component runtime")
	}
	if componentSnapshot := stack.ComponentRegistry.Snapshot(); componentSnapshot.Revision != 2 || len(componentSnapshot.Contributions) != 0 {
		t.Fatalf("Safe Mode component snapshot = %#v", componentSnapshot)
	}
	// Safe Mode restore 过滤第三方后仍保留同一 Core Query artifact，不得删除/篡改。
	assertProductionLifecycleCoreQueryPreserved(t, stack, coreArtifact, true)
	// Core queries 在 Safe Mode 下仍可 plan（Host cost policy 已在启动时注入）。
	if plan, err := stack.QueryRegistry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID: "core.query.public_topic.by_id",
		Fields:  []string{"id", "title"},
		Filters: []queryregistry.FilterValue{{Field: "id", Value: "1"}},
	}); err != nil || plan.Cost.Maximum != 500 || plan.Pagination.Mode != queryregistry.PaginationNone {
		t.Fatalf("safe-mode core plan = %#v err=%v", plan, err)
	}
	publication := stack.RouteRegistry.PublicationSnapshot().Publication
	publication.Plugins = []routes.PluginRouteSet{{
		Artifact: routes.PluginArtifact{
			ExtensionID: "third.party", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), RuntimeInstanceID: "runtime-safe-mode",
		},
		Routes: []extensionmanifest.ManifestRoute{{
			ID: "third.party.route", ContractVersion: "third.party.route@1",
			Action: extensionmanifest.RouteActionAdd, Path: "/third-party", Methods: []string{"GET"},
			Guard: extensionmanifest.GuardCorePublic, Mode: extensionmanifest.RouteModeHTTP,
			Handler: "routes/third-party", ResponseSchema: "third.party.response@1",
		}},
	}}
	snapshot, err := stack.RouteRegistry.Publish(publication)
	if err != nil {
		t.Fatalf("safe-mode route publication: %v", err)
	}
	if !snapshot.SafeMode || len(snapshot.Routes) != len(routes.CoreRouteCatalog()) ||
		len(stack.RouteRegistry.PublicationSnapshot().Publication.Plugins) != 0 {
		t.Fatalf("safe-mode snapshot = safe %t routes %d publication %#v",
			snapshot.SafeMode, len(snapshot.Routes), stack.RouteRegistry.PublicationSnapshot().Publication)
	}
	if _, err := stack.RouteRegistry.Resolve("GET", "/third-party"); !errors.Is(err, routes.ErrRouteNotFound) {
		t.Fatalf("safe mode exposed third-party route: %v", err)
	}
	for path, routeID := range map[string]string{
		"/api/v1/health": "core.route.system.health",
		"/api/v1/ready":  "core.route.system.ready",
	} {
		match, err := stack.RouteRegistry.Resolve("GET", path)
		if err != nil || match.Route.ID != routeID || match.Route.Provider.Kind != routes.ProviderCore {
			t.Fatalf("safe-mode Host route %s = %#v, %v", path, match, err)
		}
	}
	// Safe Mode 下第三方 Query 发布必须被 Registry 拒绝；Core 仍可 inspect。
	if _, err := stack.QueryRegistry.Publish(queryregistry.Publication{
		Artifact: queryregistry.Artifact{
			ExtensionID: "third.party.query", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("b", 64), VersionID: 1, RuntimeInstanceID: "query-safe-mode",
		},
		Queries: []queryregistry.QueryDeclaration{{
			ID: "third.party.query.items", ContractVersion: "third.party.query.items@1",
			Entity: "third.party.item", PlanVersion: "third.party.query.items.plan@1",
			Fields: []string{"id"}, Pagination: queryregistry.PaginationNone,
			ResultSchema: "third.party.query.items.result@1", PermissionPolicy: queryregistry.PermissionPolicyPublic,
		}},
	}); !errors.Is(err, queryregistry.ErrSafeMode) {
		t.Fatalf("safe mode accepted third-party query publication: %v", err)
	}
	assertProductionLifecycleCoreQueryPreserved(t, stack, coreArtifact, true)
}

func bootstrapLifecycleComponentExtension(t *testing.T, id string) extensions.Extension {
	t.Helper()
	target, ok := componentcatalog.FindCoreComponent("core.component.page.forum.home")
	if !ok {
		t.Fatal("reviewed forum home component target is missing")
	}
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: strings.Repeat("a", 64),
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Components: []extensions.ManifestComponent{{
				ID: id + ".component.hide-home", ContractVersion: id + ".component.hide-home@1",
				Action:   extensionmanifest.ComponentActionHide,
				TargetID: target.ID, TargetContractVersion: target.ContractVersion,
			}},
		},
	}
}

func TestProductionLifecycleStackFailsClosedWithoutExactManager(t *testing.T) {
	if _, err := requireProductionExtensionRuntime(nil); !errors.Is(err, errProductionLifecycleDependency) {
		t.Fatalf("nil runtime error = %v", err)
	}
	if _, err := requireProductionExtensionRuntime(fakeBootstrapExtensionRuntime{}); !errors.Is(err, errProductionLifecycleDependency) {
		t.Fatalf("non-Manager runtime error = %v", err)
	}

	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{})
	pool := &pgxpool.Pool{}
	_, err := newProductionLifecycleStack(productionLifecycleStackConfig{
		Pool: pool, Store: extensions.NewPostgresStore(pool),
		Features: lifecycleFeatureFacts{},
		Trust: extensions.NewExecutableTrustService(
			extensions.NewPostgresStore(pool),
			extensions.NewPostgresExecutableTrustStore(pool),
		),
		Runtime: manager, Pages: pages.NewRegistry(nil), Services: hostapi.NewServiceRegistry(),
		Caches: cacheregistry.New(),
		River:  lifecycleRiverClient{}, MigrationEngine: lifecycleMigrationEngine{},
		ExtensionRoot: t.TempDir(), QueryCursorSecret: bootstrapQueryCursorSecret(), Database: lifecycleDatabaseDisposition{},
	})
	if !errors.Is(err, errProductionLifecycleDependency) {
		t.Fatalf("Manager without protocol-v2 exact runner error = %v", err)
	}
}

func bootstrapQueryCursorSecret() []byte {
	return bytes.Repeat([]byte{0x42}, 32)
}

func TestProductionLifecycleStackBindsV2AndInspectionOptions(t *testing.T) {
	stack, _, store := newBootstrapLifecycleStack(t)
	service := extensions.NewService(store, t.TempDir())
	if err := stack.bindService(service); err != nil {
		t.Fatalf("bind lifecycle service: %v", err)
	}

	value := reflect.ValueOf(service).Elem()
	for _, field := range []string{
		"lifecycleCoordinator", "lifecyclePreflight", "lifecycleAuthority", "lifecycleInspector",
		"lifecycleFinalizer", "componentRegistry", "queryPublications", "cachePublications",
	} {
		binding := value.FieldByName(field)
		if !binding.IsValid() || binding.IsNil() {
			t.Fatalf("Service option %q was not wired", field)
		}
	}
	componentBinding := value.FieldByName("componentRegistry")
	if componentBinding.Elem().Pointer() != reflect.ValueOf(stack.ComponentRegistry).Pointer() {
		t.Fatal("theme activation service did not receive the shared production Component Registry")
	}
	queryBinding := value.FieldByName("queryPublications")
	if queryBinding.Elem().Pointer() != reflect.ValueOf(stack.Registries).Pointer() {
		t.Fatal("extension service did not receive the production runtime Query publication boundary")
	}
	cacheBinding := value.FieldByName("cachePublications")
	if cacheBinding.Elem().Pointer() != reflect.ValueOf(stack.Registries).Pointer() {
		t.Fatal("extension service did not receive the production runtime Cache publication boundary")
	}
	if assetBinding := value.FieldByName("assetRegistry"); !assetBinding.IsValid() || !assetBinding.IsNil() {
		t.Fatal("lifecycle bind exposed Asset Registry before authoritative startup restore")
	}
}

func TestProductionLifecycleStackLateBindsOneSharedAssetRegistry(t *testing.T) {
	stack, _, store := newBootstrapLifecycleStack(t)
	service := extensions.NewService(store, t.TempDir())
	if err := stack.bindService(service); err != nil {
		t.Fatal(err)
	}
	trust := extensions.NewExecutableTrustService(
		store,
		extensions.NewPostgresExecutableTrustStore(&pgxpool.Pool{}),
	)
	frontend := extensions.NewFrontendService(store, nil)
	if frontend.PublicAssetRegistry() == stack.AssetRegistry {
		t.Fatal("frontend received lifecycle Asset Registry before late binding")
	}
	before := stack.AssetRegistry.Snapshot()
	if err := stack.bindAssetRegistryConsumers(service, frontend, trust); err != nil {
		t.Fatalf("late bind asset consumers: %v", err)
	}
	serviceRegistry := reflect.ValueOf(service).Elem().FieldByName("assetRegistry")
	trustRegistry := reflect.ValueOf(trust).Elem().FieldByName("publicAssets")
	want := reflect.ValueOf(stack.AssetRegistry).Pointer()
	if serviceRegistry.IsNil() || serviceRegistry.Pointer() != want ||
		trustRegistry.IsNil() || trustRegistry.Pointer() != want ||
		frontend.PublicAssetRegistry() != stack.AssetRegistry {
		t.Fatal("asset consumers did not receive the exact shared lifecycle Registry")
	}
	if after := stack.AssetRegistry.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("late binding mutated Asset graph: before=%#v after=%#v", before, after)
	}
}

type lifecycleCleanupFinalizer struct {
	result extensionsruntime.LifecycleCleanupFinalizationResult
	err    error
}

func (f lifecycleCleanupFinalizer) FinalizeLifecycleHostCleanup(
	context.Context,
	int64,
) (extensionsruntime.LifecycleCleanupFinalizationResult, error) {
	return f.result, f.err
}

func TestLifecycleCleanupFinalizerAdapterMapsOnlyModelsContract(t *testing.T) {
	want := extensionsruntime.LifecycleCleanupFinalizationResult{
		CleanupID: "internal-cleanup", OperationID: 71, Status: "finalized",
		PhysicalPurgeCompleted: true, PurgeReceiptID: "internal-receipt",
		PurgeProofDigest: "internal-proof",
	}
	got, err := adaptLifecycleCleanupFinalizer(lifecycleCleanupFinalizer{result: want})(
		context.Background(),
		want.OperationID,
	)
	if err != nil {
		t.Fatalf("adapt cleanup finalizer: %v", err)
	}
	if got.OperationID != want.OperationID || got.Status != want.Status || !got.PhysicalPurgeComplete {
		t.Fatalf("adapted cleanup result = %#v", got)
	}
}

type lifecycleReconcileStore struct {
	extensions.Store
	items []extensions.Extension
	err   error
}

func (s lifecycleReconcileStore) List(context.Context) ([]extensions.Extension, error) {
	return append([]extensions.Extension(nil), s.items...), s.err
}

type lifecycleReconcileRuntime struct {
	calls int
	items []extensions.Extension
}

func (r *lifecycleReconcileRuntime) Reconcile(_ context.Context, items []extensions.Extension) {
	r.calls++
	r.items = append([]extensions.Extension(nil), items...)
}

func TestReconcileAPIExtensionRuntimeKeepsSafeModeFreeAndReturnsFullNormalInventory(t *testing.T) {
	plugin := extensions.Extension{ID: "demo.safe-mode", Type: extensions.TypePlugin, Status: extensions.StatusEnabled}
	theme := extensions.Extension{ID: "legacy.asset-theme", Type: extensions.TypeTheme, Status: extensions.StatusEnabled}
	want := []extensions.Extension{plugin, theme}
	store := lifecycleReconcileStore{items: want}

	safeRuntime := &lifecycleReconcileRuntime{}
	if items, err := reconcileAPIExtensionRuntime(context.Background(), true, store, safeRuntime); err != nil || len(items) != 0 {
		t.Fatalf("safe mode reconcile: %v", err)
	}
	if safeRuntime.calls != 1 || len(safeRuntime.items) != 0 {
		t.Fatalf("safe mode reconciled %#v", safeRuntime.items)
	}

	normalRuntime := &lifecycleReconcileRuntime{}
	items, err := reconcileAPIExtensionRuntime(context.Background(), false, store, normalRuntime)
	if err != nil {
		t.Fatalf("normal reconcile: %v", err)
	}
	if normalRuntime.calls != 1 || !reflect.DeepEqual(normalRuntime.items, want) ||
		!reflect.DeepEqual(items, want) {
		t.Fatalf("normal mode reconciled %#v", normalRuntime.items)
	}
}
