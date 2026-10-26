import type {
  AdminUserDetail,
  AdminUserList,
  AdminUserSortField,
  AdminUserSortOrder,
  AdminUserSummary,
  Permission,
  Role
} from '~/utils/admin/adminUsers'
import { useAdminListSurfaces } from '~/composables/admin/useAdminListSurfaces'

export const ADMIN_USERS_DEFAULT_PER_PAGE = 20

export type AdminUserListDeps = {
  /** 列表加载/筛选失败：这是唯一还该出现页面级红条的场景。 */
  onError: (message: string) => void
}

export type AdminUserListSurfaces = ReturnType<typeof useAdminListSurfaces>

/** 用户列表的查询状态与取数：页面只保留路由外壳与抽屉/弹窗装配。 */
export function useAdminUserList(deps: AdminUserListDeps) {
  const { request } = useApiClient()
  const { t } = useI18n()

  const search = ref('')
  const status = ref('')
  const roleKey = ref('')
  const sortBy = ref<AdminUserSortField>('createdAt')
  const sortOrder = ref<AdminUserSortOrder>('desc')
  const users = ref<AdminUserSummary[]>([])
  const total = ref(0)
  const page = ref(1)
  const perPage = ref(ADMIN_USERS_DEFAULT_PER_PAGE)
  const roles = ref<Role[]>([])
  const permissions = ref<Permission[]>([])
  const pending = ref(false)
  // 创建成功后要清空筛选并只刷新一次；watcher 的自动刷新在这段时间内让位。
  let suppressFilterReload = false

  const totalPages = computed(() => Math.max(1, Math.ceil(total.value / Math.max(perPage.value, 1))))

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

  async function loadUsers() {
    const params = new URLSearchParams({
      page: String(page.value),
      perPage: String(ADMIN_USERS_DEFAULT_PER_PAGE)
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
    perPage.value = result.perPage || ADMIN_USERS_DEFAULT_PER_PAGE
  }

  async function refreshUsers() {
    pending.value = true
    try {
      await loadUsers()
    } catch (error) {
      deps.onError(apiErrorMessage(error) || t('admin.users.loadFailed'))
    } finally {
      pending.value = false
    }
  }

  async function loadInitialData() {
    pending.value = true
    try {
      const [roleItems, permissionItems] = await Promise.all([
        request<Role[]>('/roles'),
        request<Permission[]>('/permissions')
      ])
      roles.value = roleItems
      permissions.value = permissionItems
      await loadUsers()
    } catch (error) {
      deps.onError(apiErrorMessage(error) || t('admin.users.loadFailed'))
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

  /** 新建用户后：清空筛选、回到第一页、只刷新一次。 */
  async function resetFiltersAndReload() {
    suppressFilterReload = true
    search.value = ''
    status.value = ''
    roleKey.value = ''
    page.value = 1
    await nextTick()
    suppressFilterReload = false
    await refreshUsers()
  }

  watch([status, roleKey, sortBy, sortOrder], () => {
    if (suppressFilterReload) {
      return
    }
    void applyFilters()
  })

  return {
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
  }
}
