<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { usePermissions } from '~/composables/identity/usePermissions'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminExtensionMigrations } from '~/composables/admin/useAdminExtensionMigrations'
import {
  migrationFailureCount,
  migrationLedgerChanged,
  migrationStatusLabelKey,
  shortMigrationChecksum,
  type ExtensionMigrationRecord
} from '~/utils/admin/extensions/migrations'
import { extensionLocalizedDisplay, type AdminExtension } from '~/utils/admin/adminExtensions'

/**
 * 插件迁移账本（后台）。
 *
 * 这是 `manifest.migrations` 的唯一读写面：`GET .../migrations` 读账本，
 * `POST .../migrations/apply` 幂等地把声明的路径记进账本（带文件校验和）。
 * v1 宿主只记录校验和、不执行插件 SQL —— 所以这里的状态文案是「已记录」而不是「已执行」，
 * 别把 `recorded` 说成跑过迁移。
 *
 * 写权限与插件页一致（`extension.plugin.manage`）；后端还会对「非内置且带可执行后端」
 * 的插件要求 super_admin，并在 Safe Mode 下拒绝 —— 这些由服务端裁决，页面只负责把
 * 服务端给的原因原样显示出来。
 */
definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionMigrations'
})

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const { request } = useApiClient()
const adminPage = useAdminPage('/extensions/migrations')
const migrationsApi = useAdminExtensionMigrations()
const { can } = usePermissions()
const { user } = useAuthSession()
const { format: formatDateTime } = useSiteDateTime()

const canApply = computed(() => can('extension.plugin.manage'))

const extensions = ref<AdminExtension[]>([])
const extensionsPending = ref(true)
const extensionsError = ref('')

const selectedId = ref(typeof route.query.extension === 'string' ? route.query.extension : '')
const rows = ref<ExtensionMigrationRecord[]>([])
const pending = ref(false)
const error = ref('')
const applyBusy = ref(false)
const confirmOpen = ref(false)

const selectedExtension = computed(() => extensions.value.find(item => item.id === selectedId.value) || null)
const declaredMigrations = computed(() => {
  const declared = selectedExtension.value?.manifest?.migrations
  return Array.isArray(declared) ? declared.filter(item => Boolean(item?.path)) : []
})
const failures = computed(() => migrationFailureCount(rows.value))
const extensionOptions = computed(() => extensions.value.map(item => ({
  label: `${extensionLocalizedDisplay(item).name || item.name} · ${item.id}`,
  value: item.id
})))

function statusLabel(status: string) {
  const key = migrationStatusLabelKey(status)
  return key && te(key) ? t(key) : status
}

function statusColor(status: string) {
  if (status === 'failed') return 'error' as const
  if (status === 'applied') return 'success' as const
  return 'neutral' as const
}

function recordedAt(row: ExtensionMigrationRecord) {
  return row.appliedAt ? formatDateTime(row.appliedAt) : '—'
}

async function loadExtensions() {
  extensionsPending.value = true
  extensionsError.value = ''
  try {
    extensions.value = await request<AdminExtension[]>('/admin/extensions')
    const stillExists = extensions.value.some(item => item.id === selectedId.value)
    if (!stillExists) {
      selectedId.value = extensions.value.find(item => item.type === 'plugin')?.id || extensions.value[0]?.id || ''
    }
  } catch (e) {
    extensionsError.value = apiErrorMessage(e) || t('admin.extensions.migrations.loadExtensionsFailed')
    extensions.value = []
  } finally {
    extensionsPending.value = false
  }
}

async function loadLedger() {
  const id = selectedId.value
  rows.value = []
  error.value = ''
  if (!id) {
    return
  }
  pending.value = true
  try {
    rows.value = await migrationsApi.list(id)
  } catch (e) {
    error.value = apiErrorMessage(e) || t('admin.extensions.migrations.loadFailed')
  } finally {
    pending.value = false
  }
}

async function selectExtension(value: unknown) {
  const id = typeof value === 'string' ? value : ''
  selectedId.value = id
  await router.replace({ query: id ? { ...route.query, extension: id } : { ...route.query, extension: undefined } })
  await loadLedger()
}

function requestApply() {
  if (!selectedExtension.value || !canApply.value) {
    return
  }
  confirmOpen.value = true
}

async function confirmApply() {
  const extension = selectedExtension.value
  if (!extension) {
    return
  }
  confirmOpen.value = false
  applyBusy.value = true
  const before = [...rows.value]
  try {
    const after = await migrationsApi.apply(extension.id)
    rows.value = Array.isArray(after) ? after : []
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: migrationLedgerChanged(before, rows.value)
        ? t('admin.extensions.migrations.applyRecorded')
        : t('admin.extensions.migrations.applyUnchanged'),
      duration: 5000
    })
  } catch (e) {
    // 失败必须显示服务端原因：Safe Mode、super_admin 要求、文件缺失都靠它区分。
    toast.add({
      color: 'error',
      duration: 0,
      icon: 'i-lucide-alert-circle',
      title: apiErrorMessage(e) || t('admin.extensions.migrations.applyFailed')
    })
  } finally {
    applyBusy.value = false
  }
}

useSeoMeta({
  title: t('admin.extensions.migrations.metaTitle')
})

await loadExtensions()
await loadLedger()
</script>

