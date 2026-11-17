<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminAddedPages, type AdminAddedPage } from '~/composables/admin/useAdminAddedPages'

/**
 * 主题新增页面（后台只读）。
 *
 * `/admin/pages` 只遍历 `coreCatalog`，**不含**主题通过 `action: add` 贡献的页面；
 * `Pages.Registry.AddedPages()` 只经 `GET /admin/pages/added` 暴露。所以后台要看
 * 「主题到底加了哪些路由」只能读这里。
 *
 * 只读是刻意的：这些条目由主题包声明，改写入口在主题包里；后台给编辑按钮只会造出
 * 一个改不动的承诺。
 */
definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionAddedPages'
})

const { t, te } = useI18n()
const adminPage = useAdminPage('/extensions/added-pages')
const addedPagesApi = useAdminAddedPages()

const rows = ref<AdminAddedPage[]>([])
const pending = ref(true)
const error = ref('')

const addCount = computed(() => rows.value.filter(row => row.action === 'add').length)
const replaceCount = computed(() => rows.value.filter(row => row.action === 'replace').length)

function actionLabel(action: string) {
  const key = `admin.extensions.addedPages.action.${action}`
  return te(key) ? t(key) : action
}

function actionColor(action: string) {
  return action === 'add' ? 'primary' as const : 'neutral' as const
}

function destination(row: AdminAddedPage) {
  if (row.action === 'add') {
    return row.path || row.id
  }
  return row.target || row.id
}

function viewLabel(view?: string) {
  if (!view) return '—'
  const key = `admin.extensions.addedPages.view.${view}`
  return te(key) ? t(key) : view
}

async function load() {
  pending.value = true
  error.value = ''
  try {
    rows.value = await addedPagesApi.list()
  } catch (e) {
    error.value = apiErrorMessage(e) || t('admin.extensions.addedPages.loadFailed')
    rows.value = []
  } finally {
    pending.value = false
  }
}

useSeoMeta({
  title: t('admin.extensions.addedPages.metaTitle')
})

await load()
</script>

<template>
  <div data-testid="admin-extension-added-pages" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.addedPages.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.addedPages.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-file-plus-2" class="size-4" />
          <span class="truncate">
            {{ t('admin.extensions.addedPages.count', { count: rows.length, add: addCount, replace: replaceCount }) }}
          </span>
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

    <div v-if="pending && rows.length === 0" class="space-y-3" aria-busy="true" data-testid="added-pages-loading">
      <SFSkeleton v-for="index in 3" :key="index" class="h-10 w-full" />
    </div>

    <SFEmptyState
      v-else-if="rows.length === 0 && !error"
      icon="i-lucide-file-plus-2"
      :title="t('admin.extensions.addedPages.emptyTitle')"
      :description="t('admin.extensions.addedPages.empty')"
    />

    <div v-else-if="rows.length > 0" class="overflow-x-auto rounded-xl border border-slate-200 dark:border-zinc-800">
      <table class="min-w-full text-left text-sm">
        <thead class="bg-slate-50 text-slate-600 dark:bg-zinc-900 dark:text-zinc-300">
          <tr>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colId') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colAction') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colPath') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colExtension') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colVersion') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colView') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.extensions.addedPages.colAccess') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="row.id"
            class="border-t border-slate-100 dark:border-zinc-800"
            data-testid="added-page-row"
          >
            <td class="px-3 py-2 font-mono text-xs">{{ row.id }}</td>
            <td class="px-3 py-2">
              <UBadge :color="actionColor(row.action)" variant="soft">
                {{ actionLabel(row.action) }}
              </UBadge>
            </td>
            <td class="px-3 py-2 font-mono text-xs">{{ destination(row) }}</td>
            <td class="px-3 py-2 font-mono text-xs text-slate-600 dark:text-zinc-400">{{ row.extensionId }}</td>
            <td class="px-3 py-2 font-mono text-xs text-slate-600 dark:text-zinc-400">{{ row.version || '—' }}</td>
            <td class="px-3 py-2 text-xs">{{ viewLabel(row.view) }}</td>
            <td class="px-3 py-2 text-xs">{{ row.access || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
