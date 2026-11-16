import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileSfcSource, compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import {
  buildAdminForumContentRouteQuery,
  parseAdminForumContentRouteQuery
} from '../../app/utils/admin/adminForumContentRouteQuery'

/**
 * UX2-14：内容管理页的 tab 与 9 个筛选字段必须与地址栏同步。
 *
 * 旧实现把 `activeTab`、`filters` 全放在内存里：切到「评论」+ 填筛选后 F5 就全复位，
 * URL 也分享不出去。现在从 `route.query` 派生、变更走 `router.replace`。
 *
 * 有意取舍（钉在测试名里）：**分页游标不入 query** —— 刷新回到第一页，tab/筛选保持。
 * `?tab=` 必须先夹到 `availableTabs`：没有 `topic.edit_any` 时不能落 `topics`。
 */

describe('UX2-14 地址栏状态解析', () => {
  test('合法 query 原样还原 tab 与筛选', () => {
    const state = parseAdminForumContentRouteQuery({
      tab: 'comments',
      status: 'hidden',
      authorUserID: '42',
      authorUsername: 'alice',
      updatedFrom: '2026-11-01T00:00',
      updatedTo: '2026-11-15T23:59',
      topicID: '7',
      titlePrefix: 'bug',
      categorySlug: 'news',
      perPage: '50'
    }, ['topics', 'comments'])

    expect(state.tab).toBe('comments')
    expect(state.filters).toEqual({
      status: 'hidden',
      authorUserID: 42,
      authorUsername: 'alice',
      updatedFrom: '2026-11-01T00:00',
      updatedTo: '2026-11-15T23:59',
      topicID: 7,
      titlePrefix: 'bug',
      categorySlug: 'news',
      perPage: 50
    })
  })

  test('非法值一律回落默认，不把坏 query 带进请求', () => {
    const state = parseAdminForumContentRouteQuery({
      tab: 'drafts',
      status: 'archived',
      authorUserID: '-3',
      topicID: 'NaN',
      perPage: '999'
    }, ['topics', 'comments'])

    expect(state.tab).toBeNull()
    expect(state.filters.status).toBe('')
    expect(state.filters.authorUserID).toBeUndefined()
    expect(state.filters.topicID).toBeUndefined()
    expect(state.filters.perPage).toBe(20)
  })

  test('?tab= 夹到权限可用分区：无主题权限时 comments 优先', () => {
    const denied = parseAdminForumContentRouteQuery({ tab: 'topics' }, ['comments'])
    expect(denied.tab).toBeNull()

    const allowed = parseAdminForumContentRouteQuery({ tab: 'topics' }, ['topics', 'comments'])
    expect(allowed.tab).toBe('topics')
  })

  test('数组/空值等异常形状不抛错', () => {
    const state = parseAdminForumContentRouteQuery({ tab: ['topics', 'comments'], status: null }, ['topics'])
    expect(state.tab).toBe('topics')
    expect(state.filters.status).toBe('')
  })

  test('写回时省略默认值，URL 保持可读（且不含游标）', () => {
    const query = buildAdminForumContentRouteQuery('topics', {
      status: '',
      authorUsername: 'alice',
      perPage: 20
    })
    expect(query).toEqual({ tab: 'topics', authorUsername: 'alice' })
    expect(query.after).toBeUndefined()
    expect(query.cursor).toBeUndefined()
  })

  test('写回时保留非默认 perPage 与全部筛选字段', () => {
    const query = buildAdminForumContentRouteQuery('comments', {
      status: 'pending',
      authorUserID: 9,
      authorUsername: 'bob',
      updatedFrom: '2026-11-01T00:00',
      updatedTo: '2026-11-15T23:59',
      topicID: 3,
      titlePrefix: 'fix',
      categorySlug: 'dev',
      perPage: 100
    })
    expect(query).toEqual({
      tab: 'comments',
      status: 'pending',
      authorUserID: '9',
      authorUsername: 'bob',
      updatedFrom: '2026-11-01T00:00',
      updatedTo: '2026-11-15T23:59',
      topicID: '3',
      titlePrefix: 'fix',
      categorySlug: 'dev',
      perPage: '100'
    })
  })
})

