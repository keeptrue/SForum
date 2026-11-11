package identity

const (
	RoleSuperAdmin = "super_admin"
	RoleMember     = "member"
	// 内置角色模板：系统角色、不可删除；权限集合可在后台调整（super_admin 除外）。
	RoleModerator = "moderator"
	RoleOperator  = "operator"
	RoleTechAdmin = "tech_admin"
	// RoleBuiltinAuto 是 Host 自动升级受保护内置插件时使用的系统角色。
	// 它只持有 extension.view / extension.plugin.manage，不授予后台访问。
	RoleBuiltinAuto = "builtin_auto"

	PermissionAdminAccess = "admin.access"
	PermissionRoleManage  = "role.manage"
	// PermissionUserView 只读用户列表/详情；不含改组与权限例外。
	PermissionUserView = "user.view"
	// PermissionUserManage 管理用户状态与用户组分配（不含个人权限例外）。
	PermissionUserManage = "user.manage"
	// PermissionUserPermissionOverride 编辑用户直接权限例外（高危，单独授予）。
	PermissionUserPermissionOverride = "user.permission_override"
	PermissionUserBan                = "user.ban"
	PermissionCategoryManage         = "category.manage"
	PermissionTagManage              = "tag.manage"
	// PermissionBadgeView 只读徽章目录与授予记录（后台徽章页的列表视图）。
	PermissionBadgeView = "badge.view"
	// PermissionBadgeManage 徽章 CRUD、手动授予/收回与全量重算。
	PermissionBadgeManage = "badge.manage"
	PermissionTopicCreate = "topic.create"
	// PermissionTopicEditOwn 作者编辑自己的主题（与回复的 post.edit_own 分离）。
	PermissionTopicEditOwn         = "topic.edit_own"
	PermissionTopicEditAny         = "topic.edit_any"
	PermissionTopicRevisionViewAny = "topic.revision.view_any"
	PermissionTopicDeleteOwn       = "topic.delete_own"
	PermissionTopicDeleteAny       = "topic.delete_any"
	PermissionTopicLock            = "topic.lock"
	PermissionTopicPin             = "topic.pin"
	PermissionPostCreate           = "post.create"
	PermissionPostEditOwn          = "post.edit_own"
	PermissionPostEditAny          = "post.edit_any"
	PermissionPostRevisionViewAny  = "post.revision.view_any"
	PermissionPostDeleteOwn        = "post.delete_own"
	PermissionPostDeleteAny        = "post.delete_any"
	PermissionModerationManage     = "moderation.manage"
	PermissionModerationReview     = "moderation.review"
	// PermissionModerationViewIP 查看内容/会话的真实客户端 IP（全文）；不含审核动作。
	PermissionModerationViewIP = "moderation.view_ip"
	// PermissionModerationReportReview 保留为源码兼容别名；数据库权限已迁移为 moderation.review。
	PermissionModerationReportReview = PermissionModerationReview
	// PermissionSettingsManage 为兼容父权限；细粒度见 settings.* / forum.settings.manage。
	PermissionSettingsManage               = "settings.manage"
	PermissionSettingsSiteManage           = "settings.site.manage"
	PermissionSettingsMailManage           = "settings.mail.manage"
	PermissionSettingsAvatarManage         = "settings.avatar.manage"
	PermissionSettingsAppearanceManage     = "settings.appearance.manage"
	PermissionSettingsNotificationsManage  = "settings.notifications.manage"
	PermissionForumSettingsManage          = "forum.settings.manage"
	PermissionSEOManage                    = "seo.manage"
	PermissionAttachmentUpload             = "attachment.upload"
	PermissionAttachmentManage             = "attachment.manage"
	PermissionAttachmentSettings           = "attachment.settings.manage"
	PermissionAttachmentUploadPolicyManage = "attachment.upload_policy.manage"
	// PermissionExtensionManage 为兼容父权限；细粒度见 extension.view/plugin/theme。
	PermissionExtensionManage       = "extension.manage"
	PermissionExtensionView         = "extension.view"
	PermissionExtensionPluginManage = "extension.plugin.manage"
	PermissionExtensionThemeManage  = "extension.theme.manage"
	PermissionDatabaseManage        = "database.manage"
	PermissionSearchManage          = "search.manage"
	PermissionJobsView              = "jobs.view"
	PermissionJobsManage            = "jobs.manage"
	// PermissionEntityMetaManage 管理实体自定义字段定义与 admin 可见元数据（F4.4）。
	PermissionEntityMetaManage = "entity_meta.manage"
	// PermissionIdentityProviderManage 管理 Host-owned 外部认证 provider 激活/排序/探测/重置。
	// 见 plans/2026-07-27-github-social-login-builtin-plugin.md M3。
	// 注：executable trust 仍为 super_admin-only，即便 provider 激活被委托。
	PermissionIdentityProviderManage = "identity.provider.manage"
	// PermissionAIManage 管理 AI 网关：供应商 profile、baseUrl 与密钥引用、
	// 配额与预算、失败姿态与自动裁决开关，以及用量与执行 trace 的查看。
	// 注：provider 密钥值本身只存在于 Secret Store，本权限不含读取明文。
	PermissionAIManage = "ai.manage"
	// PermissionTrustManage 管理信任等级阈值策略（后台写）。刻意没有对应的
	// trust.view：可达性依赖 admin.access 的读权限是死权限（D9）。
	PermissionTrustManage = "trust.manage"
)

