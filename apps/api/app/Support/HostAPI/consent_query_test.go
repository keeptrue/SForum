package hostapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const consentTestEmail = "alice.consent@example.test"

// 同意式读取面的证据：定义/投影/直连拒绝/签发门控/执行门控/审计/限流/无值。

func TestConsentGatedSelfEmailDefinitionSealsCatalogContract(t *testing.T) {
	definition := selfEmailQueryDefinition()
	if definition.ID != QuerySelfEmail || definition.PlanVersion != QueryStableCorePlanVersion ||
		definition.ResultSchemaID != QuerySelfEmailSchemaID || definition.ResultSchemaVersion != QueryStableCoreResultSchemaV1 {
		t.Fatalf("definition identity drift: %#v", definition)
	}
	if !definition.ConsentGated || !definition.ActorScoped || !definition.Single {
		t.Fatalf("definition flags: %#v", definition)
	}
	if len(definition.Filters) != 0 || len(definition.RequiredFilters) != 0 || len(definition.Sorts) != 0 {
		t.Fatalf("self-email must not expose filters/sorts: %#v", definition)
	}
	fieldNames := make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		fieldNames = append(fieldNames, field.Name)
	}
	if got := strings.Join(fieldNames, ","); got != "email,email_verified,updated_at" {
		t.Fatalf("fields = %s", got)
	}
	if !strings.Contains(definition.From, protocolV2ActorSQLToken) ||
		!strings.Contains(definition.From, "public.users") ||
		strings.Contains(definition.From, "email_lower") {
		t.Fatalf("from clause = %s", definition.From)
	}
	if !isStableProtocolV2QueryID(QuerySelfEmail) {
		t.Fatal("self-email stable id must stay reserved so a plugin cannot shadow it")
	}

	mapping, err := queryRegistryCoreMappingFor(definition)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.QueryID != QuerySelfEmailRegistryID || mapping.ContractVersion != QuerySelfEmailRegistryContractVersion ||
		mapping.PlanVersion != QuerySelfEmailRegistryPlanVersion ||
		mapping.ResultSchema != QuerySelfEmailSchemaID+"@"+QueryStableCoreResultSchemaV1 ||
		mapping.PermissionPolicy != queryregistry.PermissionPolicyLogin ||
		mapping.Pagination != queryregistry.PaginationNone {
		t.Fatalf("registry mapping = %#v", mapping)
	}
	if !IsProtocolV2ConsentGatedQuery(mapping.QueryID) {
		t.Fatal("registry projection must be classified consent-gated")
	}
	if IsProtocolV2ConsentGatedQuery("core.query.site_stats.current") {
		t.Fatal("ordinary stable query must not be consent-gated")
	}

	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var declaration *queryregistry.QueryDeclaration
	for index, binding := range catalog.Bindings() {
		if binding.HostQueryID != QuerySelfEmail {
			continue
		}
		declaration = &catalog.Publication().Queries[index]
	}
	if declaration == nil {
		t.Fatal("missing self-email projection")
	}
	if declaration.PermissionPolicy != queryregistry.PermissionPolicyLogin ||
		declaration.Pagination != queryregistry.PaginationNone ||
		len(declaration.CacheTags) != 0 || len(declaration.Filters) != 0 || len(declaration.Sort) != 0 {
		t.Fatalf("declaration = %#v", declaration)
	}
	if got := strings.Join(declaration.Fields, ","); got != "email,email_verified,updated_at" {
		t.Fatalf("declaration fields = %s", got)
	}

	body, _, err := queryRegistryCoreResultSchemaDocument(definition)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 3 {
		t.Fatalf("schema properties = %#v", schema.Properties)
	}
	if schema.Properties["email"].Type != "string" ||
		schema.Properties["email_verified"].Type != "boolean" ||
		schema.Properties["updated_at"].Type != "string" {
		t.Fatalf("schema types = %#v", schema.Properties)
	}
	if _, leaked := schema.Properties["email_lower"]; leaked {
		t.Fatal("email_lower must never enter the result schema")
	}
}

