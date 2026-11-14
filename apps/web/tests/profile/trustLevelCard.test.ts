import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  FALLBACK_LADDER,
  TL0_TIER,
  parseTrustLevelCatalog,
  trustLevelIconOf,
  trustLevelLadder,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelProgress,
  type TrustLevelMetric,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { defaultTierColorName, tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料页等级卡的冻结 DOM 契约（M8 按这些断言），N 档版：
 * 根 `data-level` / `data-earned`（TL0 显式 false）、**每档一格刻度**（TL0 + active 档数）、
 * 进度条六件套（`aria-valuemax` 是阈值不是 100、`aria-valuenow` clamp、`aria-valuetext` 人话、
 * `aria-label` 不含数字）。
 *
 * 最高档**按阶梯长度算**（`trustLevelLadderMax`），不是写死的 TL4：
 * 运营把档位加到 7 / 8 档后，「已达最高等级」必须跟着新最高档走，刻度也必须多出格子。
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
  useI18n: () => ({ t, locale: ref('zh-CN') }),
  // 卡片不再自己拼阶梯：这些绑定就是它脚本里**允许**依赖的全部。
  FALLBACK_LADDER,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelProgress,
  tierAccentStyle,
  tierColorOf
})

/**
 * `ladder` 现在是**最终**阶梯（rank 0 = TL0），由 `useTrustLevel` 组装。
 * 默认真实接缝里页面给的就是这一份，所以测试默认也传它 —— 传原始 `tiers[]` 会漏掉整条缝。
 */
function mountCard(
  level: number,
  metrics: TrustLevelMetric[] = [],
  ladder: TrustLevelTier[] = FALLBACK_LADDER
) {
  return mount(SFTrustLevelCard, {
    props: { level, metrics, ladder },
    global: { components: { UIcon: UIconStub } }
  })
}

function mountCardWithoutLadder(level: number, metrics: TrustLevelMetric[] = []) {
  return mount(SFTrustLevelCard, { props: { level, metrics }, global: { components: { UIcon: UIconStub } } })
}

/**
 * 运营自定义的**最终**阶梯：TL0 + N 个 active 档（与 `useTrustLevel` 交给卡片的形状一致）。
 * 用 `tier-<n>` 这类非内置 id，正是为了让「名字按 rank 回退」这条也顺带被走到。
 */
function ladderWith(activeCount: number): TrustLevelTier[] {
  return [
    TL0_TIER,
    ...Array.from({ length: activeCount }, (_, index) => ({
      id: `tier-${index + 1}`,
      name: { zh: `第 ${index + 1} 档`, en: `Tier ${index + 1}` },
      icon: null,
      requirements: {},
      limits: null,
      earnable: [],
      retired: false
    }))
  ]
}

