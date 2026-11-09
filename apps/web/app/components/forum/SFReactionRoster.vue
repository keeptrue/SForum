<script setup lang="ts">
/**
 * 「谁赞了」名单弹层内容。
 *
 * 自持分页状态：打开时才请求第一页（limit=20&offset=n），「加载更多」按 offset 追加并去重。
 * 错误内联展示并可重试（弹层里弹 Toast 会遮住名单）；触发器与头像组始终可用。
 */
import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'
import { useForumApi } from '~/composables/forum/useForumApi'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  forumReactionRosterCanLoadMore,
  forumReactionRosterMerge,
  forumReactionRosterTotal
} from '~/utils/forum/forumReaction'

const props = withDefaults(defineProps<{
  targetType: 'topic' | 'comment'
  targetId: number
  active: boolean
  /** 触发器的点赞总数，只用来决定首开骨架行数 */
  count?: number
}>(), {
  count: 0
})

const PAGE_SIZE = 20
/** 服务端 offset 上限：到这里就收起「加载更多」。 */
const ROSTER_OFFSET_CAP = 1000

const { t } = useI18n()
const localePath = useLocalePath()
const api = useForumApi()
const { user } = useAuthSession()

const items = ref<ForumUserSummary[]>([])
const total = ref(0)
const hasMore = ref(false)
const loading = ref(false)
const failed = ref(false)
const loaded = ref(false)

const selfId = computed(() => user.value?.id ?? null)
const showSkeleton = computed(() => loading.value && !loaded.value)
const skeletonRows = computed(() => Math.min(5, Math.max(1, props.count || 3)))
// 可见性只由「还有下一页 + 未到 offset 上限」决定：加载中按钮必须保持挂载，
// 否则焦点掉到 body、弹层高度也会跳 32px。loading 只负责 disabled / aria-busy。
const showLoadMore = computed(() => forumReactionRosterCanLoadMore(
  hasMore.value,
  false,
  items.value.length,
  ROSTER_OFFSET_CAP
))

function displayName(item: ForumUserSummary) {
  return item.displayName || item.username
}

async function load(reset: boolean) {
  if (loading.value) return
  const offset = reset ? 0 : items.value.length
  loading.value = true
  failed.value = false
  try {
    const page = props.targetType === 'topic'
      ? await api.listTopicLikes(props.targetId, PAGE_SIZE, offset)
      : await api.listCommentLikes(props.targetId, PAGE_SIZE, offset)
    const pageItems = page.items || []
    items.value = forumReactionRosterMerge(reset ? [] : items.value, pageItems)
    // offset 越界时服务端回 total=0：追加页只允许人数变大，不能把已知人数抹掉。
    total.value = forumReactionRosterTotal(total.value, page.total, reset)
    const invalidTotal = !reset && page.total === 0 && pageItems.length > 0
    hasMore.value = invalidTotal ? false : Boolean(page.hasMore)
    loaded.value = true
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}

function loadMore() {
  if (!forumReactionRosterCanLoadMore(hasMore.value, loading.value, items.value.length, ROSTER_OFFSET_CAP)) return
  void load(false)
}

function retry() {
  void load(items.value.length === 0)
}

// 首屏零请求：只有弹层真正打开（active）才拉第一页；关闭后保留已加载内容。
watch(() => props.active, (active) => {
  if (active && !loaded.value && !loading.value) {
    void load(true)
  }
}, { immediate: true })
</script>

<template>
  <div class="sf-roster" :aria-busy="showSkeleton ? 'true' : undefined">
    <header class="sf-roster__header">
      <span class="sf-roster__title">{{ t('forum.reaction.roster') }}</span>
      <span v-if="total > 0" class="sf-roster__total">{{ t('forum.reaction.rosterTotal', { count: total }) }}</span>
    </header>

    <div v-if="showSkeleton" class="sf-roster__skeleton" aria-hidden="true">
      <div v-for="row in skeletonRows" :key="row" class="sf-roster__skeleton-row">
        <span class="sf-roster__skeleton-avatar" />
        <span class="sf-roster__skeleton-bar" />
      </div>
    </div>

    <p v-else-if="failed" class="sf-roster__error" role="alert">
      <UIcon name="i-lucide-alert-circle" class="sf-roster__error-icon" aria-hidden="true" />
      <span class="sf-roster__error-text">{{ t('forum.reaction.rosterFailed') }}</span>
      <button type="button" class="sf-roster__retry" :disabled="loading" @click="retry">
        {{ t('forum.reaction.retry') }}
      </button>
    </p>

    <ul v-else-if="items.length" class="sf-roster__list">
      <li
        v-for="item in items"
        :key="item.id"
        class="sf-roster__item"
        :class="{ 'sf-roster__item--self': item.id === selfId }"
      >
        <SFAvatar :name="displayName(item)" :avatar="item.avatar" size="xs" :alt="''" />
        <NuxtLink :to="localePath(`/u/${item.username}`)" class="sf-roster__name">
          {{ displayName(item) }}
        </NuxtLink>
        <span v-if="item.id === selfId" class="sf-roster__you">{{ t('forum.reaction.you') }}</span>
      </li>
    </ul>

    <p v-else class="sf-roster__empty">{{ t('forum.reaction.empty') }}</p>

    <button
      v-if="showLoadMore"
      type="button"
      class="sf-roster__more"
      :aria-disabled="loading ? 'true' : undefined"
      :aria-busy="loading ? 'true' : undefined"
      @click="loadMore"
    >
      <template v-if="loading">
        <UIcon name="i-lucide-loader-2" class="sf-roster__more-icon sf-roster__more-icon--motion" aria-hidden="true" />
        <UIcon name="i-lucide-loader-circle" class="sf-roster__more-icon sf-roster__more-icon--still" aria-hidden="true" />
      </template>
      <UIcon v-else name="i-lucide-chevron-down" class="sf-roster__more-icon" aria-hidden="true" />
      <span>{{ t('forum.reaction.loadMore') }}</span>
    </button>
  </div>
</template>

<style scoped>
.sf-roster {
  /* 320×256 基线：12px 内边距 + 168px 列表视口（5 行 × 32px + 4 × 2px 行距） */
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: min(320px, calc(100vw - 2rem));
  max-height: 256px;
  padding: 12px;
}

.sf-roster__header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  min-height: 20px;
}

