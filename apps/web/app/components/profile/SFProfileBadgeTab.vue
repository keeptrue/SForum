<script setup lang="ts">
/**
 * 徽章墙内容区，两个消费者共用同一份实现：
 *
 * - `variant="profile"`（默认）：资料页「徽章」tab，组内按 tier 降序，取数在 useProfileBadges。
 * - `variant="catalog"`：`/badges` 公共目录，组内按 position 升序，取数在 useBadgesCatalog。
 *
 * public catalog 是第二消费者；出现第三个消费者时必须把卡片抽成
 * `components/forum/badges/SFBadgeCard.vue`。现在不抽是为了不制造第二份实现，不是为了省事。
 *
 * 纯展示：取数在调用方，这里只渲染已获 / 未获两种状态与进度。
 * 已获卡与未获卡共用同一 DOM，只靠 `data-earned` 切换视觉，避免两套结构漂移。
 */
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTier,
  forumBadgeTierIcon,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
  type ProfileBadgeEntry,
  type ProfileBadgeGroup
} from '~/utils/forum/forumBadge'

type BadgeTabVariant = 'profile' | 'catalog'

const props = withDefaults(defineProps<{
  groups: ProfileBadgeGroup[]
  loading?: boolean
  failed?: boolean
  variant?: BadgeTabVariant
}>(), {
  loading: false,
  failed: false,
  variant: 'profile'
})

const emit = defineEmits<{ retry: [] }>()

const { t } = useI18n()
const { formatDateOnly } = useSiteDateTime()
const localePath = useLocalePath()

const isCatalog = computed(() => props.variant === 'catalog')

const sortedGroups = computed(() => sortProfileBadgeGroups(props.groups).map((group) => {
  // 目录页是「管理员编排的清单」，所见即 position；资料页是「我的荣誉」，按 tier 降序。
  const badges = isCatalog.value
    ? [...group.badges].sort((left, right) => left.position - right.position)
    : sortProfileBadgeEntries(group.badges)
  return {
    key: group.key,
    name: group.name,
    badges,
    earned: group.badges.filter(badge => badge.earned).length,
    total: group.badges.length
  }
}))

const hasAny = computed(() => sortedGroups.value.some(group => group.total > 0))
const skeletonCount = computed(() => (isCatalog.value ? 8 : 3))

function tierName(badge: ProfileBadgeEntry) {
  return t(`badges.tier.${forumBadgeTier(badge.tier)}`)
}

function earnedDate(badge: ProfileBadgeEntry) {
  return badge.grantedAt ? formatDateOnly(badge.grantedAt) : ''
}

function progressText(badge: ProfileBadgeEntry) {
  return t('badges.card.progress', { current: badge.current ?? 0, threshold: badge.threshold ?? 0 })
}

function cardLabel(badge: ProfileBadgeEntry) {
  if (badge.earned) {
    // catalog 的 wire 上没有 grantedAt（D37 的公开面白名单），所以不能复用带日期的模板，
    // 否则会读成「获得于 」（日期为空）。
    if (isCatalog.value) {
      return t('badges.catalog.ariaLabelEarned', { tier: tierName(badge), name: badge.name })
    }
    return t('badges.card.ariaLabel', {
      tier: tierName(badge),
      name: badge.name,
      date: earnedDate(badge)
    })
  }
  if (forumBadgeHasProgress(badge)) {
    return t('badges.card.ariaLabelUngranted', {
      tier: tierName(badge),
      name: badge.name,
      current: badge.current ?? 0,
      threshold: badge.threshold ?? 0
    })
  }
  // 未获得的手动徽章没有阈值：文案里不能出现进度，否则会读成「进度 0/0」。
  return t('badges.card.ariaLabelNotEarned', { tier: tierName(badge), name: badge.name })
}
</script>

