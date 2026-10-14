package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	aireplyjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/AIReply"
	attachmentjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Attachments"
	auditjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Audit"
	forumjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Forum"
	identityjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Identity"
	notificationjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Notifications"
	queryregistryjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/QueryRegistry"
	searchjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Search"
	webhookjobs "github.com/zhuchunshu/sforum/apps/api/app/Jobs/Webhooks"
	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	attachments "github.com/zhuchunshu/sforum/apps/api/app/Models/Attachments"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	forum "github.com/zhuchunshu/sforum/apps/api/app/Models/Forum"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	jobsmodel "github.com/zhuchunshu/sforum/apps/api/app/Models/Jobs"
	moderation "github.com/zhuchunshu/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/zhuchunshu/sforum/apps/api/app/Models/Notifications"
	options "github.com/zhuchunshu/sforum/apps/api/app/Models/Options"
	webhooks "github.com/zhuchunshu/sforum/apps/api/app/Models/Webhooks"
	"github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
	cacheregistry "github.com/zhuchunshu/sforum/apps/api/app/Support/CacheRegistry"
	"github.com/zhuchunshu/sforum/apps/api/app/Support/Crypto"
	extensionsruntime "github.com/zhuchunshu/sforum/apps/api/app/Support/Extensions"
	health "github.com/zhuchunshu/sforum/apps/api/app/Support/Health"
	hostapi "github.com/zhuchunshu/sforum/apps/api/app/Support/HostAPI"
	humanverify "github.com/zhuchunshu/sforum/apps/api/app/Support/HumanVerify"
	installationidentity "github.com/zhuchunshu/sforum/apps/api/app/Support/InstallationIdentity"
	supportjobs "github.com/zhuchunshu/sforum/apps/api/app/Support/Jobs"
	"github.com/zhuchunshu/sforum/apps/api/app/Support/Postgres"
	secretstore "github.com/zhuchunshu/sforum/apps/api/app/Support/SecretStore"
	"github.com/zhuchunshu/sforum/apps/api/config"
)

type Worker struct {
	Client *supportjobs.Client
	// Schedules 是宿主 schedule catalog（含元数据）；River PeriodicJobs 由其 Build 得到。
	Schedules       *supportjobs.ScheduleRegistry
	PluginSchedules *supportjobs.PluginScheduleAdmissionRegistry
	// failures 只承载独立 worker 所拥有的插件运行时 coordinator 终止错误。
	// embed 与 Safe Mode 不创建 coordinator，因此保持 nil，避免伪造故障源。
	failures <-chan error

	closeOnce sync.Once
	close     func()
}

// workerExtensionRuntime 是 worker 对 extension runtime 的最小依赖面：
// mail.deliver 需要 SendMail；附件清理需要 StorageRuntime；
// 搜索索引需要 SearchRuntime；独立进程还需 Reconcile/Close。
// API embed 注入的 *extensionsruntime.Manager 满足此接口。
type workerExtensionRuntime interface {
	notificationjobs.ProviderSender
	extensionsruntime.StorageRuntime
	extensionsruntime.SearchRuntime
	Reconcile(ctx context.Context, items []extensions.Extension)
	Close(ctx context.Context)
	protocolV2ProviderBrokerSource
}

type workerNotificationChannelRuntime interface {
	InvokeVersionedProvider(context.Context, extensionsruntime.VersionedProviderInvocation) (extensionsruntime.VersionedProviderInvocationResult, error)
}

type workerNotificationChannelAdapter struct {
	runtime workerNotificationChannelRuntime
}

func (a workerNotificationChannelAdapter) InvokeNotificationChannel(ctx context.Context, request notificationjobs.ChannelProviderRequest) (notificationjobs.ChannelProviderResult, error) {
	result, err := a.runtime.InvokeVersionedProvider(ctx, extensionsruntime.VersionedProviderInvocation{
		SlotID: request.SlotID, ContractVersion: request.ContractVersion,
		Operation: extensionsruntime.VersionedProviderOperationInvoke, InputSchema: request.InputSchema,
		Input: request.Input, Revalidate: extensionsruntime.BoundedProviderDocumentRevalidator,
	})
	if errors.Is(err, extensionsruntime.ErrProviderSlotNoProvider) || errors.Is(err, extensionsruntime.ErrProviderSlotSelectionStale) {
		return notificationjobs.ChannelProviderResult{}, errors.Join(notificationjobs.ErrChannelProviderUnavailable, err)
	}
	return notificationjobs.ChannelProviderResult{
		ExtensionID: result.ExtensionID, ArtifactDigest: result.ProviderArtifact.PackageDigest, Output: result.Output,
	}, err
}

