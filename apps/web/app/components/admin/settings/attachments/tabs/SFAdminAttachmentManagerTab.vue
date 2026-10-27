<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { userDisplayName } from '~/utils/forum/userDisplay'
import {
  attachmentStatusColor,
  buildAttachmentListQuery,
  humanFileSize,
  type AdminAttachment,
  type AttachmentCleanupResult,
  type AttachmentDetail,
  type AttachmentFilters,
  type AttachmentList,
  type AttachmentRecountResult
} from '../model'
import { cleanupOutcome, reconcilePreviewOutcome, reconcileSamplesText } from '../attachmentMaintenance'

const { format: formatSiteDateTime } = useSiteDateTime()
const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const loadingAttachments = ref(false)
const loadingDetail = ref(false)
const selected = ref<AttachmentDetail | null>(null)
const list = ref<AttachmentList>({ items: [], total: 0, page: 1, perPage: 20 })
const reconciling = ref(false)
const reconcileApplying = ref(false)
const reconcileOpen = ref(false)
const reconcilePreview = ref<AttachmentRecountResult | null>(null)
const filters = reactive<AttachmentFilters>({
  query: '',
  provider: '',
  status: '',
  contentType: '',
  referenceStatus: ''
})
const totalPages = computed(() => Math.max(1, Math.ceil(list.value.total / list.value.perPage)))
const listStart = computed(() => list.value.total === 0 ? 0 : (list.value.page - 1) * list.value.perPage + 1)
const listEnd = computed(() => Math.min(list.value.total, list.value.page * list.value.perPage))

const coreProviderLabels = computed<Record<string, string>>(() => ({
  local: t('admin.attachments.providers.local')
}))
const providerChoices = computed(() => {
  const values = new Set([...Object.keys(coreProviderLabels.value), ...list.value.items.map(item => item.provider)])
  if (filters.provider) values.add(filters.provider)
  return [...values].map(value => ({ value, label: coreProviderLabels.value[value] || value }))
})

onMounted(fetchAttachments)
defineExpose({ refresh: fetchAttachments, pending: loadingAttachments })

async function requestAttachmentPage(page: number) {
  return request<AttachmentList>(`/admin/attachments?${buildAttachmentListQuery({ page, perPage: list.value.perPage }, filters)}`)
}

async function fetchAttachments(page = list.value.page) {
  loadingAttachments.value = true
  try {
    let result = await requestAttachmentPage(page)
    const lastPage = Math.max(1, Math.ceil(result.total / result.perPage))
    if (result.total > 0 && result.page > lastPage) {
      result = await requestAttachmentPage(lastPage)
    }
    list.value = result
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.listLoadFailed') })
  } finally {
    loadingAttachments.value = false
  }
}

async function applyFilters() {
  selected.value = null
  await fetchAttachments(1)
}

async function goToPage(page: number) {
  const nextPage = Math.min(totalPages.value, Math.max(1, page))
  if (nextPage === list.value.page || loadingAttachments.value) return
  selected.value = null
  await fetchAttachments(nextPage)
}

async function updateAttachmentStatus(item: AdminAttachment, status: 'active' | 'disabled') {
  try {
    const updated = await request<AdminAttachment>(`/admin/attachments/${item.id}`, {
      method: 'PATCH',
      body: { status }
    })
    replaceAttachment(updated)
    if (selected.value?.id === updated.id) selected.value = { ...selected.value, ...updated }
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.attachments.statusUpdated'), duration: 5000 })
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.updateFailed') })
  }
}

async function deleteAttachment(item: AdminAttachment) {
  try {
    const updated = await request<AdminAttachment>(`/admin/attachments/${item.id}`, { method: 'DELETE' })
    replaceAttachment(updated)
    if (selected.value?.id === updated.id) selected.value = { ...selected.value, ...updated }
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.attachments.deleted'), duration: 5000 })
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.deleteFailed') })
  }
}

async function cleanupAttachments() {
  try {
    const result = await request<AttachmentCleanupResult>('/admin/attachments/cleanup', {
      method: 'POST',
      body: { limit: 100 }
    })
    const outcome = cleanupOutcome(result)
    toast.add({
      color: outcome.color,
      icon: outcome.icon,
      title: t(outcome.messageKey, outcome.params),
      duration: 5000
    })
    await fetchAttachments()
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.cleanupFailed'), duration: 0 })
  }
}

