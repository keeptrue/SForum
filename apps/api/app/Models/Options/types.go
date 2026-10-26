package options

import (
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	NameSiteName   = "site.name"
	NameSiteURL    = "site.url"
	NameSiteDomain = "site.domain"
	// 侧边栏底部“关于 {siteName}”链接；可空表示只显示不可点击说明。
	NameSiteAboutURL          = "site.about_url"
	NameSiteAboutOpenInNewTab = "site.about_open_in_new_tab"
	// 站点副标题/标语（短文本，可空）。用于导航副标、登录页等，不等于 SEO 描述。
	NameSiteTagline = "site.tagline"
	// 站点管理员联系邮箱（可空）。系统通知/运营联系人；不是 SMTP From，也不当 secret。
	// 非 public，避免被爬虫批量采集。
	NameSiteAdminEmail = "site.admin_email"
	// Wave 2 品牌资源：附件 ID（正整数字符串，可空）+ 公开 URL 回退（可空）。
	// 前台优先解析 attachment；无法解析时用 URL；皆空则使用 Core 默认品牌资源。
	NameSiteLogoURL                    = "site.logo_url"
	NameSiteLogoAttachmentID           = "site.logo_attachment_id"
	NameSiteFaviconURL                 = "site.favicon_url"
	NameSiteFaviconAttachmentID        = "site.favicon_attachment_id"
	NameSiteAppleTouchIconURL          = "site.apple_touch_icon_url"
	NameSiteAppleTouchIconAttachmentID = "site.apple_touch_icon_attachment_id"
	// 法律页正文 stubs（Markdown，可空）；页脚链接仍由 footer.links 指向路由。
	NameLegalTermsBodyZHCN      = "legal.terms.body.zh-CN"
	NameLegalTermsBodyENUS      = "legal.terms.body.en-US"
	NameLegalPrivacyBodyZHCN    = "legal.privacy.body.zh-CN"
	NameLegalPrivacyBodyENUS    = "legal.privacy.body.en-US"
	NameLegalGuidelinesBodyZHCN = "legal.guidelines.body.zh-CN"
	NameLegalGuidelinesBodyENUS = "legal.guidelines.body.en-US"
	NameSiteDefaultLocale       = "site.default_locale"
	NameSiteSupportedLocales    = "site.supported_locales"
	// 站点展示时区（IANA，如 Asia/Shanghai）。仅影响展示与按站点日切分，库内仍存 UTC。
	NameSiteTimezone = "site.timezone"
	// 日期/时间展示预设（白名单 key，非任意 pattern）。
	NameSiteDateFormat = "site.date_format"
	NameSiteTimeFormat = "site.time_format"
	// 一周起始日：0=周日 … 6=周六。默认 1（周一）。
	NameSiteStartOfWeek = "site.start_of_week"
	// GitHub release API 镜像/代理地址。空值表示直接访问 api.github.com。
	NameSystemUpdatesGitHubMirrorURL = "system.updates.github_mirror_url"
	// 公开前端贡献面 revision（整数，从 1 起）。扩展设置变更且影响公开贡献时由宿主 bump；
	// Nuxt 匿名 /t/** SWR 缓存键 varies 此值，避免运营改设置后仍命中旧 HTML。
	// 仅宿主内部 bump；运营不可通过 admin web-options 手写覆盖。
	NamePublicSurfaceRevision              = "site.public_surface_revision"
	NameHumanVerificationProvider          = "human_verification.provider"
	NameHumanVerificationRegister          = "human_verification.scenarios.register"
	NameHumanVerificationPasswordReset     = "human_verification.scenarios.password_reset"
	NameHumanVerificationEmailVerification = "human_verification.scenarios.email_verification"
	NameHumanVerificationLoginRisk         = "human_verification.scenarios.login_risk"
	NameHumanVerificationPostRisk          = "human_verification.scenarios.post_risk"
	NameHumanVerificationLoginAlways       = "human_verification.login_risk.always"
	NameHumanVerificationLoginNewDevice    = "human_verification.login_risk.new_device"
	NameHumanVerificationLoginPrivileged   = "human_verification.login_risk.privileged"
	NameHumanVerificationRateLimit         = "human_verification.rate_limit.per_minute"
	NameAltchaSecret                       = "human_verification.altcha.secret"
	NameAltchaChallengeTTL                 = "human_verification.altcha.challenge_ttl"
	NameAltchaCost                         = "human_verification.altcha.cost"
	NameAltchaWidgetType                   = "human_verification.altcha.widget.type"
	NameAltchaWidgetAuto                   = "human_verification.altcha.widget.auto"
	NameAltchaWidgetDisplay                = "human_verification.altcha.widget.display"
	NameAltchaWidgetHideLogo               = "human_verification.altcha.widget.hide_logo"
	NameAltchaWidgetHideFooter             = "human_verification.altcha.widget.hide_footer"
	NameAltchaWidgetWorkers                = "human_verification.altcha.widget.workers"
	NameAltchaWidgetMinDuration            = "human_verification.altcha.widget.min_duration_ms"
	NameAppearanceTheme                    = "appearance.theme"
	NameAppearanceLightBackground          = "appearance.light_background"
	NameFooterCopyrightZHCN                = "footer.copyright.zh-CN"
	NameFooterCopyrightENUS                = "footer.copyright.en-US"
	NameFooterLinks                        = "footer.links"
	NameIdentityPasswordMinLength          = "identity.password.min_length"
	NameIdentityPasswordMaxLength          = "identity.password.max_length"
	NameIdentityPasswordRequireLowercase   = "identity.password.require_lowercase"
	NameIdentityPasswordRequireUppercase   = "identity.password.require_uppercase"
	NameIdentityPasswordRequireNumber      = "identity.password.require_number"
	NameIdentityPasswordRequireSymbol      = "identity.password.require_symbol"
	// 是否允许开放注册。public，便于登录页隐藏注册入口；默认 enabled。
	// 首用户 bootstrap（库内尚无任何用户）始终允许注册，不受本开关影响。
	NameIdentityRegistrationEnabled = "identity.registration.enabled"
	// 注册模式：open | invite | approval | closed。与 enabled 并存时以 mode 为准（closed 等价关闭）。
	NameIdentityRegistrationMode = "identity.registration.mode"
	// 注册后是否要求邮箱验证（产品开关；完整发信流随邮件垂直切片完善）。
	NameIdentityRegistrationRequireEmailVerification = "identity.registration.require_email_verification"
	// 未验证邮箱时是否禁止发帖/回帖（依赖 require_email_verification）。
	NameIdentityRegistrationBlockPostingUntilVerified = "identity.registration.block_posting_until_verified"
	// 新用户注册完成后是否发送欢迎邮件。默认关闭，避免新站点在未明确选择时产生额外邮件。
	NameMailWelcomeEnabled = "mail.welcome.enabled"
	// 用户名长度与字符集策略（注册时服务端强制）。
	NameIdentityUsernameMinLength = "identity.username.min_length"
	NameIdentityUsernameMaxLength = "identity.username.max_length"
	// unicode_letters_numbers | ascii
	NameIdentityUsernameCharset = "identity.username.charset"
	// 逗号分隔保留用户名（小写比较）。
	NameIdentityUsernameReserved = "identity.username.reserved"
	// 登录失败锁定：连续失败次数 / 锁定分钟数（0 表示关闭）。
	NameIdentityLoginMaxFailures    = "identity.login.max_failures"
	NameIdentityLoginLockoutMinutes = "identity.login.lockout_minutes"
	// 手动身份邮件（邮箱验证、密码找回）的共享重发与窗口限流策略。
	NameIdentityMailResendCooldownSeconds = "identity.mail_resend.cooldown_seconds"
	NameIdentityMailResendWindowMinutes   = "identity.mail_resend.window_minutes"
	NameIdentityMailResendMaxPerTarget    = "identity.mail_resend.max_per_target"
	NameIdentityMailResendMaxPerIP        = "identity.mail_resend.max_per_ip"
	// 最大活跃浏览器会话数（设备数上限）。非 public（仅后端登录时读取），admin 可调。
	// 引用 identity 包的权威定义，避免同值两处定义导致漂移（Fix #11）。
	NameIdentitySessionsMaxDevices = identity.NameSessionsMaxDevices
	// 已下线历史会话的保留天数，超过后由 periodic job 清理。非 public。
	NameIdentitySessionsKeepDays = identity.NameSessionsKeepDays

	// 新人信任阶梯：注册后 N 天内适用更严的发帖节奏与外链策略。
	NameTrustNewUserDays                   = "trust.new_user_days"
	NameTrustNewUserTopicCooldownSeconds   = "trust.new_user.topic_cooldown_seconds"
	NameTrustNewUserCommentCooldownSeconds = "trust.new_user.comment_cooldown_seconds"
	NameTrustNewUserDailyTopicLimit        = "trust.new_user.daily_topic_limit"
	NameTrustNewUserDailyCommentLimit      = "trust.new_user.daily_comment_limit"
	NameTrustNewUserForbidOutboundLinks    = "trust.new_user.forbid_outbound_links"
	NameTrustNewUserForbidAttachments      = "trust.new_user.forbid_attachments"

	// 维护模式：开启后非管理员写操作与前台写入口被拦；管理员可绕过。
	NameSiteMaintenanceEnabled = "site.maintenance.enabled"
	NameSiteMaintenanceMessage = "site.maintenance.message"

	// 客户端版本策略：原生 App / 桌面端在冷启动读取公共选项自行判定是否强制升级。
	// 空值表示不限制（默认不阻塞任何客户端）。
	NameClientMinimumVersion     = "client.minimum_version"
	NameClientRecommendedVersion = "client.recommended_version"
	NameClientUpdateNotice       = "client.update_notice"

	NameForumDefaultCategorySlug    = "forum.default_category_slug"
	NameForumTagCreationMode        = "forum.tags.creation_mode"
	NameForumTagPublicPages         = "forum.tags.public_pages"
	NameForumTagMinPerTopic         = "forum.tags.min_per_topic"
	NameForumTagMaxPerTopic         = "forum.tags.max_per_topic"
	NameForumTopicsPerPage          = "forum.pagination.topics_per_page"
	NameForumCommentsPerPage        = "forum.pagination.comments_per_page"
	NameForumTopicTitleMinRunes     = "forum.topics.title_min_runes"
	NameForumTopicTitleMaxRunes     = "forum.topics.title_max_runes"
	NameForumTopicContentMinRunes   = "forum.topics.content_min_runes"
	NameForumTopicContentMaxRunes   = "forum.topics.content_max_runes"
	NameForumTopicEditWindowMinutes = "forum.topics.edit_window_minutes"
	NameForumTopicCooldownSeconds   = "forum.topics.cooldown_seconds"
	NameForumDailyTopicLimit        = "forum.topics.daily_limit"
	NameForumCommentMinRunes        = "forum.comments.min_runes"
	NameForumCommentMaxRunes        = "forum.comments.max_runes"
	NameForumCommentMaxNestingDepth = "forum.comments.max_nesting_depth"
	// NameForumCommentsTreeDescendantsPerRoot view=tree 每根最多子孙数（D2，1–100，默认 50）。
	NameForumCommentsTreeDescendantsPerRoot = "forum.comments.tree_descendants_per_root"
	NameForumCommentEditWindowMinutes       = "forum.comments.edit_window_minutes"
	NameForumCommentCooldownSeconds         = "forum.comments.cooldown_seconds"
	NameForumDailyCommentLimit              = "forum.comments.daily_limit"
	NameForumExcerptRuneLimit               = "forum.reading.excerpt_rune_limit"
	// 游客阅读：public | login_required
	NameForumGuestRead = "forum.guest.read"
	// 列表默认排序：latest | active | hot
	NameForumListDefaultSort = "forum.list.default_sort"
	// 热度窗口天数（hot 排序用）
	NameForumListHotWindowDays = "forum.list.hot_window_days"
	// 主题行为策略
	NameForumTopicsAllowAuthorCloseReplies = "forum.topics.allow_author_close_replies"
	NameForumTopicsAllowAuthorDelete       = "forum.topics.allow_author_delete"
	NameForumTopicsAutoLockIdleDays        = "forum.topics.auto_lock_idle_days"
	NameForumTopicsShowEditMark            = "forum.topics.show_edit_mark"
	NameForumTopicsDuplicateTitlePolicy    = "forum.topics.duplicate_title_policy"
	// 评论行为
	NameForumCommentsShowEditMark         = "forum.comments.show_edit_mark"
	NameForumCommentsSoftDeleteVisibility = "forum.comments.soft_delete_visibility"
	// 正文外链呈现：站外链接是否经站内中转页（点击时判定），以及免中转域名白名单。
	// 白名单逐行书写，可带 scheme/路径，存储时归一为 host[:port]。
	NameForumLinksExternalInterstitial = "forum.links.external_interstitial"
	NameForumLinksTrustedDomains       = "forum.links.trusted_domains"
	// 提及
	NameForumMentionsEnabled                = "forum.mentions.enabled"
	NameForumMentionsMaxPerPost             = "forum.mentions.max_per_post"
	NameSEOMetaTitleTemplate                = "seo.meta_title_template"
	NameSEOMetaDescription                  = "seo.meta_description"
	NameSEOMetaKeywords                     = "seo.meta_keywords"
	NameSEOOGImageURL                       = "seo.og_image_url"
	NameSEOTwitterCard                      = "seo.twitter_card"
	NameSEOTwitterSite                      = "seo.twitter_site"
	NameSEOAllowIndexing                    = "seo.allow_indexing"
	NameSEOGoogleVerification               = "seo.google_verification"
	NameSEOBingVerification                 = "seo.bing_verification"
	NameSEOBaiduVerification                = "seo.baidu_verification"
	NameSEOYandexVerification               = "seo.yandex_verification"
	NameSEORobotsExtraAllow                 = "seo.robots.extra_allow"
	NameSEORobotsExtraDisallow              = "seo.robots.extra_disallow"
	NameSEORobotsBlockAIBots                = "seo.robots.block_ai_bots"
	NameSEORobotsBlockNonSEOBots            = "seo.robots.block_non_seo_bots"
	NameSEOSitemapEnabled                   = "seo.sitemap.enabled"
	NameSEOSitemapIncludeStaticPages        = "seo.sitemap.include_static_pages"
	NameSEOSitemapIncludeForumContent       = "seo.sitemap.include_forum_content"
	NameSEOSchemaOrgEnabled                 = "seo.schema_org.enabled"
	NameSEOSchemaOrgSearchAction            = "seo.schema_org.search_action_enabled"
	NameSEOSchemaOrgDiscussion              = "seo.schema_org.discussion_enabled"
	NameSEOSchemaOrgOrganizationLogo        = "seo.schema_org.organization_logo_url"
	NameSEOSiteInheritSiteName              = "seo.site.inherit_site_name"
	NameSEOSiteName                         = "seo.site.name"
	NameSEOHomeTitle                        = "seo.home.title"
	NameSEOHomeDescription                  = "seo.home.description"
	NameSEOHomeKeywords                     = "seo.home.keywords"
	NameSEOHomeOGTitle                      = "seo.home.og_title"
	NameSEOHomeOGDescription                = "seo.home.og_description"
	NameSEOHomeOGImageURL                   = "seo.home.og_image_url"
	NameSEOPageTitleTemplate                = "seo.page.title_template"
	NameSEOPageDefaultDescription           = "seo.page.default_description"
	NameSEOPageTitleSeparator               = "seo.page.title_separator"
	NameSEOContentCategoryDescriptionSource = "seo.content_type.category.description_source"
	NameSEOContentTopicTitleTemplate        = "seo.content_type.topic.title_template"
	NameSEOContentTopicIndexMode            = "seo.content_type.topic.index_mode"
	// 帖子详情页 URL 形态：id_slug | id | slug。默认 id_slug。
	NameSEOTopicURLMode                        = "seo.topic_url_mode"
	NameAttachmentProvider                     = "attachment.provider"
	NameAttachmentUploadEnabled                = "attachment.upload.enabled"
	NameAttachmentPathTemplate                 = "attachment.path_template"
	NameAttachmentPublicBaseURL                = "attachment.public_base_url"
	NameAttachmentMaxFileSizeMB                = "attachment.max_file_size_mb"
	NameAttachmentAllowedExtensions            = "attachment.allowed_extensions"
	NameAttachmentAllowedMIMETypes             = "attachment.allowed_mime_types"
	NameAttachmentDefaultVisibility            = "attachment.default_visibility"
	NameAttachmentCleanupOrphanDays            = "attachment.cleanup_orphan_after_days"
	NameAttachmentLocalRoot                    = "attachment.local.root"
	NameAttachmentLocalPublicPrefix            = "attachment.local.public_prefix"
	NameAttachmentCompressionEnabled           = "attachment.compression.enabled"
	NameAttachmentCompressionStrength          = "attachment.compression.strength"
	NameAttachmentCompressionMaxDimension      = "attachment.compression.max_dimension"
	NameAttachmentCompressionMinSizeKB         = "attachment.compression.min_size_kb"
	NameAttachmentCompressionMinSavingsPercent = "attachment.compression.min_savings_percent"
	// 单个扩展最多可认领多少张存活附件（公开图标等），防止插件把站点当图床。
	NameAttachmentExtensionReferenceLimit = "attachment.extension_reference_limit"

	// 头像运行时选项。统一头像策略：用户上传优先，否则按 default_provider 回退。
	NameAvatarAllowUpload           = "avatar.allow_upload"
	NameAvatarDefaultProvider       = "avatar.default_provider"
	NameAvatarGravatarBaseURL       = "avatar.gravatar_base_url"
	NameAvatarGravatarHashAlgorithm = "avatar.gravatar_hash_algorithm"
	NameAvatarDefaultStaticURL      = "avatar.default_static_url"
	NameAvatarMaxSizeKB             = "avatar.max_size_kb"
	NameAvatarMaxDimension          = "avatar.max_dimension"
	NameAvatarAllowGIF              = "avatar.allow_gif"
	NameAvatarCompressEnabled       = "avatar.compress_enabled"
	NameAvatarTargetDimension       = "avatar.target_dimension"
	NameAvatarCompressQuality       = "avatar.compress_quality"

	// 昵称策略。昵称是展示标签而不是身份（唯一性只属于 username），
	// 因此这里只有可编辑性、冷却与长度；敏感词复用 identity.username.reserved。
	NameProfileNicknameAllowUserChange = "profile.nickname.allow_user_change"
	NameProfileNicknameCooldownMinutes = "profile.nickname.cooldown_minutes"
	NameProfileNicknameMinLength       = "profile.nickname.min_length"
	NameProfileNicknameMaxLength       = "profile.nickname.max_length"
	NameProfileNicknameHistoryPublic   = "profile.nickname.history_public"

	NameNotificationReplyInApp      = "notification.reply.in_app"
	NameNotificationReplyEmail      = "notification.reply.email"
	NameNotificationMentionInApp    = "notification.mention.in_app"
	NameNotificationMentionEmail    = "notification.mention.email"
	NameNotificationModerationInApp = "notification.moderation.in_app"
	NameNotificationModerationEmail = "notification.moderation.email"

	CodeInvalid = "options.invalid"
)

var ErrInvalidOption = errors.New("options: invalid option")

type Option struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type RuntimeSettings struct {
	SiteName                  string
	SiteURL                   string
	DefaultLocale             string
	SupportedLocales          []string
	HumanVerificationProvider string
}

type AdminOption struct {
	Name          string  `json:"name"`
	Value         string  `json:"value"`
	Public        bool    `json:"public"`
	Secret        bool    `json:"secret"`
	SecretSet     bool    `json:"secretSet"`
	OverrideValue *string `json:"overrideValue,omitempty"`
	FallbackValue string  `json:"fallbackValue,omitempty"`
	Inherited     bool    `json:"inherited,omitempty"`
}

type UpdateInput struct {
	Name  string
	Value string
}
