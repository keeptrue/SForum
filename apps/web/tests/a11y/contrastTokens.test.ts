import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  appearanceThemes,
  lightBackgroundPalettes,
  resolveAppearanceTheme
} from '../../app/utils/settings/appearance'
import { FORUM_AVATAR_TONE_CLASSES, FORUM_CATEGORY_CHIP_TONE_CLASSES } from '../../app/utils/forum/forumListPresentation'

/**
 * 对比度收口（第 15 轮 · 软徽标 4.48:1 + axe `color-contrast`）。
 *
 * 审计原文只说「附件表第 5 列 `UBadge variant="soft"` 的 accent-on-soft 差 0.02 未过 AA」，
 * 但同一对 token 支撑着后台所有 soft/subtle 徽标与 soft Alert。真机实测（
 * `tests/a11y/scan.mjs --mode=contrast`，附件管理页状态列）：
 *
 *   修前：text `#2563eb` on `rgb(233,239,253)` = **4.49:1**（AA 需 4.5）
 *   修后：text `#1d4ed8` on `rgb(232,238,253)` = 5.8:1 级别
 *
 * 机制：`--ui-primary` / `--ui-success` 在浅色下改取 `--sf-accent-hover`（预设固定比
 * accent 深一档，自定义主题按 16% 压深计算）。这个测试用**独立实现的 WCAG 公式**
 * 把「每个预设主题都达标」钉住，避免换个主题又掉回 4.4x。
 */

function channel(value: number): number {
  const normalized = value / 255
  return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
}

function parseHex(hex: string): { r: number, g: number, b: number } {
  const value = hex.replace('#', '')
  return {
    r: Number.parseInt(value.slice(0, 2), 16),
    g: Number.parseInt(value.slice(2, 4), 16),
    b: Number.parseInt(value.slice(4, 6), 16)
  }
}

function luminance(color: { r: number, g: number, b: number }): number {
  return 0.2126 * channel(color.r) + 0.7152 * channel(color.g) + 0.0722 * channel(color.b)
}

function contrast(a: { r: number, g: number, b: number }, b: { r: number, g: number, b: number }): number {
  const first = luminance(a)
  const second = luminance(b)
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05)
}

/** `bg-{color}/10` 的真实底色：10% 前景色压在给定背景上（与浏览器合成结果一致）。 */
function tintOver(foreground: { r: number, g: number, b: number }, background: { r: number, g: number, b: number }, alpha = 0.1) {
  return {
    r: foreground.r * alpha + background.r * (1 - alpha),
    g: foreground.g * alpha + background.g * (1 - alpha),
    b: foreground.b * alpha + background.b * (1 - alpha)
  }
}

const AA_NORMAL = 4.5

