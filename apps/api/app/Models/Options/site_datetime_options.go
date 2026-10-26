package options

import (
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	timezone "github.com/keeptrue/sforum/apps/api/app/Support/Timezone"
)

// 推荐默认：UTC 时区 + 中文社区常用的年月日与 24 小时制 + 周一为一周起始。
const (
	recommendedSiteTimezone    = "UTC"
	recommendedSiteDateFormat  = "Y-m-d"
	recommendedSiteTimeFormat  = "H:i"
	recommendedSiteStartOfWeek = 1
)

// 日期格式预设（白名单 key，非任意 pattern）。
// 命名对齐常见 CMS 简写，便于运营理解。
var allowedSiteDateFormats = map[string]struct{}{
	"Y-m-d":    {}, // 2026-07-12
	"Y/m/d":    {}, // 2026/07/12
	"d/m/Y":    {}, // 12/07/2026
	"m/d/Y":    {}, // 07/12/2026
	"M j, Y":   {}, // Jul 12, 2026
	"j M Y":    {}, // 12 Jul 2026
	"relative": {}, // 相对时间（前端用 relative，后端仅存 key）
}

// 时间格式预设。
var allowedSiteTimeFormats = map[string]struct{}{
	"H:i":    {}, // 14:30
	"H:i:s":  {}, // 14:30:05
	"g:i a":  {}, // 2:30 pm
	"g:i A":  {}, // 2:30 PM
	"hidden": {}, // 仅日期、不显示时间
}

func init() {
	// 插在 site.* 基础项之后更清晰；append 顺序不影响查找。
	optionDefinitions = append(optionDefinitions, siteDateTimeOptionDefinitions()...)
}

func siteDateTimeOptionDefinitions() []optionDefinition {
	return []optionDefinition{
		{name: NameSiteTimezone, public: true, managePermission: identity.PermissionSettingsSiteManage},
		{name: NameSiteDateFormat, public: true, managePermission: identity.PermissionSettingsSiteManage},
		{name: NameSiteTimeFormat, public: true, managePermission: identity.PermissionSettingsSiteManage},
		{name: NameSiteStartOfWeek, public: true, managePermission: identity.PermissionSettingsSiteManage},
	}
}

func siteDateTimeRecommendedDefaults() map[string]string {
	return map[string]string{
		NameSiteTimezone:    recommendedSiteTimezone,
		NameSiteDateFormat:  recommendedSiteDateFormat,
		NameSiteTimeFormat:  recommendedSiteTimeFormat,
		NameSiteStartOfWeek: strconv.Itoa(recommendedSiteStartOfWeek),
	}
}

func normalizeSiteTimezone(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	// 通过 Support/Timezone 加载 IANA 时区（该包内嵌 tzdata，容器内无系统
	// zoneinfo 时仍可用）；无效名称直接拒绝。
	if _, err := timezone.Load(value); err != nil {
		return "", false
	}
	return value, true
}

func normalizeSiteDateFormat(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if _, ok := allowedSiteDateFormats[value]; !ok {
		return "", false
	}
	return value, true
}

func normalizeSiteTimeFormat(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if _, ok := allowedSiteTimeFormats[value]; !ok {
		return "", false
	}
	return value, true
}

func normalizeSiteStartOfWeek(value string) (string, bool) {
	return normalizeBoundedInt(value, 0, 6)
}

func coerceSiteDateTimeOptions(coerced map[string]string, defaults map[string]string) {
	if value, ok := normalizeSiteTimezone(coerced[NameSiteTimezone]); ok {
		coerced[NameSiteTimezone] = value
	} else {
		coerced[NameSiteTimezone] = defaults[NameSiteTimezone]
	}
	if value, ok := normalizeSiteDateFormat(coerced[NameSiteDateFormat]); ok {
		coerced[NameSiteDateFormat] = value
	} else {
		coerced[NameSiteDateFormat] = defaults[NameSiteDateFormat]
	}
	if value, ok := normalizeSiteTimeFormat(coerced[NameSiteTimeFormat]); ok {
		coerced[NameSiteTimeFormat] = value
	} else {
		coerced[NameSiteTimeFormat] = defaults[NameSiteTimeFormat]
	}
	if value, ok := normalizeSiteStartOfWeek(coerced[NameSiteStartOfWeek]); ok {
		coerced[NameSiteStartOfWeek] = value
	} else {
		coerced[NameSiteStartOfWeek] = defaults[NameSiteStartOfWeek]
	}
}

func isValidSiteDateTimeOptions(values map[string]string) bool {
	if _, ok := normalizeSiteTimezone(values[NameSiteTimezone]); !ok {
		return false
	}
	if _, ok := normalizeSiteDateFormat(values[NameSiteDateFormat]); !ok {
		return false
	}
	if _, ok := normalizeSiteTimeFormat(values[NameSiteTimeFormat]); !ok {
		return false
	}
	if _, ok := normalizeSiteStartOfWeek(values[NameSiteStartOfWeek]); !ok {
		return false
	}
	return true
}
