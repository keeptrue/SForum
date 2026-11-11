<script setup lang="ts">
import type { ForumBadgeRef } from '~/utils/forum/forumBadge'
import { userDisplayName, type ForumUserRef } from '~/utils/forum/userDisplay'
import SFBadgeChip from '~/components/forum/SFBadgeChip.vue'

/**
 * 作者块：头像 + 昵称 + 最高级徽章 chip，主题标题行与评论行共用。
 *
 * chip 是 `<a>`，所以它必须排在作者链接**外面**：塞进链接会变成嵌套 anchor，
 * 点击行为与键盘焦点都不确定。因此结构是 `[链接: 头像+昵称][chip]`。
 * 不需要跳转的场景（评论预览按钮内）由调用方不传 `username`，chip 退化成 span。
 */
const props = withDefaults(defineProps<{
  author?: ForumUserRef
  /** 已解析好的展示名；缺省按 author 回退。 */
  name?: string
  to?: string
  topBadge?: ForumBadgeRef | null
  size?: 'xs' | 'sm'
  avatarLoading?: 'eager' | 'lazy'
}>(), {
  name: '',
  to: undefined,
  topBadge: null,
  size: 'sm',
  avatarLoading: 'eager'
})

const displayName = computed(() => props.name?.trim() || userDisplayName(props.author))
</script>

<template>
  <span class="sf-topic-author">
    <NuxtLink v-if="to" :to="to" class="sf-topic-author__link">
      <SFAvatar :name="displayName" :avatar="author?.avatar" :size="size" :loading="avatarLoading" />
      <span class="sf-topic-author__name">{{ displayName }}</span>
    </NuxtLink>
    <span v-else class="sf-topic-author__link">
      <SFAvatar :name="displayName" :avatar="author?.avatar" :size="size" :loading="avatarLoading" />
      <span class="sf-topic-author__name">{{ displayName }}</span>
    </span>
    <SFBadgeChip v-if="topBadge" :badge="topBadge" :username="author?.username || null" />
  </span>
</template>

<style scoped>
.sf-topic-author {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  min-width: 0;
}

.sf-topic-author__link {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  min-width: 0;
  color: inherit;
  text-decoration: none;
}

.sf-topic-author__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
