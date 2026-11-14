import { afterEach, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { apiErrorMessage } from '../../app/composables/useApiClient'
import { usePermissionText } from '../../app/composables/identity/usePermissionText'
import {
  adminAccessFailureKind,
  useAdminAccessState
} from '../../app/composables/admin/identity/useAdminAccessState'
import { useAdminUserList } from '../../app/composables/admin/identity/useAdminUserList'

/**
 * 后台 403 连坐（UX2-01 / UX2-02 / UX2-16）的契约：
 * - `/roles`、`/permissions` 只是可选项目录，它们的 403 不能拖死用户表；
 * - 权限矩阵真被后端拒绝时给「权限不足」态，而不是空表 / 永久骨架屏；
 * - 失败与「真的为空」互不冒充。
 */

const globals = globalThis as typeof globalThis & Record<string, unknown>

type Snapshot = Record<string, unknown>
let snapshot: Snapshot = {}

const GLOBAL_KEYS = [
  'ref',
  'reactive',
  'computed',
  'watch',
  'nextTick',
  'onMounted',
  'useTemplateRef',
  'definePageMeta',
  'useSeoMeta',
  'useRoute',
  'useI18n',
  'useToast',
  'useApiClient',
  'apiErrorMessage'
] as const

const summary = {
  id: 425,
  username: 'qa_generated_1027',
  email: 'qa-generated-1027@example.test',
  displayName: 'QA Generated',
  locale: 'zh-CN',
  status: 'active',
  kind: 'human',
  isInitialSuperAdmin: false,
  roleKeys: ['member'],
  createdAt: '2026-10-27T02:00:00Z',
  updatedAt: '2026-10-27T02:00:00Z'
}

const forbidden = { code: 403, message: 'permission.denied', data: { reason: 'permission.denied' } }

type RequestHandler = (path: string) => Promise<unknown>

function installGlobals(request: RequestHandler) {
  snapshot = {}
  for (const key of GLOBAL_KEYS) {
    snapshot[key] = globals[key]
  }

  Object.assign(globals, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    nextTick: testVue.nextTick,
    onMounted: testVue.onMounted,
    useTemplateRef: testVue.useTemplateRef,
    definePageMeta: () => {},
    useSeoMeta: () => {},
    useRoute: () => ({ path: '/control-panel', params: {}, query: {} }),
    useI18n: () => ({
      locale: testVue.ref('zh-CN'),
      t: (key: string) => key,
      te: () => false
    }),
    useToast: () => ({ add: () => {} }),
    useApiClient: () => ({ request }),
    apiErrorMessage
  })
}

function restoreGlobals() {
  for (const key of GLOBAL_KEYS) {
    if (snapshot[key] === undefined) {
      delete globals[key]
    } else {
      globals[key] = snapshot[key]
    }
  }
  snapshot = {}
}

afterEach(() => {
  restoreGlobals()
})

const pagePath = fileURLToPath(new URL('../../app/pages/admin/users.vue', import.meta.url))
const permissionsPath = fileURLToPath(new URL('../../app/pages/admin/permissions.vue', import.meta.url))

const passthrough = { template: '<div />' }

const toolbarStub = {
  props: ['search', 'status', 'roleKey', 'sortBy', 'sortOrder', 'roleOptions', 'pending', 'total', 'perPage'],
  emits: ['apply', 'refresh', 'update:search', 'update:status', 'update:roleKey', 'update:sortBy', 'update:sortOrder'],
  template: '<div><button data-testid="toolbar-refresh" @click="$emit(\'refresh\')">refresh</button><slot /></div>'
}

const tableStub = {
  props: ['users', 'roles', 'surfaces', 'pending', 'total', 'page', 'perPage', 'totalPages', 'highlightUserId', 'detailPending', 'detailTargetId'],
  emits: ['preview', 'manage', 'update:page'],
  template: '<div data-testid="user-table" :data-rows="users.length" :data-roles="roles.length"><span v-for="user in users" :key="user.id">{{ user.username }}</span></div>'
}

const UsersPage = await compileVueSfc(pagePath, 'admin-users-403', {
  useAdminUserList,
  useAdminPage: () => ({ icon: 'i-lucide-users' }),
  useAuthSession: () => ({ can: () => true }),
  SFAdminUserListToolbar: toolbarStub,
  SFAdminUserTable: tableStub,
  SFAdminUserCreateDialog: passthrough,
  SFAdminUserPreviewModal: passthrough,
  SFAdminUserDetailDrawer: passthrough
})

