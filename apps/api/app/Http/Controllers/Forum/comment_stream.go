package forumcontroller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
)

// 评论实时信号节拍与生命周期。与通知流保持一致：心跳让中间代理不回收连接，
// 对账 tick 兜住 LISTEN 通知丢失/合并，寿命用来周期性回收长连接与重新校验可见性。
const (
	commentStreamMaxLifetime = time.Minute
	commentStreamHeartbeat   = 10 * time.Second
	commentStreamReconcile   = 15 * time.Second
)

// topicCommentRevision 返回公开评论区的修订状态，供前端对账与轮询降级使用。
// 与评论列表同口径：不可见主题统一 404，不泄漏存在性。
func (h *Controller) topicCommentRevision(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	if h.commentLive == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, forum.CodeCommentStreamUnavailable)
	}
	state, err := h.commentLive.RevisionState(c.Context(), int64(paramInt(c, "topicID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, state)
}

// topicCommentStream 推送该主题的评论修订变化（SSE）。
//
// 只推「变了」：载荷是修订号/评论数/末尾评论 id，不含正文、作者或任何会话相关投影，
// 因此公开读取也安全；正文与权限始终由 GET /topics/:topicID/comments 裁决。
func (h *Controller) topicCommentStream(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	if h.commentLive == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, forum.CodeCommentStreamUnavailable)
	}
	topicID := int64(paramInt(c, "topicID"))
	// 建流前先证明主题公开可见，并拿到可用于「已是最新」判定的初始状态。
	initial, err := h.commentLive.RevisionState(c.Context(), topicID)
	if err != nil {
		return mapForumError(err)
	}

	wakes, release, err := h.commentLive.Subscribe(topicID, strings.TrimSpace(clientip.FromCtx(c)))
	if mapped := commentStreamSubscribeError(err); mapped != nil {
		return mapped
	}
	if release == nil {
		release = func() {}
	}

	// Last-Event-ID 与 ?revision= 二选一；query 显式值优先，便于客户端首连携带本地状态。
	clientRevision, _ := strconv.ParseInt(c.Get("Last-Event-ID"), 10, 64)
	if queryRevision, parseErr := strconv.ParseInt(c.Query("revision"), 10, 64); parseErr == nil && queryRevision >= 0 {
		clientRevision = queryRevision
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-store, no-transform")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	baseContext := c.Context()
	serverDone := c.RequestCtx().Done()
	return c.SendStreamWriter(func(writer *bufio.Writer) {
		defer release()
		streamContext, cancel := context.WithTimeout(baseContext, commentStreamMaxLifetime)
		defer cancel()
		go func() {
			select {
			case <-serverDone:
				cancel()
			case <-streamContext.Done():
			}
		}()
		streamCommentRevisionEvents(streamContext, writer, h.commentLive, topicID, wakes, clientRevision, initial, commentStreamHeartbeat, commentStreamReconcile)
	})
}

// commentStreamSubscribeError 把唤醒源订阅结果映射为 HTTP 错误：
// 连接预算耗尽 → 429（客户端退化为轮询），没有唤醒源 → nil（仅靠对账 tick 推流）。
func commentStreamSubscribeError(err error) error {
	switch {
	case errors.Is(err, forum.ErrCommentRevisionConnectionLimit):
		return fiber.NewError(fiber.StatusTooManyRequests, forum.CodeCommentStreamLimited)
	case err != nil && !errors.Is(err, forum.ErrCommentRevisionWakeUnavailable):
		return err
	}
	return nil
}

func streamCommentRevisionEvents(
	ctx context.Context,
	writer *bufio.Writer,
	live *forum.CommentLiveService,
	topicID int64,
	wakes <-chan struct{},
	clientRevision int64,
	initial forum.CommentRevisionState,
	heartbeatEvery, reconcileEvery time.Duration,
) {
	last := initial.Revision
	if last != clientRevision && !writeCommentRevisionEvent(writer, initial) {
		return
	}
	reconcile := time.NewTicker(reconcileEvery)
	heartbeat := time.NewTicker(heartbeatEvery)
	defer reconcile.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakes:
		case <-reconcile.C:
		case <-heartbeat.C:
			if _, err := writer.WriteString(": heartbeat\n\n"); err != nil || writer.Flush() != nil {
				return
			}
			continue
		}
		current, err := live.RevisionState(ctx, topicID)
		if err != nil {
			// 主题被隐藏/删除（404）或读取失败：结束本次流，由客户端重连与对账处理。
			return
		}
		if current.Revision == last {
			continue
		}
		last = current.Revision
		if !writeCommentRevisionEvent(writer, current) {
			return
		}
	}
}

func writeCommentRevisionEvent(writer *bufio.Writer, state forum.CommentRevisionState) bool {
	payload, err := json.Marshal(state)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(writer, "id: %d\nevent: revision\ndata: %s\n\n", state.Revision, payload); err != nil {
		return false
	}
	return writer.Flush() == nil
}
