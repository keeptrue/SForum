import { describe, expect, test } from 'bun:test'
import { computed, defineComponent } from 'vue'
import { fileURLToPath } from 'node:url'
import { forumReactionStackOverflow } from '~/utils/forum/forumReaction'
import { compileVueSfc, mount } from '../helpers/vueSfc'

const stackPath = fileURLToPath(new URL('../../app/components/forum/SFReactionStack.vue', import.meta.url))

// 头像组桩：把收到的 props 落到 data-* 上，用来验证 +N 数学与装饰化开关。
const AvatarGroupStub = defineComponent({
  name: 'SFAvatarGroup',
  props: {
    items: { type: Array, default: () => [] },
    total: { type: Number, default: 0 },
    max: { type: Number, default: 0 },
    size: { type: String, default: '' },
    showOverflow: { type: Boolean, default: true },
    decorative: { type: Boolean, default: false }
  },
  template: `<div
    class="group"
    :data-total="total"
    :data-items="items.length"
    :data-max="max"
    :data-size="size"
    :data-chip="String(showOverflow)"
    :data-decorative="String(decorative)"
  />`
})

const ReactionStack = await compileVueSfc(stackPath, 'reaction-stack', {
  computed,
  forumReactionStackOverflow,
  useI18n: () => ({ t: (key: string, params?: { count?: number }) => `${key}:${params?.count ?? ''}` }),
  SFAvatarGroup: AvatarGroupStub
})

function user(id: number, name: string) {
  return { id, username: `u${id}`, displayName: name, avatar: { kind: 'initials', url: '', alt: '' } }
}

const preview = [user(3, '种子用户2'), user(9, '阿岚'), user(12, 'Kai')]

function mountStack(props: Record<string, unknown> = {}) {
  return mount(ReactionStack, {
    props: { items: preview, total: 3, max: 3, size: 'list', ...props },
    global: { stubs: { SFAvatarGroup: AvatarGroupStub } }
  })
}

describe('reaction stack first paint', () => {
  test('renders the preview avatars with no request and no loading state', () => {
    // 编译期没有注入任何 API：组件一旦发请求这里就会直接抛错。
    const wrapper = mountStack()
    try {
      const group = wrapper.get('.group')
      expect(group.attributes('data-items')).toBe('3')
      expect(group.attributes('data-total')).toBe('3')
      expect(wrapper.find('button').exists()).toBe(true)
      expect(wrapper.html()).not.toContain('skeleton')
    } finally {
      wrapper.unmount()
    }
  })

  test('passes the decorative flag so names only live in the trigger label', () => {
    const wrapper = mountStack()
    try {
      expect(wrapper.get('.group').attributes('data-decorative')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('reaction stack overflow math', () => {
  test('keeps the total visible so the group can draw +N', () => {
    const wrapper = mountStack({ total: 8 })
    try {
      const group = wrapper.get('.group')
      expect(group.attributes('data-items')).toBe('3')
      expect(group.attributes('data-total')).toBe('8')
    } finally {
      wrapper.unmount()
    }
  })

  test('truncates the preview to max before handing it to the group', () => {
    const wrapper = mountStack({ total: 5, max: 2 })
    try {
      const group = wrapper.get('.group')
      expect(group.attributes('data-items')).toBe('2')
      expect(group.attributes('data-total')).toBe('5')
      expect(group.attributes('data-max')).toBe('2')
    } finally {
      wrapper.unmount()
    }
  })

  test('never renders a negative overflow when the total lags behind the preview', () => {
    const wrapper = mountStack({ total: 1 })
    try {
      // visible 3 > total 1：溢出钳到 0，total 不塌成负数
      expect(wrapper.get('.group').attributes('data-total')).toBe('3')
    } finally {
      wrapper.unmount()
    }
  })

  test('count zero renders nothing at all', () => {
    const wrapper = mountStack({ total: 0, items: [] })
    try {
      expect(wrapper.find('button').exists()).toBe(false)
      expect(wrapper.find('.group').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('hides the +N chip when the caller already shows the count', () => {
    const wrapper = mountStack({ items: [], total: 4, chip: false })
    try {
      // 评论行：没有预览且不画 chip，整个触发器都不渲染
      expect(wrapper.find('button').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('reaction stack trigger accessibility', () => {
  test('exposes the roster count and a dialog popup hint on the trigger', () => {
    const wrapper = mountStack({ total: 7 })
    try {
      const trigger = wrapper.get('button')
      expect(trigger.attributes('type')).toBe('button')
      expect(trigger.attributes('aria-label')).toBe('forum.reaction.rosterAria:7')
      expect(trigger.attributes('aria-haspopup')).toBe('dialog')
    } finally {
      wrapper.unmount()
    }
  })

  test('clicking the trigger emits open without touching state', async () => {
    const wrapper = mountStack()
    try {
      await wrapper.get('button').trigger('click')
      expect(wrapper.emitted('open')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })
})