const PermissionsPage = await compileVueSfc(permissionsPath, 'admin-permissions-403', {
  usePermissionText,
  apiErrorMessage,
  useAdminPage: () => ({ icon: 'i-lucide-shield-check' }),
  useAdminAccessState
})

const alertStub = {
  props: ['title', 'description', 'color'],
  template: '<div class="alert" :data-color="color" :data-testid="$attrs[\'data-testid\']">{{ title }}|{{ description }}</div>'
}

const inputStub = {
  props: ['modelValue', 'disabled', 'icon', 'placeholder'],
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" :disabled="disabled">'
}

const commonStubs = {
  UDashboardToolbar: { template: '<div><slot name="left" /><slot name="right" /></div>' },
  UBadge: { template: '<span><slot /></span>' },
  UButton: { props: ['disabled', 'loading', 'color', 'variant', 'leadingIcon'], template: '<button :disabled="disabled"><slot /></button>' },
  UCard: { template: '<div><slot /></div>' },
  UAlert: alertStub,
  UInput: inputStub,
  UIcon: true
}

async function mountUsersPage(request: RequestHandler) {
  installGlobals(request)
  const wrapper = mount(UsersPage, {
    global: {
      stubs: {
        ...commonStubs,
        SFAdminUserListToolbar: toolbarStub,
        SFAdminUserTable: tableStub
      }
    }
  })
  await flushPromises()
  return wrapper
}

async function mountPermissionsPage(request: RequestHandler) {
  installGlobals(request)
  const wrapper = mount(PermissionsPage, { global: { stubs: commonStubs } })
  await flushPromises()
  return wrapper
}

function pathRecorder(routes: Record<string, () => Promise<unknown>>) {
  const calls: string[] = []
  const request: RequestHandler = async (path) => {
    calls.push(path)
    if (path.startsWith('/admin/admin-surfaces')) {
      return { revision: 'r1', surfaces: [], resolved: [], failures: [] }
    }
    const route = Object.keys(routes).find(key => path.startsWith(key))
    if (!route) {
      throw new Error(`unexpected request: ${path}`)
    }
    return routes[route]!()
  }

  return { request, calls }
}

describe('adminAccessFailureKind', () => {
  test('403/401 是权限问题，其余状态码是暂时不可用', () => {
    expect(adminAccessFailureKind(403)).toBe('forbidden')
    expect(adminAccessFailureKind(401)).toBe('forbidden')
    expect(adminAccessFailureKind(500)).toBe('unavailable')
    expect(adminAccessFailureKind(0)).toBe('none')
  })

  test('useAdminAccessState 由最近一次失败决定状态，reset 可退出', () => {
    installGlobals(async () => ({}))
    const state = useAdminAccessState()
    expect(state.noteFailure({ code: 500, message: 'boom' })).toBe('unavailable')
    expect(state.forbidden.value).toBe(false)

    expect(state.noteFailure(forbidden)).toBe('forbidden')
    expect(state.forbidden.value).toBe(true)

    // 403 之后的 500 必须清掉权限态：否则页面会同时说「权限不足」和「服务不可用」。
    expect(state.noteFailure({ code: 500, message: 'boom' })).toBe('unavailable')
    expect(state.forbidden.value).toBe(false)

    state.reset()
    expect(state.forbidden.value).toBe(false)
  })
})

