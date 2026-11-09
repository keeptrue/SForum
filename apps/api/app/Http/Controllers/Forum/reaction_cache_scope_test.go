package forumcontroller

import (
	"fmt"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// TestTopicDetailMarksViewerScopedResponsePrivate 固定 P1-3：登录访客的详情响应
// 含 per-viewer 的 likedByMe，必须显式 private + Vary: Cookie；匿名响应不承载
// 该字段，不应被当成私有响应。
func TestTopicDetailMarksViewerScopedResponsePrivate(t *testing.T) {
	app, _, _ := newForumTestApp()

	guest := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, nil)
	defer guest.Body.Close()
	if guest.StatusCode != nethttp.StatusOK {
		t.Fatalf("guest status=%d", guest.StatusCode)
	}
	if strings.Contains(guest.Header.Get(fiber.HeaderCacheControl), "private") {
		t.Fatalf("anonymous topic detail must not be marked private: %q", guest.Header.Get(fiber.HeaderCacheControl))
	}

	cookie := loginForumUser(t, app, 1)
	loggedIn := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, cookie)
	defer loggedIn.Body.Close()
	if loggedIn.StatusCode != nethttp.StatusOK {
		t.Fatalf("logged-in status=%d", loggedIn.StatusCode)
	}
	if got := loggedIn.Header.Get(fiber.HeaderCacheControl); got != "private, no-store" {
		t.Fatalf("logged-in topic detail Cache-Control=%q", got)
	}
	if vary := loggedIn.Header.Get(fiber.HeaderVary); !strings.Contains(vary, fiber.HeaderCookie) {
		t.Fatalf("logged-in topic detail Vary=%q, want Cookie", vary)
	}
}

// TestMapForumErrorInvalidReactionTargetUsesDedicatedReason 固定 P2：点赞目标是
// 多态的，不能借用主题的 not-found reason；主题/评论各自的 not-found 保持原样。
func TestMapForumErrorInvalidReactionTargetUsesDedicatedReason(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		reason string
	}{
		{
			name:   "invalid reaction target",
			err:    fmt.Errorf("wrapped: %w", forum.ErrInvalidReactionTarget),
			status: fiber.StatusNotFound,
			reason: forum.CodeReactionTargetInvalid,
		},
		{
			name:   "topic not found",
			err:    forum.ErrTopicNotFound,
			status: fiber.StatusNotFound,
			reason: forum.CodeTopicNotFound,
		},
		{
			name:   "comment not found",
			err:    forum.ErrCommentNotFound,
			status: fiber.StatusNotFound,
			reason: forum.CodeCommentNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapForumError(test.err)
			fiberErr, ok := mapped.(*fiber.Error)
			if !ok || fiberErr.Code != test.status || fiberErr.Message != test.reason {
				t.Fatalf("mapped=%#v, want %d/%s", mapped, test.status, test.reason)
			}
		})
	}
}
