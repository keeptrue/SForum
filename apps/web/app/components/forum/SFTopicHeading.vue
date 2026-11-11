<script setup lang="ts">
import type { ForumTopicDetail, ForumTopicExtensionBadge } from '~/utils/forum/forumTaxonomy'
import { forumTopicExtensionLabel } from '~/utils/forum/forumTaxonomy'
import SFTopicAuthorBlock from '~/components/forum/SFTopicAuthorBlock.vue'

type TopicHeadingTag = {
  id: number
  name: string
  to: string
}

const props = defineProps<{
  topic: ForumTopicDetail
  authorName: string
  authorTo?: string
  categoryTo: string
  tags: TopicHeadingTag[]
  publishedLabel: string
  updatedLabel?: string
  /** forum.topic.badges；空数组/未传时不渲染扩展徽章区 */
  extensionBadges?: ForumTopicExtensionBadge[]
}>()

const { t, locale } = useI18n()
const localePath = useLocalePath()

const badges = computed(() => props.extensionBadges || [])

function badgeLabel(badge: ForumTopicExtensionBadge) {
  return forumTopicExtensionLabel(badge, String(locale.value || 'zh-CN')) || badge.id
}

function badgeHref(badge: ForumTopicExtensionBadge) {
  const href = `${badge.href || ''}`.trim()
  // 仅消费宿主已校验的站内相对路径；拒绝外链与 API。
  if (!href.startsWith('/') || href.startsWith('//') || href.includes('://') || href.startsWith('/api')) {
    return ''
  }
  return localePath(href)
}
</script>

<template>
  <header class="sf-topic-heading">
    <nav class="sf-topic-heading__breadcrumbs" :aria-label="t('nav.mainNav')">
      <NuxtLink :to="localePath('/')">{{ t('nav.home') }}</NuxtLink>
      <UIcon name="i-lucide-chevron-right" class="size-3.5" aria-hidden="true" />
      <NuxtLink :to="categoryTo">{{ topic.categoryName }}</NuxtLink>
    </nav>
    <NuxtLink :to="categoryTo" class="sf-topic-heading__taxonomy">{{ topic.categoryName }}</NuxtLink>
    <h1 class="sf-topic-heading__title">{{ topic.title }}</h1>

    <div
      v-if="badges.length"
      class="sf-topic-heading__extension-badges"
      data-testid="topic-extension-badges"
    >
      <template v-for="badge in badges" :key="`${badge.extensionId}:${badge.id}`">
        <NuxtLink
          v-if="badgeHref(badge)"
          :to="badgeHref(badge)"
          class="sf-topic-heading__extension-badge"
          :class="`sf-topic-heading__extension-badge--${badge.tone || 'neutral'}`"
        >
          {{ badgeLabel(badge) }}
        </NuxtLink>
        <span
          v-else
          class="sf-topic-heading__extension-badge"
          :class="`sf-topic-heading__extension-badge--${badge.tone || 'neutral'}`"
        >
          {{ badgeLabel(badge) }}
        </span>
      </template>
    </div>

    <div class="sf-topic-heading__byline">
      <SFTopicAuthorBlock
        class="sf-topic-heading__author"
        :author="topic.author"
        :name="authorName"
        :to="authorTo"
        :top-badge="topic.topBadge || null"
        :trust-level="topic.authorTrustLevel || null"
      />

      <time :datetime="topic.createdAt">{{ publishedLabel }}</time>
      <span v-if="topic.edited" class="sf-topic-heading__state">
        <UIcon name="i-lucide-pencil" class="size-3.5" aria-hidden="true" />
        <time v-if="topic.editedAt && updatedLabel" :datetime="topic.editedAt">{{ updatedLabel }}</time>
        <template v-else>{{ t('topicDetail.edited') }}</template>
      </span>
      <span class="sf-topic-heading__metric">
        {{ topic.commentCount }} {{ t('topicDetail.statsComments') }}
      </span>
      <span class="sf-topic-heading__metric">
        {{ topic.viewCount }} {{ t('topicDetail.statsViews') }}
      </span>
      <span v-if="topic.isPinned" class="sf-topic-heading__state">
        <UIcon name="i-lucide-pin" class="size-3.5" aria-hidden="true" />
        {{ t('topicDetail.badge.pinned') }}
      </span>
      <span v-if="topic.status === 'locked'" class="sf-topic-heading__state">
        <UIcon name="i-lucide-lock" class="size-3.5" aria-hidden="true" />
        {{ t('topicDetail.badge.locked') }}
      </span>
      <NuxtLink
        v-for="tag in tags"
        :key="tag.id"
        :to="tag.to"
        class="sf-topic-heading__tag"
      >
        {{ tag.name }}
      </NuxtLink>
    </div>
  </header>
</template>
