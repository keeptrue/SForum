<script setup lang="ts">
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import SFAdminAttachmentStorageInstances from '~/components/admin/settings/attachments/providers/SFAdminAttachmentStorageInstances.vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  createDefaultAttachmentSettings,
  isRecommendedAttachmentSettings,
  resetAttachmentSettingsToRecommended,
  splitAttachmentSettingList,
  type AttachmentSettings
} from '~/utils/attachments/attachmentSettings'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

type ProbeResult = {
  provider: string
  ok: boolean
  message: string
  reason?: string
}

const { t, locale } = useI18n()
const route = useRoute()
const toast = useToast()
const { request } = useApiClient()
const form = reactive(createDefaultAttachmentSettings())
const saving = ref(false)
const restoring = ref(false)
const testing = ref(false)

const coreProviderLabels: Record<string, string> = {
  local: t('admin.attachments.providers.local')
}

const providerChoices = computed(() => {
  const candidates = form.candidates?.filter(item => item.available !== false && !(item.kind === 'plugin' && item.multiInstance)) ?? []
  if (candidates.length > 0) {
    return candidates.map(item => ({
      label: item.kind === 'core' && coreProviderLabels[item.value]
        ? coreProviderLabels[item.value]
        : item.label || item.value,
      value: item.value,
      kind: item.kind,
      settingsPath: item.settingsPath
    }))
  }
  return Object.entries(coreProviderLabels).map(([value, label]) => ({
    label,
    value,
    kind: 'core' as const,
    settingsPath: undefined
  }))
})

const coreProviderChoices = computed(() => providerChoices.value.filter(item => item.kind === 'core'))
const instanceProviderChoices = computed(() => providerChoices.value.filter(item => item.kind === 'instance'))
const compatibilityPluginChoices = computed(() => providerChoices.value.filter(item => item.kind === 'plugin'))
const hasEnabledMultiInstanceProvider = computed(() => form.candidates?.some(item => item.kind === 'plugin' && item.available !== false && item.multiInstance) ?? false)
const initialStorageProvider = computed(() => typeof route.query.provider === 'string' ? route.query.provider : '')
const selectedProviderIsPlugin = computed(() => providerChoices.value.find(item => item.value === form.provider)?.kind === 'plugin')
const selectedProviderIsInstance = computed(() => providerChoices.value.find(item => item.value === form.provider)?.kind === 'instance')
const selectedPluginSettingsPath = computed(() => providerChoices.value.find(item => item.value === form.provider)?.settingsPath || '')
const allowedExtensionsText = computed({
  get: () => form.allowedExtensions.join(','),
  set: (value: string) => {
    form.allowedExtensions = splitAttachmentSettingList(value)
  }
})
const allowedMimeTypesText = computed({
  get: () => form.allowedMimeTypes.join(','),
  set: (value: string) => {
    form.allowedMimeTypes = splitAttachmentSettingList(value)
  }
})
const recommendedApplied = computed(() => isRecommendedAttachmentSettings(form))
const transportMaxFileSizeMB = computed(() => form.transportMaxFileSizeBytes
  ? Math.max(1, Math.floor(form.transportMaxFileSizeBytes / (1024 * 1024)))
  : 1024)
const maxFileSizeExceedsTransport = computed(() => form.maxFileSizeMb > transportMaxFileSizeMB.value)
const beginnerDefaults = computed(() => [
  { icon: 'i-lucide-hard-drive', label: t('admin.attachments.beginner.defaults.local') },
  { icon: 'i-lucide-upload-cloud', label: t('admin.attachments.beginner.defaults.uploads') },
  { icon: 'i-lucide-shield-check', label: t('admin.attachments.beginner.defaults.safeTypes') }
])
const pathPreview = computed(() => {
  const now = new Date()
  return form.pathTemplate
    .replaceAll('{yyyy}', String(now.getFullYear()))
    .replaceAll('{mm}', String(now.getMonth() + 1).padStart(2, '0'))
    .replaceAll('{dd}', String(now.getDate()).padStart(2, '0'))
    .replaceAll('{public_id}', '7f4d9e2a')
    .replaceAll('{ext}', '.png')
})

