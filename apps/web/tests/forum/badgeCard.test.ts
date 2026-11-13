import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle
} from '~/utils/forum/forumBadge'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 徽章卡片的等级渲染契约：名字与颜色优先取载荷（等级可增删）。
 *
 * 名字缺失时回退 locale 文案（绝不把 `badges.tier.<key>` 裸键画到页面上）；
 * **颜色缺失时落中性 slate，不按 tier key 猜内置色**——猜出来的色会和运营配的色打架。
 *
 * 配色本身是 CSS（hue 12% / ring 34%），这里钉的是「哪来的色、哪来的名」——
 * 色值以 `--sf-tier-hue-light/-dark` 成对下发，卡片只按主题挑一个。
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
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const has = (key: string) => typeof lookup(key) === 'string'

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
})

const cardPath = fileURLToPath(new URL('../../app/components/forum/badges/SFBadgeCard.vue', import.meta.url))
const SFBadgeCard = await compileVueSfc(cardPath, 'sf-badge-card', {
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

function mountCard(badge: Record<string, unknown>, extra: Record<string, unknown> = {}) {
  return mount(SFBadgeCard, {
    props: { badge: { position: 0, earned: true, ...badge }, ...extra },
    global: { components: { UIcon: UIconStub } }
  })
}

describe('SFBadgeCard tier rendering', () => {
  test('a server tier name and color drive the label and the hue variables', () => {
    const wrapper = mountCard({
      slug: 'legendary',
      name: '传世之作',
      tier: 'legendary',
      icon: 'i-lucide-flame',
      tierName: { zh: '传说', en: 'Legendary' },
      tierColor: 'violet',
      tierPosition: 0
    })
    try {
      const card = wrapper.get('.sf-profile-badge-card')
      expect(card.attributes('data-tier')).toBe('legendary')
      expect(wrapper.get('.sf-profile-badge-card__tier').text()).toBe('传说')
      expect(card.attributes('style')).toContain('--sf-tier-hue-light: var(--sf-tl-violet)')
      expect(card.attributes('style')).toContain('--sf-tier-hue-dark: var(--sf-tl-violet)')
    } finally {
      wrapper.unmount()
    }
  })

  test('a custom hex color gets a brightened dark-mode value', () => {
    const wrapper = mountCard({
      slug: 'custom',
      name: '自定义',
      tier: 'custom',
      icon: 'i-lucide-gem',
      tierName: { zh: '自定义', en: 'Custom' },
      tierColor: '#0f766e'
    })
    try {
      const style = wrapper.get('.sf-profile-badge-card').attributes('style') ?? ''
      expect(style).toContain('--sf-tier-hue-light: #0f766e')
      expect(style).toContain('--sf-tier-hue-dark: color-mix(in srgb, #0f766e 74%, white)')
    } finally {
      wrapper.unmount()
    }
  })

  test('a payload without tierColor falls back to the neutral slate hue, never a guessed tier color', () => {
    // 三档都不给 tierColor：色相必须是同一个中性值，而不是 gold→amber / bronze→rose 这类猜测。
    for (const tier of ['gold', 'silver', 'bronze'] as const) {
      const wrapper = mountCard({ slug: tier, name: 'X', tier, icon: 'i-lucide-medal' })
      try {
        const style = wrapper.get('.sf-profile-badge-card').attributes('style') ?? ''
        expect(style).toContain('--sf-tier-hue-light: var(--sf-tl-slate)')
        expect(style).toContain('--sf-tier-hue-dark: var(--sf-tl-slate)')
        expect(style).not.toContain('--sf-tl-amber')
        expect(style).not.toContain('--sf-tl-rose')
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('built-in tier keys still get their locale label without a payload tierName', () => {
    for (const [tier, label] of [['gold', '金牌'], ['silver', '银牌'], ['bronze', '铜牌']] as const) {
      const wrapper = mountCard({ slug: tier, name: 'X', tier, icon: 'i-lucide-medal' })
      try {
        expect(wrapper.get('.sf-profile-badge-card__tier').text()).toBe(label)
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('an unknown tier never renders a bare i18n key', () => {
    const wrapper = mountCard({ slug: 'legacy', name: '旧徽章', tier: 'platinum', icon: '' })
    try {
      const text = wrapper.get('.sf-profile-badge-card__tier').text()
      expect(text).not.toContain('badges.tier')
      expect(text).toBe('铜牌')
      expect(wrapper.get('.sf-profile-badge-card').attributes('data-tier')).toBe('platinum')
    } finally {
      wrapper.unmount()
    }
  })

  test('unearned cards keep the neutral border and expose no progress without a threshold', () => {
    const wrapper = mountCard({ slug: 'manual', name: '手动徽章', tier: 'gold', icon: '', earned: false })
    try {
      expect(wrapper.get('.sf-profile-badge-card').attributes('data-earned')).toBe('false')
      expect(wrapper.find('.sf-profile-badge-card__progress').exists()).toBe(false)
      expect(wrapper.get('.sf-profile-badge-card__date').text()).toBe(t('badges.card.notEarned'))
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 整卡点击契约（图5 的入口）：
 *
 * - 覆盖整卡的是一个**真 button**（不是给 `<article>` 绑 click）：键盘可聚焦、可回车触发。
 * - 可访问名字挂在按钮上，卡片本身不再重复一遍（否则读屏会把同一句话读两次）。
 * - `interactive=false` 时没有那个按钮，名字回到 `<article>` 上 —— 不留一个点了没反应的假按钮。
 */
describe('SFBadgeCard 整卡点击', () => {
  const earnedBadge = {
    slug: 'helper',
    name: '优质解答',
    tier: 'silver',
    icon: '',
    grantedAt: '2026-11-03T08:00:00Z'
  }

  test('an interactive card exposes one overlay button with the accessible name and emits open', async () => {
    const wrapper = mountCard(earnedBadge)
    try {
      const article = wrapper.get('.sf-profile-badge-card')
      expect(article.attributes('data-interactive')).toBe('true')
      expect(article.attributes('aria-label')).toBeUndefined()

      const button = wrapper.get('.sf-profile-badge-card__open')
      expect(button.element.tagName).toBe('BUTTON')
      expect(button.attributes('type')).toBe('button')
      expect(button.attributes('aria-label')).toBe('银牌徽章：优质解答，获得于 2026-11-03')

      await button.trigger('click')
      expect(wrapper.emitted('open')).toHaveLength(1)
      expect((wrapper.emitted('open') as unknown[][])[0]?.[0]).toMatchObject({ slug: 'helper' })
    } finally {
      wrapper.unmount()
    }
  })

  test('the action slot sits above the overlay so wear does not also open the detail', () => {
    const wrapper = mount(SFBadgeCard, {
      props: { badge: { position: 0, earned: true, ...earnedBadge } },
      slots: { action: '<button type="button" class="probe-wear">佩戴</button>' },
      global: { components: { UIcon: UIconStub } }
    })
    try {
      // 动作位必须在覆盖按钮之上（z-index 更高），否则点佩戴会先命中整卡按钮。
      const action = wrapper.get('.sf-profile-badge-card__action')
      expect(action.exists()).toBe(true)
      expect(wrapper.get('.sf-profile-badge-card__open').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('a non-interactive card renders no dead button and keeps the name on the article', async () => {
    const wrapper = mountCard(earnedBadge, { interactive: false })
    try {
      const article = wrapper.get('.sf-profile-badge-card')
      expect(article.attributes('data-interactive')).toBe('false')
      expect(wrapper.find('.sf-profile-badge-card__open').exists()).toBe(false)
      expect(article.attributes('aria-label')).toBe('银牌徽章：优质解答，获得于 2026-11-03')

      await article.trigger('click')
      expect(wrapper.emitted('open')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})
