<script setup lang="ts">
import SFExtensionSettingsField from '~/components/extensions/settings/SFExtensionSettingsField.vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import type { AttachmentStorageCandidate } from '~/utils/attachments/attachmentSettings'
import type { AdminExtensionSettingValue } from '~/utils/admin/adminExtensions'

type ProviderField = AdminExtensionSettingValue & { secretSet?: boolean }
type ProviderSchema = { extensionId: string, label: string, fields: ProviderField[] }
type StorageInstance = {
  id: string
  extensionId: string
  name: string
  values: Record<string, string>
  schema: ProviderSchema
  configRevision: number
  status: 'unverified' | 'ready' | 'error'
  lastProbeStatus?: string
  lastProbeMessage?: string
  lastProbeAt?: string
  attachmentCount: number
  active: boolean
}
type ProbeResult = { ok: boolean, reason?: string, message: string }

const props = defineProps<{ candidates: AttachmentStorageCandidate[], currentProvider: string, initialProvider?: string }>()
const emit = defineEmits<{ changed: [provider?: string] }>()
const { t, locale } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const editorOpen = ref(false)
const deleteOpen = ref(false)
const editing = ref<StorageInstance | null>(null)
const pendingDelete = ref<StorageInstance | null>(null)
const selectedExtensionId = ref('')
const instanceName = ref('')
const formValues = reactive<Record<string, string>>({})
const saving = ref(false)
const probing = ref(false)
const activating = ref('')
const deleting = ref(false)
const initialProviderOpened = ref(false)
const draftProbeResult = ref<ProbeResult | null>(null)
const draftProbeError = ref('')

const multiInstanceProviders = computed(() => props.candidates.filter(item => item.kind === 'plugin' && item.available !== false && item.multiInstance))
const activeSchema = computed(() => editing.value?.schema
  || (multiInstanceProviders.value.find(item => item.extensionId === selectedExtensionId.value)?.schema as ProviderSchema | undefined)
  || instances.value?.find(item => item.extensionId === selectedExtensionId.value)?.schema
  || null)
const missingRequiredFields = computed(() => activeSchema.value?.fields
  .filter(field => field.required && !String(formValues[field.key] || '').trim())
  .map(field => field.label) ?? [])
const missingRequiredFieldsText = computed(() => new Intl.ListFormat(locale.value, { style: 'short', type: 'conjunction' }).format(missingRequiredFields.value))
const usesLocalStorage = computed(() => props.currentProvider === 'local')

const { data: instances, pending, error, refresh } = await useAsyncData(
  'admin-attachment-storage-instances',
  () => request<StorageInstance[]>('/admin/attachment-storage-instances'),
  { default: () => [] }
)

watch(locale, () => refresh())

function resetEditor() {
  editing.value = null
  selectedExtensionId.value = multiInstanceProviders.value[0]?.extensionId || ''
  instanceName.value = ''
  for (const key of Object.keys(formValues)) delete formValues[key]
  clearDraftProbe()
}

function clearDraftProbe() {
  draftProbeResult.value = null
  draftProbeError.value = ''
}

function openCreate(extensionId?: string) {
  resetEditor()
  selectedExtensionId.value = extensionId || selectedExtensionId.value
  const knownSchema = activeSchema.value
  for (const field of knownSchema?.fields || []) formValues[field.key] = field.default || ''
  editorOpen.value = true
}

watch([multiInstanceProviders, () => props.initialProvider], () => {
  if (initialProviderOpened.value || !props.initialProvider) return
  if (!multiInstanceProviders.value.some(item => item.extensionId === props.initialProvider)) return
  initialProviderOpened.value = true
  openCreate(props.initialProvider)
}, { immediate: true })

function openEdit(item: StorageInstance) {
  editing.value = item
  selectedExtensionId.value = item.extensionId
  instanceName.value = item.name
  for (const key of Object.keys(formValues)) delete formValues[key]
  for (const field of item.schema.fields) formValues[field.key] = item.values[field.key] || ''
  clearDraftProbe()
  editorOpen.value = true
}

function updateValue(key: string, value: string) {
  formValues[key] = value
  clearDraftProbe()
}

async function save() {
  saving.value = true
  try {
    const body = { extensionId: selectedExtensionId.value, name: instanceName.value, values: { ...formValues }, configRevision: editing.value?.configRevision }
    await request(editing.value ? `/admin/attachment-storage-instances/${editing.value.id}` : '/admin/attachment-storage-instances', {
      method: editing.value ? 'PUT' : 'POST', body
    })
    editorOpen.value = false
    await refresh()
    emit('changed')
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.attachments.storageInstances.saved'), duration: 5000 })
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(cause) || t('admin.attachments.storageInstances.saveFailed') })
  } finally {
    saving.value = false
  }
}

