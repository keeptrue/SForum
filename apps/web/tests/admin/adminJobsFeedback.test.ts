import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * UX2-07：后台任务/调度页的错误 toast 不许把原始 fetch 调试串打给运营。
 *
 * 旧实现是 `title: \`${error}\`` —— 一次网络失败会得到
 * `TypeError: fetch failed\n    at ...` 这种字符串，既不可读也泄露内部路径。
 * 现在统一走 `apiErrorMessage(error)`，取不到结构化文案时回落到本地化兜底。
 *
 * UX2-22：任务页页头与工具栏重复同一句 intro。几何基准（settings/index.vue）是
 * 「页头只有紧凑标题，工具栏承载唯一一条简介」，所以同一句文案在整页只能出现一次。
 */

type ToastPayload = { color?: string, duration?: number, title?: string }

const globals = globalThis as Record<string, unknown>
const saved = {
  reactive: globals.reactive,
  ref: globals.ref,
  watch: globals.watch,
  useApiClient: globals.useApiClient,
  useToast: globals.useToast,
  useI18n: globals.useI18n,
  useAsyncData: globals.useAsyncData
}

const toasts: ToastPayload[] = []
let requestImpl: (path: string) => Promise<unknown> = async () => ({})

afterAll(() => {
  for (const [key, value] of Object.entries(saved)) {
    globals[key] = value
  }
})

// 只在安装窗口内替换 Nuxt 自动导入；文件结束统一还原，避免跨文件污染。
globals.reactive = testVue.reactive
globals.ref = testVue.ref
globals.watch = testVue.watch
globals.useApiClient = () => ({ request: (path: string) => requestImpl(path) })
globals.useToast = () => ({ add: (payload: ToastPayload) => toasts.push(payload) })
globals.useI18n = () => ({ t: (key: string) => key })
globals.useAsyncData = () => ({
  data: testVue.ref([]),
  pending: testVue.ref(false),
  error: testVue.ref(null),
  refresh: async () => {}
})

const { useAdminJobs } = await import('../../app/composables/admin/useAdminJobs')

describe('UX2-07 任务操作失败的错误文案', () => {
  test('网络异常（TypeError: fetch failed）不把调试串塞进 toast', async () => {
    toasts.length = 0
    requestImpl = async () => {
      throw new TypeError('fetch failed\n    at fetchWithRetry (/app/internal/http/client.go:42)')
    }
    const manager = useAdminJobs()

    await manager.jobAction(7, 'retry')

    expect(toasts).toHaveLength(1)
    expect(toasts[0]?.color).toBe('error')
    expect(toasts[0]?.duration).toBe(0)
    expect(toasts[0]?.title).toBe('admin.jobs.actionFailed')
    expect(toasts[0]?.title).not.toContain('TypeError')
    expect(toasts[0]?.title).not.toContain('fetch failed')
    expect(toasts[0]?.title).not.toContain('at ')
  })

  test('结构化错误保留服务端文案（不是兜底，也不是原始对象）', async () => {
    toasts.length = 0
    requestImpl = async () => {
      throw { statusCode: 409, data: { code: 409, message: 'job is not retryable' } }
    }
    const manager = useAdminJobs()

    await manager.jobAction(9, 'cancel')

    expect(toasts[0]?.title).toBe('job is not retryable')
    expect(toasts[0]?.duration).toBe(0)
  })

  test('队列暂停/恢复失败同样只给可读文案', async () => {
    toasts.length = 0
    requestImpl = async () => {
      throw new Error('ECONNREFUSED 127.0.0.1:8080')
    }
    const manager = useAdminJobs()

    await manager.queueAction('default', true)

    expect(toasts[0]?.title).toBe('admin.jobs.actionFailed')
    expect(toasts[0]?.title).not.toContain('ECONNREFUSED')
  })
})

// ---------------------------------------------------------------------------
// UX2-22：页头与工具栏不得重复同一句 intro。
// ---------------------------------------------------------------------------

const IconStub = { props: ['name'], template: '<i />' }
const ToolbarStub = { template: '<div class="toolbar"><slot name="left" /><slot name="right" /></div>' }
const ButtonStub = { props: ['loading', 'disabled', 'icon'], emits: ['click'], template: '<button type="button" @click="$emit(\'click\')"><slot /></button>' }
const SelectStub = { props: ['modelValue', 'items'], template: '<select />' }
const InputStub = { props: ['modelValue'], template: '<input />' }
const BadgeStub = { props: ['color'], template: '<span><slot /></span>' }
const ModalStub = { props: ['open'], template: '<div><slot name="content" /></div>' }

const asyncDataStub = <T>(value: T) => ({
  data: testVue.ref(value),
  pending: testVue.ref(false),
  error: testVue.ref(null),
  refresh: async () => {}
})

const managerStub = {
  overview: asyncDataStub({ counts: { available: 1, running: 0, retryable: 0, discarded: 0 }, queues: [] }),
  schedules: asyncDataStub([]),
  jobs: asyncDataStub([]),
  filters: testVue.reactive({ queue: '__all__', state: '__all__', kind: '' }),
  selected: testVue.ref(null),
  busy: testVue.ref(''),
  refresh: async () => {},
  refreshSchedules: async () => {},
  detail: async () => {},
  jobAction: async () => {},
  queueAction: async () => {},
  setScheduleEnabled: async () => {},
  triggerSchedule: async () => null
}

const JobsPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/jobs.vue', import.meta.url)),
  'admin-jobs-page',
  {
    useAuthSession: () => ({ can: () => true }),
    useAdminRoutes: () => ({ path: (path: string) => path }),
    useAdminPage: () => ({ id: '/jobs', icon: 'i-lucide-list', labelKey: 'admin.nav.jobs' }),
    useAdminJobs: () => managerStub,
    ALL_ADMIN_JOBS_FILTER: '__all__',
    jobCanCancel: () => true,
    jobCanRetry: () => true,
    jobStateColor: () => 'success',
    announceAsyncResult: () => {},
    ref: testVue.ref,
    computed: testVue.computed,
    watch: testVue.watch,
    useI18n: () => ({ t: (key: string) => key })
  }
)

describe('UX2-22 任务页 intro 只出现一次', () => {
  test('页头不再重复工具栏那句简介', async () => {
    const wrapper = mount(JobsPage, {
      global: {
        stubs: {
          UIcon: IconStub,
          UDashboardToolbar: ToolbarStub,
          UButton: ButtonStub,
          USelect: SelectStub,
          UInput: InputStub,
          UBadge: BadgeStub,
          UModal: ModalStub
        }
      }
    })
    try {
      await flushPromises()
      const occurrences = wrapper.text().split('admin.jobs.intro').length - 1
      expect(occurrences).toBe(1)
      // 唯一落点必须在工具栏里（几何基准：工具栏承载 intro）。
      expect(wrapper.get('.toolbar').text()).toContain('admin.jobs.intro')
    } finally {
      wrapper.unmount()
    }
  })
})
