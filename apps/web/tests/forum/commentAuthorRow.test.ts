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
  props: { badge: { type: Object, required: true } },
  template: '<i data-testid="chip" />'
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
      // chip 挂在 name-suffix 槽里（与楼主标同一行），预览按钮内不嵌套链接。
      expect(wrapper.get('[data-testid="identity"] [data-testid="chip"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="author-link"]').exists()).toBe(false)

      await button.trigger('click')
      expect(wrapper.emitted('previewToggle')).toHaveLength(1)
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
