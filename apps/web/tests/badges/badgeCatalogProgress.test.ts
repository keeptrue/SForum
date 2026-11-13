import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import { badgeWornErrorKey, useBadgeWorn } from '~/composables/forum/badges/useBadgeWorn'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeRequirementText,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups
} from '~/utils/forum/forumBadge'

/**
 * `/badges` 目录页的进度展示契约（M4）。
 *
 * 两向都要成立，缺一条都算没做完：
 * - 已获得（earned=true）：**不画进度条**，aria 走 `badges.catalog.ariaLabelEarned`；
 * - 未获得且 `threshold > 0`：画进度条，aria 走 `badges.card.ariaLabelUngranted`；
 * - 未获得但没有阈值（手动徽章 / 多条件 / lte 条件）：不画进度条，aria 走 `NotEarned`。
 *
 * 进度字段现在由规则引擎派生，所以目录页与资料页用的是**同一张卡片实现**：
 * 这里不新造卡片，只断言共用实现的行为。
 */
const messages = JSON.parse(readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, string | number> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (match, name: string) => (
    params[name] === undefined ? match : String(params[name])
  ))
}

const has = (key: string) => typeof lookup(key) === 'string'

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  setup(props, { slots }) {
    const href = typeof props.to === 'string' ? props.to : (props.to as { path?: string }).path
    return () => h('a', { href }, slots.default?.())
  }
})

const EmptyStateStub = defineComponent({
  name: 'SFEmptyState',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="sf-empty-state-stub">{{ title }}</div>'
})

const cardPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeCard.vue', import.meta.url))
const SFBadgeCard = await compileVueSfc(cardPath, 'sf-badge-card-catalog-progress', {
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

function mountCard(badge: Record<string, unknown>, variant: 'catalog' | 'profile' = 'catalog') {
  return mount(SFBadgeCard, {
    props: { badge: { slug: 'x', name: 'X', tier: 'bronze', icon: '', position: 0, ...badge }, variant },
    global: { components: { UIcon: UIconStub } }
  })
}

describe('SFBadgeCard · catalog 进度两向', () => {
  test('an earned catalog card never draws a progress bar, even if the payload carries numbers', () => {
    const wrapper = mountCard({ slug: 'liked', name: '广受好评', earned: true, current: 100, threshold: 100 })
    try {
      const card = wrapper.get('.sf-profile-badge-card')
      expect(card.attributes('data-earned')).toBe('true')
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
      expect(wrapper.find('.sf-profile-badge-card__progress').exists()).toBe(false)
      expect(card.get('.sf-profile-badge-card__open').attributes('aria-label'))
        .toBe(t('badges.catalog.ariaLabelEarned', { tier: '铜牌', name: '广受好评' }))
    } finally {
      wrapper.unmount()
    }
  })

  test('an unearned measurable catalog card draws the bar and uses the ungranted aria template', () => {
    const wrapper = mountCard({ slug: 'first-post', name: '初次发帖', earned: false, current: 3, threshold: 10, position: 1 })
    try {
      const card = wrapper.get('.sf-profile-badge-card')
      expect(card.attributes('data-earned')).toBe('false')
      expect(card.get('.sf-profile-badge-card__open').attributes('aria-label'))
        .toBe(t('badges.card.ariaLabelUngranted', { tier: '铜牌', name: '初次发帖', current: 3, threshold: 10 }))

      const bar = wrapper.get('[role="progressbar"]')
      expect(bar.attributes('aria-valuenow')).toBe('3')
      expect(bar.attributes('aria-valuemax')).toBe('10')
      expect(bar.attributes('aria-valuetext')).toBe('3 / 10')
      expect(wrapper.get('.sf-profile-badge-card__progress-text').text()).toBe('3 / 10')
      expect(wrapper.get('.sf-profile-badge-card__fill').attributes('style')).toContain('width: 30%')
    } finally {
      wrapper.unmount()
    }
  })

  test('an unearned card without a threshold stays bar-less and uses the not-earned template', () => {
    const wrapper = mountCard({ slug: 'manual', name: '手动徽章', tier: 'gold', earned: false })
    try {
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
      expect(wrapper.get('.sf-profile-badge-card__open').attributes('aria-label'))
        .toBe(t('badges.card.ariaLabelNotEarned', { tier: '金牌', name: '手动徽章' }))
      expect(wrapper.get('.sf-profile-badge-card__date').text()).toBe(t('badges.card.notEarned'))
    } finally {
      wrapper.unmount()
    }
  })

  test('zero progress is still a real bar (current=0 / threshold=5)', () => {
    const wrapper = mountCard({ slug: 'zero', name: '零起点', earned: false, current: 0, threshold: 5 })
    try {
      const bar = wrapper.get('[role="progressbar"]')
      expect(bar.attributes('aria-valuenow')).toBe('0')
      expect(bar.attributes('aria-valuemax')).toBe('5')
      expect(wrapper.get('.sf-profile-badge-card__fill').attributes('style')).toContain('width: 0%')
    } finally {
      wrapper.unmount()
    }
  })
})

/* ------------------------------------------------------------------------- */
/* 目录页容器：进度字段不得改变 position 排序                                  */
/* ------------------------------------------------------------------------- */

// tab 的 setup 会调用 Nuxt 自动导入的 useState/useApiClient/useToast：只在这几组用例期间装上，
// 结束后恢复，避免把桩泄给同进程的其它测试文件。
const globals = globalThis as unknown as Record<string, unknown>
const stateStore = new Map<string, ReturnType<typeof ref>>()
const installed: Record<string, unknown> = {}

beforeAll(() => {
  for (const key of ['useToast', 'useApiClient', 'useState']) installed[key] = globals[key]
  globals.useToast = () => ({ add: () => {} })
  globals.useApiClient = () => ({ request: async () => ({ mode: 'auto', worn: null, automatic: null, upgrade: null }) })
  globals.useState = <T>(key: string, init: () => T) => {
    if (!stateStore.has(key)) stateStore.set(key, ref(init()))
    return stateStore.get(key) as ReturnType<typeof ref<T>>
  }
})

afterAll(() => {
  for (const [key, value] of Object.entries(installed)) {
    if (value === undefined) delete globals[key]
    else globals[key] = value
  }
})

const tabPath = fileURLToPath(new URL('../../app/components/profile/SFProfileBadgeTab.vue', import.meta.url))
const SFProfileBadgeTab = await compileVueSfc(tabPath, 'sf-profile-badge-tab-catalog-progress', {
  computed,
  watch,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  useToast: () => ({ add: () => {} }),
  useBadgeWorn,
  // 目录页没有佩戴动作；tab setup 会实例化它，给无副作用桩即可。
  useBadgeWearToggle: () => ({
    toggleWear: async () => {},
    wearBadge: async () => {},
    hideWorn: async () => {}
  }),
  // tab 按账号归属判断佩戴状态条；真实组合式函数读的是本文件桩掉的 useState。
  useAuthSession,
  badgeWornErrorKey,
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeRequirementText,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
  SFBadgeCard,
  SFBadgeDetailModal: defineComponent({
    name: 'SFBadgeDetailModal',
    props: {
      open: { type: Boolean, default: false },
      badge: { type: Object, default: null },
      groupName: { type: String, default: '' },
      variant: { type: String, default: 'profile' },
      self: { type: Boolean, default: false },
      worn: { type: Boolean, default: false }
    },
    emits: ['update:open', 'toggle-wear'],
    template: '<div v-if="open" class="badge-detail-stub" />'
  })
})

const catalogGroups = [
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      { slug: 'bronze-earned', name: '已获得', tier: 'bronze', icon: '', tierPosition: 2, position: 2, earned: true, current: 10, threshold: 10 },
      { slug: 'gold-progress', name: '有进度', tier: 'gold', icon: '', tierPosition: 0, position: 3, earned: false, current: 7, threshold: 20 },
      { slug: 'silver-manual', name: '无阈值', tier: 'silver', icon: '', tierPosition: 1, position: 1, earned: false }
    ]
  }
]

