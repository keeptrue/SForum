import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { useAdminUserDetailEditor } from '../../app/composables/admin/identity/useAdminUserDetailEditor'
import { useAdminUserSecurity } from '../../app/composables/admin/identity/useAdminUserSecurity'
import { useAdminUserList } from '../../app/composables/admin/identity/useAdminUserList'

// 页面级集成：真实 users.vue + 真实列表 composable + 真实详情抽屉。
// 目的只有一个——证明抽屉里的保存失败不会跑到页面级红条/错误 toast 上，
// 而列表加载失败仍然有持久可见的横幅（去掉那条自消失 toast 后不能变成静默失败）。

const pagePath = fileURLToPath(new URL('../../app/pages/admin/users.vue', import.meta.url))
const drawerPath = fileURLToPath(new URL('../../app/components/admin/identity/users/SFAdminUserDetailDrawer.vue', import.meta.url))
const accountPath = fileURLToPath(new URL('../../app/components/admin/identity/users/SFAdminUserAccountSection.vue', import.meta.url))

const AccountSection = await compileVueSfc(accountPath, 'manage-drawer-account', {
  SFAdminUserEmailVerificationControl: { template: '<div />' }
})

const detail = {
  id: 425,
  username: 'qa_generated_1027',
  email: 'qa-generated-1027@example.test',
  emailVerified: false,
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
  createdAt: '2026-10-27T02:00:00Z',
  updatedAt: '2026-10-27T02:00:00Z'
}

const fieldStub = {
  props: ['label', 'error', 'help'],
  template: '<div><span class="field-label">{{ label }}</span><slot /><p v-if="error" class="field-error">{{ error }}</p></div>'
}

const inputStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)">'
}

// 表格只负责把「管理」动作抛给页面；页面通过 useTemplateRef 调抽屉的 open()。
const tableStub = {
  props: ['users', 'roles', 'surfaces', 'pending', 'total', 'page', 'perPage', 'totalPages', 'highlightUserId', 'detailPending', 'detailTargetId'],
  emits: ['preview', 'manage', 'update:page'],
  data: () => ({ target: summary }),
  template: '<button data-testid="manage" @click="$emit(\'manage\', target)">manage</button>'
}

const passthrough = { template: '<div />' }

const Drawer = await compileVueSfc(drawerPath, 'manage-drawer', {
  useAdminUserDetailEditor,
  useAdminUserSecurity,
  SFAdminUserAccountSection: AccountSection,
  SFAdminUserProfileSection: passthrough,
  SFAdminUserSecuritySection: passthrough,
  SFAdminUserRolesSection: passthrough,
  SFAdminUserPermissionsSection: passthrough,
  SFAdminUserNicknameHistory: passthrough,
  SFAdminSurfaceOutlet: passthrough,
  formatAdminDateTime: () => '2026/10/27 03:00'
})

const Page = await compileVueSfc(pagePath, 'admin-users-page', {
  useAdminUserList,
  useAdminPage: () => ({ icon: 'i-lucide-users' }),
  useAuthSession: () => ({ can: () => true }),
  SFAdminUserListToolbar: passthrough,
  SFAdminUserTable: tableStub,
  SFAdminUserCreateDialog: passthrough,
  SFAdminUserPreviewModal: passthrough,
  SFAdminUserDetailDrawer: Drawer
})

type Call = { path: string, options?: Record<string, unknown> }

type Scenario = { failList?: boolean, patchFails?: boolean }

