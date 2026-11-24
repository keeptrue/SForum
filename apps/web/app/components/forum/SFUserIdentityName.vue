<script setup lang="ts">
import { shouldShowUserHandle, userDisplayName, type ForumUserRef } from '~/utils/forum/userDisplay'

/**
 * 统一身份呈现：展示名 + 可选小字 @username 双行模式。
 *
 * 展示名负责"认人"，@username 负责"唯一身份"；两者同指向资料页（调用方传 to）。
 * 窄屏经 CSS 隐藏 handle；不需要链接的场景（纯文本上下文）传 linked=false。
 * `name-suffix` 插槽渲染在昵称同一行（如「楼主」徽标），不会落到 handle 行。
 *
 * `variant` 决定两行之间的关系：
 * - `stacked`（默认）：两行同属一个链接，整块宽度取较宽的那一行。
 * - `split`：名字行与 @username 行是两个独立元素，名字行宽度只由「昵称 + 同行标记」决定。
 *   评论作者行需要它——徽章 chip 必须紧跟昵称，不能被更长的 @username 行撑到后面去。
 *   split 下名字行可整体交给调用方（`name-row` 槽，槽 prop 是 `displayName`），
 *   `name-suffix` 随之由调用方自行放置；不传槽时退回「名字链接 / 纯文本 + name-suffix」。
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
  /** 双行身份的行间关系，见文件头注释。 */
  variant?: 'stacked' | 'split'
}>(), {
  to: undefined,
  showHandle: true,
  linked: true,
  fallback: '',
  inline: false,
  variant: 'stacked'
})

const name = computed(() => userDisplayName(props.user, props.fallback))
const handle = computed(() => props.user?.username?.trim() || '')
// 与资料页同一口径：昵称与 username 同形时不再补一行重复的 @handle。
const showHandleLine = computed(() => props.showHandle && shouldShowUserHandle(name.value, handle.value))
const asLink = computed(() => props.linked && Boolean(props.to))
const isSplit = computed(() => props.variant === 'split')
</script>

<template>
  <span
    class="sf-user-identity-name"
    :class="{
      'sf-user-identity-name--inline': inline && !isSplit,
      'sf-user-identity-name--split': isSplit
    }"
  >
    <template v-if="isSplit">
      <!-- split：名字行独立，尾随元素（chip 等）与名字同处一行，不再被 handle 行撑宽。 -->
      <span class="sf-user-identity-name__name-line">
        <slot name="name-row" :display-name="name">
          <NuxtLink
            v-if="asLink"
            :to="to"
            class="sf-user-identity-name__link sf-user-identity-name__link--name"
          >
            <span class="sf-user-identity-name__name">{{ name }}</span>
          </NuxtLink>
          <span v-else class="sf-user-identity-name__name">{{ name }}</span>
          <slot name="name-suffix" />
        </slot>
      </span>
      <NuxtLink
        v-if="showHandleLine && asLink"
        :to="to"
        class="sf-user-identity-name__handle-link"
      >
        <span class="sf-user-identity-name__handle">@{{ handle }}</span>
      </NuxtLink>
      <span v-else-if="showHandleLine" class="sf-user-identity-name__handle">@{{ handle }}</span>
    </template>
    <template v-else-if="asLink">
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

/* split：名字行按内容收缩（不拉伸），handle 行另起一行。 */
.sf-user-identity-name--split {
  align-items: flex-start;
}

.sf-user-identity-name__link {
  display: inline-flex;
  flex-direction: column;
  min-width: 0;
  text-decoration: none;
}

/* split 下名字链接只包昵称，保持横排 */
.sf-user-identity-name__link--name {
  flex-direction: row;
  align-items: baseline;
  max-width: 100%;
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

/* split 的 handle 行是独立链接：不继承链接默认样式，宽度随 handle 收缩 */
.sf-user-identity-name__handle-link {
  display: inline-flex;
  min-width: 0;
  max-width: 100%;
  color: inherit;
  text-decoration: none;
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
