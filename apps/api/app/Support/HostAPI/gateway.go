package hostapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

// Gateway 在 loopback 上暴露 Host API，供插件子进程调用（F2.2）。
// 每个已启动插件持有独立 token；请求必须带 X-SForum-Extension-Id 与 Bearer token。
type Gateway struct {
	mu                            sync.RWMutex
	service                       *Service
	services                      *ServiceRegistry
	commands                      *protocolV2CommandEngine
	queries                       *protocolV2QueryEngine
	queryRegistry                 *ProtocolV2QueryRegistryService
	database                      *protocolV2DatabaseEngine
	cache                         *ProtocolV2CacheServiceServer
	secrets                       *ProtocolV2SecretServiceServer
	files                         *ProtocolV2FileServiceServer
	httpClient                    *ProtocolV2HttpServiceServer
	session                       *ProtocolV2SessionServiceServer
	providers                     ProtocolV2ProviderBroker
	protocolV2CommandsFrozen      bool
	protocolV2QueriesFrozen       bool
	protocolV2QueryRegistryFrozen bool
	protocolV2DatabaseFrozen      bool
	protocolV2CacheFrozen         bool
	protocolV2SecretsFrozen       bool
	protocolV2FilesFrozen         bool
	protocolV2HTTPFrozen          bool
	protocolV2SessionFrozen       bool
	protocolV2ProvidersFrozen     bool
	server                        *http.Server
	ln                            net.Listener
	baseURL                       string
	tokens                        map[string]string // extensionID → token
}

// RegisterProtocolV2 exposes typed Host services only on the caller's
// runtime-bound go-plugin broker server.
func (g *Gateway) RegisterProtocolV2(server grpc.ServiceRegistrar) {
	if g == nil || server == nil {
		return
	}
	g.mu.Lock()
	service := g.service
	services := g.services
	commands := g.commands
	queries := g.queries
	queryRegistry := g.queryRegistry
	database := g.database
	cache := g.cache
	secrets := g.secrets
	files := g.files
	httpClient := g.httpClient
	session := g.session
	providers := g.providers
	g.protocolV2CommandsFrozen = true
	g.protocolV2QueriesFrozen = true
	g.protocolV2QueryRegistryFrozen = true
	g.protocolV2DatabaseFrozen = true
	g.protocolV2CacheFrozen = true
	g.protocolV2SecretsFrozen = true
	g.protocolV2FilesFrozen = true
	g.protocolV2HTTPFrozen = true
	g.protocolV2SessionFrozen = true
	g.protocolV2ProvidersFrozen = true
	g.mu.Unlock()
	registerProtocolV2(server, service, services, commands, queries, queryRegistry, database, cache, secrets, files, httpClient, session, providers)
}

