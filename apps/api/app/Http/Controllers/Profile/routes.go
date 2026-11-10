package profilecontroller

import "github.com/gofiber/fiber/v3"

func (h *Controller) RegisterRoutes(api fiber.Router) {
	// 公开资料包含主题摘要，读取边界跟随 forum.guest.read。
	api.Get("/profiles/:username", h.publicProfile)
	// 公开活动分页（主题 / 回复 tab 加载更多）。
	api.Get("/profiles/:username/activities", h.publicActivities)
	// 公开收藏名单：他人只出公开行，本人视角出全部（缓存头在 controller 里区分）。
	api.Get("/profiles/:username/bookmarks", h.publicBookmarks)
	// 当前用户资料读写：登录后只能操作自己。
	api.Get("/profile", h.myProfile)
	// 本人收藏名单（含私密行）；必须登录。
	api.Get("/profile/bookmarks", h.myBookmarks)
	api.Put("/profile", h.updateMyProfile)
	// 改名预检：只读，回答可用性/重名数与冲突原因。
	api.Get("/profile/nickname/availability", h.nicknameAvailability)
	api.Post("/profile/avatar", h.uploadAvatar)
	api.Delete("/profile/avatar", h.deleteAvatar)
}
