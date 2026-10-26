package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	QuerySafeUserByID               = "sforum.core.safe_user.by_id"
	QueryPublicTopicsList           = "sforum.core.public_topics.list"
	QueryPublicTopicByID            = "sforum.core.public_topic.by_id"
	QueryPublicAttachmentByPublicID = "sforum.core.public_attachment.by_public_id"
	QuerySiteStats                  = "sforum.core.site_stats.current"
	QueryPublicCommentsList         = "sforum.core.public_comments.list"
	QuerySafeUserByUsername         = "sforum.core.safe_user.by_username"
	QuerySafeUsersBatch             = "sforum.core.safe_users.batch"
	QueryPublicCategoriesList       = "sforum.core.public_categories.list"
	// QuerySelfEmail 只由 host 签发的 actor 委派驱动执行；actorless 稳定入口
	// 一律拒绝（见 consent_query_definitions.go）。
	QuerySelfEmail                   = "sforum.core.identity.self_email"
	QueryStableCorePlanVersion       = "1"
	QuerySafeUserResultSchemaID      = "sforum.core.safe_user"
	QueryPublicTopicResultSchemaID   = "sforum.core.public_topic"
	QueryPublicAttachmentSchemaID    = "sforum.core.public_attachment_metadata"
	QuerySiteStatsResultSchemaID     = "sforum.core.site_stats"
	QueryPublicCommentResultSchemaID = "sforum.core.public_comment"
	QuerySafeUserAvatarSchemaID      = "sforum.core.safe_user_avatar"
	QueryPublicCategorySchemaID      = "sforum.core.public_category"
	QuerySelfEmailSchemaID           = "sforum.core.self_email"
	QueryStableCoreResultSchemaV1    = "1"
	QueryInt64ParameterSchemaID      = "sforum.core.query.int64"
	QueryTextParameterSchemaID       = "sforum.core.query.text"
	QueryStableCoreParameterSchemaV1 = "1"
	protocolV2QueryDefaultLimit      = 20
	protocolV2QueryMaximumLimit      = 100
	protocolV2QueryMaximumOffset     = 1_000_000
	protocolV2QueryMaxBatchIDs       = 100
	protocolV2QueryExecutionTimeout  = 5 * time.Second
)

var ErrProtocolV2QueryRuntimeStale = errors.New("Host Query exact runtime identity is stale")

// ProtocolV2QueryAuthority is resolved from Host-owned exact artifact and
// trust state. CoreViews remains a separate bit so future additive grants do
// not require trusting RequestContext.granted_authority or changing the query
// engine. Capabilities is the exact artifact's effective capability key set,
// used only by ConsentGated definitions (never by ordinary stable queries).
type ProtocolV2QueryAuthority struct {
	ExactArtifact bool
	CoreViews     bool
	Capabilities  []string
}

type ProtocolV2QueryAuthorityResolver interface {
	ResolveProtocolV2QueryAuthority(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error)
}

type ProtocolV2QueryRuntime interface {
	queryEngine() *protocolV2QueryEngine
}

type protocolV2QueryRuntime struct {
	engine *protocolV2QueryEngine
}

func (r *protocolV2QueryRuntime) queryEngine() *protocolV2QueryEngine {
	if r == nil {
		return nil
	}
	return r.engine
}

type protocolV2QueryExecutor interface {
	ExecuteProtocolV2Query(context.Context, protocolV2QueryPlan) ([]map[string]any, error)
}

type protocolV2QueryEngine struct {
	executor      protocolV2QueryExecutor
	authority     ProtocolV2QueryAuthorityResolver
	definitions   map[protocolV2QueryKey]protocolV2QueryDefinition
	traceSink     QueryTraceSink
	slowThreshold time.Duration
}

func newProtocolV2QueryRuntime(
	executor protocolV2QueryExecutor,
	authority ProtocolV2QueryAuthorityResolver,
	definitions ...protocolV2QueryDefinition,
) (ProtocolV2QueryRuntime, error) {
	engine, err := newProtocolV2QueryEngine(executor, authority, definitions...)
	if err != nil {
		return nil, err
	}
	return &protocolV2QueryRuntime{engine: engine}, nil
}

func newProtocolV2QueryEngine(
	executor protocolV2QueryExecutor,
	authority ProtocolV2QueryAuthorityResolver,
	definitions ...protocolV2QueryDefinition,
) (*protocolV2QueryEngine, error) {
	if executor == nil || authority == nil {
		return nil, errors.New("hostapi: query executor and authority resolver are required")
	}
	catalog := make(map[protocolV2QueryKey]protocolV2QueryDefinition, len(definitions))
	for _, source := range definitions {
		definition := cloneProtocolV2QueryDefinition(source)
		key := protocolV2QueryKey{id: definition.ID, version: definition.PlanVersion}
		if err := validateProtocolV2QueryDefinition(definition); err != nil {
			return nil, err
		}
		if _, exists := catalog[key]; exists {
			return nil, fmt.Errorf("hostapi: duplicate query %s@%s", key.id, key.version)
		}
		catalog[key] = definition
	}
	return &protocolV2QueryEngine{
		executor: executor, authority: authority, definitions: catalog,
		slowThreshold: ProtocolV2QueryDefaultSlowThreshold,
	}, nil
}

func isStableProtocolV2QueryID(queryID string) bool {
	switch strings.TrimSpace(queryID) {
	case QuerySafeUserByID, QueryPublicTopicsList, QueryPublicTopicByID, QueryPublicAttachmentByPublicID,
		QuerySiteStats, QueryPublicCommentsList, QuerySafeUserByUsername, QuerySafeUsersBatch, QueryPublicCategoriesList,
		QuerySelfEmail:
		return true
	default:
		return false
	}
}

