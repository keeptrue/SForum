<script setup lang="ts">
import { useColorModePreference } from '~/composables/appearance/useColorModePreference'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminFrontendTrust } from '~/composables/admin/useAdminFrontendTrust'
import SFAdminFrontendTrustPanel from '~/components/admin/SFAdminFrontendTrustPanel.vue'
import type {
  AdminExtensionSettingItem,
  AdminHostIdentity,
  AdminMicroFrontendBridgeV1,
  AdminMicroFrontendCleanup,
  AdminMicroFrontendModuleV1
} from '@sforum/admin-sdk'
import { ADMIN_MICRO_FRONTEND_API_VERSION } from '@sforum/admin-sdk'

import type { AdminExtension, AdminExtensionSettings, AdminFrontendStatus } from '~/utils/admin/adminExtensions'
import { assertAdminExtensionRelativePath, extensionRequestPath } from '~/runtime/admin-extensions/types'
import { hostIconResolver } from '~/utils/icons/hostIcons'
import {
  clearContributionFailures,
  contributionFailureKey,
  contributionFailureState,
  recordContributionFailure
} from '~/runtime/admin-extensions/quarantine'
import { resolveTrustedSettingsFirstPaint } from './firstPaint'

const props = defineProps<{
  extension: AdminExtension
  settings: AdminExtensionSettings
  /** 服务端 page-bootstrap 已解析出的信任结论，缺省时才回落到客户端请求。 */
  initialStatus?: AdminFrontendStatus | null
  /** 服务端 page-bootstrap 一并返回的宿主构建标识；旧宿主缺省。 */
  host?: AdminHostIdentity
  context: Readonly<{
    items: readonly AdminExtensionSettingItem[]
    values: Readonly<Record<string, string>>
    updateValue: (key: string, value: string) => void
    save: () => Promise<void>
    reset: () => Promise<void>
  }>
}>()

const extension = computed(() => props.extension)
const initialStatus = computed(() => props.initialStatus)
const { status, resolved: trustResolved, error: trustError, load: loadTrust, mutate } = useAdminFrontendTrust(extension, { initialStatus })
const { request } = useApiClient()
const { t, locale } = useI18n()
const { resolvedMode } = useColorModePreference()
const toast = useToast()
const adminRoutes = useAdminRoutes()
const target = ref<HTMLElement>()
const loading = ref(false)
const mounted = ref(false)
const failed = ref(false)
const failureMessage = ref('')
const quarantined = ref(false)
const forcedFallback = ref(false)
let cleanup: AdminMicroFrontendCleanup | undefined
let styleElement: HTMLStyleElement | undefined
let loadQueue: Promise<void> = Promise.resolve()
// 已挂载内容对应的状态指纹：watch 与 onMounted 会在同一轮各触发一次 load，
// 没有这道守卫就会“挂载 → 卸载 → 再挂载”，首屏看到两次骨架闪替。
let renderedKey = ''

const digest = computed(() => status.value?.digest || '')
const component = computed(() => status.value?.component)
const trusted = computed(() => !forcedFallback.value && ['trusted', 'source_trusted'].includes(status.value?.trustState || ''))
const firstPaint = computed(() => resolveTrustedSettingsFirstPaint({
  mounted: mounted.value,
  failed: failed.value,
  forcedFallback: forcedFallback.value,
  trustResolved: trustResolved.value,
  hasStatus: Boolean(status.value),
  trusted: trusted.value,
  // 与 performLoad 的 `!component.value || !digest.value` 早退条件保持一致：
  // 只有描述符却没有 digest 时根本装不上，必须当作「缺组件」回退，不能一直停在骨架。
  hasComponent: Boolean(component.value && digest.value)
}))
// 模板只认这两个布尔值；骨架与 Schema fallback 互斥由 firstPaint 保证。
const showSkeleton = computed(() => firstPaint.value.showSkeleton)
const showSchemaFallback = computed(() => firstPaint.value.showSchemaFallback)
const failureKey = computed(() => contributionFailureKey(`prebuilt:${digest.value}`, props.extension.id, component.value?.id || 'settings'))
const fallbackKey = computed(() => `sforum:admin-extension-schema-fallback:${encodeURIComponent(props.extension.id)}:${digest.value}`)
const componentFailureDescription = computed(() => {
  const fallback = t('admin.extensions.dynamic.componentFallback')
  return failureMessage.value ? `${fallback} ${failureMessage.value}` : fallback
})

function assetURL(name: 'entry' | 'style') {
  return `/api/v1/admin/extensions/${encodeURIComponent(props.extension.id)}/frontend/assets/${digest.value}/${name}`
}

