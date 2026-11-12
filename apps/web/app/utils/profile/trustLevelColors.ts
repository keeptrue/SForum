/**
 * 兼容层：档位配色的实现已迁到 `~/utils/theme/tierColor.ts`（徽章等级与信任等级共用同一套
 * 色值域、CSS 变量映射与对比度公式）。
 *
 * 这里**只做 re-export**，不留第二份实现：
 * - 信任等级的组件、composable、既有测试仍从本路径 import，行为不变；
 * - 新增代码请直接 import `~/utils/theme/tierColor`，别让兼容层继续扩散。
 *
 * 信任等级外观（`components/admin/forum/trust-levels/**`、`assets/css/sforum-trust-colors.css`）
 * 不在本次改动范围内：它们的逻辑与样式一行都没动，只是实现文件的落点变了。
 */
export {
  TIER_COLOR_TOKENS,
  TIER_CUSTOM_COLOR_PATTERN,
  TIER_DARK_ACCENT_MIX,
  TIER_DARK_PREVIEW_SURFACE,
  TIER_MIN_DARK_CONTRAST,
  defaultTierColorName,
  isTierColorToken,
  isTierColorValue,
  isTierCustomColor,
  normalizeTierColor,
  normalizeTierColorInput,
  tierAccentContrast,
  tierAccentDarkHex,
  tierAccentPreview,
  tierAccentStyle,
  tierAccentVars,
  tierColorOf,
  tierColorVar
} from '~/utils/theme/tierColor'

export type {
  TierAccentPreview,
  TierAccentPreviewSide,
  TierColorToken
} from '~/utils/theme/tierColor'
