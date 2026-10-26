package options

import (
	"regexp"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 客户端（原生 App / 桌面端）版本策略。三个选项全部 public：客户端在冷启动读取
// GET /web-options 即可决定是否展示强制升级页，不需要额外端点，也不改变
// 浏览器契约（浏览器忽略这些键）。
//
// 语义边界：这里只声明运营意图。版本比较由客户端用自己的版本号完成，
// 服务端不代客户端判定 updateRequired（没有客户端版本上报面）。
const (
	clientVersionMaxRunes      = 32
	clientUpdateNoticeMaxRunes = 500
)

// 版本格式：1-4 段数字，可选 -prerelease / +build 后缀（如 1.2.3、1.2.3-beta.1）。
var clientVersionPattern = regexp.MustCompile(`^\d{1,6}(\.\d{1,6}){0,3}([-+][0-9A-Za-z.-]{1,16})?$`)

func init() {
	optionDefinitions = append(optionDefinitions, clientVersionOptionDefinitions()...)
}

func clientVersionOptionDefinitions() []optionDefinition {
	site := identity.PermissionSettingsSiteManage
	return []optionDefinition{
		{name: NameClientMinimumVersion, public: true, managePermission: site},
		{name: NameClientRecommendedVersion, public: true, managePermission: site},
		{name: NameClientUpdateNotice, public: true, managePermission: site},
	}
}

// clientVersionRecommendedDefaults 的安全默认是不限制：空最低版本不会把任何
// 已发布客户端挡在门外，运营必须显式设置才生效。
func clientVersionRecommendedDefaults() map[string]string {
	return map[string]string{
		NameClientMinimumVersion:     "",
		NameClientRecommendedVersion: "",
		NameClientUpdateNotice:       "",
	}
}

func mergeClientVersionDefaults(values map[string]string) {
	for name, value := range clientVersionRecommendedDefaults() {
		if _, exists := values[name]; !exists {
			values[name] = value
		}
	}
}

func normalizeClientVersion(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > clientVersionMaxRunes || !clientVersionPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func normalizeClientUpdateNotice(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > clientUpdateNoticeMaxRunes {
		return "", false
	}
	return value, true
}

// coerceClientVersionOptions 读取路径：脏数据回退到推荐默认，避免阻断启动。
func coerceClientVersionOptions(values, defaults map[string]string) {
	for _, name := range []string{NameClientMinimumVersion, NameClientRecommendedVersion} {
		if value, ok := normalizeClientVersion(values[name]); ok {
			values[name] = value
			continue
		}
		values[name] = defaults[name]
	}
	if value, ok := normalizeClientUpdateNotice(values[NameClientUpdateNotice]); ok {
		values[NameClientUpdateNotice] = value
		return
	}
	values[NameClientUpdateNotice] = defaults[NameClientUpdateNotice]
}

func isValidClientVersionOptions(values map[string]string) bool {
	if _, ok := normalizeClientVersion(values[NameClientMinimumVersion]); !ok {
		return false
	}
	if _, ok := normalizeClientVersion(values[NameClientRecommendedVersion]); !ok {
		return false
	}
	_, ok := normalizeClientUpdateNotice(values[NameClientUpdateNotice])
	return ok
}
