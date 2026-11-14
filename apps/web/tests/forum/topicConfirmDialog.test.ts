import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineComponent, h } from 'vue'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import { useConfirmDialog } from '~/composables/forum/useConfirmDialog'

/**
 * 公共话题详情页确认弹窗的回归守卫。
 *
 * 背景：话题页的 4 个危险入口（评论删除、话题删除、话题扩展动作、评论扩展动作）
 * 以前用浏览器原生 `window.confirm`：样式脱离站内、暗色模式下突兀，且无法表达
 * 「默认不聚焦破坏性按钮」。现在统一走站内 Nuxt UI Modal（`SFConfirmDialog` +
 * `useConfirmDialog`）。
 *
 * 这个文件钉死三件事：
 * 1. composable 的 Promise 语义：确认→true、取消→false、连续请求前一个按 false 结算、
 *    结算后 request.open 归 false（弹窗由页面持有的状态驱动关闭）；
 * 2. 组件契约：标题/正文渲染、底部「取消在前、删除在后」、ESC/遮罩/关闭统一按取消结算、
 *    关闭时不渲染；
 * 3. 页面契约：源码里不再有 `window.confirm(`，且仍复用既有的删除确认文案键。
 */

const locale = (file: string) => JSON.parse(
  readFileSync(new URL(`../../i18n/locales/${file}`, import.meta.url), 'utf8')
) as Record<string, any>

function lookup(source: Record<string, any>, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    source
  )
}

describe('useConfirmDialog · Promise 语义', () => {
  test('确认返回 true，取消返回 false', async () => {
    const { request, confirm, resolveConfirm } = useConfirmDialog()

    const confirmed = confirm({
      title: '删除评论',
      description: '确定要删除这条评论吗？',
      confirmLabel: '删除',
      cancelLabel: '取消'
    })
    expect(request.open).toBe(true)
    resolveConfirm(true)
    expect(await confirmed).toBe(true)

    const cancelled = confirm({
      title: '删除评论',
      description: '确定要删除这条评论吗？',
      confirmLabel: '删除',
      cancelLabel: '取消'
    })
    resolveConfirm(false)
    expect(await cancelled).toBe(false)
  })

  test('结算后 request.open 归 false（弹窗由页面状态驱动关闭）', () => {
    const { request, confirm, resolveConfirm } = useConfirmDialog()
    confirm({
      title: '删除话题',
      description: '确定要删除这个话题吗？',
      confirmLabel: '删除',
      cancelLabel: '取消'
    })
    expect(request.open).toBe(true)
    resolveConfirm(true)
    expect(request.open).toBe(false)
  })

  test('连续两次 confirm：前一个按取消结算，不留悬挂的 Promise', async () => {
    const { request, confirm, resolveConfirm } = useConfirmDialog()

    const first = confirm({
      title: '删除评论',
      description: '确定要删除这条评论吗？',
      confirmLabel: '删除',
      cancelLabel: '取消'
    })
    const second = confirm({
      title: '删除话题',
      description: '确定要删除这个话题吗？',
      confirmLabel: '删除',
      cancelLabel: '取消'
    })

    expect(await first).toBe(false)
    expect(request.open).toBe(true)
    resolveConfirm(true)
    expect(await second).toBe(true)
  })
})

/** `UModal` 桩：把标题/正文落到 data 属性上，footer 插槽照常渲染。 */
const UModalStub = defineComponent({
  name: 'UModal',
  props: {
    open: { type: Boolean, default: false },
    title: { type: String, default: '' },
    description: { type: String, default: '' },
    ui: { type: Object, default: () => ({}) }
  },
  emits: ['update:open'],
  setup(props, { slots }) {
    return () => (props.open
      ? h('div', { class: 'umodal-stub', 'data-title': props.title, 'data-description': props.description }, [
          h('div', { class: 'umodal-stub__body' }, slots.body?.()),
          h('div', { class: 'umodal-stub__footer' }, slots.footer?.())
        ])
      : null)
  }
})

/** `UButton` 桩：保留 color/variant，把点击上抛成组件事件。 */
const UButtonStub = defineComponent({
  name: 'UButton',
  props: {
    color: { type: String, default: '' },
    variant: { type: String, default: '' }
  },
  emits: ['click'],
  setup(props, { slots, emit }) {
    return () => h('button', {
      type: 'button',
      class: 'ubutton-stub',
      'data-color': props.color,
      'data-variant': props.variant,
      onClick: () => emit('click')
    }, slots.default?.())
  }
})

const componentPath = fileURLToPath(new URL('../../app/components/forum/SFConfirmDialog.vue', import.meta.url))
const SFConfirmDialog = await compileVueSfc(componentPath, 'sf-confirm-dialog', {})

const request = {
  open: true,
  title: '删除评论',
  description: '确定要删除这条评论吗？',
  confirmLabel: '删除',
  cancelLabel: '取消'
}