func (e *protocolV2QueryEngine) execute(
	ctx context.Context,
	request *hostv2.QueryRequest,
) *hostv2.QueryResponse {
	response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
	startedAt := time.Now()
	identity := protocolV2RuntimeIdentityFromContext(ctx)
	trace := QueryTrace{
		QueryID: strings.TrimSpace(request.GetQueryId()), PlanVersion: strings.TrimSpace(request.GetPlanVersion()),
		Outcome: QueryTraceError,
	}
	if identity != nil {
		trace.ExtensionID = identity.GetExtensionId()
		trace.ExtensionVersion = identity.GetExtensionVersion()
		trace.ArtifactDigest = identity.GetArtifactDigest()
	}
	defer func() {
		if e == nil || e.traceSink == nil {
			return
		}
		trace.Duration = time.Since(startedAt)
		threshold := e.slowThreshold
		if threshold <= 0 || threshold > ProtocolV2QueryDefaultSlowThreshold {
			threshold = ProtocolV2QueryDefaultSlowThreshold
		}
		trace.Slow = trace.Duration >= threshold
		e.traceSink.RecordQueryTrace(boundedQueryTrace(trace))
	}()
	if e == nil || e.executor == nil || e.authority == nil {
		response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_backend_unavailable", "Stable Host Queries are not configured.", true)
		return response
	}
	ctx, cancel := context.WithTimeout(ctx, protocolV2QueryExecutionTimeout)
	defer cancel()
	authority, err := e.authority.ResolveProtocolV2QueryAuthority(ctx, identity)
	if err != nil {
		if errors.Is(err, ErrProtocolV2QueryRuntimeStale) {
			trace.Outcome = QueryTraceStale
			response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.query_runtime_stale", "The exact extension artifact or trust grant is no longer active.", false)
		} else if detail, outcome, interrupted := protocolV2QueryInterruption(ctx, err); interrupted {
			trace.Outcome = outcome
			response.Error = detail
		} else {
			response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_authority_unavailable", "Host Query authority could not be resolved.", true)
		}
		return response
	}
	if !authority.ExactArtifact {
		trace.Outcome = QueryTraceStale
		response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.query_runtime_stale", "The exact extension artifact or trust grant is no longer active.", false)
		return response
	}
	if !authority.CoreViews {
		trace.Outcome = QueryTraceDenied
		response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.query_core_views_denied", "The exact extension artifact has no stable core-view authority.", false)
		return response
	}
	definition, ok := e.definitions[protocolV2QueryKey{id: strings.TrimSpace(request.GetQueryId()), version: strings.TrimSpace(request.GetPlanVersion())}]
	if !ok {
		response.Error = protocolV2Unsupported("host.query_unsupported", "The query id or plan version is not supported.")
		return response
	}
	// consent-gated 定义不接受 actorless 执行：actor 只能来自 Host 签发的一次性
	// query actor 委派，而那条路径是 Query Registry 的显式投影。这里不留任何
	// 「先试执行再判权限」的窗口。
	if definition.ConsentGated {
		trace.Outcome = QueryTraceDenied
		response.Error = queryError(
			protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED, "host.query_actor_delegation_invalid",
			"This Host Query requires a Host-issued actor delegation.", false,
		)
		return response
	}
	plan, detail := buildProtocolV2QueryPlan(definition, request)
	if detail != nil {
		response.Error = detail
		return response
	}
	trace.ShapeDigest = protocolV2QueryTraceShapeDigest(plan)
	rows, err := e.executor.ExecuteProtocolV2Query(ctx, plan)
	if err != nil {
		if detail, outcome, interrupted := protocolV2QueryInterruption(ctx, err); interrupted {
			trace.Outcome = outcome
			response.Error = detail
		} else {
			response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.query_execution_failed", "The Host Query failed.", false)
		}
		return response
	}
	hasMore := len(rows) > plan.Limit
	if hasMore {
		rows = rows[:plan.Limit]
	}
	response.Rows = make([]*protocolv2.TypedDocument, 0, len(rows))
	for _, row := range rows {
		document, encodeErr := protocolV2Document(definition.ResultSchemaID, definition.ResultSchemaVersion, row)
		if encodeErr != nil {
			response.Rows = nil
			response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.query_encode_failed", "The Host Query result could not be encoded.", false)
			return response
		}
		response.Rows = append(response.Rows, document)
	}
	response.Page.HasMore = hasMore
	if hasMore {
		response.Page.NextCursor = encodeProtocolV2QueryCursor(protocolV2QueryCursor{
			QueryID: definition.ID, PlanVersion: definition.PlanVersion,
			ShapeDigest: plan.ShapeDigest, Offset: plan.Offset + plan.Limit,
		})
	}
	trace.Rows = len(response.Rows)
	trace.Outcome = QueryTraceAllowed
	return response
}

func protocolV2QueryInterruption(
	ctx context.Context,
	err error,
) (*protocolv2.ErrorDetail, QueryTraceOutcome, bool) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return queryError(
			protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED,
			"host.query_deadline_exceeded", "The Host Query exceeded its deadline.", true,
		), QueryTraceDeadline, true
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return queryError(
			protocolv2.ErrorCode_ERROR_CODE_CANCELLED,
			"host.query_cancelled", "The Host Query was cancelled.", true,
		), QueryTraceCancel, true
	}
	return nil, "", false
}
