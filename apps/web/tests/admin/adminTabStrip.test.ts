import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { adminTabElementId, nextTabIndex } from '../../app/utils/admin/adminTabStrip'

const componentPath = fileURLToPath(
  new URL('../../app/components/admin/shell/SFAdminTabStrip.vue', import.meta.url)
)
// 组件里的 `~/utils/admin/adminTabStrip` 导入会被 SFC 测试编译器剥掉，
// 这里按 helpers/vueSfc 的约定用 bindings 注入同名实现。
const TabStrip = await compileVueSfc(componentPath, 'admin-tab-strip', {
  adminTabElementId,
  nextTabIndex
})

const ICON_STUB = { props: ['name'], template: '<span class="icon" />' }

const dashboard = { id: '/', label: '控制台', icon: 'i-lucide-house', closable: false }
const roles = { id: '/roles', label: '角色', icon: 'i-lucide-shield', closable: true }
const settings = { id: '/settings', label: '设置', icon: 'i-lucide-settings', closable: true }

function mountStrip(props: Record<string, unknown>) {
  return mount(TabStrip, {
    attachTo: document.body,
    props: { label: 'Open admin pages', closeLabel: 'Close tab', ...props },
    global: { stubs: { UIcon: ICON_STUB } }
  })
}

/**
 * 装配壳：模拟 admin.vue 的接线（navigate 改 activeId、close 删页签并落到相邻项），
 * 这样「关闭后焦点去哪」测的是真实状态流转，不是组件内部假象。
 */
function mountHarness(tabs: Array<typeof dashboard>, activeId: string) {
  const navigations: string[] = []
  const Harness = testVue.defineComponent({
    setup() {
      const items = testVue.ref(tabs.map(tab => ({ ...tab })))
      const active = testVue.ref(activeId)
      return () => testVue.h(TabStrip, {
        tabs: items.value,
        activeId: active.value,
        label: 'Open admin pages',
        closeLabel: 'Close tab',
        onNavigate: (id: string) => {
          navigations.push(id)
          active.value = id
        },
        onClose: (id: string) => {
          items.value = items.value.filter(tab => tab.id !== id)
          if (active.value === id) {
            active.value = items.value[items.value.length - 1]?.id ?? '/'
          }
        }
      })
    }
  })

  return { wrapper: mount(Harness, { attachTo: document.body, global: { stubs: { UIcon: ICON_STUB } } }), navigations }
}

