import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  TRUST_LEVEL_MAX,
  TRUST_LEVEL_SLUGS,
  trustLevelIcon,
  trustLevelProgress,
  trustLevelSlug,
  type TrustLevelMetric
} from '~/composables/profile/useTrustLevel'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料页等级卡的冻结 DOM 契约（M8 按这些断言）：
 * 根 `data-level` / `data-earned`（TL0 显式 false）、五个刻度、进度条六件套
 * （`aria-valuemax` 是阈值不是 100、`aria-valuenow` clamp、`aria-valuetext` 人话、
 * `aria-label` 不含数字），TL4 页面 progressbar 计数为 0。
 *
 * 文案取自真实 zh-CN locale，避免测试自造期望值；键缺失会直接报错。
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

const UIconStub = defineComponent({
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i data-testid="card-icon" :data-icon="name" />'
})

const cardPath = fileURLToPath(new URL('../../app/components/profile/SFTrustLevelCard.vue', import.meta.url))
const SFTrustLevelCard = await compileVueSfc(cardPath, 'sf-trust-level-card', {
  computed,
  useI18n: () => ({ t }),
  TRUST_LEVEL_MAX,
  TRUST_LEVEL_SLUGS,
  trustLevelIcon,
  trustLevelProgress,
  trustLevelSlug
})

function mountCard(level: number, metrics: TrustLevelMetric[] = []) {
  return mount(SFTrustLevelCard, {
    props: { level, metrics },
    global: { components: { UIcon: UIconStub } }
  })
}

/** 常驻（TL2）→ 资深（TL3）的瓶颈：获赞 48/60 最小。6 个 metric 与 Go `Metrics` 同集合。 */
const residentMetrics: TrustLevelMetric[] = [
  { key: 'daysVisited', current: 45, threshold: 45 },
  { key: 'topicCount', current: 8, threshold: 8 },
  { key: 'commentCount', current: 80, threshold: 80 },
  { key: 'likeReceived', current: 48, threshold: 60 },
  { key: 'likeGiven', current: 20, threshold: 20 },
  { key: 'bookmarkCount', current: 5, threshold: 5 }
]

