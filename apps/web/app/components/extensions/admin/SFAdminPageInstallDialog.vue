<script setup lang="ts">
import SFAdminExecutableTrustImpact from '~/components/admin/SFAdminExecutableTrustImpact.vue'
import { ADMIN_HOST_INSTALL_TIMEOUT_MS } from '~/utils/admin/extensions/hostActions'
import type {
  AdminHostInstallEnableOutcome,
  AdminHostInstallPhase,
  AdminHostInstallRequest,
  AdminHostInstallResult,
  AdminHostInstallStagedReason,
  AdminPageHostInstallMode,
  AdminPageInstallState
} from '~/utils/admin/extensions/hostActions'
import type { ExecutableTrustStatus } from '~/utils/extensions/extensionTrust'

defineOptions({ name: 'SFAdminPageInstallDialog' })

const props = defineProps<{
  request: AdminHostInstallRequest
  state: AdminPageInstallState
  /** `stage` keeps the install-only contract; `enable` also runs the enable flow. */
  mode: AdminPageHostInstallMode
  result: AdminHostInstallResult | null
  /** What the enable step reached; only meaningful once `state` is success. */
  outcome: AdminHostInstallEnableOutcome | null
  /** Why the Host stopped after installing, when it did. */
  stagedReason: AdminHostInstallStagedReason | null
  /** Which phase failed, so the retry button promises the right work. */
  failurePhase: AdminHostInstallPhase
  /** Localized failure text for state=error; already mapped to a stable code. */
  error: string
  /** Seconds spent in the current install, for the "已用 N 秒" line. */
  elapsedSeconds: number
  /** Exact-artifact impact shown during the trust review step. */
  trustStatus: ExecutableTrustStatus | null
  trustBusy: boolean
  trustError: string
  /** Permission key the declared type needs, e.g. extension.plugin.manage. */
  permission: string
  isSuperAdmin: boolean
}>()
const emit = defineEmits<{
  confirm: []
  confirmTrust: []
  cancel: []
  retry: []
  close: []
  openManagement: []
}>()
const open = defineModel<boolean>('open', { required: true })
const { t } = useI18n()

const installLimitSeconds = Math.round(ADMIN_HOST_INSTALL_TIMEOUT_MS / 1000)

// 宿主弹窗必须压在插件自己的遮罩之上（商店遮罩 z-index:40），否则确认框会被盖住，
// 而 reka 仍给 body 加了 pointer-events:none，整页就点不动了。
// 80 高于插件遮罩、低于 toast(100)。影响清单比确认面板宽，沿用启用对话框的 4xl。
const modalUi = computed(() => ({
  overlay: 'z-[80]',
  content: props.state === 'trust-review' ? 'z-[80] sm:max-w-4xl' : 'z-[80] sm:max-w-lg'
}))

let restoreFocusTo: HTMLElement | null = null
const urlHost = computed(() => {
  try {
    return new URL(props.request.url).host
  } catch {
    return props.request.url
  }
})
const busy = computed(() => props.state === 'installing' || props.state === 'activating')
const enableMode = computed(() => props.mode === 'enable')
const typeLabelKey = computed(() => props.request.expectedType === 'theme'
  ? 'admin.extensionInstall.hostAction.typeTheme'
  : 'admin.extensionInstall.hostAction.typePlugin')
const installingDescription = computed(() =>
  `${t('admin.extensionInstall.hostAction.installingHint')} ${t('admin.extensionInstall.hostAction.elapsed', { seconds: props.elapsedSeconds, limit: installLimitSeconds })}`
)

