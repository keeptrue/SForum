<script setup lang="ts">
import { defineAsyncComponent, defineComponent, type Component } from 'vue'
import {
  parseLegacyThemeHTML,
  parseThemeRenderOutput,
  renderThemeRenderNodes,
  type ThemeRenderOutput
} from '~/composables/themes/useThemeRenderOutput'
import { applyPublicPageDocumentPolicy, fetchPublicPagePolicy } from '~/composables/errors/usePublicPageDocumentPolicy'
import type { PublicFrontendPolicy } from '~/runtime/public-extensions/pagePolicy'
import SFFooter from './SFFooter.vue'
import SFNavbar from './SFNavbar.vue'
import SFContentColumnFooter from './forum/SFContentColumnFooter.vue'
import SFNotFoundPageContent from './errors/SFNotFoundPageContent.vue'
import SFTopicShowPage from './forum/SFTopicShowPage.vue'
import SFSettingsAccountSidebar from './settings/SFSettingsAccountSidebar.vue'
import {
  collectPublicL2ComponentRefsFromRenderNodes,
  normalizePublicFrontendComponentRefs,
  type PublicFrontendComponentRef
} from '~/runtime/public-extensions/pagePolicy'

const props = defineProps<{
  html?: string
  renderOutput?: ThemeRenderOutput | null
  extensionId?: string
  dataSource?: string
  dataRoute?: string
  /** SSR resolve 注入的插件页面数据（唯一数据来源；禁止客户端再请求插件 route） */
  loaderData?: unknown
  loaderError?: string
  /** 页面区域(forum.page.regions)声明的 L2 widget refs；与主题岛 refs 合并后单次聚合 CSP */
  extraL2Refs?: PublicFrontendComponentRef[]
}>()

const { t } = useI18n()
const slots = useSlots()

// 受保护表单岛只决定核心页面在主题 HTML 中的位置，不接收凭证或会话 props。
const HostPageIsland = defineComponent({
  name: 'SFHostPageIsland',
  setup() {
    return () => slots.default?.() ?? []
  }
})

