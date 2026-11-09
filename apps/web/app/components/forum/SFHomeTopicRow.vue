<script setup lang="ts">
/**
 * 话题列表行（宿主岛子组件）。
 * 桌面 C1：徽标单独一行在标题上方，标题行 = 分类 pill + 标题，meta 行 = 头像 + 作者 ← 最近回复 + 标签 + 回复徽章。
 * 移动端 G1：徽标与标题同行，分类 pill 在 meta 行，无标签。
 * 呈现用 Tailwind + 公共 token；主题换肤走 L0 变量，深度定制走 Component Registry / L2。
 */
import type { ForumTopicExtensionBadge, ForumTopicSummary } from '~/utils/forum/forumTaxonomy'
import { forumAuthorName, forumTagPath, forumTopicExtensionLabel } from '~/utils/forum/forumTaxonomy'
import {
  forumCategoryChipToneClass,
  forumListBadgeToneClass
} from '~/utils/forum/forumListPresentation'

const props = defineProps<{
  topic: ForumTopicSummary
  to: string
  /** 发帖时间（createdAt） */
  createdLabel: string
  /** 最近活动时间（lastActivityAt） */
  activityLabel: string
  /** forum.topic.list.badges；列表级一次解析后挂到每行；空时不渲染 */
  extensionListBadges?: ForumTopicExtensionBadge[]
}>()

const { t, locale } = useI18n()
const localePath = useLocalePath()

const authorName = computed(() => forumAuthorName(props.topic.author, props.topic.authorUserId))
/** 最近回复：优先 lastReplyAuthor，否则回退楼主 */
const lastReplyAuthor = computed(() => props.topic.lastReplyAuthor || props.topic.author)
const lastReplyName = computed(() =>
  forumAuthorName(lastReplyAuthor.value, lastReplyAuthor.value?.id || props.topic.authorUserId)
)
const listBadges = computed(() => props.extensionListBadges || [])
const hasReplies = computed(() => (props.topic.commentCount || 0) > 0)
/** 桌面 C1 最多展示 3 个标签 */
const desktopTags = computed(() => (props.topic.tags || []).slice(0, 3))

const categoryChipClass = computed(() => {
  const seed = props.topic.categorySlug || props.topic.categoryName || 'c'
  return forumCategoryChipToneClass(seed)
})

function listBadgeLabel(badge: ForumTopicExtensionBadge) {
  return forumTopicExtensionLabel(badge, String(locale.value || 'zh-CN')) || badge.id
}

function listBadgeHref(badge: ForumTopicExtensionBadge) {
  const href = `${badge.href || ''}`.trim()
  if (!href.startsWith('/') || href.startsWith('//') || href.includes('://') || href.startsWith('/api')) {
    return ''
  }
  return localePath(href)
}

const pillBase =
  'inline-flex h-5 shrink-0 items-center gap-1 rounded-[3px] px-1.5 text-xs font-semibold leading-5'
</script>

