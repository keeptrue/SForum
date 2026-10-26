package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type commerceServer struct {
	*pluginv2.Server
	store *orderStore
}

// InvokeRoute 覆盖完整 Route Runtime：handler / request / response 阶段，
// 以及 custom guard（Host 用同一 RPC 以 guard id 作为 route id 调用）。
func (s *commerceServer) InvokeRoute(
	callCtx context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	routeID := request.GetRouteId()

	// Custom guard：仅允许/拒绝，不得返回 body。
	if routeID == guardOwnerID {
		return s.evaluateOwnerGuard(request), nil
	}

	switch routeID {
	case routeOrdersID:
		return s.handleOrders(callCtx, request)
	case routeManagedOrdersID:
		return s.handleManagedOrders(callCtx, request)
	case routeTopicsBeforeID:
		return s.handleTopicsBefore(request)
	case routeTopicsAfterID:
		return s.handleTopicsAfter(request)
	case routeTopicsFilterID:
		return s.handleTopicsFilter(request)
	case routeCreateWrapID:
		return s.handleCreateTopicWrap(request)
	case routeCreateReplaceID:
		return s.handleCreateTopicReplace(request)
	case routeEventsID, routeStreamID:
		// SSE/stream 必须走 StreamRoute；unary 只允许预检成功并声明 stream_follows。
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(ctx), StatusCode: http.StatusOK, StreamFollows: true,
		}, nil
	default:
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(ctx),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "commerce.route_unknown",
				Message: "unknown commerce route",
			},
		}, nil
	}
}

func (s *commerceServer) evaluateOwnerGuard(request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	ctx := request.GetContext()
	status := http.StatusNoContent
	// deny 查询或缺少 manage 权限 → 403。
	if request.GetQueryParameters()["deny"] == "1" {
		status = http.StatusForbidden
	}
	perms := request.GetContext().GetActor().GetPermissionKeys()
	allowed := false
	for _, key := range perms {
		if key == "*" || key == "sforum.commerce-workflow.manage" {
			allowed = true
			break
		}
	}
	if !allowed {
		status = http.StatusForbidden
	}
	if request.GetContractVersion() != guardOwnerID+"@1" {
		status = http.StatusForbidden
	}
	return &pluginwire.RouteResponse{Context: routeResponseContext(ctx), StatusCode: uint32(status)}
}

func (s *commerceServer) handleOrders(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	orders, err := s.store.list(callCtx)
	if err != nil {
		return routeError(ctx, "commerce.orders_list_failed", err.Error()), nil
	}
	items := make([]any, 0, len(orders))
	for _, item := range orders {
		items = append(items, map[string]any{
			"orderId": item.OrderID, "status": item.Status, "total": item.Total,
		})
	}
	// 实际调用 typed Host Query（own settings），有委托时记录 evidence。
	hostQueryOK := false
	if host, err := s.Host(); err == nil && host != nil {
		if req, err := host.DelegatedQueryRequest(
			ctx, pluginv2.HostQueryOwnSettingsID, pluginv2.HostQueryOwnSettingsVersion, "1",
		); err == nil {
			if resp, err := host.Queries.Execute(callCtx, req); err == nil && resp.GetError() == nil {
				hostQueryOK = true
			}
		}
	}
	body, err := pluginv2.NewTypedDocument(ordersResponseSchema, map[string]any{
		"orders": items, "databaseConnected": s.store.databaseConnected(),
		"hostQueryOk": hostQueryOK, "source": "commerce-workflow",
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
		Headers: []*protocolwire.Header{{Name: "X-Commerce-Source", Values: []string{"orders"}}},
	}, nil
}

func (s *commerceServer) handleManagedOrders(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	// managed 路由在 Host 侧已过 custom guard；此处再读库证明 guarded 路径可执行。
	orders, err := s.store.list(callCtx)
	if err != nil {
		return routeError(ctx, "commerce.managed_list_failed", err.Error()), nil
	}
	body, err := pluginv2.NewTypedDocument(managedOrdersResponseSchema, map[string]any{
		"count": len(orders), "guard": "owner", "managed": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *commerceServer) handleTopicsBefore(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	// before：可变 query 字段注入 commerceTrace。
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		RequestPatch: []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
			Path: "/query/commerceTrace", ValueJson: []byte(`"commerce-before"`),
		}},
	}, nil
}

func (s *commerceServer) handleTopicsAfter(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	// after 是 response-stage：只能返回 ResponsePatch，不能带 terminal Headers/Body。
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		ResponsePatch: []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
			Path: "/headers/x-commerce-trace", ValueJson: []byte(`"after"`),
		}},
	}, nil
}

func (s *commerceServer) handleTopicsFilter(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	// filter：在 body 上打标记（Host 负责 JSON patch 应用）。
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		ResponsePatch: []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
			Path: "/body/commerceFiltered", ValueJson: []byte(`true`),
		}},
	}, nil
}

func (s *commerceServer) handleCreateTopicWrap(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	stage := request.GetInvocationStage()
	if stage == pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_REQUEST {
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(ctx),
			RequestPatch: []*pluginwire.RoutePatchOperation{{
				Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
				Path: "/body/commerceWrapped", ValueJson: []byte(`true`),
			}},
		}, nil
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		ResponsePatch: []*pluginwire.RoutePatchOperation{{
			Kind: pluginwire.RoutePatchOperationKind_ROUTE_PATCH_OPERATION_KIND_ADD,
			Path: "/body/commerceWrapResult", ValueJson: []byte(`"ok"`),
		}},
	}, nil
}

func (s *commerceServer) handleCreateTopicReplace(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	title := ""
	if request.GetBody() != nil && request.GetBody().GetValue() != nil {
		if v, ok := request.GetBody().GetValue().AsMap()["title"].(string); ok {
			title = v
		}
	}
	if strings.TrimSpace(title) == "" {
		title = "commerce-replaced"
	}
	value, err := structpb.NewStruct(map[string]any{
		"id": "commerce-topic-1", "title": title, "replacedBy": "sforum.commerce-workflow",
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusCreated,
		Body: &protocolwire.TypedDocument{
			SchemaId: "sforum.route.forum.create_topic.response", SchemaVersion: "1", Value: value,
		},
	}, nil
}

func routeResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	if request == nil {
		return &protocolwire.ResponseContext{ServerTime: timestamppb.New(time.Now().UTC())}
	}
	return &protocolwire.ResponseContext{
		RequestId: request.GetRequestId(),
		Trace:     proto.Clone(request.GetTrace()).(*protocolwire.TraceContext),
		Extension: proto.Clone(request.GetExtension()).(*protocolwire.ExtensionIdentity),
		ServerTime: timestamppb.New(time.Now().UTC()),
	}
}

func routeError(ctx *protocolwire.RequestContext, reason, message string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, Reason: reason, Message: message,
		},
	}
}
