import { describe, expect, test } from 'bun:test'
import { ref } from 'vue'
import { useAdminPageHostInstall } from '../../app/composables/admin/useAdminPageHostInstall'
import { toAdminHostInstallResult } from '../../app/composables/admin/useAdminExtensionInstall'
import {
  ADMIN_HOST_ACTION_EXTENSION_INSTALL_ENABLE,
  ADMIN_HOST_DIALOG_Z_INDEX,
  ADMIN_HOST_INSTALL_TIMEOUT_MS,
  AdminHostInstallError,
  adminExtensionInstallErrorMessage,
  adminPageCanInstallAndEnable,
  adminPageCanInstallExtensions,
  adminPageDeclaresHostAction,
  adminPageHostActions,
  adminPageSourceLabel,
  extensionCanRequestEnableInstall,
  extensionCanRequestInstall,
  validateAdminHostInstallRequest
} from '../../app/utils/admin/extensions/hostActions'
import type { AdminExtension, AdminExtensionInstallResult } from '../../app/utils/admin/adminExtensions'
import {
  hostActivationAction,
  hostActivationBody,
  hostStagedDescriptionKey
} from '../../app/utils/admin/extensions/hostActivation'
import type { ExecutableTrustChallenge, ExecutableTrustStatus } from '../../app/utils/extensions/extensionTrust'

const digest = 'a'.repeat(64)

function validRequest(overrides: Record<string, unknown> = {}) {
  return {
    url: 'https://cdn.example.com/sforum.store-1.4.0.zip',
    sha256: digest,
    expectedExtensionId: 'sforum.store',
    expectedType: 'plugin',
    ...overrides
  } as never
}

function installResult(overrides: Partial<AdminExtensionInstallResult['extension']> = {}): AdminExtensionInstallResult {
  return {
    upgraded: false,
    extension: {
      id: 'sforum.store',
      name: 'App Store',
      version: '1.4.0',
      type: 'plugin',
      status: 'installed',
      manifest: { backend: { entry: 'backend/plugin' } } as never,
      packageDigest: 'b'.repeat(64),
      packagePath: '/tmp/pkg',
      installedAt: '',
      updatedAt: '',
      ...overrides
    }
  } as AdminExtensionInstallResult
}

function impact() {
  return { digest: 'impact-1', capabilities: [] } as never
}

function trustStatus(overrides: Partial<ExecutableTrustStatus> = {}): ExecutableTrustStatus {
  return { impact: impact(), trustRequired: true, trusted: false, ...overrides }
}

/** 纯前端包：manifest 没有 backend.entry，装完可以直接启用。 */
function frontendOnlyResult(): AdminExtensionInstallResult {
  return installResult({ manifest: {} as never })
}

/** 已启用插件的新版本：启用编排必须走 upgrade/restart，而不是 enable。 */
function upgradedResult(status: 'enabled' | 'disabled' = 'enabled'): AdminExtensionInstallResult {
  const lifecycleManifest = {
    backend: { protocolVersion: 2 },
    lifecycle: { contractVersion: '1' }
  } as never
  const base = installResult({ status, manifest: lifecycleManifest })
  return {
    ...base,
    upgraded: true,
    extension: {
      ...base.extension,
      stagedVersion: {
        version: '1.5.0',
        packageDigest: 'c'.repeat(64),
        manifest: lifecycleManifest
      } as never
    }
  } as AdminExtensionInstallResult
}

