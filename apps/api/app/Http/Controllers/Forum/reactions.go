package forumcontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// 点赞（reactions）HTTP 层：4 个切换端点 + 2 个公开名单端点。
// 写端点要求登录；名单端点与公开读同口径（guest.read 策略、主题可见性）。

func (h *Controller) likeTopic(c fiber.Ctx) error {
	return h.applyTopicReaction(c, true)
}

func (h *Controller) unlikeTopic(c fiber.Ctx) error {
	return h.applyTopicReaction(c, false)
}

func (h *Controller) likeComment(c fiber.Ctx) error {
	return h.applyCommentReaction(c, true)
}

func (h *Controller) unlikeComment(c fiber.Ctx) error {
	return h.applyCommentReaction(c, false)
}

func (h *Controller) topicLikes(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	reactions, err := h.reactionService()
	if err != nil {
		return err
	}
	roster, err := reactions.ListTopicLikes(c.Context(), int64(paramInt(c, "topicID")), queryInt(c, "limit"), queryInt(c, "offset"))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, roster)
}

func (h *Controller) commentLikes(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	reactions, err := h.reactionService()
	if err != nil {
		return err
	}
	roster, err := reactions.ListCommentLikes(c.Context(), int64(paramInt(c, "commentID")), queryInt(c, "limit"), queryInt(c, "offset"))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, roster)
}

func (h *Controller) applyTopicReaction(c fiber.Ctx, add bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	reactions, err := h.reactionService()
	if err != nil {
		return err
	}
	var state forum.ReactionState
	if add {
		state, err = reactions.LikeTopic(c.Context(), actor, int64(paramInt(c, "topicID")))
	} else {
		state, err = reactions.UnlikeTopic(c.Context(), actor, int64(paramInt(c, "topicID")))
	}
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, state)
}

func (h *Controller) applyCommentReaction(c fiber.Ctx, add bool) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	reactions, err := h.reactionService()
	if err != nil {
		return err
	}
	var state forum.ReactionState
	if add {
		state, err = reactions.LikeComment(c.Context(), actor, int64(paramInt(c, "commentID")))
	} else {
		state, err = reactions.UnlikeComment(c.Context(), actor, int64(paramInt(c, "commentID")))
	}
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, state)
}

// reactionService 返回已装配的协作者；未装配时以 503 明确失败，而不是静默 200。
func (h *Controller) reactionService() (*forum.ReactionService, error) {
	if h.reactions == nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "forum.reactions_unavailable")
	}
	return h.reactions, nil
}
