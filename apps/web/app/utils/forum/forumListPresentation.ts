/**
 * 列表呈现用稳定哈希与色板。
 * 主题通过 CSS 变量换肤；宿主组件用 Tailwind 消费 token，不再双写 BEM。
 */

/**
 * demo fullwidth-3col .a1–.a8 的稳定色板。
 *
 * 每一项都是**背景 + 前景成对**的：首字母必须与色板同时达标（WCAG AA 4.5:1）。
 * 前景统一保留 `text-white`（头像的观感基线：品牌色底 + 白首字母），达标方式是
 * **压深底色**而不是改字色——8 档底色对白字实测 4.85–5.55:1，全部过 AA。
 * 上一轮为达标把前景换成墨黑 `#111827`（后台头像白字→灰黑字），观感偏离设计，本轮改回。
 * 注意「成对」只能由本表提供：`SFAvatar.vue` 的公共 class 里不能再写死 `text-white`，
 * 否则第二处前景会盖掉这里的配对。对比度由 `tests/a11y/contrastTokens.test.ts` 逐对钉住。
 */
export const FORUM_AVATAR_TONE_CLASSES = [
  'bg-[#4263eb] text-white',
  'bg-[#087f5b] text-white',
  'bg-[#a16207] text-white',
  'bg-[#ae3ec9] text-white',
  'bg-[#1971c2] text-white',
  'bg-[#c92a2a] text-white',
  'bg-[#7048e8] text-white',
  'bg-[#2a8038] text-white'
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
