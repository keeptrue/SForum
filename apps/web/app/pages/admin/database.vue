<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { announcePolite } from '~/utils/a11y/liveRegion'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import {
  buildDatabaseRowsQuery,
  databaseTableKey,
  databaseValueText,
  isDatabaseCellRevealable,
  replaceDatabaseCellValue,
  type DatabaseCell,
  type DatabaseColumn,
  type DatabaseReveal,
  type DatabaseRow,
  type DatabaseRows,
  type DatabaseTableDetail,
  type DatabaseTableSummary
} from '~/utils/admin/adminDatabase'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminDatabase'
})

type FilterOperator = 'eq' | 'contains' | 'is_null' | 'not_null'
type SortDirection = 'asc' | 'desc'

const { t } = useI18n()
const toast = useToast()
const { request, apiBaseUrl, apiHeaders } = useApiClient()
const adminPage = useAdminPage('/database')

const tables = ref<DatabaseTableSummary[]>([])
const selectedTable = ref<DatabaseTableSummary | null>(null)
const detail = ref<DatabaseTableDetail | null>(null)
const rows = ref<DatabaseRows | null>(null)
const tableSearch = ref('')
const loadingTables = ref(false)
const loadingDetail = ref(false)
const loadingRows = ref(false)
const exporting = ref(false)
const errorMessage = ref('')
const revealing = ref<Record<string, boolean>>({})

const rowsQuery = reactive({
  page: 1,
  perPage: 50,
  sort: '',
  direction: 'asc' as SortDirection,
  filterColumn: '',
  filterOperator: 'contains' as FilterOperator,
  filterValue: ''
})

const filterOperators = computed(() => [
  { value: 'contains' as FilterOperator, label: t('admin.database.operators.contains') },
  { value: 'eq' as FilterOperator, label: t('admin.database.operators.eq') },
  { value: 'is_null' as FilterOperator, label: t('admin.database.operators.is_null') },
  { value: 'not_null' as FilterOperator, label: t('admin.database.operators.not_null') }
])

const filteredTables = computed(() => {
  const query = tableSearch.value.trim().toLowerCase()
  if (!query) {
    return tables.value
  }
  return tables.value.filter((table) => {
    return table.schema.toLowerCase().includes(query) ||
      table.name.toLowerCase().includes(query) ||
      databaseTableKey(table).toLowerCase().includes(query)
  })
})

const rowColumns = computed(() => rows.value?.columns || detail.value?.columns || [])
const filterNeedsValue = computed(() => rowsQuery.filterOperator !== 'is_null' && rowsQuery.filterOperator !== 'not_null')
const currentTablePath = computed(() => {
  if (!selectedTable.value) {
    return ''
  }
  return `${encodeURIComponent(selectedTable.value.schema)}/${encodeURIComponent(selectedTable.value.name)}`
})

onMounted(() => {
  void loadTables()
})

useSeoMeta({
  title: t('admin.database.metaTitle')
})

async function loadTables() {
  loadingTables.value = true
  errorMessage.value = ''
  try {
    tables.value = await request<DatabaseTableSummary[]>('/admin/database/tables')
    const firstTable = tables.value[0]
    if (!selectedTable.value && firstTable) {
      await selectTable(firstTable)
    } else if (selectedTable.value) {
      const refreshed = tables.value.find(table => databaseTableKey(table) === databaseTableKey(selectedTable.value as DatabaseTableSummary))
      if (refreshed) {
        selectedTable.value = refreshed
        await loadDetail()
      }
    }
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.database.loadFailed')
  } finally {
    loadingTables.value = false
  }
}

async function selectTable(table: DatabaseTableSummary) {
  selectedTable.value = table
  resetQuery()
  await loadDetail()
}

async function loadDetail() {
  if (!selectedTable.value) {
    return
  }
  loadingDetail.value = true
  errorMessage.value = ''
  try {
    detail.value = await request<DatabaseTableDetail>(`/admin/database/tables/${currentTablePath.value}`)
    rowsQuery.sort = detail.value.primaryKey[0] || detail.value.columns[0]?.name || ''
    await loadRows()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.database.detailLoadFailed')
  } finally {
    loadingDetail.value = false
  }
}

