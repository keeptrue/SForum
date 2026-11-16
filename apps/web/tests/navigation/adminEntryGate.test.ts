import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { joinAdminRoutePath, normalizeAdminRoutePrefix } from '../../app/utils/admin/adminRoutePrefix'
import { canWithUser } from '../../app/utils/identity/permissionCheck'

type U = { roleKeys: string[], permissions?: string[] } | null

// 直接调用生产权威实现（super_admin 绕过 + 纯查表），不再复刻一份会漂移的语义。
function can(user: U, permission: string): boolean {
  return canWithUser(user && { roleKeys: user.roleKeys, permissions: user.permissions ?? [] }, permission)
}

describe('admin entry permission gate', () => {
  const cases: Array<[string, U, boolean]> = [
    ['guest', null, false],
    ['member only (no admin.access)', { roleKeys: ['member'], permissions: ['topic.create'] }, false],
    ['super_admin via role bypass', { roleKeys: ['super_admin', 'member'], permissions: [] }, true],
    ['moderator with admin.access', { roleKeys: ['moderator'], permissions: ['admin.access', 'moderation.review'] }, true],
    ['operator with admin.access', { roleKeys: ['operator'], permissions: ['admin.access'] }, true],
    ['direct override grants admin.access', { roleKeys: ['member'], permissions: ['admin.access'] }, true]
  ]

  for (const [name, user, expected] of cases) {
    test(`${name} -> ${expected}`, () => {
      expect(can(user, 'admin.access')).toBe(expected)
    })
  }

  test('admin entry path follows the operator-configured prefix, not a hardcoded one', () => {
    expect(joinAdminRoutePath(normalizeAdminRoutePrefix('/control-panel'), '/')).toBe('/control-panel')
    expect(joinAdminRoutePath(normalizeAdminRoutePrefix('/manage'), '/')).toBe('/manage')
    expect(joinAdminRoutePath(normalizeAdminRoutePrefix(''), '/')).toBe('/control-panel')
  })

  test('the composable sources the same key the API guards use', () => {
    const perms = readFileSync(new URL('../../app/composables/identity/usePermissions.ts', import.meta.url), 'utf8')
    const menu = readFileSync(new URL('../../app/composables/navigation/usePublicUserMenu.ts', import.meta.url), 'utf8')
    expect(perms).toContain("adminAccess: 'admin.access'")
    expect(menu).toContain('can(FORUM_PERMISSIONS.adminAccess)')
  })
})
