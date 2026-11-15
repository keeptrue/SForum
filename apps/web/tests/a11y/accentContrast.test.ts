import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { accentContrastColor, resolveAppearanceTheme } from '../../app/utils/settings/appearance'

/**
 * 强调色对比度（UX2-12 + RV-22）。
 *
 * UX2-12：前景色不能按「亮度阈值 0.55」猜，必须比对比度。
 * RV-22：算出 token 之后，调用点必须真的用它——`bg-[var(--sf-accent)]` 旁边
 * 再写 `text-white` 就是绕过 token，这里做棘轮。
 */

/** 独立实现的 WCAG 对比度，避免用被测代码验证被测代码。 */
function luminance(hex: string): number {
  const value = hex.replace('#', '')
  const channel = (offset: number) => {
    const normalized = Number.parseInt(value.slice(offset, offset + 2), 16) / 255
    return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(0) + 0.7152 * channel(2) + 0.0722 * channel(4)
}

function contrast(a: string, b: string): number {
  const first = luminance(a)
  const second = luminance(b)
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05)
}

/**
 * 规则体里的 `color:` 是否写着白色。覆盖四类写法：
 * hex（#fff / #ffffff / #ffffffff）、关键字 `white`、`rgb()/rgba()`、`hsl()/hsla()`。
 * 只匹配 hex 会漏掉 `rgb(255,255,255)` 这种等价写法（变异 12 就是这么漏的）。
 */
function isWhiteForeground(body: string): boolean {
  const match = /(?:^|[;\s{])color:\s*([^;}]+)/i.exec(body)
  if (!match) return false
  const value = (match[1] ?? '').trim().toLowerCase()
  if (/^#(fff|ffffff|ffffffff)$/.test(value)) return true
  if (value === 'white') return true
  if (/^rgba?\(\s*255\s*[,\s]\s*255\s*[,\s]\s*255\s*(?:[,/][^)]*)?\)$/.test(value)) return true
  return /^hsla?\(\s*0(?:deg)?\s*[,\s]\s*0%\s*[,\s]\s*100%\s*(?:[,/][^)]*)?\)$/.test(value)
}

describe('accentContrastColor 选对比度更高的前景', () => {
  test('浅色强调色选墨黑，深色强调色选白', () => {
    expect(accentContrastColor('#fde047')).toBe('#111827') // 浅黄
    expect(accentContrastColor('#22d3ee')).toBe('#111827') // 青
    expect(accentContrastColor('#f9a8d4')).toBe('#111827') // 粉
    expect(accentContrastColor('#ffffff')).toBe('#111827')
    expect(accentContrastColor('#2563eb')).toBe('#ffffff')
    expect(accentContrastColor('#0f766e')).toBe('#ffffff')
    expect(accentContrastColor('#000000')).toBe('#ffffff')
  })

  test('每个颜色上，选中的前景都严格优于另一个候选', () => {
    const samples = ['#fde047', '#22d3ee', '#f9a8d4', '#2563eb', '#0f766e', '#7c3aed', '#e11d48', '#d97706', '#808080', '#111827']
    for (const color of samples) {
      const chosen = accentContrastColor(color)
      const other = chosen === '#111827' ? '#ffffff' : '#111827'
      expect(contrast(color, chosen)).toBeGreaterThanOrEqual(contrast(color, other))
    }
  })

  test('旧阈值 0.55 的翻车色现在至少达到 4.5:1', () => {
    // 这三个颜色在旧实现里被判成「用白字」，实测只有 1.2–1.9:1。
    for (const color of ['#fde047', '#22d3ee', '#f9a8d4']) {
      expect(contrast(color, accentContrastColor(color))).toBeGreaterThanOrEqual(4.5)
    }
  })

  test('预设主题同样产出 token：amber 必须给墨黑（白字只有 3.19:1）', () => {
    expect(resolveAppearanceTheme('amber').cssVars['--sf-accent-contrast']).toBe('#111827')
    expect(contrast('#d97706', '#111827')).toBeGreaterThan(4.5)
    expect(contrast('#d97706', '#ffffff')).toBeLessThan(4.5)
    for (const preset of ['pine_teal', 'ocean_blue', 'violet', 'rose']) {
      expect(resolveAppearanceTheme(preset).cssVars['--sf-accent-contrast']).toBe('#ffffff')
    }
  })

  test('自定义主题的 cssVars 带上了这个 token（用旧阈值会翻车的颜色）', () => {
    // #22d3ee / #f9a8d4 的相对亮度都低于旧阈值 0.55，旧实现会给白字（≈1.7–1.9:1）。
    for (const color of ['#22d3ee', '#f9a8d4']) {
      expect(resolveAppearanceTheme(`custom:${color}`).cssVars['--sf-accent-contrast']).toBe('#111827')
    }
    expect(resolveAppearanceTheme('custom:#2563eb').cssVars['--sf-accent-contrast']).toBe('#ffffff')
  })
})

