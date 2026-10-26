package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-plugin"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionobservability "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionObservability"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

var (
	ErrUnsupportedProtocol    = errors.New("unsupported plugin protocol")
	ErrInvalidPluginJobStream = errors.New("invalid protocol v2 plugin job progress stream")
	// ErrUnsafePluginRouteTarget 插件回报的 BaseURL 不允许被 API 代理（SSRF 防护）。
	ErrUnsafePluginRouteTarget = errors.New("plugin route target is not allowed")
)

type PluginSettings interface {
	ListSettings(context.Context, string) (map[string]string, error)
}

// HostAPIRegistrar 在插件启动时签发 Host API 凭证（F2.2）。
type HostAPIRegistrar interface {
	RegisterExtension(extensionID string) (token string, env []string, err error)
	UnregisterExtension(extensionID string)
}

type ProtocolStarterConfig struct {
	Settings PluginSettings
	// HostAPI 可选；注入后向子进程写入 SFORUM_HOST_API_* 环境变量。
	HostAPI HostAPIRegistrar
	// Trust 为 v2 握手解析精确 artifact grant；上传包不可省略。
	Trust RuntimeTrustSource
	// DatabaseLeases 为声明直接数据库权限的 exact runtime 签发独立短租约。
	DatabaseLeases RuntimeDatabaseLeaseRegistry
	// 测试可缩短心跳；生产零值使用推荐值。
	DatabaseLeaseHeartbeatInterval time.Duration
	DatabaseLeaseOperationTimeout  time.Duration
}

type ProtocolStarter struct {
	mu                     sync.Mutex
	runtimeSetTransition   chan struct{}
	clients                map[string]*plugin.Client
	protocols              map[string]PluginProtocol
	runtimeInstances       map[string]map[string]*protocolRuntimeInstance
	activeRuntimeInstances map[string]string
	telemetry              map[string]*protocolTelemetry
	lifecycleMu            sync.Mutex
	lifecycle              map[string]chan struct{}
	settings               PluginSettings
	hostAPI                HostAPIRegistrar
	trust                  RuntimeTrustSource
	databaseLeases         RuntimeDatabaseLeaseRegistry
	databaseLeaseHeartbeat time.Duration
	databaseLeaseTimeout   time.Duration
}

type PluginProtocol interface {
	Health() (PluginHealth, error)
	RouteTarget() (PluginRouteTarget, error)
	InvokeHook(PluginHookRequest) (PluginHookResponse, error)
	ProviderProbe(ProviderProbeRequest) (ProviderProbeResponse, error)
	SendMail(MailProviderRequest) (MailProviderResponse, error)
	// 附件存储槽 attachment.storage.provider（E6.2，分块 Put/Open）。
	StoragePutBegin(StoragePutBeginRequest) (StorageSessionResponse, error)
	StoragePutChunk(StoragePutChunkRequest) (StorageResult, error)
	StorageOpen(StorageOpenRequest) (StorageSessionResponse, error)
	StorageGetChunk(StorageGetChunkRequest) (StorageGetChunkResponse, error)
	StorageClose(StorageCloseRequest) (StorageResult, error)
	StorageDelete(StorageObjectRequest) (StorageResult, error)
	StorageStat(StorageStatRequest) (StorageStatResponse, error)
	StorageExists(StorageExistsRequest) (StorageExistsResponse, error)
	StoragePublicURL(StoragePublicURLRequest) (StorageURLResponse, error)
	StorageSignedURL(StorageSignedURLRequest) (StorageURLResponse, error)
	StorageProbe(StorageProbeRequest) (StorageProbeResponse, error)
	StorageInstanceProtocol
}

// pluginHookContextInvoker lets the typed transport propagate Host cancellation.
type pluginHookContextInvoker interface {
	InvokeHookContext(context.Context, PluginHookRequest) (PluginHookResponse, error)
}

type pluginJobContextInvoker interface {
	ExecutePluginJob(context.Context, supportjobs.PluginJobInvocation) error
}

type PluginHealth struct {
	OK bool
}

type PluginRouteTarget struct {
	BaseURL string
}

