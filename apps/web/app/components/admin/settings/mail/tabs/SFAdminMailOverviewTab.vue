<script setup lang="ts">
import type { MailProviderResponse, MailProviderState } from '../model'
const { t } = useI18n()
const { request } = useApiClient()
const pending = ref(true)
const state = ref<MailProviderState>('unconfigured')
const providerLabel = ref('')
const errorMessage = ref('')
const mailStatusText = computed(() => {
  if (state.value === 'ready') return t('admin.mailSettings.providerReady')
  if (state.value === 'unavailable') return providerLabel.value ? `${t('admin.mailSettings.selectedUnavailable')} (${providerLabel.value})` : t('admin.mailSettings.selectedUnavailable')
  return t('admin.mailSettings.inAppContinues')
})
const mailBadge = computed(() => {
  if (state.value === 'ready') return { color: 'success' as const, label: t('admin.mailSettings.ready') }
  if (state.value === 'unavailable') return { color: 'warning' as const, label: t('admin.mailSettings.unavailable') }
  return { color: 'warning' as const, label: t('admin.mailSettings.needsSetup') }
})
onMounted(load)
async function load() {
  pending.value = true
  try {
    const response = await request<MailProviderResponse>('/admin/mail/providers')
    state.value = response.state
    providerLabel.value = response.selected?.label || ''
  } catch (error) { errorMessage.value = apiErrorMessage(error) || t('admin.mailSettings.loadFailed') } finally { pending.value = false }
}
defineExpose({ refresh: load, pending })
</script>
<template>
  <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
    <template #header>
      <div>
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('admin.mailSettings.gettingStarted') }}</h2>
        <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.mailSettings.description') }}</p>
      </div>
    </template>

    <div class="max-w-5xl space-y-5">
      <SFAlert v-if="errorMessage" variant="danger" :title="errorMessage" />
      <UAlert
        color="primary"
        variant="soft"
        icon="i-lucide-sparkles"
        :title="t('admin.mailSettings.recommendedTitle')"
        :description="t('admin.mailSettings.recommendedDescription')"
      />

      <section class="divide-y divide-slate-200 border-y border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
        <div class="flex flex-col gap-2 py-4 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <h3 class="text-sm font-semibold">{{ t('admin.mailSettings.inAppStatus') }}</h3>
            <p class="mt-1 text-sm text-muted">{{ t('admin.mailSettings.inAppReady') }}</p>
          </div>
          <UBadge color="success" variant="soft">{{ t('admin.mailSettings.ready') }}</UBadge>
        </div>
        <div class="flex flex-col gap-2 py-4 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <h3 class="text-sm font-semibold">{{ t('admin.mailSettings.mailStatus') }}</h3>
            <p class="mt-1 text-sm text-muted">{{ mailStatusText }}</p>
          </div>
          <UBadge :color="mailBadge.color" variant="soft">{{ mailBadge.label }}</UBadge>
        </div>
      </section>

      <ol class="divide-y divide-slate-200 dark:divide-zinc-800">
        <li
          v-for="(step, index) in [
            { title: t('admin.mailSettings.stepProvider'), help: t('admin.mailSettings.stepProviderHelp') },
            { title: t('admin.mailSettings.stepConfigure'), help: t('admin.mailSettings.stepConfigureHelp') },
            { title: t('admin.mailSettings.stepTest'), help: t('admin.mailSettings.stepTestHelp') }
          ]"
          :key="step.title"
          class="flex gap-3 py-4"
        >
          <span class="grid size-7 shrink-0 place-items-center rounded-full bg-[var(--sf-accent)] text-xs font-bold text-white">{{ index + 1 }}</span>
          <span><strong class="block text-sm">{{ step.title }}</strong><span class="mt-1 block text-xs leading-5 text-muted">{{ step.help }}</span></span>
        </li>
      </ol>
    </div>
  </UCard>
</template>