function currentStorage() {
  return import.meta.client ? window.sessionStorage : undefined
}

async function dispose() {
  const currentCleanup = cleanup
  cleanup = undefined
  if (currentCleanup) {
    try {
      await currentCleanup()
    } catch {
      // 清理失败不阻止宿主卸载边界。
    }
  }
  styleElement?.remove()
  styleElement = undefined
  if (target.value) target.value.replaceChildren()
  mounted.value = false
  renderedKey = ''
}

function captureFailure() {
  failed.value = true
  mounted.value = false
  renderedKey = ''
  const storage = currentStorage()
  if (storage) quarantined.value = recordContributionFailure(storage, failureKey.value).quarantined
}

function appearance() {
  const root = document.documentElement
  const styles = getComputedStyle(root)
  return {
    colorMode: resolvedMode.value,
    accent: styles.getPropertyValue('--sf-accent').trim(),
    accentContrast: styles.getPropertyValue('--sf-accent-contrast').trim()
  }
}

function bridge(): AdminMicroFrontendBridgeV1 {
  return {
    apiVersion: ADMIN_MICRO_FRONTEND_API_VERSION,
    extensionId: props.extension.id,
    extensionVersion: props.extension.version,
    locale: locale.value,
    appearance: appearance(),
    icons: hostIconResolver,
    host: props.host,
    settings: {
      items: props.context.items,
      values: () => ({ ...props.context.values }),
      updateValue: props.context.updateValue,
      save: props.context.save,
      reset: props.context.reset
    },
    request: <T>(path: string, options: Record<string, unknown> = {}) => request<T>(
      extensionRequestPath(props.extension.id, path),
      options as Parameters<typeof request>[1]
    ),
    toast: (input) => {
      const error = input.kind === 'error'
      toast.add({
        title: input.title,
        description: input.description,
        color: error ? 'error' : 'success',
        icon: error ? 'i-lucide-triangle-alert' : 'i-lucide-check',
        duration: error ? Number.POSITIVE_INFINITY : 5000
      })
    },
    t,
    navigate: async (adminPath: string) => {
      await navigateTo(adminRoutes.path(assertAdminExtensionRelativePath(adminPath)))
    }
  }
}

function renderKey() {
  return [
    digest.value,
    component.value?.id || '',
    locale.value,
    resolvedMode.value,
    status.value?.trustState || '',
    forcedFallback.value ? 'schema' : 'component'
  ].join('|')
}

async function loadStyle(): Promise<string> {
  const response = await fetch(assetURL('style'), { credentials: 'include' })
  if (!response.ok) {
    throw new Error(`Admin component stylesheet request failed (${response.status})`)
  }
  const contentType = response.headers.get('content-type')?.toLowerCase() || ''
  if (!contentType.startsWith('text/css')) {
    throw new Error('Admin component stylesheet has an invalid content type')
  }
  return await response.text()
}

async function performLoad() {
  const key = renderKey()
  // 同一份状态已经挂载好了：不必卸载重来。
  if (mounted.value && key === renderedKey) return
  await dispose()
  failed.value = false
  failureMessage.value = ''
  quarantined.value = false
  if (!import.meta.client || !trustResolved.value || !trusted.value || !component.value || !digest.value) return
  const storage = currentStorage()
  if (storage && contributionFailureState(storage, failureKey.value).quarantined) {
    failed.value = true
    quarantined.value = true
    return
  }
  await nextTick()
  if (!target.value) return
  loading.value = true
  try {
    // 样式与入口并行取；样式仍然要校验 content-type 并在 mount 之前注入。
    const stylePromise = component.value.css ? loadStyle() : Promise.resolve('')
    const modulePromise = import(/* @vite-ignore */ assetURL('entry')) as Promise<AdminMicroFrontendModuleV1>
    const [styleText, module] = await Promise.all([stylePromise, modulePromise])
    if (module.apiVersion !== ADMIN_MICRO_FRONTEND_API_VERSION || typeof module.mount !== 'function') {
      throw new Error('Admin micro-frontend contract mismatch')
    }
    if (styleText) {
      styleElement = document.createElement('style')
      styleElement.dataset.sforumExtension = props.extension.id
      styleElement.textContent = styleText
      document.head.append(styleElement)
    }
    const result = await module.mount(target.value, bridge())
    if (result !== undefined && typeof result !== 'function') {
      throw new Error('Admin micro-frontend cleanup must be a function')
    }
    cleanup = typeof result === 'function' ? result : undefined
    mounted.value = true
    renderedKey = key
    if (storage) clearContributionFailures(storage, failureKey.value)
  } catch (error) {
    failureMessage.value = error instanceof Error ? error.message : String(error)
    await dispose()
    captureFailure()
  } finally {
    loading.value = false
  }
}

