<script setup lang="ts">
import { shouldShowUserHandle, userDisplayName, type ForumUserRef } from '~/utils/forum/userDisplay'

/**
 * 统一身份呈现：展示名 + 可选小字 @username 双行模式。
 *
 * 展示名负责"认人"，@username 负责"唯一身份"；两者同指向资料页（调用方传 to）。
 * 窄屏经 CSS 隐藏 handle；不需要链接的场景（纯文本上下文）传 linked=false。
 * `name-suffix` 插槽渲染在昵称同一行（如「楼主」徽标），不会落到 handle 行。
 */
const props = withDefaults(defineProps<{
  user: ForumUserRef
  /** 资料页地址；提供时名字与 handle 都是链接。 */
  to?: string
  /** 是否展示 @username 小字；评论行等空间充裕的场景默认展示。 */
  showHandle?: boolean
  /** 渲染为纯文本（不可点）的上下文，如通知头像旁的 alt。 */
  linked?: boolean
  /** user 为空时的回退文案。 */
  fallback?: string
  /** 单行模式：昵称与 @username 横排（宿主确实需要单行紧凑时）；默认双行。 */
  inline?: boolean
}>(), {
  to: undefined,
  showHandle: true,
  linked: true,
  fallback: '',
  inline: false
})

const name = computed(() => userDisplayName(props.user, props.fallback))
const handle = computed(() => props.user?.username?.trim() || '')
// 与资料页同一口径：昵称与 username 同形时不再补一行重复的 @handle。
const showHandleLine = computed(() => props.showHandle && shouldShowUserHandle(name.value, handle.value))
const asLink = computed(() => props.linked && Boolean(props.to))
</script>

<template>
  <span class="sf-user-identity-name" :class="{ 'sf-user-identity-name--inline': inline }">
    <template v-if="asLink">
      <NuxtLink :to="to" class="sf-user-identity-name__link">
        <span class="sf-user-identity-name__name-line">
          <span class="sf-user-identity-name__name">{{ name }}</span>
          <slot name="name-suffix" />
        </span>
        <span v-if="showHandleLine" class="sf-user-identity-name__handle">@{{ handle }}</span>
      </NuxtLink>
    </template>
    <template v-else>
      <span class="sf-user-identity-name__name-line">
        <span class="sf-user-identity-name__name">{{ name }}</span>
        <slot name="name-suffix" />
      </span>
      <span v-if="showHandleLine" class="sf-user-identity-name__handle">@{{ handle }}</span>
    </template>
  </span>
</template>

<style scoped>
.sf-user-identity-name {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  line-height: 1.3;
}

.sf-user-identity-name--inline {
  flex-direction: row;
  align-items: baseline;
  gap: 5px;
  max-width: 100%;
}

.sf-user-identity-name__link {
  display: inline-flex;
  flex-direction: column;
  min-width: 0;
  text-decoration: none;
}

/* 昵称行：昵称与同行后缀（如楼主徽标）一起限宽，handle 仍在下一行 */
.sf-user-identity-name__name-line {
  display: inline-flex;
  align-items: baseline;
  gap: 5px;
  min-width: 0;
  max-width: 100%;
}

/* 单行模式下 link 也保持横排，昵称与 handle 同一行 */
.sf-user-identity-name--inline .sf-user-identity-name__link {
  flex-direction: row;
  align-items: baseline;
  gap: 5px;
  max-width: 100%;
}

/* 字号/配色交给宿主上下文：默认值只保证独立使用时可读。 */
.sf-user-identity-name__name {
  flex: 0 1 auto;
  min-width: 0;
  font-weight: inherit;
  color: inherit;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-user-identity-name__link:hover .sf-user-identity-name__name,
.sf-user-identity-name__link:focus-visible .sf-user-identity-name__name {
  color: var(--sf-accent, currentColor);
}

.sf-user-identity-name__handle {
  color: var(--sf-public-text-muted, rgb(from currentColor r g b / 0.6));
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 640px) {
  .sf-user-identity-name__handle {
    display: none;
  }
}

/* 单行模式 handle：小号弱化、可压缩截断，窄屏不隐藏（由宿主 flex-wrap 兜底） */
.sf-user-identity-name--inline .sf-user-identity-name__handle {
  font-size: 0.72em;
  font-weight: 400;
  letter-spacing: 0.01em;
  flex: 0 1 auto;
  min-width: 3ch;
  opacity: 0.85;
}
</style>
