import type { Ref } from 'vue'
import type { ProfileBadgeGroup } from '~/utils/forum/forumBadge'

/** GET /profiles/{username}/badges 的响应（M3 契约：groups[] 扁平条目）。 */
type ProfileBadgesResponse = {
  groups: ProfileBadgeGroup[]
}

export type ProfileBadgesOptions = {
  /** 当前 tab 是否激活；激活才取数（与收藏/活动流同一口径）。 */
  active: () => boolean
}

/**
 * 资料页徽章墙的数据层：一次取全量，切走保留，切回来不再请求。
 *
 * 徽章对所有人相同（actor 无关，后端给了 30s 公开缓存），所以没有分页也没有
 * 私有行口径；失败只置 failed，由 tab 的「重试」按钮重新拉。
 */
export function useProfileBadges(username: Ref<string>, options: ProfileBadgesOptions) {
  const { request } = useApiClient()

  const groups = ref<ProfileBadgeGroup[]>([])
  const loading = ref(false)
  const failed = ref(false)
  const loaded = ref(false)

  function reset() {
    groups.value = []
    loading.value = false
    failed.value = false
    loaded.value = false
  }

  async function load() {
    const name = username.value
    if (!name) {
      reset()
      return
    }
    loading.value = true
    failed.value = false
    try {
      const result = await request<ProfileBadgesResponse>(`/profiles/${encodeURIComponent(name)}/badges`)
      groups.value = Array.isArray(result?.groups) ? result.groups : []
      loaded.value = true
    } catch {
      // 失败不清空已加载内容：重试期间旧数据仍在，比闪成空态稳定。
      failed.value = true
    } finally {
      loading.value = false
    }
  }

  async function ensureLoaded() {
    if (loaded.value || loading.value) {
      return
    }
    await load()
  }

  async function retry() {
    await load()
  }

  // 激活即取数；用户名变化（同一路由换人）时清空重取。
  watch([username, () => options.active()], ([name], previous) => {
    const previousName = previous?.[0]
    if (previousName !== undefined && previousName !== name) {
      reset()
    }
    if (options.active()) {
      void ensureLoaded()
    }
  }, { immediate: true })

  return { groups, loading, failed, ensureLoaded, retry }
}
