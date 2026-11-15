<script setup lang="ts">
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import { useAdminSystemUpdates } from '~/composables/admin/useAdminSystemUpdates'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'
import { formatOverviewVersion } from '~/utils/admin/adminOverview'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t, locale } = useI18n()
const toast = useToast()
const { can } = useAuthSession()
const section = useSettingsSection()
const updates = useAdminSystemUpdates()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))
const form = reactive({ mirrorUrl: '' })
const initialMirrorUrl = computed(() => map.value['system.updates.github_mirror_url']?.value || '')
const hasChanges = computed(() => form.mirrorUrl.trim() !== initialMirrorUrl.value.trim())
const canManage = computed(() => can('settings.site.manage'))
const status = computed(() => updates.status.value)

const statusColor = computed(() => {
  if (status.value?.state === 'update_available') return 'warning'
  if (status.value?.state === 'current') return 'success'
  if (status.value?.state === 'unavailable' || updates.requestFailed.value) return 'error'
  return 'neutral'
})
const statusIcon = computed(() => {
  if (status.value?.state === 'update_available') return 'i-lucide-arrow-up-circle'
  if (status.value?.state === 'current') return 'i-lucide-circle-check'
  if (status.value?.state === 'unavailable' || updates.requestFailed.value) return 'i-lucide-triangle-alert'
  return 'i-lucide-flask-conical'
})
const statusTitle = computed(() => {
  if (updates.requestFailed.value) return t('admin.settings.updates.states.requestFailed')
  return t(`admin.settings.updates.states.${status.value?.state || 'unavailable'}`)
})
const currentVersion = computed(() => formatOverviewVersion('', status.value?.currentVersion || 'dev', status.value?.currentCommit || ''))
const latestVersion = computed(() => status.value?.latestVersion ? formatOverviewVersion('', status.value.latestVersion) : t('admin.settings.updates.notAvailable'))
const checkedAt = computed(() => formatDateTime(status.value?.checkedAt))
const publishedAt = computed(() => formatDateTime(status.value?.publishedAt))
const sourceLabel = computed(() => t(`admin.settings.updates.sources.${status.value?.source || 'official'}`))

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  form.mirrorUrl = initialMirrorUrl.value
}

function restoreRecommended() {
  section.runRestore({
    apply: () => { form.mirrorUrl = '' },
    title: t('admin.settings.updates.restored')
  })
}

async function save() {
  if (!canManage.value) return
  const saved = await section.runSave({
    successTitle: t('admin.settings.updates.saved'),
    failureTitle: t('admin.settings.updates.saveFailed'),
    save: () => saveOptions([{ name: 'system.updates.github_mirror_url', value: form.mirrorUrl.trim() }])
  })
  if (saved) {
    await checkNow(false)
  }
}

async function checkNow(showSuccessToast = true) {
  if (!canManage.value) return
  try {
    const result = await updates.refresh({ force: true })
    if (result.state === 'unavailable') {
      toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: t('admin.settings.updates.checkFailed') })
      return
    }
    if (showSuccessToast) {
      toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.settings.updates.checked'), duration: 5000 })
    }
  } catch {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: t('admin.settings.updates.checkFailed') })
  }
}

function formatDateTime(value?: string) {
  if (!value) return t('admin.settings.updates.notAvailable')
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return t('admin.settings.updates.notAvailable')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="save">
    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      :ui="{ footer: 'sticky bottom-0 z-20 border-t border-slate-200 bg-white/95 p-4 backdrop-blur-sm sm:px-6 dark:border-zinc-800 dark:bg-zinc-900/95' }"
    >
      <template #header>
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="text-base font-bold">{{ t('admin.settings.updates.title') }}</h2>
            <p class="mt-1 text-xs text-muted">{{ t('admin.settings.updates.description') }}</p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-refresh-cw"
            :loading="updates.pending.value"
            :disabled="!canManage"
            @click="checkNow()"
          >
            {{ t('admin.settings.updates.checkNow') }}
          </UButton>
        </div>
      </template>

      <div class="space-y-6">
        <UAlert
          :color="statusColor"
          variant="soft"
          :icon="statusIcon"
          :title="statusTitle"
          :description="status?.errorCode ? t(`admin.settings.updates.errors.${status.errorCode}`) : undefined"
        />

        <dl class="grid gap-x-8 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
          <div>
            <dt class="text-xs font-medium text-muted">{{ t('admin.settings.updates.currentVersion') }}</dt>
            <dd class="mt-1 font-mono text-sm font-semibold">{{ currentVersion }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-muted">{{ t('admin.settings.updates.latestVersion') }}</dt>
            <dd class="mt-1 font-mono text-sm font-semibold">{{ latestVersion }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-muted">{{ t('admin.settings.updates.source') }}</dt>
            <dd class="mt-1 text-sm font-semibold">{{ sourceLabel }}</dd>
          </div>
          <div>
            <dt class="text-xs font-medium text-muted">{{ t('admin.settings.updates.checkedAt') }}</dt>
            <dd class="mt-1 text-sm">{{ checkedAt }}</dd>
          </div>
          <div v-if="status?.publishedAt">
            <dt class="text-xs font-medium text-muted">{{ t('admin.settings.updates.publishedAt') }}</dt>
            <dd class="mt-1 text-sm">{{ publishedAt }}</dd>
          </div>
          <div v-if="status?.releaseUrl" class="flex items-end">
            <UButton
              :to="status.releaseUrl"
              target="_blank"
              color="neutral"
              variant="link"
              trailing-icon="i-lucide-external-link"
              class="px-0"
            >
              {{ t('admin.settings.updates.viewRelease') }}
            </UButton>
          </div>
        </dl>

        <USeparator />

        <UFormField
          :label="t('admin.settings.updates.mirrorUrl')"
          :description="t('admin.settings.updates.mirrorUrlHint')"
          name="github-mirror-url"
        >
          <UInput
            v-model="form.mirrorUrl"
            type="url"
            class="w-full"
            :disabled="!canManage"
            :placeholder="t('admin.settings.updates.mirrorUrlPlaceholder')"
            autocomplete="url"
            maxlength="2048"
          />
        </UFormField>
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="section.saving.value"
          :disabled="!canManage"
          :show-unsaved-alert="hasChanges"
          :submit-text="t('admin.settings.save')"
          :reset-text="t('admin.settings.updates.restoreDefault')"
          @reset="restoreRecommended"
        />
      </template>
    </UCard>
  </form>
</template>
