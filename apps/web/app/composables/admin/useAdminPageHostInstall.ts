import { ref, computed, watch, nextTick } from 'vue'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import type { AdminExtension, AdminExtensionInstallResult } from '~/utils/admin/adminExtensions'
import {
  ADMIN_HOST_INSTALL_TIMEOUT_MS,
  AdminHostInstallError,
  adminPageSourceLabel,
  adminExtensionInstallErrorMessage,
  validateAdminHostInstallRequest
} from '~/utils/admin/extensions/hostActions'
import {
  hostActivationAction,
  hostActivationBody,
  hostStagedDescriptionKey
} from '~/utils/admin/extensions/hostActivation'
import type {
  AdminHostInstallEnableOutcome,
  AdminHostInstallPhase,
  AdminHostInstallRequest,
  AdminHostInstallResult,
  AdminHostInstallStagedReason,
  AdminPageHostActivateAction,
  AdminPageHostInstallMode,
  AdminPageInstallState
} from '~/utils/admin/extensions/hostActions'
import type { AdminInstallFromUrlOptions } from '~/composables/admin/useAdminExtensionInstall'
import type { ExecutableTrustChallenge, ExecutableTrustStatus } from '~/utils/extensions/extensionTrust'

/** Internal settlement: the bridge method decides which half it returns. */
type AdminPageHostInstallSettlement = {
  result: AdminHostInstallResult
  outcome: AdminHostInstallEnableOutcome
}

export type AdminPageHostInstallDeps = {
  /** Owning extension id; used for the audit source label. */
  extensionId: () => string
  /** Permission checker; a gate only — the API policy check stays authoritative. */
  can: (permission: string) => boolean
  isSuperAdmin: () => boolean
  /** Shared install transport (`useAdminExtensionInstall().installFromUrl`). */
  install: (
    url: string,
    sha256: string,
    options?: AdminInstallFromUrlOptions
  ) => Promise<AdminExtensionInstallResult>
  /** Host result normalizer, injected so tests can observe the raw response. */
  toResult: (result: AdminExtensionInstallResult) => AdminHostInstallResult
  /**
   * Raw install payload → extension record. The lifecycle helpers need the full
   * record (status, staged version, lifecycle contract), not the bridge shape.
   */
  toExtension?: (result: AdminExtensionInstallResult) => AdminExtension
  /** `GET /admin/extensions/:id/trust` through the shared trust path helper. */
  trustStatus: (item: AdminExtension, action: AdminPageHostActivateAction) => Promise<ExecutableTrustStatus>
  /** `POST /admin/extensions/:id/trust/challenge`; the token is one-use. */
  trustChallenge: (item: AdminExtension, action: AdminPageHostActivateAction) => Promise<ExecutableTrustChallenge>
  /** `POST /admin/extensions/:id/{enable|upgrade|restart}`. */
  activate: (
    item: AdminExtension,
    action: AdminPageHostActivateAction,
    body: Record<string, unknown>
  ) => Promise<AdminExtension>
  t: (key: string) => string
  notify: (input: { title: string, description?: string }) => void
  navigate: (path: string) => Promise<unknown> | unknown
}

/** 1s tick used by the dialog's "已用 N 秒" line. */
const ELAPSED_TICK_MS = 1000

/**
 * State machine behind `bridge.installExtension` and
 * `bridge.installAndEnableExtension`.
 *
 * Contract: the returned promise settles when the requested work is over — the
 * package is staged (stage mode), enabled/upgraded (enable mode), or the caller
 * gives up (cancel/close). A failed attempt keeps the dialog open so the
 * operator can retry; only success, cancel, or close settle it. One dialog and
 * one install at a time per page (`install_in_progress`).
 *
 * Enable mode never invents a success: an install that the Host will not enable
 * settles as `staged`, and a failed enable settles as an error so the caller
 * cannot mistake a stopped extension for a running one.
 */
