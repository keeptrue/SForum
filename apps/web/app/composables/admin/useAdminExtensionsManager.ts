import { useAuthSession } from '~/composables/identity/useAuthSession'
import { computed, ref } from 'vue'
import {
  EXTENSION_DELIVERY_FETCH_LIMIT,
  EXTENSION_EVENT_FETCH_LIMIT,
  extensionStats,
  mergeExtensionDeliveries,
  mergeExtensionEvents,
  type AdminContributionPointDefinition,
  type AdminEffectiveContribution,
  type AdminExtension,
  type AdminExtensionInstallResult,
  type AdminExtensionEventDefinition,
  type AdminExtensionEventDelivery,
  type AdminExtensionEvent,
  type AdminExtensionVersion,
  type AdminExtensionStatus,
  type AdminLifecycleOperation,
  type AdminLifecycleOperationDetail,
  type AdminLifecycleRecoveryInput
} from '~/utils/admin/adminExtensions'
import type {
  ExecutableTrustChallenge, ExecutableTrustStatus,
  ExtensionEnableTrustMode
} from '~/utils/extensions/extensionTrust'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import { lifecycleIdempotencyKey, useAdminUninstallCleanup } from '~/composables/admin/useAdminUninstallCleanup'
import {
  executableTrustPath,
  lifecycleSuccessIcon,
  lifecycleSuccessTitle,
  type ExtensionLifecycleAction
} from '~/utils/admin/extensions/lifecyclePresentation'

