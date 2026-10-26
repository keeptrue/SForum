// Package main 是「稳定查询面消费」参考后端（E10.4）。
//
// 它证明插件作者不改 core、不申请 raw_core，就能用三个稳定 HostQuery 合同
// 拼出一个真实的公开数据面：
//
//  1. `sforum.core.site_stats.current` —— 单行公开总量（Single，无过滤）；
//  2. `sforum.core.public_topics.list` —— 公开主题列表（默认 last_activity 降序）；
//  3. `sforum.core.safe_users.batch` —— 批量作者资料 + 头像（int64 列表过滤，
//     规范升序 JSON 数组，≤100 个 id）。
//
// 安全边界（由宿主强制，不由插件自证）：
//   - 清单声明 `database.grants: ["core_views"]`，制品精确匹配 + 信任授予；
//   - 插件拿到的只有 schema 校验后的 typed rows：BIGINT 是十进制字符串，
//     INTEGER 是数字，可空列可能是 null；
//   - 任何查询失败都按类型化失败上抛，绝不静默降级成假数据。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	extensionID = "sforum.query-surface-reference"

	statsRouteID = extensionID + ".route.stats"

	statsResponseSchemaID      = extensionID + ".stats"
	statsResponseSchemaVersion = "1"

	// 稳定查询合同（见 authoring-guide 的 Stable Host Queries 一节）。
	siteStatsQueryID   = "sforum.core.site_stats.current"
	topicsQueryID      = "sforum.core.public_topics.list"
	safeUsersBatchID   = "sforum.core.safe_users.batch"
	stableCorePlanV1   = "1"
	int64ListParamRef  = "sforum.core.query.text"
	recentTopicsLimit  = 3
	maxBatchAuthorRefs = 100
)

func main() {
	pluginv2.Serve(&querySurfaceServer{Server: pluginv2.NewServer()})
}

type querySurfaceServer struct{ *pluginv2.Server }

func (s *querySurfaceServer) InvokeRoute(
	ctx context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	switch request.GetRouteId() {
	case statsRouteID:
		if request.GetMethod() != http.MethodGet ||
			request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER {
			return s.invalidInvocation(request, statsRouteID), nil
		}
		return s.stats(ctx, request), nil
	default:
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "query_surface.unknown_route",
			},
		}, nil
	}
}

func (s *querySurfaceServer) invalidInvocation(request *pluginwire.RouteRequest, routeID string) *pluginwire.RouteResponse {
	log.Printf("query-surface-reference: refusing unexpected invocation of %s (method=%s stage=%s)",
		routeID, request.GetMethod(), request.GetInvocationStage())
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "query_surface.invalid_invocation",
		},
	}
}

