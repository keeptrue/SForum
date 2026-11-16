import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { useAdminUserDetailEditor } from '../../app/composables/admin/identity/useAdminUserDetailEditor'
import { useAdminUserSecurity } from '../../app/composables/admin/identity/useAdminUserSecurity'
import { resolveAdminUserError } from '../../app/utils/admin/adminUserFieldErrors'

/**
 * P2-2 的管理入口：撤销目标用户的全部 API 令牌（DELETE /users/{userID}/api-tokens）。
 *
 * 这里守四件事，缺一条这个入口就是坏的：
 * 1. 没有 `user.manage` 时按钮不渲染（后端才是权威，前端别把入口摆给没权限的人）；
 * 2. 点击只开确认框、不发请求——令牌撤销不可逆，不能手滑一下清空；
 * 3. 成功后 Toast 报出真实撤销数量，并重取详情；
 * 4. 失败留在抽屉里的持久横幅上，且 403 `api_token.super_admin_locked` 必须是人话
 *    （后端没把这个 reason 收进本地化表，envelope.message 就是键名本身）。
 */

const sectionPath = fileURLToPath(new URL('../../app/components/admin/identity/users/SFAdminUserSecuritySection.vue', import.meta.url))
const drawerPath = fileURLToPath(new URL('../../app/components/admin/identity/users/SFAdminUserDetailDrawer.vue', import.meta.url))
const zhPath = fileURLToPath(new URL('../../i18n/locales/zh-CN.json', import.meta.url))
const enPath = fileURLToPath(new URL('../../i18n/locales/en-US.json', import.meta.url))

const section = await compileVueSfc(sectionPath, 'security-section', {})

const passthrough = { template: '<div />' }

// 抽屉里只有安全区是真实组件：本用例关心的按钮、确认框、错误横幅都在这条链上。
const drawer = await compileVueSfc(drawerPath, 'api-token-drawer', {
  useAdminUserDetailEditor,
  useAdminUserSecurity,
  SFAdminUserSecuritySection: section,
  SFAdminUserAccountSection: passthrough,
  SFAdminUserProfileSection: passthrough,
  SFAdminUserRolesSection: passthrough,
  SFAdminUserPermissionsSection: passthrough,
  SFAdminUserNicknameHistory: passthrough,
  SFAdminSurfaceOutlet: passthrough,
  formatAdminDateTime: () => '2026/11/16 22:00'
})

const detail = {
  id: 425,
  username: 'qa_generated_1027',
  email: 'qa-generated-1027@example.test',
  emailVerified: true,
  displayName: 'QA Generated',
  locale: 'zh-CN',
  status: 'active',
  kind: 'human',
  isInitialSuperAdmin: false,
  roleKeys: ['member'],
  permissions: [],
  permissionOverrides: { allow: [], deny: [] },
  profile: { bio: '', signature: '', location: '', websiteUrl: '' },
  nicknameHistory: []
}

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
  createdAt: '2026-11-16T10:00:00Z',
  updatedAt: '2026-11-16T10:00:00Z'
}

const form = {
  username: '',
  email: '',
  displayName: '',
  locale: 'zh-CN',
  status: 'active' as const,
  kind: 'human' as const,
  bio: '',
  signature: '',
  location: '',
  websiteUrl: '',
  newPassword: '',
  confirmPassword: ''
}

type Call = { path: string, options?: Record<string, unknown> }

type Presented = { error: unknown, fallback: string, reasons?: Record<string, string> }

type Harness = {
  toasts: Record<string, unknown>[]
  calls: Call[]
  presentError: Presented[]
}

function installGlobals(options: {
  request?: (path: string, requestOptions?: Record<string, unknown>) => Promise<unknown>
} = {}): Harness {
  const toasts: Record<string, unknown>[] = []
  const calls: Call[] = []
  const presentError: Presented[] = []
  Object.assign(globalThis, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    nextTick: testVue.nextTick,
    onMounted: testVue.onMounted,
    useTemplateRef: testVue.useTemplateRef,
    useI18n: () => ({
      locale: testVue.ref('zh-CN'),
      // 参数带 count 时把数量拼进返回值，方便断言「Toast 报的是响应里的真实数字」。
      t: (key: string, params?: Record<string, unknown>) =>
        params && 'count' in params ? `${key}#${String(params.count)}` : key
    }),
    useToast: () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) }),
    useApiClient: () => ({
      request: async (path: string, requestOptions?: Record<string, unknown>) => {
        calls.push({ path, options: requestOptions })
        return options.request
          ? options.request(path, requestOptions)
          : {}
      }
    })
  })
  return { toasts, calls, presentError }
}

