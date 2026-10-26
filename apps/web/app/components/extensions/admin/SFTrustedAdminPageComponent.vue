<script setup lang="ts">
import { ADMIN_MICRO_FRONTEND_API_VERSION } from '@sforum/admin-sdk'
import type {
  AdminHostIdentity,
  AdminHostInstallRequest,
  AdminMicroFrontendCleanup,
  AdminPageBridgeV1,
  AdminPageModuleV1
} from '@sforum/admin-sdk'
import { useColorModePreference } from '~/composables/appearance/useColorModePreference'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminFrontendTrust } from '~/composables/admin/useAdminFrontendTrust'
import { useAdminExtensionInstall, toAdminHostInstallResult } from '~/composables/admin/useAdminExtensionInstall'
import { useAdminPageHostInstall } from '~/composables/admin/useAdminPageHostInstall'
import { usePermissions } from '~/composables/identity/usePermissions'
import SFAdminFrontendTrustPanel from '~/components/admin/SFAdminFrontendTrustPanel.vue'
import SFAdminPageInstallDialog from '~/components/extensions/admin/SFAdminPageInstallDialog.vue'
import type { AdminExtension, AdminExtensionAdminPage, AdminFrontendStatus } from '~/utils/admin/adminExtensions'
import { adminPageCanInstallAndEnable, adminPageCanInstallExtensions } from '~/utils/admin/extensions/hostActions'
import type { AdminPageHostActivateAction } from '~/utils/admin/extensions/hostActions'
import { executableTrustPath } from '~/utils/admin/extensions/lifecyclePresentation'
import { assertAdminExtensionRelativePath, extensionRequestPath } from '~/runtime/admin-extensions/types'
import type { ExecutableTrustChallenge, ExecutableTrustStatus } from '~/utils/extensions/extensionTrust'
import { hostIconResolver } from '~/utils/icons/hostIcons'
import {
  clearContributionFailures,
  contributionFailureKey,
  contributionFailureState,
  recordContributionFailure
} from '~/runtime/admin-extensions/quarantine'

const props = defineProps<{
  extension: AdminExtension
  page: AdminExtensionAdminPage
  /** 服务端 page-bootstrap 已解析出的信任结论，缺省时才回落到客户端请求。 */
  initialStatus?: AdminFrontendStatus | null
  /** 服务端 page-bootstrap 一并返回的宿主构建标识；旧宿主缺省。 */
  host?: AdminHostIdentity
}>()

const extension = computed(() => props.extension)
const component = computed(() => props.page.component)
const initialStatus = computed(() => props.initialStatus)
const { status, resolved: trustResolved, error: trustError, load: loadTrust } = useAdminFrontendTrust(extension, { initialStatus })
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
let cleanup: AdminMicroFrontendCleanup | undefined
let styleElement: HTMLStyleElement | undefined
let loadQueue: Promise<void> = Promise.resolve()
let loadGeneration = 0
let disposed = true

const digest = computed(() => status.value?.digest || '')
const trusted = computed(() => ['trusted', 'source_trusted'].includes(status.value?.trustState || ''))
const failureKey = computed(() => contributionFailureKey(
  `admin-page:${digest.value}`,
  props.extension.id,
  component.value?.id || props.page.path
))

function assetURL(name: 'entry' | 'style') {
  const current = component.value
  if (!current) return ''
  return `/_sforum/private-assets/extensions/${encodeURIComponent(props.extension.id)}/${digest.value}/${encodeURIComponent(current.id)}/${name}`
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
      // cleanup 失败仅影响当前插件页面，不得破坏 Host 管理后台壳层。
    }
  }
  styleElement?.remove()
  styleElement = undefined
  target.value?.replaceChildren()
  mounted.value = false
}

function appearance() {
  const styles = getComputedStyle(document.documentElement)
  return {
    colorMode: resolvedMode.value,
    accent: styles.getPropertyValue('--sf-accent').trim(),
    accentContrast: styles.getPropertyValue('--sf-accent-contrast').trim()
  }
}