export const useAdminExtensionsManager = async () => {
  const { t } = useI18n()
  const toast = useToast()
  const { request } = useApiClient()
  const { user } = useAuthSession()

  const fileInput = ref<HTMLInputElement | null>(null)
  const selectedId = ref('')
  const uploading = ref(false)
  const busyId = ref('')
  const loadingEvents = ref(false)
  const loadingAllEvents = ref(false)
  const loadingEventDefinitions = ref(false)
  const loadingEventDeliveries = ref(false)
  const loadingContributionPoints = ref(false)
  const loadingContributions = ref(false)
  const lifecycleDialogOpen = ref(false)
  const lifecycleDialogItem = ref<AdminExtension | null>(null)
  const lifecycleOperations = ref<AdminLifecycleOperation[]>([])
  const lifecycleOperation = ref<AdminLifecycleOperationDetail | null>(null)
  const lifecycleLoading = ref(false)
  const lifecycleRecoveryBusy = ref(false)
  const lifecycleError = ref('')
  const eventsByExtension = ref<Record<string, AdminExtensionEvent[]>>({})
  const eventDefinitions = ref<AdminExtensionEventDefinition[]>([])
  const eventDeliveries = ref<AdminExtensionEventDelivery[]>([])
  const contributionPoints = ref<AdminContributionPointDefinition[]>([])
  const contributions = ref<AdminEffectiveContribution[]>([])

  const {
    data,
    pending,
    error,
    refresh
  } = await useAsyncData<AdminExtension[]>('admin-extensions', () => request<AdminExtension[]>('/admin/extensions'), {
    default: (): AdminExtension[] => []
  })

  const extensions = computed(() => data.value || [])
  const stats = computed(() => extensionStats(extensions.value))
  const selected = computed(() => extensions.value.find(item => item.id === selectedId.value) || extensions.value[0])
  const selectedEvents = computed(() => selected.value ? eventsByExtension.value[selected.value.id] || [] : [])
  const aggregatedEvents = computed(() => mergeExtensionEvents(eventsByExtension.value))
  const aggregatedDeliveries = computed(() => mergeExtensionDeliveries(eventDeliveries.value))

  // 卸载确认与清理恢复（F2.4 + 清理收敛）：聚焦协作对象持有幂等键持久化、
  // 确认弹窗状态、503 结构化详情与免键收尾入口。
  const uninstallCleanup = useAdminUninstallCleanup({
    t,
    toast,
    request,
    busyId,
    refresh,
    onUninstalled: (item) => {
      const current = extensions.value.filter(row => row.id !== item.id)
      data.value = current
      if (selectedId.value === item.id) {
        selectedId.value = current[0]?.id || ''
      }
    }
  })

  function openUpload() {
    fileInput.value?.click()
  }

  async function uploadArchive(event: Event) {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    input.value = ''
    if (!file) {
      return
    }

    const form = new FormData()
    form.append('file', file)
    uploading.value = true
    try {
      // F2.4：响应为 InstallResult；兼容旧形态（直接 Extension）。
      const payload = await request<AdminExtensionInstallResult | AdminExtension>('/admin/extensions', {
        method: 'POST',
        body: form
      })
      const result = 'extension' in payload && payload.extension
        ? payload as AdminExtensionInstallResult
        : { extension: payload as AdminExtension, upgraded: false }
      selectedId.value = result.extension.id
      replaceExtension(result.extension)
      await refresh()
      if (result.upgraded) {
        toast.add({
          color: 'success',
          icon: 'i-lucide-package-plus',
          title: result.activationPending
            ? t('admin.extensions.upgradeStaged')
            : t('admin.extensions.upgraded'),
          description: result.activationPending
            ? t('admin.extensions.upgradeStagedHint')
            : result.trustRevoked
              ? t('admin.extensions.upgradedTrustRevokedHint')
              : result.requiredReEnable
                ? t('admin.extensions.upgradedReEnableHint')
                : undefined,
          duration: 5000
        })
      } else {
        toast.add({ color: 'success', icon: 'i-lucide-package-check', title: t('admin.extensions.uploaded') })
      }
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.extensions.uploadFailed') })
    } finally {
      uploading.value = false
    }
  }

  async function openLifecycleExtension(item: AdminExtension) {
    lifecycleDialogItem.value = item
    lifecycleDialogOpen.value = true
    lifecycleError.value = ''
    lifecycleOperations.value = []
    lifecycleOperation.value = null
    await loadLifecycleOperations(item.id)
  }

  function closeLifecycleExtension() {
    lifecycleDialogOpen.value = false
    lifecycleDialogItem.value = null
    lifecycleOperations.value = []
    lifecycleOperation.value = null
    lifecycleError.value = ''
  }

  async function loadLifecycleOperations(extensionId: string, preferredOperationId?: number) {
    lifecycleLoading.value = true
    lifecycleError.value = ''
    try {
      const operations = await request<AdminLifecycleOperation[]>(`/admin/extensions/${extensionId}/lifecycle`)
      lifecycleOperations.value = operations
      const selectedId = preferredOperationId && operations.some(item => item.id === preferredOperationId)
        ? preferredOperationId
        : operations[0]?.id
      if (selectedId) {
        await selectLifecycleOperation(selectedId)
      } else {
        lifecycleOperation.value = null
      }
    } catch (error) {
      lifecycleError.value = apiErrorMessage(error) || t('admin.extensions.lifecycle.loadFailed')
    } finally {
      lifecycleLoading.value = false
    }
  }

  async function selectLifecycleOperation(operationId: number) {
    const item = lifecycleDialogItem.value
    if (!item || operationId <= 0) return
    lifecycleLoading.value = true
    lifecycleError.value = ''
    try {
      lifecycleOperation.value = await request<AdminLifecycleOperationDetail>(`/admin/extensions/${item.id}/lifecycle/${operationId}`)
    } catch (error) {
      lifecycleError.value = apiErrorMessage(error) || t('admin.extensions.lifecycle.loadFailed')
    } finally {
      lifecycleLoading.value = false
    }
  }

  async function recoverLifecycleOperation(input: AdminLifecycleRecoveryInput) {
    const item = lifecycleDialogItem.value
    const operation = lifecycleOperation.value
    if (!item || !operation) return
    lifecycleRecoveryBusy.value = true
    lifecycleError.value = ''
    try {
      const updated = await request<AdminLifecycleOperationDetail>(`/admin/extensions/${item.id}/lifecycle/${operation.id}/recovery`, {
        method: 'POST',
        body: {
          decision: input.decision,
          reason: input.reason.trim(),
          escalateForced: input.escalateForced
        }
      })
      lifecycleOperation.value = updated
      lifecycleOperations.value = lifecycleOperations.value.map(row => row.id === updated.id ? updated : row)
      if (updated.operation === 'uninstall' && updated.terminalResult === 'succeeded') {
        data.value = extensions.value.filter(row => row.id !== item.id)
      }
      toast.add({
        color: 'success',
        icon: input.decision === 'retry' ? 'i-lucide-refresh-cw' : 'i-lucide-skip-forward',
        title: t(input.decision === 'retry' ? 'admin.extensions.lifecycle.retrySuccess' : 'admin.extensions.lifecycle.skipSuccess'),
        duration: 5000
      })
      await loadLifecycleOperations(item.id, updated.id)
    } catch (error) {
      lifecycleError.value = apiErrorMessage(error) || t('admin.extensions.lifecycle.recoveryFailed')
      toast.add({
        color: 'error', icon: 'i-lucide-triangle-alert', title: lifecycleError.value, duration: 0
      })
    } finally {
      lifecycleRecoveryBusy.value = false
    }
  }

  watch([lifecycleDialogOpen, lifecycleRecoveryBusy, lifecycleLoading], ([open, recoveryBusy, loading]) => {
    if (!open && !recoveryBusy && !loading) {
      lifecycleDialogItem.value = null
      lifecycleOperations.value = []
      lifecycleOperation.value = null
      lifecycleError.value = ''
    }
  })

  // V3 优先读取服务端 canonical impact；开关关闭时才回落 F2.1 capability review。
  const enableConfirmOpen = ref(false)
  const enableConfirmItem = ref<AdminExtension | null>(null)
  const enableTrustMode = ref<ExtensionEnableTrustMode>('legacy')
  const enableTrustStatus = ref<ExecutableTrustStatus | null>(null)
  const enableTrustChallenge = ref<ExecutableTrustChallenge | null>(null)
  const enableTrustError = ref('')
  const enableTrustBusy = ref(false)
  const enableLifecycleAction = ref<'enable' | 'restart'>('enable')
  const isSuperAdmin = computed(() => user.value?.roleKeys?.includes('super_admin') === true)

  // Theme activation executable trust (L2). Ordinary L0/L1 themes stay operator-buildless (preview confirm only).
  type ThemeActivationPreview = {
    version: string
    packageDigest: string
    currentThemeId: string
    currentThemeVersion: string
    currentThemeDigest: string
    canActivate: boolean
    canApproveCoreReplacements: boolean
    requiresCoreReplacementApproval: boolean
    impacts: Array<{
      contribution: { id: string, action: string, target?: string, path?: string }
      conflicts?: Array<{ id: string, extensionId: string }>
    }>
  }
  const themeActivateTrustMode = ref<ExtensionEnableTrustMode>('exact')
  const themeActivateConfirmOpen = ref(false)
  // L0/L1（及 trust_not_required 回落）用预览确认 Modal，不再走原生 confirm。
  const themePreviewConfirmOpen = ref(false)
  const themeActivateConfirmItem = ref<AdminExtension | null>(null)
  const themeActivatePreview = ref<ThemeActivationPreview | null>(null)
  const themeActivateTrustStatus = ref<ExecutableTrustStatus | null>(null)
  const themeActivateTrustChallenge = ref<ExecutableTrustChallenge | null>(null)
  const themeActivateTrustError = ref('')
  const themeActivateTrustBusy = ref(false)

  const themePreviewAddCount = computed(() =>
    (themeActivatePreview.value?.impacts || []).filter(impact => impact.contribution.action === 'add').length
  )
  const themePreviewReplaceCount = computed(() =>
    (themeActivatePreview.value?.impacts || []).filter(impact => impact.contribution.action === 'replace').length
  )
  // 当前已激活主题再次走激活流程时，UI 使用「重新激活」文案。
  const themePreviewReactivating = computed(() => themeActivateConfirmItem.value?.status === 'enabled')

  // 与 ThemeActivationRequest 对齐：完整 preview 元组 + 可选的一次性 confirmationToken。
  function themeActivationRequestBody(preview: ThemeActivationPreview, confirmationToken?: string) {
    const body: Record<string, unknown> = {
      version: preview.version,
      packageDigest: preview.packageDigest,
      currentThemeId: preview.currentThemeId,
      currentThemeVersion: preview.currentThemeVersion,
      currentThemeDigest: preview.currentThemeDigest,
      approveCoreReplacements: preview.requiresCoreReplacementApproval && preview.canApproveCoreReplacements
    }
    const token = confirmationToken?.trim()
    if (token) {
      body.confirmationToken = token
    }
    return body
  }

  function openThemePreviewConfirm(item: AdminExtension, preview: ThemeActivationPreview) {
    themeActivateConfirmItem.value = item
    themeActivatePreview.value = preview
    themePreviewConfirmOpen.value = true
  }

  function resetEnableTrust() {
    enableTrustStatus.value = null
    enableTrustChallenge.value = null
    enableTrustError.value = ''
    enableTrustBusy.value = false
  }

  function openLegacyEnable(item: AdminExtension) {
    enableTrustMode.value = 'legacy'
    enableConfirmItem.value = item
    enableConfirmOpen.value = true
  }

  async function enableExtension(item: AdminExtension, action: 'enable' | 'restart' = 'enable') {
    resetEnableTrust()
    enableLifecycleAction.value = action
    enableTrustBusy.value = true
    try {
      const status = await request<ExecutableTrustStatus>(executableTrustPath(item, action))
      enableTrustMode.value = 'exact'
      enableTrustStatus.value = status
      if (!status.trustRequired) {
        const capabilityCount = status.impact.capabilities?.length ?? item.capabilityGrants?.length ?? 0
        const targetChanged = action === 'restart' && Boolean(item.stagedVersion)
        if ((item.status !== 'enabled' || targetChanged) && capabilityCount > 0) {
          openLegacyEnable(item)
        } else {
          await lifecycle(item, action)
        }
        return
      }
      enableConfirmItem.value = item
      enableConfirmOpen.value = true
    } catch (error) {
      if (apiErrorReason(error) === 'extension.trust_not_required') {
        // V3 migration gate is off; preserve the existing boolean confirmation flow.
        const targetChanged = action === 'restart' && Boolean(item.stagedVersion)
        if ((item.status !== 'enabled' || targetChanged) && (item.capabilityGrants?.length ?? 0) > 0) {
          openLegacyEnable(item)
        } else {
          await lifecycle(item, action, { confirmCapabilities: true })
        }
        return
      }
      enableTrustMode.value = 'exact'
      enableTrustError.value = apiErrorMessage(error) || t('admin.extensions.trust.previewFailed')
      enableConfirmItem.value = item
      enableConfirmOpen.value = true
    } finally {
      enableTrustBusy.value = false
    }
  }

  async function confirmEnableExtension() {
    const item = enableConfirmItem.value
    if (!item) {
      return
    }
    const body: Record<string, unknown> = enableTrustMode.value === 'exact'
      ? { confirmationToken: enableTrustChallenge.value?.token || undefined }
      : { confirmCapabilities: true }
    enableTrustError.value = ''
    const result = await lifecycle(item, enableLifecycleAction.value, body)
    if (result.ok) {
      enableConfirmOpen.value = false
      // 保留 impact 到关闭过渡结束，避免弹窗在离场动画中闪成空内容；一次性 token 立即丢弃。
      enableTrustChallenge.value = null
      enableTrustError.value = ''
      enableTrustBusy.value = false
      return
    }
    enableTrustError.value = apiErrorMessage(result.error) || t('admin.extensions.actionFailed')
    enableTrustChallenge.value = null
    if (enableTrustMode.value === 'exact') {
      try {
        enableTrustStatus.value = await request<ExecutableTrustStatus>(
          executableTrustPath(item, enableLifecycleAction.value)
        )
      } catch {
        // Keep the actionable enable failure visible; the next challenge request rechecks impact.
      }
    }
  }

  async function issueEnableTrustChallenge() {
    const item = enableConfirmItem.value
    if (!item || !isSuperAdmin.value) return
    enableTrustBusy.value = true
    enableTrustError.value = ''
    try {
      const challenge = await request<ExecutableTrustChallenge>(
        executableTrustPath(item, enableLifecycleAction.value, true),
        {
          method: 'POST',
          body: {}
        }
      )
      enableTrustChallenge.value = challenge
      enableTrustStatus.value = {
        impact: challenge.impact,
        trustRequired: true,
        trusted: false
      }
    } catch (error) {
      enableTrustError.value = apiErrorMessage(error) || t('admin.extensions.trust.challengeFailed')
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: enableTrustError.value,
        duration: 0
      })
    } finally {
      enableTrustBusy.value = false
    }
  }

  // 带 staged 目标的重启复用的是「启用」信任对话框；用途决定标题与确认文案。
  const enableDialogPurpose = computed(() => enableLifecycleAction.value === 'restart' && enableConfirmItem.value?.stagedVersion ? 'apply' as const : 'enable' as const)

  function cancelEnableExtension() {
    enableConfirmOpen.value = false
    enableTrustChallenge.value = null
    enableTrustError.value = ''
    enableTrustBusy.value = false
    enableLifecycleAction.value = 'enable'
  }

  function resetThemeActivateTrust() {
    themeActivateTrustStatus.value = null
    themeActivateTrustChallenge.value = null
    themeActivateTrustError.value = ''
    themeActivateTrustBusy.value = false
    themeActivatePreview.value = null
    themeActivateConfirmItem.value = null
  }

  async function issueThemeActivateTrustChallenge() {
    const item = themeActivateConfirmItem.value
    if (!item || !isSuperAdmin.value) return
    themeActivateTrustBusy.value = true
    themeActivateTrustError.value = ''
    try {
      const challenge = await request<ExecutableTrustChallenge>(`/admin/extensions/${item.id}/trust/challenge`, {
        method: 'POST',
        body: {}
      })
      themeActivateTrustChallenge.value = challenge
      themeActivateTrustStatus.value = {
        impact: challenge.impact,
        trustRequired: true,
        trusted: false
      }
    } catch (error) {
      themeActivateTrustError.value = apiErrorMessage(error) || t('admin.extensions.trust.challengeFailed')
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: themeActivateTrustError.value,
        duration: 0
      })
    } finally {
      themeActivateTrustBusy.value = false
    }
  }

  // 共享 activate POST：成功时刷新列表；失败时把 error 交给调用方（对话框或 toast）。
  async function postActivateTheme(
    item: AdminExtension,
    preview: ThemeActivationPreview,
    confirmationToken?: string
  ) {
    try {
      const activated = await request<AdminExtension>(`/admin/extensions/${item.id}/activate`, {
        method: 'POST',
        body: themeActivationRequestBody(preview, confirmationToken)
      })
      replaceExtension(activated)
      await refresh()
      await loadEvents(activated.id)
      return { ok: true as const, activated }
    } catch (error) {
      return { ok: false as const, error }
    }
  }

  async function refreshThemeActivateRetryContext(item: AdminExtension) {
    const [status, preview] = await Promise.allSettled([
      request<ExecutableTrustStatus>(`/admin/extensions/${item.id}/trust`),
      request<ThemeActivationPreview>(`/admin/pages/activate-preview/${item.id}`)
    ])
    if (status.status === 'fulfilled') {
      themeActivateTrustStatus.value = status.value
    }
    if (preview.status === 'fulfilled') {
      themeActivatePreview.value = preview.value
    }
  }

  async function confirmThemeActivate() {
    const item = themeActivateConfirmItem.value
    const preview = themeActivatePreview.value
    if (!item || !preview) {
      return
    }
    themeActivateTrustError.value = ''
    themeActivateTrustBusy.value = true
    try {
      const result = await postActivateTheme(item, preview, themeActivateTrustChallenge.value?.token)
      if (result.ok) {
        themeActivateConfirmOpen.value = false
        // 保留 impact 到关闭过渡结束，避免弹窗在离场动画中闪成空内容；一次性 token 立即丢弃。
        themeActivateTrustChallenge.value = null
        themeActivateTrustError.value = ''
        themeActivateTrustBusy.value = false
        toast.add({
          color: 'success',
          icon: 'i-lucide-palette',
          title: t('admin.extensions.themeActivated'),
          duration: 5000
        })
        return
      }
      // stale/expired/replayed/denied：保留阻塞错误、丢弃 token、刷新 trust 状态后允许安全重试。
      themeActivateTrustError.value = apiErrorMessage(result.error) || t('admin.extensions.actionFailed')
      themeActivateTrustChallenge.value = null
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: themeActivateTrustError.value,
        duration: 0
      })
      // Both documents are exact and independently staleable: a package update
      // changes trust impact, while another activation changes the preview CAS tuple.
      await refreshThemeActivateRetryContext(item)
    } finally {
      themeActivateTrustBusy.value = false
    }
  }

  function cancelThemeActivate() {
    themeActivateConfirmOpen.value = false
    themeActivateTrustChallenge.value = null
    themeActivateTrustError.value = ''
    themeActivateTrustBusy.value = false
  }

  async function confirmThemePreviewActivate() {
    const item = themeActivateConfirmItem.value
    const preview = themeActivatePreview.value
    if (!item || !preview) {
      return
    }
    themePreviewConfirmOpen.value = false
    await performActivateTheme(item, preview)
  }

  function cancelThemePreviewActivate() {
    themePreviewConfirmOpen.value = false
    themeActivateConfirmItem.value = null
    themeActivatePreview.value = null
  }

  async function disableExtension(item: AdminExtension) {
    await lifecycle(item, 'disable')
  }

  async function rollbackExtension(item: AdminExtension, target: AdminExtensionVersion) {
    return lifecycle(item, 'rollback', {
      targetVersion: target.version,
      targetPackageDigest: target.packageDigest
    })
  }

  async function restartExtension(item: AdminExtension) {
    if (item.stagedVersion) {
      // staged 目标可能需要 exact trust；确认完成后仍由独立 restart Host 工作流执行。
      await enableExtension(item, 'restart')
      return
    }
    await lifecycle(item, 'restart')
  }

  async function verifyExtension(item: AdminExtension) {
    busyId.value = item.id
    try {
      const verified = await request<AdminExtension>(`/admin/extensions/${item.id}/verify`, { method: 'POST', body: {} })
      replaceExtension(verified)
      await loadEvents(verified.id)
      toast.add({ color: 'success', icon: 'i-lucide-shield-check', title: t('admin.extensions.verified') })
    } catch (error) {
      toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.extensions.actionFailed') })
    } finally {
      busyId.value = ''
    }
  }

  async function activateTheme(item: AdminExtension) {
    busyId.value = item.id
    resetThemeActivateTrust()
    themePreviewConfirmOpen.value = false
    try {
      const preview = await request<ThemeActivationPreview>(`/admin/pages/activate-preview/${item.id}`)

      // 先读 exact trust 状态：L0/L1 与迁移门关闭时不走 challenge；仅 L2 未授权才弹 exact trust 对话框。
      let trustStatus: ExecutableTrustStatus | null = null
      try {
        trustStatus = await request<ExecutableTrustStatus>(`/admin/extensions/${item.id}/trust`)
      } catch (err) {
        if (apiErrorReason(err) === 'extension.trust_not_required') {
          // V3 迁移门关闭：page-registry 预览确认走 Modal，零 challenge、零构建。
          openThemePreviewConfirm(item, preview)
          return
        }
        throw err
      }

      // trustRequired=false 仅表示普通 L0/L1（或制品不需要可执行信任）。已授权 L2 仍为 trustRequired=true。
      if (!trustStatus.trustRequired) {
        openThemePreviewConfirm(item, preview)
        return
      }

      // Executable/L2：复用 exact trust 对话框；仅 super_admin 可签发 actor-bound 一次性 token。
      themeActivateTrustMode.value = 'exact'
      themeActivateTrustStatus.value = trustStatus
      themeActivatePreview.value = preview
      themeActivateConfirmItem.value = item
      themeActivateConfirmOpen.value = true
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: apiErrorMessage(error) || t('admin.extensions.themeActivationUnavailable'),
        duration: 0
      })
    } finally {
      busyId.value = ''
    }
  }

  // 普通 L0/L1 激活（无 trust challenge）。L2 确认走 confirmThemeActivate → postActivateTheme。
  async function performActivateTheme(item: AdminExtension, preview: ThemeActivationPreview, confirmationToken?: string) {
    busyId.value = item.id
    try {
      const result = await postActivateTheme(item, preview, confirmationToken)
      if (result.ok) {
        toast.add({
          color: 'success',
          icon: 'i-lucide-palette',
          title: t('admin.extensions.themeActivated'),
          duration: 5000
        })
        return
      }
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: apiErrorMessage(result.error) || t('admin.extensions.themeActivationUnavailable'),
        duration: 0
      })
    } finally {
      busyId.value = ''
    }
  }

  async function lifecycle(
    item: AdminExtension,
    action: ExtensionLifecycleAction,
    body: Record<string, unknown> = {}
  ) {
    busyId.value = item.id
    const idempotencyKey = lifecycleIdempotencyKey()
    try {
      const updated = await request<AdminExtension>(`/admin/extensions/${item.id}/${action}`, {
        method: 'POST',
        body,
        headers: { 'Idempotency-Key': idempotencyKey }
      })
      replaceExtension(updated)
      await loadEvents(updated.id)
      toast.add({
        color: 'success',
        icon: lifecycleSuccessIcon(action),
        title: t(lifecycleSuccessTitle(action, item)),
        duration: 5000
      })
      return { ok: true as const, updated }
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: apiErrorMessage(error) || t('admin.extensions.actionFailed'),
        duration: 0
      })
      return { ok: false as const, error }
    } finally {
      busyId.value = ''
    }
  }

  async function loadEvents(id: string, limit = EXTENSION_EVENT_FETCH_LIMIT) {
    loadingEvents.value = true
    try {
      const items = await request<AdminExtensionEvent[]>(`/admin/extensions/${id}/events?limit=${limit}`)
      eventsByExtension.value = {
        ...eventsByExtension.value,
        [id]: items
      }
      return items
    } catch {
      eventsByExtension.value = {
        ...eventsByExtension.value,
        [id]: []
      }
      return []
    } finally {
      loadingEvents.value = false
    }
  }

  async function loadAllEvents(limit = EXTENSION_EVENT_FETCH_LIMIT) {
    loadingAllEvents.value = true
    try {
      const entries = await Promise.all(extensions.value.map(async (item) => {
        try {
          const items = await request<AdminExtensionEvent[]>(`/admin/extensions/${item.id}/events?limit=${limit}`)
          return [item.id, items] as const
        } catch {
          return [item.id, []] as const
        }
      }))
      eventsByExtension.value = Object.fromEntries(entries)
    } finally {
      loadingAllEvents.value = false
    }
  }

  async function loadEventDefinitions() {
    loadingEventDefinitions.value = true
    try {
      eventDefinitions.value = await request<AdminExtensionEventDefinition[]>('/admin/extensions/event-definitions')
      return eventDefinitions.value
    } catch {
      eventDefinitions.value = []
      return []
    } finally {
      loadingEventDefinitions.value = false
    }
  }

  async function loadEventDeliveries(limit = EXTENSION_DELIVERY_FETCH_LIMIT) {
    loadingEventDeliveries.value = true
    try {
      eventDeliveries.value = await request<AdminExtensionEventDelivery[]>(`/admin/extensions/event-deliveries?limit=${limit}`)
      return eventDeliveries.value
    } catch {
      eventDeliveries.value = []
      return []
    } finally {
      loadingEventDeliveries.value = false
    }
  }

  async function loadContributionPoints() {
    loadingContributionPoints.value = true
    try {
      contributionPoints.value = await request<AdminContributionPointDefinition[]>('/admin/extensions/contribution-points')
      return contributionPoints.value
    } catch {
      contributionPoints.value = []
      return []
    } finally {
      loadingContributionPoints.value = false
    }
  }

  async function loadContributions() {
    loadingContributions.value = true
    try {
      contributions.value = await request<AdminEffectiveContribution[]>('/admin/extensions/contributions')
      return contributions.value
    } catch {
      contributions.value = []
      return []
    } finally {
      loadingContributions.value = false
    }
  }

  function replaceExtension(updated: AdminExtension) {
    const current = extensions.value.slice()
    const index = current.findIndex(item => item.id === updated.id)
    if (index >= 0) {
      current[index] = updated
    } else {
      current.unshift(updated)
    }
    data.value = current
    selectedId.value = updated.id
  }

  function statusColor(status: AdminExtensionStatus) {
    if (status === 'enabled') {
      return 'success'
    }
    if (status === 'disabled') {
      return 'neutral'
    }
    return 'warning'
  }

  function typeLabel(type: AdminExtension['type']) {
    return type === 'theme' ? t('admin.extensions.types.theme') : t('admin.extensions.types.plugin')
  }

  function statusLabel(status: AdminExtensionStatus) {
    return t(`admin.extensions.status.${status}`)
  }

  return {
    extensions,
    pending,
    error,
    refresh,
    fileInput,
    selectedId,
    selected,
    selectedEvents,
    uploading,
    busyId,
    loadingEvents,
    loadingAllEvents,
    loadingEventDefinitions,
    loadingEventDeliveries,
    loadingContributionPoints,
    loadingContributions,
    eventsByExtension,
    eventDefinitions,
    eventDeliveries,
    contributionPoints,
    contributions,
    aggregatedEvents,
    aggregatedDeliveries,
    stats,
    openUpload,
    uploadArchive,
    enableExtension,
    confirmEnableExtension,
    issueEnableTrustChallenge,
    cancelEnableExtension,
    enableConfirmOpen,
    enableConfirmItem,
    enableTrustMode,
    enableTrustStatus,
    enableTrustChallenge,
    enableTrustError,
    enableTrustBusy,
    enableDialogPurpose,
    isSuperAdmin,
    themeActivateTrustMode,
    themeActivateConfirmOpen,
    themePreviewConfirmOpen,
    themeActivateConfirmItem,
    themeActivatePreview,
    themePreviewAddCount,
    themePreviewReplaceCount,
    themePreviewReactivating,
    themeActivateTrustStatus,
    themeActivateTrustChallenge,
    themeActivateTrustError,
    themeActivateTrustBusy,
    issueThemeActivateTrustChallenge,
    confirmThemeActivate,
    cancelThemeActivate,
    confirmThemePreviewActivate,
    cancelThemePreviewActivate,
    ...uninstallCleanup,
    lifecycleDialogOpen,
    lifecycleDialogItem,
    lifecycleOperations,
    lifecycleOperation,
    lifecycleLoading,
    lifecycleRecoveryBusy,
    lifecycleError,
    openLifecycleExtension,
    closeLifecycleExtension,
    selectLifecycleOperation,
    recoverLifecycleOperation,
    disableExtension,
    rollbackExtension,
    restartExtension,
    verifyExtension,
    activateTheme,
    loadEvents,
    loadAllEvents,
    loadEventDefinitions,
    loadEventDeliveries,
    loadContributionPoints,
    loadContributions,
    statusColor,
    typeLabel,
    statusLabel
  }
}
