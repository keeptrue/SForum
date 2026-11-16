export type AppearanceThemePreset = 'pine_teal' | 'ocean_blue' | 'violet' | 'rose' | 'amber'
export type AppearanceTheme = AppearanceThemePreset | `custom:${string}`
export type ResolvedAppearanceTheme = {
  theme: AppearanceTheme
  dataTheme: AppearanceThemePreset | 'custom'
  customColor: string
  cssVars: Record<string, string>
  style: string
}

export type LightBackgroundPreset =
  | 'pure_white'
  | 'porcelain'
  | 'paper'
  | 'parchment'
  | 'mist_gray'
  | 'cool_frost'
  | 'cloud_blue'
  | 'mint_mist'
  | 'sage'
  | 'sakura'
  | 'lilac_mist'
  | 'morning_apricot'
export type LightBackgroundPalette = {
  background: string
  surface: string
  muted: string
  border: string
}

export const appearanceThemes: AppearanceThemePreset[] = ['pine_teal', 'ocean_blue', 'violet', 'rose', 'amber']
export const recommendedAppearanceTheme: AppearanceThemePreset = 'pine_teal'
/**
 * 预设主题的强调色（与 `main.css` 的 `html[data-sforum-theme="…"]` 一一对应）。
 * 有了它，预设分支也能算出 `--sf-accent-contrast`：amber(#d97706) 上白字只有 3.19:1，
 * 必须给墨黑（5.1:1）。以前预设分支返回空 cssVars，全靠 main.css 里写死的 #ffffff。
 */
export const appearanceThemeAccents: Record<AppearanceThemePreset, string> = {
  pine_teal: '#0f766e',
  ocean_blue: '#2563eb',
  violet: '#7c3aed',
  rose: '#e11d48',
  amber: '#d97706'
}
export const defaultCustomThemeColor = '#2563eb'
export const lightBackgroundPresets: LightBackgroundPreset[] = [
  'pure_white',
  'porcelain',
  'paper',
  'parchment',
  'mist_gray',
  'cool_frost',
  'cloud_blue',
  'mint_mist',
  'sage',
  'sakura',
  'lilac_mist',
  'morning_apricot'
]
export const recommendedLightBackground: LightBackgroundPreset = 'pure_white'
export const lightBackgroundPalettes: Record<LightBackgroundPreset, LightBackgroundPalette> = {
  pure_white: { background: '#ffffff', surface: '#ffffff', muted: '#f5f5f8', border: '#e9e8ed' },
  porcelain: { background: '#f8faf9', surface: '#ffffff', muted: '#eff3f1', border: '#dce5e1' },
  paper: { background: '#f7f5ef', surface: '#fffdf8', muted: '#f1ede4', border: '#ded8cb' },
  parchment: { background: '#f4eedf', surface: '#fcf8ef', muted: '#ebe2cf', border: '#d8ccb5' },
  mist_gray: { background: '#f3f5f7', surface: '#ffffff', muted: '#eaedf1', border: '#dde1e6' },
  cool_frost: { background: '#f4f7fa', surface: '#fbfcfe', muted: '#eaf0f5', border: '#d9e2ea' },
  cloud_blue: { background: '#f1f6fb', surface: '#f9fcff', muted: '#e6eff8', border: '#d3e0ec' },
  mint_mist: { background: '#f1f8f5', surface: '#fbfffd', muted: '#e4f0eb', border: '#cfe1d9' },
  sage: { background: '#f2f5ef', surface: '#fbfcfa', muted: '#e6ebe1', border: '#d2dccb' },
  sakura: { background: '#fcf4f5', surface: '#fffbfb', muted: '#f7e7e9', border: '#ebd4d8' },
  lilac_mist: { background: '#f7f4fa', surface: '#fdfbff', muted: '#ede7f3', border: '#dfd4e8' },
  morning_apricot: { background: '#fcf4eb', surface: '#fffaf5', muted: '#f6e7d7', border: '#ebd5bd' }
}

const customThemePrefix = 'custom:'

export function normalizeAppearanceThemeValue(value: string | undefined): AppearanceTheme {
  const raw = value?.trim().toLowerCase() || ''
  if (isAppearanceThemePreset(raw)) {
    return raw
  }

  if (raw.startsWith(customThemePrefix)) {
    const color = normalizeHexColor(raw.slice(customThemePrefix.length))
    return color ? (`${customThemePrefix}${color}` as AppearanceTheme) : recommendedAppearanceTheme
  }

  const color = normalizeHexColor(raw)
  return color ? (`${customThemePrefix}${color}` as AppearanceTheme) : recommendedAppearanceTheme
}