describe('SFProfileBadgeTab · catalog 进度与排序', () => {
  test('catalog keeps position order even though progress fields are present', () => {
    const wrapper = mount(SFProfileBadgeTab, {
      props: { groups: catalogGroups, variant: 'catalog' },
      global: { components: { UIcon: UIconStub, NuxtLink: NuxtLinkStub, SFEmptyState: EmptyStateStub } }
    })
    try {
      const slugs = wrapper.findAll('.sf-profile-badge-card').map(node => node.attributes('data-badge-slug'))
      // position 1/2/3 升序，不是「有进度优先」也不是 tier 权重。
      expect(slugs).toEqual(['silver-manual', 'bronze-earned', 'gold-progress'])
    } finally {
      wrapper.unmount()
    }
  })

  test('inside the catalog, only the unearned measurable row gets a progress bar', () => {
    const wrapper = mount(SFProfileBadgeTab, {
      props: { groups: catalogGroups, variant: 'catalog' },
      global: { components: { UIcon: UIconStub, NuxtLink: NuxtLinkStub, SFEmptyState: EmptyStateStub } }
    })
    try {
      const bars = wrapper.findAll('[role="progressbar"]')
      expect(bars).toHaveLength(1)
      expect(wrapper.get('[data-badge-slug="gold-progress"] [role="progressbar"]').exists()).toBe(true)
      expect(wrapper.find('[data-badge-slug="bronze-earned"] [role="progressbar"]').exists()).toBe(false)
      expect(wrapper.find('[data-badge-slug="silver-manual"] [role="progressbar"]').exists()).toBe(false)
      // 已获得行仍然只有勾章与「已获得」，没有进度块。
      expect(wrapper.find('[data-badge-slug="bronze-earned"] .sf-profile-badge-card__progress').exists()).toBe(false)
      expect(wrapper.get('[data-badge-slug="bronze-earned"] .sf-profile-badge-card__seal').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('the profile variant (self=false) keeps the same progress rules', () => {
    const wrapper = mount(SFProfileBadgeTab, {
      props: { groups: catalogGroups },
      global: { components: { UIcon: UIconStub, NuxtLink: NuxtLinkStub, SFEmptyState: EmptyStateStub } }
    })
    try {
      expect(wrapper.findAll('[role="progressbar"]')).toHaveLength(1)
      expect(wrapper.find('[data-badge-slug="bronze-earned"] [role="progressbar"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
