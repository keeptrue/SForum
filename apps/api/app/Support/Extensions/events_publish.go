package extensionsruntime

import (
	"context"
	"time"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// normalizeEnvelopeAs 为插件发起的信封补齐宿主权威的默认值：ID、关联 ID 与
// 发生时间都以宿主时钟为准，不让插件伪造投递标识。已携带的值原样保留，
// 便于调用方串联自己的关联链。
func normalizeEnvelopeAs(envelope appevents.Envelope, extensionID string) appevents.Envelope {
	_ = extensionID // 命名空间校验由 ResolveDefinition 承担；这里只补投递默认值。
	if envelope.ID == "" {
		envelope.ID = appevents.NewID()
	}
	if envelope.CorrelationID == "" {
		envelope.CorrelationID = appevents.NewID()
	}
	if envelope.OccurredAt.IsZero() {
		envelope.OccurredAt = time.Now().UTC()
	}
	return envelope
}

// ExtensionEventPublisher 是 Manager 对外暴露的事件发布协作者。
//
// 它从 manager.go 拆出来是为了守住两条架构基线：Manager 的接收者方法数与
// RuntimeEventsProviders 的方法数都是不可增长的 ratchet。事件发布是独立职责，
// 放在这里比继续往 Manager 上堆方法更合适。
type ExtensionEventPublisher struct {
	providers *RuntimeEventsProviders
}

// NewExtensionEventPublisher 绑定 Manager 拥有的事件 providers。
// providers 为 nil 时发布一律报告不可用，而不是静默成功。
//
// 刻意给 Manager 加一个包内取值器而不是新增导出方法：Manager 与
// RuntimeEventsProviders 的接收者方法数都是不可增长的架构基线。
func NewExtensionEventPublisher(manager *Manager) *ExtensionEventPublisher {
	if manager == nil {
		return &ExtensionEventPublisher{}
	}
	return &ExtensionEventPublisher{providers: manager.eventsProviders}
}

// EmitFromExtension 以调用方身份发布事件。extensionID 非空时，不在 Core 目录中、
// 但落在该扩展命名空间下的事件名同样被接受 —— 与 Manifest V3 的声明侧规则
// （knownOrNamespacedContract）保持一致，否则会出现「能声明、不能发布」的规则分裂。
func (p *ExtensionEventPublisher) EmitFromExtension(
	ctx context.Context,
	extensionID string,
	envelope appevents.Envelope,
) appevents.Result {
	if p == nil || p.providers == nil {
		return appevents.Result{OK: false, Reason: "extension.runtime_unavailable", Message: "Plugin runtime is not available."}
	}
	return emitExtensionEvent(ctx, p.providers, envelope, extensionID)
}

// emitExtensionEvent 是发布的实现。extensionID 为空表示宿主发起的 Core 事件，
// 非空表示插件发起、允许落在自己命名空间下的事件。
func emitExtensionEvent(
	ctx context.Context,
	providers *RuntimeEventsProviders,
	envelope appevents.Envelope,
	extensionID string,
) appevents.Result {
	if providers == nil {
		return appevents.Result{OK: false, Reason: "extension.runtime_unavailable", Message: "Plugin runtime is not available."}
	}
	envelope = normalizeEnvelopeAs(envelope, extensionID)
	definition, ok := appevents.ResolveDefinition(envelope.Name, extensionID)
	if !ok {
		// 命名空间事件被拒与「事件不存在」是同一个外部表现：不能借错误信息
		// 探测别的插件声明了哪些事件。
		return appevents.Result{OK: false, Reason: "extension.event_unknown", Message: "Unknown extension event."}
	}
	// 自定义事件的 kind 由发布方声明；缺省已在 ResolveDefinition 里回落为 observe。
	if envelope.Kind == "" {
		envelope.Kind = definition.Kind
	}
	if envelope.Kind != definition.Kind {
		return appevents.Result{OK: false, Reason: "extension.event_kind_invalid", Message: "Extension event kind does not match the event definition."}
	}
	if envelope.Kind != appevents.KindObserve && envelope.Kind != appevents.KindFilter {
		return appevents.Result{OK: false, Reason: "extension.event_kind_invalid", Message: "Extension event kind does not match the event definition."}
	}
	if envelope.Kind == appevents.KindObserve {
		return providers.emitObserve(ctx, envelope)
	}
	return providers.emitSync(ctx, envelope)
}