function harness(options: {
  install?: (url: string, sha256: string, config?: unknown) => Promise<AdminExtensionInstallResult>
  can?: (permission: string) => boolean
  isSuperAdmin?: boolean
  trustStatus?: (item: AdminExtension, action: string) => Promise<ExecutableTrustStatus>
  trustChallenge?: (item: AdminExtension, action: string) => Promise<ExecutableTrustChallenge>
  activate?: (item: AdminExtension, action: string, body: Record<string, unknown>) => Promise<AdminExtension>
} = {}) {
  const calls: Array<{ url: string, sha256: string, config: unknown }> = []
  const toasts: Array<{ title: string, description?: string }> = []
  const navigations: string[] = []
  const trustCalls: Array<{ id: string, action: string }> = []
  const challengeCalls: Array<{ id: string, action: string }> = []
  const activateCalls: Array<{ id: string, action: string, body: Record<string, unknown> }> = []
  const hostInstall = useAdminPageHostInstall({
    extensionId: () => 'sforum.store',
    can: options.can ?? (() => true),
    isSuperAdmin: () => options.isSuperAdmin ?? true,
    install: (url, sha256, config) => {
      calls.push({ url, sha256, config })
      return options.install
        ? options.install(url, sha256, config)
        : Promise.resolve(installResult())
    },
    toResult: toAdminHostInstallResult,
    trustStatus: (item, action) => {
      trustCalls.push({ id: item.id, action })
      return options.trustStatus
        ? options.trustStatus(item, action)
        : Promise.resolve(trustStatus())
    },
    trustChallenge: (item, action) => {
      challengeCalls.push({ id: item.id, action })
      return options.trustChallenge
        ? options.trustChallenge(item, action)
        : Promise.resolve({ token: 'token-1', impact: impact(), expiresAt: '2026-10-26T09:00:00Z' })
    },
    activate: (item, action, body) => {
      activateCalls.push({ id: item.id, action, body })
      return options.activate ? options.activate(item, action, body) : Promise.resolve(item)
    },
    t: key => key,
    notify: input => toasts.push(input),
    navigate: path => { navigations.push(path) }
  })
  return { hostInstall, calls, toasts, navigations, trustCalls, challengeCalls, activateCalls }
}

describe('host install request validation', () => {
  test('normalizes a declared identity before the request leaves the page', () => {
    const validation = validateAdminHostInstallRequest({
      url: ' https://cdn.example.com/pkg.zip ',
      sha256: digest.toUpperCase(),
      expectedExtensionId: ' SForum.Store ',
      expectedType: 'Plugin',
      expectedVersion: '1.4.0'
    })
    expect(validation.ok).toBeTrue()
    if (!validation.ok) return
    expect(validation.request.expectedExtensionId).toBe('sforum.store')
    expect(validation.request.expectedType).toBe('plugin')
    expect(validation.request.sha256).toBe(digest)
    expect(validation.request.expectedVersion).toBe('1.4.0')
  })

  test('rejects every field that would make the install unbindable', () => {
    const cases: Array<[Record<string, unknown>, string]> = [
      [{ url: 'http://cdn.example.com/pkg.zip' }, 'url'],
      [{ url: 'https://user:pw@cdn.example.com/pkg.zip' }, 'url'],
      [{ sha256: '' }, 'sha256'],
      [{ sha256: 'not-a-digest' }, 'sha256'],
      [{ expectedExtensionId: 'Not An ID' }, 'extensionId'],
      [{ expectedType: 'widget' }, 'type'],
      [{ expectedVersion: '1.0.0/../x' }, 'version']
    ]
    for (const [overrides, reason] of cases) {
      const validation = validateAdminHostInstallRequest(validRequest(overrides))
      expect(validation.ok).toBeFalse()
      expect(validation.ok === false && validation.reason).toBe(reason)
    }
  })
})

