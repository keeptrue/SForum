import { describe, expect, test } from 'bun:test'
import { computed, defineComponent } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 评论作者行（从 SFComment 抽出的独立组件）：
 * 预览按钮 / 链接 / 纯文本三种形态、楼主标、徽章 chip 的挂载位置与事件。
 */
const IdentityStub = defineComponent({
  name: 'SFUserIdentityName',
  props: { user: { type: Object, default: () => ({}) }, to: { type: String, default: '' }, fallback: { type: String, default: '' } },
  template: '<span data-testid="identity"><slot name="name-suffix" /></span>'
})

const ChipStub = defineComponent({
  name: 'SFBadgeChip',
  props: {
    badge: { type: Object, required: true },
    username: { type: [String, Object], default: null }
  },
  template: '<i data-testid="chip" :data-username="username" />'
})

const NuxtLinkStub = defineComponent({
  name: 'NuxtLink',
  props: { to: { type: String, default: '' } },
  template: '<a :href="to" data-testid="author-link"><slot /></a>'
})

const rowPath = fileURLToPath(new URL('../../app/components/forum/SFCommentAuthorRow.vue', import.meta.url))
const SFCommentAuthorRow = await compileVueSfc(rowPath, 'sf-comment-author-row', {
  computed,
  useI18n: () => ({ t: (key: string) => (key === 'topicDetail.opBadge' ? '楼主' : key) }),
  SFUserIdentityName: IdentityStub,
  SFBadgeChip: ChipStub
})

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
      interactive: true,
      previewOpen: true,
      previewId: 'preview-1',
      topBadge: { slug: 'regular', name: '常客', tier: 'bronze', icon: 'i-lucide-calendar' }
    })
    try {
      const button = wrapper.get('button.sf-comment__author')
      expect(button.attributes('aria-haspopup')).toBe('dialog')
      expect(button.attributes('aria-expanded')).toBe('true')
      expect(button.attributes('aria-controls')).toBe('preview-1')
      // chip 是按钮的兄弟节点：按钮里不能再嵌按钮，且 chip 需要拿到 username 才能打开弹窗。
      expect(wrapper.find('button [data-testid="chip"]').exists()).toBe(false)
      expect(wrapper.get('[data-testid="chip"]').attributes('data-username')).toBe('alice')
      // 楼主标仍在按钮内（它只是文字徽标，不产生交互）。
      expect(wrapper.get('button [data-testid="identity"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="author-link"]').exists()).toBe(false)

      await button.trigger('click')
      expect(wrapper.emitted('previewToggle')).toHaveLength(1)
      // 原生事件必须一起透传：父层要靠 target.closest 找回触发按钮（关闭预览后还焦）。
      expect(wrapper.emitted('previewToggle')?.[0]?.[0]).toBeInstanceOf(Event)
    } finally {
      wrapper.unmount()
    }
  })

  test('falls back to a link and renders the OP badge as a sibling', () => {
    const wrapper = mountRow({ author: 'Bob', authorLink: '/u/bob', isOpAuthor: true })
    try {
      expect(wrapper.find('button').exists()).toBe(false)
      expect(wrapper.get('[data-testid="author-link"]').attributes('href')).toBe('/u/bob')
      expect(wrapper.get('.sf-comment__op-badge').text()).toBe('楼主')
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps the chip outside the author link in the non-interactive branch', () => {
    const wrapper = mountRow({
      author: 'Bob',
      authorLink: '/u/bob',
      username: 'bob',
      topBadge: { slug: 'regular', name: '常客', tier: 'bronze', icon: 'i-lucide-calendar' }
    })
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
})
