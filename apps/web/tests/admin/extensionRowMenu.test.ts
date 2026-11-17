import { beforeAll, describe, expect, test } from 'bun:test'
import { useI18nFor, translate, type LocaleName } from '../helpers/locale'
import {
  isProtectedDefaultTheme,
  type AdminExtension
} from '../../app/utils/admin/adminExtensions'
import {
  useExtensionRowMenu,
  type ExtensionMenuItem,
  type ExtensionRowMenuHandlers
} from '../../app/composables/admin/useExtensionRowMenu'

/**
 * 已启用主题的主按钮会退化成「管理」（themeActionState 对 enabled 一律返回
 * active），所以「重新激活 / 恢复默认主题」必须出现在行尾菜单里；这里锁住
 * 该项的出现条件、文案、图标与回调，以及插件行不受影响。
 */

const baseExtension = {
  version: '1.0.0',
  packagePath: 'storage/extensions/demo',
  installedAt: '2026-11-17T00:00:00Z',
  updatedAt: '2026-11-17T00:00:00Z',
  manifest: { sforumVersion: '^1.0.0' }
} satisfies Partial<AdminExtension>

function extension(input: {
  id: string
  type: 'plugin' | 'theme'
  status?: AdminExtension['status']
  source?: AdminExtension['source']
  isDeletable?: boolean
  runtime?: Partial<NonNullable<AdminExtension['runtime']>>
}): AdminExtension {
  return {
    ...baseExtension,
    id: input.id,
    name: input.id,
    type: input.type,
    status: input.status || 'installed',
    source: input.source,
    isDeletable: input.isDeletable,
    manifest: {
      id: input.id,
      name: input.id,
      version: '1.0.0',
      type: input.type,
      sforumVersion: '^1.0.0'
    },
    runtime: input.runtime as AdminExtension['runtime']
  } as AdminExtension
}

/** 菜单按真实 locale 取字：label 断言看到的就是运营看到的文案。 */
function menuFactory(locale: LocaleName, handlers: Partial<ExtensionRowMenuHandlers> = {}) {
  Object.assign(globalThis, { useI18n: useI18nFor(locale) })
  return useExtensionRowMenu({
    canUninstall: () => false,
    uninstall: () => {},
    ...handlers
  })
}

function labelsOf(items: ExtensionMenuItem[]) {
  return items.map(item => item.type === 'separator' ? '---' : item.label || '')
}

function itemByLabel(items: ExtensionMenuItem[], label: string) {
  const found = items.find(item => item.label === label)
  if (!found) {
    throw new Error(`menu item not found: ${label} in ${labelsOf(items).join(' / ')}`)
  }
  return found
}

beforeAll(() => {
  Object.assign(globalThis, {
    useRuntimeConfig: () => ({ public: { adminRoutePrefix: '/control-panel' } }),
    useLocalePath: () => (path: string) => path
  })
})

