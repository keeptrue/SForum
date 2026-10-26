import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('admin surface outlet', () => {
  test('mounts exact placement consumers and permission-filtered navigation in the admin shell', () => {
    const layout = source('../../app/layouts/admin.vue')
    expect(layout).toContain("import SFAdminSurfaceOutlet from '~/components/admin/SFAdminSurfaceOutlet.vue'")
    expect(layout).toContain("adminSurfaces.list(undefined, 'navigation')")
    expect(layout).toContain('adminSurfacePlacementPageId(surface.placementId)')
    expect(layout).toContain('<SFAdminSurfaceOutlet :page-id="currentAdminPageId" />')
  })

  test('renders only normalized descriptors and binds mutations to exact command contracts', () => {
    const outlet = source('../../app/components/admin/SFAdminSurfaceOutlet.vue')
    const client = source('../../app/composables/admin/useAdminSurfaces.ts')
    const drawer = source('../../app/components/admin/identity/users/SFAdminUserDetailDrawer.vue')
    expect(drawer).toContain("import SFAdminSurfaceOutlet from '~/components/admin/SFAdminSurfaceOutlet.vue'")
    expect(outlet).toContain('normalizeAdminSurfaceOutput(result.output)')
    expect(outlet).toContain('server: false')
    expect(outlet).toContain('mounted && placement && (pending || error || hasContent)')
    expect(outlet).toContain("surface.action === 'add'")
    expect(outlet).toContain("surface.operation === 'query'")
    expect(outlet).toContain("candidate.operation === 'command'")
    expect(outlet).toContain('candidate.extensionId === item.surface.extensionId')
    expect(outlet).toContain('candidate.placementContractVersion === item.surface.placementContractVersion')
    expect(outlet).toContain("...resolvedByKind('editor_panel')")
    expect(outlet).not.toContain('v-html')
    expect(client).toContain("headers['Idempotency-Key'] = adminSurfaceIdempotencyKey(surface.id)")
  })

  test('keeps bilingual stable operator feedback', () => {
    const en = JSON.parse(source('../../i18n/locales/en-US.json'))
    const zh = JSON.parse(source('../../i18n/locales/zh-CN.json'))
    for (const key of [
      'loadFailed', 'invalidResult', 'completed', 'failed', 'submit', 'open', 'emptyValue', 'yes', 'no',
      'extensions', 'filterAll', 'selectVisible', 'selectResource', 'selectedCount'
    ]) {
      expect(en.admin.surfaces[key]).toBeTruthy()
      expect(zh.admin.surfaces[key]).toBeTruthy()
    }
  })
})