type PluginHookRequest struct {
	DeclarationID   string
	Name            string
	Kind            string
	ContractVersion string
	DeliveryID      int64
	CorrelationID   string
	TimeoutMS       int
	Payload         map[string]any
	PatchFields     []string
}

type PluginHookResponse struct {
	OK      bool
	Reason  string
	Message string
	Patch   map[string]any
	Result  map[string]any
}

type ProviderProbeRequest struct {
	Slot string
}

type ProviderProbeResponse struct {
	OK          bool
	Reason      string
	Message     string
	Details     map[string]string
	Suggestions []string
}

type VersionedProviderRequest struct {
	DeclarationID   string
	Slot            string
	ContractVersion string
	Operation       string
	RequestSchema   string
	ResponseSchema  string
	Timeout         time.Duration
	Input           map[string]any
}

const VersionedProviderOperationInvoke = "invoke"

type VersionedProviderResponse struct {
	Output map[string]any
}

const ProtocolV2IdentityProviderSlot = "sforum.identity"

// VersionedIdentityProviderRequest keeps the package declaration reference and
// the Registry-derived wire reference separate. Package-path Schemas are valid
// Manifest material but TypedDocument always carries the canonical id@version.
type VersionedIdentityProviderRequest struct {
	ProviderID                string
	ContractVersion           string
	Kind                      string
	Handler                   string
	Priority                  int
	Operation                 string
	InputSchema               string
	InputSchemaWireReference  string
	OutputSchema              string
	OutputSchemaWireReference string
	Timeout                   time.Duration
	FailurePolicy             string
	ActorUserID               int64
	Input                     map[string]any
}

type VersionedIdentityProviderResponse struct {
	Output map[string]any
}

const (
	ProtocolV2SEOProviderSlot      = "sforum.seo"
	ProtocolV2SEOProviderOperation = "apply"
	ProtocolV2SEORequestSchema     = "sforum.seo.apply.request@1"
	ProtocolV2SEOResponseSchema    = "sforum.seo.apply.response@1"
)

// VersionedSEORequest reuses the Protocol V2 ProviderCall transport while
// freezing dispatch to one exact Manifest SEO declaration. Input and output
// remain typed documents; plugins never receive raw request/session authority.
type VersionedSEORequest struct {
	DeclarationID   string
	ContractVersion string
	Handler         string
	Timeout         time.Duration
	Input           map[string]any
}

type VersionedSEOResponse struct {
	Output map[string]any
}

type MailProviderRequest struct {
	DeliveryID    string
	CorrelationID string
	FromAddress   string
	FromName      string
	To            []string
	Subject       string
	TextBody      string
	HTMLBody      string
}

type MailProviderResponse struct {
	OK             bool
	Classification string
	Reason         string
	Message        string
}

func NewProtocolStarter(config ProtocolStarterConfig) *ProtocolStarter {
	heartbeatInterval := config.DatabaseLeaseHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = RecommendedProtocolDatabaseLeaseHeartbeatInterval
	}
	operationTimeout := config.DatabaseLeaseOperationTimeout
	if operationTimeout <= 0 {
		operationTimeout = RecommendedProtocolDatabaseLeaseOperationTimeout
	}
	runtimeSetTransition := make(chan struct{}, 1)
	runtimeSetTransition <- struct{}{}
	return &ProtocolStarter{
		runtimeSetTransition:   runtimeSetTransition,
		clients:                map[string]*plugin.Client{},
		protocols:              map[string]PluginProtocol{},
		runtimeInstances:       map[string]map[string]*protocolRuntimeInstance{},
		activeRuntimeInstances: map[string]string{},
		telemetry:              map[string]*protocolTelemetry{},
		lifecycle:              map[string]chan struct{}{},
		settings:               config.Settings,
		hostAPI:                config.HostAPI,
		trust:                  config.Trust,
		databaseLeases:         config.DatabaseLeases,
		databaseLeaseHeartbeat: heartbeatInterval,
		databaseLeaseTimeout:   operationTimeout,
	}
}

