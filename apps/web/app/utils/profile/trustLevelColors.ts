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

/** 后端白名单校验：任何来源（公开读面 / 表单 / 用户输入）的色名都先过这一关。 */
export function isTierColorToken(value: unknown): value is TierColorToken {
  return typeof value === 'string' && TIER_COLOR_SET.has(value)
}

/**
 * 白名单 → 规范化色名：命中返回原名，否则 `null`。
 * 「不认识的颜色」与「没配色」对 UI 是同一件事（按位次取默认），所以这里不抛错也不猜。
 */
export function normalizeTierColor(value: unknown): TierColorToken | null {
  return isTierColorToken(value) ? value : null
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
): TierColorToken {
  const stored = normalizeTierColor(tier?.color)
  return stored ?? defaultTierColorName(rank)
}

/** 色名 → 可渲染的 CSS 变量引用（`--sf-tl-*` 在 `sforum-trust-colors.css` 里按明暗各定义一套）。 */
export function tierColorVar(token: string | null | undefined): string {
  return `var(--sf-tl-${normalizeTierColor(token) ?? 'slate'})`
}

/**
 * 行级 `--tl-accent` 内联样式。白名单外的 token 回退 slate：
 * 写一个不存在的变量会让 `color-mix()` 整条失效，色条/图标块会直接掉成透明。
 */
export function tierAccentStyle(token: string | null | undefined): Record<string, string> {
  return { '--tl-accent': tierColorVar(token) }
}
