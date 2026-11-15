import { afterEach, describe, expect, test } from 'bun:test'
import { createFocusTrap, focusableWithin } from '../../app/utils/a11y/focusTrap'

/**
 * 自绘弹层的焦点契约（RV-23 / RV-24 的共用底层）。
 *
 * 这里测的是「Tab 会不会穿到底层页面」「Esc 归谁管」「关闭后焦点回哪」这三件事，
 * 组件级用例（举报弹窗、公共侧栏抽屉）只验证自己接线正确。
 */

let cleanups: Array<() => void> = []

function mountFixture(html: string) {
  const host = document.createElement('div')
  host.innerHTML = html
  document.body.appendChild(host)
  cleanups.push(() => host.remove())
  return host
}

afterEach(() => {
  for (const cleanup of cleanups) cleanup()
  cleanups = []
  document.body.innerHTML = ''
})

describe('focusableWithin', () => {
  test('只收 Tab 顺序上的元素，跳过 disabled / aria-hidden / hidden / tabindex=-1', () => {
    const host = mountFixture(`
      <button id="a">a</button>
      <button id="b" disabled>b</button>
      <a id="c" href="#x">c</a>
      <input id="d" type="hidden" />
      <input id="e" />
      <div id="f" tabindex="-1">f</div>
      <div id="g" tabindex="0">g</div>
      <div id="h" aria-hidden="true"><button id="h-child">h</button></div>
      <div id="i" hidden><button id="i-child">i</button></div>
    `)

    expect(focusableWithin(host).map(element => element.id)).toEqual(['a', 'c', 'e', 'g'])
  })
})

describe('focusableWithin 在真实布局下过滤 CSS 隐藏元素', () => {
  /**
   * 模拟布局引擎：happy-dom 的元素是 Proxy，`delete` 删不掉已定义的属性，
   * 所以「无布局」也显式重定义成空列表，并在 afterEach 复位，避免污染别的测试文件。
   */
  function setBodyLayout(enabled: boolean) {
    Object.defineProperty(document.body, 'getClientRects', {
      configurable: true,
      value: () => (enabled ? [{ width: 1, height: 1 }] : [])
    })
  }

  afterEach(() => setBodyLayout(false))

  function withLayout(hiddenIds: string[]) {
    setBodyLayout(true)
    const host = mountFixture(`
      <button id="visible">v</button>
      <header><button id="hidden-header">x</button></header>
      <a id="link" href="#y">y</a>
    `)
    for (const id of hiddenIds) {
      Object.defineProperty(host.querySelector(`#${id}`)!, 'getClientRects', { configurable: true, value: () => [] })
    }
    return host
  }

  test('0×0 的元素（display:none 折叠）不算 Tab 边界，避免 preventDefault 后焦点不动', () => {
    const host = withLayout(['hidden-header'])
    expect(focusableWithin(host).map(element => element.id)).toEqual(['visible', 'link'])
  })

  test('没有布局引擎时（jsdom/happy-dom）不按可见性过滤，单测不会假红', () => {
    setBodyLayout(false)
    const host = mountFixture('<button id="a">a</button><header><button id="b">b</button></header>')
    Object.defineProperty(host.querySelector('#b')!, 'getClientRects', { configurable: true, value: () => [] })
    expect(focusableWithin(host).map(element => element.id)).toEqual(['a', 'b'])
  })
})

describe('createFocusTrap', () => {
  function setup(options: { onEscape?: () => void } = {}) {
    const trigger = document.createElement('button')
    trigger.id = 'trigger'
    document.body.appendChild(trigger)
    cleanups.push(() => trigger.remove())

    const dialog = document.createElement('section')
    dialog.id = 'dialog'
    dialog.tabIndex = -1
    dialog.innerHTML = '<button id="first">1</button><button id="last">2</button>'
    document.body.appendChild(dialog)
    cleanups.push(() => dialog.remove())

    trigger.focus()
    // 两个调用点（举报弹窗、侧栏抽屉）都把初始焦点放在容器本身：读屏先读 dialog 名称。
    const trap = createFocusTrap({
      container: () => dialog,
      onEscape: options.onEscape,
      resolveInitialFocus: container => container
    })
    // 陷阱是 document 级监听：用例之间必须拆掉，否则上一个用例的陷阱会拦下一个用例的 Tab。
    cleanups.push(() => trap.deactivate({ restoreFocus: false }))
    return { trigger, dialog, trap }
  }

  function tab(shiftKey = false) {
    const event = new KeyboardEvent('keydown', { key: 'Tab', shiftKey, bubbles: true, cancelable: true })
    document.dispatchEvent(event)
    return event
  }

  test('激活时把初始焦点放到容器，关闭时归还给触发元素', () => {
    const { trigger, dialog, trap } = setup()

    trap.activate()
    expect(document.activeElement).toBe(dialog)

    trap.deactivate()
    expect(document.activeElement).toBe(trigger)
  })

  test('Tab 在首尾之间循环，不会穿到底层页面', () => {
    const { dialog, trap } = setup()
    trap.activate()

    const first = dialog.querySelector<HTMLElement>('#first')!
    const last = dialog.querySelector<HTMLElement>('#last')!

    // 末尾按 Tab → 回到第一个
    last.focus()
    expect(tab().defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first)

    // 第一个按 Shift+Tab → 跳到最后一个
    first.focus()
    expect(tab(true).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(last)
  })

  test('焦点被移到弹层外时，Tab 会把焦点拉回弹层内', () => {
    const { trigger, dialog, trap } = setup()
    trap.activate()

    // 模拟「点了一下底层页面」：焦点跑到容器外
    trigger.focus()
    expect(tab().defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(dialog.querySelector('#first'))
  })

  test('Esc 交给 onEscape，并且不再向页面继续冒泡', () => {
    let escapes = 0
    const { dialog, trap } = setup({ onEscape: () => { escapes += 1 } })
    trap.activate()
    dialog.focus()

    const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.dispatchEvent(event)

    expect(escapes).toBe(1)
    expect(event.defaultPrevented).toBe(true)
  })

  test('deactivate 之后 Tab 不再被拦截', () => {
    const { trap } = setup()
    trap.activate()
    trap.deactivate()

    const event = tab()
    expect(event.defaultPrevented).toBe(false)
  })
})
