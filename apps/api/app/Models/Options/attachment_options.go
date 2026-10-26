package options

import (
	"path"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

const attachmentPathTemplateMaxRunes = 160
const attachmentListOptionMaxRunes = 1000
const attachmentProviderTextMaxRunes = 240

var attachmentProviders = []string{storage.ProviderLocal}
var attachmentVisibilities = []string{"public", "private"}

func attachmentOptionDefinitions() []optionDefinition {
	return []optionDefinition{
		{name: NameAttachmentProvider, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentUploadEnabled, public: true, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentPathTemplate, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentPublicBaseURL, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentMaxFileSizeMB, public: true, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentAllowedExtensions, public: true, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentAllowedMIMETypes, public: true, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentDefaultVisibility, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCleanupOrphanDays, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentLocalRoot, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentLocalPublicPrefix, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCompressionEnabled, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCompressionStrength, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCompressionMaxDimension, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCompressionMinSizeKB, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentCompressionMinSavingsPercent, managePermission: identity.PermissionAttachmentSettings},
		{name: NameAttachmentExtensionReferenceLimit, managePermission: identity.PermissionAttachmentSettings},
	}
}

func mergeAttachmentDefaults(values map[string]string) {
	values[NameAttachmentProvider] = storage.ProviderLocal
	values[NameAttachmentUploadEnabled] = enabledOptionValue(true)
	values[NameAttachmentPathTemplate] = "{yyyy}/{mm}/{dd}/{public_id}{ext}"
	values[NameAttachmentPublicBaseURL] = ""
	values[NameAttachmentMaxFileSizeMB] = "20"
	values[NameAttachmentAllowedExtensions] = ".jpg,.jpeg,.png,.gif,.webp,.pdf,.txt,.zip"
	values[NameAttachmentAllowedMIMETypes] = "image/jpeg,image/png,image/gif,image/webp,application/pdf,text/plain,application/zip"
	values[NameAttachmentDefaultVisibility] = "public"
	values[NameAttachmentCleanupOrphanDays] = "30"
	values[NameAttachmentLocalRoot] = "storage/app/attachments"
	values[NameAttachmentLocalPublicPrefix] = ""
	values[NameAttachmentCompressionEnabled] = enabledOptionValue(true)
	values[NameAttachmentCompressionStrength] = "55"
	values[NameAttachmentCompressionMaxDimension] = "2560"
	values[NameAttachmentCompressionMinSizeKB] = "256"
	values[NameAttachmentCompressionMinSavingsPercent] = "8"
	values[NameAttachmentExtensionReferenceLimit] = "500"
}

// attachmentActiveContentDenylist 是禁止作为公开附件存储的"主动内容"MIME 类型。
// 这些类型在浏览器同源 inline 响应下可执行脚本/HTML，构成存储型 XSS 或同源脚本执行面，
// 因此无论运营者如何配置允许列表都不应入库。content 响应层亦对它们强制下载作为兜底。
var attachmentActiveContentDenylist = map[string]bool{
	"text/html":              true,
	"text/xml":               true,
	"application/xhtml+xml":  true,
	"application/xml":        true,
	"image/svg+xml":          true,
	"application/javascript": true,
	"text/javascript":        true,
	"application/ecmascript": true,
	"text/ecmascript":        true,
}

// IsAttachmentActiveContentType 判断给定 MIME 是否属于主动内容类型，
// 供附件 content 响应决定是否强制下载（attachment）而非内联渲染（inline）。
func IsAttachmentActiveContentType(mimeType string) bool {
	_, blocked := attachmentActiveContentDenylist[strings.ToLower(strings.TrimSpace(mimeType))]
	return blocked
}

func attachmentOptionNames() []string {
	return []string{
		NameAttachmentProvider,
		NameAttachmentUploadEnabled,
		NameAttachmentPathTemplate,
		NameAttachmentPublicBaseURL,
		NameAttachmentMaxFileSizeMB,
		NameAttachmentAllowedExtensions,
		NameAttachmentAllowedMIMETypes,
		NameAttachmentDefaultVisibility,
		NameAttachmentCleanupOrphanDays,
		NameAttachmentLocalRoot,
		NameAttachmentLocalPublicPrefix,
		NameAttachmentCompressionEnabled,
		NameAttachmentCompressionStrength,
		NameAttachmentCompressionMaxDimension,
		NameAttachmentCompressionMinSizeKB,
		NameAttachmentCompressionMinSavingsPercent,
		NameAttachmentExtensionReferenceLimit,
	}
}

func coerceAttachmentOptions(values map[string]string, defaults map[string]string) {
	for _, name := range attachmentOptionNames() {
		normalized, ok := normalizeOptionValue(name, values[name])
		if !ok {
			values[name] = defaults[name]
			continue
		}
		values[name] = normalized
	}
}

func isValidAttachmentOptions(values map[string]string) bool {
	for _, name := range attachmentOptionNames() {
		if _, ok := normalizeOptionValue(name, values[name]); !ok {
			return false
		}
	}

	provider, ok := normalizeAttachmentProvider(values[NameAttachmentProvider])
	if !ok {
		return false
	}
	// E6.1：插件选择的凭证在 extension_settings，不要求 core 云字段齐全。
	// 启用/槽位合法性在 Attachments 服务写路径与 Probe 中 fail-closed 校验。
	if storage.IsPluginSelection(provider) || storage.IsInstanceSelection(provider) {
		return true
	}
	return provider == storage.ProviderLocal
}

func normalizeAttachmentProvider(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return storage.ProviderLocal, true
	}
	// E6.1：plugin:<extensionId> 作为 attachment.provider 合法值（语法级）。
	if storage.IsPluginSelection(value) {
		sel := storage.ParseSelection(value)
		if !sel.IsValidPluginSelection() {
			return "", false
		}
		// 扩展 id 字符集与 manifest id 对齐（小写字母数字与 ._-）。
		id := sel.ExtensionID
		if len(id) == 0 || len(id) > 80 {
			return "", false
		}
		for i, r := range id {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
			if i == 0 && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return "", false
			}
			if !ok {
				return "", false
			}
		}
		return sel.Raw, true
	}
	if storage.IsInstanceSelection(value) {
		sel := storage.ParseSelection(value)
		if !sel.IsValidInstanceSelection() || len(sel.InstanceID) != 36 {
			return "", false
		}
		for i, r := range sel.InstanceID {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if r != '-' {
					return "", false
				}
				continue
			}
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return "", false
			}
		}
		return sel.Raw, true
	}
	return normalizeStringChoice(value, attachmentProviders)
}

