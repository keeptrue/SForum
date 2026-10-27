import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('admin list surface consumers', () => {
  test('binds exact placed query descriptors to same-extension commands', () => {
    const composable = source('../../app/composables/admin/useAdminListSurfaces.ts')
    expect(composable).toContain('surface.placementId === target.id')
    expect(composable).toContain('surface.placementContractVersion === target.contractVersion')
    expect(composable).toContain("surface.operation === 'query'")
    expect(composable).toContain("candidate.operation === 'command'")
    expect(composable).toContain('candidate.extensionId === item.surface.extensionId')
    expect(composable).toContain('candidate.placementContractVersion === item.surface.placementContractVersion')
  })

  test('fails closed for selected filters and keeps command idempotency at the client boundary', () => {
    const composable = source('../../app/composables/admin/useAdminListSurfaces.ts')
    const client = source('../../app/composables/admin/useAdminSurfaces.ts')
    expect(composable).toContain('if (!item.view.visibleResourceIdsDeclared) return false')
    expect(composable).toContain('resources.slice(0, 1000)')
    expect(composable).toContain('resourceIds,')
    expect(composable).toContain('filters: currentFilters()')
    expect(client).toContain("headers['Idempotency-Key'] = adminSurfaceIdempotencyKey(surface.id)")
  })

  test('exposes badges and failures without changing columns, filters, row or bulk actions', () => {
    const composable = source('../../app/composables/admin/useAdminListSurfaces.ts')
    // 徽章与列同源：页面只拿 badgesFor(id)，不自己读 cells/links。
    expect(composable).toContain('resolveAdminListBadges(resolved.value, resourceId)')
    // 按钮跟已发现的 surface 走：查询失败时列解析为空，但入口必须还在。
    expect(composable).toContain("const hasListColumns = computed(() => surfaces.value.some(surface => surface.kind === 'list_column'))")
    expect(composable).toMatch(/return \{[\s\S]*\bbadgesFor,[\s\S]*\browActionsFor,/)
    expect(composable).toMatch(/return \{[\s\S]*\bfailures,[\s\S]*\bhasListColumns,/)
    expect(composable).toContain('failureMessage,')
    expect(composable).toContain('bulkActions,')
  })

  test('connects columns, filters, row and bulk actions, and selected-user regions', () => {
    // 页面只把 surface 对象交给表格/抽屉；消费方各自渲染列、筛选、行/批量动作与详情区域。
    const page = source('../../app/pages/admin/users.vue')
    const table = source('../../app/components/admin/identity/users/SFAdminUserTable.vue')
    const drawer = source('../../app/components/admin/identity/users/SFAdminUserDetailDrawer.vue')
    expect(page).toContain(':surfaces="userListSurfaces"')
    expect(page).toContain('userListSurfaces.filters.value')
    expect(table).toContain('surfaces.columns.value.map')
    expect(table).toContain('surfaces.bulkActions.value')
    expect(table).toContain('surfaces.rowActionsFor')
    expect(drawer).toContain(":kinds=\"['detail_region', 'editor_panel']\"")
  })

  test('binds the extension pages to their own Admin Surface placements', () => {
    const pages = [
      ['../../app/pages/admin/extensions/plugins.vue', "'/extensions/plugins'", "'plugins'"],
      ['../../app/pages/admin/extensions/themes.vue', "'/extensions/themes'", "'themes'"],
      ['../../app/pages/admin/extensions/index.vue', "'/extensions'", "'overview'"]
    ]
    for (const [path, pageId, page] of pages) {
      const text = source(path!)
      expect(text).toContain(`pageId: ${pageId}`)
      expect(text).toContain(`context: { resourceType: 'extension', page: ${page} }`)
      expect(text).toContain('attributes: {')
      expect(text).toContain('stagedVersion: item.stagedVersion?.version ||')
      expect(text).toContain(':badges="rowBadges.get(item.id) || []"')
    }
  })
})
