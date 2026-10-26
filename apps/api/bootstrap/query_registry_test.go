package bootstrap

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type productionQueryActorStoreStub struct {
	actor identity.Actor
	err   error
}

func (s *productionQueryActorStoreStub) LoadActor(context.Context, int64) (identity.Actor, error) {
	return s.actor, s.err
}

func TestProductionQueryActorAuthorityUsesCanonicalLiveHostState(t *testing.T) {
	store := &productionQueryActorStoreStub{actor: identity.Actor{
		ID: 42, Status: identity.UserStatusActive,
		RoleKeys: []string{"moderator", "member", "moderator"},
		Permissions: map[string]bool{
			"query.test.read": true,
			"query.test.deny": false,
		},
		CreatedAt: time.Date(2026, 7, 16, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
	}}
	authority := &productionQueryActorAuthority{store: store}

	first, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	store.actor.RoleKeys = []string{"member", "moderator"}
	store.actor.Permissions = map[string]bool{"query.test.deny": false, "query.test.read": true}
	second, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !first.Authenticated || first.ActorUserID != 42 ||
		!strings.HasPrefix(first.ActorFingerprint, "actor:") ||
		!strings.HasPrefix(first.PolicyFingerprint, "policy:") {
		t.Fatalf("canonical projections differ: first=%#v second=%#v", first, second)
	}

	for _, policy := range []string{
		queryregistry.PermissionPolicyPublic,
		queryregistry.PermissionPolicyLogin,
		"query.test.read",
	} {
		projection, authorizeErr := authority.AuthorizeProtocolV2QueryActor(
			context.Background(), 42, queryregistry.PermissionClaim{PermissionPolicy: policy},
		)
		if authorizeErr != nil || projection != second {
			t.Fatalf("policy %q projection=%#v err=%v", policy, projection, authorizeErr)
		}
	}
	if _, err := authority.AuthorizeProtocolV2QueryActor(
		context.Background(), 42, queryregistry.PermissionClaim{PermissionPolicy: "query.test.denied"},
	); !errors.Is(err, hostapi.ErrProtocolV2QueryActorDenied) {
		t.Fatalf("denied permission error = %v", err)
	}

	store.actor.Permissions["query.test.changed"] = true
	changed, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ActorFingerprint != second.ActorFingerprint || changed.PolicyFingerprint == second.PolicyFingerprint {
		t.Fatalf("policy mutation projection=%#v before=%#v", changed, second)
	}
}

func TestProductionQueryActorAuthorityRejectsInactiveMissingAndCancelledActors(t *testing.T) {
	store := &productionQueryActorStoreStub{actor: identity.Actor{ID: 42, Status: identity.UserStatusDisabled}}
	authority := &productionQueryActorAuthority{store: store}
	if _, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42); !errors.Is(err, hostapi.ErrProtocolV2QueryActorDenied) {
		t.Fatalf("disabled actor error = %v", err)
	}
	store.actor.Status = identity.UserStatusBanned
	if _, err := authority.AuthorizeProtocolV2QueryActor(
		context.Background(), 42, queryregistry.PermissionClaim{PermissionPolicy: "sforum.shortcode.actor_decision"},
	); !errors.Is(err, hostapi.ErrProtocolV2QueryActorDenied) {
		t.Fatalf("banned actor error = %v", err)
	}
	store.actor = identity.Actor{ID: 7, Status: identity.UserStatusActive}
	if _, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42); !errors.Is(err, hostapi.ErrProtocolV2QueryActorDenied) {
		t.Fatalf("mismatched actor error = %v", err)
	}
	store.err = identity.ErrUserNotFound
	if _, err := authority.ResolveProtocolV2QueryActor(context.Background(), 42); !errors.Is(err, hostapi.ErrProtocolV2QueryActorDenied) {
		t.Fatalf("missing actor error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authority.ResolveProtocolV2QueryActor(cancelled, 42); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled actor error = %v", err)
	}
}

