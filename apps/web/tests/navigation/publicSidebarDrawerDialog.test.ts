import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { flushPromises, mount } from '../helpers/vueSfc'
import { createFocusTrap } from '../../app/utils/a11y/focusTrap'
import { nextTick, ref } from 'vue'

/**
 * 移动端公共侧栏抽屉的 dialog 语义（RV-24）。
 *
 * 这个组件同时是「桌面端普通侧栏」和「移动端抽屉」：关闭态绝不能带 dialog 语义，
 * 打开态必须有 role/aria-modal/名称、初始焦点、Tab 陷阱、Esc 关闭与焦点归还。
 */

const globals = globalThis as Record<string, unknown>
const originalUseI18n = globals.useI18n
globals.useI18n = () => ({ t: (key: string) => key })

afterAll(() => {
  globals.useI18n = originalUseI18n
})

const componentPath = fileURLToPath(
  new URL('../../app/components/forum/navigation/SFResponsivePublicSidebar.vue', import.meta.url)
)

function createDrawerStub() {
  const open = ref(false)
  return {
    open,
    claimOwner: () => {},
    closeDrawer: () => { open.value = false },
    releaseOwner: () => {}
  }
}

const drawer = createDrawerStub()
const Sidebar = await compileSidebar()

async function compileSidebar() {
  const { compileVueSfc } = await import('../helpers/vueSfc')
  return compileVueSfc(componentPath, 'sf-responsive-public-sidebar', {
    usePublicSidebarDrawer: () => drawer,
    createFocusTrap
  })
}

const UIconStub = { props: ['name'], template: '<span class="icon" />' }

async function mountSidebar() {
  const trigger = document.createElement('button')
  trigger.textContent = 'menu'
  document.body.appendChild(trigger)
  trigger.focus()

  drawer.open.value = false
  const wrapper = mount(Sidebar, {
    attachTo: document.body,
    props: { ownerId: 'public.sidebar.primary', title: '导航' },
    slots: {
      default: '<a href="#a">首页</a><a href="#b">话题</a>'
    },
    global: { stubs: { UIcon: UIconStub } }
  })
  await flushPromises()
  return { wrapper, trigger }
}

function surface(): HTMLElement {
  return document.querySelector<HTMLElement>('.sf-responsive-public-sidebar__surface')!
}

function pressKey(key: string, shiftKey = false) {
  const event = new KeyboardEvent('keydown', { key, shiftKey, bubbles: true, cancelable: true })
  document.dispatchEvent(event)
  return event
}

describe('SFResponsivePublicSidebar dialog semantics', () => {
  test('关闭态是普通侧栏，不带 dialog 语义', async () => {
    const { wrapper, trigger } = await mountSidebar()
    const element = surface()

    expect(element.getAttribute('role')).toBeNull()
    expect(element.getAttribute('aria-modal')).toBeNull()
    expect(element.getAttribute('tabindex')).toBeNull()

    wrapper.unmount()
    trigger.remove()
  })

  test('打开态是模态 dialog：名称、初始焦点都在抽屉上', async () => {
    const { wrapper, trigger } = await mountSidebar()
    drawer.open.value = true
    await flushPromises()
    await nextTick()

    const element = surface()
    expect(element.getAttribute('role')).toBe('dialog')
    expect(element.getAttribute('aria-modal')).toBe('true')
    expect(element.getAttribute('aria-label')).toBe('导航')
    expect(element.getAttribute('tabindex')).toBe('-1')
    expect(document.activeElement).toBe(element)

    wrapper.unmount()
    trigger.remove()
  })

  test('Esc 关闭抽屉，并把焦点还给汉堡按钮', async () => {
    const { wrapper, trigger } = await mountSidebar()
    drawer.open.value = true
    await flushPromises()
    await nextTick()

    pressKey('Escape')
    await flushPromises()
    await nextTick()

    expect(drawer.open.value).toBe(false)
    expect(document.activeElement).toBe(trigger)

    wrapper.unmount()
    trigger.remove()
  })

  test('Tab 在抽屉内循环（首尾互跳）', async () => {
    const { wrapper, trigger } = await mountSidebar()
    drawer.open.value = true
    await flushPromises()
    await nextTick()

    const element = surface()
    const focusable = Array.from(element.querySelectorAll<HTMLElement>('button, a[href]'))
    expect(focusable.length).toBeGreaterThan(1)
    const first = focusable[0]!
    const last = focusable[focusable.length - 1]!

    last.focus()
    expect(pressKey('Tab').defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first)

    first.focus()
    expect(pressKey('Tab', true).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(last)

    wrapper.unmount()
    trigger.remove()
  })
})
