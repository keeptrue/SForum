import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  TIER_COLOR_TOKENS,
  TIER_CUSTOM_COLOR_PATTERN,
  TIER_DARK_ACCENT_MIX,
  defaultTierColorName,
  isTierColorToken,
  isTierColorValue,
  isTierCustomColor,
  normalizeTierColor,
  tierAccentContrast,
  tierAccentDarkHex,
  tierAccentPreview,
  tierAccentStyle,
  tierColorOf,
  tierColorVar
} from '~/utils/profile/trustLevelColors'

/**
 * 档位配色的**唯一实现**（D-C1 / D-C5）：取值域、两条渲染路径、双底预览、以及 CSS 的三层映射。
 *
 * 为什么单独一个文件：`trustLevelsTabMount.test.ts` 证明「运营点了会发生什么」，
 * 但配色正确性其实全在这组纯函数与那份 CSS 里 —— 挂载测试只能看到 `--tl-accent` 字符串，
 * 看不到「hex 在深色页面被提亮」「预览两卡不跟着页面模式走」这些真正的语义。
 *
 * 这些用例守的三件事：
 * 1. 取值域 = 9 token ∪ `#rrggbb`，其它一律 `null`（不是抛错，也不是猜）；
 * 2. token 走 `--tl-accent`（明暗由 CSS 变量自己切换），hex 走 `--tl-accent-light/-dark` 两个键
 *    —— 两条路径的**形状**不能混，混了就会出现「深色页面里 hex 不跟着变」；
 * 3. CSS 里 18 个字面量只出现一次，三层映射逐字对齐 `TIER_COLOR_TOKENS`（顺序 = 默认色序）。
 */