// workerRuntimeDeps 控制 extension runtime / Host API 的所有权。
// ExtensionRuntime 非 nil 时复用注入实例（embed 模式），不再二次 Reconcile，也不在 Worker.Close 中关闭。
// ExtensionRuntime 为 nil 时由 worker 自建 Manager + Host API gateway（独立 worker 路径）。
type workerRuntimeDeps struct {
	ExtensionRuntime workerExtensionRuntime
	PluginSchedules  *supportjobs.PluginScheduleAdmissionRegistry
	// BootstrapContext 仅供独立 worker 等待首轮 durable convergence；embed
	// 路径复用 API runtime，不读取该字段。
	BootstrapContext context.Context
	// HostCacheRedis is shared by standalone Host Cache and worker heartbeat.
	// Embed mode leaves it nil because the API-owned Gateway is already bound.
	HostCacheRedis          *redis.Client
	HostCacheInstallationID string
	// QueryInvalidation is worker-owned in both standalone and embedded modes.
	// It must never reuse the API execution-cache client.
	QueryInvalidation *productionQueryInvalidationRuntime
	// AIReplyGenerator 由 embed 路径注入，复用 API 已装配的论坛服务与网关。
	//
	// 独立 worker 刻意不提供它：那边没有论坛装配，临时重建一份会得到独立的
	// 设置与发布策略，导致同样一条评论在 API 侧要过审核、在 worker 侧不过。
	// AI 回复因此只在 embed 模式下启用。
	// 为 nil 时该能力整体关闭，不注册执行器。
	AIReplyGenerator *aireply.Generator
	// AI 回复的判定在 worker 里执行，复用 API 已装配的网关与用途登记表；为 nil
	// 时自主参与整体关闭，mention 触发不受影响。
	AIReplyTriageDecider *aireply.TriageDecider
	// AIReplyThreadReader 供判定读取上下文；与生成器共用同一个 PostgresReader。
	AIReplyThreadReader aireply.ThreadReader
	// AIReplyEnqueuer 供判定判 REPLY 后入队正式生成。
	AIReplyEnqueuer *aireplyjobs.Enqueuer
	// OwnsRuntime 为 true 时 Worker.Close 关闭 runtime（及自建的 Host API gateway）。
	// 注入共享 runtime 时必须为 false，由 API shutdown 负责 Close。
	OwnsRuntime bool
}

// hostAPIGatewayCloser 仅用于 worker 自建 Host API 时的关闭钩子。
type hostAPIGatewayCloser interface {
	Close() error
}

type standaloneWorkerRuntimeCoordinatorStarter func(
	context.Context,
	config.Config,
	extensions.Store,
	workerExtensionRuntime,
	*slog.Logger,
) (*pluginRuntimeCoordinatorRuntime, error)

// startStandaloneWorkerRuntimeCoordinator 保留泛型 Store/runtime 形状仅为
// bootstrap 单测注入；生产路径必须收窄为 PostgresStore + Manager，且只通过
// 现有 exact full-set coordinator 启动插件。
var startStandaloneWorkerRuntimeCoordinator standaloneWorkerRuntimeCoordinatorStarter = func(
	ctx context.Context,
	cfg config.Config,
	store extensions.Store,
	runtime workerExtensionRuntime,
	logger *slog.Logger,
) (*pluginRuntimeCoordinatorRuntime, error) {
	coordinatorConfig := pluginRuntimeCoordinatorBootstrapConfig{
		SafeMode:    cfg.SafeMode,
		ProcessRole: extensions.PluginRuntimeProcessWorker,
		Logger:      logger,
		StopTimeout: cfg.WorkerShutdownTimeout,
	}
	// Safe Mode 必须在具体依赖断言前返回，确保测试和恢复启动都不会触发
	// hostname、genesis、节点注册、LISTEN 或插件进程。
	if cfg.SafeMode {
		return startPluginRuntimeCoordinator(ctx, coordinatorConfig)
	}
	postgresStore, storeOK := store.(*extensions.PostgresStore)
	manager, runtimeOK := runtime.(*extensionsruntime.Manager)
	if !storeOK || postgresStore == nil || !runtimeOK || manager == nil {
		return nil, fmt.Errorf("standalone worker plugin runtime coordinator requires PostgresStore and production Manager")
	}
	coordinatorConfig.Store = postgresStore
	coordinatorConfig.Manager = manager
	return startPluginRuntimeCoordinator(ctx, coordinatorConfig)
}

// workerRuntimeOwner enforces the standalone shutdown order. A terminal
// coordinator failure closes Manager admission immediately, while River and
// the Gateway remain available for the command-level graceful shutdown path.
type workerRuntimeOwner struct {
	runtime     workerExtensionRuntime
	gateway     hostAPIGatewayCloser
	coordinator *pluginRuntimeCoordinatorRuntime
	logger      *slog.Logger
	stopTimeout time.Duration
	failures    chan error

	runtimeCloseOnce sync.Once
	gatewayCloseOnce sync.Once
	closeOnce        sync.Once
}

func newWorkerRuntimeOwner(
	runtime workerExtensionRuntime,
	gateway hostAPIGatewayCloser,
	coordinator *pluginRuntimeCoordinatorRuntime,
	logger *slog.Logger,
	stopTimeout time.Duration,
) *workerRuntimeOwner {
	owner := &workerRuntimeOwner{
		runtime: runtime, gateway: gateway, coordinator: coordinator, logger: logger,
		stopTimeout: normalizedPluginRuntimeCoordinatorStopTimeout(stopTimeout),
	}
	if coordinator != nil && coordinator.Active() {
		owner.failures = make(chan error, 1)
		go owner.monitorCoordinator()
	}
	return owner
}

func (owner *workerRuntimeOwner) monitorCoordinator() {
	defer close(owner.failures)
	err, ok := <-owner.coordinator.Failures()
	if !ok || err == nil {
		return
	}
	// lease/heartbeat 已失效时，继续保留任何可调用插件都会伪造授权。
	// 先关 Manager admission，再通知命令层停止 River 并退出。
	owner.closeRuntime()
	owner.failures <- err
}

func (owner *workerRuntimeOwner) startupError() error {
	if owner == nil || owner.coordinator == nil || !owner.coordinator.Active() {
		return nil
	}
	select {
	case <-owner.coordinator.Done():
		if err := owner.coordinator.Err(); err != nil {
			return fmt.Errorf("standalone worker plugin runtime coordinator stopped during bootstrap: %w", err)
		}
		return errPluginRuntimeCoordinatorStoppedBeforeReady
	default:
		return nil
	}
}