async function loadRows() {
  if (!selectedTable.value) {
    return
  }
  loadingRows.value = true
  errorMessage.value = ''
  try {
    const query = buildDatabaseRowsQuery(rowsQuery)
    rows.value = await request<DatabaseRows>(`/admin/database/tables/${currentTablePath.value}/rows?${query}`)
    // UX2-17：表数据是异步面，加载结束后播报当前页行数。
    announcePolite(t('admin.database.rowsLoaded', { count: rows.value.rows.length }))
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.database.rowsLoadFailed')
  } finally {
    loadingRows.value = false
  }
}

function resetQuery() {
  rowsQuery.page = 1
  rowsQuery.perPage = 50
  rowsQuery.sort = ''
  rowsQuery.direction = 'asc'
  rowsQuery.filterColumn = ''
  rowsQuery.filterOperator = 'contains'
  rowsQuery.filterValue = ''
  rows.value = null
  revealing.value = {}
}

function resetFilter() {
  rowsQuery.page = 1
  rowsQuery.filterColumn = ''
  rowsQuery.filterOperator = 'contains'
  rowsQuery.filterValue = ''
  void loadRows()
}

function applyFilter() {
  rowsQuery.page = 1
  void loadRows()
}

function sortBy(column: DatabaseColumn) {
  if (rowsQuery.sort === column.name) {
    rowsQuery.direction = rowsQuery.direction === 'asc' ? 'desc' : 'asc'
  } else {
    rowsQuery.sort = column.name
    rowsQuery.direction = 'asc'
  }
  rowsQuery.page = 1
  void loadRows()
}

function goPreviousPage() {
  if (rowsQuery.page <= 1) {
    return
  }
  rowsQuery.page -= 1
  void loadRows()
}

function goNextPage() {
  if (!rows.value?.hasNext) {
    return
  }
  rowsQuery.page += 1
  void loadRows()
}

async function revealCell(row: DatabaseRow, column: DatabaseColumn) {
  if (!selectedTable.value || !row.rowKey) {
    return
  }
  const key = revealKey(row, column.name)
  revealing.value[key] = true
  try {
    const params = new URLSearchParams({ rowKey: row.rowKey, column: column.name })
    const result = await request<DatabaseReveal>(`/admin/database/tables/${currentTablePath.value}/rows/reveal?${params.toString()}`)
    rows.value = rows.value
      ? {
          ...rows.value,
          rows: rows.value.rows.map(item => item === row ? replaceDatabaseCellValue(item, column.name, result.value) : item)
        }
      : rows.value
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.database.revealFailed') })
  } finally {
    revealing.value[key] = false
  }
}

