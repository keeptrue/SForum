<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
/**
 * F4.4：实体自定义字段定义管理（user / topic）。
 * 值读写走公开/实体 API；本页只管理字段目录。
 */
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminEntityMeta'
})

type FieldDefinition = {
  id: number
  fieldKey: string
  entityType: 'user' | 'topic'
  valueType: 'string' | 'text' | 'number' | 'boolean'
  visibility: 'public' | 'owner' | 'admin'
  label: Record<string, string>
  description?: Record<string, string>
  ownerExtensionId?: string
  required: boolean
  enabled: boolean
  sortOrder: number
}

const { t, te, locale } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const adminPage = useAdminPage('/entity-meta')
const { can } = useAuthSession()
const canManage = computed(() => can('entity_meta.manage') || can('settings.manage'))

const pending = ref(true)
const saving = ref(false)
const loadError = ref('')
const items = ref<FieldDefinition[]>([])
const filterEntity = ref<'all' | 'user' | 'topic'>('all')

const form = reactive({
  fieldKey: '',
  entityType: 'user' as 'user' | 'topic',
  valueType: 'string' as FieldDefinition['valueType'],
  visibility: 'public' as FieldDefinition['visibility'],
  labelZh: '',
  labelEn: '',
  required: false,
  enabled: true,
  sortOrder: 100
})

const entityOptions = computed(() => [
  { value: 'user', label: t('admin.entityMeta.entities.user') },
  { value: 'topic', label: t('admin.entityMeta.entities.topic') }
])
const valueTypeOptions = computed(() => [
  { value: 'string', label: t('admin.entityMeta.valueTypes.string') },
  { value: 'text', label: t('admin.entityMeta.valueTypes.text') },
  { value: 'number', label: t('admin.entityMeta.valueTypes.number') },
  { value: 'boolean', label: t('admin.entityMeta.valueTypes.boolean') }
])
const visibilityOptions = computed(() => [
  { value: 'public', label: t('admin.entityMeta.visibilities.public') },
  { value: 'owner', label: t('admin.entityMeta.visibilities.owner') },
  { value: 'admin', label: t('admin.entityMeta.visibilities.admin') }
])

const filterTabs = computed(() => [
  { id: 'all' as const, label: t('admin.entityMeta.filters.all'), icon: 'i-lucide-list' },
  { id: 'user' as const, label: t('admin.entityMeta.entities.user'), icon: 'i-lucide-user' },
  { id: 'topic' as const, label: t('admin.entityMeta.entities.topic'), icon: 'i-lucide-file-text' }
])

const filteredItems = computed(() => {
  if (filterEntity.value === 'all') return items.value
  return items.value.filter(item => item.entityType === filterEntity.value)
})

function labelOf(item: FieldDefinition) {
  const labels = item.label || {}
  return labels[String(locale.value)] || labels['zh-CN'] || labels['en-US'] || item.fieldKey
}

function entityLabel(value: string) {
  if (value === 'user' || value === 'topic') {
    return t(`admin.entityMeta.entities.${value}`)
  }
  return value
}

function valueTypeLabel(value: string) {
  const key = `admin.entityMeta.valueTypes.${value}`
  return te(key) ? t(key) : value
}

function visibilityLabel(value: string) {
  const key = `admin.entityMeta.visibilities.${value}`
  return te(key) ? t(key) : value
}

async function load() {
  pending.value = true
  loadError.value = ''
  try {
    const qs = filterEntity.value === 'all' ? '' : `?entityType=${filterEntity.value}`
    const data = await request<{ items: FieldDefinition[] }>(`/admin/entity-meta/definitions${qs}`)
    items.value = data.items || []
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.entityMeta.loadFailed')
  } finally {
    pending.value = false
  }
}

async function createField() {
  if (!canManage.value) return
  saving.value = true
  loadError.value = ''
  try {
    await request('/admin/entity-meta/definitions', {
      method: 'POST',
      body: {
        fieldKey: form.fieldKey.trim(),
        entityType: form.entityType,
        valueType: form.valueType,
        visibility: form.visibility,
        label: {
          'zh-CN': form.labelZh.trim() || form.labelEn.trim(),
          'en-US': form.labelEn.trim() || form.labelZh.trim()
        },
        required: form.required,
        enabled: form.enabled,
        sortOrder: form.sortOrder
      }
    })
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.entityMeta.created'),
      duration: 5000
    })
    form.fieldKey = ''
    form.labelZh = ''
    form.labelEn = ''
    await load()
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.entityMeta.saveFailed')
    })
  } finally {
    saving.value = false
  }
}