// ---------------------------------------------------------------------------
// 渲染级证据：query → 页面状态；tab/筛选变更 → router.replace。
// ---------------------------------------------------------------------------

const globals = globalThis as Record<string, unknown>
const saved = { useI18n: globals.useI18n, useToast: globals.useToast }
afterAll(() => {
  globals.useI18n = saved.useI18n
  globals.useToast = saved.useToast
})
globals.useI18n = () => ({ t: (key: string) => key })
globals.useToast = () => ({ add: () => {} })

const { apiErrorMessage, apiErrorReason } = await import('../../app/composables/useApiClient')

type ListCall = { kind: string, filters: Record<string, unknown>, cursor?: string }
const listCalls: ListCall[] = []
let replaceCalls: Array<{ query: Record<string, string> }> = []

const contentApi = {
  list: async (kind: string, filters: Record<string, unknown>, cursor?: string) => {
    listCalls.push({ kind, filters, cursor })
    return { items: [], hasMore: false, nextCursor: '' }
  },
  getTopic: async () => null,
  getComment: async () => null,
  listTopicRevisions: async () => ({ items: [], hasMore: false }),
  listCommentRevisions: async () => ({ items: [], hasMore: false }),
  getTopicRevision: async () => null,
  getCommentRevision: async () => null,
  restoreTopicRevision: async () => null,
  restoreCommentRevision: async () => null,
  redactTopicRevision: async () => ({ redacted: true }),
  redactCommentRevision: async () => ({ redacted: true })
}

const IconStub = { props: ['name'], template: '<i />' }
const ToolbarStub = { template: '<div class="toolbar"><slot name="left" /><slot name="right" /></div>' }
const ButtonStub = {
  props: ['color', 'variant', 'size', 'icon', 'disabled', 'loading'],
  emits: ['click'],
  template: '<button type="button" :data-color="color" :data-variant="variant" @click="$emit(\'click\')"><slot /></button>'
}
const SelectStub = {
  props: ['modelValue', 'items', 'valueKey', 'labelKey'],
  emits: ['update:modelValue'],
  template: '<select class="select" :data-model="String(modelValue)" />'
}
const InputStub = {
  props: ['modelValue', 'type', 'placeholder'],
  emits: ['update:modelValue'],
  template: '<input :data-model="String(modelValue)" />'
}
const FieldStub = { template: '<label><slot /></label>' }
const AlertStub = {
  props: { color: { type: String, default: '' }, variant: { type: String, default: '' }, icon: { type: String, default: '' }, title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="alert" :data-color="color">{{ title }}</div>'
}
const BadgeStub = { props: ['color', 'variant'], template: '<span><slot /></span>' }
const SlideoverStub = { template: '<div class="slideover"><slot name="content" /></div>' }
const ModalStub = { template: '<div class="modal"><slot name="content" /></div>' }
const TooltipStub = { props: ['text'], template: '<span><slot /></span>' }
const SkeletonStub = { template: '<div class="skeleton" />' }
const EditorStub = { props: ['topic', 'comment'], emits: ['saved', 'conflict', 'cancel'], template: '<div class="editor" />' }
const TimelineStub = { props: ['revisions'], template: '<div class="timeline" />' }
const DiffStub = { template: '<div class="diff" />' }

let canTopicPerms = true
let routeQuery: Record<string, string> = {}

const ContentPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/forum/content.vue', import.meta.url)),
  'admin-forum-content-page',
  {
    usePermissions: () => ({
      can: (permission: string) => {
        if (permission.startsWith('topic.')) return canTopicPerms
        return true
      },
      user: testVue.ref({ id: 1, status: 'active', roleKeys: ['admin'] })
    }),
    userDisplayName: (author: { displayName?: string } | undefined) => author?.displayName || 'author',
    useAdminForumContent: () => contentApi,
    useAdminPage: () => ({ id: '/forum/content', icon: 'i-lucide-files', labelKey: 'admin.nav.forumContent' }),
    useSiteDateTime: () => ({ format: (value?: string) => value || '' }),
    useRoute: () => ({ query: routeQuery }),
    useRouter: () => ({ replace: async (arg: { query: Record<string, string> }) => { replaceCalls.push(arg) } }),
    useSeoMeta: () => {},
    apiErrorMessage,
    apiErrorReason,
    parseAdminForumContentRouteQuery,
    buildAdminForumContentRouteQuery,
    sanitizeHtml: (html: string) => html,
    SFTopicEditor: EditorStub,
    SFAdminForumCommentEditor: EditorStub,
    SFAdminForumRevisionDiff: DiffStub,
    SFAdminForumRevisionTimeline: TimelineStub,
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    onMounted: testVue.onMounted
  }
)