// 该映射与 ThemeRuntimeSnapshot 的 reviewed Component Registry 对齐。
// 系统 not_found 等仍可 HostPageIsland；公开 body 岛均为自包含 Host 组件。
const islandComponents: Record<string, Component> = {
  // 首页 body 岛由 SFHomePage 自包含数据+UI；主题 L1 拥有壳层结构。
  // 不再经 HostPageIsland 嵌回 pages/index slot（slot 仅 fail-closed 紧急回退）。
  'forum.component.home_page': defineAsyncComponent(() => import('./forum/SFHomePage.vue')),
  'forum.component.search_page': defineAsyncComponent(() => import('./forum/SFHomePage.vue')),
  'forum.component.category_index': defineAsyncComponent(() => import('./forum/SFCategoryIndexPage.vue')),
  'forum.component.category_show': defineAsyncComponent(() => import('./forum/SFCategoryShowPage.vue')),
  'forum.component.tag_index': defineAsyncComponent(() => import('./forum/SFTagIndexPage.vue')),
  'forum.component.badge_index': defineAsyncComponent(() => import('./forum/badges/SFBadgeCatalogPage.vue')),
  'forum.component.tag_show': defineAsyncComponent(() => import('./forum/SFTagShowPage.vue')),
  // 帖子详情是高频核心阅读路径，避免主题岛自身再制造一层异步导航边界。
  'forum.component.topic_show': SFTopicShowPage,
  // 主题外壳的正文岛：渲染受审查的正文出口，内容由外壳 provide。
  'plugin.component.page_body': defineAsyncComponent(() => import('./extensions/public/SFPluginPageBodyIsland.vue')),
  // 账号设置左栏岛：插件公开页与 /settings/profile 共用同一份左栏实现。
  // 该组件自身 await 数据，再套一层异步边界只会把 SSR 留空，因此同步导入。
  'settings.component.account_sidebar': SFSettingsAccountSidebar,
  // 内容列页脚岛：插件公开页在自身中列末尾声明，复用宿主设置页同款页脚实现。
  'ui.component.content_footer': SFContentColumnFooter,
  'forum.component.profile_show': defineAsyncComponent(() => import('./profile/SFProfileShowPage.vue')),
  'forum.component.notifications': defineAsyncComponent(() => import('./notifications/SFNotificationsPage.vue')),
  'forum.component.notification_detail': defineAsyncComponent(() => import('./notifications/detail/SFNotificationDetailPage.vue')),
  'forum.component.moderation_review': defineAsyncComponent(() => import('./moderation/SFModerationReviewPage.vue')),
  'site.component.terms': defineAsyncComponent(() => import('./legal/SFTermsPage.vue')),
  'site.component.privacy': defineAsyncComponent(() => import('./legal/SFPrivacyPage.vue')),
  'site.component.guidelines': defineAsyncComponent(() => import('./legal/SFGuidelinesPage.vue')),
  // 404 正在 Nuxt error boundary 内渲染，不能再引入异步岛边界而把 SSR 留空。
  'system.component.not_found': SFNotFoundPageContent,
  'navigation.component.navbar': SFNavbar,
  'navigation.component.footer': SFFooter,
  'navigation.component.home': defineAsyncComponent(() => import('./forum/SFHomeNavigation.vue')),
  'forum.component.topic_composer': defineAsyncComponent(() => import('./forum/SFTopicComposerPage.vue')),
  'forum.component.topic_reply': defineAsyncComponent(() => import('./forum/SFTopicReplyPage.vue')),
  'forum.component.topic_editor': defineAsyncComponent(() => import('./forum/SFTopicEditPage.vue')),
  'profile.component.settings_form': defineAsyncComponent(() => import('./settings/SFProfileSettingsPage.vue')),
  'identity.component.appearance_settings': defineAsyncComponent(() => import('./settings/SFAppearanceSettingsPage.vue')),
  'identity.component.login_methods_settings': defineAsyncComponent(() => import('./settings/SFLoginMethodsSettingsPage.vue')),
  'identity.component.local_password_settings': defineAsyncComponent(() => import('./settings/SFLocalPasswordSettingsPage.vue')),
  'identity.component.security_settings': defineAsyncComponent(() => import('./settings/SFSecuritySettingsPage.vue')),
  'identity.component.personal_access_tokens': defineAsyncComponent(() => import('./settings/SFPersonalAccessTokensPage.vue')),
  'notifications.component.settings': defineAsyncComponent(() => import('./settings/SFNotificationSettingsPage.vue')),
  'identity.component.login_form': defineAsyncComponent(() => import('./identity/SFLoginFormPage.vue')),
  'identity.component.register_form': defineAsyncComponent(() => import('./identity/SFRegisterFormPage.vue')),
  'identity.component.external_auth_continuation': defineAsyncComponent(() => import('./identity/SFExternalAuthContinuationPage.vue')),
  'identity.component.email_verification': defineAsyncComponent(() => import('./identity/SFEmailVerificationPage.vue')),
  'identity.component.recovery_request_form': defineAsyncComponent(() => import('./identity/SFRecoveryRequestPage.vue')),
  'identity.component.recovery_confirm_form': defineAsyncComponent(() => import('./identity/SFRecoveryConfirmPage.vue')),
  'core.component.shared.sfextension_widget': defineAsyncComponent(() => import('./SFExtensionWidget.vue'))
}
const legacyIslandBindings = {
  'sf-home-page': { componentId: 'forum.component.home_page' },
  'sf-search-page': { componentId: 'forum.component.search_page' },
  'sf-category-index-page': { componentId: 'forum.component.category_index' },
  'sf-category-show-page': { componentId: 'forum.component.category_show' },
  'sf-tag-index-page': { componentId: 'forum.component.tag_index' },
  'sf-badge-index-page': { componentId: 'forum.component.badge_index' },
  'sf-tag-show-page': { componentId: 'forum.component.tag_show' },
  'sf-topic-show-page': { componentId: 'forum.component.topic_show' },
  'sf-profile-page': { componentId: 'forum.component.profile_show' },
  'sf-notifications-page': { componentId: 'forum.component.notifications' },
  'sf-notification-detail-page': { componentId: 'forum.component.notification_detail' },
  'sf-moderation-review': { componentId: 'forum.component.moderation_review' },
  'sf-terms-page': { componentId: 'site.component.terms' },
  'sf-privacy-page': { componentId: 'site.component.privacy' },
  'sf-guidelines-page': { componentId: 'site.component.guidelines' },
  'sf-not-found-page': { componentId: 'system.component.not_found' },
  // 插件正文岛：主题外壳用它声明插件正文的位置。
  'sf-plugin-page-body': { componentId: 'plugin.component.page_body' },
  'sf-navbar': { componentId: 'navigation.component.navbar' },
  'sf-footer': { componentId: 'navigation.component.footer' },
  'sf-home-navigation': { componentId: 'navigation.component.home' },
  'sf-account-settings-sidebar': { componentId: 'settings.component.account_sidebar' },
  'sf-content-column-footer': { componentId: 'ui.component.content_footer' },
  'sf-topic-composer': { componentId: 'forum.component.topic_composer' },
  'sf-topic-reply': { componentId: 'forum.component.topic_reply' },
  'sf-topic-editor': { componentId: 'forum.component.topic_editor' },
  'sf-profile-settings': { componentId: 'profile.component.settings_form' },
  'sf-appearance-settings': { componentId: 'identity.component.appearance_settings' },
  'sf-login-methods-settings': { componentId: 'identity.component.login_methods_settings' },
  'sf-local-password-settings': { componentId: 'identity.component.local_password_settings' },
  'sf-security-settings': { componentId: 'identity.component.security_settings' },
  'sf-personal-access-tokens': { componentId: 'identity.component.personal_access_tokens' },
  'sf-notification-settings': { componentId: 'notifications.component.settings' },
  'sf-login-form': { componentId: 'identity.component.login_form' },
  'sf-register-form': { componentId: 'identity.component.register_form' },
  'sf-external-auth-continuation': { componentId: 'identity.component.external_auth_continuation' },
  'sf-email-verification': { componentId: 'identity.component.email_verification' },
  'sf-recovery-request': { componentId: 'identity.component.recovery_request_form' },
  'sf-recovery-confirm': { componentId: 'identity.component.recovery_confirm_form' },
  'sf-extension-widget': { componentId: 'core.component.shared.sfextension_widget' }
} as const
const allowedComponents = new Set(Object.keys(islandComponents))
const fallbackComponents = new Set(['core.component.shared.sfextension_widget'])

