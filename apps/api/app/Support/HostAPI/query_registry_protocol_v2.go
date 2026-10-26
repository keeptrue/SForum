package hostapi

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const (
	ProtocolV2QueryFilterValueSchemaID = "sforum.query.filter.value"
	ProtocolV2QueryFilterValueSchemaV1 = "1"
)

// ProtocolV2QueryActorDelegationRequest is created only by a Host-authenticated
// route/admin adapter. The Host identity authority derives every fingerprint;
// callers select only the actor, exact runtime, and intended query outlet.
type ProtocolV2QueryActorDelegationRequest struct {
	ActorUserID int64
	Runtime     *protocolv2.ExtensionIdentity
	QueryID     string
	Locale      string
	Scope       string
	MaxCost     int
}

// ProtocolV2QueryActorDelegationGrant is copied into RequestContext only for
// the exact plugin invocation that may consume it.
type ProtocolV2QueryActorDelegationGrant struct {
	QueryID             string
	ContractVersion     string
	PlanVersion         string
	ResultSchemaID      string
	ResultSchemaVersion string
	Scope               string
	Token               string
}

type ProtocolV2QueryActorDelegationIssuer interface {
	IssueProtocolV2QueryActorDelegation(context.Context, ProtocolV2QueryActorDelegationRequest) (ProtocolV2QueryActorDelegationGrant, error)
}

// ProtocolV2QueryRegistryService is the explicit Protocol V2 outlet for the
// bounded Query Registry executor. It is immutable after construction.
type ProtocolV2QueryRegistryService struct {
	registry        *queryregistry.Registry
	execution       *queryregistry.ExecutionRuntime
	actors          ProtocolV2QueryActorAuthority
	callerAdmission ProtocolV2QueryCallerAdmission
	delegations     *ProtocolV2QueryDelegationAuthority
	// consent is the Host-owned policy for ConsentGated definitions. Nil keeps
	// consent-gated queries fail closed instead of accidentally readable.
	consent *ProtocolV2QueryConsentGate
}

// ProtocolV2QueryRegistryOptions collects optional Host-owned policy for the
// Query Registry Protocol V2 outlet.
type ProtocolV2QueryRegistryOptions struct {
	ConsentGate *ProtocolV2QueryConsentGate
}

// ProtocolV2QueryRegistryOption mutates the outlet policy at construction.
type ProtocolV2QueryRegistryOption interface {
	applyProtocolV2QueryRegistry(*ProtocolV2QueryRegistryOptions)
}

type protocolV2QueryRegistryOptionFunc func(*ProtocolV2QueryRegistryOptions)

func (f protocolV2QueryRegistryOptionFunc) applyProtocolV2QueryRegistry(options *ProtocolV2QueryRegistryOptions) {
	f(options)
}

// WithProtocolV2QueryConsentGate installs the capability + rate-limit + audit
// gate used by ConsentGated definitions (for example the self-email read).
func WithProtocolV2QueryConsentGate(gate *ProtocolV2QueryConsentGate) ProtocolV2QueryRegistryOption {
	return protocolV2QueryRegistryOptionFunc(func(options *ProtocolV2QueryRegistryOptions) {
		options.ConsentGate = gate
	})
}

func NewProtocolV2QueryRegistryService(
	registry *queryregistry.Registry,
	execution *queryregistry.ExecutionRuntime,
	actors ProtocolV2QueryActorAuthority,
	callerAdmission ProtocolV2QueryCallerAdmission,
	options ...ProtocolV2QueryRegistryOption,
) (*ProtocolV2QueryRegistryService, error) {
	delegations, err := NewProtocolV2QueryDelegationAuthority()
	if err != nil {
		return nil, err
	}
	return newProtocolV2QueryRegistryService(registry, execution, actors, callerAdmission, delegations, options...)
}

