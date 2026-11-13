<script setup lang="ts">
import { useActiveThemeSkin } from '~/composables/themes/useActiveThemeSkin'
import { useExternalAuthFeedback } from '~/composables/identity/useExternalAuthFeedback'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useUserLanguage } from '~/composables/identity/useUserLanguage'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import SFApiConnectionModal from '~/components/errors/SFApiConnectionModal.vue'
import SFCelebrationHost from '~/components/notifications/SFCelebrationHost.vue'
import { useAdminTabs } from '~/composables/admin/useAdminTabs'
import { useAdminAppearancePreview } from '~/composables/admin/settings/useAdminAppearancePreview'
import { useAppliedAppearance } from '~/composables/appearance/useAppliedAppearance'
import { usePublicContentStyles } from '~/composables/extensions/usePublicContentStyles'

// no_prefix：中英共用 URL，不输出 hreflang 交替链接（同 URL 多语 SEO 无效）。
// 仍保留 html lang/dir，供无障碍与浏览器语言提示。
const localeHead = useLocaleHead({
  dir: true,
  lang: true,
  seo: false
})
const {
  siteName,
  siteFaviconUrl,
  siteAppleTouchIconUrl,
  refresh: refreshWebOptions
} = useWebOptions()
const { refresh: refreshAuthSession, setUser: setAuthUser } = useAuthSession()
const { applyStoredLanguage } = useUserLanguage()
// 消费 OAuth 回调 `ext_auth`：成功 Toast 在任意回跳页生效；登录/注册壳另读 alert。
const {
  displayAlertMessage: globalExternalAuthAlert,
  displayAlertVariant: globalExternalAuthAlertVariant,
  consumeFromRoute: consumeExternalAuthFeedback
} = useExternalAuthFeedback({ surface: 'global' })
const route = useRoute()
const adminRoutes = useAdminRoutes()
const { preview: adminAppearancePreview } = useAdminAppearancePreview()
const themeSkin = useActiveThemeSkin()
const publicContentStyles = usePublicContentStyles()
const startupOptionsTimeout = import.meta.dev ? 800 : 2000
const hasServerSession = import.meta.server
  && /(?:^|;\s*)sforum_session=/.test(useRequestHeaders(['cookie']).cookie || '')

// 引入页签缓存控制列表
const { cachedTabNames } = useAdminTabs()
const isAdminRoute = computed(() => adminRoutes.routeId(route.path) !== null)
const activeAdminAppearancePreview = computed(() =>
  adminAppearancePreview.value && isAdminRoute.value ? adminAppearancePreview.value : null
)
const {
  appliedAppearanceTheme,
  appliedLightBackground,
  savedUserAppearance,
  userAppearancePreview
} = useAppliedAppearance(activeAdminAppearancePreview)

async function refreshStartupState(options: { restoreAuth: boolean }) {
  // 开发热重载时 API 可能还在编译，首屏先使用本地默认状态。
  const tasks: Array<Promise<unknown>> = [
    refreshWebOptions({ timeout: startupOptionsTimeout }).catch(() => null)
  ]

  if (options.restoreAuth) {
    tasks.push(refreshAuthSession({ timeout: startupOptionsTimeout }))
  }

  await Promise.all(tasks)
  await applyStoredLanguage()
  return true
}

async function syncThemeSkin() {
  if (adminRoutes.routeId(route.path) !== null) {
    // 管理端隔离公开主题 CSS；内存中仍保留 lastPublic，离开时立刻恢复。
    themeSkin.clear()
    return
  }
  // 先贴上次成功的皮肤，再后台刷新，避免 API 慢时闪回 host baseline。
  themeSkin.restoreLastPublic()
  await themeSkin.refresh()
}

async function syncPublicContentStyles() {
  if (adminRoutes.routeId(route.path) !== null) {
    publicContentStyles.clear()
    return
  }
  await publicContentStyles.refresh()
}

