import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
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
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料页徽章 tab 的渲染契约（M8 会按这些断言）：
 * 卡片根 class `sf-profile-badge-card`、未获得态 `data-earned="false"`、
 * 进度条 `role="progressbar"` + `aria-valuenow` / `aria-valuemax` / `aria-valuetext`。
 * 期望文案取自真实 zh-CN locale，不在这里自造。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') {
    throw new Error(`missing locale key: ${key}`)
  }
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

/** 真锚点 + 记录点击目标：用来断言「点击后去 /badges」，而不是只断言 href 存在。 */
const navigations: string[] = []
const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: [String, Object], default: '' } },
  setup(props, { slots }) {
    const href = typeof props.to === 'string' ? props.to : (props.to as { path?: string }).path
    return () => h('a', {
      href,
      onClick: (event: Event) => {
        event.preventDefault()
        navigations.push(String(href))
      }
    }, slots.default?.())
  }
})

const EmptyStateStub = defineComponent({
  name: 'SFEmptyState',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="sf-empty-state-stub"><span class="sf-empty-state-stub__title">{{ title }}</span><span class="sf-empty-state-stub__description">{{ description }}</span></div>'
})

// 单张卡已抽成独立 SFC（组件拆分的装配线，不是行为变更）；这里把它编译好喂给容器。
const badgeCardPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeCard.vue', import.meta.url))
const has = (key: string) => typeof lookup(key) === 'string'

/** 可变 locale：条件句子的中英选择就靠它切，卡片的 tier 名仍走 zh 文案。 */
const localeRef = ref('zh-CN')

const SFBadgeCard = await compileVueSfc(badgeCardPath, 'sf-badge-card', {
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

const tabPath = fileURLToPath(new URL('../../app/components/profile/SFProfileBadgeTab.vue', import.meta.url))
const SFProfileBadgeTab = await compileVueSfc(tabPath, 'sf-profile-badge-tab', {
  computed,
  useI18n: () => ({ t, te: has, locale: localeRef }),
  useSiteDateTime: () => ({ formatDateOnly: (value: string) => value.slice(0, 10) }),
  useLocalePath: () => (path: string) => path,
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  forumBadgeRequirementText,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
  SFBadgeCard
})

function mountTab(props: Record<string, unknown>) {
  return mount(SFProfileBadgeTab, {
    props,
    global: { components: { UIcon: UIconStub, NuxtLink: NuxtLinkStub, SFEmptyState: EmptyStateStub } }
  })
}

const groups = [
  {
    key: 'honor',
    name: '荣誉',
    position: 2,
    badges: [
      { slug: 'helper', name: '优质解答', tier: 'silver', icon: 'i-lucide-award', tierPosition: 1, position: 1, earned: true, current: 0, threshold: 0, grantedAt: '2026-11-03T08:00:00Z' }
    ]
  },
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      { slug: 'first-post', name: '初次发帖', tier: 'bronze', icon: 'i-lucide-zap', tierPosition: 2, position: 1, earned: false, current: 3, threshold: 10, grantedAt: null },
      { slug: 'liked', name: '广受好评', tier: 'silver', icon: 'i-lucide-thumbs-up', tierPosition: 1, position: 2, earned: true, current: 100, threshold: 100, grantedAt: '2026-11-01T08:00:00Z' }
    ]
  }
]