/** 内置五档阶梯（兜底目录）的最高 rank：测试里算出来，不写字面量 4。 */
const TOP_OF_FALLBACK = trustLevelLadderMax(FALLBACK_LADDER)

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
  test('renders the frozen level/earned attributes and one rung per ladder entry', () => {
    const wrapper = mountCard(2, residentMetrics)
    try {
      const card = wrapper.get('.sf-trust-level-card')
      expect(card.element.tagName).toBe('SECTION')
      expect(card.attributes('data-level')).toBe('2')
      expect(card.attributes('data-earned')).toBe('true')
      expect(card.attributes('aria-label')).toBe('信任等级 常驻用户，已获得')
      expect(wrapper.get('.sf-trust-level-card__code').text()).toBe('TL2')
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('常驻用户')

      // 兜底阶梯 = TL0 + 4 档：刻度数 = 阶梯长度，不是另一套常量。
      const rungs = wrapper.findAll('.sf-trust-level-card__rung')
      expect(rungs.length).toBe(TOP_OF_FALLBACK + 1)
      expect(rungs.map(rung => rung.attributes('data-level')))
        .toEqual(Array.from({ length: TOP_OF_FALLBACK + 1 }, (_, level) => String(level)))
      expect(rungs.map(rung => rung.attributes('data-earned')))
        .toEqual(['true', 'true', 'true', 'false', 'false'])
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
        .toEqual(['true', ...Array.from({ length: TOP_OF_FALLBACK }, () => 'false')])
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

  test('the highest rung renders no progressbar at all and shows the maxed line', () => {
    // 最高档 = 阶梯长度 - 1：这里用兜底目录算出来，而不是写死 TL4。
    const top = TOP_OF_FALLBACK
    const wrapper = mountCard(top, residentMetrics)
    try {
      expect(wrapper.findAll('[role="progressbar"]').length).toBe(0)
      expect(wrapper.get('.sf-trust-level-card').attributes('data-level')).toBe(String(top))
      expect(wrapper.get('.sf-trust-level-card__code').text()).toBe(`TL${top}`)
      expect(wrapper.get('.sf-trust-level-card__maxed').text()).toBe('已达最高等级')
      expect(wrapper.find('.sf-trust-level-card__hint').exists()).toBe(false)
      expect(wrapper.findAll('.sf-trust-level-card__rung').map(rung => rung.attributes('data-earned')))
        .toEqual(Array.from({ length: top + 1 }, () => 'true'))
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
      // 等级名与刻度照常。
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('常驻用户')
      expect(wrapper.findAll('.sf-trust-level-card__rung').length).toBe(TOP_OF_FALLBACK + 1)
    } finally {
      wrapper.unmount()
    }
  })

  test('tier icons come from the frozen lucide ladder, never emoji', () => {
    // 与后端 `tier_icons.go` 的前五项一致（芽 → 叶 → 屋 → 山 → 冠）。
    const expected = ['i-lucide-sprout', 'i-lucide-leaf', 'i-lucide-home', 'i-lucide-mountain', 'i-lucide-crown']
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

  test('clamps a level beyond the ladder instead of overflowing the rungs', () => {
    const wrapper = mountCard(99, residentMetrics, ladderWith(3))
    try {
      expect(wrapper.get('.sf-trust-level-card').attributes('data-level')).toBe('3')
      expect(wrapper.get('.sf-trust-level-card__maxed').exists()).toBe(true)
      expect(wrapper.findAll('.sf-trust-level-card__rung')).toHaveLength(4)
    } finally {
      wrapper.unmount()
    }
  })

  test('renders one rung per ladder entry, including 7 and 8 active tiers', () => {
    for (const activeCount of [4, 7, 8]) {
      const ladder = ladderWith(activeCount)
      const wrapper = mountCard(activeCount, residentMetrics, ladder)
      try {
        const rungs = wrapper.findAll('.sf-trust-level-card__rung')
        // TL0 + 每档一格：档数变了刻度必须跟着变，不能停在 5 格。
        expect(rungs).toHaveLength(activeCount + 1)
        expect(rungs.map(rung => rung.attributes('data-level')))
          .toEqual(Array.from({ length: activeCount + 1 }, (_, level) => String(level)))
        expect(rungs.map(rung => rung.attributes('data-earned')))
          .toEqual(Array.from({ length: activeCount + 1 }, (_, level) => String(level <= activeCount)))
        // 最高档按 active 档数算：7 / 8 档时 TL4 只是中间格。
        expect(wrapper.get('.sf-trust-level-card').attributes('data-level')).toBe(String(activeCount))
        expect(wrapper.get('.sf-trust-level-card__code').text()).toBe(`TL${activeCount}`)
        expect(wrapper.get('.sf-trust-level-card__name').text()).toBe(`第 ${activeCount} 档`)
        expect(wrapper.findAll('[role="progressbar"]')).toHaveLength(0)
        expect(wrapper.get('.sf-trust-level-card__maxed').text()).toBe(t('profile.trustLevel.maxed'))
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('renders exactly the ladder it receives: no re-composition, no duplicate TL0', () => {
    // 真实接缝：公开读面下发的 levels[] 里已经有一条合成 TL0，`useTrustLevel` 组装成
    // 「TL0 + active 档」交给卡片。卡片曾经再拼一次 TL0，导致整卡错位一档（P1 回归）。
    const catalog = parseTrustLevelCatalog({
      levels: [
        { level: 0, levelSlug: 'newcomer', displayName: { zh: '新用户', en: 'Newcomer' }, icon: 'sprout', editable: false },
        { level: 1, levelSlug: 'member', displayName: { zh: '正式用户', en: 'Member' }, icon: 'leaf', editable: true },
        { level: 2, levelSlug: 'resident', displayName: { zh: '常驻用户', en: 'Resident' }, icon: 'home', editable: true }
      ]
    })
    const ladder = trustLevelLadder(catalog)
    expect(ladder.filter(tier => tier.id === TL0_TIER.id)).toHaveLength(1)

    const wrapper = mountCard(1, residentMetrics, ladder)
    try {
      const rungs = wrapper.findAll('.sf-trust-level-card__rung')
      expect(rungs).toHaveLength(ladder.length)
      // data-level 与档位名一一对应：TL1 必须是「正式用户」而不是 TL0 的名字。
      expect(wrapper.get('.sf-trust-level-card').attributes('data-level')).toBe('1')
      expect(wrapper.get('.sf-trust-level-card__code').text()).toBe('TL1')
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('正式用户')
      expect(wrapper.get('[data-testid="card-icon"]').attributes('data-icon')).toBe('i-lucide-leaf')
      // 最高档随阶梯长度走（这里 2），不是内置五档的 4。
      expect(mountCard(2, residentMetrics, ladder).get('.sf-trust-level-card__maxed').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('without a ladder it renders no rungs at all, but keeps name, icon and progress', () => {
    const wrapper = mountCardWithoutLadder(2, residentMetrics)
    try {
      expect(wrapper.findAll('.sf-trust-level-card__rung')).toHaveLength(0)
      // 名字/图标仍走内置回退（展示兜底，不改写任何身份）。
      expect(wrapper.get('.sf-trust-level-card__name').text()).toBe('常驻用户')
      expect(wrapper.get('[data-testid="card-icon"]').attributes('data-icon')).toBe('i-lucide-home')
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('the card source never composes a ladder itself (seam guard)', () => {
    const source = readFileSync(cardPath, 'utf8')
    // 阶梯的组成只有一个所有者（useTrustLevel）。卡片再调一次就会把 TL0 拼成两条。
    expect(source).not.toContain('trustLevelLadder(')
    expect(source).toContain('ladder: () => []')
  })

  test('offers progress toward the next rung below a 7 or 8 tier top', () => {
    for (const activeCount of [7, 8]) {
      const ladder = ladderWith(activeCount)
      const level = activeCount - 1
      const wrapper = mountCard(level, residentMetrics, ladder)
      try {
        const track = wrapper.get('[role="progressbar"]')
        expect(track.attributes('aria-label')).toBe(t('profile.trustLevel.progress.ariaLabel', {
          current: `第 ${level} 档`,
          next: `第 ${activeCount} 档`
        }))
        expect(track.attributes('aria-valuemax')).toBe('60')
        // 下一档名称来自阶梯，不是「资深用户」这类内置文案。
        expect(wrapper.get('.sf-trust-level-card__hint').text()).toBe(t('profile.trustLevel.nextHint', {
          count: 12,
          metric: t('profile.trustLevel.metric.likeReceived'),
          next: `第 ${activeCount} 档`
        }))
        expect(wrapper.find('.sf-trust-level-card__maxed').exists()).toBe(false)
        expect(wrapper.findAll('.sf-trust-level-card__rung')).toHaveLength(activeCount + 1)
      } finally {
        wrapper.unmount()
      }
    }
  })
})

describe('tier color reaches the card accent (D-C1)', () => {
  test('the card root carries the current tier color as --tl-accent', () => {
    // TL0 固定 slate：未入门的位置不跟某一档抢注意力。
    expect(mountCard(0).attributes('style')).toContain(`--tl-accent: var(--sf-tl-${defaultTierColorName(0)})`)

    const ladder = trustLevelLadder([
      { id: 'member', name: { zh: '正式用户', en: 'Member' }, icon: 'leaf', color: 'rose', retired: false },
      { id: 'resident', name: { zh: '常驻', en: 'Resident' }, icon: 'home', color: null, retired: false }
    ])
    // 存储色优先。
    expect(mountCard(1, [], ladder).attributes('style')).toContain('--tl-accent: var(--sf-tl-rose)')
    // 未配色 → 按位次默认（rank 2 = emerald），不是站点 accent。
    expect(mountCard(2, [], ladder).attributes('style')).toContain(`--tl-accent: var(--sf-tl-${defaultTierColorName(2)})`)
    expect(tierAccentStyle(tierColorOf(ladder[2], 2))).toEqual({ '--tl-accent': `var(--sf-tl-${defaultTierColorName(2)})` })
  })

  test('the icon block owns the shape axis and keeps the site accent out of the card', () => {
    const source = readFileSync(cardPath, 'utf8')
    // 形状轴（data-step）已从卡根移到图标块：与后台四分区共用 .sf-tl-mark 的阶梯几何。
    const card = mountCard(2)
    try {
      expect(card.find('.sf-trust-level-card').attributes('data-step')).toBeUndefined()
      const icon = card.get('.sf-trust-level-card__icon')
      expect(icon.classes()).toContain('sf-tl-mark')
      expect(icon.attributes('data-step')).toBe('2')
      // 自定义 hex 档的色相全靠这条属性 + 全局 `[data-sf-tl-accent]` 规则：漏了属性就没有颜色。
      expect(card.get('.sf-trust-level-card').attributes('data-sf-tl-accent')).toBeDefined()
    } finally {
      card.unmount()
    }
    // 站点色软底不许再出现在档位图形里（TL2/3 曾经用它，结果一页全是同一个色）。
    expect(source).not.toContain('--sf-accent-soft')
    expect(source).not.toContain('var(--sf-accent)')
    // 卡面**不再**声明 `--tl-accent`：`.sf-profile-side-section.sf-trust-level-card` 的特异度是 0,2,0，
    // 会压过全局 `[data-sf-tl-accent]`（0,1,0）—— 自定义 hex 档会被静默打回 slate，
    // 而普通 token 档照常工作，于是这个 bug 只在配了自定义色的站点上出现。
    expect(source).not.toMatch(/--tl-accent\s*:/)
  })

  test('level 1 keeps the tier color on the card root and the shape on the icon', () => {
    const wrapper = mountCard(1)
    try {
      // rank 1 同样吃自己的档位色（默认 teal），不给「第一档不着色」留后门。
      expect(wrapper.get('.sf-trust-level-card').attributes('style'))
        .toContain(`--tl-accent: var(--sf-tl-${defaultTierColorName(1)})`)
      const icon = wrapper.get('.sf-trust-level-card__icon')
      expect(icon.classes()).toContain('sf-tl-mark')
      expect(icon.attributes('data-step')).toBe('1')
    } finally {
      wrapper.unmount()
    }
  })

  test('progress fill and earned rungs follow the tier accent instead of the site accent', () => {
    const wrapper = mountCard(1, [{ key: 'daysVisited', current: 1, threshold: 4 }])
    // 同一条卡面上「等级」与「进度」必须同色相：写死 --sf-accent 会让两者分家。
    const source = readFileSync(new URL('../../app/components/profile/SFTrustLevelCard.vue', import.meta.url), 'utf8')
    expect(source).toContain('background: var(--tl-accent);')
    expect(source).not.toContain('background: var(--sf-accent);')
    expect(wrapper.find('.sf-trust-level-card__fill').exists()).toBe(true)
  })
})

/**
 * 整卡入口（规范 §5 / §10）：`interactive` 只追加一层透明覆盖按钮。
 *
 * 刻意**不**把根节点换成 `<button>`：那样 `role="progressbar"` 会被吞进按钮的可访问名，
 * 读屏会念成「查看信任等级详情 信任等级进度：常驻用户 → 资深用户」。
 * 不传 `interactive` 时卡片仍是纯展示，既有调用点与既有断言零变化。
 */
describe('SFTrustLevelCard · 整卡入口', () => {
  function mountEntryCard(level: number, interactive: boolean) {
    return mount(SFTrustLevelCard, {
      props: { level, metrics: residentMetrics, ladder: FALLBACK_LADDER, interactive },
      global: { components: { UIcon: UIconStub } }
    })
  }

  test('without interactive there is no overlay button at all', () => {
    const wrapper = mountEntryCard(2, false)
    try {
      expect(wrapper.find('.sf-trust-level-card__hit').exists()).toBe(false)
      // 纯展示契约不变：根仍是 section，进度条照常。
      expect(wrapper.get('.sf-trust-level-card').element.tagName).toBe('SECTION')
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('interactive appends one transparent button that opens the dialog', async () => {
    const wrapper = mountEntryCard(2, true)
    try {
      const card = wrapper.get('.sf-trust-level-card')
      // 根节点不换：冻结的 data-* / aria-* 全部原样保留。
      expect(card.element.tagName).toBe('SECTION')
      expect(card.attributes('data-level')).toBe('2')
      expect(card.attributes('data-earned')).toBe('true')
      expect(card.attributes('aria-label')).toBe('信任等级 常驻用户，已获得')

      const hit = wrapper.get('.sf-trust-level-card__hit')
      expect(hit.element.tagName).toBe('BUTTON')
      expect(hit.attributes('type')).toBe('button')
      expect(hit.attributes('aria-haspopup')).toBe('dialog')
      expect(hit.attributes('aria-label')).toBe(t('profile.trustLevel.detail.open'))
      // 覆盖层是最后一个子节点：`:has()` 只能向前找，兄弟选择器够不到。
      expect(card.element.lastElementChild?.className).toBe('sf-trust-level-card__hit')

      await hit.trigger('click')
      expect(wrapper.emitted('open')).toHaveLength(1)
      // 进度条仍在：按钮没有把它包进去。
      expect(wrapper.find('[role="progressbar"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('the overlay keeps the card frozen contract and uses the 100% tier accent for feedback', () => {
    const source = readFileSync(cardPath, 'utf8')
    // 根 section 需要 position: relative 才能承载 absolute inset:0 的覆盖层。
    expect(source).toMatch(/\.sf-profile-side-section\.sf-trust-level-card \{[^}]*position: relative/)
    expect(source).toMatch(/\.sf-trust-level-card__hit \{[^}]*position: absolute/)
    expect(source).toMatch(/\.sf-trust-level-card__hit \{[^}]*inset: 0/)
    expect(source).toMatch(/\.sf-trust-level-card__hit \{[^}]*border-radius: inherit/)
    // hover / focus 用 100% 档位色：34% 混色在 parchment 上只有 1.63:1，等于没有反馈。
    expect(source).toMatch(/\.sf-trust-level-card__hit:focus-visible \{[^}]*outline: 2px solid var\(--tl-accent/)
    expect(source).toContain('.sf-trust-level-card:has(.sf-trust-level-card__hit:hover)')
    expect(source).not.toContain('var(--sf-accent)')
    // 冻结契约：整卡不换标签、不删默认阶梯、不自己拼阶梯。
    expect(source).toContain('ladder: () => []')
    expect(source).not.toContain('trustLevelLadder(')
  })
})
