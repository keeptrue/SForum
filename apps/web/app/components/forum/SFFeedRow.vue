<script setup lang="ts">
import type { AvatarView } from '~/composables/profile/useProfileApi'

type FeedBadge = {
  label: string
  variant?: 'neutral' | 'primary' | 'info' | 'success' | 'warning' | 'danger'
}

const props = withDefaults(defineProps<{
  title: string
  excerpt?: string
  author?: string
  avatar?: AvatarView | null
  meta?: string
  score?: number
  replies?: number
  views?: number
  badges?: FeedBadge[]
  layout?: 'compact' | 'table'
  lastActivityLabel?: string
  lastActor?: string
  lastActorAvatar?: AvatarView | null
  showAvatar?: boolean
}>(), {
  excerpt: undefined,
  author: undefined,
  avatar: undefined,
  meta: undefined,
  score: 0,
  replies: 0,
  views: 0,
  badges: () => [],
  layout: 'compact',
  lastActivityLabel: undefined,
  lastActor: undefined,
  lastActorAvatar: undefined,
  showAvatar: true
})

const rowClass = computed(() => [
  'sf-feed-row',
  props.layout === 'table' ? 'sf-feed-row--table' : 'sf-feed-row--compact'
].join(' '))

const { t } = useI18n()

const resolvedLastActor = computed(() => props.lastActor || props.author || '')
const resolvedLastActivity = computed(() => props.lastActivityLabel || props.meta || '')
</script>

<template>
  <article :class="rowClass">
    <div v-if="showAvatar" class="sf-feed-row__avatar-wrapper">
      <SFAvatar :name="author || '?'" :avatar="avatar" size="sm" />
    </div>

    <div class="sf-feed-row__content">
      <div class="sf-feed-row__header">
        <h3 class="sf-feed-row__title">
          {{ title }}
        </h3>
        <div v-if="layout === 'compact'" class="sf-feed-row__actions">
          <div class="sf-feed-row__vote">
            <button class="sf-feed-row__vote-btn" :aria-label="t('forum.feed.upvote')" :title="t('forum.feed.upvote')">
              <UIcon name="i-lucide-chevron-up" class="size-3.5" />
            </button>
            <span class="sf-feed-row__vote-val">{{ score }}</span>
            <button class="sf-feed-row__vote-btn" :aria-label="t('forum.feed.downvote')" :title="t('forum.feed.downvote')">
              <UIcon name="i-lucide-chevron-down" class="size-3.5" />
            </button>
          </div>
          <div class="sf-feed-row__action-tag">
            <UIcon name="i-lucide-message-circle" class="size-3.5" />
            {{ replies }}
          </div>
        </div>
      </div>

      <div class="sf-feed-row__meta-row">
        <span v-if="excerpt && layout === 'compact'" class="sf-feed-row__excerpt">{{ excerpt }}</span>
        <span v-if="badges.length" class="sf-feed-row__badges">
          <SFBadge
            v-for="badge in badges"
            :key="badge.label"
            :variant="badge.variant || 'neutral'"
          >
            {{ badge.label }}
          </SFBadge>
        </span>
        <span v-if="author" class="sf-feed-row__author">{{ author }}</span>
        <span v-if="meta" class="sf-feed-row__time">• {{ meta }}</span>
        <span v-if="views && layout === 'compact'" class="sf-feed-row__views">
          <UIcon name="i-lucide-eye" class="size-3.5" />
          {{ t('forum.feed.viewsWithCount', { count: views }) }}
        </span>
      </div>
    </div>

    <template v-if="layout === 'table'">
      <div class="sf-feed-row__stat sf-feed-row__stat--replies" :aria-label="t('forum.feed.repliesLabel')">
        <span class="sf-feed-row__stat-value">{{ replies }}</span>
        <span class="sf-feed-row__stat-label">{{ t('forum.feed.replies') }}</span>
      </div>
      <div class="sf-feed-row__stat sf-feed-row__stat--views" :aria-label="t('forum.feed.viewsLabel')">
        <span class="sf-feed-row__stat-value">{{ views }}</span>
        <span class="sf-feed-row__stat-label">{{ t('forum.feed.views') }}</span>
      </div>
      <div class="sf-feed-row__last-activity">
        <SFAvatar v-if="resolvedLastActor" :name="resolvedLastActor" :avatar="lastActorAvatar || avatar" size="sm" />
        <span class="sf-feed-row__last-copy">
          <span v-if="resolvedLastActor" class="sf-feed-row__last-actor">{{ resolvedLastActor }}</span>
          <span v-if="resolvedLastActivity" class="sf-feed-row__last-time">{{ resolvedLastActivity }}</span>
        </span>
      </div>
    </template>
  </article>
</template>
