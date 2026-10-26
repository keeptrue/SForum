// Package main 是「能力 + actor 委派的同意式读取面」参考后端。
//
// 它证明插件可以在**宿主发起的、有活 actor 的路由调用**里，凭宿主签发的单次
// query actor 委派读取**调用者本人**的 email 元数据，而不需要 raw_core、不需要
// per-plugin RPC，也不会看到别人的 email：
//
//  1. 清单声明 `capabilities: ["users.email.read"]`，精确制品匹配 + 信任授予；
//  2. 宿主在调用里把一次性 query 委派挂在 RequestContext.HostQueryDelegations；
//  3. 插件用 `host.SelfEmailQueryRequest` 把它换成 actorless 的 QueryRequest；
//  4. 结果只渲染 `emailVerified` / `emailPresent` / `emailUpdatedAt`——
//     **永不回显 email 明文**，也永不写入插件日志。
//
// 安全边界（由宿主强制，不由插件自证）：无能力 → 拿不到委派；匿名 → 拿不到
// 委派；重放 → 账本拒绝；请求里没有任何 userId 可注入；每次读取留审计（不含值）。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
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
	extensionID = "sforum.consented-claim-reference"

	identityRouteID = extensionID + ".route.identity"

	identityResponseSchemaID      = extensionID + ".identity"
	identityResponseSchemaVersion = "1"
)

func main() {
	pluginv2.Serve(&consentedClaimServer{Server: pluginv2.NewServer()})
}

type consentedClaimServer struct{ *pluginv2.Server }

func (s *consentedClaimServer) InvokeRoute(
	ctx context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	switch request.GetRouteId() {
	case identityRouteID:
		if request.GetMethod() != http.MethodGet ||
			request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER {
			return s.invalidInvocation(request), nil
		}
		return s.identity(ctx, request), nil
	default:
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "consented_claim.unknown_route",
			},
		}, nil
	}
}

func (s *consentedClaimServer) invalidInvocation(request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	log.Printf("consented-claim-reference: refusing unexpected invocation (method=%s stage=%s)",
		request.GetMethod(), request.GetInvocationStage())
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "consented_claim.invalid_invocation",
		},
	}
}

func (s *consentedClaimServer) identity(ctx context.Context, request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	host, err := s.Host()
	if err != nil {
		return s.pluginFailure(request, "host broker unavailable")
	}
	document := map[string]any{
		"authenticated":    request.GetContext().GetActor().GetUserId() > 0,
		"consentAvailable": false,
		"emailPresent":     false,
		"emailVerified":    nil,
		"emailUpdatedAt":   nil,
		"hostQueryOutcome": "no_actor",
	}
	if !document["authenticated"].(bool) {
		return s.respond(request, document)
	}
	query, err := host.SelfEmailQueryRequest(request.GetContext())
	if err != nil {
		if errors.Is(err, pluginv2.ErrHostQueryDelegationUnavailable) {
			// 未声明 users.email.read（或被禁用）时宿主不会签发委派：这不是失败，
			// 而是能力的正确降级。
			document["hostQueryOutcome"] = "delegation_unavailable"
			return s.respond(request, document)
		}
		return s.pluginFailure(request, "self-email request unavailable")
	}
	document["consentAvailable"] = true
	rows, err := executeQueryRows(ctx, host, query)
	if err != nil {
		// 只回传宿主给的稳定 reason，绝不带请求/结果值。
		return s.pluginFailure(request, err.Error())
	}
	document["hostQueryOutcome"] = "ok"
	if len(rows) == 1 {
		document["emailPresent"] = true
		document["emailVerified"] = rows[0]["email_verified"]
		document["emailUpdatedAt"] = rows[0]["updated_at"]
	}
	return s.respond(request, document)
}

func (s *consentedClaimServer) respond(request *pluginwire.RouteRequest, document map[string]any) *pluginwire.RouteResponse {
	value, err := structpb.NewStruct(document)
	if err != nil {
		return s.pluginFailure(request, "encode identity document")
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusOK,
		Body: &protocolwire.TypedDocument{
			SchemaId: identityResponseSchemaID, SchemaVersion: identityResponseSchemaVersion, Value: value,
		},
	}
}

func (s *consentedClaimServer) pluginFailure(request *pluginwire.RouteRequest, reason string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			Reason:  "consented_claim.host_query_failed",
			Message: reason,
		},
	}
}

func executeQueryRows(ctx context.Context, host *pluginv2.Host, request *hostwire.QueryRequest) ([]map[string]any, error) {
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