func TestProductionQueryActorAuthorityAllowsAnonymousOnlyForPublicAndShortcodeDecisions(t *testing.T) {
	authority := &productionQueryActorAuthority{store: &productionQueryActorStoreStub{}}
	resolved, err := authority.ResolveProtocolV2QueryActor(context.Background(), 0)
	if err != nil || resolved.ActorUserID != 0 || resolved.Authenticated ||
		resolved.ActorFingerprint != "anonymous" || resolved.PolicyFingerprint != "public" {
		t.Fatalf("anonymous projection = %#v, %v", resolved, err)
	}
	for _, policy := range []string{queryregistry.PermissionPolicyPublic, "sforum.shortcode.actor_decision"} {
		projection, authorizeErr := authority.AuthorizeProtocolV2QueryActor(
			context.Background(), 0, queryregistry.PermissionClaim{PermissionPolicy: policy},
		)
		if authorizeErr != nil || projection != resolved {
			t.Fatalf("anonymous policy %q projection=%#v err=%v", policy, projection, authorizeErr)
		}
	}
	for _, policy := range []string{queryregistry.PermissionPolicyLogin, "forum.posts.edit_any"} {
		if _, authorizeErr := authority.AuthorizeProtocolV2QueryActor(
			context.Background(), 0, queryregistry.PermissionClaim{PermissionPolicy: policy},
		); !errors.Is(authorizeErr, hostapi.ErrProtocolV2QueryActorDenied) {
			t.Fatalf("anonymous policy %q error=%v", policy, authorizeErr)
		}
	}
}

type productionQueryAdmissionStub struct {
	gate        *extensionsruntime.RuntimeAdmissionGate
	snapshot    extensionsruntime.RuntimeInstanceSnapshot
	err         error
	calls       int
	extensionID string
	class       extensionsruntime.RuntimeCallClass
}

func (s *productionQueryAdmissionStub) acquire(
	ctx context.Context,
	extensionID string,
	class extensionsruntime.RuntimeCallClass,
) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
	s.calls++
	s.extensionID = extensionID
	s.class = class
	if s.err != nil {
		return extensionsruntime.RuntimeInstanceSnapshot{}, nil, s.err
	}
	lease, err := s.gate.Acquire(ctx, class)
	if err != nil {
		return extensionsruntime.RuntimeInstanceSnapshot{}, nil, err
	}
	snapshot := s.snapshot
	snapshot.Admission = s.gate.Snapshot()
	return snapshot, lease, nil
}

func newProductionQueryAdmissionHarness(t *testing.T) (*productionQueryRuntimeAdmission, *productionQueryAdmissionStub) {
	t.Helper()
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: "query.plugin", InstanceID: "runtime-7"}
	gate, err := extensionsruntime.NewRuntimeAdmissionGate(identity)
	if err != nil {
		t.Fatal(err)
	}
	stub := &productionQueryAdmissionStub{
		gate: gate,
		snapshot: extensionsruntime.RuntimeInstanceSnapshot{
			Identity: identity, ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64), VersionID: 9, Active: true,
		},
	}
	return &productionQueryRuntimeAdmission{acquire: stub.acquire}, stub
}

func productionQueryCallerIdentity() *protocolv2.ExtensionIdentity {
	return &protocolv2.ExtensionIdentity{
		ExtensionId: "query.plugin", ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64),
		TrustGrantId: "grant-7", RuntimeEpoch: 7, InstanceId: "runtime-7",
	}
}

func TestProductionQueryCallerAdmissionFencesExactRuntimeAndReleasesLease(t *testing.T) {
	admission, stub := newProductionQueryAdmissionHarness(t)
	if err := admission.AuthorizeProtocolV2QueryCaller(context.Background(), productionQueryCallerIdentity()); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 || stub.extensionID != "query.plugin" || stub.class != extensionsruntime.RuntimeCallHost ||
		stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("caller admission stub=%#v gate=%#v", stub, stub.gate.Snapshot())
	}

	stale := productionQueryCallerIdentity()
	stale.ArtifactDigest = strings.Repeat("b", 64)
	if err := admission.AuthorizeProtocolV2QueryCaller(context.Background(), stale); !errors.Is(err, errProductionQueryRegistryRuntimeStale) {
		t.Fatalf("stale caller error = %v", err)
	}
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("stale caller leaked lease: %#v", stub.gate.Snapshot())
	}

	stub.gate.BeginDrain()
	if err := admission.AuthorizeProtocolV2QueryCaller(context.Background(), productionQueryCallerIdentity()); !errors.Is(err, extensionsruntime.ErrRuntimeAdmissionDraining) {
		t.Fatalf("draining caller error = %v", err)
	}
}