func (owner *workerRuntimeOwner) closeRuntime() {
	if owner == nil {
		return
	}
	owner.runtimeCloseOnce.Do(func() {
		if owner.runtime == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), owner.stopTimeout)
		defer cancel()
		owner.runtime.Close(ctx)
	})
}

func (owner *workerRuntimeOwner) closeGateway() {
	if owner == nil {
		return
	}
	owner.gatewayCloseOnce.Do(func() {
		if owner.gateway != nil {
			_ = owner.gateway.Close()
		}
	})
}

func (owner *workerRuntimeOwner) Close() {
	if owner == nil {
		return
	}
	owner.closeOnce.Do(func() {
		if owner.coordinator != nil {
			ctx, cancel := context.WithTimeout(context.Background(), owner.stopTimeout)
			if err := owner.coordinator.Stop(ctx); err != nil && owner.logger != nil {
				owner.logger.Warn("standalone worker plugin runtime coordinator stop failed", "error", err)
			}
			cancel()
		}
		owner.closeRuntime()
		owner.closeGateway()
	})
}

func (owner *workerRuntimeOwner) Failures() <-chan error {
	if owner == nil {
		return nil
	}
	return owner.failures
}

type pluginJobRuntimeResolver struct {
	store extensions.Store
	trust extensionsruntime.RuntimeTrustSource
}

func (r *pluginJobRuntimeResolver) ResolvePluginJobRuntime(ctx context.Context, extensionID, jobName string) (hostapi.PluginJobRuntimeContract, error) {
	if r == nil || r.store == nil || r.trust == nil {
		return hostapi.PluginJobRuntimeContract{}, fmt.Errorf("plugin job resolver is not configured")
	}
	extension, err := r.store.Get(ctx, extensionID)
	if err != nil {
		if errors.Is(err, extensions.ErrExtensionNotFound) {
			return hostapi.PluginJobRuntimeContract{}, supportjobs.ErrPluginJobRuntimeStale
		}
		return hostapi.PluginJobRuntimeContract{}, err
	}
	if extension.Type != extensions.TypePlugin || extension.Status != extensions.StatusEnabled {
		return hostapi.PluginJobRuntimeContract{}, supportjobs.ErrPluginJobRuntimeStale
	}
	contract, err := extensions.PluginJobContractForExtension(extension, jobName)
	if err != nil {
		if errors.Is(err, extensions.ErrExtensionNotFound) || errors.Is(err, extensions.ErrInvalidManifest) {
			return hostapi.PluginJobRuntimeContract{}, supportjobs.ErrPluginJobRuntimeStale
		}
		return hostapi.PluginJobRuntimeContract{}, err
	}
	identity, err := r.trust.RuntimeIdentity(ctx, extension)
	if err != nil {
		if errors.Is(err, extensions.ErrTrustGrantNotFound) {
			return hostapi.PluginJobRuntimeContract{}, supportjobs.ErrPluginJobRuntimeStale
		}
		return hostapi.PluginJobRuntimeContract{}, err
	}
	return hostapi.PluginJobRuntimeContract{Contract: contract, TrustGrantID: identity.TrustGrantID}, nil
}

var newStandaloneWorkerRuntimeManager = func(
	store extensions.Store,
	hostAPI extensionsruntime.HostAPIRegistrar,
	settings extensionsruntime.PluginSettings,
	trust extensionsruntime.RuntimeTrustSource,
	databaseLeases extensionsruntime.RuntimeDatabaseLeaseRegistry,
) workerExtensionRuntime {
	return extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
			Settings:       settings,
			HostAPI:        hostAPI,
			Trust:          trust,
			DatabaseLeases: databaseLeases,
		}),
		DeliveryStore: store,
	})
}

