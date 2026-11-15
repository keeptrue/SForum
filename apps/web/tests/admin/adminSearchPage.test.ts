import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * 搜索索引管理页（`/control-panel/search`）的四条审计项：
 *
 * - UX2-05：403 / 500 与「从未重建过」必须可分。旧实现两个 catch 都空转，页面看起来
 *   永远像「没有记录」。
 * - UX2-06：`SFBadge` / `SFButton` 没有 `color` / `icon` prop。徽标必须真的随状态变色
 *   （`failed` → `danger`），按钮图标走 `#leading` 插槽。
 * - UX2-18：历史表 5 列在窄屏要能横向滚动，不能撑破页面。
 * - UX2-23：后台统一几何 —— `text-xl` 紧凑标题 + registry icon，简介只在工具栏出现一次。
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

const IconStub = { props: ['name'], template: '<i :data-icon="name" />' }
const ToolbarStub = { template: '<div class="toolbar"><slot name="left" /><slot name="right" /></div>' }
const CardStub = { template: '<section class="card"><slot name="header" /><slot /></section>' }
const ProgressStub = { props: ['value'], template: '<div class="progress" />' }
const ModalStub = { template: '<div class="modal"><slot name="content" /></div>' }
const LinkStub = { props: ['to'], template: '<a><slot /></a>' }
const AlertStub = {
  props: {
    color: { type: String, default: '' },
    variant: { type: String, default: '' },
    icon: { type: String, default: '' },
    title: { type: String, default: '' },
    description: { type: String, default: '' }
  },
  template: '<div class="alert" :data-color="color">{{ title }}{{ description }}<slot name="actions" /></div>'
}
const ButtonStub = { props: ['loading', 'disabled'], emits: ['click'], template: '<button type="button" @click="$emit(\'click\')"><slot /></button>' }

const RealBadge = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/SFBadge.vue', import.meta.url)),
  'sf-badge',
  { computed: testVue.computed }
)
const RealButton = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/SFButton.vue', import.meta.url)),
  'sf-button',
  { computed: testVue.computed }
)

let asyncDataMode: 'run' | 'payload' = 'run'
let asyncDataPayload: unknown = null

const { reindexHistoryLoadKind, reindexStatusBadgeVariant, reindexStatusLoadKind } = await import('../../app/utils/admin/adminSearch')
const { apiErrorStatusCode } = await import('../../app/composables/useApiClient')

type ApiState = {
  status?: () => Promise<unknown>
  runs?: () => Promise<unknown>
}

function makeApi(state: ApiState) {
  return {
    getReindexStatus: () => state.status ? state.status() : Promise.resolve(null),
    listReindexRuns: () => state.runs ? state.runs() : Promise.resolve([]),
    reindexSearch: async () => ({})
  }
}

let api = makeApi({})

const SearchPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/search.vue', import.meta.url)),
  'admin-search-page',
  {
    useAdminRoutes: () => ({ path: (path: string) => path }),
    apiErrorMessage: (error: unknown) => (error as { data?: { message?: string } })?.data?.message || '',
    apiErrorStatusCode,
    useAdminPage: () => ({ id: '/search', icon: 'i-lucide-search', labelKey: 'admin.nav.search' }),
    createAdminForumApi: () => api,
    useApiClient: () => ({ request: async () => ({}) }),
    useSiteDateTime: () => ({ format: (value?: string) => value || '' }),
    reindexHistoryLoadKind,
    reindexStatusLoadKind,
    reindexStatusBadgeVariant,
    ref: testVue.ref,
    computed: testVue.computed,
    watch: testVue.watch,
    onMounted: testVue.onMounted,
    onBeforeUnmount: testVue.onBeforeUnmount,
    useAsyncData: async (_key: string, handler: () => Promise<unknown>) => {
      // 两种模式：正常执行回调；或模拟「payload 命中」——回调根本不跑，
      // 只有序列化后的数据回到客户端（这条能抓住删掉写回块）。
      if (asyncDataMode === 'payload') return { data: testVue.ref(asyncDataPayload) }
      return { data: testVue.ref(await handler()) }
    }
  }
)

// 页面 `<script setup>` 里有顶层 await（`await useAsyncData`）→ setup 是 async 的，
// 必须挂在 `<Suspense>` 下才会真正渲染。
const PageHarness = testVue.defineComponent({
  components: { SearchPage },
  template: '<Suspense><SearchPage /></Suspense>'
})

function mountPage() {
  return mount(PageHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        SFCard: CardStub,
        SFProgress: ProgressStub,
        SFBadge: RealBadge,
        SFButton: RealButton,
        UModal: ModalStub,
        NuxtLink: LinkStub,
        UAlert: AlertStub,
        UButton: ButtonStub
      }
    }
  })
}

