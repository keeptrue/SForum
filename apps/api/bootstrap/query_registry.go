package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var errProductionQueryRegistryRuntimeStale = errors.New("bootstrap: Query Registry exact runtime is stale")

type productionQueryRegistry struct {
	Execution *queryregistry.ExecutionRuntime
	Service   *hostapi.ProtocolV2QueryRegistryService
	cache     queryregistry.QueryResultCache
}

// productionQueryActorAuthority never trusts a delegation's cached actor
// projection. Every release fence reloads the Host actor and its effective
// permissions from the authoritative Identity store.
type productionQueryActorAuthority struct {
	store identity.ActorStore
}

func (a *productionQueryActorAuthority) ResolveProtocolV2QueryActor(
	ctx context.Context,
	actorUserID int64,
) (hostapi.ProtocolV2QueryActorProjection, error) {
	if actorUserID == 0 {
		return productionAnonymousQueryActorProjection(), nil
	}
	actor, err := a.loadActiveActor(ctx, actorUserID)
	if err != nil {
		return hostapi.ProtocolV2QueryActorProjection{}, err
	}
	return productionQueryActorProjection(actor)
}

func (a *productionQueryActorAuthority) AuthorizeProtocolV2QueryActor(
	ctx context.Context,
	actorUserID int64,
	claim queryregistry.PermissionClaim,
) (hostapi.ProtocolV2QueryActorProjection, error) {
	if actorUserID == 0 {
		if claim.PermissionPolicy != queryregistry.PermissionPolicyPublic &&
			!hostapi.IsShortcodeProjectionActorPolicy(claim.PermissionPolicy) {
			return hostapi.ProtocolV2QueryActorProjection{}, hostapi.ErrProtocolV2QueryActorDenied
		}
		return productionAnonymousQueryActorProjection(), nil
	}
	actor, err := a.loadActiveActor(ctx, actorUserID)
	if err != nil {
		return hostapi.ProtocolV2QueryActorProjection{}, err
	}
	switch claim.PermissionPolicy {
	case queryregistry.PermissionPolicyPublic, queryregistry.PermissionPolicyLogin:
		// Delegated Query calls are authenticated. Reloading an active actor is
		// the live Host authority check for the two built-in policies.
	default:
		if !hostapi.IsShortcodeProjectionActorPolicy(claim.PermissionPolicy) && !actor.Can(claim.PermissionPolicy) {
			return hostapi.ProtocolV2QueryActorProjection{}, hostapi.ErrProtocolV2QueryActorDenied
		}
	}
	return productionQueryActorProjection(actor)
}

func productionAnonymousQueryActorProjection() hostapi.ProtocolV2QueryActorProjection {
	return hostapi.ProtocolV2QueryActorProjection{
		ActorUserID: 0, Authenticated: false,
		ActorFingerprint: "anonymous", PolicyFingerprint: "public",
	}
}

func (a *productionQueryActorAuthority) loadActiveActor(ctx context.Context, actorUserID int64) (identity.Actor, error) {
	if a == nil || a.store == nil || ctx == nil || actorUserID <= 0 {
		return identity.Actor{}, hostapi.ErrProtocolV2QueryActorDenied
	}
	if err := ctx.Err(); err != nil {
		return identity.Actor{}, err
	}
	actor, err := a.store.LoadActor(ctx, actorUserID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return identity.Actor{}, ctxErr
	}
	if err != nil || actor.ID != actorUserID || !actor.IsActive() {
		return identity.Actor{}, errors.Join(hostapi.ErrProtocolV2QueryActorDenied, err)
	}
	return actor, nil
}