func buildStandaloneWorkerExtensionRuntime(
	ctx context.Context,
	cfg config.Config,
	databaseBinderFactory protocolV2DatabaseCatalogBinderFactory,
	commandBinder protocolV2CommandRuntimeBinder,
	store extensions.Store,
	cipher *crypto.OptionCipher,
	trust extensionsruntime.RuntimeTrustSource,
	databaseLeases extensionsruntime.RuntimeDatabaseLeaseRegistry,
	cacheInstallationID string,
	cacheClient *redis.Client,
	logger *slog.Logger,
	coordinators ...*extensions.ActivationCoordinator,
) (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error) {
	service := extensions.NewServiceWithBuiltins(store, cfg.ExtensionRoot, cfg.BuiltinExtensionRoot)
	extensions.WithExternalExtensionRoots(cfg.ExternalExtensionRoots)(service)
	extensions.WithCipher(cipher)(service)
	extensions.WithSafeMode(cfg.SafeMode)(service)
	var activation *extensions.ActivationCoordinator
	if len(coordinators) > 0 {
		activation = coordinators[0]
	} else if activationStore, ok := store.(extensions.ActivationAttemptStore); ok {
		activation = extensions.NewActivationCoordinator(activationStore)
	}
	extensions.WithActivationCoordinator(activation)(service)
	workerHostAPI := hostapi.New(hostapi.Config{Settings: service})
	workerHostGateway := hostapi.NewGateway(workerHostAPI)
	if commandBinder == nil {
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("worker Host Command runtime binder is required")
	}
	if err := commandBinder(workerHostGateway); err != nil {
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("bind worker Host Command runtime: %w", err)
	}
	if databaseBinderFactory == nil {
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("worker DatabaseService catalog binder is required")
	}
	databaseBinder := databaseBinderFactory(workerHostGateway)
	managedRuntime := newStandaloneWorkerRuntimeManager(store, workerHostGateway, service, trust, databaseLeases)
	var cachePublications *extensionsruntime.PostgresLifecycleBoundaryRegistries
	if cacheClient != nil {
		manager, ok := managedRuntime.(*extensionsruntime.Manager)
		if !ok || manager == nil {
			managedRuntime.Close(ctx)
			_ = workerHostGateway.Close()
			return nil, nil, nil, fmt.Errorf("worker Host Cache requires the exact production runtime Manager")
		}
		admission := newPluginServiceProviderAdmission(manager)
		workerHostAPI.BindServiceProviderAdmission(admission)
		cacheRegistry := cacheregistry.New()
		if _, err := bindProductionHostCache(
			cacheInstallationID, logger, cacheClient, cacheRegistry, admission, workerHostGateway,
		); err != nil {
			managedRuntime.Close(ctx)
			_ = workerHostGateway.Close()
			return nil, nil, nil, fmt.Errorf("bind worker Host Cache runtime: %w", err)
		}
		cachePublications = extensionsruntime.NewPostgresLifecycleBoundaryRegistries(
			extensionsruntime.LifecycleRegistryBoundaryConfig{Manager: manager, Caches: cacheRegistry},
		)
		extensions.WithRuntimeCachePublications(cachePublications)(service)
	}
	if err := bindProtocolV2ProviderBroker(workerHostGateway, managedRuntime); err != nil {
		managedRuntime.Close(ctx)
		_ = workerHostGateway.Close()
		return nil, nil, nil, err
	}
	if runtime, ok := managedRuntime.(interface {
		SetStartPreparer(func(context.Context, extensions.Extension) error)
	}); ok {
		runtime.SetStartPreparer(protocolV2DatabaseStartPreparer(store, databaseBinder, cfg.SafeMode))
	}
	if runtime, ok := managedRuntime.(interface {
		WithActivation(*extensions.ActivationCoordinator, string) *extensionsruntime.Manager
	}); ok {
		runtime.WithActivation(activation, extensions.NewActivationBootID())
	}
	workerHostAPI.BindCapabilitySource(service)
	if _, err := service.SyncBuiltins(ctx); err != nil {
		managedRuntime.Close(ctx)
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("sync worker builtin extensions: %w", err)
	}
	if err := syncExternalExtensionSources(ctx, logger, service); err != nil {
		managedRuntime.Close(ctx)
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("sync worker external extension sources: %w", err)
	}
	items, err := store.List(ctx)
	if err != nil {
		managedRuntime.Close(ctx)
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("list worker extensions: %w", err)
	}
	// 独立 worker 同样会启动插件 broker，必须先绑定同一份精确 SQL catalog。
	if err := bindProtocolV2DatabaseRuntime(databaseBinder, items, cfg.SafeMode); err != nil {
		managedRuntime.Close(ctx)
		_ = workerHostGateway.Close()
		return nil, nil, nil, fmt.Errorf("bind worker DatabaseService runtime: %w", err)
	}
	cleanupRuntime := func(coordinator *pluginRuntimeCoordinatorRuntime) {
		timeout := normalizedPluginRuntimeCoordinatorStopTimeout(cfg.WorkerShutdownTimeout)
		if coordinator != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), timeout)
			_ = coordinator.Stop(stopCtx)
			cancel()
		}
		// coordinator 超时不得耗尽 Manager 的关闭预算；使用独立 context
		// 确保启动失败仍会关闭 process-local admission。
		runtimeCtx, cancel := context.WithTimeout(context.Background(), timeout)
		managedRuntime.Close(runtimeCtx)
		cancel()
		_ = workerHostGateway.Close()
	}
	coordinator, err := startStandaloneWorkerRuntimeCoordinator(ctx, cfg, store, managedRuntime, logger)
	if err != nil {
		cleanupRuntime(nil)
		return nil, nil, nil, fmt.Errorf("start standalone worker plugin runtime coordinator: %w", err)
	}
	// Registry 恢复必须读取首轮 convergence 之后的元数据，不能复用启动
	// coordinator 前的快照，否则并发 publication 可能留下陈旧 Cache 目录。
	reconciledItems, err := store.List(ctx)
	if err != nil {
		cleanupRuntime(coordinator)
		return nil, nil, nil, fmt.Errorf("reload worker extensions after runtime convergence: %w", err)
	}
	if cachePublications != nil {
		if err := cachePublications.RestoreCachePublications(ctx, reconciledItems, cfg.SafeMode); err != nil {
			cleanupRuntime(coordinator)
			return nil, nil, nil, fmt.Errorf("restore worker Cache Registry publications: %w", err)
		}
	}
	return managedRuntime, workerHostGateway, coordinator, nil
}

func NewWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Worker, error) {
	if err := runStartupMigrations(ctx, cfg, logger); err != nil {
		return nil, err
	}

	pool, err := postgres.NewPoolWithOptions(ctx, cfg.DatabaseURL, postgres.PoolOptions{
		MaxConns:        cfg.WorkerDatabaseMaxConns,
		MinConns:        cfg.WorkerDatabaseMinConns,
		MaxConnIdleTime: cfg.WorkerDatabaseMaxConnIdleTime,
		MaxConnLifetime: cfg.WorkerDatabaseMaxConnLifetime,
		ConnectTimeout:  cfg.WorkerDatabaseConnectTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres setup failed: %w", err)
	}
	hostInstallationID, err := installationidentity.NewPostgresRepository(pool).Ensure(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("ensure worker Host installation identity: %w", err)
	}

	// Host Cache 与心跳复用一个 worker-owned Redis client；必须在首个插件
	// broker 注册前创建并传入 standalone runtime builder。
	redisClient := humanverify.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword, humanverify.RedisClientOptions{
		PoolSize:        cfg.RedisPoolSize,
		MinIdleConns:    cfg.RedisMinIdleConns,
		DialTimeout:     cfg.RedisDialTimeout,
		ReadTimeout:     cfg.RedisReadTimeout,
		WriteTimeout:    cfg.RedisWriteTimeout,
		ConnMaxIdleTime: cfg.RedisConnMaxIdleTime,
		ConnMaxLifetime: cfg.RedisConnMaxLifetime,
	})
	queryInvalidation := newStandaloneWorkerQueryInvalidationRuntime(cfg, hostInstallationID, logger)
	// 独立 worker：自建 runtime，OwnsRuntime 由 newWorkerWithPool 在 nil inject 时设为 true。
	worker, err := newWorkerWithPool(cfg, pool, logger, workerRuntimeDeps{
		BootstrapContext: ctx,
		HostCacheRedis:   redisClient, HostCacheInstallationID: hostInstallationID,
		QueryInvalidation: queryInvalidation,
	})
	if err != nil {
		_ = redisClient.Close()
		pool.Close()
		return nil, err
	}
	if cfg.SafeMode {
		if err := audit.NewPostgresWriter(pool).Append(ctx, audit.Event{
			Action:   audit.ActionExtensionSafeModeBoot,
			Metadata: map[string]any{"process": "worker"},
		}); err != nil && logger != nil {
			logger.Warn("record worker safe mode boot audit failed", "error", err)
		}
	}

	// 独立 worker 进程发布心跳，供 API overview / 运维判断 stale。
	// 嵌入 API 的 worker 由 bootstrap.NewAPI 发布，避免双写无妨但这里只在独立进程路径启用。
	heartbeatStore := health.NewRedisHeartbeatStore(redisClient)
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	go (&health.Publisher{Store: heartbeatStore}).Run(heartbeatCtx)

	runtimeClose := worker.close
	worker.close = func() {
		heartbeatCancel()
		if runtimeClose != nil {
			runtimeClose()
		}
		if err := redisClient.Close(); err != nil && logger != nil {
			logger.Warn("worker shared redis close failed", "error", err)
		}
		pool.Close()
	}
	return worker, nil
}

// resolveWorkerExtensionRuntime 决定 embed 注入 vs 独立自建。
// 注入时 ownsRuntime=false 且不调用 buildStandalone（因此不会二次 Reconcile/Start）。
func resolveWorkerExtensionRuntime(
	deps workerRuntimeDeps,
	buildStandalone func() (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error),
) (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, bool, error) {
	if deps.ExtensionRuntime != nil {
		return deps.ExtensionRuntime, nil, nil, false, nil
	}
	runtime, gateway, coordinator, err := buildStandalone()
	if err != nil {
		return nil, nil, nil, false, err
	}
	return runtime, gateway, coordinator, true, nil
}

