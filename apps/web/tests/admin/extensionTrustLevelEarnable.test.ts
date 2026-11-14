import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  executableDatabaseGrants,
  executableDatabaseMigrationRisk
} from '../../app/utils/extensions/extensionTrust'

/**
 * 启用弹窗的「可被信任等级授予的权限」披露（M4a，计划 §4.5 M4.1）。
 *
 * 这一段的语义是**自动获得**而不是「可以手动授予」——所以标题必须自解释（本弹窗只渲染
 * label、不渲染 description），并且**一条都没有时整段不渲染**（空段会让运营以为功能坏了）。
 *
 * 夹具直接给 `permissionDefinitions[]`，与 `/admin/extensions/{id}/trust` 的
 * `impact.permissionDefinitions` 同形（后端 `TrustImpact.PermissionDefinitions`
 * 就是 `[]ManifestPermissionDefinition`，带 `trustLevelEarnable`）。
 */
const zh = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    zh
  )
}
const t = (key: string, params: Record<string, string | number> = {}) => {
  const value = lookup(key)
  if (typeof value !== 'string') return `[${key}]`
  return value.replace(/\{(\w+)\}/g, (_m, name: string) => String(params[name] ?? `{${name}}`))
}

const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
const UBadgeStub = defineComponent({ name: 'UBadge', template: '<span v-bind="$attrs"><slot /></span>' })
const UAlertStub = defineComponent({ name: 'UAlert', props: { title: String, description: String }, template: '<div><slot /></div>' })

const impactPath = fileURLToPath(
  new URL('../../app/components/admin/SFAdminExecutableTrustImpact.vue', import.meta.url)
)
const Impact = await compileVueSfc(impactPath, 'sf-admin-executable-trust-impact', {
  computed,
  ref,
  watch,
  onMounted,
  onBeforeUnmount,
  useI18n: () => ({ t, te: (key: string) => typeof lookup(key) === 'string', locale: ref('zh-CN') }),
  executableDatabaseGrants,
  executableDatabaseMigrationRisk
})

/** 最小 impact 夹具：只填本节用到的字段，其余留空（组件对缺字段是容错的）。 */
function makeImpact(permissionDefinitions: unknown[] = []) {
  return {
    action: 'enable',
    extensionId: 'acme.crm',
    extensionVersion: '1.0.0',
    extensionType: 'plugin',
    source: 'uploaded',
    packageDigest: 'sha256:x',
    manifestContract: 'sforum.manifest.v3',
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
    permissionDefinitions,
    media: [],
    navigation: [],
    regions: [],
    contributions: [],
    capabilities: [],
    permissions: [],
    requiredFeatures: [],
    dependencies: [],
    lifecycle: null,
    openapi: [],
    packageFiles: [],
    requestedAuthority: { rawCore: false, kernel: false },
    contracts: { hostApi: '1' },
    digest: 'sha256:y'
  }
}

function mountImpact(permissionDefinitions: unknown[] = []) {
  return mount(Impact, {
    props: { impact: makeImpact(permissionDefinitions) },
    global: { components: { UIcon: UIconStub, UBadge: UBadgeStub, UAlert: UAlertStub } }
  })
}

const SECTION = '[data-testid="extension-trust-level-earnable"]'

describe('可被信任等级授予的权限 · 披露段', () => {
  test('有 trustLevelEarnable 的权限时渲染该段，且标题写清「等级达到后自动获得」', async () => {
    const wrapper = mountImpact([
      { key: 'acme.export', contractVersion: 'acme.export@1', label: { 'zh-CN': '导出数据', 'en-US': 'Export data' }, trustLevelEarnable: true }
    ])
    try {
      await flushPromises()
      const section = wrapper.find(SECTION)
      expect(section.exists()).toBe(true)
      const title = t('admin.extensions.trust.trustLevelEarnable.title')
      expect(section.text()).toContain(title)
      // 标题本身必须点明「自动获得」，否则「可授予」会被读成「可以手动授予」。
      expect(title).toContain('自动获得')
      // 逐条：label（当前语言）+ key 小字
      expect(section.find('[data-trust-level-earnable-key="acme.export"]').exists()).toBe(true)
      expect(section.text()).toContain('导出数据')
      expect(section.text()).toContain('acme.export')
    } finally {
      wrapper.unmount()
    }
  })

  test('没有这类权限时**整段不渲染**（不是空态）', async () => {
    const withoutFlag = mountImpact([
      { key: 'acme.read', label: '只读', trustLevelEarnable: false },
      { key: 'acme.plain', label: '无标记' }
    ])
    const empty = mountImpact([])
    try {
      await flushPromises()
      expect(withoutFlag.find(SECTION).exists()).toBe(false)
      expect(empty.find(SECTION).exists()).toBe(false)
    } finally {
      withoutFlag.unmount()
      empty.unmount()
    }
  })

  test('label 的两种 wire 形态都吃：plain string 与 locale map', async () => {
    const wrapper = mountImpact([
      { key: 'acme.a', label: '导出数据', trustLevelEarnable: true },
      { key: 'acme.b', label: { 'zh-CN': '审核队列', 'en-US': 'Review queue' }, trustLevelEarnable: true }
    ])
    try {
      await flushPromises()
      const text = wrapper.find(SECTION).text()
      expect(text).toContain('导出数据')
      expect(text).toContain('审核队列')
      expect(text).not.toContain('Review queue')
    } finally {
      wrapper.unmount()
    }
  })

  test('label 缺失时回落 key（不渲染空白行）', async () => {
    const wrapper = mountImpact([{ key: 'acme.nolabel', trustLevelEarnable: true }])
    try {
      await flushPromises()
      const row = wrapper.find('[data-trust-level-earnable-key="acme.nolabel"]')
      expect(row.exists()).toBe(true)
      expect(row.text()).toContain('acme.nolabel')
    } finally {
      wrapper.unmount()
    }
  })

  test('不渲染 description（既有约束：弹窗靠 label 自解释）', async () => {
    const wrapper = mountImpact([
      {
        key: 'acme.export',
        label: '导出数据',
        description: { 'zh-CN': '这条描述不该出现在启用弹窗里', 'en-US': 'should not render' },
        trustLevelEarnable: true
      }
    ])
    try {
      await flushPromises()
      expect(wrapper.find(SECTION).text()).not.toContain('这条描述不该出现在启用弹窗里')
    } finally {
      wrapper.unmount()
    }
  })
})
