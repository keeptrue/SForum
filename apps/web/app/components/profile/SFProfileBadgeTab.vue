<script setup lang="ts">
/**
 * 徽章墙容器，两个消费者共用同一份实现：
 *
 * - `variant="profile"`（默认）：资料页「徽章」tab，组内按 tier 降序，取数在 useProfileBadges。
 * - `variant="catalog"`：`/badges` 公共目录，组内按 position 升序，取数在 useBadgesCatalog。
 *
 * 单张卡已抽到 `components/forum/badges/SFBadgeCard.vue`（icon / 名称 / tier 标记 / earned 两态 /
 * 进度条 / aria-label 模板都在那边）。本文件只拥有**容器级**职责：分组 section、组内排序、
 * 组计数、空态 / 加载态 / 失败态、目录页内链，以及两个 variant 的这四处差异。
 * 再出现新消费者时，加 variant 或复用容器；不要把卡片逻辑搬回来。
 */
import SFBadgeCard from '~/components/forum/badges/SFBadgeCard.vue'
import {
  forumBadgeRequirementText,
  sortProfileBadgeEntries,
  sortProfileBadgeGroups,
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

const { t, locale } = useI18n()
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
        class="sf-profile-badge-card--skeleton"
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
          <SFBadgeCard
            v-for="badge in group.badges"
            :key="badge.slug"
            :badge="badge"
            :variant="props.variant"
            :description="forumBadgeRequirementText(badge, locale)"
          />
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

/* 骨架不是卡片实例（它没有 badge），基础形状在容器这里自己给，别去借卡片的样式。 */
.sf-profile-badge-card--skeleton {
  box-sizing: border-box;
  min-height: 64px;
  border: 1px solid transparent;
  border-radius: 12px;
  background: var(--sf-muted);
}
</style>
