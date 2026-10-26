<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useNotFoundPagePresentation } from '~/composables/errors/useNotFoundPagePresentation'
import { applyPublicPageDocumentPolicy } from '~/composables/errors/usePublicPageDocumentPolicy'
import type { ThemeRenderOutput } from '~/composables/themes/useThemeRenderOutput'
import {
  coreResolveFallback,
  disableSharedPageCacheForPageResolve,
  PAGE_RESOLVE_REASON,
  isPageResolveSemanticNotFound,
  requestPageResolveWithRetry,
  shouldDisablePageResolveSharedCache,
  type MutableRouteRulesContext,
  type PageResolvePayload
} from '~/utils/pageResolve'
import {
  collectRegionWidgetRefs,
  fetchPageRegions,
  PAGE_REGION_PAGES,
  usePageRegionsState
} from '~/composables/pages/usePageRegions'
import {
  normalizePublicFrontendComponentRefs,
  type PublicFrontendComponentRef
} from '~/runtime/public-extensions/pagePolicy'

const props = defineProps<{
  page: string
  /** 页面级 L2 refs（非区域来源，例如资料页 widget tab）：与区域 refs 一起参与同一次 CSP 聚合。 */
  extraL2Refs?: PublicFrontendComponentRef[]
}>()

const route = useRoute()
const { locale } = useI18n()
const { user } = useAuthSession()
const { webOption } = useWebOptions()
const notFoundPresentation = useNotFoundPagePresentation()
const responseCacheControl = import.meta.server
  ? useResponseHeader('cache-control')
  : undefined
const requestEvent = import.meta.server ? useRequestEvent() : undefined
const registryEnabled = computed(() => {
  const raw = String(webOption('pages.registry_enabled', 'enabled')).toLowerCase()
  return raw === 'enabled' || raw === 'true' || raw === '1'
})

const PAGE_RESOLVE_TIMEOUT_MS = import.meta.dev ? 5000 : 8000
const PAGE_RESOLVE_RETRY_DELAY_MS = import.meta.dev ? 180 : 120

const requestQuery = computed(() => {
  const query = new URLSearchParams()
  for (const key of Object.keys(route.query).sort()) {
    const raw = route.query[key]
    const values = Array.isArray(raw) ? raw : [raw]
    for (const value of values) {
      if (value !== null && value !== undefined) {
        query.append(key, String(value))
      }
    }
  }
  return query.toString()
})

const resolveLocale = computed(() => String(locale.value || 'zh-CN'))
const resolveActorKey = computed(() => user.value?.id ? `user:${user.value.id}` : 'guest')
const resolveKey = computed(() =>
  `page-resolve:${resolveLocale.value}:${resolveActorKey.value}:${props.page}:${route.path}?${requestQuery.value}`
)

type ResolvePayload = PageResolvePayload & {
  page?: { id: string, contractVersion?: string }
  renderOutput?: ThemeRenderOutput
}

function disableSharedDocumentCache() {
  if (!import.meta.server) {
    return
  }
  disableSharedPageCacheForPageResolve(
    requestEvent?.context as MutableRouteRulesContext | undefined,
    value => {
      if (responseCacheControl) {
        responseCacheControl.value = value
      }
    }
  )
}

function applyFallbackCachePolicy(payload: ResolvePayload) {
  if (shouldDisablePageResolveSharedCache(payload)) {
    disableSharedDocumentCache()
  }
}

