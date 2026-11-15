import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { compileScript, compileTemplate, parse, rewriteDefault } from '@vue/compiler-sfc'
import { installTestDom } from '../helpers/dom'
import { useI18nFor } from '../helpers/locale'

import { buildAuthPageLink, resolveAuthReturnPath } from '../../app/utils/identity/authReturn'
import {
  passwordPolicyProgress,
  passwordPolicyProgressLevel,
  passwordPolicyRequirements,
  type PasswordPolicy
} from '../../app/composables/useWebOptions'

const zhCN = JSON.parse(await Bun.file(new URL('../../i18n/locales/zh-CN.json', import.meta.url)).text())
const enUS = JSON.parse(await Bun.file(new URL('../../i18n/locales/en-US.json', import.meta.url)).text())
const authShellSource = await Bun.file(new URL('../../app/components/identity/auth/SFAuthShell.vue', import.meta.url)).text()

installTestDom({ url: 'http://localhost/login' })
Object.assign(globalThis, {
  useSForumSeo: () => {}
})
const mountRoot = fileURLToPath(new URL('../..', import.meta.url))
const mountVue = await import('vue')
const { mount, flushPromises } = await import('@vue/test-utils')

function vueImportToConst(names: string) {
  return `const {${names.replace(/(\w+) as (\w+)/g, '$1: $2')}} = Vue`
}

async function compileMountedComponent(
  relativePath: string,
  bindings: Record<string, unknown> = {}
) {
  const path = join(mountRoot, relativePath)
  const parsed = parse(await Bun.file(path).text(), { filename: path })
  const script = compileScript(parsed.descriptor, { id: `mount-${relativePath}` })
  const template = compileTemplate({
    source: parsed.descriptor.template?.content || '', filename: path, id: `mount-${relativePath}`,
    compilerOptions: { bindingMetadata: script.bindings }
  })
  if (template.errors.length) throw new Error(String(template.errors[0]))
  const scriptCode = `const { withAsyncContext: _withAsyncContext, defineComponent: _defineComponent } = Vue\n${script.content.replace(/^import[^\n]*\n/gm, '')}`
  const templateCode = template.code
    .replace(/import \{([^}]+)\} from \"vue\"/, (_match, names) => vueImportToConst(names))
    .replace('export function render', 'function render')
  const source = `${rewriteDefault(scriptCode, '__sfc__', ['typescript'])}\n${templateCode}\n__sfc__.render = render\nreturn __sfc__`
  const executable = new Bun.Transpiler({ loader: 'ts', target: 'esnext' }).transformSync(source)
  const names = Object.keys(bindings)
  return new Function('Vue', ...names, executable)(mountVue, ...Object.values(bindings))
}

const SFAuthProviderButtons = await compileMountedComponent('app/components/identity/SFAuthProviderButtons.vue')
const SFAuthShell = mountVue.defineComponent({
  template: '<main data-testid="auth-shell"><slot /></main>'
})
const SFLoginFormPage = await compileMountedComponent(
  'app/components/identity/SFLoginFormPage.vue',
  { SFAuthProviderButtons, SFAuthShell }
)
const SFRegisterFormPage = await compileMountedComponent(
  'app/components/identity/SFRegisterFormPage.vue',
  { SFAuthProviderButtons, SFAuthShell }
)
const SFExternalAuthContinuationPage = await compileMountedComponent(
  'app/components/identity/SFExternalAuthContinuationPage.vue',
  { SFAuthShell }
)

// 与生产一致：后台入口跟随 adminRoutePrefix，默认 /control-panel。
const useAdminRoutesStub = () => ({
  path: (childPath = '/') => childPath === '/' ? '/control-panel' : `/control-panel${childPath}`
})

