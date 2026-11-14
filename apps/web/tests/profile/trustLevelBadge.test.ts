import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  FALLBACK_LADDER,
  TL0_TIER,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { defaultTierColorName, tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料页头部等级徽章的契约（规范 `2026-11-14-profile-trust-level-badge-modal-spec.md` §2–§4、§10）：
 *
 * - 牌面只写 `TL{n}` + 档位色图标，文字用前景色（对比度由文字承载，不靠颜色）；
 * - `data-level` / `data-sf-tl-accent` / 内联 `--tl-accent` 是渲染契约，缺一条自定义 hex 档
 *   在暗色下就会显示未提亮的原色；
 * - `interactive` 时是原生 `button[aria-haspopup="dialog"]`，否则 `role="img"`；
 * - TL0 的环线是虚线，且不降透明度（降了会把文字压到阈值以下）。
 *
 * 文案断言取真实 zh-CN locale：键缺失会直接报错，而不是让测试自造期望值。
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
  template: '<i data-testid="badge-icon" :data-icon="name" />'
})

const badgePath = fileURLToPath(new URL('../../app/components/profile/SFTrustLevelBadge.vue', import.meta.url))
const SFTrustLevelBadge = await compileVueSfc(badgePath, 'sf-trust-level-badge', {
  computed,
  useI18n: () => ({ t, locale: ref('zh-CN') }),
  FALLBACK_LADDER,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  tierAccentStyle,
  tierColorOf
})

function mountBadge(props: Record<string, unknown>) {
  return mount(SFTrustLevelBadge, {
    props,
    global: { components: { UIcon: UIconStub } }
  })
}

/** 运营自定义档位（非内置 id）：名字必须来自目录，回退链不能被内置五档劫持。 */
const customLadder: TrustLevelTier[] = [
  TL0_TIER,
  { id: 'tier_6', name: { zh: '见习', en: 'Trainee' }, icon: 'compass', requirements: {}, retired: false },
  { id: 'tier_7', name: { zh: '掌舵人', en: 'Helm' }, icon: 'anchor', color: 'rose', requirements: {}, retired: false }
]

