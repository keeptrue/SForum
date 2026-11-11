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
 * - chip 在预览按钮内必须是纯展示（不传 username）：按钮里不能再嵌套 `<a>`。
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

// 事件名避开 `capture`：它是原生事件阶段关键字，挂在组件上容易被当成原生监听。
// 运行时声明：capture 阶段要把原生事件原样交给父层（preventDefault / 记录 trigger）。
const emit = defineEmits(['previewToggle', 'previewCapture'])

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
    @click.capture="emit('previewCapture', $event)"
    @click="emit('previewToggle')"
  >
    <SFUserIdentityName
      :user="{ username: props.username, displayName: props.displayName }"
      :to="props.authorLink"
      :fallback="props.author"
    >
      <template #name-suffix>
        <span v-if="props.isOpAuthor" class="sf-comment__op-badge">{{ t('topicDetail.opBadge') }}</span>
        <SFBadgeChip v-if="props.topBadge" :badge="props.topBadge" />
      </template>
    </SFUserIdentityName>
  </button>
  <NuxtLink v-else-if="props.authorLink" :to="props.authorLink" class="sf-comment__author">
    {{ props.author }}
  </NuxtLink>
  <span v-else class="sf-comment__author">{{ props.author }}</span>
  <span v-if="props.isOpAuthor && !props.interactive" class="sf-comment__op-badge">
    {{ t('topicDetail.opBadge') }}
  </span>
</template>
