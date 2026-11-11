import { describe, expect, test } from 'bun:test'
import { computed, defineComponent } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { forumBadgeTier, forumUserBadgesPath } from '~/utils/forum/forumBadge'
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
const SFBadgeChip = await compileVueSfc(chipPath, 'sf-badge-chip', {
  computed,
  useI18n: () => ({ t }),
  useLocalePath: () => (path: string) => path,
  forumBadgeTier,
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

  test('unknown tier falls back to bronze and a missing icon falls back to a library icon', () => {
    const wrapper = mountChip({
      badge: { slug: 'legacy', name: '旧徽章', tier: 'platinum' as never, icon: '' }
    })
    try {
      const chip = wrapper.get('.sf-badge-chip')
      expect(chip.attributes('data-tier')).toBe('bronze')
      expect(chip.attributes('aria-label')).toBe('铜牌徽章：旧徽章')
      expect(wrapper.get('[data-testid="chip-icon"]').attributes('data-icon')).toBe('i-lucide-medal')
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