func (s *ProtocolStarter) startProtocolInstanceLocked(
	ctx context.Context,
	extension extensions.Extension,
	publish bool,
) (result RouteTarget, resultErr error) {
	if ctx == nil {
		return RouteTarget{}, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return RouteTarget{}, err
	}
	if extension.Manifest.Backend.Entry == "" {
		return RouteTarget{}, fmt.Errorf("backend entry is required")
	}
	if extension.Manifest.ManifestVersion != 3 || !pluginbootstrap.SupportsProtocolV2(
		extension.Manifest.Backend.RPC,
		extension.Manifest.Backend.ProtocolVersion,
	) {
		return RouteTarget{}, ErrUnsupportedProtocol
	}
	manifestDigest, err := protocolRuntimeManifestDigest(extension.Manifest)
	if err != nil {
		return RouteTarget{}, fmt.Errorf("freeze runtime manifest: %w", err)
	}
	path, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
	if !ok {
		return RouteTarget{}, extensions.ErrInvalidManifest
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return RouteTarget{}, fmt.Errorf("backend entry %s is not available", extension.Manifest.Backend.Entry)
	}

	// 子进程生命周期由 exact Stop/Discard 管理；调用 Start 的请求 context 只约束启动握手。
	cmd := exec.Command(path)
	cmd.Env = buildPluginProcessEnv(os.Environ())
	protocolVersion := extension.Manifest.Backend.ProtocolVersion
	instanceID, err := newProtocolV2RuntimeInstanceID()
	if err != nil {
		return RouteTarget{}, err
	}
	if s.settings != nil {
		values, err := s.settings.ListSettings(ctx, extension.ID)
		if err != nil {
			return RouteTarget{}, fmt.Errorf("load plugin settings: %w", err)
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			cmd.Env = append(cmd.Env, pluginSettingEnvName(key)+"="+values[key])
		}
	}
	clientConfig, protocolName, err := s.newPluginClientConfig(ctx, extension, cmd, instanceID)
	if err != nil {
		return RouteTarget{}, err
	}
	databaseLease, databaseEnv, err := s.issueProtocolDatabaseLease(ctx, extension, instanceID)
	if err != nil {
		return RouteTarget{}, fmt.Errorf("issue plugin database runtime lease: %w", err)
	}
	cmd.Env = append(cmd.Env, databaseEnv...)
	keepDatabaseLease := false
	defer func() {
		if databaseLease == nil || keepDatabaseLease {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.databaseLeaseTimeout)
		defer cancel()
		resultErr = errors.Join(resultErr, databaseLease.revoke(cleanupCtx))
	}()
	client := plugin.NewClient(clientConfig)
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		diagnostic := ""
		if buffer, ok := clientConfig.Stderr.(*pluginbootstrap.DiagnosticBuffer); ok {
			diagnostic = buffer.String()
		}
		return RouteTarget{}, pluginbootstrap.ClassifyStartError(err, diagnostic)
	}
	raw, err := rpcClient.Dispense(protocolName)
	if err != nil {
		client.Kill()
		return RouteTarget{}, err
	}
	protocol, ok := raw.(PluginProtocol)
	if !ok {
		client.Kill()
		return RouteTarget{}, fmt.Errorf("plugin protocol implementation mismatch")
	}
	if v2, ok := protocol.(interface {
		Handshake(context.Context) error
		Readiness(context.Context) error
	}); ok {
		if err := v2.Handshake(ctx); err != nil {
			client.Kill()
			return RouteTarget{}, err
		}
	}
	health, err := protocol.Health()
	if err != nil || !health.OK {
		client.Kill()
		if err != nil {
			return RouteTarget{}, err
		}
		return RouteTarget{}, fmt.Errorf("plugin health check failed")
	}
	readinessChecked := false
	ready := false
	if publish {
		v2, ok := protocol.(interface{ Readiness(context.Context) error })
		if !ok {
			client.Kill()
			return RouteTarget{}, ErrProtocolInstanceUnsupported
		}
		readinessChecked = true
		if err := v2.Readiness(ctx); err != nil {
			client.Kill()
			return RouteTarget{}, err
		}
		ready = true
	}
	target, err := protocol.RouteTarget()
	if err != nil {
		client.Kill()
		return RouteTarget{}, err
	}
	// 纯 provider/RPC 插件（如 SMTP）不暴露 HTTP 路由：允许空或历史哨兵值。
	baseURL := strings.TrimSpace(target.BaseURL)
	if isPluginRouteTargetNone(baseURL) {
		baseURL = ""
	} else if err := validatePluginRouteTarget(baseURL); err != nil {
		client.Kill()
		return RouteTarget{}, err
	}
	serviceRegistry := protocolV2ServiceRegistryFor(s.hostAPI)
	var registrations []hostapi.ServiceRegistration
	var serviceRuntime hostapi.ServiceRuntimePublication
	if v2, ok := protocol.(*protocolV2Client); ok {
		if v2.identity != nil {
			if v2.identity.GetInstanceId() != instanceID {
				client.Kill()
				return RouteTarget{}, ErrRuntimeAdmissionInvalid
			}
		}
		registrations, err = v2.serviceRegistrations(extension)
		if err != nil {
			client.Kill()
			return RouteTarget{}, err
		}
		if len(registrations) > 0 && serviceRegistry == nil {
			client.Kill()
			return RouteTarget{}, fmt.Errorf("protocol v2 service registry is not configured")
		}
		serviceRuntime, err = v2.serviceRuntimePublication(extension, registrations)
		if err != nil {
			client.Kill()
			return RouteTarget{}, err
		}
	}
	targetResult := RouteTarget{BaseURL: baseURL, InstanceID: instanceID}
	extensionVersion := extension.Version
	if extensionVersion == "" {
		extensionVersion = extension.Manifest.Version
	}
	instance := &protocolRuntimeInstance{
		identity:         RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: instanceID},
		extensionVersion: extensionVersion, artifactDigest: extension.PackageDigest, manifestDigest: manifestDigest,
		protocolVersion: protocolVersion, target: targetResult,
		client: client, protocol: protocol, registrations: registrations, serviceRuntime: serviceRuntime,
		databaseLease: databaseLease,
		healthy:       true, ready: ready, readinessChecked: readinessChecked, startedAt: time.Now().UTC(),
	}
	if err := s.retainProtocolInstanceLocked(instance); err != nil {
		client.Kill()
		return RouteTarget{}, err
	}
	if databaseLease != nil {
		databaseLease.startHeartbeat(s.databaseLeaseHeartbeat, s.databaseLeaseTimeout, func(error) {
			client.Kill()
		})
	}
	go s.watchClientExit(instance.identity, protocolVersion, protocol, client)
	if publish {
		if _, err := s.publishProtocolInstanceLocked(ctx, instance.identity, false); err != nil {
			s.removeProtocolInstanceLocked(instance.identity)
			client.Kill()
			return RouteTarget{}, err
		}
	}
	s.mu.Lock()
	s.recordProtocolStartLocked(extension.ID, protocolVersion)
	s.mu.Unlock()
	keepDatabaseLease = true
	return targetResult, nil
}