type SeedPermission struct {
	Key         string
	Module      string
	Description string
}

var SeedPermissions = []SeedPermission{
	{Key: PermissionAdminAccess, Module: "admin", Description: "Access the admin area."},
	{Key: PermissionRoleManage, Module: "identity", Description: "Create and update roles and role permissions."},
	{Key: PermissionUserView, Module: "identity", Description: "View user accounts without changing assignments."},
	{Key: PermissionUserManage, Module: "identity", Description: "Manage user accounts and role assignments."},
	{Key: PermissionUserPermissionOverride, Module: "identity", Description: "Edit per-user permission allow and deny overrides."},
	{Key: PermissionUserBan, Module: "identity", Description: "Ban users from participating."},
	{Key: PermissionCategoryManage, Module: "forum", Description: "Create and update categories."},
	{Key: PermissionTagManage, Module: "forum", Description: "Create, approve, disable, and manage tags."},
	{Key: PermissionBadgeView, Module: "forum", Description: "View badges and badge grants."},
	{Key: PermissionBadgeManage, Module: "forum", Description: "Create, update, disable, and delete badges; grant and revoke manual badges; recompute top badges."},
	{Key: PermissionTopicCreate, Module: "forum", Description: "Create topics."},
	{Key: PermissionTopicEditOwn, Module: "forum", Description: "Edit own topics."},
	{Key: PermissionTopicEditAny, Module: "forum", Description: "Edit any topic."},
	{Key: PermissionTopicRevisionViewAny, Module: "forum", Description: "Inspect any topic revision history."},
	{Key: PermissionTopicDeleteOwn, Module: "forum", Description: "Delete own topics."},
	{Key: PermissionTopicDeleteAny, Module: "forum", Description: "Delete any topic."},
	{Key: PermissionTopicLock, Module: "forum", Description: "Lock or unlock topics."},
	{Key: PermissionTopicPin, Module: "forum", Description: "Pin or unpin topics."},
	{Key: PermissionPostCreate, Module: "forum", Description: "Create posts."},
	{Key: PermissionPostEditOwn, Module: "forum", Description: "Edit own replies."},
	{Key: PermissionPostEditAny, Module: "forum", Description: "Edit any post."},
	{Key: PermissionPostRevisionViewAny, Module: "forum", Description: "Inspect any comment revision history."},
	{Key: PermissionPostDeleteOwn, Module: "forum", Description: "Delete own replies."},
	{Key: PermissionPostDeleteAny, Module: "forum", Description: "Delete any post."},
	{Key: PermissionModerationManage, Module: "moderation", Description: "Manage moderation settings and audit history."},
	{Key: PermissionModerationReview, Module: "moderation", Description: "Review pending content and moderation reports."},
	{Key: PermissionModerationViewIP, Module: "moderation", Description: "View full client IP addresses on content and sessions for moderation."},
	{Key: PermissionSettingsManage, Module: "admin", Description: "Legacy parent: manage all non-SEO site settings groups."},
	{Key: PermissionSettingsSiteManage, Module: "admin", Description: "Manage core site identity, locale, verification, and security settings."},
	{Key: PermissionSettingsMailManage, Module: "admin", Description: "Manage mail providers, notification policy, and delivery tests."},
	{Key: PermissionSettingsAvatarManage, Module: "admin", Description: "Manage avatar upload and default avatar settings."},
	{Key: PermissionSettingsAppearanceManage, Module: "admin", Description: "Manage appearance theme and public chrome personalization."},
	{Key: PermissionSettingsNotificationsManage, Module: "admin", Description: "Manage notification policy, channels, and delivery health."},
	{Key: PermissionForumSettingsManage, Module: "forum", Description: "Manage forum runtime limits, reading, and behavior settings."},
	{Key: PermissionSEOManage, Module: "admin", Description: "Manage search engine optimization settings."},
	{Key: PermissionAttachmentUpload, Module: "attachment", Description: "Upload attachments."},
	{Key: PermissionAttachmentManage, Module: "attachment", Description: "Manage uploaded attachments."},
	{Key: PermissionAttachmentSettings, Module: "attachment", Description: "Manage attachment storage and upload settings."},
	{Key: PermissionAttachmentUploadPolicyManage, Module: "attachment", Description: "Manage per-role and per-user attachment upload size policies."},
	{Key: PermissionExtensionManage, Module: "extension", Description: "Legacy parent: manage all extension capabilities."},
	{Key: PermissionExtensionView, Module: "extension", Description: "View installed extensions, events, and contributions."},
	{Key: PermissionExtensionPluginManage, Module: "extension", Description: "Enable, disable, and configure plugins. Installing or enabling non-builtin plugins with a backend entry requires super_admin."},
	{Key: PermissionExtensionThemeManage, Module: "extension", Description: "Activate and manage themes."},
	{Key: PermissionDatabaseManage, Module: "admin", Description: "Browse database tables and rows."},
	{Key: PermissionSearchManage, Module: "search", Description: "Rebuild and manage the search index."},
	{Key: PermissionJobsView, Module: "jobs", Description: "View background jobs, queues, failures, and worker activity."},
	{Key: PermissionJobsManage, Module: "jobs", Description: "Retry, cancel, pause, and resume background job processing."},
	{Key: PermissionEntityMetaManage, Module: "admin", Description: "Manage entity custom field definitions and admin-visible meta values."},
	{Key: PermissionIdentityProviderManage, Module: "identity", Description: "Activate, order, probe, and reset Host-owned external auth providers (login/registration/link). Executable trust remains super_admin-only."},
	{Key: PermissionAIManage, Module: "ai", Description: "Manage the AI gateway: provider profiles, base URLs, credential references, quotas, budgets, failure posture, and usage audit."},
	{Key: PermissionTrustManage, Module: "identity", Description: "Update trust level thresholds, per-level limits, and earned capability mappings."},
}

