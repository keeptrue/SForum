package aireply

import (
	"context"
	"log/slog"

	appevents "github.com/zhuchunshu/sforum/apps/api/app/Support/Events"
)

// Subscriber 包装事件发布者：原样转发事件，并额外把 comment.created 交给回复
// 判定。它不改变发布结果，判定失败也不会影响订阅方的返回值。
type Subscriber struct {
	inner   appevents.Publisher
	trigger *Trigger
}

func NewSubscriber(inner appevents.Publisher, trigger *Trigger) *Subscriber {
	return &Subscriber{inner: appevents.EnsurePublisher(inner), trigger: trigger}
}

func (s *Subscriber) Emit(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	result := s.inner.Emit(ctx, envelope)
	if s == nil || s.trigger == nil {
		return result
	}
	if envelope.Kind != appevents.KindObserve {
		return result
	}
	switch envelope.Name {
	case appevents.CommentCreated:
		// 「AI 为什么没回复」是运维会问的第一个问题。这条日志把「事件到了」
		// 与「判定跳过了」分开，否则两种失败在外部看起来完全一样。
		slog.InfoContext(ctx, "aireply: comment.created reached the reply trigger",
			"commentId", envelope.Payload["commentId"],
			"topicId", envelope.Payload["topicId"],
			"authorUserId", envelope.Payload["authorUserId"])
		s.trigger.CommentCreated(ctx, envelope)
	case appevents.TopicCreated:
		slog.InfoContext(ctx, "aireply: topic.created reached the reply trigger",
			"topicId", envelope.Payload["topicId"],
			"authorUserId", envelope.Payload["authorUserId"])
		s.trigger.TopicCreated(ctx, envelope)
	}
	return result
}
