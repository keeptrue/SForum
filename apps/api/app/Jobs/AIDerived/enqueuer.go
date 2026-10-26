package aiderivedjobs

import (
	"context"
	"fmt"
	"strings"

	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// Enqueuer 实现 aiderived.Enqueuer：把一次手动派生请求放进持久队列。
//
// 它放在任务包里而不是领域包里，是为了让依赖保持单向——领域包不认识队列
// （与 aireplyjobs.Enqueuer 同一条约定）。它只入队，不执行任何模型调用：执行
// 在 worker 侧，因此触发端不需要网关、密钥或出站策略。
type Enqueuer struct {
	dispatcher *supportjobs.Dispatcher
}

func NewEnqueuer(dispatcher *supportjobs.Dispatcher) *Enqueuer {
	return &Enqueuer{dispatcher: dispatcher}
}

// EnqueueDerive 实现 aiderived.Enqueuer。
//
// requestID 为空时拒绝入队而不是就地补一个：触发标识决定「同一次触发只产出一份
// 草稿」，在入队处悄悄生成会让「重复投递是否被去重」变得无法验证。服务层
// （aiderived.Service.Enqueue）保证它非空。
func (e *Enqueuer) EnqueueDerive(ctx context.Context, input aiderived.EnqueueInput) (string, error) {
	if e == nil || e.dispatcher == nil {
		return "", aiderived.ErrGeneratorUnavailable
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return "", aiderived.ErrArtifactInvalid
	}
	// 入队必须用**具体 kind 的 args 类型**：River 按 args 类型决定 kind，用一个
	// 带条件 Kind() 的结构会只注册出其中一个 kind（见 DigestArgs 的说明）。
	switch input.Purpose {
	case aiderived.PurposeDigest:
		args := DigestArgs{TopicID: input.TopicID, RequestedBy: input.RequestedBy, RequestID: requestID}
		if _, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
			return "", err
		}
	case aiderived.PurposeTour:
		args := TourArgs{TopicID: input.TopicID, RequestedBy: input.RequestedBy, RequestID: requestID}
		if _, err := e.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
			return "", err
		}
	default:
		// 未声明的用途在入队前就拒绝：否则它会变成一个永远没有 worker 的 job
		// 行，运营者只看到「一直在等」。
		return "", fmt.Errorf("%w: unsupported purpose %q", aiderived.ErrArtifactInvalid, input.Purpose)
	}
	return requestID, nil
}
