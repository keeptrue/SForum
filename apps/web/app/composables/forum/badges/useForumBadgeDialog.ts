import { computed, nextTick } from 'vue'
import type { ForumBadgeRef, ProfileBadgeEntry, ProfileBadgeGroup } from '~/utils/forum/forumBadge'

/**
 * 全局「徽章说明」弹窗的状态与取数。
 *
 * 为什么另起一层，而不是复用 `useProfileBadges`：
 * - `useProfileBadges` 绑的是「资料页当前是否停在徽章 tab」（`active` 开关），结果只活在
 *   那一页的组件树里；这里是**全站一次性的全局弹窗**——入口在主题作者行、评论作者行、
 *   资料页头部，打开时资料页可能根本不存在。
 * - chip 的载荷只有 `ForumBadgeRef`（slug/name/tier/icon/等级名色），**没有**说明、
 *   获得条件、获得时间；这些只在 `GET /profiles/{username}/badges` 的徽章墙条目里。
 *   所以必须按 slug 拉一次墙，拿不到就没有可展示的「说明」，绝不伪造对象。
 *
 * 缓存按 username 分桶：同一用户第二次点不再请求；请求失败只置 `failed`，不清已加载的墙
 * （失败后重开仍会重试，因为 `loaded[username]` 还是 false）。
 */
export type ForumBadgeDialogState = {
  open: boolean
  username: string
  slug: string
  /** 加载期的分组提示（chip 不知道分组）；服务端分组名到了以服务端为准。 */
  groupName: string
  /** chip 带来的最小载荷：加载期与降级展示只用于识别「点的是哪一枚」。 */
  fallback: ForumBadgeRef | null
}

export type OpenForumBadgeDialogInput = {
  username: string
  badge: ForumBadgeRef
  groupName?: string
}

export function emptyForumBadgeDialogState(): ForumBadgeDialogState {
  return { open: false, username: '', slug: '', groupName: '', fallback: null }
}

/**
 * 触发弹窗的那颗 chip（关闭后要把焦点还回去）。
 * 模块级而不是 `useState`：DOM 元素不该进 SSR payload；服务端永远不会写它。
 */
let dialogTrigger: HTMLElement | null = null

/** `import.meta.client` 在 Nuxt 构建里是常量；Bun 测试环境没有它，用 document 存在性兜底。 */
function hasDom() {
  return Boolean(import.meta.client) || typeof document !== 'undefined'
}

export function useForumBadgeDialog() {
  const { request } = useApiClient()

  const state = useState<ForumBadgeDialogState>('forum-badge-dialog', emptyForumBadgeDialogState)
  const walls = useState<Record<string, ProfileBadgeGroup[]>>('forum-badge-dialog-walls', () => ({}))
  const loaded = useState<Record<string, boolean>>('forum-badge-dialog-loaded', () => ({}))
  const pending = useState<Record<string, boolean>>('forum-badge-dialog-pending', () => ({}))
  // 失败只对「当前打开的那个人」有意义；切到别的用户时由 open 重置。
  const failed = useState<boolean>('forum-badge-dialog-failed', () => false)

  const wall = computed(() => walls.value[state.value.username] || [])
  const loading = computed(() => Boolean(pending.value[state.value.username]))

  const entry = computed<ProfileBadgeEntry | null>(() => {
    const slug = state.value.slug
    if (!slug) return null
    for (const group of wall.value) {
      const badge = group.badges.find(item => item.slug === slug)
      if (badge) return badge
    }
    return null
  })

  /** 分组名只认服务端：调用方传的 groupName 只在墙还没到时当占位。 */
  const groupName = computed(() => {
    const slug = state.value.slug
    for (const group of wall.value) {
      if (group.badges.some(item => item.slug === slug)) return group.name
    }
    return state.value.groupName
  })

  async function loadWall(username: string) {
    if (!username || loaded.value[username] || pending.value[username]) return
    pending.value = { ...pending.value, [username]: true }
    try {
      const result = await request<{ groups: ProfileBadgeGroup[] }>(
        `/profiles/${encodeURIComponent(username)}/badges`
      )
      walls.value = {
        ...walls.value,
        [username]: Array.isArray(result?.groups) ? result.groups : []
      }
      loaded.value = { ...loaded.value, [username]: true }
    } catch {
      // 失败不伪造条目，也不清掉已缓存的墙：弹窗只显示「暂时不可用」。
      if (state.value.username === username) failed.value = true
    } finally {
      pending.value = { ...pending.value, [username]: false }
    }
  }

  function ensureLoaded(username: string) {
    if (!username || loaded.value[username]) return Promise.resolve()
    return loadWall(username)
  }

  function openBadgeDialog(input: OpenForumBadgeDialogInput) {
    const username = String(input.username || '').trim()
    if (!username || !input.badge?.slug) return
    if (hasDom()) {
      const active = document.activeElement
      dialogTrigger = active instanceof HTMLElement ? active : null
    }
    state.value = {
      open: true,
      username,
      slug: input.badge.slug,
      groupName: String(input.groupName || '').trim(),
      fallback: input.badge
    }
    failed.value = false
    void ensureLoaded(username)
  }

  function closeBadgeDialog() {
    state.value = { ...state.value, open: false }
    if (!hasDom()) return
    const trigger = dialogTrigger
    dialogTrigger = null
    // 等弹窗卸载后再还焦：焦点不能落到已移除的节点上。
    void nextTick(() => {
      if (trigger?.isConnected) trigger.focus()
    })
  }

  return {
    state,
    wall,
    entry,
    groupName,
    loading,
    failed,
    openBadgeDialog,
    closeBadgeDialog,
    ensureLoaded
  }
}
