import { beforeEach, describe, expect, test } from 'bun:test'
import { readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import {
  extensionTabLabel,
  profileWidgetTabValue,
  profileWidgetTabs
} from '../../app/utils/profile/profileExtensionTab'
import type { ProfileExtensionTab, PublicProfile } from '../../app/composables/profile/useProfileApi'

/**
 * 资料页动态 tab：`forum.profile.tabs` 贡献的 widget tab 并入主 tab 栏，
 * 点击后由宿主原地挂载 SFExtensionWidget（L2），切走即卸载。
 *
 * 这里挂的是真实 SFProfileShowPage + 真实 SFProfileRightRail，只把 L2 原语替换成
 * 一个记录 props 与生命周期计数的 stub —— 「懒挂载 / 切走卸载」只有在真实 v-if
 * 分支上才有意义，静态匹配源码是证不出来的。
 */

const messages: Record<string, string> = {
  'profile.tabs.topics': '主题',
  'profile.tabs.replies': '回复',
  'profile.tabs.about': '关于',
  'profile.tabs.ariaLabel': '个人主页分区'
}

function t(key: string) {
  return messages[key] ?? key
}

const locale = testVue.ref('zh-CN')

function makeProfile(extensionTabs?: ProfileExtensionTab[]): PublicProfile {
  return {
    userId: 7,
    username: 'alice',
    displayName: 'Alice',
    profile: {
      userId: 7,
      bio: '写作与跑步。',
      signature: '',
      location: '',
      websiteUrl: '',
      avatar: { kind: 'initials', url: '', alt: 'Alice' },
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z'
    },
    topicCount: 3,
    commentCount: 2,
    recentTopics: [],
    activities: [],
    joinedAt: '2026-01-01T00:00:00Z',
    extensionTabs
  }
}

const widgetTab = (id: string, order: number, componentId: string, label: string): ProfileExtensionTab => ({
  extensionId: 'demo',
  id,
  order,
  label: { 'zh-CN': label, 'en-US': `${label}-en` },
  kind: 'widget',
  componentId,
  url: ''
})

type WidgetMount = { extensionId: string, componentId: string, props: Record<string, unknown> }

let widgetMounts: WidgetMount[] = []
let widgetAlive = 0

const WidgetStub = testVue.defineComponent({
  name: 'SFExtensionWidgetStub',
  props: {
    extensionId: { type: String, default: '' },
    componentId: { type: String, default: '' },
    componentProps: { type: Object, default: () => ({}) }
  },
  setup(props) {
    widgetAlive += 1
    widgetMounts.push({
      extensionId: props.extensionId,
      componentId: props.componentId,
      props: { ...(props.componentProps as Record<string, unknown>) }
    })
    testVue.onUnmounted(() => {
      widgetAlive -= 1
    })
    return () => testVue.h('div', {
      'data-testid': 'profile-widget-stub',
      'data-component-id': props.componentId
    })
  }
})

const nullRender = testVue.defineComponent({ render: () => null })
const slotRender = testVue.defineComponent({
  setup(_props, { slots }) {
    return () => testVue.h('div', slots.default ? slots.default() : [])
  }
})
const SkeletonStub = testVue.defineComponent({
  props: { lines: { type: Number, default: 3 } },
  render: () => testVue.h('div', { 'data-testid': 'profile-skeleton' })
})
const NuxtLinkStub = testVue.defineComponent({
  props: { to: { type: [String, Object], default: '' } },
  setup(props, { slots }) {
    return () => testVue.h('a', {
      href: typeof props.to === 'string' ? props.to : ''
    }, slots.default ? slots.default() : [])
  }
})

const profileApi = {
  async getPublicProfile() {
    return state.profile
  },
  async listPublicActivities() {
    return { items: [], page: 1, perPage: 20, total: 0, hasMore: false, kind: 'topic' as const }
  }
}

const state: { profile: PublicProfile } = { profile: makeProfile() }

const bindings: Record<string, unknown> = {
  // Nuxt 自动导入的运行时与组合式函数
  ref: testVue.ref,
  computed: testVue.computed,
  watch: testVue.watch,
  onMounted: testVue.onMounted,
  useRoute: () => ({ params: { username: 'alice' }, fullPath: '/u/alice' }),
  useI18n: () => ({ t, locale }),
  useLocalePath: () => (path: string) => path,
  useToast: () => ({ add: () => {} }),
  useWebOptions: () => ({ seoSettings: testVue.ref({ topicUrlMode: 'id_slug' }) }),
  useSiteDateTime: () => ({
    format: (value: string) => value,
    formatDateOnly: (value: string) => value,
    settings: testVue.ref({ timezone: 'UTC', dateFormat: 'Y-m-d', timeFormat: 'H:i', startOfWeek: 1 })
  }),
  useState: (_key: string, init: () => unknown) => testVue.ref(init()),
  useAsyncData: async (_key: unknown, handler: () => unknown) => ({
    data: testVue.ref(await handler()),
    pending: testVue.ref(false),
    error: testVue.ref(null)
  }),
  useSForumSeo: () => {},
  usePublicSidebarDrawer: () => ({ closeDrawer: () => {} }),
  useAuthSession: () => ({ user: testVue.ref(null) }),
  usePermissions: () => ({ can: () => false }),
  FORUM_PERMISSIONS: { topicCreate: 'forum.topic.create' },
  useForumApi: () => ({ listCategoryGroups: async () => [] }),
  useProfileApi: () => profileApi,
  // 路由壳是唯一取数点；测试里等价于「已 provide 的那份 refs」。
  useProfileShowData: async () => ({
    profile: testVue.ref(state.profile),
    pending: testVue.ref(false),
    error: testVue.ref(null)
  }),
  forumUserProfilePath: (name: string) => `/u/${name}`,
  userDisplayName: (profile: PublicProfile | null, fallback: string) => profile?.displayName || fallback,
  apiErrorMessage: (error: unknown) => String(error),
  groupProfileActivitiesByDate: () => [],
  profileActivityLink: () => '/',
  profileHasPublicDetails: () => false,
  safeUrl: (value: string) => value,
  // 本任务抽出的共享解析
  extensionTabLabel,
  profileWidgetTabValue,
  profileWidgetTabs,
  // 页面显式导入的组件（右栏挂真组件，其余只占位）
  SFHomeNavigation: nullRender,
  SFResponsivePublicSidebar: slotRender,
  SFPublicMobileUserMenu: nullRender,
  SFPublicMobileRightDrawerHeader: nullRender
}

const railPath = fileURLToPath(new URL('../../app/components/profile/SFProfileRightRail.vue', import.meta.url))
const pagePath = fileURLToPath(new URL('../../app/components/profile/SFProfileShowPage.vue', import.meta.url))

/**
 * compileVueSfc 用 `new Function` 执行 SFC 脚本，函数体里 `import.meta` 不是合法语法。
 * 页面只有 shareProfile 用它做 SSR 守卫，这里换成语义等价的字面量再编译；
 * 出现别的 `import.meta` 形态就直接失败，不让测试环境悄悄改变生产语义。
 */
function pageSourceForCompile(path: string) {
  const source = readFileSync(path, 'utf8')
  const rest = source
    .replaceAll('import.meta.client', '')
    .replaceAll('import.meta.server', '')
  if (rest.includes('import.meta')) {
    throw new Error('SFProfileShowPage uses an import.meta form the SFC test compiler cannot execute')
  }
  return source
    .replaceAll('import.meta.client', 'true')
    .replaceAll('import.meta.server', 'false')
}

const compiledPagePath = join(tmpdir(), `sforum-profile-show-${process.pid}.vue`)
writeFileSync(compiledPagePath, pageSourceForCompile(pagePath))

const ProfileRightRail = await compileVueSfc(railPath, 'profile-right-rail', bindings)
bindings.SFProfileRightRail = ProfileRightRail
const ProfileShowPage = await compileVueSfc(compiledPagePath, 'profile-show', bindings)
rmSync(compiledPagePath, { force: true })

const globalComponents = {
  SFExtensionWidget: WidgetStub,
  SFSkeleton: SkeletonStub,
  SFAvatar: nullRender,
  UIcon: nullRender,
  SFEmptyState: nullRender,
  SFRegionOutlet: nullRender,
  NuxtLink: NuxtLinkStub
}

async function mountPage(profile: PublicProfile) {
  state.profile = profile
  const Harness = testVue.defineComponent({
    render: () => testVue.h(testVue.Suspense, null, { default: () => testVue.h(ProfileShowPage) })
  })
  const wrapper = mount(Harness, { global: { components: globalComponents } })
  await flushPromises()
  await flushPromises()
  return wrapper
}

function tabButtons(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('.sf-profile-tab')
}

function tabText(wrapper: ReturnType<typeof mount>) {
  return tabButtons(wrapper).map(node => (node.text() || '').replace(/\s+/g, ' ').trim())
}

function tabByLabel(wrapper: ReturnType<typeof mount>, label: string) {
  return tabButtons(wrapper).find(node => (node.text() || '').trim().startsWith(label))
}

beforeEach(() => {
  widgetMounts = []
  widgetAlive = 0
  locale.value = 'zh-CN'
})

describe('profile extension tab label resolution', () => {
  test('falls back from current locale to zh-CN, en-US, any label, then id', () => {
    expect(extensionTabLabel({ id: 'portfolio', label: { 'zh-CN': '作品集', 'en-US': 'Portfolio' } }, 'en-US'))
      .toBe('Portfolio')
    expect(extensionTabLabel({ id: 'portfolio', label: { 'zh-CN': '作品集', 'en-US': 'Portfolio' } }, 'ja-JP'))
      .toBe('作品集')
    expect(extensionTabLabel({ id: 'portfolio', label: { 'en-US': 'Portfolio' } }, 'ja-JP'))
      .toBe('Portfolio')
    expect(extensionTabLabel({ id: 'portfolio', label: { 'de-DE': 'Portfolio' } }, 'ja-JP'))
      .toBe('Portfolio')
    expect(extensionTabLabel({ id: 'portfolio' }, 'ja-JP'))
      .toBe('portfolio')
  })
})

describe('profile widget tab selection', () => {
  test('keeps only mountable widget tabs, ordered by order then extension:id', () => {
    const tabs: ProfileExtensionTab[] = [
      { extensionId: 'demo', id: 'link', order: 1, kind: 'hostLink', url: '/docs' },
      { extensionId: 'demo', id: 'route', order: 2, kind: 'extensionRoute', url: '/extensions/demo/x' },
      widgetTab('zeta', 20, 'demo.zeta', 'Zeta'),
      widgetTab('beta', 20, 'demo.beta', 'Beta'),
      widgetTab('alpha', 10, 'demo.alpha', 'Alpha'),
      // 缺 componentId 的 widget 贡献无法挂载，直接不进入 tab 栏
      { extensionId: 'demo', id: 'broken', order: 5, kind: 'widget', url: '' }
    ]

    expect(profileWidgetTabs(tabs).map(tab => tab.componentId))
      .toEqual(['demo.alpha', 'demo.beta', 'demo.zeta'])
    expect(profileWidgetTabs(undefined)).toEqual([])
    expect(profileWidgetTabValue({ extensionId: 'demo', id: 'alpha' })).toBe('demo:alpha')
  })

  test('sorts by code units, matching the backend byte order that drives truncation', () => {
    // localeCompare 把 `a_b` 排在 `a-b` 前（'-' 0x2D < '_' 0x5F，字节序相反）。
    // 后端按 order → ExtensionID → ID 的字节序截断到 8 个，两边顺序必须逐字一致，
    // 否则「活下来的就是显示顺序前八个」这条承诺不成立。
    const tabs = [
      widgetTab('a_b', 10, 'demo.underscore', 'Underscore'),
      widgetTab('a-b', 10, 'demo.hyphen', 'Hyphen'),
      // 前缀关系：`demo` 必须整体排在 `demo-x` 之前（比较的是字段，不是 `${ext}:${id}` 拼接串）
      widgetTab('z', 10, 'demo.short-ext', 'Short'),
      widgetTab('a', 10, 'demo-x.prefix', 'Prefix')
    ]

    // 同一 extension 下按 id 的 code unit 序：'a' < 'a-b'（前缀更短优先）< 'a_b'（'-' 0x2D < '_' 0x5F）
    expect(profileWidgetTabs(tabs).map(tab => `${tab.extensionId}:${tab.id}`)).toEqual([
      'demo:a',
      'demo:a-b',
      'demo:a_b',
      'demo:z'
    ])

    const prefixed = [
      { ...widgetTab('a', 10, 'demo.short-ext', 'Short'), extensionId: 'demo', id: 'z' },
      { ...widgetTab('a', 10, 'demo-x.prefix', 'Prefix'), extensionId: 'demo-x', id: 'a' }
    ]
    expect(profileWidgetTabs(prefixed).map(tab => `${tab.extensionId}:${tab.id}`))
      .toEqual(['demo:z', 'demo-x:a'])
  })
})

describe('profile page dynamic tabs', () => {
  test('renders only the three built-in tabs when no widget tab is contributed', async () => {
    const wrapper = await mountPage(makeProfile([
      { extensionId: 'demo', id: 'link', order: 1, kind: 'hostLink', url: '/docs', label: { 'zh-CN': '文档' } }
    ]))

    expect(tabText(wrapper)).toEqual(['主题 3', '回复 2', '关于'])
    expect(wrapper.find('[data-testid="profile-widget-stub"]').exists()).toBe(false)
    expect(widgetMounts).toEqual([])
    wrapper.unmount()
  })

  test('appends widget tabs after 关于 and mounts L2 only after the tab is selected', async () => {
    const wrapper = await mountPage(makeProfile([
      { extensionId: 'demo', id: 'link', order: 1, kind: 'hostLink', url: '/docs', label: { 'zh-CN': '文档' } },
      widgetTab('portfolio', 20, 'demo.portfolio', '作品集'),
      widgetTab('guestbook', 10, 'demo.guestbook', '留言板')
    ]))

    expect(tabText(wrapper)).toEqual(['主题 3', '回复 2', '关于', '留言板', '作品集'])
    // 懒挂载：tab 已存在但未选中时不创建 L2 挂载点
    expect(widgetMounts).toEqual([])
    expect(wrapper.find('[data-testid="profile-widget-stub"]').exists()).toBe(false)

    const portfolio = tabByLabel(wrapper, '作品集')
    expect(portfolio).toBeDefined()
    await portfolio!.trigger('click')
    await flushPromises()

    expect(widgetMounts).toEqual([{
      extensionId: 'demo',
      componentId: 'demo.portfolio',
      props: { username: 'alice', userId: 7, displayName: 'Alice' }
    }])
    expect(wrapper.find('[data-testid="profile-widget-stub"]').attributes('data-component-id'))
      .toBe('demo.portfolio')
    expect(tabByLabel(wrapper, '作品集')!.attributes('aria-selected')).toBe('true')
    expect(tabByLabel(wrapper, '主题')!.attributes('aria-selected')).toBe('false')

    // 面板只有一行可访问名称，视觉内容交给插件；宿主不再叠一层标题或错误 UI
    expect(wrapper.find('.sf-profile-extension-panel').attributes('aria-label')).toBe('作品集')

    // 右栏是跳转入口，不能重复出现同一个 widget tab
    const railLinks = wrapper.find('.sf-profile-extension-links')
    expect(railLinks.exists()).toBe(true)
    expect((railLinks.text() || '').trim()).toBe('文档')
    expect(tabButtons(wrapper).filter(node => (node.text() || '').includes('作品集')).length).toBe(1)

    wrapper.unmount()
  })

  test('unmounts the L2 widget when switching to another tab', async () => {
    const wrapper = await mountPage(makeProfile([widgetTab('portfolio', 10, 'demo.portfolio', '作品集')]))

    await tabByLabel(wrapper, '作品集')!.trigger('click')
    await flushPromises()
    expect(widgetAlive).toBe(1)

    await tabByLabel(wrapper, '主题')!.trigger('click')
    await flushPromises()

    expect(widgetAlive).toBe(0)
    expect(wrapper.find('[data-testid="profile-widget-stub"]').exists()).toBe(false)

    // 再切回 widget tab 是一次全新的挂载，而不是复用隐藏实例
    await tabByLabel(wrapper, '作品集')!.trigger('click')
    await flushPromises()
    expect(widgetAlive).toBe(1)
    expect(widgetMounts.map(mount => mount.componentId)).toEqual(['demo.portfolio', 'demo.portfolio'])

    wrapper.unmount()
    expect(widgetAlive).toBe(0)
  })

  test('about tab still renders its own panel while widget tabs exist', async () => {
    const wrapper = await mountPage(makeProfile([widgetTab('portfolio', 10, 'demo.portfolio', '作品集')]))

    await tabByLabel(wrapper, '关于')!.trigger('click')
    await flushPromises()

    expect(wrapper.find('.sf-profile-about').exists()).toBe(true)
    expect(widgetMounts).toEqual([])
    wrapper.unmount()
  })
})
