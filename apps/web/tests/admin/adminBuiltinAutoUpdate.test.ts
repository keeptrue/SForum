import { afterEach, describe, expect, test } from 'bun:test'

import { useAdminBuiltinAutoUpdate } from '../../app/composables/admin/useAdminBuiltinAutoUpdate'
import {
  BUILTIN_AUTO_UPDATE_OPTION,
  builtinAutoUpdateStateLabelKey,
  isBuiltinAutoUpdateEnabledValue,
  isBuiltinMaintainedExtension,
  shouldShowBuiltinAutoUpdateDisabled
} from '../../app/utils/admin/extensions/builtinAutoUpdate'

const pluginsPage = await Bun.file(
  new URL('../../app/pages/admin/extensions/plugins.vue', import.meta.url)
).text()
const zhLocale = JSON.parse(await Bun.file(new URL('../../i18n/locales/zh-CN.json', import.meta.url)).text())
const enLocale = JSON.parse(await Bun.file(new URL('../../i18n/locales/en-US.json', import.meta.url)).text())

type OptionRecord = {
  name: string
  value: string
  public: boolean
  secret: boolean
  secretSet: boolean
}

type SavedOptions = Array<Array<{ name: string, value: string }>>

function adminOption(name: string, value: string): OptionRecord {
  return { name, value, public: false, secret: false, secretSet: false }
}

function installWebOptions(options: {
  envelope?: OptionRecord[] | Error
  saveMany?: (items: Array<{ name: string, value: string }>) => Promise<OptionRecord[]>
}) {
  const saved: SavedOptions = []
  const state = {
    fetchAdminEnvelope: async () => {
      if (options.envelope instanceof Error) {
        throw options.envelope
      }
      return { code: 0, message: 'ok', data: options.envelope || [] }
    },
    saveMany: async (items: Array<{ name: string, value: string }>) => {
      saved.push(items)
      if (options.saveMany) {
        return options.saveMany(items)
      }
      return items.map(item => adminOption(item.name, item.value))
    }
  }

  ;(globalThis as typeof globalThis & { useWebOptions: () => unknown }).useWebOptions = () => state
  return { saved }
}

afterEach(() => {
  delete (globalThis as typeof globalThis & { useWebOptions?: unknown }).useWebOptions
})

describe('builtin auto-update option parsing', () => {
  test('accepts only the documented enabled values', () => {
    for (const value of ['enabled', 'true', '1', 'yes', 'on', ' ENABLED ', 'Yes', 'ON']) {
      expect(isBuiltinAutoUpdateEnabledValue(value)).toBe(true)
    }

    for (const value of ['disabled', 'false', '0', 'no', 'off', '', 'unexpected', null, undefined]) {
      expect(isBuiltinAutoUpdateEnabledValue(value)).toBe(false)
    }
  })

  test('maps switch state to the existing locale key style', () => {
    expect(builtinAutoUpdateStateLabelKey(true)).toBe('admin.extensions.plugins.autoUpdate.stateOn')
    expect(builtinAutoUpdateStateLabelKey(false)).toBe('admin.extensions.plugins.autoUpdate.stateOff')
  })
})

describe('builtin auto-update row hints', () => {
  const builtin = { source: 'builtin', stagedVersion: { version: '2.0.0' } }
  const uploaded = { source: 'uploaded', stagedVersion: { version: '2.0.0' } }

  test('marks only builtin rows as maintained by SForum', () => {
    expect(isBuiltinMaintainedExtension(builtin)).toBe(true)
    expect(isBuiltinMaintainedExtension(uploaded)).toBe(false)
  })

  test('shows the disabled hint only when the supported switch is off', () => {
    expect(shouldShowBuiltinAutoUpdateDisabled(builtin, { supported: true, enabled: false })).toBe(true)
    expect(shouldShowBuiltinAutoUpdateDisabled(builtin, { supported: true, enabled: true })).toBe(false)
    expect(shouldShowBuiltinAutoUpdateDisabled(builtin, { supported: false, enabled: false })).toBe(false)
    expect(shouldShowBuiltinAutoUpdateDisabled(uploaded, { supported: true, enabled: false })).toBe(false)
  })

})

