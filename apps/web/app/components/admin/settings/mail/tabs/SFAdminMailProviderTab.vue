<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { normalizeEnabledOption } from '~/composables/useWebOptions'
import type { MailProvider, MailProviderResponse, MailProviderState } from '../model'
const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const { fetchAdminEnvelope } = useWebOptions()
const adminRoutes = useAdminRoutes()
const pending = ref(true)
const saving = ref(false)
const providers = ref<MailProvider[]>([])
const selected = ref('')
const selectedProvider = ref<MailProviderResponse['selected'] | null>(null)
const state = ref<MailProviderState>('unconfigured')
const recipient = ref('')
const recipientError = ref('')
const adminEmail = ref('')
const errorMessage = ref('')
const welcomeEnabled = ref(false)
const enabledProviders = computed(() => providers.value.filter(item => item.status !== 'unavailable'))
const providerItems = computed(() => enabledProviders.value.map(item => ({ label: `${item.label}${item.healthy ? '' : ` (${t('admin.mailSettings.unhealthy')})`}`, value: item.extensionId })))
const providerSettingsTarget = computed(() => selectedProvider.value?.extensionId || selected.value)
const statusBadge = computed(() => {
  if (state.value === 'ready') return { color: 'success' as const, label: t('admin.mailSettings.ready') }
  if (state.value === 'unavailable') return { color: 'warning' as const, label: t('admin.mailSettings.unavailable') }
  return { color: 'warning' as const, label: t('admin.mailSettings.needsSetup') }
})
onMounted(load)
defineExpose({ refresh: load, pending })

async function load() {
  pending.value = true
  errorMessage.value = ''
  try {
    const [response, envelope] = await Promise.all([
      request<MailProviderResponse>('/admin/mail/providers'),
      fetchAdminEnvelope()
    ])
    providers.value = response.items
    selectedProvider.value = response.selected?.extensionId ? response.selected : null
    state.value = response.state
    // Show only the resolved selection. When the provider is unavailable or
    // ambiguous, leave the select empty instead of implying a choice.
    selected.value = response.state === 'ready' ? response.selected.extensionId : ''
    adminEmail.value = envelope.data.find(item => item.name === 'site.admin_email')?.value?.trim() || ''
    welcomeEnabled.value = normalizeEnabledOption(envelope.data.find(item => item.name === 'mail.welcome.enabled')?.value, false)
    if (!recipient.value) recipient.value = adminEmail.value
  } catch (error) { errorMessage.value = apiErrorMessage(error) || t('admin.mailSettings.loadFailed') } finally { pending.value = false }
}
async function choose() { if (await act(() => request('/admin/mail/provider', { method: 'PUT', body: { extensionId: selected.value } }), 'admin.mailSettings.saved')) await load() }
async function reset() { if (await act(() => request('/admin/mail/provider/reset', { method: 'POST' }), 'admin.mailSettings.resetDone', 'admin.mailSettings.secretsPreserved')) await load() }
async function testMail() {
  const explicit = recipient.value.trim()
  recipientError.value = ''
  if (explicit && !/^\S+@\S+\.\S+$/.test(explicit)) { recipientError.value = t('admin.mailSettings.invalidRecipient'); return }
  if (!explicit && !adminEmail.value) { recipientError.value = t('admin.mailSettings.recipientOrAdminEmailRequired'); return }
  await act(() => request('/admin/mail/test', { method: 'POST', body: explicit ? { recipient: explicit } : {} }), 'admin.mailSettings.testQueued')
}
async function saveWelcomeMail() {
  await act(
    () => request('/admin/web-options', { method: 'PUT', body: { options: [{ name: 'mail.welcome.enabled', value: welcomeEnabled.value ? 'enabled' : 'disabled' }] } }),
    'admin.mailSettings.welcomeSaved'
  )
}
async function act(action: () => Promise<unknown>, key: string, description?: string): Promise<boolean> {
  saving.value = true
  try {
    await action()
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t(key), description: description ? t(description) : undefined, duration: 5000 })
    return true
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.mailSettings.saveFailed')
    return false
  } finally { saving.value = false }
}
</script>
<template>
  <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <template #header><div><h2 class="text-base font-bold">{{ t('admin.mailSettings.providerConfigTitle') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.mailSettings.providerConfigDescription') }}</p></div></template>
    <SFAlert v-if="errorMessage" variant="danger" :title="errorMessage" />
    <SFEmptyState v-if="!pending && enabledProviders.length === 0 && !selectedProvider" icon-label="MAIL" :title="t('admin.mailSettings.noProvidersTitle')" :description="t('admin.mailSettings.noProvidersDescription')" />
    <div v-else class="max-w-4xl space-y-6">
      <UAlert
        v-if="state === 'unavailable' && selectedProvider"
        color="warning"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="t('admin.mailSettings.selectedUnavailable')"
        :description="`${selectedProvider.label} · ${selectedProvider.extensionId}`"
      />
      <UFormField :label="t('admin.mailSettings.provider')" :description="t('admin.mailSettings.providerHelp')">
        <USelect v-model="selected" :items="providerItems" value-key="value" label-key="label" class="w-full" :placeholder="t('admin.mailSettings.providerPlaceholder')" />
        <p v-if="state === 'unconfigured'" class="mt-1 text-xs text-muted">{{ t('admin.mailSettings.defaultProviderHint') }}</p>
      </UFormField>
      <div class="flex flex-wrap gap-2">
        <UButton :loading="saving" :disabled="!selected" icon="i-lucide-save" @click="choose">{{ t('admin.mailSettings.save') }}</UButton>
        <UButton color="neutral" variant="outline" icon="i-lucide-rotate-ccw" @click="reset">{{ t('admin.mailSettings.reset') }}</UButton>
        <UButton v-if="providerSettingsTarget" :to="adminRoutes.path(`/extensions/${providerSettingsTarget}/pages/settings`)" color="neutral" variant="outline" icon="i-lucide-settings">{{ t('admin.mailSettings.providerSettings') }}</UButton>
      </div>
      <div class="border-t border-slate-200 pt-5 dark:border-zinc-800">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <label class="flex min-w-0 cursor-pointer items-start gap-3 text-sm">
            <input v-model="welcomeEnabled" type="checkbox" class="mt-1 size-4">
            <span><strong class="block">{{ t('admin.mailSettings.welcomeEnabled') }}</strong><span class="mt-1 block text-xs text-muted">{{ t('admin.mailSettings.welcomeEnabledHelp') }}</span></span>
          </label>
          <UButton color="neutral" variant="outline" icon="i-lucide-save" :loading="saving" @click="saveWelcomeMail">{{ t('admin.mailSettings.saveWelcome') }}</UButton>
        </div>
      </div>
      <div class="border-t border-slate-200 pt-5 dark:border-zinc-800"><UFormField :label="t('admin.mailSettings.testRecipient')" :error="recipientError"><UInput v-model="recipient" type="email" class="w-full" /></UFormField><UButton class="mt-3" icon="i-lucide-send" :disabled="state !== 'ready'" :loading="saving" @click="testMail">{{ t('admin.mailSettings.sendTest') }}</UButton></div>
      <UBadge :color="statusBadge.color" variant="soft">{{ statusBadge.label }}</UBadge>
    </div>
  </UCard>
</template>
