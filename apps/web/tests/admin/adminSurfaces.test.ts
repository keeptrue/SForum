import { describe, expect, test } from 'bun:test'
import {
  adminSurfaceIdempotencyKey,
  adminSurfacePlacementPageId,
  normalizeAdminSurfaceOutput,
  resolveAdminListBadges,
  resolveAdminSurfacePlacement,
  type AdminListBadgeSource,
  type AdminSurfaceContract,
  type AdminSurfaceViewModel
} from '../../app/utils/admin/adminSurfaces'

const emptyView: AdminSurfaceViewModel = {
  title: undefined,
  description: undefined,
  message: undefined,
  value: undefined,
  tone: 'neutral',
  icon: undefined,
  pageId: undefined,
  commandSurfaceId: undefined,
  items: [],
  cells: {},
  links: {},
  options: [],
  visibleResourceIds: [],
  visibleResourceIdsDeclared: false,
  fields: [],
  values: {},
  refresh: false,
  download: undefined
}

function listColumn(overrides: Partial<AdminSurfaceContract> = {}): AdminSurfaceContract {
  return {
    id: 'fixture.admin.surface.update',
    contractVersion: 'sforum.admin.surface@1',
    extensionId: 'sforum.store',
    extensionVersion: '1.0.0',
    kind: 'list_column',
    action: 'add',
    placementId: 'core.component.page.admin.extensions.plugins',
    placementContractVersion: 'sforum.component.page.admin.extensions.plugins@1',
    label: '更新',
    operation: 'query',
    priority: 0,
    invokable: true,
    ...overrides
  }
}

function resolved(
  surface: AdminSurfaceContract,
  view: Partial<AdminSurfaceViewModel>
): AdminListBadgeSource {
  return { surface, view: { ...emptyView, ...view } }
}

describe('admin surface consumers', () => {
  test('resolves static and dynamic exact placements', () => {
    expect(resolveAdminSurfacePlacement('/users')).toEqual({
      id: 'core.component.page.admin.users',
      contractVersion: 'sforum.component.page.admin.users@1',
      route: '/admin/users'
    })
    expect(resolveAdminSurfacePlacement('/attachments/manager')?.id).toBe('core.component.page.admin.attachments')
    expect(adminSurfacePlacementPageId('core.component.page.admin.attachments')).toBe('/attachments/manager')
    expect(resolveAdminSurfacePlacement('/extensions/demo.plugin/pages/audit/log')).toEqual({
      id: 'core.component.page.admin.extensions.extension.id.pages.page.path',
      contractVersion: 'sforum.component.page.admin.extensions.extension.id.pages.page.path@1',
      route: '/admin/extensions/:extensionId/pages/:pagePath*'
    })
    expect(resolveAdminSurfacePlacement('/missing')).toBeUndefined()
    expect(adminSurfacePlacementPageId('core.component.page.admin.users')).toBe('/users')
    expect(adminSurfacePlacementPageId('core.component.page.admin.extensions.extension.id.pages.page.path')).toBeUndefined()
  })

  test('keeps only bounded Host-rendered result descriptors', () => {
    expect(normalizeAdminSurfaceOutput({
      title: 'Risk review',
      tone: 'warning',
      icon: 'i-lucide-shield-alert',
      pageId: '/users',
      commandSurfaceId: 'fixture.admin.surface.submit',
      items: [{ label: 'Score', value: 7 }],
      cells: { '42': 'trusted', '<script>': 'blocked' },
      options: [{ label: 'All', value: 'all' }],
      fields: [{ key: 'note', label: 'Note', type: 'textarea', required: true }],
      download: { url: '/api/v1/exports/42', filename: 'users.csv' },
      html: '<img src=x onerror=alert(1)>'
    })).toEqual({
      title: 'Risk review',
      description: undefined,
      message: undefined,
      value: undefined,
      tone: 'warning',
      icon: 'i-lucide-shield-alert',
      pageId: '/users',
      commandSurfaceId: 'fixture.admin.surface.submit',
      items: [{ label: 'Score', value: 7 }],
      cells: { '42': 'trusted' },
      links: {},
      options: [{ label: 'All', value: 'all' }],
      visibleResourceIds: [],
      visibleResourceIdsDeclared: false,
      fields: [{ key: 'note', label: 'Note', type: 'textarea', required: true, placeholder: undefined, options: [] }],
      values: {},
      refresh: false,
      download: { url: '/api/v1/exports/42', filename: 'users.csv' }
    })
    expect(normalizeAdminSurfaceOutput({ visibleResourceIds: [] })).toMatchObject({
      visibleResourceIds: [],
      visibleResourceIdsDeclared: true
    })
    expect(normalizeAdminSurfaceOutput({ html: '<script>alert(1)</script>', pageId: '//evil.test', icon: '<svg>' })).toBeNull()
  })

  test('keeps only admin-relative resource links', () => {
    expect(normalizeAdminSurfaceOutput({
      links: {
        'sforum.store': '/extensions/sforum.store',
        'short': '/extensions/sforum.store?tab=updates',
        // 资源键是不透明 ID，只限长度；值才是可点击目标，必须过 safeAdminPageId。
        'odd.key': '/extensions/odd',
        'protocol.relative': '//evil.test/x',
        'absolute': 'https://evil.test/x',
        'javascript': 'javascript:alert(1)',
        'backslash': '/extensions/evil\\path',
        'control': '/extensions/\u0000evil',
        'tooLong': `/${'a'.repeat(600)}`
      }
    })).toMatchObject({
      links: {
        'sforum.store': '/extensions/sforum.store',
        'short': '/extensions/sforum.store?tab=updates',
        'odd.key': '/extensions/odd'
      }
    })
  })

  test('treats an empty links map as no result instead of a renderable view', () => {
    expect(normalizeAdminSurfaceOutput({ links: {} })).toBeNull()
    expect(normalizeAdminSurfaceOutput({ links: { 'sforum.store': 'https://evil.test' } })).toBeNull()
  })

  test('builds bounded printable idempotency keys', () => {
    const key = adminSurfaceIdempotencyKey('fixture.admin.surface.command')
    expect(key.length).toBeGreaterThan(20)
    expect(key.length).toBeLessThanOrEqual(128)
    expect(key).toMatch(/^[\x21-\x7e]+$/)
  })
})