describe('useAdminBuiltinAutoUpdate', () => {
  test('loads the admin option and parses truthy values as enabled', async () => {
    const mock = installWebOptions({ envelope: [adminOption(BUILTIN_AUTO_UPDATE_OPTION, 'on')] })
    const state = useAdminBuiltinAutoUpdate()

    expect(state.supported.value).toBe(false)
    expect(await state.load()).toBe(true)
    expect(state.supported.value).toBe(true)
    expect(state.enabled.value).toBe(true)
    expect(mock.saved).toEqual([])
  })

  test('reports the option as unsupported when it is absent', async () => {
    installWebOptions({ envelope: [adminOption('site.name', 'SForum')] })
    const state = useAdminBuiltinAutoUpdate()

    expect(await state.load()).toBe(true)
    expect(state.supported.value).toBe(false)
    expect(state.enabled.value).toBe(false)
  })

  test('saves the canonical payload and mirrors the returned option', async () => {
    const mock = installWebOptions({ envelope: [adminOption(BUILTIN_AUTO_UPDATE_OPTION, 'enabled')] })
    const state = useAdminBuiltinAutoUpdate()
    await state.load()

    expect(await state.save(false)).toBe(true)
    expect(mock.saved).toEqual([[
      { name: BUILTIN_AUTO_UPDATE_OPTION, value: 'disabled' }
    ]])
    expect(state.enabled.value).toBe(false)
    expect(state.saving.value).toBe(false)
  })

  test('restoreRecommended writes enabled', async () => {
    const mock = installWebOptions({ envelope: [adminOption(BUILTIN_AUTO_UPDATE_OPTION, 'disabled')] })
    const state = useAdminBuiltinAutoUpdate()
    await state.load()

    expect(await state.restoreRecommended()).toBe(true)
    expect(mock.saved.at(-1)).toEqual([
      { name: BUILTIN_AUTO_UPDATE_OPTION, value: 'enabled' }
    ])
    expect(state.enabled.value).toBe(true)
  })

  test('keeps the previous state when saving fails', async () => {
    installWebOptions({
      envelope: [adminOption(BUILTIN_AUTO_UPDATE_OPTION, 'enabled')],
      saveMany: async () => {
        throw new Error('save failed')
      }
    })
    const state = useAdminBuiltinAutoUpdate()
    await state.load()

    expect(await state.save(false)).toBe(false)
    expect(state.enabled.value).toBe(true)
    expect(state.saving.value).toBe(false)
  })

  test('keeps the switch hidden when loading fails', async () => {
    installWebOptions({ envelope: new Error('load failed') })
    const state = useAdminBuiltinAutoUpdate()

    expect(await state.load()).toBe(false)
    expect(state.supported.value).toBe(false)
    expect(state.enabled.value).toBe(false)
  })
})

describe('plugins page auto-update wiring', () => {
  test('gates the compact switch on plugin management permission and keeps accessible text', () => {
    expect(pluginsPage).toContain("can('extension.plugin.manage')")
    expect(pluginsPage).toContain('v-if="showBuiltinAutoUpdate"')
    expect(pluginsPage).toContain(':aria-label="builtinAutoUpdateAriaLabel"')
    expect(pluginsPage).toContain(':title="builtinAutoUpdateHelp"')
    expect(pluginsPage).toContain("t('admin.extensions.plugins.autoUpdate.restoreRecommended')")
  })

  test('keeps success toasts dismissible and error toasts persistent', () => {
    expect(pluginsPage).toContain('duration: 5000')
    expect(pluginsPage).toContain('duration: 0')
  })

  test('keeps the builtin row hints but leaves the staged badge to the core update state', () => {
    expect(pluginsPage).toContain('isBuiltinMaintainedExtension(item)')
    expect(pluginsPage).toContain('shouldShowBuiltinAutoUpdateDisabled(item, builtinAutoUpdateState)')
    // 同版本不同摘要现在统一显示「重启后生效」；页面不再自己拼 staged 徽章。
    expect(pluginsPage).not.toContain('stagedVersionHint(item)')
    expect(pluginsPage).not.toContain("t('admin.extensions.plugins.autoUpdate.incompleteHint')")
    expect(pluginsPage).toContain('<SFAdminExtensionIdentity')
  })

  test('ships every auto-update key in both locales', () => {
    const keys = [
      'label',
      'help',
      'stateOn',
      'stateOff',
      'saveSuccess',
      'saveFailed',
      'restoreRecommended',
      'restoreSuccess',
      'builtinMaintained',
      'builtinMaintainedHint',
      'disabledBadge',
      'disabledHint',
      'loadFailed'
    ]

    for (const key of keys) {
      expect(zhLocale.admin.extensions.plugins.autoUpdate[key]).toBeString()
      expect(enLocale.admin.extensions.plugins.autoUpdate[key]).toBeString()
    }
  })
})