// isPluginRouteTargetNone 表示插件不提供可代理的 HTTP BaseURL。
// 兼容旧哨兵 "disabled"（SSRF 加固前 SMTP 等插件使用）。
func isPluginRouteTargetNone(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "disabled", "none":
		return true
	default:
		return false
	}
}

// validatePluginRouteTarget 限制插件 RouteTarget 仅允许 loopback http(s)，阻断 SSRF。
// 调用方应先用 isPluginRouteTargetNone 处理无路由插件。
func validatePluginRouteTarget(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafePluginRouteTarget, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed", ErrUnsafePluginRouteTarget, parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: userinfo not allowed", ErrUnsafePluginRouteTarget)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrUnsafePluginRouteTarget)
	}
	// 字面量 loopback 主机名直接放行，避免依赖解析器。
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrUnsafePluginRouteTarget, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: no addresses for %q", ErrUnsafePluginRouteTarget, host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: host %q resolves to non-loopback %s", ErrUnsafePluginRouteTarget, host, ip)
		}
		// 双保险：拒绝 link-local / 未指定地址（IsLoopback 通常已排除）。
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("%w: host %q resolves to disallowed address %s", ErrUnsafePluginRouteTarget, host, ip)
		}
	}
	return nil
}

func (s *ProtocolStarter) Stop(ctx context.Context, extension extensions.Extension) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlockSet, err := s.lockRuntimeSetTransition(ctx)
	if err != nil {
		return err
	}
	defer unlockSet()
	unlock, err := s.lockExtensionLifecycleContext(ctx, extension.ID)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	instances := make([]RuntimeInstanceIdentity, 0, len(s.runtimeInstances[extension.ID]))
	for instanceID := range s.runtimeInstances[extension.ID] {
		instances = append(instances, RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: instanceID})
	}
	s.mu.Unlock()
	for _, identity := range instances {
		if err := s.stopProtocolInstanceLocked(identity, true); err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
			return err
		}
	}
	return nil
}