// SeedRoleTemplate 是内置角色模板的源码权威定义。
// 数据库 seed 通过 migration 写入；测试与后台「应用模板」应复用此处权限包。
type SeedRoleTemplate struct {
	Key            string
	Alias          string
	Description    string
	PermissionKeys []string
}

// SeedMemberPermissions 默认注册用户组权限（与 identity_rbac + phase1 migration 对齐）。
var SeedMemberPermissions = []string{
	PermissionTopicCreate,
	PermissionTopicEditOwn,
	PermissionTopicDeleteOwn,
	PermissionPostCreate,
	PermissionPostEditOwn,
	PermissionPostDeleteOwn,
	PermissionAttachmentUpload,
}

// SeedRoleTemplates 内置岗位模板：版主 / 站点运营 / 技术管理。
// 不含 super_admin（全能受保护）与 member（默认注册组，权限更窄）。
var SeedRoleTemplates = []SeedRoleTemplate{
	{
		Key:         RoleModerator,
		Alias:       "版主",
		Description: "内容运营：审核、锁帖/置顶、编辑删除任意主题与回复，并可封禁用户。",
		PermissionKeys: []string{
			PermissionAdminAccess,
			PermissionModerationReview,
			PermissionModerationViewIP,
			PermissionTopicLock,
			PermissionTopicPin,
			PermissionTopicEditAny,
			PermissionTopicRevisionViewAny,
			PermissionTopicDeleteAny,
			PermissionPostEditAny,
			PermissionPostRevisionViewAny,
			PermissionPostDeleteAny,
			PermissionUserBan,
			// 只读：版主能看徽章目录与授予记录，不能改规则或手动授予。
			PermissionBadgeView,
		},
	},
	{
		Key:         RoleOperator,
		Alias:       "站点运营",
		Description: "站点日常运营：用户与内容结构、站点/邮件/外观等设置；不含技术发布与权限例外。",
		PermissionKeys: []string{
			PermissionAdminAccess,
			PermissionUserView,
			PermissionUserManage,
			// 有意不含 user.permission_override：个人权限例外留给超管或单独授权。
			PermissionSettingsSiteManage,
			PermissionSettingsMailManage,
			PermissionSettingsAvatarManage,
			PermissionSettingsAppearanceManage,
			PermissionSettingsNotificationsManage,
			PermissionForumSettingsManage,
			PermissionSEOManage,
			PermissionCategoryManage,
			PermissionTagManage,
			PermissionAttachmentManage,
			PermissionAttachmentSettings,
			PermissionAttachmentUploadPolicyManage,
			PermissionEntityMetaManage,
			// 外部登录 provider 的 Host-owned 激活/探测/重置；executable trust 仍 super_admin-only。
			PermissionIdentityProviderManage,
			// 徽章是运营资产：站点运营可配置规则并手动授予。
			PermissionBadgeView,
			PermissionBadgeManage,
		},
	},
	{
		Key:         RoleTechAdmin,
		Alias:       "技术管理",
		Description: "技术运维：扩展、搜索索引、后台任务、数据库浏览与附件存储设置。",
		PermissionKeys: []string{
			PermissionAdminAccess,
			PermissionExtensionView,
			PermissionExtensionPluginManage,
			PermissionExtensionThemeManage,
			PermissionJobsView,
			PermissionJobsManage,
			PermissionSearchManage,
			PermissionDatabaseManage,
			PermissionAttachmentSettings,
			PermissionAIManage,
		},
	},
}

// IsBuiltInSystemRole 内置系统角色（不可删除；member 另走默认注册组锁定）。
func IsBuiltInSystemRole(roleKey string) bool {
	switch roleKey {
	case RoleSuperAdmin, RoleMember, RoleModerator, RoleOperator, RoleTechAdmin, RoleBuiltinAuto:
		return true
	default:
		return false
	}
}

// RoleTemplateByKey 按 key 查找内置模板；不存在时 ok=false。
func RoleTemplateByKey(roleKey string) (SeedRoleTemplate, bool) {
	for _, template := range SeedRoleTemplates {
		if template.Key == roleKey {
			return template, true
		}
	}
	return SeedRoleTemplate{}, false
}