func TestConsentQueryStableEntryRefusesActorlessExecution(t *testing.T) {
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("consent-gated definition must not reach the actorless executor")
		return nil, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true, Capabilities: []string{"users.email.read"}}, nil
	}))
	traces := &recordingQueryTraceSink{}
	engine.traceSink = traces
	response := engine.execute(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()),
		&hostv2.QueryRequest{
			Context: testProtocolV2RequestContext(), QueryId: QuerySelfEmail, PlanVersion: QueryStableCorePlanVersion,
		},
	)
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED ||
		response.GetError().GetReason() != "host.query_actor_delegation_invalid" ||
		len(response.GetRows()) != 0 {
		t.Fatalf("stable entry response = %#v", response.GetError())
	}
	if len(traces.traces) != 1 || traces.traces[0].Outcome != QueryTraceDenied || traces.traces[0].Rows != 0 {
		t.Fatalf("traces = %#v", traces.traces)
	}
}

func TestConsentQueryDelegationIssuanceRequiresCapabilityAndActor(t *testing.T) {
	h := newConsentQueryHarness(t)
	grant, err := h.issue(42)
	if err != nil {
		t.Fatal(err)
	}
	if grant.QueryID != QuerySelfEmailRegistryID || grant.ContractVersion != QuerySelfEmailRegistryContractVersion ||
		grant.PlanVersion != QuerySelfEmailRegistryPlanVersion ||
		grant.ResultSchemaID != QuerySelfEmailSchemaID || grant.ResultSchemaVersion != QueryStableCoreResultSchemaV1 ||
		grant.Scope != ProtocolV2ConsentQueryScope || strings.TrimSpace(grant.Token) == "" {
		t.Fatalf("grant = %#v", grant)
	}
	if h.capabilities.firstCall() != "users.email.read" {
		t.Fatalf("capability checks = %#v", h.capabilities.calls)
	}

	// 匿名（actor 0）拿不到委派。
	if _, err := h.issue(0); !errors.Is(err, ErrProtocolV2QueryActorDenied) {
		t.Fatalf("anonymous issuance error = %v", err)
	}

	// 未声明能力：签发侧直接拒绝，不存在「先给委派再由查询拒绝」。
	h.capabilities.setErr(ErrProtocolV2QueryCapabilityDenied)
	if _, err := h.issue(42); !errors.Is(err, ErrProtocolV2QueryCapabilityDenied) {
		t.Fatalf("capability-denied issuance error = %v", err)
	}

	// 普通稳定查询的投影不受 consent 门控影响。
	h.capabilities.setErr(nil)
	h.capabilities.resetCalls()
	if _, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), ProtocolV2QueryActorDelegationRequest{
		ActorUserID: 42, Runtime: h.runtime, QueryID: "core.query.site_stats.current",
		Locale: "zh-CN", Scope: "admin.stats", MaxCost: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.capabilities.calls) != 0 {
		t.Fatalf("ordinary query consulted consent capabilities: %#v", h.capabilities.calls)
	}
}

