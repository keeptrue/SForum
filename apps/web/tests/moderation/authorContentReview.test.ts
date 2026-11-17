import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent, ref } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  authorReviewReviewNote,
  authorReviewSections,
  authorReviewTargetPath,
  type AuthorReviewItem
} from '../../app/utils/moderation/authorReview'

const componentPath = new URL('../../app/components/settings/SFContentReviewPage.vue', import.meta.url).pathname
const pagePath = new URL('../../app/pages/settings/content.vue', import.meta.url).pathname
const navPath = new URL('../../app/components/settings/SFSettingsAccountNav.vue', import.meta.url).pathname
const shellPath = new URL('../../app/components/settings/SFSettingsShell.vue', import.meta.url).pathname
const composablePath = new URL('../../app/composables/moderation/useAuthorContentReview.ts', import.meta.url).pathname

const source = (path: string) => readFileSync(path, 'utf8')
const zh = () => JSON.parse(source(new URL('../../i18n/locales/zh-CN.json', import.meta.url).pathname)) as Record<string, unknown>
const en = () => JSON.parse(source(new URL('../../i18n/locales/en-US.json', import.meta.url).pathname)) as Record<string, unknown>

function lookup(messages: Record<string, unknown>, key: string): string {
  const value = key.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, messages)
  return typeof value === 'string' ? value : key
}

function item(input: Partial<AuthorReviewItem>): AuthorReviewItem {
  return {
    targetType: 'topic',
    targetId: 1,
    topicId: 1,
    title: '主题标题',
    excerpt: '摘要',
    status: 'pending',
    reviewNote: '',
    createdAt: '2026-01-02T03:04:05Z',
    ...input
  }
}

// 纯逻辑：分桶、链接目标、审核意见取值。这三条是页面语义的地基，先单独锁住。
describe('author review presentation helpers', () => {
  test('splits pending and rejected without reordering', () => {
    const sections = authorReviewSections([
      item({ targetId: 1, status: 'rejected' }),
      item({ targetId: 2, status: 'pending' }),
      item({ targetId: 3, status: 'rejected' })
    ])

    expect(sections.pending.map(entry => entry.targetId)).toEqual([2])
    expect(sections.rejected.map(entry => entry.targetId)).toEqual([1, 3])
  })

  test('drops unknown status instead of guessing a bucket', () => {
    const sections = authorReviewSections([
      item({ targetId: 1, status: 'approved' as AuthorReviewItem['status'] }),
      item({ targetId: 2, status: 'pending' })
    ])

    expect(sections.pending.map(entry => entry.targetId)).toEqual([2])
    expect(sections.rejected).toEqual([])
  })

  test('links topics to the topic page and comments to their comment anchor', () => {
    expect(authorReviewTargetPath({ targetType: 'topic', targetId: 12 })).toBe('/t/12')
    expect(authorReviewTargetPath({ targetType: 'comment', targetId: 55, topicId: 12 })).toBe('/t/12#comment-55')
  })

  test('refuses to build a dead link when the target cannot be located', () => {
    expect(authorReviewTargetPath({ targetType: 'comment', targetId: 55 })).toBe('')
    expect(authorReviewTargetPath({ targetType: 'topic', targetId: 0 })).toBe('')
  })

  test('trims the review note so blank notes are detectable', () => {
    expect(authorReviewReviewNote({ reviewNote: '  内容不符合版规  ' })).toBe('内容不符合版规')
    expect(authorReviewReviewNote({ reviewNote: '   ' })).toBe('')
  })
})

const SFSettingsShellStub = defineComponent({
  name: 'SFSettingsShell',
  props: { active: String, title: String, description: String, titleId: String, railLabel: String },
  template: '<section data-settings-shell :data-active="active" :data-title="title"><slot name="head-actions" /><slot /></section>'
})
const PassthroughStub = defineComponent({ name: 'Passthrough', template: '<span><slot /></span>' })
const SkeletonStub = defineComponent({ name: 'SFSkeleton', template: '<div data-skeleton />' })
const EmptyStateStub = defineComponent({
  name: 'SFEmptyState',
  props: { title: String, description: String, actionLabel: String, icon: String },
  emits: ['action'],
  template: '<div data-empty-state :data-title="title" :data-description="description"><button v-if="actionLabel" data-empty-action @click="$emit(\'action\')">{{ actionLabel }}</button></div>'
})
const AlertStub = defineComponent({ name: 'UAlert', props: { title: String }, template: '<div data-alert :data-title="title" />' })
const BadgeStub = defineComponent({ name: 'UBadge', template: '<span data-badge><slot /></span>' })
const IconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i :data-icon="name" />' })
const ButtonStub = defineComponent({
  name: 'SFButton',
  props: { variant: String, size: String, loading: Boolean },
  emits: ['click'],
  template: '<button data-sf-button @click="$emit(\'click\')"><slot /></button>'
})
const NuxtLinkStub = defineComponent({ name: 'NuxtLink', props: { to: String }, template: '<a data-link :href="to"><slot /></a>' })

