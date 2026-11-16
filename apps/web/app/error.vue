<script setup lang="ts">
import { useActiveThemeSkin } from '~/composables/themes/useActiveThemeSkin'
import { useAppliedAppearance } from '~/composables/appearance/useAppliedAppearance'
import { useSystemErrorPagePresentation } from '~/composables/errors/useSystemErrorPagePresentation'
import { useErrorPageShell } from '~/composables/errors/useErrorPageShell'
import SFSystemErrorPage from '~/components/errors/SFSystemErrorPage.vue'
import SFSystemErrorBootstrapFallback from '~/components/errors/SFSystemErrorBootstrapFallback.vue'
import SFErrorPageContent from '~/components/errors/SFErrorPageContent.vue'
import type { NuxtError } from '#app'
import {
  isThemeableSystemErrorStatus,
  resolveErrorPageContent,
  systemErrorPageIdForStatus
} from '~/utils/errors/errorPage'
import { localeHeadLinks, localeHeadMeta } from '~/utils/seo/localeHeadLinks'

const props = defineProps<{
  error: NuxtError
}>()
const nuxtError = computed(() => props.error)
const systemPage = computed(() => resolveErrorPageContent(nuxtError.value?.statusCode))
const isThemeableSystemError = computed(() => isThemeableSystemErrorStatus(nuxtError.value?.statusCode))
const systemPageId = computed(() => systemErrorPageIdForStatus(nuxtError.value?.statusCode))
// 错误页不能假设 i18n 插件跑完了：它一旦失败，useI18n 会抛 NOT_INSTALLED 并
// 把错误页变成二次故障，掩盖真正的原始错误。未就绪时整页降级为零依赖兜底。
const { i18nReady, t, localeHead } = useErrorPageShell()
const {
  siteName
} = useWebOptions()
const {
  appliedAppearanceTheme,
  appliedLightBackground,
  savedUserAppearance,
  userAppearancePreview
} = useAppliedAppearance()
const themeSkin = useActiveThemeSkin()
const systemTitle = computed(() => t(systemPage.value.titleKey, { siteName: siteName.value }))
const systemDescription = computed(() => t(systemPage.value.descriptionKey, { siteName: siteName.value }))

if (i18nReady && isThemeableSystemError.value) {
  useSeoMeta({
    title: systemTitle,
    description: systemDescription,
    robots: 'noindex,nofollow'
  })

  // Nuxt 的硬导航错误文档不会经过 app.vue，404 必须在根错误页补齐当前主题 L0。
  useHead(() => {
    const htmlAttrs: Record<string, string | undefined> = {
      ...localeHead.value.htmlAttrs,
      'data-sforum-theme': appliedAppearanceTheme.value.dataTheme,
      'data-sforum-light-background': appliedLightBackground.value,
      'data-sforum-user-appearance': savedUserAppearance.value ? 'custom' : 'site-default',
      'data-sforum-user-appearance-preview': userAppearancePreview.value ? 'active' : undefined
    }
    if (appliedAppearanceTheme.value.style) {
      htmlAttrs.style = [htmlAttrs.style, appliedAppearanceTheme.value.style]
        .filter(Boolean)
        .join('; ')
    }
    return {
      htmlAttrs,
      bodyAttrs: {
        'data-sforum-error': String(systemPage.value.statusCode)
      },
      link: [
        ...localeHeadLinks(localeHead.value.link),
        ...themeSkin.links.value.map(href => ({
          rel: 'stylesheet' as const,
          href,
          key: `sforum-theme-skin:${href}`,
          // 与 app.vue 同契约：皮肤永远排在宿主 CSS 之前。
          tagPriority: 'critical' as const,
          'data-sforum-theme-skin': '1'
        }))
      ],
      meta: localeHeadMeta(localeHead.value.meta)
    }
  })
}

// 服务端错误插件和资源路由在进入 error.vue 前准备最终快照；这里保持同步消费。
const systemPresentation = i18nReady && isThemeableSystemError.value ? useSystemErrorPagePresentation(systemPageId) : null
const resolvedSystemPage = shallowRef(systemPresentation?.resolvedPage.value || null)
if (import.meta.client && i18nReady && isThemeableSystemError.value && !resolvedSystemPage.value) {
  void systemPresentation!.prepare().then((resolved) => {
    resolvedSystemPage.value = resolved
  })
}
</script>

<template>
  <SFSystemErrorBootstrapFallback v-if="!i18nReady" :error="nuxtError" />
  <SFSystemErrorPage
    v-else-if="isThemeableSystemError"
    :error="nuxtError"
    :resolved-page="resolvedSystemPage"
  />
  <UApp v-else :toaster="{ position: 'bottom-right' }">
    <SFErrorPageContent :error="nuxtError" />
  </UApp>
</template>