const PageHarness = testVue.defineComponent({
  components: { ContentPage },
  template: '<Suspense><ContentPage /></Suspense>'
})

function mountPage() {
  return mount(PageHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        USelect: SelectStub,
        UInput: InputStub,
        UFormField: FieldStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UTooltip: TooltipStub,
        USkeleton: SkeletonStub,
        USlideover: SlideoverStub,
        UModal: ModalStub,
        UTextarea: { props: ['modelValue'], template: '<textarea />' }
      },
      directives: {
        highlight: {},
        mention: {}
      }
    }
  })
}

function tabButton(wrapper: ReturnType<typeof mountPage>, key: string) {
  const button = wrapper.findAll('button').find(item => item.text() === `admin.forum.content.tabs.${key}`)
  if (!button) throw new Error(`找不到分区按钮 ${key}`)
  return button
}

describe('UX2-14 渲染级：query 决定初始状态', () => {
  test('深链 ?tab=comments + 筛选：F5 后分区与筛选都在', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    routeQuery = { tab: 'comments', status: 'hidden', authorUsername: 'alice', perPage: '50' }
    const wrapper = mountPage()
    try {
      await flushPromises()

      expect(tabButton(wrapper, 'comments').attributes('data-color')).toBe('primary')
      expect(tabButton(wrapper, 'topics').attributes('data-color')).toBe('neutral')
      // 第一个 USelect 是状态筛选，第二个是每页条数。
      const selects = wrapper.findAll('.select')
      expect(selects[0]!.attributes('data-model')).toBe('hidden')
      expect(selects.at(-1)!.attributes('data-model')).toBe('50')
      expect(listCalls[0]).toMatchObject({
        kind: 'comments',
        filters: { status: 'hidden', authorUsername: 'alice', perPage: 50 }
      })
    } finally {
      wrapper.unmount()
    }
  })

  test('无主题权限时 ?tab=topics 被夹回 comments', async () => {
    canTopicPerms = false
    listCalls.length = 0
    replaceCalls = []
    routeQuery = { tab: 'topics' }
    const wrapper = mountPage()
    try {
      await flushPromises()
      expect(wrapper.text()).toContain('admin.forum.content.tabs.comments')
      expect(wrapper.text()).not.toContain('admin.forum.content.tabs.topics')
      expect(listCalls[0]?.kind).toBe('comments')
    } finally {
      canTopicPerms = true
      wrapper.unmount()
    }
  })
})

// SSR 语义的同一页面：把 `import.meta.client` 改成 false，验证「服务端不写地址栏」。
const ContentPageServer = await compileSfcSource(
  (await Bun.file(fileURLToPath(new URL('../../app/pages/admin/forum/content.vue', import.meta.url))).text())
    .replaceAll('import.meta.client', 'false'),
  'admin-forum-content-page-server',
  {
    usePermissions: () => ({
      can: (permission: string) => permission.startsWith('topic.') || true,
      user: testVue.ref({ id: 1, status: 'active', roleKeys: ['admin'] })
    }),
    userDisplayName: (author: { displayName?: string } | undefined) => author?.displayName || 'author',
    useAdminForumContent: () => contentApi,
    useAdminPage: () => ({ id: '/forum/content', icon: 'i-lucide-files', labelKey: 'admin.nav.forumContent' }),
    useSiteDateTime: () => ({ format: (value?: string) => value || '' }),
    useRoute: () => ({ query: {} }),
    useRouter: () => ({ replace: async (arg: { query: Record<string, string> }) => { replaceCalls.push(arg) } }),
    useSeoMeta: () => {},
    apiErrorMessage,
    apiErrorReason,
    sanitizeHtml: (html: string) => html,
    parseAdminForumContentRouteQuery,
    buildAdminForumContentRouteQuery,
    SFTopicEditor: EditorStub,
    SFAdminForumCommentEditor: EditorStub,
    SFAdminForumRevisionDiff: DiffStub,
    SFAdminForumRevisionTimeline: TimelineStub,
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    onMounted: testVue.onMounted
  }
)

