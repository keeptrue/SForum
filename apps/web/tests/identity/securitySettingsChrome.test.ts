import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const page = () => source('../../app/components/settings/SFSecuritySettingsPage.vue')
const loginMethodsPage = () => source('../../app/components/settings/SFLoginMethodsSettingsPage.vue')
const tokensPage = () => source('../../app/components/settings/SFPersonalAccessTokensPage.vue')
const shell = () => source('../../app/components/settings/SFSettingsShell.vue')
const accountSidebar = () => source('../../app/components/settings/SFSettingsAccountSidebar.vue')
const route = () => source('../../app/pages/settings/security/index.vue')
const loginMethodsRoute = () => source('../../app/pages/settings/security/login-methods.vue')
const tokensRoute = () => source('../../app/pages/settings/tokens.vue')
const themeTemplate = () => source('../../../../extensions/builtin/themes/sforum-default/templates/settings-security.html')
const loginMethodsThemeTemplate = () => source('../../../../extensions/builtin/themes/sforum-default/templates/settings-login-methods.html')
const tokensThemeTemplate = () => source('../../../../extensions/builtin/themes/sforum-default/templates/settings-tokens.html')

describe('security settings chrome', () => {
  test('keeps Page Registry ownership boundaries intact', () => {
    expect(route()).toContain('SFPageOutlet')
    expect(route()).toContain('page="forum.settings.security"')
    expect(route()).toContain('<SFSecuritySettingsPage')
    expect(themeTemplate()).toContain('data-theme-owned="presentation"')
    expect(themeTemplate()).toContain('data-page="forum.settings.security"')
    expect(themeTemplate()).toContain('<sf-security-settings')
  })

  test('keeps login methods as a dedicated Page Registry surface', () => {
    expect(loginMethodsRoute()).toContain('SFPageOutlet')
    expect(loginMethodsRoute()).toContain('page="forum.settings.login_methods"')
    expect(loginMethodsRoute()).toContain('<SFLoginMethodsSettingsPage')
    expect(loginMethodsThemeTemplate()).toContain('data-theme-owned="presentation"')
    expect(loginMethodsThemeTemplate()).toContain('data-page="forum.settings.login_methods"')
    expect(loginMethodsThemeTemplate()).toContain('<sf-login-methods-settings')

    const src = loginMethodsPage()
    expect(src).toContain('<SFSettingsShell')
    // 账号安全整合后，三个 tab 页共享同一侧栏高亮项。
    expect(src).toContain('active="security"')
    expect(src).toContain('class="sforum-settings-login-methods"')
    expect(src).toContain('identity.component.login_methods_settings')
    expect(src).toContain('return-path="/settings/security/login-methods"')
  })

  test('keeps personal access tokens as a dedicated Page Registry surface with selected scopes', () => {
    expect(tokensRoute()).toContain('SFPageOutlet')
    expect(tokensRoute()).toContain('page="forum.settings.tokens"')
    expect(tokensRoute()).toContain('<SFPersonalAccessTokensPage')
    expect(tokensThemeTemplate()).toContain('data-theme-owned="presentation"')
    expect(tokensThemeTemplate()).toContain('data-page="forum.settings.tokens"')
    expect(tokensThemeTemplate()).toContain('<sf-personal-access-tokens')

    const src = tokensPage()
    expect(src).toContain('<SFSettingsShell')
    expect(src).toContain('active="tokens"')
    expect(src).toContain('identity.component.personal_access_tokens')
    expect(src).toContain('TOKEN_SCOPE_PRESETS')
    expect(src).toContain('<SFTabs')
    expect(src).toContain("activeTokenTab === 'create'")
    expect(src).toContain('personal-access-token-manage-tab')
    expect(src).toContain('type="checkbox"')
    expect(src).toContain('v-model="form.scopes"')
    expect(src).not.toContain('scopesText')
  })

  test('shares the public three-column settings chrome via SFSettingsShell', () => {
    const src = page()
    expect(src).toContain('<SFSettingsShell')
    expect(src).toContain('active="security"')
    expect(src).toContain('class="sforum-settings-security"')
    expect(src).toContain('accountSecurity.rail.devicesTitle')
    expect(src).toContain('listSessions()')
    expect(src).not.toContain('listAPITokens()')
    expect(src).not.toContain('scopesText')
    expect(src).not.toContain('<SFLinkedAccountsSection')
    expect(src).not.toContain('sf-public-page__container--narrow')

    const shellSrc = shell()
    const sidebarSrc = accountSidebar()
    expect(shellSrc).toContain('data-layout="fullwidth-3col"')
    expect(shellSrc).toContain('sforum-settings__layout')
    expect(shellSrc).toContain('<SFSettingsAccountSidebar')
    // 左栏（站点导航 + 账号导航 + 抽屉所有者声明）由侧栏组件独占，
    // 插件公开页的 sf-account-settings-sidebar 岛复用同一份。
    expect(sidebarSrc).toContain('<SFHomeNavigation')
    expect(sidebarSrc).toContain('navigation-mode="route"')
    expect(sidebarSrc).toContain(':show-categories="false"')
    expect(sidebarSrc).toContain('<SFSettingsAccountNav')
    expect(sidebarSrc).toContain('<SFResponsivePublicSidebar')
    expect(sidebarSrc).toContain('owner-id="forum.settings"')
    expect(shellSrc).not.toContain('<SFSettingsAccountNav')
    expect(shellSrc).toContain('usePublicSidebarDrawer()')
    expect(shellSrc).not.toContain("useState<boolean>('forum-mobile-menu-open'")
    expect(shellSrc).toContain("useState<boolean>('forum-mobile-info-open'")
  })

  test('shows login history by default with server-side pagination', () => {
    const src = page()

    expect(src).toContain('const HISTORY_PAGE_SIZE = 10')
    expect(src).toContain('includeHistory: true, page: historyPage.value, perPage: HISTORY_PAGE_SIZE')
    expect(src).toContain('<SFPagination')
    expect(src).toContain('accountSecurity.showHistory')
    expect(src).not.toContain('showHistory = ref(false)')
    expect(src).not.toContain('@click="loadHistory"')
    expect(src).not.toContain('perPage: 50')
  })
})

