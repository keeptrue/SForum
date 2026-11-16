<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
/**
 * F4.5：站点产品开关。与 RBAC 正交——只控制产品面是否开启，不授予权限。
 */
import { apiErrorMessage } from '~/composables/useApiClient'
import { announcePolite } from '~/utils/a11y/liveRegion'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminFeatureFlags'
})

type FeatureOption = {
  name: string
  value: string
  public: boolean
  secret: boolean
  secretSet: boolean
}

type FeatureCatalogItem = {
  name: string
  public: boolean
  recommendedDefault: string
  description: string
}

const { t, te } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const adminPage = useAdminPage('/settings/features')
const { can } = useAuthSession()
// 只认细粒度子键：settings.manage 是后端的展开来源，会话里始终存在，
// 把它当候选会让管理员 deny 掉的 settings.site.manage 重新放行（task-24）。
const canManage = computed(() => can('settings.site.manage'))

const pending = ref(true)
const saving = ref(false)
const restoring = ref(false)
const loadError = ref('')
const items = ref<FeatureOption[]>([])
const catalog = ref<FeatureCatalogItem[]>([])
const savedSnapshot = ref('')

const form = reactive<Record<string, boolean>>({})

const catalogByName = computed(() => {
  const map: Record<string, FeatureCatalogItem> = {}
  for (const item of catalog.value) {
    map[item.name] = item
  }
  return map
})

const hasChanges = computed(() => formSnapshot() !== savedSnapshot.value)

function featureLabel(name: string) {
  const key = `admin.features.flags.${name}`
  return te(key) ? t(key) : name
}

function featureHelp(name: string) {
  const key = `admin.features.flagsHelp.${name}`
  return te(key) ? t(key) : (catalogByName.value[name]?.description || '')
}

function isEnabled(value: string) {
  return value === 'enabled' || value === 'true' || value === '1'
}

function formSnapshot() {
  return JSON.stringify(
    Object.keys(form)
      .sort()
      .map(name => [name, form[name]])
  )
}

function captureSnapshot() {
  savedSnapshot.value = formSnapshot()
}

async function load() {
  pending.value = true
  loadError.value = ''
  try {
    const data = await request<{ items: FeatureOption[], catalog: FeatureCatalogItem[] }>('/admin/features')
    items.value = data.items || []
    catalog.value = data.catalog || []
    for (const item of items.value) {
      form[item.name] = isEnabled(item.value)
    }
    captureSnapshot()
    announcePolite(t('admin.common.settingsLoaded'))
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.features.loadFailed')
  } finally {
    pending.value = false
  }
}

async function save() {
  if (!canManage.value) return
  saving.value = true
  loadError.value = ''
  try {
    const options = Object.keys(form).map(name => ({
      name,
      value: form[name] ? 'enabled' : 'disabled'
    }))
    await request('/admin/features', {
      method: 'PUT',
      body: { options }
    })
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.features.saved'),
      duration: 5000
    })
    await load()
  } catch (error) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.features.saveFailed')
    })
  } finally {
    saving.value = false
  }
}

async function restoreRecommended() {
  if (!canManage.value) return
  restoring.value = true
  loadError.value = ''
  try {
    await request('/admin/features/restore-defaults', { method: 'POST' })
    toast.add({
      color: 'success',
      icon: 'i-lucide-rotate-ccw',
      title: t('admin.features.restored'),
      duration: 5000
    })
    await load()
  } catch (error) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.features.saveFailed')
    })
  } finally {
    restoring.value = false
  }
}

function resetForm() {
  for (const item of items.value) {
    form[item.name] = isEnabled(item.value)
  }
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.features.resetChanges'),
    duration: 5000
  })
}

useSeoMeta({
  title: t('admin.features.title')
})

onMounted(load)
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.features.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.features.description') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-toggle-left" class="size-4" />
        <span class="truncate">{{ t('admin.features.toolbar') }}</span>
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
      :title="t('admin.features.recommendedTitle')"
      :description="t('admin.features.recommendedDescription')"
    />

    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"
    >
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.features.panelTitle') }}
            </h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.features.panelHelp') }}
            </p>
          </div>
          <UBadge color="neutral" variant="soft" class="border border-slate-200 font-mono dark:border-zinc-800">
            features.*
          </UBadge>
        </div>
      </template>

      <div v-if="pending" class="py-10 text-center text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.common.loading') }}
      </div>

      <div v-else-if="!items.length" class="py-10 text-center text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.features.empty') }}
      </div>

      <div v-else class="divide-y divide-slate-100 dark:divide-zinc-800">
        <div
          v-for="item in items"
          :key="item.name"
          class="flex flex-wrap items-center justify-between gap-4 py-4 first:pt-0 last:pb-0"
        >
          <div class="min-w-0 flex-1">
            <p class="font-medium text-slate-900 dark:text-white">
              {{ featureLabel(item.name) }}
            </p>
            <p class="mt-0.5 text-sm text-slate-500 dark:text-zinc-400">
              {{ featureHelp(item.name) }}
            </p>
            <p class="mt-1 font-mono text-[11px] text-slate-400 dark:text-zinc-500">
              {{ item.name }}
              <span
                v-if="!item.public"
                class="ml-2 rounded bg-slate-100 px-1.5 py-0.5 text-[10px] dark:bg-zinc-800"
              >
                {{ t('admin.features.adminOnly') }}
              </span>
            </p>
          </div>
          <USwitch
            v-model="form[item.name]"
            :disabled="!canManage || saving || restoring || pending"
          />
        </div>
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="saving || restoring"
          :disabled="!canManage || pending"
          :show-unsaved-alert="hasChanges"
          :submit-text="t('admin.features.save')"
          :reset-text="t('admin.features.restoreRecommended')"
          reset-icon="i-lucide-rotate-ccw"
          @reset="restoreRecommended"
          @submit="save"
        >
          <template #actions>
            <UButton
              type="button"
              color="neutral"
              variant="outline"
              leading-icon="i-lucide-rotate-ccw"
              :disabled="!canManage || saving || restoring || pending || !hasChanges"
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
              :disabled="!canManage || saving || pending"
              class="border-slate-200 font-medium dark:border-zinc-700"
              @click="restoreRecommended"
            >
              {{ t('admin.features.restoreRecommended') }}
            </UButton>
            <UButton
              type="button"
              leading-icon="i-lucide-save"
              :loading="saving"
              :disabled="!canManage || restoring || pending"
              class="bg-[var(--sf-accent)] font-semibold text-[var(--sf-accent-contrast)] hover:bg-[var(--sf-accent-hover)]"
              @click="save"
            >
              {{ t('admin.features.save') }}
            </UButton>
          </template>
        </SFAdminFormFooter>
      </template>
    </UCard>
  </div>
</template>