describe('UX2-14 服务端渲染不写地址栏', () => {
  test('SSR 语义下切分区不调用 router.replace（只在客户端同步）', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    const Harness = testVue.defineComponent({
      components: { ContentPageServer },
      template: '<Suspense><ContentPageServer /></Suspense>'
    })
    const wrapper = mount(Harness, {
      global: {
        stubs: {
          UIcon: IconStub,
          UDashboardToolbar: ToolbarStub,
          UButton: ButtonStub,
          USelect: SelectStub,
          UInput: InputStub,
          UFormField: FieldStub,
          UAlert: AlertStub,
          UBadge: BadgeStub,
          UTooltip: TooltipStub,
          USkeleton: SkeletonStub,
          USlideover: SlideoverStub,
          UModal: ModalStub,
          UTextarea: { props: ['modelValue'], template: '<textarea />' }
        },
        directives: { highlight: {}, mention: {} }
      }
    })
    try {
      await flushPromises()
      const tab = wrapper.findAll('button').find(item => item.text() === 'admin.forum.content.tabs.comments')
      await tab!.trigger('click')
      await flushPromises()
      // 数据照常切换，但地址栏不动（SSR 没有历史栈）。
      expect(listCalls.at(-1)?.kind).toBe('comments')
      expect(replaceCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('UX2-14 渲染级：变更写回地址栏', () => {
  test('切分区走 router.replace，且只改 query 不新增历史', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    routeQuery = {}
    const wrapper = mountPage()
    try {
      await flushPromises()
      await tabButton(wrapper, 'comments').trigger('click')
      await flushPromises()

      expect(replaceCalls.at(-1)?.query).toMatchObject({ tab: 'comments' })
      expect(listCalls.at(-1)?.kind).toBe('comments')
    } finally {
      wrapper.unmount()
    }
  })

  test('应用筛选写回全部 9 个字段（默认值省略）', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    routeQuery = { tab: 'topics', status: 'active' }
    const wrapper = mountPage()
    try {
      await flushPromises()
      const apply = wrapper.findAll('button').find(button => button.text() === 'admin.forum.content.applyFilters')
      await apply!.trigger('click')
      await flushPromises()

      expect(replaceCalls.at(-1)?.query).toEqual({ tab: 'topics', status: 'active' })
      expect(listCalls.at(-1)).toMatchObject({ kind: 'topics', filters: { status: 'active', perPage: 20 } })
    } finally {
      wrapper.unmount()
    }
  })
})

// ---------------------------------------------------------------------------
// 反向同步 + clamp 纠正（第 15 轮：content.vue 缺 route.query 反向 watch）。
//
// 页面从 `route.query` 派生状态，但此前只有 state → URL 单向。同路由改 query
// （地址栏回车、站内带筛选的链接）不会生效；`?tab=` 被权限夹掉后地址栏仍留着
// 原值，分享出去会把下一个人再夹一次。这里用**响应式 route** 复现真实导航。
// ---------------------------------------------------------------------------

const liveRoute = testVue.reactive({ query: {} as Record<string, unknown> })

const ContentPageLive = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/forum/content.vue', import.meta.url)),
  'admin-forum-content-page-live',
  {
    usePermissions: () => ({
      can: (permission: string) => {
        if (permission.startsWith('topic.')) return canTopicPerms
        return true
      },
      user: testVue.ref({ id: 1, status: 'active', roleKeys: ['admin'] })
    }),
    userDisplayName: (author: { displayName?: string } | undefined) => author?.displayName || 'author',
    useAdminForumContent: () => contentApi,
    useAdminPage: () => ({ id: '/forum/content', icon: 'i-lucide-files', labelKey: 'admin.nav.forumContent' }),
    useSiteDateTime: () => ({ format: (value?: string) => value || '' }),
    useRoute: () => liveRoute,
    useRouter: () => ({ replace: async (arg: { query: Record<string, string> }) => { replaceCalls.push(arg) } }),
    useSeoMeta: () => {},
    apiErrorMessage,
    apiErrorReason,
    parseAdminForumContentRouteQuery,
    buildAdminForumContentRouteQuery,
    sanitizeHtml: (html: string) => html,
    SFTopicEditor: EditorStub,
    SFAdminForumCommentEditor: EditorStub,
    SFAdminForumRevisionDiff: DiffStub,
    SFAdminForumRevisionTimeline: TimelineStub,
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    onMounted: testVue.onMounted
  }
)

const LiveHarness = testVue.defineComponent({
  components: { ContentPageLive },
  template: '<Suspense><ContentPageLive /></Suspense>'
})

function mountLivePage() {
  return mount(LiveHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        USelect: SelectStub,
        UInput: InputStub,
        UFormField: FieldStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UTooltip: TooltipStub,
        USkeleton: SkeletonStub,
        USlideover: SlideoverStub,
        UModal: ModalStub,
        UTextarea: { props: ['modelValue'], template: '<textarea />' },
        SFEmptyState: { props: ['title'], template: '<div class="empty">{{ title }}</div>' }
      },
      directives: { highlight: {}, mention: {} }
    }
  })
}