// headline 说的是「宿主做到了哪一步」，alert 说的是「为什么停在这」。两者不能用同一个
// key：同一句话上下各一遍，还会把「升级候选已保存」这层语义吞掉。
const headlineKey = computed(() => {
  if (props.outcome === 'enabled') return 'admin.extensionInstall.hostAction.enabledTitle'
  if (props.outcome === 'upgraded') return 'admin.extensionInstall.hostAction.upgradedTitle'
  return props.result?.upgraded
    ? 'admin.extensionInstall.result.upgradeStaged'
    : 'admin.extensionInstall.result.installed'
})
// 只有「停在这」才需要标题；enabled/upgraded 已经由 headline 说完，alert 只留一句说明。
const stoppedTitleKey = computed(() => {
  switch (props.stagedReason) {
    case 'theme': return 'admin.extensionInstall.hostAction.stagedThemeTitle'
    case 'super_admin': return 'admin.extensionInstall.hostAction.stagedSuperAdminTitle'
    case 'declined': return 'admin.extensionInstall.hostAction.stagedDeclinedTitle'
    default: return 'admin.extensionInstall.hostAction.stagedOnlyTitle'
  }
})
const outcomeBodyKey = computed(() => {
  if (props.outcome === 'enabled') return 'admin.extensionInstall.hostAction.enabledBody'
  if (props.outcome === 'upgraded') return 'admin.extensionInstall.hostAction.upgradedBody'
  switch (props.stagedReason) {
    case 'theme': return 'admin.extensionInstall.hostAction.stagedThemeBody'
    case 'super_admin': return 'admin.extensionInstall.hostAction.stagedSuperAdminBody'
    case 'declined': return 'admin.extensionInstall.hostAction.stagedDeclinedBody'
    default: return 'admin.extensionInstall.hostAction.stagedOnlyBody'
  }
})
const running = computed(() => props.outcome === 'enabled' || props.outcome === 'upgraded')
/**
 * 成功态底部的唯一去处：只在管理页还有事要做时才渲染。
 *
 * - 已在跑（enabled/upgraded）：什么都不用做，按钮整颗不出现 —— 这里曾经恒显示
 *   「去插件页完成信任与启用」，而信任早就在上一次启用时确认过了，等于凭空派活。
 * - 只有信任确实待办（超管待审 / 操作员刚拒绝）才在文案里提信任；主题从来不需要，
 *   旧宿主的暂存路径（`stage`）在装完之前也说不清制品是否已信任，所以只写「启用」。
 */
const managementAction = computed<{ key: string } | null>(() => {
  if (props.state !== 'success' || !props.result) return null
  if (running.value) return null
  if (props.result.type === 'theme') return { key: 'admin.extensionInstall.hostAction.openThemes' }
  switch (props.stagedReason) {
    case 'super_admin':
    case 'declined':
      return { key: 'admin.extensionInstall.hostAction.openPlugins' }
    default:
      return { key: 'admin.extensionInstall.hostAction.openPluginsEnable' }
  }
})
const failedTitleKey = computed(() => props.failurePhase === 'activate'
  ? 'admin.extensionInstall.hostAction.enableFailedTitle'
  : 'admin.extensionInstall.hostAction.failedTitle')
const failedHintKey = computed(() => props.failurePhase === 'activate'
  ? 'admin.extensionInstall.hostAction.enableFailedHint'
  : 'admin.extensionInstall.hostAction.failedHint')
const retryKey = computed(() => props.failurePhase === 'activate'
  ? 'admin.extensionInstall.hostAction.retryEnable'
  : 'admin.extensionInstall.hostAction.retry')
const primaryKey = computed(() => {
  if (props.state === 'activating') return 'admin.extensionInstall.hostAction.activating'
  if (props.state === 'installing') return 'admin.extensionInstall.hostAction.installing'
  return enableMode.value
    ? 'admin.extensionInstall.hostAction.confirmEnable'
    : 'admin.extensionInstall.hostAction.confirm'
})

// 焦点管理：宿主只负责“记住触发元素并在关闭后归还”。对话框内的初始焦点与
// 焦点陷阱由 UModal/FocusScope 负责——它落在关闭按钮上（后果性动作不是默认
// 焦点），Tab 一次即可到达“安装”。不要和 FocusScope 抢焦点。
function syncFocus(visible: boolean) {
  // SSR 没有 DOM：与 import.meta.client 同义，但让这个组件能在测试里直接挂载。
  if (typeof document === 'undefined') return
  if (visible) {
    restoreFocusTo = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return
  }
  const previous = restoreFocusTo
  restoreFocusTo = null
  // 触发元素可能已经不在文档里（例如商城先关掉了自己的详情弹窗）：focus() 会静默失败，
  // 焦点就掉到 body。只在它仍然挂载时归还。
  if (previous?.isConnected) previous.focus()
}

watch(open, syncFocus)

function cancel() {
  if (busy.value) return
  emit('cancel')
}
</script>

