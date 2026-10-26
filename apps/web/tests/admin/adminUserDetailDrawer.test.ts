import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { useAdminUserDetailEditor } from '../../app/composables/admin/identity/useAdminUserDetailEditor'
import { useAdminUserSecurity } from '../../app/composables/admin/identity/useAdminUserSecurity'

const drawerPath = fileURLToPath(
  new URL('../../app/components/admin/identity/users/SFAdminUserDetailDrawer.vue', import.meta.url)
)
const accountPath = fileURLToPath(
  new URL('../../app/components/admin/identity/users/SFAdminUserAccountSection.vue', import.meta.url)
)

const AccountSection = await compileVueSfc(accountPath, 'drawer-account-section', {
  SFAdminUserEmailVerificationControl: { template: '<div />' }
})

const passthrough = { template: '<div />' }

const Drawer = await compileVueSfc(drawerPath, 'admin-user-detail-drawer', {
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

const baseUser = {
  id: 425,
  username: 'qa_generated_1027',
  email: 'qa-generated-1027@example.test',
  emailVerified: false,
  displayName: 'QA Generated',
  locale: 'zh-CN',
  status: 'active',
  isInitialSuperAdmin: false,
  roleKeys: ['member'],
  permissions: [],
  permissionOverrides: { allow: [], deny: [] },
  profile: { bio: '', signature: '', location: '', websiteUrl: '' },
  nicknameHistory: []
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

function installGlobals(requests: Array<{ path: string, options?: Record<string, unknown> }>, toasts: Record<string, unknown>[]) {
  Object.assign(globalThis, {
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    nextTick: testVue.nextTick,
    useI18n: () => ({ t: (key: string) => key }),
    useToast: () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) }),
    usePermissionText: () => ({
      permissionLabel: (permission: { key: string }) => permission.key,
      permissionDescription: () => '',
      permissionModuleLabel: (module: string) => module
    }),
    useApiClient: () => ({
      request: async (path: string, options?: Record<string, unknown>) => {
        requests.push({ path, options })
        if (options?.method === 'PATCH') {
          // 后端契约：422 + data.fields 携带本地化字段消息。
          throw {
            code: 422,
            message: '用户更新内容不正确，请检查账户和资料字段后重试。',
            data: {
              reason: 'user.invalid_update',
              fields: { email: ['邮箱格式不正确。'] }
            }
          }
        }
        return baseUser
      }
    })
  })
}

function mountDrawer() {
  const requests: Array<{ path: string, options?: Record<string, unknown> }> = []
  const toasts: Record<string, unknown>[] = []
  installGlobals(requests, toasts)
  const wrapper = mount(Drawer, {
    props: { roles: [], permissions: [] },
    global: {
      stubs: {
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
        UButton: { template: '<button><slot /></button>' },
        USkeleton: true
      }
    }
  })
  return { wrapper, requests, toasts }
}

async function openAndSaveInvalidEmail(wrapper: ReturnType<typeof mount>) {
  await (wrapper.vm as unknown as { open: (user: typeof baseUser) => Promise<void> }).open(baseUser)
  await flushPromises()
  await wrapper.get('[data-user-field="email"] input').setValue('not-an-email')
  const save = wrapper.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
  await save!.trigger('click')
  await flushPromises()
}

describe('admin user detail drawer errors', () => {
  test('renders the backend field message and the summary inside the drawer', async () => {
    const { wrapper } = mountDrawer()
    try {
      await openAndSaveInvalidEmail(wrapper)
      expect(wrapper.get('[data-user-field="email"] .field-error').text()).toBe('邮箱格式不正确。')
      expect(wrapper.get('[data-testid="drawer"] .alert').text()).toContain('admin.users.fieldFallback')
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the failure out of page-level alerts and error toasts', async () => {
    const { wrapper, toasts } = mountDrawer()
    try {
      await openAndSaveInvalidEmail(wrapper)
      expect(toasts.filter(toast => toast.color === 'error')).toHaveLength(0)
      // 抽屉不向页面冒错误事件：列表区红条只服务列表加载/筛选失败。
      expect(Object.keys(wrapper.emitted())).not.toContain('error')
    } finally {
      wrapper.unmount()
    }
  })

  test('does not call the API when nothing changed', async () => {
    const { wrapper, requests } = mountDrawer()
    try {
      await (wrapper.vm as unknown as { open: (user: typeof baseUser) => Promise<void> }).open(baseUser)
      await flushPromises()
      const save = wrapper.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
      await save!.trigger('click')
      await flushPromises()
      expect(requests.filter(item => item.options?.method === 'PATCH')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('sends the edited account fields in the PATCH body', async () => {
    const { wrapper, requests } = mountDrawer()
    try {
      await openAndSaveInvalidEmail(wrapper)
      const patches = requests.filter(item => item.options?.method === 'PATCH')
      expect(patches).toHaveLength(1)
      const patch = patches[0]
      expect(patch?.path).toBe('/users/425')
      expect((patch?.options?.body as Record<string, unknown>).email).toBe('not-an-email')
    } finally {
      wrapper.unmount()
    }
  })
})