describe('soft / subtle 徽标的文字与底色（每个外观主题都达标）', () => {
  test('品牌主色不被文本 token 接管：实心填充仍取 500 档', () => {
    const css = readFileSync(new URL('../../app/assets/css/main.css', import.meta.url), 'utf8')
    const root = css.slice(css.indexOf(':root {'), css.indexOf('.dark {'))
    // F2（R6）：`--ui-primary` 是实心按钮/链接/焦点环的品牌主色，必须保持 accent 档位；
    // 文本可读性由 `.text-primary` 的覆盖规则解决，不能靠压深这个 token。
    expect(root).toContain('--ui-primary: var(--ui-color-primary-500);')
    expect(root).toContain('--ui-success: var(--ui-color-success-500);')
    expect(root).toContain('--sf-accent-strong: var(--sf-accent-hover);')
    const dark = css.slice(css.indexOf('.dark {'))
    expect(dark).toContain('--ui-primary: var(--ui-color-primary-400);')
  })

  test('文本专用覆盖只作用于 .text-primary / .text-success，且只在浅色生效', () => {
    const css = readFileSync(new URL('../../app/assets/css/sforum-components.css', import.meta.url), 'utf8')
    expect(css).toContain(':root:not(.dark) .text-primary {')
    expect(css).toContain(':root:not(.dark) .text-success {')
    // 不能出现「改 --ui-primary 来提对比度」的回退写法。
    expect(css).not.toContain('--ui-primary: var(--sf-accent-strong)')
  })

  test('浅色自定义 accent（#fde047）：填充色原样保留，文本色仍达标', () => {
    const vars = resolveAppearanceTheme('custom:#fde047').cssVars
    // 品牌色不被压深（否则实心按钮会变成近黑）。
    expect(vars['--sf-accent']).toBe('#fde047')
    // 文本 token 单独压深到 AA。
    const strong = vars['--sf-accent-strong'] as string
    expect(strong).not.toBe('#fde047')
    const text = parseHex(strong)
    expect(contrast(text, parseHex('#ffffff'))).toBeGreaterThanOrEqual(AA_NORMAL)
    expect(contrast(text, tintOver(text, parseHex('#ffffff')))).toBeGreaterThanOrEqual(AA_NORMAL)
  })

  test('每个预设主题：accent-strong 文字在同色 10% 底上都 ≥ 4.5:1', () => {
    const offenders: string[] = []
    for (const preset of appearanceThemes) {
      const vars = resolveAppearanceTheme(preset).cssVars
      const strong = vars['--sf-accent-strong']
      expect({ preset, strong: Boolean(strong) }).toEqual({ preset, strong: true })
      const text = parseHex(strong!)
      // 徽标/Alert 的 10% 底压在上的是卡片面或页面底，不是 muted 底（muted 是表头/次级块）。
      for (const [backgroundName, palette] of Object.entries(lightBackgroundPalettes)) {
        for (const surface of [palette.surface, palette.background]) {
          const ratio = contrast(text, tintOver(text, parseHex(surface)))
          if (ratio < AA_NORMAL) offenders.push(`${preset} / ${backgroundName} / ${surface} :: ${ratio.toFixed(2)}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })

  test('自定义主题（任意 accent）：strongAccentColor 迭代压深后达标', () => {
    const offenders: string[] = []
    for (const accent of ['#fde047', '#22d3ee', '#f9a8d4', '#2563eb', '#0f766e', '#7c3aed', '#e11d48', '#d97706']) {
      const vars = resolveAppearanceTheme(`custom:${accent}`).cssVars
      const text = parseHex(vars['--sf-accent-strong'] as string)
      const ratio = contrast(text, tintOver(text, parseHex('#ffffff')))
      if (ratio < AA_NORMAL) offenders.push(`${accent} :: ${ratio.toFixed(2)}`)
    }
    expect(offenders).toEqual([])
  })
})

describe('头像色板：首字母与背景成对达标（axe 报 1.86:1 的根因）', () => {
  test('8 个色板逐对 ≥ 4.5:1', () => {
    const offenders: string[] = []
    for (const pair of FORUM_AVATAR_TONE_CLASSES) {
      const background = /bg-\[(#[0-9a-fA-F]{6})\]/.exec(pair)?.[1]
      const foreground = /text-\[(#[0-9a-fA-F]{6})\]/.exec(pair)?.[1] ?? (/text-white/.test(pair) ? '#ffffff' : undefined)
      expect({ pair, background: Boolean(background), foreground: Boolean(foreground) }).toEqual({ pair, background: true, foreground: true })
      const ratio = contrast(parseHex(foreground!), parseHex(background!))
      if (ratio < AA_NORMAL) offenders.push(`${pair} :: ${ratio.toFixed(2)}`)
    }
    expect(offenders).toEqual([])
  })

  test('SFAvatar 不再把 text-white 写进公共 class（首字母前景由色板决定）', () => {
    const source = readFileSync(new URL('../../app/components/SFAvatar.vue', import.meta.url), 'utf8')
    const classBlock = source.slice(source.indexOf('const classes = ['), source.indexOf('const imageAlt'))
    expect(classBlock).not.toContain("'relative inline-flex shrink-0 select-none items-center justify-center overflow-hidden font-semibold leading-none text-white'")
    expect(classBlock).toContain('forumAvatarToneClass(toneSeed.value)')
  })
})

describe('首页分类胶囊色板：文字与底色成对达标', () => {
  test('4 个胶囊色板逐对 ≥ 4.5:1（橙色一档修前 2.84:1）', () => {
    const offenders: string[] = []
    for (const pair of FORUM_CATEGORY_CHIP_TONE_CLASSES) {
      const background = /bg-\[(#[0-9a-fA-F]{6})\]/.exec(pair)?.[1]
      const foreground = /text-\[(#[0-9a-fA-F]{6})\]/.exec(pair)?.[1]
      expect({ pair, background: Boolean(background), foreground: Boolean(foreground) })
        .toEqual({ pair, background: true, foreground: true })
      const ratio = contrast(parseHex(foreground!), parseHex(background!))
      if (ratio < AA_NORMAL) offenders.push(`${pair} :: ${ratio.toFixed(2)}`)
    }
    expect(offenders).toEqual([])
  })
})

describe('后台次要文字：在每种外观背景上都达标', () => {
  const css = readFileSync(new URL('../../app/assets/css/sforum-components.css', import.meta.url), 'utf8')

  function ruleColor(selector: string): string {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const match = new RegExp(`${escaped}\\s*\\{\\s*color:\\s*(#[0-9a-fA-F]{6});`).exec(css)
    expect({ selector, found: Boolean(match) }).toEqual({ selector, found: true })
    return match![1]!
  }

  test('规则存在且只在浅色生效（深色模式不参与）', () => {
    expect(css).toContain(':root:not(.dark) .sforum-admin-shell .text-slate-400 {')
    expect(css).toContain(':root:not(.dark) .sforum-admin-shell .text-slate-500 {')
  })

  test('slate-400 / slate-500 的替代色在每个预设背景与 muted 底上都 ≥ 4.5:1', () => {
    const colors = [
      ruleColor(':root:not(.dark) .sforum-admin-shell .text-slate-400'),
      ruleColor(':root:not(.dark) .sforum-admin-shell .text-slate-500')
    ]
    const offenders: string[] = []
    for (const color of colors) {
      for (const [name, palette] of Object.entries(lightBackgroundPalettes)) {
        for (const surface of [palette.background, palette.surface, palette.muted]) {
          const ratio = contrast(parseHex(color), parseHex(surface))
          if (ratio < AA_NORMAL) offenders.push(`${color} on ${name}/${surface} :: ${ratio.toFixed(2)}`)
        }
      }
    }
    expect(offenders).toEqual([])
  })

  test('--text-muted（公开页脚版权行）在每种外观背景上达标', () => {
    const main = readFileSync(new URL('../../app/assets/css/main.css', import.meta.url), 'utf8')
    const root = main.slice(main.indexOf(':root {'), main.indexOf('.dark {'))
    const color = /--text-muted:\s*(#[0-9a-fA-F]{6});/.exec(root)?.[1]
    expect(Boolean(color)).toBe(true)
    const offenders: string[] = []
    for (const [name, palette] of Object.entries(lightBackgroundPalettes)) {
      const ratio = contrast(parseHex(color!), parseHex(palette.background))
      if (ratio < AA_NORMAL) offenders.push(`${name} :: ${ratio.toFixed(2)}`)
    }
    expect(offenders).toEqual([])
  })
})
