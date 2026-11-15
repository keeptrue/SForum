import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * 审核管理页两条审计项：
 *
 * - UX2-04：设置弹窗的三态必须可分。旧实现在 `settings` 为空时无条件渲染骨架，
 *   于是 403（没有权限）与 500（加载失败）都表现为「永远转圈」。
 * - UX2-20：审核记录固定只取前 30 条、`total` 被丢弃、没有分页控件 ——
 *   更早的审核决定在页面上完全不可达。
 */

const globals = globalThis as Record<string, unknown>
const saved = { useI18n: globals.useI18n }
afterAll(() => {
  globals.useI18n = saved.useI18n
})
globals.useI18n = () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key })

const { apiErrorStatusCode } = await import('../../app/composables/useApiClient')

type HistoryCall = { page?: number, perPage?: number, admin?: boolean }
const historyCalls: HistoryCall[] = []

let settingsHandler: () => Promise<unknown> = async () => ({ mode: 'rules' })
let historyHandler: (filters: HistoryCall) => Promise<unknown> = async () => ({ items: [], total: 0, page: 1, perPage: 30 })

const moderationApi = {
  getSettings: () => settingsHandler(),
  updateSettings: async (input: unknown) => input,
  resetSettings: async () => ({ mode: 'off' }),
  listHistory: (filters: HistoryCall = {}, admin = false) => {
    historyCalls.push({ ...filters, admin })
    return historyHandler(filters)
  }
}

/**
 * `useAsyncData` 的最小替身：立即跑一次 + 支持 `watch` 源变化时重跑。
 * 分页的行为证据就落在这里 —— 点第 2 页必须真的以 `page: 2` 重新取数。
 */
function useAsyncDataStub(
  _key: string,
  handler: () => Promise<unknown>,
  options: { default?: () => unknown, watch?: unknown[] } = {}
) {
  const data = testVue.ref(options.default ? options.default() : undefined)
  const pending = testVue.ref(true)
  const error = testVue.ref(null)
  const run = async () => {
    pending.value = true
    try {
      data.value = await handler()
      error.value = null
    } catch (cause) {
      error.value = cause
    } finally {
      pending.value = false
    }
  }
  for (const source of options.watch || []) {
    testVue.watch(source as never, () => { void run() })
  }
  void run()
  return { data, pending, error, refresh: run }
}

const IconStub = { props: ['name'], template: '<i />' }
const ToolbarStub = { template: '<div class="toolbar"><slot name="left" /><slot name="right" /></div>' }
const ButtonStub = { props: ['loading', 'disabled', 'icon'], emits: ['click'], template: '<button type="button" @click="$emit(\'click\')"><slot /></button>' }
const BadgeStub = { props: ['color', 'variant'], template: '<span><slot /></span>' }
const AlertStub = {
  props: { color: { type: String, default: '' }, variant: { type: String, default: '' }, icon: { type: String, default: '' }, title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="alert" :data-color="color">{{ title }}</div>'
}
const CardStub = { template: '<section class="card"><slot name="header" /><slot /></section>' }
const ModalStub = { template: '<div class="modal"><slot name="content" /></div>' }
const SkeletonStub = { props: ['width', 'height'], template: '<div class="skeleton" />' }
const PaginationStub = {
  props: { page: { type: Number, default: 1 }, totalPages: { type: Number, default: 1 } },
  emits: ['update:page'],
  template: `<nav class="pagination" :data-page="page" :data-total-pages="totalPages">
    <button type="button" data-testid="pagination-next" @click="$emit('update:page', page + 1)">next</button>
  </nav>`
}
const TableStub = { props: ['items', 'loading'], template: '<div class="decision-table" :data-count="items?.length ?? 0" />' }
const SettingsFormStub = { props: ['modelValue'], template: '<div class="settings-form" />' }

const ModerationPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/moderation.vue', import.meta.url)),
  'admin-moderation-page',
  {
    useModerationApi: () => moderationApi,
    useAdminPage: () => ({ id: '/moderation', icon: 'i-lucide-shield-check', labelKey: 'admin.nav.moderation' }),
    apiErrorMessage: (error: unknown) => (error as { data?: { message?: string } })?.data?.message || '',
    apiErrorStatusCode,
    ModerationDecisionTable: TableStub,
    ModerationSettingsForm: SettingsFormStub,
    ref: testVue.ref,
    computed: testVue.computed,
    useAsyncData: useAsyncDataStub,
    useSeoMeta: () => {}
  }
)

