// Package main 是「插件命名空间会话状态工作流」参考后端。
//
// 它实现一个最小的多步结账工作流，用来证明 M6/S9 的三条运行时性质：
//
//  1. 会话状态跨请求保持：GET 路由读步数，POST 路由推进并写回，
//     下一次 GET 能看到前一次 POST 写入的字节；
//  2. 会话状态随会话结束消失：宿主侧的擦除钩子失效命名空间之后，
//     同一工作流回到初始步（由集成测试驱动真 store 验证）；
//  3. 委派是单次调用的：插件把本次调用收到的 HostSessionDelegation 回带到
//     Host 的 PluginSessionService（也是真实 gRPC），宿主在调用结算后收回，
//     重放被拒。
//
// 安全边界（由宿主强制，不由插件自证）：
//   - 插件拿不到也提供不了扩展 id、用户 id 或会话 id——请求形状里没有这些字段（S7）；
//   - 值是不透明字节，插件自己做版本化并容忍未命中（S2）；
//   - 登出/撤销/擦除由宿主的擦除钩子处理，插件没有任何入口可以代劳。
//
// `delegationEcho` 是**集成测试面**：把本次调用的委派令牌原样带回响应文档，
// 供宿主 CI 在调用结算后用同一条真实服务断言「重放被拒」。令牌本身只承载
// 运行时绑定，绝不含会话指纹（S8；`TestProtocolV2SessionDelegationTokenCarriesNoSessionIdentifier`）。
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	extensionID = "sforum.session-checkout-reference"

	stateRouteID   = extensionID + ".route.state"
	advanceRouteID = extensionID + ".route.advance"

	stateResponseSchemaID       = extensionID + ".checkout-state"
	advanceRequestSchemaID      = extensionID + ".advance.request"
	stateResponseSchemaVersion  = "1"
	advanceRequestSchemaVersion = "1"
	checkoutSessionNamespaceKey = "checkout/step"
	// checkoutValueKind 让状态字节自带版本（S2 迁移策略：插件自己的版本化义务）。
	checkoutValueKind = "checkout-step@1"
)

// maxCheckoutStep 保持值形状有界：工作流是 3 步的演示，不是通用计数器。
const maxCheckoutStep = 3

func main() {
	pluginv2.Serve(&sessionCheckoutServer{Server: pluginv2.NewServer()})
}

type sessionCheckoutServer struct{ *pluginv2.Server }

// checkoutStepValue 是存储在宿主会话命名空间里的不透明字节（S2）。显式携带
// kind，让未来改形状的插件版本能认出它。（未知 kind 按未命中处理。）
type checkoutStepValue struct {
	Kind string `json:"kind"`
	Step int    `json:"step"`
}

type checkoutStepState struct {
	// Step 是当前步；0 表示「没有可用的会话委派」，插件级降级。
	Step         int  `json:"step"`
	SessionBound bool `json:"sessionBound"`
	Persisted    bool `json:"persisted,omitempty"`
	// DelegationEcho 是集成测试面（见包注释），不是产品响应语义。
	DelegationEcho string `json:"delegationEcho,omitempty"`
}

func (s *sessionCheckoutServer) InvokeRoute(
	ctx context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	switch request.GetRouteId() {
	case stateRouteID:
		if request.GetMethod() != http.MethodGet ||
			request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER {
			return s.invalidInvocation(request, stateRouteID), nil
		}
		return s.readStep(ctx, request), nil
	case advanceRouteID:
		if request.GetMethod() != http.MethodPost ||
			request.GetInvocationStage() != pluginwire.RouteInvocationStage_ROUTE_INVOCATION_STAGE_HANDLER {
			return s.invalidInvocation(request, advanceRouteID), nil
		}
		return s.advance(ctx, request), nil
	default:
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(request.GetContext()),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "session_checkout.unknown_route",
			},
		}, nil
	}
}

func (s *sessionCheckoutServer) invalidArgument(request *pluginwire.RouteRequest, reason string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason,
		},
	}
}

func (s *sessionCheckoutServer) invalidInvocation(request *pluginwire.RouteRequest, routeID string) *pluginwire.RouteResponse {
	log.Printf("session-checkout-reference: refusing unexpected invocation of %s (method=%s stage=%s)",
		routeID, request.GetMethod(), request.GetInvocationStage())
	return s.invalidArgument(request, "session_checkout.invalid_invocation")
}

