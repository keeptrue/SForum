import { computed, ref, type Ref } from 'vue'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import {
  canUpgradePlugin,
  extensionUpgradeCandidates,
  extensionUpgradeLifecycleAction,
  type AdminExtension
} from '~/utils/admin/adminExtensions'
import { executableTrustPath } from '~/utils/admin/extensions/lifecyclePresentation'
import { resolveExtensionUpdateState } from '~/utils/admin/extensions/updateState'
import type { ExecutableTrustChallenge, ExecutableTrustStatus } from '~/utils/extensions/extensionTrust'
import type { ExtensionEnableTrustMode } from '~/utils/extensions/extensionTrust'

type AdminExtensionUpgradeFlowOptions = {
  extensions: Ref<AdminExtension[]>
  refresh: () => Promise<unknown>
  isSuperAdmin: Readonly<Ref<boolean>>
}

type UpgradePreparation = 'completed' | 'failed' | 'waiting'

export function useAdminExtensionUpgradeFlow(options: AdminExtensionUpgradeFlowOptions) {
  const { t } = useI18n()
  const { request } = useApiClient()
  const toast = useToast()

  const upgradeConfirmOpen = ref(false)
  const upgradeConfirmItem = ref<AdminExtension | null>(null)
  const upgradeTrustMode = ref<ExtensionEnableTrustMode>('exact')
  const upgradeTrustStatus = ref<ExecutableTrustStatus | null>(null)
  const upgradeTrustChallenge = ref<ExecutableTrustChallenge | null>(null)
  const upgradeTrustError = ref('')
  const upgradeTrustBusy = ref(false)
  const upgradeBusyId = ref('')
  const upgradeAllBusy = ref(false)
  const upgradeAllCompleted = ref(0)
  const upgradeAllTotal = ref(0)
  const upgradeAllFailures = ref<string[]>([])
  const upgradeQueue = ref<string[]>([])

  // 批量只处理「版本变高」的插件：同版本内容更新需要逐行决定重启时机，
  // 主题激活会切换公开站点外观，两者都不能被「全部升级」带走。
  const upgradeCandidates = computed(() => extensionUpgradeCandidates(options.extensions.value))

  function resetUpgradeTrust() {
    upgradeTrustMode.value = 'exact'
    upgradeTrustStatus.value = null
    upgradeTrustChallenge.value = null
    upgradeTrustError.value = ''
    upgradeTrustBusy.value = false
  }

  function closeUpgradeDialog() {
    upgradeConfirmOpen.value = false
    upgradeConfirmItem.value = null
    resetUpgradeTrust()
  }

  function finishUpgradeAll(cancelled = false) {
    const completed = upgradeAllCompleted.value
    const total = upgradeAllTotal.value
    const failed = upgradeAllFailures.value.length
    upgradeQueue.value = []
    upgradeAllBusy.value = false

    if (cancelled) {
      toast.add({
        color: 'neutral',
        icon: 'i-lucide-circle-stop',
        title: t('admin.extensions.upgradeAllStopped', { completed, total }),
        duration: 5000
      })
      return
    }
    if (failed > 0) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: t('admin.extensions.upgradeAllPartial', { succeeded: completed, failed }),
        description: upgradeAllFailures.value.join(', '),
        duration: 0
      })
      return
    }
    toast.add({
      color: 'success',
      icon: 'i-lucide-package-check',
      title: t('admin.extensions.upgradeAllSuccess', { count: completed }),
      duration: 5000
    })
  }

  async function performUpgrade(
    item: AdminExtension,
    input: { confirmationToken?: string, confirmCapabilities?: boolean } = {},
    bulk = false
  ) {
    upgradeBusyId.value = item.id
    try {
      const action = extensionUpgradeLifecycleAction(item)
      const body: Record<string, unknown> = {}
      if (input.confirmationToken?.trim()) body.confirmationToken = input.confirmationToken.trim()
      if (input.confirmCapabilities) body.confirmCapabilities = true
      await request<AdminExtension>(`/admin/extensions/${item.id}/${action}`, {
        method: 'POST',
        body,
        headers: { 'Idempotency-Key': globalThis.crypto.randomUUID() }
      })
      await options.refresh()
      if (!bulk) {
        const updated = resolveExtensionUpdateState(item)
        toast.add({
          color: 'success',
          icon: 'i-lucide-package-check',
          title: updated.kind === 'content'
            ? t('admin.extensions.update.restartedToApply')
            : t('admin.extensions.update.upgradedTo', {
              version: item.stagedVersion?.version || item.version
            }),
          duration: 5000
        })
      }
      return true
    } catch (error) {
      const message = apiErrorMessage(error) || t('admin.extensions.actionFailed')
      if (!bulk) {
        toast.add({
          color: 'error',
          icon: 'i-lucide-triangle-alert',
          title: message,
          duration: 0
        })
      }
      return message
    } finally {
      upgradeBusyId.value = ''
    }
  }

  async function prepareUpgrade(item: AdminExtension, bulk = false): Promise<UpgradePreparation> {
    if (!canUpgradePlugin(item)) return 'failed'
    resetUpgradeTrust()
    upgradeConfirmItem.value = item
    upgradeTrustBusy.value = true
    try {
      const status = await request<ExecutableTrustStatus>(executableTrustPath(item, 'upgrade'))
      upgradeTrustStatus.value = status
      if (!status.trustRequired) {
        const capabilityCount = status.impact.capabilities?.length ?? item.capabilityGrants?.length ?? 0
        if (capabilityCount > 0) {
          upgradeTrustMode.value = 'legacy'
          upgradeConfirmOpen.value = true
          return 'waiting'
        }
        const result = await performUpgrade(item, { confirmCapabilities: true }, bulk)
        if (result === true) return 'completed'
        upgradeAllFailures.value.push(`${item.id}: ${result}`)
        return 'failed'
      }
      if (status.trusted) {
        const result = await performUpgrade(item, {}, bulk)
        if (result === true) return 'completed'
        upgradeAllFailures.value.push(`${item.id}: ${result}`)
        return 'failed'
      }
      upgradeConfirmOpen.value = true
      return 'waiting'
    } catch (error) {
      if (apiErrorReason(error) === 'extension.trust_not_required') {
        if ((item.capabilityGrants?.length ?? 0) > 0) {
          upgradeTrustMode.value = 'legacy'
          upgradeConfirmOpen.value = true
          return 'waiting'
        }
        const result = await performUpgrade(item, { confirmCapabilities: true }, bulk)
        if (result === true) return 'completed'
        upgradeAllFailures.value.push(`${item.id}: ${result}`)
        return 'failed'
      }
      upgradeTrustError.value = apiErrorMessage(error) || t('admin.extensions.trust.previewFailed')
      upgradeConfirmOpen.value = true
      return 'waiting'
    } finally {
      upgradeTrustBusy.value = false
    }
  }

  async function continueUpgradeAll() {
    while (upgradeQueue.value.length > 0) {
      const extensionId = upgradeQueue.value.shift()
      const item = options.extensions.value.find(candidate => candidate.id === extensionId)
      if (!item || !canUpgradePlugin(item)) continue
      const result = await prepareUpgrade(item, true)
      if (result === 'waiting') return
      if (result === 'completed') upgradeAllCompleted.value += 1
    }
    finishUpgradeAll()
  }

  async function upgradeExtension(item: AdminExtension) {
    if (upgradeAllBusy.value) return
    await prepareUpgrade(item)
  }

  async function upgradeAllExtensions() {
    if (upgradeAllBusy.value || upgradeCandidates.value.length === 0) return
    closeUpgradeDialog()
    upgradeAllBusy.value = true
    upgradeAllCompleted.value = 0
    upgradeAllFailures.value = []
    upgradeQueue.value = upgradeCandidates.value.map(item => item.id)
    upgradeAllTotal.value = upgradeQueue.value.length
    await continueUpgradeAll()
  }

  async function issueUpgradeTrustChallenge() {
    const item = upgradeConfirmItem.value
    if (!item || !options.isSuperAdmin.value) return
    upgradeTrustBusy.value = true
    upgradeTrustError.value = ''
    try {
      const challenge = await request<ExecutableTrustChallenge>(executableTrustPath(item, 'upgrade', true), {
        method: 'POST',
        body: {}
      })
      upgradeTrustChallenge.value = challenge
      upgradeTrustStatus.value = {
        impact: challenge.impact,
        trustRequired: true,
        trusted: false
      }
    } catch (error) {
      upgradeTrustError.value = apiErrorMessage(error) || t('admin.extensions.trust.challengeFailed')
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: upgradeTrustError.value,
        duration: 0
      })
    } finally {
      upgradeTrustBusy.value = false
    }
  }

  async function confirmUpgradeExtension() {
    const item = upgradeConfirmItem.value
    if (!item) return
    upgradeTrustError.value = ''
    upgradeTrustBusy.value = true
    const bulk = upgradeAllBusy.value
    const result = await performUpgrade(item, upgradeTrustMode.value === 'exact'
      ? { confirmationToken: upgradeTrustChallenge.value?.token }
      : { confirmCapabilities: true }, bulk)
    if (result !== true) {
      upgradeTrustError.value = result
      upgradeTrustChallenge.value = null
      try {
        upgradeTrustStatus.value = await request<ExecutableTrustStatus>(executableTrustPath(item, 'upgrade'))
      } catch {
        // Keep the upgrade failure visible; requesting a new challenge rechecks the exact artifact.
      }
      upgradeTrustBusy.value = false
      return
    }

    closeUpgradeDialog()
    if (bulk) {
      upgradeAllCompleted.value += 1
      await continueUpgradeAll()
    }
  }

  function cancelUpgradeExtension() {
    const bulk = upgradeAllBusy.value
    closeUpgradeDialog()
    if (bulk) finishUpgradeAll(true)
  }

  return {
    upgradeCandidates,
    upgradeConfirmOpen,
    upgradeConfirmItem,
    upgradeTrustMode,
    upgradeTrustStatus,
    upgradeTrustChallenge,
    upgradeTrustError,
    upgradeTrustBusy,
    upgradeBusyId,
    upgradeAllBusy,
    upgradeAllCompleted,
    upgradeAllTotal,
    upgradeExtension,
    upgradeAllExtensions,
    issueUpgradeTrustChallenge,
    confirmUpgradeExtension,
    cancelUpgradeExtension
  }
}