describe('SFTrustLevelCard', () => {
  test('renders the frozen level/earned attributes and five fixed rungs', () => {
    const wrapper = mountCard(2, residentMetrics)
    try {
      const card = wrapper.get('.sf-trust-level-card')
      expect(card.element.tagName).toBe('SECTION')
      expect(card.attributes('data-level')).toBe('2')
      expect(card.attributes('data-earned')).toBe('true')
      expect(card.attributes('aria-label')).toBe('信任等级 常驻用户，已获得')
      expect(wrapper.get('.sf-trust-level-card__code').text()).toBe('TL2')
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('常驻用户')

      const rungs = wrapper.findAll('.sf-trust-level-card__rung')
      expect(rungs.length).toBe(5)
      expect(rungs.map(rung => rung.attributes('data-level'))).toEqual(['0', '1', '2', '3', '4'])
      expect(rungs.map(rung => rung.attributes('data-earned'))).toEqual(['true', 'true', 'true', 'false', 'false'])
    } finally {
      wrapper.unmount()
    }
  })

  test('TL0 spells out data-earned="false" and still shows progress', () => {
    const wrapper = mountCard(0, [
      { key: 'commentCount', current: 2, threshold: 5 },
      { key: 'daysVisited', current: 1, threshold: 3 }
    ])
    try {
      const card = wrapper.get('.sf-trust-level-card')
      expect(card.attributes('data-level')).toBe('0')
      expect(card.attributes('data-earned')).toBe('false')
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('新用户')
      // 只有第 0 格已走过。
      expect(wrapper.findAll('.sf-trust-level-card__rung').map(rung => rung.attributes('data-earned')))
        .toEqual(['true', 'false', 'false', 'false', 'false'])
      // TL0 有下一档，进度条必须照常渲染。
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('progressbar carries the six-part contract with the threshold as valuemax', () => {
    const wrapper = mountCard(2, residentMetrics)
    try {
      const track = wrapper.get('[role="progressbar"]')
      expect(track.attributes('aria-valuemin')).toBe('0')
      // 阈值不是 100。
      expect(track.attributes('aria-valuemax')).toBe('60')
      expect(track.attributes('aria-valuenow')).toBe('48')
      expect(track.attributes('aria-valuetext')).toBe('获赞 48 / 60')
      expect(track.attributes('aria-label')).toBe('信任等级进度：常驻用户 → 资深用户')
      // 数字只在 valuetext 里，aria-label 不许出现数字。
      expect(track.attributes('aria-label')).not.toMatch(/\d/)
      expect(wrapper.get('.sf-trust-level-card__progress-text').text()).toBe('48 / 60')
      // 副文案与进度条使用同一个瓶颈 metric。
      expect(wrapper.get('.sf-trust-level-card__hint').text()).toBe('再获 12 个获赞可升到「资深用户」')
      expect(wrapper.get('.sf-trust-level-card__fill').attributes('style')).toContain('width: 80%')
    } finally {
      wrapper.unmount()
    }
  })

  test('clamps valuenow when the current value overshoots the threshold', () => {
    const wrapper = mountCard(1, [{ key: 'likeReceived', current: 90, threshold: 60 }])
    try {
      const track = wrapper.get('[role="progressbar"]')
      expect(track.attributes('aria-valuemax')).toBe('60')
      expect(track.attributes('aria-valuenow')).toBe('60')
      // valuetext 保留真实 current：读者要知道「已经 90 个赞」。
      expect(track.attributes('aria-valuetext')).toBe('获赞 90 / 60')
      expect(wrapper.get('.sf-trust-level-card__fill').attributes('style')).toContain('width: 100%')
    } finally {
      wrapper.unmount()
    }
  })

  test('picks the lowest ratio and ignores zero thresholds', () => {
    const wrapper = mountCard(2, [
      { key: 'topicCount', current: 0, threshold: 0 },
      { key: 'commentCount', current: 10, threshold: 20 },
      { key: 'daysVisited', current: 3, threshold: 15 }
    ])
    try {
      const track = wrapper.get('[role="progressbar"]')
      // 10/20 = 0.5 与 3/15 = 0.2 → 访问天数 是瓶颈。
      expect(track.attributes('aria-valuemax')).toBe('15')
      expect(track.attributes('aria-valuetext')).toBe('访问天数 3 / 15')
    } finally {
      wrapper.unmount()
    }
  })

  test('TL4 renders no progressbar at all and shows the maxed line', () => {
    const wrapper = mountCard(4, residentMetrics)
    try {
      expect(wrapper.findAll('[role="progressbar"]').length).toBe(0)
      expect(wrapper.get('.sf-trust-level-card').attributes('data-level')).toBe('4')
      expect(wrapper.get('.sf-trust-level-card__maxed').text()).toBe('已达最高等级')
      expect(wrapper.find('.sf-trust-level-card__hint').exists()).toBe(false)
      expect(wrapper.findAll('.sf-trust-level-card__rung').map(rung => rung.attributes('data-earned')))
        .toEqual(['true', 'true', 'true', 'true', 'true'])
    } finally {
      wrapper.unmount()
    }
  })

  test('all-zero thresholds render neither progress nor the maxed line', () => {
    // 实际只会是「新人期上限闸压住等级」：不是最高档，也不许猜原因写文案。
    const wrapper = mountCard(2, [
      { key: 'topicCount', current: 5, threshold: 0 },
      { key: 'commentCount', current: 5, threshold: 0 }
    ])
    try {
      expect(wrapper.findAll('[role="progressbar"]').length).toBe(0)
      expect(wrapper.find('.sf-trust-level-card__maxed').exists()).toBe(false)
      expect(wrapper.find('.sf-trust-level-card__hint').exists()).toBe(false)
      // 等级名与五格刻度照常。
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('常驻用户')
      expect(wrapper.findAll('.sf-trust-level-card__rung').length).toBe(5)
    } finally {
      wrapper.unmount()
    }
  })

  test('tier icons come from the frozen lucide ladder, never emoji', () => {
    const expected = ['i-lucide-sprout', 'i-lucide-leaf', 'i-lucide-house', 'i-lucide-mountain-snow', 'i-lucide-crown']
    for (const [level, icon] of expected.entries()) {
      const wrapper = mountCard(level, residentMetrics)
      try {
        expect(wrapper.get('[data-testid="card-icon"]').attributes('data-icon')).toBe(icon)
        expect(wrapper.get('.sf-trust-level-card').html()).not.toMatch(/[\u{1F300}-\u{1FAFF}]/u)
      } finally {
        wrapper.unmount()
      }
    }
  })
})
