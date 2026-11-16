<script setup lang="ts">
import type { AdminUserSortField, AdminUserSortOrder } from '~/utils/admin/adminUsers'

const props = defineProps<{
  search: string
  status: string
  roleKey: string
  sortBy: AdminUserSortField
  sortOrder: AdminUserSortOrder
  roleOptions: Array<{ key: string, label: string }>
  pending: boolean
  total: number
  perPage: number
}>()

const emit = defineEmits<{
  apply: []
  refresh: []
  'update:search': [value: string]
  'update:status': [value: string]
  'update:roleKey': [value: string]
  'update:sortBy': [value: AdminUserSortField]
  'update:sortOrder': [value: AdminUserSortOrder]
}>()
const { t } = useI18n()

const selectClass = 'h-9 rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition-colors hover:border-slate-300 focus:border-[var(--sf-accent)] disabled:cursor-not-allowed disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200'
</script>

<template>
  <!--
    390 宽下工具栏内部横向滚动（Nuxt UI 的 root 自带 overflow-x-auto）：
    容器必须可聚焦，否则键盘用户滚不到右侧的排序/刷新控件（axe scrollable-region-focusable）。
  -->
  <UDashboardToolbar
    class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400"
    tabindex="0"
    role="region"
    :aria-label="t('admin.users.toolbarLabel')"
  >
    <template #left>
      <div class="flex flex-wrap items-center gap-2">
        <UInput
          :model-value="props.search"
          icon="i-lucide-search"
          :placeholder="t('admin.users.searchPlaceholder')"
          :disabled="pending"
          class="w-72 max-w-full"
          @update:model-value="emit('update:search', String($event))"
          @keyup.enter="emit('apply')"
        />
        <select
          :value="props.status"
          :disabled="pending"
          :aria-label="t('admin.users.status')"
          :class="selectClass"
          @change="emit('update:status', ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('admin.users.allStatuses') }}</option>
          <option value="active">{{ t('admin.users.statusActive') }}</option>
          <option value="disabled">{{ t('admin.users.statusDisabled') }}</option>
          <option value="banned">{{ t('admin.users.statusBanned') }}</option>
        </select>
        <select
          :value="props.roleKey"
          :disabled="pending"
          :aria-label="t('admin.users.roles')"
          :class="selectClass"
          @change="emit('update:roleKey', ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t('admin.users.allRoles') }}</option>
          <option v-for="role in roleOptions" :key="role.key" :value="role.key">
            {{ role.label }}
          </option>
        </select>
        <select
          :value="props.sortBy"
          data-testid="admin-user-sort-field"
          :disabled="pending"
          :aria-label="t('admin.users.sortByLabel')"
          :title="t('admin.users.sortByLabel')"
          :class="selectClass"
          @change="emit('update:sortBy', ($event.target as HTMLSelectElement).value as AdminUserSortField)"
        >
          <option value="createdAt">{{ t('admin.users.sortByValue', { value: t('admin.users.sortCreatedAt') }) }}</option>
          <option value="updatedAt">{{ t('admin.users.sortByValue', { value: t('admin.users.sortUpdatedAt') }) }}</option>
          <option value="username">{{ t('admin.users.sortByValue', { value: t('admin.users.sortUsername') }) }}</option>
          <option value="displayName">{{ t('admin.users.sortByValue', { value: t('admin.users.sortDisplayName') }) }}</option>
          <option value="email">{{ t('admin.users.sortByValue', { value: t('admin.users.sortEmail') }) }}</option>
          <option value="status">{{ t('admin.users.sortByValue', { value: t('admin.users.sortStatus') }) }}</option>
        </select>
        <select
          :value="props.sortOrder"
          data-testid="admin-user-sort-order"
          :disabled="pending"
          :aria-label="t('admin.users.sortOrderLabel')"
          :title="t('admin.users.sortOrderLabel')"
          :class="selectClass"
          @change="emit('update:sortOrder', ($event.target as HTMLSelectElement).value as AdminUserSortOrder)"
        >
          <option value="desc">{{ t('admin.users.sortOrderValue', { value: t('admin.users.sortDescending') }) }}</option>
          <option value="asc">{{ t('admin.users.sortOrderValue', { value: t('admin.users.sortAscending') }) }}</option>
        </select>
        <slot />
      </div>
    </template>
    <template #right>
      <div class="flex items-center gap-3">
        <UButton
          color="neutral"
          variant="outline"
          icon="i-lucide-refresh-cw"
          :loading="pending"
          :aria-label="t('admin.users.refresh')"
          :title="t('admin.users.refresh')"
          class="border-slate-200 dark:border-zinc-700"
          @click="emit('refresh')"
        >
          <span class="hidden sm:inline">{{ t('admin.users.refresh') }}</span>
        </UButton>
        <UBadge color="neutral" variant="soft" class="border border-slate-200 font-medium dark:border-zinc-800">
          {{ t('admin.users.count', { count: total }) }}
        </UBadge>
        <UBadge color="neutral" variant="outline" class="hidden font-medium lg:inline-flex">
          {{ t('admin.users.perPageHint', { count: perPage }) }}
        </UBadge>
      </div>
    </template>
  </UDashboardToolbar>
</template>
