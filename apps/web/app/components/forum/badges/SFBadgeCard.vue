<script setup lang="ts">
/**
 * 单张徽章卡：资料页「徽章」tab 与 `/badges` 公共目录共用的同一份实现。
 *
 * 职责边界：这里只渲染一格（图标砖 / 名称 / tier 标记 / earned 两态 / 进度条 / aria-label 模板）。
 * 分组容器、组内排序、空态 / 加载态 / 失败态、以及两个 variant 的容器级差异，
 * 全部由 `profile/SFProfileBadgeTab.vue` 拥有——不要在卡片里加取数或分组逻辑。
 */
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  type ProfileBadgeEntry
} from '~/utils/forum/forumBadge'

type BadgeCardVariant = 'profile' | 'catalog'

const props = withDefaults(defineProps<{
  badge: ProfileBadgeEntry
  variant?: BadgeCardVariant
  /**
   * 目录 wire 带 description，但 designer 规范 §6 的卡片没有它的展示位；
   * 宿主当前不传（传了会改目录页视觉）。规范定稿后在这里接一行即可。
   */
  description?: string
}>(), {
  variant: 'profile',
  description: ''
})

const { t, te, locale } = useI18n()
const { formatDateOnly } = useSiteDateTime()

const isCatalog = computed(() => props.variant === 'catalog')

// 等级名/色都优先取载荷（运营可增删等级），缺失才回退内置三档文案与色。
const tierName = computed(() => forumBadgeTierLabel(props.badge, locale.value, t, te))
const tierStyle = computed(() => forumBadgeTierStyle(props.badge))

function earnedDate() {
  return props.badge.grantedAt ? formatDateOnly(props.badge.grantedAt) : ''
}

function progressText() {
  return t('badges.card.progress', { current: props.badge.current ?? 0, threshold: props.badge.threshold ?? 0 })
}

function cardLabel() {
  if (props.badge.earned) {
    // catalog 的 wire 上没有 grantedAt（D37 的公开面白名单），所以不能复用带日期的模板，
    // 否则会读成「获得于 」（日期为空）。
    if (isCatalog.value) {
      return t('badges.catalog.ariaLabelEarned', { tier: tierName.value, name: props.badge.name })
    }
    return t('badges.card.ariaLabel', {
      tier: tierName.value,
      name: props.badge.name,
      date: earnedDate()
    })
  }
  if (forumBadgeHasProgress(props.badge)) {
    return t('badges.card.ariaLabelUngranted', {
      tier: tierName.value,
      name: props.badge.name,
      current: props.badge.current ?? 0,
      threshold: props.badge.threshold ?? 0
    })
  }
  // 未获得的手动徽章没有阈值：文案里不能出现进度，否则会读成「进度 0/0」。
  return t('badges.card.ariaLabelNotEarned', { tier: tierName.value, name: props.badge.name })
}
</script>

<template>
  <article
    class="sf-profile-badge-card"
    :class="[
      `sf-profile-badge-card--${props.variant}`,
      { 'sf-profile-badge-card--progress': forumBadgeHasProgress(props.badge) }
    ]"
    :data-tier="forumBadgeTier(props.badge.tier)"
    :data-earned="props.badge.earned ? 'true' : 'false'"
    :data-badge-slug="props.badge.slug"
    :style="tierStyle"
    :aria-label="cardLabel()"
  >
    <span class="sf-profile-badge-card__icon" aria-hidden="true">
      <UIcon :name="props.badge.icon || forumBadgeTierIcon(props.badge.tier)" class="sf-profile-badge-card__glyph" />
      <!-- 目录页的「已获得」勾章：形状线索，去色后仍与未获得可辨。 -->
      <span v-if="isCatalog && props.badge.earned" class="sf-profile-badge-card__seal">
        <UIcon name="i-lucide-check" class="sf-profile-badge-card__seal-glyph" />
      </span>
    </span>

    <div class="sf-profile-badge-card__body">
      <span class="sf-profile-badge-card__name">{{ props.badge.name }}</span>
      <span v-if="props.description" class="sf-profile-badge-card__description">{{ props.description }}</span>
      <span class="sf-profile-badge-card__meta">
        <span class="sf-profile-badge-card__tier">{{ tierName }}</span>
        <template v-if="props.badge.earned">
          <span aria-hidden="true">·</span>
          <span class="sf-profile-badge-card__date">
            {{ isCatalog ? t('badges.catalog.earned') : t('badges.card.earnedOn', { date: earnedDate() }) }}
          </span>
        </template>
        <template v-else>
          <span aria-hidden="true">·</span>
          <span class="sf-profile-badge-card__date">{{ t('badges.card.notEarned') }}</span>
        </template>
      </span>

      <div v-if="forumBadgeHasProgress(props.badge)" class="sf-profile-badge-card__progress">
        <div
          class="sf-profile-badge-card__track"
          role="progressbar"
          aria-valuemin="0"
          :aria-valuenow="props.badge.current ?? 0"
          :aria-valuemax="props.badge.threshold ?? 0"
          :aria-valuetext="progressText()"
        >
          <span
            class="sf-profile-badge-card__fill"
            :style="{ width: `${forumBadgeProgressPercent(props.badge.current, props.badge.threshold)}%` }"
          />
        </div>
        <span class="sf-profile-badge-card__progress-text">{{ progressText() }}</span>
      </div>
    </div>
  </article>
