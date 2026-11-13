/**
 * 徽章详情弹窗与 URL 的单一对齐点（`?badge=<slug>`）。
 *
 * 与 `useProfileTabQuery` 同一套手法，理由也一样：详情弹窗是**可分享的地址**，
 * 不能只活在组件本地 ref 里。`/u/{username}?tab=badges&badge=first-post` 与
 * `/badges?badge=first-post` 打开后必须直接停在那一枚上，刷新不丢。
 *
 * 三条口径：
 * 1. 初始化读一次 `?badge=`；非法形状（数组/空白/非字符串）当作没有。之后地址栏是事实来源：
 *    前进/后退与「同一组件换人」都由 `watch(route.query.badge)` 对齐（写回路径自带
 *    「值相同就不写」的短路，不成环）。
 * 2. 打开写回 query，关闭删除该键并保留其它参数（`?tab=`、分页等不受影响）。
 * 3. slug 在已加载清单里不存在（徽章被停用/删除/换成新 key）时**静默清 query**，
 *    不弹一个空弹窗 —— 地址栏里留一个打不开的 `?badge=` 比没有更糟。
 *    「已加载」由调用方用 `loaded` 判定：清单还没到就清，等于把深链吃掉。
 *
 * 组件只消费 `focusSlug`，不自己碰 route/router。
 */
export function useBadgeDetailQuery() {
  const route = useRoute()
  const router = useRouter()

  function fromQuery(value: unknown): string {
    const raw = Array.isArray(value) ? value[0] : value
    return typeof raw === 'string' ? raw.trim() : ''
  }

  const focusSlug = ref<string>(fromQuery(route.query.badge))

  /** 写回 query；空 slug 删除该键，地址栏保持干净。 */
  function syncBadgeQuery(slug: string) {
    const target = String(slug || '').trim()
    if (fromQuery(route.query.badge) === target) {
      return
    }
    // SSR 期间不碰 router（Nuxt 会对 setup 里的导航调用告警）：水合后同一条 watcher
    // 会在客户端再跑一次，地址栏那时才对齐。
    if (typeof window === 'undefined') return
    const query = { ...route.query }
    if (target) {
      query.badge = target
    } else {
      delete query.badge
    }
    void router.replace({ query })
  }

  /** 打开某一枚：本地先变（弹窗立刻出现），地址栏随后跟上。 */
  function openBadge(slug: string) {
    const target = String(slug || '').trim()
    if (!target) return
    focusSlug.value = target
    syncBadgeQuery(target)
  }

  /** 关闭：清本地并删 query。 */
  function closeBadge() {
    if (!focusSlug.value) {
      syncBadgeQuery('')
      return
    }
    focusSlug.value = ''
    syncBadgeQuery('')
  }

  /** 子组件 `update:focusSlug` 的唯一落点：空串 = 关闭，非空 = 打开那一枚。 */
  function setFocusSlug(slug: string) {
    const target = String(slug || '').trim()
    if (target) {
      openBadge(target)
      return
    }
    closeBadge()
  }

  /** slug 已不在可展示清单里（未知/停用）：静默清掉，不报错也不弹空弹窗。 */
  function resetBadgeIfMissing(available: string[]) {
    if (!focusSlug.value) return
    if (available.includes(focusSlug.value)) return
    closeBadge()
  }

  // 地址栏是唯一事实来源：浏览器前进/后退、以及 `/u/a?badge=x` → `/u/b` 复用同一组件时，
  // `focusSlug` 都要跟着地址栏走。写回路径自带「值相同就不写」的短路，不会和这里成环。
  watch(() => fromQuery(route.query.badge), (next) => {
    if (next === focusSlug.value) return
    focusSlug.value = next
  })

  return { focusSlug, openBadge, closeBadge, setFocusSlug, syncBadgeQuery, resetBadgeIfMissing }
}
