import { afterEach, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'

import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import type { ForumCategoryGroup, ForumSettings } from '../../app/utils/forum/forumTaxonomy'

/**
 * UX2-03：论坛设置不能在「读失败」时把推荐默认值当成现值。
 *
 * 旧实现给 useAsyncData 塞了 `default: () => ({ groups: [], settings: createDefaultForumSettings() })`，
 * 读失败时子 tab 照常渲染推荐值、保存按钮照常可点 —— 一点保存就把默认值 PUT 回站点。
 * 现在的契约：读失败 ⇒ 不挂载子 tab（保存入口不存在）；成功 ⇒ 子 tab 拿到的是真实配置。
 */

const globals = globalThis as typeof globalThis & Record<string, unknown>

type Snapshot = Record<string, unknown>
let snapshot: Snapshot = {}

const GLOBAL_KEYS = [
  'ref',
  'computed',
  'watch',
  'useI18n',
  'useRoute',
  'useRouter',
  'useAsyncData',
  'useApiClient',
  'useSeoMeta',
  'definePageMeta'
] as const

const pagePath = fileURLToPath(new URL('../../app/pages/admin/forum/settings.vue', import.meta.url))

/** 刻意与 createDefaultForumSettings() 不同：现值必须来自加载结果，不是推荐值。 */
const loadedSettings: ForumSettings = {
  defaultCategorySlug: 'announcements',
  tagCreationMode: 'open',
  tagPublicPages: false,
  tagMinPerTopic: 2,
  tagMaxPerTopic: 6,
  topicsPerPage: 37,
  commentsPerPage: 41,
  topicTitleMinRunes: 4,
  topicTitleMaxRunes: 120,
  topicContentMinRunes: 10,
  topicContentMaxRunes: 40000,
  topicEditWindowMinutes: 45,
  topicCooldownSeconds: 90,
  dailyTopicLimit: 7,
  commentMinRunes: 3,
  commentMaxRunes: 5000,
  commentMaxNestingDepth: 4,
  treeDescendantsPerRoot: 40,
  commentEditWindowMinutes: 20,
  commentCooldownSeconds: 15,
  dailyCommentLimit: 70,
  excerptRuneLimit: 160,
  guestRead: 'login_required',
  listDefaultSort: 'hot',
  listHotWindowDays: 14,
  allowAuthorCloseReplies: true,
  allowAuthorDelete: true,
  autoLockIdleDays: 30,
  showTopicEditMark: true,
  duplicateTitlePolicy: 'block',
  showCommentEditMark: true,
  softDeleteVisibility: 'staff_only',
  mentionsEnabled: true,
  mentionsMaxPerPost: 4,
  externalInterstitial: true,
  trustedDomains: 'example.test'
}

/** 子 tab 只做一件事：记录自己拿到的 props，好让「推荐值没被当成现值」可断言。 */
function createTabProbe() {
  const received: Array<Record<string, unknown>> = []
  const component = {
    name: 'SFAdminForumTabProbe',
    props: ['settings', 'categories', 'pending'],
    emits: ['saved', 'refresh'],
    setup(props: Record<string, unknown>) {
      received.push({ ...props })
      return () => testVue.h('div', { 'data-testid': 'forum-settings-tab' }, [
        testVue.h('button', { type: 'submit', 'data-testid': 'forum-settings-save' }, 'save')
      ])
    }
  }

  return { component, received }
}

type LoadOutcome = { ok: true, groups: ForumCategoryGroup[], settings: ForumSettings } | { ok: false, error: unknown }

async function mountSettingsPage(outcome: LoadOutcome) {
  snapshot = {}
  for (const key of GLOBAL_KEYS) {
    snapshot[key] = globals[key]
  }

  const tabProbe = createTabProbe()
  const loads: string[] = []

  Object.assign(globals, {
    ref: testVue.ref,
    computed: testVue.computed,
    watch: testVue.watch,
    useI18n: () => ({ t: (key: string) => key }),
    useRoute: () => ({ path: '/control-panel/forum/settings', params: {}, query: {} }),
    useRouter: () => ({ replace: async () => {} }),
    useSeoMeta: () => {},
    definePageMeta: () => {},
    // 页面把 request 交给 createAdminForumApi；这里只需要一个占位，取数走下面的桩。
    useApiClient: () => ({ request: async () => ({}) }),
    // 复刻 Nuxt：不给 default 时读失败 data 保持 undefined（asyncData.js: data = unref(options.default())）。
    useAsyncData: async (_key: string, loader: () => Promise<unknown>) => {
      try {
        return { data: testVue.ref(await loader()), pending: testVue.ref(false), error: testVue.ref(null), refresh: async () => {} }
      } catch (error) {
        return { data: testVue.ref(undefined), pending: testVue.ref(false), error: testVue.ref(error), refresh: async () => {} }
      }
    }
  })

  const Page = await compileVueSfc(pagePath, 'admin-forum-settings', {
    useAdminPage: () => ({ icon: 'i-lucide-sliders-horizontal' }),
    createAdminForumApi: () => ({
      listCategoryGroups: async () => {
        loads.push('category-groups')
        if (!outcome.ok) throw outcome.error
        return outcome.groups
      },
      getSettings: async () => {
        loads.push('settings')
        if (!outcome.ok) throw outcome.error
        return outcome.settings
      }
    }),
    SFAdminFixedTabNav: {
      props: ['items', 'modelValue', 'ariaLabel'],
      template: '<div data-testid="admin-forum-settings-tabs" />'
    },
    SFAdminForumGeneralTab: tabProbe.component,
    SFAdminForumTopicsTab: tabProbe.component,
    SFAdminForumCommentsTab: tabProbe.component,
    SFAdminForumTagsTab: tabProbe.component,
    SFAdminForumReadingTab: tabProbe.component,
    SFAdminForumBehaviorTab: tabProbe.component,
    SFAdminForumSearchTab: tabProbe.component
  })

  // 页面脚本带顶层 await（async setup）：必须有 Suspense 边界，否则 Vue 直接不渲染。
  const wrapper = mount({
    render: () => testVue.h(testVue.Suspense, null, { default: () => testVue.h(Page) })
  }, {
    global: {
      stubs: {
        UDashboardToolbar: { template: '<div><slot name="left" /><slot name="right" /></div>' },
        UAlert: {
          props: ['title', 'description', 'color'],
          template: '<div class="alert" :data-testid="$attrs[\'data-testid\']">{{ title }}|{{ description }}</div>'
        },
        UButton: { props: ['loading', 'icon', 'color', 'variant', 'leadingIcon'], template: '<button><slot /></button>' },
        UIcon: true
      }
    }
  })
  await flushPromises()
  await flushPromises()

  return { wrapper, tabProbe, loads }
}

function restoreGlobals() {
  for (const key of GLOBAL_KEYS) {
    if (snapshot[key] === undefined) {
      delete globals[key]
    } else {
      globals[key] = snapshot[key]
    }
  }
  snapshot = {}
}

afterEach(() => {
  restoreGlobals()
})

describe('admin forum settings load gate', () => {
  test('allowed: a successful load mounts the tab surface with the real current settings', async () => {
    const { wrapper, tabProbe, loads } = await mountSettingsPage({ ok: true, groups: [], settings: loadedSettings })
    try {
      expect(loads).toEqual(['category-groups', 'settings'])
      expect(wrapper.find('[data-testid="admin-forum-settings-load-failed"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="admin-forum-settings-tabs"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="forum-settings-save"]').exists()).toBe(true)

      expect(tabProbe.received).toHaveLength(1)
      // props 拿到的是响应式代理，按值比对：现值的每个字段都必须来自加载结果。
      expect(tabProbe.received[0]!.settings).toEqual(loadedSettings)
      expect((tabProbe.received[0]!.settings as ForumSettings).topicsPerPage).toBe(37)
    } finally {
      wrapper.unmount()
    }
  })

  test('denied: a failed load offers no save entry and never shows recommended defaults as current values', async () => {
    const failure = { code: 500, message: '服务暂时不可用。' }
    const { wrapper, tabProbe } = await mountSettingsPage({ ok: false, error: failure })
    try {
      // 先断言行为：保存按钮是唯一写入口，读失败时它根本不该存在。
      expect(wrapper.find('[data-testid="forum-settings-save"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="forum-settings-tab"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="admin-forum-settings-tabs"]').exists()).toBe(false)

      // 子 tab 一次都没收到 settings —— 推荐默认值没有机会冒充现值。
      expect(tabProbe.received).toEqual([])

      const alert = wrapper.find('[data-testid="admin-forum-settings-load-failed"]')
      expect(alert.exists()).toBe(true)
      expect(alert.text()).toContain('admin.forum.settings.loadFailed')
      expect(alert.text()).toContain('admin.forum.settings.loadFailedSaveDisabled')
    } finally {
      wrapper.unmount()
    }
  })

  test('denied: the page no longer seeds useAsyncData with createDefaultForumSettings', async () => {
    const source = await Bun.file(pagePath).text()
    expect(source).not.toContain('default: () => ({ groups: []')
    expect(source).not.toContain('createDefaultForumSettings')
    expect(source).toContain('v-if="data"')
  })
})