describe('host action declaration', () => {
  test('only a component page can carry actions', () => {
    const componentPage = { view: 'component' as const, hostActions: [' Extension.Install ', 'extension.install'] }
    expect(adminPageHostActions(componentPage)).toEqual(['extension.install'])
    expect(adminPageCanInstallExtensions(componentPage)).toBeTrue()
    expect(adminPageDeclaresHostAction({ view: 'about', hostActions: ['extension.install'] }, 'extension.install')).toBeFalse()
    expect(adminPageCanInstallExtensions({ view: 'component', hostActions: ['other.action'] })).toBeFalse()
    expect(adminPageCanInstallExtensions(null)).toBeFalse()
  })

  test('install-and-enable is a separate declaration from install-only', () => {
    const enablePage = { view: 'component' as const, hostActions: [ADMIN_HOST_ACTION_EXTENSION_INSTALL_ENABLE] }
    expect(adminPageCanInstallAndEnable(enablePage)).toBeTrue()
    // 声明更强动作不等于声明旧动作：每个方法各按自己的动作注入。
    expect(adminPageCanInstallExtensions(enablePage)).toBeFalse()
    expect(adminPageCanInstallAndEnable({ view: 'component', hostActions: ['extension.install'] })).toBeFalse()
    expect(adminPageCanInstallAndEnable({ view: 'about', hostActions: [ADMIN_HOST_ACTION_EXTENSION_INSTALL_ENABLE] })).toBeFalse()
    expect(adminPageCanInstallAndEnable(null)).toBeFalse()
  })

  test('the extension-level predicates read the same manifest pages the bridge sees', () => {
    const declaring = {
      id: 'sforum.store',
      manifest: {
        admin: {
          pages: [{
            path: '/catalog',
            label: 'Catalog',
            view: 'component',
            component: { id: 'catalog', apiVersion: 1, entry: 'frontend/admin/dist/catalog.mjs' },
            hostActions: ['extension.install']
          }]
        }
      }
    } as never
    const enabling = {
      id: 'sforum.store',
      manifest: {
        admin: {
          pages: [{
            path: '/catalog',
            label: 'Catalog',
            view: 'component',
            component: { id: 'catalog', apiVersion: 1, entry: 'frontend/admin/dist/catalog.mjs' },
            hostActions: ['extension.install.enable']
          }]
        }
      }
    } as never
    const plain = {
      id: 'plain.plugin',
      manifest: {
        admin: {
          pages: [{ path: '/about', label: 'About', view: 'component', component: { id: 'x', apiVersion: 1, entry: 'frontend/admin/dist/x.mjs' } }]
        }
      }
    } as never
    expect(extensionCanRequestInstall(declaring)).toBeTrue()
    expect(extensionCanRequestEnableInstall(declaring)).toBeFalse()
    expect(extensionCanRequestEnableInstall(enabling)).toBeTrue()
    expect(extensionCanRequestEnableInstall(plain)).toBeFalse()
    expect(extensionCanRequestEnableInstall(null)).toBeFalse()
    expect(extensionCanRequestInstall(plain)).toBeFalse()
    expect(extensionCanRequestInstall(null)).toBeFalse()
  })

  test('audit label is derived from the owning extension and bounded', () => {
    expect(adminPageSourceLabel('SForum.Store')).toBe('admin-page:sforum.store')
    expect(adminPageSourceLabel('a'.repeat(120)).length).toBe(64)
    expect(adminPageSourceLabel('a'.repeat(120)).endsWith(':')).toBeFalse()
  })
})

describe('shared install error mapping', () => {
  test('one backend reason maps to one message for both surfaces', () => {
    const error = { code: 422, message: 'extension.expected_mismatch', data: { reason: 'extension.expected_mismatch' } }
    const bridged = adminExtensionInstallErrorMessage(key => key, error, 'admin.extensionInstall.hostAction.failedTitle')
    const paneled = adminExtensionInstallErrorMessage(key => key, error, 'admin.extensionInstall.url.failed')
    expect(bridged).toBe('admin.extensionInstall.errors.expectedMismatch')
    expect(paneled).toBe(bridged)
  })

  test('unknown reasons fall back per surface instead of leaking transport text', () => {
    const error = { code: 422, message: 'internal_error', data: { reason: 'internal_error' } }
    expect(adminExtensionInstallErrorMessage(key => key, error, 'fallback.key')).toBe('fallback.key')
    expect(adminExtensionInstallErrorMessage(key => key, new Error('boom'), 'fallback.key')).toBe('fallback.key')
  })

  test('platform gate interpolates the required and host platforms', () => {
    const calls: Array<{ key: string, params?: Record<string, unknown> }> = []
    const translate = (key: string, params?: Record<string, unknown>) => {
      calls.push({ key, params })
      return key
    }
    const error = {
      code: 422,
      message: 'extension.platform_incompatible',
      data: {
        reason: 'extension.platform_incompatible',
        fields: {
          required: ['darwin/arm64'],
          host: ['linux/amd64'],
          entry: ['backend/plugin']
        }
      }
    }

    expect(adminExtensionInstallErrorMessage(translate, error, 'admin.extensionInstall.url.failed'))
      .toBe('admin.extensionInstall.errors.platformIncompatible')
    expect(calls).toEqual([{
      key: 'admin.extensionInstall.errors.platformIncompatible',
      params: { required: 'darwin/arm64', host: 'linux/amd64' }
    }])
  })

  test('platform gate without diagnostics never prints empty placeholders', () => {
    const translate = (key: string, params?: Record<string, unknown>) =>
      params ? `${key}|${Object.keys(params).join(',')}` : key
    const base = { code: 422, message: 'extension.platform_incompatible', data: { reason: 'extension.platform_incompatible' } }
    const generic = 'admin.extensionInstall.errors.platformIncompatibleGeneric'

    expect(adminExtensionInstallErrorMessage(translate, base, 'fallback.key')).toBe(generic)
    expect(adminExtensionInstallErrorMessage(translate, {
      ...base,
      data: { ...base.data, fields: { required: ['darwin/arm64'] } }
    }, 'fallback.key')).toBe(generic)
    expect(adminExtensionInstallErrorMessage(translate, {
      ...base,
      data: { ...base.data, fields: { required: [], host: [] } }
    }, 'fallback.key')).toBe(generic)
  })
})