func newWorkerWithPool(cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger, deps workerRuntimeDeps) (*Worker, error) {
	queryInvalidation := deps.QueryInvalidation
	queryInvalidationHandedOff := false
	defer func() {
		if !queryInvalidationHandedOff {
			queryInvalidation.Close()
		}
	}()
	registry := supportjobs.NewRegistry()
	// Safe Mode 也注册 kind；nil invalidator 会让已提交任务 snooze，避免被
	// River 当作 unknown kind 消耗重试或永久丢弃。
	queryregistryjobs.Register(registry, queryInvalidation.Invalidator(), logger)
	pluginSchedules := deps.PluginSchedules
	if pluginSchedules == nil {
		pluginSchedules = supportjobs.NewPluginScheduleAdmissionRegistry()
	}
	extensionStore := extensions.NewPostgresStore(pool)
	runtimeTrust := extensions.NewExecutableTrustService(extensionStore, extensions.NewPostgresExecutableTrustStore(pool))
	databaseLeaseRegistry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(pool, nil)
	reapCtx, reapCancel := context.WithTimeout(context.Background(), extensionsruntime.RecommendedProtocolDatabaseLeaseOperationTimeout)
	defer reapCancel()
	if _, err := databaseLeaseRegistry.ReapExpiredRuntimeLeases(
		reapCtx, extensionsruntime.DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	); err != nil {
		return nil, fmt.Errorf("reap worker extension database runtime leases: %w", err)
	}
	optionCipher, err := crypto.NewOptionCipher(cfg.OptionEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create worker option cipher: %w", err)
	}

	bootstrapCtx := deps.BootstrapContext
	if bootstrapCtx == nil {
		bootstrapCtx = context.Background()
	}
	extensionRuntime, hostGateway, runtimeCoordinator, ownsRuntime, err := resolveWorkerExtensionRuntime(deps, func() (workerExtensionRuntime, hostAPIGatewayCloser, *pluginRuntimeCoordinatorRuntime, error) {
		activation := extensions.NewActivationCoordinator(extensionStore).WithAuditor(audit.NewPostgresWriter(pool))
		commandJobClient, commandErr := supportjobs.NewInsertOnlyClient(pool, supportjobs.FromAppConfig(cfg))
		if commandErr != nil {
			return nil, nil, nil, fmt.Errorf("worker Host Command dispatcher setup failed: %w", commandErr)
		}
		commandDispatcher := supportjobs.NewDispatcher(commandJobClient)
		return buildStandaloneWorkerExtensionRuntime(
			bootstrapCtx, cfg,
			postgresProtocolV2DatabaseCatalogBinderFactory(
				pool,
				hostapi.WithProtocolV2DatabaseTraceSink(hostapi.NewSlogDatabaseTraceSink(logger)),
				hostapi.WithProtocolV2DatabaseQueryInvalidationJobs(commandDispatcher),
			),
			postgresProtocolV2CommandRuntimeBinder(
				pool,
				commandDispatcher,
				moderation.NewPostgresStore(pool),
				attachments.NewPostgresStore(pool),
			),
			extensionStore, optionCipher, runtimeTrust, databaseLeaseRegistry,
			deps.HostCacheInstallationID, deps.HostCacheRedis, logger, activation,
		)
	})
	if err != nil {
		return nil, err
	}
	var runtimeOwner *workerRuntimeOwner
	if ownsRuntime {
		runtimeOwner = newWorkerRuntimeOwner(
			extensionRuntime, hostGateway, runtimeCoordinator, logger, cfg.WorkerShutdownTimeout,
		)
	}
	runtimeOwnershipTransferred := false
	defer func() {
		if !runtimeOwnershipTransferred && runtimeOwner != nil {
			runtimeOwner.Close()
		}
	}()
	if runtime, ok := extensionRuntime.(interface {
		BindProviderSlotSelections(extensionsruntime.ProviderSlotSelectionStore)
	}); ok {
		runtime.BindProviderSlotSelections(extensionsruntime.NewPostgresProviderSlotSelectionStore(pool))
	}

	workerOptions := options.NewServiceWithDefaults(options.NewPostgresStore(pool), optionsDefaultsFromConfig(cfg)).
		WithCipher(optionCipher)
	legacyMailValues, err := workerOptions.InternalValues(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load worker legacy mail options: %w", err)
	}
	notificationStore := notifications.NewPostgresStore(pool)
	if err := notificationStore.AdoptLegacyMail(context.Background(), legacyMailValues); err != nil {
		return nil, fmt.Errorf("adopt worker legacy mail settings: %w", err)
	}
	// mail.deliver 必须命中共享/本进程同一 runtime，避免 embed 时打到第二套插件进程。
	mailProviders := extensionsruntime.NewMailProviderRegistry(extensionStore)
	notificationjobs.Register(registry, &notificationjobs.DeliverMailWorker{Store: notificationStore, Providers: mailProviders, Sender: extensionRuntime})
	channelRuntime, ok := extensionRuntime.(workerNotificationChannelRuntime)
	if !ok {
		return nil, fmt.Errorf("notification channel provider runtime is unavailable")
	}
	notificationjobs.RegisterChannels(registry, &notificationjobs.DeliverChannelWorker{
		Store: notificationStore, Invoker: workerNotificationChannelAdapter{runtime: channelRuntime},
	})
	// F3.3：出站 webhook 投递（SSRF 安全客户端；生产禁 http；secret 解密）。
	webhookStore := webhooks.NewPostgresStore(pool)
	webhookSecretSvc := webhooks.NewService(webhookStore, nil, nil).WithCipher(optionCipher)
	webhookjobs.Register(registry, &webhookjobs.DeliverWorker{
		Store:     webhookStore,
		Secrets:   webhookSecretSvc,
		Migrator:  webhookStore,
		AllowHTTP: !strings.EqualFold(cfg.AppEnv, "production"),
	})
	// 孤儿附件清理：handler 已存在，F1 通过 schedule registry 挂上 daily maintenance。
	// 与 API 相同：插件存储路径需注入 runtime（E6.2）。
	attachmentStore := attachments.NewPostgresStore(pool)
	attachmentSecretStore, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create worker attachment secret store: %w", err)
	}
	attachmentSecretAudit, err := secretstore.NewPostgresAuditStore(pool)
	if err != nil {
		return nil, fmt.Errorf("create worker attachment secret audit store: %w", err)
	}
	requireSecretEncryption := strings.EqualFold(cfg.AppEnv, "production") || strings.EqualFold(cfg.AppEnv, "staging")
	attachmentSecrets, err := secretstore.NewWithOptions(secretstore.Options{
		Store: attachmentSecretStore, Audit: attachmentSecretAudit, Cipher: optionCipher,
		RequireEncryption: requireSecretEncryption, AllowTransparent: !requireSecretEncryption,
	})
	if err != nil {
		return nil, fmt.Errorf("create worker attachment secret service: %w", err)
	}
	attachmentService := attachments.NewService(attachmentStore, workerOptions).
		WithStoragePluginRuntime(extensionsruntime.NewPluginStorageAdapterFactory(extensionRuntime, 0)).
		WithSecretStore(attachmentSecrets)
	attachmentjobs.Register(registry, attachmentService)
	compressionService := attachments.NewCompressionService(attachmentStore, attachmentService, workerOptions, nil)
	attachmentjobs.RegisterCompression(registry, attachmentStore, compressionService, logger)
	// 审计日志保留期清理（F1.4）：默认 90 天，handler 可后续接 runtime option。
	auditWriter := audit.NewPostgresWriter(pool)
	auditjobs.Register(registry, &auditjobs.CleanupEventsWorker{
		Cleaner: auditWriter,
		KeepDays: func(context.Context) (int, error) {
			return audit.RecommendedRetentionDays, nil
		},
		Logger: logger,
	})
	// 搜索 worker：引擎经 search.provider；无提供方时 job 立即成功。
	searchReconciler := registerSearchWorkers(registry, pool, extensionStore, extensionRuntime, logger)
	registerIdentityCleanupWorker(registry, cfg, pool, logger)
	registerForumAutoLockWorker(registry, cfg, pool, logger)
	registerForumFlushViewCountsWorker(registry, pool, deps.HostCacheRedis, logger)
	// AI 回复：只有 embed 路径会注入生成器，独立 worker 拿不到等价装配。
	registerAIReplyWorker(registry, deps.AIReplyGenerator, deps.AIReplyTriageDecider, deps.AIReplyThreadReader, deps.AIReplyEnqueuer)
	// F2.2：插件经 Host API 入队的 extension.plugin_job。
	pluginJobEnqueuer := &hostapi.RiverJobEnqueuer{}
	registry.Add(func(workers *river.Workers) error {
		var executor hostapi.PluginJobExecutor
		if candidate, ok := extensionRuntime.(hostapi.PluginJobExecutor); ok {
			executor = candidate
		}
		river.AddWorker(workers, &hostapi.PluginJobWorker{
			Resolver: &pluginJobRuntimeResolver{store: extensionStore, trust: runtimeTrust},
			Executor: executor,
		})
		return nil
	})
	registry.Add(func(workers *river.Workers) error {
		river.AddWorker(workers, &hostapi.PluginScheduleTriggerWorker{
			Schedules: pluginSchedules, Jobs: pluginJobEnqueuer,
		})
		return nil
	})

	// 周期任务统一经 Schedule Registry 注册，禁止在 bootstrap 散落 NewPeriodicJob。
	// 启用状态读 web_options；constructor 返回 nil 时 River 跳过本次插入（无需重启 worker）。
	scheduleOptions := jobsmodel.NewPostgresOptionStore(pool)
	wrapEnabled := func(scheduleID string, ctor river.PeriodicJobConstructor) river.PeriodicJobConstructor {
		return func() (river.JobArgs, *river.InsertOpts) {
			value, ok, err := scheduleOptions.Get(context.Background(), supportjobs.ScheduleEnabledOptionName(scheduleID))
			if err == nil && !supportjobs.ParseScheduleEnabled(value, ok) {
				return nil, nil
			}
			return ctor()
		}
	}
	scheduleRegistry, err := supportjobs.NewCoreScheduleRegistry(map[string]river.PeriodicJobConstructor{
		supportjobs.ScheduleIdentityCleanupSessions: wrapEnabled(
			supportjobs.ScheduleIdentityCleanupSessions,
			func() (river.JobArgs, *river.InsertOpts) {
				return identityjobs.CleanupSessionsArgs{}, nil
			},
		),
		supportjobs.ScheduleAttachmentsCleanupOrphans: wrapEnabled(
			supportjobs.ScheduleAttachmentsCleanupOrphans,
			func() (river.JobArgs, *river.InsertOpts) {
				// Limit=0 时 worker 使用默认批大小 100。
				return attachmentjobs.CleanupOrphansArgs{}, nil
			},
		),
		supportjobs.ScheduleAttachmentsReconcileCompression: wrapEnabled(
			supportjobs.ScheduleAttachmentsReconcileCompression,
			func() (river.JobArgs, *river.InsertOpts) {
				return attachmentjobs.ReconcileCompressionArgs{}, nil
			},
		),
		supportjobs.ScheduleAuditCleanupEvents: wrapEnabled(
			supportjobs.ScheduleAuditCleanupEvents,
			func() (river.JobArgs, *river.InsertOpts) {
				return auditjobs.CleanupEventsArgs{}, nil
			},
		),
		supportjobs.ScheduleForumAutoLockIdle: wrapEnabled(
			supportjobs.ScheduleForumAutoLockIdle,
			func() (river.JobArgs, *river.InsertOpts) {
				return forumjobs.AutoLockIdleArgs{}, nil
			},
		),
		supportjobs.ScheduleForumFlushViewCounts: wrapEnabled(
			supportjobs.ScheduleForumFlushViewCounts,
			func() (river.JobArgs, *river.InsertOpts) {
				return forumjobs.FlushViewCountsArgs{}, nil
			},
		),
		supportjobs.ScheduleSearchReconcile: wrapEnabled(
			supportjobs.ScheduleSearchReconcile,
			func() (river.JobArgs, *river.InsertOpts) {
				opts := searchjobs.ReconcileArgs{}.EnqueueOptions().RiverInsertOpts()
				return searchjobs.ReconcileArgs{}, opts
			},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("schedule registry: %w", err)
	}
	periodicJobs, err := scheduleRegistry.BuildPeriodicJobs()
	if err != nil {
		return nil, fmt.Errorf("build periodic jobs: %w", err)
	}

	if registry.IsEmpty() {
		if err := runtimeOwner.startupError(); err != nil {
			return nil, err
		}
		runtimeOwnershipTransferred = true
		queryInvalidationHandedOff = true
		return &Worker{
			Schedules: scheduleRegistry, PluginSchedules: pluginSchedules,
			failures: runtimeOwner.Failures(), close: func() {
				queryInvalidation.Close()
				runtimeOwner.Close()
			},
		}, nil
	}

	workers, err := registry.Build()
	if err != nil {
		return nil, fmt.Errorf("worker registration failed: %w", err)
	}

	client, err := supportjobs.NewClientWithPeriodic(pool, supportjobs.FromAppConfig(cfg), workers, periodicJobs)
	if err != nil {
		return nil, fmt.Errorf("job client setup failed: %w", err)
	}
	searchReconciler.WithDispatcher(supportjobs.NewDispatcher(client))
	pluginJobEnqueuer.Dispatcher = supportjobs.NewDispatcher(client)
	if err := pluginSchedules.BindPeriodicPublisher(
		supportjobs.NewPluginSchedulePeriodicPublisher(client.PeriodicJobs()),
	); err != nil {
		return nil, fmt.Errorf("bind plugin schedule publisher: %w", err)
	}
	if ownsRuntime && !cfg.SafeMode {
		if err := publishStandalonePluginSchedules(context.Background(), extensionStore, extensionRuntime, runtimeTrust, pluginSchedules); err != nil {
			return nil, fmt.Errorf("publish standalone plugin schedules: %w", err)
		}
	}
	if err := runtimeOwner.startupError(); err != nil {
		return nil, err
	}

	// 仅 worker 拥有 runtime 时关闭；embed 注入路径由 API 在 River stop 之后关闭。
	runtimeOwnershipTransferred = true
	queryInvalidationHandedOff = true
	return &Worker{
		Client:          client,
		Schedules:       scheduleRegistry,
		PluginSchedules: pluginSchedules,
		failures:        runtimeOwner.Failures(),
		close: func() {
			queryInvalidation.Close()
			runtimeOwner.Close()
		},
	}, nil
}

// registerIdentityCleanupWorker 注册历史会话清理 worker。
// keep_days 从 runtime option 读取（每次执行时实时解析，admin 改动即生效）。
// 对应的 daily schedule 由 Schedule Registry 统一拥有，不在此返回 PeriodicJob。
// registerAIReplyWorker 注册 AI 回复生成与自主参与判定。它只在 embed 模式注入
// 生成器时注册：生成器携带 API 的论坛服务与网关，独立 worker 拿不到等价装配。
func registerAIReplyWorker(registry *supportjobs.Registry, generator *aireply.Generator, decider *aireply.TriageDecider, threads aireply.ThreadReader, enqueuer *aireplyjobs.Enqueuer) {
	if registry == nil || generator == nil {
		return
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[aireplyjobs.GenerateReplyArgs](workers, aireplyjobs.NewGenerateReplyWorker(generator))
	})
	// 判定与生成共用网关与用途登记表。判 REPLY 后经 enqueuer 入队正式生成，
	// 与 mention 路径同一条 Unique ByArgs 去重。判定组件缺失（旧装配）时只注册
	// 生成——autonomous 模式下 trigger 会因没有判定入队器而按未启用处理。
	if decider == nil || threads == nil || enqueuer == nil {
		return
	}
	generate := func(ctx context.Context, input aireply.ReplyJobInput) error {
		return enqueuer.EnqueueReply(ctx, input)
	}
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[aireplyjobs.TriageReplyArgs](workers,
			aireplyjobs.NewTriageReplyWorker(decider, threads).WithGenerate(generate))
	})
}

