import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, h } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import { shouldShowUserHandle, userDisplayName } from '../../app/utils/forum/userDisplay'

/**
 * 评论作者行（从 SFComment 抽出的独立组件）：
 * 预览按钮 / 链接 / 纯文本三种形态、楼主标、徽章 chip 的挂载位置与事件。
 *
 * 这里用**真实编译**的 `SFUserIdentityName`，因为本文件锁的正是两者之间的 DOM 关系：
 * chip 必须和昵称同处一行、不能被更长的 `@username` 行推到后面（那是纯 CSS 几何问题的
 * 结构根因），同时不许出现嵌套的 button/a。
 */
const identityPath = fileURLToPath(new URL('../../app/components/forum/SFUserIdentityName.vue', import.meta.url))
const rowPath = fileURLToPath(new URL('../../app/components/forum/SFCommentAuthorRow.vue', import.meta.url))

const SFUserIdentityName = await compileVueSfc(identityPath, 'sf-user-identity-name-comment-row', {
  computed,
  userDisplayName,
  shouldShowUserHandle
})

/** chip 的真实形态：拿到 username 时是 `<button>`（点击开徽章说明），否则是纯展示 span。 */
const ChipStub = defineComponent({
  name: 'SFBadgeChip',
  props: {
    badge: { type: Object, required: true },
    username: { type: [String, Object], default: null }
  },
  template: '<button v-if="username" type="button" data-testid="chip" :data-username="username">{{ badge.name }}</button>'
    + '<span v-else data-testid="chip">{{ badge.name }}</span>'
})

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: String, default: '' } },
  template: '<a :href="to" data-testid="author-link"><slot /></a>'
})

const SFCommentAuthorRow = await compileVueSfc(rowPath, 'sf-comment-author-row', {
  computed,
  useI18n: () => ({ t: (key: string) => (key === 'topicDetail.opBadge' ? '楼主' : key) }),
  SFUserIdentityName,
  SFBadgeChip: ChipStub
})

const badge = { slug: 'regular', name: '常客', tier: 'bronze', icon: 'i-lucide-calendar' }

function mountRow(props: Record<string, unknown>) {
  return mount(SFCommentAuthorRow, {
    props,
    global: { components: { NuxtLink: NuxtLinkStub } }
  })
}