async function previewReconcile() {
  reconciling.value = true
  try {
    const preview = await request<AttachmentRecountResult>('/admin/attachments/reconcile', {
      method: 'POST',
      body: { dryRun: true }
    })
    if (reconcilePreviewOutcome(preview) === 'noop') {
      toast.add({ color: 'info', icon: 'i-lucide-check', title: t('admin.attachments.reconcileUpToDate'), duration: 5000 })
      return
    }
    reconcilePreview.value = preview
    reconcileOpen.value = true
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.reconcileFailed'), duration: 0 })
  } finally {
    reconciling.value = false
  }
}

async function applyReconcile() {
  reconcileApplying.value = true
  try {
    const result = await request<AttachmentRecountResult>('/admin/attachments/reconcile', {
      method: 'POST',
      body: { dryRun: false }
    })
    reconcileOpen.value = false
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.attachments.reconcileApplied', {
        lowered: result.lowered,
        raised: result.raised,
        unchanged: result.unchanged
      }),
      duration: 5000
    })
    await fetchAttachments()
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.reconcileFailed'), duration: 0 })
  } finally {
    reconcileApplying.value = false
  }
}

const samplesText = computed(() => reconcileSamplesText(reconcilePreview.value?.samples))

/** 低频维护动作收进图标下拉：三个文字按钮会把这一列挤到换行甚至裁切。 */
const maintenanceItems = computed(() => [[
  { label: t('admin.attachments.reconcile'), icon: 'i-lucide-calculator', onSelect: () => { void previewReconcile() } }
]])

function replaceAttachment(item: AdminAttachment) {
  list.value.items = list.value.items.map(current => current.id === item.id ? item : current)
}

async function selectAttachment(item: AdminAttachment) {
  selected.value = { ...item, references: selected.value?.id === item.id ? selected.value.references : [] }
  loadingDetail.value = true
  try {
    selected.value = await request<AttachmentDetail>(`/admin/attachments/${item.id}`)
  } catch (error) {
    toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.attachments.detailLoadFailed') })
  } finally {
    loadingDetail.value = false
  }
}

async function copySelectedURL() {
  if (!selected.value?.url || !import.meta.client) return
  await navigator.clipboard.writeText(selected.value.url)
  toast.add({ color: 'success', icon: 'i-lucide-copy-check', title: t('admin.attachments.copied'), duration: 5000 })
}

function providerLabel(provider: string) {
  return coreProviderLabels.value[provider] || provider
}

function isPreviewableImage(item: AdminAttachment) {
  return item.contentType.startsWith('image/') && item.url
}
</script>

