<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { useProfileApi, type ProfileActivityKind, type ProfileActivityPage } from '~/composables/profile/useProfileApi'
import { useProfileShowData } from '~/composables/profile/useProfileShowData'
import { useProfileTabQuery } from '~/composables/profile/useProfileTabQuery'
import { useBadgeDetailQuery } from '~/composables/profile/useBadgeDetailQuery'
import { useProfileBadges } from '~/composables/profile/useProfileBadges'
import { useTrustLevel } from '~/composables/profile/useTrustLevel'
import { useProfileBookmarks } from '~/composables/profile/useProfileBookmarks'
import {
  PROFILE_ACTIVITY_PER_PAGE,
  profileActivityKindForTab,
  useProfileActivityFeeds
} from '~/composables/profile/useProfileActivityFeeds'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useForumApi } from '~/composables/forum/useForumApi'
import SFProfileBookmarkTab from '~/components/profile/SFProfileBookmarkTab.vue'
import SFProfileBadgeTab from '~/components/profile/SFProfileBadgeTab.vue'
import SFProfileFeedTab from '~/components/profile/SFProfileFeedTab.vue'
import SFProfileRightRail from '~/components/profile/SFProfileRightRail.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.profile.show。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 * 布局对齐 demo B1：intro + 分区 tab（主题 / 回复 / 关于 + 扩展 widget tab）+ 分日时间线。
 * 活动流的取数与渲染在 `useProfileActivityFeeds` + `SFProfileFeedTab`，这里只管 tab 编排。
 */

import {
  forumUserProfilePath,
  type ForumCategoryGroup
} from '~/utils/forum/forumTaxonomy'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { profileHasPublicDetails } from '~/utils/profile/profileActivity'
import { profileMetaRows } from '~/utils/settings/profileSettings'
import { buildProfileTabs, type ProfileTabOption } from '~/utils/profile/profileTabs'
import {
  extensionTabLabel,
  profileWidgetTabValue,
  profileWidgetTabs
} from '~/utils/profile/profileExtensionTab'
import { safeUrl } from '~/utils/sfUrl'

const route = useRoute()
const { t, locale } = useI18n()
const localePath = useLocalePath()
const toast = useToast()
const { formatDateOnly } = useSiteDateTime()
// 收藏开关与控件同一口径：关闭时 tab 本身不出现，而不是点进去吃 403。
const { bookmarksEnabled, badgesEnabled } = useWebOptions()
const profileApi = useProfileApi()
const forumApi = useForumApi()
const { user: authUser } = useAuthSession()
const { can } = usePermissions()
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()

const username = computed(() => String(route.params.username ?? ''))
const renderedAt = useState<number>(`forum-profile-rendered-at-${username.value}`, () => Date.now())
// tab 状态与 `?tab=` 的同步全在 useProfileTabQuery：本页只消费 activeTab。
const { activeTab, syncTabQuery, resetTabIfMissing } = useProfileTabQuery()

// 路由壳是唯一取数点（见 useProfileShowData）：这里 inject 同一份 refs，
// 既不会重复请求，也不会和壳的 options 产生 dev 警告。
const { profile, pending: profilePending, error: profileError } = await useProfileShowData(username)

const { data: categoryGroups, pending: categoriesPending } = await useAsyncData(
  'forum-profile-category-groups',
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)

// SSR/首屏：主题 tab 第一页走独立分页接口，避免内嵌 20 条混合活动截断。
const { data: initialTopicPage } = await useAsyncData(
  () => `forum-profile-activities-topic-${username.value}`,
  async () => {
    if (!username.value) {
      return null as ProfileActivityPage | null
    }
    try {
      return await profileApi.listPublicActivities(username.value, {
        kind: 'topic',
        page: 1,
        perPage: PROFILE_ACTIVITY_PER_PAGE
      })
    } catch {
      // 资料可展示时活动失败不阻断整页；客户端可再试。
      return null as ProfileActivityPage | null
    }
  },
  { default: () => null as ProfileActivityPage | null }
)

useSForumSeo(computed(() => ({
  type: 'profile',
  path: forumUserProfilePath(username.value),
  title: userDisplayName(profile.value, username.value),
  description: profile.value?.profile.bio || t('profile.metaDescription'),
  public: Boolean(profile.value),
  noindex: !profile.value,
  variables: { authorName: userDisplayName(profile.value, username.value) },
  authorName: userDisplayName(profile.value, username.value)
})))

