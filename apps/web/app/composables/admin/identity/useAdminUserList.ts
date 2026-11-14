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
import { useAdminAccessState } from '~/composables/admin/identity/useAdminAccessState'

export const ADMIN_USERS_DEFAULT_PER_PAGE = 20

export type AdminUserListDeps = {
  /**
   * 列表加载/筛选失败：这是唯一还该出现页面级红条的场景。
   * 传空串表示**清除**（成功后必须撤掉上一次的红条，错误不能常驻）。
   */
  onError: (message: string) => void
}

export type AdminUserListSurfaces = ReturnType<typeof useAdminListSurfaces>

/** 用户列表的查询状态与取数：页面只保留路由外壳与抽屉/弹窗装配。 */
export function useAdminUserList(deps: AdminUserListDeps) {
  const { request } = useApiClient()
  const { t } = useI18n()
  const access = useAdminAccessState()

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
  const rolesUnavailable = ref(false)
  const permissionsUnavailable = ref(false)
  const pending = ref(false)
  // 创建成功后要清空筛选并只刷新一次；watcher 的自动刷新在这段时间内让位。
  let suppressFilterReload = false

  const totalPages = computed(() => Math.max(1, Math.ceil(total.value / Math.max(perPage.value, 1))))

  /**
   * 用户组 / 权限目录只服务于筛选下拉与详情抽屉的可选项。任一不可用都只是**降级**，
   * 不是页面失败：用户表照常渲染，由这里给出可读提示（复用既有键，不新增 i18n）。
   */
  const catalogDegradedMessage = computed(() => {
    if (rolesUnavailable.value) {
      return t('admin.roles.loadFailed')
    }
    if (permissionsUnavailable.value) {
      return t('admin.permissions.loadFailed')
    }

    return ''
  })

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
      // 只有真的拿到数据才清位：先清位会让重试期间 403 面板先消失（闪成空表）。
      access.reset()
      deps.onError('')
    } catch (error) {
      reportListFailure(error)
    } finally {
      pending.value = false
    }
  }

  /** 列表取数失败：403 说清是权限问题，其余沿用后端消息/兜底文案。 */
  function reportListFailure(error: unknown) {
    if (access.noteFailure(error) === 'forbidden') {
      deps.onError(t('errors.permissionDenied'))
      return
    }

    deps.onError(apiErrorMessage(error) || t('admin.users.loadFailed'))
  }

  async function loadInitialData() {
    pending.value = true
    try {
      // `/roles`、`/permissions` 只给筛选器与抽屉提供可选项。operator 常只有 `user.view`
      // （给了用户管理但没给角色管理），它们的 403 不该连坐用户列表：用 allSettled 让
      // 用户表照常渲染，目录降级由 catalogDegradedMessage 告知页面。
      const [roleResult, permissionResult] = await Promise.allSettled([
        request<Role[]>('/roles'),
        request<Permission[]>('/permissions')
      ])

      if (roleResult.status === 'fulfilled') {
        roles.value = roleResult.value
        rolesUnavailable.value = false
      } else {
        roles.value = []
        rolesUnavailable.value = true
      }

      if (permissionResult.status === 'fulfilled') {
        permissions.value = permissionResult.value
        permissionsUnavailable.value = false
      } else {
        permissions.value = []
        permissionsUnavailable.value = true
      }

      // 用户组不可用时不能保留一个指向读不到的用户组的筛选条件。
      // 用 suppress 挡住 watcher：这次清空不该再换一次请求。
      if (rolesUnavailable.value && roleKey.value) {
        suppressFilterReload = true
        roleKey.value = ''
        await nextTick()
        suppressFilterReload = false
      }

      await loadUsers()
      // 同上：403 面板上的重试走的就是这里，成功后才清位 + 撤红条。
      access.reset()
      deps.onError('')
    } catch (error) {
      reportListFailure(error)
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
    rolesUnavailable,
    permissionsUnavailable,
    catalogDegradedMessage,
    forbidden: access.forbidden,
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
