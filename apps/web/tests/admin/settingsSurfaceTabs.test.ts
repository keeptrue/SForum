import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  ADMIN_SETTINGS_EXTENSION_TAB_PREFIX,
  isExtensionSettingsTabId
} from '../../app/utils/admin/adminExtensions'
import {
  adminSettingsSurfaceTabsForState,
  adminSurfaceTabsFailureKind,
  adminSurfaceTabsViewState
} from '../../app/utils/admin/adminSettingsSurfaceTabs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const page = () => source('../../app/pages/admin/settings/index.vue')
const extensionTab = () => source('../../app/components/admin/settings/contributions/SFAdminSettingsExtensionTab.vue')
const draft = () => source('../../app/composables/admin/useAdminExtensionSettingsDraft.ts')
const fixedNav = () => source('../../app/components/admin/settings/shared/SFAdminFixedTabNav.vue')
const zhCN = () => JSON.parse(source('../../i18n/locales/zh-CN.json'))
const enUS = () => JSON.parse(source('../../i18n/locales/en-US.json'))

describe('admin settings surface tabs', () => {
  test('extension tab ids live in their own namespace', () => {
    expect(ADMIN_SETTINGS_EXTENSION_TAB_PREFIX).toBe('ext.')
    expect(isExtensionSettingsTabId('ext.demo.settings')).toBe(true)
    expect(isExtensionSettingsTabId('basic')).toBe(false)
    expect(isExtensionSettingsTabId(undefined)).toBe(false)
  })

  test('fixed eight tabs stay first and plugin tabs append after them', () => {
    const src = page()
    expect(src).toContain('...fixedTabs.value,')
    expect(src).toContain('...surfaceTabs.value.map(tab => ({')
    expect(src).toContain("icon: tab.icon || 'i-lucide-plug'")
    // 固定 8 个 tab 一个都不能少
    for (const id of ['basic', 'accountSecurity', 'registration', 'newcomers', 'maintenance', 'client', 'verification', 'updates']) {
      expect(src).toContain(`{ id: '${id}',`)
    }
  })

  test('normalizeTab accepts dynamic ids and falls back to basic', () => {
    const src = page()
    expect(src).toMatch(/function normalizeTab\(value: unknown\): string \{[\s\S]*const ids = tabs\.value\.map\(tab => tab\.id\)/)
    expect(src).toContain("ids.includes(candidate) ? candidate : 'basic'")
  })

  test('late-arriving surface tabs re-normalise against the URL, not the fallen-back state', () => {
    const src = page()
    // 深链 ?tab=ext.* 只有在归一化基准是 route.query.tab 时才活得过表面晚到；
    // 用 activeTab 当基准会把它钉死在 'basic'（并回头改写 URL）。
    expect(src).toContain('normalizeTab(route.query.tab ?? activeTab.value)')
    expect(src).not.toMatch(/watch\(surfaceTabs,[\s\S]{0,200}normalizeTab\(activeTab\.value\)/)
  })

  test('?tab= sync uses replace, never push', () => {
    const src = page()
    expect(src).toContain('router.replace({ query: { ...route.query, tab } })')
    expect(src).not.toContain('router.push(')
  })

  test('a failing surface-tabs request records a reason instead of swallowing it', () => {
    const src = page()
    expect(src).toContain("request<AdminSettingsSurfaceTab[]>('/admin/settings/surface-tabs')")
    // task-13：不再 `catch { return [] }`；失败带 status/forbidden/message 回到页面。
    expect(src).not.toMatch(/catch \{\s*return \[\]\s*\}/)
    expect(src).toContain("status: 'error'")
    expect(src).toContain('adminSurfaceTabsFailureKind(apiErrorStatusCode(error))')
    expect(src).toContain("const { data: surfaceTabsLoad, pending: surfaceTabsPending, refresh: refreshSurfaceTabs }")
  })

  test('keeps the established admin geometry: text-xl title, toolbar, fixed tab nav, min-w-0 column', () => {
    const src = page()
    expect(src).toContain('class="flex items-center gap-2 text-xl font-bold')
    expect(src).toContain('<UDashboardToolbar')
    expect(src).toContain('<SFAdminFixedTabNav')
    expect(src).toContain('v-if="activeSurfaceTab"')
    // 不另造第二套 tab 栏、不改共享 tab 样式
    expect(src).not.toContain('role="tablist"')
    expect(fixedNav()).toContain('role="tablist"')
    expect(fixedNav()).not.toContain('sforum-settings__tabs')
  })

  test('rotation: the extension tab renders through the existing renderer and owns its save', () => {
    const src = extensionTab()
    expect(src).toContain('<SFExtensionSettingsRenderer')
    expect(src).toContain(':settings="settings"')
    expect(src).toContain("useAdminExtensionSettingsDraft({ settings })")
    expect(src).toContain('@save="save"')
    expect(src).toContain('@reset="reset"')
    expect(src).toContain('@action="executeAction"')
    expect(src).toContain("t('admin.settings.contributions.extensionHint', { extensionId: tab.extensionId })")
  })

  test('the plugin settings draft reuses the extension settings endpoints', () => {
    const src = draft()
    expect(src).toContain('`/admin/extensions/${encodeURIComponent(extensionId.value)}/settings`')
    expect(src).toContain("{ method: 'PUT', body: { values: { ...values.value } } }")
    expect(src).toContain('recommendedExtensionSettingValues(settings.items || [])')
    expect(src).toContain('/settings/actions/${encodeURIComponent(action.id)}')
    expect(src).toContain("mode: 'preserve'")
    // 成功 5s 自动消失；错误不自动消失
    expect(src).toContain('duration: 5000')
    expect(src).toContain('duration: 0')
  })

  test('plugin tab copy exists in both locales', () => {
    for (const locale of [zhCN(), enUS()]) {
      for (const key of ['badge', 'extensionHint', 'saved', 'resetDone', 'secretsPreserved', 'saveFailed', 'actionFailed']) {
        expect(locale.admin.settings.contributions[key]).toBeTruthy()
      }
    }
  })
})

describe('surface-tabs three states (task-13: no silent disappearance)', () => {
  const tab = (id: string) => ({
    id,
    label: { 'zh-CN': '演示设置', 'en-US': 'Demo settings' },
    type: 'settings' as const,
    extensionId: 'demo',
    settings: { items: [] } as never
  })

  test('a failed load (403 / network) is an error state and yields zero plugin tabs', () => {
    // 403 → 权限提示；500/网络 → 暂时失败。两者都是 error，不许冒充 empty。
    expect(adminSurfaceTabsFailureKind(403)).toBe('forbidden')
    expect(adminSurfaceTabsFailureKind(401)).toBe('forbidden')
    expect(adminSurfaceTabsFailureKind(500)).toBe('unavailable')
    expect(adminSurfaceTabsFailureKind(0)).toBe('unavailable')

    const state = adminSurfaceTabsViewState('error', 0)
    expect(state).toBe('error')
    // 即使内存里还留着上一轮数据，error 下也不允许产出 tab（不许伪造/残留）。
    expect(adminSettingsSurfaceTabsForState(state, [tab('ext.demo.settings')])).toEqual([])
  })

  test('a successful empty response is empty (no hint, no plugin tab)', () => {
    const state = adminSurfaceTabsViewState('ready', 0)
    expect(state).toBe('empty')
    expect(adminSettingsSurfaceTabsForState(state, [])).toEqual([])
  })

  test('a successful response with tabs renders them', () => {
    const state = adminSurfaceTabsViewState('ready', 1)
    expect(state).toBe('ready')
    expect(adminSettingsSurfaceTabsForState(state, [tab('ext.demo.settings')])).toHaveLength(1)
    expect(adminSurfaceTabsViewState('loading', 0)).toBe('loading')
    expect(adminSettingsSurfaceTabsForState('loading', [tab('ext.demo.settings')])).toEqual([])
  })

  test('the page only shows the hint on error and never as a toast', () => {
    const src = page()
    expect(src).toContain('data-testid="admin-settings-surface-tabs-hint"')
    expect(src).toContain(`v-if="surfaceTabsState === 'error'"`)
    expect(src).toContain("t('admin.settings.contributions.visibilityForbidden')")
    expect(src).toContain("t('admin.settings.contributions.visibilityUnavailable')")
    expect(src).toContain('adminSettingsSurfaceTabsForState')
    // 非阻塞：用行内提示，不用 Toast；也不把失败写进 tab 栏。
    expect(src).not.toContain('toast.add')
    expect(src).toContain('<details')
    // 核心 8 个 tab 与提示无关，始终在前。
    expect(src).toContain('...fixedTabs.value,')
  })

  test('the permission copy exists in both locales and explains the missing permission', () => {
    for (const locale of [zhCN(), enUS()]) {
      const copy = locale.admin.settings.contributions
      for (const key of ['visibilityTitle', 'visibilityForbidden', 'visibilityUnavailable', 'visibilityDetails', 'visibilityDetailEmpty', 'retry']) {
        expect(copy[key]).toBeTruthy()
      }
      expect(copy.visibilityForbidden).toContain('extension.view')
    }
    expect(zhCN().admin.settings.contributions.visibilityForbidden).toContain('查看扩展')
  })
})
