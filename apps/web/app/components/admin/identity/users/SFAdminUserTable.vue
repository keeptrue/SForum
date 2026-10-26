<script setup lang="ts">
import type { AdminUserSummary, Role } from '~/utils/admin/adminUsers'
import type { AdminUserListSurfaces } from '~/composables/admin/identity/useAdminUserList'

const props = defineProps<{
  users: AdminUserSummary[]
  roles: Role[]
  surfaces: AdminUserListSurfaces
  pending: boolean
  total: number
  page: number
  perPage: number
  totalPages: number
  highlightUserId: number | null
  detailPending: boolean
  detailTargetId: number | null
}>()

const emit = defineEmits<{
  preview: [user: AdminUserSummary]
  manage: [user: AdminUserSummary]
  'update:page': [page: number]
}>()

const { t } = useI18n()

// 列由列表 surface 的可见列拼出来：插件列必须和核心列走同一条渲染路径。
const columns = computed(() => [
  ...(props.surfaces.hasBulkActions.value ? [{ id: 'selection', header: '' }] : []),
  {
    accessorKey: 'username',
    header: t('admin.users.username')
  },
  {
    accessorKey: 'email',
    header: t('admin.users.email')
  },
  {
    id: 'roles',
    header: t('admin.users.roles')
  },
  ...props.surfaces.columns.value.map(item => ({
    id: `extension_${item.surface.id.replace(/[^a-zA-Z0-9_]/g, '_')}`,
    header: item.view.title || item.surface.label,
    accessorFn: (user: AdminUserSummary) => props.surfaces.columnValue(item, String(user.id))
  })),
  {
    id: 'status',
    header: t('admin.users.status')
  },
  {
    id: 'actions',
    header: t('admin.users.actions')
  }
])
</script>

<template>
  <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
    <div
      v-if="props.surfaces.hasBulkActions.value"
      class="mb-3 flex min-h-9 flex-wrap items-center gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
    >
      <UCheckbox
        :model-value="props.surfaces.allVisibleSelected.value"
        :label="t('admin.surfaces.selectVisible')"
        @update:model-value="props.surfaces.selectAllVisible($event === true)"
      />
      <UBadge v-if="props.surfaces.selectedResourceIds.value.length" color="neutral" variant="soft">
        {{ t('admin.surfaces.selectedCount', { count: props.surfaces.selectedResourceIds.value.length }) }}
      </UBadge>
      <UButton
        v-for="action in props.surfaces.bulkActions.value"
        :key="action.surface.id"
        size="sm"
        :color="action.tone"
        variant="soft"
        :icon="action.icon"
        :loading="props.surfaces.busySurfaceId.value === action.surface.id"
        :disabled="props.surfaces.selectedResourceIds.value.length === 0"
        @click="props.surfaces.executeAction(action, props.surfaces.selectedResourceIds.value)"
      >
        {{ action.label }}
      </UButton>
    </div>

    <UTable
      :data="props.users"
      :columns="columns"
      :loading="props.pending"
      :empty="t('admin.users.empty')"
      :caption="t('admin.users.caption')"
      sticky
      class="max-h-[calc(100vh-20rem)]"
    >
      <template #selection-cell="{ row }">
        <UCheckbox
          :model-value="props.surfaces.selectedResourceIds.value.includes(String(row.original.id))"
          :aria-label="t('admin.surfaces.selectResource', { label: row.original.displayName })"
          @update:model-value="props.surfaces.toggleResource(String(row.original.id), $event === true)"
        />
      </template>

      <template #username-cell="{ row }">
        <div
          class="min-w-0 rounded-md px-1 transition-colors"
          :class="props.highlightUserId === row.original.id ? 'bg-[var(--sf-accent)]/5' : ''"
        >
          <div class="flex items-center gap-2">
            <span class="font-semibold text-slate-900 dark:text-white">{{ row.original.displayName }}</span>
            <UBadge v-if="row.original.isInitialSuperAdmin" color="warning" variant="soft">
              {{ t('admin.users.initialSuperAdmin') }}
            </UBadge>
            <UBadge v-if="props.highlightUserId === row.original.id" color="primary" variant="soft">
              {{ t('admin.users.createNewBadge') }}
            </UBadge>
          </div>
          <code class="text-xs text-slate-500 dark:text-zinc-400">{{ row.original.username }}</code>
        </div>
      </template>

      <template #email-cell="{ row }">
        <span class="text-sm text-slate-600 dark:text-zinc-300">{{ row.original.email }}</span>
      </template>

      <template #roles-cell="{ row }">
        <div class="flex flex-wrap gap-1.5">
          <UBadge
            v-for="key in row.original.roleKeys"
            :key="key"
            color="neutral"
            variant="outline"
            class="font-medium"
          >
            {{ props.roles.find(role => role.key === key)?.alias || key }}
          </UBadge>
        </div>
      </template>

      <template #status-cell="{ row }">
        <UBadge :color="row.original.status === 'active' ? 'success' : row.original.status === 'banned' ? 'error' : 'neutral'" variant="soft">
          {{ t(`admin.users.statusMap.${row.original.status}`) }}
        </UBadge>
      </template>

      <template #actions-cell="{ row }">
        <div class="flex flex-wrap items-center gap-1">
          <UButton
            color="neutral"
            variant="ghost"
            leading-icon="i-lucide-eye"
            @click="emit('preview', row.original)"
          >
            {{ t('admin.users.preview') }}
          </UButton>
          <UButton
            color="primary"
            variant="ghost"
            leading-icon="i-lucide-panel-right-open"
            :loading="props.detailPending && props.detailTargetId === row.original.id"
            @click="emit('manage', row.original)"
          >
            {{ t('admin.users.manage') }}
          </UButton>
          <UButton
            v-for="action in props.surfaces.rowActionsFor(String(row.original.id))"
            :key="action.surface.id"
            size="sm"
            :color="action.tone"
            variant="ghost"
            :icon="action.icon"
            :loading="props.surfaces.busySurfaceId.value === action.surface.id"
            @click="props.surfaces.executeAction(action, [String(row.original.id)])"
          >
            {{ action.label }}
          </UButton>
        </div>
      </template>
    </UTable>

    <div
      v-if="props.total > 0"
      class="mt-4 flex flex-col gap-3 border-t border-slate-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
    >
      <p class="text-xs text-slate-500 dark:text-zinc-400">
        {{ t('admin.users.pagination', { page: props.page, pages: props.totalPages }) }}
        · {{ t('admin.users.count', { count: props.total }) }}
      </p>
      <UPagination
        v-if="props.totalPages > 1"
        :page="props.page"
        :total="props.total"
        :items-per-page="props.perPage"
        class="justify-end"
        @update:page="value => emit('update:page', value)"
      />
    </div>
  </UCard>
</template>
