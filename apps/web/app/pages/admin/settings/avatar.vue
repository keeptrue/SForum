<script setup lang="ts">
import type { AdminWebOption, AvatarDefaultProvider, AvatarGravatarHashAlgorithm } from '~/composables/useWebOptions'
import {
  enabledOptionValue,
  normalizeAvatarBaseUrl,
  normalizeAvatarDefaultProvider,
  normalizeAvatarHashAlgorithm,
  recommendedAvatarSettings,
  resolveAvatarSettings
} from '~/composables/useWebOptions'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage } from '~/composables/useApiClient'
import { announcePolite } from '~/utils/a11y/liveRegion'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminAvatarSettings'
})

const { t } = useI18n()
const toast = useToast()
const { fetchAdminEnvelope, saveMany } = useWebOptions()
const adminPage = useAdminPage('/settings/avatar')

const optionNames = [
  'avatar.allow_upload',
  'avatar.default_provider',
  'avatar.gravatar_base_url',
  'avatar.gravatar_hash_algorithm',
  'avatar.default_static_url',
  'avatar.max_size_kb',
  'avatar.max_dimension',
  'avatar.allow_gif',
  'avatar.compress_enabled',
  'avatar.target_dimension',
  'avatar.compress_quality'
] as const

const providerOptions = computed<Array<{ value: AvatarDefaultProvider, label: string, description: string }>>(() => [
  { value: 'initials', label: t('admin.avatar.providers.initials.label'), description: t('admin.avatar.providers.initials.description') },
  { value: 'gravatar', label: t('admin.avatar.providers.gravatar.label'), description: t('admin.avatar.providers.gravatar.description') },
  { value: 'static', label: t('admin.avatar.providers.static.label'), description: t('admin.avatar.providers.static.description') }
])

const hashOptions = computed<Array<{ value: AvatarGravatarHashAlgorithm, label: string }>>(() => [
  { value: 'sha256', label: 'SHA-256' },
  { value: 'md5', label: 'MD5' }
])

const form = reactive({
  allowUpload: recommendedAvatarSettings.allowUpload,
  defaultProvider: recommendedAvatarSettings.defaultProvider,
  gravatarBaseUrl: recommendedAvatarSettings.gravatarBaseUrl,
  gravatarHashAlgorithm: recommendedAvatarSettings.gravatarHashAlgorithm,
  defaultStaticUrl: recommendedAvatarSettings.defaultStaticUrl,
  maxSizeKb: recommendedAvatarSettings.maxSizeKb,
  maxDimension: recommendedAvatarSettings.maxDimension,
  allowGif: recommendedAvatarSettings.allowGif,
  compressEnabled: recommendedAvatarSettings.compressEnabled,
  targetDimension: recommendedAvatarSettings.targetDimension,
  compressQuality: recommendedAvatarSettings.compressQuality
})

const pending = ref(true)
const saving = ref(false)
const restoring = ref(false)
const loadError = ref('')
const adminOptionsMap = ref<Record<string, AdminWebOption>>({})
const savedSnapshot = ref('')

const recommendedApplied = computed(() => {
  const current = settingsPayload()
  const recommended = recommendedPayload()
  return optionNames.every(name => current[name] === recommended[name])
})

const hasChanges = computed(() => formSnapshot() !== savedSnapshot.value)

const previewURL = computed(() => {
  if (form.defaultProvider === 'gravatar') {
    return normalizeAvatarBaseUrl(form.gravatarBaseUrl) + '0000000000000000000000000000000000000000000000000000000000000000'
  }
  if (form.defaultProvider === 'static') {
    return form.defaultStaticUrl || t('admin.avatar.previewStaticMissing')
  }
  return t('admin.avatar.previewInitials')
})

await load()

useSeoMeta({
  title: t('admin.avatar.metaTitle')
})

async function load() {
  pending.value = true
  loadError.value = ''
  try {
    const envelope = await fetchAdminEnvelope()
    applyAdminOptions(envelope.data)
    announcePolite(t('admin.common.settingsLoaded'))
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.avatar.loadFailed')
  } finally {
    pending.value = false
  }
}

function applyAdminOptions(items: AdminWebOption[]) {
  adminOptionsMap.value = Object.fromEntries(items.map((item) => [item.name, item]))
  const rawValues = Object.fromEntries(
    optionNames.map(name => [name, adminOptionsMap.value[name]?.value ?? ''])
  )
  Object.assign(form, resolveAvatarSettings(rawValues))
  savedSnapshot.value = formSnapshot()
}

async function saveSettings(successTitle = t('admin.avatar.saved')) {
  saving.value = true
  try {
    const updated = await saveMany(Object.entries(settingsPayload()).map(([name, value]) => ({ name, value })))
    applyAdminOptions(updated)
    toast.add({ color: 'success', icon: 'i-lucide-check', title: successTitle, duration: 5000 })
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.avatar.saveFailed')
    })
  } finally {
    saving.value = false
  }
}