func (s *ProtocolStarter) ExecutePluginJob(ctx context.Context, invocation supportjobs.PluginJobInvocation) error {
	if s == nil {
		return extensions.ErrRuntimeUnavailable
	}
	s.mu.Lock()
	protocol := s.protocols[invocation.Contract.ExtensionID]
	s.mu.Unlock()
	invoker, ok := protocol.(pluginJobContextInvoker)
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	// 真实 Job 路径写入扩展可观测性（非测试直接 Record）。
	started := time.Now()
	err := invoker.ExecutePluginJob(ctx, invocation)
	extensionobservability.ObserveJob(
		invocation.Contract.ExtensionID,
		invocation.Contract.ArtifactDigest,
		invocation.Contract.JobName,
		time.Since(started),
		err,
	)
	return err
}

func (s *ProtocolStarter) lockExtensionLifecycle(extensionID string) func() {
	unlock, _ := s.lockExtensionLifecycleContext(context.Background(), extensionID)
	return unlock
}

func (s *ProtocolStarter) lockExtensionLifecycleContext(ctx context.Context, extensionID string) (func(), error) {
	if s == nil || ctx == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	s.lifecycleMu.Lock()
	if s.lifecycle == nil {
		s.lifecycle = map[string]chan struct{}{}
	}
	lock := s.lifecycle[extensionID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		s.lifecycle[extensionID] = lock
	}
	s.lifecycleMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		if err := ctx.Err(); err != nil {
			lock <- struct{}{}
			return nil, err
		}
		return func() { lock <- struct{}{} }, nil
	}
}

func (s *ProtocolStarter) lockRuntimeSetTransition(ctx context.Context) (func(), error) {
	if s == nil || ctx == nil || s.runtimeSetTransition == nil {
		return nil, ErrRuntimeAdmissionInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.runtimeSetTransition:
		if err := ctx.Err(); err != nil {
			s.runtimeSetTransition <- struct{}{}
			return nil, err
		}
		return func() { s.runtimeSetTransition <- struct{}{} }, nil
	}
}

func (s *ProtocolStarter) watchClientExit(identity RuntimeInstanceIdentity, protocolVersion int, protocol PluginProtocol, client *plugin.Client) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !client.Exited() {
		<-ticker.C
	}

	unlockSet, err := s.lockRuntimeSetTransition(context.Background())
	if err != nil {
		return
	}
	defer unlockSet()
	unlock := s.lockExtensionLifecycle(identity.ExtensionID)
	defer unlock()
	s.mu.Lock()
	instance := s.runtimeInstanceLocked(identity)
	if instance == nil || instance.client != client {
		s.mu.Unlock()
		return
	}
	active := s.activeRuntimeInstances[identity.ExtensionID] == identity.InstanceID
	delete(s.runtimeInstances[identity.ExtensionID], identity.InstanceID)
	if len(s.runtimeInstances[identity.ExtensionID]) == 0 {
		delete(s.runtimeInstances, identity.ExtensionID)
	}
	if active {
		delete(s.activeRuntimeInstances, identity.ExtensionID)
		delete(s.clients, identity.ExtensionID)
		delete(s.protocols, identity.ExtensionID)
	}
	s.mu.Unlock()
	if protocolVersion == 2 {
		s.unregisterProtocolV2Services(identity.ExtensionID, protocol)
	}
	_ = s.revokeProtocolDatabaseLease(instance)
}