describe('useExtensionRowMenu theme reactivation entry', () => {
  test('enabled theme with an activate handler gets the reactivate entry between manage and uninstall', () => {
    const activateCalls: AdminExtension[] = []
    const theme = extension({ id: 'uploaded.theme', type: 'theme', status: 'enabled', source: 'uploaded', isDeletable: true })
    const items = menuFactory('zh-CN', {
      activate: item => activateCalls.push(item),
      canUninstall: () => true
    })(theme)[0]

    expect(labelsOf(items)).toEqual([
      translate('zh-CN', 'admin.extensions.manage'),
      translate('zh-CN', 'admin.extensions.reactivateTheme'),
      '---',
      translate('zh-CN', 'admin.extensions.uninstall')
    ])

    const reactivate = itemByLabel(items, translate('zh-CN', 'admin.extensions.reactivateTheme'))
    expect(reactivate.icon).toBe('i-lucide-refresh-cw')
    expect(reactivate.title).toBe(translate('zh-CN', 'admin.extensions.reactivateThemeHint'))
    expect(reactivate.to).toBeUndefined()

    reactivate.onSelect?.()
    expect(activateCalls).toHaveLength(1)
    expect(activateCalls[0]?.id).toBe('uploaded.theme')
  })

  test('the protected built-in default theme is offered as restore-default-theme', () => {
    const activateCalls: AdminExtension[] = []
    const items = menuFactory('zh-CN', {
      activate: item => activateCalls.push(item)
    })(extension({ id: 'sforum.default-theme', type: 'theme', status: 'enabled', source: 'builtin' }))[0]

    const restore = itemByLabel(items, translate('zh-CN', 'admin.extensions.restoreDefaultTheme'))
    expect(restore.icon).toBe('i-lucide-refresh-cw')
    expect(restore.title).toBe(translate('zh-CN', 'admin.extensions.reactivateThemeHint'))
    expect(labelsOf(items)).not.toContain(translate('zh-CN', 'admin.extensions.reactivateTheme'))

    restore.onSelect?.()
    expect(activateCalls.map(item => item.id)).toEqual(['sforum.default-theme'])
  })

  test('no entry when the page does not pass activate, or the theme is not enabled', () => {
    const enabled = extension({ id: 'uploaded.theme', type: 'theme', status: 'enabled', source: 'uploaded' })
    const disabled = extension({ id: 'uploaded.theme', type: 'theme', status: 'disabled', source: 'uploaded' })
    const installed = extension({ id: 'uploaded.theme', type: 'theme', status: 'installed', source: 'uploaded' })

    const withoutHandler = menuFactory('zh-CN')(enabled)[0]
    expect(labelsOf(withoutHandler)).toEqual([translate('zh-CN', 'admin.extensions.manage')])

    const activate = () => {}
    for (const theme of [disabled, installed]) {
      const items = menuFactory('zh-CN', { activate })(theme)[0]
      expect(labelsOf(items)).toEqual([translate('zh-CN', 'admin.extensions.manage')])
    }
  })

  test('plugin rows are untouched even when the page passes activate', () => {
    const plugin = extension({
      id: 'demo.plugin',
      type: 'plugin',
      status: 'enabled',
      runtime: { state: 'running', routeCount: 0, hookCount: 0, providerCount: 0 }
    })
    const items = menuFactory('zh-CN', { activate: () => {} })(plugin)[0]

    expect(labelsOf(items)).toEqual([
      translate('zh-CN', 'admin.extensions.manage'),
      translate('zh-CN', 'admin.extensions.disable'),
      translate('zh-CN', 'admin.extensions.restart'),
      translate('zh-CN', 'admin.extensions.lifecycle.title')
    ])
  })

  test('both locales ship the action copy (en-US is not a key echo)', () => {
    const items = menuFactory('en-US', { activate: () => {} })(
      extension({ id: 'sforum.default-theme', type: 'theme', status: 'enabled', source: 'builtin' })
    )[0]

    expect(itemByLabel(items, translate('en-US', 'admin.extensions.restoreDefaultTheme')).title)
      .toBe(translate('en-US', 'admin.extensions.reactivateThemeHint'))
    expect(translate('en-US', 'admin.extensions.restoreDefaultTheme')).not.toBe('admin.extensions.restoreDefaultTheme')
    expect(translate('en-US', 'admin.extensions.reactivateTheme')).not.toBe('admin.extensions.reactivateTheme')
  })

  test('isProtectedDefaultTheme only matches the built-in default theme', () => {
    expect(isProtectedDefaultTheme({ id: 'sforum.default-theme', source: 'builtin' })).toBe(true)
    expect(isProtectedDefaultTheme({ id: 'sforum.default-theme', source: 'uploaded' })).toBe(false)
    expect(isProtectedDefaultTheme({ id: 'sforum.default-theme', source: undefined })).toBe(false)
    expect(isProtectedDefaultTheme({ id: 'demo.theme', source: 'builtin' })).toBe(false)
  })
})

describe('extension pages wire the theme activation handler', () => {
  const read = (path: string) => Bun.file(new URL(path, import.meta.url)).text()

  /** 只取 useExtensionRowMenu({...}) 实参块：避免整文件字符串匹配掩盖漏传。 */
  function rowMenuOptions(source: string) {
    const start = source.indexOf('const rowMenu = useExtensionRowMenu({')
    expect(start).toBeGreaterThan(0)
    const end = source.indexOf('\n})', start)
    expect(end).toBeGreaterThan(start)
    return source.slice(start, end)
  }

  test('themes page passes activateTheme', async () => {
    const options = rowMenuOptions(await read('../../app/pages/admin/extensions/themes.vue'))
    expect(options).toContain('activate: activateTheme')
  })

  test('overview page passes activateTheme for its mixed plugin/theme rows', async () => {
    const source = await read('../../app/pages/admin/extensions/index.vue')
    expect(source).toContain('activateTheme,')
    expect(rowMenuOptions(source)).toContain('activate: activateTheme')
  })

  test('plugins page does not advertise a theme activation action', async () => {
    const options = rowMenuOptions(await read('../../app/pages/admin/extensions/plugins.vue'))
    expect(options).not.toContain('activate:')
  })
})
