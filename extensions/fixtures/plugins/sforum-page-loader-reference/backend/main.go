// Package main 是「带 typed loader data 的可执行插件页」参考后端。
//
// 它只做一件事：在 Protocol V2 子进程里实现一条**公开数据路由**，返回页面模板需要的
// 类型化文档。宿主 Page Loader Gateway 通过 `InvokeRouteInstance` 调用它（决定 R2），
// 因此插件不需要回报 loopback BaseURL。
//
// 安全前提（由宿主强制，不由插件自证）：
//   - 路由必须是 manifest 里声明的 `core.guard.public` GET 路由，否则 loader 拒绝调用；
//   - 调用以**过滤授权**抵达：loader 不携带 cookie/authorization，也没有 actor 身份。
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	extensionID = "sforum.page-loader-reference"
	// 数据路由 id 必须与 manifest 的 routes[].id 一致。
	pageDataRouteID = extensionID + ".route.page-data"
	// 响应 schema 必须与 manifest 的 routes[].responseSchema 一致。
	// 注意线协议约定：TypedDocument.SchemaId 只写 id，版本单独放在 SchemaVersion；
	// manifest 里的引用才是 `id@version` 形式（validateProtocolV2DocumentRef 会拆开比对）。
	pageDataResponseSchemaID = extensionID + ".page-data.response"
	pageDataResponseVersion  = "1"
)

func main() {
	pluginv2.Serve(&pageLoaderServer{Server: pluginv2.NewServer()})
}

type pageLoaderServer struct{ *pluginv2.Server }

func (s *pageLoaderServer) InvokeRoute(
	_ context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	if request.GetRouteId() != pageDataRouteID {
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "page_loader.unknown_route",
			},
		}, nil
	}
	// 宿主只应以 GET + handler 阶段调用公开数据路由；其他形态一律拒绝，
	// 让夹具本身也能暴露「loader 语义被改动」的回归。
	if request.GetMethod() != http.MethodGet ||
		request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER {
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "page_loader.invalid_invocation",
			},
		}, nil
	}
	slug := request.GetPathParameters()["slug"]
	document, err := structpb.NewStruct(map[string]any{
		"title": "Loaded from the plugin runtime: " + slug,
		"state": "published",
	})
	if err != nil {
		return nil, err
	}
	log.Printf("page-loader-reference: served page data for slug=%q", slug)
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusOK,
		Body: &protocolwire.TypedDocument{
			SchemaId: pageDataResponseSchemaID, SchemaVersion: pageDataResponseVersion, Value: document,
		},
	}, nil
}

// routeResponseContext 回填宿主请求上下文（与 commerce 参考包同一形态：
// requestId/trace/extension/serverTime），宿主用它关联调用与审计。
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
