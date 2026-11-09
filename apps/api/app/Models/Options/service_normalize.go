package options

import (
	"strconv"
	"strings"
)

func copyValues(values map[string]string) map[string]string {
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func allOptionNames() []string {
	names := make([]string, 0, len(optionDefinitions))
	for _, definition := range optionDefinitions {
		names = append(names, definition.name)
	}
	return names
}

func normalizeName(name string) string {
	return strings.TrimSpace(name)
}

func normalizeOptionValue(name string, value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch name {
	case NameSiteName:
		return value, value != "" && len([]rune(value)) <= 80
	case NameSiteURL:
		// 空值表示不覆盖部署环境的 APP_URL；公开读取阶段统一解析为有效地址。
		if value == "" {
			return "", true
		}
		return value, isValidURL(value)
	case NameSiteDomain:
		return normalizeSiteDomain(value)
	case NameSiteAboutURL:
		return normalizeSiteAboutURL(value)
	case NameSiteAboutOpenInNewTab:
		return normalizeEnabledOption(value)
	case NameSiteDefaultLocale:
		return normalizeLocaleChoice(value, builtInLocales)
	case NameSiteSupportedLocales:
		locales := parseStoredLocales(value)
		if len(locales) == 0 {
			return "", false
		}
		return strings.Join(locales, ","), true
	case NameSiteTagline:
		return normalizeSiteTagline(value)
	case NameSiteAdminEmail:
		return normalizeSiteAdminEmail(value)
	case NameSiteLogoURL, NameSiteLogoAttachmentID,
		NameSiteFaviconURL, NameSiteFaviconAttachmentID,
		NameSiteAppleTouchIconURL, NameSiteAppleTouchIconAttachmentID,
		NameLegalTermsBodyZHCN, NameLegalTermsBodyENUS,
		NameLegalPrivacyBodyZHCN, NameLegalPrivacyBodyENUS,
		NameLegalGuidelinesBodyZHCN, NameLegalGuidelinesBodyENUS:
		return normalizeSiteBrandOption(name, value)
	case NameSiteTimezone:
		return normalizeSiteTimezone(value)
	case NameSiteDateFormat:
		return normalizeSiteDateFormat(value)
	case NameSiteTimeFormat:
		return normalizeSiteTimeFormat(value)
	case NameSiteStartOfWeek:
		return normalizeSiteStartOfWeek(value)
	case NameSystemUpdatesGitHubMirrorURL:
		return normalizeSystemUpdatesGitHubMirrorURL(value)
	case NamePublicSurfaceRevision:
		return normalizePublicSurfaceRevision(value)
	case NameHumanVerificationProvider:
		return normalizeHumanVerificationProvider(value)
	case NameHumanVerificationRegister, NameHumanVerificationPasswordReset, NameHumanVerificationEmailVerification, NameHumanVerificationLoginRisk, NameHumanVerificationPostRisk,
		NameHumanVerificationLoginAlways, NameHumanVerificationLoginNewDevice, NameHumanVerificationLoginPrivileged:
		return normalizeEnabledOption(value)
	case NameHumanVerificationRateLimit:
		parsed, ok := parseBoundedInt(value, humanVerificationRateLimitMin, humanVerificationRateLimitMax)
		if !ok {
			return "", false
		}
		return strconv.Itoa(parsed), true
	case NameAltchaSecret:
		return value, true
	case NameAltchaChallengeTTL:
		duration, ok := parsePositiveDuration(value)
		if !ok {
			return "", false
		}
		return duration.String(), true
	case NameAltchaCost:
		parsed, ok := parsePositiveInt(value)
		if !ok {
			return "", false
		}
		return strconv.Itoa(parsed), true
	case NameAltchaWidgetType:
		return normalizeAltchaWidgetType(value)
	case NameAltchaWidgetAuto:
		return normalizeAltchaWidgetAuto(value)
	case NameAltchaWidgetDisplay:
		return normalizeAltchaWidgetDisplay(value)
	case NameAltchaWidgetHideLogo, NameAltchaWidgetHideFooter:
		return normalizeEnabledOption(value)
	case NameAltchaWidgetWorkers:
		parsed, ok := parseBoundedInt(value, altchaWidgetWorkersMin, altchaWidgetWorkersMax)
		if !ok {
			return "", false
		}
		return strconv.Itoa(parsed), true
	case NameAltchaWidgetMinDuration:
		parsed, ok := parseBoundedInt(value, altchaWidgetMinDurationMin, altchaWidgetMinDurationMax)
		if !ok {
			return "", false
		}
		return strconv.Itoa(parsed), true
	case NameAppearanceTheme:
		return normalizeAppearanceTheme(value)
	case NameAppearanceLightBackground:
		return normalizeAppearanceLightBackground(value)
	case NameFooterCopyrightZHCN, NameFooterCopyrightENUS:
		return normalizeFooterCopyright(value)
	case NameFooterLinks:
		return normalizeFooterLinks(value)
	case NameIdentityPasswordMinLength:
		return normalizeBoundedInt(value, passwordMinLengthMin, passwordMinLengthMax)
	case NameIdentityPasswordMaxLength:
		return normalizeBoundedInt(value, passwordMaxLengthMin, passwordMaxLengthMax)
	case NameIdentityPasswordRequireLowercase, NameIdentityPasswordRequireUppercase, NameIdentityPasswordRequireNumber, NameIdentityPasswordRequireSymbol, NameIdentityRegistrationEnabled, NameMailWelcomeEnabled:
		return normalizeEnabledOption(value)
	case NameIdentityRegistrationMode,
		NameIdentityRegistrationRequireEmailVerification,
		NameIdentityRegistrationBlockPostingUntilVerified,
		NameIdentityUsernameMinLength,
		NameIdentityUsernameMaxLength,
		NameIdentityUsernameCharset,
		NameIdentityUsernameReserved,
		NameIdentityLoginMaxFailures,
		NameIdentityLoginLockoutMinutes,
		NameTrustNewUserDays,
		NameTrustNewUserTopicCooldownSeconds,
		NameTrustNewUserCommentCooldownSeconds,
		NameTrustNewUserDailyTopicLimit,
		NameTrustNewUserDailyCommentLimit,
		NameTrustNewUserForbidOutboundLinks,
		NameTrustNewUserForbidAttachments,
		NameSiteMaintenanceEnabled,
		NameSiteMaintenanceMessage,
		NameForumGuestRead,
		NameForumListDefaultSort,
		NameForumListHotWindowDays,
		NameForumTopicsAllowAuthorCloseReplies,
		NameForumTopicsAllowAuthorDelete,
		NameForumTopicsAutoLockIdleDays,
		NameForumTopicsShowEditMark,
		NameForumTopicsDuplicateTitlePolicy,
		NameForumCommentsShowEditMark,
		NameForumCommentsSoftDeleteVisibility,
		NameForumMentionsEnabled,
		NameForumMentionsMaxPerPost,
		NameForumLinksExternalInterstitial,
		NameForumLinksTrustedDomains:
		return normalizeCommunityPolicyOption(name, value)
	case NameClientMinimumVersion, NameClientRecommendedVersion:
		// 客户端版本策略：空值合法（表示不限制）。
		return normalizeClientVersion(value)
	case NameClientUpdateNotice:
		return normalizeClientUpdateNotice(value)
	case NameIdentitySessionsMaxDevices:
		// 限制在 1-20；非法值返回 false，上游保留默认值（beginner-friendly：配置错误不致功能失效）。
		return normalizeBoundedInt(value, sessionsMaxDevicesMin, sessionsMaxDevicesMax)
	case NameIdentitySessionsKeepDays:
		return normalizeBoundedInt(value, sessionsKeepDaysMin, sessionsKeepDaysMax)
	case NameIdentityMailResendCooldownSeconds,
		NameIdentityMailResendWindowMinutes,
		NameIdentityMailResendMaxPerTarget,
		NameIdentityMailResendMaxPerIP:
		return normalizeMailResendOption(name, value)
	case NameForumDefaultCategorySlug:
		return normalizeForumSlug(value)
	case NameForumTagCreationMode:
		return normalizeForumTagCreationMode(value)
	case NameForumTagPublicPages:
		return normalizeEnabledOption(value)
	case NameFeatureSearch, NameFeatureRegistration, NameFeatureAttachments, NameFeatureMentions, NameFeaturePublicProfiles, NameFeatureWebhooks, NameFeatureReactions:
		// F4.5：产品开关仅接受 enabled/disabled。
		return normalizeEnabledOption(value)
	case NamePagesRegistryEnabled, NameThemesRuntimeL0Enabled, NameThemesRuntimeL1Enabled:
		// Page Registry / runtime theme dual-stack 开关。
		return normalizeEnabledOption(value)
	case NameForumTagMinPerTopic, NameForumTagMaxPerTopic:
		return normalizeBoundedInt(value, forumTagMaxPerTopicMin, forumTagMaxPerTopicMax)
	case NameForumTopicsPerPage, NameForumCommentsPerPage:
		return normalizeBoundedInt(value, forumPaginationMin, forumPaginationMax)
	case NameForumTopicTitleMinRunes:
		return normalizeBoundedInt(value, forumTitleMinRunesMin, forumTitleMinRunesMax)
	case NameForumTopicTitleMaxRunes:
		return normalizeBoundedInt(value, forumTitleMaxRunesMin, forumTitleMaxRunesMax)
	case NameForumTopicContentMinRunes:
		return normalizeBoundedInt(value, forumContentMinRunesMin, forumContentMinRunesMax)
	case NameForumTopicContentMaxRunes:
		return normalizeBoundedInt(value, forumContentMaxRunesMin, forumContentMaxRunesMax)
	case NameForumCommentMinRunes:
		return normalizeBoundedInt(value, forumCommentMinRunesMin, forumCommentMinRunesMax)
	case NameForumCommentMaxRunes:
		return normalizeBoundedInt(value, forumCommentMaxRunesMin, forumCommentMaxRunesMax)
	case NameForumCommentMaxNestingDepth:
		return normalizeBoundedInt(value, forumNestingMin, forumNestingMax)
	case NameForumCommentsTreeDescendantsPerRoot:
		return normalizeBoundedInt(value, forumTreeDescendantsMin, forumTreeDescendantsMax)
	case NameForumTopicEditWindowMinutes, NameForumCommentEditWindowMinutes:
		return normalizeBoundedInt(value, forumEditWindowMin, forumEditWindowMax)
	case NameForumTopicCooldownSeconds, NameForumCommentCooldownSeconds:
		return normalizeBoundedInt(value, forumCooldownMin, forumCooldownMax)
	case NameForumDailyTopicLimit, NameForumDailyCommentLimit:
		return normalizeBoundedInt(value, forumDailyLimitMin, forumDailyLimitMax)
	case NameForumExcerptRuneLimit:
		return normalizeBoundedInt(value, forumExcerptMin, forumExcerptMax)
	case NameSEOMetaTitleTemplate:
		return normalizeSEOTitleTemplate(value)
	case NameSEOMetaDescription:
		return normalizeBoundedText(value, seoDescriptionMaxRunes)
	case NameSEOMetaKeywords:
		return normalizeBoundedText(value, seoKeywordsMaxRunes)
	case NameSEOOGImageURL, NameSEOSchemaOrgOrganizationLogo:
		return normalizeOptionalURL(value)
	case NameSEOTwitterCard:
		return normalizeSEOTwitterCard(value)
	case NameSEOTwitterSite:
		return normalizeSEOTwitterSite(value)
	case NameSEOAllowIndexing, NameSEORobotsBlockAIBots, NameSEORobotsBlockNonSEOBots, NameSEOSitemapEnabled, NameSEOSitemapIncludeStaticPages, NameSEOSitemapIncludeForumContent, NameSEOSchemaOrgEnabled, NameSEOSchemaOrgSearchAction, NameSEOSchemaOrgDiscussion:
		return normalizeEnabledOption(value)
	case NameSEOGoogleVerification, NameSEOBingVerification, NameSEOBaiduVerification, NameSEOYandexVerification:
		return normalizeSEOVerificationToken(value)
	case NameSEORobotsExtraAllow, NameSEORobotsExtraDisallow:
		return normalizeSEORobotsPathList(value)
	case NameSEOTopicURLMode:
		// 帖子 URL 形态枚举，大小写归一后白名单校验。
		return normalizeChoice(value, []string{"id_slug", "id", "slug"})
	case NameAttachmentProvider:
		return normalizeAttachmentProvider(value)
	case NameAttachmentUploadEnabled:
		return normalizeEnabledOption(value)
	case NameAttachmentPathTemplate:
		return normalizeAttachmentPathTemplate(value)
	case NameAttachmentLocalRoot:
		return normalizeAttachmentLocalRoot(value)
	case NameAttachmentPublicBaseURL, NameAttachmentLocalPublicPrefix:
		return normalizeOptionalURL(value)
	case NameAttachmentMaxFileSizeMB:
		return normalizeBoundedInt(value, 1, 1024)
	case NameAttachmentAllowedExtensions:
		return normalizeAttachmentExtensions(value)
	case NameAttachmentAllowedMIMETypes:
		return normalizeAttachmentMIMETypes(value)
	case NameAttachmentDefaultVisibility:
		return normalizeStringChoice(value, attachmentVisibilities)
	case NameAttachmentCleanupOrphanDays:
		return normalizeBoundedInt(value, 1, 3650)
	case NameAttachmentCompressionEnabled:
		return normalizeEnabledOption(value)
	case NameAttachmentCompressionStrength:
		return normalizeBoundedInt(value, 0, 100)
	case NameAttachmentCompressionMaxDimension:
		return normalizeBoundedInt(value, 320, 8192)
	case NameAttachmentCompressionMinSizeKB:
		return normalizeBoundedInt(value, 1, 1024*1024)
	case NameAttachmentCompressionMinSavingsPercent:
		return normalizeBoundedInt(value, 0, 90)
	case NameAttachmentExtensionReferenceLimit:
		return normalizeBoundedInt(value, 1, 10000)
	case NameAvatarAllowUpload, NameAvatarAllowGIF, NameAvatarCompressEnabled:
		return normalizeEnabledOption(value)
	case NameAvatarDefaultProvider:
		return normalizeAvatarProvider(value)
	case NameAvatarGravatarBaseURL:
		return normalizeAvatarGravatarBaseURL(value)
	case NameAvatarGravatarHashAlgorithm:
		return normalizeAvatarHashAlgorithm(value)
	case NameAvatarDefaultStaticURL:
		return normalizeOptionalURL(value)
	case NameAvatarMaxSizeKB:
		return normalizeBoundedInt(value, avatarMaxSizeKBMin, avatarMaxSizeKBMax)
	case NameAvatarMaxDimension:
		return normalizeBoundedInt(value, avatarDimensionMin, avatarDimensionMax)
	case NameAvatarTargetDimension:
		return normalizeBoundedInt(value, avatarDimensionMin, avatarDimensionMax)
	case NameAvatarCompressQuality:
		return normalizeBoundedInt(value, avatarCompressQualityMin, avatarCompressQualityMax)
	case NameProfileNicknameAllowUserChange, NameProfileNicknameHistoryPublic:
		return normalizeEnabledOption(value)
	case NameProfileNicknameCooldownMinutes:
		return normalizeBoundedInt(value, 0, nicknameCooldownMinutesMax)
	case NameProfileNicknameMinLength:
		return normalizeBoundedInt(value, nicknameMinLengthMin, nicknameMinLengthMax)
	case NameProfileNicknameMaxLength:
		return normalizeBoundedInt(value, nicknameMaxLengthMin, nicknameMaxLengthMax)
	case NameNotificationReplyInApp, NameNotificationReplyEmail, NameNotificationMentionInApp, NameNotificationMentionEmail, NameNotificationModerationInApp, NameNotificationModerationEmail:
		return normalizeEnabledOption(value)
	case NameExtensionBuiltinAutoUpdate:
		return normalizeEnabledOption(value)
	default:
		return normalizeSEOOption(name, value)
	}
}

// 邮件 provider 白名单。
