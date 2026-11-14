import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent, h, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  FALLBACK_LADDER,
  TL0_TIER,
  localizedTierName,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelNextLevel,
  trustLevelProgress,
  trustLevelRequirementRows,
  type TrustLevelCondition,
  type TrustLevelMetric,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 信任等级详情弹窗的渲染契约（规范 §6–§8、§10）：
 *
 * - 头部：档位图标 + 档位名（h2）+ `TLn` + 已获得/尚未获得 + 右上 X；
 * - 门槛：下一档 `> 0` 的指标清单，标题对他人页也写「升到「{next}」的门槛」，不写「还差」；
 * - 进度：**仅本人**、有瓶颈、未满级；六件套口径与等级卡完全一致；
 * - 规则条件：**仅本人**且非空；`data-satisfied` 供断言；
 * - 满级：用「已达最高等级」取代门槛与进度；全 0 门槛整块不渲染且不解释。
 *
 * 文案断言取真实 zh-CN locale；UModal 桩只把 `open` 与 `#content` 透出来。
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
  template: '<i :data-icon="name" />'
})

/** UModal 桩：只把 `open` / `title` 与 `#content` 透出来，`update:open` 原样上抛。 */
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

const modalPath = fileURLToPath(new URL('../../app/components/profile/SFTrustLevelDetailModal.vue', import.meta.url))
const SFTrustLevelDetailModal = await compileVueSfc(modalPath, 'sf-trust-level-detail-modal', {
  computed,
  useI18n: () => ({ t, locale: ref('zh-CN') }),
  FALLBACK_LADDER,
  localizedTierName,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelNextLevel,
  trustLevelProgress,
  trustLevelRequirementRows,
  tierAccentStyle,
  tierColorOf
})

/** 常驻（TL2）→ 资深（TL3）的瓶颈：获赞 48/60 最小。6 个 metric 与 Go `Metrics` 同集合。 */
const residentMetrics: TrustLevelMetric[] = [
  { key: 'daysVisited', current: 45, threshold: 45 },
  { key: 'topicCount', current: 8, threshold: 8 },
  { key: 'commentCount', current: 80, threshold: 80 },
  { key: 'likeReceived', current: 48, threshold: 60 },
  { key: 'likeGiven', current: 20, threshold: 20 },
  { key: 'bookmarkCount', current: 5, threshold: 5 }
]

const viewerConditions: TrustLevelCondition[] = [
  { satisfied: true, text: { zh: '已发布 3 个主题', en: 'Published 3 topics' } },
  { satisfied: false, text: { zh: '获得 10 个赞', en: 'Received 10 likes' } }
]

/** 运营自定义阶梯：TL0 + 两档，用于验证「下一档」不依赖内置五档。 */
const customLadder: TrustLevelTier[] = [
  TL0_TIER,
  { id: 'tier_6', name: { zh: '见习', en: 'Trainee' }, icon: 'compass', requirements: {}, retired: false },
  {
    id: 'tier_7',
    name: { zh: '掌舵人', en: 'Helm' },
    icon: 'anchor',
    // 全 0：这一档不按数值判定 → 门槛区块整块不渲染（规范 §8）。
    requirements: { days_visited: 0, topic_count: 0 },
    retired: false
  }
]

function mountModal(props: Record<string, unknown> = {}) {
  return mount(SFTrustLevelDetailModal, {
    props: { open: true, level: 2, ladder: FALLBACK_LADDER, ...props },
    global: { components: { UIcon: UIconStub, UModal: UModalStub } }
  })
}

