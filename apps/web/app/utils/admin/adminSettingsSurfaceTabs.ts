/**
 * 站点设置页「插件设置分区」（`admin.settings.tabs`）的加载态判定。
 *
 * 权限归属已定：插件分区属于**扩展管理员面** —— 可见要 `extension.view`，
 * 保存要 `extension.plugin.manage`。因此 `RoleOperator` 看不到它是正确行为，
 * 但他必须被告知「看不见是因为没有扩展查看权限」，而不是看到一个什么都没有的
 * tab 栏然后以为「本站没有插件设置」。
 *
 * 三种状态互斥，且 **失败绝不能被当成「真的为空」**：
 * - `loading`：还在取；
 * - `error`：403 / 网络失败 / 服务不可用 —— 出非阻塞提示，不伪造 tab；
 * - `empty`：请求成功且确实没有插件分区；
 * - `ready`：请求成功且有插件分区。
 */
export type AdminSurfaceTabsViewState = 'loading' | 'empty' | 'ready' | 'error'

export type AdminSurfaceTabsLoadState = 'loading' | 'ready' | 'error'

export function adminSurfaceTabsViewState(
  loadState: AdminSurfaceTabsLoadState,
  tabCount: number
): AdminSurfaceTabsViewState {
  if (loadState === 'loading') return 'loading'
  if (loadState === 'error') return 'error'
  return tabCount > 0 ? 'ready' : 'empty'
}

/**
 * 失败原因是「没有权限」还是「暂时不可用」：两者的可读解释不同。
 * 403（含 401 之外的权限拒绝）按权限处理，其余按暂时失败处理。
 */
export type AdminSurfaceTabsFailureKind = 'forbidden' | 'unavailable'

export function adminSurfaceTabsFailureKind(statusCode: number): AdminSurfaceTabsFailureKind {
  return statusCode === 403 || statusCode === 401 ? 'forbidden' : 'unavailable'
}

/**
 * 只有 `ready` 才把插件 tab 交给 tab 栏。error / loading / empty 一律给空数组 ——
 * 失败时既不能伪造 tab，也不能拿上一次的残留冒充当前可见分区。
 */
export function adminSettingsSurfaceTabsForState<T>(
  state: AdminSurfaceTabsViewState,
  tabs: T[] | undefined
): T[] {
  return state === 'ready' ? (tabs || []) : []
}