describe('SFProfileBadgeTab', () => {
  test('groups follow position and entries follow the server tier position', () => {
    const wrapper = mountTab({ groups })
    try {
      const titles = wrapper.findAll('.sf-badge-group__name').map(node => node.text())
      expect(titles).toEqual(['内容', '荣誉'])
      const slugs = wrapper.findAll('.sf-profile-badge-card').map(node => node.attributes('data-badge-slug'))
      expect(slugs).toEqual(['liked', 'first-post', 'helper'])
    } finally {
      wrapper.unmount()
    }
  })

  test('earned and unearned cards differ only by data-earned and the progress block', () => {
    const wrapper = mountTab({ groups })
    try {
      const earned = wrapper.get('[data-badge-slug="liked"]')
      expect(earned.attributes('data-earned')).toBe('true')
      expect(earned.attributes('data-tier')).toBe('silver')
      expect(earned.attributes('aria-label')).toBe('银牌徽章：广受好评，获得于 2026-11-01')
      expect(earned.find('[role="progressbar"]').exists()).toBe(false)

      const unearned = wrapper.get('[data-badge-slug="first-post"]')
      expect(unearned.attributes('data-earned')).toBe('false')
      expect(unearned.attributes('aria-label')).toBe('铜牌徽章：初次发帖，未获得，进度 3/10')
    } finally {
      wrapper.unmount()
    }
  })

  test('progress bar exposes now/max/text and a proportional fill width', () => {
    const wrapper = mountTab({ groups })
    try {
      const bar = wrapper.get('[role="progressbar"]')
      expect(bar.attributes('aria-valuenow')).toBe('3')
      expect(bar.attributes('aria-valuemax')).toBe('10')
      expect(bar.attributes('aria-valuetext')).toBe('3 / 10')
      expect(wrapper.get('.sf-profile-badge-card__fill').attributes('style')).toContain('width: 30%')
    } finally {
      wrapper.unmount()
    }
  })

  test('group header counts earned against total', () => {
    const wrapper = mountTab({ groups })
    try {
      const counts = wrapper.findAll('.sf-badge-group__count').map(node => node.text())
      expect(counts).toEqual(['1 / 2', '1 / 1'])
    } finally {
      wrapper.unmount()
    }
  })

  test('empty and failed states come from the locale, and retry re-emits', async () => {
    const empty = mountTab({ groups: [] })
    try {
      expect(empty.text()).toContain('还没有获得徽章')
    } finally {
      empty.unmount()
    }

    const failed = mountTab({ groups: [], failed: true })
    try {
      expect(failed.text()).toContain('徽章加载失败。')
      await failed.get('.sf-profile-badges__retry').trigger('click')
      expect(failed.emitted('retry')).toHaveLength(1)
    } finally {
      failed.unmount()
    }
  })

  test('loading renders three skeleton cards without exposing fake content', () => {
    const wrapper = mountTab({ groups, loading: true })
    try {
      expect(wrapper.findAll('.sf-profile-badge-card--skeleton')).toHaveLength(3)
      expect(wrapper.find('.sf-badge-group').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('links to the public catalog from the profile tab (B-27)', async () => {
    navigations.length = 0
    const wrapper = mountTab({ groups })
    try {
      const link = wrapper.get('.sf-profile-badges__more-link')
      // 真锚点，不是 @click 改 location。
      expect(link.element.tagName).toBe('A')
      expect(link.attributes('href')).toBe('/badges')
      // 文案已本地化（不是裸键）。
      expect(link.text()).toBe('查看全部徽章')

      await link.trigger('click')
      expect(navigations).toEqual(['/badges'])
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * catalog 变体（`/badges`）：只差 designer 规范 §10 的七处。
 * 这里钉的是「差在哪儿」——排序口径、组计数、勾章、空态文案、骨架数量、失败态语义。
 */
const catalogGroups = [
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      { slug: 'bronze-first', name: '初次发帖', tier: 'bronze', icon: '', tierPosition: 2, position: 2, earned: false },
      { slug: 'gold-writer', name: '笔耕不辍', tier: 'gold', icon: '', tierPosition: 0, position: 1, earned: true, grantedAt: '2026-11-01T08:00:00Z' }
    ]
  }
]

describe('SFProfileBadgeTab · catalog variant', () => {
  test('sorts entries by position instead of tier weight', () => {
    const wrapper = mountTab({ groups: catalogGroups, variant: 'catalog' })
    try {
      const slugs = wrapper.findAll('.sf-profile-badge-card').map(node => node.attributes('data-badge-slug'))
      expect(slugs).toEqual(['gold-writer', 'bronze-first'])
    } finally {
      wrapper.unmount()
    }
  })

  test('labels the section with the catalog title and counts only totals', () => {
    const wrapper = mountTab({ groups: catalogGroups, variant: 'catalog' })
    try {
      expect(wrapper.get('section.sf-profile-badges').attributes('aria-label')).toBe('徽章目录')
      expect(wrapper.get('.sf-badge-group__count').text()).toBe('2 枚')
    } finally {
      wrapper.unmount()
    }
  })

  test('earned catalog cards say earned instead of a blank grant date', () => {
    const wrapper = mountTab({ groups: catalogGroups, variant: 'catalog' })
    try {
      const earned = wrapper.get('[data-badge-slug="gold-writer"]')
      // wire 上没有 grantedAt：不能复用「获得于 {date}」模板，否则会读成「获得于 」。
      expect(earned.attributes('aria-label')).toBe('金牌徽章：笔耕不辍，已获得')
      expect(earned.text()).toContain('已获得')
      expect(earned.text()).not.toContain('获得于')
    } finally {
      wrapper.unmount()
    }
  })

  test('earned cards get a check seal and no catalog card gets a progress bar', () => {
    const wrapper = mountTab({ groups: catalogGroups, variant: 'catalog' })
    try {
      expect(wrapper.get('[data-badge-slug="gold-writer"] .sf-profile-badge-card__seal').exists()).toBe(true)
      expect(wrapper.find('[data-badge-slug="bronze-first"] .sf-profile-badge-card__seal').exists()).toBe(false)
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('empty state uses the catalog copy instead of the profile copy', () => {
    const wrapper = mountTab({ groups: [], variant: 'catalog' })
    try {
      expect(wrapper.get('.sf-empty-state-stub__title').text()).toBe('还没有徽章')
      expect(wrapper.get('.sf-empty-state-stub__description').text()).toBe('站点还没有配置任何启用中的徽章。')
      expect(wrapper.text()).not.toContain('还没有获得徽章')
    } finally {
      wrapper.unmount()
    }
  })

  test('loading renders eight skeletons and hides the profile entry link', () => {
    const wrapper = mountTab({ groups: catalogGroups, loading: true, variant: 'catalog' })
    try {
      expect(wrapper.findAll('.sf-profile-badge-card--skeleton')).toHaveLength(8)
      expect(wrapper.find('.sf-profile-badges__more-link').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('failed state is an alert with a retry control', async () => {
    const wrapper = mountTab({ groups: [], failed: true, variant: 'catalog' })
    try {
      expect(wrapper.get('.sf-profile-badges__state').attributes('role')).toBe('alert')
      await wrapper.get('.sf-profile-badges__retry').trigger('click')
      expect(wrapper.emitted('retry')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 条件句子（`requirementText`）的渲染契约。
 *
 * 句子由服务端生成、逐语言回落；前端只选语言。所以这里钉三件事：
 * 1) 两种语言各取到自己的那份；
 * 2) 选中语言为空时回落到另一种，两边都空则**整行不渲染**；
 * 3) 页面上只出现人话，不出现规则内部结构（`cmp` / `params` / `op` / `rule` …）。
 */
const requirementGroups = (requirementText: unknown) => [
  {
    key: 'content',
    name: '内容',
    position: 1,
    badges: [
      {
        slug: 'first-post',
        name: '初次发帖',
        tier: 'bronze',
        icon: '',
        tierPosition: 0,
        position: 1,
        earned: false,
        current: 3,
        threshold: 10,
        requirementText
      }
    ]
  }
]

const BILINGUAL = { zh: '在任意版块发布 10 个主题', en: 'Publish 10 topics in any category' }

function descriptionText(groupsValue: unknown[]): string | undefined {
  const wrapper = mountTab({ groups: groupsValue })
  try {
    const node = wrapper.find('.sf-profile-badge-card__description')
    return node.exists() ? node.text() : undefined
  } finally {
    wrapper.unmount()
  }
}

describe('SFProfileBadgeTab · requirementText', () => {
  test('zh-CN shows the Chinese sentence and en-US shows the English one', () => {
    expect(descriptionText(requirementGroups(BILINGUAL))).toBe(BILINGUAL.zh)
    localeRef.value = 'en-US'
    try {
      expect(descriptionText(requirementGroups(BILINGUAL))).toBe(BILINGUAL.en)
    } finally {
      localeRef.value = 'zh-CN'
    }
  })

  test('an empty selected language falls back to the other instead of rendering nothing', () => {
    // 选中语言缺失：中文空 → 显示英文；英文空 → 显示中文。
    expect(descriptionText(requirementGroups({ zh: '', en: BILINGUAL.en }))).toBe(BILINGUAL.en)
    localeRef.value = 'en-US'
    try {
      expect(descriptionText(requirementGroups({ zh: BILINGUAL.zh, en: '' }))).toBe(BILINGUAL.zh)
    } finally {
      localeRef.value = 'zh-CN'
    }
  })

  test('both languages empty (multi-condition badge) renders no description row at all', () => {
    expect(descriptionText(requirementGroups({ zh: '', en: '' }))).toBeUndefined()
    // 键整个缺失（老载荷）也必须是同一结果，不能画出空行。
    expect(descriptionText(requirementGroups(null))).toBeUndefined()
  })

  test('the rendered sentence carries no rule AST tokens', () => {
    const sentence = 'Reply in 5 different topics; each parent property counts once'
    const text = descriptionText(requirementGroups({ zh: sentence, en: sentence }))
    expect(text).toBe(sentence)
    // 词边界正则：子串匹配会把 `topics` 里的 `op`、`params` 里的 `param` 误判成泄露。
    expect(text).not.toMatch(/\b(cmp|params|children|op|rule)\b/)
    expect(text).not.toMatch(/\b(topics|parent)\b.*\b(ast|node)\b/)
  })
})