func productionQueryActorProjection(actor identity.Actor) (hostapi.ProtocolV2QueryActorProjection, error) {
	createdAt := ""
	if !actor.CreatedAt.IsZero() {
		createdAt = actor.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	actorFingerprint, err := productionQueryFingerprint("actor", struct {
		ID        int64               `json:"id"`
		Status    identity.UserStatus `json:"status"`
		CreatedAt string              `json:"createdAt,omitempty"`
	}{ID: actor.ID, Status: actor.Status, CreatedAt: createdAt})
	if err != nil {
		return hostapi.ProtocolV2QueryActorProjection{}, hostapi.ErrProtocolV2QueryActorDenied
	}
	permissions := make([]string, 0, len(actor.Permissions))
	for permission, allowed := range actor.Permissions {
		if allowed {
			permissions = append(permissions, permission)
		}
	}
	policyFingerprint, err := productionQueryFingerprint("policy", struct {
		Status      identity.UserStatus `json:"status"`
		Roles       []string            `json:"roles"`
		Permissions []string            `json:"permissions"`
	}{
		Status: actor.Status, Roles: productionQuerySortedUnique(actor.RoleKeys),
		Permissions: productionQuerySortedUnique(permissions),
	})
	if err != nil {
		return hostapi.ProtocolV2QueryActorProjection{}, hostapi.ErrProtocolV2QueryActorDenied
	}
	return hostapi.ProtocolV2QueryActorProjection{
		ActorUserID: actor.ID, Authenticated: true,
		ActorFingerprint: actorFingerprint, PolicyFingerprint: policyFingerprint,
	}, nil
}

func productionQueryFingerprint(namespace string, value any) (string, error) {
	document, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	return namespace + ":" + hex.EncodeToString(digest[:]), nil
}

func productionQuerySortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) < 2 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

type productionQueryRuntimeAcquirer func(
	context.Context,
	string,
	extensionsruntime.RuntimeCallClass,
) (extensionsruntime.RuntimeInstanceSnapshot, *extensionsruntime.RuntimeAdmissionLease, error)

// productionQueryRuntimeAdmission provides both the caller fence and the
// executable artifact fence from one exact-runtime linearization point.
type productionQueryRuntimeAdmission struct {
	acquire productionQueryRuntimeAcquirer
}

func newProductionQueryRuntimeAdmission(runtime *extensionsruntime.Manager) *productionQueryRuntimeAdmission {
	if runtime == nil {
		return nil
	}
	return &productionQueryRuntimeAdmission{acquire: runtime.AcquireActiveRuntimeCall}
}

func (a *productionQueryRuntimeAdmission) AuthorizeProtocolV2QueryCaller(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
) error {
	if a == nil || a.acquire == nil || ctx == nil || !productionQueryCallerIdentityValid(identity) {
		return errProductionQueryRegistryRuntimeStale
	}
	snapshot, lease, err := a.acquire(ctx, identity.GetExtensionId(), extensionsruntime.RuntimeCallHost)
	if err != nil {
		if productionQueryErrorMatchesContext(ctx, err) {
			return productionQueryContextError(ctx)
		}
		return errors.Join(errProductionQueryRegistryRuntimeStale, err)
	}
	if lease == nil {
		return errProductionQueryRegistryRuntimeStale
	}
	defer lease.Release()
	if err := productionQueryRuntimeSnapshotMatches(
		ctx, lease, snapshot, identity.GetExtensionId(), identity.GetExtensionVersion(),
		identity.GetArtifactDigest(), 0, identity.GetInstanceId(),
	); err != nil {
		return err
	}
	return nil
}

