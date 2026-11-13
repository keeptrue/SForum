import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, nextTick, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
} from '~/utils/forum/forumBadge'

/**
 * 徽章详情弹窗的渲染契约（图5）：
 *
 * - 大图标（等级色）/ 名称 / 等级徽标 / 分组名 / 说明 / 完整获得条件 / 进度 / 获得时间。
 * - 佩戴按钮只属于「本人 + 已获得 + 资料页」；目录页与未获得都没有。
 * - 纯展示：佩戴只 `emit('toggle-wear')`，关闭只 `emit('update:open', false)`。
 * - 缺哪块就不渲染哪块：没有说明/条件时不留空标题，也没有空段落。
 */
const zh = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    zh
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}
const has = (key: string) => typeof lookup(key) === 'string'

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

/** UModal 桩：只把 `open` 与 `#content` 透出来，`update:open` 原样上抛。 */
const UModalStub = defineComponent({
  name: 'UModal',
  props: {
    open: { type: Boolean, default: false },
    title: { type: String, default: '' },
    ui: { type: Object, default: () => ({}) }
  },
  emits: ['update:open'],
  setup(props, { slots }) {
    return () => (props.open
      ? h('div', { class: 'umodal-stub', 'data-title': props.title }, slots.content?.())
      : null)
  }
})

/**
 * 信息块已抽成 `SFBadgeDetailBody`（与庆祝弹窗同源）。这里编译**真组件**再喂给弹窗，
 * 否则「抽出去之后详情弹窗的渲染契约不变」这句话就无从证明。
 */
const bodyPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeDetailBody.vue', import.meta.url))
const SFBadgeDetailBody = await compileVueSfc(bodyPath, 'sf-badge-detail-body', {
  computed,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value.slice(0, 10) }),
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
})

const modalPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeDetailModal.vue', import.meta.url))
const SFBadgeDetailModal = await compileVueSfc(modalPath, 'sf-badge-detail-modal', {
  computed,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  SFBadgeDetailBody
})

const earnedBadge = {
  slug: 'first-post',
  name: '初次发帖',
  tier: 'gold',
  tierName: { zh: '金牌', en: 'Gold' },
  tierColor: 'amber',
  icon: 'i-lucide-zap',
  position: 0,
  earned: true,
  grantedAt: '2026-11-03T08:00:00Z',
  description: '第一次在站内发布主题。',
  requirementText: { zh: '在任意版块发布 1 个主题', en: 'Publish 1 topic in any category' }
}

const unearnedBadge = {
  slug: 'veteran',
  name: '资深会员',
  tier: 'silver',
  tierColor: '',
  icon: '',
  position: 1,
  earned: false,
  current: 3,
  threshold: 10,
  requirementText: { zh: '在任意版块发布 10 个主题', en: 'Publish 10 topics in any category' }
}

function mountModal(props: Record<string, unknown>) {
  return mount(SFBadgeDetailModal, {
    props: { open: true, badge: earnedBadge, ...props },
    global: { components: { UIcon: UIconStub, UModal: UModalStub } }
  })
}

