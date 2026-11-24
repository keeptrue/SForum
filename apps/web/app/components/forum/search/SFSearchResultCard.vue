<script setup lang="ts">
/**
 * 搜索结果的卡片形态（仅用于结果面容器 ≥900px 的两列卡阵）。
 *
 * 为什么不直接用 `SFHomeTopicRow`：行组件是「列表」语义——没有摘要、没有标签 pills、
 * 没有独立表面，两列排开读起来像表格。搜索场景里摘要就是判断相关性的依据，
 * 缺了它用户只能靠标题猜。所以宽容器走卡片、窄容器仍走行（见 SFSearchPage 的双列表）。
 *
 * 性能：本组件**不加** `backdrop-filter`。玻璃感由父级结果面开一次，卡片只保留
 * 半透明填充 + 1px 亮边 + 阴影——父级模糊会让子级的半透明填充同样呈磨砂感。
 */
import type { ForumTopicSummary } from '~/utils/forum/forumTaxonomy'
import { forumAuthorName } from '~/utils/forum/forumTaxonomy'
import { highlightSegments, tokenizeQuery } from '~/utils/forum/searchHighlight'

const props = defineProps<{
  topic: ForumTopicSummary
  to: string
  createdLabel: string
  activityLabel: string
  /** 搜索页命中高亮词（原样查询串）；空串 = 不高亮 */
  highlightQuery?: string
}>()

const { t } = useI18n()

const authorName = computed(() => forumAuthorName(props.topic.author, props.topic.authorUserId))
const initial = computed(() => (authorName.value || '?').slice(0, 1).toUpperCase())
const tags = computed(() => (props.topic.tags || []).slice(0, 3))
const highlightTerms = computed(() => tokenizeQuery(props.highlightQuery || ''))
const titleSegments = computed(() => highlightSegments(props.topic.title, highlightTerms.value))
const excerptSegments = computed(() => highlightSegments(props.topic.excerpt || '', highlightTerms.value))
</script>

<template>
  <article class="sf-aurora-card">
    <a class="sf-aurora-card__link" :href="props.to">
      <h2 class="sf-aurora-card__title">
        <template v-for="(segment, index) in titleSegments" :key="index">
          <mark v-if="segment.hit" class="sf-search-hit">{{ segment.text }}</mark>
          <template v-else>{{ segment.text }}</template>
        </template>
      </h2>
      <p v-if="props.topic.excerpt" class="sf-aurora-card__excerpt">
        <template v-for="(segment, index) in excerptSegments" :key="index">
          <mark v-if="segment.hit" class="sf-search-hit">{{ segment.text }}</mark>
          <template v-else>{{ segment.text }}</template>
        </template>
      </p>
      <!--
        标签用 span 而不是链接：整张卡已经是一个 <a>，HTML 解析器遇到嵌套 <a> 会提前
        闭合外层链接，把 meta 行甩出卡片（实测踩过）。标签入口在详情页里。
      -->
      <div v-if="tags.length" class="sf-aurora-card__tags">
        <span v-for="tag in tags" :key="tag.slug" class="sf-aurora-card__tag">
          #{{ tag.name }}
        </span>
      </div>
      <div class="sf-aurora-card__meta">
        <span class="sf-aurora-card__avatar" aria-hidden="true">{{ initial }}</span>
        <span class="sf-aurora-card__author">{{ authorName }}</span>
        <span class="sf-aurora-card__sep" aria-hidden="true">·</span>
        <span>{{ props.activityLabel }}</span>
        <span class="sf-aurora-card__stats">
          <span>{{ t('home.feed.replyCount', { count: props.topic.commentCount }) }}</span>
          <span>{{ props.topic.viewCount }} {{ t('home.feed.views') }}</span>
        </span>
      </div>
    </a>
  </article>
</template>