.sf-roster__title {
  font-size: 12px;
  font-weight: 600;
}

.sf-roster__total {
  font-size: 12px;
  color: var(--sf-public-text-secondary, #676773);
  font-variant-numeric: tabular-nums;
}

.sf-roster__list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 168px;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  /* 滚动条出现时名字不横移 */
  scrollbar-gutter: stable;
  list-style: none;
}

.sf-roster__item {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 6px;
  border-radius: 6px;
}

.sf-roster__item:hover {
  background: var(--sf-public-surface-muted, #f1f5f9);
}

.sf-roster__item--self .sf-roster__name {
  font-weight: 600;
}

.sf-roster__name {
  flex: 1 1 auto;
  min-width: 0;
  color: inherit;
  font-size: 13px;
  font-weight: 500;
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-roster__name:hover {
  text-decoration: underline;
}

/* 「（你）」必须落在截断 span 之外，否则会被 ellipsis 吃掉 */
.sf-roster__you {
  flex-shrink: 0;
  margin-inline-start: 4px;
  font-size: 12px;
  color: var(--sf-public-text-secondary, #676773);
}

.sf-roster__empty {
  margin: 0;
  padding: 4px 6px;
  font-size: 12px;
  color: var(--sf-public-text-secondary, #676773);
}

.sf-roster__error {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  margin: 0;
  padding: 0 6px;
  font-size: 12px;
  color: var(--sf-danger, #b91c1c);
}

.dark .sf-roster__error {
  color: var(--sf-danger-dark, #f87171);
}

.sf-roster__error-icon {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
}

.sf-roster__error-text {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-roster__retry {
  flex-shrink: 0;
  /* 触达下限 24×24（在 32px 错误块内，块高不变） */
  min-height: 24px;
  padding: 0 4px;
  border: 0;
  background: transparent;
  color: var(--sf-accent, #0f766e);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.sf-roster__retry:hover {
  text-decoration: underline;
}

.sf-roster__more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: 100%;
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--sf-public-text-secondary, #676773);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.sf-roster__more:hover {
  background: var(--sf-public-surface-muted, #f1f5f9);
}

/* 不用原生 disabled：Chrome 会在两帧内把焦点踢到 body，键盘用户中途失去落点。
   重复请求由 loadMore() 的 canLoadMore 守卫挡住，这里只做视觉与语义上的弱化。 */
.sf-roster__more[aria-disabled='true'] {
  cursor: default;
  opacity: 0.7;
}

.sf-roster__more-icon {
  width: 14px;
  height: 14px;
}

.sf-roster__more-icon--motion {
  animation: sf-roster-spin 0.8s linear infinite;
}

.sf-roster__more-icon--still {
  display: none;
}

@keyframes sf-roster-spin {
  to {
    transform: rotate(360deg);
  }
}

.sf-roster__skeleton {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.sf-roster__skeleton-row {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 6px;
}

.sf-roster__skeleton-avatar {
  width: 20px;
  height: 20px;
  flex-shrink: 0;
  border-radius: 999px;
  background: var(--sf-public-surface-muted, #f1f5f9);
}

.sf-roster__skeleton-bar {
  width: 96px;
  height: 8px;
  border-radius: 999px;
  background: var(--sf-public-surface-muted, #f1f5f9);
}

@media (prefers-reduced-motion: reduce) {
  .sf-roster,
  .sf-roster__more-icon {
    transition: none;
    animation: none;
  }

  /* 动效关闭时用静态 loader-circle 表达「加载中」 */
  .sf-roster__more-icon--motion {
    display: none;
  }

  .sf-roster__more-icon--still {
    display: inline-block;
  }
}
</style>
