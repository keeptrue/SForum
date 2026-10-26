package options

import (
	"strconv"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// Wave 2 品牌与法律正文：logo/favicon/apple-touch + terms/privacy/guidelines Markdown。
// 与 site_identity / community_policy 一样用 init 追加定义，避免继续膨胀 service.go。

const (
	// 品牌资源 URL 上限（含相对路径）。
	brandAssetURLMaxRunes = 500
	// 法律页 Markdown 正文上限（运营可写完整 stub，仍限制体积）。
	legalBodyMaxRunes = 50000
)

func init() {
	optionDefinitions = append(optionDefinitions, siteBrandOptionDefinitions()...)
}

func siteBrandOptionDefinitions() []optionDefinition {
	site := identity.PermissionSettingsSiteManage
	return []optionDefinition{
		// 品牌资源对前台公开，便于 navbar / <link rel="icon"> 直接读取。
		{name: NameSiteLogoURL, public: true, managePermission: site},
		{name: NameSiteLogoAttachmentID, public: true, managePermission: site},
		{name: NameSiteFaviconURL, public: true, managePermission: site},
		{name: NameSiteFaviconAttachmentID, public: true, managePermission: site},
		{name: NameSiteAppleTouchIconURL, public: true, managePermission: site},
		{name: NameSiteAppleTouchIconAttachmentID, public: true, managePermission: site},
		// 法律正文 public，静态法律页 SSR 无需 admin 会话。
		{name: NameLegalTermsBodyZHCN, public: true, managePermission: site},
		{name: NameLegalTermsBodyENUS, public: true, managePermission: site},
		{name: NameLegalPrivacyBodyZHCN, public: true, managePermission: site},
		{name: NameLegalPrivacyBodyENUS, public: true, managePermission: site},
		{name: NameLegalGuidelinesBodyZHCN, public: true, managePermission: site},
		{name: NameLegalGuidelinesBodyENUS, public: true, managePermission: site},
	}
}

func siteBrandRecommendedDefaults() map[string]string {
	return map[string]string{
		NameSiteLogoURL:                    "",
		NameSiteLogoAttachmentID:           "",
		NameSiteFaviconURL:                 "",
		NameSiteFaviconAttachmentID:        "",
		NameSiteAppleTouchIconURL:          "",
		NameSiteAppleTouchIconAttachmentID: "",
		// 推荐 stub：短、可运营、不依赖外部律师文案；空站也能打开法律页。
		NameLegalTermsBodyZHCN:      recommendedLegalTermsZHCN,
		NameLegalTermsBodyENUS:      recommendedLegalTermsENUS,
		NameLegalPrivacyBodyZHCN:    recommendedLegalPrivacyZHCN,
		NameLegalPrivacyBodyENUS:    recommendedLegalPrivacyENUS,
		NameLegalGuidelinesBodyZHCN: recommendedLegalGuidelinesZHCN,
		NameLegalGuidelinesBodyENUS: recommendedLegalGuidelinesENUS,
	}
}

const (
	recommendedLegalTermsZHCN = `## 服务条款

欢迎使用本社区。使用本站服务即表示你同意遵守本条款与适用法律法规。

1. 你应对自己发布的内容负责，不得侵犯他人权利。
2. 运营者可在必要时调整服务或暂停账号以维护社区秩序。
3. 本条款可能更新；重大变更将通过站内公告等方式提示。

如有疑问，请通过站点管理邮箱联系运营方。`

	recommendedLegalTermsENUS = `## Terms of Service

Welcome to this community. By using the site you agree to these terms and applicable law.

1. You are responsible for content you post and must not infringe others' rights.
2. Operators may adjust the service or suspend accounts to protect the community.
3. These terms may be updated; material changes will be announced on the site.

Contact the site operator via the admin email if you have questions.`

	recommendedLegalPrivacyZHCN = `## 隐私政策

我们仅收集运营社区所必需的信息（例如账号、会话与内容数据）。

1. 账号与登录相关数据用于身份验证与安全。
2. 你在公开区域发布的内容对其他访客可见。
3. 我们不会出售你的个人信息；在法律要求或保护安全时可能披露必要信息。

你可联系运营方行使访问、更正或删除等合理请求（受法律与备份策略约束）。`

	recommendedLegalPrivacyENUS = `## Privacy Policy

We collect only what is needed to run the community (for example account, session, and content data).

1. Account and login data are used for authentication and security.
2. Content you post in public areas is visible to other visitors.
3. We do not sell your personal information; we may disclose data when required by law or to protect safety.

Contact the operator for reasonable access, correction, or deletion requests (subject to law and backups).`

	recommendedLegalGuidelinesZHCN = `## 社区指南

请保持友善、就事论事，共同维护可讨论的空间。

1. 禁止垃圾广告、骚扰、仇恨与违法内容。
2. 尊重他人；引用或转载请注明来源。
3. 版主与管理员可按规则处理违规内容与账号。

感谢你的参与，让社区更好。`

	recommendedLegalGuidelinesENUS = `## Community Guidelines

Be kind and constructive so discussion stays useful for everyone.

1. No spam, harassment, hate, or illegal content.
2. Respect others; credit sources when quoting or reposting.
3. Moderators and admins may act on content and accounts under site rules.

Thank you for helping keep the community healthy.`
)

func mergeSiteBrandDefaults(values map[string]string) {
	for name, value := range siteBrandRecommendedDefaults() {
		if _, exists := values[name]; !exists {
			values[name] = value
		}
	}
}

func coerceSiteBrandOptions(coerced, defaults map[string]string) {
	for _, name := range []string{
		NameSiteLogoURL,
		NameSiteFaviconURL,
		NameSiteAppleTouchIconURL,
	} {
		if value, ok := normalizeBrandAssetURL(coerced[name]); ok {
			coerced[name] = value
		} else {
			coerced[name] = defaults[name]
		}
	}
	for _, name := range []string{
		NameSiteLogoAttachmentID,
		NameSiteFaviconAttachmentID,
		NameSiteAppleTouchIconAttachmentID,
	} {
		if value, ok := normalizeAttachmentIDOption(coerced[name]); ok {
			coerced[name] = value
		} else {
			coerced[name] = defaults[name]
		}
	}
	for _, name := range legalBodyOptionNames() {
		if value, ok := normalizeLegalBody(coerced[name]); ok {
			coerced[name] = value
		} else {
			coerced[name] = defaults[name]
		}
	}
}

func isValidSiteBrandOptions(values map[string]string) bool {
	for _, name := range []string{
		NameSiteLogoURL,
		NameSiteFaviconURL,
		NameSiteAppleTouchIconURL,
	} {
		if _, ok := normalizeBrandAssetURL(values[name]); !ok {
			return false
		}
	}
	for _, name := range []string{
		NameSiteLogoAttachmentID,
		NameSiteFaviconAttachmentID,
		NameSiteAppleTouchIconAttachmentID,
	} {
		if _, ok := normalizeAttachmentIDOption(values[name]); !ok {
			return false
		}
	}
	for _, name := range legalBodyOptionNames() {
		if _, ok := normalizeLegalBody(values[name]); !ok {
			return false
		}
	}
	return true
}

func normalizeSiteBrandOption(name, value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch name {
	case NameSiteLogoURL, NameSiteFaviconURL, NameSiteAppleTouchIconURL:
		return normalizeBrandAssetURL(value)
	case NameSiteLogoAttachmentID, NameSiteFaviconAttachmentID, NameSiteAppleTouchIconAttachmentID:
		return normalizeAttachmentIDOption(value)
	case NameLegalTermsBodyZHCN, NameLegalTermsBodyENUS,
		NameLegalPrivacyBodyZHCN, NameLegalPrivacyBodyENUS,
		NameLegalGuidelinesBodyZHCN, NameLegalGuidelinesBodyENUS:
		return normalizeLegalBody(value)
	default:
		return "", false
	}
}

func legalBodyOptionNames() []string {
	return []string{
		NameLegalTermsBodyZHCN,
		NameLegalTermsBodyENUS,
		NameLegalPrivacyBodyZHCN,
		NameLegalPrivacyBodyENUS,
		NameLegalGuidelinesBodyZHCN,
		NameLegalGuidelinesBodyENUS,
	}
}

// normalizeBrandAssetURL 允许空、站内相对路径或 http(s) 绝对 URL。
func normalizeBrandAssetURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if utf8.RuneCountInString(value) > brandAssetURLMaxRunes {
		return "", false
	}
	// 站内路径（如 /media/logo.png）；拒绝协议相对 //evil.com。
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value, true
	}
	return normalizeOptionalURL(value)
}

// normalizeAttachmentIDOption 存 attachments.id 的十进制字符串；空表示未绑定。
func normalizeAttachmentIDOption(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return "", false
	}
	return strconv.FormatInt(id, 10), true
}

// normalizeLegalBody 允许空（运营可清空 stub）；仅限制长度，内容渲染侧再消毒。
func normalizeLegalBody(value string) (string, bool) {
	// 保留运营输入的换行；仅去掉首尾空白。
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > legalBodyMaxRunes {
		return "", false
	}
	return value, true
}
