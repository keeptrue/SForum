package pages

import (
	"strings"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// productionThemeIslandBindings 是宿主公开主题岛的权威映射。
// 内容页 body 岛映射到 HostPageIsland（Nuxt 默认 slot），主题只控制壳层。
func productionThemeIslandBindings() map[string]themecompiler.IslandBinding {
	return map[string]themecompiler.IslandBinding{
		"sf-home-page":                  {ComponentID: "forum.component.home_page"},
		"sf-search-page":                {ComponentID: "forum.component.search_page"},
		"sf-category-index-page":        {ComponentID: "forum.component.category_index"},
		"sf-category-show-page":         {ComponentID: "forum.component.category_show"},
		"sf-tag-index-page":             {ComponentID: "forum.component.tag_index"},
		"sf-tag-show-page":              {ComponentID: "forum.component.tag_show"},
		"sf-topic-show-page":            {ComponentID: "forum.component.topic_show"},
		"sf-profile-page":               {ComponentID: "forum.component.profile_show"},
		"sf-notifications-page":         {ComponentID: "forum.component.notifications"},
		"sf-notification-detail-page":   {ComponentID: "forum.component.notification_detail"},
		"sf-moderation-review":          {ComponentID: "forum.component.moderation_review"},
		"sf-terms-page":                 {ComponentID: "site.component.terms"},
		"sf-privacy-page":               {ComponentID: "site.component.privacy"},
		"sf-guidelines-page":            {ComponentID: "site.component.guidelines"},
		"sf-not-found-page":             {ComponentID: "system.component.not_found"},
		"sf-error-details":              {ComponentID: "system.component.error_details"},
		"sf-error-actions":              {ComponentID: "system.component.error_actions"},
		"sf-error-recovery":             {ComponentID: "system.component.error_recovery"},
		"sf-error-sidebar":              {ComponentID: "system.component.error_sidebar"},
		"sf-error-rail":                 {ComponentID: "system.component.error_rail"},
		"sf-navbar":                     {ComponentID: "navigation.component.navbar"},
		"sf-footer":                     {ComponentID: "navigation.component.footer"},
		"sf-home-navigation":            {ComponentID: "navigation.component.home"},
		"sf-topic-composer":             {ComponentID: "forum.component.topic_composer"},
		"sf-topic-reply":                {ComponentID: "forum.component.topic_reply"},
		"sf-topic-editor":               {ComponentID: "forum.component.topic_editor"},
		"sf-profile-settings":           {ComponentID: "profile.component.settings_form"},
		"sf-appearance-settings":        {ComponentID: "identity.component.appearance_settings"},
		"sf-login-methods-settings":     {ComponentID: "identity.component.login_methods_settings"},
		"sf-local-password-settings":    {ComponentID: "identity.component.local_password_settings"},
		"sf-security-settings":          {ComponentID: "identity.component.security_settings"},
		"sf-personal-access-tokens":     {ComponentID: "identity.component.personal_access_tokens"},
		"sf-notification-settings":      {ComponentID: "notifications.component.settings"},
		"sf-login-form":                 {ComponentID: "identity.component.login_form"},
		"sf-register-form":              {ComponentID: "identity.component.register_form"},
		"sf-external-auth-continuation": {ComponentID: "identity.component.external_auth_continuation"},
		"sf-email-verification":         {ComponentID: "identity.component.email_verification"},
		"sf-recovery-request":           {ComponentID: "identity.component.recovery_request_form"},
		"sf-recovery-confirm":           {ComponentID: "identity.component.recovery_confirm_form"},
		"sf-plugin-page-body":           {ComponentID: "plugin.component.page_body"},
		// 插件公开设置页左栏岛：active prop 必填，插件模板只能传已声明的属性。
		// 插件页（如 /settings/oauth-apps）与 /settings/profile 因此共用同一份左栏实现，
		// 不会各自维护一份会漂移的导航标记。
		"sf-account-settings-sidebar": {
			ComponentID: "settings.component.account_sidebar",
			Props: []themecompiler.IslandPropContract{
				{Name: "active", Type: themecompiler.IslandPropString, Required: true},
			},
		},
		// 插件公开页内容列页脚岛：与宿主设置页同一份 SFContentColumnFooter，
		// 渲染在中列末尾，页脚出血宽度与内容列对齐（不越出视口）。
		"sf-content-column-footer": {ComponentID: "ui.component.content_footer"},
		"sf-extension-widget": {
			ComponentID:   "core.component.shared.sfextension_widget",
			AllowFallback: true,
			Props: []themecompiler.IslandPropContract{
				{Name: "extension-id", Type: themecompiler.IslandPropString, Required: true},
				{Name: "component-id", Type: themecompiler.IslandPropString, Required: true},
			},
		},
	}
}

// RequiredThemeBodyIslandTag 返回可替换公开页在 L1 模板中应嵌入的宿主 body 岛标签。
// 不可替换页返回空串。
func RequiredThemeBodyIslandTag(pageID string) string {
	switch strings.TrimSpace(pageID) {
	case "forum.home":
		return "sf-home-page"
	case "forum.search":
		return "sf-search-page"
	case "forum.category.index":
		return "sf-category-index-page"
	case "forum.category.show":
		return "sf-category-show-page"
	case "forum.tag.index":
		return "sf-tag-index-page"
	case "forum.tag.show":
		return "sf-tag-show-page"
	case "forum.topic.show":
		return "sf-topic-show-page"
	case "forum.topic.create":
		return "sf-topic-composer"
	case "forum.topic.reply":
		return "sf-topic-reply"
	case "forum.topic.edit":
		return "sf-topic-editor"
	case "forum.profile.show":
		return "sf-profile-page"
	case "forum.settings.profile":
		return "sf-profile-settings"
	case "forum.settings.appearance":
		return "sf-appearance-settings"
	case "forum.settings.login_methods":
		return "sf-login-methods-settings"
	case "forum.settings.password":
		return "sf-local-password-settings"
	case "forum.settings.security":
		return "sf-security-settings"
	case "forum.settings.tokens":
		return "sf-personal-access-tokens"
	case "forum.settings.notifications":
		return "sf-notification-settings"
	case "forum.notifications":
		return "sf-notifications-page"
	case "forum.notification.show":
		return "sf-notification-detail-page"
	case "moderation.review":
		return "sf-moderation-review"
	case "auth.login":
		return "sf-login-form"
	case "auth.register":
		return "sf-register-form"
	case "auth.external_continuation":
		return "sf-external-auth-continuation"
	case "auth.email_verification":
		return "sf-email-verification"
	case "auth.forgot_password":
		return "sf-recovery-request"
	case "auth.reset_password":
		return "sf-recovery-confirm"
	case "site.terms":
		return "sf-terms-page"
	case "site.privacy":
		return "sf-privacy-page"
	case "site.guidelines":
		return "sf-guidelines-page"
	case "system.forbidden", "system.not_found", "system.rate_limited", "system.server_error":
		return "sf-error-details"
	default:
		return ""
	}
}