func newProtocolV2QueryRegistryService(
	registry *queryregistry.Registry,
	execution *queryregistry.ExecutionRuntime,
	actors ProtocolV2QueryActorAuthority,
	callerAdmission ProtocolV2QueryCallerAdmission,
	delegations *ProtocolV2QueryDelegationAuthority,
	options ...ProtocolV2QueryRegistryOption,
) (*ProtocolV2QueryRegistryService, error) {
	if registry == nil || execution == nil || !execution.BoundToRegistry(registry) ||
		actors == nil || callerAdmission == nil || delegations == nil {
		return nil, errors.New("hostapi: Query Registry Protocol V2 service requires one bound execution and Host authority")
	}
	for _, query := range registry.Snapshot().Queries {
		if protocolV2QueryRegistryReservedID(query.ID) {
			return nil, errors.New("hostapi: Query Registry Protocol V2 service conflicts with a reserved Host query id")
		}
	}
	configured := ProtocolV2QueryRegistryOptions{}
	for _, option := range options {
		if option != nil {
			option.applyProtocolV2QueryRegistry(&configured)
		}
	}
	return &ProtocolV2QueryRegistryService{
		registry: registry, execution: execution, actors: actors,
		callerAdmission: callerAdmission, delegations: delegations, consent: configured.ConsentGate,
	}, nil
}

func (s *ProtocolV2QueryRegistryService) IssueProtocolV2QueryActorDelegation(
	ctx context.Context,
	request ProtocolV2QueryActorDelegationRequest,
) (ProtocolV2QueryActorDelegationGrant, error) {
	if s == nil || s.registry == nil || s.execution == nil || s.actors == nil ||
		s.callerAdmission == nil || s.delegations == nil || ctx == nil || request.ActorUserID < 0 {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryDelegationInvalid
	}
	if err := ctx.Err(); err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, err
	}
	runtime := cloneProtocolV2ExtensionIdentity(request.Runtime)
	if !validProtocolV2QueryRuntimeBinding(runtime) {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryDelegationInvalid
	}
	if err := s.callerAdmission.AuthorizeProtocolV2QueryCaller(ctx, runtime); err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, errors.Join(ErrProtocolV2QueryCallerStale, err)
	}
	registryState := s.registry.CacheState()
	query, err := s.registry.Resolve(request.QueryID)
	if err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, err
	}
	if protocolV2QueryRegistryReservedID(query.ID) {
		return ProtocolV2QueryActorDelegationGrant{}, queryregistry.ErrConflict
	}
	// consent-gated definitions: never mint authority for an anonymous caller or
	// for an exact artifact that does not declare the required capability.
	if IsProtocolV2ConsentGatedQuery(query.ID) {
		if request.ActorUserID <= 0 {
			return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryActorDenied
		}
		if err := s.consent.AuthorizeCapability(ctx, runtime); err != nil {
			return ProtocolV2QueryActorDelegationGrant{}, err
		}
	}
	projection, err := s.actors.ResolveProtocolV2QueryActor(ctx, request.ActorUserID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ProtocolV2QueryActorDelegationGrant{}, ctxErr
	}
	if err == nil {
		projection, err = normalizeProtocolV2QueryActorProjection(projection)
	}
	if err != nil || projection.ActorUserID != request.ActorUserID {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryActorDenied
	}
	binding, err := normalizeProtocolV2QueryDelegationBinding(protocolV2QueryDelegationBinding{
		Actor: projection, Runtime: runtime, Query: query, Registry: registryState,
		Locale: request.Locale, Scope: request.Scope, MaxCost: request.MaxCost,
	})
	if err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, err
	}
	schemaID, schemaVersion, ok := splitProtocolV2QuerySchemaRef(query.ResultSchema)
	if !ok {
		return ProtocolV2QueryActorDelegationGrant{}, ErrProtocolV2QueryDelegationInvalid
	}
	// Actor resolution and registry lookup may block. Do not mint fresh
	// authority for a caller that was disabled or quarantined meanwhile.
	if err := s.callerAdmission.AuthorizeProtocolV2QueryCaller(ctx, runtime); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ProtocolV2QueryActorDelegationGrant{}, ctxErr
		}
		return ProtocolV2QueryActorDelegationGrant{}, errors.Join(ErrProtocolV2QueryCallerStale, err)
	}
	if s.registry.CacheState() != binding.Registry {
		return ProtocolV2QueryActorDelegationGrant{}, queryregistry.ErrRevisionConflict
	}
	token, err := s.delegations.issue(ctx, binding)
	if err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProtocolV2QueryActorDelegationGrant{}, err
	}
	if s.registry.CacheState() != binding.Registry {
		return ProtocolV2QueryActorDelegationGrant{}, queryregistry.ErrRevisionConflict
	}
	return ProtocolV2QueryActorDelegationGrant{
		QueryID: query.ID, ContractVersion: query.ContractVersion, PlanVersion: query.PlanVersion,
		ResultSchemaID: schemaID, ResultSchemaVersion: schemaVersion,
		Scope: binding.Scope, Token: token,
	}, nil
}

