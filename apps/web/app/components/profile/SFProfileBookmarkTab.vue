<script setup lang="ts">
import type { BookmarkEntry } from '~/composables/forum/useBookmark'
import {
  bookmarkEmptyStateCopy,
  bookmarkRemindStatusKey,
  bookmarkTargetTypeIcon,
  bookmarkTargetTypeLabelKey,
  formatBookmarkRemindAt
} from '~/utils/forum/forumBookmark'
import { forumTopicPath } from '~/utils/forum/forumTaxonomy'

/**
 * 资料页「收藏」tab 的内容区：混合流（主题 + 评论）按收藏时间倒序。
 *
 * 数据与分页在 useProfileBookmarks；这里只做渲染与「加载更多 / 重试」触发。
 * 私密行只可能出现在本人视角（服务端裁剪），所以锁标记也只按 self 渲染。
 */
const props = defineProps<{
  items: BookmarkEntry[]
  self: boolean
  loading: boolean
  loadingMore: boolean
  hasMore: boolean
  failed: boolean
}>()

const emit = defineEmits<{ loadMore: []; retry: [] }>()

const { t, locale } = useI18n()
const localePath = useLocalePath()
const { seoSettings } = useWebOptions()
const { format, settings: dateTimeSettings } = useSiteDateTime()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

const emptyCopy = computed(() => bookmarkEmptyStateCopy(props.self ? 'self' : 'public'))

// 评论目标只带 id/topicId（后端 BookmarkCommentRef 没有标题），按 #id 呈现，
// 类型徽标已经说明它是评论；不虚构标题。
function entryTitle(entry: BookmarkEntry): string {
  if (entry.targetType === 'topic') {
    return entry.topic?.title || `#${entry.targetId}`
  }
  return `#${entry.comment?.id ?? entry.targetId}`
}

function entryExcerpt(entry: BookmarkEntry): string {
  return entry.targetType === 'topic' ? (entry.topic?.excerpt || '') : ''
}

function entryMeta(entry: BookmarkEntry): string {
  if (entry.targetType === 'topic') {
    return entry.topic?.categoryName || ''
  }
  return entry.comment?.id ? `#${entry.comment.id}` : ''
}

/** 没有足够字段拼深链时返回 null：宁可不跳，也不跳到错误的主题上。 */
function entryLink(entry: BookmarkEntry): string | null {
  if (entry.targetType === 'topic' && entry.topic) {
    return localePath(forumTopicPath({ id: entry.topic.id, slug: entry.topic.slug || '' }, topicUrlMode.value))
  }
  // 评论行没有主题 slug：固定用 id 形态的深链，避免拼出 /t/34/ 这种空 slug。
  const comment = entry.comment
  if (comment?.topicId) {
    const base = localePath(forumTopicPath({ id: comment.topicId, slug: '' }, 'id'))
    return comment.id ? `${base}#comment-${comment.id}` : base
  }
  return null
}

function bookmarkedAt(entry: BookmarkEntry): string {
  return format(entry.createdAt)
}

/**
 * 提醒 chip 的 title：把具体时刻挂在悬停上，列表本身保持一行。
 *
 * 只有本人视角才有提醒可言：后端已按 viewer 裁剪，这里再兜一层，
 * 他人视角即使意外收到 remindAt 也不泄露时刻。
 */
function remindTitle(entry: BookmarkEntry): string {
  if (!props.self) {
    return ''
  }
  const stamp = entry.remindedAt || entry.remindAt
  return stamp ? formatBookmarkRemindAt(stamp, dateTimeSettings.value, String(locale.value || 'zh-CN')) : ''
}
</script>

