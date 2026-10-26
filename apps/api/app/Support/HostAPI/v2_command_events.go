package hostapi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 插件发布自身事件的边界。
const (
	CommandEventsEmitID            = "sforum.events.emit"
	CommandEventsEmitVersion       = "1"
	CommandEventsEmitInputSchema   = "sforum.events.emit.input"
	CommandEventsEmitOutputSchema  = "sforum.events.emit.result"
	CommandEventsEmitSchemaVersion = "1"

	// ProtocolV2EventsEmitMaxNameLen 与 Manifest 侧的事件名约束保持同一量级。
	ProtocolV2EventsEmitMaxNameLen      = 160
	ProtocolV2EventsEmitMaxPayloadBytes = 64 * 1024
	// ProtocolV2EventsEmitMaxPayloadKeys 防止一个巨大扁平对象把订阅方的
	// hook input 与 delivery 记录一起撑大。
	ProtocolV2EventsEmitMaxPayloadKeys = 64
)

// ProtocolV2EventPublisher 是命令与扩展运行时事件总线之间的窄接口。
// 它刻意只有一个方法：命令不需要知道投递、重试与 delivery 记录的任何细节。
type ProtocolV2EventPublisher interface {
	EmitFromExtension(ctx context.Context, extensionID string, envelope appevents.Envelope) appevents.Result
}

type protocolV2EventsEmitInput struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

// newProtocolV2EventsEmitCommandDefinition 让插件发布自己命名空间下的事件。
//
// Manifest V3 早就允许插件声明自定义事件名（以 <extensionId>. 开头即可通过
// knownOrNamespacedContract），但运行期没有发布入口：Emit 只认 Core 目录里的名字。
// 这条命令补上入口，并把命名空间所有权钉死在运行期身份上 —— 名字前缀必须是
// 已验证的 runtime extension id，而不是请求里自称的值。
func newProtocolV2EventsEmitCommandDefinition(publisher ProtocolV2EventPublisher) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandEventsEmitID, Version: CommandEventsEmitVersion,
		InputSchemaID: CommandEventsEmitInputSchema, InputSchemaVersion: CommandEventsEmitSchemaVersion,
		OutputSchemaID: CommandEventsEmitOutputSchema, OutputSchemaVersion: CommandEventsEmitSchemaVersion,
		ActorMode:          protocolV2CommandActorService,
		RequiredCapability: capabilities.EventsEmit,
	}
	// Preview 只给数量与名称，不带 payload 内容：Plan 结果会进入 UI 与审计，
	// 社区内容不该借它外泄。
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2EventsEmitInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2EventsEmitPreparation(input, 0, 0, true)
	}
	// Prepare 是权威校验阶段：身份、命名空间与 kind 都在这里定死。
	// 事件发布没有业务写入需要事务保护，private 只承载校验结果。
	definition.Prepare = func(ctx context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2EventsEmitInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if runtime == nil {
			return nil, protocolV2EventsEmitError("events.publisher_unavailable")
		}
		extensionID := strings.TrimSpace(runtime.GetExtensionId())
		// 请求里自称的扩展 ID 必须与已验证的 runtime 身份一致。
		if request == nil || request.GetContext().GetExtension().GetExtensionId() != extensionID {
			return nil, protocolV2EventsEmitError("events.publisher_unavailable")
		}
		// 命名空间所有权：插件只能发自己前缀的事件。这一条在命令层再判一次，
		// 不依赖事件层 —— 两者是两道独立的门。
		if !appevents.OwnsEventName(extensionID, input.Name) {
			return nil, protocolV2EventsEmitError("events.namespace_forbidden")
		}
		if publisher == nil {
			return nil, protocolV2EventsEmitError("events.publisher_unavailable")
		}
		prepared := &protocolV2PreparedEventsEmit{input: input, extensionID: extensionID}
		// Prepare 的产物同样会被执行路径当作计划校验（Policy + ProjectedResult），
		// 因此它必须是完整准备，而不是只带 private 的壳：否则真实调用会在
		// 「prepare 后重新 finalize」这一步被拒为 schema/policy 不匹配。
		preparation, err := protocolV2EventsEmitPreparation(input, 0, 0, false)
		if err != nil {
			return nil, err
		}
		preparation.private = prepared
		return preparation, nil
	}
	definition.Execute = func(ctx context.Context, _ pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		prepared, ok := preparation.private.(*protocolV2PreparedEventsEmit)
		if !ok || prepared == nil {
			return nil, protocolV2EventsEmitError("events.payload_invalid")
		}
		result := publisher.EmitFromExtension(ctx, prepared.extensionID, appevents.Envelope{
			Name:    prepared.input.Name,
			Kind:    prepared.input.Kind,
			Payload: prepared.input.Payload,
		})
		if !result.OK {
			return nil, protocolV2EventsEmitError(result.Reason)
		}
		output, err := protocolV2EventsEmitDocument(prepared.input.Name, 1, 0, false)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandExecution{Output: output}, nil
	}
	return definition
}

