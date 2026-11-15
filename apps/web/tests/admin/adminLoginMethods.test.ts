import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { compileScript, compileTemplate, parse, rewriteDefault } from '@vue/compiler-sfc'
import { installTestDom } from '../helpers/dom'
import { compileSfcSource } from '../helpers/vueSfc'

import { adminPageDefinitions } from '../../app/config/adminModules'
import { ROLE_TEMPLATE_DEFINITIONS } from '../../app/config/roleTemplates'
import {
  adminProbeFeedback,
  adminProbeLabelKind,
  adminProviderIcon,
  adminProviderStateBadges,
  adminProviderSupportsOp,
  adminProviderTitle,
  type AdminIdentityProvider
} from '../../app/utils/admin/adminLoginMethods'

installTestDom({ url: 'http://localhost/admin/settings/login-methods' })
const adminRoot = fileURLToPath(new URL('../..', import.meta.url))
const adminVue = await import('vue')
const { mount, flushPromises } = await import('@vue/test-utils')
const adminSettingsRendererStub = {
  emits: ['action'],
  template: '<button data-testid="admin-configure" @click="$emit(\'action\', { id: \'configure\', label: \'Configure\', available: true })">Configure</button>'
}

async function compileAdminComponent(relativePath: string) {
  const path = join(adminRoot, relativePath)
  const parsed = parse(await Bun.file(path).text(), { filename: path })
  const script = compileScript(parsed.descriptor, { id: `admin-${relativePath}` })
  const template = compileTemplate({
    source: parsed.descriptor.template?.content || '', filename: path, id: `admin-${relativePath}`,
    compilerOptions: { bindingMetadata: script.bindings }
  })
  if (template.errors.length) throw new Error(String(template.errors[0]))
  const scriptCode = `const { withAsyncContext: _withAsyncContext, defineComponent: _defineComponent } = Vue\n${script.content
    .replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
    .replace(/^export type \{[^}]+\}\s*$/gm, '')
    .replace(/import\.meta\.client/g, 'true')}`
  const templateCode = template.code
    .replace(/import \{([^}]+)\} from \"vue\"/, (_match, names) => `const {${names.replace(/(\w+) as (\w+)/g, '$1: $2')}} = Vue`)
    .replace('export function render', 'function render')
  const source = `${rewriteDefault(scriptCode, '__sfc__', ['typescript'])}\n${templateCode}\n__sfc__.render = render\nreturn __sfc__`
  return new Function('Vue', 'SFExtensionSettingsRenderer', new Bun.Transpiler({ loader: 'ts', target: 'esnext' }).transformSync(source))(
    adminVue,
    adminSettingsRendererStub
  )
}

const AdminLoginMethods = await compileAdminComponent('app/pages/admin/settings/login-methods.vue')

function provider(overrides: Partial<AdminIdentityProvider> = {}): AdminIdentityProvider {
  return {
    id: 'example.provider.auth', kind: 'auth', priority: 10,
    operations: ['login.start', 'registration.start', 'link.start', 'provider.probe'],
    ownerExtensionId: 'example.provider', ownerPackageDigest: 'a'.repeat(64),
    discovered: true, trusted: true, enabled: true, configured: true, probed: true,
    artifactBound: true, activated: true, publiclyActivated: true,
    loginEnabled: true, registrationEnabled: true, linkEnabled: true,
    revision: 7, callbackPath: '/auth/providers/example.provider.auth/callback', safeMode: false,
    label: 'Example Login', icon: 'i-lucide-key-round', lastProbeReason: 'example.probe_ok',
    ...overrides
  }
}

