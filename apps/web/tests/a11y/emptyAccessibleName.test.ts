import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'
import { useI18nFor } from '../helpers/locale'

/**
 * 空串的可访问名语义（第 15 轮遗留项）。
 *
 * 审计原文：`SFAvatar` 传 `name=''`、`SFTabs` 传 `ariaLabel=''` 会回退到默认文案，
 * 当时全仓无人传空串，所以「要不要改成空串 = 空」一直悬着。这一轮把语义定下来：
 *
 * - `SFTabs.ariaLabel`：空串 / 纯空白 = **未提供** → 回落到本地化默认名。
 *   一个没有可访问名的 tablist 对读屏只是噪声；「没有页签」应当不渲染组件。
 * - `SFAvatar.name`：空串 = 没有名字 → 仍按「匿名用户」播报（不是静默）。
 * - `SFAvatar.alt`：**只有**这里空串有强语义 —— 装饰性头像，`aria-hidden` + `alt=""`。
 *   纯空白按未提供处理，避免产出「   」这种伪可访问名。
 *
 * 两条相反的方向是刻意的：装饰性图像与容器地标不是同一件事，所以分开钉。
 */

const bindings = {
  computed: testVue.computed,
  ref: testVue.ref,
  watch: testVue.watch,
  useI18n: () => useI18nFor('zh-CN')()
}

const SFAvatar = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/SFAvatar.vue', import.meta.url)),
  'sf-avatar-empty-name',
  { ...bindings, forumAvatarToneClass: () => 'tone' }
)
const SFTabs = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/SFTabs.vue', import.meta.url)),
  'sf-tabs-empty-label',
  bindings
)

function mountComponent(component: unknown, props: Record<string, unknown>) {
  return mount(component as never, {
    props,
    global: {
      stubs: { UIcon: true, SFBadge: true },
      mocks: {}
    }
  } as never)
}

const uploadedAvatar = { kind: 'uploaded', url: '/media/avatars/pub-1' }

describe('SFAvatar：空串不产出伪可访问名', () => {
  test('name 为空串 → 仍按匿名用户播报（不是空名）', () => {
    const wrapper = mountComponent(SFAvatar, { name: '', avatar: uploadedAvatar })
    try {
      expect(wrapper.get('img').attributes('alt')).toBe('匿名用户')
      expect(wrapper.attributes('aria-hidden')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('name 为纯空白 → 与未提供等价', () => {
    const wrapper = mountComponent(SFAvatar, { name: '   ', avatar: uploadedAvatar })
    try {
      expect(wrapper.get('img').attributes('alt')).toBe('匿名用户')
    } finally {
      wrapper.unmount()
    }
  })

  test('alt 显式空串 → 装饰性：aria-hidden + 空 alt', () => {
    const wrapper = mountComponent(SFAvatar, { name: '张三', avatar: uploadedAvatar, alt: '' })
    try {
      expect(wrapper.attributes('aria-hidden')).toBe('true')
      expect(wrapper.get('img').attributes('alt')).toBe('')
    } finally {
      wrapper.unmount()
    }
  })

  test('avatar.alt 为空串（服务端字段）→ 按未提供处理，不静默变成装饰', () => {
    // 服务端 Alt() 的契约是非空（DisplayName → Username → "User"）；空值只可能是
    // 旧/坏载荷。此时静默 aria-hidden 会把「没名字」升级成「读屏跳过」。
    const wrapper = mountComponent(SFAvatar, {
      name: '张三',
      avatar: { ...uploadedAvatar, alt: '' }
    })
    try {
      expect(wrapper.attributes('aria-hidden')).toBeUndefined()
      expect(wrapper.get('img').attributes('alt')).toBe('张三')
    } finally {
      wrapper.unmount()
    }
  })

  test('avatar.alt 有值 → 用它做可访问名（服务端权威）', () => {
    const wrapper = mountComponent(SFAvatar, {
      name: '张三',
      avatar: { ...uploadedAvatar, alt: '李四' }
    })
    try {
      expect(wrapper.get('img').attributes('alt')).toBe('李四')
    } finally {
      wrapper.unmount()
    }
  })

  test('alt 为纯空白 → 视同未提供，回落显示名', () => {
    const wrapper = mountComponent(SFAvatar, { name: '张三', avatar: uploadedAvatar, alt: '   ' })
    try {
      expect(wrapper.attributes('aria-hidden')).toBeUndefined()
      expect(wrapper.get('img').attributes('alt')).toBe('张三')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFTabs：空串页签条名回落到默认文案', () => {
  const items = [{ label: '概览', value: 'overview' }]

  test('ariaLabel 未提供 → 本地化默认名', () => {
    const wrapper = mountComponent(SFTabs, { items })
    try {
      expect(wrapper.get('[role="tablist"]').attributes('aria-label')).toBe('分区切换')
    } finally {
      wrapper.unmount()
    }
  })

  test('ariaLabel 为空串 → 与未提供等价（不产出 aria-label=""）', () => {
    const wrapper = mountComponent(SFTabs, { items, ariaLabel: '' })
    try {
      expect(wrapper.get('[role="tablist"]').attributes('aria-label')).toBe('分区切换')
    } finally {
      wrapper.unmount()
    }
  })

  test('ariaLabel 为纯空白 → 同样回落，不把空白当名字', () => {
    const wrapper = mountComponent(SFTabs, { items, ariaLabel: '   ' })
    try {
      expect(wrapper.get('[role="tablist"]').attributes('aria-label')).toBe('分区切换')
    } finally {
      wrapper.unmount()
    }
  })

  test('显式文案原样保留（含首尾空白也按 trim 后使用）', () => {
    const wrapper = mountComponent(SFTabs, { items, ariaLabel: '  主题分区  ' })
    try {
      expect(wrapper.get('[role="tablist"]').attributes('aria-label')).toBe('主题分区')
    } finally {
      wrapper.unmount()
    }
  })
})
