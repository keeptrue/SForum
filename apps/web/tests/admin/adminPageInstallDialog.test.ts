import { beforeAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { installTestDom } from '../helpers/dom'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'

installTestDom()

const dialogPath = 'app/components/extensions/admin/SFAdminPageInstallDialog.vue'
const bridgePath = 'app/components/extensions/admin/SFTrustedAdminPageComponent.vue'
const noticePath = 'app/components/admin/SFAdminExtensionHostActionsNotice.vue'

async function source(relative: string) {
  return Bun.file(new URL(`../../${relative}`, import.meta.url)).text()
}

const dialog = await source(dialogPath)
const bridge = await source(bridgePath)
const notice = await source(noticePath)

function collectI18nKeys(value: string) {
  const keys = new Set<string>()
  for (const match of value.matchAll(/t\('([^']+)'/g)) keys.add(match[1] as string)
  for (const match of value.matchAll(/'((?:admin|validation)\.[a-zA-Z0-9_.]+)'/g)) keys.add(match[1] as string)
  return [...keys]
}

function lookup(locale: Record<string, unknown>, key: string) {
  return key.split('.').reduce<unknown>((current, segment) => {
    if (!current || typeof current !== 'object') return undefined
    return (current as Record<string, unknown>)[segment]
  }, locale)
}

describe('admin page install dialog', () => {
  test('compiles and keeps its template free of structural errors', () => {
    const { descriptor } = parse(dialog, { filename: 'SFAdminPageInstallDialog.vue' })
    const compiled = compileTemplate({
      source: descriptor.template?.content || '',
      filename: 'SFAdminPageInstallDialog.vue',
      id: 'admin-page-install-dialog'
    })
    expect(compiled.errors).toEqual([])
  })

  test('shows the URL host, the full URL, the digest and the declared identity', () => {
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.subtitle', { host: urlHost })")
    expect(dialog).toContain('{{ request.url }}')
    expect(dialog).toContain('{{ request.sha256 }}')
    expect(dialog).toContain('{{ request.expectedExtensionId }}')
    expect(dialog).toContain('{{ request.expectedVersion ||')
    expect(dialog).toContain('{{ t(typeLabelKey) }}')
    expect(dialog).toContain('{{ permission }}')
  })

  test('warns about super_admin, the post-download verification and staging only', () => {
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.superAdminTitle')")
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.verifyBody')")
    expect(dialog).toContain('admin.extensionInstall.hostAction.stagedOnlyBody')
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.installingHint')")
  })

  test('the template literal matches the declared stacking constant', async () => {
    const { ADMIN_HOST_DIALOG_Z_INDEX } = await import('../../app/utils/admin/extensions/hostActions')
    // Tailwind 没有 safelist，模板里必须是字面量；这条把字面量与常量绑在一起。
    expect(dialog).toContain(`z-[${ADMIN_HOST_DIALOG_Z_INDEX}]`)
    expect(ADMIN_HOST_DIALOG_Z_INDEX).toBeGreaterThan(40)
    expect(ADMIN_HOST_DIALOG_Z_INDEX).toBeLessThan(100)
  })

  test('fixes the deadlock: the host modal sits above the plugin overlay and below toasts', () => {
    // 商店遮罩是 z-index:40，UModal 默认没有任何 z-index，弹窗会被盖住；
    // 而 reka 在弹窗打开时给 body 加了 pointer-events:none —— 于是整页点不动。
    expect(dialog).toContain("overlay: 'z-[80]'")
    expect(dialog).toContain("'z-[80] sm:max-w-lg'")
    expect(dialog).toContain("'z-[80] sm:max-w-4xl'")
    expect(dialog).toContain(':ui="modalUi"')
    expect(dialog).not.toContain('z-[100]')
    expect(dialog).toContain(':dismissible="false"')
  })

  test('spins the installing icon and reports elapsed seconds against the 120 s budget', () => {
    expect(dialog).toContain(":ui=\"{ icon: 'animate-spin motion-reduce:animate-none' }\"")
    expect(dialog).toContain('ADMIN_HOST_INSTALL_TIMEOUT_MS')
    expect(dialog).toContain('installLimitSeconds')
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.elapsed', { seconds: props.elapsedSeconds, limit: installLimitSeconds })")
  })

  test('renders the trust review step through the shared impact component', () => {
    expect(dialog).toContain("import SFAdminExecutableTrustImpact from '~/components/admin/SFAdminExecutableTrustImpact.vue'")
    expect(dialog).toContain('<SFAdminExecutableTrustImpact')
    expect(dialog).toContain(':impact="trustStatus.impact"')
    expect(dialog).toContain(':guard-authorization="trustStatus.guardAuthorization"')
    // 不在宿主弹窗里另写一份权限披露。
    expect(dialog).not.toContain('requestedAuthority')
    expect(dialog).not.toContain('packageFiles')
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.trustReviewConfirm')")
  })

  test('separates enabled, upgraded and staged results', () => {
    expect(dialog).toContain("props.outcome === 'enabled'")
    expect(dialog).toContain("props.outcome === 'upgraded'")
    for (const key of ['stagedThemeTitle', 'stagedSuperAdminTitle', 'stagedDeclinedTitle', 'stagedOnlyTitle']) {
      expect(dialog).toContain(`admin.extensionInstall.hostAction.${key}`)
    }
    // 启用失败必须是失败文案 + 只重试启用，不能落进成功分支。
    expect(dialog).toContain("props.failurePhase === 'activate'")
    expect(dialog).toContain('admin.extensionInstall.hostAction.enableFailedTitle')
    expect(dialog).toContain('admin.extensionInstall.hostAction.retryEnable')
  })

  test('announces the enable step before the operator confirms', () => {
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.titleEnable')")
    expect(dialog).toContain("t('admin.extensionInstall.hostAction.enableNoticeTitle')")
    expect(dialog).toContain('admin.extensionInstall.hostAction.confirmEnable')
    expect(dialog).toContain('enableNoticeBodyDelegated')
  })

  test('renders cancel, retry, success and failure branches', () => {
    for (const key of ['cancel', 'retry', 'confirm', 'close', 'failedTitle', 'openPlugins', 'openPluginsEnable', 'openThemes']) {
      expect(dialog).toContain(`admin.extensionInstall.hostAction.${key}`)
    }
    expect(dialog).toContain("@click=\"emit('confirm')\"")
    expect(dialog).toContain("@click=\"cancel\"")
    expect(dialog).toContain("@click=\"emit('retry')\"")
    expect(dialog).toContain("@click=\"emit('openManagement')\"")
  })

  test('returns focus to the trigger and leaves the initial focus to the modal scope', () => {
    expect(dialog).toContain('restoreFocusTo = document.activeElement instanceof HTMLElement')
    // 触发元素可能已被关掉（商城详情先收起）：脱离文档就不归还，免得焦点掉到 body。
    expect(dialog).toContain('if (previous?.isConnected) previous.focus()')
    expect(dialog).toContain('watch(open, syncFocus)')
    // 不和 FocusScope 抢初始焦点：确认动作不是默认焦点，必须显式点击/一次 Tab。
    expect(dialog).not.toContain('autofocus')
    expect(dialog).not.toContain('focusPrimaryAction')
    // 遮罩/ESC 关闭等于取消，不能留下悬挂的 promise。
    expect(dialog).toContain(':dismissible="false"')
  })

  test('every dialog and bridge copy key exists in both locales', async () => {
    const locales = ['zh-CN', 'en-US'] as const
    const keys = new Set([...collectI18nKeys(dialog), ...collectI18nKeys(bridge)])
    const installKeys = [...keys].filter(key => key.startsWith('admin.extensionInstall.'))
    expect(installKeys.length).toBeGreaterThan(0)
    for (const locale of locales) {
      const messages = JSON.parse(await Bun.file(
        new URL(`../../i18n/locales/${locale}.json`, import.meta.url)
      ).text()) as Record<string, unknown>
      for (const key of installKeys) {
        expect({ locale, key, value: lookup(messages, key) }).toMatchObject({ locale, key })
        const value = lookup(messages, key)
        expect(typeof value).toBe('string')
        expect(String(value).length > 0).toBeTrue()
      }
    }
  })
})

describe('bridge injection surface', () => {
  test('injects each host action only for pages that declare it', () => {
    expect(bridge).toContain('const canInstall = adminPageCanInstallExtensions(props.page)')
    expect(bridge).toContain('const canInstallAndEnable = adminPageCanInstallAndEnable(props.page)')
    expect(bridge).toContain('if (!canInstall && !canInstallAndEnable) return base')
    expect(bridge).toContain('installExtension: (request: AdminHostInstallRequest) => hostInstall.start(request)')
    expect(bridge).toContain('installAndEnableExtension: (request: AdminHostInstallRequest) => hostInstall.startEnable(request)')
    expect(bridge).toContain('satisfies AdminPageBridgeV1')
    // 旧宿主行为不变：声明 extension.install 的页面不会多拿到启用能力。
    expect(bridge).not.toContain('installAndEnableExtension: hostInstall')
  })

  test('wires the shared transport and the shared error mapping, not a private copy', () => {
    expect(bridge).toContain('useAdminPageHostInstall({')
    expect(bridge).toContain('install: installFromUrl')
    expect(bridge).toContain('toResult: toAdminHostInstallResult')
    // 安装只能走共享 transport，不能在组件里自己拼安装请求。
    expect(bridge).not.toContain('install-from-url')
    expect(bridge).not.toContain('timeout:')
  })

  test('aborts an in-flight install when the page unmounts', () => {
    expect(bridge).toContain('hostInstall.abortOnUnmount()')
  })

  test('reuses the shared trust path and lifecycle helpers instead of a private enable flow', () => {
    expect(bridge).toContain('executableTrustPath(item, action)')
    expect(bridge).toContain('executableTrustPath(item, action, true)')
    expect(bridge).toContain('`/admin/extensions/${item.id}/${action}`')
    expect(bridge).toContain('useAdminPageHostInstall({')
    // 弹窗自己不该拼生命周期请求。
    expect(bridge).not.toContain("'/enable'")
  })
})

describe('admin page install dialog rendering', () => {
  const impact = {
    schemaVersion: 'sforum.trust-impact@2',
    digest: 'impact-1',
    extensionId: 'sforum.store',
    extensionVersion: '1.4.0',
    capabilities: []
  }
  let Dialog: Awaited<ReturnType<typeof compileVueSfc>>

  beforeAll(async () => {
    Object.assign(globalThis, {
      computed: testVue.computed,
      watch: testVue.watch,
      useI18n: () => ({
        t: (key: string, params?: Record<string, unknown>) => params ? `${key}|${JSON.stringify(params)}` : key
      })
    })
    Dialog = await compileVueSfc(
      fileURLToPath(new URL('../../app/components/extensions/admin/SFAdminPageInstallDialog.vue', import.meta.url)),
      'admin-page-install-dialog',
      {
        ADMIN_HOST_INSTALL_TIMEOUT_MS: 120000,
        SFAdminExecutableTrustImpact: {
          props: ['impact', 'guardAuthorization'],
          template: '<div data-testid="trust-impact">{{ impact.digest }}</div>'
        }
      }
    )
  })

  function uiStubs() {
    return {
      UModal: { props: ['open', 'ui'], template: '<div v-if="open"><slot name="content" /></div>' },
      UButton: {
        props: ['disabled', 'loading', 'icon'],
        emits: ['click'],
        template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
      },
      UAlert: {
        props: ['title', 'description', 'ui'],
        template: '<div data-alert><span data-alert-title>{{ title }}</span><span data-alert-body>{{ description }}</span></div>'
      },
      UIcon: { props: ['name'], template: '<i :data-icon="name" />' }
    }
  }

  function mountDialog(overrides: Record<string, unknown> = {}) {
    return mount(Dialog, {
      props: {
        open: true,
        request: {
          url: 'https://cdn.example.com/sforum.store-1.4.0.zip',
          sha256: 'a'.repeat(64),
          expectedExtensionId: 'sforum.store',
          expectedType: 'plugin'
        },
        state: 'confirming',
        mode: 'enable',
        result: null,
        outcome: null,
        stagedReason: null,
        failurePhase: 'install',
        error: '',
        elapsedSeconds: 0,
        trustStatus: null,
        trustBusy: false,
        trustError: '',
        permission: 'extension.plugin.manage',
        isSuperAdmin: true,
        ...overrides
      },
      global: { stubs: uiStubs() }
    })
  }

  const installed = {
    extensionId: 'sforum.store',
    version: '1.4.0',
    type: 'plugin',
    packageDigest: 'b'.repeat(64),
    upgraded: false,
    requiresTrust: true
  }

  test('an enabled result reads as running, never as merely staged', () => {
    const wrapper = mountDialog({ state: 'success', result: installed, outcome: 'enabled' })
    try {
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.enabledTitle')
      expect(wrapper.text()).not.toContain('admin.extensionInstall.hostAction.stagedOnlyTitle')
    } finally {
      wrapper.unmount()
    }
  })

  test('the success panel states the result once and explains the stop separately', () => {
    const wrapper = mountDialog({
      state: 'success',
      result: { ...installed, type: 'theme' },
      outcome: 'staged',
      stagedReason: 'theme'
    })
    try {
      const headline = wrapper.find('[data-role="result-headline"]').text()
      const alertTitle = wrapper.find('[data-alert-title]').text()
      expect(headline).toBe('admin.extensionInstall.result.installed')
      expect(alertTitle).toBe('admin.extensionInstall.hostAction.stagedThemeTitle')
      expect(headline).not.toBe(alertTitle)
    } finally {
      wrapper.unmount()
    }
  })

  test('a staged upgrade keeps the upgrade-candidate wording', () => {
    const wrapper = mountDialog({
      state: 'success',
      result: { ...installed, upgraded: true },
      outcome: 'staged',
      stagedReason: 'stage'
    })
    try {
      expect(wrapper.find('[data-role="result-headline"]').text())
        .toBe('admin.extensionInstall.result.upgradeStaged')
      expect(wrapper.find('[data-alert-title]').text())
        .toBe('admin.extensionInstall.hostAction.stagedOnlyTitle')
    } finally {
      wrapper.unmount()
    }
  })

  test('an enabled result does not repeat its headline in the alert', () => {
    const wrapper = mountDialog({ state: 'success', result: installed, outcome: 'enabled' })
    try {
      expect(wrapper.find('[data-role="result-headline"]').text())
        .toBe('admin.extensionInstall.hostAction.enabledTitle')
      expect(wrapper.find('[data-alert-title]').text()).toBe('')
      expect(wrapper.find('[data-alert-body]').text())
        .toBe('admin.extensionInstall.hostAction.enabledBody')
    } finally {
      wrapper.unmount()
    }
  })

  test('the enable confirm step discloses that the host confirms capabilities', () => {
    const wrapper = mountDialog()
    try {
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.enableCapabilitiesNote')
    } finally {
      wrapper.unmount()
    }
    const staged = mountDialog({ mode: 'stage' })
    try {
      // stage 模式没有「代你确认能力」这回事，就不该出现这句话。
      expect(staged.text()).not.toContain('admin.extensionInstall.hostAction.enableCapabilitiesNote')
    } finally {
      staged.unmount()
    }
  })

  test('a staged result explains why the host stopped', () => {
    const theme = mountDialog({
      state: 'success',
      result: { ...installed, type: 'theme' },
      outcome: 'staged',
      stagedReason: 'theme'
    })
    try {
      expect(theme.text()).toContain('admin.extensionInstall.hostAction.stagedThemeTitle')
    } finally {
      theme.unmount()
    }
    const delegated = mountDialog({ state: 'success', result: installed, outcome: 'staged', stagedReason: 'super_admin' })
    try {
      expect(delegated.text()).toContain('admin.extensionInstall.hostAction.stagedSuperAdminTitle')
    } finally {
      delegated.unmount()
    }
  })

  test('a failed enable shows the enable failure and only offers to retry the enable', () => {
    const wrapper = mountDialog({
      state: 'error',
      failurePhase: 'activate',
      error: 'extension.lifecycle_failed'
    })
    try {
      // 当前状态在前，后端错误在后：绝不能读成「成功」。
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.enableFailedTitle')
      expect(wrapper.text()).toContain('extension.lifecycle_failed')
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.enableFailedHint')
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.retryEnable')
      expect(wrapper.text()).not.toContain('admin.extensionInstall.hostAction.enabledTitle')
    } finally {
      wrapper.unmount()
    }
  })

  test('the trust review renders the shared impact report and confirms through one event', async () => {
    const wrapper = mountDialog({ state: 'trust-review', trustStatus: { impact, trustRequired: true, trusted: false } })
    try {
      expect(wrapper.find('[data-testid="trust-impact"]').text()).toBe('impact-1')
      const confirm = wrapper.findAll('button').find(candidate =>
        candidate.text() === 'admin.extensionInstall.hostAction.trustReviewConfirm')
      expect(confirm).toBeTruthy()
      await confirm?.trigger('click')
      expect(wrapper.emitted('confirmTrust')).toEqual([[]])
    } finally {
      wrapper.unmount()
    }
  })

  test('the installing state shows the elapsed seconds against the install budget', () => {
    const wrapper = mountDialog({ state: 'installing', elapsedSeconds: 12 })
    try {
      const body = wrapper.find('[data-alert-body]').text()
      expect(body).toContain('admin.extensionInstall.hostAction.installingHint')
      expect(body).toContain('"seconds":12')
      expect(body).toContain('"limit":120')
    } finally {
      wrapper.unmount()
    }
  })

  test('a package that is already running offers no management button', () => {
    for (const outcome of ['enabled', 'upgraded'] as const) {
      const wrapper = mountDialog({ state: 'success', result: installed, outcome })
      try {
        // 只剩「关闭」：信任早在上一次启用时就确认过了，不能再派人去「完成信任与启用」。
        expect(wrapper.findAll('button').map(node => node.text()).filter(Boolean))
          .toEqual(['admin.extensionInstall.hostAction.close'])
        expect(wrapper.text()).not.toContain('admin.extensionInstall.hostAction.openPlugins')
        expect(wrapper.text()).not.toContain('admin.extensionInstall.hostAction.openThemes')
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('a staged package points at the page that still has work and names trust only when it is pending', () => {
    const cases: Array<{ props: Record<string, unknown>, key: string }> = [
      { props: { result: { ...installed, type: 'theme' }, stagedReason: 'theme' }, key: 'openThemes' },
      { props: { result: installed, stagedReason: 'super_admin' }, key: 'openPlugins' },
      { props: { result: installed, stagedReason: 'declined' }, key: 'openPlugins' },
      // 旧宿主只暂存：装完之前说不清制品是否已信任，只能写「启用」。
      { props: { result: installed, stagedReason: 'stage' }, key: 'openPluginsEnable' }
    ]
    for (const { props, key } of cases) {
      const wrapper = mountDialog({ state: 'success', outcome: 'staged', ...props })
      try {
        expect(wrapper.findAll('button').map(node => node.text()).filter(Boolean)).toEqual([
          'admin.extensionInstall.hostAction.close',
          `admin.extensionInstall.hostAction.${key}`
        ])
      } finally {
        wrapper.unmount()
      }
    }
  })

  test('the enable step is announced before the operator clicks install', () => {
    const wrapper = mountDialog()
    try {
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.titleEnable')
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.enableNoticeTitle')
      expect(wrapper.text()).toContain('admin.extensionInstall.hostAction.confirmEnable')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('host action disclosure', () => {
  test('one shared notice serves the plugin detail, the theme detail and the enable dialog', async () => {
    const consumers = [
      'app/pages/admin/extensions/plugins.vue',
      'app/pages/admin/extensions/themes.vue',
      'app/components/admin/SFAdminExtensionEnableDialog.vue'
    ]
    for (const path of consumers) {
      const content = await source(path)
      expect(content).toContain('SFAdminExtensionHostActionsNotice')
      expect(content).toContain('<SFAdminExtensionHostActionsNotice :extension=')
    }
    // 共用同一个 i18n key，避免两处文案漂移。
    expect(notice).toContain("t('admin.extensions.hostActions.title')")
    expect(notice).toContain("t('admin.extensions.hostActions.body')")
    expect(notice).toContain('extensionCanRequestInstall')
  })

  test('the notice stays out of capability risk levels', async () => {
    expect(notice).not.toContain('capabilityRisk')
    expect(notice).not.toContain('capabilityGrants')
    const enableDialog = await source('app/components/admin/SFAdminExtensionEnableDialog.vue')
    // capability 列表仍是独立分支；披露是它前面的一条单独提示。
    expect(enableDialog).toContain("t(`admin.extensions.capabilityRisk.${grant.risk}`)")
    const noticeIndex = enableDialog.indexOf('<SFAdminExtensionHostActionsNotice')
    const capabilityIndex = enableDialog.indexOf('capabilityGrants?.length')
    expect(noticeIndex).toBeGreaterThan(0)
    expect(noticeIndex).toBeLessThan(capabilityIndex)
  })
})