export function useAdminPageHostInstall(deps: AdminPageHostInstallDeps) {
  const dialogOpen = ref(false)
  const request = ref<AdminHostInstallRequest | null>(null)
  const state = ref<AdminPageInstallState>('idle')
  const mode = ref<AdminPageHostInstallMode>('stage')
  const result = ref<AdminHostInstallResult | null>(null)
  const outcome = ref<AdminHostInstallEnableOutcome | null>(null)
  const stagedReason = ref<AdminHostInstallStagedReason | null>(null)
  const failurePhase = ref<AdminHostInstallPhase>('install')
  const errorMessage = ref('')
  const elapsedSeconds = ref(0)
  const trustStatus = ref<ExecutableTrustStatus | null>(null)
  const trustChallenge = ref<ExecutableTrustChallenge | null>(null)
  const trustBusy = ref(false)
  const trustError = ref('')

  let settler: {
    resolve: (value: AdminPageHostInstallSettlement) => void
    reject: (reason: AdminHostInstallError) => void
  } | null = null
  let elapsedTimer: ReturnType<typeof setInterval> | undefined
  /** Set after a successful install so retry re-runs only the activation. */
  let activationItem: AdminExtension | null = null
  let activationAction: AdminPageHostActivateAction = 'enable'
  /** Invalidates in-flight continuations when the caller resets the dialog. */
  let generation = 0
  /** Guards the trust-fetch → activate hand-off against a loop. */
  let trustDepth = 0

  const permission = computed(() => request.value?.expectedType === 'theme'
    ? 'extension.theme.manage'
    : 'extension.plugin.manage')
  const busy = computed(() => state.value === 'installing' || state.value === 'activating')

  function settle(error: AdminHostInstallError | null, value?: AdminPageHostInstallSettlement) {
    const current = settler
    settler = null
    if (!current) return
    if (error) current.reject(error)
    else if (value) current.resolve(value)
  }

  function stopElapsed() {
    if (elapsedTimer) clearInterval(elapsedTimer)
    elapsedTimer = undefined
  }

  function startElapsed() {
    stopElapsed()
    elapsedSeconds.value = 0
    elapsedTimer = setInterval(() => {
      elapsedSeconds.value += 1
    }, ELAPSED_TICK_MS)
  }

  /** Per-attempt state; both a fresh start and a teardown must land here. */
  function clearAttempt() {
    result.value = null
    outcome.value = null
    stagedReason.value = null
    failurePhase.value = 'install'
    errorMessage.value = ''
    elapsedSeconds.value = 0
    trustStatus.value = null
    trustChallenge.value = null
    trustBusy.value = false
    trustError.value = ''
    activationItem = null
    activationAction = 'enable'
    trustDepth = 0
  }

  function reset() {
    generation += 1
    stopElapsed()
    dialogOpen.value = false
    request.value = null
    state.value = 'idle'
    clearAttempt()
  }

  function begin(input: AdminHostInstallRequest, nextMode: AdminPageHostInstallMode) {
    return new Promise<AdminPageHostInstallSettlement>((resolve, reject) => {
      if (settler || ['confirming', 'installing', 'trust-review', 'activating'].includes(state.value)) {
        reject(new AdminHostInstallError('install_in_progress'))
        return
      }
      const validation = validateAdminHostInstallRequest(input)
      if (!validation.ok) {
        reject(new AdminHostInstallError('install_invalid_request', `invalid ${validation.reason}`))
        return
      }
      const next = validation.request
      const required = next.expectedType === 'theme' ? 'extension.theme.manage' : 'extension.plugin.manage'
      if (!deps.can(required)) {
        reject(new AdminHostInstallError('install_permission_denied'))
        return
      }
      settler = { resolve, reject }
      request.value = next
      mode.value = nextMode
      clearAttempt()
      // 确认框本身就是确认动作，因此不要求调用瞬间处于用户手势。
      state.value = 'confirming'
      dialogOpen.value = true
    })
  }

  /** Stage only — unchanged behaviour of `bridge.installExtension`. */
  function start(input: AdminHostInstallRequest) {
    return begin(input, 'stage').then(settlement => settlement.result)
  }

  /** Install, then run the Host's own enable flow. */
  function startEnable(input: AdminHostInstallRequest) {
    return begin(input, 'enable').then(settlement => ({
      ...settlement.result,
      outcome: settlement.outcome
    }))
  }

  // 外部关闭（ESC/父级重置）等同于取消：promise 不能因为对话框消失而永远悬着。
  watch(dialogOpen, visible => {
    if (visible || state.value === 'idle' || state.value === 'installing' || state.value === 'activating') return
    if (state.value === 'trust-review' && mode.value === 'enable' && result.value) {
      declineEnable()
      return
    }
    settle(rejection())
  })

  /**
   * The error a close/cancel settles with. A failed attempt keeps its phase so a
   * caller can tell "nothing landed" from "installed but not enabled" — the
   * second must never be offered as a reinstall.
   */
  function rejection(): AdminHostInstallError {
    const detail = errorMessage.value || undefined
    if (state.value !== 'error') return new AdminHostInstallError('install_cancelled', detail)
    return new AdminHostInstallError('install_failed', detail, failurePhase.value)
  }

  /**
   * Stop after the install. `stage` keeps the historical copy of the
   * install-only bridge; the other reasons explain why enable did not run.
   */
  function finishStaged(installed: AdminHostInstallResult, reason: AdminHostInstallStagedReason) {
    stopElapsed()
    stagedReason.value = reason
    outcome.value = 'staged'
    state.value = 'success'
    deps.notify({
      title: deps.t(installed.upgraded
        ? 'admin.extensionInstall.result.upgradeStaged'
        : 'admin.extensionInstall.result.installed'),
      description: deps.t(hostStagedDescriptionKey(reason))
    })
    settle(null, { result: installed, outcome: 'staged' })
  }

  /** The operator declined the impact review: installed, deliberately not enabled. */
  function declineEnable() {
    const installed = result.value
    if (!installed) return
    stopElapsed()
    deps.notify({
      title: deps.t(installed.upgraded
        ? 'admin.extensionInstall.result.upgradeStaged'
        : 'admin.extensionInstall.result.installed'),
      description: deps.t(hostStagedDescriptionKey('declined'))
    })
    reset()
    settle(null, { result: installed, outcome: 'staged' })
  }

  function finishEnabled(installed: AdminHostInstallResult, nextOutcome: 'enabled' | 'upgraded') {
    stopElapsed()
    outcome.value = nextOutcome
    stagedReason.value = null
    state.value = 'success'
    deps.notify({
      title: deps.t(nextOutcome === 'enabled'
        ? 'admin.extensions.enabled'
        : 'admin.extensions.upgraded'),
      description: deps.t(nextOutcome === 'enabled'
        ? 'admin.extensionInstall.hostAction.enabledBody'
        : 'admin.extensionInstall.hostAction.upgradedBody')
    })
    settle(null, { result: installed, outcome: nextOutcome })
  }

  function failInstall(error: unknown) {
    stopElapsed()
    failurePhase.value = 'install'
    errorMessage.value = adminExtensionInstallErrorMessage(
      deps.t,
      error,
      'admin.extensionInstall.hostAction.failedTitle'
    )
    state.value = 'error'
  }

  function failActivate(error: unknown) {
    stopElapsed()
    failurePhase.value = 'activate'
    errorMessage.value = apiErrorMessage(error) || deps.t('admin.extensionInstall.hostAction.enableFailedTitle')
    state.value = 'error'
  }

  async function runActivation(
    installed: AdminHostInstallResult,
    item: AdminExtension,
    action: AdminPageHostActivateAction,
    confirmationToken?: string
  ) {
    const current = generation
    state.value = 'activating'
    failurePhase.value = 'activate'
    errorMessage.value = ''
    try {
      await deps.activate(item, action, hostActivationBody(item, confirmationToken))
      if (current !== generation) return
      finishEnabled(installed, action === 'enable' ? 'enabled' : 'upgraded')
    } catch (error) {
      if (current !== generation) return
      // 后端仍要求 exact-artifact 确认（例如前端漏判了可执行后端）：转人工审阅。
      if (!confirmationToken && apiErrorReason(error) === 'extension.trust_challenge_required') {
        await requestTrustReview(installed, item, action)
        return
      }
      failActivate(error)
    }
  }

  async function requestTrustReview(
    installed: AdminHostInstallResult,
    item: AdminExtension,
    action: AdminPageHostActivateAction
  ) {
    if (trustDepth > 1) {
      // 状态读取与启用结论互相矛盾，不要无限重试。
      failActivate(null)
      return
    }
    trustDepth += 1
    const current = generation
    state.value = 'activating'
    trustBusy.value = true
    trustError.value = ''
    try {
      const status = await deps.trustStatus(item, action)
      if (current !== generation) return
      trustStatus.value = status
      // 已信任的制品与「本就不需要信任」都直接启用：只有未信任的可执行制品才要人工确认。
      if (!status.trustRequired || status.trusted) {
        await runActivation(installed, item, action)
        return
      }
      // 未信任的可执行制品：只有超级管理员能审阅影响并签发一次性确认。
      if (!deps.isSuperAdmin()) {
        finishStaged(installed, 'super_admin')
        return
      }
      state.value = 'trust-review'
    } catch (error) {
      if (current !== generation) return
      if (apiErrorReason(error) === 'extension.trust_not_required') {
        await runActivation(installed, item, action)
        return
      }
      failurePhase.value = 'activate'
      errorMessage.value = apiErrorMessage(error) || deps.t('admin.extensionInstall.hostAction.trustUnavailable')
      state.value = 'error'
    } finally {
      if (current === generation) trustBusy.value = false
    }
  }

  async function prepareActivation(
    installed: AdminHostInstallResult,
    item: AdminExtension,
    action: AdminPageHostActivateAction
  ) {
    activationItem = item
    activationAction = action
    if (!installed.requiresTrust) {
      await runActivation(installed, item, action)
      return
    }
    // `requiresTrust` 只说「有可执行后端」，不等于「未信任」：已有 live grant 的制品
    // 服务端会直接放行，任何管理员都能启用。角色只在读完信任状态之后才决定。
    await requestTrustReview(installed, item, action)
  }

  async function confirm() {
    const current = request.value
    if (!current || state.value !== 'confirming') return
    const installing = generation
    errorMessage.value = ''
    trustError.value = ''
    trustDepth = 0
    failurePhase.value = 'install'
    state.value = 'installing'
    startElapsed()
    let response: AdminExtensionInstallResult
    try {
      response = await deps.install(current.url, current.sha256, {
        expectations: {
          expectedExtensionId: current.expectedExtensionId,
          expectedType: current.expectedType,
          ...(current.expectedVersion ? { expectedVersion: current.expectedVersion } : {}),
          source: adminPageSourceLabel(deps.extensionId())
        },
        timeout: ADMIN_HOST_INSTALL_TIMEOUT_MS
      })
    } catch (error) {
      if (installing !== generation) return
      failInstall(error)
      return
    }
    if (installing !== generation) return
    stopElapsed()
    const installed = deps.toResult(response)
    result.value = installed
    if (mode.value === 'stage') {
      finishStaged(installed, 'stage')
      return
    }
    // 主题安装后要由操作员在主题页激活；宿主不替它做激活决定。
    if (installed.type === 'theme') {
      finishStaged(installed, 'theme')
      return
    }
    const item = (deps.toExtension ?? (payload => payload.extension))(response)
    const action = hostActivationAction(item, installed.upgraded)
    await prepareActivation(installed, item, action)
  }

  /** Trust review confirmed: issue the one-use token and immediately activate. */
  async function confirmTrust() {
    const item = activationItem
    const installed = result.value
    if (!item || !installed || state.value !== 'trust-review') return
    const current = generation
    trustBusy.value = true
    trustError.value = ''
    try {
      const challenge = await deps.trustChallenge(item, activationAction)
      if (current !== generation) return
      trustChallenge.value = challenge
      trustStatus.value = { impact: challenge.impact, trustRequired: true, trusted: false }
      await runActivation(installed, item, activationAction, challenge.token)
    } catch (error) {
      if (current !== generation) return
      trustError.value = apiErrorMessage(error) || deps.t('admin.extensionInstall.hostAction.challengeFailed')
      state.value = 'trust-review'
    } finally {
      if (current === generation) trustBusy.value = false
      // 一次性 token 用完即弃：失败后重试必须重新签发。
      trustChallenge.value = null
    }
  }

  function close() {
    if (busy.value) return
    if (state.value === 'trust-review' && mode.value === 'enable' && result.value) {
      declineEnable()
      return
    }
    const error = rejection()
    reset()
    settle(error)
  }

  function retry() {
    if (state.value !== 'error') return
    errorMessage.value = ''
    // 启用失败只重试启用：包已经在本地，重新下载既慢又会换掉已核对过的制品。
    if (failurePhase.value === 'activate' && activationItem && result.value) {
      trustDepth = 0
      void prepareActivation(result.value, activationItem, activationAction)
      return
    }
    state.value = 'confirming'
  }

  async function openManagement() {
    const type = result.value?.type === 'theme' ? 'theme' : 'plugin'
    reset()
    // 先让弹窗真的从 DOM 上消失，再换页。
    // `reset()` 只改 ref，DOM 要到 Vue 的渲染微任务才更新；而动态扩展页是 KeepAlive
    // 缓存的、弹窗又 teleport 到 body —— 同一帧里跳走，遮罩和 body 上的焦点锁会留在
    // 新页面上，表现就是「到了插件页，弹窗还浮着」。
    await nextTick()
    await deps.navigate(type === 'theme' ? '/extensions/themes' : '/extensions/plugins')
  }

  /**
   * 页面被 KeepAlive 停用（切 tab、换路由）时调用：弹窗不能跨页面存活。
   *
   * 走 `close()` 的同一套收尾，免得两条路径对「信任审阅没确认」给出不同结论；
   * 正在下载/启用时 `close()` 自己不动作 —— 那条 promise 由 unmount 的 abortOnUnmount
   * 收尾，提前取消等于把一次进行中的安装判死。
   */
  function dismissOnDeactivate() {
    close()
  }

  /** Page teardown aborts the in-flight install; the caller retries. */
  function abortOnUnmount() {
    settle(new AdminHostInstallError('install_cancelled', 'admin page unmounted'))
    reset()
  }

  return {
    dialogOpen,
    request,
    state,
    mode,
    result,
    outcome,
    stagedReason,
    failurePhase,
    errorMessage,
    elapsedSeconds,
    trustStatus,
    trustChallenge,
    trustBusy,
    trustError,
    permission,
    busy,
    isSuperAdmin: computed(() => deps.isSuperAdmin()),
    start,
    startEnable,
    confirm,
    confirmTrust,
    close,
    retry,
    openManagement,
    dismissOnDeactivate,
    abortOnUnmount
  }
}
