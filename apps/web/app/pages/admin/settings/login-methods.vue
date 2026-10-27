<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
/**
 * 管理后台「登录方式 / Login methods」。
 * Host 聚合 Registry + 激活目录 + 设置 + callback；不硬编码 GitHub 厂商逻辑。
 * 设置表单复用 SFExtensionSettingsRenderer（与扩展动态设置页同源）。
 */
import SFExtensionSettingsRenderer from '~/components/extensions/settings/SFExtensionSettingsRenderer.vue'
import { apiErrorMessage, apiErrorStatusCode } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import {
  recommendedExtensionSettingValues,
  type AdminExtensionPageBootstrap,
  type AdminExtensionSettings,
  type AdminExtensionSettingsAction,
  type AdminExtensionSettingsActionResult
} from '~/utils/admin/adminExtensions'
import {
  adminProbeFeedback,
  adminProbeLabelKind,
  adminProviderIcon,
  adminProviderShortDigest,
  adminProviderStateBadges,
  adminProviderSupportsOp,
  adminProviderTitle,
  type AdminIdentityProvider
} from '~/utils/admin/adminLoginMethods'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminLoginMethods'
})

export type { AdminIdentityProvider }

type ProviderPatch = {
  expectedRevision: number
  loginEnabled?: boolean
  registrationEnabled?: boolean
  linkEnabled?: boolean
  priority?: number
}

const { t, locale } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const adminPage = useAdminPage('/settings/login-methods')
const adminRoutes = useAdminRoutes()

const providers = ref<AdminIdentityProvider[]>([])
const pending = ref(true)
const saving = ref(false)
const probingId = ref('')
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string>>({})
const activeTabId = ref<string | null>(null)

// 每个 provider 的扩展设置表单状态（按 ownerExtensionId 缓存）。
const settingsByExtension = reactive<Record<string, AdminExtensionSettings | null>>({})
const settingsValues = reactive<Record<string, Record<string, string>>>({})
const settingsLoading = reactive<Record<string, boolean>>({})
const settingsSaving = reactive<Record<string, boolean>>({})
const actionLoading = reactive<Record<string, Record<string, boolean>>>({})
const actionResults = reactive<Record<string, Record<string, AdminExtensionSettingsActionResult | undefined>>>({})

const hasProviders = computed(() => providers.value.length > 0)
const anySafeMode = computed(() => providers.value.some(item => item.safeMode))

const selectedProvider = computed(() => 
  providers.value.find(p => p.id === activeTabId.value) || null
)

// 提供方展示名只来自 Host catalog；文案用它替换旧版写死的 GitHub。
const selectedProviderLabel = computed(() =>
  selectedProvider.value?.label?.trim() || t('auth.providers.genericName')
)

useSeoMeta({
  title: () => t('admin.loginMethods.metaTitle')
})

function stateBadge(item: AdminIdentityProvider) {
  // 公开状态由标题中的唯一徽标负责，避免 artifact drift 时出现相反文案。
  return adminProviderStateBadges(item).filter(badge => badge.key !== 'publiclyActivated')
}

function supportsOp(item: AdminIdentityProvider, op: 'login' | 'registration' | 'link') {
  return adminProviderSupportsOp(item, op)
}

// T8B：label/icon 只消费 Host catalog，禁止 id 子串猜品牌。
function providerIcon(item: AdminIdentityProvider) {
  return adminProviderIcon(item)
}

function providerTitle(item: AdminIdentityProvider) {
  return adminProviderTitle(item)
}

function shortDigest(digest: string) {
  return adminProviderShortDigest(digest)
}