func (s *ProtocolV2QueryRegistryService) execute(
	ctx context.Context,
	request *hostv2.QueryRequest,
) *hostv2.QueryResponse {
	response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
	if s == nil || s.registry == nil || s.execution == nil || s.actors == nil ||
		s.callerAdmission == nil || s.delegations == nil {
		response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_registry_unavailable", "The Query Registry outlet is not configured.", true)
		return response
	}
	if ctx == nil {
		response.Error = queryRegistryProtocolV2Error(ErrProtocolV2QueryDelegationInvalid)
		return response
	}
	runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
	if err := validateProtocolV2QueryRegistryEnvelope(request, runtime); err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	if err := s.callerAdmission.AuthorizeProtocolV2QueryCaller(ctx, runtime); err != nil {
		response.Error = queryRegistryProtocolV2Error(errors.Join(ErrProtocolV2QueryCallerStale, err))
		return response
	}
	registryState := s.registry.CacheState()
	query, err := s.registry.Resolve(request.GetQueryId())
	if err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	if err := validateProtocolV2QueryRegistryContract(request, query); err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	if s.registry.CacheState() != registryState {
		response.Error = queryRegistryProtocolV2Error(queryregistry.ErrRevisionConflict)
		return response
	}
	planRequest, err := protocolV2QueryRegistryPlanRequest(request)
	if err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	stored, err := s.delegations.lookup(request.GetActorDelegation())
	if err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	binding := protocolV2QueryDelegationBinding{
		Actor:   stored.Binding.Actor,
		Runtime: runtime, Query: query, Registry: registryState,
		Locale: request.GetContext().GetLocale(), Scope: request.GetScope(), MaxCost: stored.Binding.MaxCost,
	}
	delegation, err := s.delegations.verify(request.GetActorDelegation(), binding)
	if err != nil {
		response.Error = queryRegistryProtocolV2Error(err)
		return response
	}
	// consent-gated definitions re-check the exact-artifact capability and the
	// per-(actor, extension) rate limit before reading anything. The delegation
	// is not consumed on this path, so a denied call cannot burn the token.
	consentGated := IsProtocolV2ConsentGatedQuery(query.ID)
	if consentGated {
		if err := s.consent.Authorize(
			ctx, runtime, delegation.Binding.Actor.ActorUserID, query.ID,
		); err != nil {
			response.Error = queryRegistryProtocolV2Error(err)
			return response
		}
	}
	permissionRecheck := &protocolV2QueryPermissionRecheck{service: s, delegation: delegation}
	planRequest.Permission = queryregistry.PermissionInput{
		Authenticated:     delegation.Binding.Actor.Authenticated,
		ActorFingerprint:  delegation.Binding.Actor.ActorFingerprint,
		PolicyFingerprint: delegation.Binding.Actor.PolicyFingerprint,
		Recheck:           permissionRecheck,
	}
	planRequest.Locale = delegation.Binding.Locale
	planRequest.Scope = delegation.Binding.Scope
	planRequest.MaxCost = delegation.Binding.MaxCost

	executionContext := contextWithShortcodeProjectionActor(ctx, delegation.Binding.Actor.ActorUserID)
	result, err := s.execution.Execute(executionContext, planRequest)
	if err != nil {
		response.Error = queryRegistryProtocolV2Error(permissionRecheck.classify(err))
		return response
	}
	if consentGated {
		// 每次成功读取一条审计（不含任何字段值）。审计不可用时 fail closed：
		// 宿主不返回无法追溯的同意数据。
		event := ProtocolV2ConsentQueryEvent{
			ActorUserID: delegation.Binding.Actor.ActorUserID,
			ExtensionID: runtime.GetExtensionId(), ExtensionVersion: runtime.GetExtensionVersion(),
			ArtifactDigest: runtime.GetArtifactDigest(),
			QueryID:        query.ID, PlanVersion: query.PlanVersion,
			RequestID: request.GetContext().GetRequestId(), Rows: len(result.Rows),
		}
		if err := s.consent.Record(executionContext, event); err != nil {
			response.Error = queryRegistryProtocolV2Error(err)
			return response
		}
	}
	response.Rows = make([]*protocolv2.TypedDocument, 0, len(result.Rows))
	for _, row := range result.Rows {
		document, encodeErr := protocolV2Document(
			request.GetResultSchemaId(), request.GetResultSchemaVersion(), map[string]any(row),
		)
		if encodeErr != nil {
			response.Rows = nil
			response.Error = queryRegistryProtocolV2Error(queryregistry.ErrResultInvalid)
			return response
		}
		response.Rows = append(response.Rows, document)
	}
	response.Page.HasMore = result.Page.HasMore
	response.Page.NextCursor = result.Page.NextCursor
	if result.Page.NextOffset > 0 {
		response.NextOffset = uint64(result.Page.NextOffset)
	}
	return response
}