async function mountPanel(items: AuthorReviewItem[], messages: Record<string, unknown> = zh()) {
  const Component = await compileVueSfc(componentPath, 'sf-content-review-panel', {
    computed,
    ref,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => {
        const raw = lookup(messages, key)
        return params ? raw.replace(/\{(\w+)\}/g, (_m, name: string) => params[name] ?? '') : raw
      }
    }),
    useLocalePath: () => (path: string) => path,
    useSiteDateTime: () => ({ format: (value: string) => (value ? '2026-01-02 11:04' : '') }),
    navigateTo: () => {},
    useAuthorContentReview: () => ({ list: async () => ({ items }) }),
    // 与真实实现同语义：调用 loader 拿数据，供组件按真实响应形状渲染。
    useAsyncData: async (_key: string, loader: () => Promise<AuthorReviewItem[]>) => {
      const data = ref(await loader())
      return { data, pending: ref(false), error: ref(null), refresh: async () => { data.value = await loader() } }
    },
    apiErrorMessage: () => '',
    authorReviewSections,
    authorReviewReviewNote,
    authorReviewTargetPath,
    SFSettingsShell: SFSettingsShellStub
  })

  // 组件用顶层 `await useAsyncData(...)`（与其它账号设置页一致，SSR 首屏就带数据），
  // 所以挂载时必须给它一个 Suspense 边界，否则 setup 的 promise 没人接。
  const Root = defineComponent({
    name: 'AuthorReviewRoot',
    components: { SFContentReviewPage: Component },
    template: '<Suspense><SFContentReviewPage /></Suspense>'
  })

  const wrapper = mount(Root, {
    global: {
      components: {
        SFSettingsShell: SFSettingsShellStub,
        SFButton: ButtonStub,
        SFEmptyState: EmptyStateStub,
        SFSkeleton: SkeletonStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UIcon: IconStub,
        NuxtLink: NuxtLinkStub,
        UApp: PassthroughStub
      }
    }
  })
  await flushPromises()
  return wrapper
}

