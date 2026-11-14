<script setup lang="ts">
import { usePermissionText } from '~/composables/identity/usePermissionText'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminAccessState } from '~/composables/admin/identity/useAdminAccessState'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminPermissions'
})

type Permission = {
  key: string
  module: string
  label: string
  description: string
}

type Role = {
  id: number
  key: string
  alias: string
  description: string
  isSystem: boolean
  isDefault: boolean
  isDeletable: boolean
  isEnabled: boolean
  permissionKeys: string[]
}

type RolePermissionSet = {
  roleKey: string
  permissionKeys: string[]
}

type PermissionMatrix = {
  permissions: Permission[]
  roles: RolePermissionSet[]
}

const ROLE_COMPARE_LIMIT = 5

const { t } = useI18n()
const { request } = useApiClient()
const { permissionLabel, permissionDescription, permissionModuleLabel } = usePermissionText()
const adminPage = useAdminPage('/permissions')

const roles = ref<Role[]>([])
const matrix = ref<PermissionMatrix>({ permissions: [], roles: [] })
const roleSearch = ref('')
const selectedRoleKeys = ref<string[]>([])
const showOnlyDifferences = ref(false)
const pending = ref(false)
const errorMessage = ref('')
const rolesUnavailable = ref(false)
const matrixFailed = ref(false)
// 后端才是权限权威：这里不按 can() 预判，只把真实的 403 画成可操作的状态。
const { forbidden: matrixForbidden, noteFailure: noteAccessFailure, reset: resetAccess } = useAdminAccessState()

onMounted(() => {
  void loadData()
})

const groupedPermissions = computed(() => {
  const groups = new Map<string, Permission[]>()
  for (const permission of matrix.value.permissions) {
    const group = groups.get(permission.module) ?? []
    group.push(permission)
    groups.set(permission.module, group)
  }
  return Array.from(groups.entries()).map(([module, items]) => ({ module, items }))
})

const roleSearchKeyword = computed(() => roleSearch.value.trim().toLowerCase())

/**
 * 用户组列的可读名称。`/roles` 不可用时退回矩阵自带的 `roleKey`：
 * 矩阵本身仍然可读，不能把「读不到用户组」画成「没有用户组」。
 */
const roleList = computed<Role[]>(() => {
  if (roles.value.length > 0) {
    return roles.value
  }

  return matrix.value.roles.map(set => ({
    id: 0,
    key: set.roleKey,
    alias: set.roleKey,
    description: '',
    isSystem: false,
    isDefault: false,
    isDeletable: false,
    isEnabled: true,
    permissionKeys: [...set.permissionKeys]
  }))
})

const selectableRoles = computed(() => {
  const keyword = roleSearchKeyword.value
  if (!keyword) {
    return roleList.value
  }

  return roleList.value.filter((role) => {
    return [role.key, role.alias, role.description]
      .some(value => value.toLowerCase().includes(keyword))
  })
})

const selectedRoleKeySet = computed(() => new Set(selectedRoleKeys.value))

const visibleRoles = computed(() => {
  if (selectedRoleKeys.value.length > 0) {
    return roleList.value.filter(role => selectedRoleKeySet.value.has(role.key))
  }

  return selectableRoles.value.slice(0, ROLE_COMPARE_LIMIT)
})

const hiddenRoleCount = computed(() => {
  if (selectedRoleKeys.value.length > 0) {
    return Math.max(roleList.value.length - selectedRoleKeys.value.length, 0)
  }

  return Math.max(selectableRoles.value.length - visibleRoles.value.length, 0)
})

const roleCompareLimitReached = computed(() => selectedRoleKeys.value.length >= ROLE_COMPARE_LIMIT)

const rolePermissionMap = computed(() => {
  const map = new Map<string, Set<string>>()
  for (const role of matrix.value.roles) {
    map.set(role.roleKey, new Set(role.permissionKeys))
  }
  return map
})

const filteredPermissionGroups = computed(() => {
  if (!showOnlyDifferences.value) {
    return groupedPermissions.value
  }

  return groupedPermissions.value
    .map(group => ({
      module: group.module,
      items: group.items.filter(permission => permissionHasVisibleDifference(permission.key))
    }))
    .filter(group => group.items.length > 0)
})

useSeoMeta({
  title: t('admin.permissions.metaTitle')
})