const displayName = computed(() => userDisplayName(profile.value, username.value))
const isSelf = computed(() => Boolean(profile.value && authUser.value?.id === profile.value.userId))
// 等级卡数据：他人视角只有 profile.trustLevel；本人额外读 /trust-levels 的 viewer 拿进度。
const { trustLevel, ladder: trustLevelLadder } = useTrustLevel(computed(() => profile.value?.trustLevel), () => isSelf.value)
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate))
const navCategories = computed(() => categoryGroups.value.flatMap(group => group.categories || []))
const navTotalTopics = computed(() => navCategories.value.reduce((sum, category) => sum + category.topicCount, 0))
const hasPublicDetails = computed(() => profileHasPublicDetails(profile.value))
// 「关于」里的自定义字段：服务端已按可见性过滤，前端只负责空值不渲染 + 链接白名单。
// meta 是 PublicProfile 的顶层属性（与 profile 同级），不是 profile 里的字段。
const metaRows = computed(() => profileMetaRows(profile.value?.meta, locale.value))
const recentTopics = computed(() => profile.value?.recentTopics || [])

// 两条活动流常驻：切 tab 只换 activeFeed，已加载的流不重复请求。
const {
  activeFeed,
  switchingTab,
  loadingMore,
  showLoadMore,
  categoryParticipationCount,
  ensureFeedLoaded,
  loadMoreActivities
} = useProfileActivityFeeds(username, {
  activeTab,
  initialTopicPage: initialTopicPage.value,
  recentTopics: () => recentTopics.value
})

const activeFeedKind = computed<ProfileActivityKind>(
  () => profileActivityKindForTab(activeTab.value) ?? 'topic'
)

// 收藏 tab：只在激活时取数，切走保留结果（与活动流同一缓存口径）。
const isBookmarksTab = computed(() => activeTab.value === 'bookmarks')
const {
  sortedItems: bookmarkItems,
  loading: bookmarksLoading,
  loadingMore: bookmarksLoadingMore,
  hasMore: bookmarksHasMore,
  failed: bookmarksFailed,
  loadMore: loadMoreBookmarks,
  retry: retryBookmarks
} = useProfileBookmarks(username, {
  active: () => isBookmarksTab.value,
  self: () => isSelf.value
})

// 徽章墙：只在激活时取数，切走保留结果（与收藏/活动流同一缓存口径）。
const isBadgesTab = computed(() => activeTab.value === 'badges')
const {
  groups: badgeGroups,
  loading: badgesLoading,
  failed: badgesFailed,
  loaded: badgesLoaded,
  retry: retryBadges
} = useProfileBadges(username, { active: () => isBadgesTab.value })

// `?badge=<slug>` 深链：弹窗住在徽章 tab 里，所以带 slug 进页要先把 tab 切过去。
const { focusSlug, setFocusSlug, resetBadgeIfMissing } = useBadgeDetailQuery()

watch(focusSlug, (slug) => {
  if (!slug || !badgesEnabled.value || activeTab.value === 'badges') return
  activeTab.value = 'badges'
  // SSR 期间不碰 router（Nuxt 会告警）：水合后这里会再跑一次，把地址栏对齐成 tab=badges。
  if (import.meta.client) syncTabQuery('badges')
}, { immediate: true })

// 离开徽章 tab 时清掉 `?badge=`：地址栏不能留一个当前页面打不开的弹窗参数。
watch(activeTab, (tab) => {
  if (tab !== 'badges' && focusSlug.value) setFocusSlug('')
})

const badgeSlugs = computed(() => badgeGroups.value.flatMap(group => group.badges.map(badge => badge.slug)))

// 清单**真的到过服务端**之后才确认 slug 还在：停用/删除的徽章静默清 query，不弹空弹窗。
// `!loaded` 这一条是必需的：`loading` 初值 false + `groups` 为 [] 时守卫会先于 composable
// 的首次请求同步跑一次，那时「找不到」只是「还没请求」，清掉就等于把 `?badge=` 深链吃掉。
watch([badgeSlugs, badgesLoading, badgesFailed, badgesLoaded], ([slugs, loading, failed, loaded]) => {
  if (loading || failed || !loaded) return
  resetBadgeIfMissing(slugs)
}, { immediate: true })

