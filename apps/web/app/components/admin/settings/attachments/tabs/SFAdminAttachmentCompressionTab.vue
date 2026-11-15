<script setup lang="ts">
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import {
  compressionJPEGQuality,
  estimatedOutputBytes,
  isRecommendedAttachmentCompressionSettings,
  normalizeAttachmentCompressionSettings,
  recommendedAttachmentCompressionSettings,
  type AttachmentCompressionSettings,
  type AttachmentCompressionStats
} from '~/components/admin/settings/attachments/compressionModel'
import { humanFileSize } from '~/components/admin/settings/attachments/model'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { apiErrorMessage } from '~/composables/useApiClient'

const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const { can } = useAuthSession()
const form = reactive(normalizeAttachmentCompressionSettings())
const stats = reactive<AttachmentCompressionStats>({
  pending: 0,
  running: 0,
  failed: 0,
  readyVariants: 0,
  originalBytes: 0,
  variantBytes: 0,
  savedBytes: 0
})
const previewOriginalMb = ref(5)
const saving = ref(false)
const restoring = ref(false)
const backfilling = ref(false)
const canBackfill = computed(() => can('attachment.manage'))
const recommendedApplied = computed(() => isRecommendedAttachmentCompressionSettings(form))
const previewOriginalBytes = computed(() => Math.max(0, previewOriginalMb.value || 0) * 1024 * 1024)
const previewOutputBytes = computed(() => estimatedOutputBytes(previewOriginalBytes.value, form.strength))
const previewSavedBytes = computed(() => Math.max(0, previewOriginalBytes.value - previewOutputBytes.value))
const previewSavedPercent = computed(() => previewOriginalBytes.value > 0
  ? Math.round(previewSavedBytes.value / previewOriginalBytes.value * 100)
  : 0)
const statItems = computed(() => [
  { label: t('admin.attachments.compression.stats.ready'), value: stats.readyVariants, icon: 'i-lucide-images' },
  { label: t('admin.attachments.compression.stats.pending'), value: stats.pending + stats.running, icon: 'i-lucide-loader-circle' },
  { label: t('admin.attachments.compression.stats.failed'), value: stats.failed, icon: 'i-lucide-triangle-alert' },
  { label: t('admin.attachments.compression.stats.saved'), value: humanFileSize(stats.savedBytes), icon: 'i-lucide-database-zap' }
])

const { data, pending, error, refresh } = await useAsyncData(
  'admin-attachment-compression-tab',
  async () => {
    const [settings, compressionStats] = await Promise.all([
      request<AttachmentCompressionSettings>('/admin/attachment-compression-settings'),
      request<AttachmentCompressionStats>('/admin/attachments/compression-stats')
    ])
    return { settings, stats: compressionStats }
  }
)

watch(data, value => {
  if (!value) return
  applySettings(value.settings)
  Object.assign(stats, value.stats)
}, { immediate: true })

watch(() => form.strength, strength => {
  form.strength = Math.min(100, Math.max(0, Math.round(strength || 0)))
  form.jpegQuality = compressionJPEGQuality(form.strength)
})

defineExpose({ pending, refresh })

async function saveSettings() {
  saving.value = true
  try {
    await persistSettings(settingsPayload(), t('admin.attachments.compression.saved'))
  } catch (cause) {
    showError(cause, 'admin.attachments.compression.saveFailed')
  } finally {
    saving.value = false
  }
}

async function restoreRecommended() {
  const previous = settingsPayload()
  restoring.value = true
  try {
    await persistSettings(recommendedAttachmentCompressionSettings, t('admin.attachments.compression.restored'))
  } catch (cause) {
    applySettings(previous)
    showError(cause, 'admin.attachments.compression.restoreFailed')
  } finally {
    restoring.value = false
  }
}

async function optimizeExisting() {
  backfilling.value = true
  try {
    const result = await request<{ scheduled: number }>('/admin/attachments/compression/backfill', {
      method: 'POST',
      body: { limit: 5000 }
    })
    toast.add({
      color: 'success',
      icon: 'i-lucide-list-restart',
      title: t('admin.attachments.compression.backfillScheduled', { count: result.scheduled }),
      duration: 5000
    })
    await refresh()
  } catch (cause) {
    showError(cause, 'admin.attachments.compression.backfillFailed')
  } finally {
    backfilling.value = false
  }
}