func (s *ProtocolStarter) unregisterProtocolV2Services(extensionID string, protocol PluginProtocol) {
	registry := protocolV2ServiceRegistryFor(s.hostAPI)
	if registry == nil {
		return
	}
	if v2, ok := protocol.(*protocolV2Client); ok && v2.identity != nil {
		registry.UnregisterProtocolV2ServiceInstance(extensionID, v2.identity.GetInstanceId())
		return
	}
	registry.UnregisterProtocolV2Services(extensionID)
}

func (s *ProtocolStarter) InvokeHook(ctx context.Context, extension extensions.Extension, input HookInput) HookResult {
	started := time.Now()
	result := s.invokeHook(ctx, extension, input)
	// 真实 Hook 路径写入扩展可观测性（Route/Hook/Job 等由各入口写入 Process）。
	var observeErr error
	if !result.OK {
		observeErr = errors.New(result.Reason)
	}
	extensionobservability.ObserveHook(
		extension.ID, extension.PackageDigest, input.Name,
		time.Since(started), observeErr,
	)
	return result
}

func (s *ProtocolStarter) invokeHook(ctx context.Context, extension extensions.Extension, input HookInput) HookResult {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.mu.Unlock()
	if protocol == nil {
		return HookResult{OK: false, Reason: "extension.runtime_unavailable", Message: "Plugin runtime is not available."}
	}
	s.mu.Lock()
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	timeoutMS := int(input.Timeout / time.Millisecond)
	if timeoutMS <= 0 && input.Timeout > 0 {
		timeoutMS = 1
	}
	req := PluginHookRequest{
		DeclarationID:   input.DeclarationID,
		Name:            input.Name,
		Kind:            input.Kind,
		ContractVersion: input.ContractVersion,
		DeliveryID:      input.DeliveryID,
		CorrelationID:   input.CorrelationID,
		TimeoutMS:       timeoutMS,
		Payload:         input.Payload,
		PatchFields:     input.PatchFields,
	}
	if contextual, ok := protocol.(pluginHookContextInvoker); ok {
		resp, err := contextual.InvokeHookContext(ctx, req)
		return protocolHookResult(ctx, resp, err)
	}
	// 统一用 goroutine + select 约束所有 provider 实现的宿主 deadline。
	type outcome struct {
		resp PluginHookResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := protocol.InvokeHook(req)
		done <- outcome{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return HookResult{
			OK:      false,
			Reason:  "extension.hook_timeout",
			Message: "Plugin hook exceeded the host timeout. Heavy work must enqueue a job.",
		}
	case out := <-done:
		return protocolHookResult(ctx, out.resp, out.err)
	}
}

func (s *ProtocolStarter) InvokeVersionedProvider(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedProviderRequest,
) (VersionedProviderResponse, error) {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return VersionedProviderResponse{}, fmt.Errorf("versioned provider requires Protocol V2")
	}
	return client.InvokeVersionedProvider(ctx, input)
}

func (s *ProtocolStarter) InvokeVersionedSEO(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedSEORequest,
) (VersionedSEOResponse, error) {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return VersionedSEOResponse{}, fmt.Errorf("versioned SEO provider requires Protocol V2")
	}
	return client.InvokeVersionedSEO(ctx, input)
}

func (s *ProtocolStarter) InvokeQuery(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedQueryRequest,
) ([]queryregistry.QueryRow, error) {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return nil, fmt.Errorf("executable query requires Protocol V2")
	}
	return client.InvokeQuery(ctx, input)
}

func (s *ProtocolStarter) FilterQueryResult(
	ctx context.Context,
	extension extensions.Extension,
	input VersionedQueryResultFilterRequest,
) ([]queryregistry.QueryRow, error) {
	s.mu.Lock()
	protocol := s.protocols[extension.ID]
	s.recordProtocolCallLocked(extension.ID)
	s.mu.Unlock()
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return nil, fmt.Errorf("query result filter requires Protocol V2")
	}
	return client.FilterQueryResult(ctx, input)
}

func protocolHookResult(ctx context.Context, response PluginHookResponse, err error) HookResult {
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return HookResult{
				OK:      false,
				Reason:  "extension.hook_timeout",
				Message: "Plugin hook exceeded the host timeout. Heavy work must enqueue a job.",
			}
		}
		return HookResult{OK: false, Reason: "extension.hook_failed", Message: err.Error()}
	}
	return HookResult{OK: response.OK, Reason: response.Reason, Message: response.Message, Patch: response.Patch, Result: response.Result}
}