</template>

<style scoped>
.sf-profile-badge-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-sizing: border-box;
  min-height: 64px;
  padding: 12px;
  border: 1px solid var(--sf-border);
  border-radius: 12px;
  background: var(--sf-card);
}

.sf-profile-badge-card--progress {
  min-height: 84px;
}

/* 单列档横排：图标左、文本右，信息密度不降。 */
@media (max-width: 767px) {
  .sf-profile-badge-card {
    flex-direction: row;
    align-items: center;
  }

  .sf-profile-badge-card__body {
    flex: 1 1 auto;
    min-width: 0;
  }
}

.sf-profile-badge-card__icon {
  position: relative;
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  color: var(--sf-tier-hue);
}

.sf-profile-badge-card__glyph {
  width: 18px;
  height: 18px;
}

/* 勾章绝对定位，不参与布局：有无勾章不改变卡片高度。 */
.sf-profile-badge-card__seal {
  position: absolute;
  top: -4px;
  right: -4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 12px;
  height: 12px;
  border-radius: 999px;
  background: var(--sf-accent);
  color: var(--sf-public-surface);
}

.sf-profile-badge-card__seal-glyph {
  width: 8px;
  height: 8px;
}

.sf-profile-badge-card__body {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sf-profile-badge-card__name {
  overflow: hidden;
  color: var(--sf-fg);
  font-size: 13px;
  font-weight: 700;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-profile-badge-card__description {
  overflow: hidden;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-profile-badge-card__meta {
  display: flex;
  align-items: center;
  gap: 4px;
  overflow: hidden;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  white-space: nowrap;
}

.sf-profile-badge-card__tier {
  color: var(--sf-fg-secondary);
  font-weight: 600;
}

.sf-profile-badge-card__date {
  overflow: hidden;
  font-variant-numeric: tabular-nums;
  text-overflow: ellipsis;
}

.sf-profile-badge-card__progress {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
}

.sf-profile-badge-card__track {
  flex: 1 1 auto;
  height: 4px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
}

.sf-profile-badge-card__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--sf-accent);
}

.sf-profile-badge-card__progress-text {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

/* 统一配色：hue = 等级色，填充 12%，环线 34%；名称永远是 --sf-fg。
   等级色成对下发（明/暗），这里按主题挑一个 —— 没有 [data-tier] 变体规则。 */
.sf-profile-badge-card {
  --sf-tier-hue: var(--sf-tier-hue-light);
}

.dark .sf-profile-badge-card {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}

.sf-profile-badge-card[data-earned="true"] {
  border-color: color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, var(--sf-card));
}

/* 未获得：不降透明度，只把名称降到 secondary、图标降到 tertiary。 */
.sf-profile-badge-card[data-earned="false"] .sf-profile-badge-card__name {
  color: var(--sf-fg-secondary);
}

/* hover 反馈只属于资料页；目录页卡片不可点击，边框变色是假承诺。 */
.sf-profile-badge-card--profile:hover {
  border-color: var(--sf-public-border-strong);
}

/* 目录页：虚线只表示「未获得」，tier 不改 border-style。 */
.sf-profile-badge-card--catalog[data-earned="false"] {
  border-style: dashed;
}
</style>
