<script setup lang="ts">
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'

/** 客户端版本策略：只声明运营意图，版本比较由 App / 桌面端自己完成。 */
const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const toast = useToast()
const section = useSettingsSection()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))
const form = reactive({ minimumVersion: '', recommendedVersion: '', updateNotice: '' })
const initial = computed(() => ({
  minimumVersion: map.value['client.minimum_version']?.value || '',
  recommendedVersion: map.value['client.recommended_version']?.value || '',
  updateNotice: map.value['client.update_notice']?.value || ''
}))
const hasChanges = computed(() =>
  form.minimumVersion.trim() !== initial.value.minimumVersion.trim()
  || form.recommendedVersion.trim() !== initial.value.recommendedVersion.trim()
  || form.updateNotice.trim() !== initial.value.updateNotice.trim()
)

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  Object.assign(form, initial.value)
}

async function save() {
  await section.runSave({
    successTitle: t('admin.settings.saved'),
    failureTitle: t('admin.settings.saveFailed'),
    save: () => saveOptions([
      { name: 'client.minimum_version', value: form.minimumVersion.trim() },
      { name: 'client.recommended_version', value: form.recommendedVersion.trim() },
      { name: 'client.update_notice', value: form.updateNotice.trim() }
    ])
  })
}

function resetChanges() {
  resetFromItems()
  toast.add({ color: 'neutral', icon: 'i-lucide-rotate-ccw', title: t('admin.settings.client.resetChanges'), duration: 5000 })
}

function restoreRecommended() {
  form.minimumVersion = ''
  form.recommendedVersion = ''
  form.updateNotice = ''
  toast.add({ color: 'neutral', icon: 'i-lucide-rotate-ccw', title: t('admin.settings.client.restoredRecommended'), duration: 5000 })
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="save">
    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div><h2 class="text-base font-bold">{{ t('admin.settings.client.title') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.settings.client.description') }}</p></div>
          <UBadge color="neutral" variant="soft" class="font-mono">client.*</UBadge>
        </div>
      </template>
      <div class="space-y-5">
        <UAlert color="info" variant="soft" icon="i-lucide-smartphone" :title="t('admin.settings.client.hintTitle')" :description="t('admin.settings.client.hint')" />
        <UFormField :label="t('admin.settings.client.minimumVersion')" :description="t('admin.settings.client.minimumVersionHint')" name="client-minimum-version">
          <UInput v-model="form.minimumVersion" class="w-full sm:max-w-xs" maxlength="32" :placeholder="t('admin.settings.client.versionPlaceholder')" />
        </UFormField>
        <UFormField :label="t('admin.settings.client.recommendedVersion')" :description="t('admin.settings.client.recommendedVersionHint')" name="client-recommended-version">
          <UInput v-model="form.recommendedVersion" class="w-full sm:max-w-xs" maxlength="32" :placeholder="t('admin.settings.client.versionPlaceholder')" />
        </UFormField>
        <UFormField :label="t('admin.settings.client.updateNotice')" :description="t('admin.settings.client.updateNoticeHint')" name="client-update-notice">
          <UTextarea v-model="form.updateNotice" :rows="3" class="w-full" :placeholder="t('admin.settings.client.updateNoticePlaceholder')" maxlength="500" />
        </UFormField>
        <p class="text-xs text-muted">{{ t('admin.settings.client.formatHint') }}</p>
      </div>
      <template #footer>
        <SFAdminFormFooter :saving="section.saving.value" :show-unsaved-alert="hasChanges" :submit-text="t('admin.settings.save')" @reset="resetChanges">
          <template #actions>
            <UButton type="button" color="neutral" variant="ghost" leading-icon="i-lucide-rotate-ccw" :disabled="section.saving.value" @click="restoreRecommended">
              {{ t('admin.settings.client.restoreRecommended') }}
            </UButton>
            <UButton type="button" color="neutral" variant="outline" class="border-slate-200 dark:border-zinc-700 font-medium" :disabled="section.saving.value" @click="resetChanges">
              {{ t('admin.form.reset') }}
            </UButton>
            <UButton type="submit" leading-icon="i-lucide-save" :loading="section.saving.value" class="bg-[var(--sf-accent)] hover:bg-[var(--sf-accent-hover)] text-[var(--sf-accent-contrast)] font-semibold">
              {{ t('admin.settings.save') }}
            </UButton>
          </template>
        </SFAdminFormFooter>
      </template>
    </UCard>
  </form>
</template>
