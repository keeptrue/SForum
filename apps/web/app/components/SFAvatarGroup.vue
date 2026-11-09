<script setup lang="ts">
import type { AvatarView } from '~/composables/profile/useProfileApi'

export type AvatarGroupSize = '2xs' | 'xs' | 'list' | 'sm' | 'md' | 'lg'

export type SFAvatarGroupItem = {
  id: number | string
  name: string
  avatar?: AvatarView | null
  to?: string
}

const props = withDefaults(defineProps<{
  items: SFAvatarGroupItem[]
  /** 最多展示的头像数；超出显示 +N */
  max?: number
  /**
   * 去重后的总人数。可大于 items.length（API 已截断展示列表时）。
   * 省略时按 items.length 计算 overflow。
   */
  total?: number
  size?: AvatarGroupSize
  loading?: 'eager' | 'lazy'
  /** 整组可点击（如打开贡献时间线） */
  clickable?: boolean
  /** 组内头像装饰化：alt 置空、整组 aria-hidden，可读名只来自调用方的 aria-label */
  decorative?: boolean
  /** 是否渲染 +N chip；计数已在别处展示时关掉，避免重复信息 */
  showOverflow?: boolean
  ariaLabel?: string
}>(), {
  max: 5,
  total: undefined,
  size: 'md',
  loading: 'lazy',
  clickable: false,
  decorative: false,
  showOverflow: true,
  ariaLabel: undefined
})

const emit = defineEmits<{
  click: []
}>()

const visibleItems = computed(() => props.items.slice(0, Math.max(1, props.max)))
const overflowCount = computed(() => {
  const total = props.total != null && props.total > 0
    ? props.total
    : props.items.length
  return Math.max(0, total - visibleItems.value.length)
})
const showOverflowChip = computed(() => props.showOverflow && overflowCount.value > 0)

function onActivate() {
  if (props.clickable) {
    emit('click')
  }
}
</script>

<template>
  <component
    :is="clickable ? 'button' : 'div'"
    :type="clickable ? 'button' : undefined"
    class="sf-avatar-group"
    :class="[`sf-avatar-group--${size}`, { 'sf-avatar-group--clickable': clickable }]"
    :aria-label="ariaLabel"
    :aria-hidden="decorative ? 'true' : undefined"
    @click="onActivate"
  >
    <span
      v-for="(item, index) in visibleItems"
      :key="item.id"
      class="sf-avatar-group__item"
      :style="{ zIndex: visibleItems.length - index }"
    >
      <span class="sf-avatar-group__avatar">
        <SFAvatar
          :name="item.name"
          :avatar="item.avatar"
          :size="size"
          :loading="loading"
          :alt="decorative ? '' : item.name"
        />
      </span>
    </span>
    <span
      v-if="showOverflowChip"
      class="sf-avatar-group__more"
      :class="`sf-avatar-group__more--${size}`"
      aria-hidden="true"
    >
      +{{ overflowCount }}
    </span>
  </component>
</template>

<style scoped>
.sf-avatar-group {
  /* 叠放偏移、描边宽度与描边色按尺寸映射；调用方可在容器上覆盖这两个变量。 */
  --sf-avatar-group-overlap: -8px;
  --sf-avatar-ring-width: 2px;
  --sf-avatar-ring-color: var(--sf-card, #fff);
  --sf-avatar-group-more-gap: 6px;
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  padding: 0;
  margin: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
}

.sf-avatar-group--xs,
.sf-avatar-group--2xs {
  --sf-avatar-group-more-gap: 4px;
}

.sf-avatar-group--xs {
  --sf-avatar-group-overlap: -6px;
  --sf-avatar-ring-width: 1.5px;
}

.sf-avatar-group--2xs {
  --sf-avatar-group-overlap: -6px;
  --sf-avatar-ring-width: 1.5px;
}

.sf-avatar-group--clickable {
  cursor: pointer;
  border-radius: 999px;
}

.sf-avatar-group--clickable:hover .sf-avatar-group__avatar {
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--sf-accent, #f97316) 35%, transparent);
}

.sf-avatar-group--clickable:focus-visible {
  outline: 2px solid var(--sf-accent, #f97316);
  outline-offset: 3px;
}

.sf-avatar-group__item {
  position: relative;
  /* img 参与行盒基线会多出 6px descender：flex 化让叠放盒严格等于头像尺寸 */
  display: flex;
  margin-inline-start: var(--sf-avatar-group-overlap, -8px);
}

.sf-avatar-group__item:first-child {
  margin-inline-start: 0;
}

.sf-avatar-group__avatar {
  display: inline-flex;
  border-radius: 999px;
  box-shadow: 0 0 0 var(--sf-avatar-ring-width, 2px) var(--sf-avatar-ring-color, var(--sf-card, #fff));
}

.sf-avatar-group__more {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  margin-inline-start: var(--sf-avatar-group-more-gap, 6px);
  border-radius: 999px;
  background: var(--sf-public-surface-muted, #f1f5f9);
  color: var(--sf-public-text-secondary, #64748b);
  font-weight: 600;
  line-height: 1;
}

.sf-avatar-group__more--2xs {
  min-width: 18px;
  height: 18px;
  padding: 0 4px;
  font-size: 9px;
}

.sf-avatar-group__more--xs {
  min-width: 24px;
  height: 24px;
  padding: 0 5px;
  font-size: 11px;
  border: 1px solid var(--sf-public-border, #e9e8ed);
}

.sf-avatar-group__more--list {
  min-width: 36px;
  height: 36px;
  padding: 0 8px;
  font-size: 12px;
  border: 1px solid var(--sf-public-border, #e9e8ed);
}

.sf-avatar-group__more--sm {
  min-width: 32px;
  height: 32px;
  padding: 0 8px;
  font-size: 12px;
}

.sf-avatar-group__more--md {
  min-width: 40px;
  height: 40px;
  padding: 0 10px;
  font-size: 12px;
}

.sf-avatar-group__more--lg {
  min-width: 56px;
  height: 56px;
  padding: 0 12px;
  font-size: 14px;
}

@media (prefers-reduced-motion: reduce) {
  .sf-avatar-group,
  .sf-avatar-group__avatar,
  .sf-avatar-group__more {
    transition: none;
    animation: none;
  }
}
</style>