func TestProductionQueryExecutionAdmissionHoldsAndReleasesExactPluginLease(t *testing.T) {
	admission, stub := newProductionQueryAdmissionHarness(t)
	artifact := queryregistry.Artifact{
		ExtensionID: "query.plugin", ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 9, RuntimeInstanceID: "runtime-7",
	}
	release, err := admission.AcquireQueryExecution(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if release == nil || stub.class != extensionsruntime.RuntimeCallProvider || stub.gate.Snapshot().ActiveTotal != 1 {
		t.Fatalf("active execution stub=%#v gate=%#v", stub, stub.gate.Snapshot())
	}
	release()
	release()
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("execution lease was not idempotently released: %#v", stub.gate.Snapshot())
	}

	artifact.RuntimeInstanceID = "runtime-old"
	if _, err := admission.AcquireQueryExecution(context.Background(), artifact); !errors.Is(err, errProductionQueryRegistryRuntimeStale) {
		t.Fatalf("stale artifact error = %v", err)
	}
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("stale artifact leaked lease: %#v", stub.gate.Snapshot())
	}

	artifact.RuntimeInstanceID = "runtime-7"
	artifact.VersionID = 8
	if _, err := admission.AcquireQueryExecution(context.Background(), artifact); !errors.Is(err, errProductionQueryRegistryRuntimeStale) {
		t.Fatalf("wrong VersionID error = %v", err)
	}
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("wrong VersionID leaked lease: %#v", stub.gate.Snapshot())
	}

	cancelled, cancel := context.WithCancelCause(context.Background())
	callerCause := errors.New("caller cancelled exact admission")
	cancel(callerCause)
	artifact.VersionID = 9
	if _, err := admission.AcquireQueryExecution(cancelled, artifact); !errors.Is(err, context.Canceled) ||
		!errors.Is(err, callerCause) ||
		errors.Is(err, errProductionQueryRegistryRuntimeStale) {
		t.Fatalf("cancelled execution error = %v", err)
	}
}

func TestProductionQueryExecutionAdmissionPropagatesForceDrainContext(t *testing.T) {
	admission, stub := newProductionQueryAdmissionHarness(t)
	artifact := queryregistry.Artifact{
		ExtensionID: "query.plugin", ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 9, RuntimeInstanceID: "runtime-7",
	}
	lease, err := admission.AcquireQueryExecutionLease(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Context == nil || lease.Release == nil || stub.gate.Snapshot().ActiveTotal != 1 {
		t.Fatalf("contextual execution lease=%#v gate=%#v", lease, stub.gate.Snapshot())
	}
	forceCause := errors.New("force drain query runtime")
	forced := stub.gate.ForceCancel(forceCause)
	if !forced.Forced || forced.ActiveTotal != 1 {
		t.Fatalf("forced admission snapshot=%#v", forced)
	}
	select {
	case <-lease.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("ForceDrain did not cancel the Query execution lease")
	}
	if cause := context.Cause(lease.Context); !errors.Is(cause, extensionsruntime.ErrRuntimeAdmissionForced) ||
		!errors.Is(cause, forceCause) {
		t.Fatalf("ForceDrain execution cause=%v", cause)
	}
	lease.Release()
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("forced Query execution lease leaked: %#v", stub.gate.Snapshot())
	}
}

func TestProductionQueryExecutionAdmissionPrefersForcedAcquireOverLaterCallerCancel(t *testing.T) {
	ctx, cancelCaller := context.WithCancelCause(t.Context())
	forceCause := errors.New("runtime forced before acquire returned")
	callerCause := errors.New("later caller cancellation")
	admission := &productionQueryRuntimeAdmission{acquire: func(
		context.Context,
		string,
		extensionsruntime.RuntimeCallClass,
	) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error) {
		forced := errors.Join(extensionsruntime.ErrRuntimeAdmissionForced, forceCause)
		cancelCaller(callerCause)
		return extensionsruntime.RuntimeInstanceSnapshot{}, nil, forced
	}}
	artifact := queryregistry.Artifact{
		ExtensionID: "query.plugin", ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 9, RuntimeInstanceID: "runtime-7",
	}
	_, err := admission.AcquireQueryExecutionLease(ctx, artifact)
	if !errors.Is(err, errProductionQueryRegistryRuntimeStale) ||
		!errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) ||
		errors.Is(err, callerCause) {
		t.Fatalf("forced acquisition winner error=%v", err)
	}
}

