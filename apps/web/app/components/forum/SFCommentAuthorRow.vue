<script setup lang="ts">
import type { ForumBadgeRef } from '~/utils/forum/forumBadge'
import SFBadgeChip from '~/components/forum/SFBadgeChip.vue'
import SFUserIdentityName from '~/components/forum/SFUserIdentityName.vue'

/**
 * 评论作者身份行：预览触发按钮（或链接 / 纯文本）+ 昵称 + 楼主标 + 最高级徽章 chip。
 *
 * 从 SFComment 抽出：作者行职责独立（谁写的、什么身份），与正文、动作行无关，可单独测；
 * SFComment 也因此回到 500 行以内。
 *
 * 三条约束：
 * - 预览按钮里不能有链接、也不能嵌 chip（`button > a` / `button > button` 都非法）。
 *   所以身份块用 `variant="split"`：名字行只装「昵称 + 楼主标」，chip 是名字行里的兄弟，
 *   `@username` 单独成行（它自己指向资料页）。名字行宽度只由昵称决定，chip 因此贴住昵称右缘，
 *   不会被更长的 `@username` 推到 handle 宽度之后。
 * - 触发按钮的 DOM 节点经稳定 class `sf-comment__author-trigger` 暴露：父层做「点击外部关闭
 *   预览」与焦点归还时用 `closest()` 找它，不跨组件拿节点。
 * - 名字文本由本组件的 `#name-row` 槽渲染（身份组件只提供名字行容器与 `@username` 行），
 *   昵称截断样式落在 `sforum-comment.css` 的 `.sf-comment__author-name` 上：
 *   槽内容属于调用方作用域，共享身份组件的 scoped 规则到不了这里。
 */
const props = withDefaults(defineProps<{
  author: string
  authorLink?: string
  username?: string
  displayName?: string
  topBadge?: ForumBadgeRef | null
  isOpAuthor?: boolean
  /** 有可预览身份时才渲染成按钮（与父层 canPreviewUser 同义）。 */
  interactive?: boolean
  previewOpen?: boolean
  previewId?: string
}>(), {
  authorLink: undefined,
  username: '',
  displayName: '',
  topBadge: null,
  isOpAuthor: false,
  interactive: false,
  previewOpen: false,
  previewId: undefined
})

const emit = defineEmits(['previewToggle'])

const { t } = useI18n()
</script>

<template>
  <SFUserIdentityName
    v-if="props.interactive"
    variant="split"
    class="sf-comment__author"
    :user="{ username: props.username, displayName: props.displayName }"
    :to="props.authorLink"
    :fallback="props.author"
  >
    <template #name-row="{ displayName }">
      <button
        type="button"
        class="sf-comment__author-trigger"
        aria-haspopup="dialog"
        :aria-expanded="props.previewOpen"
        :aria-controls="props.previewOpen ? props.previewId : undefined"
        @click="emit('previewToggle', $event)"
      >
        <span class="sf-comment__author-name">{{ displayName }}</span>
        <span v-if="props.isOpAuthor" class="sf-comment__op-badge">{{ t('topicDetail.opBadge') }}</span>
      </button>
      <!-- chip：名字行内、触发按钮外的兄弟节点，点击只开徽章说明，不触发用户预览。 -->
      <SFBadgeChip v-if="props.topBadge" :badge="props.topBadge" :username="props.username || null" />
    </template>
  </SFUserIdentityName>
  <template v-else>
    <NuxtLink v-if="props.authorLink" :to="props.authorLink" class="sf-comment__author">
      {{ props.author }}
    </NuxtLink>
    <span v-else class="sf-comment__author">{{ props.author }}</span>
    <span v-if="props.isOpAuthor" class="sf-comment__op-badge">{{ t('topicDetail.opBadge') }}</span>
    <SFBadgeChip v-if="props.topBadge" :badge="props.topBadge" :username="props.username || null" />
  </template>
</template>