const { can, user } = usePermissions()
const { installFromUrl } = useAdminExtensionInstall()
const hostInstall = useAdminPageHostInstall({
  extensionId: () => props.extension.id,
  can,
  isSuperAdmin: () => Boolean(user.value?.roleKeys?.includes('super_admin')),
  install: installFromUrl,
  toResult: toAdminHostInstallResult,
  // 信任路径与升级动作都走既有助手：宿主弹窗不是第二套启用实现。
  trustStatus: (item, action) => request<ExecutableTrustStatus>(executableTrustPath(item, action)),
  trustChallenge: (item, action) => request<ExecutableTrustChallenge>(
    executableTrustPath(item, action, true),
    { method: 'POST', body: {} }
  ),
  activate: (item, action: AdminPageHostActivateAction, body) => request<AdminExtension>(
    `/admin/extensions/${item.id}/${action}`,
    {
      method: 'POST',
      body,
      headers: { 'Idempotency-Key': globalThis.crypto.randomUUID() }
    }
  ),
  t,
  notify: input => toast.add({
    color: 'success',
    icon: 'i-lucide-package-check',
    title: input.title,
    description: input.description,
    duration: 5000
  }),
  navigate: path => navigateTo(adminRoutes.path(path))
})
const {
  dialogOpen: installDialogOpen,
  request: installRequest,
  state: installState,
  mode: installMode,
  result: installResult,
  outcome: installOutcome,
  stagedReason: installStagedReason,
  failurePhase: installFailurePhase,
  errorMessage: installError,
  elapsedSeconds: installElapsedSeconds,
  trustStatus: installTrustStatus,
  trustBusy: installTrustBusy,
  trustError: installTrustError,
  permission: installPermission,
  isSuperAdmin: installIsSuperAdmin
} = hostInstall

