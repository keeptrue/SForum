/**
 * 档位配色（D-C1 / D-C5）：每个档位**默认就有颜色**，运营可按档覆盖。
 *
 * 三件事只在这里定义，别处不许再写一份：
 * 1. **色序**：`TIER_COLOR_TOKENS` 的顺序就是默认配色顺序，`index = rank`（TL0 = slate）。
 *    **9 色**，不是 8：`slate` 被虚拟 TL0 占掉，而 active 档最多 8 档（`MaxTiers=8`），
 *    只备 8 色会让满配时的 TL8 循环回 `slate`，与 TL0 撞色 —— 阶梯最后一段反而看不出是几档。
 *    `amber` 是终点金（最高档），`fuchsia` 插在 `violet` 与 `rose` 之间。
 * 2. **有效色**：`tierColorOf` 是「存储值命中白名单就用它，否则按位次取默认」的唯一实现，
 *    与 `trustLevelIconOf` 同策略 —— 未配置的档位也有颜色，零配置就有可读的阶梯。
 * 3. **CSS 变量名**：`--sf-tl-<token>`（取值在 `assets/css/sforum-trust-colors.css`），
 *    `tierAccentStyle` 把它落到 `--tl-accent`，组件只认这一个变量。
 *
 * **必须与后端 `apps/api/app/Models/TrustLevels/tier_colors.go` 的 `var tierColors = []string{...}`
 * 逐字同序**：顺序也是语义（默认配色 = 位次），漂移的表现是「后台预览的色和前台看到的色不是同一个」，
 * 而这种偏差平时看不见。`tests/admin/forum/trustLevelNameFallback.test.ts` 会直接读那个 Go 文件比对，
 * 改那边就要改这里。
 *
 * **两种取值形态**：推荐色板是 9 个 token；运营商也可以填一个自定义 `#rrggbb`
 * （`TIER_CUSTOM_COLOR_PATTERN`）。两者的差别不在「能不能渲染」，而在**明暗两套页面**：
 * token 有明暗两组取值（CSS 文件里定义），自定义色只有一个值，深色页面靠
 * `color-mix(in srgb, <hex> 74%, white)` 现算提亮。这条公式同时以 JS（`tierAccentDarkHex`，
 * 供对比度提示）与 CSS（`tierAccentStyle`，供实际渲染）两种形式存在，**改一处必须改另一处**。
 */
export const TIER_COLOR_TOKENS = [
  'slate',
  'teal',
  'emerald',
  'sky',
  'indigo',
  'violet',
  'fuchsia',
  'rose',
  'amber'
] as const

export type TierColorToken = typeof TIER_COLOR_TOKENS[number]

const TIER_COLOR_SET: ReadonlySet<string> = new Set(TIER_COLOR_TOKENS)

/** 自定义色只接受六位 `#rrggbb`（小写归一）；`#RGB` 短写法由输入组件先展开。 */
export const TIER_CUSTOM_COLOR_PATTERN = /^#[0-9a-f]{6}$/

/** 后端白名单校验：任何来源（公开读面 / 表单 / 用户输入）的色名都先过这一关。 */
export function isTierColorToken(value: unknown): value is TierColorToken {
  return typeof value === 'string' && TIER_COLOR_SET.has(value)
}

/** 自定义 hex（大小写不敏感）：命中返回 `true`，值本身不做归一（归一在 `normalizeTierColor`）。 */
export function isTierCustomColor(value: unknown): value is string {
  return typeof value === 'string' && TIER_CUSTOM_COLOR_PATTERN.test(value.trim().toLowerCase())
}

/** 合法的档位色 = 推荐 token 或自定义 hex。写表单前的最后一道形状闸门。 */
export function isTierColorValue(value: unknown): value is string {
  return isTierColorToken(value) || isTierCustomColor(value)
}

/**
 * 归一化：token 原名返回、hex 转小写返回、其它一律 `null`。
 * 「不认识的颜色」与「没配色」对 UI 是同一件事（按位次取默认），所以这里不抛错也不猜。
 */
export function normalizeTierColor(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const trimmed = value.trim().toLowerCase()
  if (isTierColorToken(trimmed)) return trimmed
  if (isTierCustomColor(trimmed)) return trimmed
  return null
}

/** 按位次取默认色（rank 0 = TL0 = slate；超出 9 个 token 后循环取）。 */
export function defaultTierColorName(rank: number): TierColorToken {
  const index = Math.max(0, Math.trunc(Number(rank) || 0))
  return TIER_COLOR_TOKENS[index % TIER_COLOR_TOKENS.length]!
}

/**
 * 档位的**有效色**：存储值命中白名单优先，否则按 rank 取默认。
 *
 * 停用档位没有 rank，调用方传数组位置（与 `trustLevelIconOf` 同一策略）：
 * 停用档不参与判定，颜色只是「它在列表里长什么样」。
 */
export function tierColorOf(
  tier: { color?: string | null } | null | undefined,
  rank: number
): string {
  const stored = normalizeTierColor(tier?.color)
  return stored ?? defaultTierColorName(rank)
}

/** 色值 → 可渲染的 CSS 变量引用（`--sf-tl-*` 在 `sforum-trust-colors.css` 里按明暗各定义一套）。 */
export function tierColorVar(value: string | null | undefined): string {
  const normalized = normalizeTierColor(value) ?? 'slate'
  return isTierCustomColor(normalized) ? normalized : `var(--sf-tl-${normalized})`
}

