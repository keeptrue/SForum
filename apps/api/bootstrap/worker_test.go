package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/config"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
)

func TestWorkerStartStopAllowNilClient(t *testing.T) {
	worker := &Worker{}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("start idle worker: %v", err)
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("stop idle worker: %v", err)
	}
}

func TestWorkerCloseRunsCleanupOnce(t *testing.T) {
	calls := 0
	worker := &Worker{
		close: func() {
			calls++
		},
	}

	worker.Close()
	worker.Close()

	if calls != 1 {
		t.Fatalf("expected cleanup once, got %d", calls)
	}
}

func TestPluginJobRuntimeResolverBindsLiveArtifactAndTrust(t *testing.T) {
	item := runtimeSettingsExtension("demo.plugin")
	item.Version = "1.0.0"
	item.PackageDigest = "digest-v1"
	item.Manifest.Jobs = []extensions.ManifestJob{{
		ID: "demo.plugin.job.sync", ContractVersion: "demo.plugin.job.sync@1",
		Name: "demo.sync", Handler: "job.sync", PayloadSchema: "demo.sync.payload@1", RetryPolicy: "bounded",
	}}
	store := &bootstrapExtensionSettingsStore{item: item}
	resolver := &pluginJobRuntimeResolver{store: store, trust: pluginJobTrustStub{grant: "grant-1"}}
	target, err := resolver.ResolvePluginJobRuntime(context.Background(), item.ID, "demo.sync")
	if err != nil {
		t.Fatal(err)
	}
	if target.Contract.ArtifactDigest != item.PackageDigest || target.Contract.JobContract != "demo.plugin.job.sync@1" ||
		target.TrustGrantID != "grant-1" {
		t.Fatalf("target = %#v", target)
	}

	item.Status = extensions.StatusDisabled
	store.item = item
	if _, err := resolver.ResolvePluginJobRuntime(context.Background(), item.ID, "demo.sync"); !errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		t.Fatalf("disabled error = %v", err)
	}
	item.Status = extensions.StatusEnabled
	item.Manifest.Jobs = nil
	store.item = item
	if _, err := resolver.ResolvePluginJobRuntime(context.Background(), item.ID, "demo.sync"); !errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		t.Fatalf("removed declaration error = %v", err)
	}
	item.Manifest.Jobs = []extensions.ManifestJob{{
		ID: "demo.plugin.job.sync", ContractVersion: "demo.plugin.job.sync@1",
		Name: "demo.sync", Handler: "job.sync", PayloadSchema: "demo.sync.payload@1", RetryPolicy: "bounded",
	}}
	store.item = item
	resolver.trust = pluginJobTrustStub{err: extensions.ErrTrustGrantNotFound}
	if _, err := resolver.ResolvePluginJobRuntime(context.Background(), item.ID, "demo.sync"); !errors.Is(err, supportjobs.ErrPluginJobRuntimeStale) {
		t.Fatalf("revoked trust error = %v", err)
	}
}

func TestStandaloneWorkerPublishesExactPluginSchedules(t *testing.T) {
	item := runtimeSettingsExtension("demo.plugin")
	item.Version = "1.0.0"
	item.PackageDigest = "digest-a"
	item.Manifest.Jobs = []extensions.ManifestJob{{
		ID: "demo.plugin.job.sync", ContractVersion: "demo.plugin.job.sync@1",
		Name: "demo.sync", Handler: "job.sync", PayloadSchema: "demo.plugin.sync.payload@1",
		RetryPolicy: "bounded", MaxAttempts: 5, RetryDelaySeconds: 30, ConcurrencyLimit: 2,
	}}
	item.Manifest.Schedules = []extensions.ManifestSchedule{{
		ID: "demo.plugin.schedule.sync", ContractVersion: "demo.plugin.schedule.sync@1",
		JobID: "demo.plugin.job.sync", Cron: "0 3 * * *", Timezone: "UTC",
	}}
	runtime := &countingWorkerRuntime{active: map[string]extensionsruntime.RuntimeInstanceSnapshot{
		item.ID: {
			Identity:         extensionsruntime.RuntimeInstanceIdentity{ExtensionID: item.ID, InstanceID: "instance-a"},
			ExtensionVersion: item.Version, ArtifactDigest: item.PackageDigest, Active: true,
		},
	}}
	registry := supportjobs.NewPluginScheduleAdmissionRegistry()
	if err := publishStandalonePluginSchedules(
		context.Background(), &bootstrapExtensionSettingsStore{item: item}, runtime,
		pluginJobTrustStub{grant: "grant-a"}, registry,
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Snapshot(supportjobs.PluginScheduleRuntimeIdentity{
		ExtensionID: item.ID, ExtensionVersion: item.Version,
		ArtifactDigest: item.PackageDigest, InstanceID: "instance-a",
	})
	if err != nil || !snapshot.Active || len(snapshot.Schedules) != 1 ||
		!snapshot.Schedules[0].Contract.Valid() || snapshot.Schedules[0].TrustGrantID != "grant-a" {
		t.Fatalf("schedule snapshot = %#v, %v", snapshot, err)
	}
}

type pluginJobTrustStub struct {
	grant string
	err   error
}

func (s pluginJobTrustStub) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: s.grant}, s.err
}