describe('SFTrustLevelDetailModal · 本人视角', () => {
  test('renders the header, the next-tier thresholds, the progress and the rule conditions', () => {
    const wrapper = mountModal({ self: true, metrics: residentMetrics, conditions: viewerConditions })
    try {
      // 可访问名字指向可见标题；UModal 自己那份标题是「信任等级详情」。
      const panel = wrapper.get('.sf-trust-level-detail')
      expect(panel.attributes('aria-labelledby')).toBe('sf-trust-level-detail-title')
      expect(wrapper.get('.umodal-stub').attributes('data-title')).toBe('信任等级详情')
      expect(wrapper.get('#sf-trust-level-detail-title').text()).toBe('常驻用户')
      expect(wrapper.get('#sf-trust-level-detail-title').element.tagName).toBe('H2')
      expect(wrapper.get('.sf-trust-level-detail__code').text()).toBe('TL2')
      expect(wrapper.get('.sf-trust-level-detail__state').text()).toBe('已获得')
      expect(wrapper.get('.sf-trust-level-detail__mark').classes()).toContain('sf-tl-mark')
      expect(wrapper.get('.sf-trust-level-detail__mark').attributes('data-step')).toBe('2')
      expect(wrapper.get('.sf-trust-level-detail__glyph').attributes('data-icon')).toBe('i-lucide-home')
      // 档位色落在面板根上：进度条与条件图标都靠它。
      expect(panel.attributes('data-sf-tl-accent')).toBeDefined()
      expect(panel.attributes('style')).toContain('--tl-accent: var(--sf-tl-emerald)')

      // 门槛：下一档（TL3 资深用户）6 个指标全 > 0，顺序 = TRUST_LEVEL_METRIC_KEYS。
      const titles = wrapper.findAll('.sf-trust-level-detail__block-title').map(node => node.text())
      expect(titles).toEqual(['升到「资深用户」的门槛', '我的进度', '规则条件'])
      const rows = wrapper.findAll('.sf-trust-level-detail__row')
      expect(rows.map(row => row.attributes('data-metric')))
        .toEqual(['daysVisited', 'topicCount', 'commentCount', 'likeReceived', 'likeGiven', 'bookmarkCount'])
      expect(rows.map(row => row.get('.sf-trust-level-detail__row-label').text()))
        .toEqual(['访问天数', '主题', '回复', '获赞', '点赞', '收藏'])
      expect(rows.map(row => row.get('.sf-trust-level-detail__row-value').text()))
        .toEqual(['≥ 45', '≥ 8', '≥ 80', '≥ 30', '≥ 20', '≥ 5'])
      // 他人页也不写「还差」：门槛描述的是档位规则，不是这个人。
      expect(wrapper.text()).not.toContain('还差')

      // 进度六件套：阈值是 valuemax（不是 100），now 是 clamp 后的值，数字只在 valuetext 里。
      const track = wrapper.get('[role="progressbar"]')
      expect(track.attributes('aria-valuemin')).toBe('0')
      expect(track.attributes('aria-valuemax')).toBe('60')
      expect(track.attributes('aria-valuenow')).toBe('48')
      expect(track.attributes('aria-valuetext')).toBe('获赞 48 / 60')
      expect(track.attributes('aria-label')).toBe('信任等级进度：常驻用户 → 资深用户')
      expect(track.attributes('aria-label')).not.toMatch(/\d/)
      expect(wrapper.get('.sf-trust-level-detail__progress-text').text()).toBe('48 / 60')
      expect(wrapper.get('.sf-trust-level-detail__fill').attributes('style')).toContain('width: 80%')
      expect(wrapper.get('.sf-trust-level-detail__hint').text()).toBe('再获 12 个获赞可升到「资深用户」')

      // 规则条件：图标 + 服务端句子，`data-satisfied` 供断言。
      const conditions = wrapper.findAll('.sf-trust-level-detail__condition')
      expect(conditions.map(node => node.attributes('data-satisfied'))).toEqual(['true', 'false'])
      expect(conditions[0]!.text()).toContain('已发布 3 个主题')
      expect(conditions[1]!.text()).toContain('获得 10 个赞')
      expect(conditions[0]!.text()).toContain('已满足')
      expect(conditions[1]!.text()).toContain('未满足')
      expect(conditions.map(node => node.get('[data-icon]').attributes('data-icon')))
        .toEqual(['i-lucide-check', 'i-lucide-x'])
    } finally {
      wrapper.unmount()
    }
  })

  test('the viewer nextLevel wins over the ladder neighbour', () => {
    // level 1 的邻位是 2，但后端说下一档是 3（配置里删过档时就会出现这种跳跃）。
    const wrapper = mountModal({ self: true, level: 1, nextLevel: 3 })
    try {
      expect(wrapper.findAll('.sf-trust-level-detail__block-title')[0]!.text()).toBe('升到「资深用户」的门槛')
      expect(wrapper.findAll('.sf-trust-level-detail__row')).toHaveLength(6)
    } finally {
      wrapper.unmount()
    }
  })

  test('a missing metrics list renders no progress block and no "maxed" line', () => {
    // 本人但读面没回来：不画进度，也不假装满级（规范 §8 最后一行）。
    const wrapper = mountModal({ self: true })
    try {
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
      expect(wrapper.find('.sf-trust-level-detail__maxed').exists()).toBe(false)
      // 门槛照常：那是档位规则，不依赖 viewer。
      expect(wrapper.findAll('.sf-trust-level-detail__block-title').map(node => node.text()))
        .toEqual(['升到「资深用户」的门槛'])
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTrustLevelDetailModal · 他人视角', () => {
  test('shows the current level and the next-tier thresholds only', () => {
    // 故意把 metrics/conditions 传进来：他人视角即便误传也不许出现进度与条件（规范 §7）。
    const wrapper = mountModal({ self: false, metrics: residentMetrics, conditions: viewerConditions })
    try {
      expect(wrapper.get('.sf-trust-level-detail__name').text()).toBe('常驻用户')
      expect(wrapper.findAll('.sf-trust-level-detail__row')).toHaveLength(6)
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
      expect(wrapper.find('.sf-trust-level-detail__hint').exists()).toBe(false)
      expect(wrapper.findAll('.sf-trust-level-detail__condition')).toHaveLength(0)
      expect(wrapper.text()).not.toContain('我的进度')
      expect(wrapper.text()).not.toContain('规则条件')
      // 门槛标题是「升到…」，不是「还差…」。
      expect(wrapper.get('.sf-trust-level-detail__block-title').text()).toBe('升到「资深用户」的门槛')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTrustLevelDetailModal · 满级与降级', () => {
  test('the top rung replaces the thresholds and the progress with the maxed line', () => {
    // 满级时后端既不下发 nextLevel 也不下发 conditions：只留「已达最高等级」。
    const wrapper = mountModal({ self: true, level: 4, metrics: residentMetrics })
    try {
      expect(wrapper.get('.sf-trust-level-detail__state').text()).toBe('已获得')
      expect(wrapper.get('.sf-trust-level-detail__maxed').text()).toBe('已达最高等级')
      expect(wrapper.findAll('.sf-trust-level-detail__row')).toHaveLength(0)
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
      expect(wrapper.findAll('.sf-trust-level-detail__block-title')).toHaveLength(0)
      expect(wrapper.findAll('.sf-trust-level-detail__condition')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('an all-zero requirements tier renders no threshold block and no explanation', () => {
    // 规范 §8：整块不渲染，不写「没有门槛」——这一档不按数值判定，不是「谁都能升」。
    const wrapper = mountModal({ self: true, level: 1, ladder: customLadder })
    try {
      expect(wrapper.findAll('.sf-trust-level-detail__row')).toHaveLength(0)
      expect(wrapper.findAll('.sf-trust-level-detail__block-title')).toHaveLength(0)
      expect(wrapper.text()).not.toContain('门槛')
      expect(wrapper.text()).not.toContain('没有')
      // 头部照常：名字/图标来自目录。
      expect(wrapper.get('.sf-trust-level-detail__name').text()).toBe('见习')
      expect(wrapper.get('.sf-trust-level-detail__glyph').attributes('data-icon')).toBe('i-lucide-compass')
    } finally {
      wrapper.unmount()
    }
  })

  test('TL0 spells out "not earned" and keeps the dashed mark', () => {
    const wrapper = mountModal({ level: 0 })
    try {
      expect(wrapper.get('.sf-trust-level-detail__state').text()).toBe('尚未获得')
      expect(wrapper.get('.sf-trust-level-detail__code').text()).toBe('TL0')
      expect(wrapper.get('.sf-trust-level-detail').attributes('data-level')).toBe('0')
    } finally {
      wrapper.unmount()
    }
  })

  test('an out-of-range level clamps to the ladder top', () => {
    const wrapper = mountModal({ level: 99, ladder: customLadder })
    try {
      expect(wrapper.get('.sf-trust-level-detail__code').text()).toBe('TL2')
      expect(wrapper.get('.sf-trust-level-detail__name').text()).toBe('掌舵人')
      expect(wrapper.get('.sf-trust-level-detail__maxed').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTrustLevelDetailModal · 开关', () => {
  test('a closed modal renders nothing at all', () => {
    const wrapper = mountModal({ open: false })
    try {
      expect(wrapper.find('.sf-trust-level-detail').exists()).toBe(false)
      expect(wrapper.find('.umodal-stub').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('both close controls emit update:open=false', async () => {
    const wrapper = mountModal({ self: true })
    try {
      const close = wrapper.get('.sf-trust-level-detail__close')
      expect(close.attributes('aria-label')).toBe('关闭')
      expect(close.attributes('title')).toBe('关闭')
      await close.trigger('click')

      await wrapper.get('.sf-trust-level-detail__done').trigger('click')
      expect(wrapper.emitted('update:open')).toEqual([[false], [false]])
    } finally {
      wrapper.unmount()
    }
  })

  test('the modal carries the shared content class so the shell keeps its padding', () => {
    const wrapper = mountModal({ self: true })
    try {
      expect(wrapper.getComponent(UModalStub).props('ui')).toEqual({ content: 'sf-trust-level-detail-modal' })
    } finally {
      wrapper.unmount()
    }
  })
})
