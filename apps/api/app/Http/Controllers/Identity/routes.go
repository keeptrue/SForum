package identitycontroller

import "github.com/gofiber/fiber/v3"

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/human-verification/challenge", h.humanVerificationChallenge)

	auth := api.Group("/auth")
	auth.Get("/registration-status", h.registrationStatus)
	auth.Post("/register", h.register)
	auth.Post("/login", h.login)
	auth.Post("/logout", h.logout)
	auth.Get("/session", h.session)
	auth.Put("/locale", h.updateCurrentUserLocale)
	auth.Put("/appearance", h.updateCurrentUserAppearance)
	auth.Delete("/appearance", h.clearCurrentUserAppearance)
	auth.Post("/password-reset/request", h.passwordResetRequest)
	auth.Post("/password-reset/confirm", h.passwordResetConfirm)
	auth.Post("/email-verification/request", h.emailVerificationRequest)
	auth.Get("/email-verification/confirm", h.emailVerificationConfirm)
	auth.Post("/email-verification/confirm", h.emailVerificationConfirm)
	// 外部 Identity 提供方：列表公开；start/complete 按操作决定是否要求登录。
	auth.Get("/providers", h.listAuthProviders)
	auth.Post("/providers/:providerId/:operation/start", h.authProviderStart)
	auth.Post("/providers/:providerId/:operation/complete", h.authProviderComplete)
	// 保留 Core 回调路由（OAuth 浏览器重定向回来）：已入 Core Route Catalog，
	// 但对 Route Registry 替换关闭（Host 独占 state/PKCE/会话权威）。
	// 见 plans/2026-07-27-github-social-login-builtin-plugin.md M1/T1E。
	auth.Get("/providers/:providerId/callback", h.externalAuthCallback)
	// 未绑定登录断言：公开 prepare 只返回可选终点；link 需要当前近期认证会话。
	auth.Post("/external-continuation/prepare", h.externalAuthContinuationPreparation)
	auth.Post("/external-continuation/link", h.externalAuthContinuationLink)
	// 外部注册：用一次性票据原子创建用户 + 默认角色 + link。
	auth.Post("/external-registration/prepare", h.externalRegistrationPreparation)
	auth.Post("/external-registration", h.externalRegistration)
	// 账号安全：已绑定身份列表与解绑（仅自服务）。
	auth.Get("/external-identities", h.externalIdentities)
	auth.Delete("/external-identities/:linkId", h.externalIdentityUnlink)
	// 自助添加/更改本地密码（external-only 首次设置；需 recent-auth）。
	auth.Post("/password", h.setupPassword)

	// 管理员 Login Methods 界面：identity.provider.manage。
	// 见 plans/2026-07-27-github-social-login-builtin-plugin.md M3。
	api.Get("/admin/identity/providers", h.adminListIdentityProviders)
	api.Patch("/admin/identity/providers/:providerId", h.adminPatchIdentityProvider)
	api.Post("/admin/identity/providers/:providerId/probe", h.adminProbeIdentityProvider)
	api.Post("/admin/identity/providers/reset", h.adminResetIdentityProviders)

	// 扩展资料分区：仅已登录自服务。
	api.Get("/profile/sections", h.listProfileSections)
	api.Put("/profile/sections/:sectionId", h.updateProfileSection)

	// 账号安全 / 登录设备管理：自服务，仅需已登录（auth.required），
	// 越权由 store 层 user_id 过滤保证。
	sessions := auth.Group("/sessions")
	sessions.Get("", h.listSessions)
	sessions.Post("/revoke-others", h.revokeOtherSessions)
	sessions.Delete("/:sessionId", h.revokeSession)

	// 个人访问令牌（F3.4）：仅 cookie 会话可管理；Bearer 不可创建/列出。
	tokens := auth.Group("/tokens")
	tokens.Get("", h.listAPITokens)
	tokens.Post("", h.createAPIToken)
	tokens.Delete("/:tokenID", h.revokeAPIToken)
	tokens.Post("/:tokenID/rotate", h.rotateAPIToken)

	// 邮件测试：管理员验证 SMTP 配置是否生效。
	api.Post("/admin/mail/test", h.adminMailTest)

	api.Get("/permissions", h.listPermissions)
	api.Get("/permissions/matrix", h.permissionMatrix)

	api.Get("/roles", h.listRoles)
	api.Post("/roles", h.createRole)
	api.Patch("/roles/:roleKey", h.updateRole)
	api.Delete("/roles/:roleKey", h.deleteRole)
	api.Put("/roles/:roleKey/permissions", h.replaceRolePermissions)

	api.Get("/users", h.listUsers)
	// 管理员直接创建可登录账号（user.manage；super_admin 授予另需超管）。
	api.Post("/users", h.createUser)
	api.Get("/users/:userID", h.getUser)
	// 管理员更新账户/资料字段（user.manage；封禁另需 user.ban）。
	api.Patch("/users/:userID", h.updateUser)
	api.Put("/users/:userID/email-verification", h.setUserEmailVerification)
	api.Put("/users/:userID/roles", h.replaceUserRoles)
	api.Put("/users/:userID/permission-overrides", h.replaceUserPermissionOverrides)
	// 管理员强制下线目标用户的全部设备（user.manage）。
	api.Post("/users/:userID/sessions/revoke", h.adminRevokeUserSessions)
	// 管理员直接设置目标用户密码（user.manage；改密后强制下线全部设备）。
	api.Post("/users/:userID/password", h.adminSetUserPassword)
	// 管理员清空目标用户相关真实 IP（user.manage；隐私合规）。
	api.Post("/users/:userID/client-ips/clear", h.adminClearUserClientIPs)

	// 插件只提交建议；角色授权必须由 role.manage 管理员通过浏览器会话显式审批。
	api.Get("/roles/suggestions", h.listRoleSuggestions)
	api.Post("/roles/suggestions/:suggestionID/decision", h.decideRoleSuggestion)
}