const buttonStub = {
  props: ['loading', 'disabled'],
  template: '<button :disabled="disabled"><slot /></button>'
}
const fieldStub = {
  props: ['label', 'error', 'help'],
  template: '<div><slot /></div>'
}
const inputStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<input :value="modelValue">'
}
const alertStub = {
  props: ['title', 'description', 'color'],
  template: '<div class="alert" :data-color="color">{{ title }}|{{ description }}</div>'
}
const modalStub = {
  props: ['open'],
  emits: ['update:open'],
  template: '<div v-if="open" data-testid="confirm"><slot name="content" /></div>'
}

function mountSection(overrides: Record<string, unknown> = {}) {
  // 安全区只用 useI18n + ref：装好全局自动导入再挂载。
  installGlobals()
  return mount(section, {
    props: {
      form: { ...form },
      errors: {},
      resetting: false,
      revoking: false,
      revokingApiTokens: false,
      clearing: false,
      canManage: true,
      clearError: () => {},
      ...overrides
    },
    global: {
      stubs: {
        UButton: buttonStub,
        UModal: modalStub,
        UFormField: fieldStub,
        UInput: inputStub,
        UAlert: alertStub,
        UIcon: true
      }
    }
  })
}

function mountDrawer(canManage = true) {
  return mount(drawer, {
    props: { roles: [], permissions: [], canManage },
    global: {
      stubs: {
        USlideover: {
          props: ['open'],
          emits: ['update:open'],
          template: '<div v-if="open" data-testid="drawer"><slot name="content" /></div>'
        },
        UButton: buttonStub,
        UModal: modalStub,
        UFormField: fieldStub,
        UInput: inputStub,
        UAlert: alertStub,
        USkeleton: true,
        UIcon: true
      }
    }
  })
}

function apiTokenButton(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('button').find(item => item.text() === 'admin.users.revokeAPITokens')
}

const superAdminLocked = {
  code: 403,
  message: 'api_token.super_admin_locked',
  data: { reason: 'api_token.super_admin_locked' }
}