function bridge(): AdminPageBridgeV1 {
  const base = {
    apiVersion: ADMIN_MICRO_FRONTEND_API_VERSION,
    extensionId: props.extension.id,
    extensionVersion: props.extension.version,
    locale: locale.value,
    appearance: appearance(),
    icons: hostIconResolver,
    host: props.host,
    page: {
      path: props.page.path,
      label: props.page.label,
      description: props.page.description,
      icon: props.page.icon
    },
    request: <T>(path: string, options: Record<string, unknown> = {}) => request<T>(
      extensionRequestPath(props.extension.id, path),
      {
        ...options,
        // 插件路由返回自己声明的裸 JSON，没有宿主 `{ code, message, data }` 信封。
        // 走 'body' 拿整个响应体；默认 'data' 会解出 envelope.data === undefined。
        // 放在展开之后：模式由宿主契约决定，插件不能自己改。
        responseMode: 'body'
      } as Parameters<typeof request>[1]
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
  } satisfies AdminPageBridgeV1
  // 每个宿主动作单独注入：页面声明 extension.install 才拿到 installExtension，
  // 声明 extension.install.enable 才拿到 installAndEnableExtension。旧宿主行为不变。
  const canInstall = adminPageCanInstallExtensions(props.page)
  const canInstallAndEnable = adminPageCanInstallAndEnable(props.page)
  if (!canInstall && !canInstallAndEnable) return base
  return {
    ...base,
    ...(canInstall
      ? { installExtension: (request: AdminHostInstallRequest) => hostInstall.start(request) }
      : {}),
    ...(canInstallAndEnable
      ? { installAndEnableExtension: (request: AdminHostInstallRequest) => hostInstall.startEnable(request) }
      : {})
  }
}

function loadIsCurrent(generation: number) {
  return !disposed && generation === loadGeneration
}

async function performLoad(generation: number) {
  await dispose()
  if (!loadIsCurrent(generation)) return
  loading.value = false
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
  if (!loadIsCurrent(generation) || !target.value) return
  loading.value = true
  try {
    if (component.value.css) {
      const response = await fetch(assetURL('style'), { credentials: 'include' })
      if (!loadIsCurrent(generation)) return
      if (!response.ok || !response.headers.get('content-type')?.toLowerCase().startsWith('text/css')) {
        throw new Error(`Admin page stylesheet request failed (${response.status})`)
      }
      const stylesheet = document.createElement('style')
      stylesheet.dataset.sforumExtension = props.extension.id
      stylesheet.dataset.sforumComponent = component.value.id
      stylesheet.textContent = await response.text()
      if (!loadIsCurrent(generation)) return
      styleElement = stylesheet
      document.head.append(stylesheet)
    }
    // Vite dev 的 import 分析会给无法静态分析的动态 import 注入 ?import（injectQuery），
    // 私有资产代理对该 query 返回 404。injectQuery 只对以 "/" 或 "." 开头的 URL 注入，
    // 这里用运行时绝对 URL 绕过注入；生产构建同样按原样保留（@vite-ignore）。
    const entryURL = new URL(assetURL('entry'), window.location.origin).href
    const module = await import(/* @vite-ignore */ entryURL) as AdminPageModuleV1
    if (!loadIsCurrent(generation)) return
    if (module.apiVersion !== ADMIN_MICRO_FRONTEND_API_VERSION || typeof module.mount !== 'function') {
      throw new Error('Admin page component contract mismatch')
    }
    const mountTarget = target.value
    if (!mountTarget) return
    const result = await module.mount(mountTarget, bridge())
    if (result !== undefined && typeof result !== 'function') {
      throw new Error('Admin page component cleanup must be a function')
    }
    if (!loadIsCurrent(generation)) {
      if (typeof result === 'function') await result()
      return
    }
    cleanup = typeof result === 'function' ? result : undefined
    mounted.value = true
    if (storage) clearContributionFailures(storage, failureKey.value)
  } catch (error) {
    if (!loadIsCurrent(generation)) return
    failureMessage.value = error instanceof Error ? error.message : String(error)
    await dispose()
    failed.value = true
    if (storage) quarantined.value = recordContributionFailure(storage, failureKey.value).quarantined
  } finally {
    if (loadIsCurrent(generation)) loading.value = false
  }
}

function load() {
  const generation = ++loadGeneration
  const next = loadQueue.then(() => performLoad(generation), () => performLoad(generation))
  loadQueue = next.catch(() => {})
  return next
}

async function retry() {
  const storage = currentStorage()
  if (storage) clearContributionFailures(storage, failureKey.value)
  // 重试必须回源，不能复用 bootstrap 初值。
  await loadTrust(true)
  await load()
}

watch([() => status.value?.trustState, digest, component, locale, resolvedMode], () => void load())
onMounted(() => {
  disposed = false
  void load()
})
onBeforeUnmount(() => {
  disposed = true
  loadGeneration += 1
  // 页面卸载会中止进行中的安装请求；不要让插件的 promise 永远悬着。
  hostInstall.abortOnUnmount()
  void dispose()
})
</script>

<template>
  <section
    class="min-w-0"
    :data-extension-id="extension.id"
    :data-component-id="component?.id"
    :aria-busy="loading ? 'true' : undefined"
  >
    <div ref="target" v-show="mounted || loading" />

    <!-- 信任状态未知，或已授权但组件尚未挂载：同一骨架，SSR 与首帧一致 -->
    <div
      v-if="(!trustResolved || (trusted && !mounted)) && !failed"
      class="rounded-lg border border-slate-200 bg-white p-5 dark:border-zinc-800 dark:bg-zinc-900"
    >
      <SFSkeleton :lines="4" />
      <span class="sr-only" role="status">{{ t('admin.extensions.dynamic.componentLoading') }}</span>
    </div>

    <UAlert
      v-else-if="failed"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="quarantined ? t('admin.extensions.dynamic.componentQuarantined') : t('admin.extensions.dynamic.componentLoadFailed')"
      :description="failureMessage || t('admin.extensions.dynamic.componentFallback')"
      :actions="[{ label: t('admin.extensions.dynamic.componentRetry'), onClick: retry }]"
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
    <!-- 到这里 status 必非空，!trusted 才真正代表"未授权" -->
    <template v-else-if="!trusted">
      <UAlert
        color="warning"
        variant="subtle"
        icon="i-lucide-shield-alert"
        :title="t('admin.extensions.dynamic.componentTrustRequired')"
        :description="t('admin.extensions.frontend.prebuiltDescription')"
      />
      <SFAdminFrontendTrustPanel
        :extension="extension"
        :initial-status="status"
        @changed="() => loadTrust(true)"
      />
    </template>

    <!-- 只有声明了 hostActions 的页面才可能打开这个确认框；它属于 bridge，
         面板流程（SFAdminExtensionInstallPanel）保持不变。 -->
    <SFAdminPageInstallDialog
      v-if="installRequest"
      v-model:open="installDialogOpen"
      :request="installRequest"
      :state="installState"
      :mode="installMode"
      :result="installResult"
      :outcome="installOutcome"
      :staged-reason="installStagedReason"
      :failure-phase="installFailurePhase"
      :error="installError"
      :elapsed-seconds="installElapsedSeconds"
      :trust-status="installTrustStatus"
      :trust-busy="installTrustBusy"
      :trust-error="installTrustError"
      :permission="installPermission"
      :is-super-admin="installIsSuperAdmin"
      @confirm="hostInstall.confirm"
      @confirm-trust="hostInstall.confirmTrust"
      @cancel="hostInstall.close"
      @close="hostInstall.close"
      @retry="hostInstall.retry"
      @open-management="hostInstall.openManagement"
    />
  </section>
</template>