describe('admin login-method catalog contracts', () => {
  test('exposes the permission-protected admin route and recommended role mapping', () => {
    const page = adminPageDefinitions.find(item => item.id === '/settings/login-methods')
    expect(page?.componentName).toBe('AdminLoginMethods')
    expect(page?.requiredPermissions).toEqual(['identity.provider.manage'])
    expect(ROLE_TEMPLATE_DEFINITIONS.find(item => item.key === 'operator')?.permissionKeys)
      .toContain('identity.provider.manage')
  })

  test('projects generic catalog label, icon, operations, probe and lifecycle badges', () => {
    const full = provider()
    expect(adminProviderTitle(full)).toBe('Example Login')
    expect(adminProviderIcon(full)).toBe('i-lucide-key-round')
    expect(adminProviderSupportsOp(full, 'login')).toBe(true)
    expect(adminProviderSupportsOp(provider({ operations: ['provider.probe'] }), 'login')).toBe(false)
    expect(adminProbeLabelKind(full)).toBe('ok')
    expect(adminProbeFeedback({ ok: true, reason: 'example.probe_ok' })).toEqual({ success: true, reason: 'example.probe_ok' })
    for (const badge of adminProviderStateBadges(full)) expect(badge.on).toBe(true)
  })

  test('falls back to a generic identity rather than inferring a provider brand', () => {
    const bare = provider({ label: undefined, icon: undefined })
    expect(adminProviderTitle(bare)).toBe('example.provider')
    expect(adminProviderIcon(bare)).toBe('i-lucide-key-round')
  })

  test('mounts the production page and sends generic settings, activation, and probe requests', async () => {
    const requests: Array<{ path: string, options?: Record<string, unknown> }> = []
    const item = provider()
    Object.assign(globalThis, {
      computed: adminVue.computed, ref: adminVue.ref, reactive: adminVue.reactive, watch: adminVue.watch,
      definePageMeta: () => {}, defineOptions: () => {}, useSeoMeta: () => {},
      useI18n: () => ({ locale: adminVue.ref('en-US'), t: (key: string) => key }),
      useToast: () => ({ add: () => {} }), useAdminPage: () => ({ icon: 'i-lucide-key-round' }),
      // UX2-15：页面现在把选中提供方同步进 `?tab=`，挂载需要路由替身。
      useRoute: () => ({ query: {} }), useRouter: () => ({ replace: async () => {} }),
      // UX2-15 全收：页面改用共享固定页签条（不再是自建 tablist），挂载需要真实组件。
      SFAdminFixedTabNav: await compileSfcSource(
        await Bun.file(join(adminRoot, 'app/components/admin/settings/shared/SFAdminFixedTabNav.vue')).text(),
        'admin-login-methods-fixed-tab-nav'
      ),
      useAdminRoutes: () => ({ path: (path: string) => path }),
      apiErrorMessage: () => '', apiErrorStatusCode: () => 0,
      recommendedExtensionSettingValues: () => ({ client_id: '' }),
      adminProviderIcon: (entry: AdminIdentityProvider) => entry.icon || 'i-lucide-key-round',
      adminProviderTitle: (entry: AdminIdentityProvider) => entry.label || entry.ownerExtensionId,
      adminProviderShortDigest: () => 'aaaaaaaa',
      adminProviderStateBadges: () => [], adminProviderSupportsOp: () => true,
      adminProbeLabelKind: () => 'ok', adminProbeFeedback: (result: { ok: boolean, reason?: string }) => ({ success: result.ok, reason: result.reason || '' }),
      useApiClient: () => ({ request: async (path: string, options?: Record<string, unknown>) => {
        requests.push({ path, options })
        if (path === '/admin/identity/providers') return [item]
        if (path.includes('page-bootstrap')) return { settings: { items: [{ key: 'client_id', value: '', type: 'text' }] } }
        if (path.endsWith('/probe')) return { ok: true, reason: 'example.probe_ok' }
        if (path.includes('/settings/actions/')) return { success: true, message: 'done', durationMs: 1 }
        return item
      } })
    })
    const wrapper = mount({ components: { AdminLoginMethods }, template: '<Suspense><AdminLoginMethods /></Suspense>' }, {
      global: { stubs: {
        UIcon: true, UBadge: { template: '<span><slot /></span>' }, UAlert: { props: ['title', 'description'], template: '<div>{{ title }}{{ description }}</div>' },
        SFAlert: { props: ['title', 'description'], template: '<div>{{ title }}{{ description }}<slot /></div>' }, SFEmptyState: true,
        UDashboardToolbar: { template: '<section><slot name="left" /><slot name="right" /></section>' },
        UButton: { emits: ['click'], template: '<button @click="$emit(\'click\', $event)"><slot /></button>' },
        USwitch: { props: ['modelValue'], emits: ['update:modelValue'], template: '<button data-testid="admin-operation-switch" @click="$emit(\'update:modelValue\', !modelValue)" />' },
        SFExtensionSettingsRenderer: adminSettingsRendererStub
      } }
    })
    try {
      await flushPromises()
      expect(wrapper.text()).toContain('Example Login')
      expect(wrapper.text()).not.toContain('GitHub')
      const switches = wrapper.findAll('[data-testid="admin-operation-switch"]')
      await switches[1]!.trigger('click')
      await flushPromises()
      await wrapper.findAll('button').find(button => button.text() === 'admin.loginMethods.runProbe')!.trigger('click')
      await wrapper.get('[data-testid="admin-configure"]').trigger('click')
      await flushPromises()
      expect(requests).toContainEqual(expect.objectContaining({
        path: '/admin/identity/providers/example.provider.auth',
        options: expect.objectContaining({ method: 'PATCH', body: expect.objectContaining({ registrationEnabled: false }) })
      }))
      expect(requests).toContainEqual(expect.objectContaining({ path: '/admin/identity/providers/example.provider.auth/probe' }))
      expect(requests).toContainEqual(expect.objectContaining({ path: '/admin/extensions/example.provider/settings/actions/configure' }))
    } finally {
      wrapper.unmount()
    }
  })
})
