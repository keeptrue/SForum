package options

import (
	"net/mail"
	"net/url"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 副标题与管理邮箱的长度边界。
const (
	siteTaglineMaxRunes    = 160
	siteAdminEmailMaxRunes = 254
	siteAboutURLMaxRunes   = 2048
)

func init() {
	optionDefinitions = append(optionDefinitions, siteIdentityOptionDefinitions()...)
}

func siteIdentityOptionDefinitions() []optionDefinition {
	return []optionDefinition{
		{name: NameSiteDomain, public: true, managePermission: identity.PermissionSettingsSiteManage},
		{name: NameSiteAboutURL, public: true, managePermission: identity.PermissionSettingsSiteManage},
		{name: NameSiteAboutOpenInNewTab, public: true, managePermission: identity.PermissionSettingsSiteManage},
		// 标语对前台可见，用于导航/登录页。
		{name: NameSiteTagline, public: true, managePermission: identity.PermissionSettingsSiteManage},
		// 管理邮箱仅后台可读，降低公开爬取风险。
		{name: NameSiteAdminEmail, public: false, managePermission: identity.PermissionSettingsSiteManage},
	}
}

func siteIdentityRecommendedDefaults() map[string]string {
	return map[string]string{
		NameSiteDomain:            "127.0.0.1:3000",
		NameSiteAboutURL:          "",
		NameSiteAboutOpenInNewTab: enabledOptionValue(false),
		NameSiteTagline:           "",
		NameSiteAdminEmail:        "",
	}
}

func normalizeSiteDomain(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}

	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", false
	}
	if strings.Trim(parsed.Path, "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}

	return strings.ToLower(parsed.Host), true
}

func siteDomainFromURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err == nil && parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return "127.0.0.1:3000"
}

func normalizeSiteTagline(value string) (string, bool) {
	// 允许空；去掉首尾空白；限制 rune 长度。
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > siteTaglineMaxRunes {
		return "", false
	}
	return value, true
}

func normalizeSiteAboutURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > siteAboutURLMaxRunes {
		return "", false
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") &&
		!strings.HasPrefix(value, "/api") && !strings.HasPrefix(value, "/admin") &&
		!strings.ContainsAny(value, " \t\r\n") {
		return value, true
	}
	return value, isValidURL(value)
}

func normalizeSiteAdminEmail(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > siteAdminEmailMaxRunes {
		return "", false
	}
	// 与 identity 注册校验一致：net/mail 解析且整串即地址。
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", false
	}
	return value, true
}

func coerceSiteIdentityOptions(coerced map[string]string, defaults map[string]string) {
	if value, ok := normalizeSiteDomain(coerced[NameSiteDomain]); ok {
		coerced[NameSiteDomain] = value
	} else {
		coerced[NameSiteDomain] = defaults[NameSiteDomain]
	}
	if value, ok := normalizeSiteTagline(coerced[NameSiteTagline]); ok {
		coerced[NameSiteTagline] = value
	} else {
		coerced[NameSiteTagline] = defaults[NameSiteTagline]
	}
	if value, ok := normalizeSiteAboutURL(coerced[NameSiteAboutURL]); ok {
		coerced[NameSiteAboutURL] = value
	} else {
		coerced[NameSiteAboutURL] = defaults[NameSiteAboutURL]
	}
	if value, ok := normalizeEnabledOption(coerced[NameSiteAboutOpenInNewTab]); ok {
		coerced[NameSiteAboutOpenInNewTab] = value
	} else {
		coerced[NameSiteAboutOpenInNewTab] = defaults[NameSiteAboutOpenInNewTab]
	}
	if value, ok := normalizeSiteAdminEmail(coerced[NameSiteAdminEmail]); ok {
		coerced[NameSiteAdminEmail] = value
	} else {
		coerced[NameSiteAdminEmail] = defaults[NameSiteAdminEmail]
	}
}

func isValidSiteIdentityOptions(values map[string]string) bool {
	if _, ok := normalizeSiteDomain(values[NameSiteDomain]); !ok {
		return false
	}
	if _, ok := normalizeSiteTagline(values[NameSiteTagline]); !ok {
		return false
	}
	if _, ok := normalizeSiteAboutURL(values[NameSiteAboutURL]); !ok {
		return false
	}
	if _, ok := normalizeEnabledOption(values[NameSiteAboutOpenInNewTab]); !ok {
		return false
	}
	if _, ok := normalizeSiteAdminEmail(values[NameSiteAdminEmail]); !ok {
		return false
	}
	return true
}