describe('admin user detail drawer: revoke all API tokens', () => {
  test('does not render the action without user.manage', () => {
    const wrapper = mountSection({ canManage: false })
    try {
      expect(wrapper.text()).not.toContain('admin.users.revokeAPITokens')
      // 同区其它动作不受影响：只有这一个入口按权限收口。
      expect(wrapper.text()).toContain('admin.users.revokeSessions')
    } finally {
      wrapper.unmount()
    }
  })

  test('renders beside force sign-out and only opens the confirmation first', async () => {
    const wrapper = mountSection()
    try {
      const apiTokens = apiTokenButton(wrapper)!
      const sessions = wrapper.findAll('button').find(item => item.text().includes('admin.users.revokeSessions'))!
      expect(apiTokens).toBeDefined()
      // 并排：两个按钮同一个 flex 容器。
      expect(apiTokens.element.parentElement).toBe(sessions.element.parentElement)
      // 按钮自己带一句「只作废令牌、会话不动」的说明。
      expect(apiTokens.attributes('title')).toBe('admin.users.revokeAPITokensHelp')

      expect(wrapper.find('[data-testid="confirm"]').exists()).toBe(false)
      await apiTokens.trigger('click')
      expect(wrapper.find('[data-testid="confirm"]').exists()).toBe(true)
      // 关键：点一下按钮不发请求，必须先确认。
      expect(wrapper.emitted('revokeApiTokens')).toBeUndefined()

      const confirm = wrapper.get('[data-testid="confirm"]')
      expect(confirm.text()).toContain('admin.users.revokeAPITokensConfirmTitle')
      expect(confirm.text()).toContain('admin.users.revokeAPITokensConfirmHelp')

      // 取消 → 不发请求。
      await confirm.findAll('button')[0]!.trigger('click')
      expect(wrapper.emitted('revokeApiTokens')).toBeUndefined()

      // 再次打开并确认 → 才抛给上层去发请求。
      await apiTokenButton(wrapper)!.trigger('click')
      const confirmButtons = wrapper.get('[data-testid="confirm"]').findAll('button')
      await confirmButtons[confirmButtons.length - 1]!.trigger('click')
      expect(wrapper.emitted('revokeApiTokens')).toHaveLength(1)
      expect(wrapper.find('[data-testid="confirm"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('drawer passes the permission gate down: no user.manage, no entry', async () => {
    installGlobals({ request: async () => detail })
    const wrapper = mountDrawer(false)
    try {
      await (wrapper.vm as unknown as { open: (user: typeof summary) => Promise<void> }).open(summary)
      await flushPromises()
      expect(apiTokenButton(wrapper)).toBeUndefined()
      expect(wrapper.text()).toContain('admin.users.revokeSessions')
    } finally {
      wrapper.unmount()
    }
  })

  test('confirm copy spells out the difference from force sign-out', () => {
    const zh = JSON.parse(readFileSync(zhPath, 'utf8')).admin.users
    const en = JSON.parse(readFileSync(enPath, 'utf8')).admin.users
    // 区别必须是「令牌失效、浏览器会话不受影响」，以及和强制下线的对比。
    expect(zh.revokeAPITokensConfirmHelp).toContain('API 令牌')
    expect(zh.revokeAPITokensConfirmHelp).toContain('浏览器')
    expect(zh.revokeAPITokensConfirmHelp).toContain('强制下线')
    expect(en.revokeAPITokensConfirmHelp).toContain('API token')
    expect(en.revokeAPITokensConfirmHelp).toContain('Browser sessions')
    expect(en.revokeAPITokensConfirmHelp).toContain('Force-signing out')
    // 强制下线自己的说明必须承认它会连令牌一起作废（后端同一事务里撤销 api_tokens）。
    expect(zh.sessionSectionHelp).toContain('API 令牌')
    expect(en.sessionSectionHelp).toContain('API token')
  })

  test('sends DELETE, toasts the real count and refreshes the detail', async () => {
    const harness = installGlobals({
      request: async (path, options) => {
        if (path === '/users/425/api-tokens' && options?.method === 'DELETE') {
          return { revoked: 3 }
        }
        if (path === '/users/425') {
          return detail
        }
        return {}
      }
    })
    const wrapper = mountDrawer()
    try {
      await (wrapper.vm as unknown as { open: (user: typeof summary) => Promise<void> }).open(summary)
      await flushPromises()

      await apiTokenButton(wrapper)!.trigger('click')
      const confirmButtons = wrapper.get('[data-testid="confirm"]').findAll('button')
      await confirmButtons[confirmButtons.length - 1]!.trigger('click')
      await flushPromises()

      const deletes = harness.calls.filter(call => call.path === '/users/425/api-tokens')
      expect(deletes).toHaveLength(1)
      expect(deletes[0]!.options?.method).toBe('DELETE')
      expect(harness.toasts).toEqual([
        { title: 'admin.users.apiTokensRevoked#3', color: 'success', icon: 'i-lucide-check-circle', duration: 5000 }
      ])
      // 详情被重取：打开时一次 + 撤销成功后一次。
      expect(harness.calls.filter(call => call.path === '/users/425' && !call.options?.method)).toHaveLength(2)
      // 成功路径不留错误横幅。
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps a super_admin 403 as a readable banner inside the drawer', async () => {
    const harness = installGlobals({
      request: async (path, options) => {
        if (path === '/users/425/api-tokens' && options?.method === 'DELETE') {
          throw superAdminLocked
        }
        if (path === '/users/425') {
          return detail
        }
        return {}
      }
    })
    const wrapper = mountDrawer()
    try {
      await (wrapper.vm as unknown as { open: (user: typeof summary) => Promise<void> }).open(summary)
      await flushPromises()

      await apiTokenButton(wrapper)!.trigger('click')
      const confirmButtons = wrapper.get('[data-testid="confirm"]').findAll('button')
      await confirmButtons[confirmButtons.length - 1]!.trigger('click')
      await flushPromises()

      // 错误留在抽屉里的持久横幅上，不是会自动消失的 toast。
      const banner = wrapper.get('[role="alert"]')
      expect(banner.text()).toContain('admin.users.drawerErrorTitle')
      expect(banner.text()).toContain('admin.users.revokeAPITokensSuperAdminLocked')
      // 技术键名不许直接甩给操作者。
      expect(banner.text()).not.toContain('api_token.super_admin_locked')
      expect(harness.toasts.filter(toast => toast.color === 'error')).toHaveLength(0)
      expect(harness.toasts).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('reason mapping is what turns the raw key into copy', () => {
    const fallback = 'admin.users.revokeAPITokensFailed'
    const copy = {
      fallback,
      fieldsSummary: () => 'fields',
      noChanges: 'no-changes'
    }
    // 没有映射时，后端未本地化的 reason 会原样显示：这就是必须补 reasons 的原因。
    expect(resolveAdminUserError(superAdminLocked, copy).summary).toBe('api_token.super_admin_locked')
    expect(resolveAdminUserError(superAdminLocked, {
      ...copy,
      reasons: { 'api_token.super_admin_locked': 'admin.users.revokeAPITokensSuperAdminLocked' }
    }).summary).toBe('admin.users.revokeAPITokensSuperAdminLocked')
  })
})

describe('useAdminUserSecurity: revokeUserAPITokens', () => {
  test('calls the documented endpoint and reports the revoked count', async () => {
    const calls: Call[] = []
    const harness = installGlobals({
      request: async (path, options) => {
        calls.push({ path, options })
        return { revoked: 7 }
      }
    })
    let refreshed = 0
    const security = useAdminUserSecurity({
      form: { ...form },
      userId: () => 425,
      presentError: (error, fallback, reasons) => harness.presentError.push({ error, fallback, reasons }),
      setFieldError: () => {},
      clearErrors: () => {},
      notifySaved: (message) => harness.toasts.push({ title: message }),
      refreshDetail: async () => {
        refreshed += 1
      }
    })

    await security.revokeUserAPITokens()

    expect(calls).toEqual([{ path: '/users/425/api-tokens', options: { method: 'DELETE' } }])
    expect(harness.toasts).toEqual([{ title: 'admin.users.apiTokensRevoked#7' }])
    expect(refreshed).toBe(1)
    expect(security.revokingAPITokens.value).toBe(false)
  })

  test('maps the super_admin 403 to readable copy and does not refresh', async () => {
    const harness = installGlobals({
      request: async () => {
        throw superAdminLocked
      }
    })
    let refreshed = 0
    const security = useAdminUserSecurity({
      form: { ...form },
      userId: () => 425,
      presentError: (error, fallback, reasons) => harness.presentError.push({ error, fallback, reasons }),
      setFieldError: () => {},
      clearErrors: () => {},
      notifySaved: (message) => harness.toasts.push({ title: message }),
      refreshDetail: async () => {
        refreshed += 1
      }
    })

    await security.revokeUserAPITokens()

    expect(harness.toasts).toHaveLength(0)
    expect(refreshed).toBe(0)
    expect(harness.presentError).toHaveLength(1)
    expect(harness.presentError[0]!.fallback).toBe('admin.users.revokeAPITokensFailed')
    expect(harness.presentError[0]!.reasons).toEqual({
      'api_token.super_admin_locked': 'admin.users.revokeAPITokensSuperAdminLocked'
    })
  })

  test('ignores a second click while the first request is in flight', async () => {
    let resolve: (value: unknown) => void = () => {}
    const calls: Call[] = []
    const harness = installGlobals({
      request: (path, options) => {
        calls.push({ path, options })
        return new Promise((done) => {
          resolve = done
        })
      }
    })
    const security = useAdminUserSecurity({
      form: { ...form },
      userId: () => 425,
      presentError: () => {},
      setFieldError: () => {},
      clearErrors: () => {},
      notifySaved: () => {},
      refreshDetail: async () => {}
    })

    const first = security.revokeUserAPITokens()
    const second = security.revokeUserAPITokens()
    expect(calls).toHaveLength(1)
    resolve({ revoked: 1 })
    await Promise.all([first, second])
    expect(calls).toHaveLength(1)
  })
})