export function isAppearanceThemePreset(value: string): value is AppearanceThemePreset {
  return appearanceThemes.includes(value as AppearanceThemePreset)
}

export function buildCustomAppearanceThemeValue(color: string): AppearanceTheme {
  return `${customThemePrefix}${normalizeHexColor(color) || defaultCustomThemeColor}` as AppearanceTheme
}

export function customColorFromAppearanceTheme(value: string): string | null {
  const normalized = normalizeAppearanceThemeValue(value)
  if (!normalized.startsWith(customThemePrefix)) {
    return null
  }
  return normalizeHexColor(normalized.slice(customThemePrefix.length))
}

export function resolveAppearanceTheme(value: string): ResolvedAppearanceTheme {
  const theme = normalizeAppearanceThemeValue(value)
  const customColor = customColorFromAppearanceTheme(theme) || defaultCustomThemeColor

  if (isAppearanceThemePreset(theme)) {
    const accent = appearanceThemeAccents[theme]
    const cssVars = {
      '--sf-accent-contrast': accentContrastColor(accent),
      // 预设也要按实测对比度给出 soft 表面文字色：amber(#d97706) 的 accent-hover
      // 在自身 10% 底上只有 4.38:1，靠「深一档」猜不够。
      '--sf-accent-strong': strongAccentColor(accent)
    }
    return { theme, dataTheme: theme, customColor, cssVars, style: cssVarsToStyle(cssVars) }
  }

  const cssVars = buildCustomThemeVars(customColor)
  return { theme, dataTheme: 'custom', customColor, cssVars, style: cssVarsToStyle(cssVars) }
}

