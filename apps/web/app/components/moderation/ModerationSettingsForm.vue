<script setup lang="ts">
import { useModerationApi } from '~/composables/moderation/useModerationApi'
import { apiErrorMessage } from '~/composables/useApiClient'
import type { ModerationMode, ModerationSettings } from '~/composables/moderation/useModerationApi'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'

const props = defineProps<{ modelValue: ModerationSettings }>()
const emit = defineEmits<{ updated: [settings: ModerationSettings] }>()
const { t } = useI18n()
const toast = useToast()
const moderationApi = useModerationApi()

const form = reactive<ModerationSettings>({ ...props.modelValue })
const saving = ref(false)
const resetting = ref(false)
const errorMessage = ref('')
const savedSnapshot = ref(JSON.stringify(props.modelValue))

const modeOptions: Array<{ value: ModerationMode; label: string; description: string }> = [
  { value: 'off', label: t('admin.moderation.mode.off'), description: t('admin.moderation.mode.offDescription') },
  { value: 'rules', label: t('admin.moderation.mode.rules'), description: t('admin.moderation.mode.rulesDescription') },
  { value: 'all', label: t('admin.moderation.mode.all'), description: t('admin.moderation.mode.allDescription') }
]

const hasChanges = computed(() => JSON.stringify(form) !== savedSnapshot.value)

watch(() => props.modelValue, (value) => {
  Object.assign(form, value)
  savedSnapshot.value = JSON.stringify(value)
})

async function saveSettings() {
  saving.value = true
  errorMessage.value = ''
  try {
    const updated = await moderationApi.updateSettings({ ...form })
    Object.assign(form, updated)
    savedSnapshot.value = JSON.stringify(updated)
    emit('updated', updated)
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.moderation.settingsSaved'),
      duration: 5000
    })
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.moderation.settingsSaveFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: errorMessage.value
    })
  } finally {
    saving.value = false
  }
}

async function resetSettings() {
  resetting.value = true
  errorMessage.value = ''
  try {
    const updated = await moderationApi.resetSettings()
    Object.assign(form, updated)
    savedSnapshot.value = JSON.stringify(updated)
    emit('updated', updated)
    toast.add({
      color: 'success',
      icon: 'i-lucide-rotate-ccw',
      title: t('admin.moderation.settingsReset'),
      duration: 5000
    })
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.moderation.settingsResetFailed')
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: errorMessage.value
    })
  } finally {
    resetting.value = false
  }
}

function discardChanges() {
  Object.assign(form, props.modelValue)
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.moderation.resetChanges'),
    duration: 5000
  })
}
</script>

<template>
  <UCard
    class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
    :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"
  >
    <template #header>
      <div class="flex items-center justify-between gap-3">
        <div>
          <h2 id="moderation-settings-title" class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.moderation.settingsTitle') }}
          </h2>
          <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.moderation.settingsDescription') }}
          </p>
        </div>
        <UBadge color="neutral" variant="soft" class="border border-slate-200 font-mono dark:border-zinc-800">
          moderation.*
        </UBadge>
      </div>
    </template>

    <UAlert
      v-if="errorMessage"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      class="mb-4 w-full shrink-0"
      :title="errorMessage"
    />

    <div class="grid gap-3 md:grid-cols-3" role="radiogroup" :aria-label="t('admin.moderation.modeLabel')">
      <button
        v-for="option in modeOptions"
        :key="option.value"
        type="button"
        class="min-h-24 rounded-lg border p-4 text-left transition-colors"
        :class="form.mode === option.value
          ? 'border-[var(--sf-accent)] bg-[var(--sf-accent-soft)] dark:bg-teal-950/20'
          : 'border-slate-200 bg-white hover:border-slate-300 dark:border-zinc-800 dark:bg-zinc-950 dark:hover:border-zinc-700'"
        role="radio"
        :aria-checked="form.mode === option.value"
        @click="form.mode = option.value"
      >
        <span class="flex items-center gap-2 font-semibold text-slate-900 dark:text-zinc-100">
          {{ option.label }}
          <UBadge v-if="option.value === 'rules'" color="primary" variant="soft">
            {{ t('admin.moderation.recommended') }}
          </UBadge>
        </span>
        <span class="mt-2 block text-xs leading-5 text-slate-500 dark:text-zinc-400">
          {{ option.description }}
        </span>
      </button>
    </div>

    <div v-if="form.mode === 'rules'" class="mt-5 grid gap-4 md:grid-cols-2">
      <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
        <UCheckbox v-model="form.reviewNewUsers" class="mt-0.5" />
        <span class="min-w-0">
          <span class="block text-sm font-semibold text-slate-800 dark:text-zinc-200">
            {{ t('admin.moderation.reviewNewUsers') }}
          </span>
          <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.moderation.reviewNewUsersDescription') }}
          </span>
          <UInput
            v-if="form.reviewNewUsers"
            v-model.number="form.newUserMaxAgeDays"
            size="lg"
            type="number"
            :min="0"
            :max="3650"
            class="mt-3 w-full max-w-xs"
          />
        </span>
      </label>
      <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
        <UCheckbox v-model="form.reviewExternalLinks" class="mt-0.5" />
        <span>
          <span class="block text-sm font-semibold text-slate-800 dark:text-zinc-200">
            {{ t('admin.moderation.reviewExternalLinks') }}
          </span>
          <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.moderation.reviewExternalLinksDescription') }}
          </span>
        </span>
      </label>
    </div>

    <template #footer>
      <SFAdminFormFooter
        :saving="saving || resetting"
        :show-unsaved-alert="hasChanges"
        :submit-text="t('admin.moderation.saveSettings')"
        :reset-text="t('admin.moderation.restoreDefaults')"
        reset-icon="i-lucide-rotate-ccw"
        @reset="resetSettings"
        @submit="saveSettings"
      >
        <template #left>
          <p class="max-w-2xl text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.moderation.resetScope') }}
          </p>
        </template>
        <template #actions>
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-rotate-ccw"
            :disabled="saving || resetting || !hasChanges"
            class="border-slate-200 font-medium dark:border-zinc-700"
            @click="discardChanges"
          >
            {{ t('admin.form.reset') }}
          </UButton>
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-sparkles"
            :loading="resetting"
            :disabled="saving"
            class="border-slate-200 font-medium dark:border-zinc-700"
            @click="resetSettings"
          >
            {{ t('admin.moderation.restoreDefaults') }}
          </UButton>
          <UButton
            type="button"
            leading-icon="i-lucide-save"
            :loading="saving"
            :disabled="resetting"
            class="bg-[var(--sf-accent)] font-semibold text-[var(--sf-accent-contrast)] hover:bg-[var(--sf-accent-hover)]"
            @click="saveSettings"
          >
            {{ t('admin.moderation.saveSettings') }}
          </UButton>
        </template>
      </SFAdminFormFooter>
    </template>
  </UCard>
</template>
