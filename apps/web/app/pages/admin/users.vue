<script setup lang="ts">
import { usePermissionText } from '~/composables/identity/usePermissionText'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'
import SFAdminSurfaceOutlet from '~/components/admin/SFAdminSurfaceOutlet.vue'
import SFAdminUserListToolbar from '~/components/admin/identity/users/SFAdminUserListToolbar.vue'
import SFAdminUserEmailVerificationControl from '~/components/admin/identity/users/SFAdminUserEmailVerificationControl.vue'
import SFAdminUserNicknameHistory from '~/components/admin/identity/users/SFAdminUserNicknameHistory.vue'
import SFAdminUserPreviewModal from '~/components/admin/identity/users/SFAdminUserPreviewModal.vue'
import SFAdminUserCreateDialog from '~/components/admin/identity/users/SFAdminUserCreateDialog.vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import { firstAdminUserErrorField, resolveAdminUserError } from '~/utils/admin/adminUserFieldErrors'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { formatAdminDateTime } from '~/utils/admin/adminDateFormat'
import type {
  AdminUserDetail, AdminUserList,
  AdminUserSortField, AdminUserSortOrder,
  AdminUserSummary,
  Permission, Role,
  UserStatus
} from '~/utils/admin/adminUsers'
definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})
defineOptions({
  name: 'AdminUsers'
})

const DEFAULT_PER_PAGE = 20
// 预览弹层内列表分页：会话卡片较占高，权限 chip 可更密。

const { t } = useI18n()
const { request } = useApiClient()
const { permissionLabel, permissionDescription, permissionModuleLabel } = usePermissionText()
const toast = useToast()
const adminPage = useAdminPage('/users')
// 预览弹层自带数据获取与分页；页面只保留打开入口和行内 loading 状态。
type PreviewModalHandle = {
  open: (user: AdminUserSummary) => Promise<void>
  pending: boolean
  targetId: number | null
}
const previewModal = useTemplateRef<PreviewModalHandle>('previewModal')
const { can } = useAuthSession()
const canManage = computed(() => can('user.manage'))
const createOpen = ref(false)
function openCreateDialog() {
  createOpen.value = true
}
const highlightUserId = ref<number | null>(null)
let highlightTimer: ReturnType<typeof setTimeout> | null = null
// 创建成功后要清空筛选并只刷新一次；watcher 的自动刷新在这段时间内让位。
let suppressFilterReload = false

const search = ref('')
const status = ref('')
const roleKey = ref('')
const sortBy = ref<AdminUserSortField>('createdAt')
const sortOrder = ref<AdminUserSortOrder>('desc')
const users = ref<AdminUserSummary[]>([])
const total = ref(0)
const page = ref(1)
const perPage = ref(DEFAULT_PER_PAGE)
const roles = ref<Role[]>([])
const permissions = ref<Permission[]>([])
const selectedUser = ref<AdminUserDetail | null>(null)
const selectedRoleKeys = ref<string[]>([])
const allowOverrides = ref<string[]>([])
const denyOverrides = ref<string[]>([])

// 账户/资料编辑草稿
const editUsername = ref('')
const editEmail = ref('')
const editDisplayName = ref('')
const editLocale = ref('zh-CN')
const editStatus = ref<UserStatus>('active')
// 账号类型：human 参与论坛；bot 由系统驱动、不能登录，是 AI 助手的载体。
const editKind = ref<'human' | 'bot'>('human')
const editBio = ref('')
const editSignature = ref('')
const editLocation = ref('')
const editWebsiteUrl = ref('')

const pending = ref(false)
const detailPending = ref(false)
const savingAccount = ref(false)
const savingProfile = ref(false)
const savingRoles = ref(false)
const savingOverrides = ref(false)
const revokingSessions = ref(false)
const clearingClientIPs = ref(false)
const resettingPassword = ref(false)
const newPassword = ref('')
const confirmPassword = ref('')
// 页面级错误只服务列表加载/筛选失败；抽屉内的保存失败一律留在抽屉里。
const errorMessage = ref('')
const drawerOpen = ref(false)
const drawerError = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
const errorFocusRequest = ref(0)
const drawerEl = ref<HTMLElement | null>(null)
const detailError = ref('')
const detailTarget = ref<AdminUserSummary | null>(null)
let detailRequestToken = 0
const overrideModes = ['inherit', 'allow', 'deny'] as const

onMounted(() => {
  void loadInitialData()
})

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / Math.max(perPage.value, 1))))

// 分页计算会夹紧页码；同步回 ref，避免删减数据后落在空页。

