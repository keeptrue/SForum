import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { forumBadgeTier, forumBadgeTierLabel, forumBadgeTierStyle, forumUserBadgesPath } from '~/utils/forum/forumBadge'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 冻结的 DOM 契约（M8 会按这些断言）：
 * 根 class `sf-badge-chip`、`data-tier`、`data-badge-slug`、`aria-label` 为
 * `{tierName}徽章：{name}`。文案取自真实 locale 文件，避免测试自造期望值。
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

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: String, default: '' } },
  template: '<a :href="to" data-testid="chip-link"><slot /></a>'
})

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i data-testid="chip-icon" :data-icon="name" />'
})

const chipPath = fileURLToPath(new URL('../../app/components/forum/SFBadgeChip.vue', import.meta.url))
const has = (key: string) => typeof lookup(key) === 'string'

const SFBadgeChip = await compileVueSfc(chipPath, 'sf-badge-chip', {
  computed,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  useLocalePath: () => (path: string) => path,
  forumBadgeTier,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  forumUserBadgesPath
})

function mountChip(props: Record<string, unknown>) {
  return mount(SFBadgeChip, {
    props,
    global: { components: { NuxtLink: NuxtLinkStub, UIcon: UIconStub } }
  })
}

describe('SFBadgeChip', () => {
  test('link variant carries the frozen aria-label, tier and slug data attributes', () => {
    const wrapper = mountChip({
      badge: { slug: 'voice-of-community', name: '众望所归', tier: 'gold', icon: 'i-lucide-crown' },
      username: 'alice'
    })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.attributes('data-tier')).toBe('gold')
      expect(chip.attributes('data-badge-slug')).toBe('voice-of-community')
      expect(chip.attributes('aria-label')).toBe('金牌徽章：众望所归')
      expect(wrapper.get('[data-testid="chip-link"]').attributes('href')).toBe('/u/alice?tab=badges')
      expect(wrapper.get('[data-testid="chip-icon"]').attributes('data-icon')).toBe('i-lucide-crown')
      expect(wrapper.get('.sf-badge-chip__name').text()).toBe('众望所归')
    } finally {
      wrapper.unmount()
    }
  })

  test('without a username it degrades to a non-interactive span with the same label', () => {
    const wrapper = mountChip({
      badge: { slug: 'regular', name: '常客', tier: 'silver', icon: 'i-lucide-calendar-check' }
    })
    try {
      expect(wrapper.find('[data-testid="chip-link"]').exists()).toBe(false)
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.element.tagName).toBe('SPAN')
      expect(chip.attributes('aria-label')).toBe('银牌徽章：常客')
      expect(chip.attributes('role')).toBe('img')
    } finally {
      wrapper.unmount()
    }
  })

  test('an unknown tier keeps its raw key but never renders a bare i18n key', () => {
    // 等级是运营可增删的数据：未知 key 不能被折叠成 bronze（那会把新等级全画成铜牌），
    // 也不能把 `badges.tier.platinum` 直接当文案渲染出去。
    const wrapper = mountChip({
      badge: { slug: 'legacy', name: '旧徽章', tier: 'platinum', icon: '' }
    })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.attributes('data-tier')).toBe('platinum')
      expect(chip.attributes('aria-label')).toBe('铜牌徽章：旧徽章')
      expect(chip.attributes('aria-label')).not.toContain('badges.tier')
      expect(wrapper.get('[data-testid="chip-icon"]').attributes('data-icon')).toBe('i-lucide-medal')
    } finally {
      wrapper.unmount()
    }
  })

  test('a server-provided tier name and color win over the built-in fallbacks', () => {
    const wrapper = mountChip({
      badge: {
        slug: 'legendary',
        name: '传世之作',
        tier: 'legendary',
        icon: 'i-lucide-flame',
        tierName: { zh: '传说', en: 'Legendary' },
        tierColor: 'violet',
        tierPosition: 0
      }
    })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.attributes('aria-label')).toBe('传说徽章：传世之作')
      // hue 成对下发：token 走 --sf-tl-* 变量，明暗取值由 CSS 自己切。
      expect(chip.attributes('style')).toContain('--sf-tier-hue-light: var(--sf-tl-violet)')
      expect(chip.attributes('style')).toContain('--sf-tier-hue-dark: var(--sf-tl-violet)')
    } finally {
      wrapper.unmount()
    }
  })

  test('a custom hex tier color is brightened for dark mode instead of reusing one literal', () => {
    const wrapper = mountChip({
      badge: {
        slug: 'custom',
        name: '自定义',
        tier: 'custom',
        icon: 'i-lucide-gem',
        tierName: { zh: '自定义', en: 'Custom' },
        tierColor: '#7c3aed'
      }
    })
    try {
      const style = wrapper.get('.sf-badge-chip').attributes('style') ?? ''
      expect(style).toContain('--sf-tier-hue-light: #7c3aed')
      expect(style).toContain('--sf-tier-hue-dark: color-mix(in srgb, #7c3aed 74%, white)')
    } finally {
      wrapper.unmount()
    }
  })

  test('tier label always comes from the locale, never from the raw enum value', () => {
    for (const [tier, expected] of [['gold', '金牌'], ['silver', '银牌'], ['bronze', '铜牌']] as const) {
      const wrapper = mountChip({ badge: { slug: tier, name: 'X', tier, icon: 'i-lucide-medal' } })
      try {
        expect(wrapper.get('.sf-badge-chip').attributes('aria-label')).toBe(`${expected}徽章：X`)
      } finally {
        wrapper.unmount()
      }
    }
  })
})
