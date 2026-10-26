import { describe, expect, test } from 'bun:test'

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { ADMIN_NAV_SECTION_IDS, adminSidebarNavigation } from '../../app/config/adminModules'

function source(relative: string): string {
  return readFileSync(fileURLToPath(new URL(relative, import.meta.url)), 'utf8')
}

function locale(name: string): Record<string, unknown> {
  return JSON.parse(source(`../../i18n/locales/${name}.json`)) as Record<string, unknown>
}

function lookup(catalog: Record<string, unknown>, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => {
    if (!node || typeof node !== 'object') return undefined
    return (node as Record<string, unknown>)[part]
  }, catalog)
}

describe('admin shared navigation sections', () => {
  const layout = source('../../app/layouts/admin.vue')

  test('every section folder is registered, and every registration has a folder', () => {
    // 两处必须一致：注册表决定「哪些 group 值算共享分区」，文件夹决定容器长什么样。
    // 少一边就会出现「插件写进了一个不存在的分区」或「分区永远没有成员」。
    const sectionIds = adminSidebarNavigation
      .flat()
      .filter((entry): entry is Extract<typeof entry, { type: 'folder' }> => entry.type === 'folder' && Boolean((entry as { sectionId?: string }).sectionId))
      .map(entry => (entry as { sectionId?: string }).sectionId!)

    expect(sectionIds.length).toBeGreaterThan(0)
    expect([...sectionIds].sort()).toEqual([...ADMIN_NAV_SECTION_IDS].sort())
  })

  test('the AI section is a first-level menu holding the core console page', () => {
    const aiFolder = adminSidebarNavigation
      .flat()
      .find(entry => entry.type === 'folder' && (entry as { sectionId?: string }).sectionId === 'ai-tools') as
      | { labelKey: string, children: readonly { type: string, pageId?: string }[] }
      | undefined

    expect(aiFolder).toBeDefined()
    expect(aiFolder!.labelKey).toBe('admin.nav.aiTools')
    expect(aiFolder!.children.map(child => child.pageId)).toContain('/ai')
  })

  test('the core AI console is no longer nested under system settings', () => {
    const systemFolder = adminSidebarNavigation
      .flat()
      .find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.system') as
      | { children: readonly { pageId?: string }[] }
      | undefined

    expect(systemFolder).toBeDefined()
    expect(systemFolder!.children.map(child => child.pageId)).not.toContain('/ai')
  })

  test('plugin pages join the section instead of forming a second group', () => {
    // 插件页按 manifest 的 group 并入 Core 分区；未注册的 group 保持旧行为。
    expect(layout).toContain('ADMIN_NAV_SECTION_IDS')
    expect(layout).toContain('sectionExtensionPages')
    expect(layout).toContain('isRegisteredNavSection(item.group)')
    expect(layout).toContain('entry.sectionId')
    // 父菜单要在子项（插件页）激活时也高亮/展开。
    expect(layout).toContain('children.some(child => child.active)')
  })

  test('ships the new nav label in both locales', () => {
    for (const name of ['zh-CN', 'en-US']) {
      const catalog = locale(name)
      expect(typeof lookup(catalog, 'admin.nav.aiTools')).toBe('string')
      // 一级菜单与页面同名会让侧栏出现两层「AI 能力」，页面改叫控制台。
      expect(lookup(catalog, 'admin.nav.ai')).not.toBe(lookup(catalog, 'admin.nav.aiTools'))
    }
  })
})
