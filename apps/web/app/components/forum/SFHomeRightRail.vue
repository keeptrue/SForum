<script setup lang="ts">
import { useActiveThemeSettings } from '~/composables/themes/useActiveThemeSettings'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useForumHomeGreeting } from '~/composables/forum/useForumHomeGreeting'
import {
  forumTopicPath,
  type ForumTag,
  type ForumTopicSummary,
  type TopicUrlMode
} from '~/utils/forum/forumTaxonomy'

const props = defineProps<{
  hotTopics: ForumTopicSummary[]
  tags: ForumTag[]
  selectedTagSlug?: string
  totalTopics: number
  totalReplies: number
  categoryCount: number
  tagCount: number
  topicUrlMode: TopicUrlMode
  /** 是否展示发帖快捷入口（API 仍最终鉴权） */
  canCreateTopic?: boolean
}>()

const emit = defineEmits<{
  'select-tag': [slug: string]
}>()

const { t } = useI18n()
const localePath = useLocalePath()
const { user } = useAuthSession()
const { siteName } = useWebOptions()
const {
  rightRailShowHot,
  rightRailShowStats,
  rightRailShowTags,
  rightRailShowAuthCard,
  rightRailWelcome
} = useActiveThemeSettings()

const guestWelcome = computed(() => rightRailWelcome.value || t('home.rightRail.welcomeDesc'))

const isAuthenticated = computed(() => Boolean(user.value))
const displayName = computed(() =>
  userDisplayName(user.value)
)
// 问候语按时段生成：SSR 用站点时区，挂载后改用访客本地时区。
const { greetingKey } = useForumHomeGreeting()
const profileTo = computed(() =>
  user.value ? localePath(`/u/${user.value.username}`) : localePath('/login')
)
const settingsTo = computed(() => localePath('/settings/profile'))
const newTopicTo = computed(() => localePath('/topics/new'))
const loginTo = computed(() => localePath('/login'))
const registerTo = computed(() => localePath('/register'))

const stats = computed(() => [
  { key: 'topics', label: t('home.dock.topics'), value: props.totalTopics },
  { key: 'replies', label: t('home.sidebar.statReplies'), value: props.totalReplies },
  { key: 'categories', label: t('home.dock.categories'), value: props.categoryCount },
  { key: 'tags', label: t('home.dock.tags'), value: props.tagCount }
])

function topicTo(topic: ForumTopicSummary) {
  return localePath(forumTopicPath(topic, props.topicUrlMode))
}

function formatCount(value: number) {
  const n = Math.max(0, Number.isFinite(value) ? Math.floor(value) : 0)
  if (n >= 10000) {
    return `${(n / 1000).toFixed(n >= 100000 ? 0 : 1).replace(/\.0$/, '')}k`
  }
  return n.toLocaleString()
}

function hotRank(index: string | number) {
  return String(Number(index) + 1).padStart(2, '0')
}

function isTopHotRank(index: string | number) {
  return Number(index) < 3
}
</script>

<template>
  <aside class="sforum-home__right" :aria-label="t('home.rightRail.ariaLabel')">
    <div class="sf-home-right-rail">
      <!-- 访客：登录转化；已登录：轻量用户卡 + 快捷入口（无虚构统计） -->
      <section v-if="rightRailShowAuthCard && isAuthenticated && user" class="sf-home-right-rail__card">
        <div class="sf-home-right-rail__user">
          <NuxtLink :to="profileTo" class="sf-home-right-rail__user-main">
            <SFAvatar :name="displayName" :avatar="user.avatar" size="md" />
            <span class="sf-home-right-rail__user-text">
              <span class="sf-home-right-rail__user-name">
                {{ t(greetingKey, { name: displayName }) }}
              </span>
              <span class="sf-home-right-rail__user-sub">{{ t('home.rightRail.welcomeUserSubtitle') }}</span>
            </span>
          </NuxtLink>
        </div>
      </section>

      <section v-else-if="rightRailShowAuthCard" class="sf-home-right-rail__card">
        <header class="sf-home-right-rail__head">
          <h3 class="sf-home-right-rail__title">
            {{ t('home.sidebar.welcomeTitle', { siteName }) }}
          </h3>
        </header>
        <div class="sf-home-right-rail__welcome">
          <p>{{ guestWelcome }}</p>
          <NuxtLink :to="loginTo" class="sf-home-right-rail__action sf-home-right-rail__action--primary sf-home-right-rail__action--block">
            {{ t('home.sidebar.loginBtn') }}
          </NuxtLink>
          <NuxtLink :to="registerTo" class="sf-home-right-rail__action sf-home-right-rail__action--outline sf-home-right-rail__action--block">
            {{ t('home.sidebar.registerBtn') }}
          </NuxtLink>
        </div>
      </section>

      <!-- 热门：用当前已加载列表按回复数近似，不新增接口 -->
      <section v-if="rightRailShowHot" class="sf-home-right-rail__card">
        <header class="sf-home-right-rail__head">
          <h3 class="sf-home-right-rail__title">{{ t('home.sidebar.hotThreads') }}</h3>
          <span class="sf-home-right-rail__meta">{{ t('home.rightRail.hotByReplies') }}</span>
        </header>
        <ol v-if="hotTopics.length" class="sf-home-right-rail__hot-list">
          <li v-for="(topic, index) in hotTopics" :key="topic.id" class="sf-home-right-rail__hot-item">
            <span
              class="sf-home-right-rail__rank"
              :class="{ 'is-top': isTopHotRank(index) }"
              aria-hidden="true"
            >
              {{ hotRank(index) }}
            </span>
            <NuxtLink :to="topicTo(topic)" :prefetch="false" class="sf-home-right-rail__hot-link">
              <span class="sf-home-right-rail__hot-title">{{ topic.title }}</span>
            </NuxtLink>
            <span class="sf-home-right-rail__hot-count" :title="t('home.sidebar.repliesCount', { count: topic.commentCount })">
              {{ topic.commentCount }}
            </span>
          </li>
        </ol>
        <p v-else class="sf-home-right-rail__empty">
          {{ t('home.rightRail.hotEmpty') }}
        </p>
      </section>

      <section v-if="rightRailShowStats" class="sf-home-right-rail__card">
        <header class="sf-home-right-rail__head">
          <h3 class="sf-home-right-rail__title">{{ t('home.sidebar.forumStats') }}</h3>
        </header>
        <div class="sf-home-right-rail__stats">
          <div v-for="stat in stats" :key="stat.key" class="sf-home-right-rail__stat">
            <strong>{{ formatCount(stat.value) }}</strong>
            <span>{{ stat.label }}</span>
          </div>
        </div>
      </section>

      <section v-if="rightRailShowTags && tags.length" class="sf-home-right-rail__card">
        <header class="sf-home-right-rail__head">
          <h3 class="sf-home-right-rail__title">{{ t('home.rightRail.hotTags') }}</h3>
        </header>
        <div class="sf-home-right-rail__tags">
          <button
            v-for="tag in tags"
            :key="tag.slug"
            type="button"
            class="sf-home-right-rail__tag"
            :class="{ 'is-active': selectedTagSlug === tag.slug }"
            :aria-pressed="selectedTagSlug === tag.slug"
            @click="emit('select-tag', tag.slug)"
          >
            #{{ tag.name }}
          </button>
        </div>
      </section>
    </div>
    <!-- 宿主区域出口等附加内容（例如 forum.home sidebar 区域） -->
    <slot />
  </aside>
</template>
