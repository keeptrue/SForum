import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, inject, nextTick, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import { fileURLToPath } from 'node:url'
import { unlinkSync } from 'node:fs'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import { commentBranchPresentation } from '~/utils/forum/forumCommentPresentation'
import { forumAuthorName } from '~/utils/forum/forumTaxonomy'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { sanitizeHtml } from '~/utils/sfSanitize'

const commentSourcePath = fileURLToPath(new URL('../../app/components/forum/SFComment.vue', import.meta.url))
// SFComment 另有一个普通 <script> 只放类型与 inject key；compileVueSfc 不支持带 export 的
// 双 script，这里在测试侧剥掉它，并把 key 作为 binding 注入。
const plainScript = /<script lang="ts">[\s\S]*?<\/script>\s*/
const commentSource = (await Bun.file(commentSourcePath).text()).replace(plainScript, '')
const commentPath = `/tmp/sf-comment-action-row-${process.pid}.vue`
await Bun.write(commentPath, commentSource)

// 只关心动作行结构：子组件全部桩掉，真实 SFComment 的 v-if 与 DOM 顺序仍是产品代码。
const Stub = { template: '<span />' }
// 点赞控件桩：把评论行传入的几何与数据落到 data-* 上，锁死「2xs + max=2 + 不画 chip」。
const ReactionStub = defineComponent({
  name: 'SFReactionControl',
  props: {
    targetType: { type: String, default: '' },
    targetId: { type: Number, default: 0 },
    count: { type: Number, default: 0 },
    liked: { type: Boolean, default: false },
    likedBy: { type: Array, default: () => [] },
    max: { type: Number, default: 0 },
    stackSize: { type: String, default: '' },
    chip: { type: Boolean, default: true },
    enabled: { type: Boolean, default: true },
    size: { type: String, default: '' }
  },
  template: `<div
    class="sf-reaction"
    :data-stack-size="stackSize"
    :data-max="max"
    :data-chip="String(chip)"
    :data-liked-by="likedBy.length"
    :data-count="count"
    :data-liked="String(liked)"
  />`
})

const reactionsEnabled = ref(true)

const SFComment = await compileVueSfc(commentPath, 'sf-comment-action-row', {
  computed,
  ref,
  inject,
  onMounted,
  onBeforeUnmount,
  nextTick,
  useId,
  useI18n: () => ({ t: (key: string) => key }),
  useWebOptions: () => ({ reactionsEnabled }),
  sanitizeHtml,
  forumAuthorName,
  userDisplayName,
  commentBranchPresentation,
  COMMENT_EDITOR_RENDERER_KEY: 'sforum-comment-editor-renderer',
  SFReactionControl: ReactionStub,
  SFMentionContent: Stub,
  SFCommentAIActivity: Stub,
  SFUserIdentityName: Stub,
  SFBadgeChip: Stub,
  SFCommentAuthorRow: Stub,
  SFCommentUserPreview: Stub,
  SFComment: Stub
})
unlinkSync(commentPath)

const comment = {
  id: 1,
  likeCount: 2,
  likedByMe: false,
  likedBy: [
    { id: 9, username: 'u9', displayName: '阿岚', avatar: { kind: 'initials', url: '', alt: '' } },
    { id: 12, username: 'u12', displayName: 'Kai', avatar: { kind: 'initials', url: '', alt: '' } }
  ],
  status: 'active',
  author: { id: 3, username: 'seed_qa', displayName: 'QA' },
  content: { htmlContent: '<p>hi</p>' }
}

function mountComment() {
  return mount(SFComment, {
    props: { comment, actions: [], author: 'QA' },
    global: {
      directives: { highlight: {} },
      stubs: {
        NuxtLink: { template: '<a><slot /></a>' },
        SFAvatar: true,
        UDropdownMenu: true,
        UIcon: true,
        SFReactionControl: ReactionStub
      }
    }
  })
}

describe('comment action row visibility', () => {
  test('renders the row with the reaction control first when actions are empty', () => {
    reactionsEnabled.value = true
    const wrapper = mountComment()
    try {
      const row = wrapper.get('.sf-comment__actions')
      expect(row.element.firstElementChild?.className).toContain('sf-reaction')
    } finally {
      wrapper.unmount()
    }
  })

  test('passes the compact 2xs stack, max=2, no chip, and the comment likedBy preview', () => {
    reactionsEnabled.value = true
    const wrapper = mountComment()
    try {
      const control = wrapper.get('.sf-comment__actions .sf-reaction')
      expect(control.attributes('data-stack-size')).toBe('2xs')
      expect(control.attributes('data-max')).toBe('2')
      // 评论行计数就在心形旁：chip 是重复信息
      expect(control.attributes('data-chip')).toBe('false')
      expect(control.attributes('data-liked-by')).toBe('2')
      expect(control.attributes('data-count')).toBe('2')
    } finally {
      wrapper.unmount()
    }
  })

  test('hides the row when reactions are disabled and there are no actions', () => {
    reactionsEnabled.value = false
    const wrapper = mountComment()
    try {
      expect(wrapper.find('.sf-comment__actions').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