func normalizeAttachmentPathTemplate(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return "", false
	}
	if !strings.Contains(value, "{public_id}") {
		return "", false
	}
	if len([]rune(value)) > attachmentPathTemplateMaxRunes {
		return "", false
	}
	for _, part := range strings.Split(value, "/") {
		if strings.TrimSpace(part) == "" {
			return "", false
		}
	}
	return value, true
}

func normalizeAttachmentExtensions(value string) (string, bool) {
	parts := strings.Split(value, ",")
	seen := map[string]bool{}
	extensions := []string{}
	for _, part := range parts {
		item := strings.ToLower(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		if !strings.HasPrefix(item, ".") {
			item = "." + item
		}
		if len(item) < 2 || len(item) > 24 {
			return "", false
		}
		for _, char := range strings.TrimPrefix(item, ".") {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return "", false
			}
		}
		if !seen[item] {
			seen[item] = true
			extensions = append(extensions, item)
		}
	}
	joined := strings.Join(extensions, ",")
	return joined, joined != "" && len([]rune(joined)) <= attachmentListOptionMaxRunes
}

func normalizeAttachmentMIMETypes(value string) (string, bool) {
	parts := strings.Split(value, ",")
	seen := map[string]bool{}
	items := []string{}
	for _, part := range parts {
		item := strings.ToLower(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		// 全通配过于宽泛，且会与主动内容 denylist 交叉；拒绝配置。
		if item == "*/*" {
			return "", false
		}
		segments := strings.Split(item, "/")
		if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
			return "", false
		}
		for _, segment := range segments {
			if segment == "*" {
				continue
			}
			for _, char := range segment {
				if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '+' && char != '.' {
					return "", false
				}
			}
		}
		// 主动内容类型（HTML/SVG/JS 等）硬封禁：公开附件以同源 inline 响应返回，
		// 允许这些类型会直接形成存储型 XSS。运营者不可通过允许列表放开。
		if attachmentActiveContentDenylist[item] {
			return "", false
		}
		if !seen[item] {
			seen[item] = true
			items = append(items, item)
		}
	}
	joined := strings.Join(items, ",")
	return joined, joined != "" && len([]rune(joined)) <= attachmentListOptionMaxRunes
}

func normalizeAttachmentLocalRoot(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || len([]rune(value)) > attachmentProviderTextMaxRunes {
		return "", false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f || char == '<' || char == '>' {
			return "", false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", false
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "/" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}