function resetCreateForm() {
  form.fieldKey = ''
  form.entityType = 'user'
  form.valueType = 'string'
  form.visibility = 'public'
  form.labelZh = ''
  form.labelEn = ''
  form.required = false
  form.enabled = true
  form.sortOrder = 100
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.entityMeta.resetForm'),
    duration: 5000
  })
}

async function toggleEnabled(item: FieldDefinition) {
  if (!canManage.value || item.ownerExtensionId) return
  try {
    await request(`/admin/entity-meta/definitions/${encodeURIComponent(item.fieldKey)}`, {
      method: 'PATCH',
      body: { enabled: !item.enabled }
    })
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.entityMeta.updated'),
      duration: 5000
    })
    await load()
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.entityMeta.saveFailed')
    })
  }
}

async function removeField(item: FieldDefinition) {
  if (!canManage.value || item.ownerExtensionId) return
  if (!confirm(t('admin.entityMeta.deleteConfirm', { key: item.fieldKey }))) return
  try {
    await request(`/admin/entity-meta/definitions/${encodeURIComponent(item.fieldKey)}`, {
      method: 'DELETE'
    })
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.entityMeta.deleted'),
      duration: 5000
    })
    await load()
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('admin.entityMeta.saveFailed')
    })
  }
}

function setFilter(id: 'all' | 'user' | 'topic') {
  filterEntity.value = id
}

watch(filterEntity, () => {
  void load()
})

useSeoMeta({
  title: t('admin.entityMeta.title')
})

