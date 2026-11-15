import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * 两处「页签状态与地址栏脱钩」的收口：
 *
 * - UX2-15（本轮 12 条之一）：登录方式页自建 `role="tablist"`，选中项只在内存里，
 *   F5 回落到第一个提供方、URL 分享不出去。现在 `?tab=<providerId>` 双向同步，
 *   未知 id 回落首个可用提供方。
 * - 连带项（Lead 裁决，不计入 12 条）：`entity-meta.vue` 的筛选页签同理，
 *   同步 `?entity=user|topic`，非法值回落 `all`。
 */

const globals = globalThis as Record<string, unknown>
const saved = { useI18n: globals.useI18n, useToast: globals.useToast }
afterAll(() => {
  globals.useI18n = saved.useI18n
  globals.useToast = saved.useToast
})
globals.useI18n = () => ({ t: (key: string) => key })
globals.useToast = () => ({ add: () => {} })

const { apiErrorMessage, apiErrorStatusCode } = await import('../../app/composables/useApiClient')

type ReplaceCall = { query: Record<string, unknown> }

const IconStub = { props: ['name'], template: '<i />' }
const ToolbarStub = { template: '<div class="toolbar"><slot name="left" /><slot name="right" /></div>' }
const ButtonStub = {
  props: ['color', 'variant', 'size', 'icon', 'leadingIcon', 'disabled', 'loading', 'block'],
  emits: ['click'],
  template: '<button type="button" :data-color="color" @click="$emit(\'click\')"><slot /></button>'
}
const AlertStub = {
  props: { variant: { type: String, default: '' }, title: { type: String, default: '' }, description: { type: String, default: '' }, closable: { type: Boolean, default: false }, color: { type: String, default: '' } },
  template: '<div class="alert">{{ title }}</div>'
}
const SwitchStub = { props: ['modelValue', 'ariaLabel', 'disabled'], template: '<button type="button" role="switch" :aria-checked="String(Boolean(modelValue))" />' }
const EmptyStub = { props: ['title', 'description'], template: '<div class="empty" />' }
const RendererStub = { template: '<div class="renderer" />' }

function makeProvider(id: string, label: string) {
  return {
    id,
    kind: 'oauth',
    priority: 10,
    operations: ['login', 'registration', 'link'],
    ownerExtensionId: '',
    ownerPackageDigest: '',
    discovered: true,
    trusted: true,
    enabled: true,
    configured: true,
    probed: true,
    artifactBound: true,
    activated: true,
    publiclyActivated: true,
    loginEnabled: true,
    registrationEnabled: false,
    linkEnabled: true,
    revision: 3,
    callbackPath: `/auth/${id}/callback`,
    callbackUrl: `https://forum.example.com/auth/${id}/callback`,
    safeMode: false,
    label
  }
}

let providers: ReturnType<typeof makeProvider>[] = []
let routeQuery: Record<string, unknown> = {}
let replaceCalls: ReplaceCall[] = []

const request = async (path: string) => {
  if (path.includes('/admin/identity/providers')) return providers
  return {}
}

const FixedTabNav = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/admin/settings/shared/SFAdminFixedTabNav.vue', import.meta.url)),
  'sf-admin-fixed-tab-nav'
)

const LoginMethodsPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/settings/login-methods.vue', import.meta.url)),
  'admin-login-methods-page',
  {
    useAdminRoutes: () => ({ path: (path: string) => path }),
    apiErrorMessage,
    apiErrorStatusCode,
    useAdminPage: () => ({ id: '/settings/login-methods', icon: 'i-lucide-log-in', labelKey: 'admin.nav.loginMethods' }),
    recommendedExtensionSettingValues: () => ({}),
    SFExtensionSettingsRenderer: RendererStub,
    SFAdminFixedTabNav: FixedTabNav,
    adminProbeFeedback: () => '',
    adminProbeLabelKind: () => 'never',
    adminProviderIcon: () => 'i-lucide-key',
    adminProviderShortDigest: (digest: string) => digest,
    adminProviderStateBadges: () => [],
    adminProviderSupportsOp: () => true,
    adminProviderTitle: (item: { label?: string, id: string }) => item.label || item.id,
    useApiClient: () => ({ request }),
    useI18n: () => ({ t: (key: string) => key, locale: testVue.ref('zh-CN') }),
    useRoute: () => ({ query: routeQuery }),
    useRouter: () => ({ replace: async (arg: ReplaceCall) => { replaceCalls.push(arg) } }),
    useSeoMeta: () => {},
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch
  }
)