describe('SFAdminTabStrip：ARIA tablist 结构', () => {
  test('tablist 的直接子元素全部是 role=tab，没有夹层 div', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/' })
    try {
      const tablist = wrapper.get('[role="tablist"]')
      const children = Array.from(tablist.element.children)

      expect(children).toHaveLength(2)
      expect(children.every(child => child.getAttribute('role') === 'tab')).toBe(true)
      expect(children.every(child => child.tagName === 'BUTTON')).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('roving tabindex：只有一个页签留在 Tab 序列里，且就是选中项', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/roles' })
    try {
      const tabs = wrapper.findAll('[role="tab"]')
      const reachable = tabs.filter(tab => tab.attributes('tabindex') === '0')

      expect(reachable).toHaveLength(1)
      expect(reachable[0]!.attributes('aria-selected')).toBe('true')
      expect(reachable[0]!.attributes('id')).toBe(adminTabElementId('/roles'))
      expect(tabs.map(tab => tab.attributes('tabindex'))).toEqual(['-1', '0'])
    } finally {
      wrapper.unmount()
    }
  })

  test('关闭控件在 tablist 之外、可聚焦、带 aria-label', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/roles' })
    try {
      const tablist = wrapper.get('[role="tablist"]').element
      const close = wrapper.get('button[aria-label="Close tab"]')

      expect(tablist.contains(close.element)).toBe(false)
      expect(close.element.tagName).toBe('BUTTON')
      expect((close.element as HTMLButtonElement).disabled).toBe(false)
      expect(close.attributes('type')).toBe('button')
      expect(close.attributes('tabindex')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('页签内的 X 是装饰元素：aria-hidden、不可聚焦、不是 tablist 直接子元素', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles, settings], activeId: '/' })
    try {
      const tablist = wrapper.get('[role="tablist"]').element
      const tabs = wrapper.findAll('[role="tab"]')
      const rolesTab = tabs[1]!

      const x = rolesTab.get('[aria-hidden="true"]')
      // tabindex 不存在 = 完全不可聚焦（aria-hidden 元素一旦可聚焦就是 axe 违规）。
      expect(x.attributes('tabindex')).toBeUndefined()
      expect(x.element.tagName).toBe('SPAN')
      // 在页签按钮内部，而不是 tablist 的直接子元素。
      expect(rolesTab.element.contains(x.element)).toBe(true)
      expect(Array.from(tablist.children).includes(x.element)).toBe(false)

      // 不可关闭的页签没有 X。
      expect(tabs[0]!.find('[aria-hidden="true"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('点页签内的 X 只 emit close，不 emit navigate', async () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/' })
    try {
      const x = wrapper.findAll('[role="tab"]')[1]!.get('[aria-hidden="true"]')

      await x.trigger('click')

      expect(wrapper.emitted('close')).toEqual([['/roles']])
      expect(wrapper.emitted('navigate')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('tablist 直接子元素仍然只有 button[role=tab]（axe 判定条件）', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles, settings], activeId: '/roles' })
    try {
      const children = Array.from(wrapper.get('[role="tablist"]').element.children)

      expect(children).toHaveLength(3)
      expect(children.every(child => child.tagName === 'BUTTON')).toBe(true)
      expect(children.every(child => child.getAttribute('role') === 'tab')).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('激活项不可关闭时（控制台）不渲染关闭控件', () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/' })
    try {
      expect(wrapper.find('button[aria-label="Close tab"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAdminTabStrip：键盘导航', () => {
  test('←/→ 在页签间移动并激活，含首尾循环', async () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles, settings], activeId: '/' })
    try {
      const tabs = wrapper.findAll('[role="tab"]')

      await tabs[0]!.trigger('keydown', { key: 'ArrowRight' })
      expect(wrapper.emitted('navigate')).toEqual([['/roles']])

      await tabs[0]!.trigger('keydown', { key: 'ArrowLeft' })
      expect(wrapper.emitted('navigate')![1]).toEqual(['/settings'])
    } finally {
      wrapper.unmount()
    }
  })

  test('Home/End 跳到首尾，激活后焦点跟到页签按钮上', async () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles, settings], activeId: '/roles' })
    try {
      const tabs = wrapper.findAll('[role="tab"]')

      await tabs[1]!.trigger('keydown', { key: 'End' })
      await flushPromises()
      expect(wrapper.emitted('navigate')).toEqual([['/settings']])
      expect(document.activeElement).toBe(tabs[2]!.element)

      await tabs[1]!.trigger('keydown', { key: 'Home' })
      await flushPromises()
      expect(wrapper.emitted('navigate')![1]).toEqual(['/'])
      expect(document.activeElement).toBe(tabs[0]!.element)
    } finally {
      wrapper.unmount()
    }
  })

  test('event.target 不是页签时不劫持方向键', async () => {
    const wrapper = mountStrip({ tabs: [dashboard, roles], activeId: '/roles' })
    try {
      // 冒泡到 tablist 但目标不是页签本体（tablist 自身 / 关闭控件）：不能切页签。
      await wrapper.get('[role="tablist"]').trigger('keydown', { key: 'ArrowRight' })
      await wrapper.get('[role="tablist"]').trigger('keydown', { key: 'End' })
      await wrapper.get('button[aria-label="Close tab"]').trigger('keydown', { key: 'ArrowLeft' })

      expect(wrapper.emitted('navigate')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAdminTabStrip：关闭后的焦点落点', () => {
  test('关闭当前页签后，焦点落到新的激活页签（不回 body）', async () => {
    const { wrapper } = mountHarness([dashboard, roles, settings], '/settings')
    try {
      await wrapper.get('button[aria-label="Close tab"]').trigger('click')
      await flushPromises()

      const tabs = wrapper.findAll('[role="tab"]')
      expect(tabs).toHaveLength(2)
      expect(tabs.map(tab => tab.attributes('aria-selected'))).toEqual(['false', 'true'])
      expect(document.activeElement).toBe(tabs[1]!.element)
      expect(document.activeElement).not.toBe(document.body)
    } finally {
      wrapper.unmount()
    }
  })

  test('点击页签本体只触发一次 navigate（不双触发）', async () => {
    const { wrapper, navigations } = mountHarness([dashboard, roles], '/')
    try {
      await wrapper.findAll('[role="tab"]')[1]!.trigger('click')
      await flushPromises()

      expect(navigations).toEqual(['/roles'])
      expect(wrapper.findAll('[role="tab"]')[1]!.attributes('aria-selected')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('nextTabIndex：纯函数', () => {
  test('←/→ 循环，Home/End 到首尾', () => {
    expect(nextTabIndex(0, 3, 'ArrowRight')).toBe(1)
    expect(nextTabIndex(2, 3, 'ArrowRight')).toBe(0)
    expect(nextTabIndex(0, 3, 'ArrowLeft')).toBe(2)
    expect(nextTabIndex(1, 3, 'ArrowLeft')).toBe(0)
    expect(nextTabIndex(1, 3, 'Home')).toBe(0)
    expect(nextTabIndex(1, 3, 'End')).toBe(2)
  })

  test('非导航键返回 null，越界下标收敛到首项', () => {
    expect(nextTabIndex(0, 3, 'Enter')).toBeNull()
    expect(nextTabIndex(0, 3, 'Tab')).toBeNull()
    expect(nextTabIndex(0, 0, 'ArrowRight')).toBeNull()
    expect(nextTabIndex(-1, 3, 'ArrowRight')).toBe(1)
    expect(nextTabIndex(9, 3, 'ArrowRight')).toBe(1)
  })
})

describe('admin 壳层 skip link 接线', () => {
  // skip link 在布局里，没有可挂载的组件测试环境（布局依赖整套 Nuxt composable），
  // 这一条只能按源码断言；页签条本身的行为全部由上面的挂载测试覆盖。
  const layout = readFileSync(new URL('../../app/layouts/admin.vue', import.meta.url), 'utf8')
  const zhCN = readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
  const enUS = readFileSync(new URL('../../i18n/locales/en-US.json', import.meta.url), 'utf8')

  test('skip link 指向主内容容器，且排在侧栏之前', () => {
    expect(layout).toContain('href="#sforum-admin-main"')
    expect(layout).toContain('id="sforum-admin-main"')
    expect(layout).toContain('tabindex="-1"')
    expect(layout.indexOf('href="#sforum-admin-main"'))
      .toBeLessThan(layout.indexOf('<UDashboardSidebar'))
  })

  test('主内容容器是 tabpanel，并被激活页签标注', () => {
    expect(layout).toContain('role="tabpanel"')
    expect(layout).toContain(':aria-labelledby="adminTabElementId(adminTabs.activeTabId.value)"')
  })

  test('文案在两个 locale 同步存在', () => {
    expect(zhCN).toContain('"skipToContent"')
    expect(enUS).toContain('"skipToContent"')
  })
})
