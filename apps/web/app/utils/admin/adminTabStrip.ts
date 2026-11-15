// 后台多页签条的纯逻辑：键盘导航落点与页签 DOM id 规则。
// 抽成独立模块是为了让「←/→ 循环、Home/End」这类行为可以被单测，
// 不必挂载整个后台布局（布局依赖一堆 Nuxt composable 与异步 data）。

export const ADMIN_TAB_ELEMENT_ID_PREFIX = 'sforum-admin-tab'

/** 页签按钮的 DOM id；tabpanel 的 aria-labelledby 指向它。 */
export function adminTabElementId(tabId: string): string {
  return `${ADMIN_TAB_ELEMENT_ID_PREFIX}-${tabId}`
}

/**
 * 键盘导航应该激活的页签下标；不是导航键（或没有页签）时返回 null。
 * 越界的 current 收敛到第 0 项，保证焦点永远落在存在的页签上。
 */
export function nextTabIndex(current: number, length: number, key: string): number | null {
  if (length <= 0) {
    return null
  }

  const safe = current >= 0 && current < length ? current : 0
  switch (key) {
    case 'ArrowRight':
      return (safe + 1) % length
    case 'ArrowLeft':
      return (safe - 1 + length) % length
    case 'Home':
      return 0
    case 'End':
      return length - 1
    default:
      return null
  }
}