describe('SFTrustLevelBadge · 牌面', () => {
  test('renders the tier colour plate with the code, the icon and the frozen data attributes', () => {
    const wrapper = mountBadge({ level: 2 })
    try {
      const badge = wrapper.get('.sf-trust-level-badge')
      // 只读徽章是 span + role="img"：不该出现在 tab 序列里。
      expect(badge.element.tagName).toBe('SPAN')
      expect(badge.attributes('role')).toBe('img')
      expect(badge.attributes('data-level')).toBe('2')
      // 自定义 hex 档的色相全靠这条属性 + 全局 `[data-sf-tl-accent]` 规则。
      expect(badge.attributes('data-sf-tl-accent')).toBeDefined()
      expect(badge.attributes('style')).toContain(`--tl-accent: var(--sf-tl-${defaultTierColorName(2)})`)
      expect(wrapper.get('.sf-trust-level-badge__code').text()).toBe('TL2')
      // 牌面不写档位名：长名会挤爆 390px 的身份行（规范 §1）。
      expect(badge.text()).toBe('TL2')
      expect(wrapper.get('[data-testid="badge-icon"]').attributes('data-icon')).toBe('i-lucide-home')
      // aria-label 自带档位名与 TLn；title 只补全名 + 状态。
      expect(badge.attributes('aria-label')).toBe('信任等级 常驻用户，TL2，已获得')
      expect(badge.attributes('title')).toBe('信任等级 常驻用户，已获得')
    } finally {
      wrapper.unmount()
    }
  })

  test('TL0 says "not earned" and keeps the same geometry', () => {
    const wrapper = mountBadge({ level: 0 })
    try {
      const badge = wrapper.get('.sf-trust-level-badge')
      expect(badge.attributes('data-level')).toBe('0')
      expect(badge.attributes('aria-label')).toBe('信任等级 新用户，TL0，尚未获得')
      expect(badge.attributes('style')).toContain(`--tl-accent: var(--sf-tl-${defaultTierColorName(0)})`)
      expect(wrapper.get('.sf-trust-level-badge__code').text()).toBe('TL0')
    } finally {
      wrapper.unmount()
    }
  })

  test('custom tier names and icons come from the ladder, not from the builtin five', () => {
    const wrapper = mountBadge({ level: 2, ladder: customLadder })
    try {
      expect(wrapper.get('.sf-trust-level-badge').attributes('aria-label')).toBe('信任等级 掌舵人，TL2，已获得')
      expect(wrapper.get('[data-testid="badge-icon"]').attributes('data-icon')).toBe('i-lucide-anchor')
      // 存储色优先（rose），不是 rank 2 的默认 emerald。
      expect(wrapper.get('.sf-trust-level-badge').attributes('style')).toContain('--tl-accent: var(--sf-tl-rose)')
    } finally {
      wrapper.unmount()
    }
  })

  test('an out-of-range level clamps to the top rung instead of printing TL99', () => {
    const wrapper = mountBadge({ level: 99, ladder: customLadder })
    try {
      expect(wrapper.get('.sf-trust-level-badge').attributes('data-level')).toBe('2')
      expect(wrapper.get('.sf-trust-level-badge__code').text()).toBe('TL2')
    } finally {
      wrapper.unmount()
    }
  })

  test('an empty ladder falls back to the builtin names and icons', () => {
    const wrapper = mountBadge({ level: 1, ladder: [] })
    try {
      expect(wrapper.get('.sf-trust-level-badge').attributes('aria-label')).toBe('信任等级 正式用户，TL1，已获得')
      expect(wrapper.get('[data-testid="badge-icon"]').attributes('data-icon')).toBe('i-lucide-leaf')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTrustLevelBadge · 交互', () => {
  test('interactive renders a native button that emits open', async () => {
    const wrapper = mountBadge({ level: 2, interactive: true })
    try {
      const badge = wrapper.get('.sf-trust-level-badge')
      expect(badge.element.tagName).toBe('BUTTON')
      expect(badge.attributes('type')).toBe('button')
      expect(badge.attributes('aria-haspopup')).toBe('dialog')
      expect(badge.attributes('role')).toBeUndefined()
      expect(badge.attributes('aria-label')).toBe('信任等级 常驻用户，TL2，已获得')

      await badge.trigger('click')
      expect(wrapper.emitted('open')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('the read-only span never emits open', async () => {
    const wrapper = mountBadge({ level: 2 })
    try {
      await wrapper.get('.sf-trust-level-badge').trigger('click')
      expect(wrapper.emitted('open')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTrustLevelBadge · 样式契约', () => {
  const source = readFileSync(badgePath, 'utf8')

  test('TL0 draws a dashed ring instead of lowering opacity', () => {
    // 虚线表达「未入门」（与等级卡同一套语言）；降透明度会把文字与图标一起压到阈值以下。
    expect(source).toMatch(/\.sf-trust-level-badge\[data-level="0"\]\s*\{[^}]*border-style:\s*dashed/)
    expect(source).not.toMatch(/\.sf-trust-level-badge\[data-level="0"\][^}]*opacity/)
  })

  test('the no-color-mix fallback keeps a readable neutral plate', () => {
    // 旧浏览器没有 color-mix：基础规则那条 `border: 1px solid color-mix(...)` 是 shorthand，
    // 取值非法时整条声明失效（border-style 回 none），所以兜底必须重写**完整 shorthand**。
    const fallback = source.match(/@supports not \(color: color-mix\([^)]*\)\)\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
    expect(fallback).toContain('border: 1px solid var(--sf-border)')
    expect(fallback).toContain('background: transparent')
  })

  test('the plate never declares --tl-accent itself and never hard-codes a tier token class', () => {
    // 声明 --tl-accent 会盖掉内联色（自定义 hex 档被静默打回 slate）；
    // 9 个 `--sf-tl-*` token 类会绕开 data-sf-tl-accent，暗色下拿到未提亮的原色。
    expect(source).not.toMatch(/--tl-accent\s*:/)
    expect(source).not.toMatch(/\.sf-trust-level-badge\[data-color/)
    expect(source).toContain('data-sf-tl-accent')
  })

  test('the plate uses the public foreground token for the code text', () => {
    expect(source).toMatch(/\.sf-trust-level-badge\s*\{[^}]*color:\s*var\(--sf-public-text\)/)
    expect(source).toMatch(/\.sf-trust-level-badge\s*\{[^}]*height:\s*20px/)
  })
})
