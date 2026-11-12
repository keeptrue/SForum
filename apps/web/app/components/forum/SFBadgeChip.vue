<script setup lang="ts">
/**
 * 作者旁的单个徽章 chip（纯展示，不取数）。
 *
 * 18px 高是硬约束：它要塞进昵称行盒（桌面约 17.9px、窄屏约 18.2px），有徽章和没徽章时
 * 作者行高度差必须为 0。因此唯一挂载点是 `SFUserIdentityName` 的 `#name-suffix` 槽，
 * 本组件不带 margin，间距全部由父级 `gap` 提供。
 *
 * **统一配色规则**（与徽章卡片同一套）：hue = 等级色，填充 = 同色 12%，环线 = 同色 34%；
 * 文字恒为 `--sf-fg` —— 11px 文字吃等级色在暗色下只有 3:1 左右，够图形不够正文。
 * 等级不再写死 gold/silver/bronze 三套 CSS：色值随载荷下发（`tierColor`），
 * 没有 `[data-tier]` 变体规则，也就没有「新等级没有样式」的空档。
 */
import {
  forumBadgeTier,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
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

const { t, te, locale } = useI18n()
const localePath = useLocalePath()

const tier = computed(() => forumBadgeTier(props.badge.tier))
const tierName = computed(() => forumBadgeTierLabel(props.badge, locale.value, t, te))
const tierStyle = computed(() => forumBadgeTierStyle(props.badge))
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
    :style="tierStyle"
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
    :style="tierStyle"
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
  /* 等级色成对下发（明/暗），这里按主题挑一个；文字恒为 --sf-fg。 */
  --sf-tier-hue: var(--sf-tier-hue-light);
  color: var(--sf-fg);
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
}

.dark .sf-badge-chip {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}

.sf-badge-chip__icon {
  width: 12px;
  height: 12px;
  flex: 0 0 auto;
  color: var(--sf-tier-hue);
}

.sf-badge-chip__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-badge-chip:hover,
.sf-badge-chip:focus-visible {
  border-color: color-mix(in srgb, var(--sf-tier-hue) 64%, transparent);
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