const profileTabs = computed<ProfileTabOption[]>(() => buildProfileTabs({
  t,
  locale: String(locale.value),
  topicCount: profile.value?.topicCount ?? 0,
  commentCount: profile.value?.commentCount ?? 0,
  bookmarksEnabled: bookmarksEnabled.value,
  badgesEnabled: badgesEnabled.value,
  extensionTabs: profile.value?.extensionTabs
}))

// 激活的是哪个扩展 widget：直接按 profile 求值，不额外留一个只被用一次的 computed。
const activeWidgetTab = computed(() => profileWidgetTabs(profile.value?.extensionTabs)
  .find(tab => profileWidgetTabValue(tab) === activeTab.value))

const activeWidgetTabLabel = computed(() => activeWidgetTab.value
  ? extensionTabLabel(activeWidgetTab.value, String(locale.value))
  : '')

// 面板只拿最小身份上下文；扩展自己按需再走公开 L2 bridge 请求数据。
const widgetComponentProps = computed<Record<string, unknown>>(() => ({
  username: profile.value?.username || username.value,
  userId: profile.value?.userId ?? 0,
  displayName: displayName.value
}))

// 扩展被禁用/升级后 widget tab 会消失；激活态留在已不存在的 tab 上会静默渲染成空时间线。
watch(profileTabs, tabs => resetTabIfMissing(tabs.map(tab => tab.value)))

const websiteHost = computed(() => {
  const raw = profile.value?.profile.websiteUrl?.trim() || ''
  if (!raw) {
    return ''
  }
  return raw.replace(/^https?:\/\//i, '').replace(/\/$/, '')
})

const websiteHref = computed(() => {
  const raw = profile.value?.profile.websiteUrl?.trim() || ''
  return raw ? safeUrl(raw) : ''
})

/** 自定义字段值只有确实是 http(s) 链接时才渲染成链接（safeUrl 已过滤危险协议）。 */
function metaHref(value: string): string {
  const url = safeUrl(value)
  return /^https?:\/\//i.test(url) ? url : ''
}

function formatDate(value: string) {
  return formatDateOnly(value)
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

async function selectTab(tab: string) {
  activeTab.value = tab
  syncTabQuery(tab)
  if (tab === 'topics' || tab === 'replies') {
    await ensureFeedLoaded(tab)
  }
}

async function shareProfile() {
  if (!import.meta.client) {
    return
  }
  const url = window.location.href
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(url)
    } else {
      const input = document.createElement('input')
      input.value = url
      document.body.appendChild(input)
      input.select()
      document.execCommand('copy')
      document.body.removeChild(input)
    }
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('profile.shareCopied')
    })
  } catch {
    toast.add({
      color: 'error',
      icon: 'i-lucide-alert-triangle',
      title: t('profile.shareFailed')
    })
  }
}
</script>