describe('useAdminUserList catalog degradation', () => {
  test('keeps loading users when /roles and /permissions are forbidden', async () => {
    const { request, calls } = pathRecorder({
      '/roles': async () => {
        throw forbidden
      },
      '/permissions': async () => {
        throw forbidden
      },
      '/users?': async () => ({ items: [summary], total: 1, page: 1, perPage: 20 })
    })
    installGlobals(request)

    const errors: string[] = []
    const list = useAdminUserList({ onError: message => errors.push(message) })
    await list.loadInitialData()

    expect(list.users.value).toHaveLength(1)
    expect(list.total.value).toBe(1)
    expect(list.rolesUnavailable.value).toBe(true)
    expect(list.permissionsUnavailable.value).toBe(true)
    expect(list.catalogDegradedMessage.value).toBe('admin.roles.loadFailed')
    // 目录 403 只是降级：它不是列表失败，页面级红条不能亮。
    // 成功路径会用空串撤掉红条（`onError('')`），所以这里只关心没有失败文案。
    expect(errors.filter(message => message !== '')).toEqual([])
    expect(errors[errors.length - 1]).toBe('')
    expect(calls.some(path => path.startsWith('/users?'))).toBe(true)
  })

  test('reports a forbidden user list as a permission problem, not a load failure', async () => {
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions': async () => [],
      '/users?': async () => {
        throw forbidden
      }
    })
    installGlobals(request)

    const errors: string[] = []
    const list = useAdminUserList({ onError: message => errors.push(message) })
    await list.loadInitialData()

    expect(list.forbidden.value).toBe(true)
    expect(errors).toEqual(['errors.permissionDenied'])
  })

  test('clears the forbidden state once a retry succeeds', async () => {
    let denied = true
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions': async () => [],
      '/users?': async () => {
        if (denied) {
          throw forbidden
        }
        return { items: [summary], total: 1, page: 1, perPage: 20 }
      }
    })
    installGlobals(request)

    const list = useAdminUserList({ onError: () => {} })
    await list.loadInitialData()
    expect(list.forbidden.value).toBe(true)

    // 权限被补授后重试：状态必须能退出，不能永久粘住。
    denied = false
    await list.loadInitialData()
    expect(list.forbidden.value).toBe(false)
    expect(list.users.value).toHaveLength(1)
  })

  test('drops the forbidden state when the next failure is not a permission problem', async () => {
    let mode: 'forbidden' | 'unavailable' = 'forbidden'
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions': async () => [],
      '/users?': async () => {
        if (mode === 'forbidden') {
          throw forbidden
        }
        throw { code: 500, message: '服务暂时不可用。' }
      }
    })
    installGlobals(request)

    const errors: string[] = []
    const list = useAdminUserList({ onError: message => errors.push(message) })
    await list.loadInitialData()
    expect(list.forbidden.value).toBe(true)

    mode = 'unavailable'
    await list.loadInitialData()
    expect(list.forbidden.value).toBe(false)
    expect(errors).toEqual(['errors.permissionDenied', '服务暂时不可用。'])
  })
})

