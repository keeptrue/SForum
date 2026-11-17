import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, defineComponent, ref } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'
import {
  migrationFailureCount,
  migrationLedgerChanged,
  migrationStatusLabelKey,
  shortMigrationChecksum,
  type ExtensionMigrationRecord
} from '../../../app/utils/admin/extensions/migrations'

const migrationsPath = new URL('../../../app/pages/admin/extensions/migrations.vue', import.meta.url).pathname
const addedPagesPath = new URL('../../../app/pages/admin/extensions/added-pages.vue', import.meta.url).pathname
const modulesPath = new URL('../../../app/config/adminModules.ts', import.meta.url).pathname
const source = (path: string) => readFileSync(path, 'utf8')
const zh = () => JSON.parse(source(new URL('../../../i18n/locales/zh-CN.json', import.meta.url).pathname)) as Record<string, unknown>
const en = () => JSON.parse(source(new URL('../../../i18n/locales/en-US.json', import.meta.url).pathname)) as Record<string, unknown>

function lookup(messages: Record<string, unknown>, key: string): string {
  const value = key.split('.').reduce<unknown>((node, part) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[part]
    return undefined
  }, messages)
  return typeof value === 'string' ? value : key
}

function record(input: Partial<ExtensionMigrationRecord>): ExtensionMigrationRecord {
  return {
    path: 'migrations/001_init.sql',
    checksum: 'abcdef0123456789abcdef0123456789',
    status: 'recorded',
    appliedAt: '2026-01-02T03:04:05Z',
    ...input
  }
}

// ── 纯逻辑：状态映射、失败计数、校验和截断、幂等反馈 ───────────────────────────
describe('extension migration presentation helpers', () => {
  test('maps the three contract statuses and refuses to guess unknown ones', () => {
    expect(migrationStatusLabelKey('recorded')).toBe('admin.extensions.migrations.status.recorded')
    expect(migrationStatusLabelKey('applied')).toBe('admin.extensions.migrations.status.applied')
    expect(migrationStatusLabelKey('failed')).toBe('admin.extensions.migrations.status.failed')
    expect(migrationStatusLabelKey('skipped')).toBeNull()
  })

  test('counts failed rows so the page can warn about missing files', () => {
    expect(migrationFailureCount([record({ status: 'failed' }), record({ status: 'recorded' })])).toBe(1)
    expect(migrationFailureCount([])).toBe(0)
    expect(migrationFailureCount(undefined)).toBe(0)
  })

  test('shortens long checksums but keeps short ones intact', () => {
    expect(shortMigrationChecksum('abcdef0123456789abcdef')).toBe('abcdef012345…')
    expect(shortMigrationChecksum('abc')).toBe('abc')
    expect(shortMigrationChecksum('')).toBe('')
    expect(shortMigrationChecksum(undefined)).toBe('')
  })

  test('detects whether apply actually added rows (idempotent second run)', () => {
    const before = [record({ path: 'a.sql' })]
    expect(migrationLedgerChanged(before, [...before])).toBe(false)
    expect(migrationLedgerChanged(before, [record({ path: 'a.sql' }), record({ path: 'b.sql' })])).toBe(true)
    // 同一路径换了校验和也算变化（文件被改过）。
    expect(migrationLedgerChanged(before, [record({ path: 'a.sql', checksum: 'ffff' })])).toBe(true)
  })
})

// ── 挂载：迁移账本页 ────────────────────────────────────────────────────────────
type Call = { method: string, path: string }

const SkeletonStub = defineComponent({ name: 'SFSkeleton', template: '<div data-skeleton />' })
const EmptyStateStub = defineComponent({
  name: 'SFEmptyState',
  props: { title: String, description: String, icon: String },
  template: '<div data-empty-state :data-title="title" :data-description="description" />'
})
const AlertStub = defineComponent({ name: 'SFAlert', props: { title: String, variant: String }, template: '<div data-alert :data-variant="variant" :data-title="title" />' })
const ToolbarStub = defineComponent({ name: 'UDashboardToolbar', template: '<div data-toolbar><slot name="left" /><slot name="right" /></div>' })
const ButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean, loading: Boolean, title: String },
  emits: ['click'],
  template: '<button data-button :data-disabled="String(disabled)" @click="$emit(\'click\')"><slot /></button>'
})
const BadgeStub = defineComponent({ name: 'UBadge', props: { color: String }, template: '<span data-badge :data-color="color"><slot /></span>' })
const SelectStub = defineComponent({
  name: 'USelect',
  props: { modelValue: String, items: Array },
  emits: ['update:modelValue'],
  template: '<select data-select :value="modelValue"><option v-for="o in items" :key="o.value" :value="o.value">{{ o.label }}</option></select>'
})
const IconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i :data-icon="name" />' })
const ModalStub = defineComponent({
  name: 'UModal',
  props: { open: Boolean },
  template: '<div v-if="open" data-modal><slot name="content" /></div>'
})

