/**
 * 列表呈现用稳定哈希与色板。
 * 主题通过 CSS 变量换肤；宿主组件用 Tailwind 消费 token，不再双写 BEM。
 */

/**
 * demo fullwidth-3col .a1–.a8 的稳定色板。
 *
 * 每一项都是**背景 + 前景成对**的：首字母必须与色板同时达标（WCAG AA 4.5:1）。
 * 旧实现把 `text-white` 写死在 SFAvatar 的公共 class 里，8 个色板里 7 个只有
 * 1.86–4.26:1（axe `color-contrast` 在后台头像与公开页同时报）。浅/中亮度色板
 * 改用墨黑 `#111827`，深紫换成 `#7048e8`（原 `#845ef7` 与黑、白都不达标）。
 * 对比度由 `tests/a11y/avatarToneContrast.test.ts` 逐对钉住。
 */
export const FORUM_AVATAR_TONE_CLASSES = [
  'bg-[#5c7cfa] text-[#111827]',
  'bg-[#20c997] text-[#111827]',
  'bg-[#fab005] text-[#111827]',
  'bg-[#cc5de8] text-[#111827]',
  'bg-[#339af0] text-[#111827]',
  'bg-[#ff6b6b] text-[#111827]',
  'bg-[#7048e8] text-white',
  'bg-[#51cf66] text-[#111827]'
] as const

/**
 * hybrid demo variant B .topic-category.tone-*
 *
 * 三档原来都没过 AA：橙 `#e1761d` on `#fff4e8` = 2.84:1（axe 实测，首页胶囊 12 节点）、
 * 绿 3.59:1、红 3.68:1。压深到 #a34a00 / #1b7a3d / #b02a2a（4.9–5.9:1），色相不变。
 * 四对由 `tests/a11y/contrastTokens.test.ts` 一起钉住。
 */
export const FORUM_CATEGORY_CHIP_TONE_CLASSES = [
  'bg-[#eaf2ff] text-[#1467d9]',
  'bg-[#edf8ef] text-[#1b7a3d]',
  'bg-[#fff0f0] text-[#b02a2a]',
  'bg-[#fff4e8] text-[#a34a00]'
] as const

export function hashStableTone(seed: string, modulo: number): number {
  let h = 0
  const source = seed || 'x'
  for (let i = 0; i < source.length; i += 1) {
    h = (h * 31 + source.charCodeAt(i)) >>> 0
  }
  return modulo > 0 ? h % modulo : 0
}

export function forumAvatarToneClass(seed: string): string {
  const index = hashStableTone(seed, FORUM_AVATAR_TONE_CLASSES.length)
  return FORUM_AVATAR_TONE_CLASSES[index] || FORUM_AVATAR_TONE_CLASSES[0]
}

export function forumCategoryChipToneClass(seed: string): string {
  const index = hashStableTone(seed, FORUM_CATEGORY_CHIP_TONE_CLASSES.length)
  return FORUM_CATEGORY_CHIP_TONE_CLASSES[index] || FORUM_CATEGORY_CHIP_TONE_CLASSES[0]
}

/** 扩展列表徽章 tone → Tailwind */
export const FORUM_LIST_BADGE_TONE_CLASSES: Record<string, string> = {
  neutral: 'bg-slate-100 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300',
  info: 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300',
  success: 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300',
  warning: 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300',
  danger: 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
}

export function forumListBadgeToneClass(tone?: string): string {
  const key = tone && tone in FORUM_LIST_BADGE_TONE_CLASSES ? tone : 'neutral'
  return FORUM_LIST_BADGE_TONE_CLASSES[key] ?? FORUM_LIST_BADGE_TONE_CLASSES.neutral ?? 'bg-slate-100 text-slate-600'
}