func registerIdentityCleanupWorker(registry *supportjobs.Registry, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) {
	optionStore := options.NewPostgresStore(pool)
	optionsService := options.NewServiceWithDefaults(optionStore, optionsDefaultsFromConfig(cfg))
	identityStore := identity.NewPostgresStore(pool)

	registry.Add(func(workers *river.Workers) error {
		river.AddWorker(workers, &identityjobs.CleanupSessionsWorker{
			Store: identityStore,
			KeepDays: func(ctx context.Context) (int, error) {
				raw, err := optionsService.WebOption(ctx, options.NameIdentitySessionsKeepDays)
				if err != nil {
					return 0, err
				}
				if days, err := strconv.Atoi(raw); err == nil && days > 0 {
					return days, nil
				}
				return identity.RecommendedSessionsKeepDays, nil
			},
			Logger: logger,
		})
		return nil
	})
}

// registerForumAutoLockWorker 注册闲置主题自动锁帖 worker。
// idle days 每次执行时从 web_options 读取；0 或缺失时 job 空跑。
func registerForumAutoLockWorker(registry *supportjobs.Registry, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) {
	optionStore := options.NewPostgresStore(pool)
	optionsService := options.NewServiceWithDefaults(optionStore, optionsDefaultsFromConfig(cfg))
	forumStore := forum.NewPostgresStore(pool)

	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[forumjobs.AutoLockIdleArgs](workers, &forumjobs.AutoLockIdleWorker{
			Locker: forumStore,
			IdleDays: func(ctx context.Context) (int, error) {
				raw, err := optionsService.WebOption(ctx, options.NameForumTopicsAutoLockIdleDays)
				if err != nil {
					return 0, err
				}
				if days, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && days > 0 {
					return days, nil
				}
				return 0, nil
			},
			Logger: logger,
		})
	})
}