if (import.meta.server) {
  // 无会话 Cookie 的请求可在 SSR 阶段确定为访客，避免公开 chrome 首屏留白。
  if (!hasServerSession) {
    setAuthUser(null)
  }
  // 带会话的页面由 public-session-cache 禁止共享缓存，可以安全输出登录态首屏。
  await useAsyncData('app-startup', async () => {
    await Promise.all([
      refreshStartupState({ restoreAuth: hasServerSession }),
      syncThemeSkin(),
      syncPublicContentStyles()
    ])
    return true
  })
} else {

  watch(() => route.path, () => {
    void syncThemeSkin()
    void syncPublicContentStyles()
  }, { flush: 'post' })
  // 浏览器挂载后再恢复会话，避免复用 SSR 的 app-startup payload 时跳过 auth 刷新。
  onMounted(() => {
    void refreshStartupState({ restoreAuth: true })
    // 公共主题 L0 皮肤不得进入独立的管理端样式边界。
    void syncThemeSkin()
    void syncPublicContentStyles()
    void consumeExternalAuthFeedback()
  })
  watch(() => route.fullPath, () => {
    void consumeExternalAuthFeedback()
  })
}

useHead(() => {
  const htmlAttrs: Record<string, string | undefined> = {
    ...localeHead.value.htmlAttrs,
    'data-sforum-theme': appliedAppearanceTheme.value.dataTheme,
    'data-sforum-light-background': appliedLightBackground.value,
    'data-sforum-admin-appearance-preview': adminAppearancePreview.value && isAdminRoute.value ? 'active' : undefined,
    'data-sforum-user-appearance': savedUserAppearance.value ? 'custom' : 'site-default',
    'data-sforum-user-appearance-preview': userAppearancePreview.value ? 'active' : undefined
  }
  const themeStyle = appliedAppearanceTheme.value.style
  if (themeStyle) {
    htmlAttrs.style = [htmlAttrs.style, themeStyle].filter(Boolean).join('; ')
  }

  // favicon 已在 useWebOptions 中解析 Core 默认值；Apple Touch icon 仍仅按运营配置注入。
  const brandLinks: Array<Record<string, string>> = []
  if (siteFaviconUrl.value) {
    brandLinks.push({ rel: 'icon', href: siteFaviconUrl.value })
  }
  if (siteAppleTouchIconUrl.value) {
    brandLinks.push({ rel: 'apple-touch-icon', href: siteAppleTouchIconUrl.value })
  }
  const themeLinks = themeSkin.links.value.map(href => ({
    rel: 'stylesheet',
    href,
    key: `sforum-theme-skin:${href}`,
    // 皮肤是基底、宿主 CSS 细化：固定在 head 最前，避免管理端往返重挂后反超宿主。
    tagPriority: 'critical' as const,
    'data-sforum-theme-skin': '1'
  }))
  // 扩展样式不声明 crossorigin：同源时 integrity 仍校验，被 QQ X5 云加速/CDN
  // 换源时降级为 no-cors 加载，避免 CORS 拒载后整块样式丢失。
  const extensionStyleLinks = publicContentStyles.links.value.map(style => ({
    rel: 'stylesheet',
    href: style.href,
    integrity: style.integrity,
    key: `sforum-extension-style:${style.handle}`,
    'data-sforum-extension-style': style.handle
  }))

  return {
    htmlAttrs,
    link: [...(localeHead.value.link || []), ...brandLinks, ...themeLinks, ...extensionStyleLinks],
    meta: localeHead.value.meta,
    // 公开页面的完整标题由 useSForumSeo/resolveSEO 负责；根组件只提供空标题回退。
    titleTemplate: title => title || siteName.value
  }
})
</script>

<template>
  <UApp :toaster="{ position: 'bottom-right' }">
    <NuxtLoadingIndicator
      color="var(--sf-accent)"
      :height="3"
      :throttle="80"
    />
    <NuxtLayout>
      <NuxtPage :keepalive="{ include: cachedTabNames }" />
    </NuxtLayout>
    <div
      v-if="globalExternalAuthAlert"
      class="pointer-events-auto fixed inset-x-4 top-4 z-[100] mx-auto max-w-xl"
    >
      <SFAlert
        :title="globalExternalAuthAlert"
        :variant="globalExternalAuthAlertVariant"
        compact
      />
    </div>
    <SFApiConnectionModal />
    <!-- 站内庆祝浮卡：左下角，与右下角 toast 不重叠；内部自行 gate 登录态/功能开关/管理端路由。 -->
    <SFCelebrationHost />
  </UApp>
</template>