describe('bridge install state machine', () => {
  test('confirm performs one 120 s install carrying the declared identity', async () => {
    const { hostInstall, calls, toasts } = harness()
    const pending = hostInstall.start(validRequest())
    expect(hostInstall.state.value).toBe('confirming')
    expect(hostInstall.dialogOpen.value).toBeTrue()
    await hostInstall.confirm()
    const installed = await pending

    expect(calls).toHaveLength(1)
    expect(calls[0]?.config).toEqual({
      expectations: {
        expectedExtensionId: 'sforum.store',
        expectedType: 'plugin',
        source: 'admin-page:sforum.store'
      },
      timeout: 120000
    })
    expect(ADMIN_HOST_INSTALL_TIMEOUT_MS).toBe(120000)
    expect(installed.extensionId).toBe('sforum.store')
    expect(installed.requiresTrust).toBeTrue()
    expect(hostInstall.state.value).toBe('success')
    expect(toasts).toHaveLength(1)
  })

  test('cancel sends nothing and rejects with install_cancelled', async () => {
    const { hostInstall, calls } = harness()
    const pending = hostInstall.start(validRequest())
    hostInstall.close()
    await expect(pending).rejects.toMatchObject({ code: 'install_cancelled' })
    expect(calls).toHaveLength(0)
    expect(hostInstall.state.value).toBe('idle')
    expect(hostInstall.dialogOpen.value).toBeFalse()
  })

  test('single flight rejects a second install while one is confirming', async () => {
    const { hostInstall } = harness()
    void hostInstall.start(validRequest())
    await expect(hostInstall.start(validRequest({ expectedExtensionId: 'other.plugin' })))
      .rejects.toMatchObject({ code: 'install_in_progress' })
  })

  test('permission pre-check rejects before any dialog', async () => {
    const { hostInstall } = harness({ can: permission => permission !== 'extension.plugin.manage' })
    await expect(hostInstall.start(validRequest())).rejects.toMatchObject({ code: 'install_permission_denied' })
    expect(hostInstall.dialogOpen.value).toBeFalse()
    expect(hostInstall.state.value).toBe('idle')
  })

  test('an invalid request is rejected before the network', async () => {
    const { hostInstall, calls } = harness()
    await expect(hostInstall.start(validRequest({ expectedType: 'widget' })))
      .rejects.toMatchObject({ code: 'install_invalid_request' })
    expect(calls).toHaveLength(0)
  })

  test('a failure keeps the dialog open for retry, and close settles it', async () => {
    let attempt = 0
    const { hostInstall, calls } = harness({
      install: async () => {
        attempt += 1
        if (attempt === 1) throw { code: 422, message: 'extension.expected_mismatch', data: { reason: 'extension.expected_mismatch' } }
        return installResult()
      }
    })
    const pending = hostInstall.start(validRequest())
    await hostInstall.confirm()

    expect(hostInstall.state.value).toBe('error')
    expect(hostInstall.errorMessage.value).toBe('admin.extensionInstall.errors.expectedMismatch')
    hostInstall.retry()
    expect(hostInstall.state.value).toBe('confirming')
    await hostInstall.confirm()
    await expect(pending).resolves.toMatchObject({ extensionId: 'sforum.store' })
    expect(calls).toHaveLength(2)
  })

  test('closing after a failure rejects with install_failed', async () => {
    const { hostInstall } = harness({ install: async () => { throw new Error('boom') } })
    const pending = hostInstall.start(validRequest())
    await hostInstall.confirm()
    hostInstall.close()
    await expect(pending).rejects.toMatchObject({ code: 'install_failed' })
  })

  test('teardown aborts a pending install', async () => {
    const { hostInstall } = harness()
    const pending = hostInstall.start(validRequest())
    hostInstall.abortOnUnmount()
    await expect(pending).rejects.toMatchObject({ code: 'install_cancelled' })
  })

  test('success navigates to the matching management page', async () => {
    const { hostInstall, navigations } = harness()
    void hostInstall.start(validRequest())
    await hostInstall.confirm()
    await hostInstall.openManagement()
    expect(navigations).toEqual(['/extensions/plugins'])
    expect(hostInstall.state.value).toBe('idle')
  })

  test('errors are typed so plugins can branch on code', () => {
    const error = new AdminHostInstallError('install_permission_denied')
    expect(error).toBeInstanceOf(Error)
    expect(error.name).toBe('AdminHostInstallError')
    expect(error.code).toBe('install_permission_denied')
  })
})