async function probeDraft() {
  clearDraftProbe()
  probing.value = true
  try {
    const result = await request<ProbeResult>('/admin/attachment-storage-instances/probe', {
      method: 'POST',
      body: { extensionId: selectedExtensionId.value, instanceId: editing.value?.id, values: { ...formValues } }
    })
    draftProbeResult.value = result
    toast.add({
      color: result.ok ? 'success' : 'warning', icon: result.ok ? 'i-lucide-check' : 'i-lucide-triangle-alert',
      title: result.ok ? t('admin.attachments.testPassed') : t('admin.attachments.testFailed'),
      description: result.message, duration: 5000
    })
    if (editing.value) await refresh()
  } catch (cause) {
    draftProbeError.value = apiErrorMessage(cause) || t('admin.attachments.testFailed')
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: draftProbeError.value })
  } finally {
    probing.value = false
  }
}

async function activate(item: StorageInstance) {
  activating.value = item.id
  try {
    await request(`/admin/attachment-storage-instances/${item.id}/activate`, { method: 'POST', body: {} })
    emit('changed', `instance:${item.id}`)
    await refresh()
    toast.add({ color: 'success', icon: 'i-lucide-circle-check', title: t('admin.attachments.storageInstances.activated', { name: item.name }), duration: 5000 })
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(cause) || t('admin.attachments.storageInstances.activateFailed') })
  } finally {
    activating.value = ''
  }
}

async function activateLocal() {
  activating.value = 'local'
  try {
    await request('/admin/attachment-storage-instances/local/activate', { method: 'POST', body: {} })
    emit('changed', 'local')
    await refresh()
    toast.add({ color: 'success', icon: 'i-lucide-hard-drive', title: t('admin.attachments.storageInstances.localActivated'), duration: 5000 })
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(cause) || t('admin.attachments.storageInstances.activateFailed') })
  } finally {
    activating.value = ''
  }
}

function confirmDelete(item: StorageInstance) {
  pendingDelete.value = item
  deleteOpen.value = true
}

async function removeInstance() {
  if (!pendingDelete.value) return
  deleting.value = true
  try {
    await request<{ deleted: boolean }>(`/admin/attachment-storage-instances/${pendingDelete.value.id}`, { method: 'DELETE' })
    deleteOpen.value = false
    pendingDelete.value = null
    await refresh()
    emit('changed')
    toast.add({ color: 'success', icon: 'i-lucide-trash-2', title: t('admin.attachments.storageInstances.deleted'), duration: 5000 })
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(cause) || t('admin.attachments.storageInstances.deleteFailed') })
  } finally {
    deleting.value = false
  }
}

function statusColor(status: StorageInstance['status']) {
  return status === 'ready' ? 'success' : status === 'error' ? 'error' : 'neutral'
}
</script>