describe('UX2-05 错误态与空态可分', () => {
  test('拿不到状态码（0）算错误，不算「从未重建过」', () => {
    expect(reindexStatusLoadKind(0)).toBe('error')
    expect(reindexHistoryLoadKind(0)).toBe('error')
    expect(reindexStatusLoadKind(404)).toBe('empty')
    expect(reindexStatusLoadKind(403)).toBe('forbidden')
    expect(reindexStatusLoadKind(500)).toBe('error')
  })

  test('404 是空态：不报错，明确说「尚未重建过」', async () => {
    api = makeApi({
      status: async () => { throw { statusCode: 404, data: { code: 404, message: 'no run' } } },
      runs: async () => []
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      expect(wrapper.find('[data-testid="admin-search-status-error"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="admin-search-status-empty"]').exists()).toBe(true)
      expect(wrapper.text()).toContain('admin.search.current.empty')
    } finally {
      wrapper.unmount()
    }
  })

  test('403 是权限错误：给出明确文案，不再伪装成空态', async () => {
    api = makeApi({
      status: async () => { throw { statusCode: 403, data: { code: 403, message: 'forbidden' } } },
      runs: async () => []
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const alert = wrapper.get('[data-testid="admin-search-status-error"]')
      expect(alert.text()).toContain('admin.search.forbidden')
      // 只说「缺权限」不够：必须给出下一步（找管理员 / 重新登录）。
      expect(alert.text()).toContain('admin.search.forbiddenHint')
      expect(wrapper.find('[data-testid="admin-search-status-empty"]').exists()).toBe(false)
      // 403 是「无权限」专属态：重试按钮不该出现（权限不会因为重试而出现）。
      expect(alert.find('button').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('状态请求 500 是「可重试」错误：有重试按钮、没有权限提示', async () => {
    api = makeApi({
      status: async () => { throw { statusCode: 500, data: { code: 500, message: 'search backend down' } } },
      runs: async () => []
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const alert = wrapper.get('[data-testid="admin-search-status-error"]')
      expect(alert.text()).toContain('search backend down')
      expect(alert.text()).not.toContain('admin.search.forbiddenHint')
      // 与 403 互为反面：500 必须给重试入口。
      expect(alert.find('button').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('历史列表失败不再静默：错误态 + 重试', async () => {
    api = makeApi({
      status: async () => null,
      runs: async () => { throw { statusCode: 500, data: { code: 500, message: 'index unavailable' } } }
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const alert = wrapper.get('[data-testid="admin-search-history-error"]')
      expect(alert.text()).toContain('index unavailable')
      expect(alert.find('button').exists()).toBe(true)
      // 关键：错误态与「暂无重建记录」不能同屏 —— 否则 403/500 仍与「从未重建」不可区分。
      expect(wrapper.find('[data-testid="admin-search-history-empty"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('首屏数据与轮询', () => {
  test('payload 命中（回调不跑）时仍渲染首屏数据', async () => {
    asyncDataMode = 'payload'
    asyncDataPayload = {
      status: {
        id: 7,
        status: 'completed',
        total: 9,
        processed: 9,
        remaining: 0,
        percent: 100,
        startedAt: '2026-11-15T10:00:00Z',
        finishedAt: '2026-11-15T10:01:00Z'
      },
      runs: [{ id: 7, status: 'completed', total: 9, startedAt: '2026-11-15T10:00:00Z', finishedAt: '2026-11-15T10:01:00Z' }],
      statusError: '',
      statusForbidden: false,
      historyError: '',
      historyForbidden: false
    }
    // 回调不跑 → 组件 ref 只能靠 payload 写回填充。
    api = makeApi({
      status: async () => { throw new Error('handler must not run') },
      runs: async () => { throw new Error('handler must not run') }
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      expect(wrapper.findAll('[data-testid="admin-search-history-scroll"] tbody tr')).toHaveLength(1)
      expect(wrapper.get('.sf-badge').classes()).toContain('sf-badge--success')
    } finally {
      asyncDataMode = 'run'
      asyncDataPayload = null
      wrapper.unmount()
    }
  })

  test('轮询 tick 同时刷新状态与历史（不能只刷状态）', async () => {
    let running = true
    let runsCalls = 0
    api = makeApi({
      status: async () => ({
        id: 1,
        status: running ? 'running' : 'completed',
        total: 10,
        processed: running ? 3 : 10,
        remaining: running ? 7 : 0,
        percent: running ? 30 : 100,
        startedAt: '2026-11-15T10:00:00Z',
        finishedAt: running ? null : '2026-11-15T10:01:00Z'
      }),
      runs: async () => {
        runsCalls += 1
        return [{ id: 1, status: running ? 'running' : 'completed', total: 10, startedAt: '2026-11-15T10:00:00Z', finishedAt: null }]
      }
    })

    // 捕获页面注册的轮询 tick，手动驱动一次（不依赖真实 2s 计时器）。
    let tick: (() => Promise<void>) | null = null
    const originalSetInterval = globals.setInterval
    const originalClearInterval = globals.clearInterval
    globals.setInterval = ((handler: () => Promise<void>) => {
      tick = handler
      return 99
    }) as never
    globals.clearInterval = (() => {}) as never

    const wrapper = mountPage()
    try {
      await flushPromises()
      const runsAfterMount = runsCalls
      expect(runsAfterMount).toBeGreaterThan(0)
      expect(typeof tick).toBe('function')

      running = false
      await tick!()
      await flushPromises()

      // 重建刚完成：历史必须被重新拉取，否则表格停在旧记录上。
      expect(runsCalls).toBe(runsAfterMount + 1)
      expect(wrapper.findAll('[data-testid="admin-search-history-scroll"] tbody tr')).toHaveLength(1)
    } finally {
      globals.setInterval = originalSetInterval
      globals.clearInterval = originalClearInterval
      wrapper.unmount()
    }
  })
})

describe('UX2-06 徽标与按钮只用真实 prop', () => {
  test('状态徽标随状态变色：completed=success / failed=danger / running=info', async () => {
    api = makeApi({
      status: async () => null,
      runs: async () => [
        { id: 3, status: 'completed', total: 120, startedAt: '2026-11-15T10:00:00Z', finishedAt: '2026-11-15T10:01:00Z' },
        { id: 2, status: 'failed', total: 0, startedAt: '2026-11-15T09:00:00Z', finishedAt: '2026-11-15T09:00:30Z' },
        { id: 1, status: 'running', total: 40, startedAt: '2026-11-15T08:00:00Z', finishedAt: null }
      ]
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const badges = wrapper.findAll('.sf-badge')
      expect(badges.map(badge => badge.classes().join(' '))).toEqual([
        'sf-badge sf-badge--success',
        'sf-badge sf-badge--danger',
        'sf-badge sf-badge--info'
      ])
      // `error` 不是合法 variant：一旦有人把 `failed` 映射回 `error`，这里立刻红。
      expect(wrapper.html()).not.toContain('sf-badge--error')
    } finally {
      wrapper.unmount()
    }
  })

  test('重建按钮的图标走 #leading 插槽，而不是不存在的 icon prop', async () => {
    api = makeApi({ status: async () => null, runs: async () => [] })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const button = wrapper.get('.sf-button')
      expect(button.find('.sf-button__icon').exists()).toBe(true)
      expect(button.find('.sf-button__icon').html()).toContain('data-icon="i-lucide-refresh-cw"')
      // SFButton 没有 color prop：属性若被传进来会落在 DOM 上。
      expect(button.attributes('color')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})

describe('UX2-18 历史表横向滚动容器', () => {
  test('表格包在 overflow-x-auto 里并带最小宽度', async () => {
    api = makeApi({
      status: async () => null,
      runs: async () => [
        { id: 1, status: 'completed', total: 1, startedAt: '2026-11-15T10:00:00Z', finishedAt: '2026-11-15T10:01:00Z' }
      ]
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const scroller = wrapper.get('[data-testid="admin-search-history-scroll"]')
      expect(scroller.classes()).toContain('overflow-x-auto')
      const table = scroller.get('table')
      expect(table.classes()).toContain('min-w-[640px]')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('UX2-23 后台统一几何', () => {
  test('紧凑标题 + registry icon，简介只在工具栏出现一次', async () => {
    // 必须带一条「当前重建」数据：否则卡片内那段（曾经的重复标题）根本不会渲染，
    // 重复键断言就抓不到。
    api = makeApi({
      status: async () => ({
        id: 3,
        status: 'completed',
        total: 10,
        processed: 10,
        remaining: 0,
        percent: 100,
        startedAt: '2026-11-15T10:00:00Z',
        finishedAt: '2026-11-15T10:01:00Z'
      }),
      runs: async () => []
    })
    const wrapper = mountPage()
    try {
      await flushPromises()
      const heading = wrapper.get('h2')
      expect(heading.classes()).toContain('text-xl')
      expect(heading.find('[data-icon="i-lucide-search"]').exists()).toBe(true)
      // 页面标题只出现一次；卡片头不再重复同一个标题。
      expect(wrapper.text().split('admin.search.title').length - 1).toBe(1)
      // 简介（description）的唯一落点是工具栏。
      expect(wrapper.get('.toolbar').text()).toContain('admin.search.description')
      expect(wrapper.text().split('admin.search.description').length - 1).toBe(1)
      // 同一个 i18n 键不得在页面上渲染两次（卡头与卡内曾各来一遍）。
      const text = wrapper.text()
      for (const key of ['admin.search.title', 'admin.search.current.title', 'admin.search.history.title']) {
        expect(text.split(key).length - 1).toBe(1)
      }
    } finally {
      wrapper.unmount()
    }
  })
})