const userSurfaceResources = computed(() => users.value.map(user => ({
  id: String(user.id),
  attributes: {
    username: user.username,
    email: user.email,
    displayName: user.displayName,
    locale: user.locale,
    status: user.status,
    isInitialSuperAdmin: user.isInitialSuperAdmin,
    roleKeys: [...user.roleKeys],
    createdAt: user.createdAt,
    updatedAt: user.updatedAt
  }
})))
const userListSurfaces = useAdminListSurfaces({
  pageId: '/users',
  resources: userSurfaceResources,
  context: computed(() => ({
    resourceType: 'user',
    page: page.value,
    perPage: perPage.value,
    total: total.value,
    coreFilters: {
      query: search.value.trim(),
      status: status.value,
      roleKey: roleKey.value,
      sortBy: sortBy.value,
      sortOrder: sortOrder.value
    }
  })),
  refreshHost: refreshUsers
})
const visibleUsers = computed(() => users.value.filter(user => userListSurfaces.isResourceVisible(String(user.id))))

const columns = computed(() => [
  ...(userListSurfaces.hasBulkActions.value ? [{ id: 'selection', header: '' }] : []),
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
  ...userListSurfaces.columns.value.map(item => ({
    id: `extension_${item.surface.id.replace(/[^a-zA-Z0-9_]/g, '_')}`,
    header: item.view.title || item.surface.label,
    accessorFn: (user: AdminUserSummary) => userListSurfaces.columnValue(item, String(user.id))
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

const groupedPermissions = computed(() => {
  const groups = new Map<string, Permission[]>()
  for (const permission of permissions.value) {
    const group = groups.get(permission.module) ?? []
    group.push(permission)
    groups.set(permission.module, group)
  }
  return Array.from(groups.entries()).map(([module, items]) => ({ module, items }))
})

const selectedUserIsSuperAdmin = computed(() => {
  return selectedUser.value?.roleKeys.includes('super_admin') ?? false
})

const roleOptions = computed(() => {
  return roles.value.map(role => ({
    key: role.key,
    label: role.alias || role.key
  }))
})

const localeOptions = computed(() => [
  { value: 'zh-CN', label: t('admin.users.localeZh') },
  { value: 'en-US', label: t('admin.users.localeEn') }
])

const statusOptions = computed(() => [
  { value: 'active', label: t('admin.users.statusActive') },
  { value: 'disabled', label: t('admin.users.statusDisabled') },
  { value: 'banned', label: t('admin.users.statusBanned') }
])

const kindOptions = computed(() => [
  { value: 'human', label: t('admin.users.kindHuman') },
  { value: 'bot', label: t('admin.users.kindBot') }
])

useSeoMeta({
  title: t('admin.users.metaTitle')
})

const formatDateTime = formatAdminDateTime

function showSuccess(message: string) {
  toast.add({
    title: message,
    color: 'success',
    icon: 'i-lucide-check-circle',
    duration: 5000
  })
}

function showError(message: string) {
  errorMessage.value = message
  toast.add({
    title: message,
    color: 'error',
    icon: 'i-lucide-triangle-alert'
  })
}

function clearDrawerErrors() {
  drawerError.value = ''
  fieldErrors.value = {}
}

// 抽屉内失败：摘要进抽屉顶部，字段级原因交给后端 data.fields，前端只兜底 summary。
function presentDrawerError(error: unknown, fallback: string) {
  const presentation = resolveAdminUserError(error, {
    fallback,
    fieldsSummary: t('admin.users.fieldFallback'),
    noChanges: t('admin.users.noChanges')
  })
  drawerError.value = presentation.summary
  fieldErrors.value = presentation.fields
  if (Object.keys(presentation.fields).length > 0) {
    errorFocusRequest.value += 1
  }
}

async function loadInitialData() {
  pending.value = true
  errorMessage.value = ''
  try {
    const [roleItems, permissionItems] = await Promise.all([
      request<Role[]>('/roles'),
      request<Permission[]>('/permissions')
    ])
    roles.value = roleItems
    permissions.value = permissionItems
    await loadUsers()
  } catch (error) {
    showError(apiErrorMessage(error) || t('admin.users.loadFailed'))
  } finally {
    pending.value = false
  }
}

async function loadUsers() {
  const params = new URLSearchParams({
    page: String(page.value),
    perPage: String(DEFAULT_PER_PAGE)
  })
  if (search.value.trim()) {
    params.set('query', search.value.trim())
  }
  if (status.value) {
    params.set('status', status.value)
  }
  if (roleKey.value) {
    params.set('roleKey', roleKey.value)
  }
  params.set('sortBy', sortBy.value)
  params.set('sortOrder', sortOrder.value)

  const result = await request<AdminUserList>(`/users?${params.toString()}`)
  users.value = result.items
  total.value = result.total
  page.value = result.page || page.value
  perPage.value = result.perPage || DEFAULT_PER_PAGE
}

async function refreshUsers() {
  pending.value = true
  errorMessage.value = ''
  try {
    await loadUsers()
  } catch (error) {
    showError(apiErrorMessage(error) || t('admin.users.loadFailed'))
  } finally {
    pending.value = false
  }
}

async function goToPage(nextPage: number) {
  if (nextPage < 1 || nextPage > totalPages.value || nextPage === page.value) {
    return
  }
  page.value = nextPage
  await refreshUsers()
}

async function applyFilters() {
  page.value = 1
  await refreshUsers()
}

async function handleUserCreated(user: AdminUserDetail) {
  suppressFilterReload = true
  search.value = ''
  status.value = ''
  roleKey.value = ''
  page.value = 1
  await nextTick()
  suppressFilterReload = false
  await refreshUsers()
  highlightUserId.value = user.id
  if (highlightTimer) clearTimeout(highlightTimer)
  highlightTimer = setTimeout(() => {
    highlightUserId.value = null
  }, 6000)
}

function applyDetailToForm(detail: AdminUserDetail) {
  selectedUser.value = detail
  selectedRoleKeys.value = [...detail.roleKeys]
  allowOverrides.value = [...detail.permissionOverrides.allow]
  denyOverrides.value = [...detail.permissionOverrides.deny]
  editUsername.value = detail.username
  editEmail.value = detail.email
  editDisplayName.value = detail.displayName
  editLocale.value = detail.locale || 'zh-CN'
  editStatus.value = detail.status
  editKind.value = detail.kind === 'bot' ? 'bot' : 'human'
  editBio.value = detail.profile?.bio ?? ''
  editSignature.value = detail.profile?.signature ?? ''
  editLocation.value = detail.profile?.location ?? ''
  editWebsiteUrl.value = detail.profile?.websiteUrl ?? ''
  // 切换用户时清空密码表单，避免误提交到下一位。
  newPassword.value = ''
  confirmPassword.value = ''
}

// 详情加载失败也要开抽屉：错误、重试都在抽屉里，不再落到列表区红条。
async function openUser(user: AdminUserSummary) {
  const token = ++detailRequestToken
  drawerOpen.value = true
  detailTarget.value = user
  detailPending.value = true
  detailError.value = ''
  clearDrawerErrors()
  try {
    const detail = await request<AdminUserDetail>(`/users/${user.id}`)
    if (token !== detailRequestToken) return
    applyDetailToForm(detail)
  } catch (error) {
    if (token !== detailRequestToken) return
    selectedUser.value = null
    detailError.value = apiErrorMessage(error) || t('admin.users.detailLoadFailed')
  } finally {
    if (token === detailRequestToken) {
      detailPending.value = false
    }
  }
}

async function retryOpenUser() {
  if (detailTarget.value) {
    await openUser(detailTarget.value)
  }
}

// 插件 surface 触发的刷新不该把抽屉打回骨架屏，静默重取即可。
async function refreshSelectedUser() {
  if (!selectedUser.value) {
    return
  }
  try {
    applyDetailToForm(await request<AdminUserDetail>(`/users/${selectedUser.value.id}`))
  } catch (error) {
    presentDrawerError(error, t('admin.users.detailLoadFailed'))
  }
}

function closeUser() {
  detailRequestToken += 1
  drawerOpen.value = false
  detailPending.value = false
  detailError.value = ''
  detailTarget.value = null
  selectedUser.value = null
  selectedRoleKeys.value = []
  allowOverrides.value = []
  denyOverrides.value = []
  newPassword.value = ''
  confirmPassword.value = ''
  clearDrawerErrors()
}

function toggleRole(key: string) {
  if (selectedRoleKeys.value.includes(key)) {
    selectedRoleKeys.value = selectedRoleKeys.value.filter(item => item !== key)
    return
  }
  selectedRoleKeys.value = [...selectedRoleKeys.value, key].sort()
}

function permissionMode(key: string) {
  if (allowOverrides.value.includes(key)) {
    return 'allow'
  }
  if (denyOverrides.value.includes(key)) {
    return 'deny'
  }
  return 'inherit'
}

function setPermissionMode(key: string, mode: 'inherit' | 'allow' | 'deny') {
  allowOverrides.value = allowOverrides.value.filter(item => item !== key)
  denyOverrides.value = denyOverrides.value.filter(item => item !== key)
  if (mode === 'allow') {
    allowOverrides.value = [...allowOverrides.value, key].sort()
  }
  if (mode === 'deny') {
    denyOverrides.value = [...denyOverrides.value, key].sort()
  }
}

async function saveAccount() {
  if (!selectedUser.value) {
    return
  }
  savingAccount.value = true
  clearDrawerErrors()
  try {
    const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}`, {
      method: 'PATCH',
      body: {
        username: editUsername.value.trim(),
        email: editEmail.value.trim(),
        displayName: editDisplayName.value.trim(),
        locale: editLocale.value,
        status: editStatus.value,
        kind: editKind.value
      }
    })
    applyDetailToForm(detail)
    await loadUsers()
    showSuccess(t('admin.users.accountSaved'))
  } catch (error) {
    presentDrawerError(error, t('admin.users.saveAccountFailed'))
  } finally {
    savingAccount.value = false
  }
}

async function saveProfile() {
  if (!selectedUser.value) {
    return
  }
  savingProfile.value = true
  clearDrawerErrors()
  try {
    const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}`, {
      method: 'PATCH',
      body: {
        bio: editBio.value,
        signature: editSignature.value,
        location: editLocation.value,
        websiteUrl: editWebsiteUrl.value.trim()
      }
    })
    applyDetailToForm(detail)
    showSuccess(t('admin.users.profileSaved'))
  } catch (error) {
    presentDrawerError(error, t('admin.users.saveProfileFailed'))
  } finally {
    savingProfile.value = false
  }
}

async function saveRoles() {
  if (!selectedUser.value) {
    return
  }
  savingRoles.value = true
  clearDrawerErrors()
  try {
    const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}/roles`, {
      method: 'PUT',
      body: {
        roleKeys: selectedRoleKeys.value
      }
    })
    applyDetailToForm(detail)
    await loadUsers()
    showSuccess(t('admin.users.rolesSaved'))
  } catch (error) {
    presentDrawerError(error, t('admin.users.saveRolesFailed'))
  } finally {
    savingRoles.value = false
  }
}

async function savePermissionOverrides() {
  if (!selectedUser.value) {
    return
  }
  savingOverrides.value = true
  clearDrawerErrors()
  try {
    const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}/permission-overrides`, {
      method: 'PUT',
      body: {
        allow: allowOverrides.value,
        deny: denyOverrides.value
      }
    })
    applyDetailToForm(detail)
    await loadUsers()
    showSuccess(t('admin.users.permissionsSaved'))
  } catch (error) {
    presentDrawerError(error, t('admin.users.savePermissionsFailed'))
  } finally {
    savingOverrides.value = false
  }
}

// 管理员强制下线目标用户的全部设备（user.manage；禁止对自己操作）。
async function revokeUserSessions() {
  if (!selectedUser.value || revokingSessions.value) {
    return
  }
  revokingSessions.value = true
  clearDrawerErrors()
  try {
    const result = await request<{ revoked: number }>(`/users/${selectedUser.value.id}/sessions/revoke`, {
      method: 'POST'
    })
    showSuccess(t('admin.users.sessionsRevoked', { count: result.revoked }))
  } catch (error) {
    presentDrawerError(error, t('admin.users.revokeSessionsFailed'))
  } finally {
    revokingSessions.value = false
  }
}

// 隐私合规：清空该用户相关真实客户端 IP。
async function clearUserClientIPs() {
  if (!selectedUser.value || clearingClientIPs.value) {
    return
  }
  clearingClientIPs.value = true
  clearDrawerErrors()
  try {
    await request(`/users/${selectedUser.value.id}/client-ips/clear`, {
      method: 'POST'
    })
    showSuccess(t('admin.users.clientIPsCleared'))
  } catch (error) {
    presentDrawerError(error, t('admin.users.clearClientIPsFailed'))
  } finally {
    clearingClientIPs.value = false
  }
}

// 管理员直接设置目标用户密码（user.manage）；成功后目标全部设备会被下线。
async function resetUserPassword() {
  if (!selectedUser.value || resettingPassword.value) {
    return
  }
  const password = newPassword.value
  // 纯 UX 判断：必填与两次一致。长度/复杂度是后端策略，前端不镜像。
  if (!password) {
    drawerError.value = ''
    fieldErrors.value = { password: [t('admin.users.passwordRequired')] }
    errorFocusRequest.value += 1
    return
  }
  if (password !== confirmPassword.value) {
    drawerError.value = ''
    fieldErrors.value = { confirmPassword: [t('admin.users.passwordMismatch')] }
    errorFocusRequest.value += 1
    return
  }
  resettingPassword.value = true
  clearDrawerErrors()
  try {
    const result = await request<{ revokedSessions: number }>(`/users/${selectedUser.value.id}/password`, {
      method: 'POST',
      body: { password }
    })
    newPassword.value = ''
    confirmPassword.value = ''
    showSuccess(t('admin.users.passwordResetSuccess', { count: result.revokedSessions ?? 0 }))
  } catch (error) {
    presentDrawerError(error, t('admin.users.passwordResetFailed'))
  } finally {
    resettingPassword.value = false
  }
}

function clearFieldError(name: string) {
  if (!fieldErrors.value[name]) {
    return
  }
  const next = { ...fieldErrors.value }
  delete next[name]
  fieldErrors.value = next
}

// 保存失败后把视野带到第一个出错字段：滚动 + 聚焦，键盘操作者不用自己找。
watch(errorFocusRequest, async () => {
  await nextTick()
  const name = firstAdminUserErrorField(fieldErrors.value)
  if (!name) {
    return
  }
  const field = drawerEl.value?.querySelector<HTMLElement>(`[data-user-field="${name}"]`)
  const target = field?.querySelector<HTMLElement>('input, select, textarea')
  target?.scrollIntoView({ block: 'center' })
  target?.focus()
})

watch([status, roleKey, sortBy, sortOrder], () => {
  if (suppressFilterReload) return
  void applyFilters()
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

    <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <div
        v-if="userListSurfaces.hasBulkActions.value"
        class="mb-3 flex min-h-9 flex-wrap items-center gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
      >
        <UCheckbox
          :model-value="userListSurfaces.allVisibleSelected.value"
          :label="t('admin.surfaces.selectVisible')"
          @update:model-value="userListSurfaces.selectAllVisible($event === true)"
        />
        <UBadge v-if="userListSurfaces.selectedResourceIds.value.length" color="neutral" variant="soft">
          {{ t('admin.surfaces.selectedCount', { count: userListSurfaces.selectedResourceIds.value.length }) }}
        </UBadge>
        <UButton
          v-for="action in userListSurfaces.bulkActions.value"
          :key="action.surface.id"
          size="sm"
          :color="action.tone"
          variant="soft"
          :icon="action.icon"
          :loading="userListSurfaces.busySurfaceId.value === action.surface.id"
          :disabled="userListSurfaces.selectedResourceIds.value.length === 0"
          @click="userListSurfaces.executeAction(action, userListSurfaces.selectedResourceIds.value)"
        >
          {{ action.label }}
        </UButton>
      </div>
      <UTable
        :data="visibleUsers"
        :columns="columns"
        :loading="pending"
        :empty="t('admin.users.empty')"
        :caption="t('admin.users.caption')"
        sticky
        class="max-h-[calc(100vh-20rem)]"
      >
        <template #selection-cell="{ row }">
          <UCheckbox
            :model-value="userListSurfaces.selectedResourceIds.value.includes(String(row.original.id))"
            :aria-label="t('admin.surfaces.selectResource', { label: row.original.displayName })"
            @update:model-value="userListSurfaces.toggleResource(String(row.original.id), $event === true)"
          />
        </template>

        <template #username-cell="{ row }">
          <div
            class="min-w-0 rounded-md px-1 transition-colors"
            :class="highlightUserId === row.original.id ? 'bg-[var(--sf-accent)]/5' : ''"
          >
            <div class="flex items-center gap-2">
              <span class="font-semibold text-slate-900 dark:text-white">{{ row.original.displayName }}</span>
              <UBadge v-if="row.original.isInitialSuperAdmin" color="warning" variant="soft">
                {{ t('admin.users.initialSuperAdmin') }}
              </UBadge>
              <UBadge v-if="highlightUserId === row.original.id" color="primary" variant="soft">
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
              {{ roles.find(role => role.key === key)?.alias || key }}
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
              :loading="previewModal?.pending && previewModal?.targetId === row.original.id"
              @click="previewModal?.open(row.original)"
            >
              {{ t('admin.users.preview') }}
            </UButton>
            <UButton
              color="primary"
              variant="ghost"
              leading-icon="i-lucide-panel-right-open"
              :loading="detailPending && detailTarget?.id === row.original.id"
              @click="openUser(row.original)"
            >
              {{ t('admin.users.manage') }}
            </UButton>
            <UButton
              v-for="action in userListSurfaces.rowActionsFor(String(row.original.id))"
              :key="action.surface.id"
              size="sm"
              :color="action.tone"
              variant="ghost"
              :icon="action.icon"
              :loading="userListSurfaces.busySurfaceId.value === action.surface.id"
              @click="userListSurfaces.executeAction(action, [String(row.original.id)])"
            >
              {{ action.label }}
            </UButton>
          </div>
        </template>
      </UTable>

      <div
        v-if="total > 0"
        class="mt-4 flex flex-col gap-3 border-t border-slate-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
      >
        <p class="text-xs text-slate-500 dark:text-zinc-400">
          {{ t('admin.users.pagination', { page, pages: totalPages }) }}
          · {{ t('admin.users.count', { count: total }) }}
        </p>
        <UPagination
          v-if="totalPages > 1"
          :page="page"
          :total="total"
          :items-per-page="perPage"
          class="justify-end"
          @update:page="goToPage"
        />
      </div>
    </UCard>
  </div>


  <USlideover
    :open="drawerOpen"
    side="right"
    :title="selectedUser?.displayName || t('admin.users.manage')"
    :description="t('admin.users.drawerDescription')"
    :close="false"
    :ui="{ content: 'w-full max-w-3xl' }"
    @update:open="value => !value && closeUser()"
  >
    <template #content>
      <div ref="drawerEl" class="flex h-full min-w-0 flex-col bg-white dark:bg-zinc-950">
        <header class="flex items-start justify-between gap-4 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-semibold text-slate-950 dark:text-zinc-50">
              {{ selectedUser?.displayName || t('admin.users.manage') }}
            </h3>
            <p v-if="selectedUser" class="text-xs text-slate-500 dark:text-zinc-400">
              {{ selectedUser.username }} · {{ selectedUser.email }}
            </p>
            <p v-if="selectedUser" class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
              {{ t('admin.users.userId') }}: {{ selectedUser.id }}
              · {{ t('admin.users.createdAt') }}: {{ formatDateTime(selectedUser.createdAt) }}
              · {{ t('admin.users.updatedAt') }}: {{ formatDateTime(selectedUser.updatedAt) }}
            </p>
          </div>
          <UButton color="neutral" variant="ghost" icon="i-lucide-x" :aria-label="t('admin.users.close')" @click="closeUser" />
        </header>

        <!-- 保存失败摘要钉在头部下方，不随内容滚走；错误提示不自动消失。 -->
        <div v-if="drawerError" class="border-b border-slate-200 px-5 py-3 dark:border-zinc-800" role="alert">
          <UAlert
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="t('admin.users.drawerErrorTitle')"
            :description="drawerError"
          />
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <div v-if="detailPending" class="space-y-4">
            <USkeleton class="h-6 w-1/2" />
            <USkeleton class="h-40 w-full" />
            <USkeleton class="h-40 w-full" />
          </div>

          <div v-else-if="detailError" class="space-y-3">
            <UAlert
              color="error"
              variant="soft"
              icon="i-lucide-triangle-alert"
              :title="t('admin.users.detailLoadFailed')"
              :description="detailError"
            />
            <UButton color="primary" leading-icon="i-lucide-refresh-cw" @click="retryOpenUser">
              {{ t('admin.users.detailRetry') }}
            </UButton>
          </div>

          <div v-else-if="selectedUser" class="space-y-5">
            <SFAdminSurfaceOutlet
              page-id="/users"
              :kinds="['detail_region', 'editor_panel']"
              :context="{ resourceType: 'user', resource: selectedUser }"
              @refresh="refreshSelectedUser"
            />

            <!-- 账户信息 -->
            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.users.accountSection') }}
                  </h4>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.accountSectionHelp') }}
                  </p>
                </div>
                <UButton
                  color="primary"
                  leading-icon="i-lucide-save"
                  :loading="savingAccount"
                  @click="saveAccount"
                >
                  {{ t('admin.users.saveAccount') }}
                </UButton>
              </div>

              <div class="grid gap-3 sm:grid-cols-2">
                <UFormField data-user-field="username" :label="t('admin.users.username')" :error="fieldErrors.username?.[0]">
                  <UInput
                    v-model="editUsername"
                    class="w-full"
                    @update:model-value="clearFieldError('username')"
                  />
                </UFormField>
                <UFormField data-user-field="email" :label="t('admin.users.email')" :error="fieldErrors.email?.[0]">
                  <UInput
                    v-model="editEmail"
                    type="email"
                    class="w-full"
                    @update:model-value="clearFieldError('email')"
                  />
                </UFormField>
                <UFormField data-user-field="displayName" :label="t('admin.users.displayName')" :error="fieldErrors.displayName?.[0]">
                  <UInput
                    v-model="editDisplayName"
                    class="w-full"
                    @update:model-value="clearFieldError('displayName')"
                  />
                </UFormField>
                <UFormField data-user-field="locale" :label="t('admin.users.locale')" :error="fieldErrors.locale?.[0]">
                  <USelect v-model="editLocale" :items="localeOptions" class="w-full" />
                </UFormField>
                <UFormField
                  data-user-field="status"
                  class="sm:col-span-2"
                  :label="t('admin.users.status')"
                  :error="fieldErrors.status?.[0]"
                >
                  <USelect v-model="editStatus" :items="statusOptions" class="w-full max-w-xs" />
                  <p v-if="selectedUser.isInitialSuperAdmin" class="mt-1 text-xs text-amber-600 dark:text-amber-400">
                    {{ t('admin.users.initialSuperAdmin') }}
                  </p>
                </UFormField>
                <UFormField
                  data-user-field="kind"
                  class="sm:col-span-2"
                  :label="t('admin.users.kind')"
                  :error="fieldErrors.kind?.[0]"
                  :help="t('admin.users.kindHint')"
                >
                  <USelect
                    v-model="editKind"
                    :items="kindOptions"
                    :disabled="selectedUser.isInitialSuperAdmin"
                    class="w-full max-w-xs"
                  />
                </UFormField>
              </div>
              <SFAdminUserEmailVerificationControl :user="selectedUser" @updated="applyDetailToForm" />
            </section>

            <SFAdminUserNicknameHistory :changes="selectedUser.nicknameHistory" />

            <!-- 公开资料 -->
            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.users.profileSection') }}
                  </h4>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.profileSectionHelp') }}
                  </p>
                </div>
                <UButton
                  color="primary"
                  leading-icon="i-lucide-save"
                  :loading="savingProfile"
                  @click="saveProfile"
                >
                  {{ t('admin.users.saveProfile') }}
                </UButton>
              </div>

              <div class="grid gap-3">
                <UFormField data-user-field="bio" :label="t('admin.users.bio')" :error="fieldErrors.bio?.[0]">
                  <UTextarea
                    v-model="editBio"
                    :rows="3"
                    class="w-full"
                    @update:model-value="clearFieldError('bio')"
                  />
                </UFormField>
                <UFormField data-user-field="signature" :label="t('admin.users.signature')" :error="fieldErrors.signature?.[0]">
                  <UTextarea
                    v-model="editSignature"
                    :rows="2"
                    class="w-full"
                    @update:model-value="clearFieldError('signature')"
                  />
                </UFormField>
                <div class="grid gap-3 sm:grid-cols-2">
                  <UFormField data-user-field="location" :label="t('admin.users.location')" :error="fieldErrors.location?.[0]">
                    <UInput
                      v-model="editLocation"
                      class="w-full"
                      @update:model-value="clearFieldError('location')"
                    />
                  </UFormField>
                  <UFormField data-user-field="websiteUrl" :label="t('admin.users.websiteUrl')" :error="fieldErrors.websiteUrl?.[0]">
                    <UInput
                      v-model="editWebsiteUrl"
                      type="url"
                      placeholder="https://"
                      class="w-full"
                      @update:model-value="clearFieldError('websiteUrl')"
                    />
                  </UFormField>
                </div>
              </div>
            </section>

            <!-- 账号安全 -->
            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="space-y-4">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.users.passwordSection') }}
                  </h4>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.passwordSectionHelp') }}
                  </p>
                  <div class="mt-3 grid gap-3 sm:grid-cols-2">
                    <UFormField data-user-field="password" :label="t('admin.users.newPassword')" :error="fieldErrors.password?.[0]">
                      <UInput
                        v-model="newPassword"
                        type="password"
                        autocomplete="new-password"
                        class="w-full"
                        :placeholder="t('admin.users.newPasswordPlaceholder')"
                        @update:model-value="clearFieldError('password')"
                      />
                    </UFormField>
                    <UFormField data-user-field="confirmPassword" :label="t('admin.users.confirmPassword')" :error="fieldErrors.confirmPassword?.[0]">
                      <UInput
                        v-model="confirmPassword"
                        type="password"
                        autocomplete="new-password"
                        class="w-full"
                        :placeholder="t('admin.users.confirmPasswordPlaceholder')"
                        @update:model-value="clearFieldError('confirmPassword')"
                      />
                    </UFormField>
                  </div>
                  <div class="mt-3 flex flex-wrap items-center gap-2">
                    <UButton
                      color="primary"
                      leading-icon="i-lucide-key-round"
                      :loading="resettingPassword"
                      :disabled="!newPassword || !confirmPassword"
                      @click="resetUserPassword"
                    >
                      {{ t('admin.users.resetPassword') }}
                    </UButton>
                    <p class="text-xs text-slate-500 dark:text-zinc-400">
                      {{ t('admin.users.passwordResetHint') }}
                    </p>
                  </div>
                </div>
                <div class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
                  <div>
                    <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                      {{ t('admin.users.sessionSection') }}
                    </h4>
                    <p class="text-xs text-slate-500 dark:text-zinc-400">
                      {{ t('admin.users.sessionSectionHelp') }}
                    </p>
                  </div>
                  <UButton
                    color="error"
                    variant="soft"
                    leading-icon="i-lucide-log-out"
                    :loading="revokingSessions"
                    @click="revokeUserSessions"
                  >
                    {{ t('admin.users.revokeSessions') }}
                  </UButton>
                </div>
                <div class="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
                  <div>
                    <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                      {{ t('admin.users.clearClientIPs') }}
                    </h4>
                    <p class="text-xs text-slate-500 dark:text-zinc-400">
                      {{ t('admin.users.clearClientIPsHelp') }}
                    </p>
                  </div>
                  <UButton
                    color="neutral"
                    variant="soft"
                    leading-icon="i-lucide-shield-off"
                    :loading="clearingClientIPs"
                    @click="clearUserClientIPs"
                  >
                    {{ t('admin.users.clearClientIPs') }}
                  </UButton>
                </div>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.users.roleSection') }}
                  </h4>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.roleSectionHelp') }}
                  </p>
                </div>
                <UButton
                  color="primary"
                  leading-icon="i-lucide-save"
                  :loading="savingRoles"
                  @click="saveRoles"
                >
                  {{ t('admin.users.saveRoles') }}
                </UButton>
              </div>

              <div class="grid gap-2 sm:grid-cols-2">
                <label
                  v-for="role in roles"
                  :key="role.key"
                  class="flex min-h-14 cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm transition hover:border-[var(--sf-accent-soft-border)] dark:border-zinc-800 dark:bg-zinc-950"
                >
                  <input
                    type="checkbox"
                    class="mt-1 size-4 accent-[var(--sf-accent)]"
                    :checked="selectedRoleKeys.includes(role.key)"
                    @change="toggleRole(role.key)"
                  >
                  <span class="min-w-0">
                    <span class="block font-semibold text-slate-900 dark:text-zinc-100">{{ role.alias }}</span>
                    <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ role.key }}</span>
                  </span>
                </label>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ t('admin.users.permissionSection') }}
                  </h4>
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ selectedUserIsSuperAdmin ? t('admin.users.superAdminOverrideLocked') : t('admin.users.permissionSectionHelp') }}
                  </p>
                </div>
                <UButton
                  color="primary"
                  leading-icon="i-lucide-save"
                  :loading="savingOverrides"
                  :disabled="selectedUserIsSuperAdmin"
                  @click="savePermissionOverrides"
                >
                  {{ t('admin.users.savePermissions') }}
                </UButton>
              </div>

              <div class="space-y-4">
                <div v-for="group in groupedPermissions" :key="group.module" class="space-y-2">
                  <div class="text-xs font-semibold uppercase text-slate-500 dark:text-zinc-400">
                    {{ permissionModuleLabel(group.module) }}
                  </div>
                  <div class="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-950">
                    <div
                      v-for="permission in group.items"
                      :key="permission.key"
                      class="grid gap-3 px-3 py-2 text-sm sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
                    >
                      <div class="min-w-0">
                        <span class="block font-semibold text-slate-900 dark:text-zinc-100">
                          {{ permissionLabel(permission) }}
                        </span>
                        <code class="mt-0.5 block text-xs text-slate-500 dark:text-zinc-400">{{ permission.key }}</code>
                        <p class="mt-0.5 text-xs text-slate-500 dark:text-zinc-400">
                          {{ permissionDescription(permission) }}
                        </p>
                      </div>
                      <div class="inline-flex rounded-md border border-slate-200 bg-slate-50 p-0.5 text-xs dark:border-zinc-800 dark:bg-zinc-900">
                        <button
                          v-for="mode in overrideModes"
                          :key="mode"
                          type="button"
                          class="h-7 px-2.5 font-medium transition"
                          :class="permissionMode(permission.key) === mode
                            ? 'rounded bg-white text-[var(--sf-accent)] shadow-sm dark:bg-zinc-800 dark:text-[var(--sf-accent-dark)]'
                            : 'text-slate-500 hover:text-slate-900 dark:text-zinc-400 dark:hover:text-zinc-100'"
                          :disabled="selectedUserIsSuperAdmin"
                          @click="setPermissionMode(permission.key, mode)"
                        >
                          {{ t(`admin.users.overrideMode.${mode}`) }}
                        </button>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </section>
          </div>
        </div>
      </div>
    </template>
  </USlideover>

  <SFAdminUserCreateDialog
    v-model:open="createOpen"
    :roles="roles"
    @created="handleUserCreated"
  />

  <SFAdminUserPreviewModal
    ref="previewModal"
    :roles="roles"
    @manage="openUser"
  />
</template>