const { data: settings, pending, error, refresh } = await useAsyncData(
  'admin-attachment-settings-tab',
  () => request<AttachmentSettings>('/admin/attachment-settings')
)

watch(locale, () => refresh())

watch(settings, value => {
  if (value) applySettings(value)
}, { immediate: true })

defineExpose({ refresh, pending })

async function saveSettings() {
  saving.value = true
  try {
    await persistSettings(t('admin.attachments.settingsSaved'))
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.settingsSaveFailed') })
  } finally {
    saving.value = false
  }
}

async function restoreRecommendedSettings() {
  const previous = settingsPayload()
  Object.assign(form, resetAttachmentSettingsToRecommended(form))
  restoring.value = true
  try {
    await persistSettings(t('admin.attachments.recommendedRestored'))
  } catch (error) {
    applySettings(previous)
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.recommendedRestoreFailed') })
  } finally {
    restoring.value = false
  }
}

async function persistSettings(successTitle: string) {
  const updated = await request<AttachmentSettings>('/admin/attachment-settings', {
    method: 'PUT',
    body: settingsPayload()
  })
  applySettings(updated)
  toast.add({ color: 'success', icon: 'i-lucide-check', title: successTitle, duration: 5000 })
}

async function testConnection() {
  testing.value = true
  try {
    const result = await request<ProbeResult>('/admin/attachment-settings/test', { method: 'POST', body: {} })
    const detailParts = [result.message, result.reason].filter(Boolean)
    toast.add({
      color: result.ok ? 'success' : 'warning',
      icon: result.ok ? 'i-lucide-check' : 'i-lucide-triangle-alert',
      title: result.ok ? t('admin.attachments.testPassed') : t('admin.attachments.testFailed'),
      description: detailParts.join(result.message && result.reason ? ' · ' : ''),
      duration: 5000
    })
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.testFailed') })
  } finally {
    testing.value = false
  }
}

async function handleStorageInstanceChanged(provider?: string) {
  if (provider) {
    form.provider = provider
  }
  await refresh()
}

function applySettings(value: AttachmentSettings) {
  Object.assign(form, createDefaultAttachmentSettings(), value)
}

function settingsPayload(): AttachmentSettings {
  return {
    ...form,
    allowedExtensions: [...form.allowedExtensions],
    allowedMimeTypes: [...form.allowedMimeTypes],
    local: { ...form.local }
  }
}

