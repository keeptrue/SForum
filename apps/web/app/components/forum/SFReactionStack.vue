<script setup lang="ts">
/**
 * 点赞头像叠放 + 触发器（纯展示）。
 *
 * 首屏就用父层已有的 likedBy 渲染，零请求：点开才由 SFReactionRoster 拉完整名单。
 * 预览为空但计数大于 0 时，只画 +N 兜底，不再退化成一串文字。
 * 可读名只在触发器的 aria-label 上；组内头像与 chip 全部装饰化。
 */
import type { AvatarGroupSize } from '~/components/SFAvatarGroup.vue'
import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'
import { forumReactionStackOverflow } from '~/utils/forum/forumReaction'

const props = withDefaults(defineProps<{
  items: ForumUserSummary[]
  total: number
  max?: number
  size?: AvatarGroupSize
  /** 是否渲染 +N chip：评论行计数就在心形旁，chip 是重复信息 */
  chip?: boolean
}>(), {
  max: 3,
  size: 'xs',
  chip: true
})

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()

const visibleItems = computed(() => props.items.slice(0, Math.max(1, props.max)))
const overflowCount = computed(() => forumReactionStackOverflow(props.total, visibleItems.value.length))
const groupItems = computed(() => visibleItems.value.map(item => ({
  id: item.id,
  name: item.displayName || item.username,
  avatar: item.avatar
})))
const label = computed(() => t('forum.reaction.rosterAria', { count: Math.max(0, props.total) }))
const visible = computed(() => props.total > 0 && (visibleItems.value.length > 0 || props.chip))
</script>

<template>
  <button
    v-if="visible"
    type="button"
    class="sf-reaction-stack"
    :aria-label="label"
    aria-haspopup="dialog"
    @click="emit('open')"
  >
    <SFAvatarGroup
      :items="groupItems"
      :total="visibleItems.length + overflowCount"
      :max="props.max"
      :size="props.size"
      :show-overflow="props.chip"
      decorative
    />
  </button>
</template>

<style scoped>
.sf-reaction-stack {
  display: inline-flex;
  align-items: center;
  /* 36→24 后内容只有 28px：min-height 38 会把动作行从 59 压到 57，
     所以触发器最小高度提到 40，热区只增不减，行几何保持原样。 */
  min-height: 40px;
  padding: 2px;
  margin: 0;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: inherit;
  font: inherit;
  cursor: pointer;
}

.sf-reaction-stack:focus-visible {
  outline: 2px solid var(--sf-accent, #0f766e);
  outline-offset: 2px;
}

@media (prefers-reduced-motion: reduce) {
  .sf-reaction-stack {
    transition: none;
    animation: none;
  }
}
</style>
