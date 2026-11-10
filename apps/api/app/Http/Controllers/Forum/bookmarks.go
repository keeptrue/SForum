package forumcontroller

import (
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// 收藏（bookmarks）HTTP 层：4 个切换端点。写端点要求登录；错误映射走
// error_mapping.go（403 forum.bookmarks_disabled / 404 forum.bookmark_target_invalid）。

func (h *Controller) bookmarkTopic(c fiber.Ctx) error {
	return h.applyTopicBookmark(c, true)
}

func (h *Controller) unbookmarkTopic(c fiber.Ctx) error {
	return h.applyTopicBookmark(c, false)
}

func (h *Controller) bookmarkComment(c fiber.Ctx) error {
	return h.applyCommentBookmark(c, true)
}

func (h *Controller) unbookmarkComment(c fiber.Ctx) error {
	return h.applyCommentBookmark(c, false)
}

type bookmarkRequestBody struct {
	Visibility string     `json:"visibility,omitempty"`
	RemindAt   *time.Time `json:"remindAt,omitempty"`
	// ClearReminder 是三态提醒里的「清除」分支：remindAt:null 与「没给」在
	// JSON 里无法区分，所以必须有一个显式布尔位，否则清除永远写不掉旧值。
	ClearReminder bool `json:"clearReminder,omitempty"`
}

func (h *Controller) applyTopicBookmark(c fiber.Ctx, add bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	bookmarks, err := h.bookmarkService()
	if err != nil {
		return err
	}
	input, err := decodeBookmarkBody(c, add)
	if err != nil {
		return err
	}
	var state forum.BookmarkState
	if add {
		state, err = bookmarks.BookmarkTopic(c.Context(), actor, int64(paramInt(c, "topicID")), input)
	} else {
		state, err = bookmarks.UnbookmarkTopic(c.Context(), actor, int64(paramInt(c, "topicID")))
	}
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, state)
}

func (h *Controller) applyCommentBookmark(c fiber.Ctx, add bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	bookmarks, err := h.bookmarkService()
	if err != nil {
		return err
	}
	input, err := decodeBookmarkBody(c, add)
	if err != nil {
		return err
	}
	var state forum.BookmarkState
	if add {
		state, err = bookmarks.BookmarkComment(c.Context(), actor, int64(paramInt(c, "commentID")), input)
	} else {
		state, err = bookmarks.UnbookmarkComment(c.Context(), actor, int64(paramInt(c, "commentID")))
	}
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, state)
}

// decodeBookmarkBody 解析可选请求体；PUT 空体合法（仅补行）。
//
// DELETE 不读 body：删除语义只看「行是否存在」，visibility/remindAt 对它没有
// 任何含义，带 body 的 DELETE 只可能是客户端误用。不解析就不存在「畸形 body
// 的 DELETE 报 422」——契约里 DELETE 也就不必声明 422。
func decodeBookmarkBody(c fiber.Ctx, add bool) (forum.BookmarkWriteInput, error) {
	input := forum.BookmarkWriteInput{}
	if !add {
		return input, nil
	}
	if c.Request().Body() == nil || len(c.Request().Body()) == 0 {
		return input, nil
	}
	var body bookmarkRequestBody
	if err := c.Bind().Body(&body); err != nil {
		return input, fiber.NewError(fiber.StatusUnprocessableEntity, "forum.bookmark_body_invalid")
	}
	input.Visibility = body.Visibility
	input.RemindAt = body.RemindAt
	input.ClearReminder = body.ClearReminder
	return input, nil
}

// bookmarkService 返回已装配的协作者；未装配时以 503 明确失败，而不是静默 200。
func (h *Controller) bookmarkService() (*forum.BookmarkService, error) {
	if h.bookmarks == nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "forum.bookmarks_unavailable")
	}
	return h.bookmarks, nil
}
