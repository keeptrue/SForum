import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { flushPromises, mount } from '../helpers/vueSfc'
import { createFocusTrap } from '../../app/utils/a11y/focusTrap'
import { nextTick } from 'vue'

/**
 * 举报弹窗的键盘契约（RV-23）。
 *
 * 之前它只有 role="dialog" + aria-modal：Tab 能穿到底层页面、Esc 关不掉、
 * 关掉后焦点留在 body。这里逐条钉住四个行为：初始焦点、Tab 循环、Esc、焦点归还。
 *
 * `<Teleport>` 在这个用例里被 stub 成内联渲染：真实 teleport 会把节点送进
 * Vue 在模块加载时抓住的那份 document，而测试文件里的 document 是 helpers/vueSfc
 * 后来换上的另一棵树，两边对不上；stub 掉之后焦点/查询都落在同一棵树里。
 */

const globals = globalThis as Record<string, unknown>
const originalUseI18n = globals.useI18n
globals.useI18n = () => ({ t: (key: string) => key })

afterAll(() => {
  globals.useI18n = originalUseI18n
})

const componentPath = fileURLToPath(
  new URL('../../app/components/moderation/SFReportDialog.vue', import.meta.url)
)
const Dialog = await compileReportDialog()

async function compileReportDialog() {
  const { compileVueSfc } = await import('../helpers/vueSfc')
  return compileVueSfc(componentPath, 'sf-report-dialog', { createFocusTrap })
}

const UIconStub = { props: ['name'], template: '<span class="icon" />' }
const AlertStub = {
  props: ['title', 'variant', 'closable'],
  emits: ['close'],
  template: '<div :role="variant === \'danger\' ? \'alert\' : \'status\'">{{ title }}</div>'
}
const ButtonStub = {
  props: ['variant', 'size', 'disabled', 'loading'],
  emits: ['click'],
  template: '<button type="button" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
}

const REASONS = [{ label: 'Spam', value: 'spam' }, { label: 'Abuse', value: 'abuse' }]

async function mountClosedDialog() {
  const trigger = document.createElement('button')
  trigger.textContent = 'report'
  document.body.appendChild(trigger)
  trigger.focus()

  const wrapper = mount(Dialog, {
    attachTo: document.body,
    props: {
      open: false,
      reasons: REASONS,
      reason: 'spam',
      body: '',
      submitting: false
    },
    global: {
      stubs: { UIcon: UIconStub, SFAlert: AlertStub, SFButton: ButtonStub, Teleport: true }
    }
  })
  await flushPromises()
  return { wrapper, trigger }
}

async function openDialog(wrapper: { setProps: (props: Record<string, unknown>) => Promise<void> }) {
  await wrapper.setProps({ open: true })
  await flushPromises()
  await nextTick()
  return document.querySelector<HTMLElement>('[role="dialog"]')
}

function pressKey(key: string, shiftKey = false) {
  const event = new KeyboardEvent('keydown', { key, shiftKey, bubbles: true, cancelable: true })
  document.dispatchEvent(event)
  return event
}

describe('SFReportDialog keyboard contract', () => {
  test('打开后是模态 dialog，并且初始焦点落在对话框本身', async () => {
    const { wrapper, trigger } = await mountClosedDialog()
    const dialog = await openDialog(wrapper)

    expect(dialog).not.toBeNull()
    expect(dialog!.getAttribute('aria-modal')).toBe('true')
    expect(dialog!.getAttribute('aria-labelledby')).toBe('sf-report-dialog-title')
    expect(dialog!.getAttribute('tabindex')).toBe('-1')
    expect(document.activeElement).toBe(dialog)

    wrapper.unmount()
    trigger.remove()
  })

  test('Esc 关闭弹窗（emit close）', async () => {
    const { wrapper, trigger } = await mountClosedDialog()
    await openDialog(wrapper)

    pressKey('Escape')
    await flushPromises()

    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
    trigger.remove()
  })

  test('Tab 在弹窗内循环，不穿到底层页面', async () => {
    const { wrapper, trigger } = await mountClosedDialog()
    const dialog = (await openDialog(wrapper))!
    const focusable = Array.from(dialog.querySelectorAll<HTMLElement>('button, textarea'))
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

  test('关闭后焦点归还给触发按钮', async () => {
    const { wrapper, trigger } = await mountClosedDialog()
    await openDialog(wrapper)
    expect(document.activeElement).not.toBe(trigger)

    await wrapper.setProps({ open: false })
    await flushPromises()
    await nextTick()

    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(document.activeElement).toBe(trigger)

    wrapper.unmount()
    trigger.remove()
  })
})
