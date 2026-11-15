<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useAdminForumContent } from '~/composables/admin/useAdminForumContent'
import SFTopicEditor from '~/components/forum/SFTopicEditor.vue'
import type {
  AdminForumContentDetail,
  AdminForumContentFilters,
  AdminForumContentKind,
  AdminForumContentRow,
  AdminForumTopicDetail,
  ForumRevisionDetail,
  ForumRevisionSummary
} from '~/utils/admin/adminForumContent'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import {
  buildAdminForumContentRouteQuery,
  parseAdminForumContentRouteQuery
} from '~/utils/admin/adminForumContentRouteQuery'
import SFAdminForumCommentEditor from '~/components/admin/forum/SFAdminForumCommentEditor.vue'
import SFAdminForumRevisionDiff from '~/components/admin/forum/SFAdminForumRevisionDiff.vue'
import SFAdminForumRevisionTimeline from '~/components/admin/forum/SFAdminForumRevisionTimeline.vue'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminForumContent' })

const { t } = useI18n()
const toast = useToast()
const { can, user } = usePermissions()
const adminPage = useAdminPage('/forum/content')
const contentApi = useAdminForumContent()
const { format: formatSiteDateTime } = useSiteDateTime()

const canTopics = computed(() => can('topic.edit_any') || can('topic.revision.view_any'))
const canComments = computed(() => can('post.edit_any') || can('post.revision.view_any'))
const canEditTopics = computed(() => can('topic.edit_any'))
const canEditComments = computed(() => can('post.edit_any'))
const canViewTopicHistory = computed(() => can('topic.revision.view_any'))
const canViewCommentHistory = computed(() => can('post.revision.view_any'))
// Reka Select 将空字符串保留给清空模型值，不能作为实际 SelectItem 的 value。
const ALL_STATUS_VALUE = '__all__'
const availableTabs = computed<AdminForumContentKind[]>(() => [
  ...(canTopics.value ? ['topics' as const] : []),
  ...(canComments.value ? ['comments' as const] : [])
])
const route = useRoute()
const router = useRouter()
// UX2-14：tab 与筛选从地址栏派生（F5 / 分享后保持）。
// 有意取舍：分页游标不入 query —— 刷新回到第一页，见 adminForumContentRouteQuery 的注释。
const routeState = parseAdminForumContentRouteQuery(route.query, availableTabs.value)
const activeTab = ref<AdminForumContentKind>(routeState.tab ?? (canTopics.value ? 'topics' : 'comments'))
const filters = reactive<AdminForumContentFilters>({
  ...routeState.filters,
  status: routeState.filters.status || ALL_STATUS_VALUE
})
const list = ref<AdminForumContentRow[]>([])
const pending = ref(false)
const errorMessage = ref('')
const cursors = ref<string[]>([''])
const cursorIndex = ref(0)
const hasMore = ref(false)
const nextCursor = ref('')
const selected = ref<AdminForumContentDetail | null>(null)
const detailPending = ref(false)
const detailError = ref('')
const staffReason = ref('')
const conflict = ref<AdminForumContentDetail | null>(null)
const historyOpen = ref(false)
const history = ref<ForumRevisionSummary[]>([])
const historyHasMore = ref(false)
const historyCursor = ref('')
const historyPending = ref(false)
const selectedRevision = ref<ForumRevisionSummary | null>(null)
const revisionDetail = ref<ForumRevisionDetail | null>(null)
const currentRevisionDetail = ref<ForumRevisionDetail | null>(null)
const revisionDetailPending = ref(false)
const revisionDetailError = ref('')
const revisionAction = ref<'restore' | 'redact' | null>(null)
const revisionActionReason = ref('')
const redactionConfirmation = ref('')
const revisionActionError = ref('')
const revisionActionPending = ref(false)

const selectedIsDeleted = computed(() => selected.value?.status === 'deleted')
const selectedCanEdit = computed(() => {
  if (!selected.value || selectedIsDeleted.value) return false
  return selected.value.targetType === 'topic' ? canEditTopics.value : canEditComments.value
})
const selectedCanViewHistory = computed(() => selected.value?.targetType === 'topic'
  ? canViewTopicHistory.value
  : canViewCommentHistory.value)