const LoginHarness = testVue.defineComponent({
  components: { LoginMethodsPage },
  template: '<Suspense><LoginMethodsPage /></Suspense>'
})

function mountLoginMethods() {
  return mount(LoginHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        UAlert: AlertStub,
        USwitch: SwitchStub,
        SFAlert: AlertStub,
        SFEmptyState: EmptyStub
      }
    }
  })
}

function tabButtons(wrapper: ReturnType<typeof mountLoginMethods>) {
  return wrapper.findAll('[role="tab"]')
}

describe('UX2-15 登录方式页签与 ?tab= 同步', () => {
  test('深链 ?tab=gitlab：刷新后仍停在该提供方', async () => {
    providers = [makeProvider('github', 'GitHub'), makeProvider('gitlab', 'GitLab')]
    routeQuery = { tab: 'gitlab' }
    replaceCalls = []
    const wrapper = mountLoginMethods()
    try {
      await flushPromises()
      const tabs = tabButtons(wrapper)
      expect(tabs.map(tab => tab.attributes('aria-selected'))).toEqual(['false', 'true'])
      expect(wrapper.get('#login-methods-panel').attributes('aria-labelledby')).toBe('login-methods-panel-tab-gitlab')
      // 已经是目标状态：不产生多余的导航。
      expect(replaceCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('a11y 闭环：每个 tab 的 aria-controls 指向存在的 panel，panel 指回激活 tab', async () => {
    providers = [makeProvider('github', 'GitHub'), makeProvider('gitlab', 'GitLab')]
    routeQuery = { tab: 'github' }
    replaceCalls = []
    const wrapper = mountLoginMethods()
    try {
      await flushPromises()
      const panel = wrapper.get('#login-methods-panel')
      expect(panel.attributes('role')).toBe('tabpanel')

      const tabs = tabButtons(wrapper)
      expect(tabs).toHaveLength(2)
      for (const tab of tabs) {
        expect(tab.attributes('aria-controls')).toBe('login-methods-panel')
        expect(tab.attributes('id')).toMatch(/^login-methods-panel-tab-/)
        // aria-controls 指向的节点必须真的存在（不是悬空引用）。
        expect(wrapper.find(`#${tab.attributes('id')}`).exists()).toBe(true)
      }
      const active = tabs.find(tab => tab.attributes('aria-selected') === 'true')
      expect(panel.attributes('aria-labelledby')).toBe(active?.attributes('id'))
      // 页面上只有共享页签条这一套 tablist（不再自建第二套）。
      expect(wrapper.findAll('[role="tablist"]')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('未知 id 回落首个提供方，并把地址栏纠正过来', async () => {
    providers = [makeProvider('github', 'GitHub'), makeProvider('gitlab', 'GitLab')]
    routeQuery = { tab: 'saml-not-installed' }
    replaceCalls = []
    const wrapper = mountLoginMethods()
    try {
      await flushPromises()
      expect(tabButtons(wrapper)[0]?.attributes('aria-selected')).toBe('true')
      expect(replaceCalls.at(-1)?.query).toMatchObject({ tab: 'github' })
    } finally {
      wrapper.unmount()
    }
  })

  test('点击另一个提供方：router.replace 写入 ?tab=，不新增历史', async () => {
    providers = [makeProvider('github', 'GitHub'), makeProvider('gitlab', 'GitLab')]
    routeQuery = {}
    replaceCalls = []
    const wrapper = mountLoginMethods()
    try {
      await flushPromises()
      const tabs = tabButtons(wrapper)
      await tabs[1]!.trigger('click')
      await flushPromises()

      expect(replaceCalls.at(-1)?.query).toMatchObject({ tab: 'gitlab' })
      expect(tabButtons(wrapper)[1]?.attributes('aria-selected')).toBe('true')
      expect(wrapper.get('#login-methods-panel').attributes('aria-labelledby')).toBe('login-methods-panel-tab-gitlab')
    } finally {
      wrapper.unmount()
    }
  })
})

// ---------------------------------------------------------------------------
// 连带项：entity-meta 的 `?entity=`。
// ---------------------------------------------------------------------------

const FieldStub = { template: '<label><slot /></label>' }
const CardStub = { template: '<section><slot name="header" /><slot /><slot name="footer" /></section>' }
const BadgeStub = { props: ['color', 'variant'], template: '<span><slot /></span>' }
const InputStub = { props: ['modelValue'], template: '<input />' }
const CheckboxStub = { props: ['modelValue'], template: '<input type="checkbox" />' }
const FooterStub = { props: ['saving', 'submitText', 'resetText'], emits: ['submit', 'reset'], template: '<div class="footer"><slot name="actions" /></div>' }
const NativeSelectStub = { props: ['modelValue', 'options'], template: '<select />' }

let entityRouteQuery: Record<string, unknown> = {}
let entityReplaceCalls: ReplaceCall[] = []

const EntityMetaPage = await compileVueSfc(
  fileURLToPath(new URL('../../app/pages/admin/entity-meta.vue', import.meta.url)),
  'admin-entity-meta-page',
  {
    apiErrorMessage,
    useAdminPage: () => ({ id: '/entity-meta', icon: 'i-lucide-shapes', labelKey: 'admin.nav.entityMeta' }),
    useAuthSession: () => ({ can: () => true }),
    SFAdminFormFooter: FooterStub,
    SFNativeSelect: NativeSelectStub,
    useI18n: () => ({ t: (key: string) => key, te: () => true, locale: testVue.ref('zh-CN') }),
    useToast: () => ({ add: () => {} }),
    useApiClient: () => ({ request: async () => ({ items: [] }) }),
    useRoute: () => ({ query: entityRouteQuery }),
    useRouter: () => ({ replace: async (arg: ReplaceCall) => { entityReplaceCalls.push(arg) } }),
    useSeoMeta: () => {},
    ref: testVue.ref,
    reactive: testVue.reactive,
    computed: testVue.computed,
    watch: testVue.watch,
    onMounted: testVue.onMounted
  }
)

const EntityHarness = testVue.defineComponent({
  components: { EntityMetaPage },
  template: '<Suspense><EntityMetaPage /></Suspense>'
})

function mountEntityMeta() {
  return mount(EntityHarness, {
    global: {
      stubs: {
        UIcon: IconStub,
        UDashboardToolbar: ToolbarStub,
        UButton: ButtonStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UCard: CardStub,
        UFormField: FieldStub,
        UInput: InputStub,
        UCheckbox: CheckboxStub
      }
    }
  })
}

describe('连带项 entity-meta 的 ?entity= 同步', () => {
  test('深链 ?entity=topic：刷新后仍筛在「主题」', async () => {
    entityRouteQuery = { entity: 'topic' }
    entityReplaceCalls = []
    const wrapper = mountEntityMeta()
    try {
      await flushPromises()
      const tabs = wrapper.findAll('[role="tab"]')
      expect(tabs.map(tab => tab.attributes('aria-selected'))).toEqual(['false', 'false', 'true'])
    } finally {
      wrapper.unmount()
    }
  })

  test('点击页签写回 ?entity=；选「全部」时移除该参数', async () => {
    entityRouteQuery = { entity: 'topic' }
    entityReplaceCalls = []
    const wrapper = mountEntityMeta()
    try {
      await flushPromises()
      const tabs = wrapper.findAll('[role="tab"]')
      await tabs[1]!.trigger('click')
      await flushPromises()
      expect(entityReplaceCalls.at(-1)?.query).toMatchObject({ entity: 'user' })

      await tabs[0]!.trigger('click')
      await flushPromises()
      expect(entityReplaceCalls.at(-1)?.query.entity).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})