<template>
  <section class="sf-profile-bookmarks" :aria-label="t('bookmark.profileTab.title')">
    <section class="sf-profile-section-head">
      <div>
        <h2>{{ t('bookmark.profileTab.title') }}</h2>
        <p>{{ t('bookmark.profileTab.description') }}</p>
      </div>
    </section>

    <p v-if="self" class="sf-profile-bookmarks__notice">
      <UIcon name="i-lucide-lock" class="size-3.5" aria-hidden="true" />
      {{ t('bookmark.profileTab.privateNotice') }}
    </p>

    <div v-if="loading && !items.length" class="sf-profile-loading">
      <SFSkeleton :lines="4" />
    </div>

    <div v-else-if="failed && !items.length" class="sf-profile-empty">
      <UIcon name="i-lucide-alert-triangle" class="size-7" aria-hidden="true" />
      <strong>{{ t('bookmark.profileTab.loadFailed') }}</strong>
      <button type="button" class="sf-button sf-button--ghost sf-button--md" @click="emit('retry')">
        {{ t('common.retry') }}
      </button>
    </div>

    <ul v-else-if="items.length" class="sf-profile-bookmarks__list">
      <li v-for="entry in items" :key="`${entry.targetType}:${entry.targetId}`" class="sf-profile-bookmark">
        <div class="sf-profile-bookmark__row">
          <!-- 整行可点：用覆盖式链接而不是包一层 <a>，正文仍可正常选中与读屏 -->
          <NuxtLink
            v-if="entryLink(entry)"
            :to="entryLink(entry)!"
            class="sf-profile-bookmark__overlay"
          >
            <span class="sr-only">{{ entryTitle(entry) }}</span>
          </NuxtLink>
          <span class="sf-profile-bookmark__badge" :class="`sf-profile-bookmark__badge--${entry.targetType}`">
            <UIcon :name="bookmarkTargetTypeIcon(entry.targetType)" class="size-3.5" aria-hidden="true" />
            {{ t(bookmarkTargetTypeLabelKey(entry.targetType)) }}
          </span>
          <span class="sf-profile-bookmark__body">
            <strong>{{ entryTitle(entry) }}</strong>
            <span v-if="entryExcerpt(entry)" class="sf-profile-bookmark__excerpt">{{ entryExcerpt(entry) }}</span>
            <span class="sf-profile-bookmark__meta">
              <template v-if="entryMeta(entry)">{{ entryMeta(entry) }} · </template>
              {{ bookmarkedAt(entry) }}
            </span>
            <!-- 已提醒 / 将提醒分开：有 remindedAt 才是真的响过；提醒是私域，只在本人视角出现 -->
            <span
              v-if="self && (entry.remindedAt || entry.remindAt)"
              class="sf-profile-bookmark__remind"
              :class="{ 'is-done': Boolean(entry.remindedAt) }"
              :title="remindTitle(entry)"
            >
              <UIcon :name="entry.remindedAt ? 'i-lucide-bell-ring' : 'i-lucide-bell'" class="size-3" aria-hidden="true" />
              {{ t(bookmarkRemindStatusKey(entry.remindedAt)) }}
            </span>
          </span>
          <span
            v-if="entry.visibility === 'private'"
            class="sf-profile-bookmark__lock"
            :title="t('bookmark.visibility.private')"
          >
            <UIcon name="i-lucide-lock" class="size-3.5" aria-hidden="true" />
            <span class="sr-only">{{ t('bookmark.visibility.private') }}</span>
          </span>
        </div>
      </li>
    </ul>

    <div v-else class="sf-profile-empty">
      <UIcon :name="emptyCopy.icon" class="size-7" aria-hidden="true" />
      <strong>{{ t(emptyCopy.title) }}</strong>
      <span>{{ t(emptyCopy.description) }}</span>
      <NuxtLink v-if="self" :to="localePath('/')" class="sf-button sf-button--ghost sf-button--md">
        {{ t('bookmark.empty.cta') }}
      </NuxtLink>
    </div>

    <div v-if="hasMore" class="sf-profile-load-more">
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
        {{ loadingMore ? t('bookmark.profileTab.loadingMore') : t('bookmark.profileTab.loadMore') }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.sf-profile-bookmarks__notice {
  display: flex;
  align-items: center;
  gap: 0.375rem;
  margin: 0 0 0.75rem;
  font-size: 0.8125rem;
  color: var(--sf-public-muted, #64748b);
}

.sf-profile-bookmarks__list {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.sf-profile-bookmark__row {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 0.625rem;
  padding: 0.625rem 0.75rem;
  border: 1px solid var(--sf-border-light, #e2e8f0);
  border-radius: 0.5rem;
  min-width: 0;
}

.sf-profile-bookmark__overlay {
  position: absolute;
  inset: 0;
  z-index: 1;
  border-radius: inherit;
  text-decoration: none;
}

.sf-profile-bookmark__row:hover {
  background: color-mix(in srgb, currentColor 5%, transparent);
}

.sf-profile-bookmark__overlay:focus-visible {
  outline: 2px solid var(--sf-public-accent, #e11d48);
  outline-offset: 2px;
}

.sf-profile-bookmark__badge {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  flex: 0 0 auto;
  padding: 0.0625rem 0.375rem;
  border-radius: 999px;
  font-size: 0.6875rem;
  font-weight: 600;
  line-height: 1.5;
  background: color-mix(in srgb, var(--sf-public-accent, #e11d48) 12%, transparent);
  color: var(--sf-public-accent, #e11d48);
}

.sf-profile-bookmark__badge--comment {
  background: color-mix(in srgb, var(--sf-public-muted, #64748b) 16%, transparent);
  color: var(--sf-public-muted, #64748b);
}

.sf-profile-bookmark__body {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 0;
  flex: 1 1 auto;
}

.sf-profile-bookmark__body strong {
  font-size: 0.9375rem;
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.sf-profile-bookmark__excerpt {
  font-size: 0.8125rem;
  line-height: 1.5;
  color: var(--sf-public-muted, #64748b);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.sf-profile-bookmark__meta {
  font-size: 0.75rem;
  color: var(--sf-public-muted, #64748b);
}

.sf-profile-bookmark__remind {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  align-self: flex-start;
  flex: 0 0 auto;
  padding: 0.0625rem 0.375rem;
  border-radius: 999px;
  font-size: 0.6875rem;
  font-weight: 600;
  line-height: 1.5;
  background: color-mix(in srgb, var(--sf-public-muted, #64748b) 14%, transparent);
  color: var(--sf-public-muted, #64748b);
}

.sf-profile-bookmark__remind.is-done {
  background: color-mix(in srgb, var(--sf-public-accent, #e11d48) 12%, transparent);
  color: var(--sf-public-accent, #e11d48);
}

.sf-profile-bookmark__lock {
  display: inline-flex;
  align-items: center;
  flex: 0 0 auto;
  color: var(--sf-public-muted, #64748b);
}

@media (max-width: 480px) {
  .sf-profile-bookmark__row {
    flex-wrap: wrap;
  }
}
</style>
