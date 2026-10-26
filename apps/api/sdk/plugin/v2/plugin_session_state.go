package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

var (
	// ErrPluginSessionDelegationUnavailable 表示这次调用没有宿主签发的会话委派。
	// 它是**预期**结果：后台任务、非 HTTP 请求发起的调用、以及被擦除的会话都没有它。
	ErrPluginSessionDelegationUnavailable = errors.New("protocol v2 session delegation is unavailable")
	// ErrPluginSessionResponseInvalid 表示宿主响应与请求不匹配（上下文/身份漂移）。
	ErrPluginSessionResponseInvalid = errors.New("protocol v2 session response is invalid")
)

// HostPluginSessionError 是宿主返回的类型化失败。Reason 是稳定标识（例如
// `host.plugin_session_key_limit`），插件应按它分支，而不是解析 Message。
type HostPluginSessionError struct {
	Code      protocolwire.ErrorCode
	Reason    string
	Message   string
	Retryable bool
}

func (e *HostPluginSessionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return "plugin session state: " + e.Reason + ": " + e.Message
	}
	return "plugin session state: " + e.Message
}

// HostSessionState 是插件侧唯一可用的会话状态入口。
//
// 三个刻意的形状约束：
//
//  1. 它只能从**宿主交给本次调用的**上下文里构造（`Host.SessionState(parent)`）。
//     插件拿不到扩展 id、用户 id 或 session id，也无法自己构造委派令牌。
//  2. 值是不透明字节，没有宿主侧 schema（S2）：插件要自己做版本化，并且必须能容忍
//     未命中——这是可丢失的工作流状态，不是持久数据。
//  3. 生命周期由宿主决定：有效 TTL 取 min(请求值, 宿主 24h 上限, 会话剩余 idle,
//     会话剩余绝对寿命)，插件状态永远不会延长 Core 会话。
type HostSessionState struct {
	host       *Host
	parent     *protocolwire.RequestContext
	delegation *protocolwire.HostSessionDelegation
}

// SessionState 为一次宿主发起的调用返回会话状态句柄。
//
// 没有委派时返回 ErrPluginSessionDelegationUnavailable：这是正常路径（后台调用、
// 匿名请求之外的场景、已擦除会话），插件应当降级而不是当成故障。
func (h *Host) SessionState(parent *protocolwire.RequestContext) (*HostSessionState, error) {
	if h == nil || h.PluginSession == nil {
		return nil, ErrHostUnavailable
	}
	if parent == nil {
		return nil, ErrPluginSessionDelegationUnavailable
	}
	delegation := parent.GetHostSessionDelegation()
	if delegation == nil || strings.TrimSpace(delegation.GetToken()) == "" {
		return nil, ErrPluginSessionDelegationUnavailable
	}
	return &HostSessionState{host: h, parent: parent, delegation: delegation}, nil
}

// Get 读取一个命名空间键。未命中返回 (nil, false, nil)；只有真实故障才返回错误，
// 这样插件可以把「没有状态」与「存储坏了」分开处理。
func (s *HostSessionState) Get(ctx context.Context, namespaceKey string) ([]byte, bool, error) {
	if s == nil || s.host == nil || s.delegation == nil || ctx == nil {
		return nil, false, ErrPluginSessionDelegationUnavailable
	}
	requestContext := s.requestContext()
	response, err := s.host.PluginSession.Get(ctx, &hostwire.PluginSessionGetRequest{
		Context: requestContext, NamespaceKey: namespaceKey,
	})
	if err != nil {
		return nil, false, err
	}
	if err := s.validateResponse(requestContext, response.GetContext(), response.GetError()); err != nil {
		return nil, false, err
	}
	if !response.GetFound() {
		return nil, false, nil
	}
	return append([]byte(nil), response.GetValue()...), true, nil
}

// Set 写入一个命名空间键并返回宿主实际应用的 TTL。请求 0 表示「随会话」，宿主仍会
// 用它自己的上限与会话剩余寿命收紧。
func (s *HostSessionState) Set(
	ctx context.Context,
	namespaceKey string,
	value []byte,
	ttl time.Duration,
) (time.Duration, error) {
	if s == nil || s.host == nil || s.delegation == nil || ctx == nil {
		return 0, ErrPluginSessionDelegationUnavailable
	}
	if ttl < 0 || ttl > maxPluginSessionRequestTTL {
		return 0, fmt.Errorf("plugin session state: ttl %s is out of range", ttl)
	}
	requestContext := s.requestContext()
	response, err := s.host.PluginSession.Set(ctx, &hostwire.PluginSessionSetRequest{
		Context: requestContext, NamespaceKey: namespaceKey,
		Value: append([]byte(nil), value...), TtlMs: uint32(ttl.Milliseconds()),
	})
	if err != nil {
		return 0, err
	}
	if err := s.validateResponse(requestContext, response.GetContext(), response.GetError()); err != nil {
		return 0, err
	}
	return time.Duration(response.GetEffectiveTtlMs()) * time.Millisecond, nil
}

// Delete 删除一个命名空间键并报告它此前是否存在。
func (s *HostSessionState) Delete(ctx context.Context, namespaceKey string) (bool, error) {
	if s == nil || s.host == nil || s.delegation == nil || ctx == nil {
		return false, ErrPluginSessionDelegationUnavailable
	}
	requestContext := s.requestContext()
	response, err := s.host.PluginSession.Delete(ctx, &hostwire.PluginSessionDeleteRequest{
		Context: requestContext, NamespaceKey: namespaceKey,
	})
	if err != nil {
		return false, err
	}
	if err := s.validateResponse(requestContext, response.GetContext(), response.GetError()); err != nil {
		return false, err
	}
	return response.GetDeleted(), nil
}

// maxPluginSessionRequestTTL 与宿主 S6 信封一致：请求超过 24h 会被宿主拒绝，
// 与其发一次必然失败的往返，不如在插件侧就地拒绝。
const maxPluginSessionRequestTTL = 24 * time.Hour

// requestContext 复刻宿主发来的运行时字段并回带委派令牌。委派必须显式回带，
// 因为 Host.RequestContext 会清掉所有委派材料（它们只由对应的 helper 消费）。
func (s *HostSessionState) requestContext() *protocolwire.RequestContext {
	requestContext := s.host.RequestContext(s.parent)
	requestContext.HostSessionDelegation = proto.Clone(s.delegation).(*protocolwire.HostSessionDelegation)
	return requestContext
}

func (s *HostSessionState) validateResponse(
	request *protocolwire.RequestContext,
	response *protocolwire.ResponseContext,
	detail *protocolwire.ErrorDetail,
) error {
	if detail != nil {
		if detail.GetCode() == protocolwire.ErrorCode_ERROR_CODE_UNSPECIFIED {
			return ErrPluginSessionResponseInvalid
		}
		return &HostPluginSessionError{
			Code: detail.GetCode(), Reason: detail.GetReason(),
			Message: detail.GetMessage(), Retryable: detail.GetRetryable(),
		}
	}
	if response == nil || response.GetRequestId() != request.GetRequestId() ||
		!proto.Equal(response.GetExtension(), s.host.identity) {
		return ErrPluginSessionResponseInvalid
	}
	return nil
}