const { data: resolved, error: resolveError, pending } = await useAsyncData(
  resolveKey,
  async () => {
    if (!registryEnabled.value) {
      return coreResolveFallback(
        props.page,
        false,
        PAGE_RESOLVE_REASON.authoritativeCore
      ) as ResolvePayload
    }

    try {
      const { request } = useApiClient()
      const query = new URLSearchParams({ id: props.page, path: route.path })
      if (requestQuery.value) {
        query.set('query', requestQuery.value)
      }
      const payload = await requestPageResolveWithRetry(
        request,
        `/pages/resolve?${query.toString()}`,
        {
          timeout: PAGE_RESOLVE_TIMEOUT_MS,
          maxAttempts: 2,
          retryDelayMs: PAGE_RESOLVE_RETRY_DELAY_MS,
          serverInternal: import.meta.server
        }
      ) as ResolvePayload
      applyFallbackCachePolicy(payload)
      return payload
    } catch (error) {
      if (isPageResolveSemanticNotFound(error)) {
        disableSharedDocumentCache()
        throw error
      }
      const payload = coreResolveFallback(
        props.page,
        true,
        PAGE_RESOLVE_REASON.transportUnavailable
      ) as ResolvePayload
      applyFallbackCachePolicy(payload)
      return payload
    }
  },
  {
    watch: [
      () => props.page,
      () => route.path,
      requestQuery,
      registryEnabled,
      resolveLocale,
      resolveActorKey
    ]
  }
)

if (isPageResolveSemanticNotFound(resolveError.value)) {
  if (import.meta.client) {
    await notFoundPresentation.prepare()
  }
  throw createError({ statusCode: 404, statusMessage: 'Page not found' })
}

// 标准页面区域(forum.page.regions):与 resolve 同层拉取,SSR 阶段写入共享状态,
// 让页内 SFRegionOutlet 与 CSP 聚合都能在首屏渲染前拿到 widget refs。
const pageRegionsState = usePageRegionsState(props.page)
if (PAGE_REGION_PAGES.has(props.page)) {
  const { data: pageRegionsData } = await useAsyncData(
    `sf-page-regions:${resolveLocale.value}:${props.page}`,
    () => fetchPageRegions(props.page)
  )
  watchEffect(() => {
    pageRegionsState.value = pageRegionsData.value ?? null
  })
}
// 页面 widget refs = 区域目录里的 widget + 页面自己声明的 widget（如资料页 tab，
// 它们不在区域目录里）。两个来源各自已经失败关闭，这里只做合并去重。
const regionWidgetRefs = computed(() => {
  const regionRefs = collectRegionWidgetRefs(pageRegionsState.value)
  const extraRefs = props.extraL2Refs ?? []
  if (!extraRefs.length) {
    return regionRefs
  }
  try {
    return normalizePublicFrontendComponentRefs([...regionRefs, ...extraRefs])
  } catch {
    // 兜底保留区域 refs：一条坏 ref 不该让整页 L2 策略失效。
    return regionRefs
  }
})

// CSP 单点聚合:主题模板路径把 refs 交给 SFThemeTemplate 合并(那里是唯一调用点);
// 原生路径(host chrome / 纯 slot)没有 ThemeTemplate,由本组件(已在 async setup 内)聚合一次。
// 聚合条件用合并后的长度：只有页面级 widget、没有区域 widget 时也必须写 header。
const willUseThemeTemplate = computed(() => {
  const payload = resolved.value as ResolvePayload | null
  return Boolean(payload
    && (payload.provider || 'core') !== 'core'
    && (payload.renderOutput || (payload.templateHtml || '').trim())
    && !payload.fallback)
})
if (import.meta.server && regionWidgetRefs.value.length > 0 && !willUseThemeTemplate.value) {
  await applyPublicPageDocumentPolicy(regionWidgetRefs.value)
}

if (import.meta.server) {
  watchEffect(() => {
    if (resolved.value) {
      applyFallbackCachePolicy(resolved.value as ResolvePayload)
    }
  })
}
</script>

<template>
  <div v-if="pending && !resolved" class="min-h-screen" aria-busy="true" />
  <SFPageOutletRender
    v-else
    :page="page"
    :resolved="resolved"
    :resolve-error="resolveError"
    :region-widget-refs="regionWidgetRefs"
  >
    <slot />
  </SFPageOutletRender>
</template>