function i18nBindings(messages: Record<string, unknown>) {
  return {
    t: (key: string, params?: Record<string, string>) => {
      const raw = lookup(messages, key)
      return params ? raw.replace(/\{(\w+)\}/g, (_m, name: string) => params[name] ?? '') : raw
    },
    te: (key: string) => lookup(messages, key) !== key
  }
}

async function mountMigrations(options: {
  extensions?: unknown[]
  ledger?: ExtensionMigrationRecord[]
  applyResult?: () => Promise<unknown>
  canManage?: boolean
  messages?: Record<string, unknown>
} = {}) {
  const messages = options.messages || zh()
  const calls: Call[] = []
  const toasts: Array<{ title: string, color: string }> = []
  const { t, te } = i18nBindings(messages)
  const extensions = options.extensions ?? [
    { id: 'sforum.wiki', name: 'Wiki', version: '1.2.0', type: 'plugin', status: 'enabled', manifest: { migrations: [{ path: 'migrations/001_init.sql' }] } }
  ]

  const request = async (path: string, init?: { method?: string }) => {
    const method = init?.method || 'GET'
    calls.push({ method, path })
    if (path === '/admin/extensions') return extensions
    if (path.endsWith('/migrations/apply')) {
      return options.applyResult ? await options.applyResult() : [record({ path: 'migrations/001_init.sql' })]
    }
    if (path.endsWith('/migrations')) return options.ledger ?? []
    throw new Error(`unexpected ${method} ${path}`)
  }

  const Page = await compileVueSfc(migrationsPath, 'admin-extension-migrations-page', {
    computed,
    ref,
    useI18n: () => ({ t, te }),
    useRoute: () => ({ query: {} }),
    useRouter: () => ({ replace: async () => {} }),
    useToast: () => ({ add: (toast: { title: string, color: string }) => { toasts.push(toast) } }),
    useSeoMeta: () => {},
    useSiteDateTime: () => ({ format: () => '2026-01-02 11:04' }),
    useAdminPage: () => ({ id: '/extensions/migrations', icon: 'i-lucide-database-zap' }),
    usePermissions: () => ({ can: () => options.canManage !== false }),
    useAuthSession: () => ({ user: ref({ roleKeys: [] }) }),
    useApiClient: () => ({ request }),
    // 页面显式导入该 composable；harness 会剥掉 import，所以按同名绑定注入，
    // 端点路径与真实实现保持一致（断言才有意义）。
    useAdminExtensionMigrations: () => ({
      list: (id: string) => request(`/admin/extensions/${encodeURIComponent(id)}/migrations`),
      apply: (id: string) => request(`/admin/extensions/${encodeURIComponent(id)}/migrations/apply`, { method: 'POST' })
    }),
    extensionLocalizedDisplay: (item: { name?: string }) => ({ name: item.name || '' }),
    // 与真实实现同语义：从宿主错误信封里取 message，页面才有「服务端原因」可显示。
    apiErrorMessage: (error: { data?: { message?: string } }) => error?.data?.message || '',
    migrationFailureCount,
    migrationLedgerChanged,
    migrationStatusLabelKey,
    shortMigrationChecksum
  })

  const Root = defineComponent({
    name: 'MigrationPageRoot',
    components: { Page },
    template: '<Suspense><Page /></Suspense>'
  })

  const wrapper = mount(Root, {
    global: {
      components: {
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        UBadge: BadgeStub,
        USelect: SelectStub,
        UIcon: IconStub,
        UModal: ModalStub,
        SFAlert: AlertStub,
        SFEmptyState: EmptyStateStub,
        SFSkeleton: SkeletonStub
      }
    }
  })
  await flushPromises()
  return { wrapper, calls, toasts }
}