<template>
  <section
    class="sf-profile-badges"
    :class="`sf-profile-badges--${props.variant}`"
    :aria-label="isCatalog ? t('badges.catalog.title') : t('profile.tabs.badges')"
  >
    <div
      v-if="props.loading"
      class="sf-badge-grid"
      :aria-busy="isCatalog ? 'true' : undefined"
      aria-hidden="true"
    >
      <div
        v-for="index in skeletonCount"
        :key="index"
        class="sf-profile-badge-card sf-profile-badge-card--skeleton"
      />
    </div>

    <p
      v-else-if="props.failed"
      class="sf-profile-badges__state"
      :role="isCatalog ? 'alert' : undefined"
    >
      {{ t('badges.loadFailed') }}
      <button type="button" class="sf-profile-badges__retry" @click="emit('retry')">
        <UIcon
          v-if="isCatalog"
          name="i-lucide-refresh-cw"
          class="sf-profile-badges__retry-icon"
          aria-hidden="true"
        />
        {{ t('badges.retry') }}
      </button>
    </p>

    <SFEmptyState
      v-else-if="!hasAny && isCatalog"
      :title="t('badges.catalog.emptyTitle')"
      :description="t('badges.catalog.emptyDescription')"
    />

    <p v-else-if="!hasAny" class="sf-profile-badges__state">{{ t('badges.empty') }}</p>

    <template v-else>
      <section v-for="group in sortedGroups" :key="group.key" class="sf-badge-group">
        <h2 class="sf-badge-group__title">
          <span class="sf-badge-group__name">{{ group.name }}</span>
          <span class="sf-badge-group__count">
            {{ isCatalog
              ? t('badges.catalog.groupTotal', { total: group.total })
              : t('badges.group.count', { earned: group.earned, total: group.total }) }}
          </span>
        </h2>

        <div class="sf-badge-grid">
          <article
            v-for="badge in group.badges"
            :key="badge.slug"
            class="sf-profile-badge-card"
            :class="{ 'sf-profile-badge-card--progress': forumBadgeHasProgress(badge) }"
            :data-tier="forumBadgeTier(badge.tier)"
            :data-earned="badge.earned ? 'true' : 'false'"
            :data-badge-slug="badge.slug"
            :aria-label="cardLabel(badge)"
          >
            <span class="sf-profile-badge-card__icon" aria-hidden="true">
              <UIcon :name="badge.icon || forumBadgeTierIcon(badge.tier)" class="sf-profile-badge-card__glyph" />
              <!-- 目录页的「已获得」勾章：形状线索，去色后仍与未获得可辨。 -->
              <span v-if="isCatalog && badge.earned" class="sf-profile-badge-card__seal">
                <UIcon name="i-lucide-check" class="sf-profile-badge-card__seal-glyph" />
              </span>
            </span>

            <div class="sf-profile-badge-card__body">
              <span class="sf-profile-badge-card__name">{{ badge.name }}</span>
              <span class="sf-profile-badge-card__meta">
                <span class="sf-profile-badge-card__tier">{{ tierName(badge) }}</span>
                <template v-if="badge.earned">
                  <span aria-hidden="true">·</span>
                  <span class="sf-profile-badge-card__date">
                    {{ isCatalog ? t('badges.catalog.earned') : t('badges.card.earnedOn', { date: earnedDate(badge) }) }}
                  </span>
                </template>
                <template v-else>
                  <span aria-hidden="true">·</span>
                  <span class="sf-profile-badge-card__date">{{ t('badges.card.notEarned') }}</span>
                </template>
              </span>

              <div v-if="forumBadgeHasProgress(badge)" class="sf-profile-badge-card__progress">
                <div
                  class="sf-profile-badge-card__track"
                  role="progressbar"
                  aria-valuemin="0"
                  :aria-valuenow="badge.current ?? 0"
                  :aria-valuemax="badge.threshold ?? 0"
                  :aria-valuetext="progressText(badge)"
                >
                  <span
                    class="sf-profile-badge-card__fill"
                    :style="{ width: `${forumBadgeProgressPercent(badge.current, badge.threshold)}%` }"
                  />
                </div>
                <span class="sf-profile-badge-card__progress-text">{{ progressText(badge) }}</span>
              </div>
            </div>
          </article>
        </div>
      </section>
    </template>

    <!-- 目录页的站内入口：资料页是唯一能发现徽章的地方，没有这条链就没有入口。 -->
    <div v-if="!isCatalog" class="sf-profile-badges__more">
      <NuxtLink :to="localePath('/badges')" class="sf-profile-badges__more-link">
        {{ t('badges.catalog.viewAll') }}
      </NuxtLink>
    </div>
  </section>