describe('admin list badges', () => {
  test('folds only exact add + invokable list columns into badges', () => {
    const source = resolved(listColumn(), {
      title: '可更新',
      tone: 'warning',
      icon: 'i-lucide-arrow-up-circle',
      cells: { 'sforum.store': 'v2.0.0' },
      links: { 'sforum.store': '/extensions/sforum.store' }
    })
    expect(resolveAdminListBadges([source], 'sforum.store')).toEqual([{
      id: 'fixture.admin.surface.update',
      label: 'v2.0.0',
      title: '可更新',
      tone: 'warning',
      icon: 'i-lucide-arrow-up-circle',
      pageId: '/extensions/sforum.store'
    }])

    // 列替换/隐藏或不可调用都不算「附加列」，不能渲染成徽章。
    expect(resolveAdminListBadges([resolved(listColumn({ action: 'replace' }), { cells: { a: 'x' } })], 'a')).toEqual([])
    expect(resolveAdminListBadges([resolved(listColumn({ invokable: false }), { cells: { a: 'x' } })], 'a')).toEqual([])
    expect(resolveAdminListBadges([resolved(listColumn({ kind: 'row_action' }), { cells: { a: 'x' } })], 'a')).toEqual([])
  })

  test('skips empty cells and resources the surface marks invisible', () => {
    const source = resolved(listColumn(), {
      cells: { empty: '', nullish: null, value: '可更新' }
    })
    expect(resolveAdminListBadges([source], 'empty')).toEqual([])
    expect(resolveAdminListBadges([source], 'nullish')).toEqual([])
    expect(resolveAdminListBadges([source], 'missing')).toEqual([])
    expect(resolveAdminListBadges([source], '')).toEqual([])
    expect(resolveAdminListBadges([source], 'value').map(badge => badge.label)).toEqual(['可更新'])

    const filtered = resolved(listColumn(), {
      cells: { hidden: '可更新' },
      visibleResourceIds: ['visible'],
      visibleResourceIdsDeclared: true
    })
    expect(resolveAdminListBadges([filtered], 'hidden')).toEqual([])
    expect(resolveAdminListBadges([filtered], 'visible')).toEqual([])
  })

  test('uses the short column title, not a long description, as the badge tooltip', () => {
    const source = resolved(listColumn({ label: '更新' }), {
      title: '可更新',
      description: '商店发现新版本，并附带了很长的发布说明与迁移提示。',
      cells: { 'sforum.store': '可更新' }
    })
    expect(resolveAdminListBadges([source], 'sforum.store')[0]?.title).toBe('可更新')

    const labelOnly = resolved(listColumn({ label: '更新' }), {
      cells: { 'sforum.store': '可更新' }
    })
    expect(resolveAdminListBadges([labelOnly], 'sforum.store')[0]?.title).toBe('更新')
  })

  test('never inherits a link from Object.prototype for opaque resource ids', () => {
    // 资源 ID 是扩展给的字符串，'constructor'/'toString' 会撞上原型链。
    const source = resolved(listColumn(), {
      cells: { constructor: '可更新', toString: '可更新' },
      links: {}
    })
    expect(resolveAdminListBadges([source], 'constructor')).toMatchObject([{ pageId: undefined }])
    expect(resolveAdminListBadges([source], 'toString')).toMatchObject([{ pageId: undefined }])

    // 同名的自有链接仍然有效。
    const linked = resolved(listColumn(), {
      cells: { constructor: '可更新' },
      links: { constructor: '/extensions/demo' }
    })
    expect(resolveAdminListBadges([linked], 'constructor')).toMatchObject([{ pageId: '/extensions/demo' }])
  })

  test('degrades to a non-clickable badge when the surface declares no link', () => {
    const source = resolved(listColumn(), { cells: { 'sforum.store': '可更新' } })
    expect(resolveAdminListBadges([source], 'sforum.store')).toMatchObject([{ pageId: undefined }])
  })
})
