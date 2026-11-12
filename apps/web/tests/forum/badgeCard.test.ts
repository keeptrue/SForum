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
 * 徽章卡片的等级渲染契约：名字与颜色优先取载荷（等级可增删），
 * 缺失时回退内置三档文案与色，且**永不**把 `badges.tier.<key>` 裸键画到页面上。
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

  test('built-in tiers without payload fields fall back to the locale copy and built-in colors', () => {
    for (const [tier, label] of [['gold', '金牌'], ['silver', '银牌'], ['bronze', '铜牌']] as const) {
      const wrapper = mountCard({ slug: tier, name: 'X', tier, icon: 'i-lucide-medal' })
      try {
        expect(wrapper.get('.sf-profile-badge-card__tier').text()).toBe(label)
        expect(wrapper.get('.sf-profile-badge-card').attributes('style')).toContain('--sf-tier-hue-light')
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