<template>
  <article
    class="sf-home-topic-row min-w-0 border-b border-[var(--sf-border-light,#eef0f3)] px-3 py-3.5 transition-colors duration-100 last:border-b-0 hover:bg-[var(--sf-public-row-hover)] max-[720px]:border-0 max-[720px]:px-2 max-[720px]:py-3 max-[720px]:border-b max-[720px]:border-b-[var(--sf-public-border)]"
    :class="topic.isPinned ? 'sf-home-topic-row--pinned' : ''"
    data-sf-component="forum.topic_list_row"
  >
    <div class="sf-home-topic-row__copy min-w-0">
      <div class="sf-home-topic-row__title-line flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1">
        <!-- 徽标：桌面独占一行在标题上方；移动端与标题同行 -->
        <span
          class="sf-home-topic-row__badges min-w-0 max-[720px]:contents min-[721px]:flex min-[721px]:basis-full min-[721px]:items-center min-[721px]:gap-x-1.5 min-[721px]:gap-y-1"
        >
          <span
            v-if="topic.isPinned"
            :class="[pillBase, 'sf-home-topic-row__pin-badge']"
          >
            <UIcon name="i-lucide-pin" class="size-3 shrink-0" aria-hidden="true" />
            {{ t('home.badge.pinned') }}
          </span>
          <span
            v-if="topic.status === 'locked'"
            :class="[pillBase, 'bg-slate-100 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300']"
          >
            <UIcon name="i-lucide-lock" class="size-3 shrink-0" aria-hidden="true" />
            {{ t('home.badge.locked') }}
          </span>
          <template v-for="badge in listBadges" :key="`${badge.extensionId}:${badge.id}`">
            <NuxtLink
              v-if="listBadgeHref(badge)"
              :to="listBadgeHref(badge)"
              :class="[pillBase, forumListBadgeToneClass(badge.tone)]"
              data-testid="topic-list-extension-badge"
            >
              {{ listBadgeLabel(badge) }}
            </NuxtLink>
            <span
              v-else
              :class="[pillBase, forumListBadgeToneClass(badge.tone)]"
              data-testid="topic-list-extension-badge"
            >
              {{ listBadgeLabel(badge) }}
            </span>
          </template>
        </span>

        <!-- 桌面：分类 pill 在标题前 -->
        <NuxtLink
          v-if="topic.categoryName"
          :to="localePath(`/c/${topic.categorySlug}`)"
          class="sf-home-topic-row__desktop-category hidden min-[721px]:inline-flex h-5 shrink-0 items-center rounded-[5px] px-2 text-[11px] font-semibold leading-5 no-underline"
          :class="categoryChipClass"
          :title="topic.categoryName"
        >
          {{ topic.categoryName }}
        </NuxtLink>

        <h2 class="sf-home-topic-row__title m-0 min-w-0 flex-1 text-base font-semibold leading-snug text-[var(--sf-public-text)]">
          <NuxtLink
            :to="to"
            :prefetch="false"
            class="text-inherit no-underline hover:text-[var(--sf-accent)]"
          >
            {{ topic.title }}
          </NuxtLink>
        </h2>
      </div>

      <!-- meta：头像 + 作者 +（← 最近回复 · 最近活动 | · 发帖时间）+ 分类 pill（移动端）+ 标签（桌面）+ 回复徽章 -->
      <div class="sf-home-topic-row__meta mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs font-medium text-[var(--sf-public-text-muted)]">
        <span class="sf-home-topic-row__meta-avatar" aria-hidden="true">
          <SFAvatar
            :name="authorName"
            :avatar="topic.author?.avatar"
            alt=""
            size="list"
          />
        </span>
        <span class="inline-flex min-w-0 items-center gap-1">
          <NuxtLink
            v-if="topic.author"
            class="truncate font-medium text-[var(--sf-public-text-muted)] no-underline hover:text-[var(--sf-accent)]"
            :to="localePath(`/u/${topic.author.username}`)"
          >
            {{ authorName }}
          </NuxtLink>
          <span v-else class="truncate">{{ authorName }}</span>
          <template v-if="hasReplies">
            <span class="sf-home-topic-row__meta-arrow inline-flex shrink-0 items-center text-[var(--sf-public-text-muted)]" aria-hidden="true">
              <UIcon name="i-lucide-arrow-left" class="size-3" />
            </span>
            <span class="sf-home-topic-row__meta-replier inline-flex min-w-0 items-center gap-1">
              <NuxtLink
                v-if="lastReplyAuthor?.username"
                class="truncate font-medium text-[var(--sf-public-text-muted)] no-underline hover:text-[var(--sf-accent)]"
                :to="localePath(`/u/${lastReplyAuthor.username}`)"
              >
                {{ lastReplyName }}
              </NuxtLink>
              <span v-else class="truncate">{{ lastReplyName }}</span>
              <span aria-hidden="true">·</span>
              <time :datetime="topic.lastActivityAt || topic.createdAt">{{ activityLabel }}</time>
            </span>
          </template>
          <template v-else>
            <span aria-hidden="true">·</span>
            <time :datetime="topic.createdAt">{{ createdLabel }}</time>
          </template>
        </span>

        <!-- 移动端：分类 pill -->
        <span
          v-if="topic.categoryName"
          class="sf-home-topic-row__mobile-taxonomy hidden max-[720px]:flex"
          :aria-label="t('composer.categoryLabel')"
        >
          <NuxtLink
            :to="localePath(`/c/${topic.categorySlug}`)"
            class="sf-home-topic-row__mobile-category"
            :class="categoryChipClass"
          >
            {{ topic.categoryName }}
          </NuxtLink>
        </span>

        <!-- 桌面：最多 3 个标签 -->
        <span
          v-if="desktopTags.length"
          class="sf-home-topic-row__desktop-tags hidden min-[721px]:inline-flex items-center gap-1.5"
          :aria-label="t('composer.tagsLabel')"
        >
          <NuxtLink
            v-for="tag in desktopTags"
            :key="tag.slug"
            :to="localePath(forumTagPath(tag.slug))"
            class="text-[var(--sf-public-text-muted)] no-underline hover:text-[var(--sf-accent)]"
          >
            #{{ tag.name }}
          </NuxtLink>
        </span>

        <span
          v-if="topic.likeCount > 0"
          class="ml-auto inline-flex shrink-0 items-center gap-1 text-[11px] tabular-nums text-[var(--sf-public-text-secondary)]"
          :aria-label="t('home.feed.likeCount', { count: topic.likeCount })"
        >
          <UIcon name="i-lucide-heart" class="size-3.5" aria-hidden="true" />
          {{ topic.likeCount }}
        </span>

        <span
          class="sf-home-topic-row__reply-badge inline-flex h-[22px] min-w-[26px] shrink-0 items-center justify-center rounded-full border border-[var(--sf-public-border-strong)] bg-[var(--sf-public-surface-muted)] px-2 text-[11px] font-bold tabular-nums text-[var(--sf-public-text-secondary)]"
          :class="topic.likeCount > 0 ? '' : 'ml-auto'"
          :aria-label="t('home.feed.replyCount', { count: topic.commentCount })"
        >
          {{ topic.commentCount }}
        </span>
      </div>
    </div>
  </article>
</template>
