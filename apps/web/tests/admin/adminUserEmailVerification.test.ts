import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

const componentPath = fileURLToPath(
  new URL('../../app/components/admin/identity/users/SFAdminUserEmailVerificationControl.vue', import.meta.url)
)
const EmailVerificationControl = await compileVueSfc(componentPath, 'admin-user-email-verification')

const baseUser = {
  id: 42,
  username: 'member',
  email: 'member@example.com',
  emailVerified: false,
  displayName: 'Member',
  locale: 'zh-CN',
  status: 'active',
  isInitialSuperAdmin: false,
  roleKeys: ['member'],
  permissions: [],
  permissionOverrides: { allow: [], deny: [] },
  profile: { bio: '', signature: '', location: '', websiteUrl: '' }
}

function buttonStub() {
  return {
    props: ['disabled', 'loading'],
    emits: ['click'],
    template: '<button :disabled="disabled || loading" @click="$emit(\'click\', $event)"><slot /></button>'
  }
}

type ControlScenario = { failWith?: unknown, apiMessage?: string }

function mountControl(
  emailVerified: boolean,
  requests: Array<{ path: string, options: Record<string, unknown> }>,
  scenario: ControlScenario = {}
) {
  const toasts: Record<string, unknown>[] = []
  Object.assign(globalThis, {
    ref: testVue.ref,
    useI18n: () => ({ t: (key: string) => key }),
    useApiClient: () => ({
      request: async (path: string, options: Record<string, unknown>) => {
        requests.push({ path, options })
        if (scenario.failWith !== undefined) {
          throw scenario.failWith
        }
        return { ...baseUser, emailVerified: (options.body as { verified: boolean }).verified }
      }
    }),
    useToast: () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) }),
    apiErrorMessage: () => scenario.apiMessage ?? ''
  })
  return {
    toasts,
    wrapper: mount(EmailVerificationControl, {
      props: { user: { ...baseUser, emailVerified } },
      global: {
        stubs: {
          UButton: buttonStub(),
          UBadge: { template: '<span><slot /></span>' },
          UAlert: {
            props: ['title', 'description', 'color'],
            template: '<div class="alert" :data-color="color">{{ title }}|{{ description }}</div>'
          },
          UIcon: true,
          UModal: {
            props: ['open'],
            emits: ['update:open'],
            template: '<div v-if="open" data-testid="confirmation"><slot name="content" /></div>'
          }
        }
      }
    })
  }
}

describe('admin user email verification control', () => {
  test('confirms and marks an unverified user as verified', async () => {
    const requests: Array<{ path: string, options: Record<string, unknown> }> = []
    const { wrapper, toasts } = mountControl(false, requests)
    try {
      await wrapper.get('button').trigger('click')
      expect(wrapper.get('[data-testid="confirmation"]').text()).toContain('admin.users.markEmailVerifiedConfirmTitle')
      const confirm = wrapper.findAll('button').filter(button => button.text().includes('admin.users.markEmailVerified')).at(-1)
      await confirm!.trigger('click')
      await flushPromises()
      expect(requests).toEqual([{
        path: '/users/42/email-verification',
        options: { method: 'PUT', body: { verified: true } }
      }])
      expect(wrapper.emitted('updated')?.[0]?.[0]).toMatchObject({ id: 42, emailVerified: true })
      expect(toasts[0]).toMatchObject({ color: 'success', duration: 5000 })
    } finally {
      wrapper.unmount()
    }
  })

  test('confirms and resets a verified user to unverified', async () => {
    const requests: Array<{ path: string, options: Record<string, unknown> }> = []
    const { wrapper } = mountControl(true, requests)
    try {
      await wrapper.get('button').trigger('click')
      expect(wrapper.get('[data-testid="confirmation"]').text()).toContain('admin.users.resetEmailVerificationConfirmTitle')
      const confirm = wrapper.findAll('button').filter(button => button.text().includes('admin.users.resetEmailVerification')).at(-1)
      await confirm!.trigger('click')
      await flushPromises()
      expect(requests[0]).toMatchObject({
        path: '/users/42/email-verification',
        options: { body: { verified: false } }
      })
      expect(wrapper.emitted('updated')?.[0]?.[0]).toMatchObject({ id: 42, emailVerified: false })
    } finally {
      wrapper.unmount()
    }
  })

  // 失败必须留在确认弹窗里：关窗会让人以为「点了没反应」，弹 error toast 又会 5 秒后消失。
  test('keeps a failed verification inside the confirmation dialog', async () => {
    const requests: Array<{ path: string, options: Record<string, unknown> }> = []
    const { wrapper, toasts } = mountControl(false, requests, {
      failWith: {
        code: 503,
        message: '邮件验证服务暂时不可用，请稍后重试。',
        data: { reason: 'auth.email_verification_unavailable' }
      },
      apiMessage: '邮件验证服务暂时不可用，请稍后重试。'
    })
    try {
      await wrapper.get('button').trigger('click')
      const confirm = wrapper.findAll('button').filter(button => button.text().includes('admin.users.markEmailVerified')).at(-1)
      await confirm!.trigger('click')
      await flushPromises()

      expect(requests).toHaveLength(1)
      // 弹窗不关、错误在弹窗里、没有成功事件、没有 toast。
      expect(wrapper.find('[data-testid="confirmation"]').exists()).toBe(true)
      expect(wrapper.get('[data-testid="confirmation"] .alert').text())
        .toContain('邮件验证服务暂时不可用，请稍后重试。')
      expect(wrapper.emitted('updated')).toBeUndefined()
      expect(toasts).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})
