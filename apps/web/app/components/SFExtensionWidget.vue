<script setup lang="ts">
import { useColorModePreference } from '~/composables/appearance/useColorModePreference'
import type {
  PublicFrontendBridgeV1,
  PublicFrontendCleanup,
  PublicFrontendComponentDescriptor
} from '~/runtime/public-extensions/types'
import {
  PUBLIC_FRONTEND_API_VERSION,
  PUBLIC_FRONTEND_DESCRIPTOR_TIMEOUT_MS,
  PUBLIC_FRONTEND_TRUST_NOTICE,
  assertPublicNavigationPath,
  parsePublicFrontendDescriptor,
  publicComponentPath,
  publicExtensionRequestPath,
  publicFrontendRequestOptions
} from '~/runtime/public-extensions/types'
import { loadPublicFrontendRelease } from '~/runtime/public-extensions/assets'
import { createPublicFrontendLeaseMonitor } from '~/runtime/public-extensions/lease'
import { mountPublicFrontendModule } from '~/runtime/public-extensions/mount'
import { hostIconResolver } from '~/utils/icons/hostIcons'
import {
  clearPublicContributionFailures,
  publicContributionFailureKey,
  publicContributionFailureState,
  recordPublicContributionFailure
} from '~/runtime/public-extensions/quarantine'

const props = withDefaults(defineProps<{
  extensionId?: string
  componentId?: string
  widgetId?: string
  name?: string
  componentProps?: Readonly<Record<string, unknown>>
}>(), {
  extensionId: '',
  componentId: '',
  widgetId: '',
  name: '',
  componentProps: () => ({})
})

const { apiBaseUrl, request } = useApiClient()
const { t, locale } = useI18n()
const { resolvedMode } = useColorModePreference()
const route = useRoute()
const target = ref<HTMLElement>()
const ssrRoot = ref<HTMLElement>()
const state = ref<'idle' | 'loading' | 'mounted' | 'fallback' | 'quarantined'>('idle')
const descriptor = shallowRef<PublicFrontendComponentDescriptor>()
let cleanup: PublicFrontendCleanup | undefined
let releaseAssets: (() => Promise<void>) | undefined
let loadQueue: Promise<void> = Promise.resolve()
let destroyed = false
// 瞬时失败（dev 下 air 重启、网络抖动）会让页面停在 L1 骨架直到手动刷新；
// 这里做有界退避重试。每次失败仍记入隔离计数，持续失败的组件照常到达
// 3 次隔离上限并停止重试；挂载成功清零。
const L2_RETRY_LIMIT = 2
const L2_RETRY_BACKOFF_MS: number[] = [1_500, 4_000]
let retryTimer: ReturnType<typeof setTimeout> | undefined
let retryAttempt = 0

function cancelScheduledRetry() {
  if (retryTimer !== undefined) {
    clearTimeout(retryTimer)
    retryTimer = undefined
  }
}

function scheduleRetryAfterFailure() {
  if (destroyed || retryAttempt >= L2_RETRY_LIMIT) return
  const delay = L2_RETRY_BACKOFF_MS[Math.min(retryAttempt, L2_RETRY_BACKOFF_MS.length - 1)]
  retryAttempt += 1
  retryTimer = setTimeout(() => {
    retryTimer = undefined
    if (!destroyed) void load()
  }, delay)
}

const resolvedComponentId = computed(() => props.componentId || props.widgetId || props.name)
const failureKey = computed(() => descriptor.value
  ? publicContributionFailureKey(descriptor.value.impactDigest, descriptor.value.extensionId, descriptor.value.componentId)
  : '')

function currentStorage() {
  return import.meta.client ? window.sessionStorage : undefined
}

async function dispose() {
  const currentCleanup = cleanup
  const currentRelease = releaseAssets
  cleanup = undefined
  releaseAssets = undefined
  try {
    await currentCleanup?.()
  } catch {
    // 第三方 cleanup 失败不能阻止宿主恢复 L1/SSR 回退。
  }
  try {
    if (currentRelease) await currentRelease()
  } catch {
    // CSS lease cleanup is best effort; the ref-count owner remains Host code.
  }
  target.value?.replaceChildren()
}

function appearance() {
  const styles = getComputedStyle(document.documentElement)
  return {
    colorMode: resolvedMode.value,
    accent: styles.getPropertyValue('--sf-accent').trim(),
    accentContrast: styles.getPropertyValue('--sf-accent-contrast').trim()
  }
}

function enqueue(task: () => Promise<void>) {
  const next = loadQueue.then(task, task)
  loadQueue = next.catch(() => {})
  return next
}

const leaseMonitor = createPublicFrontendLeaseMonitor({
  read: current => request<unknown>(
    publicComponentPath(current.extensionId, current.componentId),
    publicFrontendRequestOptions(current, {
      timeout: PUBLIC_FRONTEND_DESCRIPTOR_TIMEOUT_MS
    }) as Parameters<typeof request>[1]
  ),
  onChanged: () => load(),
  onUnavailable: () => enqueue(async () => {
    await dispose()
    state.value = 'fallback'
  })
})