// protocolV2PreparedEventsEmit 承载 Prepare 阶段校验过的输入，避免 Execute
// 重新解析一个可能被改过的请求。
type protocolV2PreparedEventsEmit struct {
	input       protocolV2EventsEmitInput
	extensionID string
}

func protocolV2EventsEmitInputFromRequest(request *hostv2.CommandRequest) (protocolV2EventsEmitInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2EventsEmitInput](request)
	if err != nil {
		return input, protocolV2EventsEmitError("events.payload_invalid")
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Name == "" || len(input.Name) > ProtocolV2EventsEmitMaxNameLen || input.Payload == nil {
		return input, protocolV2EventsEmitError("events.payload_invalid")
	}
	// 自定义事件只允许 observe 与 filter：validate 是宿主同步校验语义，
	// 插件自行发布一个「校验」事件没有宿主写路径可依附。
	if input.Kind != appevents.KindObserve && input.Kind != appevents.KindFilter {
		return input, protocolV2EventsEmitError("events.kind_invalid")
	}
	body, err := json.Marshal(input.Payload)
	if err != nil || len(body) > ProtocolV2EventsEmitMaxPayloadBytes ||
		len(input.Payload) > ProtocolV2EventsEmitMaxPayloadKeys {
		return input, protocolV2EventsEmitError("events.payload_invalid")
	}
	return input, nil
}

func protocolV2EventsEmitDocument(name string, delivered, failed int, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandEventsEmitOutputSchema, CommandEventsEmitSchemaVersion, map[string]any{
		"planned": planned, "name": name,
		"deliveredCount": strconv.Itoa(delivered), "failedCount": strconv.Itoa(failed),
	})
}

// protocolV2EventsEmitPreparation 供 Preview 使用：只暴露名称与计数，
// 不把 payload 内容放进计划结果。
func protocolV2EventsEmitPreparation(input protocolV2EventsEmitInput, delivered, failed int, planned bool) (*protocolV2CommandPreparation, error) {
	document, err := protocolV2EventsEmitDocument(input.Name, delivered, failed, planned)
	if err != nil {
		return nil, err
	}
	impact := []*hostv2.ImpactItem{{
		Module: "events", Action: "publish", ResourceType: "events.emit",
		ResourceId: input.Name, Summary: "publish " + input.Kind + " event to declared listeners",
		Reversible: false,
	}}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: CommandEventsEmitID + "@" + CommandEventsEmitVersion, ResourceId: input.Name, Allowed: true,
			Reason: "The Host verifies that the event name belongs to the calling extension's own namespace, then publishes it to Host-validated listeners only.",
		}},
		ProjectedResult: document,
		Impact:          impact,
	}, nil
}

func protocolV2EventsEmitError(reason string) error {
	code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	message := "The event emission request is invalid."
	retryable := false
	switch reason {
	case "events.namespace_forbidden":
		code, message = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"The event name must start with the calling extension's own namespace."
	case "events.kind_invalid":
		message = "The event kind must be observe or filter."
	case "events.publisher_unavailable":
		code, message = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"The Host event publisher is not available for this exact runtime."
	case "extension.event_unknown":
		code, message = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "The event is not published by this extension."
	}
	return newProtocolV2CommandError(code, reason, message, retryable)
}
