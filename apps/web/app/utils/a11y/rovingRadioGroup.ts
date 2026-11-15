/**
 * radiogroup 的 roving tabindex 计算（纯函数，方便单测）。
 *
 * 规则按 WAI-ARIA 的 radio group 模式：
 * - 整组只有**一个** tab 停靠点：选中项；没有选中项时退回第一项；
 * - ←/↑ 与 →/↓ 循环移动，Home/End 到两端；
 * - Enter/Space 由原生 button 的 click 负责，这里不拦。
 */

export function radioGroupTabIndex(values: readonly string[], selected: string | null | undefined, value: string): number {
  if (values.length === 0) return -1
  const stop = selected && values.includes(selected) ? selected : values[0]
  return stop === value ? 0 : -1
}

export function radioGroupNextIndex(count: number, index: number, key: string): number | null {
  if (count <= 0) return null
  const safeIndex = index >= 0 && index < count ? index : 0
  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return (safeIndex + 1) % count
    case 'ArrowLeft':
    case 'ArrowUp':
      return (safeIndex - 1 + count) % count
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return null
  }
}