func (s *ProtocolStarter) SendMail(ctx context.Context, extensionID string, request MailProviderRequest) (MailProviderResponse, error) {
	s.mu.Lock()
	protocol := s.protocols[extensionID]
	s.mu.Unlock()
	if protocol == nil {
		return MailProviderResponse{}, extensions.ErrRuntimeUnavailable
	}
	s.mu.Lock()
	s.recordProtocolCallLocked(extensionID)
	s.mu.Unlock()
	type outcome struct {
		resp MailProviderResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := protocol.SendMail(request)
		done <- outcome{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return MailProviderResponse{
			OK:             false,
			Classification: "temporary",
			Reason:         "extension.hook_timeout",
			Message:        "Mail provider call exceeded the host timeout.",
		}, nil
	case out := <-done:
		return out.resp, out.err
	}
}

func (s *ProtocolStarter) ProviderProbe(ctx context.Context, extensionID string, request ProviderProbeRequest) (ProviderProbeResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(protocol PluginProtocol) (ProviderProbeResponse, error) {
		return protocol.ProviderProbe(request)
	}, ProviderProbeResponse{Reason: "extension.action_timeout", Message: "Provider probe exceeded the host timeout."})
}

// protocolFor 返回已启动扩展的协议面；未运行时返回 nil。
func (s *ProtocolStarter) protocolFor(extensionID string) PluginProtocol {
	s.mu.Lock()
	defer s.mu.Unlock()
	protocol := s.protocols[extensionID]
	if protocol != nil {
		s.recordProtocolCallLocked(extensionID)
	}
	return protocol
}

// callStorage 在 ctx 截止前执行一次存储 RPC。
// 超时返回 onTimeout（err=nil），与 SendMail 一致，便于宿主按 OK/Reason 处理。
func callStorage[T any](ctx context.Context, protocol PluginProtocol, fn func(PluginProtocol) (T, error), onTimeout T) (T, error) {
	var zero T
	if protocol == nil {
		return zero, extensions.ErrRuntimeUnavailable
	}
	type outcome struct {
		resp T
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := fn(protocol)
		done <- outcome{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return onTimeout, nil
	case out := <-done:
		return out.resp, out.err
	}
}

func (s *ProtocolStarter) StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageSessionResponse, error) {
		return p.StoragePutBegin(request)
	}, StorageSessionResponse{Reason: "extension.hook_timeout", Message: "Storage PutBegin exceeded the host timeout."})
}

func (s *ProtocolStarter) StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StoragePutChunk(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage PutChunk exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageSessionResponse, error) {
		return p.StorageOpen(request)
	}, StorageSessionResponse{Reason: "extension.hook_timeout", Message: "Storage Open exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageGetChunkResponse, error) {
		return p.StorageGetChunk(request)
	}, StorageGetChunkResponse{Reason: "extension.hook_timeout", Message: "Storage GetChunk exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageClose(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage Close exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageDelete(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage Delete exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageStatResponse, error) {
		return p.StorageStat(request)
	}, StorageStatResponse{Reason: "extension.hook_timeout", Message: "Storage Stat exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageExistsResponse, error) {
		return p.StorageExists(request)
	}, StorageExistsResponse{Reason: "extension.hook_timeout", Message: "Storage Exists exceeded the host timeout."})
}

func (s *ProtocolStarter) StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageURLResponse, error) {
		return p.StoragePublicURL(request)
	}, StorageURLResponse{Reason: "extension.hook_timeout", Message: "Storage PublicURL exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageURLResponse, error) {
		return p.StorageSignedURL(request)
	}, StorageURLResponse{Reason: "extension.hook_timeout", Message: "Storage SignedURL exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageProbeResponse, error) {
		return p.StorageProbe(request)
	}, StorageProbeResponse{Reason: "extension.hook_timeout", Message: "Storage Probe exceeded the host timeout."})
}