/**
 * 行级 `--tl-accent` 内联样式。白名单外的 token 回退 slate：
 * 写一个不存在的变量会让 `color-mix()` 整条失效，色条/图标块会直接掉成透明。
 *
 * token 路径只有一个 `--tl-accent`（明暗取值由 CSS 变量自己随主题切换）；
 * hex 路径没有「第二组取值」，所以内联**明/暗两个**变量，由 `sforum-trust-colors.css` 的
 * `[data-sf-tl-accent]` 规则在当前配色下挑一个 —— 内联 `--tl-accent` 优先级更高，
 * token 档不会受那两条规则影响。
 */
export function tierAccentStyle(value: string | null | undefined): Record<string, string> {
  const normalized = normalizeTierColor(value)
  if (normalized && isTierCustomColor(normalized)) {
    return {
      '--tl-accent-light': normalized,
      '--tl-accent-dark': `color-mix(in srgb, ${normalized} 74%, white)`
    }
  }
  return { '--tl-accent': tierColorVar(normalized) }
}

/** 预览深色卡面的固定底色（与公开页实测深色卡面同一值，不跟后台主题走）。 */
export const TIER_DARK_PREVIEW_SURFACE = '#1b201f'

/** 深色页面提亮比例：`color-mix(in srgb, <hex> 74%, white)`。 */
export const TIER_DARK_ACCENT_MIX = 0.74

/** 深色卡面下识别色的最低可接受对比度（WCAG 非文本 3:1）。 */
export const TIER_MIN_DARK_CONTRAST = 3

/**
 * 双底预览的一侧：`scope` 交给组件绑 `data-sf-tl-scope`（CSS 据此选明/暗 token 取值），
 * `style` 是这一侧该内联的 `--tl-accent`。组件**不做**任何「当前是否暗色」判断。
 */
export type TierAccentPreviewSide = { scope: 'light' | 'dark', style: Record<string, string> }

export type TierAccentPreview = { light: TierAccentPreviewSide, dark: TierAccentPreviewSide }

/**
 * 同页并排的两套模式预览（小设 spec §4）。
 *
 * token 色在明暗两侧都是同一个 `var(--sf-tl-<token>)`：取值由卡片的 `data-sf-tl-scope`
 * 决定（`sforum-trust-colors.css` 里 `[data-sf-tl-scope="dark"]` 把 token 映射到深色字面量），
 * 这样暗色后台里的「浅底卡」也能拿到真正的浅色，而不是继承 html.dark 的那套。
 *
 * hex 没有第二组取值，所以两侧都直接写字面量：明卡原色，暗卡是提亮后的 `color-mix()`。
 */
export function tierAccentPreview(
  value: string | null | undefined,
  rank: number
): TierAccentPreview {
  const normalized = normalizeTierColor(value) ?? defaultTierColorName(rank)
  if (isTierCustomColor(normalized)) {
    return {
      light: { scope: 'light', style: { '--tl-accent': normalized } },
      dark: {
        scope: 'dark',
        style: { '--tl-accent': `color-mix(in srgb, ${normalized} ${TIER_DARK_ACCENT_MIX * 100}%, white)` }
      }
    }
  }
  const tokenStyle = (): Record<string, string> => ({ '--tl-accent': `var(--sf-tl-${normalized})` })
  return {
    light: { scope: 'light', style: tokenStyle() },
    dark: { scope: 'dark', style: tokenStyle() }
  }
}

function parseHexRgb(hex: string | null | undefined): [number, number, number] | null {
  const normalized = normalizeTierColor(hex)
  if (!normalized || !isTierCustomColor(normalized)) return null
  const value = Number.parseInt(normalized.slice(1), 16)
  return [(value >> 16) & 0xff, (value >> 8) & 0xff, value & 0xff]
}

function mixTowardWhite(channel: number): number {
  return Math.round(channel * TIER_DARK_ACCENT_MIX + 255 * (1 - TIER_DARK_ACCENT_MIX))
}

function linearize(channel: number): number {
  const ratio = channel / 255
  return ratio <= 0.03928 ? ratio / 12.92 : ((ratio + 0.055) / 1.055) ** 2.4
}

function relativeLuminance(red: number, green: number, blue: number): number {
  return 0.2126 * linearize(red) + 0.7152 * linearize(green) + 0.0722 * linearize(blue)
}

/** 自定义 hex 在深色页面上的实际取值（提亮后）；非 hex 返回 `null`。 */
export function tierAccentDarkHex(value: string | null | undefined): string | null {
  const rgb = parseHexRgb(value)
  if (!rgb) return null
  const mixed = rgb.map(mixTowardWhite)
  return `#${mixed.map(channel => channel.toString(16).padStart(2, '0')).join('')}`
}

/**
 * 自定义 hex 在深色卡面上的对比度（WCAG 2.x，1–21）。
 *
 * 只对自定义 hex 有定义：token 的深色取值住在 CSS 里，JS 侧不抄第二份 ——
 * 抄了就会在调整色板时悄悄失真。返回 `null` = 「无法判断」，调用方不要当 0 用。
 */
export function tierAccentContrast(value: string | null | undefined): number | null {
  const rgb = parseHexRgb(value)
  if (!rgb) return null
  const surface = parseHexRgb(TIER_DARK_PREVIEW_SURFACE)
  if (!surface) return null
  const accentLuminance = relativeLuminance(mixTowardWhite(rgb[0]), mixTowardWhite(rgb[1]), mixTowardWhite(rgb[2]))
  const surfaceLuminance = relativeLuminance(surface[0], surface[1], surface[2])
  const lighter = Math.max(accentLuminance, surfaceLuminance)
  const darker = Math.min(accentLuminance, surfaceLuminance)
  return (lighter + 0.05) / (darker + 0.05)
}