func TestConsentQueryExecuteAllowedAuditsWithoutValues(t *testing.T) {
	h := newConsentQueryHarness(t)
	grant, err := h.issue(42)
	if err != nil {
		t.Fatal(err)
	}
	request := h.request(grant)
	response := h.execute(request)
	if response.GetError() != nil || len(response.GetRows()) != 1 {
		t.Fatalf("response = %#v err=%v", response, response.GetError())
	}
	row := response.GetRows()[0].GetValue().AsMap()
	if row["email"] != consentTestEmail || row["email_verified"] != true ||
		row["updated_at"] != "2026-10-18T09:00:00Z" {
		t.Fatalf("row = %#v", row)
	}
	if h.executorCalls.Load() != 1 || h.lastActor != 42 || !h.lastActorBound {
		t.Fatalf("executor calls=%d actor=%d bound=%t", h.executorCalls.Load(), h.lastActor, h.lastActorBound)
	}

	// 审计：一条、有标识、无值。
	events := h.auditor.snapshot()
	if len(events) != 1 {
		t.Fatalf("audit events = %#v", events)
	}
	event := events[0]
	if event.Action != ProtocolV2ConsentQueryAuditAction || event.ActorUserID != 42 {
		t.Fatalf("audit event = %#v", event)
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), consentTestEmail) {
		t.Fatalf("audit metadata leaked the email: %s", metadata)
	}
	if !strings.Contains(string(metadata), QuerySelfEmailRegistryID) ||
		!strings.Contains(string(metadata), "caller.plugin") {
		t.Fatalf("audit metadata is missing identifiers: %s", metadata)
	}

	// 限流：键按 (actor, extension) 隔离，窗口与上限来自 gate 默认值。
	if len(h.limiter.keys) != 1 || !strings.HasPrefix(h.limiter.keys[0], "email_claim:42:caller.plugin") {
		t.Fatalf("rate limit keys = %#v", h.limiter.keys)
	}
	if h.limiter.max != ProtocolV2ConsentQueryRateLimitMax ||
		h.limiter.window != ProtocolV2ConsentQueryRateLimitWindow {
		t.Fatalf("rate limit bound = %d/%s", h.limiter.max, h.limiter.window)
	}

	// 重放：同一令牌第二次必须被账本拒绝，且不再读行。
	replay := h.execute(h.request(grant))
	if replay.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_CONFLICT ||
		replay.GetError().GetReason() != "host.query_actor_delegation_replayed" {
		t.Fatalf("replay error = %#v", replay.GetError())
	}
	if h.executorCalls.Load() != 1 || len(h.auditor.snapshot()) != 1 {
		t.Fatalf("replay reached the executor: calls=%d audits=%d", h.executorCalls.Load(), len(h.auditor.snapshot()))
	}

	// trace：只有标识与行数，没有任何字段值。
	traceText := h.traceBuffer.String()
	if strings.Contains(traceText, consentTestEmail) || strings.Contains(traceText, "email_verified") {
		t.Fatalf("trace leaked result values: %s", traceText)
	}
	if !strings.Contains(traceText, QuerySelfEmailRegistryID) || !strings.Contains(traceText, "rows=1") {
		t.Fatalf("trace is missing host metadata: %s", traceText)
	}
}

func TestConsentQueryExecuteDeniedWithoutCapability(t *testing.T) {
	h := newConsentQueryHarness(t)
	grant, err := h.issue(42)
	if err != nil {
		t.Fatal(err)
	}
	h.capabilities.setErr(ErrProtocolV2QueryCapabilityDenied)
	response := h.execute(h.request(grant))
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED ||
		response.GetError().GetReason() != "host.query_capability_denied" {
		t.Fatalf("capability denial = %#v", response.GetError())
	}
	if h.executorCalls.Load() != 0 || len(h.auditor.snapshot()) != 0 || len(h.limiter.keys) != 0 {
		t.Fatalf("denied call touched executor/audit/limiter: calls=%d audits=%d limits=%d",
			h.executorCalls.Load(), len(h.auditor.snapshot()), len(h.limiter.keys))
	}
}

func TestConsentQueryExecuteRateLimited(t *testing.T) {
	h := newConsentQueryHarness(t)
	h.limiter.setDeny(true)
	grant, err := h.issue(42)
	if err != nil {
		t.Fatal(err)
	}
	response := h.execute(h.request(grant))
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED ||
		response.GetError().GetReason() != "host.query_rate_limited" {
		t.Fatalf("rate limit denial = %#v", response.GetError())
	}
	if h.executorCalls.Load() != 0 || len(h.auditor.snapshot()) != 0 {
		t.Fatalf("rate-limited call reached executor/audit: calls=%d audits=%d",
			h.executorCalls.Load(), len(h.auditor.snapshot()))
	}
}

