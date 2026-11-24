<script setup lang="ts">
/**
 * 搜索结果面（02 极光玻璃的「单块玻璃」）。
 *
 * 性能硬约束：`backdrop-filter` **只在本组件根节点开一次**。结果行/卡片一律不加——
 * 列表 10–20 行就是 10–20 个合成层，滚动必然掉帧。父级开了模糊之后，子级的半透明
 * 填充同样呈磨砂感，视觉等价而成本从 N 降到 1。
 *
 * 状态四态互斥：loading / error / empty / ready。`ready` 且 count=0 由父级归一到 empty，
 * 不允许「空列表 + 0 个结果标题」再叠一层空态。
 */
import type { SearchFeedState } from './useSearchFeed'

const props = withDefaults(defineProps<{
  state: SearchFeedState
  count: number
  query?: string
  hasMore?: boolean
  loadingMore?: boolean
  loadMoreError?: boolean
}>(), {
  query: '',
  hasMore: false,
  loadingMore: false,
  loadMoreError: false
})

const emit = defineEmits<{
  reset: []
  retry: []
  loadMore: []
}>()

const { t } = useI18n()

const visible = computed(() => props.state !== 'idle')
const showFoot = computed(() => props.state === 'ready' && (props.hasMore || props.loadingMore || props.loadMoreError))
</script>

<template>
  <section
    v-if="visible"
    class="sf-aurora-surface"
    :data-state="props.state"
    :aria-busy="props.state === 'loading' || props.loadingMore"
  >
    <header v-if="props.state === 'ready' || props.state === 'empty'" class="sf-aurora-surface__head">
      <span class="sf-aurora-surface__count">
        {{ t('search.resultCount', { count: props.count }) }}
      </span>
    </header>

    <div v-if="props.state === 'loading'" class="sf-aurora-surface__skeleton" aria-hidden="true">
      <div v-for="row in 7" :key="row" class="sf-aurora-surface__skeleton-row">
        <SFSkeleton avatar :lines="2" />
      </div>
    </div>

    <div v-else-if="props.state === 'error'" class="sf-aurora-surface__error" role="alert">
      <span>{{ t('home.feed.loadFailed') }}</span>
      <SFButton size="sm" variant="ghost" @click="emit('retry')">
        <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
        {{ t('home.feed.retry') }}
      </SFButton>
    </div>

    <div
      v-else-if="props.state === 'empty'"
      class="sf-aurora-surface__empty"
      data-sf-region="search-empty"
    >
      <!-- 光核（纯 CSS）取代 SFEmptyState 的默认小图标：同一屏里两个图标是噪音。 -->
      <div class="sf-aurora-orb" aria-hidden="true">
        <span class="sf-aurora-orb__ring" />
        <span class="sf-aurora-orb__ring" />
        <span class="sf-aurora-orb__ring" />
        <span class="sf-aurora-orb__core" />
      </div>
      <SFEmptyState
        :title="t('search.emptyTitle')"
        :description="t('search.emptyDescription')"
        :action-label="t('search.clearQuery')"
        @action="emit('reset')"
      />
    </div>

    <div v-else class="sf-aurora-surface__grid">
      <slot />
    </div>

    <footer v-if="showFoot" class="sf-aurora-surface__foot">
      <span v-if="props.loadingMore" class="sf-aurora-surface__foot-note">
        {{ t('home.feed.loadingMore') }}
      </span>
      <template v-else-if="props.loadMoreError">
        <span class="sf-aurora-surface__foot-note">{{ t('home.feed.loadMoreFailed') }}</span>
        <SFButton size="sm" variant="ghost" @click="emit('loadMore')">
          <UIcon name="i-lucide-refresh-cw" class="size-4" aria-hidden="true" />
          {{ t('home.feed.retryLoadMore') }}
        </SFButton>
      </template>
      <SFButton v-else size="sm" variant="ghost" @click="emit('loadMore')">
        <UIcon name="i-lucide-chevron-down" class="size-4" aria-hidden="true" />
        {{ t('home.feed.loadMore') }}
      </SFButton>
    </footer>
  </section>
</template>