func TestProductionQuerySnapshotMatchPreservesForceDrainCause(t *testing.T) {
	_, stub := newProductionQueryAdmissionHarness(t)
	snapshot, lease, err := stub.acquire(t.Context(), "query.plugin", extensionsruntime.RuntimeCallProvider)
	if err != nil {
		t.Fatal(err)
	}
	forceCause := errors.New("force drain during snapshot match")
	stub.gate.ForceCancel(forceCause)
	err = productionQueryRuntimeSnapshotMatches(
		t.Context(), lease, snapshot, "query.plugin", "1.2.3", strings.Repeat("a", 64), 9, "runtime-7",
	)
	if !errors.Is(err, errProductionQueryRegistryRuntimeStale) ||
		!errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) {
		t.Fatalf("snapshot ForceDrain error=%v", err)
	}
	lease.Release()
}

func TestProductionQuerySnapshotMatchPrefersForceDrainOverLaterCallerCancel(t *testing.T) {
	_, stub := newProductionQueryAdmissionHarness(t)
	ctx, cancelCaller := context.WithCancelCause(t.Context())
	snapshot, lease, err := stub.acquire(ctx, "query.plugin", extensionsruntime.RuntimeCallProvider)
	if err != nil {
		t.Fatal(err)
	}
	forceCause := errors.New("ForceDrain won before caller cancel")
	stub.gate.ForceCancel(forceCause)
	cancelCaller(errors.New("later caller cancellation"))
	err = productionQueryRuntimeSnapshotMatches(
		ctx, lease, snapshot, "query.plugin", "1.2.3", strings.Repeat("a", 64), 9, "runtime-7",
	)
	if !errors.Is(err, errProductionQueryRegistryRuntimeStale) ||
		!errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) {
		t.Fatalf("snapshot cancellation winner error=%v", err)
	}
	lease.Release()
}