func TestConsentQueryExecuteRejectsMissingOrForeignDelegation(t *testing.T) {
	h := newConsentQueryHarness(t)

	missing := h.request(ProtocolV2QueryActorDelegationGrant{
		QueryID: QuerySelfEmailRegistryID, ContractVersion: QuerySelfEmailRegistryContractVersion,
		PlanVersion:    QuerySelfEmailRegistryPlanVersion,
		ResultSchemaID: QuerySelfEmailSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
		Scope: ProtocolV2ConsentQueryScope,
	})
	response := h.execute(missing)
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED ||
		response.GetError().GetReason() != "host.query_actor_delegation_invalid" {
		t.Fatalf("missing delegation = %#v", response.GetError())
	}

	// 另一条查询的委派不能被复用（绑定不符）。
	foreign, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), ProtocolV2QueryActorDelegationRequest{
		ActorUserID: 42, Runtime: h.runtime, QueryID: "core.query.site_stats.current",
		Locale: "zh-CN", Scope: ProtocolV2ConsentQueryScope, MaxCost: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := h.request(foreign)
	request.QueryId = QuerySelfEmailRegistryID
	request.ContractVersion = QuerySelfEmailRegistryContractVersion
	request.PlanVersion = QuerySelfEmailRegistryPlanVersion
	request.ResultSchemaId = QuerySelfEmailSchemaID
	request.ResultSchemaVersion = QueryStableCoreResultSchemaV1
	response = h.execute(request)
	if response.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED ||
		response.GetError().GetReason() != "host.query_actor_delegation_invalid" {
		t.Fatalf("foreign delegation = %#v", response.GetError())
	}
	if h.executorCalls.Load() != 0 {
		t.Fatalf("invalid delegations reached the executor: %d", h.executorCalls.Load())
	}
}

// ---- harness -------------------------------------------------------------

type consentQueryHarness struct {
	service        *ProtocolV2QueryRegistryService
	runtime        *protocolv2.ExtensionIdentity
	capabilities   *consentCapabilityStub
	auditor        *consentAuditorStub
	limiter        *consentLimiterStub
	traceBuffer    *bytes.Buffer
	executorCalls  atomic.Int64
	lastActor      int64
	lastActorBound bool
}

func newConsentQueryHarness(t *testing.T) *consentQueryHarness {
	t.Helper()
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	registry, _, err := NewQueryRegistryCoreRegistry(QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h := &consentQueryHarness{
		runtime: &protocolv2.ExtensionIdentity{
			ExtensionId: "caller.plugin", ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64),
			TrustGrantId: "41", RuntimeEpoch: 7, InstanceId: "caller-instance-7",
		},
		capabilities: &consentCapabilityStub{},
		auditor:      &consentAuditorStub{},
		limiter:      &consentLimiterStub{},
		traceBuffer:  &bytes.Buffer{},
	}
	executor := protocolV2QueryExecutorFunc(func(ctx context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
		h.executorCalls.Add(1)
		h.lastActor, h.lastActorBound = shortcodeProjectionActorFromContext(ctx)
		return []map[string]any{{
			"email": consentTestEmail, "email_verified": true, "updated_at": "2026-10-18T09:00:00Z",
		}}, nil
	})
	authority := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	stableRuntime, err := newProtocolV2QueryRuntime(executor, authority, stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewProtocolV2QueryRegistryProviderResolver(stableRuntime, catalog.Bindings())
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := queryregistry.NewJSONResultSchemaCatalog(catalog.Schemas())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Schemas: schemas,
		Trace:   NewQueryRegistryTraceAdapter(NewSlogQueryTraceSink(slog.New(slog.NewTextHandler(h.traceBuffer, &slog.HandlerOptions{Level: slog.LevelDebug})))),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	actors := &protocolV2QueryTestActors{projection: ProtocolV2QueryActorProjection{
		ActorUserID: 42, Authenticated: true, ActorFingerprint: "actor:42", PolicyFingerprint: "policy:member",
	}}
	actors.authorize = func(actorUserID int64, claim queryregistry.PermissionClaim, _ int64) (ProtocolV2QueryActorProjection, error) {
		if actorUserID != 42 || claim.PermissionPolicy != queryregistry.PermissionPolicyLogin {
			return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
		}
		return actors.projection, nil
	}
	delegations, err := newProtocolV2QueryDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), time.Now, protocolV2QueryDelegationTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	h.service, err = newProtocolV2QueryRegistryService(
		registry, execution, actors, &protocolV2QueryTestCallerAdmission{}, delegations,
		WithProtocolV2QueryConsentGate(&ProtocolV2QueryConsentGate{
			Capabilities: h.capabilities, Auditor: h.auditor, Limiter: h.limiter,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *consentQueryHarness) issue(actorUserID int64) (ProtocolV2QueryActorDelegationGrant, error) {
	return h.service.IssueProtocolV2QueryActorDelegation(context.Background(), ProtocolV2QueryActorDelegationRequest{
		ActorUserID: actorUserID, Runtime: h.runtime, QueryID: QuerySelfEmailRegistryID,
		Locale: "zh-CN", Scope: ProtocolV2ConsentQueryScope, MaxCost: ProtocolV2ConsentQueryMaxCost,
	})
}

func (h *consentQueryHarness) request(grant ProtocolV2QueryActorDelegationGrant) *hostv2.QueryRequest {
	return &hostv2.QueryRequest{
		Context: &protocolv2.RequestContext{
			RequestId: "consent-request-1", Locale: "zh-CN",
			Extension: cloneProtocolV2ExtensionIdentity(h.runtime),
		},
		QueryId: grant.QueryID, ContractVersion: grant.ContractVersion, PlanVersion: grant.PlanVersion,
		ResultSchemaId: grant.ResultSchemaID, ResultSchemaVersion: grant.ResultSchemaVersion,
		Scope: grant.Scope, ActorDelegation: grant.Token, Page: &protocolv2.PageRequest{Limit: 1},
	}
}

func (h *consentQueryHarness) execute(request *hostv2.QueryRequest) *hostv2.QueryResponse {
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), h.runtime)
	return h.service.execute(ctx, request)
}

type consentCapabilityStub struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (s *consentCapabilityStub) AuthorizeProtocolV2QueryCapability(
	_ context.Context,
	_ *protocolv2.ExtensionIdentity,
	capability string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, capability)
	return s.err
}