export function normalizeHexColor(value: string | undefined): string | null {
  const raw = value?.trim().toLowerCase().replace(/^#/, '') || ''
  return /^[0-9a-f]{6}$/.test(raw) ? `#${raw}` : null
}

/**
 * 强调色上的前景色（UX2-12）。
 *
 * 旧实现用「相对亮度 > 0.55」当阈值：0.55 落在亮度中段，浅黄(#fde047)、青(#22d3ee)、
 * 粉(#f9a8d4) 这类颜色会被判成「该用白字」，实际对比度只有 1.2–1.9:1。
 * 现在直接比两种候选前景（白 / 墨黑）与强调色的对比度，取更高的那个——
 * 这正是 WCAG 要求的方向，也自动覆盖 4.5:1 边界。
 */
export function accentContrastColor(color: string): string {
  const rgb = hexToRgb(color)
  const onWhite = contrastRatio(rgb, WHITE)
  const onInk = contrastRatio(rgb, INK)
  return onInk > onWhite ? INK_HEX : '#ffffff'
}

export function normalizeLightBackground(value: string | undefined): LightBackgroundPreset {
  const normalized = value?.trim().toLowerCase()
  return lightBackgroundPresets.find(preset => preset === normalized) || recommendedLightBackground
}

/**
 * soft/subtle 表面上的文字色（`--sf-accent-strong`）。
 *
 * Nuxt UI 的 soft 徽标/Alert 用 `text-{color}` 配 `bg-{color}/10`：文字与底色同源，
 * 只有文字足够深才可能过 AA。预设主题的 `--sf-accent-hover` 已经深一档；自定义主题
 * 可以是任意色（浅黄 #fde047 压 16% 后仍只有 1.8:1），所以这里按**实测对比度**迭代
 * 压深，直到在「自身 10% 底」与白底上都 ≥ 4.5:1。
 */
export function strongAccentColor(accent: string): string {
  const base = normalizeHexColor(accent) || defaultCustomThemeColor
  // 参照底取「所有浅色外观预设里最暗的那张」：soft 徽标可能直接落在页面底上，
  // 只对白底达标会漏掉 parchment(#f4eedf) / mist_gray(#f3f5f7) 这类背景。
  const backdrop = darkestLightBackground()
  const tint = mixHex(base, backdrop, 0.9)
  let candidate = base
  for (let step = 0; step < 24; step += 1) {
    const rgb = hexToRgb(candidate)
    if (contrastRatio(rgb, hexToRgb(tint)) >= 4.5 && contrastRatio(rgb, hexToRgb(backdrop)) >= 4.5) return candidate
    candidate = mixHex(candidate, '#000000', 0.12)
  }
  return candidate
}

/** 最暗的浅色外观背景（对比度计算的最坏参照底）。 */
function darkestLightBackground(): string {
  let darkest = '#ffffff'
  let lowest = Number.POSITIVE_INFINITY
  for (const palette of Object.values(lightBackgroundPalettes)) {
    const luminance = relativeLuminance(hexToRgb(palette.background))
    if (luminance < lowest) {
      lowest = luminance
      darkest = palette.background
    }
  }
  return darkest
}

function buildCustomThemeVars(color: string): Record<string, string> {
  const accent = normalizeHexColor(color) || defaultCustomThemeColor
  const rgb = hexToRgb(accent)
  const hover = mixHex(accent, '#000000', 0.16)
  const dark = mixHex(accent, '#ffffff', 0.32)

  return {
    '--sf-accent': accent,
    '--sf-accent-hover': hover,
    '--sf-accent-strong': strongAccentColor(accent),
    '--sf-accent-soft': mixHex(accent, '#ffffff', 0.92),
    '--sf-accent-soft-border': mixHex(accent, '#ffffff', 0.68),
    '--sf-accent-dark': dark,
    '--sf-accent-rgb': `${rgb.r} ${rgb.g} ${rgb.b}`,
    '--sf-accent-contrast': accentContrastColor(accent),
    '--sf-primary-50': mixHex(accent, '#ffffff', 0.94),
    '--sf-primary-100': mixHex(accent, '#ffffff', 0.88),
    '--sf-primary-200': mixHex(accent, '#ffffff', 0.72),
    '--sf-primary-300': mixHex(accent, '#ffffff', 0.52),
    '--sf-primary-400': mixHex(accent, '#ffffff', 0.28),
    '--sf-primary-500': accent,
    '--sf-primary-600': accent,
    '--sf-primary-700': hover,
    '--sf-primary-800': mixHex(accent, '#000000', 0.3),
    '--sf-primary-900': mixHex(accent, '#000000', 0.45),
    '--sf-primary-950': mixHex(accent, '#000000', 0.62),
    // 成功 Toast 跟随站点强调色，避免回落为 Nuxt UI 默认绿色。
    '--ui-color-success-50': 'var(--sf-primary-50)',
    '--ui-color-success-100': 'var(--sf-primary-100)',
    '--ui-color-success-200': 'var(--sf-primary-200)',
    '--ui-color-success-300': 'var(--sf-primary-300)',
    '--ui-color-success-400': 'var(--sf-primary-400)',
    '--ui-color-success-500': 'var(--sf-primary-500)',
    '--ui-color-success-600': 'var(--sf-primary-600)',
    '--ui-color-success-700': 'var(--sf-primary-700)',
    '--ui-color-success-800': 'var(--sf-primary-800)',
    '--ui-color-success-900': 'var(--sf-primary-900)',
    '--ui-color-success-950': 'var(--sf-primary-950)'
  }
}

function cssVarsToStyle(vars: Record<string, string>) {
  return Object.entries(vars).map(([name, value]) => `${name}: ${value}`).join('; ')
}

type RGB = { r: number, g: number, b: number }

function hexToRgb(hex: string): RGB {
  const value = (normalizeHexColor(hex) || defaultCustomThemeColor).slice(1)
  return {
    r: Number.parseInt(value.slice(0, 2), 16),
    g: Number.parseInt(value.slice(2, 4), 16),
    b: Number.parseInt(value.slice(4, 6), 16)
  }
}

function mixHex(from: string, to: string, amount: number) {
  const start = hexToRgb(from)
  const end = hexToRgb(to)
  const mix = (a: number, b: number) => Math.round(a + (b - a) * amount)
  return rgbToHex({ r: mix(start.r, end.r), g: mix(start.g, end.g), b: mix(start.b, end.b) })
}

function rgbToHex(rgb: RGB) {
  return `#${[rgb.r, rgb.g, rgb.b].map(value => value.toString(16).padStart(2, '0')).join('')}`
}

const INK_HEX = '#111827'
const INK: RGB = { r: 0x11, g: 0x18, b: 0x27 }
const WHITE: RGB = { r: 255, g: 255, b: 255 }

/** WCAG 对比度（(L1+0.05)/(L2+0.05)），用于挑选强调色上的前景。 */
function contrastRatio(a: RGB, b: RGB) {
  const first = relativeLuminance(a)
  const second = relativeLuminance(b)
  const lighter = Math.max(first, second)
  const darker = Math.min(first, second)
  return (lighter + 0.05) / (darker + 0.05)
}

function relativeLuminance(rgb: RGB) {
  const channel = (value: number) => {
    const normalized = value / 255
    return normalized <= 0.03928 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(rgb.r) + 0.7152 * channel(rgb.g) + 0.0722 * channel(rgb.b)
}
