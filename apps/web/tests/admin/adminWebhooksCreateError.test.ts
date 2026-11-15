import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * UX2-13：Webhook 新建失败的错误提示必须渲染在模态**内部**。
 *
 * 旧实现把 `errorMessage` 同时用于「加载失败」和「新建失败」，而它的渲染位置在模态外；
 * 模态一打开，焦点被捕获、遮罩盖住页级提示，运营看到的是「点了创建什么都没发生」。
 *
 * 现在的契约是拆成两个 ref：`loadError` 留在页面（加载/启停/删除失败），
 * `modalError` 渲染在模态内表单上方（新建失败）。两条路径都要有行为证据。
 */

type ToastPayload = { color?: string, duration?: number, title?: string }

const globals = globalThis as Record<string, unknown>
const saved = { useI18n: globals.useI18n, useToast: globals.useToast }
afterAll(() => {
  globals.useI18n = saved.useI18n
  globals.useToast = saved.useToast
})

const toasts: ToastPayload[] = []
globals.useI18n = () => ({ t: (key: string) => key })
globals.useToast = () => ({ add: (payload: ToastPayload) => toasts.push(payload) })

const AlertStub = {
  props: { color: { type: String, default: '' }, variant: { type: String, default: '' }, icon: { type: String, default: '' }, title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="alert" :data-color="color">{{ title }}</div>'
}
const ModalStub = { template: '<div class="modal"><slot name="content" /></div>' }
const ButtonStub = {
  props: ['disabled', 'loading', 'icon', 'color', 'variant', 'size', 'leadingIcon'],
  emits: ['click'],
  template: '<button type="button" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
}
const InputStub = {
  props: ['modelValue', 'type', 'placeholder'],
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
}
const FieldStub = { template: '<label><slot /></label>' }
const BadgeStub = { props: ['color', 'variant'], template: '<span><slot /></span>' }
const IconStub = { props: ['name'], template: '<i />' }

type Call = { path: string, method: string }
const calls: Call[] = []
let handlers: { get?: () => Promise<unknown>, post?: () => Promise<unknown> } = {}

const request = async (path: string, options: { method?: string } = {}) => {
  const method = options.method || 'GET'
  calls.push({ path, method })
  if (method === 'GET') {
    if (handlers.get) return handlers.get()
    if (path.includes('/endpoints')) return { items: [] }
    if (path.includes('/deliveries')) return { items: [] }
    return { items: [] }
  }
  if (handlers.post) return handlers.post()
  return {}
}

const WebhooksPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/webhooks.vue', import.meta.url)),
  'admin-webhooks-page',
  {
    useAuthSession: () => ({ can: () => true }),
    useAdminPage: () => ({ id: '/webhooks', icon: 'i-lucide-webhook', labelKey: 'admin.nav.webhooks' }),
    apiErrorMessage: (error: unknown) => (error as { data?: { message?: string } })?.data?.message || '',
    useApiClient: () => ({ request }),
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    onMounted: testVue.onMounted
  }
)

function mountPage() {
  return mount(WebhooksPage, {
    global: {
      stubs: {
        UAlert: AlertStub,
        UModal: ModalStub,
        UButton: ButtonStub,
        UInput: InputStub,
        UFormField: FieldStub,
        UBadge: BadgeStub,
        UIcon: IconStub
      }
    }
  })
}

async function openCreateAndSubmit(wrapper: ReturnType<typeof mountPage>) {
  const createTrigger = wrapper.findAll('button').find(button => button.text() === 'admin.webhooks.create')
  if (!createTrigger) throw new Error('找不到「新建端点」按钮')
  await createTrigger.trigger('click')
  await flushPromises()

  const inputs = wrapper.get('.modal').findAll('input')
  await inputs[0]!.setValue('ops-hook')
  await inputs[1]!.setValue('https://example.com/hooks')
  await flushPromises()

  const submit = wrapper.get('.modal').findAll('button').filter(button => button.text() === 'admin.webhooks.create').at(-1)
  if (!submit) throw new Error('找不到模态内的创建按钮')
  await submit.trigger('click')
  await flushPromises()
}

describe('UX2-13 新建失败提示在模态内', () => {
  test('创建失败：错误只出现在模态内部，页级提示保持干净', async () => {
    toasts.length = 0
    calls.length = 0
    handlers = {
      post: async () => {
        throw { statusCode: 422, data: { code: 422, message: 'target_url must be https' } }
      }
    }
    const wrapper = mountPage()
    try {
      await flushPromises()
      await openCreateAndSubmit(wrapper)

      const modalAlerts = wrapper.get('.modal').findAll('.alert')
      expect(modalAlerts).toHaveLength(1)
      expect(modalAlerts[0]!.text()).toBe('target_url must be https')
      // 页级（模态外）不得出现同一条错误：它会被遮罩挡住，等于没提示。
      // 页面上常驻的「推荐用法」提示不是错误色，所以按 data-color 过滤。
      const errorAlerts = wrapper.findAll('.alert').filter(alert => alert.attributes('data-color') === 'error')
      expect(errorAlerts).toHaveLength(1)
      expect(wrapper.get('[data-testid="admin-webhooks-create-error"]').text()).toBe('target_url must be https')
    } finally {
      wrapper.unmount()
    }
  })

  test('加载失败仍留在页级：不能藏进（默认关闭的）模态', async () => {
    calls.length = 0
    handlers = {
      get: async () => {
        throw { statusCode: 500, data: { code: 500, message: 'endpoint store unavailable' } }
      }
    }
    const wrapper = mountPage()
    try {
      await flushPromises()
      const errorAlerts = wrapper.findAll('.alert').filter(alert => alert.attributes('data-color') === 'error')
      expect(errorAlerts).toHaveLength(1)
      expect(errorAlerts[0]!.text()).toBe('endpoint store unavailable')
      expect(wrapper.get('.modal').findAll('.alert')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('重新打开模态会清掉上一次的失败提示', async () => {
    calls.length = 0
    handlers = {
      post: async () => {
        throw { statusCode: 422, data: { code: 422, message: 'name is required' } }
      }
    }
    const wrapper = mountPage()
    try {
      await flushPromises()
      await openCreateAndSubmit(wrapper)
      expect(wrapper.get('.modal').findAll('.alert')).toHaveLength(1)

      // 关闭（模态内的取消按钮）→ 重新打开：旧错误必须消失。
      const cancel = wrapper.get('.modal').findAll('button').find(button => button.text() === 'admin.common.cancel')
      await cancel!.trigger('click')
      await flushPromises()
      const reopen = wrapper.findAll('button').find(button => button.text() === 'admin.webhooks.create')
      await reopen!.trigger('click')
      await flushPromises()
      expect(wrapper.get('.modal').findAll('.alert')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})