describe('admin extension migrations page', () => {
  test('renders the ledger rows with status, checksum and note', async () => {
    const { wrapper, calls } = await mountMigrations({
      ledger: [
        record({ path: 'migrations/001_init.sql', status: 'recorded' }),
        record({ path: 'migrations/002_users.sql', status: 'failed', checksum: '', message: 'file missing' })
      ]
    })
    try {
      expect(calls[0]).toEqual({ method: 'GET', path: '/admin/extensions' })
      expect(calls[1]!.path).toBe('/admin/extensions/sforum.wiki/migrations')

      const rows = wrapper.findAll('[data-testid="migration-row"]')
      expect(rows).toHaveLength(2)
      expect(rows[0]!.text()).toContain('migrations/001_init.sql')
      expect(rows[0]!.text()).toContain(lookup(zh(), 'admin.extensions.migrations.status.recorded'))
      expect(rows[1]!.text()).toContain(lookup(zh(), 'admin.extensions.migrations.status.failed'))
      expect(rows[1]!.text()).toContain('file missing')
      // 有 failed 行时必须出现警告条。
      expect(wrapper.find('[data-alert][data-variant="warning"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('apply goes through a confirm dialog and posts to the documented endpoint', async () => {
    const { wrapper, calls, toasts } = await mountMigrations()
    try {
      const applyButton = wrapper.find('[data-testid="migration-apply"]')
      expect(applyButton.exists()).toBe(true)

      await applyButton.trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-modal]').exists()).toBe(true)
      // 确认之前不得发写请求。
      expect(calls.filter(call => call.method === 'POST')).toHaveLength(0)

      await wrapper.find('[data-testid="migration-apply-confirm"]').trigger('click')
      await flushPromises()

      const posts = calls.filter(call => call.method === 'POST')
      expect(posts).toHaveLength(1)
      expect(posts[0]!.path).toBe('/admin/extensions/sforum.wiki/migrations/apply')
      expect(toasts.at(-1)?.title).toBe(lookup(zh(), 'admin.extensions.migrations.applyRecorded'))
    } finally {
      wrapper.unmount()
    }
  })

  test('a failed apply surfaces the server reason instead of a generic message', async () => {
    const { wrapper, toasts } = await mountMigrations({
      applyResult: async () => {
        throw { data: { message: 'extension.safe_mode_active' } }
      }
    })
    try {
      await wrapper.find('[data-testid="migration-apply"]').trigger('click')
      await flushPromises()
      await wrapper.find('[data-testid="migration-apply-confirm"]').trigger('click')
      await flushPromises()

      expect(toasts.at(-1)?.color).toBe('error')
      expect(toasts.at(-1)?.title).toBe('extension.safe_mode_active')
    } finally {
      wrapper.unmount()
    }
  })

  test('the apply button is disabled without the plugin-manage permission', async () => {
    const { wrapper } = await mountMigrations({ canManage: false })
    try {
      expect(wrapper.find('[data-testid="migration-apply"]').attributes('data-disabled')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })

  test('an empty ledger falls back to the shared empty state, not a blank table', async () => {
    const { wrapper } = await mountMigrations({ ledger: [] })
    try {
      expect(wrapper.find('[data-empty-state]').exists()).toBe(true)
      expect(wrapper.find('[data-empty-state]').attributes('data-title')).toBe(lookup(zh(), 'admin.extensions.migrations.emptyTitle'))
      expect(wrapper.find('[data-testid="migration-row"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

// ── 挂载：主题新增页面（只读） ──────────────────────────────────────────────────
describe('admin theme-added pages page', () => {
  test('renders rows read-only and never offers an edit action', async () => {
    const messages = zh()
    const { t, te } = i18nBindings(messages)
    const calls: string[] = []
    const rows = [
      { id: 'demo.theme.docs', action: 'add', path: '/docs', extensionId: 'demo.theme', version: '1.0.0', view: 'template', access: 'public' },
      { id: 'forum.home', action: 'replace', target: 'forum.home', extensionId: 'demo.theme', version: '1.0.0', view: 'template', access: 'public' }
    ]

    const Page = await compileVueSfc(addedPagesPath, 'admin-added-pages-page', {
      computed,
      ref,
      useI18n: () => ({ t, te }),
      useSeoMeta: () => {},
      useAdminPage: () => ({ id: '/extensions/added-pages', icon: 'i-lucide-file-plus-2' }),
      useAdminAddedPages: () => ({
        list: async () => { calls.push('/admin/pages/added'); return rows }
      }),
      apiErrorMessage: (error: { data?: { message?: string } }) => error?.data?.message || ''
    })

    const Root = defineComponent({ name: 'AddedPagesRoot', components: { Page }, template: '<Suspense><Page /></Suspense>' })
    const wrapper = mount(Root, {
      global: {
        components: {
          UDashboardToolbar: ToolbarStub,
          UButton: ButtonStub,
          UBadge: BadgeStub,
          UIcon: IconStub,
          SFAlert: AlertStub,
          SFEmptyState: EmptyStateStub,
          SFSkeleton: SkeletonStub
        }
      }
    })
    await flushPromises()
    try {
      expect(calls).toEqual(['/admin/pages/added'])
      const rendered = wrapper.findAll('[data-testid="added-page-row"]')
      expect(rendered).toHaveLength(2)
      expect(rendered[0]!.text()).toContain('demo.theme.docs')
      expect(rendered[0]!.text()).toContain('/docs')
      expect(rendered[1]!.text()).toContain(lookup(zh(), 'admin.extensions.addedPages.action.replace'))

      // 只读：表体里不得出现任何按钮（刷新按钮在工具栏，不在行内）。
      const bodyButtons = wrapper.findAll('[data-testid="added-page-row"] [data-button]')
      expect(bodyButtons).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('empty list shows the shared empty state', async () => {
    const messages = zh()
    const { t, te } = i18nBindings(messages)
    const Page = await compileVueSfc(addedPagesPath, 'admin-added-pages-empty', {
      computed,
      ref,
      useI18n: () => ({ t, te }),
      useSeoMeta: () => {},
      useAdminPage: () => ({ id: '/extensions/added-pages', icon: 'i-lucide-file-plus-2' }),
      useAdminAddedPages: () => ({ list: async () => [] }),
      apiErrorMessage: (error: { data?: { message?: string } }) => error?.data?.message || ''
    })
    const Root = defineComponent({ name: 'AddedPagesEmptyRoot', components: { Page }, template: '<Suspense><Page /></Suspense>' })
    const wrapper = mount(Root, {
      global: {
        components: {
          UDashboardToolbar: ToolbarStub, UButton: ButtonStub, UBadge: BadgeStub, UIcon: IconStub,
          SFAlert: AlertStub, SFEmptyState: EmptyStateStub, SFSkeleton: SkeletonStub
        }
      }
    })
    await flushPromises()
    try {
      expect(wrapper.find('[data-empty-state]').attributes('data-title')).toBe(lookup(zh(), 'admin.extensions.addedPages.emptyTitle'))
    } finally {
      wrapper.unmount()
    }
  })
})

// ── 接线与双语 ─────────────────────────────────────────────────────────────────
describe('admin wiring and copy', () => {
  test('both modules are registered with their page components and permissions', () => {
    const modules = source(modulesPath)
    expect(modules).toContain("id: '/extensions/migrations'")
    expect(modules).toContain("componentName: 'AdminExtensionMigrations'")
    expect(modules).toContain("id: '/extensions/added-pages'")
    expect(modules).toContain("componentName: 'AdminExtensionAddedPages'")
    // 页面访问权限与后端一致：只读面用 extension.view，apply 用 extension.plugin.manage。
    expect(modules).toContain("requiredPermissions: ['extension.view', 'extension.plugin.manage']")
    expect(modules).toContain("requiredPermissions: ['extension.view', 'extension.theme.manage', 'extension.manage']")
  })

  test('both locales carry the same keys for the two new namespaces', () => {
    const zhAdmin = (zh().admin as Record<string, unknown>) as { extensions: Record<string, Record<string, unknown>>, nav: Record<string, unknown> }
    const enAdmin = (en().admin as Record<string, unknown>) as { extensions: Record<string, Record<string, unknown>>, nav: Record<string, unknown> }

    expect(Object.keys(enAdmin.extensions.migrations!).sort()).toEqual(Object.keys(zhAdmin.extensions.migrations!).sort())
    expect(Object.keys(enAdmin.extensions.addedPages!).sort()).toEqual(Object.keys(zhAdmin.extensions.addedPages!).sort())
    expect(Object.keys((enAdmin.extensions.migrations as { status: object }).status).sort())
      .toEqual(Object.keys((zhAdmin.extensions.migrations as { status: object }).status).sort())
    expect(zhAdmin.nav.extensionMigrations).toBeTruthy()
    expect(enAdmin.nav.extensionMigrations).toBeTruthy()
    expect(zhAdmin.nav.extensionAddedPages).toBeTruthy()
    expect(enAdmin.nav.extensionAddedPages).toBeTruthy()
  })
})