const isSuperAdmin = computed(() => user.value?.status === 'active' && user.value.roleKeys.includes('super_admin'))
const editingAnotherAuthor = computed(() => Boolean(selected.value && selected.value.authorUserId !== user.value?.id))
const requiresReason = computed(() => selectedCanEdit.value && editingAnotherAuthor.value)
const pageNumber = computed(() => cursorIndex.value + 1)
const selectedRevisionCanRestore = computed(() => Boolean(
  selected.value && selectedRevision.value && selectedCanEdit.value && !selectedIsDeleted.value
  && !selectedRevision.value.current && !selectedRevision.value.redacted && selectedRevision.value.restorableFields.includes('content')
))
const selectedRevisionCanRedact = computed(() => Boolean(
  selected.value && selectedRevision.value && isSuperAdmin.value && !selectedRevision.value.current && !selectedRevision.value.redacted
))

const statusOptions = computed(() => [
  { label: t('admin.forum.content.allStatuses'), value: ALL_STATUS_VALUE },
  ...['active', 'locked', 'pending', 'rejected', 'hidden', 'deleted'].map(value => ({
    label: t(`admin.forum.content.status.${value}`), value
  }))
])
const perPageOptions = [20, 50, 100].map(value => ({ label: `${value}`, value }))

function isCurrentTabAllowed() {
  return activeTab.value === 'topics' ? canTopics.value : canComments.value
}

async function load() {
  if (!isCurrentTabAllowed()) {
    list.value = []
    return
  }
  pending.value = true
  errorMessage.value = ''
  try {
    const response = await contentApi.list(activeTab.value, requestFilters(), cursors.value[cursorIndex.value])
    list.value = response.items
    hasMore.value = response.hasMore
    nextCursor.value = response.nextCursor || ''
  } catch (cause) {
    list.value = []
    errorMessage.value = apiErrorMessage(cause) || t('admin.forum.content.loadFailed')
  } finally {
    pending.value = false
  }
}

function requestFilters(): AdminForumContentFilters {
  return {
    ...filters,
    status: filters.status === ALL_STATUS_VALUE ? '' : filters.status,
    updatedFrom: normalizeFilterDate(filters.updatedFrom),
    updatedTo: normalizeFilterDate(filters.updatedTo)
  }
}

function normalizeFilterDate(value?: string) {
  if (!value) return ''
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString()
}

function resetPagination() {
  cursors.value = ['']
  cursorIndex.value = 0
}

// 状态变更后写回地址栏：replace 不污染历史，且不触发新的导航栈。
async function syncRouteQuery() {
  // SSR 没有浏览器历史；`watch(availableTabs, { immediate: true })` 可能在服务端触发
  // `selectTab`，此时调 `router.replace` 会告警（与 entity-meta / login-methods 同一条守卫）。
  if (!import.meta.client) return
  const query = buildAdminForumContentRouteQuery(activeTab.value, {
    ...filters,
    status: filters.status === ALL_STATUS_VALUE ? '' : filters.status
  })
  await router.replace({ query })
}

async function applyFilters() {
  resetPagination()
  await syncRouteQuery()
  await load()
}

async function resetFilters() {
  Object.assign(filters, { status: ALL_STATUS_VALUE, authorUserID: undefined, authorUsername: '', updatedFrom: '', updatedTo: '', topicID: undefined, titlePrefix: '', categorySlug: '', perPage: 20 })
  await applyFilters()
}

async function selectTab(tab: AdminForumContentKind) {
  if (activeTab.value === tab || (tab === 'topics' ? !canTopics.value : !canComments.value)) return
  activeTab.value = tab
  selected.value = null
  conflict.value = null
  resetPagination()
  await syncRouteQuery()
  await load()
}

async function nextPage() {
  if (!hasMore.value || !nextCursor.value) return
  cursors.value = [...cursors.value.slice(0, cursorIndex.value + 1), nextCursor.value]
  cursorIndex.value += 1
  await load()
}

async function previousPage() {
  if (cursorIndex.value === 0) return
  cursorIndex.value -= 1
  await load()
}

async function openDetail(row: AdminForumContentRow) {
  detailPending.value = true
  detailError.value = ''
  selected.value = null
  conflict.value = null
  staffReason.value = ''
  resetHistory()
  try {
    selected.value = row.targetType === 'topic'
      ? await contentApi.getTopic(row.id)
      : await contentApi.getComment(row.id)
  } catch (cause) {
    detailError.value = apiErrorMessage(cause) || t('admin.forum.content.detailLoadFailed')
  } finally {
    detailPending.value = false
  }
}