function providerLabel(provider: string) {
  return providerChoices.value.find(item => item.value === provider)?.label || provider
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="saveSettings">
    <UAlert
      v-if="error"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="t('admin.attachments.settingsLoadFailed')"
      class="mb-4"
    />

    <section class="mb-4 rounded-lg border border-emerald-200 bg-emerald-50/80 p-4 text-sm text-emerald-950 dark:border-emerald-900/60 dark:bg-emerald-950/30 dark:text-emerald-100">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div class="flex gap-3">
          <div class="grid size-10 shrink-0 place-items-center rounded-lg bg-white text-emerald-700 shadow-sm dark:bg-emerald-900/60 dark:text-emerald-200">
            <UIcon name="i-lucide-sparkles" class="size-5" />
          </div>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-base font-bold text-emerald-950 dark:text-emerald-50">
                {{ t('admin.attachments.beginner.title') }}
              </h3>
              <UBadge v-if="recommendedApplied" color="success" variant="soft">
                {{ t('admin.attachments.beginner.currentRecommended') }}
              </UBadge>
            </div>
            <p class="mt-1 max-w-3xl text-sm text-emerald-800 dark:text-emerald-200">
              {{ t('admin.attachments.beginner.description') }}
            </p>
          </div>
        </div>
        <UButton
          type="button"
          color="primary"
          leading-icon="i-lucide-rotate-ccw"
          :loading="restoring"
          :disabled="saving || pending"
          class="shrink-0"
          @click="restoreRecommendedSettings"
        >
          {{ t('admin.attachments.restoreRecommended') }}
        </UButton>
      </div>

      <div class="mt-4 grid gap-2 md:grid-cols-3">
        <div
          v-for="item in beginnerDefaults"
          :key="item.label"
          class="flex items-center gap-2 rounded-md border border-emerald-200 bg-white/80 px-3 py-2 dark:border-emerald-900/60 dark:bg-emerald-950/40"
        >
          <UIcon :name="item.icon" class="size-4 shrink-0 text-emerald-700 dark:text-emerald-200" />
          <span class="text-xs font-medium text-emerald-900 dark:text-emerald-100">{{ item.label }}</span>
        </div>
      </div>

      <p class="mt-3 text-xs text-emerald-700 dark:text-emerald-200">
        {{ t('admin.attachments.beginner.secretNote') }}
      </p>
    </section>

    <SFAdminAttachmentStorageInstances
      :candidates="form.candidates || []"
      :current-provider="form.provider"
      :initial-provider="initialStorageProvider"
      @changed="handleStorageInstanceChanged"
    />

    <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-bold text-slate-900 dark:text-zinc-100">
              {{ t('admin.attachments.settingsTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.attachments.settingsDescription') }}
            </p>
          </div>
          <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-plug-zap" :loading="testing" @click="testConnection">
            {{ t('admin.attachments.testCurrentStorage') }}
          </UButton>
        </div>
      </template>

      <div class="grid gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div class="grid gap-4">
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.attachments.currentWriter')" :help="t('admin.attachments.fieldHelp.provider')" name="attachment-provider">
              <SFNativeSelect v-model="form.provider" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
                <optgroup v-if="coreProviderChoices.length" :label="t('admin.attachments.providerGroups.core')">
                  <option v-for="choice in coreProviderChoices" :key="choice.value" :value="choice.value">
                    {{ choice.label }}
                  </option>
                </optgroup>
                <optgroup v-if="instanceProviderChoices.length" :label="t('admin.attachments.providerGroups.instances')">
                  <option v-for="choice in instanceProviderChoices" :key="choice.value" :value="choice.value">
                    {{ choice.label }}
                  </option>
                </optgroup>
                <optgroup v-else :label="t('admin.attachments.providerGroups.instances')">
                  <option disabled value="__storage_instance_hint__">
                    {{ hasEnabledMultiInstanceProvider ? t('admin.attachments.addStorageInstanceHint') : t('admin.attachments.enableStoragePluginHint') }}
                  </option>
                </optgroup>
                <optgroup v-if="compatibilityPluginChoices.length" :label="t('admin.attachments.providerGroups.compatibility')">
                  <option v-for="choice in compatibilityPluginChoices" :key="choice.value" :value="choice.value">
                    {{ choice.label }}
                  </option>
                </optgroup>
              </SFNativeSelect>
              <p v-if="selectedProviderIsInstance" class="mt-1.5 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.attachments.instanceProviderHint') }}
              </p>
              <p v-if="selectedProviderIsPlugin" class="mt-1.5 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.attachments.pluginProviderHint') }}
                <NuxtLink
                  v-if="selectedPluginSettingsPath"
                  :to="`/admin${selectedPluginSettingsPath}`"
                  class="ml-1 font-medium text-[var(--sf-accent)] underline-offset-2 hover:underline dark:text-[var(--sf-accent-dark)]"
                >
                  {{ t('admin.attachments.openPluginSettings') }}
                </NuxtLink>
              </p>
            </UFormField>
            <UFormField :label="t('admin.attachments.maxFileSize')" :help="t('admin.attachments.fieldHelp.maxFileSize')" name="attachment-max-size">
              <UInput v-model.number="form.maxFileSizeMb" size="lg" type="number" min="1" :max="transportMaxFileSizeMB" icon="i-lucide-hard-drive-upload" class="w-full">
                <template #trailing>
                  <span class="pointer-events-none text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.units.megabytes') }}</span>
                </template>
              </UInput>
              <p v-if="maxFileSizeExceedsTransport" class="mt-2 text-xs text-error" role="alert">
                {{ t('admin.attachments.transportLimitExceeded', { limit: transportMaxFileSizeMB }) }}
              </p>
            </UFormField>
          </div>

          <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
            <input v-model="form.uploadEnabled" type="checkbox" class="mt-1 size-4 rounded border-slate-300 text-[var(--sf-accent)] focus:ring-[var(--sf-accent)]" />
            <span>
              <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.uploadEnabled') }}</span>
              <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.uploadEnabledDescription') }}</span>
            </span>
          </label>

          <UFormField
            :label="t('admin.attachments.pathTemplate')"
            :help="t('admin.attachments.fieldHelp.pathTemplate', { yyyy: '{yyyy}', mm: '{mm}', dd: '{dd}', public_id: '{public_id}', ext: '{ext}' })"
            name="attachment-path-template"
          >
            <UInput v-model="form.pathTemplate" size="lg" icon="i-lucide-route" class="w-full font-mono" />
            <p class="mt-2 break-all text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.attachments.pathPreview') }} {{ pathPreview }}
            </p>
          </UFormField>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.attachments.defaultVisibility')" :help="t('admin.attachments.fieldHelp.defaultVisibility')" name="attachment-visibility">
              <SFNativeSelect v-model="form.defaultVisibility" class="h-10 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100">
                <option value="public">{{ t('admin.attachments.visibility.public') }}</option>
                <option value="private">{{ t('admin.attachments.visibility.private') }}</option>
              </SFNativeSelect>
            </UFormField>
          </div>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.attachments.allowedExtensions')" :help="t('admin.attachments.fieldHelp.allowedExtensions')" name="attachment-extensions">
              <UTextarea v-model="allowedExtensionsText" size="lg" :rows="4" class="w-full font-mono text-xs" />
            </UFormField>
            <UFormField :label="t('admin.attachments.allowedMimeTypes')" :help="t('admin.attachments.fieldHelp.allowedMimeTypes')" name="attachment-mime-types">
              <UTextarea v-model="allowedMimeTypesText" size="lg" :rows="4" class="w-full font-mono text-xs" />
            </UFormField>
          </div>

          <UFormField :label="t('admin.attachments.cleanupDays')" :help="t('admin.attachments.fieldHelp.cleanupDays')" name="attachment-cleanup-days">
            <UInput v-model.number="form.cleanupOrphanAfterDays" size="lg" type="number" min="1" max="3650" icon="i-lucide-calendar-clock" class="w-full">
              <template #trailing>
                <span class="pointer-events-none text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.units.days') }}</span>
              </template>
            </UInput>
          </UFormField>

          <div v-if="form.provider === 'local'" class="grid gap-4">
            <UFormField :label="t('admin.attachments.localRoot')" :help="t('admin.attachments.localRootDescription')" name="attachment-local-root">
              <UInput v-model="form.local.root" size="lg" icon="i-lucide-folder-tree" class="w-full font-mono" />
            </UFormField>
            <details class="border-t border-slate-200 pt-3 dark:border-zinc-800">
              <summary class="flex cursor-pointer list-none items-center gap-2 text-sm font-semibold text-slate-700 dark:text-zinc-200">
                <UIcon name="i-lucide-network" class="size-4" />
                {{ t('admin.attachments.localPublicAccessAdvanced') }}
                <UIcon name="i-lucide-chevron-down" class="ml-auto size-4" />
              </summary>
              <UFormField
                :label="t('admin.attachments.localPublicPrefix')"
                :help="t('admin.attachments.fieldHelp.localPublicPrefix')"
                name="attachment-local-prefix"
                class="mt-4"
              >
                <UInput
                  v-model="form.local.publicPrefix"
                  size="lg"
                  type="url"
                  icon="i-lucide-globe-2"
                  placeholder="https://cdn.example.com/uploads"
                  class="w-full"
                />
              </UFormField>
            </details>
          </div>

          <!-- 插件提供方的凭证只在通用扩展设置页管理。 -->
          <div
            v-else-if="selectedProviderIsPlugin"
            class="rounded-lg border border-dashed border-slate-300 bg-slate-50 p-4 dark:border-zinc-700 dark:bg-zinc-950/50"
          >
            <div class="flex items-start gap-3">
              <UIcon name="i-lucide-puzzle" class="mt-0.5 size-5 shrink-0 text-[var(--sf-accent)]" />
              <div class="min-w-0 space-y-2">
                <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.attachments.pluginProviderTitle') }}
                </p>
                <p class="text-xs leading-relaxed text-slate-600 dark:text-zinc-400">
                  {{ t('admin.attachments.pluginProviderHint') }}
                </p>
                <p class="text-xs text-slate-500 dark:text-zinc-500">
                  {{ t('admin.attachments.pluginProviderSecretsNote') }}
                </p>
                <div class="flex flex-wrap gap-2 pt-1">
                  <UButton
                    v-if="selectedPluginSettingsPath"
                    :to="`/admin${selectedPluginSettingsPath}`"
                    size="sm"
                    color="primary"
                    variant="soft"
                    leading-icon="i-lucide-settings-2"
                  >
                    {{ t('admin.attachments.openPluginSettings') }}
                  </UButton>
                  <UButton
                    type="button"
                    size="sm"
                    color="neutral"
                    variant="outline"
                    leading-icon="i-lucide-plug-zap"
                    :loading="testing"
                    @click="testConnection"
                  >
                    {{ t('admin.attachments.testCurrentStorage') }}
                  </UButton>
                </div>
              </div>
            </div>
          </div>
        </div>

        <aside class="rounded-lg border border-slate-200 bg-slate-50 p-4 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
          <h3 class="font-bold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.summary') }}</h3>
          <dl class="mt-3 space-y-3">
            <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.currentWriter') }}</dt><dd class="font-medium">{{ providerLabel(form.provider) }}</dd></div>
            <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.maxFileSize') }}</dt><dd class="font-medium">{{ form.maxFileSizeMb }} MB</dd></div>
            <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.defaultVisibility') }}</dt><dd class="font-medium">{{ t(`admin.attachments.visibility.${form.defaultVisibility}`) }}</dd></div>
            <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.allowedExtensions') }}</dt><dd class="break-words font-mono text-xs">{{ form.allowedExtensions.join(', ') }}</dd></div>
          </dl>
        </aside>
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="saving || restoring"
          :disabled="pending"
          :submit-text="t('admin.attachments.saveSettings')"
          :reset-text="t('admin.attachments.restoreRecommended')"
          reset-icon="i-lucide-rotate-ccw"
          @reset="restoreRecommendedSettings"
        >
          <template #left>
            <div class="flex items-center gap-2">
              <UIcon
                :name="recommendedApplied ? 'i-lucide-circle-check' : 'i-lucide-info'"
                :class="recommendedApplied ? 'size-4 text-emerald-500' : 'size-4 text-slate-400 dark:text-zinc-500'"
              />
              <span>
                {{ recommendedApplied ? t('admin.attachments.beginner.currentRecommended') : t('admin.attachments.beginner.restoreHint') }}
              </span>
            </div>
          </template>
        </SFAdminFormFooter>
      </template>
    </UCard>
  </form>
</template>