<template>
  <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
    <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <template #header>
        <div class="grid gap-3 lg:grid-cols-[1fr_150px_150px_150px_auto]">
          <UInput v-model="filters.query" size="lg" icon="i-lucide-search" :placeholder="t('admin.attachments.searchPlaceholder')" @keyup.enter="applyFilters" />
          <select v-model="filters.provider" class="h-10 rounded-md border border-slate-200 bg-white px-3 text-sm dark:border-zinc-700 dark:bg-zinc-950">
            <option value="">{{ t('admin.attachments.allProviders') }}</option>
            <option v-for="choice in providerChoices" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
          </select>
          <select v-model="filters.status" class="h-10 rounded-md border border-slate-200 bg-white px-3 text-sm dark:border-zinc-700 dark:bg-zinc-950">
            <option value="">{{ t('admin.attachments.allStatuses') }}</option>
            <option value="active">{{ t('admin.attachments.status.active') }}</option>
            <option value="disabled">{{ t('admin.attachments.status.disabled') }}</option>
            <option value="deleted">{{ t('admin.attachments.status.deleted') }}</option>
          </select>
          <select v-model="filters.referenceStatus" class="h-10 rounded-md border border-slate-200 bg-white px-3 text-sm dark:border-zinc-700 dark:bg-zinc-950">
            <option value="">{{ t('admin.attachments.allReferences') }}</option>
            <option value="referenced">{{ t('admin.attachments.referenced') }}</option>
            <option value="orphan">{{ t('admin.attachments.orphan') }}</option>
          </select>
          <div class="flex items-center justify-end gap-2">
            <UButton color="primary" leading-icon="i-lucide-search" :loading="loadingAttachments" @click="applyFilters">{{ t('admin.attachments.filter') }}</UButton>
            <UButton color="neutral" variant="outline" leading-icon="i-lucide-trash-2" @click="cleanupAttachments">{{ t('admin.attachments.cleanup') }}</UButton>
            <UDropdownMenu :items="maintenanceItems" :content="{ align: 'end' }">
              <template #default="{ open }">
                <UButton
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-wrench"
                  :loading="reconciling"
                  :aria-label="t('admin.attachments.maintenance')"
                  :title="t('admin.attachments.maintenance')"
                  :aria-expanded="open"
                />
              </template>
            </UDropdownMenu>
          </div>
        </div>
      </template>

      <div class="overflow-x-auto">
        <table class="min-w-full divide-y divide-slate-200 text-sm dark:divide-zinc-800">
          <thead class="text-left text-xs uppercase text-slate-500 dark:text-zinc-400">
            <tr>
              <th class="px-3 py-2">{{ t('admin.attachments.file') }}</th>
              <th class="px-3 py-2">{{ t('admin.attachments.provider') }}</th>
              <th class="px-3 py-2">{{ t('admin.attachments.size') }}</th>
              <th class="px-3 py-2">{{ t('admin.attachments.references') }}</th>
              <th class="px-3 py-2">{{ t('admin.attachments.statusLabel') }}</th>
              <th class="px-3 py-2">{{ t('admin.attachments.createdAt') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-slate-100 dark:divide-zinc-800">
            <tr v-for="item in list.items" :key="item.id" class="cursor-pointer hover:bg-slate-50 dark:hover:bg-zinc-950/60" @click="selectAttachment(item)">
              <td class="px-3 py-3">
                <div class="flex min-w-[220px] items-center gap-3">
                  <img v-if="isPreviewableImage(item)" :src="item.url" :alt="item.name" class="size-10 rounded-md border border-slate-200 object-cover dark:border-zinc-800" loading="lazy">
                  <div v-else class="grid size-10 place-items-center rounded-md border border-slate-200 bg-slate-50 text-slate-500 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-400">
                    <UIcon name="i-lucide-file" class="size-5" />
                  </div>
                  <div class="min-w-0">
                    <div class="truncate font-medium text-slate-900 dark:text-zinc-100">{{ item.name }}</div>
                    <div class="max-w-xs truncate font-mono text-xs text-slate-500 dark:text-zinc-400">{{ item.contentType }}</div>
                  </div>
                </div>
              </td>
              <td class="px-3 py-3">{{ providerLabel(item.provider) }}</td>
              <td class="px-3 py-3">{{ humanFileSize(item.size) }}</td>
              <td class="px-3 py-3">{{ item.referenceCount }}</td>
              <td class="px-3 py-3"><UBadge :color="attachmentStatusColor(item.status)" variant="soft">{{ t(`admin.attachments.status.${item.status}`) }}</UBadge></td>
              <td class="px-3 py-3 text-xs text-slate-500 dark:text-zinc-400">{{ formatSiteDateTime(item.createdAt) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <SFEmptyState v-if="!loadingAttachments && list.items.length === 0" :title="t('admin.attachments.empty')" />

      <div
        v-if="list.total > 0"
        class="mt-4 flex flex-col gap-3 border-t border-slate-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
      >
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.attachments.paginationSummary', { start: listStart, end: listEnd, total: list.total }) }}
        </p>
        <UPagination
          v-if="totalPages > 1"
          :page="list.page"
          :total="list.total"
          :items-per-page="list.perPage"
          class="justify-end"
          @update:page="goToPage"
        />
      </div>
    </UCard>

    <aside class="rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <template v-if="selected">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h3 class="truncate text-base font-bold text-slate-900 dark:text-zinc-100">{{ selected.name }}</h3>
            <p class="mt-1 font-mono text-xs text-slate-500 dark:text-zinc-400">{{ selected.publicId }}</p>
          </div>
          <UBadge :color="attachmentStatusColor(selected.status)" variant="soft">{{ t(`admin.attachments.status.${selected.status}`) }}</UBadge>
        </div>
        <div v-if="loadingDetail" class="mt-3 flex items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
          <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
          {{ t('admin.attachments.loadingDetail') }}
        </div>
        <dl class="mt-4 space-y-3 text-sm">
          <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.objectKey') }}</dt><dd class="break-all font-mono text-xs">{{ selected.objectKey }}</dd></div>
          <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.sha256') }}</dt><dd class="break-all font-mono text-xs">{{ selected.sha256 }}</dd></div>
          <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.owner') }}</dt><dd>{{ userDisplayName(selected.owner, '-') }}</dd></div>
          <div><dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.url') }}</dt><dd class="break-all font-mono text-xs">{{ selected.url }}</dd></div>
        </dl>
        <div class="mt-4 flex flex-wrap gap-2">
          <UButton color="neutral" variant="outline" leading-icon="i-lucide-external-link" :to="selected.url" target="_blank">{{ t('admin.attachments.open') }}</UButton>
          <UButton color="neutral" variant="outline" leading-icon="i-lucide-copy" @click="copySelectedURL">{{ t('admin.attachments.copyLink') }}</UButton>
          <UButton v-if="selected.status !== 'disabled'" color="warning" variant="soft" leading-icon="i-lucide-eye-off" @click="updateAttachmentStatus(selected, 'disabled')">{{ t('admin.attachments.disable') }}</UButton>
          <UButton v-if="selected.status !== 'active'" color="success" variant="soft" leading-icon="i-lucide-rotate-ccw" @click="updateAttachmentStatus(selected, 'active')">{{ t('admin.attachments.restore') }}</UButton>
          <UButton color="error" variant="soft" leading-icon="i-lucide-trash-2" :disabled="selected.referenceCount > 0" @click="deleteAttachment(selected)">{{ t('admin.attachments.delete') }}</UButton>
        </div>
        <section class="mt-6 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.referencesTitle') }}</h4>
          <div v-if="selected.references.length > 0" class="mt-3 space-y-2">
            <div v-for="reference in selected.references" :key="reference.id" class="rounded-md border border-slate-200 p-3 text-xs dark:border-zinc-800">
              <div class="font-medium text-slate-900 dark:text-zinc-100">{{ reference.resourceType }} #{{ reference.resourceId }}</div>
              <div class="mt-1 text-slate-500 dark:text-zinc-400">{{ reference.context || t('admin.attachments.noReferenceContext') }}</div>
            </div>
          </div>
          <p v-else class="mt-3 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.noReferences') }}</p>
        </section>
      </template>
      <SFEmptyState v-else :title="t('admin.attachments.selectOne')" />
    </aside>
  </div>

  <UModal v-model:open="reconcileOpen" :ui="{ content: 'sm:max-w-lg' }">
    <template #content>
      <div class="p-5">
        <h3 class="text-base font-bold">
          {{ t('admin.attachments.reconcileConfirmTitle') }}
        </h3>
        <p class="mt-2 text-sm text-slate-600 dark:text-zinc-300">
          {{ t('admin.attachments.reconcileConfirmBody') }}
        </p>
        <dl class="mt-4 grid grid-cols-3 gap-3 text-center">
          <div class="rounded-md border border-slate-200 p-3 dark:border-zinc-800">
            <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.reconcileLowered') }}</dt>
            <dd class="mt-1 font-mono text-lg font-bold">{{ reconcilePreview?.lowered ?? 0 }}</dd>
          </div>
          <div class="rounded-md border border-slate-200 p-3 dark:border-zinc-800">
            <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.reconcileRaised') }}</dt>
            <dd class="mt-1 font-mono text-lg font-bold">{{ reconcilePreview?.raised ?? 0 }}</dd>
          </div>
          <div class="rounded-md border border-slate-200 p-3 dark:border-zinc-800">
            <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.attachments.reconcileUnchanged') }}</dt>
            <dd class="mt-1 font-mono text-lg font-bold">{{ reconcilePreview?.unchanged ?? 0 }}</dd>
          </div>
        </dl>
        <p v-if="samplesText" class="mt-3 break-words text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.attachments.reconcileSamples', { ids: samplesText }) }}
        </p>
        <div class="mt-5 flex justify-end gap-2">
          <UButton type="button" color="neutral" variant="ghost" @click="() => { reconcileOpen = false }">{{ t('admin.common.cancel') }}</UButton>
          <UButton type="button" icon="i-lucide-calculator" :loading="reconcileApplying" @click="applyReconcile">{{ t('admin.attachments.reconcileApply') }}</UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
