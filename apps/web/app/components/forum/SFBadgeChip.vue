<script setup lang="ts">
/**
 * 作者旁的单个徽章 chip（纯展示，不取数）。
 *
 * 18px 高是硬约束：它要塞进昵称行盒（桌面约 17.9px、窄屏约 18.2px），有徽章和没徽章时
 * 作者行高度差必须为 0。因此唯一挂载点是 `SFUserIdentityName` 的 `#name-suffix` 槽，
 * 本组件不带 margin，间距全部由父级 `gap` 提供。
 *
 * tier 只染图标、环线与填充，文字永远是 `--sf-fg`：暗色下 `--sf-warning` 对 11px 文字
 * 只有约 3.5:1，够图形不够正文。tier 语义由 `aria-label` / `title` / `data-tier` 承担。
 */
import {
  forumBadgeTier,
  forumUserBadgesPath,
  type ForumBadgeRef
} from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  badge: ForumBadgeRef
  /** 资料页用户名；缺省时退化为纯展示 span（通知、后台表格等场景）。 */
  username?: string | null
}>(), {
  username: null
})

const { t } = useI18n()
const localePath = useLocalePath()

const tier = computed(() => forumBadgeTier(props.badge.tier))
const tierName = computed(() => t(`badges.tier.${tier.value}`))
const name = computed(() => props.badge.name?.trim() || props.badge.slug)
const icon = computed(() => props.badge.icon?.trim() || 'i-lucide-medal')
const label = computed(() => t('badges.chip.ariaLabel', { tier: tierName.value, name: name.value }))
const linkHint = computed(() => t('badges.chip.ariaLabelLink', { tier: tierName.value, name: name.value }))
const to = computed(() => {
  const path = forumUserBadgesPath(props.username)
  return path ? localePath(path) : null
})
</script>

<template>
  <NuxtLink
    v-if="to"
    :to="to"
    class="sf-badge-chip"
    :data-tier="tier"
    :data-badge-slug="badge.slug"
    :aria-label="label"
    :title="linkHint"
  >
    <UIcon :name="icon" class="sf-badge-chip__icon" aria-hidden="true" />
    <span class="sf-badge-chip__name">{{ name }}</span>
  </NuxtLink>
  <span
    v-else
    class="sf-badge-chip"
    role="img"
    :data-tier="tier"
    :data-badge-slug="badge.slug"
    :aria-label="label"
    :title="label"
  >
    <UIcon :name="icon" class="sf-badge-chip__icon" aria-hidden="true" />
    <span class="sf-badge-chip__name">{{ name }}</span>
  </span>
</template>

<style scoped>
.sf-badge-chip {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  height: 18px;
  max-width: 120px;
  padding: 0 6px 0 5px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  line-height: 18px;
  text-decoration: none;
  /* tier 变体只改这三个自定义属性，不复制规则 */
  color: var(--sf-fg);
  background: var(--sf-badge-chip-bg, transparent);
  border: 1px solid var(--sf-badge-chip-ring);
}

.sf-badge-chip__icon {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  color: var(--sf-badge-chip-hue);
}

.sf-badge-chip__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-badge-chip[data-tier="gold"] {
  --sf-badge-chip-hue: var(--sf-accent);
  --sf-badge-chip-bg: var(--sf-accent-soft);
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-accent) 34%, transparent);
}

.sf-badge-chip[data-tier="silver"] {
  --sf-badge-chip-hue: var(--sf-fg-secondary);
  --sf-badge-chip-bg: transparent;
  --sf-badge-chip-ring: var(--sf-public-border-strong);
}

.sf-badge-chip[data-tier="bronze"] {
  --sf-badge-chip-hue: var(--sf-warning);
  --sf-badge-chip-bg: transparent;
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-warning) 45%, transparent);
  border-style: dashed;
}

.dark .sf-badge-chip[data-tier="gold"] {
  --sf-badge-chip-hue: var(--sf-accent-dark);
  --sf-badge-chip-ring: color-mix(in srgb, var(--sf-accent-dark) 38%, transparent);
}

.sf-badge-chip:hover,
.sf-badge-chip:focus-visible {
  border-color: var(--sf-badge-chip-hue);
}

.sf-badge-chip:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

@media (max-width: 480px) {
  .sf-badge-chip {
    max-width: 96px;
  }
}
</style>
