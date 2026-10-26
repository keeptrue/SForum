package pluginv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 插件发布自身命名空间事件的身份化助手。
//
// 与 AI 补全命令同一手法：SDK 只依赖契约，不 import Host 实现；边界值在这里
// 重复声明一次，Host 侧另有等价常量，两侧由各自测试钉住。
const (
	HostEventsEmitCommandID       = "sforum.events.emit"
	HostEventsEmitCommandVersion  = "1"
	HostEventsEmitInputSchemaRef  = "sforum.events.emit.input@1"
	HostEventsEmitResultSchemaRef = "sforum.events.emit.result@1"

	HostEventsEmitMaxNameLen      = 160
	HostEventsEmitMaxPayloadBytes = 64 * 1024
	HostEventsEmitMaxPayloadKeys  = 64
	HostEventsEmitMaxIdempotency  = 128

	// 自定义事件只允许 observe 与 filter：validate 是宿主同步校验语义，
	// 插件自行发布一个「校验」事件没有宿主写路径可依附。
	HostEventsEmitKindObserve = "observe"
	HostEventsEmitKindFilter  = "filter"
)

// ErrHostEventEmitInvalid 表示本次发布请求在作者侧就能判定为无效。
var ErrHostEventEmitInvalid = errors.New("protocol v2 event publish request is invalid")

// EventEmitInput 是一次插件事件发布。
//
// Name 必须是本插件命名空间下的事件名（`<extensionId>.` 前缀）。SDK 无法在这里
// 权威判定命名空间归属——那属于 Host 的已验证 runtime 身份——但会拒绝明显不可能
// 成立的名字（空、超长、含空白），让一次必然失败的往返留在本地。
type EventEmitInput struct {
	Name           string
	Kind           string
	Payload        map[string]any
	IdempotencyKey string
}

// EventEmitRequest 构造 actorless 的 sforum.events.emit@1 请求。
// Host 仍然是权威校验方：命名空间归属、kind 与 payload 边界都在那里重判。
func (h *Host) EventEmitRequest(parent *protocolwire.RequestContext, input EventEmitInput) (*hostwire.CommandRequest, error) {
	if h == nil || h.Commands == nil {
		return nil, ErrHostEventEmitInvalid
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.TrimSpace(input.Kind)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Name == "" || len(input.Name) > HostEventsEmitMaxNameLen || strings.ContainsAny(input.Name, " \t\n") {
		return nil, ErrHostEventEmitInvalid
	}
	switch input.Kind {
	case HostEventsEmitKindObserve, HostEventsEmitKindFilter:
	default:
		return nil, ErrHostEventEmitInvalid
	}
	if input.Payload == nil || len(input.Payload) > HostEventsEmitMaxPayloadKeys {
		return nil, ErrHostEventEmitInvalid
	}
	if !validEventIdempotencyKey(input.IdempotencyKey) {
		return nil, ErrHostEventEmitInvalid
	}
	body, err := json.Marshal(input.Payload)
	if err != nil || len(body) > HostEventsEmitMaxPayloadBytes {
		return nil, ErrHostEventEmitInvalid
	}
	document, err := NewTypedDocument(HostEventsEmitInputSchemaRef, map[string]any{
		"name": input.Name, "kind": input.Kind, "payload": input.Payload,
	})
	if err != nil {
		return nil, ErrHostEventEmitInvalid
	}
	requestContext := h.RequestContext(parent)
	if requestContext == nil {
		return nil, ErrHostEventEmitInvalid
	}
	requestContext.IdempotencyKey = input.IdempotencyKey
	return &hostwire.CommandRequest{
		Context: requestContext, CommandId: HostEventsEmitCommandID,
		CommandVersion: HostEventsEmitCommandVersion,
		IdempotencyKey: input.IdempotencyKey, Input: document,
	}, nil
}

// EmitEvent 通过 runtime 作用域的 Host broker 执行 sforum.events.emit@1。
// 被拒绝的命令与其它命令族一样，以正常的 CommandResult 返回，原因码在
// ErrorDetail 里（events.namespace_forbidden / events.payload_invalid /
// events.kind_invalid / events.publisher_unavailable）。
//
// 调用前请确认本插件声明了 events.emit 能力与 host_commands 数据库授权。
func (h *Host) EmitEvent(ctx context.Context, parent *protocolwire.RequestContext, input EventEmitInput) (*hostwire.CommandResult, error) {
	request, err := h.EventEmitRequest(parent, input)
	if err != nil {
		return nil, err
	}
	return h.Commands.Execute(ctx, request)
}

// EventEmitResultValues 读取结果文档。计数是十进制字符串：protobuf Struct 只有
// double，放大整数会丢精度，因此 Host 把计数编码成字符串。
func EventEmitResultValues(result *hostwire.CommandResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	return TypedDocumentValues(result.GetOutput())
}

// EventEmitDelivery 是投递计数的解析结果。解析失败或字段缺失时返回 zero
// 与 false，调用方据此判断「命令成功但结果文档不可读」。
func EventEmitDelivery(result *hostwire.CommandResult) (delivered int, failed int, ok bool) {
	values := EventEmitResultValues(result)
	delivered, deliveredOK := decimalStringField(values, "deliveredCount")
	failed, failedOK := decimalStringField(values, "failedCount")
	if !deliveredOK || !failedOK {
		return 0, 0, false
	}
	return delivered, failed, true
}

func validEventIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > HostEventsEmitMaxIdempotency {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func decimalStringField(values map[string]any, key string) (int, bool) {
	text, ok := values[key].(string)
	if !ok {
		return 0, false
	}
	parsed := 0
	if text == "" {
		return 0, false
	}
	for index := 0; index < len(text); index++ {
		if text[index] < '0' || text[index] > '9' {
			return 0, false
		}
		parsed = parsed*10 + int(text[index]-'0')
	}
	return parsed, true
}