function probeLabel(item: AdminIdentityProvider) {
  switch (adminProbeLabelKind(item)) {
    case 'never':
      return t('admin.loginMethods.probe.never')
    case 'pending':
      return t('admin.loginMethods.probe.pending')
    case 'unavailable':
      return t('admin.loginMethods.probe.unavailable')
    case 'ok':
      return t('admin.loginMethods.probe.ok')
    default:
      return (item.lastProbeReason || '').trim()
  }
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

function errorToast(title: string) {
  toast.add({
    color: 'error',
    icon: 'i-lucide-triangle-alert',
    title,
    duration: 0
  })
}

async function loadProviders() {
  pending.value = true
  errorMessage.value = ''
  try {
    const list = await request<AdminIdentityProvider[]>('/admin/identity/providers')
    providers.value = Array.isArray(list) ? list : []
    if (!activeTabId.value && providers.value[0]) {
      activeTabId.value = providers.value[0].id
    }
    // 展开项设置在后台刷新，不阻塞列表。
    const selected = providers.value.find(item => item.id === activeTabId.value)
    if (selected?.ownerExtensionId) {
      void ensureSettings(selected.ownerExtensionId)
    }
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.loginMethods.loadFailed')
  } finally {
    pending.value = false
  }
}

async function ensureSettings(extensionId: string) {
  if (!extensionId || settingsByExtension[extensionId]) {
    return
  }
  settingsLoading[extensionId] = true
  try {
    const bootstrap = await request<AdminExtensionPageBootstrap>(
      `/admin/extensions/${encodeURIComponent(extensionId)}/page-bootstrap?path=${encodeURIComponent('/settings')}`
    )
    const settings = bootstrap.settings || null
    settingsByExtension[extensionId] = settings
    const values: Record<string, string> = {}
    for (const item of settings?.items || []) {
      values[item.key] = item.value
    }
    settingsValues[extensionId] = values
  } catch (error) {
    // 无 extension 权限或扩展未发现时保留空态；不阻断激活开关。
    settingsByExtension[extensionId] = null
    if (import.meta.client) {
      toast.add({
        color: 'warning',
        icon: 'i-lucide-info',
        title: apiErrorMessage(error) || t('admin.loginMethods.settingsLoadFailed'),
        duration: 5000
      })
    }
  } finally {
    settingsLoading[extensionId] = false
  }
}

function expandProvider(item: AdminIdentityProvider) {
  activeTabId.value = item.id
  if (item.ownerExtensionId) {
    void ensureSettings(item.ownerExtensionId)
  }
}

function recommendedApplied(extensionId: string) {
  const settings = settingsByExtension[extensionId]
  const values = settingsValues[extensionId] || {}
  const items = settings?.items || []
  if (!items.length) return true
  const recommended = recommendedExtensionSettingValues(items)
  return Object.entries(recommended).every(([key, value]) => (values[key] ?? '') === value)
}

function updateSettingValue(extensionId: string, key: string, value: string) {
  if (!settingsValues[extensionId]) {
    settingsValues[extensionId] = {}
  }
  settingsValues[extensionId][key] = value
}

async function saveSettings(extensionId: string) {
  settingsSaving[extensionId] = true
  fieldErrors.value = {}
  try {
    const updated = await request<AdminExtensionSettings>(`/admin/extensions/${encodeURIComponent(extensionId)}/settings`, {
      method: 'PUT',
      body: { values: { ...(settingsValues[extensionId] || {}) } }
    })
    settingsByExtension[extensionId] = updated
    const values: Record<string, string> = {}
    for (const item of updated.items) {
      values[item.key] = item.value
    }
    settingsValues[extensionId] = values
    successToast(t('admin.loginMethods.settingsSaved'))
    await loadProviders()
  } catch (error) {
    errorToast(apiErrorMessage(error) || t('admin.loginMethods.settingsSaveFailed'))
  } finally {
    settingsSaving[extensionId] = false
  }
}

async function resetSettings(extensionId: string) {
  const settings = settingsByExtension[extensionId]
  if (!settings) return
  settingsSaving[extensionId] = true
  try {
    const updated = await request<AdminExtensionSettings>(`/admin/extensions/${encodeURIComponent(extensionId)}/settings`, {
      method: 'PUT',
      body: { values: recommendedExtensionSettingValues(settings.items || []) }
    })
    settingsByExtension[extensionId] = updated
    const values: Record<string, string> = {}
    for (const item of updated.items) {
      values[item.key] = item.value
    }
    settingsValues[extensionId] = values
    successToast(t('admin.loginMethods.settingsReset'), t('admin.loginMethods.secretsPreserved'))
    await loadProviders()
  } catch (error) {
    errorToast(apiErrorMessage(error) || t('admin.loginMethods.settingsSaveFailed'))
  } finally {
    settingsSaving[extensionId] = false
  }
}

async function executeSettingsAction(extensionId: string, action: AdminExtensionSettingsAction) {
  const settings = settingsByExtension[extensionId]
  if (!settings || !action.available) return
  if (!actionLoading[extensionId]) actionLoading[extensionId] = {}
  if (!actionResults[extensionId]) actionResults[extensionId] = {}
  actionLoading[extensionId][action.id] = true
  actionResults[extensionId][action.id] = undefined
  const values: Record<string, string> = {}
  const secrets: Record<string, { mode: 'preserve' | 'replace', value?: string }> = {}
  const form = settingsValues[extensionId] || {}
  const allowed = new Set(action.fields?.length ? action.fields : settings.items.map(item => item.key))
  for (const item of settings.items) {
    if (!allowed.has(item.key)) continue
    if (item.type === 'secret') {
      const draft = form[item.key] || ''
      secrets[item.key] = draft ? { mode: 'replace', value: draft } : { mode: 'preserve' }
    } else {
      values[item.key] = form[item.key] ?? ''
    }
  }
  try {
    const result = await request<AdminExtensionSettingsActionResult>(
      `/admin/extensions/${encodeURIComponent(extensionId)}/settings/actions/${encodeURIComponent(action.id)}`,
      { method: 'POST', body: { values, secrets } }
    )
    actionResults[extensionId][action.id] = result
    if (result.success) {
      successToast(result.message || action.label)
    }
  } catch (error) {
    const message = apiErrorMessage(error) || t('admin.loginMethods.actionFailed')
    actionResults[extensionId][action.id] = { success: false, reason: 'request_failed', message, durationMs: 0 }
    errorToast(message)
  } finally {
    actionLoading[extensionId][action.id] = false
  }
}

async function patchProvider(item: AdminIdentityProvider, patch: Omit<ProviderPatch, 'expectedRevision'>) {
  const key = item.id
  fieldErrors.value = { ...fieldErrors.value, [key]: '' }
  saving.value = true
  try {
    await request(`/admin/identity/providers/${encodeURIComponent(item.id)}`, {
      method: 'PATCH',
      body: {
        expectedRevision: item.revision,
        ...patch
      } satisfies ProviderPatch
    })
    successToast(t('admin.loginMethods.activationSaved'))
    await loadProviders()
  } catch (error: unknown) {
    const message = apiErrorMessage(error) || t('admin.loginMethods.activationSaveFailed')
    // stale revision：刷新列表并提示运营重试。
    if (apiErrorStatusCode(error) === 409) {
      fieldErrors.value[key] = t('admin.loginMethods.staleRevision')
      await loadProviders()
    } else {
      fieldErrors.value[key] = message
    }
    errorToast(message)
  } finally {
    saving.value = false
  }
}

async function toggleOperation(item: AdminIdentityProvider, op: 'login' | 'registration' | 'link', value: boolean) {
  if (op === 'login') {
    await patchProvider(item, { loginEnabled: value })
  } else if (op === 'registration') {
    await patchProvider(item, { registrationEnabled: value })
  } else {
    await patchProvider(item, { linkEnabled: value })
  }
}

async function probeProvider(item: AdminIdentityProvider) {
  probingId.value = item.id
  fieldErrors.value = { ...fieldErrors.value, [item.id]: '' }
  try {
    const result = await request<{ ok: boolean, status?: string, reason?: string, message?: string }>(
      `/admin/identity/providers/${encodeURIComponent(item.id)}/probe`,
      { method: 'POST' }
    )
    // T8B：真实 provider.probe 可 ok=true；pending 不是产品成功路径。
    const feedback = adminProbeFeedback(result)
    if (feedback.success) {
      successToast(t('admin.loginMethods.probe.ok'), result?.message || feedback.reason)
    } else {
      toast.add({
        color: 'info',
        icon: 'i-lucide-activity',
        title: t('admin.loginMethods.probe.recorded'),
        description: feedback.reason === 'probe_pending'
          ? t('admin.loginMethods.probe.pendingHelp')
          : (feedback.reason || result?.message || t('admin.loginMethods.probe.unavailable')),
        duration: 5000
      })
    }
    await loadProviders()
  } catch (error) {
    errorToast(apiErrorMessage(error) || t('admin.loginMethods.probe.failed'))
  } finally {
    probingId.value = ''
  }
}

async function copyCallback(item: AdminIdentityProvider) {
  const value = (item.callbackUrl || item.callbackPath || '').trim()
  if (!value) {
    errorToast(t('admin.loginMethods.callbackMissing'))
    return
  }
  try {
    if (import.meta.client && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
    } else {
      throw new Error('clipboard unavailable')
    }
    successToast(t('admin.loginMethods.callbackCopied'))
  } catch {
    errorToast(t('admin.loginMethods.callbackCopyFailed'))
  }
}

async function restoreDefaults() {
  saving.value = true
  errorMessage.value = ''
  try {
    await request('/admin/identity/providers/reset', { method: 'POST' })
    successToast(t('admin.loginMethods.defaultsRestored'), t('admin.loginMethods.secretsPreserved'))
    await loadProviders()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.loginMethods.defaultsRestoreFailed')
    errorToast(errorMessage.value)
  } finally {
    saving.value = false
  }
}

await loadProviders()

// locale 变化时重新拉设置标签（Host 已按 locale 解析）。
watch(locale, () => {
  for (const key of Object.keys(settingsByExtension)) {
    delete settingsByExtension[key]
  }
  const selected = providers.value.find(item => item.id === activeTabId.value)
  if (selected?.ownerExtensionId) {
    void ensureSettings(selected.ownerExtensionId)
  }
})
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.loginMethods.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.loginMethods.description') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-log-in" class="size-4" />
        <span class="truncate">{{ t('admin.loginMethods.count', { count: providers.length }) }}</span>
      </div>
    </template>
    <template #right>
      <div class="flex flex-wrap gap-2">
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-rotate-ccw"
          class="border-slate-200 dark:border-zinc-700"
          :loading="saving"
          @click="restoreDefaults"
        >
          {{ t('admin.loginMethods.restoreDefaults') }}
        </UButton>
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          class="border-slate-200 dark:border-zinc-700"
          :loading="pending"
          @click="loadProviders"
        >
          {{ t('admin.home.refresh') }}
        </UButton>
      </div>
    </template>
  </UDashboardToolbar>

  <div class="flex w-full min-w-0 flex-col gap-4">
    <SFAlert
      v-if="errorMessage"
      variant="danger"
      :title="errorMessage"
      closable
      @close="errorMessage = ''"
    />

    <UAlert
      color="primary"
      variant="soft"
      icon="i-lucide-sparkles"
      class="w-full shrink-0"
      :title="t('admin.loginMethods.recommendedTitle')"
      :description="`${t('admin.loginMethods.recommendedDescription', { provider: selectedProviderLabel })} ${t('admin.loginMethods.secretsPreserved')}`"
    />

    <SFAlert
      v-if="anySafeMode"
      variant="warning"
      :title="t('admin.loginMethods.safeModeTitle')"
      :description="t('admin.loginMethods.safeModeDescription')"
    />

    <div v-if="pending && !hasProviders" class="rounded-lg border border-slate-200 bg-white p-8 text-sm text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      {{ t('admin.loginMethods.loading') }}
    </div>

    <div
      v-else-if="!hasProviders"
      class="rounded-lg border border-dashed border-slate-200 bg-white p-10 dark:border-zinc-700 dark:bg-zinc-900"
    >
      <SFEmptyState
        icon-label="AUTH"
        :title="t('admin.loginMethods.emptyTitle')"
        :description="t('admin.loginMethods.emptyDescription')"
      />
      <div class="mt-5 flex flex-wrap justify-center gap-2">
        <UButton icon="i-lucide-plug" color="primary" :to="adminRoutes.path('/extensions/plugins')">
          {{ t('admin.loginMethods.openPlugins') }}
        </UButton>
        <UButton icon="i-lucide-refresh-cw" color="neutral" variant="subtle" :loading="pending" @click="loadProviders">
          {{ t('admin.home.refresh') }}
        </UButton>
      </div>
    </div>

    <div v-else class="w-full">
      <div
        role="tablist"
        :aria-label="t('admin.loginMethods.title')"
        class="relative z-0 mb-4 flex flex-wrap gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
      >
        <UButton
          v-for="item in providers"
          :key="item.id"
          size="md"
          class="min-h-10 px-4"
          :color="activeTabId === item.id ? 'primary' : 'neutral'"
          :variant="activeTabId === item.id ? 'solid' : 'ghost'"
          :leading-icon="providerIcon(item)"
          role="tab"
          :aria-selected="activeTabId === item.id"
          @click="expandProvider(item)"
        >
          {{ providerTitle(item) }}
        </UButton>
      </div>

      <div v-if="selectedProvider" class="space-y-4">
              <p v-if="fieldErrors[selectedProvider?.id]" class="text-sm text-red-600 dark:text-red-400">
                {{ fieldErrors[selectedProvider?.id] }}
              </p>

              <SFAlert
                v-if="selectedProvider?.activated && !selectedProvider.artifactBound"
                variant="warning"
                :title="t('admin.loginMethods.artifactDriftTitle')"
                :description="t('admin.loginMethods.artifactDriftDescription')"
              />

              <div class="grid gap-4 lg:grid-cols-2">
                <section class="space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950/40">
                  <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.loginMethods.operationsTitle') }}
                  </h3>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.loginMethods.operationsHelp') }}
                  </p>
                  <div class="space-y-3">
                    <div class="flex items-center justify-between gap-3">
                      <div>
                        <div class="text-sm font-medium">{{ t('admin.loginMethods.ops.login') }}</div>
                        <div class="text-xs text-slate-500">{{ t('admin.loginMethods.ops.loginHelp', { provider: selectedProviderLabel }) }}</div>
                      </div>
                      <USwitch
                        :model-value="selectedProvider?.loginEnabled"
                        :disabled="saving || !supportsOp(selectedProvider!, 'login') || !selectedProvider?.enabled"
                        @update:model-value="toggleOperation(selectedProvider!, 'login', Boolean($event))"
                      />
                    </div>
                    <div class="flex items-center justify-between gap-3">
                      <div>
                        <div class="text-sm font-medium">{{ t('admin.loginMethods.ops.registration') }}</div>
                        <div class="text-xs text-slate-500">{{ t('admin.loginMethods.ops.registrationHelp', { provider: selectedProviderLabel }) }}</div>
                      </div>
                      <USwitch
                        :model-value="selectedProvider?.registrationEnabled"
                        :disabled="saving || !supportsOp(selectedProvider!, 'registration') || !selectedProvider?.enabled"
                        @update:model-value="toggleOperation(selectedProvider!, 'registration', Boolean($event))"
                      />
                    </div>
                    <div class="flex items-center justify-between gap-3">
                      <div>
                        <div class="text-sm font-medium">{{ t('admin.loginMethods.ops.link') }}</div>
                        <div class="text-xs text-slate-500">{{ t('admin.loginMethods.ops.linkHelp', { provider: selectedProviderLabel }) }}</div>
                      </div>
                      <USwitch
                        :model-value="selectedProvider?.linkEnabled"
                        :disabled="saving || !supportsOp(selectedProvider!, 'link') || !selectedProvider?.enabled"
                        @update:model-value="toggleOperation(selectedProvider!, 'link', Boolean($event))"
                      />
                    </div>
                  </div>
                  <p class="text-xs text-slate-400">
                    {{ t('admin.loginMethods.revision', { revision: selectedProvider?.revision }) }}
                  </p>
                </section>

                <section class="space-y-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950/40">
                  <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.loginMethods.callbackTitle') }}
                  </h3>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.loginMethods.callbackHelp') }}
                  </p>
                  <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
                    <code class="min-w-0 flex-1 truncate rounded-md bg-slate-50 px-3 py-2 text-xs text-slate-700 dark:bg-zinc-950 dark:text-zinc-200">
                      {{ selectedProvider?.callbackUrl || selectedProvider?.callbackPath || '—' }}
                    </code>
                    <UButton
                      color="neutral"
                      variant="outline"
                      leading-icon="i-lucide-copy"
                      class="shrink-0 border-slate-200 dark:border-zinc-700"
                      @click="copyCallback(selectedProvider!)"
                    >
                      {{ t('admin.loginMethods.copyCallback') }}
                    </UButton>
                  </div>

                  <div class="border-t border-slate-200 pt-3 dark:border-zinc-800">
                    <div class="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <div class="text-sm font-medium">{{ t('admin.loginMethods.probeTitle') }}</div>
                        <div class="text-xs text-slate-500">{{ probeLabel(selectedProvider!) }}</div>
                        <div v-if="selectedProvider?.lastProbeAt" class="mt-0.5 text-xs text-slate-400">
                          {{ selectedProvider.lastProbeAt }}
                        </div>
                      </div>
                      <UButton
                        color="neutral"
                        variant="subtle"
                        leading-icon="i-lucide-activity"
                        :loading="probingId === selectedProvider.id"
                        :disabled="!selectedProvider?.enabled"
                        @click="probeProvider(selectedProvider!)"
                      >
                        {{ t('admin.loginMethods.runProbe') }}
                      </UButton>
                    </div>
                    <p class="mt-2 text-xs text-slate-500">
                      {{ t('admin.loginMethods.probe.help') }}
                    </p>
                  </div>
                </section>
              </div>

              <section class="space-y-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                      {{ t('admin.loginMethods.settingsTitle') }}
                    </h3>
                    <p class="text-xs text-slate-500 dark:text-zinc-400">
                      {{ t('admin.loginMethods.settingsHelp') }}
                    </p>
                  </div>
                  <UButton
                    v-if="selectedProvider?.settingsPath || selectedProvider?.ownerExtensionId"
                    color="neutral"
                    variant="ghost"
                    size="sm"
                    leading-icon="i-lucide-external-link"
                    :to="adminRoutes.path(selectedProvider?.settingsPath || `/extensions/${selectedProvider?.ownerExtensionId}/pages/settings`)"
                  >
                    {{ t('admin.loginMethods.openExtensionSettings') }}
                  </UButton>
                </div>

                <div
                  v-if="settingsLoading[selectedProvider?.ownerExtensionId]"
                  class="rounded-lg border border-slate-200 p-6 text-sm text-slate-500 dark:border-zinc-800 dark:text-zinc-400"
                >
                  {{ t('admin.loginMethods.settingsLoading') }}
                </div>
                <SFExtensionSettingsRenderer
                  v-else-if="settingsByExtension[selectedProvider?.ownerExtensionId]"
                  :settings="settingsByExtension[selectedProvider?.ownerExtensionId] || null"
                  :values="settingsValues[selectedProvider?.ownerExtensionId] || {}"
                  :loading="Boolean(settingsLoading[selectedProvider?.ownerExtensionId])"
                  :saving="Boolean(settingsSaving[selectedProvider?.ownerExtensionId])"
                  :recommended-applied="recommendedApplied(selectedProvider?.ownerExtensionId)"
                  :action-loading="actionLoading[selectedProvider?.ownerExtensionId] || {}"
                  :action-results="actionResults[selectedProvider?.ownerExtensionId] || {}"
                  @update="(key, value) => updateSettingValue(selectedProvider?.ownerExtensionId!, key, value)"
                  @save="saveSettings(selectedProvider?.ownerExtensionId!)"
                  @reset="resetSettings(selectedProvider?.ownerExtensionId!)"
                  @action="(action) => executeSettingsAction(selectedProvider?.ownerExtensionId!, action)"
                />
                <SFAlert
                  v-else
                  variant="info"
                  :title="t('admin.loginMethods.settingsUnavailableTitle')"
                  :description="t('admin.loginMethods.settingsUnavailableDescription')"
                />
              </section>
      </div>
    </div>
  </div>
</template>