<template>
  <div data-testid="admin-extension-migrations" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.migrations.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.migrations.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 flex-wrap items-center gap-3 text-sm">
          <USelect
            :model-value="selectedId"
            :items="extensionOptions"
            :placeholder="t('admin.extensions.migrations.selectPlaceholder')"
            :loading="extensionsPending"
            class="min-w-64"
            data-testid="migration-extension-select"
            @update:model-value="selectExtension"
          />
          <span class="truncate">
            {{ t('admin.extensions.migrations.count', { count: rows.length, declared: declaredMigrations.length }) }}
          </span>
        </div>
      </template>
      <template #right>
        <div class="flex items-center gap-2">
          <UButton
            icon="i-lucide-rotate-cw"
            color="neutral"
            variant="subtle"
            :loading="pending"
            @click="loadLedger"
          >
            {{ t('admin.extensions.refresh') }}
          </UButton>
          <UButton
            v-if="selectedExtension"
            icon="i-lucide-database-zap"
            color="primary"
            variant="soft"
            :disabled="!canApply"
            :loading="applyBusy"
            :title="canApply ? undefined : t('admin.extensions.migrations.applyNoPermission')"
            data-testid="migration-apply"
            @click="requestApply"
          >
            {{ t('admin.extensions.migrations.apply') }}
          </UButton>
        </div>
      </template>
    </UDashboardToolbar>

    <SFAlert
      v-if="extensionsError"
      variant="danger"
      :title="extensionsError"
      class="mb-4"
    />

    <SFAlert
      v-if="failures > 0"
      variant="warning"
      :title="t('admin.extensions.migrations.failedWarning', { count: failures })"
      class="mb-4"
    />

    <SFAlert
      v-if="error"
      variant="danger"
      :title="error"
      class="mb-4"
    />

    <SFEmptyState
      v-if="!selectedExtension && !extensionsPending && !extensionsError"
      icon="i-lucide-package"
      :title="t('admin.extensions.migrations.noExtensionsTitle')"
      :description="t('admin.extensions.migrations.noExtensions')"
    />

    <template v-else-if="selectedExtension">
      <p
        v-if="declaredMigrations.length === 0"
        class="mb-4 rounded-lg border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300"
        data-testid="migration-no-declarations"
      >
        {{ t('admin.extensions.migrations.noDeclarations') }}
      </p>

      <div v-if="pending && rows.length === 0" class="space-y-3" aria-busy="true" data-testid="migration-loading">
        <SFSkeleton v-for="index in 3" :key="index" class="h-10 w-full" />
      </div>

      <SFEmptyState
        v-else-if="rows.length === 0 && !error"
        icon="i-lucide-scroll-text"
        :title="t('admin.extensions.migrations.emptyTitle')"
        :description="t('admin.extensions.migrations.empty')"
      />

      <div v-else-if="rows.length > 0" class="overflow-x-auto rounded-xl border border-slate-200 dark:border-zinc-800">
        <table class="min-w-full text-left text-sm">
          <thead class="bg-slate-50 text-slate-600 dark:bg-zinc-900 dark:text-zinc-300">
            <tr>
              <th class="px-3 py-2 font-medium">{{ t('admin.extensions.migrations.colPath') }}</th>
              <th class="px-3 py-2 font-medium">{{ t('admin.extensions.migrations.colStatus') }}</th>
              <th class="px-3 py-2 font-medium">{{ t('admin.extensions.migrations.colChecksum') }}</th>
              <th class="px-3 py-2 font-medium">{{ t('admin.extensions.migrations.colRecordedAt') }}</th>
              <th class="px-3 py-2 font-medium">{{ t('admin.extensions.migrations.colMessage') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="`${row.path}:${row.checksum}`"
              class="border-t border-slate-100 dark:border-zinc-800"
              data-testid="migration-row"
            >
              <td class="px-3 py-2 font-mono text-xs">{{ row.path }}</td>
              <td class="px-3 py-2">
                <UBadge :color="statusColor(row.status)" variant="soft">
                  {{ statusLabel(row.status) }}
                </UBadge>
              </td>
              <td
                class="px-3 py-2 font-mono text-xs text-slate-600 dark:text-zinc-400"
                :title="row.checksum || undefined"
              >
                {{ shortMigrationChecksum(row.checksum) || '—' }}
              </td>
              <td class="px-3 py-2 text-xs text-slate-600 dark:text-zinc-400">{{ recordedAt(row) }}</td>
              <td class="px-3 py-2 text-xs text-slate-600 dark:text-zinc-400">{{ row.message || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <UModal v-model:open="confirmOpen">
      <template #content>
        <div class="p-5 sm:p-6">
          <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.migrations.confirmTitle') }}
          </h2>
          <p class="mt-2 text-sm text-slate-600 dark:text-zinc-300">
            {{ t('admin.extensions.migrations.confirmBody', {
              extension: selectedExtension ? extensionLocalizedDisplay(selectedExtension).name || selectedExtension.id : '',
              count: declaredMigrations.length
            }) }}
          </p>
          <p class="mt-2 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.extensions.migrations.confirmHint') }}
          </p>
          <div class="mt-5 flex justify-end gap-2">
            <UButton color="neutral" variant="subtle" @click="confirmOpen = false">
              {{ t('admin.extensions.migrations.cancel') }}
            </UButton>
            <UButton color="primary" :loading="applyBusy" data-testid="migration-apply-confirm" @click="confirmApply">
              {{ t('admin.extensions.migrations.confirmAction') }}
            </UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>
