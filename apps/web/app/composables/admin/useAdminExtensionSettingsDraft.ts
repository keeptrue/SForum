import { apiErrorMessage } from '~/composables/useApiClient'
import {
  recommendedExtensionSettingValues,
  type AdminExtensionSettings,
  type AdminExtensionSettingsAction,
  type AdminExtensionSettingsActionResult
} from '~/utils/admin/adminExtensions'

/**
 * 站点设置页里的「插件设置 tab」草稿。
 *
 * 与扩展动态设置页同源：设置文档由 `GET /admin/settings/surface-tabs` 内联下发，
 * 保存/重置/动作的请求形状与扩展设置页完全一致（`PUT /admin/extensions/{id}/settings`
 * 与 `.../settings/actions/{actionId}`），因此插件作者不需要为新入口再写一套逻辑。
 *
 * 每个 tab 一个实例（一个插件一份文档），不需要像登录方式页那样按扩展 id 建映射。
 */
export type UseAdminExtensionSettingsDraftOptions = {
  /** 内联的插件设置文档；tab 切换时可以是新对象。 */
  settings: Ref<AdminExtensionSettings | null>
  /** 保存/重置成功后的回调（例如刷新左侧计数或 tab 标题）。 */
  onSaved?: (settings: AdminExtensionSettings) => void
}

function valuesFrom(settings: AdminExtensionSettings | null): Record<string, string> {
  const values: Record<string, string> = {}
  for (const item of settings?.items || []) {
    values[item.key] = item.value
  }
  return values
}

export function useAdminExtensionSettingsDraft(options: UseAdminExtensionSettingsDraftOptions) {
  const { t } = useI18n()
  const toast = useToast()
  const { request } = useApiClient()

  const values = ref<Record<string, string>>(valuesFrom(options.settings.value))
  const saving = ref(false)
  const actionLoading = ref<Record<string, boolean>>({})
  const actionResults = ref<Record<string, AdminExtensionSettingsActionResult | undefined>>({})

  const extensionId = computed(() => options.settings.value?.extensionId || '')
  const loading = computed(() => !options.settings.value)
  const recommendedApplied = computed(() => {
    const items = options.settings.value?.items || []
    if (!items.length) return true
    const recommended = recommendedExtensionSettingValues(items)
    return Object.entries(recommended).every(([key, value]) => (values.value[key] ?? '') === value)
  })

  watch(() => options.settings.value, (settings) => {
    values.value = valuesFrom(settings)
    actionLoading.value = {}
    actionResults.value = {}
  })

  function updateValue(key: string, value: string) {
    values.value = { ...values.value, [key]: value }
  }

  function successToast(title: string, description?: string) {
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title,
      description,
      duration: 5000
    })
  }

  // 错误不自动消失：运营需要看清插件到底回了什么。
  function errorToast(title: string) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title,
      duration: 0
    })
  }

  function applySaved(updated: AdminExtensionSettings) {
    values.value = valuesFrom(updated)
    options.onSaved?.(updated)
  }

  async function save() {
    if (!extensionId.value || saving.value) return
    saving.value = true
    try {
      const updated = await request<AdminExtensionSettings>(
        `/admin/extensions/${encodeURIComponent(extensionId.value)}/settings`,
        { method: 'PUT', body: { values: { ...values.value } } }
      )
      applySaved(updated)
      successToast(t('admin.settings.contributions.saved'))
    } catch (error) {
      errorToast(apiErrorMessage(error) || t('admin.settings.contributions.saveFailed'))
    } finally {
      saving.value = false
    }
  }

  async function reset() {
    const settings = options.settings.value
    if (!settings || !extensionId.value || saving.value) return
    saving.value = true
    try {
      const updated = await request<AdminExtensionSettings>(
        `/admin/extensions/${encodeURIComponent(extensionId.value)}/settings`,
        { method: 'PUT', body: { values: recommendedExtensionSettingValues(settings.items || []) } }
      )
      applySaved(updated)
      successToast(t('admin.settings.contributions.resetDone'), t('admin.settings.contributions.secretsPreserved'))
    } catch (error) {
      errorToast(apiErrorMessage(error) || t('admin.settings.contributions.saveFailed'))
    } finally {
      saving.value = false
    }
  }

  async function executeAction(action: AdminExtensionSettingsAction) {
    const settings = options.settings.value
    if (!settings || !action.available || !extensionId.value) return
    actionLoading.value = { ...actionLoading.value, [action.id]: true }
    actionResults.value = { ...actionResults.value, [action.id]: undefined }
    const payload: Record<string, string> = {}
    const secrets: Record<string, { mode: 'preserve' | 'replace', value?: string }> = {}
    const allowed = new Set(action.fields?.length ? action.fields : settings.items.map(item => item.key))
    for (const item of settings.items) {
      if (!allowed.has(item.key)) continue
      if (item.type === 'secret') {
        const draft = values.value[item.key] || ''
        secrets[item.key] = draft ? { mode: 'replace', value: draft } : { mode: 'preserve' }
      } else {
        payload[item.key] = values.value[item.key] ?? ''
      }
    }
    try {
      const result = await request<AdminExtensionSettingsActionResult>(
        `/admin/extensions/${encodeURIComponent(extensionId.value)}/settings/actions/${encodeURIComponent(action.id)}`,
        { method: 'POST', body: { values: payload, secrets } }
      )
      actionResults.value = { ...actionResults.value, [action.id]: result }
      if (result.success) {
        successToast(result.message || action.label)
      }
    } catch (error) {
      const message = apiErrorMessage(error) || t('admin.settings.contributions.actionFailed')
      actionResults.value = {
        ...actionResults.value,
        [action.id]: { success: false, reason: 'request_failed', message, durationMs: 0 }
      }
      errorToast(message)
    } finally {
      actionLoading.value = { ...actionLoading.value, [action.id]: false }
    }
  }

  return {
    values,
    saving,
    loading,
    recommendedApplied,
    actionLoading,
    actionResults,
    updateValue,
    save,
    reset,
    executeAction
  }
}