describe('consolidated account security tabs', () => {
  const tabNav = () => source('../../app/components/settings/SFSettingsSecurityTabNav.vue')
  const passwordPage = () => source('../../app/components/settings/SFLocalPasswordSettingsPage.vue')
  const accountNav = () => source('../../app/components/settings/SFSettingsAccountNav.vue')
  const nuxtConfig = () => source('../../nuxt.config.ts')
  const zhCN = JSON.parse(source('../../i18n/locales/zh-CN.json'))
  const enUS = JSON.parse(source('../../i18n/locales/en-US.json'))

  test('renders one shared route-driven tab nav across the three security islands', () => {
    const src = tabNav()
    expect(src).toContain("localePath('/settings/security')")
    expect(src).toContain("localePath('/settings/security/login-methods')")
    expect(src).toContain("localePath('/settings/security/password')")
    expect(src).toContain('route.path')
    // 呈现与无障碍语义由共享 tab 栏独占（资料设置页同一份实现）。
    expect(src).toContain('<SFSettingsTabNav')
    const sharedNav = source('../../app/components/settings/SFSettingsTabNav.vue')
    expect(sharedNav).toContain(":aria-current=\"isActive(tab.id) ? 'page' : undefined\"")
    expect(sharedNav).toContain('sforum-settings__tabs')
    for (const [island, name] of [[page(), 'security'], [loginMethodsPage(), 'login-methods'], [passwordPage(), 'password']] as const) {
      expect(island, name).toContain('<SFSettingsSecurityTabNav')
      // 合并后的侧栏高亮：三个 tab 页共用单一「账号安全」项。
      expect(island, name).toContain('active="security"')
      expect(island, name).not.toContain('active="loginMethods"')
      expect(island, name).not.toContain('active="password"')
    }
  })

  test('tab labels resolve in both locales', () => {
    for (const locale of [zhCN, enUS]) {
      const tabs = locale.accountSecurity.tabs
      expect(tabs.devices).toBeTruthy()
      expect(tabs.loginMethods).toBeTruthy()
      expect(tabs.password).toBeTruthy()
      expect(tabs.navAriaLabel).toBeTruthy()
    }
  })

  test('sidebar collapses login methods and password into one account security entry', () => {
    const src = accountNav()
    expect(src).toContain("localePath('/settings/security')")
    expect(src).toContain("active === 'security'")
    expect(src).not.toContain("localePath('/settings/login-methods')")
    expect(src).not.toContain("localePath('/settings/password')")
    expect(src).not.toContain("active === 'loginMethods'")
    expect(src).not.toContain("active === 'password'")
    // 旧路径不得再出现在侧栏与 tab 导航。
    expect(src).not.toContain("'/settings/login-methods'")
    expect(src).not.toContain("'/settings/password'")
    expect(tabNav()).not.toContain("'/settings/login-methods'")
    expect(tabNav()).not.toContain("'/settings/password'")
  })

  test('legacy flat paths redirect into the security subtree', () => {
    const src = nuxtConfig()
    expect(src).toContain("'/settings/login-methods': { redirect: { to: '/settings/security/login-methods', statusCode: 302 } }")
    expect(src).toContain("'/settings/password': { redirect: { to: '/settings/security/password', statusCode: 302 } }")
  })

  test('internal return destinations follow the new subtree', () => {
    expect(loginMethodsPage()).toContain("localePath('/settings/security/password')")
    expect(passwordPage()).toContain("'/settings/security/password')")
    expect(source('../../app/components/identity/SFLinkedAccountsSection.vue')).toContain("returnPath: '/settings/security/login-methods'")
  })
})