async function exportCSV() {
  if (!selectedTable.value) {
    return
  }
  exporting.value = true
  try {
    const query = buildDatabaseRowsQuery({
      sort: rowsQuery.sort,
      direction: rowsQuery.direction,
      filterColumn: rowsQuery.filterColumn,
      filterOperator: rowsQuery.filterOperator,
      filterValue: rowsQuery.filterValue
    })
    const suffix = query ? `?${query}` : ''
    const response = await fetch(`${apiBaseUrl}/admin/database/tables/${currentTablePath.value}/export.csv${suffix}`, {
      credentials: 'include',
      headers: apiHeaders()
    })
    if (!response.ok) {
      throw new Error(response.statusText)
    }
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${selectedTable.value.schema}.${selectedTable.value.name}.csv`
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.database.exportFailed') })
  } finally {
    exporting.value = false
  }
}

function cell(row: DatabaseRow, column: string): DatabaseCell | undefined {
  return row.values[column]
}

function cellText(value: unknown) {
  const text = databaseValueText(value)
  return text === 'NULL' ? t('admin.database.nullValue') : text
}

function revealKey(row: DatabaseRow, column: string) {
  return `${row.rowKey || 'row'}:${column}`
}

function sortIcon(column: DatabaseColumn) {
  if (rowsQuery.sort !== column.name) {
    return 'i-lucide-arrow-up-down'
  }
  return rowsQuery.direction === 'asc' ? 'i-lucide-arrow-up' : 'i-lucide-arrow-down'
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    return '0 B'
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = value
  let unitIndex = 0
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex += 1
  }
  return `${size.toFixed(size >= 10 || unitIndex === 0 ? 0 : 1)} ${units[unitIndex]}`
}

function tableKindLabel(kind: string) {
  const key = `admin.database.kinds.${kind}`
  const label = t(key)
  return label === key ? kind : label
}
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.database.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.database.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex flex-wrap items-center gap-2">
        <UInput
          v-model="tableSearch"
          icon="i-lucide-search"
          :placeholder="t('admin.database.searchPlaceholder')"
          class="w-72 max-w-full"
        />
        <UBadge color="neutral" variant="soft" class="border border-slate-200 font-medium dark:border-zinc-800">
          {{ t('admin.database.tableCount', { count: filteredTables.length }) }}
        </UBadge>
      </div>
    </template>
    <template #right>
      <div class="flex items-center gap-2">
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="loadingTables"
          @click="loadTables"
        >
          {{ t('admin.database.refresh') }}
        </UButton>
        <UButton
          color="primary"
          variant="solid"
          leading-icon="i-lucide-download"
          :loading="exporting"
          :disabled="!selectedTable"
          @click="exportCSV"
        >
          {{ t('admin.database.export') }}
        </UButton>
      </div>
    </template>
  </UDashboardToolbar>

  <UAlert
    v-if="errorMessage"
    color="error"
    variant="soft"
    icon="i-lucide-triangle-alert"
    :title="errorMessage"
    class="mb-4"
  />

  <div class="grid gap-4 xl:grid-cols-[320px_minmax(0,1fr)]">
    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <h3 class="text-sm font-semibold text-slate-950 dark:text-zinc-50">
            {{ t('admin.database.tables') }}
          </h3>
          <UIcon v-if="loadingTables" name="i-lucide-loader-circle" class="size-4 animate-spin text-slate-400" />
        </div>
      </template>

      <div v-if="filteredTables.length === 0" class="rounded-md border border-dashed border-slate-200 px-3 py-8 text-center text-sm text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
        {{ t('admin.database.emptyTables') }}
      </div>
      <div v-else class="max-h-[calc(100vh-18rem)] space-y-1 overflow-y-auto pr-1">
        <button
          v-for="table in filteredTables"
          :key="databaseTableKey(table)"
          type="button"
          class="w-full rounded-md border px-3 py-2 text-left transition"
          :class="selectedTable && databaseTableKey(selectedTable) === databaseTableKey(table)
            ? 'border-[var(--sf-accent-soft-border)] bg-[var(--sf-accent-soft)] text-slate-950 dark:border-[var(--sf-accent-dark)] dark:bg-zinc-800 dark:text-zinc-50'
            : 'border-transparent text-slate-700 hover:border-slate-200 hover:bg-slate-50 dark:text-zinc-300 dark:hover:border-zinc-800 dark:hover:bg-zinc-950'"
          @click="selectTable(table)"
        >
          <span class="flex items-center justify-between gap-2">
            <span class="min-w-0">
              <span class="block truncate text-sm font-semibold">{{ table.name }}</span>
              <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ table.schema }} · {{ tableKindLabel(table.kind) }}</span>
            </span>
            <span class="shrink-0 text-xs text-slate-500 dark:text-zinc-400">{{ formatBytes(table.sizeBytes) }}</span>
          </span>
        </button>
      </div>
    </UCard>

    <div class="min-w-0 space-y-4">
      <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
        <template #header>
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <h3 class="truncate text-sm font-semibold text-slate-950 dark:text-zinc-50">
                {{ selectedTable ? databaseTableKey(selectedTable) : t('admin.database.table') }}
              </h3>
              <p class="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                {{ selectedTable?.comment || t('admin.database.noComment') }}
              </p>
            </div>
            <div v-if="selectedTable" class="flex flex-wrap gap-1.5">
              <UBadge color="neutral" variant="soft">{{ tableKindLabel(selectedTable.kind) }}</UBadge>
              <UBadge color="neutral" variant="outline">{{ formatBytes(selectedTable.sizeBytes) }}</UBadge>
              <UBadge color="neutral" variant="outline">{{ selectedTable.estimatedRows.toLocaleString() }}</UBadge>
            </div>
          </div>
        </template>

        <div v-if="loadingDetail" class="flex items-center gap-2 text-sm text-slate-500 dark:text-zinc-400">
          <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
          {{ t('admin.database.loading') }}
        </div>
        <div v-else-if="detail" class="grid gap-4 2xl:grid-cols-[minmax(0,1fr)_minmax(280px,360px)]">
          <div class="min-w-0">
            <div class="mb-2 flex items-center gap-2">
              <UIcon name="i-lucide-columns-3" class="size-4 text-[var(--sf-accent)]" />
              <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.database.columns') }}</h4>
            </div>
            <div class="overflow-x-auto rounded-md border border-slate-200 dark:border-zinc-800">
              <table class="min-w-full divide-y divide-slate-200 text-sm dark:divide-zinc-800">
                <thead class="bg-slate-50 text-xs uppercase text-slate-500 dark:bg-zinc-950 dark:text-zinc-400">
                  <tr>
                    <th class="px-3 py-2 text-left">{{ t('admin.database.table') }}</th>
                    <th class="px-3 py-2 text-left">{{ t('admin.database.kind') }}</th>
                    <th class="px-3 py-2 text-left">{{ t('admin.database.nullable') }}</th>
                    <th class="px-3 py-2 text-left">{{ t('admin.database.defaultValue') }}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-slate-100 dark:divide-zinc-800">
                  <tr v-for="column in detail.columns" :key="column.name">
                    <td class="px-3 py-2">
                      <div class="flex flex-wrap items-center gap-1.5">
                        <code class="rounded bg-slate-50 px-1.5 py-0.5 text-xs font-semibold dark:bg-zinc-800">{{ column.name }}</code>
                        <UBadge v-if="column.isPrimaryKey" color="primary" variant="soft">{{ t('admin.database.primaryKey') }}</UBadge>
                        <UBadge v-if="column.isSensitive" color="warning" variant="soft">{{ t('admin.database.sensitive') }}</UBadge>
                      </div>
                    </td>
                    <td class="px-3 py-2 text-slate-600 dark:text-zinc-300">{{ column.dataType }}</td>
                    <td class="px-3 py-2 text-slate-600 dark:text-zinc-300">{{ column.nullable ? t('admin.database.yes') : t('admin.database.no') }}</td>
                    <td class="px-3 py-2 text-slate-500 dark:text-zinc-400">{{ column.defaultValue || t('admin.database.noDefault') }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="space-y-4">
            <div>
              <div class="mb-2 flex items-center gap-2">
                <UIcon name="i-lucide-key-round" class="size-4 text-[var(--sf-accent)]" />
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.database.indexes') }}</h4>
              </div>
              <div class="space-y-2">
                <div v-for="index in detail.indexes" :key="index.name" class="rounded-md border border-slate-200 px-3 py-2 dark:border-zinc-800">
                  <div class="flex flex-wrap items-center gap-1.5">
                    <code class="text-xs font-semibold">{{ index.name }}</code>
                    <UBadge v-if="index.primary" color="primary" variant="soft">{{ t('admin.database.primaryKey') }}</UBadge>
                    <UBadge v-else-if="index.unique" color="info" variant="soft">UNIQUE</UBadge>
                  </div>
                  <p class="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{{ index.definition }}</p>
                </div>
              </div>
            </div>
            <div>
              <div class="mb-2 flex items-center gap-2">
                <UIcon name="i-lucide-list-checks" class="size-4 text-[var(--sf-accent)]" />
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.database.constraints') }}</h4>
              </div>
              <div class="space-y-2">
                <div v-for="constraint in detail.constraints" :key="constraint.name" class="rounded-md border border-slate-200 px-3 py-2 dark:border-zinc-800">
                  <div class="flex items-center gap-2">
                    <code class="text-xs font-semibold">{{ constraint.name }}</code>
                    <UBadge color="neutral" variant="soft">{{ constraint.type }}</UBadge>
                  </div>
                  <p class="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{{ constraint.definition }}</p>
                </div>
              </div>
            </div>
          </div>
        </div>
      </UCard>

      <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
        <template #header>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex items-center gap-2">
              <UIcon name="i-lucide-table-2" class="size-4 text-[var(--sf-accent)]" />
              <h3 class="text-sm font-semibold text-slate-950 dark:text-zinc-50">{{ t('admin.database.rows') }}</h3>
            </div>
            <div class="flex items-center gap-2 text-xs text-slate-500 dark:text-zinc-400">
              <span>{{ t('admin.database.page', { page: rowsQuery.page }) }}</span>
              <UButton color="neutral" variant="outline" size="xs" leading-icon="i-lucide-chevron-left" :disabled="rowsQuery.page <= 1 || loadingRows" @click="goPreviousPage">
                {{ t('admin.database.previous') }}
              </UButton>
              <UButton color="neutral" variant="outline" size="xs" trailing-icon="i-lucide-chevron-right" :disabled="!rows?.hasNext || loadingRows" @click="goNextPage">
                {{ t('admin.database.next') }}
              </UButton>
            </div>
          </div>
        </template>

        <div class="mb-4 flex flex-wrap items-center gap-2">
          <select v-model="rowsQuery.filterColumn" :aria-label="t('admin.database.filterColumnAria')" class="h-9 rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition-colors hover:border-slate-300 focus:border-[var(--sf-accent)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-200">
            <option value="">{{ t('admin.database.allColumns') }}</option>
            <option v-for="column in rowColumns" :key="column.name" :value="column.name">{{ column.name }}</option>
          </select>
          <select v-model="rowsQuery.filterOperator" :aria-label="t('admin.database.filterOperatorAria')" class="h-9 rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition-colors hover:border-slate-300 focus:border-[var(--sf-accent)] dark:border-zinc-700 dark:bg-zinc-950 dark:text-zinc-200">
            <option v-for="operator in filterOperators" :key="operator.value" :value="operator.value">{{ operator.label }}</option>
          </select>
          <UInput
            v-if="filterNeedsValue"
            v-model="rowsQuery.filterValue"
            icon="i-lucide-search"
            :placeholder="t('admin.database.filterValue')"
            class="w-64 max-w-full"
            @keyup.enter="applyFilter"
          />
          <UButton color="primary" variant="solid" leading-icon="i-lucide-filter" :loading="loadingRows" @click="applyFilter">
            {{ t('admin.database.applyFilter') }}
          </UButton>
          <UButton color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" :disabled="loadingRows" @click="resetFilter">
            {{ t('admin.database.resetFilter') }}
          </UButton>
        </div>

        <div v-if="loadingRows" class="flex items-center gap-2 text-sm text-slate-500 dark:text-zinc-400">
          <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
          {{ t('admin.database.loading') }}
        </div>
        <div v-else-if="!rows || rows.rows.length === 0" class="rounded-md border border-dashed border-slate-200 px-3 py-8 text-center text-sm text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
          {{ t('admin.database.emptyRows') }}
        </div>
        <div v-else class="overflow-auto rounded-md border border-slate-200 dark:border-zinc-800">
          <table class="min-w-full divide-y divide-slate-200 text-sm dark:divide-zinc-800">
            <thead class="bg-slate-50 text-xs uppercase text-slate-500 dark:bg-zinc-950 dark:text-zinc-400">
              <tr>
                <th v-for="column in rowColumns" :key="column.name" class="whitespace-nowrap px-3 py-2 text-left">
                  <button type="button" class="inline-flex items-center gap-1 font-semibold" @click="sortBy(column)">
                    {{ column.name }}
                    <UIcon :name="sortIcon(column)" class="size-3.5" />
                  </button>
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-slate-100 dark:divide-zinc-800">
              <tr v-for="(row, rowIndex) in rows.rows" :key="row.rowKey || rowIndex" class="align-top">
                <td v-for="column in rowColumns" :key="column.name" class="max-w-[24rem] px-3 py-2">
                  <div class="flex min-w-40 items-start gap-2">
                    <code class="min-w-0 whitespace-pre-wrap break-words rounded bg-slate-50 px-1.5 py-0.5 text-xs text-slate-700 dark:bg-zinc-800 dark:text-zinc-200">
                      {{ cellText(cell(row, column.name)?.value) }}
                    </code>
                    <UButton
                      v-if="isDatabaseCellRevealable(cell(row, column.name)) && row.rowKey"
                      color="neutral"
                      variant="ghost"
                      size="xs"
                      icon="i-lucide-eye"
                      :aria-label="t('admin.database.showValue')"
                      :loading="revealing[revealKey(row, column.name)]"
                      @click="revealCell(row, column)"
                    />
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </UCard>
    </div>
  </div>
</template>
