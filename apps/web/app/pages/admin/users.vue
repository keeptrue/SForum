<script setup lang="ts">
import type { AdminUserDetail, AdminUserSummary } from '~/utils/admin/adminUsers'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminUserList } from '~/composables/admin/identity/useAdminUserList'
import SFAdminUserListToolbar from '~/components/admin/identity/users/SFAdminUserListToolbar.vue'
import SFAdminUserTable from '~/components/admin/identity/users/SFAdminUserTable.vue'
import SFAdminUserCreateDialog from '~/components/admin/identity/users/SFAdminUserCreateDialog.vue'
import SFAdminUserPreviewModal from '~/components/admin/identity/users/SFAdminUserPreviewModal.vue'
import SFAdminUserDetailDrawer from '~/components/admin/identity/users/SFAdminUserDetailDrawer.vue'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})
defineOptions({
  name: 'AdminUsers'
})

type PreviewModalHandle = {
  open: (user: AdminUserSummary) => Promise<void>
  pending: boolean
  targetId: number | null
}
type DetailDrawerHandle = {
  open: (user: AdminUserSummary) => Promise<void>
  close: () => void
  pending: boolean
  targetId: number | null
}

const { t } = useI18n()
const adminPage = useAdminPage('/users')
const { can } = useAuthSession()
const canManage = computed(() => can('user.manage'))

// 页面级红条只留给列表加载/筛选失败；抽屉与弹窗的错误各自留在自己的壳里。
// 这里只写持久横幅：同一句再弹一个会自消失的 error toast 属于重复告知。
const errorMessage = ref('')
function showError(message: string) {
  errorMessage.value = message
}

const {
  search,
  status,
  roleKey,
  sortBy,
  sortOrder,
  users,
  total,
  page,
  perPage,
  roles,
  permissions,
  pending,
  totalPages,
  userListSurfaces,
  visibleUsers,
  loadInitialData,
  refreshUsers,
  goToPage,
  applyFilters,
  resetFiltersAndReload
} = useAdminUserList({ onError: showError })

const roleOptions = computed(() => roles.value.map(role => ({
  key: role.key,
  label: role.alias || role.key
})))

const previewModal = useTemplateRef<PreviewModalHandle>('previewModal')
const detailDrawer = useTemplateRef<DetailDrawerHandle>('detailDrawer')

const createOpen = ref(false)
function openCreateDialog() {
  createOpen.value = true
}

const highlightUserId = ref<number | null>(null)
let highlightTimer: ReturnType<typeof setTimeout> | null = null

// 创建成功后清空筛选、回到第一页并高亮新行，让操作者立刻看到结果。
async function handleUserCreated(user: AdminUserDetail) {
  await resetFiltersAndReload()
  highlightUserId.value = user.id
  if (highlightTimer) clearTimeout(highlightTimer)
  highlightTimer = setTimeout(() => {
    highlightUserId.value = null
  }, 6000)
}

useSeoMeta({
  title: t('admin.users.metaTitle')
})

onMounted(() => {
  void loadInitialData()
})
</script>

<template>
  <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.users.title') }}
      </h2>
      <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.users.intro') }}
      </p>
    </div>
    <UButton
      v-if="canManage"
      color="primary"
      icon="i-lucide-plus"
      class="self-start"
      @click="openCreateDialog"
    >
      {{ t('admin.users.createAction') }}
    </UButton>
  </div>

  <SFAdminUserListToolbar
    v-model:search="search"
    v-model:status="status"
    v-model:role-key="roleKey"
    v-model:sort-by="sortBy"
    v-model:sort-order="sortOrder"
    :role-options="roleOptions"
    :pending="pending"
    :total="total"
    :per-page="perPage"
    @apply="applyFilters"
    @refresh="refreshUsers"
  >
    <label v-for="item in userListSurfaces.filters.value" :key="item.surface.id" class="flex items-center gap-2">
      <span class="sr-only">{{ item.view.title || item.surface.label }}</span>
      <select
        :value="userListSurfaces.filterValue(item.surface.id)"
        :disabled="userListSurfaces.pending.value"
        class="h-9 rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition-colors hover:border-slate-300 focus:border-[var(--sf-accent)] dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200"
        @change="userListSurfaces.setFilter(item.surface.id, ($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ item.view.title || item.surface.label }}: {{ t('admin.surfaces.filterAll') }}</option>
        <option v-for="option in item.view.options" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
    </label>
  </SFAdminUserListToolbar>

  <div class="flex flex-col gap-4">
    <UAlert
      v-if="errorMessage"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="errorMessage"
    />
    <UAlert
      v-if="userListSurfaces.failureMessage.value"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="t('admin.surfaces.loadFailed')"
      :description="userListSurfaces.failureMessage.value"
    />

    <SFAdminUserTable
      :users="visibleUsers"
      :roles="roles"
      :surfaces="userListSurfaces"
      :pending="pending"
      :total="total"
      :page="page"
      :per-page="perPage"
      :total-pages="totalPages"
      :highlight-user-id="highlightUserId"
      :detail-pending="detailDrawer?.pending ?? false"
      :detail-target-id="detailDrawer?.targetId ?? null"
      @preview="user => previewModal?.open(user)"
      @manage="user => detailDrawer?.open(user)"
      @update:page="goToPage"
    />
  </div>

  <SFAdminUserDetailDrawer
    ref="detailDrawer"
    :roles="roles"
    :permissions="permissions"
    @changed="refreshUsers"
  />

  <SFAdminUserCreateDialog
    v-model:open="createOpen"
    :roles="roles"
    @created="handleUserCreated"
  />

  <SFAdminUserPreviewModal
    ref="previewModal"
    :roles="roles"
    @manage="user => detailDrawer?.open(user)"
  />
</template>
