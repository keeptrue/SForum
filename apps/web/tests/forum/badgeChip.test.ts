import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { forumBadgeTier, forumBadgeTierLabel, forumBadgeTierStyle } from '~/utils/forum/forumBadge'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 冻结的 DOM 契约（M8 会按这些断言）：
 * 根 class `sf-badge-chip`、`data-tier`、`data-badge-slug`、`aria-label` 为
 * `{tierName}徽章：{name}`；可点形态（有 username）是 `<button aria-haspopup="dialog">`，
 * 点击只打开徽章说明弹窗，不再导航到资料页徽章 tab。文案取自真实 locale 文件。
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
  template: '<i data-testid="chip-icon" :data-icon="name" />'
})

/** 弹窗宿主是全局单例；chip 只负责把「点了谁」交给它。 */
const openedDialogs: Array<Record<string, unknown>> = []
const dialogStub = () => ({
  openBadgeDialog: (input: Record<string, unknown>) => {
    openedDialogs.push(input)
  }
})

const chipPath = fileURLToPath(new URL('../../app/components/forum/SFBadgeChip.vue', import.meta.url))
const has = (key: string) => typeof lookup(key) === 'string'

const SFBadgeChip = await compileVueSfc(chipPath, 'sf-badge-chip', {
  computed,
  useI18n: () => ({ t, te: has, locale: ref('zh-CN') }),
  useForumBadgeDialog: dialogStub,
  forumBadgeTier,
  forumBadgeTierLabel,
  forumBadgeTierStyle
})

function mountChip(props: Record<string, unknown>) {
  openedDialogs.length = 0
  return mount(SFBadgeChip, {
    props,
    global: { components: { UIcon: UIconStub } }
  })
}

describe('SFBadgeChip', () => {
  test('with a username it is a dialog button: same label plus the action, no navigation', async () => {
    const badge = { slug: 'voice-of-community', name: '众望所归', tier: 'gold', icon: 'i-lucide-crown' }
    const wrapper = mountChip({ badge, username: 'alice' })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.element.tagName).toBe('BUTTON')
      expect(chip.attributes('type')).toBe('button')
      expect(chip.attributes('aria-haspopup')).toBe('dialog')
      expect(chip.attributes('data-tier')).toBe('gold')
      expect(chip.attributes('data-badge-slug')).toBe('voice-of-community')
      expect(chip.attributes('aria-label')).toBe('金牌徽章：众望所归，查看徽章说明')
      expect(chip.attributes('title')).toBe('金牌徽章：众望所归，查看徽章说明')
      // 不再有链接：点击是打开说明弹窗，不是导航。
      expect(wrapper.find('a').exists()).toBe(false)
      expect(wrapper.get('[data-testid="chip-icon"]').attributes('data-icon')).toBe('i-lucide-crown')
      expect(wrapper.get('.sf-badge-chip__name').text()).toBe('众望所归')

      await chip.trigger('click')
      expect(openedDialogs).toEqual([{ username: 'alice', badge }])
    } finally {
      wrapper.unmount()
    }
  })

  test('without a username it degrades to a non-interactive span with the frozen label', () => {
    const wrapper = mountChip({
      badge: { slug: 'regular', name: '常客', tier: 'silver', icon: 'i-lucide-calendar-check' }
    })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.element.tagName).toBe('SPAN')
      expect(chip.attributes('role')).toBe('img')
      expect(chip.attributes('aria-label')).toBe('银牌徽章：常客')
      expect(wrapper.find('button').exists()).toBe(false)
      expect(wrapper.find('a').exists()).toBe(false)
      expect(openedDialogs).toEqual([])
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