describe('SFCommentAuthorRow', () => {
  test('renders the preview trigger button with dialog wiring and emits on click', async () => {
    const wrapper = mountRow({
      author: 'Alice',
      authorLink: '/u/alice',
      username: 'alice',
      displayName: 'Alice',
      interactive: true,
      previewOpen: true,
      previewId: 'preview-1',
      topBadge: badge
    })
    try {
      const button = wrapper.get('button.sf-comment__author-trigger')
      expect(button.attributes('aria-haspopup')).toBe('dialog')
      expect(button.attributes('aria-expanded')).toBe('true')
      expect(button.attributes('aria-controls')).toBe('preview-1')
      expect(button.get('.sf-comment__author-name').text()).toBe('Alice')

      await button.trigger('click')
      expect(wrapper.emitted('previewToggle')).toHaveLength(1)
      // 原生事件必须一起透传：父层要靠 target.closest 找回触发按钮（关闭预览后还焦）。
      expect(wrapper.emitted('previewToggle')?.[0]?.[0]).toBeInstanceOf(Event)
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the chip in the name row, beside the trigger instead of after the handle line', () => {
    const wrapper = mountRow({
      author: 'Alice',
      authorLink: '/u/alice',
      username: 'alice',
      displayName: 'Alice',
      interactive: true,
      topBadge: badge
    })
    try {
      const trigger = wrapper.get('button.sf-comment__author-trigger')
      const chip = wrapper.get('[data-testid="chip"]')
      const nameLine = wrapper.get('.sf-user-identity-name__name-line')
      const handle = wrapper.get('.sf-user-identity-name__handle')

      // 结构根因断言：chip 与触发按钮是同一名字行里的兄弟，而不是整块身份之后的兄弟。
      // 后者会让 chip 的 x 落在 `@username` 宽度之后（短昵称时出现几十像素空隙）。
      expect(chip.element.parentElement).toBe(trigger.element.parentElement)
      expect(nameLine.element.contains(chip.element)).toBe(true)
      expect(trigger.element.contains(chip.element)).toBe(false)
      // @username 单独成行且是它自己的资料页链接，不包住 chip。
      expect(handle.text()).toBe('@alice')
      expect(handle.element.closest('a')).not.toBeNull()
      expect(handle.element.closest('a')?.contains(chip.element)).toBe(false)
      expect(nameLine.element.contains(handle.element)).toBe(false)
      // chip 需要 username 才能打开徽章说明弹窗。
      expect(chip.attributes('data-username')).toBe('alice')
    } finally {
      wrapper.unmount()
    }
  })

  test('never nests interactive elements in the author row', () => {
    const wrapper = mountRow({
      author: 'Alice',
      authorLink: '/u/alice',
      username: 'alice',
      displayName: 'Alice',
      isOpAuthor: true,
      interactive: true,
      topBadge: badge
    })
    try {
      expect(wrapper.findAll('button button')).toHaveLength(0)
      expect(wrapper.findAll('button a')).toHaveLength(0)
      expect(wrapper.findAll('a button')).toHaveLength(0)
      expect(wrapper.findAll('a a')).toHaveLength(0)
      // 楼主标是纯文字徽标，留在触发按钮内（与昵称同基线）。
      expect(wrapper.get('button.sf-comment__author-trigger .sf-comment__op-badge').text()).toBe('楼主')
    } finally {
      wrapper.unmount()
    }
  })

  test('falls back to a link and renders the OP badge before the chip', () => {
    const wrapper = mountRow({ author: 'Bob', authorLink: '/u/bob', username: 'bob', isOpAuthor: true, topBadge: badge })
    try {
      // 非交互分支没有预览按钮（chip 自己仍是 button，这里只找触发按钮）。
      expect(wrapper.find('button.sf-comment__author-trigger').exists()).toBe(false)
      expect(wrapper.get('[data-testid="author-link"]').attributes('href')).toBe('/u/bob')
      const parts = wrapper.findAll('.sf-comment__author, .sf-comment__op-badge, [data-testid="chip"]')
        .map(node => node.classes().includes('sf-comment__op-badge') ? 'op' : node.attributes('data-testid') === 'chip' ? 'chip' : 'author')
      // 规格顺序：昵称 → 楼主 → tier chip。
      expect(parts).toEqual(['author', 'op', 'chip'])
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the chip outside the author link in the non-interactive branch', () => {
    const wrapper = mountRow({ author: 'Bob', authorLink: '/u/bob', username: 'bob', topBadge: badge })
    try {
      // 链接里不能嵌按钮：chip 必须是链接的兄弟节点，并带上 username。
      expect(wrapper.find('a [data-testid="chip"]').exists()).toBe(false)
      expect(wrapper.get('[data-testid="chip"]').attributes('data-username')).toBe('bob')
      expect(wrapper.get('[data-testid="author-link"]').attributes('href')).toBe('/u/bob')
    } finally {
      wrapper.unmount()
    }
  })

  test('degrades to plain text without a link and without preview support', () => {
    const wrapper = mountRow({ author: 'Carol' })
    try {
      const span = wrapper.get('span.sf-comment__author')
      expect(span.text()).toBe('Carol')
      expect(wrapper.find('[data-testid="chip"]').exists()).toBe(false)
      expect(wrapper.find('.sf-comment__op-badge').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  /**
   * 评论行永远传 `#name-row` 槽，所以身份组件 split 分支的 fallback（不传槽时的默认内容）
   * 只有直接挂真实组件才能走到。这条锁的是 fallback 的形状：名字链接 + name-suffix 同处
   * 名字行，`@username` 是名字行之外的独立资料页链接，且全程没有嵌套交互元素。
   */
  test('split variant falls back to a name link with the suffix in the name row and an independent handle link', () => {
    const wrapper = mount(SFUserIdentityName, {
      props: {
        user: { username: 'alice', displayName: 'Alice' },
        to: '/u/alice',
        variant: 'split'
      },
      slots: {
        'name-suffix': () => h('span', { class: 'suffix-marker', 'data-testid': 'suffix-marker' }, '徽章位')
      },
      global: { components: { NuxtLink: NuxtLinkStub } }
    })
    try {
      const root = wrapper.get('span.sf-user-identity-name')
      const nameLine = wrapper.get('.sf-user-identity-name__name-line')
      const nameLink = wrapper.get('.sf-user-identity-name__link--name')
      const handleLink = wrapper.get('a.sf-user-identity-name__handle-link')
      const handle = wrapper.get('.sf-user-identity-name__handle')
      const marker = wrapper.get('[data-testid="suffix-marker"]')

      expect(root.classes()).toContain('sf-user-identity-name--split')
      // 名字行 = 名字链接 + name-suffix，且后缀在名字链接之外（不把标记塞进链接）。
      expect(nameLine.element.contains(nameLink.element)).toBe(true)
      expect(nameLine.element.contains(marker.element)).toBe(true)
      expect(nameLink.element.contains(marker.element)).toBe(false)
      expect(nameLink.attributes('href')).toBe('/u/alice')
      expect(nameLink.text()).toBe('Alice')
      // @username 独立成行、自己是指向资料页的链接，不在名字行内。
      expect(handleLink.attributes('href')).toBe('/u/alice')
      expect(handle.text()).toBe('@alice')
      expect(nameLine.element.contains(handle.element)).toBe(false)
      expect(handle.element.closest('a')).toBe(handleLink.element)
      // 没有嵌套交互元素。
      expect(wrapper.findAll('button button')).toHaveLength(0)
      expect(wrapper.findAll('button a')).toHaveLength(0)
      expect(wrapper.findAll('a button')).toHaveLength(0)
      expect(wrapper.findAll('a a')).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })
})