describe('auth route support contracts', () => {
  test('shares the persisted language menu through the authentication shell', () => {
    expect(authShellSource).toContain("from '~/composables/navigation/useNavbarLanguageMenu'")
    expect(authShellSource).toContain('const { currentLocaleName, languageMenuItems } = useNavbarLanguageMenu()')
    expect(authShellSource).toContain('<UDropdownMenu :items="languageMenuItems"')
    expect(authShellSource).toContain(":aria-label=\"t('nav.language')\"")
    expect(authShellSource).toContain(':title="currentLocaleName"')
    expect(authShellSource).toContain('i-tabler-language')
    expect(authShellSource).toContain('{{ currentLocaleName }}')
    expect(authShellSource).toContain('<UDropdownMenu :items="colorModeMenuItems"')
    expect(authShellSource).toContain('{{ colorModePreferenceLabel }}')
    expect(authShellSource).toContain('i-lucide-chevron-down')
    expect(authShellSource).not.toContain('<UButton')
    expect(authShellSource).toContain('data-ssr-fallback="auth-language"')
    expect(authShellSource).toContain('data-ssr-fallback="auth-appearance"')
    expect(authShellSource).toContain('i-tabler-brightness-filled')
    expect(authShellSource).not.toContain('sf-auth-shell__utility-placeholder')
    expect(authShellSource.match(/aria-hidden="true"[\s\S]*?tabindex="-1"[\s\S]*?data-ssr-fallback="auth-/g)?.length).toBe(2)
    expect(authShellSource).toContain('[data-ssr-fallback] { pointer-events: none; }')
  })

  test('preserves local return targets and rejects auth-page or external redirects', () => {
    expect(buildAuthPageLink('/register', '/topics/123?page=2')).toEqual({
      path: '/register', query: { redirect: '/topics/123?page=2' }
    })
    expect(buildAuthPageLink('/login', '/register')).toBe('/login')
    expect(resolveAuthReturnPath('/login', '/settings/security', '/')).toBe('/settings/security')
    expect(resolveAuthReturnPath('https://evil.example', '', '/')).toBe('/')
  })

  test('evaluates the shared password policy without rendering a parallel form', () => {
    const policy: PasswordPolicy = {
      minLength: 8, maxLength: 64,
      requireLowercase: true, requireUppercase: true, requireNumber: true, requireSymbol: true
    }
    expect(passwordPolicyProgressLevel(passwordPolicyProgress('short', policy))).toBe('weak')
    expect(passwordPolicyProgress('Str0ng-pass!', policy)).toBe(100)
    expect(passwordPolicyRequirements('Str0ng-pass!', policy).every(item => item.met)).toBe(true)
    expect(zhCN.auth.passwordRequirementSymbol).toBeTruthy()
    expect(enUS.auth.passwordRequirementSymbol).toBeTruthy()
  })

  test('mounts the production login form with password fallback and generic provider start', async () => {
    const starts: Array<[string, string]> = []
    const loginRiskAlways = mountVue.ref(false)
    const locale = mountVue.ref('en-US')
    const providers = mountVue.ref([{
      id: 'example.provider.auth', kind: 'auth', contractVersion: 'example.auth@1', priority: 1,
      operations: ['login.start'], activatedOperations: ['login'], label: 'Example Login', icon: 'i-lucide-key-round'
    }, {
      id: 'source.provider.auth', kind: 'auth', contractVersion: 'source.auth@1', priority: 2,
      operations: ['login.start'], activatedOperations: ['login'], label: 'Unlinked Source', icon: 'i-lucide-key-round'
    }])
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch, onMounted: mountVue.onMounted,
      // 真 locale 翻译器：`aria-label` 之类的可读名称断言必须看到用户看到的字。
      useI18n: () => ({ ...useI18nFor('en-US')(), locale }),
      useToast: () => ({ add: () => {} }), useLocalePath: () => (path: string) => path,
      useRoute: () => ({ query: { continuation_provider: 'source.provider.auth' } }),
      useRouter: () => ({ replace: async () => {} }),
      useApiClient: () => ({ apiBaseUrl: 'http://api.test', request: async (path: string) => path === '/auth/registration-status' ? { nextUserIsInitialSuperAdmin: false, registrationEnabled: true } : { id: 1 } }),
      useAuthSession: () => ({ setUser: () => {} }),
      useAuthReturnNavigation: () => ({ returnFromAuth: async () => {}, authPageLink: (path: string) => path, destination: mountVue.ref('/') }),
      useWebOptions: () => ({ siteName: mountVue.ref('SForum'), siteTagline: mountVue.ref(''), altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0 }), loginRiskAlways }),
      useAuthProviders: () => ({ loginProviders: providers, redirectToProvider: async (id: string, operation: string) => { starts.push([id, operation]) } }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorMessage: () => '', apiErrorReason: () => '',
      authProviderDisplayMeta: (provider: { label?: string, icon?: string }, fallback: string) => ({ label: provider.label || fallback, icon: provider.icon || 'i-lucide-key-round' })
    })
    const wrapper = mount({
      components: { SFLoginFormPage },
      template: '<Suspense><SFLoginFormPage /></Suspense>'
    }, {
      global: { stubs: { NuxtLink: { template: '<a><slot /></a>' }, UIcon: true, SFAlert: true, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      expect(wrapper.get('#login-input').exists()).toBe(true)
      expect(wrapper.get('#password-input').exists()).toBe(true)
      // RV-28：登录/注册 tab 组是可读名称的唯一来源，en-US 下必须是英文（曾经写死「登录或注册」）。
      expect(wrapper.get('nav.auth-tabs').attributes('aria-label')).toBe('Log in or sign up')
      const provider = wrapper.get('[data-provider-id="example.provider.auth"]')
      expect(wrapper.find('[data-provider-id="source.provider.auth"]').exists()).toBe(false)
      await provider.trigger('click')
      expect(starts).toEqual([['example.provider.auth', 'login']])
      providers.value = []
      await mountVue.nextTick()
      expect(wrapper.find('[data-testid="auth-provider-buttons"]').exists()).toBe(false)
      expect(wrapper.get('#login-input').exists()).toBe(true)
      // 登录强化「总是验证」：公开选项生效时，进入页面就应渲染 ALTCHA，而不是先吃一次 422。
      loginRiskAlways.value = true
      await mountVue.nextTick()
      expect(wrapper.find('#login-human-verification-label').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('offers existing-account and registration continuations without duplicating either form', async () => {
    const navigations: unknown[] = []
    const user = mountVue.ref(null)
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, onMounted: mountVue.onMounted,
      useRoute: () => ({ query: { ticket: 'opaque-ticket', redirect: '/topics' } }),
      useRouter: () => ({ resolve: (target: { path: string, query: Record<string, string> }) => ({ fullPath: `${target.path}?ticket=${target.query.ticket}&bind=1` }) }),
      useLocalePath: () => (path: string) => path,
      useI18n: () => ({ t: (key: string, values?: Record<string, string>) => values?.name ? `${key}:${values.name}` : key }),
      useToast: () => ({ add: () => {} }),
      useApiClient: () => ({
        request: async (path: string, options?: { body?: Record<string, unknown> }) => {
          if (path !== '/auth/external-continuation/prepare') throw new Error(`unexpected request ${path}`)
          expect(options?.body).toEqual({ ticket: 'opaque-ticket' })
          return {
            providerId: 'source.provider.auth', providerLabel: 'Source Login', providerIcon: 'i-lucide-key-round',
            usernameHint: '', displayName: '', emailHint: '', emailVerified: false,
            canLinkExisting: true, canRegister: true
          }
        }
      }),
      useAuthSession: () => ({ user, status: mountVue.ref('guest'), refresh: async () => null, setUser: () => {} }),
      useAuthReturnNavigation: () => ({ destination: mountVue.ref('/topics'), returnFromAuth: async () => {} }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {},
      navigateTo: async (target: unknown) => { navigations.push(target) },
      apiErrorMessage: () => '', apiErrorReason: () => ''
    })
    const wrapper = mount({ components: { SFExternalAuthContinuationPage }, template: '<Suspense><SFExternalAuthContinuationPage /></Suspense>' }, {
      global: {
        stubs: {
          UIcon: true, SFAlert: true,
          UButton: { template: '<button><slot /></button>' }
        }
      }
    })
    try {
      await flushPromises()
      expect(wrapper.find('#login-input').exists()).toBe(false)
      expect(wrapper.find('#username-input').exists()).toBe(false)
      await wrapper.get('[data-testid="continue-with-existing"]').trigger('click')
      await wrapper.get('[data-testid="continue-with-registration"]').trigger('click')
      expect(navigations).toEqual([
        {
          path: '/login',
          query: {
            redirect: '/auth/continue?ticket=opaque-ticket&bind=1',
            continuation_provider: 'source.provider.auth'
          }
        },
        { path: '/register', query: { ticket: 'opaque-ticket', redirect: '/topics' } }
      ])
    } finally {
      wrapper.unmount()
    }
  })

  test('mounts the production registration form with an independent catalog operation and fallback after start failure', async () => {
    const starts: Array<[string, string]> = []
    const providers = mountVue.ref([{
      id: 'example.provider.auth', kind: 'auth', contractVersion: 'example.auth@1', priority: 1,
      operations: ['registration.start'], activatedOperations: ['registration'], label: 'Example Register', icon: 'i-lucide-key-round'
    }])
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch,
      useI18n: () => ({ ...useI18nFor('en-US')(), locale: mountVue.ref('en-US') }),
      useToast: () => ({ add: () => {} }), useLocalePath: () => (path: string) => path,
      useRoute: () => ({ path: '/register', query: {} }), useRouter: () => ({ replace: async () => {} }),
      useApiClient: () => ({ apiBaseUrl: 'http://api.test', request: async (path: string) => path === '/auth/registration-status' ? { nextUserIsInitialSuperAdmin: false, registrationEnabled: true } : { id: 1 } }),
      useAuthSession: () => ({ setUser: () => {} }),
      useAuthReturnNavigation: () => ({ returnFromAuth: async () => {}, authPageLink: (path: string) => path, destination: mountVue.ref('/') }),
      useAdminRoutes: useAdminRoutesStub,
      useWebOptions: () => ({ siteName: mountVue.ref('SForum'), siteTagline: mountVue.ref(''), webOption: (_name: string, fallback = '') => fallback, humanVerificationEnabledFor: () => false, altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0, type: 'checkbox', auto: 'off', display: 'default', workers: 1 }), passwordPolicy: mountVue.ref({ minLength: 1, maxLength: 64 }) }),
      useAuthProviders: () => ({ registrationProviders: providers, redirectToProvider: async (id: string, operation: string) => { starts.push([id, operation]); throw new Error('provider start failed') } }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorFields: () => ({}), apiErrorMessage: (error: Error) => error.message, apiErrorReason: () => '',
      registerErrorMessage: () => '', passwordPolicyProgress: () => 100, passwordPolicyProgressLevel: () => 'strong', passwordPolicyRequirements: () => [],
      authProviderDisplayMeta: (provider: { label?: string, icon?: string }, fallback: string) => ({ label: provider.label || fallback, icon: provider.icon || 'i-lucide-key-round' })
    })
    const wrapper = mount({ components: { SFRegisterFormPage }, template: '<Suspense><SFRegisterFormPage /></Suspense>' }, {
      global: { stubs: { NuxtLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }, UIcon: true, SFAlert: { props: ['title'], template: '<div>{{ title }}<slot /></div>' }, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      expect(wrapper.get('#reg-password-input').exists()).toBe(true)
      expect(wrapper.get('nav.auth-tabs').attributes('aria-label')).toBe('Log in or sign up')
      await wrapper.get('[data-provider-id="example.provider.auth"]').trigger('click')
      await flushPromises()
      expect(starts).toEqual([['example.provider.auth', 'registration']])
      expect(wrapper.text()).toContain('provider start failed')
      providers.value = []
      await mountVue.nextTick()
      expect(wrapper.find('[data-testid="auth-provider-buttons"]').exists()).toBe(false)
      expect(wrapper.get('#reg-password-input').exists()).toBe(true)
      const legalLinks = wrapper.findAll('.auth-terms a')
      expect(legalLinks).toHaveLength(2)
      expect(legalLinks.map(link => link.attributes('href'))).toEqual(['/terms', '/privacy'])
    } finally {
      wrapper.unmount()
    }
  })

  test('redirects an unverified registration to the email verification waiting page', async () => {
    const navigations: unknown[] = []
    let returnedFromAuth = false
    let sessionUser: { emailVerified?: boolean } | null = null
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch,
      useI18n: () => ({ ...useI18nFor('zh-CN')(), locale: mountVue.ref('zh-CN') }),
      useToast: () => ({ add: () => {} }), useLocalePath: () => (path: string) => path,
      useRoute: () => ({ path: '/register', query: {} }),
      useRouter: () => ({ replace: async (target: unknown) => { navigations.push(target) } }),
      useApiClient: () => ({
        apiBaseUrl: 'http://api.test',
        request: async (path: string) => {
          if (path === '/auth/registration-status') return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
          if (path === '/auth/register') return { id: 42, username: 'new-user', emailVerified: false }
          throw new Error(`unexpected request ${path}`)
        }
      }),
      useAuthSession: () => ({ setUser: (user: { emailVerified?: boolean }) => { sessionUser = user } }),
      useAuthReturnNavigation: () => ({
        returnFromAuth: async () => { returnedFromAuth = true },
        authPageLink: (path: string) => path,
        destination: mountVue.ref('/topics')
      }),
      useAdminRoutes: useAdminRoutesStub,
      useWebOptions: () => ({
        siteName: mountVue.ref('SForum'), siteTagline: mountVue.ref(''),
        webOption: (name: string, fallback = '') => name === 'identity.registration.require_email_verification' ? 'enabled' : fallback,
        humanVerificationEnabledFor: () => false,
        altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0, type: 'checkbox', auto: 'off', display: 'default', workers: 1 }),
        passwordPolicy: mountVue.ref({ minLength: 1, maxLength: 64 })
      }),
      useAuthProviders: () => ({ registrationProviders: mountVue.ref([]), redirectToProvider: async () => {} }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorFields: () => ({}), apiErrorMessage: () => '', apiErrorReason: () => '',
      registerErrorMessage: () => '', passwordPolicyProgress: () => 100, passwordPolicyProgressLevel: () => 'strong', passwordPolicyRequirements: () => []
    })
    const wrapper = mount({ components: { SFRegisterFormPage }, template: '<Suspense><SFRegisterFormPage /></Suspense>' }, {
      global: { stubs: { NuxtLink: { template: '<a><slot /></a>' }, UIcon: true, SFAlert: true, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      expect(wrapper.get('nav.auth-tabs').attributes('aria-label')).toBe('登录或注册')
      await wrapper.get('#username-input').setValue('new-user')
      await wrapper.get('#email-input').setValue('new-user@example.com')
      await wrapper.get('#displayname-input').setValue('New User')
      await wrapper.get('#reg-password-input').setValue('password')
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(sessionUser?.emailVerified).toBe(false)
      expect(returnedFromAuth).toBe(false)
      expect(navigations).toEqual([{
        path: '/email-verification',
        query: { redirect: '/topics' }
      }])
    } finally {
      wrapper.unmount()
    }
  })

  test('reuses the registration form and autofills only verified external hints', async () => {
    const submitted: Array<{ path: string, body: Record<string, unknown> }> = []
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch,
      useI18n: () => ({ locale: mountVue.ref('en-US'), t: (key: string) => key }),
      useToast: () => ({ add: () => {} }), useLocalePath: () => (path: string) => path,
      useRoute: () => ({ path: '/register', query: { ticket: 'opaque-ticket', redirect: '/topics' } }),
      useRouter: () => ({ replace: async () => {} }),
      useApiClient: () => ({
        apiBaseUrl: 'http://api.test',
        request: async (path: string, options?: { body?: Record<string, unknown> }) => {
          if (path === '/auth/external-registration/prepare') {
            expect(options?.body).toEqual({ ticket: 'opaque-ticket' })
            return { usernameHint: 'octocat', emailHint: 'octocat@example.com', displayName: 'The Octocat', emailVerified: true }
          }
          if (path === '/auth/registration-status') {
            return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
          }
          if (path === '/auth/external-registration') {
            submitted.push({ path, body: options?.body || {} })
            return { id: 42, username: 'octocat' }
          }
          throw new Error(`unexpected request ${path}`)
        }
      }),
      useAuthSession: () => ({ setUser: () => {} }),
      useAuthReturnNavigation: () => ({ returnFromAuth: async () => {}, authPageLink: (path: string) => path, destination: mountVue.ref('/topics') }),
      useAdminRoutes: useAdminRoutesStub,
      useWebOptions: () => ({ webOption: (_name: string, fallback = '') => fallback, humanVerificationEnabledFor: () => false, altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0, type: 'checkbox', auto: 'off', display: 'default', workers: 1 }), passwordPolicy: mountVue.ref({ minLength: 1, maxLength: 64 }) }),
      useAuthProviders: () => ({ registrationProviders: mountVue.ref([]), redirectToProvider: async () => {} }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorFields: () => ({}), apiErrorMessage: () => '', apiErrorReason: () => '',
      registerErrorMessage: () => '', passwordPolicyProgress: () => 100, passwordPolicyProgressLevel: () => 'strong', passwordPolicyRequirements: () => []
    })
    const wrapper = mount({ components: { SFRegisterFormPage }, template: '<Suspense><SFRegisterFormPage /></Suspense>' }, {
      global: { stubs: { NuxtLink: { template: '<a><slot /></a>' }, UIcon: true, SFAlert: { props: ['title'], template: '<div>{{ title }}<slot /></div>' }, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      expect((wrapper.get('#username-input').element as HTMLInputElement).value).toBe('octocat')
      expect((wrapper.get('#email-input').element as HTMLInputElement).value).toBe('octocat@example.com')
      expect((wrapper.get('#displayname-input').element as HTMLInputElement).value).toBe('The Octocat')
      expect(wrapper.find('#reg-password-input').exists()).toBe(false)
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(submitted).toHaveLength(1)
      expect(submitted[0]?.body).toMatchObject({
        ticket: 'opaque-ticket', username: 'octocat', email: 'octocat@example.com', displayName: 'The Octocat'
      })
    } finally {
      wrapper.unmount()
    }
  })

  test('announces super administrator access only to the registration that actually received it', async () => {
    const toasts: Array<Record<string, unknown>> = []
    const sessionUsers: Array<{ isInitialSuperAdmin?: boolean }> = []
    const landingOverrides: Array<string | undefined> = []
    let returnedFromAuth = false
    const registerPayload = { id: 7, username: 'first-user', emailVerified: true, isInitialSuperAdmin: true }
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch,
      useI18n: () => ({ locale: mountVue.ref('zh-CN'), t: (key: string) => key }),
      useToast: () => ({ add: (toast: Record<string, unknown>) => toasts.push(toast) }),
      useLocalePath: () => (path: string) => path,
      useRoute: () => ({ path: '/register', query: {} }),
      useRouter: () => ({ replace: async () => {} }),
      useApiClient: () => ({
        apiBaseUrl: 'http://api.test',
        request: async (path: string) => {
          if (path === '/auth/registration-status') return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
          if (path === '/auth/register') return registerPayload
          throw new Error(`unexpected request ${path}`)
        }
      }),
      useAuthSession: () => ({ setUser: (user: { isInitialSuperAdmin?: boolean }) => { sessionUsers.push(user) } }),
      useAuthReturnNavigation: () => ({
        returnFromAuth: async (overrideDestination?: string) => {
          returnedFromAuth = true
          landingOverrides.push(overrideDestination)
        },
        authPageLink: (path: string) => path,
        destination: mountVue.ref('/')
      }),
      useAdminRoutes: useAdminRoutesStub,
      useWebOptions: () => ({
        siteName: mountVue.ref('SForum'), siteTagline: mountVue.ref(''),
        webOption: (_name: string, fallback = '') => fallback,
        humanVerificationEnabledFor: () => false,
        altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0, type: 'checkbox', auto: 'off', display: 'default', workers: 1 }),
        passwordPolicy: mountVue.ref({ minLength: 1, maxLength: 64 })
      }),
      useAuthProviders: () => ({ registrationProviders: mountVue.ref([]), redirectToProvider: async () => {} }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorFields: () => ({}), apiErrorMessage: () => '', apiErrorReason: () => '',
      registerErrorMessage: () => '', passwordPolicyProgress: () => 100, passwordPolicyProgressLevel: () => 'strong', passwordPolicyRequirements: () => []
    })
    const wrapper = mount({ components: { SFRegisterFormPage }, template: '<Suspense><SFRegisterFormPage /></Suspense>' }, {
      global: { stubs: { NuxtLink: { template: '<a><slot /></a>' }, UIcon: true, SFAlert: true, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      await wrapper.get('#username-input').setValue('first-user')
      await wrapper.get('#email-input').setValue('first@example.com')
      await wrapper.get('#displayname-input').setValue('First User')
      await wrapper.get('#reg-password-input').setValue('password')
      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(sessionUsers).toHaveLength(1)
      expect(returnedFromAuth).toBe(true)
      // 首注册者直接落到后台概览，而不是常规认证回跳目标（此处为 '/'）。
      expect(landingOverrides).toEqual(['/control-panel'])
      expect(toasts.map(toast => toast.title)).toContain('auth.initialSuperAdminGranted')

      toasts.length = 0
      registerPayload.isInitialSuperAdmin = false
      await wrapper.get('form').trigger('submit')
      await flushPromises()
      expect(toasts.map(toast => toast.title)).not.toContain('auth.initialSuperAdminGranted')
      expect(landingOverrides[1]).toBe('/')
    } finally {
      wrapper.unmount()
    }
  })

  test('routes the first administrator through email verification back to the admin panel', async () => {
    const navigations: unknown[] = []
    let returnedFromAuth = false
    Object.assign(globalThis, {
      computed: mountVue.computed, ref: mountVue.ref, reactive: mountVue.reactive, watch: mountVue.watch,
      useI18n: () => ({ locale: mountVue.ref('zh-CN'), t: (key: string) => key }),
      useToast: () => ({ add: () => {} }), useLocalePath: () => (path: string) => path,
      useRoute: () => ({ path: '/register', query: {} }),
      useRouter: () => ({ replace: async (target: unknown) => { navigations.push(target) } }),
      useApiClient: () => ({
        apiBaseUrl: 'http://api.test',
        request: async (path: string) => {
          if (path === '/auth/registration-status') return { nextUserIsInitialSuperAdmin: false, registrationEnabled: true }
          if (path === '/auth/register') {
            return { id: 9, username: 'first-user', emailVerified: false, isInitialSuperAdmin: true }
          }
          throw new Error(`unexpected request ${path}`)
        }
      }),
      useAuthSession: () => ({ setUser: () => {} }),
      useAuthReturnNavigation: () => ({
        returnFromAuth: async () => { returnedFromAuth = true },
        authPageLink: (path: string) => path,
        destination: mountVue.ref('/topics')
      }),
      useAdminRoutes: useAdminRoutesStub,
      useWebOptions: () => ({
        siteName: mountVue.ref('SForum'), siteTagline: mountVue.ref(''),
        webOption: (name: string, fallback = '') => name === 'identity.registration.require_email_verification' ? 'enabled' : fallback,
        humanVerificationEnabledFor: () => false,
        altchaWidgetSettings: mountVue.ref({ hideLogo: true, hideFooter: true, minDuration: 0, type: 'checkbox', auto: 'off', display: 'default', workers: 1 }),
        passwordPolicy: mountVue.ref({ minLength: 1, maxLength: 64 })
      }),
      useAuthProviders: () => ({ registrationProviders: mountVue.ref([]), redirectToProvider: async () => {} }),
      useExternalAuthFeedback: () => ({ alertMessage: mountVue.ref(''), alertVariant: mountVue.ref('danger') }),
      useAsyncData: async (_key: string, handler: () => Promise<unknown>) => ({ data: mountVue.ref(await handler()), pending: mountVue.ref(false) }),
      useSeoMeta: () => {}, apiErrorFields: () => ({}), apiErrorMessage: () => '', apiErrorReason: () => '',
      registerErrorMessage: () => '', passwordPolicyProgress: () => 100, passwordPolicyProgressLevel: () => 'strong', passwordPolicyRequirements: () => []
    })
    const wrapper = mount({ components: { SFRegisterFormPage }, template: '<Suspense><SFRegisterFormPage /></Suspense>' }, {
      global: { stubs: { NuxtLink: { template: '<a><slot /></a>' }, UIcon: true, SFAlert: true, ClientOnly: true, 'altcha-widget': true, SFAuthProviderButtons } }
    })
    try {
      await flushPromises()
      await wrapper.get('#username-input').setValue('first-user')
      await wrapper.get('#email-input').setValue('first@example.com')
      await wrapper.get('#displayname-input').setValue('First User')
      await wrapper.get('#reg-password-input').setValue('password')
      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(returnedFromAuth).toBe(false)
      // 验证前置仍然生效，但验证后的落点是后台，而不是 referrer（'/topics'）。
      expect(navigations).toEqual([{
        path: '/email-verification',
        query: { redirect: '/control-panel' }
      }])
    } finally {
      wrapper.unmount()
    }
  })

  test('no longer previews the bootstrap super administrator window to unauthenticated visitors', async () => {
    expect(zhCN.auth.initialSuperAdminGranted).toBeTruthy()
    expect(enUS.auth.initialSuperAdminGranted).toBeTruthy()
    expect(zhCN.auth.firstUserAdminNotice).toBeUndefined()
    expect(enUS.auth.firstUserAdminNotice).toBeUndefined()

    const registerFormSource = await Bun.file(
      new URL('../../app/components/identity/SFRegisterFormPage.vue', import.meta.url)
    ).text()
    expect(registerFormSource).not.toContain('firstUserAdminNotice')
    expect(registerFormSource).not.toContain('isBootstrapRegistration')
    expect(registerFormSource).toContain("t('auth.initialSuperAdminGranted')")
  })
})