// sessionState 为一次调用返回会话状态句柄；没有委派时返回 nil（调用方降级），
// 这正是契约对「后台调用/已擦除会话」的预期姿态。
func (s *sessionCheckoutServer) sessionState(request *pluginwire.RouteRequest) (*pluginv2.HostSessionState, error) {
	host, err := s.Host()
	if err != nil {
		return nil, err
	}
	state, err := host.SessionState(request.GetContext())
	if err != nil {
		if errors.Is(err, pluginv2.ErrPluginSessionDelegationUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	return state, nil
}

func (s *sessionCheckoutServer) readStep(ctx context.Context, request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	document := &checkoutStepState{Step: 0, SessionBound: false}
	state, err := s.sessionState(request)
	if err != nil {
		log.Printf("session-checkout-reference: session state handle failed: %v", err)
		return s.respond(request, document)
	}
	// 委派令牌在插件视角是运行时绑定的不透明串（S8）：回带给宿主是它唯一的合法去处，
	// 响应文档里的 echo 只供宿主 CI 验证「结算后重放被拒」。
	document.DelegationEcho = delegationToken(request.GetContext())
	document.SessionBound = true
	if state == nil {
		// 理论上到不了这里（token 存在但句柄为 nil 是形状错误），按未绑定降级。
		document.SessionBound = false
		document.DelegationEcho = ""
		return s.respond(request, document)
	}
	value, found, err := state.Get(ctx, checkoutSessionNamespaceKey)
	if err != nil {
		// 存储故障：按类型化失败回给宿主，而不是把故障踩底成 step 1。
		return s.pluginFailure(request, err.Error())
	}
	step := int(0)
	if found {
		parsed, ok := parseStep(value)
		if ok {
			step = parsed
		}
	} else {
		// 未命中是预期路径（S2）：工作流从第 1 步开始，不把 miss 当故障。
		step = 1
	}
	document.Step = step
	document.Persisted = found
	return s.respond(request, document)
}

func (s *sessionCheckoutServer) advance(ctx context.Context, request *pluginwire.RouteRequest) *pluginwire.RouteResponse {
	body := request.GetBody()
	action := ""
	if body != nil {
		action, _ = body.GetValue().AsMap()["action"].(string)
	}
	if action != "next" {
		return s.invalidArgument(request, "session_checkout.invalid_action")
	}
	state, err := s.sessionState(request)
	if err != nil {
		log.Printf("session-checkout-reference: session state handle failed: %v", err)
		return s.respond(request, &checkoutStepState{})
	}
	if state == nil {
		return s.respond(request, &checkoutStepState{})
	}
	value, found, err := state.Get(ctx, checkoutSessionNamespaceKey)
	if err != nil {
		return s.pluginFailure(request, err.Error())
	}
	step := 1
	persisted := false
	if found {
		if parsed, ok := parseStep(value); ok {
			step = parsed
			persisted = true
		}
	}
	if step >= maxCheckoutStep {
		return s.invalidArgument(request, "session_checkout.already_final_step")
	}
	newStep := step + 1
	effectiveTTL, err := state.Set(
		ctx, checkoutSessionNamespaceKey, encodeStep(newStep), 24*time.Hour,
	)
	if err != nil {
		return s.pluginFailure(request, err.Error())
	}
	log.Printf("session-checkout-reference: advanced checkout to step %d (effectiveTTL=%s)", newStep, effectiveTTL)
	return s.respond(request, &checkoutStepState{
		Step: newStep, SessionBound: true, Persisted: persisted,
		DelegationEcho: delegationToken(request.GetContext()),
	})
}

func (s *sessionCheckoutServer) pluginFailure(request *pluginwire.RouteRequest, reason string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()),
		Error: &protocolwire.ErrorDetail{
			Code:      protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			Reason:    "session_checkout.store_failure",
			Message:   reason,
			Retryable: true,
		},
	}
}

func encodeStep(step int) []byte {
	value := checkoutStepValue{Kind: checkoutValueKind, Step: step}
	body, _ := json.Marshal(value)
	return body
}

// parseStep 只认同一 kind 的字节；未知形状按未命中处理（S2 的容错要求）。
func parseStep(value []byte) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	var parsed checkoutStepValue
	if err := json.Unmarshal(value, &parsed); err != nil || parsed.Kind != checkoutValueKind {
		return 0, false
	}
	if parsed.Step < 1 || parsed.Step > maxCheckoutStep {
		return 0, false
	}
	return parsed.Step, true
}

func delegationToken(parent *protocolwire.RequestContext) string {
	if parent == nil || parent.GetHostSessionDelegation() == nil {
		return ""
	}
	return parent.GetHostSessionDelegation().GetToken()
}

func (s *sessionCheckoutServer) respond(request *pluginwire.RouteRequest, state *checkoutStepState) *pluginwire.RouteResponse {
	// 契约字段=响应的全部语义；persisted/delegationEcho 是文档里的可选字段。
	fields := map[string]any{"step": float64(state.Step), "sessionBound": state.SessionBound}
	if state.Persisted {
		fields["persisted"] = true
	}
	if state.DelegationEcho != "" {
		fields["delegationEcho"] = state.DelegationEcho
	}
	value, err := structpb.NewStruct(fields)
	if err != nil {
		return s.pluginFailure(request, err.Error())
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(request.GetContext()), StatusCode: http.StatusOK,
		Body: &protocolwire.TypedDocument{
			SchemaId: stateResponseSchemaID, SchemaVersion: stateResponseSchemaVersion, Value: value,
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