function closeDetail() {
  selected.value = null
  detailError.value = ''
  conflict.value = null
  staffReason.value = ''
  resetHistory()
}

function showConflict() {
  conflict.value = selected.value
}

async function reloadLatest() {
  if (!conflict.value) return
  const target = conflict.value
  conflict.value = null
  await openDetail(target)
  await load()
}

function resetHistory() {
  historyOpen.value = false
  history.value = []
  historyHasMore.value = false
  historyCursor.value = ''
  selectedRevision.value = null
  revisionDetail.value = null
  currentRevisionDetail.value = null
  revisionDetailError.value = ''
  revisionAction.value = null
  revisionActionError.value = ''
}

async function viewHistory() {
  conflict.value = null
  if (!selected.value || !selectedCanViewHistory.value) return
  historyOpen.value = true
  if (!history.value.length) await loadHistory()
}

async function loadHistory(append = false) {
  if (!selected.value || !selectedCanViewHistory.value) return
  historyPending.value = true
  revisionDetailError.value = ''
  try {
    const after = append ? historyCursor.value : undefined
    const response = selected.value.targetType === 'topic'
      ? await contentApi.listTopicRevisions(selected.value.id, after)
      : await contentApi.listCommentRevisions(selected.value.id, after)
    history.value = append ? [...history.value, ...response.items] : response.items
    historyHasMore.value = response.hasMore
    historyCursor.value = response.nextCursor || ''
  } catch (cause) {
    revisionDetailError.value = apiErrorMessage(cause) || t('admin.forum.content.history.loadFailed')
  } finally {
    historyPending.value = false
  }
}

async function loadRevision(revision: ForumRevisionSummary) {
  const target = selected.value
  if (!target || revision.redacted) {
    selectedRevision.value = revision
    revisionDetail.value = null
    revisionDetailError.value = revision.redacted ? t('admin.forum.content.history.redactedDetail') : ''
    return
  }
  selectedRevision.value = revision
  revisionDetail.value = null
  currentRevisionDetail.value = null
  revisionDetailPending.value = true
  revisionDetailError.value = ''
  try {
    const current = history.value.find(item => item.current)
    // 捕获局部 target，避免异步回调里 selected 被清空后 TS 仍把 id 当成可能 null。
    const loadDetail = (revisionNo: number) => target.targetType === 'topic'
      ? contentApi.getTopicRevision(target.id, revisionNo)
      : contentApi.getCommentRevision(target.id, revisionNo)
    const [detail, currentDetail] = await Promise.all([
      loadDetail(revision.revisionNo),
      current && current.revisionNo !== revision.revisionNo ? loadDetail(current.revisionNo) : Promise.resolve(null)
    ])
    revisionDetail.value = detail
    currentRevisionDetail.value = currentDetail || detail
  } catch (cause) {
    revisionDetailError.value = apiErrorMessage(cause) || t('admin.forum.content.history.detailLoadFailed')
  } finally {
    revisionDetailPending.value = false
  }
}

function openRevisionAction(action: 'restore' | 'redact') {
  revisionAction.value = action
  revisionActionReason.value = ''
  redactionConfirmation.value = ''
  revisionActionError.value = ''
}

function closeRevisionAction() {
  revisionAction.value = null
  revisionActionError.value = ''
}