<template>
  <main class="sforum-home sf-profile-page" data-layout="fullwidth-3col">
    <template v-if="profileError && !profile">
      <div class="sf-profile-state">
        <SFEmptyState
          :title="t('profile.notFound.title')"
          :description="t('profile.notFound.description')"
        />
      </div>
    </template>

    <template v-else-if="profilePending && !profile">
      <div class="sforum-home__layout sforum-home__layout--with-right sf-profile-layout">
        <SFResponsivePublicSidebar
          owner-id="forum.profile.show"
          :title="t('home.sidebar.navTitle')"
          class="sforum-home__sidebar"
        >
          <SFHomeNavigation
            :categories="navCategories"
            :total-topics="navTotalTopics"
            :pending="categoriesPending"
            :can-create-topic="canCreateTopic"
            navigation-mode="route"
            desktop-only
          />
        </SFResponsivePublicSidebar>
        <section class="sforum-home__main sf-profile-main">
          <SFSkeleton :lines="6" />
        </section>
      </div>
    </template>

    <template v-else-if="profile">
      <div class="sforum-home__layout sforum-home__layout--with-right sf-profile-layout">
        <SFResponsivePublicSidebar
          owner-id="forum.profile.show"
          :title="t('home.sidebar.navTitle')"
          class="sforum-home__sidebar"
        >
          <SFHomeNavigation
            :categories="navCategories"
            :total-topics="navTotalTopics"
            :pending="categoriesPending"
            :can-create-topic="canCreateTopic"
            navigation-mode="route"
            desktop-only
          />
        </SFResponsivePublicSidebar>

        <section class="sforum-home__main sf-profile-main" aria-labelledby="profile-name">
          <SFRegionOutlet page="forum.profile.show" region="content_before" />

          <header class="sf-profile-intro">
            <SFAvatar :name="displayName" :avatar="profile.profile.avatar" size="lg" class="sf-profile-intro__avatar" />
            <div class="sf-profile-intro__content">
              <div class="sf-profile-intro__name-row">
                <h1 id="profile-name">{{ displayName }}</h1>
                <span class="sf-profile-handle">@{{ profile.username }}</span>
                <span class="sf-profile-uid">UID {{ profile.userId }}</span>
              </div>
              <p v-if="profile.profile.bio" class="sf-profile-intro__bio">{{ profile.profile.bio }}</p>
              <p v-else class="sf-profile-intro__bio sf-profile-intro__bio--empty">{{ t('profile.bioEmpty') }}</p>
              <!-- 简介下 meta：位置 / 网站 / 加入时间，对齐 demo B1 profile-intro__meta -->
              <div class="sf-profile-intro__meta">
                <span v-if="profile.profile.location">
                  <UIcon name="i-lucide-map-pin" class="size-3.5" aria-hidden="true" />
                  {{ profile.profile.location }}
                </span>
                <a
                  v-if="websiteHref"
                  :href="websiteHref"
                  target="_blank"
                  rel="noopener noreferrer nofollow"
                >
                  <UIcon name="i-lucide-link" class="size-3.5" aria-hidden="true" />
                  {{ websiteHost }}
                </a>
                <span>
                  <UIcon name="i-lucide-calendar-days" class="size-3.5" aria-hidden="true" />
                  {{ t('profile.joinedOn', { date: formatDate(profile.joinedAt) }) }}
                </span>
              </div>
            </div>
            <div class="sf-profile-intro__actions">
              <NuxtLink
                v-if="isSelf"
                :to="localePath('/settings/profile')"
                class="sf-button sf-button--primary sf-button--md sf-profile-edit-link"
              >
                <UIcon name="i-lucide-settings-2" class="size-4" aria-hidden="true" />
                {{ t('profile.editProfile') }}
              </NuxtLink>
              <button
                type="button"
                class="sf-profile-share-button"
                :aria-label="t('profile.shareProfile')"
                :title="t('profile.shareProfile')"
                @click="shareProfile"
              >
                <UIcon name="i-lucide-share-2" class="size-4" aria-hidden="true" />
              </button>
            </div>
          </header>

          <SFRegionOutlet page="forum.profile.show" region="profile_header_after" />

          <!-- 分区 tab：内置三项在前，扩展 widget tab 追加在「关于」之后 -->
          <nav class="sf-profile-tabs" :aria-label="t('profile.tabs.ariaLabel')">
            <button
              v-for="tab in profileTabs"
              :key="tab.value"
              type="button"
              class="sf-profile-tab"
              :class="{ 'is-active': activeTab === tab.value }"
              role="tab"
              :aria-selected="activeTab === tab.value ? 'true' : 'false'"
              @click="selectTab(tab.value)"
            >
              {{ tab.label }}
              <span v-if="tab.count !== undefined" class="sf-profile-tab__count">{{ tab.count }}</span>
            </button>
          </nav>

          <template v-if="activeTab === 'about'">
            <section class="sf-profile-about" :aria-label="t('profile.tabs.about')">
              <h2>{{ t('profile.aboutTitle', { name: displayName }) }}</h2>
              <p v-if="profile.profile.bio">{{ profile.profile.bio }}</p>
              <p v-else class="sf-profile-about__empty">{{ t('profile.bioEmpty') }}</p>
              <ul class="sf-profile-detail-list">
                <li v-if="profile.profile.location">
                  <UIcon name="i-lucide-map-pin" class="size-4" aria-hidden="true" />
                  <span>{{ profile.profile.location }}</span>
                </li>
                <li v-if="websiteHref">
                  <UIcon name="i-lucide-link" class="size-4" aria-hidden="true" />
                  <span>
                    <a :href="websiteHref" target="_blank" rel="noopener noreferrer nofollow">
                      {{ websiteHost }}
                    </a>
                  </span>
                </li>
                <li>
                  <UIcon name="i-lucide-calendar-days" class="size-4" aria-hidden="true" />
                  <span>{{ t('profile.joinedOn', { date: formatDate(profile.joinedAt) }) }}</span>
                </li>
                <li v-if="profile.profile.signature">
                  <UIcon name="i-lucide-quote" class="size-4" aria-hidden="true" />
                  <span>{{ profile.profile.signature }}</span>
                </li>
                <li v-for="row in metaRows" :key="row.fieldKey">
                  <UIcon
                    :name="metaHref(row.value) ? 'i-lucide-link' : 'i-lucide-tag'"
                    class="size-4"
                    aria-hidden="true"
                  />
                  <span>
                    <span class="sf-profile-detail-label">{{ row.label }}</span>
                    <template v-if="metaHref(row.value)">
                      <a :href="metaHref(row.value)" target="_blank" rel="noopener noreferrer nofollow">{{ row.value }}</a>
                    </template>
                    <template v-else>{{ row.value }}</template>
                  </span>
                </li>
              </ul>
            </section>
          </template>

          <template v-else-if="activeTab === 'bookmarks'">
            <SFProfileBookmarkTab
              :items="bookmarkItems"
              :self="isSelf"
              :loading="bookmarksLoading"
              :loading-more="bookmarksLoadingMore"
              :has-more="bookmarksHasMore"
              :failed="bookmarksFailed"
              @load-more="loadMoreBookmarks"
              @retry="retryBookmarks"
            />
          </template>

          <template v-else-if="activeTab === 'badges'">
            <SFProfileBadgeTab
              :groups="badgeGroups"
              :loading="badgesLoading"
              :failed="badgesFailed"
              :loaded="badgesLoaded"
              :self="isSelf"
              :focus-slug="focusSlug"
              @retry="retryBadges"
              @update:focus-slug="setFocusSlug"
            />
          </template>

          <template v-else-if="activeWidgetTab">
            <!-- 扩展 widget tab：宿主只提供挂载点。SSR 骨架、失败隔离、退避重试和重试按钮
                 全部由 SFExtensionWidget 自己承担，这里不再叠一层错误 UI。
                 v-if 而非 v-show：切走即卸载 L2 组件、租约监控与 CSS 租约。 -->
            <section class="sf-profile-extension-panel" :aria-label="activeWidgetTabLabel">
              <SFExtensionWidget
                :extension-id="activeWidgetTab.extensionId"
                :component-id="activeWidgetTab.componentId"
                :component-props="widgetComponentProps"
              >
                <SFSkeleton :lines="3" />
              </SFExtensionWidget>
            </section>
          </template>

          <template v-else>
            <!-- 主题 / 回复活动流：状态与取数在 useProfileActivityFeeds，渲染在 SFProfileFeedTab。 -->
            <SFProfileFeedTab
              :kind="activeFeedKind"
              :items="activeFeed.items"
              :topic-count="profile.topicCount"
              :comment-count="profile.commentCount"
              :category-count="categoryParticipationCount"
              :like-received-count="profile.likeReceivedCount ?? 0"
              :days-visited="profile.daysVisited ?? 0"
              :switching="switchingTab"
              :loading-more="loadingMore"
              :show-load-more="showLoadMore"
              :rendered-at="renderedAt"
              @load-more="loadMoreActivities"
            />
          </template>

          <SFRegionOutlet page="forum.profile.show" region="content_after" />
        </section>

        <aside class="sforum-home__right sf-profile-right" :aria-label="t('profile.publicDetails')">
          <SFProfileRightRail
            :profile="profile"
            :display-name="displayName"
            :has-public-details="hasPublicDetails"
            :recent-topics="recentTopics"
            :trust-level="trustLevel"
            :trust-level-ladder="trustLevelLadder"
          />
          <SFRegionOutlet page="forum.profile.show" region="sidebar" />
        </aside>
      </div>
    </template>

    <button
      v-if="profile && mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="t('common.close')"
      @click="closeMobileDrawers"
    />

    <aside v-if="profile && mobileInfoOpen" class="sforum-mobile-drawer sforum-mobile-drawer--right">
      <SFPublicMobileRightDrawerHeader
        :title="t('profile.publicDetails')"
        :close-label="t('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <SFProfileRightRail
        :profile="profile"
        :display-name="displayName"
        :has-public-details="hasPublicDetails"
        :recent-topics="recentTopics"
        :trust-level="trustLevel"
        :trust-level-ladder="trustLevelLadder"
        drawer
      />
    </aside>
  </main>
</template>
