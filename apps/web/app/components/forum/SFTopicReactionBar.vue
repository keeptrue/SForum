<script setup lang="ts">
import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'
import SFReactionControl from '~/components/forum/SFReactionControl.vue'
/**
 * 主题正文下方的动作行：分享 / 回复 / 点赞。
 *
 * 从 SFTopicShowPage 抽出：点赞需要一个宿主位，而该文件已在架构基线上限，
 * 顺手把整行收进独立组件（净减行），并让点赞与分享/回复共用同一行几何。
 */
const props = withDefaults(defineProps<{
  topicId: number
  likeCount: number
  likedByMe?: boolean
  /** 点赞头像预览：自己优先、其余最早 */
  likedBy?: ForumUserSummary[]
  showReply?: boolean
  reactionsEnabled?: boolean
}>(), {
  likedByMe: false,
  likedBy: () => [],
  showReply: false,
  reactionsEnabled: true
})

const emit = defineEmits<{
  share: []
  reply: []
  change: [liked: boolean, count: number, likedBy: ForumUserSummary[]]
}>()

const { t } = useI18n()
</script>

<template>
  <div class="sforum-topic-page__actions">
    <button type="button" class="sforum-topic-page__action-btn" @click="emit('share')">
      <UIcon name="i-lucide-share-2" class="size-4" aria-hidden="true" />
      {{ t('topicDetail.share') }}
    </button>
    <button
      v-if="props.showReply"
      type="button"
      class="sforum-topic-page__action-btn sforum-topic-page__action-btn--primary"
      @click="emit('reply')"
    >
      <UIcon name="i-lucide-reply" class="size-4" aria-hidden="true" />
      {{ t('topicDetail.replyTopic') }}
    </button>
    <SFReactionControl
      target-type="topic"
      :target-id="props.topicId"
      :count="props.likeCount"
      :liked="props.likedByMe"
      :liked-by="props.likedBy"
      :enabled="props.reactionsEnabled"
      size="md"
      @change="(liked, count, likedBy) => emit('change', liked, count, likedBy)"
    />
  </div>
</template>
