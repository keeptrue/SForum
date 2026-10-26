<script setup lang="ts">
import type { PageResolvePayload } from '~/utils/pageResolve'
import type { PublicFrontendComponentRef } from '~/runtime/public-extensions/pagePolicy'

const props = defineProps<{
  page: string
  resolved?: PageResolvePayload | null
  resolveError?: unknown
  /** 页面级 widget refs（区域 + 页面自声明，例如资料页 tab）：仅主题模板路径需要，
   * 由 SFThemeTemplate 合并进单次 CSP 聚合；原生路径的 CSP 已在 SFPageOutletResolver 聚合。 */
  regionWidgetRefs?: PublicFrontendComponentRef[]
}>()

const provider = computed(() => props.resolved?.provider || 'core')
const hasResolved = computed(() => Boolean(props.resolved))
const templateHtml = computed(() => (props.resolved?.templateHtml || '').trim())
const renderOutput = computed(() => props.resolved?.renderOutput)
const useTemplate = computed(() =>
  provider.value !== 'core'
  && Boolean(renderOutput.value || templateHtml.value)
  && !props.resolved?.fallback
)
const showFallbackNotice = computed(() => Boolean(props.resolved?.fallback || props.resolveError))

const useHostPublicChrome = computed(() => {
  if (!hasResolved.value) {
    return false
  }
  if (useTemplate.value) {
    return false
  }
  const id = props.page
  return !id.startsWith('auth.') && !id.startsWith('system.') && id !== 'dev.components'
})
</script>

<template>
  <div
    class="sf-page-outlet"
    :data-page="page"
    :data-provider="provider"
    :data-template="useTemplate ? '1' : '0'"
    :data-host-chrome="useHostPublicChrome ? '1' : '0'"
  >
    <SFSystemThemeTemplate
      v-if="useTemplate && page.startsWith('system.')"
      :html="templateHtml"
      :render-output="renderOutput"
      :extension-id="resolved?.extensionId || provider"
    />
    <SFThemeTemplate
      v-else-if="useTemplate"
      :html="templateHtml"
      :render-output="renderOutput"
      :extension-id="resolved?.extensionId || provider"
      :data-source="resolved?.dataSource"
      :data-route="resolved?.dataRoute"
      :loader-data="resolved?.loaderData"
      :loader-error="resolved?.loaderError"
      :extra-l2-refs="regionWidgetRefs"
    >
      <slot />
    </SFThemeTemplate>
    <SFHostPublicChrome v-else-if="useHostPublicChrome">
      <slot />
    </SFHostPublicChrome>
    <slot v-else />
    <p v-if="showFallbackNotice" class="sr-only">
      page registry fallback to core
    </p>
  </div>
</template>