// TestResolveWorkerExtensionRuntimeInjectedSkipsStandalone 覆盖 embed 路径：
// 注入 runtime 时不得调用 buildStandalone（否则会二次 Reconcile / 双起插件）。
func TestResolveWorkerExtensionRuntimeInjectedSkipsStandalone(t *testing.T) {
	injected := &countingWorkerRuntime{}
	standaloneBuilds := 0

	runtime, gateway, coordinator, owns, err := resolveWorkerExtensionRuntime(
		workerRuntimeDeps{ExtensionRuntime: injected, OwnsRuntime: false},
		func() (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error) {
			standaloneBuilds++
			return &countingWorkerRuntime{}, &noopHostGateway{}, nil, nil
		},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if runtime != injected {
		t.Fatal("expected injected runtime to be returned")
	}
	if gateway != nil {
		t.Fatal("expected no host gateway when runtime is injected")
	}
	if coordinator != nil {
		t.Fatal("embedded worker must not own a plugin runtime coordinator")
	}
	if owns {
		t.Fatal("expected OwnsRuntime=false for injected runtime")
	}
	if standaloneBuilds != 0 {
		t.Fatalf("expected standalone builder not called, got %d", standaloneBuilds)
	}
}

// TestResolveWorkerExtensionRuntimeStandaloneOwns 覆盖独立 worker：自建并拥有 runtime。
func TestResolveWorkerExtensionRuntimeStandaloneOwns(t *testing.T) {
	standalone := &countingWorkerRuntime{}
	gw := &noopHostGateway{}
	standaloneBuilds := 0

	runtime, gateway, coordinator, owns, err := resolveWorkerExtensionRuntime(
		workerRuntimeDeps{},
		func() (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error) {
			standaloneBuilds++
			return standalone, gw, newInactivePluginRuntimeCoordinatorRuntime(), nil
		},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if runtime != standalone {
		t.Fatal("expected standalone runtime")
	}
	if gateway != gw {
		t.Fatal("expected standalone host gateway")
	}
	if coordinator == nil {
		t.Fatal("expected standalone coordinator handle")
	}
	if !owns {
		t.Fatal("expected OwnsRuntime=true for standalone")
	}
	if standaloneBuilds != 1 {
		t.Fatalf("expected standalone builder once, got %d", standaloneBuilds)
	}
}

func TestStandaloneWorkerRuntimeUsesCipherServiceSettings(t *testing.T) {
	original := newStandaloneWorkerRuntimeManager
	defer func() { newStandaloneWorkerRuntimeManager = original }()
	stubStandaloneWorkerRuntimeCoordinator(t, nil)

	cipher, _ := crypto.NewOptionCipher(strings.Repeat("b", 64))
	enc, _ := cipher.Encrypt("worker-secret")
	item := runtimeSettingsExtension("worker.plugin")
	store := &bootstrapExtensionSettingsStore{
		item:     item,
		settings: map[string]string{"token": enc},
	}
	var got map[string]string
	newStandaloneWorkerRuntimeManager = func(_ extensions.Store, _ extensionsruntime.HostAPIRegistrar, settings extensionsruntime.PluginSettings, _ extensionsruntime.RuntimeTrustSource, _ extensionsruntime.RuntimeDatabaseLeaseRegistry) workerExtensionRuntime {
		var err error
		got, err = settings.ListSettings(context.Background(), item.ID)
		if err != nil {
			t.Fatalf("load standalone worker settings: %v", err)
		}
		return &countingWorkerRuntime{}
	}
	runtime, gateway, coordinator, err := buildStandaloneWorkerExtensionRuntime(
		context.Background(), config.Config{ExtensionRoot: t.TempDir()},
		recordingDatabaseBinderFactory(nil), newRecordingCommandRuntimeBinder(nil),
		store, cipher, nil, nil, "", nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer coordinator.Stop(context.Background())
	runtime.Close(context.Background())
	if got["token"] != "worker-secret" {
		t.Fatalf("standalone worker runtime received %#v", got)
	}
}

func TestStandaloneWorkerSafeModeReconcilesNoExtensions(t *testing.T) {
	original := newStandaloneWorkerRuntimeManager
	defer func() { newStandaloneWorkerRuntimeManager = original }()

	plugin := runtimeSettingsExtension("broken.plugin")
	plugin.Manifest.Backend = extensions.ManifestBackend{Entry: "missing/plugin"}
	store := &bootstrapExtensionSettingsStore{item: plugin}
	runtime := &countingWorkerRuntime{}
	newStandaloneWorkerRuntimeManager = func(_ extensions.Store, _ extensionsruntime.HostAPIRegistrar, _ extensionsruntime.PluginSettings, _ extensionsruntime.RuntimeTrustSource, _ extensionsruntime.RuntimeDatabaseLeaseRegistry) workerExtensionRuntime {
		return runtime
	}
	built, gateway, coordinator, err := buildStandaloneWorkerExtensionRuntime(context.Background(), config.Config{
		SafeMode: true, ExtensionRoot: t.TempDir(),
	}, recordingDatabaseBinderFactory(nil), newRecordingCommandRuntimeBinder(nil), store, nil, nil, nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer coordinator.Stop(context.Background())
	built.Close(context.Background())
	if len(runtime.reconciledItems) != 0 {
		t.Fatalf("safe worker reconciled extensions: %#v", runtime.reconciledItems)
	}
}

func TestStandaloneWorkerBindsDatabaseCatalogBeforeCoordinator(t *testing.T) {
	original := newStandaloneWorkerRuntimeManager
	defer func() { newStandaloneWorkerRuntimeManager = original }()

	plugin := databaseCatalogFixture(t)
	store := &bootstrapExtensionSettingsStore{item: plugin}
	runtime := &countingWorkerRuntime{}
	binder := &recordingDatabaseCatalogBinder{}
	commandBinder := &recordingCommandRuntimeBinder{}
	coordinatorStarts := 0
	stubStandaloneWorkerRuntimeCoordinator(t, func() {
		coordinatorStarts++
		if binder.bindCalls != 1 || len(binder.bound.queries) != 1 || len(binder.bound.executes) != 1 {
			t.Fatalf("database catalog was not bound before coordinator: %#v", binder)
		}
		if commandBinder.bindCalls != 1 {
			t.Fatalf("Host Command catalog was not bound before coordinator: %#v", commandBinder)
		}
	})
	newStandaloneWorkerRuntimeManager = func(_ extensions.Store, _ extensionsruntime.HostAPIRegistrar, _ extensionsruntime.PluginSettings, _ extensionsruntime.RuntimeTrustSource, _ extensionsruntime.RuntimeDatabaseLeaseRegistry) workerExtensionRuntime {
		return runtime
	}
	built, gateway, coordinator, err := buildStandaloneWorkerExtensionRuntime(
		context.Background(), config.Config{ExtensionRoot: t.TempDir()},
		recordingDatabaseBinderFactory(binder), newRecordingCommandRuntimeBinder(commandBinder),
		store, nil, nil, nil, "", nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	defer coordinator.Stop(context.Background())
	built.Close(context.Background())
	if coordinatorStarts != 1 {
		t.Fatalf("coordinator starts = %d", coordinatorStarts)
	}
	if len(runtime.reconciledItems) != 0 {
		t.Fatalf("standalone worker must not use direct Reconcile: %#v", runtime.reconciledItems)
	}
}

func TestStandaloneWorkerRegistersHostCacheBeforeReturningRuntime(t *testing.T) {
	stubStandaloneWorkerRuntimeCoordinator(t, nil)
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	item := runtimeSettingsExtension("worker.cache.bootstrap")
	item.Status = extensions.StatusDisabled
	store := &bootstrapExtensionSettingsStore{item: item}

	runtime, closer, coordinator, err := buildStandaloneWorkerExtensionRuntime(
		context.Background(),
		config.Config{DatabaseURL: "postgres://db/sforum", ExtensionRoot: t.TempDir()},
		recordingDatabaseBinderFactory(nil), newRecordingCommandRuntimeBinder(nil),
		store, nil, nil, nil, validHostCacheInstallationID, client, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = coordinator.Stop(context.Background())
		runtime.Close(context.Background())
		_ = closer.Close()
	})
	gateway, ok := closer.(*hostapi.Gateway)
	if !ok {
		t.Fatalf("standalone gateway = %T", closer)
	}
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, found := server.GetServiceInfo()[hostv2.CacheService_ServiceDesc.ServiceName]; !found {
		t.Fatalf("standalone Protocol V2 services = %#v", server.GetServiceInfo())
	}
}

func TestStandaloneWorkerHostCacheRejectsNonProductionRuntime(t *testing.T) {
	original := newStandaloneWorkerRuntimeManager
	defer func() { newStandaloneWorkerRuntimeManager = original }()
	newStandaloneWorkerRuntimeManager = func(
		extensions.Store,
		extensionsruntime.HostAPIRegistrar,
		extensionsruntime.PluginSettings,
		extensionsruntime.RuntimeTrustSource,
		extensionsruntime.RuntimeDatabaseLeaseRegistry,
	) workerExtensionRuntime {
		return &countingWorkerRuntime{}
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	_, gateway, _, err := buildStandaloneWorkerExtensionRuntime(
		context.Background(),
		config.Config{DatabaseURL: "postgres://db/sforum", ExtensionRoot: t.TempDir()},
		recordingDatabaseBinderFactory(nil), newRecordingCommandRuntimeBinder(nil),
		&bootstrapExtensionSettingsStore{}, nil, nil, nil, validHostCacheInstallationID, client, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "exact production runtime Manager") {
		t.Fatalf("non-production runtime error = %v", err)
	}
	if gateway != nil {
		t.Fatal("failed standalone Host Cache returned a live Gateway")
	}
}

func recordingDatabaseBinderFactory(binder *recordingDatabaseCatalogBinder) protocolV2DatabaseCatalogBinderFactory {
	if binder == nil {
		binder = &recordingDatabaseCatalogBinder{}
	}
	return func(*hostapi.Gateway) protocolV2DatabaseCatalogBinder { return binder }
}

func stubStandaloneWorkerRuntimeCoordinator(t *testing.T, onStart func()) {
	t.Helper()
	original := startStandaloneWorkerRuntimeCoordinator
	startStandaloneWorkerRuntimeCoordinator = func(
		context.Context,
		config.Config,
		extensions.Store,
		workerExtensionRuntime,
		*slog.Logger,
	) (*pluginRuntimeCoordinatorRuntime, error) {
		if onStart != nil {
			onStart()
		}
		return newInactivePluginRuntimeCoordinatorRuntime(), nil
	}
	t.Cleanup(func() { startStandaloneWorkerRuntimeCoordinator = original })
}

type recordingCommandRuntimeBinder struct {
	bindCalls int
}

func newRecordingCommandRuntimeBinder(binder *recordingCommandRuntimeBinder) protocolV2CommandRuntimeBinder {
	if binder == nil {
		binder = &recordingCommandRuntimeBinder{}
	}
	return func(*hostapi.Gateway) error {
		binder.bindCalls++
		return nil
	}
}

// TestEmbedSharedRuntimeSingleStart 模拟 API Reconcile + embed 注入后不再 Start。
// 真实双起根因是 newWorkerWithPool 自建 Manager 再 Reconcile；注入后 Start 计数应保持 1。
func TestEmbedSharedRuntimeSingleStart(t *testing.T) {
	starter := &countingStarter{}
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	plugin := extensions.Extension{
		ID:     "sforum.smtp",
		Type:   extensions.TypePlugin,
		Status: extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "backend/plugin"},
		},
	}

	// API 路径：Reconcile 一次 → Start 一次。
	manager.Reconcile(context.Background(), []extensions.Extension{plugin})
	if starter.starts["sforum.smtp"] != 1 {
		t.Fatalf("after API reconcile expected 1 start, got %d", starter.starts["sforum.smtp"])
	}

	// Embed 路径：注入同一 manager，resolve 不得触发二次 build/Reconcile。
	runtime, _, coordinator, owns, err := resolveWorkerExtensionRuntime(
		workerRuntimeDeps{ExtensionRuntime: manager, OwnsRuntime: false},
		func() (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error) {
			// 若被调用，会再 NewManager+Reconcile，导致第二次 Start。
			t.Fatal("standalone builder must not run when runtime is injected")
			return nil, nil, nil, nil
		},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if owns {
		t.Fatal("embed must not own shared runtime")
	}
	if coordinator != nil {
		t.Fatal("embed must not create a plugin runtime coordinator")
	}
	// 注入后 worker 侧不应再 Reconcile；Start 仍为 1。
	if starter.starts["sforum.smtp"] != 1 {
		t.Fatalf("after embed inject expected still 1 start, got %d", starter.starts["sforum.smtp"])
	}
	// Worker.Close 在 owns=false 时不应 Close runtime；模拟 API 顺序：先 worker close 再 runtime close。
	worker := &Worker{close: nil} // ownsRuntime=false → closeFn nil
	worker.Close()
	if starter.stops["sforum.smtp"] != 0 {
		t.Fatalf("worker close must not stop shared plugin, stops=%d", starter.stops["sforum.smtp"])
	}
	runtime.Close(context.Background())
	if starter.stops["sforum.smtp"] != 1 {
		t.Fatalf("API close should stop plugin once, got %d", starter.stops["sforum.smtp"])
	}
}

type countingWorkerRuntime struct {
	closeCalls      int
	reconciledItems []extensions.Extension
	onReconcile     func()
	active          map[string]extensionsruntime.RuntimeInstanceSnapshot
}

func (c *countingWorkerRuntime) ActiveRuntimeInstance(extensionID string) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	if snapshot, ok := c.active[extensionID]; ok {
		return snapshot, nil
	}
	return extensionsruntime.RuntimeInstanceSnapshot{}, extensionsruntime.ErrRuntimeInstanceNotFound
}

func (c *countingWorkerRuntime) ProtocolV2ProviderBroker() (hostapi.ProtocolV2ProviderBroker, error) {
	return workerProviderBrokerStub{}, nil
}

type workerProviderBrokerStub struct{}

func (workerProviderBrokerStub) InvokeProtocolV2Provider(context.Context, hostapi.ProtocolV2ProviderInvocation) (hostapi.ProtocolV2ProviderResult, error) {
	return hostapi.ProtocolV2ProviderResult{}, errors.New("worker provider broker test stub")
}

func (c *countingWorkerRuntime) SendMail(context.Context, string, extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error) {
	return extensionsruntime.MailProviderResponse{}, nil
}

func (c *countingWorkerRuntime) StoragePutBegin(context.Context, string, extensionsruntime.StoragePutBeginRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (c *countingWorkerRuntime) StoragePutChunk(context.Context, string, extensionsruntime.StoragePutChunkRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (c *countingWorkerRuntime) StorageOpen(context.Context, string, extensionsruntime.StorageOpenRequest) (extensionsruntime.StorageSessionResponse, error) {
	return extensionsruntime.StorageSessionResponse{}, nil
}
func (c *countingWorkerRuntime) StorageGetChunk(context.Context, string, extensionsruntime.StorageGetChunkRequest) (extensionsruntime.StorageGetChunkResponse, error) {
	return extensionsruntime.StorageGetChunkResponse{}, nil
}
func (c *countingWorkerRuntime) StorageClose(context.Context, string, extensionsruntime.StorageCloseRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (c *countingWorkerRuntime) StorageDelete(context.Context, string, extensionsruntime.StorageObjectRequest) (extensionsruntime.StorageResult, error) {
	return extensionsruntime.StorageResult{}, nil
}
func (c *countingWorkerRuntime) StorageStat(context.Context, string, extensionsruntime.StorageStatRequest) (extensionsruntime.StorageStatResponse, error) {
	return extensionsruntime.StorageStatResponse{}, nil
}
func (c *countingWorkerRuntime) StorageExists(context.Context, string, extensionsruntime.StorageExistsRequest) (extensionsruntime.StorageExistsResponse, error) {
	return extensionsruntime.StorageExistsResponse{}, nil
}
func (c *countingWorkerRuntime) StoragePublicURL(context.Context, string, extensionsruntime.StoragePublicURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (c *countingWorkerRuntime) StorageSignedURL(context.Context, string, extensionsruntime.StorageSignedURLRequest) (extensionsruntime.StorageURLResponse, error) {
	return extensionsruntime.StorageURLResponse{}, nil
}
func (c *countingWorkerRuntime) StorageProbe(context.Context, string, extensionsruntime.StorageProbeRequest) (extensionsruntime.StorageProbeResponse, error) {
	return extensionsruntime.StorageProbeResponse{}, nil
}
func (c *countingWorkerRuntime) SearchEngineProbe(context.Context, string, extensionsruntime.SearchEngineProbeRequest) (extensionsruntime.SearchEngineProbeResponse, error) {
	return extensionsruntime.SearchEngineProbeResponse{}, nil
}
func (c *countingWorkerRuntime) SearchEngineEnsure(context.Context, string) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (c *countingWorkerRuntime) SearchEngineIndex(context.Context, string, extensionsruntime.SearchEngineIndexRequest) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (c *countingWorkerRuntime) SearchEngineDelete(context.Context, string, extensionsruntime.SearchEngineDeleteRequest) (extensionsruntime.SearchEngineResult, error) {
	return extensionsruntime.SearchEngineResult{}, nil
}
func (c *countingWorkerRuntime) SearchEngineSearch(context.Context, string, extensionsruntime.SearchEngineSearchRequest) (extensionsruntime.SearchEngineSearchResponse, error) {
	return extensionsruntime.SearchEngineSearchResponse{}, nil
}

func (c *countingWorkerRuntime) Reconcile(_ context.Context, items []extensions.Extension) {
	if c.onReconcile != nil {
		c.onReconcile()
	}
	c.reconciledItems = append([]extensions.Extension(nil), items...)
}

func (c *countingWorkerRuntime) Close(context.Context) { c.closeCalls++ }

type noopHostGateway struct{}

func (noopHostGateway) Close() error { return nil }

type countingStarter struct {
	starts map[string]int
	stops  map[string]int
}

type bootstrapExtensionSettingsStore struct {
	extensions.Store
	item     extensions.Extension
	settings map[string]string
}

func (s *bootstrapExtensionSettingsStore) List(context.Context) ([]extensions.Extension, error) {
	return []extensions.Extension{s.item}, nil
}

func (s *bootstrapExtensionSettingsStore) Get(context.Context, string) (extensions.Extension, error) {
	return s.item, nil
}

func (s *bootstrapExtensionSettingsStore) ListSettings(context.Context, string) (map[string]string, error) {
	return s.settings, nil
}

func runtimeSettingsExtension(id string) extensions.Extension {
	return extensions.Extension{
		ID:     id,
		Type:   extensions.TypePlugin,
		Status: extensions.StatusEnabled,
		Manifest: extensions.Manifest{
			Settings: []extensions.ManifestSetting{{Key: "token", Type: "secret"}},
		},
	}
}

func (s *countingStarter) Start(_ context.Context, extension extensions.Extension) (extensionsruntime.RouteTarget, error) {
	if s.starts == nil {
		s.starts = map[string]int{}
	}
	s.starts[extension.ID]++
	return extensionsruntime.RouteTarget{}, nil
}

func (s *countingStarter) Stop(_ context.Context, extension extensions.Extension) error {
	if s.stops == nil {
		s.stops = map[string]int{}
	}
	s.stops[extension.ID]++
	return nil
}