function installGlobals(requests: Call[], toasts: Record<string, unknown>[], scenario: Scenario) {
  const failList = scenario.failList === true
  const patchFails = scenario.patchFails !== false
  Object.assign(globalThis, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    nextTick: testVue.nextTick,
    onMounted: testVue.onMounted,
    useTemplateRef: testVue.useTemplateRef,
    definePageMeta: () => {},
    useSeoMeta: () => {},
    useRoute: () => ({ path: '/users', params: {}, query: {} }),
    // useAdminUserList 走 Nuxt 自动导入；这里给一个和真实 apiErrorMessage 同形的实现。
    apiErrorMessage: (error: unknown) => {
      const candidate = error as { message?: string, data?: { message?: string } } | null
      return candidate?.message || candidate?.data?.message || ''
    },
    useI18n: () => ({
      locale: testVue.ref('zh-CN'),
      t: (key: string, params?: Record<string, unknown>) =>
        typeof params?.fields === 'string' ? `${key}(${params.fields})` : key
    }),
    useToast: () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) }),
    usePermissionText: () => ({
      permissionLabel: (permission: { key: string }) => permission.key,
      permissionDescription: () => '',
      permissionModuleLabel: (module: string) => module
    }),
    useApiClient: () => ({
      request: async (path: string, options?: Record<string, unknown>) => {
        requests.push({ path, options })
        if (path.startsWith('/admin/admin-surfaces')) {
          return { revision: 'r1', surfaces: [], resolved: [], failures: [] }
        }
        if (path === '/roles' || path === '/permissions') {
          if (failList) throw { code: 500, message: '列表加载失败：服务暂时不可用。', data: { reason: 'internal_error' } }
          return []
        }
        if (path.startsWith('/users?')) {
          return { items: [summary], total: 1, page: 1, perPage: 20 }
        }
        if (options?.method === 'PATCH' && patchFails) {
          throw {
            code: 422,
            message: '提交的用户信息有误，请按标出的字段修改后再保存。',
            data: { reason: 'user.invalid_update', fields: { email: ['邮箱格式不正确。'] } }
          }
        }
        if (path === '/users/425' || (options?.method === 'PATCH' && path === '/users/425')) {
          return detail
        }
        return {}
      }
    })
  })
}

async function mountPage(scenario: Scenario = {}) {
  const requests: Call[] = []
  const toasts: Record<string, unknown>[] = []
  installGlobals(requests, toasts, scenario)
  const wrapper = mount(Page, {
    global: {
      stubs: {
        SFAdminUserTable: tableStub,
        USlideover: {
          props: ['open'],
          emits: ['update:open'],
          template: '<div v-if="open" data-testid="drawer"><slot name="content" /></div>'
        },
        UAlert: {
          props: ['title', 'description', 'color'],
          template: '<div class="alert" :data-color="color">{{ title }}|{{ description }}</div>'
        },
        UFormField: fieldStub,
        UInput: inputStub,
        USelect: inputStub,
        UTextarea: inputStub,
        UButton: { props: ['disabled', 'loading'], template: '<button><slot /></button>' },
        UIcon: true,
        USkeleton: true
      }
    }
  })
  await flushPromises()
  return { wrapper, requests, toasts }
}

function pageAlerts(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('.alert').filter((item) => {
    const element = item.element as HTMLElement
    return !element.closest('[data-testid="drawer"]')
  })
}

describe('admin users page and detail drawer', () => {
  test('keeps a failed list load visible as a persistent banner without an error toast', async () => {
    const { wrapper, toasts } = await mountPage({ failList: true })
    try {
      const alerts = pageAlerts(wrapper)
      expect(alerts).toHaveLength(1)
      expect(alerts[0]!.text()).toContain('列表加载失败：服务暂时不可用。')
      // 页面横幅已持久显示同一条消息：再弹一条会自消失的 error toast 属于重复告知。
      expect(toasts).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps a drawer save failure out of the page banner and out of toasts', async () => {
    const { wrapper, toasts } = await mountPage()
    try {
      expect(pageAlerts(wrapper)).toHaveLength(0)
      await wrapper.get('[data-testid="manage"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-testid="drawer"]').exists()).toBe(true)

      await wrapper.get('[data-user-field="email"] input').setValue('not-an-email')
      const save = wrapper.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
      await save!.trigger('click')
      await flushPromises()

      // 字段文案 + 抽屉顶部摘要都在抽屉内。
      expect(wrapper.get('[data-user-field="email"] .field-error').text()).toBe('邮箱格式不正确。')
      expect(wrapper.get('[data-testid="drawer"] .alert').text())
        .toContain('admin.users.fieldFallback(admin.users.email)')
      // 页面级红条与 error toast 都不该被这条失败触发。
      expect(pageAlerts(wrapper)).toHaveLength(0)
      expect(toasts.filter(toast => toast.color === 'error')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('shows a success toast for a completed drawer save', async () => {
    const { wrapper, requests, toasts } = await mountPage({ patchFails: false })
    try {
      await wrapper.get('[data-testid="manage"]').trigger('click')
      await flushPromises()
      await wrapper.get('[data-user-field="email"] input').setValue('next@example.test')
      const save = wrapper.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
      await save!.trigger('click')
      await flushPromises()
      expect(requests.filter(item => item.options?.method === 'PATCH')).toHaveLength(1)
      expect(toasts).toEqual([{ title: 'admin.users.accountSaved', color: 'success', icon: 'i-lucide-check-circle', duration: 5000 }])
    } finally {
      wrapper.unmount()
    }
  })
})