onMounted(load)
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.entityMeta.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.entityMeta.description') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-tags" class="size-4" />
        <span class="truncate">{{ t('admin.entityMeta.toolbar') }}</span>
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
      :title="t('admin.entityMeta.recommendedTitle')"
      :description="t('admin.entityMeta.recommendedDescription')"
    />

    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"
    >
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.entityMeta.createTitle') }}
            </h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.entityMeta.createHelp') }}
            </p>
          </div>
          <UBadge color="neutral" variant="soft" class="border border-slate-200 font-mono dark:border-zinc-800">
            entity-meta
          </UBadge>
        </div>
      </template>

      <div class="grid max-w-5xl gap-4 md:grid-cols-2">
        <UFormField :label="t('admin.entityMeta.fieldKey')" name="entity-meta-field-key">
          <UInput
            v-model="form.fieldKey"
            size="lg"
            icon="i-lucide-key-round"
            placeholder="demo.extra_field"
            class="w-full"
            :disabled="!canManage || saving"
          />
        </UFormField>
        <UFormField :label="t('admin.entityMeta.entityType')" name="entity-meta-entity-type">
          <select
            v-model="form.entityType"
            class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
            :disabled="!canManage || saving"
          >
            <option v-for="option in entityOptions" :key="option.value" :value="option.value">
              {{ option.label }}
            </option>
          </select>
        </UFormField>
        <UFormField :label="t('admin.entityMeta.valueType')" name="entity-meta-value-type">
          <select
            v-model="form.valueType"
            class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
            :disabled="!canManage || saving"
          >
            <option v-for="option in valueTypeOptions" :key="option.value" :value="option.value">
              {{ option.label }}
            </option>
          </select>
        </UFormField>
        <UFormField :label="t('admin.entityMeta.visibility')" name="entity-meta-visibility">
          <select
            v-model="form.visibility"
            class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base text-slate-900 outline-none transition focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-100"
            :disabled="!canManage || saving"
          >
            <option v-for="option in visibilityOptions" :key="option.value" :value="option.value">
              {{ option.label }}
            </option>
          </select>
        </UFormField>
        <UFormField :label="t('admin.entityMeta.labelZh')" name="entity-meta-label-zh">
          <UInput v-model="form.labelZh" size="lg" class="w-full" :disabled="!canManage || saving" />
        </UFormField>
        <UFormField :label="t('admin.entityMeta.labelEn')" name="entity-meta-label-en">
          <UInput v-model="form.labelEn" size="lg" class="w-full" :disabled="!canManage || saving" />
        </UFormField>
      </div>

      <div class="mt-5 grid gap-3 md:grid-cols-2">
        <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
          <UCheckbox v-model="form.required" class="mt-0.5" :disabled="!canManage || saving" />
          <span>
            <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">
              {{ t('admin.entityMeta.required') }}
            </span>
            <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.entityMeta.requiredHelp') }}
            </span>
          </span>
        </label>
        <label class="flex items-start gap-3 rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
          <UCheckbox v-model="form.enabled" class="mt-0.5" :disabled="!canManage || saving" />
          <span>
            <span class="block text-sm font-semibold text-slate-900 dark:text-zinc-100">
              {{ t('admin.entityMeta.enabled') }}
            </span>
            <span class="mt-1 block text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.entityMeta.enabledHelp') }}
            </span>
          </span>
        </label>
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="saving"
          :disabled="!canManage || pending"
          :submit-text="t('admin.entityMeta.create')"
          :reset-text="t('admin.form.reset')"
          submit-icon="i-lucide-plus"
          @reset="resetCreateForm"
          @submit="createField"
        />
      </template>
    </UCard>

    <div
      role="tablist"
      :aria-label="t('admin.entityMeta.filters.label')"
      class="relative z-0 flex flex-wrap gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
    >
      <UButton
        v-for="tab in filterTabs"
        :key="tab.id"
        size="md"
        class="min-h-10 px-4"
        :color="filterEntity === tab.id ? 'primary' : 'neutral'"
        :variant="filterEntity === tab.id ? 'solid' : 'ghost'"
        :leading-icon="tab.icon"
        role="tab"
        :aria-selected="filterEntity === tab.id"
        @click="setFilter(tab.id)"
      >
        {{ tab.label }}
      </UButton>
    </div>

    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
      <template #header>
        <div>
          <h2 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.entityMeta.listTitle') }}
          </h2>
          <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.entityMeta.listHelp') }}
          </p>
        </div>
      </template>

      <div v-if="pending" class="py-10 text-center text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.common.loading') }}
      </div>
      <div v-else-if="!filteredItems.length" class="py-10 text-center text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.entityMeta.empty') }}
      </div>
      <div v-else class="divide-y divide-slate-100 dark:divide-zinc-800">
        <div
          v-for="item in filteredItems"
          :key="item.fieldKey"
          class="flex flex-wrap items-center justify-between gap-3 py-4 first:pt-0 last:pb-0"
        >
          <div class="min-w-0">
            <p class="font-medium text-slate-900 dark:text-white">
              {{ labelOf(item) }}
              <span class="ml-2 font-mono text-xs text-slate-400">{{ item.fieldKey }}</span>
            </p>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ entityLabel(item.entityType) }}
              · {{ valueTypeLabel(item.valueType) }}
              · {{ visibilityLabel(item.visibility) }}
              <span v-if="!item.enabled" class="ml-2 text-amber-600 dark:text-amber-400">
                {{ t('admin.entityMeta.disabled') }}
              </span>
              <UBadge
                v-if="item.ownerExtensionId"
                color="neutral"
                variant="subtle"
                class="ml-2 align-middle"
              >
                {{ t('admin.entityMeta.managedByExtension') }}
              </UBadge>
            </p>
            <p v-if="item.ownerExtensionId" class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.entityMeta.managedByExtensionHint', { extensionId: item.ownerExtensionId }) }}
            </p>
          </div>
          <div class="flex gap-2">
            <UButton
              size="sm"
              color="neutral"
              variant="outline"
              class="border-slate-200 dark:border-zinc-700"
              :disabled="!canManage || Boolean(item.ownerExtensionId)"
              :title="item.ownerExtensionId ? t('admin.entityMeta.managedByExtensionHint', { extensionId: item.ownerExtensionId }) : undefined"
              @click="toggleEnabled(item)"
            >
              {{ item.enabled ? t('admin.entityMeta.disable') : t('admin.entityMeta.enable') }}
            </UButton>
            <UButton
              size="sm"
              color="error"
              variant="soft"
              :disabled="!canManage || Boolean(item.ownerExtensionId)"
              :title="item.ownerExtensionId ? t('admin.entityMeta.managedByExtensionHint', { extensionId: item.ownerExtensionId }) : undefined"
              @click="removeField(item)"
            >
              {{ t('admin.entityMeta.delete') }}
            </UButton>
          </div>
        </div>
      </div>
    </UCard>
  </div>
</template>