const cssPath = fileURLToPath(new URL('../../../app/assets/css/sforum-trust-colors.css', import.meta.url))
const cssSource = readFileSync(cssPath, 'utf8')
// 注释里出现过 hex 举例（#1b201f），先剥注释再数，否则「字面量只出现一次」会被注释误伤。
const css = cssSource.replace(/\/\*[\s\S]*?\*\//g, '')

/** 取一个选择器块的内容（不支持嵌套，本文件恰好没有嵌套）。 */
function cssBlock(selector: string): string {
  const start = css.indexOf(`${selector} {`)
  if (start === -1) throw new Error(`CSS 里找不到选择器块：${selector}`)
  const open = css.indexOf('{', start)
  const close = css.indexOf('}', open)
  return css.slice(open + 1, close)
}

function declarations(block: string): Array<[string, string]> {
  return block
    .split(';')
    .map(line => line.trim())
    .filter(Boolean)
    .map((line) => {
      const split = line.indexOf(':')
      return [line.slice(0, split).trim(), line.slice(split + 1).trim()] as [string, string]
    })
}

function hexLiterals(block: string): string[] {
  return [...block.matchAll(/#[0-9a-fA-F]{3,8}\b/g)].map(match => match[0].toLowerCase())
}

describe('color value domain', () => {
  test('normalizeTierColor keeps tokens, lowercases hex, and refuses everything else', () => {
    expect(normalizeTierColor('rose')).toBe('rose')
    // 前后空白来自输入框，归一化吸收掉；值本身不做语义推测。
    expect(normalizeTierColor('  violet  ')).toBe('violet')
    expect(normalizeTierColor('#7C3AED')).toBe('#7c3aed')
    expect(normalizeTierColor(' #7c3aed ')).toBe('#7c3aed')
    // 「不认识的颜色」= 「没配色」：一律 null，既不抛错也不猜。
    for (const value of ['chartreuse', '#fff', '#7c3aed0', '#7c3aed00', 'red', 'rgb(0,0,0)', '', null, 0, {}]) {
      expect(normalizeTierColor(value)).toBeNull()
    }
  })

  test('isTierColorValue is the shape gate that trades with the server', () => {
    for (const token of TIER_COLOR_TOKENS) expect(isTierColorValue(token)).toBe(true)
    // 大写 hex 合法（归一化在 normalizeTierColor 里），三位短写法不合法（六位是契约）。
    expect(isTierColorValue('#7C3AED')).toBe(true)
    for (const value of ['#fff', 'red', '', null, 123, undefined, '#7c3aed00']) {
      expect(isTierColorValue(value)).toBe(false)
    }
  })

  test('isTierCustomColor distinguishes "a hex" from "a token"', () => {
    expect(isTierCustomColor('#7C3AED')).toBe(true)
    expect(isTierCustomColor('rose')).toBe(false)
    expect(isTierCustomColor('7c3aed')).toBe(false)
    // 正则与被测函数同源：断言常量本身也钉住，避免有人把 `^#?` 加进去后两处一起变绿。
    expect(TIER_CUSTOM_COLOR_PATTERN.source).toBe('^#[0-9a-f]{6}$')
  })

  test('tierColorOf prefers the stored color and otherwise follows the rank', () => {
    expect(tierColorOf({ color: '#7C3AED' }, 3)).toBe('#7c3aed')
    expect(tierColorOf({ color: 'rose' }, 0)).toBe('rose')
    // 停用档 / 未配置：按调用方给的位次取默认。
    expect(tierColorOf(null, 3)).toBe(defaultTierColorName(3))
    expect(tierColorOf({ color: 'chartreuse' }, 2)).toBe(defaultTierColorName(2))
    // 默认色序是 9 色循环（rank 9 = slate，与 TL0 同色只因位次取模，不是 bug）。
    expect(defaultTierColorName(9)).toBe('slate')
  })

  test('tierColorVar emits a CSS reference for tokens and the literal for hex', () => {
    expect(tierColorVar('rose')).toBe('var(--sf-tl-rose)')
    expect(tierColorVar('#7C3AED')).toBe('#7c3aed')
    expect(tierColorVar(null)).toBe('var(--sf-tl-slate)')
    // 写一个不存在的变量会让 color-mix 整条失效（色条直接透明），所以必须回退 slate。
    expect(tierColorVar('chartreuse')).toBe('var(--sf-tl-slate)')
  })
})

describe('accent rendering paths', () => {
  test('token path keeps a single inline --tl-accent so CSS can switch light/dark', () => {
    expect(tierAccentStyle('rose')).toEqual({ '--tl-accent': 'var(--sf-tl-rose)' })
    expect(tierAccentStyle('amber')).toEqual({ '--tl-accent': 'var(--sf-tl-amber)' })
    // 垃圾值与 null 同义：回退 slate token（而不是写一个不存在的变量名）。
    expect(tierAccentStyle(null)).toEqual({ '--tl-accent': 'var(--sf-tl-slate)' })
    expect(tierAccentStyle('chartreuse')).toEqual({ '--tl-accent': 'var(--sf-tl-slate)' })
  })

  test('hex path inlines both variants and never touches --tl-accent', () => {
    const style = tierAccentStyle('#7C3AED')
    expect(style).toEqual({
      '--tl-accent-light': '#7c3aed',
      '--tl-accent-dark': `color-mix(in srgb, #7c3aed ${TIER_DARK_ACCENT_MIX * 100}%, white)`
    })
    // 内联 --tl-accent 会**压过**全局 [data-sf-tl-accent] 规则，深色页面就再也提亮不了。
    expect(style['--tl-accent']).toBeUndefined()
  })

  test('tierAccentPreview returns both card modes with identical token styles', () => {
    const token = tierAccentPreview('rose', 1)
    expect(token).toEqual({
      light: { scope: 'light', style: { '--tl-accent': 'var(--sf-tl-rose)' } },
      dark: { scope: 'dark', style: { '--tl-accent': 'var(--sf-tl-rose)' } }
    })
    // scope 只决定 CSS 解析出哪套 hex，不改变 style 本身（组件里没有任何「是否暗色」的判断）。
    expect(token.light.style).toEqual(token.dark.style)

    const custom = tierAccentPreview('#7C3AED', 1)
    expect(custom.light.style).toEqual({ '--tl-accent': '#7c3aed' })
    expect(custom.dark.style).toEqual({
      '--tl-accent': `color-mix(in srgb, #7c3aed ${TIER_DARK_ACCENT_MIX * 100}%, white)`
    })
    // 自定义色的两卡都是字面量：卡片自身声明，不依赖页面主题，也不依赖 scope 规则。
    expect(custom.light.scope).toBe('light')
    expect(custom.dark.scope).toBe('dark')
    expect(JSON.stringify(custom)).not.toContain('var(--sf-tl-')
  })

  test('tierAccentPreview resolves null / garbage through the rank before previewing', () => {
    const fromRank = { light: { scope: 'light', style: { '--tl-accent': 'var(--sf-tl-sky)' } }, dark: { scope: 'dark', style: { '--tl-accent': 'var(--sf-tl-sky)' } } }
    // null 不能直接进预览（那会渲染成「无颜色」），必须先按位次解析出有效色。
    expect(tierAccentPreview(null, 3)).toEqual(fromRank)
    expect(tierAccentPreview('chartreuse', 3)).toEqual(fromRank)
  })
})

describe('custom hex dark variant', () => {
  test('tierAccentDarkHex applies the same 74% white mix the CSS uses', () => {
    // 手算：0x7c=124 → round(124*0.74 + 66.3) = 158 = 0x9e；0x3a=58 → 109 = 0x6d；0xed=237 → 242 = 0xf2。
    expect(tierAccentDarkHex('#7C3AED')).toBe('#9e6df2')
    // 白色提亮后仍是白色；纯黑被抬到 #424242（0*0.74 + 255*0.26 = 66.3 → 66 = 0x42），
    // 否则深色卡面上等于看不见 —— 这就是「自定义色也要有深色变体」的由来。
    expect(tierAccentDarkHex('#ffffff')).toBe('#ffffff')
    expect(tierAccentDarkHex('#000000')).toBe('#424242')
    // 非 hex 一律 null：token 的深色取值住在 CSS 里，JS 不抄第二份。
    expect(tierAccentDarkHex('violet')).toBeNull()
    expect(tierAccentDarkHex(null)).toBeNull()
  })

  test('tierAccentContrast measures the mixed accent against the dark card surface', () => {
    // 深到发黑的品牌色在深色页面上会糊掉 —— 这是「提示但不阻断」的唯一依据。
    expect(tierAccentContrast('#111827')!).toBeLessThan(3)
    // 极亮色远超阈值，不会误报。
    expect(tierAccentContrast('#fde047')!).toBeGreaterThan(7)
    expect(tierAccentContrast('#7c3aed')!).toBeGreaterThanOrEqual(3)
    // 无法判断时返回 null，调用方不得当 0 用（token 没有 JS 侧深色取值）。
    expect(tierAccentContrast('violet')).toBeNull()
    expect(tierAccentContrast(null)).toBeNull()
  })
})

describe('CSS three-layer palette mapping', () => {
  test('the 18 literals appear exactly once and nowhere else', () => {
    const rootBlock = cssBlock(':root')
    const literals = hexLiterals(rootBlock)
    expect(literals).toHaveLength(18)
    // 每个字面量全文只出现一次：出现两次就是「第二份真相」回来了。
    for (const literal of literals) {
      expect(css.split(literal).length - 1).toBe(1)
    }
    // 词法形状：`--sf-tl-<token>-light` / `-dark` 各 9 条，token 序列 = TIER_COLOR_TOKENS。
    const decls = declarations(rootBlock)
    expect(decls.map(([name]) => name)).toEqual([
      ...TIER_COLOR_TOKENS.map(token => `--sf-tl-${token}-light`),
      ...TIER_COLOR_TOKENS.map(token => `--sf-tl-${token}-dark`)
    ])
    // 抽查语义锚点：violet 的明暗两个值（改配色表必须同时改这里与测试里的期望）。
    expect(decls.find(([name]) => name === '--sf-tl-violet-light')?.[1]).toBe('#7c3aed')
    expect(decls.find(([name]) => name === '--sf-tl-violet-dark')?.[1]).toBe('#a78bfa')
  })

  test('the three mapping blocks contain no hex literal at all', () => {
    for (const selector of [':root,\n[data-sf-tl-scope="light"]', '.dark', '[data-sf-tl-scope="dark"]']) {
      const block = cssBlock(selector)
      // 映射块里写 hex = 18 个字面量以外又长出第二份真值。
      expect(hexLiterals(block)).toHaveLength(0)
    }
  })

  test('light maps to -light, .dark and the dark scope map to -dark, in token order', () => {
    const expectMapping = (selector: string, suffix: 'light' | 'dark') => {
      const decls = declarations(cssBlock(selector))
      expect(decls.map(([name]) => name)).toEqual(TIER_COLOR_TOKENS.map(token => `--sf-tl-${token}`))
      for (const [index, token] of TIER_COLOR_TOKENS.entries()) {
        expect(decls[index]![1]).toBe(`var(--sf-tl-${token}-${suffix})`)
      }
    }
    expectMapping(':root,\n[data-sf-tl-scope="light"]', 'light')
    expectMapping('.dark', 'dark')
    expectMapping('[data-sf-tl-scope="dark"]', 'dark')
  })

  test('the accent rules pick a variant for hex rows and stay out of the token path', () => {
    const light = declarations(cssBlock('[data-sf-tl-accent]'))
    const dark = declarations(cssBlock('.dark [data-sf-tl-accent]'))
    expect(light).toHaveLength(1)
    expect(dark).toHaveLength(1)
    expect(light[0]![0]).toBe('--tl-accent')
    expect(dark[0]![0]).toBe('--tl-accent')
    // light 规则只认 -light；dark 规则先认 -dark，缺省再回 -light（token 档没有这两个键，
    // 所以真正生效的仍是它自己的内联 --tl-accent，两条规则不会把 token 打回 slate）。
    expect(light[0]![1]).toBe('var(--tl-accent-light, var(--sf-tl-slate))')
    expect(dark[0]![1]).toBe('var(--tl-accent-dark, var(--tl-accent-light, var(--sf-tl-slate)))')
  })
})
