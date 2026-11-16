import { describe, expect, test } from 'bun:test'

import { findAdminPageDefinition, shouldShowAdminPageInNav } from '~/config/adminModules'
import { canWithUser } from '~/utils/identity/permissionCheck'

/**
 * task-24：菜单准入候选里不得出现父键。
 *
 * 父键（`settings.manage` / `user.manage`）是后端 `ExpandEffectivePermissions` 的展开
 * 来源，持父权限的会话里**始终带着它**；把它写进 `requiredPermissions`，管理员 deny 掉的
 * 子键就会凭父键重新可见（菜单有入口、点进去 API 403）。
 *
 * 两侧都验：父权限展开后子键在集合内 → 入口可见；deny 子键（集合里没有它）→ 入口消失。
 * 把父键加回候选，denied 用例必红。`can` 用的是生产权威 `canWithUser`，
 * 门用的是 `layouts/admin.vue#buildNavigationItem` 同一个 `shouldShowAdminPageInNav`。
 */

const SETTINGS_PARENT = 'settings.manage'
const SETTINGS_CHILDREN = [
  'settings.site.manage',
  'settings.mail.manage',
  'settings.notifications.manage',
  'settings.avatar.manage',
  'settings.appearance.manage',
  'forum.settings.manage',
  'entity_meta.manage'
]

/** 建模「后端下发什么」：展开子键、跳过被 deny 的子键。不是前端判定逻辑。 */
function settingsSession(denied: readonly string[] = []): string[] {
  return [SETTINGS_PARENT, ...SETTINGS_CHILDREN.filter(child => !denied.includes(child))]
}

/** `user.manage` 只展开 `user.view`（不展开高危的 user.permission_override）。 */
function userSession(denied: readonly string[] = []): string[] {
  return ['user.manage', ...(denied.includes('user.view') ? [] : ['user.view'])]
}

const visibility = { professionalMode: true, operationsMode: true }

function navVisible(pageId: string, permissions: string[]): boolean {
  const page = findAdminPageDefinition(pageId)
  if (!page) throw new Error(`unknown admin page: ${pageId}`)
  return shouldShowAdminPageInNav(page, permission => canWithUser({ roleKeys: ['operator'], permissions }, permission), visibility)
}

function candidates(pageId: string): readonly string[] {
  const page = findAdminPageDefinition(pageId)
  if (!page) throw new Error(`unknown admin page: ${pageId}`)
  return page.requiredPermissions ?? []
}

describe('/entity-meta 菜单候选只认子键', () => {
  test('候选里没有父键 settings.manage', () => {
    expect(candidates('/entity-meta')).toEqual(['entity_meta.manage'])
  })

  test('父权限展开后 entity_meta.manage 在集合内 → 入口可见', () => {
    expect(navVisible('/entity-meta', settingsSession())).toBe(true)
  })

  test('deny entity_meta.manage → 入口消失（父键仍在集合里）', () => {
    const permissions = settingsSession(['entity_meta.manage'])
    expect(permissions).toContain(SETTINGS_PARENT)
    expect(navVisible('/entity-meta', permissions)).toBe(false)
  })
})

describe('/webhooks 菜单候选只认子键', () => {
  test('候选里没有父键 settings.manage', () => {
    expect(candidates('/webhooks')).toEqual(['settings.site.manage'])
  })

  test('父权限展开后 settings.site.manage 在集合内 → 入口可见', () => {
    expect(navVisible('/webhooks', settingsSession())).toBe(true)
  })

  test('deny settings.site.manage → 入口消失（父键仍在集合里）', () => {
    const permissions = settingsSession(['settings.site.manage'])
    expect(permissions).toContain(SETTINGS_PARENT)
    expect(navVisible('/webhooks', permissions)).toBe(false)
  })
})

describe('/users 菜单候选只认子键', () => {
  test('候选里没有父键 user.manage', () => {
    expect(candidates('/users')).toEqual(['user.view'])
  })

  test('user.manage 展开出 user.view → 入口可见', () => {
    expect(navVisible('/users', userSession())).toBe(true)
  })

  test('deny user.view → 入口消失（user.manage 仍在集合里）', () => {
    const permissions = userSession(['user.view'])
    expect(permissions).toContain('user.manage')
    expect(navVisible('/users', permissions)).toBe(false)
  })
})

describe('/permissions 菜单候选只认子键', () => {
  test('候选里没有父键 user.manage', () => {
    expect(candidates('/permissions')).toEqual(['role.manage', 'user.view'])
  })

  test('user.manage 展开出 user.view → 入口可见', () => {
    expect(navVisible('/permissions', userSession())).toBe(true)
  })

  test('deny user.view 且无 role.manage → 入口消失', () => {
    expect(navVisible('/permissions', userSession(['user.view']))).toBe(false)
  })
})
