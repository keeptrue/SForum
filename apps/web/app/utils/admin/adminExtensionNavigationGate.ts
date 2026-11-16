import type { AdminExtensionNavigationItem } from '~/utils/admin/adminExtensions'

/**
 * 后台 shell 拉取扩展导航（`GET /admin/extensions/navigation`）的准入。
 *
 * **只认细粒度子键。** `extension.manage` 是后端的展开来源
 * （`apps/api/app/Models/Identity/permission_compat.go` 的 `legacyPermissionChildren`
 * 展开出 `extension.view` / `extension.plugin.manage` / `extension.theme.manage`），
 * 持父权限的会话里始终带着它；把它写进候选，管理员 deny 掉三个子键之后 shell 仍会
 * 拉取并渲染扩展菜单（task-26）。未 deny 时子键本来就在展开集合里，不需要父键兜底。
 *
 * 判定与拉取放在一处，是为了让「有没有发这个请求」可以被真实断言，而不是散在
 * `layouts/admin.vue` 的一个内联 OR 里。
 *
 * **已知边界（诚实的空档）**：本文件的用例钉住的是这个唯一权威。如果日后有人绕过
 * `loadAdminExtensionNavigation`、在 `admin.vue` 里重新内联
 * `|| can('extension.manage')`，**没有任何单测会红** —— 只有 review 会。要彻底堵死
 * 就得真挂载 877 行的 admin shell，代价与脆弱度都不值当，故选择零成本标注。
 */
export const EXTENSION_NAVIGATION_PERMISSIONS = [
  'extension.view',
  'extension.plugin.manage',
  'extension.theme.manage'
] as const

export function canReadExtensionNavigation(can: (permission: string) => boolean): boolean {
  return EXTENSION_NAVIGATION_PERMISSIONS.some(permission => can(permission))
}

export function loadAdminExtensionNavigation(
  can: (permission: string) => boolean,
  request: <T>(path: string) => Promise<T>
): Promise<AdminExtensionNavigationItem[]> {
  if (!canReadExtensionNavigation(can)) {
    return Promise.resolve([])
  }
  return request<AdminExtensionNavigationItem[]>('/admin/extensions/navigation')
}