describe('RV-22 棘轮：强调色背景上不得再写死白字', () => {
  test('app 下没有任何 class 串同时出现 bg-[var(--sf-accent)] 与 text-white', () => {
    const files = execFileSync('grep', ['-rl', 'bg-\\[var(--sf-accent)\\]', 'app'], { encoding: 'utf8' })
      .trim()
      .split('\n')
      .filter(Boolean)

    const offenders: string[] = []
    for (const file of files) {
      const lines = readFileSync(file, 'utf8').split('\n')
      lines.forEach((line, index) => {
        if (line.includes('bg-[var(--sf-accent)]') && line.includes('text-white')) {
          offenders.push(`${file}:${index + 1}`)
        }
      })
    }

    expect(offenders).toEqual([])
  })

  test('CSS 层棘轮：accent 背景的规则里不得再写死白色前景', () => {
    // class 串棘轮扫不到 .css 文件：`background: var(--sf-accent); color: #fff;`
    // 是同一个缺陷的另一种写法（浅 accent 下白字 ≈1.2:1）。
    const files = execFileSync('grep', ['-rl', 'var(--sf-accent)', 'app', '--include=*.css'], { encoding: 'utf8' })
      .trim()
      .split('\n')
      .filter(Boolean)

    const offenders: string[] = []
    for (const file of files) {
      const source = readFileSync(file, 'utf8')
      const rule = /([^{}]+)\{([^{}]*)\}/g
      let match: RegExpExecArray | null
      while ((match = rule.exec(source))) {
        const body = match[2] ?? ''
        const accentBackground = /background(?:-color)?:\s*var\(--sf-accent(?:-hover)?\)/.test(body)
        if (accentBackground && isWhiteForeground(body)) {
          offenders.push(`${file}:${source.slice(0, match.index).split('\n').length}`)
        }
      }
    }

    expect(offenders).toEqual([])
  })

  test('白色前景判定覆盖 hex / 关键字 / rgb / hsl 四形态', () => {
    for (const value of ['#fff', '#ffffff', '#ffffffff', 'white', 'rgb(255, 255, 255)', 'rgba(255, 255, 255, 0.8)', 'hsl(0, 0%, 100%)', 'hsla(0 0% 100% / 0.9)']) {
      expect(isWhiteForeground(`background: var(--sf-accent); color: ${value};`)).toBe(true)
    }
    for (const value of ['#111827', '#fde047', 'black', 'rgb(17, 24, 39)', 'hsl(220, 39%, 11%)', 'var(--sf-accent-contrast)']) {
      expect(isWhiteForeground(`background: var(--sf-accent); color: ${value};`)).toBe(false)
    }
  })

  test('.sf-button--primary 用 token 而不是 #ffffff', () => {
    const css = readFileSync(new URL('../../app/assets/css/sforum-components.css', import.meta.url), 'utf8')
    const block = css.match(/\.sf-button--primary\s*\{[^}]*\}/s)?.[0] ?? ''
    expect(block).toContain('color: var(--sf-accent-contrast')
    expect(block).not.toMatch(/color:\s*#ffffff/)
  })
})