async function submitRevisionAction() {
  if (!selected.value || !selectedRevision.value || !revisionAction.value) return
  const reason = revisionActionReason.value.trim()
  if (!reason || (revisionAction.value === 'redact' && redactionConfirmation.value.trim() !== 'REDACT')) {
    revisionActionError.value = revisionAction.value === 'redact'
      ? t('admin.forum.content.history.redactionConfirmationRequired')
      : t('admin.forum.content.history.reasonRequired')
    return
  }
  revisionActionPending.value = true
  revisionActionError.value = ''
  try {
    if (revisionAction.value === 'restore') {
      const updated = selected.value.targetType === 'topic'
        ? await contentApi.restoreTopicRevision(selected.value.id, selectedRevision.value.revisionNo, { expectedRevision: selected.value.currentRevision, reason })
        : await contentApi.restoreCommentRevision(selected.value.id, selectedRevision.value.revisionNo, { expectedRevision: selected.value.currentRevision, reason })
      toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('admin.forum.content.history.restored', { revision: updated.currentRevision }), duration: 5000 })
      closeRevisionAction()
      await openDetail(selected.value)
      historyOpen.value = true
      await loadHistory()
      return
    }
    if (selected.value.targetType === 'topic') {
      await contentApi.redactTopicRevision(selected.value.id, selectedRevision.value.revisionNo, { expectedRevision: selected.value.currentRevision, reason, confirmation: 'REDACT' })
    } else {
      await contentApi.redactCommentRevision(selected.value.id, selectedRevision.value.revisionNo, { expectedRevision: selected.value.currentRevision, reason, confirmation: 'REDACT' })
    }
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('admin.forum.content.history.redacted'), duration: 5000 })
    closeRevisionAction()
    await loadHistory()
    const redacted = history.value.find(item => item.revisionNo === selectedRevision.value?.revisionNo)
    if (redacted) await loadRevision(redacted)
  } catch (cause) {
    if (apiErrorReason(cause) === 'forum.revision_conflict') {
      conflict.value = selected.value
      closeRevisionAction()
      return
    }
    revisionActionError.value = apiErrorMessage(cause) || t('admin.forum.content.history.actionFailed')
  } finally {
    revisionActionPending.value = false
  }
}

async function saved() {
  if (selected.value) await openDetail(selected.value)
  await load()
}

watch(availableTabs, (tabs) => {
  if (!tabs.includes(activeTab.value) && tabs[0]) void selectTab(tabs[0])
}, { immediate: true })

void load()
useSeoMeta({ title: t('admin.forum.content.metaTitle') })
</script>