func (s *consentCapabilityStub) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *consentCapabilityStub) resetCalls() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

func (s *consentCapabilityStub) firstCall() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return ""
	}
	return s.calls[0]
}

type consentAuditorStub struct {
	mu     sync.Mutex
	events []audit.Event
}

func (s *consentAuditorStub) Append(_ context.Context, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *consentAuditorStub) snapshot() []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Event(nil), s.events...)
}

type consentLimiterStub struct {
	mu     sync.Mutex
	keys   []string
	max    int
	window time.Duration
	deny   bool
	err    error
}

func (s *consentLimiterStub) Allow(_ context.Context, key string, max int, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	s.max, s.window = max, window
	if s.err != nil {
		return false, s.err
	}
	if s.deny {
		return false, nil
	}
	return true, nil
}

func (s *consentLimiterStub) setDeny(deny bool) {
	s.mu.Lock()
	s.deny = deny
	s.mu.Unlock()
}

// 精确制品能力投影：manifest 只存显式声明，影响评审存完整 Grant 列表（含宿主
// 推断项）。比较前必须过滤 implied，否则任何 backend/jobs 插件都会被误判漂移。
func TestConsentQueryAuthorityCapabilityProjection(t *testing.T) {
	manifest, err := protocolV2QueryManifestCapabilities([]byte(`["users.email.read","host.api"]`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(manifest, ","); got != "host.api,users.email.read" {
		t.Fatalf("manifest capabilities = %s", got)
	}
	impact, err := protocolV2QueryImpactCapabilities([]byte(
		`[{"key":"users.email.read"},{"key":"host.api"},{"key":"jobs.enqueue","implied":true}]`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(impact, ","); got != "host.api,users.email.read" {
		t.Fatalf("impact explicit capabilities = %s", got)
	}
	for _, document := range []string{`null`, ``, `[]`} {
		caps, err := protocolV2QueryManifestCapabilities([]byte(document))
		if err != nil || len(caps) != 0 {
			t.Fatalf("manifest %q = %v, %v", document, caps, err)
		}
		caps, err = protocolV2QueryImpactCapabilities([]byte(document))
		if err != nil || len(caps) != 0 {
			t.Fatalf("impact %q = %v, %v", document, caps, err)
		}
	}
	for _, document := range []string{`["host.api","host.api"]`, `[" host.api"]`, `{"key":"host.api"}`} {
		if _, err := protocolV2QueryManifestCapabilities([]byte(document)); err == nil {
			t.Fatalf("manifest %q must fail closed", document)
		}
	}
	if _, err := protocolV2QueryImpactCapabilities([]byte(`[{"key":"host.api"},{"key":"host.api"}]`)); err == nil {
		t.Fatal("duplicate impact capability must fail closed")
	}
}
