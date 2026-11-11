<script setup lang="ts">
import type { ProfileActivity, ProfileActivityKind } from '~/composables/profile/useProfileApi'
import {
  groupProfileActivitiesByDate,
  profileActivityLink
} from '~/utils/profile/profileActivity'

/**
 * 资料页「主题 / 回复」tab 的内容区：资料统计摘要 + 分日时间线 + 加载更多。
 * 数据与取数在 `useProfileActivityFeeds`，这里只做渲染与「加载更多」触发，
 * 所以切 tab 缓存、SSR 首屏、错误提示的行为都不随组件边界改变。
 */
const props = defineProps<{
  kind: ProfileActivityKind
  items: ProfileActivity[]
  topicCount: number
  commentCount: number
  categoryCount: number
  /** 累计获赞（user_activity 快照，与主题数的实时 COUNT 语义不同）。 */
  likeReceivedCount?: number
  /** 累计访问天数（user_activity 快照）。 */
  daysVisited?: number
  /** 切 tab 后首屏取数中；与空列表同时成立才显示骨架。 */
  switching: boolean
  loadingMore: boolean
  showLoadMore: boolean
  /** 首屏渲染时刻，供「今天 / 昨天」分组使用（SSR 与客户端必须同一时刻）。 */
  renderedAt: number
}>()

const emit = defineEmits<{ loadMore: [] }>()

const { t, locale } = useI18n()
const localePath = useLocalePath()
const { seoSettings } = useWebOptions()
const { format, settings: dateTimeSettings } = useSiteDateTime()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

const activityGroups = computed(() => groupProfileActivitiesByDate(props.items, {
  settings: dateTimeSettings.value,
  locale: String(locale.value || 'zh-CN'),
  topicUrlMode: topicUrlMode.value,
  labels: {
    today: t('profile.timeline.today'),
    yesterday: t('profile.timeline.yesterday')
  },
  now: new Date(props.renderedAt)
}))

function formatDateTime(value: string) {
  return format(value)
}

function activityTo(activity: ProfileActivity) {
  return localePath(profileActivityLink(activity, topicUrlMode.value))
}
</script>

<template>
  <div class="sf-profile-summary sf-profile-summary--quint" aria-label="profile statistics">
    <div class="sf-profile-summary__item">
      <strong>{{ topicCount }}</strong>
      <span>{{ t('profile.topicCount') }}</span>
    </div>
    <div class="sf-profile-summary__item">
      <strong>{{ commentCount }}</strong>
      <span>{{ t('profile.commentCount') }}</span>
    </div>
    <div class="sf-profile-summary__item">
      <strong>{{ categoryCount }}</strong>
      <span>{{ t('profile.categoryCount') }}</span>
    </div>
    <div class="sf-profile-summary__item">
      <strong>{{ likeReceivedCount ?? 0 }}</strong>
      <span>{{ t('profile.likeReceivedCount') }}</span>
    </div>
    <div class="sf-profile-summary__item">
      <strong>{{ daysVisited ?? 0 }}</strong>
      <span>{{ t('profile.daysVisited') }}</span>
    </div>
  </div>

  <section class="sf-profile-section-head">
    <div>
      <h2>
        {{ kind === 'comment' ? t('profile.timeline.repliesTitle') : t('profile.timeline.title') }}
      </h2>
      <p>
        {{ kind === 'comment' ? t('profile.timeline.repliesDescription') : t('profile.timeline.description') }}
      </p>
    </div>
  </section>

  <div v-if="switching && !activityGroups.length" class="sf-profile-loading">
    <SFSkeleton :lines="4" />
  </div>

  <div v-else-if="activityGroups.length" class="sf-profile-timeline">
    <section v-for="group in activityGroups" :key="group.key" class="sf-profile-activity-day">
      <header class="sf-profile-activity-day__head">
        <h3>{{ group.label }}</h3>
        <time :datetime="group.key">{{ group.dateLabel }}</time>
      </header>
      <div class="sf-profile-activity-day__events">
        <NuxtLink
          v-for="activity in group.items"
          :key="`${activity.kind}:${activity.commentId || activity.topic.id}:${activity.createdAt}`"
          :to="activityTo(activity)"
          class="sf-profile-activity"
          :class="`sf-profile-activity--${activity.kind}`"
        >
          <span class="sf-profile-activity__icon" aria-hidden="true">
            <UIcon
              :name="activity.kind === 'topic' ? 'i-lucide-square-pen' : 'i-lucide-message-circle-reply'"
              class="size-4"
            />
          </span>
          <span class="sf-profile-activity__body">
            <span class="sf-profile-activity__type">
              {{ activity.kind === 'topic' ? t('profile.timeline.topicAction') : t('profile.timeline.commentAction') }}
            </span>
            <strong>{{ activity.topic.title }}</strong>
            <span v-if="activity.excerpt" class="sf-profile-activity__excerpt">{{ activity.excerpt }}</span>
            <span class="sf-profile-activity__meta">
              {{ activity.topic.categoryName }}
              <template v-if="activity.kind === 'topic'">
                · {{ t('profile.timeline.replyCount', { count: activity.topic.commentCount }) }}
              </template>
              <template v-else-if="activity.commentId">
                · #{{ activity.commentId }}
              </template>
            </span>
          </span>
          <time class="sf-profile-activity__time" :datetime="activity.createdAt">
            {{ activity.timeLabel || formatDateTime(activity.createdAt) }}
          </time>
        </NuxtLink>
      </div>
    </section>

    <div v-if="showLoadMore" class="sf-profile-load-more">
      <button
        type="button"
        class="sf-button sf-button--ghost sf-button--md sf-profile-load-more__button"
        :disabled="loadingMore"
        :aria-busy="loadingMore ? 'true' : undefined"
        @click="emit('loadMore')"
      >
        <UIcon
          :name="loadingMore ? 'i-lucide-loader-2' : 'i-lucide-chevrons-down'"
          class="size-4"
          :class="{ 'sf-profile-load-more__spinner': loadingMore }"
          aria-hidden="true"
        />
        {{ loadingMore ? t('profile.timeline.loadingMore') : t('profile.timeline.loadMore') }}
      </button>
    </div>
  </div>

  <div v-else class="sf-profile-empty">
    <UIcon name="i-lucide-message-square-off" class="size-7" aria-hidden="true" />
    <strong>
      {{ kind === 'comment' ? t('profile.timeline.emptyRepliesTitle') : t('profile.timeline.emptyTitle') }}
    </strong>
    <span>
      {{ kind === 'comment' ? t('profile.timeline.emptyRepliesDescription') : t('profile.timeline.emptyDescription') }}
    </span>
  </div>
</template>