describe('install result normalization', () => {
  test('prefers the staged candidate identity', () => {
    const result = toAdminHostInstallResult({
      ...installResult(),
      upgraded: true,
      extension: {
        ...installResult().extension,
        stagedVersion: {
          version: '1.5.0',
          packageDigest: 'c'.repeat(64),
          manifest: { backend: { entry: 'backend/plugin' } }
        } as never
      }
    })
    expect(result.version).toBe('1.5.0')
    expect(result.packageDigest).toBe('c'.repeat(64))
    expect(result.upgraded).toBeTrue()
    expect(result.requiresTrust).toBeTrue()
  })

  test('a front-end-only package does not claim a trust requirement', () => {
    const result = toAdminHostInstallResult(installResult({ manifest: {} as never }))
    expect(result.requiresTrust).toBeFalse()
  })
})

describe('dialog stacking contract', () => {
  test('the constant sits strictly between plugin overlays and toasts', () => {
    expect(ADMIN_HOST_DIALOG_Z_INDEX).toBeGreaterThan(40)
    expect(ADMIN_HOST_DIALOG_Z_INDEX).toBeLessThan(100)
  })
})

describe('install and enable orchestration', () => {
  test('install-only mode still stages and never touches activation', async () => {
    const { hostInstall, activateCalls, trustCalls } = harness({ install: async () => frontendOnlyResult() })
    const pending = hostInstall.start(validRequest())
    await hostInstall.confirm()
    expect(activateCalls).toHaveLength(0)
    expect(trustCalls).toHaveLength(0)
    expect(hostInstall.stagedReason.value).toBe('stage')
    const settled = await pending
    expect(settled).toMatchObject({ extensionId: 'sforum.store' })
    // 旧 bridge 的返回形状不变：没有 outcome 字段。
    expect('outcome' in settled).toBeFalse()
  })

  test('a front-end-only package is enabled without any trust round trip', async () => {
    const { hostInstall, calls, activateCalls, trustCalls, challengeCalls, toasts } = harness({
      install: async () => frontendOnlyResult()
    })
    const pending = hostInstall.startEnable(validRequest())
    expect(hostInstall.mode.value).toBe('enable')
    expect(hostInstall.state.value).toBe('confirming')
    await hostInstall.confirm()
    const settled = await pending

    expect(calls).toHaveLength(1)
    expect(trustCalls).toHaveLength(0)
    expect(challengeCalls).toHaveLength(0)
    expect(activateCalls).toEqual([{ id: 'sforum.store', action: 'enable', body: {} }])
    expect(settled).toMatchObject({ extensionId: 'sforum.store', outcome: 'enabled' })
    expect(hostInstall.state.value).toBe('success')
    expect(hostInstall.outcome.value).toBe('enabled')
    expect(toasts).toHaveLength(1)
    expect(toasts[0]?.title).toBe('admin.extensions.enabled')
  })

  test('a package with an executable backend waits for the impact review', async () => {
    const { hostInstall, activateCalls, trustCalls, challengeCalls } = harness()
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()

    expect(hostInstall.state.value).toBe('trust-review')
    expect(trustCalls).toEqual([{ id: 'sforum.store', action: 'enable' }])
    expect(activateCalls).toHaveLength(0)
    expect(hostInstall.trustStatus.value?.trustRequired).toBeTrue()

    await hostInstall.confirmTrust()
    expect(challengeCalls).toEqual([{ id: 'sforum.store', action: 'enable' }])
    expect(activateCalls).toEqual([{ id: 'sforum.store', action: 'enable', body: { confirmationToken: 'token-1' } }])
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
    // 一次性 token 用完即弃。
    expect(hostInstall.trustChallenge.value).toBeNull()
  })

  test('an already trusted artifact enables without a second review', async () => {
    const { hostInstall, activateCalls, challengeCalls } = harness({
      trustStatus: async () => trustStatus({ trusted: true })
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(challengeCalls).toHaveLength(0)
    expect(activateCalls).toEqual([{ id: 'sforum.store', action: 'enable', body: {} }])
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
  })

  test('a backend that still demands a challenge falls back to the review step', async () => {
    let attempts = 0
    const { hostInstall, activateCalls } = harness({
      install: async () => frontendOnlyResult(),
      activate: async (item) => {
        attempts += 1
        if (attempts === 1) {
          throw {
            code: 403,
            message: 'extension.trust_challenge_required',
            data: { reason: 'extension.trust_challenge_required' }
          }
        }
        return item
      }
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(hostInstall.state.value).toBe('trust-review')
    await hostInstall.confirmTrust()
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
    expect(activateCalls).toHaveLength(2)
  })

  test('an untrusted artifact plus a non-super-admin falls back to staged', async () => {
    const { hostInstall, activateCalls, trustCalls, challengeCalls } = harness({ isSuperAdmin: false })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    // 角色不能替代信任状态：先读状态，确认未信任之后才按角色回落。
    expect(trustCalls).toEqual([{ id: 'sforum.store', action: 'enable' }])
    expect(challengeCalls).toHaveLength(0)
    expect(activateCalls).toHaveLength(0)
    expect(hostInstall.state.value).toBe('success')
    expect(hostInstall.stagedReason.value).toBe('super_admin')
    await expect(pending).resolves.toMatchObject({ outcome: 'staged' })
  })

  test('an already trusted artifact is enabled for a non-super-admin', async () => {
    const { hostInstall, activateCalls, challengeCalls, trustCalls } = harness({
      isSuperAdmin: false,
      trustStatus: async () => trustStatus({ trusted: true })
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    // 已有 live grant：服务端直接放行，不该拿角色去挡。
    expect(trustCalls).toHaveLength(1)
    expect(challengeCalls).toHaveLength(0)
    expect(activateCalls).toEqual([{ id: 'sforum.store', action: 'enable', body: {} }])
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
  })

  test('a trusted artifact with no trust requirement at all also enables for a non-super-admin', async () => {
    const { hostInstall, activateCalls } = harness({
      isSuperAdmin: false,
      trustStatus: async () => trustStatus({ trustRequired: false, trusted: false })
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(activateCalls).toHaveLength(1)
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
  })

  test('a theme stops after install because activation is an operator decision', async () => {
    const { hostInstall, activateCalls } = harness({
      install: async () => installResult({ type: 'theme', manifest: {} as never })
    })
    const pending = hostInstall.startEnable(validRequest({ expectedType: 'theme' }))
    await hostInstall.confirm()
    expect(activateCalls).toHaveLength(0)
    expect(hostInstall.stagedReason.value).toBe('theme')
    await expect(pending).resolves.toMatchObject({ outcome: 'staged' })
  })

  test('declining the review settles as staged instead of failing the promise', async () => {
    const { hostInstall, activateCalls } = harness()
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(hostInstall.state.value).toBe('trust-review')
    hostInstall.close()
    await expect(pending).resolves.toMatchObject({ outcome: 'staged' })
    expect(hostInstall.dialogOpen.value).toBeFalse()
    expect(activateCalls).toHaveLength(0)
  })

  test('an upgraded plugin reuses the shared upgrade lifecycle action', async () => {
    const { hostInstall, activateCalls } = harness({ install: async () => upgradedResult('enabled') })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(activateCalls).toEqual([{ id: 'sforum.store', action: 'upgrade', body: {} }])
    await expect(pending).resolves.toMatchObject({ outcome: 'upgraded' })
  })

  test('an upgrade that cannot use lifecycle v2 restarts instead', async () => {
    const { hostInstall, activateCalls } = harness({ install: async () => upgradedResult('disabled') })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(activateCalls[0]?.action).toBe('restart')
    await expect(pending).resolves.toMatchObject({ outcome: 'upgraded' })
  })

  test('closing after an enable failure rejects with install_failed + phase activate', async () => {
    const { hostInstall } = harness({
      install: async () => frontendOnlyResult(),
      activate: async () => {
        throw { code: 500, message: 'extension.lifecycle_failed', data: { reason: 'extension.lifecycle_failed' } }
      }
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    expect(hostInstall.state.value).toBe('error')
    hostInstall.close()
    // 包已经在盘上：调用方必须能区分「没装成」与「装好但没启用」。
    await expect(pending).rejects.toMatchObject({ code: 'install_failed', phase: 'activate' })
  })

  test('closing after an install failure rejects with phase install', async () => {
    const { hostInstall } = harness({ install: async () => { throw new Error('boom') } })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()
    hostInstall.close()
    await expect(pending).rejects.toMatchObject({ code: 'install_failed', phase: 'install' })
  })

  test('a cancel carries no phase', async () => {
    const { hostInstall } = harness()
    const pending = hostInstall.startEnable(validRequest())
    hostInstall.close()
    await expect(pending).rejects.toMatchObject({ code: 'install_cancelled' })
    await expect(pending).rejects.toHaveProperty('phase', undefined)
  })

  test('a failed enable keeps the package and retry only repeats the enable', async () => {
    let attempts = 0
    const { hostInstall, calls, activateCalls } = harness({
      install: async () => frontendOnlyResult(),
      activate: async (item) => {
        attempts += 1
        if (attempts === 1) {
          throw { code: 500, message: 'extension.lifecycle_failed', data: { reason: 'extension.lifecycle_failed' } }
        }
        return item
      }
    })
    const pending = hostInstall.startEnable(validRequest())
    await hostInstall.confirm()

    expect(hostInstall.state.value).toBe('error')
    expect(hostInstall.failurePhase.value).toBe('activate')
    expect(hostInstall.errorMessage.value).toBe('extension.lifecycle_failed')

    hostInstall.retry()
    await expect(pending).resolves.toMatchObject({ outcome: 'enabled' })
    expect(calls).toHaveLength(1)
    expect(activateCalls).toHaveLength(2)
  })

  test('elapsed seconds tick while installing and stop when it settles', async () => {
    const { hostInstall } = harness({
      install: () => new Promise(resolve => setTimeout(() => resolve(frontendOnlyResult()), 1150))
    })
    const pending = hostInstall.startEnable(validRequest())
    void hostInstall.confirm()
    await new Promise(resolve => setTimeout(resolve, 1100))
    expect(hostInstall.elapsedSeconds.value).toBeGreaterThanOrEqual(1)
    await pending
    const settledAt = hostInstall.elapsedSeconds.value
    await new Promise(resolve => setTimeout(resolve, 1100))
    expect(hostInstall.elapsedSeconds.value).toBe(settledAt)
  })

  test('teardown aborts an install-and-enable too', async () => {
    const { hostInstall } = harness()
    const pending = hostInstall.startEnable(validRequest())
    hostInstall.abortOnUnmount()
    await expect(pending).rejects.toMatchObject({ code: 'install_cancelled' })
  })
})

describe('host activation decisions', () => {
  const lifecycleV2 = {
    backend: { protocolVersion: 2 },
    lifecycle: { contractVersion: '1' }
  } as never

  function extension(overrides: Partial<AdminExtension> = {}): AdminExtension {
    return { ...installResult().extension, ...overrides }
  }

  test('a new install enables, a staged replacement reuses the upgrade path', () => {
    expect(hostActivationAction(extension(), false)).toBe('enable')
    const stagedV2 = extension({
      status: 'enabled',
      manifest: lifecycleV2,
      stagedVersion: { version: '1.5.0', manifest: lifecycleV2, packageDigest: 'c'.repeat(64) } as never
    })
    expect(hostActivationAction(stagedV2, true)).toBe('upgrade')
    // 不能用 lifecycle v2 的插件走 restart，与扩展管理页的判断一致。
    expect(hostActivationAction(extension({ status: 'enabled' }), true)).toBe('restart')
  })

  test('a one-use token replaces the legacy capability confirmation', () => {
    const granted = extension({
      capabilityGrants: [{ key: 'forum.write', risk: 'low', labelZh: '', labelEn: '', description: '' }]
    })
    expect(hostActivationBody(granted)).toEqual({ confirmCapabilities: true })
    expect(hostActivationBody(extension())).toEqual({})
    expect(hostActivationBody(granted, ' token-9 ')).toEqual({ confirmationToken: 'token-9' })
  })

  test('every staged reason has its own explanation', () => {
    const keys = (['stage', 'theme', 'super_admin', 'declined'] as const).map(hostStagedDescriptionKey)
    expect(new Set(keys).size).toBe(4)
    for (const key of keys) expect(key.startsWith('admin.extensionInstall.hostAction.')).toBeTrue()
  })
})

// ref() 仅用于让本文件在任何测试顺序下都先加载 Vue；行为断言不依赖它。
void ref