function mountDialog(props: Record<string, unknown> = {}) {
  return mount(SFConfirmDialog, {
    props: { request: { ...request, ...props } },
    global: { components: { UModal: UModalStub, UButton: UButtonStub } }
  })
}

describe('SFConfirmDialog · 渲染契约', () => {
  test('标题与正文都渲染出来', () => {
    const wrapper = mountDialog()
    try {
      const modal = wrapper.get('.umodal-stub')
      expect(modal.attributes('data-title')).toBe('删除评论')
      expect(modal.attributes('data-description')).toBe('确定要删除这条评论吗？')
    } finally {
      wrapper.unmount()
    }
  })

  test('底部：取消（neutral/ghost）在前，删除（error）在后', () => {
    const wrapper = mountDialog()
    try {
      const buttons = wrapper.findAll('.ubutton-stub')
      expect(buttons).toHaveLength(2)
      expect(buttons[0].attributes('data-color')).toBe('neutral')
      expect(buttons[0].attributes('data-variant')).toBe('ghost')
      expect(buttons[0].text()).toBe('取消')
      expect(buttons[1].attributes('data-color')).toBe('error')
      expect(buttons[1].text()).toBe('删除')
    } finally {
      wrapper.unmount()
    }
  })

  test('破坏性按钮不抢默认焦点：组件不给任何元素挂 autofocus', () => {
    const source = readFileSync(
      new URL('../../app/components/forum/SFConfirmDialog.vue', import.meta.url),
      'utf8'
    )
    expect(source).not.toContain('autofocus')
    // 取消按钮在 DOM 顺序上先于删除按钮：弹窗把焦点交给首个可聚焦元素时，
    // 落点也是取消，而不是破坏性动作。
    const wrapper = mountDialog()
    try {
      const buttons = wrapper.findAll('.ubutton-stub')
      expect(buttons[0].text()).toBe('取消')
      expect(buttons[1].text()).toBe('删除')
    } finally {
      wrapper.unmount()
    }
  })

  test('点击取消按 false 结算，点击删除按 true 结算', async () => {
    const wrapper = mountDialog()
    try {
      const buttons = wrapper.findAll('.ubutton-stub')
      await buttons[0].trigger('click')
      expect(wrapper.emitted('settle')).toEqual([[false]])

      await buttons[1].trigger('click')
      expect(wrapper.emitted('settle')).toEqual([[false], [true]])
    } finally {
      wrapper.unmount()
    }
  })

  test('ESC / 点遮罩 / 关闭按钮统一按取消结算', async () => {
    const wrapper = mountDialog()
    try {
      wrapper.findComponent(UModalStub).vm.$emit('update:open', false)
      await wrapper.vm.$nextTick()
      expect(wrapper.emitted('settle')).toEqual([[false]])
    } finally {
      wrapper.unmount()
    }
  })

  test('关闭时不渲染任何内容', () => {
    const wrapper = mountDialog({ open: false })
    try {
      expect(wrapper.find('.umodal-stub').exists()).toBe(false)
      expect(wrapper.findAll('.ubutton-stub')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('话题页 · 确认弹窗契约', () => {
  const page = readFileSync(
    new URL('../../app/components/forum/SFTopicShowPage.vue', import.meta.url),
    'utf8'
  )

  test('页面不再使用浏览器原生 window.confirm', () => {
    expect(page).not.toContain('window.confirm(')
    expect(page).not.toMatch(/window\.confirm/)
  })

  test('页面显式引入确认弹窗组件与组合式，并挂载一次', () => {
    expect(page).toContain("import SFConfirmDialog from '~/components/forum/SFConfirmDialog.vue'")
    expect(page).toContain("import { useConfirmDialog } from '~/composables/forum/useConfirmDialog'")
    expect(page).toContain('<SFConfirmDialog')
    expect(page.match(/<SFConfirmDialog/g)).toHaveLength(1)
    expect(page).toContain(':request="confirmRequest"')
    expect(page).toContain('@settle="resolveConfirm"')
  })

  test('四个危险入口都走站内确认弹窗，并复用既有确认文案键', () => {
    expect(page).toContain('topicDetail.confirmCommentDelete')
    expect(page).toContain('topicDetail.confirmDelete')
    expect(page).toContain('topicDetail.confirmExtensionAction')
    expect(page).toContain('topicDetail.confirmDialog.commentDeleteTitle')
    expect(page).toContain('topicDetail.confirmDialog.topicDeleteTitle')
    expect(page).toContain('topicDetail.confirmDialog.extensionActionTitle')
    expect(page.match(/await confirm\(/g)).toHaveLength(4)
  })

  test('新标题键在两种语言下成对存在且文案非空', () => {
    for (const file of ['zh-CN.json', 'en-US.json']) {
      const source = locale(file)
      for (const key of ['commentDeleteTitle', 'topicDeleteTitle', 'extensionActionTitle']) {
        const value = lookup(source, `topicDetail.confirmDialog.${key}`)
        expect(typeof value).toBe('string')
        expect((value as string).length).toBeGreaterThan(0)
      }
    }
  })
})
