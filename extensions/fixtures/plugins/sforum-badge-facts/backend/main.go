// Package main 是「事件转事实」夹具后端（M3 活样本，不是产品插件）。
//
// 它演示 C 路径（事件 → 事实计数）的等价实现，**不新增 core 事件订阅总线**：
//
//  1. 清单 `events[]` 声明一个 observe 观察者，监听 core 的 `reaction.created`；
//  2. observe handler 从事件负载取真实主体 `actorUserId`（不是调用方扩展身份）；
//  3. 通过 Host 命令 `badges.fact.record`（SDK `RecordBadgeFact`）上报计数，
//     `dedupeKey` 直接用投递 id —— 重复投递命中唯一索引，回执 `inserted:false`
//     且 `total` 不变，那**不是错误**；
//  4. factKey 必须是清单 `badgeConditions[].key` 里声明过的那个，否则 Host 拒绝
//     （`fact_key_not_declared`）。
//
// 能力边界：清单声明 `capabilities: ["badges.fact.write"]` 与
// `database.grants: ["host_commands"]`；插件没有核心库连接，写入只有一个入口。
package main

import (
	"context"
	"strconv"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	extensionID = "sforum.badge-facts"

	// 与清单 events[0] 逐字一致；Host 按 handler id 找入口。
	observeHandlerID = extensionID + ".observe"
	observeEventID   = extensionID + ".event.reaction-created"
	observeEventName = "reaction.created"

	// 与清单 badgeConditions[0].key 逐字一致：Host 只接受已声明的事实键。
	reactionsFactKey = extensionID + ".reactions.given"

	resultSchemaRef = extensionID + ".reaction-created.result@1"
)

func main() {
	pluginv2.Serve(&badgeFactsPlugin{Server: pluginv2.NewServer()})
}

type badgeFactsPlugin struct{ *pluginv2.Server }

// InvokeHook 是 observe 观察者的唯一入口。
func (p *badgeFactsPlugin) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	response := &pluginwire.HookResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(),
			Extension: request.GetContext().GetExtension(),
		},
		Accepted: false,
	}
	if request.GetHookId() != observeHandlerID || request.GetHookName() != observeEventName {
		return hookFailure(response, protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			"badge_facts.hook_not_found", "This plugin only observes reaction.created."), nil
	}
	// 运行时身份由握手绑定：Health 不通过就说明制品已过期，先拒绝再说。
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}

	payload := pluginv2.TypedDocumentValues(request.GetPayload())
	userID, ok := payloadUserID(payload["actorUserId"])
	if !ok {
		return hookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"badge_facts.user_required", "reaction.created must carry actorUserId."), nil
	}
	host, err := p.Host()
	if err != nil {
		return hookFailure(response, protocolwire.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"badge_facts.host_unavailable", "Host broker is not available."), nil
	}
	// 幂等键 = 投递 id：同一事件重投只记一次；没有投递 id 时留空（两次真实发生
	// 就是两次累加，比塞一个随机键更诚实）。
	result, err := host.RecordBadgeFact(ctx, request.GetContext(), pluginv2.BadgeFactInput{
		UserID:     userID,
		FactKey:    reactionsFactKey,
		Delta:      1,
		OccurredAt: time.Now().UTC(),
		DedupeKey:  request.GetDeliveryId(),
	})
	if err != nil {
		return hookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"badge_facts.record_failed", err.Error()), nil
	}
	if commandError := result.GetError(); commandError != nil {
		// 被 Host 拒绝（能力缺失、事实键未声明、参数非法）按原样上抛，
		// 不吞成 accepted：观察者失败应该是可见的。
		response.Error = commandError
		return response, nil
	}

	inserted, total, readable := pluginv2.BadgeFactRecordResult(result)
	values := map[string]any{"inserted": inserted}
	if readable {
		// total 回执是十进制字符串，SDK 已解析成 int64。
		values["total"] = strconv.FormatInt(total, 10)
	}
	document, err := pluginv2.NewTypedDocument(resultSchemaRef, values)
	if err != nil {
		return hookFailure(response, protocolwire.ErrorCode_ERROR_CODE_INTERNAL,
			"badge_facts.result_schema_mismatch", err.Error()), nil
	}
	response.Accepted = true
	response.Result = document
	return response, nil
}

// payloadUserID 兼容两种编码：JSON 数字（float64）与十进制字符串。
// Host 事实写入以 int64 为口径，这里只做一次窄化，非法主体一律判失败。
func payloadUserID(raw any) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		if value <= 0 || value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func hookFailure(response *pluginwire.HookResponse, code protocolwire.ErrorCode, reason, detail string) *pluginwire.HookResponse {
	response.Accepted = false
	response.Error = &protocolwire.ErrorDetail{Code: code, Reason: reason, Message: detail}
	return response
}
