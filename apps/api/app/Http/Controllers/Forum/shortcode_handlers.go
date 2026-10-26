package forumcontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// 短代码相关的公开读端点集中在独立文件，避免 controller.go 超过架构
// 1000 行上限。引用选择器、可编辑源码读取与受保护内容响应头均为
// 短代码系统的 Host 侧入口。

func (h *Controller) composerReferences(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.referenceSelector.ListReferenceOptions(c.Context(), actor, forum.ReferenceSelectorInput{
		Kind:       c.Query("kind"),
		Query:      c.Query("query"),
		SelectedID: int64(queryInt(c, "selectedId")),
		Limit:      queryInt(c, "limit"),
	})
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) topicEditSource(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	source, err := h.editableSources.GetTopicEditSource(c.Context(), actor, int64(paramInt(c, "topicID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, source)
}

func (h *Controller) commentEditSource(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	source, err := h.editableSources.GetCommentEditSource(c.Context(), actor, int64(paramInt(c, "commentID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, source)
}

func setProtectedContentResponse(c fiber.Ctx) {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Vary(fiber.HeaderCookie, fiber.HeaderAuthorization, fiber.HeaderAcceptLanguage)
}