function load() {
  // trust、locale、color mode 可能在同一轮同时变化；串行卸载/挂载，避免两个异步 import 重复写入同一边界。
  const next = loadQueue.then(performLoad, performLoad)
  loadQueue = next.catch(() => {})
  return next
}

async function retry() {
  const storage = currentStorage()
  if (storage) clearContributionFailures(storage, failureKey.value)
  quarantined.value = false
  // 重试必须回源，不能复用 bootstrap 初值。
  await loadTrust(true)
  await load()
}

async function restoreSchemaFallback() {
  forcedFallback.value = true
  currentStorage()?.setItem(fallbackKey.value, '1')
  await loadQueue
  await dispose()
  if (status.value?.trustState === 'trusted') {
    await mutate('revoke')
    await loadTrust(true)
  }
}

async function resumeComponent() {
  currentStorage()?.removeItem(fallbackKey.value)
  forcedFallback.value = false
  await loadTrust(true)
  await load()
}

watch([() => status.value?.trustState, digest, component, locale, resolvedMode], () => {
  forcedFallback.value = currentStorage()?.getItem(fallbackKey.value) === '1'
  void load()
})
onMounted(() => {
  forcedFallback.value = currentStorage()?.getItem(fallbackKey.value) === '1'
  void load()
})
onBeforeUnmount(dispose)
</script>

<template>
  <div class="space-y-4" :data-extension-id="extension.id" :data-component-id="component?.id">
    <div ref="target" v-show="mounted || loading" class="sf-trusted-settings-component" />
    <div v-if="mounted" class="flex justify-end">
      <UButton color="neutral" variant="subtle" icon="i-lucide-undo-2" @click="restoreSchemaFallback">
        {{ t('admin.extensions.dynamic.useSchemaFallback') }}
      </UButton>
    </div>

    <div v-if="!mounted">
      <!-- 信任未读到、或已授权但组件尚未挂载：同一副骨架。SSR 首帧画的就是它，
           微前端挂载后一次替换，不再先铺一张 Schema 表单再整块换掉。 -->
      <div v-if="showSkeleton" class="space-y-4" aria-busy="true">
        <div class="rounded-lg border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900">
          <SFSkeleton :lines="2" />
        </div>
        <div class="rounded-lg border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900">
          <SFSkeleton :lines="6" />
        </div>
        <span class="sr-only" role="status">{{ t('admin.extensions.dynamic.componentLoading') }}</span>
      </div>
      <UAlert
        v-else-if="failed"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="quarantined ? t('admin.extensions.dynamic.componentQuarantined') : t('admin.extensions.dynamic.componentLoadFailed')"
        :description="componentFailureDescription"
        :actions="[{ label: t('admin.extensions.dynamic.componentRetry'), onClick: retry }]"
      />
      <UAlert
        v-else-if="forcedFallback"
        color="info"
        variant="subtle"
        icon="i-lucide-layout-template"
        :title="t('admin.extensions.dynamic.schemaFallbackActive')"
        :description="t('admin.extensions.dynamic.componentFallback')"
        :actions="[{ label: t('admin.extensions.dynamic.resumeComponent'), onClick: resumeComponent }]"
      />
      <!-- 读不到信任状态不等于未授权，必须与下面的授权警告分开 -->
      <UAlert
        v-else-if="!status"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        :title="t('admin.extensions.dynamic.componentStatusUnavailable')"
        :description="trustError"
        :actions="[{ label: t('admin.extensions.dynamic.componentRetry'), onClick: retry }]"
      />
      <UAlert
        v-else-if="!trusted"
        color="warning"
        variant="subtle"
        icon="i-lucide-shield-alert"
        :title="status?.trustState === 'invalidated' ? t('admin.extensions.dynamic.componentChanged') : t('admin.extensions.dynamic.componentTrustRequired')"
        :description="t('admin.extensions.dynamic.componentFallback')"
      />

      <SFAdminFrontendTrustPanel
        v-if="status?.kind === 'prebuilt_component' && !trusted && !forcedFallback"
        :extension="extension"
        :initial-status="status"
        @changed="() => loadTrust(true)"
      />
      <!-- 只有确定要回退到声明式 Schema UI 时才渲染表单，避免首帧先表单后组件 -->
      <div v-if="showSchemaFallback" class="mt-4">
        <slot name="fallback" />
      </div>
    </div>
  </div>
</template>