describe('SFBadgeDetailModal · 内容', () => {
  test('renders the big icon, name, tier badge, group name, description and requirement', () => {
    const wrapper = mountModal({ groupName: '内容', self: true })
    try {
      const panel = wrapper.get('.sf-badge-detail')
      // 可访问名字指向可见标题（UModal 的 title 与 h2 是同一句话）。
      expect(panel.attributes('aria-labelledby')).toBe('sf-badge-detail-title')
      expect(wrapper.get('.umodal-stub').attributes('data-title')).toBe('初次发帖')
      // 信息块是抽出去的共用组件（不是弹窗里另抄一份）。
      expect(wrapper.find('.sf-badge-detail > .sf-badge-detail-body').exists()).toBe(true)

      expect(wrapper.get('#sf-badge-detail-title').text()).toBe('初次发帖')
      expect(wrapper.get('.sf-badge-detail-body__icon .sf-badge-detail-body__glyph').attributes('data-icon')).toBe('i-lucide-zap')
      expect(wrapper.get('.sf-badge-detail-body__tier').text()).toBe('金牌')
      expect(wrapper.get('.sf-badge-detail-body__group').text()).toBe('内容')

      const sections = wrapper.findAll('.sf-badge-detail-body__section')
      expect(sections).toHaveLength(2)
      expect(wrapper.text()).toContain('第一次在站内发布主题。')
      expect(wrapper.text()).toContain('在任意版块发布 1 个主题')
      // 标签也是本地化文案，不是裸键。
      expect(wrapper.text()).toContain('徽章说明')
      expect(wrapper.text()).toContain('获得条件')
      expect(wrapper.text()).not.toContain('badges.detail')

      // 标题层级不跳级：徽章名 h2 → 字段标签 h3（抽取前就是这个层级，别退回 h4）。
      expect(wrapper.get('#sf-badge-detail-title').element.tagName).toBe('H2')
      for (const label of wrapper.findAll('.sf-badge-detail-body__label')) {
        expect(label.element.tagName).toBe('H3')
      }
    } finally {
      wrapper.unmount()
    }
  })

  test('the tier color drives the hue variables (payload color, not a guessed one)', () => {
    const wrapper = mountModal({})
    try {
      const style = wrapper.get('.sf-badge-detail-body').attributes('style') ?? ''
      expect(style).toContain('--sf-tier-hue-light: var(--sf-tl-amber)')
    } finally {
      wrapper.unmount()
    }

    // 没给 tierColor 的等级落中性 slate，不猜。
    const neutral = mountModal({ badge: unearnedBadge })
    try {
      expect(neutral.get('.sf-badge-detail-body').attributes('style')).toContain('--sf-tier-hue-light: var(--sf-tl-slate)')
    } finally {
      neutral.unmount()
    }
  })

  test('progress shows x/y, the percentage and a proportional bar', () => {
    const wrapper = mountModal({ badge: unearnedBadge })
    try {
      const bar = wrapper.get('[role="progressbar"]')
      expect(bar.attributes('aria-valuenow')).toBe('3')
      expect(bar.attributes('aria-valuemax')).toBe('10')
      expect(bar.attributes('aria-valuetext')).toBe('3 / 10（30%）')
      expect(wrapper.get('.sf-badge-detail-body__fill').attributes('style')).toContain('width: 30%')
      expect(wrapper.text()).toContain('进度')
    } finally {
      wrapper.unmount()
    }
  })

  test('the grant date only shows when the payload carries one', () => {
    const profile = mountModal({})
    try {
      expect(profile.get('.sf-badge-detail-body__earned').text()).toBe('获得于 2026-11-03')
    } finally {
      profile.unmount()
    }

    // 目录页 wire 上没有 grantedAt（公开面白名单），所以那里没有获得时间可画。
    const catalog = mountModal({
      variant: 'catalog',
      badge: { ...earnedBadge, grantedAt: undefined }
    })
    try {
      expect(catalog.find('.sf-badge-detail-body__earned').exists()).toBe(false)
      expect(catalog.text()).not.toContain('获得于')
    } finally {
      catalog.unmount()
    }
  })

  test('missing description and requirement render no empty sections', () => {
    const wrapper = mountModal({
      badge: { slug: 'manual', name: '手动徽章', tier: 'bronze', icon: '', position: 0, earned: true, grantedAt: '2026-11-03T08:00:00Z' }
    })
    try {
      expect(wrapper.findAll('.sf-badge-detail-body__section')).toHaveLength(0)
      expect(wrapper.text()).not.toContain('徽章说明')
      expect(wrapper.text()).not.toContain('获得条件')
    } finally {
      wrapper.unmount()
    }
  })

  test('a closed modal renders nothing at all', () => {
    const wrapper = mountModal({ open: false })
    try {
      expect(wrapper.find('.sf-badge-detail').exists()).toBe(false)
      expect(wrapper.find('.umodal-stub').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFBadgeDetailModal · 佩戴与关闭', () => {
  test('the wear toggle only appears for self + earned + profile', () => {
    const self = mountModal({ self: true })
    try {
      expect(self.get('.sf-badge-detail__wear').attributes('aria-pressed')).toBe('false')
      expect(self.get('.sf-badge-detail__wear').text()).toBe('佩戴')
      expect(self.get('.sf-badge-detail__wear').attributes('aria-label')).toBeUndefined()
      expect(self.get('.sf-badge-detail__wear').attributes('title')).toBe('佩戴')
    } finally {
      self.unmount()
    }

    // 他人页 / 目录页 / 未获得：都没有佩戴按钮。
    for (const props of [
      { self: false },
      { self: true, variant: 'catalog' },
      { self: true, badge: unearnedBadge }
    ]) {
      const wrapper = mountModal(props)
      try {
        expect(wrapper.find('.sf-badge-detail__wear').exists()).toBe(false)
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('the visible text is the action, the state is aria-pressed, the title repeats the action', async () => {
    const wrapper = mountModal({ self: true, worn: true })
    try {
      const button = wrapper.get('.sf-badge-detail__wear')
      // 可见文字就是动作：已佩戴时写「取消佩戴」，不写「佩戴中」这种状态词。
      // WCAG 2.5.3 label-in-name 仍成立——读屏与语音控制按可见文字「取消佩戴」识别，
      // 所以 title 与可见文字同文案（title 不承担区分状态或动作的职责，状态只在 aria-pressed）。
      expect(button.attributes('aria-pressed')).toBe('true')
      expect(button.text()).toBe('取消佩戴')
      expect(button.attributes('aria-label')).toBeUndefined()
      expect(button.attributes('title')).toBe('取消佩戴')

      await button.trigger('click')
      expect(wrapper.emitted('toggle-wear')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('both close controls emit update:open=false with a localized aria-label', async () => {
    const wrapper = mountModal({ self: true })
    try {
      const close = wrapper.get('.sf-badge-detail__close')
      expect(close.attributes('aria-label')).toBe('关闭徽章详情')
      expect(close.attributes('title')).toBe('关闭徽章详情')

      await close.trigger('click')
      expect(wrapper.emitted('update:open')).toEqual([[false]])

      await wrapper.get('.sf-badge-detail__done').trigger('click')
      expect(wrapper.emitted('update:open')).toEqual([[false], [false]])
    } finally {
      wrapper.unmount()
    }
  })
})

/* ------------------------------------------------------------------------- */
/* 深链：`?badge=` 的读写与失效清理                                            */
/* ------------------------------------------------------------------------- */

type ReplacePayload = { query: Record<string, unknown> }

const composablePath = new URL('../../app/composables/profile/useBadgeDetailQuery.ts', import.meta.url)

type BadgeQueryApi = {
  focusSlug: { value: string }
  openBadge: (slug: string) => void
  closeBadge: () => void
  setFocusSlug: (slug: string) => void
  syncBadgeQuery: (slug: string) => void
  resetBadgeIfMissing: (available: string[]) => void
}

/**
 * 直接跑真实源码（剥 import，注入 route/router/ref/watch）。
 *
 * `route` 必须是**响应式**的：`?badge=` 要跟随浏览器前进/后退，watcher 看不见地址栏变化
 * 就等于没实现。`router.replace` 之后 route 同步改写，与真实路由行为一致。
 */
function loadBadgeQuery(initialQuery: Record<string, unknown> = {}) {
  const source = readFileSync(composablePath, 'utf8')
  const executable = new Bun.Transpiler({ loader: 'ts', target: 'browser' })
    .transformSync(source)
    .replace('export function useBadgeDetailQuery', 'return function useBadgeDetailQuery')

  const route = ref<{ query: Record<string, unknown> }>({ query: { ...initialQuery } })
  // useRoute() 在真实 Nuxt 里返回**稳定的**响应式 route 对象：这里用 getter 代理同样的语义，
  // 否则 composable 会把创建那一刻的 query 快照下来，watcher 永远看不到地址栏变化。
  const routeProxy = {
    get query() {
      return route.value.query
    }
  }
  const replaces: ReplacePayload[] = []
  const factory = new Function('useRoute', 'useRouter', 'ref', 'watch', executable)
  const useBadgeDetailQuery = factory(
    () => routeProxy,
    () => ({
      replace: (payload: ReplacePayload) => {
        route.value = { query: { ...payload.query } }
        replaces.push(payload)
        return Promise.resolve()
      }
    }),
    ref,
    watch
  ) as () => BadgeQueryApi

  return {
    api: useBadgeDetailQuery(),
    replaces,
    get query() {
      return route.value.query
    },
    /** 模拟浏览器前进/后退：地址栏变了，组件不重新挂载。 */
    navigate(query: Record<string, unknown>) {
      route.value = { query }
    }
  }
}

describe('useBadgeDetailQuery', () => {
  test('initialises from ?badge= and ignores malformed values', () => {
    expect(loadBadgeQuery({ badge: 'first-post' }).api.focusSlug.value).toBe('first-post')
    expect(loadBadgeQuery({ badge: ['helper'] }).api.focusSlug.value).toBe('helper')
    expect(loadBadgeQuery({ badge: '   ' }).api.focusSlug.value).toBe('')
    expect(loadBadgeQuery({ badge: 42 }).api.focusSlug.value).toBe('')
    expect(loadBadgeQuery({}).api.focusSlug.value).toBe('')
  })

  test('opening writes the query and keeps the other parameters', () => {
    const ctx = loadBadgeQuery({ tab: 'badges' })
    ctx.api.openBadge('helper')
    expect(ctx.api.focusSlug.value).toBe('helper')
    expect(ctx.query).toEqual({ tab: 'badges', badge: 'helper' })
  })

  test('closing removes only the badge parameter', () => {
    const ctx = loadBadgeQuery({ tab: 'badges', badge: 'helper', page: '2' })
    ctx.api.closeBadge()
    expect(ctx.api.focusSlug.value).toBe('')
    expect(ctx.query).toEqual({ tab: 'badges', page: '2' })
  })

  test('opening the badge already in the query does not navigate again', () => {
    const ctx = loadBadgeQuery({ badge: 'helper' })
    ctx.api.openBadge('helper')
    expect(ctx.replaces).toEqual([])
  })

  test('an unknown or disabled slug is cleared silently, without an error', () => {
    const ctx = loadBadgeQuery({ tab: 'badges', badge: 'retired-badge' })
    ctx.api.resetBadgeIfMissing(['helper', 'first-post'])
    expect(ctx.api.focusSlug.value).toBe('')
    expect(ctx.query).toEqual({ tab: 'badges' })
  })

  test('a slug that is still available is left untouched', () => {
    const ctx = loadBadgeQuery({ badge: 'helper' })
    ctx.api.resetBadgeIfMissing(['helper', 'first-post'])
    expect(ctx.api.focusSlug.value).toBe('helper')
    expect(ctx.replaces).toEqual([])
  })

  test('update:focusSlug maps empty to close and non-empty to open', () => {
    const ctx = loadBadgeQuery({})
    ctx.api.setFocusSlug('helper')
    expect(ctx.query).toEqual({ badge: 'helper' })
    ctx.api.setFocusSlug('')
    expect(ctx.query).toEqual({})
    expect(ctx.api.focusSlug.value).toBe('')
  })

  test('follows the address bar on browser back/forward', async () => {
    const ctx = loadBadgeQuery({ tab: 'badges' })
    expect(ctx.api.focusSlug.value).toBe('')

    // 前进：地址栏出现 ?badge=helper，组件没有重新挂载。
    ctx.navigate({ tab: 'badges', badge: 'helper' })
    await nextTick()
    expect(ctx.api.focusSlug.value).toBe('helper')

    // 后退：该参数消失，弹窗跟着关。
    ctx.navigate({ tab: 'badges' })
    await nextTick()
    expect(ctx.api.focusSlug.value).toBe('')

    // 跟随地址栏不该反过来写地址栏（写回路径的「值相同」短路）。
    expect(ctx.replaces).toEqual([])
  })

  test('reusing the component for another profile without ?badge= clears the selection', async () => {
    const ctx = loadBadgeQuery({ badge: 'first-post' })
    expect(ctx.api.focusSlug.value).toBe('first-post')

    // `/u/a?badge=x` → `/u/b`：同一个 SFC 复用，地址栏里已经没有 badge。
    ctx.navigate({})
    await nextTick()
    expect(ctx.api.focusSlug.value).toBe('')
    expect(ctx.replaces).toEqual([])
  })
})