const renderState = computed(() => {
  try {
    const nodes = props.renderOutput
      ? parseThemeRenderOutput(props.renderOutput, { allowedComponents, fallbackComponents })
      : parseLegacyThemeHTML(props.html || '', legacyIslandBindings)
    return { nodes, error: false }
  } catch {
    return { nodes: [], error: true }
  }
})

// SSR：按页面实际 L2 岛聚合 Host document CSP。
// 策略不可用（信任撤销 / Safe Mode / 制品不可用）时不写 header；widget 自身回退 L1。
const publicL2Refs = computed(() => {
  const themeRefs = (() => {
    if (renderState.value.error) return []
    try {
      return collectPublicL2ComponentRefsFromRenderNodes(renderState.value.nodes)
    } catch {
      return []
    }
  })()
  // 区域 widget refs 与主题岛 refs 合并去重；单次 applyPublicPageDocumentPolicy 保证
  // 每响应恰好一份确定性 CSP 头（严禁下游各自聚合）。
  try {
    return normalizePublicFrontendComponentRefs([...themeRefs, ...(props.extraL2Refs ?? [])])
  } catch {
    return themeRefs
  }
})
// 仅在页面声明了 L2 岛时聚合 CSP，避免每个公开页多一次 404。
const publicPagePolicy = import.meta.server && publicL2Refs.value.length > 0
  ? await applyPublicPageDocumentPolicy(publicL2Refs.value)
  : null

// 客户端换页：策略读取只差一次同源 GET，让 <link> 预注入紧跟首帧（替代原先
// 描述符→资产→lease 的完整瀑布，样式闪变窗口从 ~250ms 压到一次 RTT）。
const clientPolicy = ref<PublicFrontendPolicy | null>(null)
if (import.meta.client && publicL2Refs.value.length > 0) {
  void fetchPublicPagePolicy(publicL2Refs.value).then((policy) => {
    if (policy) clientPolicy.value = policy
  })
}
const l2StyleLinks = computed(() => {
  const styles = publicPagePolicy?.styles ?? clientPolicy.value?.styles ?? []
  return styles.map(style => ({
    rel: 'stylesheet',
    href: style.assetPath,
    'data-sforum-extension': style.extensionId,
    'data-sforum-asset': style.handle,
    'data-sforum-digest': style.digest
  }))
})
useHead(() => ({ link: l2StyleLinks.value }))

const ThemeRenderTree = defineComponent({
  name: 'SFThemeRenderTree',
  setup() {
    return () => renderThemeRenderNodes(
      renderState.value.nodes,
      componentId => islandComponents[componentId]
    )
  }
})

const pluginDataError = computed(() => Boolean(props.loaderError))

</script>

<template>
  <div
    class="sf-theme-template"
    :data-extension-id="extensionId || ''"
    :data-document-policy-digest="publicPagePolicy?.documentPolicy.digest || undefined"
  >
    <SFAlert
      v-if="pluginDataError || renderState.error"
      variant="danger"
      class="mb-4"
      :title="t('errors.page.serviceUnavailable.title')"
    />
    <ThemeRenderTree v-else />
  </div>
</template>