describe('反向同步：同路由改 query 时页面状态跟随', () => {
  test('地址栏换成 ?tab=comments&status=hidden：分区与筛选跟着变，并重新取数', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'topics' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      expect(listCalls.at(-1)?.kind).toBe('topics')

      liveRoute.query = { tab: 'comments', status: 'hidden' }
      await flushPromises()

      expect(tabButton(wrapper, 'comments').attributes('data-color')).toBe('primary')
      expect(listCalls.at(-1)).toMatchObject({ kind: 'comments', filters: { status: 'hidden' } })
      // 已经是规范形态：不需要纠正地址栏。
      expect(replaceCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('非法 query 值（?tab=drafts）回落默认分区，并把地址栏纠正成规范值', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'drafts', perPage: '999' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      expect(listCalls.at(-1)).toMatchObject({ kind: 'topics', filters: { perPage: 20 } })
      expect(replaceCalls.at(-1)?.query).toEqual({ tab: 'topics' })
    } finally {
      wrapper.unmount()
    }
  })
})

describe('写回地址栏时保留非本页参数（F4）', () => {
  test('?tab=topics&utm=x 被夹正时，utm 原样留在 query 里', async () => {
    canTopicPerms = false
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'topics', utm: 'spring', ref: 'newsletter' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      expect(replaceCalls.at(-1)?.query).toEqual({ utm: 'spring', ref: 'newsletter', tab: 'comments' })
    } finally {
      canTopicPerms = true
      wrapper.unmount()
    }
  })

  test('切分区时也不丢外部参数（写回是合并而不是整段替换）', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'topics', utm: 'spring' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      await tabButton(wrapper, 'comments').trigger('click')
      await flushPromises()
      expect(replaceCalls.at(-1)?.query).toMatchObject({ tab: 'comments', utm: 'spring' })
    } finally {
      wrapper.unmount()
    }
  })
})

describe('clamp 纠正：无权限分区不再留在地址栏', () => {
  test('无主题权限时 ?tab=topics 挂载即改写为 ?tab=comments', async () => {
    canTopicPerms = false
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'topics' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      expect(listCalls.at(-1)?.kind).toBe('comments')
      expect(replaceCalls.at(-1)?.query).toEqual({ tab: 'comments' })
    } finally {
      canTopicPerms = true
      wrapper.unmount()
    }
  })

  test('自己写回地址栏不触发第二轮取数（watch → replace → watch 不得抖动）', async () => {
    canTopicPerms = true
    listCalls.length = 0
    replaceCalls = []
    liveRoute.query = { tab: 'topics' }
    const wrapper = mountLivePage()
    try {
      await flushPromises()
      const before = listCalls.length
      await tabButton(wrapper, 'comments').trigger('click')
      await flushPromises()
      expect(listCalls.length).toBe(before + 1)

      // 模拟路由把刚写入的 query 回灌给页面（真实浏览器的下一 tick）。
      const written = replaceCalls.at(-1)!.query
      liveRoute.query = { ...written }
      await flushPromises()
      expect(listCalls.length).toBe(before + 1)
      expect(replaceCalls).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})