<template>
  <section class="mb-4 border-y border-slate-200 py-4 dark:border-zinc-800">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-base font-bold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.storageInstances.title') }}</h3>
        <p class="mt-1 max-w-3xl text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.storageInstances.description') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <UButton v-if="!usesLocalStorage" type="button" color="neutral" variant="outline" icon="i-lucide-hard-drive" :loading="activating === 'local'" @click="activateLocal">
          {{ t('admin.attachments.storageInstances.useLocal') }}
        </UButton>
        <UButton type="button" icon="i-lucide-plus" :disabled="multiInstanceProviders.length === 0" @click="openCreate()">
          {{ t('admin.attachments.storageInstances.add') }}
        </UButton>
      </div>
    </div>

    <UAlert v-if="error" class="mt-4" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="t('admin.attachments.storageInstances.loadFailed')" />
    <UAlert v-else-if="!pending && multiInstanceProviders.length === 0" class="mt-4" color="neutral" variant="soft" icon="i-lucide-puzzle" :title="t('admin.attachments.storageInstances.pluginRequired')" />
    <div v-else-if="instances.length" class="mt-4 grid gap-3 lg:grid-cols-2">
      <article v-for="item in instances" :key="item.id" class="rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <h4 class="truncate text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ item.name }}</h4>
              <UBadge v-if="item.active" color="primary" variant="soft">{{ t('admin.attachments.storageInstances.current') }}</UBadge>
              <UBadge :color="statusColor(item.status)" variant="subtle">{{ t(`admin.attachments.storageInstances.status.${item.status}`) }}</UBadge>
            </div>
            <p class="mt-1 truncate text-xs text-slate-500 dark:text-zinc-400">{{ item.schema.label }} · {{ item.extensionId }}</p>
            <p class="mt-2 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.storageInstances.historyCount', { count: item.attachmentCount }) }}</p>
            <p v-if="item.lastProbeMessage && item.status === 'error'" class="mt-1 break-words text-xs text-red-600 dark:text-red-300">{{ item.lastProbeMessage }}</p>
          </div>
          <UIcon name="i-lucide-database" class="size-5 shrink-0 text-slate-400" />
        </div>
        <div class="mt-4 flex flex-wrap gap-2">
          <UButton type="button" size="sm" icon="i-lucide-arrow-right-left" :disabled="item.active" :loading="activating === item.id" @click="activate(item)">{{ t('admin.attachments.storageInstances.makeCurrent') }}</UButton>
          <UButton type="button" size="sm" color="neutral" variant="outline" icon="i-lucide-pencil" @click="openEdit(item)">{{ t('admin.common.edit') }}</UButton>
          <UButton type="button" size="sm" color="error" variant="ghost" icon="i-lucide-trash-2" :disabled="item.active || item.attachmentCount > 0" :title="item.attachmentCount > 0 ? t('admin.attachments.storageInstances.deleteBlocked') : undefined" @click="confirmDelete(item)">{{ t('admin.attachments.storageInstances.delete') }}</UButton>
        </div>
      </article>
    </div>
    <p v-else-if="!pending" class="mt-4 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.storageInstances.empty') }}</p>
  </section>

  <UModal v-model:open="editorOpen" :ui="{ content: 'sm:max-w-3xl' }">
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <header class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div><h3 class="text-base font-bold">{{ editing ? t('admin.attachments.storageInstances.editTitle') : t('admin.attachments.storageInstances.createTitle') }}</h3></div>
          <UButton type="button" icon="i-lucide-x" color="neutral" variant="ghost" :aria-label="t('admin.common.cancel')" @click="() => { editorOpen = false }" />
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <div class="grid gap-4 sm:grid-cols-2">
            <UFormField :label="t('admin.attachments.storageInstances.name')" required><UInput v-model="instanceName" class="w-full" :placeholder="t('admin.attachments.storageInstances.namePlaceholder')" /></UFormField>
            <UFormField :label="t('admin.attachments.storageInstances.provider')">
              <USelect v-model="selectedExtensionId" class="w-full" :disabled="Boolean(editing)" value-key="value" label-key="label" :items="multiInstanceProviders.map(item => ({ value: item.extensionId, label: item.label }))" />
            </UFormField>
          </div>
          <UAlert class="mt-4" color="primary" variant="soft" icon="i-lucide-list-checks" :title="t('admin.attachments.storageInstances.guideTitle')" :description="t('admin.attachments.storageInstances.guideDescription')" />
          <div v-if="activeSchema" class="mt-4 divide-y divide-slate-200 border-y border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
            <SFExtensionSettingsField v-for="field in activeSchema.fields" :key="field.key" :item="field" :model-value="formValues[field.key] || ''" @update:model-value="updateValue(field.key, $event)" />
          </div>
          <UAlert v-else class="mt-4" color="neutral" variant="soft" icon="i-lucide-info" :title="t('admin.attachments.storageInstances.schemaAfterFirstSave')" />
          <UAlert v-if="missingRequiredFields.length" class="mt-4" color="warning" variant="soft" icon="i-lucide-circle-alert" :title="t('admin.attachments.storageInstances.missingRequired', { fields: missingRequiredFieldsText })" />
          <UAlert
            v-else-if="draftProbeResult"
            class="mt-4"
            :color="draftProbeResult.ok ? 'success' : 'error'"
            variant="soft"
            :icon="draftProbeResult.ok ? 'i-lucide-circle-check' : 'i-lucide-circle-x'"
            :title="draftProbeResult.ok ? t('admin.attachments.storageInstances.draftTestPassed') : t('admin.attachments.storageInstances.draftTestFailed')"
            :description="draftProbeResult.message"
          />
          <UAlert v-else-if="draftProbeError" class="mt-4" color="error" variant="soft" icon="i-lucide-circle-x" :title="t('admin.attachments.storageInstances.draftTestFailed')" :description="draftProbeError" />
        </div>
        <footer class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="mr-auto min-w-0 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.attachments.storageInstances.testDraftHint') }}
          </div>
          <UButton type="button" color="neutral" variant="outline" icon="i-lucide-plug-zap" :loading="probing" :disabled="!activeSchema || missingRequiredFields.length > 0" @click="probeDraft">{{ t('admin.attachments.storageInstances.testDraft') }}</UButton>
          <UButton type="button" icon="i-lucide-save" :loading="saving" :disabled="!instanceName || !selectedExtensionId || missingRequiredFields.length > 0" @click="save">{{ t('admin.common.save') }}</UButton>
        </footer>
      </div>
    </template>
  </UModal>

  <UModal v-model:open="deleteOpen" :ui="{ content: 'sm:max-w-lg' }">
    <template #content>
      <div class="p-5">
        <h3 class="text-base font-bold">{{ t('admin.attachments.storageInstances.deleteTitle') }}</h3>
        <p class="mt-2 text-sm text-slate-600 dark:text-zinc-300">{{ t('admin.attachments.storageInstances.deleteConfirm', { name: pendingDelete?.name }) }}</p>
        <div class="mt-5 flex justify-end gap-2">
          <UButton type="button" color="neutral" variant="ghost" @click="() => { deleteOpen = false }">{{ t('admin.common.cancel') }}</UButton>
          <UButton type="button" color="error" icon="i-lucide-trash-2" :loading="deleting" @click="removeInstance">{{ t('admin.attachments.storageInstances.delete') }}</UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