func (a *productionQueryRuntimeAdmission) AcquireQueryExecution(
	ctx context.Context,
	artifact queryregistry.Artifact,
) (func(), error) {
	lease, err := a.AcquireQueryExecutionLease(ctx, artifact)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

func (a *productionQueryRuntimeAdmission) AcquireQueryExecutionLease(
	ctx context.Context,
	artifact queryregistry.Artifact,
) (queryregistry.ExecutionAdmissionLease, error) {
	// ExecutionRuntime itself recognizes the unforgeable Core seal and bypasses
	// this adapter. Never trust the public Core boolean at this outer boundary.
	if a == nil || a.acquire == nil || ctx == nil || artifact.Core ||
		strings.TrimSpace(artifact.ExtensionID) == "" || strings.TrimSpace(artifact.ExtensionVersion) == "" ||
		strings.TrimSpace(artifact.PackageDigest) == "" || artifact.VersionID <= 0 ||
		strings.TrimSpace(artifact.RuntimeInstanceID) == "" {
		return queryregistry.ExecutionAdmissionLease{}, errProductionQueryRegistryRuntimeStale
	}
	snapshot, lease, err := a.acquire(ctx, artifact.ExtensionID, extensionsruntime.RuntimeCallProvider)
	if err != nil {
		if productionQueryErrorMatchesContext(ctx, err) {
			return queryregistry.ExecutionAdmissionLease{}, productionQueryContextError(ctx)
		}
		return queryregistry.ExecutionAdmissionLease{}, errors.Join(errProductionQueryRegistryRuntimeStale, err)
	}
	if lease == nil {
		return queryregistry.ExecutionAdmissionLease{}, errProductionQueryRegistryRuntimeStale
	}
	if err := productionQueryRuntimeSnapshotMatches(
		ctx, lease, snapshot, artifact.ExtensionID, artifact.ExtensionVersion,
		artifact.PackageDigest, artifact.VersionID, artifact.RuntimeInstanceID,
	); err != nil {
		lease.Release()
		return queryregistry.ExecutionAdmissionLease{}, err
	}
	return queryregistry.ExecutionAdmissionLease{Context: lease.Context, Release: lease.Release}, nil
}

func productionQueryCallerIdentityValid(identity *protocolv2.ExtensionIdentity) bool {
	if identity == nil || identity.GetRuntimeEpoch() == 0 || strings.TrimSpace(identity.GetTrustGrantId()) == "" {
		return false
	}
	for _, value := range []string{
		identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest(), identity.GetInstanceId(),
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}

func productionQueryRuntimeSnapshotMatches(
	ctx context.Context,
	lease *extensionsruntime.RuntimeAdmissionLease,
	snapshot extensionsruntime.RuntimeInstanceSnapshot,
	extensionID string,
	extensionVersion string,
	artifactDigest string,
	versionID int64,
	instanceID string,
) error {
	if ctx == nil || lease == nil || lease.Context == nil {
		return errProductionQueryRegistryRuntimeStale
	}
	parentCause := context.Cause(ctx)
	leaseCause := context.Cause(lease.Context)
	if leaseCause != nil && (parentCause == nil ||
		!errors.Is(leaseCause, parentCause) || !errors.Is(parentCause, leaseCause)) {
		return errors.Join(errProductionQueryRegistryRuntimeStale, leaseCause)
	}
	if parentCause != nil {
		return productionQueryContextError(ctx)
	}
	if leaseCause != nil {
		return errors.Join(errProductionQueryRegistryRuntimeStale, leaseCause)
	}
	if !snapshot.Active || snapshot.Admission.Draining || snapshot.Admission.Forced ||
		snapshot.Identity.ExtensionID != extensionID || snapshot.Identity.InstanceID != instanceID ||
		snapshot.ExtensionVersion != extensionVersion || snapshot.ArtifactDigest != artifactDigest ||
		(versionID > 0 && snapshot.VersionID != versionID) {
		return errProductionQueryRegistryRuntimeStale
	}
	return nil
}

func productionQueryErrorMatchesContext(ctx context.Context, err error) bool {
	if ctx == nil || err == nil {
		return false
	}
	cause := context.Cause(ctx)
	if cause == nil {
		return false
	}
	if errors.Is(err, cause) && errors.Is(cause, err) {
		return true
	}
	return ctx.Err() != nil && errors.Is(err, cause) && errors.Is(err, ctx.Err())
}

func productionQueryContextError(ctx context.Context) error {
	if ctx == nil {
		return errProductionQueryRegistryRuntimeStale
	}
	cause := context.Cause(ctx)
	state := ctx.Err()
	if cause == nil {
		return state
	}
	if state != nil && !errors.Is(cause, state) {
		return errors.Join(state, cause)
	}
	return cause
}

// productionQueryRegistryOptions 收集生产 outlet 的可选宿主策略。零值保持
// 「consent-gated 查询 fail closed」的默认语义。
type productionQueryRegistryOptions struct {
	consentGate *hostapi.ProtocolV2QueryConsentGate
}

// productionQueryRegistryOption mutates the production outlet policy.
type productionQueryRegistryOption func(*productionQueryRegistryOptions)

// withProductionQueryRegistryConsentGate installs the capability + rate-limit +
// audit gate used by ConsentGated definitions.
func withProductionQueryRegistryConsentGate(
	gate *hostapi.ProtocolV2QueryConsentGate,
) productionQueryRegistryOption {
	return func(options *productionQueryRegistryOptions) { options.consentGate = gate }
}

func bindProductionQueryRegistry(
	registry *queryregistry.Registry,
	catalog *hostapi.QueryRegistryCoreCatalog,
	stableRuntime hostapi.ProtocolV2QueryRuntime,
	actors identity.ActorStore,
	runtime *extensionsruntime.Manager,
	gateway *hostapi.Gateway,
	trace hostapi.QueryTraceSink,
	options ...productionQueryRegistryOption,
) (*productionQueryRegistry, error) {
	return bindProductionQueryRegistryWithCache(
		registry, catalog, stableRuntime, actors, runtime, gateway, trace, nil, options...,
	)
}

func bindProductionQueryRegistryWithCache(
	registry *queryregistry.Registry,
	catalog *hostapi.QueryRegistryCoreCatalog,
	stableRuntime hostapi.ProtocolV2QueryRuntime,
	actors identity.ActorStore,
	runtime *extensionsruntime.Manager,
	gateway *hostapi.Gateway,
	trace hostapi.QueryTraceSink,
	cache queryregistry.QueryResultCache,
	options ...productionQueryRegistryOption,
) (*productionQueryRegistry, error) {
	if registry == nil || catalog == nil || stableRuntime == nil || actors == nil || runtime == nil || gateway == nil || trace == nil {
		return nil, fmt.Errorf("bootstrap: production Query Registry dependency unavailable")
	}
	configured := productionQueryRegistryOptions{}
	for _, option := range options {
		if option != nil {
			option(&configured)
		}
	}
	coreProviders, err := hostapi.NewProtocolV2QueryRegistryProviderResolver(stableRuntime, catalog.Bindings())
	if err != nil {
		return nil, fmt.Errorf("create Query Registry provider resolver: %w", err)
	}
	// Core HostAPI bindings remain primary; third-party Handler 声明走 Protocol V2
	// query.runtime@1，在调用时按 exact active runtime 解析，不捕获启动瞬间。
	providers, err := extensionsruntime.NewCompositeQueryProviderResolver(coreProviders, runtime, registry)
	if err != nil {
		return nil, fmt.Errorf("create composite Query Registry provider resolver: %w", err)
	}
	filterSource, err := extensionsruntime.NewProtocolV2QueryResultFilterSource(runtime, registry)
	if err != nil {
		return nil, fmt.Errorf("create Query Registry result filter source: %w", err)
	}
	coreSchemas, err := queryregistry.NewJSONResultSchemaCatalog(catalog.Schemas())
	if err != nil {
		return nil, fmt.Errorf("create Query Registry result schema catalog: %w", err)
	}
	// Core 走 Host 目录；第三方 Schema 来自 lifecycle 发布到同一 Registry revision。
	schemas, err := queryregistry.NewCompositeResultSchemaValidator(registry, coreSchemas)
	if err != nil {
		return nil, fmt.Errorf("create composite Query Registry result schema validator: %w", err)
	}
	admission := newProductionQueryRuntimeAdmission(runtime)
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Admission: admission, Schemas: schemas,
		ResultFilterSource: filterSource, Trace: hostapi.NewQueryRegistryTraceAdapter(trace), Cache: cache,
	})
	if err != nil {
		return nil, fmt.Errorf("create Query Registry execution runtime: %w", err)
	}
	serviceOptions := make([]hostapi.ProtocolV2QueryRegistryOption, 0, 1)
	if configured.consentGate != nil {
		serviceOptions = append(serviceOptions, hostapi.WithProtocolV2QueryConsentGate(configured.consentGate))
	}
	service, err := hostapi.NewProtocolV2QueryRegistryService(
		registry, execution, &productionQueryActorAuthority{store: actors}, admission, serviceOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("create Query Registry Protocol V2 service: %w", err)
	}
	if err := gateway.BindProtocolV2QueryRegistryService(service); err != nil {
		return nil, fmt.Errorf("bind Query Registry Protocol V2 service: %w", err)
	}
	return &productionQueryRegistry{Execution: execution, Service: service, cache: cache}, nil
}