async function persistSettings(value: Partial<AttachmentCompressionSettings>, successTitle: string) {
  const updated = await request<AttachmentCompressionSettings>('/admin/attachment-compression-settings', {
    method: 'PUT',
    body: normalizeAttachmentCompressionSettings(value)
  })
  applySettings(updated)
  toast.add({ color: 'success', icon: 'i-lucide-check', title: successTitle, duration: 5000 })
  await refresh()
}

function applySettings(value: Partial<AttachmentCompressionSettings>) {
  Object.assign(form, normalizeAttachmentCompressionSettings(value))
}

function settingsPayload(): AttachmentCompressionSettings {
  return normalizeAttachmentCompressionSettings(form)
}

function showError(cause: unknown, fallbackKey: string) {
  toast.add({
    color: 'error', duration: 0,
    icon: 'i-lucide-triangle-alert',
    title: apiErrorMessage(cause) || t(fallbackKey)
  })
}
</script>

<template>
  <form class="min-w-0" @submit.prevent="saveSettings">
    <UAlert
      v-if="error"
      class="mb-4"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="t('admin.attachments.compression.loadFailed')"
    />

    <section class="mb-4 border border-emerald-200 bg-emerald-50/80 p-4 text-sm text-emerald-950 dark:border-emerald-900/60 dark:bg-emerald-950/30 dark:text-emerald-100">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div class="flex min-w-0 gap-3">
          <div class="grid size-10 shrink-0 place-items-center rounded-md bg-white text-emerald-700 shadow-sm dark:bg-emerald-900/60 dark:text-emerald-200">
            <UIcon name="i-lucide-image-down" class="size-5" />
          </div>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-base font-bold">{{ t('admin.attachments.compression.recommendedTitle') }}</h3>
              <UBadge v-if="recommendedApplied" color="success" variant="soft">
                {{ t('admin.attachments.compression.recommendedApplied') }}
              </UBadge>
            </div>
            <p class="mt-1 max-w-3xl text-emerald-800 dark:text-emerald-200">
              {{ t('admin.attachments.compression.recommendedDescription') }}
            </p>
          </div>
        </div>
        <UButton type="button" color="primary" leading-icon="i-lucide-rotate-ccw" :loading="restoring" :disabled="saving || pending" class="shrink-0" @click="restoreRecommended">
          {{ t('admin.attachments.restoreRecommended') }}
        </UButton>
      </div>
    </section>

    <UCard :ui="{ root: 'rounded-lg', body: 'p-0 sm:p-0', footer: 'p-4 sm:px-5' }">
      <div class="grid min-w-0 gap-0 lg:grid-cols-[minmax(0,1fr)_minmax(300px,0.72fr)]">
        <section class="min-w-0 space-y-6 p-5 sm:p-6">
          <div class="flex items-start justify-between gap-4 border-b border-slate-200 pb-5 dark:border-zinc-800">
            <div class="min-w-0">
              <h3 class="text-base font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.compression.automaticTitle') }}</h3>
              <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.automaticDescription') }}</p>
            </div>
            <USwitch v-model="form.enabled" :aria-label="t('admin.attachments.compression.enabled')" />
          </div>

          <UFormField :label="t('admin.attachments.compression.strength')" :description="t('admin.attachments.compression.strengthDescription')">
            <div class="mt-2 flex items-center gap-4">
              <USlider v-model="form.strength" :min="0" :max="100" :step="1" :disabled="!form.enabled" class="min-w-0 flex-1" />
              <span class="w-12 shrink-0 text-right font-mono text-sm font-semibold text-slate-800 dark:text-zinc-200">{{ form.strength }}%</span>
            </div>
            <div class="mt-2 flex items-center justify-between text-xs text-slate-500 dark:text-zinc-400">
              <span>{{ t('admin.attachments.compression.lighter') }}</span>
              <span>{{ t('admin.attachments.compression.jpegQuality', { quality: form.jpegQuality }) }}</span>
              <span>{{ t('admin.attachments.compression.smaller') }}</span>
            </div>
          </UFormField>

          <div class="grid gap-4 sm:grid-cols-3">
            <UFormField :label="t('admin.attachments.compression.maxDimension')" :description="t('admin.attachments.compression.maxDimensionDescription')">
              <UInputNumber v-model="form.maxDimension" :min="320" :max="8192" :step="1" :disabled="!form.enabled" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.attachments.compression.minSize')" :description="t('admin.attachments.compression.minSizeDescription')">
              <UInputNumber v-model="form.minSizeKb" :min="1" :max="1048576" :step="1" :disabled="!form.enabled" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.attachments.compression.minSavings')" :description="t('admin.attachments.compression.minSavingsDescription')">
              <UInputNumber v-model="form.minSavingsPercent" :min="0" :max="90" :step="1" :disabled="!form.enabled" class="w-full" />
            </UFormField>
          </div>

          <UAlert color="neutral" variant="soft" icon="i-lucide-shield-check" :title="t('admin.attachments.compression.originalsTitle')" :description="t('admin.attachments.compression.originalsDescription')" />
        </section>

        <aside class="min-w-0 border-t border-slate-200 bg-slate-50/70 p-5 dark:border-zinc-800 dark:bg-zinc-950/50 sm:p-6 lg:border-l lg:border-t-0">
          <div class="flex items-start gap-3">
            <UIcon name="i-lucide-gauge" class="mt-0.5 size-5 shrink-0 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
            <div>
              <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.compression.previewTitle') }}</h3>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.previewDescription') }}</p>
            </div>
          </div>

          <UFormField class="mt-5" :label="t('admin.attachments.compression.exampleOriginalSize')">
            <UInputNumber v-model="previewOriginalMb" :min="0.1" :max="1024" :step="0.1" class="w-full" />
          </UFormField>

          <div class="mt-5 grid grid-cols-2 gap-px overflow-hidden rounded-md border border-slate-200 bg-slate-200 dark:border-zinc-800 dark:bg-zinc-800">
            <div class="bg-white p-3 dark:bg-zinc-900">
              <div class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.before') }}</div>
              <div class="mt-1 font-mono text-lg font-semibold">{{ humanFileSize(previewOriginalBytes) }}</div>
            </div>
            <div class="bg-white p-3 dark:bg-zinc-900">
              <div class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.estimatedAfter') }}</div>
              <div class="mt-1 font-mono text-lg font-semibold text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]">{{ humanFileSize(previewOutputBytes) }}</div>
            </div>
          </div>
          <p class="mt-3 text-sm font-medium text-slate-700 dark:text-zinc-300">
            {{ t('admin.attachments.compression.estimatedSavings', { size: humanFileSize(previewSavedBytes), percent: previewSavedPercent }) }}
          </p>
          <p class="mt-2 text-xs leading-5 text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.estimateDisclaimer') }}</p>
        </aside>
      </div>

      <section class="border-t border-slate-200 px-5 py-5 dark:border-zinc-800 sm:px-6">
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="item in statItems" :key="item.label" class="flex min-w-0 items-center gap-3 border-l-2 border-slate-200 pl-3 dark:border-zinc-700">
            <UIcon :name="item.icon" class="size-4 shrink-0 text-slate-500 dark:text-zinc-400" />
            <div class="min-w-0"><div class="truncate text-xs text-slate-500 dark:text-zinc-400">{{ item.label }}</div><div class="font-mono text-sm font-semibold">{{ item.value }}</div></div>
          </div>
        </div>

        <div v-if="canBackfill" class="mt-5 flex flex-col gap-3 border-t border-slate-200 pt-5 dark:border-zinc-800 sm:flex-row sm:items-center sm:justify-between">
          <div class="min-w-0">
            <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.compression.backfillTitle') }}</h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.compression.backfillDescription') }}</p>
          </div>
          <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-list-restart" :loading="backfilling" :disabled="!form.enabled || saving || pending" class="shrink-0" @click="optimizeExisting">
            {{ t('admin.attachments.compression.backfillAction') }}
          </UButton>
        </div>
      </section>

      <template #footer>
        <SFAdminFormFooter :saving="saving || restoring" :disabled="pending" :submit-text="t('admin.attachments.compression.save')" :reset-text="t('admin.attachments.restoreRecommended')" @reset="restoreRecommended">
          <template #left>
            <div class="hidden min-w-0 items-center gap-2 sm:flex">
              <UIcon :name="recommendedApplied ? 'i-lucide-circle-check' : 'i-lucide-info'" :class="recommendedApplied ? 'size-4 shrink-0 text-emerald-500' : 'size-4 shrink-0 text-slate-400 dark:text-zinc-500'" />
              <span class="truncate">{{ recommendedApplied ? t('admin.attachments.compression.recommendedApplied') : t('admin.attachments.compression.unsavedHint') }}</span>
            </div>
          </template>
        </SFAdminFormFooter>
      </template>
    </UCard>
  </form>
</template>
