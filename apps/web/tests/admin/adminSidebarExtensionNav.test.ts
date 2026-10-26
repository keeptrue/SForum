import { describe, expect, test } from 'bun:test'

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { adminSidebarNavigation, isExtensionAdminPageId, isAdminNavigationEntryActive, type AdminNavigationFolderEntry } from '../../app/config/adminModules'
import {
  adminExtensionNavPageId,
  extensionAdminPageRoute,
  extensionPageSitsUnderExtensionsFolder,
  type AdminExtensionNavigationItem
} from '../../app/utils/admin/adminExtensions'

function source(relative: string): string {
  return readFileSync(fileURLToPath(new URL(relative, import.meta.url)), 'utf8')
}

function extensionFolder(): AdminNavigationFolderEntry {
  const folder = adminSidebarNavigation
    .flat()
    .find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.extensions')

  expect(folder).toBeDefined()
  return folder as AdminNavigationFolderEntry
}

function navigationItem(overrides: Partial<AdminExtensionNavigationItem> = {}): AdminExtensionNavigationItem {
  return {
    extensionId: 'sforum.store',
    extensionName: 'Store',
    extensionType: 'plugin',
    extensionStatus: 'enabled',
    path: '/catalog',
    label: 'Catalog',
    description: '',
    icon: 'i-lucide-plug',
    view: 'settings',
    order: 10,
    ...overrides
  }
}

// 一个扩展声明了「应用商城」分组，另一个并入了 Core 注册分区，两个不该再归「扩展管理」。
const declaredGroups: AdminExtensionNavigationItem[] = [
  navigationItem({ path: '/catalog', group: '应用商城' }),
  navigationItem({ extensionId: 'sforum.ai', path: '/console', group: 'ai-tools' }),
  navigationItem({ path: '/blank', group: '   ' }),
  navigationItem({ path: '/flat' })
]

describe('extension page menu ownership', () => {
  test('declaring a group hands the page to that menu, not to extension management', () => {
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, '/extensions/sforum.store/pages/catalog')).toBe(false)
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, '/extensions/sforum.ai/pages/console')).toBe(false)
  })

  test('a blank or missing group keeps the page under extension management', () => {
    // 空白 group 与无 group 在侧栏里都没有独立菜单，必须仍归「扩展管理」。
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, '/extensions/sforum.store/pages/blank')).toBe(true)
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, '/extensions/sforum.store/pages/flat')).toBe(true)
  })

  test('unknown page ids default to extension management instead of silently dropping ownership', () => {
    // 导航数据尚未加载（或清单刚变）时为真空：宁可高亮扩展管理，也不要侧栏全暗。
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, '/extensions/other.plugin/pages/nothing')).toBe(true)
    expect(extensionPageSitsUnderExtensionsFolder(declaredGroups, null)).toBe(true)
    expect(extensionPageSitsUnderExtensionsFolder([], '/extensions/sforum.store/pages/catalog')).toBe(true)
  })

  test('the extension management folder only highlights core extension pages', () => {
    const folder = extensionFolder()

    // catalog 属于「应用商城」，静态 config 不能替它把「扩展管理」点亮。
    expect(isExtensionAdminPageId('/extensions/sforum.store/pages/catalog')).toBe(true)
    expect(isAdminNavigationEntryActive(folder, '/extensions/sforum.store/pages/catalog')).toBe(false)
    expect(isAdminNavigationEntryActive(folder, '/extensions/plugins')).toBe(true)
    expect(isAdminNavigationEntryActive(folder, '/extensions')).toBe(true)
  })

  test('the sidebar page id delegates to the route helper shared with the extension page', () => {
    expect(adminExtensionNavPageId({ extensionId: 'a.b', path: '/settings' })).toBe('/extensions/a.b/pages/settings')
    // 委托给同一个路由函数：侧栏 pageId 与 pages/[...pagePath].vue 算出的 id 不能各写一套。
    expect(adminExtensionNavPageId({ extensionId: 'a.b', path: '/settings' }))
      .toBe(extensionAdminPageRoute('a.b', '/settings'))
    // 空路径归一化成关于页，和页面组件自身一致。
    expect(adminExtensionNavPageId({ extensionId: 'a.b', path: '/' })).toBe(extensionAdminPageRoute('a.b', '/'))
    expect(adminExtensionNavPageId({ extensionId: 'a.b', path: '' })).toBe('/extensions/a.b/pages/about')
  })

  test('the admin shell wires the placement rule into the extension folder highlight', () => {
    const layout = source('../../app/layouts/admin.vue')
    // 归属判定是纯函数（行为已在本文件覆盖），这一条只钉住「布局确实调用它」这条接线，
    // 防止接线被删掉时纯函数测试仍全绿。
    expect(layout).toContain('extensionPageSitsUnderExtensionsFolder(')
    expect(layout).toContain('activeExtensionPageIsFlat')
  })
})
