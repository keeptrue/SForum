import { describe, expect, test } from 'bun:test'
import { computed, defineComponent } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import { shouldShowUserHandle, userDisplayName } from '../../app/utils/forum/userDisplay'

/**
 * `SFUserIdentityName` 的 split 变体（默认 fallback 分支）。
 *
 * 评论作者行传了 `#name-row` 槽，走的是调用方渲染名字的那条路；这里锁的是**不传槽**时
 * 身份组件自己的退路：名字行照旧是「名字链接 / 纯文本 + name-suffix」，`@username` 独立成行
 * 且是它自己的链接。两条路都必须在名字行上收敛，否则 split 一旦被别处复用就会悄悄退化成
 * 「handle 撑宽整块」的老问题。
 *
 * 挂的是真实编译的组件（不是 stub），断言 DOM 关系而不是模板字符串。
 */
const identityPath = fileURLToPath(new URL('../../app/components/forum/SFUserIdentityName.vue', import.meta.url))

const SFUserIdentityName = await compileVueSfc(identityPath, 'sf-user-identity-name-split-fallback', {
  computed,
  userDisplayName,
  shouldShowUserHandle
})

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: String, default: '' } },
  template: '<a :href="to" data-testid="link"><slot /></a>'
})

function mountSplit(props: Record<string, unknown> = {}, slots: Record<string, string> = {}) {
  return mount(SFUserIdentityName, {
    props: {
      user: { displayName: '大佬', username: 'zhuchunshu' },
      to: '/u/zhuchunshu',
      variant: 'split',
      ...props
    },
    slots,
    global: { components: { NuxtLink: NuxtLinkStub } }
  })
}

describe('SFUserIdentityName split fallback', () => {
  test('puts the name link and the handle link in separate rows', () => {
    const wrapper = mountSplit()
    try {
      const nameLine = wrapper.get('.sf-user-identity-name__name-line')
      const nameLink = nameLine.get('a')
      const handleLink = wrapper.get('.sf-user-identity-name__handle-link')

      // 名字行里就是身份组件的昵称节点，不是调用方另写的一份。
      expect(nameLine.get('.sf-user-identity-name__name').text()).toBe('大佬')
      expect(nameLink.text()).toBe('大佬')
      expect(nameLink.attributes('href')).toBe('/u/zhuchunshu')
      // handle 是独立链接，不在名字链接里，也不在名字行里。
      expect(handleLink.attributes('href')).toBe('/u/zhuchunshu')
      expect(handleLink.text()).toBe('@zhuchunshu')
      expect(handleLink.element.closest('.sf-user-identity-name__name-line')).toBeNull()
      expect(nameLine.element.contains(handleLink.element)).toBe(false)
      expect(nameLink.element.contains(handleLink.element)).toBe(false)
      // 名字行宽度只由名字决定，handle 行不参与。
      expect(nameLine.text()).not.toContain('@zhuchunshu')
      // 结构上没有嵌套交互元素（这条变体迟早会接按钮，四种组合都要为 0）。
      expect(wrapper.findAll('button button')).toHaveLength(0)
      expect(wrapper.findAll('button a')).toHaveLength(0)
      expect(wrapper.findAll('a button')).toHaveLength(0)
      expect(wrapper.findAll('a a')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('renders the name-suffix slot inside the name row, outside the name link', () => {
    const wrapper = mountSplit({}, { 'name-suffix': '<i data-testid="op">楼主</i>' })
    try {
      const nameLine = wrapper.get('.sf-user-identity-name__name-line')
      const suffix = wrapper.get('[data-testid="op"]')

      expect(nameLine.element.contains(suffix.element)).toBe(true)
      // 后缀是名字行的兄弟节点而不是名字链接的子节点：调用方往槽里放按钮时不会变成 a>button。
      expect(suffix.element.closest('a')).toBeNull()
      expect(wrapper.findAll('a button')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('lets the caller take over the whole name row through the name-row slot', () => {
    const wrapper = mountSplit({}, { 'name-row': '<b data-testid="caller">大佬</b>' })
    try {
      const nameLine = wrapper.get('.sf-user-identity-name__name-line')

      expect(nameLine.find('[data-testid="caller"]').exists()).toBe(true)
      // 槽一旦提供，默认的名字链接不再渲染（否则同一个名字会出现两次可点目标）。
      expect(nameLine.find('a').exists()).toBe(false)
      // handle 行不受槽影响，仍是独立链接。
      expect(wrapper.get('.sf-user-identity-name__handle-link').text()).toBe('@zhuchunshu')
    } finally {
      wrapper.unmount()
    }
  })

  test('degrades the handle to plain text when the identity is not linked', () => {
    const wrapper = mountSplit({ linked: false })
    try {
      expect(wrapper.find('.sf-user-identity-name__handle-link').exists()).toBe(false)
      expect(wrapper.get('.sf-user-identity-name__handle').text()).toBe('@zhuchunshu')
      expect(wrapper.findAll('a')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('hides the handle row entirely when showHandle is off', () => {
    const wrapper = mountSplit({ showHandle: false })
    try {
      expect(wrapper.find('.sf-user-identity-name__handle').exists()).toBe(false)
      expect(wrapper.get('.sf-user-identity-name__name-line').text()).toBe('大佬')
    } finally {
      wrapper.unmount()
    }
  })
})
