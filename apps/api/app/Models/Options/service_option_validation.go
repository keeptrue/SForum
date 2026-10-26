package options

import (
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

func isValidValueSet(values map[string]string) bool {
	if strings.TrimSpace(values[NameSiteName]) == "" || len([]rune(values[NameSiteName])) > 80 {
		return false
	}
	if !isValidURL(values[NameSiteURL]) {
		return false
	}

	supported := parseStoredLocales(values[NameSiteSupportedLocales])
	if len(supported) == 0 {
		return false
	}
	if _, ok := normalizeLocaleChoice(values[NameSiteDefaultLocale], supported); !ok {
		return false
	}
	if !isValidSiteDateTimeOptions(values) {
		return false
	}
	if !isValidSiteIdentityOptions(values) {
		return false
	}
	if !isValidSiteBrandOptions(values) {
		return false
	}
	if !isValidCommunityPolicyOptions(values) {
		return false
	}
	if !isValidClientVersionOptions(values) {
		return false
	}

	provider, ok := normalizeHumanVerificationProvider(values[NameHumanVerificationProvider])
	if !ok {
		return false
	}
	if provider == humanverify.ProviderAltcha && strings.TrimSpace(values[NameAltchaSecret]) == "" {
		return false
	}
	for _, scenario := range humanVerificationScenarios {
		if _, ok := normalizeEnabledOption(values[scenario.name]); !ok {
			return false
		}
	}
	for _, name := range humanVerificationLoginTriggerOptions {
		if _, ok := normalizeEnabledOption(values[name]); !ok {
			return false
		}
	}
	if _, ok := parseBoundedInt(values[NameHumanVerificationRateLimit], humanVerificationRateLimitMin, humanVerificationRateLimitMax); !ok {
		return false
	}
	if _, ok := parsePositiveDuration(values[NameAltchaChallengeTTL]); !ok {
		return false
	}
	if _, ok := parsePositiveInt(values[NameAltchaCost]); !ok {
		return false
	}
	if _, ok := normalizeAltchaWidgetType(values[NameAltchaWidgetType]); !ok {
		return false
	}
	if _, ok := normalizeAltchaWidgetAuto(values[NameAltchaWidgetAuto]); !ok {
		return false
	}
	if _, ok := normalizeAltchaWidgetDisplay(values[NameAltchaWidgetDisplay]); !ok {
		return false
	}
	if _, ok := normalizeEnabledOption(values[NameAltchaWidgetHideLogo]); !ok {
		return false
	}
	if _, ok := normalizeEnabledOption(values[NameAltchaWidgetHideFooter]); !ok {
		return false
	}
	if _, ok := parseBoundedInt(values[NameAltchaWidgetWorkers], altchaWidgetWorkersMin, altchaWidgetWorkersMax); !ok {
		return false
	}
	if _, ok := parseBoundedInt(values[NameAltchaWidgetMinDuration], altchaWidgetMinDurationMin, altchaWidgetMinDurationMax); !ok {
		return false
	}
	if !validateAppearanceOptions(values) {
		return false
	}
	if _, ok := normalizeFooterCopyright(values[NameFooterCopyrightZHCN]); !ok {
		return false
	}
	if _, ok := normalizeFooterCopyright(values[NameFooterCopyrightENUS]); !ok {
		return false
	}
	if _, ok := normalizeFooterLinks(values[NameFooterLinks]); !ok {
		return false
	}
	minLength, minOK := parseBoundedInt(values[NameIdentityPasswordMinLength], passwordMinLengthMin, passwordMinLengthMax)
	maxLength, maxOK := parseBoundedInt(values[NameIdentityPasswordMaxLength], passwordMaxLengthMin, passwordMaxLengthMax)
	if !minOK || !maxOK || maxLength < minLength {
		return false
	}
	for _, name := range passwordPolicyBooleanOptionNames() {
		if _, ok := normalizeEnabledOption(values[name]); !ok {
			return false
		}
	}
	if _, ok := normalizeEnabledOption(values[NameIdentityRegistrationEnabled]); !ok {
		return false
	}
	if !isValidMailResendOptions(values) {
		return false
	}
	if _, ok := normalizeForumSlug(values[NameForumDefaultCategorySlug]); !ok {
		return false
	}
	if _, ok := normalizeForumTagCreationMode(values[NameForumTagCreationMode]); !ok {
		return false
	}
	if _, ok := normalizeEnabledOption(values[NameForumTagPublicPages]); !ok {
		return false
	}
	if _, ok := parseBoundedInt(values[NameForumTagMinPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); !ok {
		return false
	}
	if _, ok := parseBoundedInt(values[NameForumTagMaxPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); !ok {
		return false
	}
	if minTags, okMin := parseBoundedInt(values[NameForumTagMinPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); okMin {
		if maxTags, okMax := parseBoundedInt(values[NameForumTagMaxPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); okMax && minTags > maxTags {
			return false
		}
	}
	for _, name := range []string{NameForumTopicsPerPage, NameForumCommentsPerPage} {
		if _, ok := parseBoundedInt(values[name], forumPaginationMin, forumPaginationMax); !ok {
			return false
		}
	}
	if !validForumContentLimitOptionValues(values) {
		return false
	}
	if _, ok := normalizeSEOTitleTemplate(values[NameSEOMetaTitleTemplate]); !ok {
		return false
	}
	if _, ok := normalizeBoundedText(values[NameSEOMetaDescription], seoDescriptionMaxRunes); !ok {
		return false
	}
	if _, ok := normalizeBoundedText(values[NameSEOMetaKeywords], seoKeywordsMaxRunes); !ok {
		return false
	}
	if _, ok := normalizeOptionalURL(values[NameSEOOGImageURL]); !ok {
		return false
	}
	if _, ok := normalizeSEOTwitterCard(values[NameSEOTwitterCard]); !ok {
		return false
	}
	if _, ok := normalizeSEOTwitterSite(values[NameSEOTwitterSite]); !ok {
		return false
	}
	for _, name := range seoEnabledOptionNames() {
		if _, ok := normalizeEnabledOption(values[name]); !ok {
			return false
		}
	}
	for _, name := range seoVerificationOptionNames() {
		if _, ok := normalizeSEOVerificationToken(values[name]); !ok {
			return false
		}
	}
	if _, ok := normalizeSEORobotsPathList(values[NameSEORobotsExtraAllow]); !ok {
		return false
	}
	if _, ok := normalizeSEORobotsPathList(values[NameSEORobotsExtraDisallow]); !ok {
		return false
	}
	if _, ok := normalizeOptionalURL(values[NameSEOSchemaOrgOrganizationLogo]); !ok {
		return false
	}
	if !isValidAttachmentOptions(values) {
		return false
	}
	if !isValidAvatarOptions(values) {
		return false
	}
	return true
}

func isKnownOption(name string) bool {
	_, ok := optionDefinitionFor(name)
	return ok
}

func isPublicOption(name string) bool {
	definition, ok := optionDefinitionFor(name)
	return ok && definition.public
}

func isSecretOption(name string) bool {
	definition, ok := optionDefinitionFor(name)
	return ok && definition.secret
}

// encryptValue 加密敏感值；未配置 cipher 时透明返回原文（开发环境）。
func (s *Service) encryptValue(value string) (string, error) {
	if s.cipher == nil {
		return value, nil
	}
	return s.cipher.Encrypt(value)
}

// decryptValue 解密敏感值；未配置 cipher 或值为明文时透明返回原文（兼容历史明文）。
func (s *Service) decryptValue(value string) string {
	if s.cipher == nil {
		return value
	}
	decrypted, err := s.cipher.Decrypt(value)
	if err != nil {
		// 解密失败（密钥轮换/数据损坏）时回退原文，避免单个坏值拖垮全部 options 读取。
		return value
	}
	return decrypted
}

func optionDefinitionFor(name string) (optionDefinition, bool) {
	for _, definition := range optionDefinitions {
		if definition.name == name {
			return definition, true
		}
	}
	return optionDefinition{}, false
}

func actorCanManageAnyOption(actor identity.Actor) bool {
	for _, definition := range optionDefinitions {
		if actor.Can(definition.managePermission) {
			return true
		}
	}
	return false
}
