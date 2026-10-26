import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'

const componentPath = fileURLToPath(
  new URL('../../app/components/admin/identity/users/SFAdminUserAccountSection.vue', import.meta.url)
)
const AccountSection = await compileVueSfc(componentPath, 'admin-user-account-section', {
  // 邮箱验证子组件在编译时被剥掉 import，用绑定注入一个空壳即可。
  SFAdminUserEmailVerificationControl: { template: '<div data-testid="email-verification" />' }
})

Object.assign(globalThis, {
  computed: testVue.computed,
  useI18n: () => ({ t: (key: string) => key })
})

const baseUser = {
  id: 7,
  username: 'alice',
  email: 'alice@example.test',
  displayName: 'Alice',
  locale: 'zh-CN',
  status: 'active',
  kind: 'human',
  isInitialSuperAdmin: false,
  roleKeys: ['member'],
  permissionOverrides: { allow: [], deny: [] },
  profile: { bio: '', signature: '', location: '', websiteUrl: '' },
  nicknameHistory: []
}

function createForm() {
  return testVue.reactive({
    username: 'alice',
    email: 'alice@example.test',
    displayName: 'Alice',
    locale: 'zh-CN',
    status: 'active',
    kind: 'human',
    bio: '',
    signature: '',
    location: '',
    websiteUrl: '',
    newPassword: '',
    confirmPassword: ''
  })
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

function mountSection(errors: Record<string, string[]>, cleared: string[] = [], dirty = true) {
  return mount(AccountSection, {
    props: {
      user: baseUser,
      form: createForm(),
      errors,
      saving: false,
      dirty,
      clearError: (name: string) => { cleared.push(name) }
    },
    global: {
      stubs: {
        UFormField: fieldStub,
        UInput: inputStub,
        USelect: inputStub,
        UButton: { template: '<button @click="$emit(\'click\')"><slot /></button>' }
      }
    }
  })
}

describe('admin user account section', () => {
  test('renders the backend field message under its own field', () => {
    const wrapper = mountSection({ email: ['邮箱格式不正确。'] })
    try {
      expect(wrapper.get('[data-user-field="email"] .field-error').text()).toBe('邮箱格式不正确。')
      expect(wrapper.findAll('.field-error')).toHaveLength(1)
      expect(wrapper.find('[data-user-field="username"] .field-error').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps every backend field key addressable by the focus order', () => {
    const wrapper = mountSection({ displayName: ['显示名不能为空。'], websiteUrl: ['网址格式不正确。'] })
    try {
      expect(wrapper.get('[data-user-field="displayName"] .field-error').text()).toBe('显示名不能为空。')
      expect(wrapper.find('[data-user-field="websiteUrl"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('disables save until the operator changes something', () => {
    const clean = mountSection({}, [], false)
    const changed = mountSection({}, [], true)
    try {
      const cleanButton = clean.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
      const changedButton = changed.findAll('button').find(item => item.text().includes('admin.users.saveAccount'))
      expect(cleanButton!.attributes('disabled')).toBeDefined()
      expect(changedButton!.attributes('disabled')).toBeUndefined()
    } finally {
      clean.unmount()
      changed.unmount()
    }
  })

  test('clears a field error when the operator edits that field', async () => {
    const cleared: string[] = []
    const wrapper = mountSection({ email: ['邮箱格式不正确。'] }, cleared)
    try {
      await wrapper.get('[data-user-field="email"] input').setValue('alice@example.com')
      expect(cleared).toEqual(['email'])
    } finally {
      wrapper.unmount()
    }
  })

})