<template>
  <div data-testid="admin-forum-content" class="min-w-0 w-full space-y-4">
    <header class="flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.forum.content.title') }}
      </h2>
      <p class="max-w-4xl text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.forum.content.intro') }}</p>
    </header>

    <UAlert
      v-if="!availableTabs.length"
      color="warning"
      variant="soft"
      icon="i-lucide-shield-alert"
      :title="t('admin.forum.content.permissionDenied')"
    />

    <template v-else>
      <UDashboardToolbar class="rounded-lg border border-slate-200 bg-white px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
        <template #left>
          <div class="flex flex-wrap items-center gap-2">
            <UButton
              v-for="tab in availableTabs"
              :key="tab"
              size="sm"
              :color="activeTab === tab ? 'primary' : 'neutral'"
              :variant="activeTab === tab ? 'solid' : 'outline'"
              @click="selectTab(tab)"
            >
              {{ t(`admin.forum.content.tabs.${tab}`) }}
            </UButton>
          </div>
        </template>
        <template #right>
          <UButton icon="i-lucide-refresh-cw" color="neutral" variant="subtle" :loading="pending" @click="load">
            {{ t('admin.common.refresh') }}
          </UButton>
        </template>
      </UDashboardToolbar>

      <section class="grid gap-3 rounded-lg border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900 md:grid-cols-2 xl:grid-cols-4">
        <UFormField :label="t('admin.forum.content.filters.status')">
          <USelect v-model="filters.status" :items="statusOptions" value-key="value" label-key="label" class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.forum.content.filters.author')">
          <UInput v-model="filters.authorUsername" :placeholder="t('admin.forum.content.filters.authorPlaceholder')" class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.forum.content.filters.authorId')">
          <UInput v-model.number="filters.authorUserID" type="number" min="1" class="w-full" />
        </UFormField>
        <UFormField v-if="activeTab === 'topics'" :label="t('admin.forum.content.filters.titlePrefix')">
          <UInput v-model="filters.titlePrefix" class="w-full" />
        </UFormField>
        <UFormField v-if="activeTab === 'topics'" :label="t('admin.forum.content.filters.categorySlug')">
          <UInput v-model="filters.categorySlug" class="w-full" />
        </UFormField>
        <UFormField v-else :label="t('admin.forum.content.filters.topicId')">
          <UInput v-model.number="filters.topicID" type="number" min="1" class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.forum.content.filters.updatedFrom')">
          <UInput v-model="filters.updatedFrom" type="datetime-local" class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.forum.content.filters.updatedTo')">
          <UInput v-model="filters.updatedTo" type="datetime-local" class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.forum.content.filters.perPage')">
          <USelect v-model="filters.perPage" :items="perPageOptions" value-key="value" label-key="label" class="w-full" />
        </UFormField>
        <div class="flex flex-wrap items-end gap-2">
          <UButton icon="i-lucide-filter" @click="applyFilters">{{ t('admin.forum.content.applyFilters') }}</UButton>
          <UButton icon="i-lucide-rotate-ccw" color="neutral" variant="outline" @click="resetFilters">{{ t('admin.common.reset') }}</UButton>
        </div>
      </section>

      <UAlert v-if="errorMessage" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="errorMessage" />

      <div class="overflow-x-auto rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <table class="w-full min-w-[760px] text-left text-sm">
          <thead class="border-b border-slate-200 bg-slate-50 text-xs text-slate-500 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-400">
            <tr>
              <th class="px-3 py-3 font-medium">{{ t('admin.forum.content.columns.content') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.forum.content.columns.author') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.forum.content.columns.status') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.forum.content.columns.revision') }}</th>
              <th class="px-3 py-3 font-medium">{{ t('admin.forum.content.columns.updatedAt') }}</th>
              <th class="px-3 py-3 text-right font-medium">{{ t('admin.forum.content.columns.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="pending" v-for="index in 5" :key="`skeleton-${index}`" class="border-b border-slate-100 dark:border-zinc-800">
              <td v-for="cell in 6" :key="cell" class="px-3 py-3"><USkeleton class="h-4 w-full" /></td>
            </tr>
            <tr v-else-if="!list.length">
              <td colspan="6" class="px-4 py-12 text-center text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.forum.content.empty') }}</td>
            </tr>
            <tr v-for="row in list" :key="`${row.targetType}-${row.id}`" class="border-b border-slate-100 last:border-0 dark:border-zinc-800">
              <td class="max-w-md px-3 py-3">
                <button type="button" class="block max-w-full text-left" @click="openDetail(row)">
                  <span class="block truncate font-medium text-slate-900 hover:text-[var(--sf-accent)] dark:text-zinc-100">{{ row.title || row.topicTitle || `#${row.id}` }}</span>
                  <span class="mt-0.5 block truncate text-xs text-slate-500 dark:text-zinc-400">{{ row.excerpt || t('admin.forum.content.noExcerpt') }}</span>
                </button>
              </td>
              <td class="px-3 py-3 text-xs text-slate-600 dark:text-zinc-300">{{ userDisplayName(row.author, `#${row.authorUserId}`) }}</td>
              <td class="px-3 py-3"><UBadge color="neutral" variant="soft">{{ t(`admin.forum.content.status.${row.status}`) }}</UBadge></td>
              <td class="px-3 py-3 tabular-nums">{{ row.currentRevision }}</td>
              <td class="px-3 py-3 whitespace-nowrap text-xs text-slate-500 dark:text-zinc-400">{{ formatSiteDateTime(row.updatedAt) }}</td>
              <td class="px-3 py-3 text-right">
                <div class="flex justify-end gap-1">
                  <UTooltip :text="t('admin.forum.content.inspect')"><UButton icon="i-lucide-eye" color="neutral" variant="ghost" size="sm" :aria-label="t('admin.forum.content.inspect')" @click="openDetail(row)" /></UTooltip>
                  <UTooltip v-if="row.targetType === 'topic' ? canEditTopics : canEditComments" :text="t('admin.common.edit')"><UButton icon="i-lucide-pencil" color="neutral" variant="ghost" size="sm" :aria-label="t('admin.common.edit')" @click="openDetail(row)" /></UTooltip>
                  <UTooltip v-if="row.targetType === 'topic' ? canViewTopicHistory : canViewCommentHistory" :text="t('admin.forum.content.viewHistory')"><UButton icon="i-lucide-history" color="neutral" variant="ghost" size="sm" :aria-label="t('admin.forum.content.viewHistory')" @click="openDetail(row).then(viewHistory)" /></UTooltip>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="flex items-center justify-between gap-3">
        <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.forum.content.page', { page: pageNumber }) }}</span>
        <div class="flex gap-2"><UButton color="neutral" variant="outline" :disabled="cursorIndex === 0 || pending" @click="previousPage">{{ t('admin.forum.content.previous') }}</UButton><UButton color="neutral" variant="outline" :disabled="!hasMore || pending" @click="nextPage">{{ t('admin.forum.content.next') }}</UButton></div>
      </div>
    </template>

    <USlideover :open="Boolean(selected || detailPending || detailError)" :ui="{ content: 'w-full max-w-3xl' }" @update:open="value => !value && closeDetail()">
      <template #content>
        <div class="flex h-full min-w-0 flex-col">
          <header class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
            <div class="min-w-0"><h3 class="truncate text-base font-semibold text-slate-900 dark:text-zinc-100">{{ selected?.title || selected?.topicTitle || t('admin.forum.content.detailTitle') }}</h3><p v-if="selected" class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.forum.content.authorLine', { author: userDisplayName(selected.author, `#${selected.authorUserId}`), revision: selected.currentRevision }) }}</p></div>
            <UButton icon="i-lucide-x" color="neutral" variant="ghost" :aria-label="t('common.close')" @click="closeDetail" />
          </header>
          <main class="min-h-0 flex-1 overflow-y-auto p-5">
            <div v-if="detailPending" class="space-y-4"><USkeleton class="h-6 w-1/2" /><USkeleton class="h-32 w-full" /><USkeleton class="h-64 w-full" /></div>
            <UAlert v-else-if="detailError" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="detailError" />
            <template v-else-if="selected">
              <UAlert v-if="conflict" color="error" variant="soft" icon="i-lucide-refresh-cw" :title="t('admin.forum.content.conflictTitle')" :description="t('admin.forum.content.conflictDescription')" class="mb-4">
                <template #actions><UButton size="sm" color="neutral" variant="outline" @click="reloadLatest">{{ t('admin.forum.content.reloadLatest') }}</UButton><UButton v-if="selectedCanViewHistory" size="sm" color="neutral" variant="ghost" @click="viewHistory">{{ t('admin.forum.content.viewHistory') }}</UButton></template>
              </UAlert>
              <UAlert v-if="editingAnotherAuthor && selectedCanEdit" color="warning" variant="soft" icon="i-lucide-user-round-pen" :title="t('admin.forum.content.editingAnotherAuthor')" class="mb-4" />
              <UAlert v-if="selectedIsDeleted" color="neutral" variant="soft" icon="i-lucide-lock" :title="t('admin.forum.content.deletedInspectionOnly')" class="mb-4" />
              <div v-if="selectedCanViewHistory" class="mb-4 flex items-center justify-between gap-3 rounded-lg border border-slate-200 p-3 dark:border-zinc-800">
                <span class="text-sm font-medium text-slate-700 dark:text-zinc-200">{{ t('admin.forum.content.history.title') }}</span>
                <UButton icon="i-lucide-history" color="neutral" variant="outline" size="sm" @click="viewHistory">{{ t('admin.forum.content.viewHistory') }}</UButton>
              </div>
              <div v-if="selectedCanEdit" class="mb-4 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.forum.content.canonicalSaveHint') }}</div>
              <SFTopicEditor
                v-if="selected.targetType === 'topic' && selectedCanEdit"
                :topic="selected as AdminForumTopicDetail"
                v-model:staff-reason="staffReason"
                :require-staff-reason="requiresReason"
                :editing-another-author="editingAnotherAuthor"
                @saved="saved"
                @conflict="showConflict"
                @cancel="closeDetail"
              />
              <SFAdminForumCommentEditor
                v-else-if="selected.targetType === 'comment' && selectedCanEdit"
                :comment="selected"
                v-model:reason="staffReason"
                :require-reason="requiresReason"
                @saved="saved"
                @conflict="showConflict"
              />
              <div v-else class="space-y-3"><p class="whitespace-pre-wrap break-words text-sm text-slate-700 dark:text-zinc-200">{{ selected.content.rawContent }}</p></div>

              <section v-if="historyOpen && selectedCanViewHistory" class="mt-8 border-t border-slate-200 pt-6 dark:border-zinc-800">
                <SFAdminForumRevisionTimeline
                  :revisions="history"
                  :selected-revision-no="selectedRevision?.revisionNo"
                  :loading-revision-no="revisionDetailPending ? selectedRevision?.revisionNo : undefined"
                  :loading="historyPending"
                  :has-more="historyHasMore"
                  @select="loadRevision"
                  @more="loadHistory(true)"
                />

                <div v-if="selectedRevision" class="mt-5 space-y-4">
                  <UAlert v-if="selectedRevision.redacted" color="error" variant="soft" icon="i-lucide-shield-alert" :title="t('admin.forum.content.history.redactedDetail')" :description="t('admin.forum.content.history.redactedDescription')" />
                  <UAlert v-else-if="!selectedRevision.snapshotComplete" color="warning" variant="soft" icon="i-lucide-info" :title="t('admin.forum.content.history.legacyTitle')" :description="t('admin.forum.content.history.legacyDescription')" />
                  <UAlert v-if="revisionDetailError && !selectedRevision.redacted" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="revisionDetailError" />
                  <div v-else-if="revisionDetailPending" class="space-y-3"><USkeleton class="h-28 w-full" /><USkeleton class="h-48 w-full" /></div>
                  <template v-else-if="revisionDetail">
                    <div class="flex flex-wrap gap-2">
                      <UButton v-if="selectedRevisionCanRestore" icon="i-lucide-rotate-ccw" size="sm" @click="openRevisionAction('restore')">{{ t('admin.forum.content.history.restore') }}</UButton>
                      <UButton v-if="selectedRevisionCanRedact" icon="i-lucide-shield-x" color="error" variant="outline" size="sm" @click="openRevisionAction('redact')">{{ t('admin.forum.content.history.redact') }}</UButton>
                    </div>
                    <section class="rounded-lg border border-slate-200 p-4 dark:border-zinc-800">
                      <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.forum.content.history.previewTitle') }}</h4>
                      <div v-if="revisionDetail.preview" class="sf-prose mt-3 max-w-none overflow-wrap-anywhere" v-highlight v-mention v-html="sanitizeHtml(revisionDetail.preview.htmlContent)" />
                      <p v-else class="mt-3 whitespace-pre-wrap break-words text-sm text-slate-700 dark:text-zinc-200">{{ revisionDetail.rawContent }}</p>
                    </section>
                    <SFAdminForumRevisionDiff v-if="currentRevisionDetail" :current-revision="currentRevisionDetail" :revision="revisionDetail" />
                  </template>
                </div>
              </section>
            </template>
          </main>
        </div>
      </template>
    </USlideover>

    <UModal :open="Boolean(revisionAction)" @update:open="value => !value && closeRevisionAction()">
      <template #content>
        <div v-if="revisionAction" class="space-y-4 p-6">
          <div class="flex items-start gap-3">
            <UIcon :name="revisionAction === 'redact' ? 'i-lucide-shield-alert' : 'i-lucide-rotate-ccw'" class="mt-0.5 size-5" :class="revisionAction === 'redact' ? 'text-red-600' : 'text-[var(--sf-accent)]'" />
            <div><h3 class="text-base font-semibold text-slate-900 dark:text-zinc-100">{{ t(`admin.forum.content.history.${revisionAction}Title`) }}</h3><p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">{{ t(`admin.forum.content.history.${revisionAction}Description`) }}</p></div>
          </div>
          <UAlert v-if="revisionAction === 'redact'" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="t('admin.forum.content.history.redactionWarning')" />
          <UAlert v-if="revisionActionError" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="revisionActionError" />
          <UFormField :label="t('admin.forum.content.history.reason')" :error="!revisionActionReason.trim() && revisionActionError ? revisionActionError : undefined"><UTextarea v-model="revisionActionReason" :rows="3" :disabled="revisionActionPending" :placeholder="t('admin.forum.content.history.reasonPlaceholder')" class="w-full" /></UFormField>
          <UFormField v-if="revisionAction === 'redact'" :label="t('admin.forum.content.history.redactionConfirmation')"><UInput v-model="redactionConfirmation" :disabled="revisionActionPending" placeholder="REDACT" autocomplete="off" class="w-full font-mono" /></UFormField>
          <div class="flex justify-end gap-2"><UButton color="neutral" variant="ghost" :disabled="revisionActionPending" @click="closeRevisionAction">{{ t('admin.common.cancel') }}</UButton><UButton :color="revisionAction === 'redact' ? 'error' : 'primary'" :loading="revisionActionPending" @click="submitRevisionAction">{{ t(`admin.forum.content.history.${revisionAction}`) }}</UButton></div>
        </div>
      </template>
    </UModal>
  </div>
</template>