async function loadData() {
  pending.value = true
  errorMessage.value = ''
  matrixFailed.value = false
  resetAccess()
  try {
    // `/roles` 只提供列别名与对比选择：它 403 时矩阵仍然可读（列名退回 roleKey），
    // 所以两个请求各自结算，绝不让用户组列表的权限问题连坐矩阵本身。
    const [roleResult, matrixResult] = await Promise.allSettled([
      request<Role[]>('/roles'),
      request<PermissionMatrix>('/permissions/matrix')
    ])

    if (roleResult.status === 'fulfilled') {
      roles.value = roleResult.value
      rolesUnavailable.value = false
    } else {
      roles.value = []
      rolesUnavailable.value = true
    }

    if (matrixResult.status === 'rejected') {
      matrix.value = { permissions: [], roles: [] }
      // 403 是「权限不足」，不是「没有权限项」：既不能落进空态，也不能报成加载失败。
      if (noteAccessFailure(matrixResult.reason) !== 'forbidden') {
        matrixFailed.value = true
        errorMessage.value = apiErrorMessage(matrixResult.reason) || t('admin.permissions.loadFailed')
      }
      return
    }

    matrix.value = matrixResult.value
    const availableRoleKeys = new Set(roleList.value.map(role => role.key))
    selectedRoleKeys.value = selectedRoleKeys.value.filter(key => availableRoleKeys.has(key))
  } finally {
    pending.value = false
  }
}

function roleHasPermission(roleKey: string, permissionKey: string) {
  return rolePermissionMap.value.get(roleKey)?.has(permissionKey) ?? false
}

function roleIsSelected(roleKey: string) {
  return selectedRoleKeySet.value.has(roleKey)
}

function roleIsCompared(roleKey: string) {
  return visibleRoles.value.some(role => role.key === roleKey)
}

function toggleRoleSelection(roleKey: string) {
  const selected = new Set(selectedRoleKeys.value)
  if (selected.has(roleKey)) {
    selected.delete(roleKey)
  } else if (selected.size < ROLE_COMPARE_LIMIT) {
    selected.add(roleKey)
  }

  selectedRoleKeys.value = roleList.value
    .map(role => role.key)
    .filter(key => selected.has(key))
}

function resetComparisonFilters() {
  roleSearch.value = ''
  selectedRoleKeys.value = []
  showOnlyDifferences.value = false
}