describe('admin users page', () => {
  test('renders the user table when /roles and /permissions answer 403', async () => {
    const { request } = pathRecorder({
      '/roles': async () => {
        throw forbidden
      },
      '/permissions': async () => {
        throw forbidden
      },
      '/users?': async () => ({ items: [summary], total: 1, page: 1, perPage: 20 })
    })
    const wrapper = await mountUsersPage(request)
    try {
      const table = wrapper.get('[data-testid="user-table"]')
      expect(table.attributes('data-rows')).toBe('1')
      expect(table.text()).toContain('qa_generated_1027')
      // 目录降级要可见，但只能是一条 warning，不能冒充列表加载失败。
      const degraded = wrapper.get('[data-testid="admin-users-catalog-degraded"]')
      expect(degraded.text()).toContain('admin.roles.loadFailed')
      expect(wrapper.findAll('.alert')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('renders the shared forbidden state instead of an empty table when /users is denied', async () => {
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions': async () => [],
      '/users?': async () => {
        throw forbidden
      }
    })
    const wrapper = await mountUsersPage(request)
    try {
      const denied = wrapper.get('[data-testid="admin-users-forbidden"]')
      expect(denied.text()).toContain('errors.page.forbidden.title')
      // 空表会被读成「本站没有用户」：403 时表格根本不该出现。
      expect(wrapper.find('[data-testid="user-table"]').exists()).toBe(false)
      // 原因由页面级红条承载，面板不重复同一句话。
      const alerts = wrapper.findAll('.alert')
      expect(alerts).toHaveLength(1)
      expect(alerts[0]!.text()).toContain('errors.permissionDenied')
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the forbidden panel while the retry is in flight and clears the banner after success', async () => {
    let denied = true
    const gate: { release?: () => void } = {}
    const request: RequestHandler = async (path) => {
      if (path.startsWith('/admin/admin-surfaces')) {
        return { revision: 'r1', surfaces: [], resolved: [], failures: [] }
      }
      if (path === '/roles' || path === '/permissions') {
        return []
      }
      if (path.startsWith('/users?')) {
        if (denied) {
          throw forbidden
        }
        // 手动控制的 promise：让「请求在飞」这一刻可被断言。
        return await new Promise((resolve) => {
          gate.release = () => resolve({ items: [summary], total: 1, page: 1, perPage: 20 })
        })
      }
      throw new Error(`unexpected request: ${path}`)
    }

    const wrapper = await mountUsersPage(request)
    try {
      expect(wrapper.find('[data-testid="admin-users-forbidden"]').exists()).toBe(true)
      expect(wrapper.findAll('.alert')).toHaveLength(1)

      denied = false
      await wrapper.get('[data-testid="admin-users-forbidden"] button').trigger('click')
      await flushPromises()

      // 请求在飞：面板必须还在。先清位会让它闪成空表（旧实现就是这个缺口）。
      expect(gate.release).toBeDefined()
      expect(wrapper.find('[data-testid="admin-users-forbidden"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="user-table"]').exists()).toBe(false)

      gate.release!()
      await flushPromises()

      expect(wrapper.find('[data-testid="admin-users-forbidden"]').exists()).toBe(false)
      expect(wrapper.get('[data-testid="user-table"]').attributes('data-rows')).toBe('1')
      // 成功后红条必须撤掉，错误不能常驻。
      expect(wrapper.findAll('.alert')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('brings the panel and the banner back when the list is denied again', async () => {
    let denied = false
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions': async () => [],
      '/users?': async () => {
        if (denied) {
          throw forbidden
        }
        return { items: [summary], total: 1, page: 1, perPage: 20 }
      }
    })
    const wrapper = await mountUsersPage(request)
    try {
      expect(wrapper.find('[data-testid="user-table"]').exists()).toBe(true)
      expect(wrapper.findAll('.alert')).toHaveLength(0)

      denied = true
      await wrapper.get('[data-testid="toolbar-refresh"]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-testid="admin-users-forbidden"]').exists()).toBe(true)
      const alerts = wrapper.findAll('.alert')
      expect(alerts).toHaveLength(1)
      expect(alerts[0]!.text()).toContain('errors.permissionDenied')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('admin permissions page', () => {
  test('renders an explicit forbidden state when the matrix answers 403', async () => {
    const { request } = pathRecorder({
      '/roles': async () => [{ key: 'admin', alias: '管理员', description: '', permissionKeys: ['user.view'] }],
      '/permissions/matrix': async () => {
        throw forbidden
      }
    })
    const wrapper = await mountPermissionsPage(request)
    try {
      const denied = wrapper.get('[data-testid="admin-permissions-forbidden"]')
      expect(denied.text()).toContain('errors.page.forbidden.title')
      expect(denied.text()).toContain('errors.permissionDenied')
      // 403 不能被画成空目录，也不能报成加载失败。
      expect(wrapper.text()).not.toContain('admin.permissions.empty')
      expect(wrapper.find('[data-testid="admin-permissions-failed"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the matrix readable with role keys when /roles answers 403', async () => {
    const { request } = pathRecorder({
      '/roles': async () => {
        throw forbidden
      },
      '/permissions/matrix': async () => ({
        permissions: [{ key: 'user.view', module: 'user', label: '查看用户', description: '查看用户列表' }],
        roles: [{ roleKey: 'admin', permissionKeys: ['user.view'] }]
      })
    })
    const wrapper = await mountPermissionsPage(request)
    try {
      expect(wrapper.find('[data-testid="admin-permissions-forbidden"]').exists()).toBe(false)
      const degraded = wrapper.get('[data-testid="admin-permissions-roles-degraded"]')
      expect(degraded.text()).toContain('admin.roles.loadFailed')

      const text = wrapper.text()
      expect(text).toContain('user.view')
      expect(text).toContain('admin')
      expect(wrapper.find('table').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('shows a failure state instead of the empty state when the matrix is unavailable', async () => {
    const { request } = pathRecorder({
      '/roles': async () => [],
      '/permissions/matrix': async () => {
        throw { code: 500, message: '权限加载失败：服务暂时不可用。' }
      }
    })
    const wrapper = await mountPermissionsPage(request)
    try {
      const failed = wrapper.get('[data-testid="admin-permissions-failed"]')
      expect(failed.text()).toContain('权限加载失败：服务暂时不可用。')
      expect(wrapper.text()).not.toContain('admin.permissions.empty')
      expect(wrapper.find('[data-testid="admin-permissions-forbidden"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