// BindProtocolV2CacheService installs the production cache boundary before any
// plugin broker is registered. Each broker retains this exact server pointer
// for its lifetime; changing cache contracts requires a new Gateway boot.
func (g *Gateway) BindProtocolV2CacheService(server *ProtocolV2CacheServiceServer) error {
	if g == nil || server == nil || server.service == nil {
		return fmt.Errorf("hostapi: protocol v2 cache service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2CacheFrozen {
		return fmt.Errorf("hostapi: protocol v2 cache service is frozen until the next Gateway boot")
	}
	if g.cache != nil {
		return fmt.Errorf("hostapi: protocol v2 cache service is already bound")
	}
	g.cache = server
	return nil
}

// BindProtocolV2SessionService installs the S9 plugin session-state boundary
// before any plugin broker is registered, so every broker for this boot serves
// the same session contract.
func (g *Gateway) BindProtocolV2SessionService(server *ProtocolV2SessionServiceServer) error {
	if g == nil || server == nil || server.store == nil || server.delegations == nil {
		return fmt.Errorf("hostapi: protocol v2 session service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2SessionFrozen {
		return fmt.Errorf("hostapi: protocol v2 session service is frozen until the next Gateway boot")
	}
	if g.session != nil {
		return fmt.Errorf("hostapi: protocol v2 session service is already bound")
	}
	g.session = server
	return nil
}

// BindProtocolV2SecretService installs the production Secret Store boundary.
func (g *Gateway) BindProtocolV2SecretService(server *ProtocolV2SecretServiceServer) error {
	if g == nil || server == nil || server.secrets == nil {
		return fmt.Errorf("hostapi: protocol v2 secret service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2SecretsFrozen {
		return fmt.Errorf("hostapi: protocol v2 secret service is frozen until the next Gateway boot")
	}
	if g.secrets != nil {
		return fmt.Errorf("hostapi: protocol v2 secret service is already bound")
	}
	g.secrets = server
	return nil
}

// BindProtocolV2FileService installs the production Plugin Files boundary.
func (g *Gateway) BindProtocolV2FileService(server *ProtocolV2FileServiceServer) error {
	if g == nil || server == nil || server.files == nil {
		return fmt.Errorf("hostapi: protocol v2 file service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2FilesFrozen {
		return fmt.Errorf("hostapi: protocol v2 file service is frozen until the next Gateway boot")
	}
	if g.files != nil {
		return fmt.Errorf("hostapi: protocol v2 file service is already bound")
	}
	g.files = server
	return nil
}

// BindProtocolV2HttpService installs the production Host HTTP boundary.
func (g *Gateway) BindProtocolV2HttpService(server *ProtocolV2HttpServiceServer) error {
	if g == nil || server == nil || server.http == nil {
		return fmt.Errorf("hostapi: protocol v2 http service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2HTTPFrozen {
		return fmt.Errorf("hostapi: protocol v2 http service is frozen until the next Gateway boot")
	}
	if g.httpClient != nil {
		return fmt.Errorf("hostapi: protocol v2 http service is already bound")
	}
	g.httpClient = server
	return nil
}

func (g *Gateway) BindProtocolV2ProviderBroker(provider ProtocolV2ProviderBroker) error {
	if g == nil || provider == nil {
		return fmt.Errorf("hostapi: protocol v2 provider broker is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2ProvidersFrozen {
		return fmt.Errorf("hostapi: protocol v2 provider broker is frozen until the next Gateway boot")
	}
	if g.providers != nil {
		return fmt.Errorf("hostapi: protocol v2 provider broker is already bound")
	}
	g.providers = provider
	return nil
}

// BindProtocolV2DatabaseRuntime installs one exact-artifact statement catalog.
// Broker registration freezes it so SQL and authority bindings cannot drift
// under an already-running plugin process.
func (g *Gateway) BindProtocolV2DatabaseRuntime(runtime ProtocolV2DatabaseRuntime) error {
	if g == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	if runtime == nil || runtime.databaseEngine() == nil {
		return fmt.Errorf("hostapi: protocol v2 database runtime is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2DatabaseFrozen {
		return fmt.Errorf("hostapi: protocol v2 database runtime is frozen until the next Gateway boot")
	}
	if g.database != nil {
		return fmt.Errorf("hostapi: protocol v2 database runtime is already bound")
	}
	g.database = runtime.databaseEngine()
	return nil
}

// PublishProtocolV2DatabaseRuntime replaces the snapshot used only by future
// broker registrations. Already-registered brokers retain their immutable
// engine pointer, so an upgrade cannot change SQL beneath an active process.
func (g *Gateway) PublishProtocolV2DatabaseRuntime(runtime ProtocolV2DatabaseRuntime) error {
	if g == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	if runtime == nil || runtime.databaseEngine() == nil {
		return fmt.Errorf("hostapi: protocol v2 database runtime is required")
	}
	g.mu.Lock()
	g.database = runtime.databaseEngine()
	g.mu.Unlock()
	return nil
}

// BindProtocolV2QueryRuntime installs the immutable stable-query catalog once.
// Broker registration freezes the snapshot for the lifetime of this Gateway.
func (g *Gateway) BindProtocolV2QueryRuntime(runtime ProtocolV2QueryRuntime) error {
	if g == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	if runtime == nil || runtime.queryEngine() == nil {
		return fmt.Errorf("hostapi: protocol v2 query runtime is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2QueriesFrozen {
		return fmt.Errorf("hostapi: protocol v2 query runtime is frozen until the next Gateway boot")
	}
	if g.queries != nil {
		return fmt.Errorf("hostapi: protocol v2 query runtime is already bound")
	}
	g.queries = runtime.queryEngine()
	return nil
}

// BindProtocolV2QueryRegistryService installs the actor-delegated Query
// Registry outlet exactly once. Broker registration freezes the pointer for the
// Gateway lifetime; bootstrap must construct the complete execution runtime
// before binding it.
func (g *Gateway) BindProtocolV2QueryRegistryService(service *ProtocolV2QueryRegistryService) error {
	if g == nil || service == nil || service.registry == nil || service.execution == nil ||
		!service.execution.BoundToRegistry(service.registry) || service.actors == nil ||
		service.callerAdmission == nil || service.delegations == nil {
		return fmt.Errorf("hostapi: protocol v2 Query Registry service is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2QueryRegistryFrozen {
		return fmt.Errorf("hostapi: protocol v2 Query Registry service is frozen until the next Gateway boot")
	}
	if g.queryRegistry != nil {
		return fmt.Errorf("hostapi: protocol v2 Query Registry service is already bound")
	}
	g.queryRegistry = service
	return nil
}

// IssueProtocolV2QueryActorDelegation exposes only the Host-side issuer. The
// signed token is delivered to one plugin invocation and can be consumed once.
func (g *Gateway) IssueProtocolV2QueryActorDelegation(
	ctx context.Context,
	request ProtocolV2QueryActorDelegationRequest,
) (ProtocolV2QueryActorDelegationGrant, error) {
	if g == nil {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryDelegationInvalid
	}
	g.mu.RLock()
	service := g.queryRegistry
	g.mu.RUnlock()
	if service == nil {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryDelegationInvalid
	}
	return service.IssueProtocolV2QueryActorDelegation(ctx, request)
}

// BindProtocolV2CommandRuntime installs one immutable Host-owned command
// catalog. RegisterProtocolV2 freezes the catalog; replacement then requires a
// new Gateway/server boot so running brokers never observe contract drift.
func (g *Gateway) BindProtocolV2CommandRuntime(runtime ProtocolV2CommandRuntime) error {
	if g == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	if runtime == nil {
		return fmt.Errorf("hostapi: protocol v2 command runtime is required")
	}
	engine := runtime.commandEngine()
	if engine == nil {
		return fmt.Errorf("hostapi: protocol v2 command runtime is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protocolV2CommandsFrozen {
		return fmt.Errorf("hostapi: protocol v2 command runtime is frozen until the next Gateway boot")
	}
	g.commands = engine
	return nil
}

// BindCommandCapabilitySource 注入进程能力源，供 extensions.manage 等命令门禁使用。
// 可在 catalog 冻结后调用，仅替换能力解析依赖，不改命令集合。
func (g *Gateway) BindCommandCapabilitySource(source CapabilitySource) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.commands != nil {
		g.commands.BindCapabilitySource(source)
	}
}

// ProtocolV2ActorDelegationIssuer exposes only the boot-scoped issuer used by
// Host-owned route/admin adapters. It is nil until a delegated command catalog
// is bound and never crosses the plugin transport boundary.
func (g *Gateway) ProtocolV2ActorDelegationIssuer() ProtocolV2ActorDelegationIssuer {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.commands == nil || g.commands.delegations == nil {
		return nil
	}
	return g.commands.delegations
}

// IssueProtocolV2ActorDelegations derives command scopes from the immutable
// Host catalog instead of duplicating them in a route, controller, or manifest.
func (g *Gateway) IssueProtocolV2ActorDelegations(
	ctx context.Context,
	request ProtocolV2ActorDelegationBundleRequest,
) ([]ProtocolV2ActorDelegationGrant, error) {
	if g == nil {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	g.mu.RLock()
	commands := g.commands
	g.mu.RUnlock()
	return commands.issueProtocolV2ActorDelegations(ctx, request)
}

// NewGateway 创建未启动的网关；Start 后才监听。
func NewGateway(service *Service) *Gateway {
	return &Gateway{
		service:  service,
		services: NewServiceRegistry(),
		tokens:   map[string]string{},
	}
}

// MintProtocolV2SessionDelegation 让 Gateway 本身成为 S9 会话委派的签发者。
//
// 生产装配把裸 Gateway 作为 `HostAPIRegistrar` 传给 ProtocolStarter
// （bootstrap/app.go），protocolV2Client 通过类型断言解析 S9 签发方；缺这两个
// 方法时带会话范围的宿主发起调用会直接失败。语义与延迟策略照旧：只在绑定了
// 会话服务的 Gateway 上可用，未绑定时 fail closed，不静默降级。
func (g *Gateway) MintProtocolV2SessionDelegation(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	scope ProtocolV2SessionScope,
) (string, error) {
	if g == nil {
		return "", ErrProtocolV2SessionDelegationInvalid
	}
	g.mu.RLock()
	session := g.session
	g.mu.RUnlock()
	if session == nil {
		return "", fmt.Errorf("hostapi: protocol v2 session service is not bound; cannot mint a session delegation")
	}
	// Safe Mode 不拦 mint：签发只是悬挂一个令牌，真正的边界在三个操作上
	// （读静默未命中 + 写拒绝）。把失败推迟到操作层，插件得到的是它设计里的
	// 「按调用降级」，而不是整次调用失败。
	return session.delegations.MintProtocolV2SessionDelegation(ctx, identity, scope)
}

// RetireProtocolV2SessionDelegation 在调用结算时收回签发台账记录（幂等）。
func (g *Gateway) RetireProtocolV2SessionDelegation(token string) error {
	if g == nil {
		return ErrProtocolV2SessionDelegationInvalid
	}
	g.mu.RLock()
	session := g.session
	g.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("hostapi: protocol v2 session service is not bound; cannot retire a session delegation")
	}
	return session.delegations.RetireProtocolV2SessionDelegation(token)
}

// ReplaceProtocolV2Services atomically publishes one runtime's complete
// handshake service declaration set.
func (g *Gateway) ReplaceProtocolV2Services(extensionID string, registrations []ServiceRegistration) error {
	registry := g.ProtocolV2ServiceRegistry()
	if registry == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	return registry.ReplaceExtension(extensionID, registrations)
}

// PublishProtocolV2ServiceRuntime atomically exposes one exact runtime as both
// a service provider and a dependency-authorized caller.
func (g *Gateway) PublishProtocolV2ServiceRuntime(publication ServiceRuntimePublication) error {
	registry := g.ProtocolV2ServiceRegistry()
	if registry == nil {
		return fmt.Errorf("hostapi gateway is nil")
	}
	return registry.ReplaceRuntime(publication)
}

// PrepareProtocolV2ServiceRuntimeSet validates a complete desired runtime
// graph without changing reader visibility. Commit publishes the graph once.
func (g *Gateway) PrepareProtocolV2ServiceRuntimeSet(publications []ServiceRuntimePublication) (*ServiceRuntimeSetTransaction, error) {
	registry := g.ProtocolV2ServiceRegistry()
	if registry == nil {
		return nil, fmt.Errorf("hostapi gateway is nil")
	}
	return registry.PrepareRuntimeSet(publications)
}

// ProtocolV2ServiceRuntimeSetMatches verifies exact complete-set visibility
// without incrementing discovery revisions.
func (g *Gateway) ProtocolV2ServiceRuntimeSetMatches(publications []ServiceRuntimePublication) (bool, error) {
	registry := g.ProtocolV2ServiceRegistry()
	if registry == nil {
		return false, fmt.Errorf("hostapi gateway is nil")
	}
	return registry.RuntimeSetMatches(publications)
}

// AcquireProtocolV2ServiceRuntimeSet pins a verified complete graph against
// lifecycle writers until the caller releases the lease.
func (g *Gateway) AcquireProtocolV2ServiceRuntimeSet(publications []ServiceRuntimePublication) (*ServiceRuntimeSetLease, error) {
	return g.AcquireProtocolV2ServiceRuntimeSetContext(context.Background(), publications)
}

// AcquireProtocolV2ServiceRuntimeSetContext is the cancellable form used by
// coordinated runtime-set transitions.
func (g *Gateway) AcquireProtocolV2ServiceRuntimeSetContext(ctx context.Context, publications []ServiceRuntimePublication) (*ServiceRuntimeSetLease, error) {
	registry := g.ProtocolV2ServiceRegistry()
	if registry == nil {
		return nil, fmt.Errorf("hostapi gateway is nil")
	}
	return registry.AcquireRuntimeSetContext(ctx, publications)
}

// UnregisterProtocolV2Services removes all services owned by one runtime.
func (g *Gateway) UnregisterProtocolV2Services(extensionID string) {
	if registry := g.ProtocolV2ServiceRegistry(); registry != nil {
		registry.UnregisterExtension(extensionID)
	}
}

// UnregisterProtocolV2ServiceInstance removes services only when the current
// registry snapshot still belongs to the stopping runtime instance.
func (g *Gateway) UnregisterProtocolV2ServiceInstance(extensionID, instanceID string) bool {
	registry := g.ProtocolV2ServiceRegistry()
	return registry != nil && registry.UnregisterProtocolV2ServiceInstance(extensionID, instanceID)
}

// ProtocolV2ServiceRegistry exposes the shared immutable-snapshot registry to
// runtime assembly and host-owned inspectors.
func (g *Gateway) ProtocolV2ServiceRegistry() *ServiceRegistry {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	registry := g.services
	g.mu.RUnlock()
	return registry
}

// Start 绑定 127.0.0.1:0 并开始服务。
func (g *Gateway) Start() error {
	if g == nil || g.service == nil {
		return fmt.Errorf("hostapi gateway: service required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/call", g.handleCall)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	g.ln = ln
	g.baseURL = "http://" + ln.Addr().String()
	g.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = g.server.Serve(ln)
	}()
	return nil
}

// BaseURL 返回 loopback 根地址。
func (g *Gateway) BaseURL() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.baseURL
}

// RegisterExtension 为插件签发 token，返回应注入子进程的环境变量。
func (g *Gateway) RegisterExtension(extensionID string) (token string, env []string, err error) {
	if g == nil {
		return "", nil, fmt.Errorf("hostapi gateway is nil")
	}
	if err := g.Start(); err != nil {
		return "", nil, err
	}
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return "", nil, fmt.Errorf("extension id required")
	}
	token, err = randomToken()
	if err != nil {
		return "", nil, err
	}
	g.mu.Lock()
	g.tokens[extensionID] = token
	base := g.baseURL
	g.mu.Unlock()

	env = []string{
		"SFORUM_HOST_API_URL=" + base,
		"SFORUM_HOST_API_TOKEN=" + token,
		"SFORUM_EXTENSION_ID=" + extensionID,
		"SFORUM_HOST_API_VERSION=" + Version,
	}
	return token, env, nil
}

// UnregisterExtension 撤销 token。
func (g *Gateway) UnregisterExtension(extensionID string) {
	if g == nil {
		return
	}
	extensionID = strings.TrimSpace(extensionID)
	g.mu.Lock()
	delete(g.tokens, extensionID)
	services := g.services
	g.mu.Unlock()
	if services != nil {
		services.UnregisterExtension(extensionID)
	}
}

// Close 停止监听。
func (g *Gateway) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokens = map[string]string{}
	if g.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = g.server.Shutdown(ctx)
		g.server = nil
	}
	g.ln = nil
	g.baseURL = ""
	return nil
}

func (g *Gateway) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	extensionID := strings.TrimSpace(r.Header.Get("X-SForum-Extension-Id"))
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimPrefix(auth, "Bearer ")
	token = strings.TrimSpace(token)
	if extensionID == "" || token == "" {
		writeJSON(w, http.StatusUnauthorized, Response{OK: false, Reason: "host.unauthorized", Message: "missing extension credentials"})
		return
	}
	g.mu.RLock()
	expected := g.tokens[extensionID]
	service := g.service
	g.mu.RUnlock()
	if expected == "" || expected != token {
		writeJSON(w, http.StatusUnauthorized, Response{OK: false, Reason: "host.unauthorized", Message: "invalid extension credentials"})
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, Response{OK: false, Reason: "host.invalid_request", Message: "invalid JSON body"})
		return
	}
	// 强制 extensionId 为已认证身份，防止插件冒充其它扩展。
	req.ExtensionID = extensionID
	resp := service.Call(r.Context(), req)
	status := http.StatusOK
	if !resp.OK && resp.Reason == "host.capability_denied" {
		status = http.StatusForbidden
	}
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Client 是插件侧最小 Host API 客户端（也可作 SDK stub）。
type Client struct {
	BaseURL     string
	Token       string
	ExtensionID string
	HTTP        *http.Client
}

// Call 发起一次 Host API 调用。
func (c *Client) Call(ctx context.Context, method string, payload map[string]any) (Response, error) {
	if c == nil || c.BaseURL == "" {
		return Response{}, fmt.Errorf("hostapi client is not configured")
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout + time.Second}
	}
	body, err := json.Marshal(Request{Method: method, ExtensionID: c.ExtensionID, Payload: payload})
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/call", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-SForum-Extension-Id", c.ExtensionID)
	res, err := httpClient.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer res.Body.Close()
	var resp Response
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// ClientFromEnv 从插件进程环境变量构造客户端。
func ClientFromEnv() (*Client, error) {
	base := strings.TrimSpace(os.Getenv("SFORUM_HOST_API_URL"))
	token := strings.TrimSpace(os.Getenv("SFORUM_HOST_API_TOKEN"))
	ext := strings.TrimSpace(os.Getenv("SFORUM_EXTENSION_ID"))
	if base == "" || token == "" || ext == "" {
		return nil, fmt.Errorf("hostapi env not configured")
	}
	return &Client{BaseURL: base, Token: token, ExtensionID: ext}, nil
}