describe('author content review page renders two separable lists', () => {
  test('pending and rejected land in their own sections with counts', async () => {
    const wrapper = await mountPanel([
      item({ targetId: 1, title: '待审主题', status: 'pending' }),
      item({ targetId: 2, title: '被退回主题', status: 'rejected', reviewNote: '缺少来源链接' })
    ])
    try {
      const pending = wrapper.find('[data-sforum-review-section="pending"]')
      const rejected = wrapper.find('[data-sforum-review-section="rejected"]')

      expect(pending.exists()).toBe(true)
      expect(rejected.exists()).toBe(true)
      expect(pending.text()).toContain('待审主题')
      expect(pending.text()).not.toContain('被退回主题')
      expect(rejected.text()).toContain('被退回主题')
      expect(rejected.text()).not.toContain('待审主题')
      expect(pending.find('[data-sforum-review-count]').text()).toBe('1')
      expect(rejected.find('[data-sforum-review-count]').text()).toBe('1')
    } finally {
      wrapper.unmount()
    }
  })

  test('rejected items always render the review note, with a plain fallback when empty', async () => {
    const wrapper = await mountPanel([
      item({ targetId: 2, title: '有意见', status: 'rejected', reviewNote: '缺少来源链接' }),
      item({ targetId: 3, title: '没意见', status: 'rejected', reviewNote: '' })
    ])
    try {
      const notes = wrapper.findAll('[data-sforum-review-note]')
      expect(notes).toHaveLength(2)
      expect(notes[0]!.text()).toContain('审核理由')
      expect(notes[0]!.text()).toContain('缺少来源链接')
      expect(notes[1]!.text()).toContain(lookup(zh(), 'moderation.authorStatus.reviewNoteEmpty'))
    } finally {
      wrapper.unmount()
    }
  })

  test('each list keeps its own empty state when the other has items', async () => {
    const wrapper = await mountPanel([
      item({ targetId: 2, title: '被退回主题', status: 'rejected', reviewNote: '重复发帖' })
    ])
    try {
      const pending = wrapper.find('[data-sforum-review-section="pending"]')
      expect(pending.find('[data-empty-state]').attributes('data-title')).toBe(lookup(zh(), 'moderation.authorStatus.pendingEmptyTitle'))
      expect(wrapper.find('[data-sforum-review-section="rejected"]').find('[data-empty-state]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('targets link to the topic page or the comment anchor', async () => {
    const wrapper = await mountPanel([
      item({ targetType: 'topic', targetId: 12, title: '主题' }),
      item({ targetType: 'comment', targetId: 55, topicId: 12, title: '评论' })
    ])
    try {
      const hrefs = wrapper.findAll('[data-link]').map(node => node.attributes('href'))
      expect(hrefs).toEqual(['/t/12', '/t/12#comment-55'])
    } finally {
      wrapper.unmount()
    }
  })

  test('renders the English copy when the locale is en-US', async () => {
    const wrapper = await mountPanel([
      item({ targetId: 2, title: 'Rejected topic', status: 'rejected', reviewNote: 'Missing sources' })
    ], en())
    try {
      const rejected = wrapper.find('[data-sforum-review-section="rejected"]')
      expect(rejected.text()).toContain(lookup(en(), 'moderation.authorStatus.rejected'))
      expect(rejected.text()).toContain(lookup(en(), 'moderation.authorStatus.reason'))
      expect(rejected.text()).toContain('Missing sources')
      expect(wrapper.find('[data-settings-shell]').attributes('data-title')).toBe(lookup(en(), 'moderation.authorStatus.title'))
      expect(wrapper.text()).not.toContain(lookup(zh(), 'moderation.authorStatus.rejected'))
    } finally {
      wrapper.unmount()
    }
  })

  test('both empty still shows both sections plus a plain-language create action', async () => {
    const wrapper = await mountPanel([])
    try {
      expect(wrapper.findAll('[data-sforum-review-section]')).toHaveLength(2)
      expect(wrapper.findAll('[data-empty-state]')).toHaveLength(3)
      const action = wrapper.find('[data-empty-action]')
      expect(action.exists()).toBe(true)
      expect(action.text()).toBe(lookup(zh(), 'moderation.authorStatus.createAction'))
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 真实 settings chrome 里的挂载：上面那些用例把 `SFSettingsShell` 整个 stub 掉了，
 * 只能证明「组件自己能渲染」，证明不了「它在账号设置壳里能打开」。
 * 这里换成**真实的** `SFSettingsShell.vue`（只桩掉它的子组件与抽屉状态），
 * 断言 active/title 传递与主列插槽接线都对。
 */
async function mountPanelInRealSettingsShell(items: AuthorReviewItem[]) {
  const messages = zh()
  const SidebarStub = defineComponent({
    name: 'SFSettingsAccountSidebar',
    props: { active: String },
    template: '<nav data-sidebar :data-active="active" />'
  })
  const PageHeaderStub = defineComponent({
    name: 'SFPublicPageHeader',
    props: { title: String, subtitle: String, titleId: String },
    template: '<header data-page-header :data-title="title" :data-subtitle="subtitle" />'
  })
  const ColumnFooterStub = defineComponent({ name: 'SFContentColumnFooter', template: '<footer data-column-footer />' })
  const EmptyStub = defineComponent({ name: 'EmptyStub', template: '<span />' })

  const Shell = await compileVueSfc(shellPath, 'sf-settings-shell-real', {
    computed,
    useI18n: () => ({ t: (key: string) => lookup(messages, key) }),
    useState: (_key: string, init: () => boolean) => ref(init()),
    usePublicSidebarDrawer: () => ({ closeDrawer: () => {} }),
    SFSettingsAccountSidebar: SidebarStub,
    SFContentColumnFooter: ColumnFooterStub,
    SFPublicPageHeader: PageHeaderStub,
    SFPublicMobileUserMenu: EmptyStub,
    SFPublicMobileRightDrawerHeader: EmptyStub
  })

  const Panel = await compileVueSfc(componentPath, 'sf-content-review-in-real-shell', {
    computed,
    ref,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => {
        const raw = lookup(messages, key)
        return params ? raw.replace(/\{(\w+)\}/g, (_m, name: string) => params[name] ?? '') : raw
      }
    }),
    useLocalePath: () => (path: string) => path,
    useSiteDateTime: () => ({ format: () => '2026-01-02 11:04' }),
    navigateTo: () => {},
    useAuthorContentReview: () => ({ list: async () => ({ items }) }),
    useAsyncData: async (_key: string, loader: () => Promise<AuthorReviewItem[]>) => {
      const data = ref(await loader())
      return { data, pending: ref(false), error: ref(null), refresh: async () => {} }
    },
    apiErrorMessage: () => '',
    authorReviewSections,
    authorReviewReviewNote,
    authorReviewTargetPath,
    SFSettingsShell: Shell
  })

  const Root = defineComponent({
    name: 'AuthorReviewRealShellRoot',
    components: { Panel },
    template: '<Suspense><Panel /></Suspense>'
  })

  const wrapper = mount(Root, {
    global: {
      components: {
        SFButton: ButtonStub,
        SFEmptyState: EmptyStateStub,
        SFSkeleton: SkeletonStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UIcon: IconStub,
        NuxtLink: NuxtLinkStub,
        UApp: PassthroughStub
      }
    }
  })
  await flushPromises()
  return wrapper
}

describe('author content review renders inside the real settings chrome', () => {
  test('the real shell receives active=content and renders the lists in its main column', async () => {
    const wrapper = await mountPanelInRealSettingsShell([
      item({ targetId: 2, title: '被退回主题', status: 'rejected', reviewNote: '缺少来源链接' })
    ])
    try {
      // 真实壳的根节点是 main.sforum-settings，且整页只有这一个 main。
      expect(wrapper.findAll('main.sforum-settings')).toHaveLength(1)
      expect(wrapper.find('[data-settings-shell]').exists()).toBe(false)

      // 左栏拿到 active=content（导航高亮靠它）。
      expect(wrapper.find('[data-sidebar]').attributes('data-active')).toBe('content')

      // 页头拿到真实文案。
      expect(wrapper.find('[data-page-header]').attributes('data-title')).toBe(lookup(zh(), 'moderation.authorStatus.title'))
      expect(wrapper.find('[data-page-header]').attributes('data-subtitle')).toBe(lookup(zh(), 'moderation.authorStatus.description'))

      // 两个列表仍然在主列里，审核意见照常渲染。
      expect(wrapper.findAll('[data-sforum-review-section]')).toHaveLength(2)
      expect(wrapper.find('[data-sforum-review-note]').text()).toContain('缺少来源链接')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('author content review wiring stays on the new location', () => {
  test('the route is auth-gated and does not resurrect /my/content-review', () => {
    const page = source(pagePath)
    expect(page).toContain('requiresAuth: true')
    expect(page).not.toContain('/my/content-review')
  })

  test('the route goes through the Page Registry outlet with its own page id', () => {
    const page = source(pagePath)
    expect(page).toContain('<SFPageOutlet page="forum.settings.content">')
  })

  test('the route owns no chrome: navbar/footer come from the outlet only', () => {
    // 一旦页面自己再挂 SFHostPublicChrome / sf-navbar，就会与主题/回退壳的 chrome
    // 重复（AGENTS.md 明确禁止）。chrome 的唯一所有者是 SFPageOutlet。
    const page = source(pagePath)
    expect(page).not.toContain('SFHostPublicChrome')
    expect(page).not.toContain('sf-navbar')
    expect(page).not.toContain('sf-footer')
  })

  test('the account nav points at /settings/content', () => {
    const nav = source(navPath)
    expect(nav).toContain("localePath('/settings/content')")
    expect(nav).toContain("active === 'content'")
  })

  test('the composable reads the documented author endpoint', () => {
    const composable = source(composablePath)
    expect(composable).toContain("request<AuthorReviewList>('/me/content-review')")
  })

  test('both locales carry the same authorStatus keys', () => {
    const zhKeys = Object.keys((zh().moderation as Record<string, Record<string, unknown>>).authorStatus!).sort()
    const enKeys = Object.keys((en().moderation as Record<string, Record<string, unknown>>).authorStatus!).sort()
    expect(enKeys).toEqual(zhKeys)
    for (const key of ['nav', 'pending', 'rejected', 'reason', 'reviewNoteEmpty', 'pendingEmptyTitle', 'rejectedEmptyTitle']) {
      expect(zhKeys).toContain(key)
    }
  })
})
