<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { paginateItems } from '~/utils/admin/adminExtensions'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionPages'
})

const { t, te } = useI18n()
const toast = useToast()
const adminPage = useAdminPage('/extensions/pages')
const { request } = useApiClient()
const { user } = useAuthSession()
const isSuperAdmin = computed(() => user.value?.roleKeys?.includes('super_admin') === true)

type PageRow = {
  page: {
    id: string
    pathPattern: string
    access: string
    contractVersion: string
    replaceable: boolean
    themeable: boolean
    virtual?: boolean
  }
  provider: string
  extensionId?: string
  contributionId?: string
  /** 当前绑定或目录 contract；只读展示，不可编辑 */
  contractVersion?: string
  candidates?: Array<{
    id: string
    extensionId: string
    version?: string
    packageDigest?: string
    template?: string
    contract?: string
  }>
}

const pending = ref(true)
const error = ref('')
const rows = ref<PageRow[]>([])
const currentPage = ref(1)

// 页面注册表目录通常几十条，每页 20 比事件列表默认 8 更合适。
const PAGE_SIZE = 20
const pageInfo = computed(() => paginateItems(rows.value, currentPage.value, PAGE_SIZE))

watch(() => pageInfo.value.page, (page) => {
  currentPage.value = page
})

function accessLabel(access: string) {
  const key = `admin.extensions.pages.access.${access}`
  return te(key) ? t(key) : access
}

function providerLabel(provider: string) {
  if (provider === 'core') {
    return t('admin.extensions.pages.providerCore')
  }
  return provider
}

function pathLabel(row: PageRow) {
  if (row.page.virtual) {
    const key = `admin.extensions.pages.virtual.${row.page.id.replaceAll('.', '_')}`
    return te(key) ? t(key) : t('admin.extensions.pages.virtual.system')
  }
  return row.page.pathPattern || '—'
}

async function load() {
  pending.value = true
  error.value = ''
  try {
    rows.value = await request<PageRow[]>('/admin/pages')
    // 数据刷新后夹紧页码，避免删减后落在空页。
    currentPage.value = pageInfo.value.page
  } catch (e) {
    error.value = apiErrorMessage(e) || t('admin.extensions.pages.loadFailed')
    rows.value = []
  } finally {
    pending.value = false
  }
}

async function restoreCore(pageId: string) {
  try {
    await request(`/admin/pages/${encodeURIComponent(pageId)}/restore-core`, { method: 'POST' })
    toast.add({
      color: 'primary',
      icon: 'i-lucide-rotate-ccw',
      title: t('admin.extensions.pages.restored'),
      duration: 5000
    })
    await load()
  } catch (e) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-alert-circle',
      title: apiErrorMessage(e) || t('admin.extensions.pages.restoreFailed')
    })
  }
}

async function approve(pageId: string, candidate: {
  id: string
  extensionId: string
  version?: string
  packageDigest?: string
  template?: string
  contract?: string
}, pageContract?: string) {
  if (!isSuperAdmin.value) {
    return
  }
  try {
    // templatePath 不由客户端决定；contract 必须与目录/贡献一致，仅只读回传。
    await request(`/admin/pages/${encodeURIComponent(pageId)}/approve`, {
      method: 'POST',
      body: {
        extensionId: candidate.extensionId,
        contributionId: candidate.id,
        version: candidate.version || '',
        packageDigest: candidate.packageDigest || '',
        contractVersion: candidate.contract || pageContract || ''
      }
    })
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: t('admin.extensions.pages.approved'),
      duration: 5000
    })
    await load()
  } catch (e) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-alert-circle',
      title: apiErrorMessage(e) || t('admin.extensions.pages.approveFailed')
    })
  }
}

useSeoMeta({
  title: t('admin.extensions.pages.metaTitle')
})

void load()
</script>

<template>
  <div data-testid="admin-extension-pages" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.pages.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.pages.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-layout-template" class="size-4" />
          <span class="truncate">{{ t('admin.extensions.pages.count', { count: rows.length }) }}</span>
        </div>
      </template>
      <template #right>
        <UButton
          icon="i-lucide-rotate-cw"
          color="neutral"
          variant="subtle"
          :loading="pending"
          @click="load"
        >
          {{ t('admin.extensions.refresh') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <SFAlert
      v-if="error"
      variant="danger"
      :title="error"
      class="mb-4"
    />

    <div class="overflow-x-auto rounded-xl border border-slate-200 dark:border-zinc-800">
      <table class="min-w-full text-left text-sm">
        <thead class="bg-slate-50 text-slate-600 dark:bg-zinc-900 dark:text-zinc-300">
          <tr>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colId') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colPath') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colAccess') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colProvider') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colContract') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.pages.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in pageInfo.items"
            :key="row.page.id"
            class="border-t border-slate-100 dark:border-zinc-800"
          >
            <td class="px-3 py-2 font-mono text-xs">{{ row.page.id }}</td>
            <td class="px-3 py-2 text-xs">
              <span
                v-if="row.page.virtual"
                class="inline-flex items-center gap-1 rounded-md border border-slate-200 bg-slate-50 px-2 py-1 font-medium text-slate-600 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300"
              >
                <UIcon name="i-lucide-file-warning" class="size-3.5" aria-hidden="true" />
                {{ pathLabel(row) }}
              </span>
              <span v-else class="font-mono">{{ pathLabel(row) }}</span>
            </td>
            <td class="px-3 py-2">{{ accessLabel(row.page.access) }}</td>
            <td class="px-3 py-2">
              <span class="font-medium">{{ providerLabel(row.provider) }}</span>
              <span
                v-if="row.candidates?.length"
                class="ml-2 text-xs text-slate-500"
              >
                {{ t('admin.extensions.pages.candidatesCount', { count: row.candidates.length }) }}
              </span>
            </td>
            <td class="px-3 py-2 font-mono text-xs text-slate-600 dark:text-zinc-400">
              {{ row.contractVersion || row.page.contractVersion || '—' }}
            </td>
            <td class="px-3 py-2">
              <div class="flex flex-wrap gap-2">
                <UButton
                  v-if="row.provider !== 'core' && row.page.replaceable"
                  size="xs"
                  color="neutral"
                  variant="soft"
                  icon="i-lucide-rotate-ccw"
                  @click="restoreCore(row.page.id)"
                >
                  {{ t('admin.extensions.pages.restoreCore') }}
                </UButton>
                <template v-if="isSuperAdmin && row.page.replaceable">
                  <UButton
                    v-for="c in row.candidates || []"
                    :key="c.id"
                    size="xs"
                    color="primary"
                    variant="soft"
                    icon="i-lucide-check"
                    @click="approve(row.page.id, c, row.page.contractVersion)"
                  >
                    {{ t('admin.extensions.pages.approveExtension', { extensionId: c.extensionId }) }}
                  </UButton>
                </template>
              </div>
            </td>
          </tr>
          <tr v-if="!pending && rows.length === 0">
            <td
              colspan="6"
              class="px-3 py-8 text-center text-slate-500"
            >
              {{ t('admin.extensions.pages.empty') }}
            </td>
          </tr>
        </tbody>
      </table>
      <div
        v-if="pageInfo.totalPages > 1"
        class="flex flex-col gap-3 border-t border-slate-100 px-4 py-4 text-xs text-slate-500 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800 dark:text-zinc-400"
      >
        <span>
          {{ t('admin.extensions.eventPageSummary', { start: pageInfo.start, end: pageInfo.end, count: pageInfo.total }) }}
        </span>
        <SFPagination v-model:page="currentPage" :total-pages="pageInfo.totalPages" />
      </div>
    </div>
  </div>
</template>