type protocolV2QueryPermissionRecheck struct {
	service    *ProtocolV2QueryRegistryService
	delegation protocolV2VerifiedQueryDelegation

	mu       sync.Mutex
	consumed bool
	lastErr  error
}

func (r *protocolV2QueryPermissionRecheck) AuthorizeQuery(
	ctx context.Context,
	claim queryregistry.PermissionClaim,
) error {
	if r == nil || r.service == nil || ctx == nil {
		return queryregistry.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return r.deny(err)
	}
	binding := r.delegation.Binding
	if r.service.registry.CacheState() != binding.Registry {
		return r.deny(queryregistry.ErrRevisionConflict)
	}
	if claim.QueryID != binding.Query.ID || claim.ContractVersion != binding.Query.ContractVersion ||
		claim.PlanVersion != binding.Query.PlanVersion || claim.ResultSchema != binding.Query.ResultSchema ||
		claim.PermissionPolicy != binding.Query.PermissionPolicy || claim.Artifact != binding.Query.Artifact ||
		claim.Locale != binding.Locale || claim.Scope != binding.Scope {
		return r.deny(ErrProtocolV2QueryDelegationInvalid)
	}
	if err := r.service.callerAdmission.AuthorizeProtocolV2QueryCaller(ctx, binding.Runtime); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return r.deny(ctxErr)
		}
		return r.deny(errors.Join(ErrProtocolV2QueryCallerStale, err))
	}
	projection, err := r.service.actors.AuthorizeProtocolV2QueryActor(ctx, binding.Actor.ActorUserID, claim)
	if err == nil {
		projection, err = normalizeProtocolV2QueryActorProjection(projection)
	}
	if err != nil || projection != binding.Actor {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return r.deny(ctxErr)
		}
		return r.deny(ErrProtocolV2QueryActorDenied)
	}
	// Actor policy resolution may perform I/O. Recheck the exact caller after it
	// returns so a concurrent disable cannot open the provider invocation fence.
	if err := r.service.callerAdmission.AuthorizeProtocolV2QueryCaller(ctx, binding.Runtime); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return r.deny(ctxErr)
		}
		return r.deny(errors.Join(ErrProtocolV2QueryCallerStale, err))
	}
	if r.service.registry.CacheState() != binding.Registry {
		return r.deny(queryregistry.ErrRevisionConflict)
	}
	if err := ctx.Err(); err != nil {
		return r.deny(err)
	}
	if err := r.consumeOnce(); err != nil {
		return r.deny(err)
	}
	return nil
}

func (r *protocolV2QueryPermissionRecheck) consumeOnce() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed {
		return nil
	}
	if err := r.service.delegations.consume(r.delegation); err != nil {
		return err
	}
	r.consumed = true
	return nil
}