async function restoreRecommended() {
  Object.assign(form, { ...recommendedAvatarSettings })
  restoring.value = true
  try {
    await saveSettings(t('admin.avatar.restored'))
  } finally {
    restoring.value = false
  }
}

function resetForm() {
  applyAdminOptions(Object.values(adminOptionsMap.value))
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.avatar.resetChanges'),
    duration: 5000
  })
}

function settingsPayload(): Record<string, string> {
  form.defaultProvider = normalizeAvatarDefaultProvider(form.defaultProvider)
  form.gravatarBaseUrl = normalizeAvatarBaseUrl(form.gravatarBaseUrl)
  form.gravatarHashAlgorithm = normalizeAvatarHashAlgorithm(form.gravatarHashAlgorithm)
  form.maxSizeKb = boundedNumber(form.maxSizeKb, recommendedAvatarSettings.maxSizeKb, 1, 10240)
  form.maxDimension = boundedNumber(form.maxDimension, recommendedAvatarSettings.maxDimension, 32, 4096)
  form.targetDimension = boundedNumber(form.targetDimension, recommendedAvatarSettings.targetDimension, 32, 4096)
  form.compressQuality = boundedNumber(form.compressQuality, recommendedAvatarSettings.compressQuality, 1, 100)
  return {
    'avatar.allow_upload': enabledOptionValue(form.allowUpload),
    'avatar.default_provider': form.defaultProvider,
    'avatar.gravatar_base_url': form.gravatarBaseUrl,
    'avatar.gravatar_hash_algorithm': form.gravatarHashAlgorithm,
    'avatar.default_static_url': form.defaultStaticUrl.trim(),
    'avatar.max_size_kb': String(form.maxSizeKb),
    'avatar.max_dimension': String(form.maxDimension),
    'avatar.allow_gif': enabledOptionValue(form.allowGif),
    'avatar.compress_enabled': enabledOptionValue(form.compressEnabled),
    'avatar.target_dimension': String(form.targetDimension),
    'avatar.compress_quality': String(form.compressQuality)
  }
}

function recommendedPayload(): Record<string, string> {
  return {
    'avatar.allow_upload': enabledOptionValue(recommendedAvatarSettings.allowUpload),
    'avatar.default_provider': recommendedAvatarSettings.defaultProvider,
    'avatar.gravatar_base_url': recommendedAvatarSettings.gravatarBaseUrl,
    'avatar.gravatar_hash_algorithm': recommendedAvatarSettings.gravatarHashAlgorithm,
    'avatar.default_static_url': recommendedAvatarSettings.defaultStaticUrl,
    'avatar.max_size_kb': String(recommendedAvatarSettings.maxSizeKb),
    'avatar.max_dimension': String(recommendedAvatarSettings.maxDimension),
    'avatar.allow_gif': enabledOptionValue(recommendedAvatarSettings.allowGif),
    'avatar.compress_enabled': enabledOptionValue(recommendedAvatarSettings.compressEnabled),
    'avatar.target_dimension': String(recommendedAvatarSettings.targetDimension),
    'avatar.compress_quality': String(recommendedAvatarSettings.compressQuality)
  }
}

function formSnapshot() {
  return JSON.stringify(settingsPayload())
}