</template>

<style scoped>
.sf-profile-badges {
  display: block;
}

.sf-profile-badges__state {
  margin: 32px 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
}

.sf-profile-badges__retry {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: 8px;
  color: var(--sf-accent);
  font-size: 13px;
  text-decoration: underline;
}

.sf-profile-badges__retry:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-profile-badges__retry-icon {
  width: 14px;
  height: 14px;
}

.sf-profile-badges__more {
  margin-top: 20px;
}

.sf-profile-badges__more-link {
  color: var(--sf-accent);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
}

.sf-profile-badges__more-link:hover {
  text-decoration: underline;
}

.sf-profile-badges__more-link:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-badge-group + .sf-badge-group {
  margin-top: 28px;
}

.sf-badge-group:first-child {
  margin-top: 4px;
}

.sf-badge-group__title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0;
  color: var(--sf-fg);
  font-size: 15px;
  font-weight: 700;
  line-height: 1.3;
}

.sf-badge-group__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-badge-group__count {
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}

/* 单列 → 4 列：390 档硬塞两列会让名称与进度条一起挤坏，规范明确走单列。 */
.sf-badge-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 12px;
  margin: 10px 0 0;
}

@media (min-width: 768px) {
  .sf-badge-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1024px) {
  .sf-badge-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 1280px) {
  .sf-badge-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

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
  border: 1px solid var(--sf-badge-card-ring, var(--sf-border));
  border-radius: 10px;
  background: var(--sf-badge-card-soft, transparent);
  color: var(--sf-badge-card-hue, var(--sf-fg-tertiary));
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

/* tier 只染图标容器与环线；名称永远是 --sf-fg。 */
.sf-profile-badge-card[data-earned="true"][data-tier="gold"] {
  --sf-badge-card-hue: var(--sf-accent);
  --sf-badge-card-soft: var(--sf-accent-soft);
  --sf-badge-card-ring: color-mix(in srgb, var(--sf-accent) 34%, transparent);
  border-color: color-mix(in srgb, var(--sf-accent) 34%, transparent);
  background: var(--sf-accent-soft);
}

.sf-profile-badge-card[data-earned="true"][data-tier="silver"] {
  --sf-badge-card-hue: var(--sf-fg-secondary);
  --sf-badge-card-soft: transparent;
  --sf-badge-card-ring: var(--sf-public-border-strong);
}

.sf-profile-badge-card[data-earned="true"][data-tier="bronze"] {
  --sf-badge-card-hue: var(--sf-warning);
  --sf-badge-card-soft: var(--sf-warning-soft);
  --sf-badge-card-ring: color-mix(in srgb, var(--sf-warning) 45%, transparent);
}

/* 未获得：不降透明度，只把名称降到 secondary、图标降到 tertiary。 */
.sf-profile-badge-card[data-earned="false"] .sf-profile-badge-card__name {
  color: var(--sf-fg-secondary);
}

.dark .sf-profile-badge-card[data-earned="true"][data-tier="gold"] {
  --sf-badge-card-hue: var(--sf-accent-dark);
  --sf-badge-card-ring: color-mix(in srgb, var(--sf-accent-dark) 38%, transparent);
}

/* hover 反馈只属于资料页；目录页卡片不可点击，边框变色是假承诺。 */
.sf-profile-badges--profile .sf-profile-badge-card:hover {
  border-color: var(--sf-public-border-strong);
}

/* 目录页：虚线只表示「未获得」，tier 不改 border-style。 */
.sf-profile-badges--catalog .sf-profile-badge-card[data-earned="false"] {
  border-style: dashed;
}

.sf-profile-badge-card--skeleton {
  background: var(--sf-muted);
  border-color: transparent;
}
</style>