function permissionHasVisibleDifference(permissionKey: string) {
  if (visibleRoles.value.length < 2) {
    return false
  }

  const permissionStates = visibleRoles.value.map(role => roleHasPermission(role.key, permissionKey))
  return new Set(permissionStates).size > 1
}
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.permissions.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.permissions.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex items-center gap-2 text-sm">
        <UIcon name="i-lucide-list-checks" class="size-4 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        <span>{{ t('admin.permissions.matrix') }}</span>
      </div>
    </template>
    <template #right>
      <div class="flex items-center gap-3">
        <UButton
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="pending"
          class="border-slate-200 dark:border-zinc-700"
          @click="loadData"
        >
          {{ t('admin.permissions.refresh') }}
        </UButton>
        <UBadge
          v-if="!matrixForbidden && !matrixFailed"
          color="neutral"
          variant="soft"
          class="border border-slate-200 dark:border-zinc-800 font-medium"
        >
          {{ t('admin.permissions.count', { count: matrix.permissions.length }) }}
        </UBadge>
      </div>
    </template>
  </UDashboardToolbar>

  <div class="flex flex-col gap-4">
    <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <div v-if="pending" class="flex items-center justify-center py-16 text-slate-400 dark:text-zinc-500">
        <UIcon name="i-lucide-loader-circle" class="mr-2 size-5 animate-spin" />
        <span class="text-sm">{{ t('admin.permissions.loading') }}</span>
      </div>

      <div
        v-else-if="matrixForbidden"
        data-testid="admin-permissions-forbidden"
        class="flex flex-col items-center justify-center py-16 text-slate-400 dark:text-zinc-500"
      >
        <UIcon name="i-lucide-shield-alert" class="size-12 mb-4" />
        <p class="text-base font-semibold text-slate-900 dark:text-zinc-100">
          {{ t('errors.page.forbidden.title') }}
        </p>
        <p class="mt-1 max-w-md text-center text-sm">
          {{ t('errors.permissionDenied') }}
        </p>
        <UButton
          class="mt-4"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="pending"
          @click="loadData"
        >
          {{ t('common.retry') }}
        </UButton>
      </div>

      <div
        v-else-if="matrixFailed"
        data-testid="admin-permissions-failed"
        class="flex flex-col items-center justify-center py-16 text-slate-400 dark:text-zinc-500"
      >
        <UIcon name="i-lucide-triangle-alert" class="size-12 mb-4" />
        <p class="max-w-md text-center text-sm text-slate-600 dark:text-zinc-300">
          {{ errorMessage || t('admin.permissions.loadFailed') }}
        </p>
        <UButton
          class="mt-4"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-refresh-cw"
          :loading="pending"
          @click="loadData"
        >
          {{ t('common.retry') }}
        </UButton>
      </div>

      <div v-else-if="matrix.permissions.length === 0" class="flex flex-col items-center justify-center py-16 text-slate-400 dark:text-zinc-500">
        <UIcon name="i-lucide-shield-check" class="size-12 mb-4" />
        <p class="text-sm">{{ t('admin.permissions.empty') }}</p>
      </div>

      <div v-else class="space-y-6">
        <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-950/60">
          <div class="mb-4 flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
            <div>
              <h3 class="text-sm font-semibold text-slate-950 dark:text-zinc-50">
                {{ t('admin.permissions.comparisonScope') }}
              </h3>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.permissions.comparisonScopeHelp', { limit: ROLE_COMPARE_LIMIT }) }}
              </p>
            </div>
            <UBadge color="neutral" variant="soft" class="w-fit border border-slate-200 dark:border-zinc-800">
              {{ t('admin.permissions.displayedRoles', { shown: visibleRoles.length, total: roleList.length }) }}
            </UBadge>
          </div>

          <div class="grid gap-3 xl:grid-cols-[minmax(0,1fr)_auto_auto] xl:items-center">
            <UInput
              v-model="roleSearch"
              icon="i-lucide-search"
              :disabled="rolesUnavailable"
              :placeholder="t('admin.permissions.roleSearchPlaceholder')"
            />
            <label class="flex cursor-pointer items-center gap-2 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 transition hover:border-[var(--sf-accent-soft-border)] dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-200">
              <input
                v-model="showOnlyDifferences"
                type="checkbox"
                :disabled="rolesUnavailable"
                class="size-4 accent-[var(--sf-accent)]"
              >
              <span>{{ t('admin.permissions.onlyDifferences') }}</span>
            </label>
            <UButton
              color="neutral"
              variant="outline"
              leading-icon="i-lucide-filter-x"
              class="justify-center border-slate-200 dark:border-zinc-700"
              @click="resetComparisonFilters"
            >
              {{ t('admin.permissions.clearFilters') }}
            </UButton>
          </div>

          <p
            v-if="rolesUnavailable"
            data-testid="admin-permissions-roles-degraded"
            class="mt-4 flex items-center gap-2 rounded-md border border-dashed border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-400"
          >
            <UIcon name="i-lucide-shield-alert" class="size-3.5 shrink-0" />
            <span>{{ t('admin.roles.loadFailed') }}</span>
          </p>

          <div v-if="selectableRoles.length > 0" class="mt-4 max-h-40 overflow-y-auto pr-1">
            <div class="flex flex-wrap gap-2">
              <button
                v-for="role in selectableRoles"
                :key="role.key"
                type="button"
                class="inline-flex min-h-9 max-w-full cursor-pointer items-center gap-2 rounded-md border px-3 py-1.5 text-left text-xs transition-colors disabled:cursor-not-allowed disabled:opacity-45"
                :class="roleIsCompared(role.key)
                  ? 'border-[var(--sf-accent-soft-border)] bg-[var(--sf-accent-soft)] text-[var(--sf-accent)] dark:bg-[rgb(var(--sf-accent-rgb)/0.15)] dark:text-[var(--sf-accent-dark)]'
                  : 'border-slate-200 bg-white text-slate-600 hover:border-slate-300 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:border-zinc-700'"
                :disabled="roleCompareLimitReached && !roleIsSelected(role.key)"
                :aria-pressed="roleIsSelected(role.key)"
                @click="toggleRoleSelection(role.key)"
              >
                <UIcon :name="roleIsCompared(role.key) ? 'i-lucide-check' : 'i-lucide-circle'" class="size-3.5 shrink-0" />
                <span class="min-w-0">
                  <span class="block truncate font-semibold">{{ role.alias }}</span>
                  <code class="block truncate text-[11px] opacity-75">{{ role.key }}</code>
                </span>
              </button>
            </div>
          </div>
          <p v-else-if="!rolesUnavailable" class="mt-4 rounded-md border border-dashed border-slate-200 bg-white px-3 py-3 text-sm text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
            {{ t('admin.permissions.noMatchingRoles') }}
          </p>

          <p v-if="selectedRoleKeys.length === 0 && hiddenRoleCount > 0" class="mt-3 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.permissions.defaultRolePreview', { limit: ROLE_COMPARE_LIMIT, hidden: hiddenRoleCount }) }}
          </p>
          <p v-else-if="roleCompareLimitReached" class="mt-3 text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.permissions.roleLimitReached', { limit: ROLE_COMPARE_LIMIT }) }}
          </p>
        </section>

        <div v-if="visibleRoles.length === 0" class="flex flex-col items-center justify-center rounded-lg border border-dashed border-slate-200 py-12 text-slate-400 dark:border-zinc-800 dark:text-zinc-500">
          <UIcon name="i-lucide-users-round" class="size-10 mb-3" />
          <p class="text-sm">{{ t('admin.permissions.noMatchingRoles') }}</p>
        </div>

        <div v-else-if="filteredPermissionGroups.length === 0" class="flex flex-col items-center justify-center rounded-lg border border-dashed border-slate-200 py-12 text-slate-400 dark:border-zinc-800 dark:text-zinc-500">
          <UIcon name="i-lucide-git-compare-arrows" class="size-10 mb-3" />
          <p class="text-sm">{{ t('admin.permissions.noDifferences') }}</p>
        </div>

        <template v-else>
          <section v-for="group in filteredPermissionGroups" :key="group.module" class="space-y-3">
            <div class="flex items-center justify-between gap-3">
              <h3 class="text-sm font-semibold uppercase tracking-normal text-slate-600 dark:text-zinc-300">
                {{ permissionModuleLabel(group.module) }}
              </h3>
              <UBadge color="neutral" variant="soft">
                {{ t('admin.permissions.moduleCount', { count: group.items.length }) }}
              </UBadge>
            </div>

            <div class="overflow-x-auto rounded-lg border border-slate-200 dark:border-zinc-800">
              <table class="min-w-full divide-y divide-slate-200 text-sm dark:divide-zinc-800">
                <thead class="bg-slate-50 dark:bg-zinc-950">
                  <tr>
                    <th class="sticky left-0 z-10 min-w-72 bg-slate-50 px-3 py-2 text-left text-xs font-semibold text-slate-500 dark:bg-zinc-950 dark:text-zinc-400">
                      {{ t('admin.permissions.permission') }}
                    </th>
                    <th
                      v-for="role in visibleRoles"
                      :key="role.key"
                      class="min-w-36 px-3 py-2 text-center text-xs font-semibold text-slate-500 dark:text-zinc-400"
                    >
                      <span class="block truncate text-slate-700 dark:text-zinc-200">{{ role.alias }}</span>
                      <code class="text-[11px] font-normal text-slate-400 dark:text-zinc-500">{{ role.key }}</code>
                    </th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-slate-200 bg-white dark:divide-zinc-800 dark:bg-zinc-900">
                  <tr v-for="permission in group.items" :key="permission.key">
                    <td class="sticky left-0 z-10 min-w-72 bg-white px-3 py-2 dark:bg-zinc-900">
                      <span class="block font-semibold text-slate-900 dark:text-zinc-100">
                        {{ permissionLabel(permission) }}
                      </span>
                      <code class="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">{{ permission.key }}</code>
                      <p class="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                        {{ permissionDescription(permission) }}
                      </p>
                    </td>
                    <td
                      v-for="role in visibleRoles"
                      :key="`${permission.key}:${role.key}`"
                      class="px-3 py-2 text-center"
                    >
                      <span
                        class="inline-flex size-7 items-center justify-center rounded-full"
                        :class="roleHasPermission(role.key, permission.key)
                          ? 'bg-[var(--sf-accent-soft)] text-[var(--sf-accent)] dark:bg-[rgb(var(--sf-accent-rgb)/0.15)] dark:text-[var(--sf-accent-dark)]'
                          : 'bg-slate-50 text-slate-300 dark:bg-zinc-950 dark:text-zinc-700'"
                      >
                        <UIcon :name="roleHasPermission(role.key, permission.key) ? 'i-lucide-check' : 'i-lucide-minus'" class="size-4" />
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </template>
      </div>
    </UCard>
  </div>
</template>