func TestProductionQueryExecutionForceDrainCancelsProvider(t *testing.T) {
	admission, stub := newProductionQueryAdmissionHarness(t)
	artifact := queryregistry.Artifact{
		ExtensionID: "query.plugin", ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 9, RuntimeInstanceID: "runtime-7",
	}
	declaration := queryregistry.QueryDeclaration{
		ID: "query.plugin.items", ContractVersion: "query.plugin.items@1", Entity: "query.plugin.item",
		PlanVersion: "query.plugin.items.plan@1", Fields: []string{"id"}, Pagination: queryregistry.PaginationNone,
		ResultSchema: "query.plugin.item@1", PermissionPolicy: queryregistry.PermissionPolicyPublic,
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(func(queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
		return queryregistry.QueryCost{Units: 1, Maximum: 100}, nil
	}))).WithPluginAdmission(func(candidate queryregistry.Artifact) bool {
		return candidate == artifact
	})
	if _, err := registry.Publish(queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	providerStarted := make(chan struct{})
	provider := queryregistry.ExecutableProviderFunc(func(ctx context.Context, _ queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
		close(providerStarted)
		<-ctx.Done()
		return queryregistry.ProviderExecutionResult{}, ctx.Err()
	})
	providers, err := queryregistry.NewStaticProviderResolver([]queryregistry.ExecutableProviderBinding{{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema,
		Artifact: artifact, Provider: provider,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers,
		Schemas:   queryregistry.ResultSchemaValidatorFunc(func(context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow) error { return nil }),
		Admission: admission,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, executeErr := runtime.Execute(t.Context(), queryregistry.PlanRequest{QueryID: declaration.ID})
		done <- executeErr
	}()
	select {
	case <-providerStarted:
	case err := <-done:
		t.Fatalf("production Query execution stopped before provider: %v", err)
	case <-time.After(time.Second):
		t.Fatal("production Query provider did not start")
	}
	forceCause := errors.New("production Query ForceDrain")
	stub.gate.ForceCancel(forceCause)
	select {
	case err := <-done:
		if !errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, forceCause) {
			t.Fatalf("production Execute ForceDrain error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("production Execute did not stop after ForceDrain")
	}
	if stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("production Execute leaked admission: %#v", stub.gate.Snapshot())
	}
}

func TestProductionQueryExecutionWrongVersionIDCannotReachCacheHit(t *testing.T) {
	admission, stub := newProductionQueryAdmissionHarness(t)
	artifact := queryregistry.Artifact{
		ExtensionID: "query.plugin", ExtensionVersion: "1.2.3", PackageDigest: strings.Repeat("a", 64),
		VersionID: 8, RuntimeInstanceID: "runtime-7",
	}
	declaration := queryregistry.QueryDeclaration{
		ID: "query.plugin.cached", ContractVersion: "query.plugin.cached@1", Entity: "query.plugin.item",
		PlanVersion: "query.plugin.cached.plan@1", Fields: []string{"id"}, Pagination: queryregistry.PaginationOffset,
		ResultSchema: "query.plugin.item@1", PermissionPolicy: queryregistry.PermissionPolicyPublic,
		CacheTags: []string{"query.plugin.items"},
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(func(queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
		return queryregistry.QueryCost{Units: 1, Maximum: 100}, nil
	}))).WithPluginAdmission(func(candidate queryregistry.Artifact) bool { return candidate == artifact })
	if _, err := registry.Publish(queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	providers, err := queryregistry.NewStaticProviderResolver([]queryregistry.ExecutableProviderBinding{{
		QueryID: declaration.ID, ContractVersion: declaration.ContractVersion,
		PlanVersion: declaration.PlanVersion, ResultSchema: declaration.ResultSchema, Artifact: artifact,
		Provider: queryregistry.ExecutableProviderFunc(func(context.Context, queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
			providerCalls.Add(1)
			return queryregistry.ProviderExecutionResult{Rows: []queryregistry.QueryRow{{"id": "1"}}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	cache := &productionQueryCacheHitStub{}
	runtime, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Cache: cache, Admission: admission,
		Schemas: queryregistry.ResultSchemaValidatorFunc(func(context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: declaration.ID, Pagination: queryregistry.PaginationRequest{Limit: 10},
	})
	if !errors.Is(err, queryregistry.ErrArtifactUnavailable) || !errors.Is(err, errProductionQueryRegistryRuntimeStale) ||
		cache.loads.Load() != 0 || providerCalls.Load() != 0 || stub.gate.Snapshot().ActiveTotal != 0 {
		t.Fatalf("wrong VersionID cache execution err=%v loads=%d provider=%d gate=%#v",
			err, cache.loads.Load(), providerCalls.Load(), stub.gate.Snapshot())
	}
}

type productionQueryCacheHitStub struct {
	loads atomic.Int32
}

func (c *productionQueryCacheHitStub) LoadQueryResult(
	context.Context,
	string,
	[]string,
) (queryregistry.CachedQueryResult, queryregistry.QueryResultCacheFence, bool, error) {
	c.loads.Add(1)
	return queryregistry.CachedQueryResult{}, nil, true, nil
}

func (*productionQueryCacheHitStub) StoreQueryResult(
	context.Context,
	string,
	queryregistry.CachedQueryResult,
	[]string,
	queryregistry.QueryResultCacheFence,
) error {
	return nil
}

type productionQueryAuthorityResolverStub struct{}

func (productionQueryAuthorityResolverStub) ResolveProtocolV2QueryAuthority(
	context.Context,
	*protocolv2.ExtensionIdentity,
) (hostapi.ProtocolV2QueryAuthority, error) {
	return hostapi.ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
}

type productionQueryTraceSinkStub struct{}

func (productionQueryTraceSinkStub) RecordQueryTrace(hostapi.QueryTrace) {}

func TestBindProductionQueryRegistryUsesLifecycleSnapshotAndFreezesGateway(t *testing.T) {
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
	registry, catalog, err := hostapi.NewQueryRegistryCoreRegistry(hostapi.QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	actors := &productionQueryActorStoreStub{actor: identity.Actor{ID: 42, Status: identity.UserStatusActive}}
	runtimeManager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{})
	gateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = gateway.Close() })
	cache := &productionQueryCacheHitStub{}

	bound, err := bindProductionQueryRegistryWithCache(
		registry, catalog, stableRuntime, actors, runtimeManager, gateway, productionQueryTraceSinkStub{}, cache,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bound == nil || bound.Execution == nil || bound.Service == nil || bound.cache != cache ||
		!bound.Execution.BoundToRegistry(registry) {
		t.Fatalf("bound Query Registry = %#v", bound)
	}
	compatibilityGateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = compatibilityGateway.Close() })
	compatibility, err := bindProductionQueryRegistry(
		registry, catalog, stableRuntime, actors, runtimeManager, compatibilityGateway, productionQueryTraceSinkStub{},
	)
	if err != nil || compatibility == nil || compatibility.cache != nil || compatibility.Execution == nil {
		t.Fatalf("cacheless compatibility binding=%#v err=%v", compatibility, err)
	}
	if _, err := bindProductionQueryRegistryWithCache(
		registry, catalog, stableRuntime, actors, runtimeManager, gateway, productionQueryTraceSinkStub{}, nil,
	); err == nil {
		t.Fatal("Gateway accepted a second production Query Registry binding")
	}
}