func (s *querySurfaceServer) stats(ctx context.Context, request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	host, err := s.Host()
	if err != nil {
		return s.pluginFailure(request, "host broker unavailable")
	}

	statsRows, err := executeQueryRows(ctx, host, request.GetContext(), &hostwire.QueryRequest{
		Context: host.RequestContext(request.GetContext()),
		QueryId: siteStatsQueryID, PlanVersion: stableCorePlanV1,
	})
	if err != nil {
		return s.pluginFailure(request, "site stats: "+err.Error())
	}
	if len(statsRows) != 1 {
		return s.pluginFailure(request, "site stats must return exactly one row")
	}
	siteStats := map[string]any{
		"topicCount":            statsRows[0]["topic_count"],
		"commentCount":          statsRows[0]["comment_count"],
		"userCount":             statsRows[0]["user_count"],
		"publicAttachmentCount": statsRows[0]["public_attachment_count"],
	}

	topicsRows, err := executeQueryRows(ctx, host, request.GetContext(), &hostwire.QueryRequest{
		Context: host.RequestContext(request.GetContext()),
		QueryId: topicsQueryID, PlanVersion: stableCorePlanV1,
		Fields: []string{"id", "title", "slug", "author_user_id"},
		Page:   &protocolwire.PageRequest{Limit: recentTopicsLimit},
	})
	if err != nil {
		return s.pluginFailure(request, "recent topics: "+err.Error())
	}
	recentTopics := make([]map[string]any, 0, len(topicsRows))
	authorRefs := make([]int64, 0, len(topicsRows))
	for _, row := range topicsRows {
		topic := map[string]any{
			"id":    row["id"],
			"title": row["title"],
			"slug":  row["slug"],
		}
		if author, ok := row["author_user_id"].(string); ok && author != "" {
			topic["authorUserId"] = author
			if parsed, parseErr := strconv.ParseInt(author, 10, 64); parseErr == nil && parsed > 0 {
				authorRefs = append(authorRefs, parsed)
			}
		}
		recentTopics = append(recentTopics, topic)
	}

	authors := make([]map[string]any, 0, len(authorRefs))
	if len(authorRefs) > 0 {
		refs := canonicalAuthorRefs(authorRefs)
		filterValue, err := pluginv2.NewTypedDocument(int64ListParamRef, map[string]any{"value": formatIDList(refs)})
		if err != nil {
			return s.pluginFailure(request, "author filter document: "+err.Error())
		}
		batchRows, err := executeQueryRows(ctx, host, request.GetContext(), &hostwire.QueryRequest{
			Context: host.RequestContext(request.GetContext()),
			QueryId: safeUsersBatchID, PlanVersion: stableCorePlanV1,
			Fields:  []string{"user_id", "username", "display_name", "avatar_public_id"},
			Filters: []*hostwire.QueryFilter{{Field: "user_id", Operator: "eq", Value: filterValue}},
		})
		if err != nil {
			return s.pluginFailure(request, "author batch: "+err.Error())
		}
		for _, row := range batchRows {
			author := map[string]any{
				"userId":      row["user_id"],
				"username":    row["username"],
				"displayName": row["display_name"],
			}
			if avatar, ok := row["avatar_public_id"]; ok && avatar != nil {
				author["avatarPublicId"] = avatar
			}
			authors = append(authors, author)
		}
	}

	value, err := structpb.NewStruct(map[string]any{
		"siteStats":    siteStats,
		"recentTopics": recentTopics,
		"authors":      authors,
		"hostQueryOk":  true,
	})
	if err != nil {
		return s.pluginFailure(request, err.Error())
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusOK,
		Body: &protocolwire.TypedDocument{
			SchemaId: statsResponseSchemaID, SchemaVersion: statsResponseSchemaVersion, Value: value,
		},
	}
}

// canonicalAuthorRefs 去重、过滤非正值并严格升序排列——`int64_list` 过滤
// 只接受 canonical JSON 数组（与宿主侧 parseProtocolV2QueryInt64List 一致）。
func canonicalAuthorRefs(refs []int64) []int64 {
	seen := make(map[int64]struct{}, len(refs))
	unique := make([]int64, 0, len(refs))
	for _, ref := range refs {
		if ref <= 0 {
			continue
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		unique = append(unique, ref)
	}
	sort.Slice(unique, func(left, right int) bool { return unique[left] < unique[right] })
	if len(unique) > maxBatchAuthorRefs {
		unique = unique[:maxBatchAuthorRefs]
	}
	return unique
}

func formatIDList(refs []int64) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		parts = append(parts, strconv.FormatInt(ref, 10))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func executeQueryRows(
	ctx context.Context,
	host *pluginv2.Host,
	parent *protocolwire.RequestContext,
	request *hostwire.QueryRequest,
) ([]map[string]any, error) {
	response, err := host.Queries.Execute(ctx, request)
	if err != nil {
		return nil, err
	}
	if response.GetError() != nil {
		return nil, errors.New(response.GetError().GetReason())
	}
	rows := make([]map[string]any, 0, len(response.GetRows()))
	for _, row := range response.GetRows() {
		if row.GetValue() == nil {
			return nil, errors.New("query row has no value document")
		}
		rows = append(rows, row.GetValue().AsMap())
	}
	return rows, nil
}

func (s *querySurfaceServer) pluginFailure(request *pluginwire.RouteRequest, reason string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			Reason:  "query_surface.host_query_failed",
			Message: reason,
		},
	}
}

func routeResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	if request == nil {
		return &protocolwire.ResponseContext{ServerTime: timestamppb.New(time.Now().UTC())}
	}
	return &protocolwire.ResponseContext{
		RequestId:  request.GetRequestId(),
		Trace:      proto.Clone(request.GetTrace()).(*protocolwire.TraceContext),
		Extension:  proto.Clone(request.GetExtension()).(*protocolwire.ExtensionIdentity),
		ServerTime: timestamppb.New(time.Now().UTC()),
	}
}
