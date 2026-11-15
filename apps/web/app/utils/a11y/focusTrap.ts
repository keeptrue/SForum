/**
 * 自绘弹层的焦点陷阱。
 *
 * 只负责「dialog 语义之外的四个行为」：初始焦点、Tab/Shift+Tab 循环、Esc、关闭后焦点归还。
 * 角色/`aria-modal`/`aria-labelledby` 仍由调用方模板声明，这里不碰 DOM 语义。
 *
 * 不用 `focus-trap` 包：本项目只有两个自绘弹层（举报对话框、移动端公共侧栏抽屉），
 * 两者都需要「初始焦点落在容器本身（读屏先读标题）」而不是第一个可聚焦元素，
 * 依赖包的 autofocus 约定反而要绕。
 *
 * 可聚焦判定不看 `offsetParent` / `getClientRects()`：jsdom 没有布局，量出来恒为空，
 * 那样单测会把所有元素都判成不可见，等于没测。
 */

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])'
].join(',')

/**
 * 布局引擎是否可用。
 *
 * jsdom / happy-dom 没有布局：`getClientRects()` 恒为空，照它过滤会把所有元素判成
 * 不可见，单测等于没测。用 body 的 rect 探测一次：真实浏览器里 body 恒有 rect
 * （整页隐藏除外），测试环境恒为 0，据此优雅退化。
 */
function layoutIsAvailable(): boolean {
  if (typeof document === 'undefined') return false
  const body = document.body
  if (!body) return false
  return body.getClientRects().length > 0
}

/**
 * 容器内按 Tab 顺序可聚焦的元素。
 *
 * `[hidden]` 与 `aria-hidden="true"` 的整棵子树都要排除（不是只看元素自己）：
 * 抽屉里「桌面端才有」的内容就是靠 hidden 收起来的，把焦点送进去等于把键盘用户
 * 丢进看不见的地方。
 *
 * CSS 隐藏（`display: none` / 0×0 折叠）的元素同样要排除：它们能匹配选择器，
 * 但 `focus()` 不会生效。留着当 Tab 边界会死锁——末尾按 Tab 被 preventDefault 了，
 * 焦点却停在原地，键盘用户彻底走不动（桌面宽度下的抽屉头部关闭按钮就是这种）。
 */
export function focusableWithin(container: HTMLElement): HTMLElement[] {
  const layout = layoutIsAvailable()
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    .filter(element => element.closest('[hidden], [aria-hidden="true"]') === null)
    .filter(element => !layout || element.getClientRects().length > 0)
}

export type FocusTrapOptions = {
  /** 弹层根元素：用 getter，`v-if` 渲染时创建陷阱的那一刻容器还不存在。 */
  container: () => HTMLElement | null
  /** Esc 的处理者（通常是 emit('close')）。 */
  onEscape?: () => void
  /** 初始焦点；缺省取容器内第一个可聚焦元素。 */
  resolveInitialFocus?: (container: HTMLElement) => HTMLElement | null | undefined
}

export type FocusTrap = {
  activate: () => void
  deactivate: (options?: { restoreFocus?: boolean }) => void
  isActive: () => boolean
}

export function createFocusTrap(options: FocusTrapOptions): FocusTrap {
  let active = false
  let previouslyFocused: HTMLElement | null = null

  function focusElement(element: HTMLElement | null | undefined) {
    element?.focus()
  }

  function handleKeydown(event: KeyboardEvent) {
    const container = options.container()
    if (!container) return

    if (event.key === 'Escape') {
      event.preventDefault()
      options.onEscape?.()
      return
    }

    if (event.key !== 'Tab') return

    const focusable = focusableWithin(container)
    if (focusable.length === 0) {
      // 没有可聚焦元素时把 Tab 留在弹层里，不让焦点穿到底层页面。
      event.preventDefault()
      focusElement(container)
      return
    }

    const first = focusable[0]!
    const last = focusable[focusable.length - 1]!
    const current = document.activeElement as HTMLElement | null

    if (!current || !container.contains(current)) {
      event.preventDefault()
      focusElement(event.shiftKey ? last : first)
      return
    }

    if (event.shiftKey && (current === first || current === container)) {
      event.preventDefault()
      focusElement(last)
      return
    }

    if (!event.shiftKey && current === last) {
      event.preventDefault()
      focusElement(first)
    }
  }

  return {
    isActive: () => active,
    activate() {
      if (active) return
      active = true
      previouslyFocused = document.activeElement as HTMLElement | null
      document.addEventListener('keydown', handleKeydown, true)
      const container = options.container()
      if (!container) return
      const initial = options.resolveInitialFocus
        ? options.resolveInitialFocus(container)
        : focusableWithin(container)[0]
      focusElement(initial)
    },
    deactivate(deactivateOptions = {}) {
      if (!active) return
      active = false
      document.removeEventListener('keydown', handleKeydown, true)
      const restore = deactivateOptions.restoreFocus !== false
      const target = previouslyFocused
      previouslyFocused = null
      // 触发元素可能已经随弹层一起被移出 DOM：`isConnected` 之后才归还焦点。
      if (restore && target?.isConnected) target.focus()
    }
  }
}
