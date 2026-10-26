package options

import (
	"strconv"
	"strings"

	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// applyDefaultsOverrides 把部署期 Defaults（站点标识、语言、人机验证密钥与参数）
// 覆盖到推荐默认之上。从 service.go 抽出的聚焦协作函数：service.go 属于遗留大文件
// 基线，新增逻辑不得继续堆在里面。
func applyDefaultsOverrides(values map[string]string, defaults Defaults) {
	if value := strings.TrimSpace(defaults.SiteName); value != "" {
		values[NameSiteName] = value
	}
	if value := strings.TrimSpace(defaults.SiteURL); isValidURL(value) {
		values[NameSiteURL] = value
	}
	values[NameSiteDomain] = siteDomainFromURL(values[NameSiteURL])
	if len(defaults.SupportedLocales) > 0 {
		if locales := normalizeLocaleList(defaults.SupportedLocales); len(locales) > 0 {
			values[NameSiteSupportedLocales] = strings.Join(locales, ",")
		}
	}
	supported := parseStoredLocales(values[NameSiteSupportedLocales])
	if value, ok := normalizeLocaleChoice(defaults.DefaultLocale, supported); ok {
		values[NameSiteDefaultLocale] = value
	} else if len(supported) > 0 {
		values[NameSiteDefaultLocale] = supported[0]
	}
	if value, ok := normalizeHumanVerificationProvider(defaults.HumanVerificationProvider); ok {
		values[NameHumanVerificationProvider] = value
	}
	if value := strings.TrimSpace(defaults.AltchaSecret); value != "" {
		values[NameAltchaSecret] = value
	}
	if defaults.AltchaChallengeTTL > 0 {
		values[NameAltchaChallengeTTL] = defaults.AltchaChallengeTTL.String()
	}
	if defaults.AltchaCost > 0 {
		values[NameAltchaCost] = strconv.Itoa(defaults.AltchaCost)
	}
	if values[NameHumanVerificationProvider] == humanverify.ProviderAltcha && values[NameAltchaSecret] == "" {
		values[NameHumanVerificationProvider] = humanverify.ProviderDisabled
	}
}
