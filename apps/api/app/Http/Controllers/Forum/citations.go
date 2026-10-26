package forumcontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
)

// commentCitations 返回一条评论作为 AI 产物时留下的依据。
//
// 它与 GET /comments/:commentID/replies 同族：同一资源、同一套可见性判定、
// 同样的 guest-read 门与 OptionalActor 解析。三条纪律：
//   - 只读，不写任何东西；
//   - 可见性完全由服务层决定（评论摘要 → 主题公开可见），控制器不做二次判断；
//   - 零引用返回 `{items: []}` 而不是 404——「这条评论没有依据」是正常结论。
//
// 这里解析 viewer 是为了与 replies 保持一致的会话语义（无效会话在这里就该被
// 拒绝，而不是等到别处），但它不参与引用可见性——见 CitationService.
// CommentCitations 的注释。
func (h *Controller) commentCitations(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	if _, err := apphttp.OptionalActor(c, h.sessions, h.users); err != nil {
		return err
	}
	list, err := h.citations.CommentCitations(c.Context(), int64(paramInt(c, "commentID")))
	if err != nil {
		return mapForumError(err)
	}
	return apphttp.OK(c, list)
}