// registerForumFlushViewCountsWorker 注册浏览量刷盘（D3 / M2）。
// redisClient 为 nil 时仍注册 worker，但 Drain 空跑（嵌入路径应注入 shared Redis）。
func registerForumFlushViewCountsWorker(registry *supportjobs.Registry, pool *pgxpool.Pool, redisClient *redis.Client, logger *slog.Logger) {
	forumStore := forum.NewPostgresStore(pool)
	counter := forum.NewRedisTopicViewCounter(redisClient).WithLogger(logger)
	forumjobs.RegisterFlushViewCounts(registry, &forumjobs.FlushViewCountsWorker{
		Drainer: counter,
		Store:   forumStore,
		Logger:  logger,
	})
}

func (w *Worker) Start(ctx context.Context) error {
	return supportjobs.Start(ctx, w.Client)
}

func (w *Worker) Stop(ctx context.Context) error {
	return supportjobs.Stop(ctx, w.Client)
}

// Failures reports terminal failures from the standalone plugin runtime
// coordinator. It is nil for embedded workers and Safe Mode, so callers may
// include it directly in a select without creating a false shutdown signal.
func (w *Worker) Failures() <-chan error {
	if w == nil {
		return nil
	}
	return w.failures
}

func (w *Worker) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		if w.close != nil {
			w.close()
		}
	})
}