function bridge(current: PublicFrontendComponentDescriptor): PublicFrontendBridgeV1 {
  if (!ssrRoot.value) throw new Error('public component SSR root is unavailable')
  return Object.freeze({
    apiVersion: PUBLIC_FRONTEND_API_VERSION,
    trust: PUBLIC_FRONTEND_TRUST_NOTICE,
    extensionId: current.extensionId,
    extensionVersion: current.extensionVersion,
    host: current.coreVersion ? Object.freeze({ coreVersion: current.coreVersion }) : undefined,
    packageDigest: current.packageDigest,
    impactDigest: current.impactDigest,
    componentId: current.componentId,
    locale: locale.value,
    appearance: Object.freeze(appearance()),
    icons: hostIconResolver,
    props: Object.freeze({ ...props.componentProps }),
    ssrRoot: ssrRoot.value,
    request: async <T>(path: string, options: Record<string, unknown> = {}) => {
      try {
        return await request<T>(
          publicExtensionRequestPath(current.extensionId, path),
          publicFrontendRequestOptions(current, options) as Parameters<typeof request>[1]
        )
      } catch (error) {
        // A rejected exact request may be the first observable revoke/upgrade
        // signal; revalidate immediately instead of waiting for the lease tick.
        void leaseMonitor.trigger()
        throw error
      }
    },
    navigate: async (path: string) => {
      await navigateTo(assertPublicNavigationPath(path))
    }
  })
}

async function performLoad() {
  leaseMonitor.stop()
  await dispose()
  descriptor.value = undefined
  state.value = 'fallback'
  const extensionId = props.extensionId.trim()
  const componentId = resolvedComponentId.value.trim()
  if (!import.meta.client || destroyed || !extensionId || !componentId || !target.value || !ssrRoot.value) return
  state.value = 'loading'
  try {
    const raw = await request<unknown>(publicComponentPath(extensionId, componentId), {
      timeout: PUBLIC_FRONTEND_DESCRIPTOR_TIMEOUT_MS
    })
    const current = parsePublicFrontendDescriptor(raw, extensionId, componentId)
    descriptor.value = current
    const storage = currentStorage()
    const key = failureKey.value
    if (storage && publicContributionFailureState(storage, key).quarantined) {
      state.value = 'quarantined'
      // 隔离只绑定当前 impact digest；继续租约检查，升级后的新产物可自动恢复。
      leaseMonitor.start(current)
      return
    }
    const loaded = await loadPublicFrontendRelease(current, apiBaseUrl || '')
    if (destroyed || !target.value) {
      await loaded.release()
      return
    }
    releaseAssets = loaded.release
    const currentTarget = target.value
    const currentBridge = bridge(current)
    cleanup = await mountPublicFrontendModule(loaded.module, currentTarget, currentBridge)
    state.value = 'mounted'
    retryAttempt = 0
    leaseMonitor.start(current)
    if (storage) clearPublicContributionFailures(storage, key)
  } catch {
    await dispose()
    const storage = currentStorage()
    if (storage && failureKey.value) {
      const failure = recordPublicContributionFailure(storage, failureKey.value)
      state.value = failure.quarantined ? 'quarantined' : 'fallback'
      if (!failure.quarantined) scheduleRetryAfterFailure()
    } else {
      state.value = 'fallback'
      scheduleRetryAfterFailure()
    }
  }
}

function load() {
  leaseMonitor.stop()
  return enqueue(performLoad)
}

// 显式来源的加载（路由/语言/主题变化、重新挂载）重置重试计数；重试自身触发的
// load 不经过这里，保证计数跨重试累计。
function loadFresh() {
  retryAttempt = 0
  cancelScheduledRetry()
  void load()
}

watch([
  () => props.extensionId,
  resolvedComponentId,
  () => props.componentProps,
  () => locale.value,
  resolvedMode,
  () => route.fullPath
], () => void loadFresh(), { deep: true })

function revalidateVisibleLease() {
  if (document.visibilityState === 'visible') void leaseMonitor.trigger()
}

onMounted(() => {
  window.addEventListener('focus', revalidateVisibleLease)
  document.addEventListener('visibilitychange', revalidateVisibleLease)
  void load()
})
onBeforeUnmount(() => {
  destroyed = true
  leaseMonitor.stop()
  cancelScheduledRetry()
  window.removeEventListener('focus', revalidateVisibleLease)
  document.removeEventListener('visibilitychange', revalidateVisibleLease)
  void loadQueue.then(dispose, dispose)
})

defineExpose({ reload: load })
</script>

<template>
  <div
    class="sf-extension-widget"
    :data-extension-id="extensionId"
    :data-component-id="resolvedComponentId"
    :data-l2-state="state"
    :data-l2-trust="descriptor ? PUBLIC_FRONTEND_TRUST_NOTICE : undefined"
    :aria-busy="state === 'loading' ? 'true' : undefined"
  >
    <div
      v-show="state !== 'mounted' && state !== 'loading'"
      ref="ssrRoot"
      class="sf-extension-widget__fallback"
      data-l2-fallback=""
    >
      <slot />
      <!-- 自动重试耗尽或被隔离后，给用户一个免刷新的恢复出口。
           loadFresh 会重置退避计数，失败继续计入隔离，语义不变。 -->
      <UButton
        v-if="state === 'fallback' || state === 'quarantined'"
        icon="i-tabler-refresh"
        color="neutral"
        variant="subtle"
        size="xs"
        class="mt-2"
        data-testid="public-l2-retry"
        @click="loadFresh"
      >
        {{ t('public.extensions.l2Retry') }}
      </UButton>
    </div>
    <div
      ref="target"
      class="sf-extension-widget__target"
      data-l2-target=""
    />
  </div>
</template>
