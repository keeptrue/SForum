/**
 * 资料页 tab 与 URL 的单一对齐点。
 *
 * 作者旁徽章 chip 用 `/u/{username}?tab=badges` 直达，所以 tab 不能只活在组件本地 ref 里：
 * 初始化读一次 `?tab=`（非法值回落默认），切 tab 写回 query，tab 已不存在
 *（收藏/徽章关闭、扩展下线）时回落默认并纠正 query，避免地址栏留着一个假 tab。
 *
 * 组件只消费 `activeTab`，不自己碰 route/router。
 */
export function useProfileTabQuery(defaultTab = 'topics') {
  const route = useRoute()
  const router = useRouter()

  function fromQuery(value: unknown): string | null {
    const raw = Array.isArray(value) ? value[0] : value
    return typeof raw === 'string' && raw.trim() ? raw.trim() : null
  }

  const activeTab = ref<string>(fromQuery(route.query.tab) ?? defaultTab)

  /** 写回 query；默认 tab 不带参数，地址栏保持干净。 */
  function syncTabQuery(tab: string) {
    if (fromQuery(route.query.tab) === tab) {
      return
    }
    const query = { ...route.query }
    if (tab === defaultTab) {
      delete query.tab
    } else {
      query.tab = tab
    }
    void router.replace({ query })
  }

  /** tab 已不存在时回落默认并纠正 query；存在则原地不动。 */
  function resetTabIfMissing(available: string[]) {
    if (available.includes(activeTab.value)) {
      return
    }
    activeTab.value = defaultTab
    syncTabQuery(defaultTab)
  }

  return { activeTab, syncTabQuery, resetTabIfMissing }
}
