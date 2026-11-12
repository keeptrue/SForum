import { describe, expect, test } from 'bun:test'
import { computed, ref, nextTick } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'
import type { AdminCapabilityGrant } from '../../app/utils/admin/adminExtensions'

/**
 * 启用/信任披露面：能力清单必须**渲染**后端已给的 `labelZh/labelEn/description`，
 * 而不是只在折叠的 JSON dump 里出现（「键在值空」那类假绿的近亲：数据在、界面不显示）。
 *
 * 稳定选择器 `data-testid="extension-capabilities"` 与 `[data-capability-disclosure]`
 * 供老运的 390 验收断言，改名必须同批回报。
 */
const i18nStub = () => ({
  t: (key: string) => key,
  locale: { value: 'zh-CN' }
})

const TrustImpact = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/SFAdminExecutableTrustImpact.vue', import.meta.url)),
  'trust-impact',
  {
    computed,
    ref,
    watch: testVue.watch,
    onMounted: testVue.onMounted,
    onBeforeUnmount: testVue.onBeforeUnmount,
    nextTick,
    useI18n: i18nStub,
    executableDatabaseGrants: () => [],
    executableDatabaseMigrationRisk: () => ({
      hasDatabaseChanges: false,
      hasMigrations: false,
      nonTransactional: [],
      missingRequiredBackup: false
    })
  }
)

const UIconStub = { props: ['name'], template: '<i />' }
const UBadgeStub = { props: ['color', 'variant', 'size'], template: '<span><slot /></span>' }
const UAlertStub = {
  props: ['title', 'description', 'color', 'variant', 'icon'],
  template: '<div><slot /></div>'
}

function capability(overrides: Partial<AdminCapabilityGrant> = {}): AdminCapabilityGrant {
  return {
    key: 'badges.fact.write',
    risk: 'medium',
    labelZh: '写入徽章事实计数',
    labelEn: 'Write badge fact counters',
    description: '允许扩展写入自定义事实计数。',
    ...overrides
  }
}

function impactWith(capabilities: AdminCapabilityGrant[]) {
  return {
    action: 'enable',
    extensionId: 'example.ext',
    extensionVersion: '1.0.0',
    extensionType: 'plugin',
    source: 'builtin',
    packageDigest: 'a'.repeat(64),
    manifestContract: 'sforum.extension/v3',
    schemaVersion: 3,
    artifactDigests: {},
    binaries: [],
    backend: {},
    routes: [],
    guards: [],
    guardDeclarations: [],
    hooks: [],
    events: [],
    migrations: [],
    migrationDeclarations: [],
    providers: [],
    jobs: [],
    schedules: [],
    components: [],
    registryComponents: [],
    templates: [],
    assets: [],
    content: [],
    database: null,
    cache: [],
    services: [],
    commands: [],
    adminSurfaces: [],
    queries: [],
    queryResultFilters: [],
    identity: null,
    permissionDefinitions: [],
    media: [],
    navigation: [],
    regions: [],
    contributions: [],
    capabilities,
    permissions: [],
    requiredFeatures: [],
    dependencies: [],
    lifecycle: null,
    openapi: [],
    packageFiles: [],
    requestedAuthority: {
      backendExecution: true,
      adminFrontendExecution: false,
      rawRequest: false,
      rawCoreDatabase: false,
      outboundNetwork: false,
      packageFiles: [],
      secrets: []
    },
    contracts: { hostApi: 'sforum.host-api/v2' },
    digest: 'b'.repeat(64)
  }
}

function mountImpact(capabilities: AdminCapabilityGrant[]) {
  return mount(TrustImpact, {
    props: { impact: impactWith(capabilities) },
    global: { stubs: { UIcon: UIconStub, UBadge: UBadgeStub, UAlert: UAlertStub } }
  })
}

describe('capability disclosure on the trust surface', () => {
  test('renders the backend label instead of only dumping JSON', () => {
    const wrapper = mountImpact([capability()])
    const section = wrapper.find('[data-testid="extension-capabilities"]')
    expect(section.exists()).toBe(true)
    // 值级：插件/后端声明的中文 label 真的出现在渲染文本里。
    expect(section.text()).toContain('写入徽章事实计数')
    expect(section.text()).toContain('badges.fact.write')
    wrapper.unmount()
  })

  test('adds a trust disclosure for the badge fact capability', () => {
    const wrapper = mountImpact([capability()])
    const disclosure = wrapper.find('[data-capability-disclosure]')
    expect(disclosure.exists()).toBe(true)
    expect(disclosure.text()).toBe('admin.extensions.trust.capabilities.disclosures.badgesFactWrite')
    wrapper.unmount()
  })

  test('other capabilities fall back to the backend description, never to an empty row', () => {
    const wrapper = mountImpact([capability({ key: 'host.api', labelZh: '宿主 API', description: '可调用宿主 API。' })])
    const section = wrapper.find('[data-testid="extension-capabilities"]')
    expect(section.text()).toContain('宿主 API')
    expect(section.text()).toContain('可调用宿主 API。')
    expect(wrapper.find('[data-capability-disclosure]').exists()).toBe(false)
    wrapper.unmount()
  })

  test('no capabilities means no empty section', () => {
    const wrapper = mountImpact([])
    expect(wrapper.find('[data-testid="extension-capabilities"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
