import type { ComputedRef, Ref } from 'vue'
import {
  useProfileApi,
  type ProfileActivity,
  type ProfileActivityKind,
  type ProfileActivityPage
} from '~/composables/profile/useProfileApi'
import { apiErrorMessage } from '~/composables/useApiClient'

/** 活动流分页口径：SSR 首屏与「加载更多」必须一致，否则页码会错位。 */
export const PROFILE_ACTIVITY_PER_PAGE = 20

export type ProfileActivityFeedState = {
  items: ProfileActivity[]
  page: number
  hasMore: boolean
  loaded: boolean
}

/**
 * tab 值 → 活动流类型。内置 `topics` / `replies` 有活动流，
 * `about` 与扩展 widget tab 没有，返回 null。
 */
export function profileActivityKindForTab(tab: string): ProfileActivityKind | null {
  if (tab === 'topics') {
    return 'topic'
  }
  if (tab === 'replies') {
    return 'comment'
  }
  return null
}

export type ProfileActivityFeedsOptions = {
  /** 当前激活 tab；决定 `activeFeed` 与「加载更多」作用于哪条流。 */
  activeTab: Ref<string>
  /** SSR 首屏第一页；失败时为 null，客户端挂载后会再试一次。 */
  initialTopicPage?: ProfileActivityPage | null
  /** 右栏最近主题也计入「参与分类数」。 */
  recentTopics?: () => { categorySlug: string }[]
}

export type ProfileActivityFeeds = {
  activeFeed: ComputedRef<ProfileActivityFeedState>
  switchingTab: Ref<boolean>
  loadingMore: Ref<boolean>
  showLoadMore: ComputedRef<boolean>
  categoryParticipationCount: ComputedRef<number>
  ensureFeedLoaded: (tab: string) => Promise<void>
  loadMoreActivities: () => Promise<void>
}

function emptyFeed(): ProfileActivityFeedState {
  return { items: [], page: 0, hasMore: false, loaded: false }
}

function activityKey(activity: ProfileActivity): string {
  return `${activity.kind}:${activity.commentId || activity.topic.id}:${activity.createdAt}`
}

/**
 * 资料页「主题 / 回复」两条活动流的状态与取数。
 *
 * 两条流都常驻在资料页 setup 里，切 tab 只切 `activeTab`：已加载的流不重新请求，
 * 切回来立即可见。渲染交给 `SFProfileFeedTab`，这里只管数据与错误提示。
 */
export function useProfileActivityFeeds(
  username: Ref<string>,
  options: ProfileActivityFeedsOptions
): ProfileActivityFeeds {
  const profileApi = useProfileApi()
  const { t } = useI18n()
  const toast = useToast()

  const topicFeed = ref<ProfileActivityFeedState>(emptyFeed())
  const replyFeed = ref<ProfileActivityFeedState>(emptyFeed())
  const switchingTab = ref(false)
  const loadingMore = ref(false)

  if (options.initialTopicPage) {
    topicFeed.value = {
      items: [...options.initialTopicPage.items],
      page: options.initialTopicPage.page,
      hasMore: options.initialTopicPage.hasMore,
      loaded: true
    }
  }

  const activeFeed = computed(() => {
    if (options.activeTab.value === 'replies') {
      return replyFeed.value
    }
    if (options.activeTab.value === 'topics') {
      return topicFeed.value
    }
    return emptyFeed()
  })

  const showLoadMore = computed(() => {
    // 「关于」和扩展 widget 面板都没有分页，只有两条活动流会有下一页。
    if (!profileActivityKindForTab(options.activeTab.value)) {
      return false
    }
    return activeFeed.value.loaded && activeFeed.value.hasMore
  })

  const categoryParticipationCount = computed(() => {
    const slugs = new Set<string>()
    for (const activity of [...topicFeed.value.items, ...replyFeed.value.items]) {
      if (activity.topic.categorySlug) {
        slugs.add(activity.topic.categorySlug)
      }
    }
    for (const topic of options.recentTopics?.() || []) {
      if (topic.categorySlug) {
        slugs.add(topic.categorySlug)
      }
    }
    return slugs.size
  })

  function feedRefForKind(kind: ProfileActivityKind) {
    return kind === 'comment' ? replyFeed : topicFeed
  }

  async function ensureFeedLoaded(tab: string) {
    const kind = profileActivityKindForTab(tab)
    if (!kind || !username.value) {
      return
    }
    const feed = feedRefForKind(kind)
    if (feed.value.loaded) {
      return
    }
    switchingTab.value = true
    try {
      const page = await profileApi.listPublicActivities(username.value, {
        kind,
        page: 1,
        perPage: PROFILE_ACTIVITY_PER_PAGE
      })
      feed.value = {
        items: [...page.items],
        page: page.page,
        hasMore: page.hasMore,
        loaded: true
      }
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-alert-triangle',
        title: apiErrorMessage(error) || t('profile.timeline.loadFailed')
      })
    } finally {
      switchingTab.value = false
    }
  }

  // SSR 初始活动拉取失败（首屏 catch → null）时 loaded 保持 false，
  // 默认 Topics tab 会静默显示"暂无动态"。客户端挂载后重试一次，
  // 再失败则走 ensureFeedLoaded 的 toast 提示，而不是无声吞掉错误。
  onMounted(() => {
    if (!topicFeed.value.loaded) {
      void ensureFeedLoaded('topics')
    }
  })

  async function loadMoreActivities() {
    const kind = profileActivityKindForTab(options.activeTab.value)
    if (!kind || !username.value || loadingMore.value) {
      return
    }
    const feed = feedRefForKind(kind)
    if (!feed.value.hasMore) {
      return
    }
    loadingMore.value = true
    try {
      const nextPage = feed.value.page + 1
      const page = await profileApi.listPublicActivities(username.value, {
        kind,
        page: nextPage,
        perPage: PROFILE_ACTIVITY_PER_PAGE
      })
      const seen = new Set(feed.value.items.map(activityKey))
      const appended = page.items.filter((item) => {
        const key = activityKey(item)
        if (seen.has(key)) {
          return false
        }
        seen.add(key)
        return true
      })
      feed.value = {
        items: [...feed.value.items, ...appended],
        page: page.page,
        hasMore: page.hasMore,
        loaded: true
      }
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-alert-triangle',
        title: apiErrorMessage(error) || t('profile.timeline.loadMoreFailed')
      })
    } finally {
      loadingMore.value = false
    }
  }

  return {
    activeFeed,
    switchingTab,
    loadingMore,
    showLoadMore,
    categoryParticipationCount,
    ensureFeedLoaded,
    loadMoreActivities
  }
}