function boundedNumber(value: number, fallback: number, min: number, max: number) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) {
    return fallback
  }
  return Math.min(max, Math.max(min, Math.round(parsed)))
}
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.avatar.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.avatar.description') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-user-round-cog" class="size-4" />
        <span class="truncate">{{ t('admin.avatar.toolbar') }}</span>
      </div>
    </template>
    <template #right>
      <UButton
        color="neutral"
        variant="outline"
        leading-icon="i-lucide-refresh-cw"
        :loading="pending"
        class="border-slate-200 dark:border-zinc-700"
        @click="load()"
      >
        {{ t('admin.common.refresh') }}
      </UButton>
    </template>
  </UDashboardToolbar>

  <div class="flex w-full min-w-0 flex-col gap-4">
    <UAlert
      v-if="loadError"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      class="w-full shrink-0"
      :title="loadError"
    />

    <UAlert
      color="primary"
      variant="soft"
      icon="i-lucide-sparkles"
      class="w-full shrink-0"
      :title="recommendedApplied
        ? `${t('admin.avatar.recommendedTitle')} · ${t('admin.avatar.currentRecommended')}`
        : t('admin.avatar.recommendedTitle')"
      :description="t('admin.avatar.recommendedDescription')"
    />

    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"
    >
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.avatar.defaultProvider') }}
            </h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.avatar.description') }}
            </p>
          </div>
          <UBadge color="neutral" variant="soft" class="border border-slate-200 font-mono dark:border-zinc-800">
            avatar.*
          </UBadge>
        </div>
      </template>

      <div class="grid gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div class="grid gap-5">
          <div class="grid gap-3">
            <label
              v-for="provider in providerOptions"
              :key="provider.value"
              class="flex cursor-pointer gap-3 rounded-lg border p-4 transition"
              :class="form.defaultProvider === provider.value
                ? 'border-[var(--sf-accent)] bg-[var(--sf-accent-soft)] dark:bg-teal-950/20'
                : 'border-slate-200 dark:border-zinc-800'"
            >
              <input v-model="form.defaultProvider" class="mt-1 size-4" type="radio" :value="provider.value">
              <span>
                <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ provider.label }}</span>
                <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ provider.description }}</span>
              </span>
            </label>
          </div>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.avatar.gravatarBaseUrl')" name="avatar-gravatar-base-url">
              <UInput size="lg" v-model="form.gravatarBaseUrl" type="url" icon="i-lucide-link" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.avatar.gravatarHashAlgorithm')" name="avatar-gravatar-hash">
              <SFNativeSelect
                v-model="form.gravatarHashAlgorithm"
                class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
              >
                <option v-for="option in hashOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </option>
              </SFNativeSelect>
            </UFormField>
          </div>

          <UFormField :label="t('admin.avatar.defaultStaticUrl')" name="avatar-static-url">
            <UInput size="lg" v-model="form.defaultStaticUrl" type="url" icon="i-lucide-image" class="w-full" />
          </UFormField>

          <div class="grid gap-3 md:grid-cols-3">
            <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
              <UCheckbox v-model="form.allowUpload" class="mt-0.5" />
              <span>
                <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.avatar.allowUpload') }}</span>
                <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.allowUploadDescription') }}</span>
              </span>
            </label>
            <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
              <UCheckbox v-model="form.allowGif" class="mt-0.5" />
              <span>
                <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.avatar.allowGif') }}</span>
                <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.allowGifDescription') }}</span>
              </span>
            </label>
            <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
              <UCheckbox v-model="form.compressEnabled" class="mt-0.5" />
              <span>
                <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.avatar.compressEnabled') }}</span>
                <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.compressDescription') }}</span>
              </span>
            </label>
          </div>

          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.avatar.maxSizeKb')" name="avatar-max-size">
              <UInput size="lg" v-model.number="form.maxSizeKb" type="number" min="1" max="10240" icon="i-lucide-hard-drive-upload" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.avatar.maxDimension')" name="avatar-max-dimension">
              <UInput size="lg" v-model.number="form.maxDimension" type="number" min="32" max="4096" icon="i-lucide-scan" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.avatar.targetDimension')" name="avatar-target-dimension">
              <UInput size="lg" v-model.number="form.targetDimension" type="number" min="32" max="4096" icon="i-lucide-crop" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.avatar.compressQuality')" name="avatar-quality">
              <UInput size="lg" v-model.number="form.compressQuality" type="number" min="1" max="100" icon="i-lucide-gauge" class="w-full" />
            </UFormField>
          </div>
        </div>

        <aside class="rounded-lg border border-slate-200 bg-slate-50 p-4 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
          <h2 class="font-bold text-slate-900 dark:text-zinc-100">{{ t('admin.avatar.summary') }}</h2>
          <dl class="mt-3 space-y-3">
            <div>
              <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.defaultProvider') }}</dt>
              <dd class="font-medium">{{ t(`admin.avatar.providers.${form.defaultProvider}.label`) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.uploadRules') }}</dt>
              <dd class="font-medium">{{ form.maxSizeKb }} KB · {{ form.maxDimension }} px</dd>
            </div>
            <div>
              <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.avatar.preview') }}</dt>
              <dd class="break-all font-mono text-xs">{{ previewURL }}</dd>
            </div>
          </dl>
        </aside>
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="saving || restoring"
          :disabled="pending"
          :show-unsaved-alert="hasChanges"
          :submit-text="t('admin.avatar.save')"
          :reset-text="t('admin.form.reset')"
          @reset="resetForm"
          @submit="saveSettings()"
        >
          <template #actions>
            <UButton
              type="button"
              color="neutral"
              variant="outline"
              leading-icon="i-lucide-rotate-ccw"
              :disabled="pending || saving || restoring || !hasChanges"
              class="border-slate-200 font-medium dark:border-zinc-700"
              @click="resetForm"
            >
              {{ t('admin.form.reset') }}
            </UButton>
            <UButton
              type="button"
              color="neutral"
              variant="outline"
              leading-icon="i-lucide-sparkles"
              :loading="restoring"
              :disabled="pending || saving"
              class="border-slate-200 font-medium dark:border-zinc-700"
              @click="restoreRecommended"
            >
              {{ t('admin.avatar.restoreRecommended') }}
            </UButton>
            <UButton
              type="button"
              leading-icon="i-lucide-save"
              :loading="saving"
              :disabled="pending || restoring"
              class="bg-[var(--sf-accent)] font-semibold text-[var(--sf-accent-contrast)] hover:bg-[var(--sf-accent-hover)]"
              @click="saveSettings()"
            >
              {{ t('admin.avatar.save') }}
            </UButton>
          </template>
        </SFAdminFormFooter>
      </template>
    </UCard>
  </div>
</template>
