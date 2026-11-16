import { describe, expect, test } from 'bun:test'

import {
  EXTENSION_NAVIGATION_PERMISSIONS,
  canReadExtensionNavigation,
  loadAdminExtensionNavigation
} from '~/utils/admin/adminExtensionNavigationGate'
import type { AdminExtensionNavigationItem } from '~/utils/admin/adminExtensions'
import { canWithUser } from '~/utils/identity/permissionCheck'

/**
 * task-26：后台 shell 拉取扩展导航的准入里不得出现父键 `extension.manage`。
 *
 * `extension.manage` 是后端 `ExpandEffectivePermissions` 的展开来源，持父权限的会话里
 * **始终带着它**。把它留在候选里，管理员 deny 掉 `extension.view` /
 * `extension.plugin.manage` / `extension.theme.manage` 之后，shell 仍会请求
 * `/admin/extensions/navigation` 并渲染扩展菜单 —— 入口在、点进去 403。
 *
 * 两侧都验：父权限未 deny 时子键本来就在展开集合里 → 照常拉取；三个子键都被 deny、
 * 只剩父键 → `request` 一次都不许发。把父键加回候选，denied 用例必红。
 */

const EXTENSION_PARENT = 'extension.manage'
const EXTENSION_CHILDREN = ['extension.view', 'extension.plugin.manage', 'extension.theme.manage']

/** 建模「后端下发什么」：保留父键、展开子键、跳过被 deny 的子键。不是前端判定逻辑。 */
function sessionPermissions(denied: readonly string[] = []): string[] {
  return [EXTENSION_PARENT, ...EXTENSION_CHILDREN.filter(child => !denied.includes(child))]
}

function canFrom(permissions: string[]): (permission: string) => boolean {
  return permission => canWithUser({ roleKeys: ['operator'], permissions }, permission)
}

function recordingRequest(items: AdminExtensionNavigationItem[] = []) {
  const calls: string[] = []
  const request = async <T>(path: string): Promise<T> => {
    calls.push(path)
    return items as unknown as T
  }
  return { calls, request }
}

const navItem: AdminExtensionNavigationItem = {
  extensionId: 'acme.forum',
  extensionName: 'Acme',
  extensionType: 'plugin',
  extensionStatus: 'enabled',
  path: '/acme/settings',
  label: 'Acme',
  description: '',
  icon: 'i-lucide-plug',
  view: 'settings',
  order: 1
}

describe('扩展导航准入候选只认子键', () => {
  test('候选里没有父键 extension.manage', () => {
    expect(EXTENSION_NAVIGATION_PERMISSIONS).toEqual(EXTENSION_CHILDREN)
    expect(EXTENSION_NAVIGATION_PERMISSIONS).not.toContain(EXTENSION_PARENT)
  })
})

describe('持父权限且子键未 deny → 照常拉取', () => {
  test('判定为 true', () => {
    expect(canReadExtensionNavigation(canFrom(sessionPermissions()))).toBe(true)
  })

  test('真的发出请求并带上扩展导航数据', async () => {
    const { calls, request } = recordingRequest([navItem])
    const items = await loadAdminExtensionNavigation(canFrom(sessionPermissions()), request)

    expect(calls).toEqual(['/admin/extensions/navigation'])
    expect(items).toEqual([navItem])
  })
})

describe('deny 三个子键、仅持父键 → 不拉取', () => {
  test('判定为 false（父键仍在集合里）', () => {
    const permissions = sessionPermissions(EXTENSION_CHILDREN)
    expect(permissions).toEqual([EXTENSION_PARENT])
    expect(canReadExtensionNavigation(canFrom(permissions))).toBe(false)
  })

  test('request 一次都不被调用，返回空导航', async () => {
    const { calls, request } = recordingRequest([navItem])
    const items = await loadAdminExtensionNavigation(canFrom(sessionPermissions(EXTENSION_CHILDREN)), request)

    expect(calls).toEqual([])
    expect(items).toEqual([])
  })
})

describe('只持子键（无父键）也能拉取', () => {
  test('extension.view 单独授予即可', async () => {
    const { calls, request } = recordingRequest([navItem])
    const items = await loadAdminExtensionNavigation(canFrom(['extension.view']), request)

    expect(calls).toEqual(['/admin/extensions/navigation'])
    expect(items).toEqual([navItem])
  })
})