<template>
  <!-- 层级修复见 modalUi：压住插件遮罩，且不与 toast 抢。 -->
  <UModal
    v-model:open="open"
    :ui="modalUi"
    :close="false"
    :dismissible="false"
  >
    <template #content>
      <div
        data-testid="admin-page-install-dialog"
        class="flex max-h-[min(88vh,760px)] min-h-0 min-w-0 flex-col"
      >
        <header class="shrink-0 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-100">
                {{ enableMode
                  ? t('admin.extensionInstall.hostAction.titleEnable')
                  : t('admin.extensionInstall.hostAction.title') }}
              </h2>
              <p class="mt-1 break-all text-sm text-slate-600 dark:text-zinc-300">
                {{ t('admin.extensionInstall.hostAction.subtitle', { host: urlHost }) }}
              </p>
            </div>
            <UButton
              icon="i-lucide-x"
              color="neutral"
              variant="ghost"
              :disabled="busy"
              :aria-label="t('admin.extensionInstall.hostAction.close')"
              :title="t('admin.extensionInstall.hostAction.close')"
              @click="emit('close')"
            />
          </div>
        </header>

        <div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <template v-if="state === 'success' && result">
            <div class="flex min-w-0 items-start gap-3">
              <span
                class="flex size-9 shrink-0 items-center justify-center rounded-full"
                :class="running
                  ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
                  : 'bg-amber-500/15 text-amber-700 dark:text-amber-300'"
              >
                <UIcon :name="running ? 'i-lucide-check' : 'i-lucide-package-check'" class="size-5" />
              </span>
              <div class="min-w-0">
                <p
                  class="text-sm font-semibold text-slate-900 dark:text-zinc-100"
                  data-role="result-headline"
                >
                  {{ t(headlineKey) }}
                </p>
                <p class="mt-1 break-all text-xs text-slate-500 dark:text-zinc-400">
                  {{ result.extensionId }} · {{ result.version }}
                </p>
              </div>
            </div>
            <dl class="grid gap-3 text-sm">
              <div class="min-w-0">
                <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                  {{ t('admin.extensionInstall.hostAction.packageDigest') }}
                </dt>
                <dd class="mt-1 break-all font-mono text-xs text-slate-700 dark:text-zinc-200">
                  {{ result.packageDigest || '—' }}
                </dd>
              </div>
            </dl>
            <UAlert
              v-if="!running"
              color="warning"
              variant="subtle"
              icon="i-lucide-shield-alert"
              :title="t(stoppedTitleKey)"
              :description="t(outcomeBodyKey)"
            />
            <!-- 已经在跑：headline 已经说了「已启用 / 已升级」，这里只补一句不用再去 core。 -->
            <UAlert
              v-else
              color="success"
              variant="subtle"
              icon="i-lucide-shield-check"
              :description="t(outcomeBodyKey)"
            />
          </template>

          <template v-else-if="state === 'error'">
            <!-- 标题永远是「当前状态」，原始错误放描述：启用失败时运营商必须先看到
                 「已安装、未启用」，而不是一条后端错误码。 -->
            <UAlert
              color="error"
              variant="subtle"
              icon="i-lucide-triangle-alert"
              role="alert"
              :title="t(failedTitleKey)"
              :description="error || undefined"
            />
            <p class="text-xs text-slate-500 dark:text-zinc-400">
              {{ t(failedHintKey) }}
            </p>
          </template>

          <template v-else-if="state === 'trust-review'">
            <UAlert
              color="warning"
              variant="subtle"
              icon="i-lucide-shield-alert"
              :title="t('admin.extensionInstall.hostAction.trustReviewTitle')"
              :description="t('admin.extensionInstall.hostAction.trustReviewBody')"
            />
            <UAlert
              v-if="trustError"
              color="error"
              variant="subtle"
              icon="i-lucide-triangle-alert"
              role="alert"
              :title="trustError"
            />
            <p v-if="trustBusy && !trustStatus" class="text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.hostAction.trustChecking') }}
            </p>
            <!-- 权限披露只复用现成组件：同一份影响文档，不在宿主弹窗里再写一遍。 -->
            <SFAdminExecutableTrustImpact
              v-if="trustStatus"
              :impact="trustStatus.impact"
              :guard-authorization="trustStatus.guardAuthorization"
            />
            <p class="text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensionInstall.hostAction.trustReviewHint') }}
            </p>
          </template>

          <template v-else>
            <UAlert
              v-if="state === 'installing'"
              color="info"
              variant="subtle"
              icon="i-lucide-loader-circle"
              :ui="{ icon: 'animate-spin motion-reduce:animate-none' }"
              :title="t('admin.extensionInstall.hostAction.installingTitle')"
              :description="installingDescription"
            />
            <UAlert
              v-else-if="state === 'activating'"
              color="info"
              variant="subtle"
              icon="i-lucide-loader-circle"
              :ui="{ icon: 'animate-spin motion-reduce:animate-none' }"
              :title="t('admin.extensionInstall.hostAction.activatingTitle')"
              :description="t('admin.extensionInstall.hostAction.activatingHint')"
            />
            <UAlert
              v-if="enableMode && state === 'confirming'"
              color="neutral"
              variant="subtle"
              icon="i-lucide-play"
              :title="t('admin.extensionInstall.hostAction.enableNoticeTitle')"
              :description="isSuperAdmin
                ? t('admin.extensionInstall.hostAction.enableNoticeBody')
                : t('admin.extensionInstall.hostAction.enableNoticeBodyDelegated')"
            />
            <!-- 无后端信任的包会直接带 confirmCapabilities 启用：这句话就是那次确认的披露。 -->
            <p
              v-if="enableMode && state === 'confirming'"
              class="text-xs text-slate-500 dark:text-zinc-400"
            >
              {{ t('admin.extensionInstall.hostAction.enableCapabilitiesNote') }}
            </p>
            <dl class="grid gap-3 text-sm">
              <div class="min-w-0">
                <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                  {{ t('admin.extensionInstall.hostAction.url') }}
                </dt>
                <dd class="mt-1 min-w-0 break-all text-slate-900 dark:text-zinc-100">
                  {{ urlHost }}
                  <details class="mt-1">
                    <summary class="cursor-pointer text-xs text-[var(--sf-accent)]">
                      {{ t('admin.extensionInstall.hostAction.expandUrl') }}
                    </summary>
                    <code class="mt-1 block break-all font-mono text-xs text-slate-600 dark:text-zinc-300">
                      {{ request.url }}
                    </code>
                  </details>
                </dd>
              </div>
              <div class="min-w-0">
                <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                  {{ t('admin.extensionInstall.hostAction.sha256') }}
                </dt>
                <dd class="mt-1 break-all font-mono text-xs text-slate-700 dark:text-zinc-200">
                  {{ request.sha256 }}
                </dd>
              </div>
              <div class="grid min-w-0 gap-3 sm:grid-cols-3">
                <div class="min-w-0">
                  <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                    {{ t('admin.extensionInstall.hostAction.extensionId') }}
                  </dt>
                  <dd class="mt-1 break-all font-mono text-xs text-slate-900 dark:text-zinc-100">
                    {{ request.expectedExtensionId }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                    {{ t('admin.extensionInstall.hostAction.version') }}
                  </dt>
                  <dd class="mt-1 break-all font-mono text-xs text-slate-900 dark:text-zinc-100">
                    {{ request.expectedVersion || '—' }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                    {{ t('admin.extensionInstall.hostAction.type') }}
                  </dt>
                  <dd class="mt-1 break-all text-xs text-slate-900 dark:text-zinc-100">
                    {{ t(typeLabelKey) }}
                  </dd>
                </div>
              </div>
              <div class="min-w-0">
                <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">
                  {{ t('admin.extensionInstall.hostAction.permission') }}
                </dt>
                <dd class="mt-1 break-all font-mono text-xs text-slate-700 dark:text-zinc-200">
                  {{ permission }}
                </dd>
              </div>
            </dl>

            <UAlert
              v-if="!isSuperAdmin"
              color="warning"
              variant="subtle"
              icon="i-lucide-lock-keyhole"
              :title="t('admin.extensionInstall.hostAction.superAdminTitle')"
              :description="t('admin.extensionInstall.hostAction.superAdminBody')"
            />
            <UAlert
              color="neutral"
              variant="subtle"
              icon="i-lucide-shield-check"
              :title="t('admin.extensionInstall.hostAction.verifyTitle')"
              :description="t('admin.extensionInstall.hostAction.verifyBody')"
            />
          </template>
        </div>

        <footer class="flex shrink-0 flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <template v-if="state === 'success'">
            <UButton color="neutral" variant="ghost" @click="emit('close')">
              {{ t('admin.extensionInstall.hostAction.close') }}
            </UButton>
            <!-- 只在管理页还有事要做时出现；已在跑就只剩「关闭」。 -->
            <UButton
              v-if="managementAction"
              icon="i-lucide-external-link"
              @click="emit('openManagement')"
            >
              {{ t(managementAction.key) }}
            </UButton>
          </template>
          <template v-else-if="state === 'trust-review'">
            <UButton color="neutral" variant="ghost" :disabled="trustBusy" @click="cancel">
              {{ t('admin.extensionInstall.hostAction.cancel') }}
            </UButton>
            <UButton
              color="warning"
              icon="i-lucide-shield-check"
              :loading="trustBusy"
              :disabled="trustBusy"
              @click="emit('confirmTrust')"
            >
              {{ t('admin.extensionInstall.hostAction.trustReviewConfirm') }}
            </UButton>
          </template>
          <template v-else-if="state === 'error'">
            <UButton color="neutral" variant="ghost" @click="emit('close')">
              {{ t('admin.extensionInstall.hostAction.close') }}
            </UButton>
            <UButton icon="i-lucide-rotate-ccw" @click="emit('retry')">
              {{ t(retryKey) }}
            </UButton>
          </template>
          <template v-else>
            <UButton color="neutral" variant="ghost" :disabled="busy" @click="cancel">
              {{ t('admin.extensionInstall.hostAction.cancel') }}
            </UButton>
            <UButton
              :icon="enableMode ? 'i-lucide-play' : 'i-lucide-download'"
              :loading="busy"
              :disabled="busy"
              @click="emit('confirm')"
            >
              {{ t(primaryKey) }}
            </UButton>
          </template>
        </footer>
      </div>
    </template>
  </UModal>
</template>