// 页面 setup 是 async（顶层 await useAsyncData），必须挂在 Suspense 下。
const PageHarness = testVue.defineComponent({
  components: { ModerationPage },
  template: '<Suspense><ModerationPage /></Suspense>'
})

function mountPage() {
  return mount(PageHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        UBadge: BadgeStub,
        UAlert: AlertStub,
        UCard: CardStub,
        UModal: ModalStub,
        SFSkeleton: SkeletonStub,
        SFPagination: PaginationStub,
        UFormField: { template: '<label><slot /></label>' }
      }
    }
  })
}

async function openSettings(wrapper: ReturnType<typeof mountPage>) {
  const trigger = wrapper.findAll('button').find(button => button.text() === 'admin.moderation.openSettings')
  if (!trigger) throw new Error('找不到「策略设置」按钮')
  await trigger.trigger('click')
  await flushPromises()
}

describe('UX2-04 设置弹窗三态', () => {
  test('403：明确说没有权限，不转圈，并给重试入口', async () => {
    historyCalls.length = 0
    settingsHandler = async () => {
      throw { statusCode: 403, data: { code: 403, message: 'forbidden' } }
    }
    historyHandler = async () => ({ items: [], total: 0, page: 1, perPage: 30 })
    const wrapper = mountPage()
    try {
      await flushPromises()
      await openSettings(wrapper)

      const panel = wrapper.get('[data-testid="admin-moderation-settings-error"]')
      expect(panel.text()).toContain('admin.moderation.settingsForbidden')
      expect(wrapper.get('.modal').findAll('.skeleton')).toHaveLength(0)
      expect(panel.findAll('button').length).toBeGreaterThan(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('500：给错误原因与重试，而不是永久骨架', async () => {
    settingsHandler = async () => {
      throw { statusCode: 500, data: { code: 500, message: 'settings backend down' } }
    }
    historyHandler = async () => ({ items: [], total: 0, page: 1, perPage: 30 })
    const wrapper = mountPage()
    try {
      await flushPromises()
      await openSettings(wrapper)

      expect(wrapper.get('[data-testid="admin-moderation-settings-error"]').text()).toContain('settings backend down')
      expect(wrapper.get('.modal').findAll('.skeleton')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('加载中：只有此时才显示骨架', async () => {
    let release: (value: unknown) => void = () => {}
    settingsHandler = () => new Promise(resolve => { release = resolve })
    historyHandler = async () => ({ items: [], total: 0, page: 1, perPage: 30 })
    const wrapper = mountPage()
    try {
      await flushPromises()
      await openSettings(wrapper)

      expect(wrapper.get('.modal').findAll('.skeleton').length).toBeGreaterThan(0)
      expect(wrapper.find('[data-testid="admin-moderation-settings-error"]').exists()).toBe(false)

      release({ mode: 'rules' })
      await flushPromises()
      expect(wrapper.get('.modal').findAll('.skeleton')).toHaveLength(0)
      expect(wrapper.get('.modal').find('.settings-form').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('UX2-20 审核记录分页', () => {
  test('total 参与分页：65 条 / 每页 30 → 3 页，切页按 page=2 重新取数', async () => {
    settingsHandler = async () => ({ mode: 'rules' })
    historyCalls.length = 0
    historyHandler = async (filters) => ({
      items: Array.from({ length: filters.page === 2 ? 30 : 30 }, (_, index) => ({ id: (filters.page ?? 1) * 100 + index })),
      total: 65,
      page: filters.page ?? 1,
      perPage: filters.perPage ?? 30
    })
    const wrapper = mountPage()
    try {
      await flushPromises()

      const pagination = wrapper.get('.pagination')
      expect(pagination.attributes('data-total-pages')).toBe('3')
      expect(pagination.attributes('data-page')).toBe('1')
      expect(wrapper.text()).toContain('admin.moderation.history.page')
      expect(historyCalls[0]).toMatchObject({ page: 1, perPage: 30, admin: true })

      await wrapper.get('[data-testid="pagination-next"]').trigger('click')
      await flushPromises()

      expect(wrapper.get('.pagination').attributes('data-page')).toBe('2')
      expect(historyCalls.at(-1)).toMatchObject({ page: 2, perPage: 30, admin: true })
    } finally {
      wrapper.unmount()
    }
  })

  test('只有一页时不渲染分页控件', async () => {
    settingsHandler = async () => ({ mode: 'rules' })
    historyCalls.length = 0
    historyHandler = async () => ({ items: [{ id: 1 }], total: 1, page: 1, perPage: 30 })
    const wrapper = mountPage()
    try {
      await flushPromises()
      expect(wrapper.find('.pagination').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