func (r *protocolV2QueryPermissionRecheck) deny(err error) error {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
	return queryregistry.ErrDenied
}

func (r *protocolV2QueryPermissionRecheck) classify(err error) error {
	if !errors.Is(err, queryregistry.ErrDenied) {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastErr != nil {
		return r.lastErr
	}
	return err
}

func validateProtocolV2QueryRegistryEnvelope(
	request *hostv2.QueryRequest,
	runtime *protocolv2.ExtensionIdentity,
) error {
	if request == nil || request.GetContext() == nil || !validProtocolV2QueryRuntimeBinding(runtime) ||
		request.GetContext().GetActor() != nil || !proto.Equal(request.GetContext().GetExtension(), runtime) ||
		len(request.GetContext().GetHostCommandDelegations()) != 0 ||
		len(request.GetContext().GetHostQueryDelegations()) != 0 || strings.TrimSpace(request.GetActorDelegation()) == "" {
		return ErrProtocolV2QueryDelegationInvalid
	}
	return nil
}

func validateProtocolV2QueryRegistryContract(
	request *hostv2.QueryRequest,
	query queryregistry.QueryContribution,
) error {
	schemaID, schemaVersion, ok := splitProtocolV2QuerySchemaRef(query.ResultSchema)
	if !ok || strings.TrimSpace(request.GetQueryId()) != query.ID ||
		strings.TrimSpace(request.GetContractVersion()) != query.ContractVersion ||
		strings.TrimSpace(request.GetPlanVersion()) != query.PlanVersion ||
		strings.TrimSpace(request.GetResultSchemaId()) != schemaID ||
		strings.TrimSpace(request.GetResultSchemaVersion()) != schemaVersion {
		return ErrProtocolV2QueryDelegationInvalid
	}
	return nil
}

func protocolV2QueryRegistryReservedID(queryID string) bool {
	return queryID == QueryOwnSettingsID || queryID == QueryExtensionInventoryID || isStableProtocolV2QueryID(queryID)
}

func protocolV2QueryRegistryPlanRequest(request *hostv2.QueryRequest) (queryregistry.PlanRequest, error) {
	if request == nil {
		return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
	}
	filters := make([]queryregistry.FilterValue, 0, len(request.GetFilters()))
	for _, filter := range request.GetFilters() {
		if filter == nil || strings.TrimSpace(filter.GetOperator()) != "eq" {
			return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
		}
		document := filter.GetValue()
		if document == nil || document.GetSchemaId() != ProtocolV2QueryFilterValueSchemaID ||
			document.GetSchemaVersion() != ProtocolV2QueryFilterValueSchemaV1 || document.GetValue() == nil {
			return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
		}
		values := document.GetValue().AsMap()
		value, ok := values["value"].(string)
		if !ok || len(values) != 1 {
			return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
		}
		filters = append(filters, queryregistry.FilterValue{Field: filter.GetField(), Value: value})
	}
	sorts := make([]queryregistry.SortValue, 0, len(request.GetSorts()))
	for _, sortValue := range request.GetSorts() {
		if sortValue == nil {
			return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
		}
		sorts = append(sorts, queryregistry.SortValue{Field: sortValue.GetField(), Descending: sortValue.GetDescending()})
	}
	page := request.GetPage()
	if request.GetOffset() > math.MaxInt {
		return queryregistry.PlanRequest{}, queryregistry.ErrInvalid
	}
	return queryregistry.PlanRequest{
		QueryID: request.GetQueryId(), PlanVersion: request.GetPlanVersion(),
		ResultSchema: request.GetResultSchemaId() + "@" + request.GetResultSchemaVersion(),
		Fields:       append([]string(nil), request.GetFields()...), Relations: append([]string(nil), request.GetRelations()...),
		Filters: filters, Sorts: sorts,
		Pagination: queryregistry.PaginationRequest{
			Offset: int(request.GetOffset()), Limit: int(page.GetLimit()), Cursor: page.GetCursor(),
		},
	}, nil
}

func splitProtocolV2QuerySchemaRef(reference string) (string, string, bool) {
	reference = strings.TrimSpace(reference)
	index := strings.LastIndexByte(reference, '@')
	if index <= 0 || index == len(reference)-1 {
		return "", "", false
	}
	version := reference[index+1:]
	if version[0] == '0' {
		return "", "", false
	}
	for _, character := range version {
		if character < '0' || character > '9' {
			return "", "", false
		}
	}
	return reference[:index], version, true
}

func queryRegistryProtocolV2Error(err error) *protocolv2.ErrorDetail {
	switch {
	case errors.Is(err, ErrProtocolV2QueryCallerStale), errors.Is(err, queryregistry.ErrArtifactUnavailable),
		errors.Is(err, queryregistry.ErrArtifactConflict), errors.Is(err, queryregistry.ErrRevisionConflict):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME, "host.query_runtime_stale", "The exact runtime, query artifact, or registry revision is no longer active.", false)
	case errors.Is(err, context.Canceled):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_CANCELLED, "host.query_cancelled", "The Query Registry execution was cancelled.", true)
	case errors.Is(err, context.DeadlineExceeded):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, "host.query_deadline_exceeded", "The Query Registry execution exceeded its deadline.", true)
	case errors.Is(err, ErrProtocolV2QueryRegistryUnavailable):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_registry_unavailable", "The Query Registry outlet is not configured.", true)
	case errors.Is(err, ErrProtocolV2QueryDelegationReplayed):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.query_actor_delegation_replayed", "The Host-signed query delegation was already used.", false)
	case errors.Is(err, ErrProtocolV2QueryReplayUnavailable):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_replay_ledger_unavailable", "The query replay ledger has reached its safe bound.", true)
	case errors.Is(err, ErrProtocolV2QueryDelegationInvalid):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED, "host.query_actor_delegation_invalid", "The Host-signed query delegation is invalid or stale.", false)
	case errors.Is(err, ErrProtocolV2QueryCapabilityDenied):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.query_capability_denied", "The exact extension artifact does not declare the capability this query requires.", false)
	case errors.Is(err, ErrProtocolV2QueryRateLimited):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED, "host.query_rate_limited", "The consent query rate limit for this actor and extension was reached.", true)
	case errors.Is(err, ErrProtocolV2QueryConsentGateUnavailable):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_consent_gate_unavailable", "The consent query gate is not configured or unavailable.", true)
	case errors.Is(err, ErrProtocolV2QueryActorDenied), errors.Is(err, queryregistry.ErrDenied):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.query_actor_permission_denied", "The delegated actor is not allowed to execute this query.", false)
	case errors.Is(err, queryregistry.ErrCostExceeded):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED, "host.query_cost_exceeded", "The query exceeds the Host cost limit.", false)
	case errors.Is(err, queryregistry.ErrResultTooLarge):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_MESSAGE_TOO_LARGE, "host.query_result_too_large", "The query result exceeds the Host response limit.", false)
	case errors.Is(err, queryregistry.ErrProviderUnavailable), errors.Is(err, queryregistry.ErrDependencyDenied):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_provider_unavailable", "The exact query provider or dependency is unavailable.", true)
	case errors.Is(err, queryregistry.ErrContractInsufficient):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH, "host.query_contract_insufficient", "The requested query shape is not supported by this contract.", false)
	case errors.Is(err, queryregistry.ErrInvalid), errors.Is(err, queryregistry.ErrExecutionInvalid),
		errors.Is(err, queryregistry.ErrCursorInvalid), errors.Is(err, queryregistry.ErrConflict),
		errors.Is(err, queryregistry.ErrNotFound):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.query_request_invalid", "The Query Registry request does not match an active contract.", false)
	case errors.Is(err, queryregistry.ErrResultInvalid), errors.Is(err, queryregistry.ErrCachePoisoned):
		return queryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.query_result_invalid", "The query result failed its Host release contract.", false)
	default:
		return queryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.query_execution_failed", "The Query Registry execution failed.", false)
	}
}

var _ ProtocolV2QueryActorDelegationIssuer = (*ProtocolV2QueryRegistryService)(nil)
var _ ProtocolV2QueryActorDelegationIssuer = (*Gateway)(nil)
