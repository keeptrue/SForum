/**
 * 前端权限判定的唯一权威实现（`usePermissions().can()`、`useAuthSession().can`、
 * `hasPermission()` 全部转发到这里）。
 *
 * 只查后端下发的生效权限集，**不做父→子展开**：
 *
 * - API 侧 `ExpandEffectivePermissions(keys, denied)` 已经把父权限展开成子权限，
 *   并且跳过被显式 deny 的子权限，`CurrentUser.permissions` 就是这个最终集合
 *   （契约见 `contracts/openapi/schemas/identity.yaml#/CurrentUser/permissions`）。
 * - 会话不下发 denied 集合，所以前端**没有**判断「某个子权限被 deny」的信息。
 *   前端一旦自己按父权限展开，就会把管理员 deny 掉的子权限重新放行：菜单出现
 *   入口、点进去 API 403（F-1）。语义必须与后端 `Actor.Can()` 同构 —— 后端
 *   也是「显式 false 条目压过父权限展开」，前端查表即等价。
 */
export type PermissionSubject = {
  roleKeys: readonly string[]
  permissions: readonly string[]
} | null | undefined

export function canWithUser(user: PermissionSubject, permission: string): boolean {
  if (!user || !permission) {
    return false
  }
  // 与会话 composable 和后端 policy 保持一致：super_admin 拥有全部权限。
  if (user.roleKeys.includes('super_admin')) {
    return true
  }
  return user.permissions?.includes(permission) === true
}
