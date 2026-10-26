package webhooks

import (
	"context"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// BridgePublisher 在 extension runtime 发事件后扇出 webhook（不改动插件投递语义）。
type BridgePublisher struct {
	Inner  appevents.Publisher
	Fanout *Service
}

func (b BridgePublisher) Emit(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	result := appevents.Result{OK: true}
	if b.Inner != nil {
		result = b.Inner.Emit(ctx, envelope)
	}
	// 仅在 observe 成功路径扇出；filter 拒绝时不投递 webhook。
	if result.OK && b.Fanout != nil {
		// 异步扇出失败不得影响业务 Result。
		b.Fanout.Fanout(ctx, envelope)
	}
	return result
}
