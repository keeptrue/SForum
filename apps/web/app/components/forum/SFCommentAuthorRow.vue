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
 * 两个约束：
 * - 预览按钮内不能有链接（`button > a` 非法）：名字用 `linked=false` 渲染成纯文本，
 *   资料页链接只留在非 interactive 分支；chip 也挂在按钮/链接**外面**，点击只开徽章说明。
 * - 触发按钮的 DOM 节点经 `defineExpose` 暴露，父层做「点击外部关闭预览」判定时要用它。
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
  <button
    v-if="props.interactive"
    type="button"
    class="sf-comment__author sf-comment__author-trigger"
    aria-haspopup="dialog"
    :aria-expanded="props.previewOpen"
    :aria-controls="props.previewOpen ? props.previewId : undefined"
    @click="emit('previewToggle', $event)"
  >
    <SFUserIdentityName
      :user="{ username: props.username, displayName: props.displayName }"
      :to="props.authorLink"
      :fallback="props.author"
      :linked="false"
    >
      <template #name-suffix>
        <span v-if="props.isOpAuthor" class="sf-comment__op-badge">{{ t('topicDetail.opBadge') }}</span>
      </template>
    </SFUserIdentityName>
  </button>
  <NuxtLink v-else-if="props.authorLink" :to="props.authorLink" class="sf-comment__author">
    {{ props.author }}
  </NuxtLink>
  <span v-else class="sf-comment__author">{{ props.author }}</span>
  <!-- 徽章 chip：按钮/链接的兄弟节点，点击只开徽章说明，不触发用户预览。 -->
  <SFBadgeChip v-if="props.topBadge" :badge="props.topBadge" :username="props.username || null" />
  <span v-if="props.isOpAuthor && !props.interactive" class="sf-comment__op-badge">
    {{ t('topicDetail.opBadge') }}
  </span>
</template>
