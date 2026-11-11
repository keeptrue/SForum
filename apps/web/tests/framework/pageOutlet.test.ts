import { describe, expect, it } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const root = join(import.meta.dir, '../..')

function read(rel: string) {
  return readFileSync(join(root, rel), 'utf8')
}

describe('SFPageOutlet catalog wiring', () => {
  const catalogPages: Array<[string, string]> = [
    ['app/pages/index.vue', 'forum.home'],
    ['app/pages/search.vue', 'forum.search'],
    ['app/pages/categories/index.vue', 'forum.category.index'],
    ['app/pages/c/[categorySlug].vue', 'forum.category.show'],
    ['app/pages/tags/index.vue', 'forum.tag.index'],
    ['app/pages/badges/index.vue', 'forum.badge.index'],
    ['app/pages/tags/[tagSlug].vue', 'forum.tag.show'],
    ['app/pages/t/[...path].vue', 'forum.topic.show'],
    ['app/pages/topics/new.vue', 'forum.topic.create'],
    ['app/pages/topics/reply.vue', 'forum.topic.reply'],
    ['app/pages/u/[username].vue', 'forum.profile.show'],
    ['app/pages/settings/profile.vue', 'forum.settings.profile'],
    ['app/pages/settings/appearance.vue', 'forum.settings.appearance'],
    ['app/pages/settings/security/login-methods.vue', 'forum.settings.login_methods'],
    ['app/pages/settings/security/password.vue', 'forum.settings.password'],
    ['app/pages/settings/security/index.vue', 'forum.settings.security'],
    ['app/pages/settings/tokens.vue', 'forum.settings.tokens'],
    ['app/pages/settings/notifications.vue', 'forum.settings.notifications'],
    ['app/pages/notifications/index.vue', 'forum.notifications'],
    ['app/pages/moderation/index.vue', 'moderation.review'],
    ['app/pages/login.vue', 'auth.login'],
    ['app/pages/register.vue', 'auth.register'],
    ['app/pages/auth/continue.vue', 'auth.external_continuation'],
    ['app/pages/forgot-password.vue', 'auth.forgot_password'],
    ['app/pages/reset-password.vue', 'auth.reset_password'],
    ['app/pages/terms.vue', 'site.terms'],
    ['app/pages/privacy.vue', 'site.privacy'],
    ['app/pages/guidelines.vue', 'site.guidelines'],
    ['app/pages/outbound.vue', 'site.outbound'],
    ['app/pages/components.vue', 'dev.components']
  ]

  for (const [file, pageId] of catalogPages) {
    it(`${file} uses SFPageOutlet page="${pageId}"`, () => {
      const src = read(file)
      expect(src).toContain('SFPageOutlet')
      expect(src).toContain(`page="${pageId}"`)
    })
  }

  it('routes themeable system errors through the dedicated themed boundary', () => {
    const errorPage = read('app/error.vue')
    const systemError = read('app/components/errors/SFSystemErrorPage.vue')
    expect(errorPage).toContain('SFSystemErrorPage')
    expect(errorPage).toContain('const nuxtError = computed(() => props.error)')
    expect(errorPage).toContain("robots: 'noindex,nofollow'")
    expect(systemError).toContain('SFSystemThemeTemplate')
    expect(systemError).toContain(':data-page="page.pageId"')
    expect(systemError).toContain(':render-output="resolvedPage?.renderOutput"')
    expect(systemError).toContain('SFSystemErrorEmergencyPage')
    expect(errorPage).toContain('useSystemErrorPagePresentation(systemPageId)')
    expect(errorPage).toContain('isThemeableSystemErrorStatus')
    expect(errorPage).toContain('systemErrorPageIdForStatus')
    expect(errorPage).toContain('useActiveThemeSkin()')
    expect(errorPage).toContain("'data-sforum-theme-skin': '1'")
    // 皮肤是基底、宿主 CSS 细化：两条渲染路径都必须把皮肤链固定在 head 最前，
    // 否则管理端往返重挂皮肤后会反超宿主 CSS，列线等基础几何被主题清零。
    expect(errorPage).toContain("tagPriority: 'critical' as const")
    expect(read('app/app.vue')).toContain("tagPriority: 'critical' as const")
    expect(errorPage).toContain("'data-sforum-theme': appliedAppearanceTheme.value.dataTheme")
    expect(errorPage).toContain("'data-sforum-light-background': appliedLightBackground.value")
    expect(errorPage).toContain("'data-sforum-error': String(systemPage.value.statusCode)")
    expect(read('app/assets/css/sforum-theme.css')).toContain('body[data-sforum-error="404"] > nuxt-error-overlay.pip-hidden')
    const errorPanel = read('app/components/errors/SFErrorPagePanel.vue')
    // 动作走共享组合式并渲染成真链接：入口 chunk 被拒载时仍可导航。
    expect(errorPanel).toContain('useErrorPageActions()')
    expect(errorPanel).toContain(':href="homeHref"')
    expect(errorPanel).toContain(':href="retryHref"')
    const errorActions = read('app/composables/errors/useErrorPageActions.ts')
    expect(errorActions).toContain('function clearResolvedDevErrorOverlay()')
    expect(errorActions).toContain("document.querySelector('nuxt-error-overlay.pip-hidden')?.remove()")
    expect(errorActions).toContain('await clearError({ redirect: homeHref.value })')
    expect(errorActions.indexOf('await clearError({ redirect: homeHref.value })'))
      .toBeLessThan(errorActions.indexOf('clearResolvedDevErrorOverlay()', errorActions.indexOf('async function goHome(')))
    expect(errorPage).toContain('const resolvedSystemPage = shallowRef(')
    expect(errorPage).not.toContain('await prepareNotFoundPage')
    expect(errorPage).not.toContain('onServerPrefetch')
    expect(errorPage).not.toContain('<script setup lang="ts" async>')

    const presentation = read('app/composables/errors/useSystemErrorPagePresentation.ts')
    const notFoundPresentation = read('app/composables/errors/useNotFoundPagePresentation.ts')
    const startupState = read('app/composables/errors/useErrorPageStartupState.ts')
    expect(presentation).toContain('themeSkin.refresh({')
    expect(startupState).toContain('serverInternal: import.meta.server')
    expect(startupState).toContain('serverInternal: true')
    expect(presentation).toContain("if (resolved.provider === 'core' || resolved.fallback)")
    expect(presentation).toContain('const startupTask = startupState.refresh()')
    expect(presentation).toContain('await startupTask')
    expect(notFoundPresentation).toContain('const startupTask = startupState.refresh()')
    expect(notFoundPresentation).toContain('await startupTask')
    expect(presentation).toContain('exactThemeIdentityForPageResolve(resolved)')
    expect(presentation).toContain('enterCoreEmergency(')
    expect(presentation).toContain('Promise.all([')
    expect(presentation).not.toContain('Promise.allSettled')

    const systemResolve = read('app/composables/errors/useSystemErrorPageResolve.ts')
    expect(systemResolve).not.toContain('actorKey')
    expect(systemResolve).toContain('system-error-page-resolve:${resolvedPageId.value}:${String(locale.value || \'zh-CN\')}:${route.path}')
    expect(systemResolve).toContain('useState<PageResolvePayload | null>')
    expect(systemResolve).toContain('clearNuxtState(stateKey)')
    expect(systemResolve).not.toContain('return useAsyncData')
    expect(systemResolve).toContain('async function refresh(')
    expect(systemResolve).toContain('serverInternal: import.meta.server')
    expect(systemResolve).toContain('failure = shallowRef<unknown>(null)')
    expect(systemResolve).toContain('useCoreFallback')
    expect(systemResolve).toContain('deferCommit')
    expect(presentation).toContain('apply: false')
    expect(presentation).toContain('themeSkin.commit(skinResult)')
    expect(systemError).not.toContain('class="min-h-screen"')
    expect(systemResolve).toContain('timeout: import.meta.dev ? 800 : 1000, maxAttempts: 1')

    const serverPlugin = read('app/plugins/not-found-theme.server.ts')
    expect(serverPlugin).toContain("name: 'sforum-system-error-theme'")
    expect(serverPlugin).toContain('await useSystemErrorPagePresentation(pageId).prepare()')
    expect(read('app/components/SFPageOutletResolver.vue')).toContain('await notFoundPresentation.prepare()')
    // 两个动态公开页 catch-all 现已共用同一解析入口，404 呈现准备也随之下沉到那里。
    expect(read('app/composables/pages/useRegistryPageResolve.ts')).toContain('await notFoundPresentation.prepare()')

    const activeThemeSkin = read('app/composables/themes/useActiveThemeSkin.ts')
    expect(activeThemeSkin.indexOf('const { request } = useApiClient()'))
      .toBeLessThan(activeThemeSkin.indexOf('async function refresh('))
    expect(activeThemeSkin.slice(activeThemeSkin.indexOf('async function refresh(')))
      .not.toContain('const { request } = useApiClient()')

    const systemTemplate = read('app/components/SFSystemThemeTemplate.vue')
    const systemNode = read('app/components/SFSystemThemeNode.vue')
    expect(systemTemplate).toContain('renderThemeRenderNodes')
    expect(systemTemplate).toContain('const ThemeNodes = () =>')
    expect(systemTemplate).toContain('L0/L1')
    expect(systemTemplate).toContain('SFSystemErrorEmergencyPage')
    expect(systemTemplate).toContain('<SFSystemErrorEmergencyPage v-else')
    expect(systemTemplate).toContain('class="sf-theme-template sf-system-theme-template"')
    expect(systemTemplate).not.toContain('SFAlert')
    expect(systemTemplate).toContain("'system.component.error_details': SFSystemErrorDetails")
    expect(systemTemplate).toContain("'system.component.error_actions': SFSystemErrorActions")
    expect(systemTemplate).toContain("'system.component.error_recovery': SFSystemErrorRecovery")
    expect(systemTemplate).toContain("'system.component.error_sidebar': SFSystemErrorSidebar")
    expect(systemTemplate).toContain("'system.component.error_rail': SFSystemErrorRail")
    expect(systemNode).toContain('<SFNotFoundPageContent')
    expect(systemNode).toContain('<SFNavbar')
    expect(systemNode).toContain('<SFFooter')
    expect(systemNode).toContain('const self = getCurrentInstance()!.type')
    expect(systemNode).not.toContain('<SFSystemThemeNode')
    expect(systemNode).toContain("import SFNavbar from './SFNavbar.vue'")
    expect(systemNode).toContain("import SFNotFoundPageContent from './errors/SFNotFoundPageContent.vue'")
    expect(systemNode).toContain("import SFFooter from './SFFooter.vue'")
    expect(read('app/components/SFNavbar.vue')).not.toContain('await useAsyncData')
    expect(read('app/components/SFNavbar.vue')).toContain('fetchRemoteChrome')
    expect(read('app/components/SFFooter.vue')).not.toContain("await useAsyncData('site-public-friend-links'")
    expect(read('app/components/SFFooter.vue')).toContain('fetchRemoteChrome')
    expect(read('app/components/errors/SFSystemErrorEmergencyPage.vue')).toContain('emergency')
    const errorContent = read('app/components/errors/SFErrorPageContent.vue')
    expect(errorContent).toContain(':fetch-remote-chrome="!emergency"')
    expect(errorContent).toContain('!props.emergency && normalizeErrorStatus')
    const recoveryBody = read('app/components/errors/SFSystemErrorRecovery.vue')
    expect(recoveryBody).toContain('method="get"')
    expect(recoveryBody).toContain(':action="searchAction"')
    expect(recoveryBody).toContain('name="q"')
    expect(recoveryBody).not.toContain('@submit.prevent')
    const recoveryData = read('app/composables/errors/useSystemErrorRecoveryData.ts')
    expect(recoveryData).toContain('RECOVERY_DATA_TIMEOUT_MS')
    expect(recoveryData).toContain('serverInternal: import.meta.server')
    expect(recoveryData).toContain('forumApi.listCategoryGroups(readOptions)')
    expect(recoveryData).toContain('forumApi.listTags(readOptions)')
    expect(recoveryData).toContain('forumApi.listTopics({ page: 1 }, readOptions)')
    expect(read('server/plugins/not-found-document-policy.ts')).toContain("headers['cache-control'] = 'no-store'")
    expect(read('server/plugins/not-found-document-policy.ts')).toContain("headers['x-robots-tag'] = 'noindex,nofollow'")
    expect(read('server/plugins/not-found-document-policy.ts')).toContain("path.startsWith('/api/')")
    expect(read('server/plugins/not-found-document-policy.ts')).toContain("contentType.includes('application/json')")
    expect(systemError).not.toContain('setResponseHeader(')
    const systemDetails = read('app/components/errors/SFSystemErrorDetails.vue')
    expect(systemDetails).toContain('sforum-system-error__status-code')
    expect(systemDetails).toContain("t('errors.page.statusCaption')")
    expect(read('i18n/locales/zh-CN.json')).toContain('"statusCaption": "HTTP STATUS"')

    const adminPages = read('app/pages/admin/extensions/pages.vue')
    expect(adminPages).toContain('virtual?: boolean')
    expect(adminPages).toContain('function pathLabel(row: PageRow)')
    expect(adminPages).toContain('admin.extensions.pages.virtual.${row.page.id.replaceAll')
    expect(adminPages).toContain('i-lucide-file-warning')
    expect(read('i18n/locales/zh-CN.json')).toContain('"system_rate_limited": "429 访问过频错误页"')
    expect(read('i18n/locales/en-US.json')).toContain('"system_server_error": "5xx service error page"')
  })

  it('resolves protected pages and supplies the core page as a Host island slot', () => {
    const outlet = [
      read('app/components/SFPageOutlet.vue'),
      read('app/components/SFPageOutletResolver.vue'),
      read('app/components/SFPageOutletRender.vue')
    ].join('\n')
    const template = read('app/components/SFThemeTemplate.vue')
    expect(outlet).not.toContain('CONSTRAINED_PAGES')
    expect(outlet).not.toContain('isConstrained')
    expect(outlet).toContain('<slot />')
    // Auth forms are Host body islands (Host code, not theme-executable).
    expect(template).toContain("'identity.component.login_form': defineAsyncComponent(() => import('./identity/SFLoginFormPage.vue'))")
    expect(template).toContain("'forum.component.topic_composer': defineAsyncComponent(() => import('./forum/SFTopicComposerPage.vue'))")
    expect(template).toContain("'forum.component.topic_reply': defineAsyncComponent(() => import('./forum/SFTopicReplyPage.vue'))")
    expect(template).toContain("'forum.component.topic_editor': defineAsyncComponent(() => import('./forum/SFTopicEditPage.vue'))")
    expect(template).toContain("'forum.component.home_page': defineAsyncComponent(() => import('./forum/SFHomePage.vue'))")
    expect(template).toContain("'forum.component.search_page': defineAsyncComponent(() => import('./forum/SFHomePage.vue'))")
    expect(template).toContain("'system.component.not_found': SFNotFoundPageContent")
    expect(read('app/components/SFSystemThemeTemplate.vue')).toContain("'system.component.error_details': SFSystemErrorDetails")
    expect(template).not.toContain('resolveComponent(')
    expect(template).toContain('slots.default?.()')
    // fail-closed 公开页走宿主 chrome；主题成功路径不套 Host chrome。
    expect(outlet).toContain('SFHostPublicChrome')
    expect(outlet).toContain('useHostPublicChrome')
    const hostChrome = read('app/components/SFHostPublicChrome.vue')
    expect(hostChrome).toContain('sf-host-public-chrome--fullwidth-3col')
    expect(hostChrome).toContain('data-layout="fullwidth-3col"')
    expect(hostChrome).toContain('sf-host-public-chrome__body')
  })

  it('binds core theme resolution to the current path and query', () => {
    const src = read('app/components/SFPageOutletResolver.vue')
    expect(src).toContain("path: route.path")
    expect(src).toContain("query.set('query', requestQuery.value)")
    expect(src).toContain('resolveLocale.value')
    expect(src).toContain('resolveActorKey.value')
    expect(src).toContain('${route.path}?${requestQuery.value}')
  })

  it('does not cache rendered Page Registry output across paths or actors', () => {
    const src = read('app/components/SFPageOutletResolver.vue')
    const util = read('app/utils/pageResolve.ts')
    expect(src).not.toContain('usePageResolveShellCache')
    expect(src).not.toContain('rememberShell')
    expect(src).not.toContain('recallShell')
    expect(src).not.toContain('data-stale')
    expect(src).not.toContain('default: () =>')
    expect(src).toContain('requestPageResolveWithRetry')
    expect(src).toContain('PAGE_RESOLVE_TIMEOUT_MS')
    expect(src).toContain('PAGE_RESOLVE_REASON.transportUnavailable')
    expect(src).toContain('disableSharedPageCacheForPageResolve')
    expect(src).toContain('disableSharedDocumentCache()')
    expect(src).toContain('requestEvent?.context')
    expect(util).toContain('routeRules.cache = false')
    expect(util).toContain('routeRules.swr = false')
  })

  it('loads only Host-issued exact-artifact L2 descriptors', () => {
    const src = read('app/components/SFExtensionWidget.vue')
    expect(src).toContain('parsePublicFrontendDescriptor')
    expect(src).toContain('publicComponentPath')
    expect(src).toContain('data-l2-fallback')
    expect(src).not.toContain('entry?: string')
    expect(src).not.toContain('integrity?: string')
  })

  it('both dynamic registry catch-alls share one resolution implementation', () => {
    // 解析与 401/403/404 映射收敛到单一 composable；两个 catch-all 只负责路径来源。
    const resolve = read('app/composables/pages/useRegistryPageResolve.ts')
    expect(resolve).toContain('/pages/resolve-path')
    expect(resolve).toContain('createError')

    for (const file of ['app/pages/[...sfRegistryPage].vue', 'app/pages/x/[...path].vue']) {
      const src = read(file)
      expect(src).toContain('useRegistryPageResolve')
      expect(src).toContain('<SFRegistryPageShell')
      // 解析逻辑不得再被复制回路由壳。
      expect(src).not.toContain('/pages/resolve-path')
      expect(src).not.toContain('createError')
    }
  })

  it('renders Host emergency output for dynamic extension pages', () => {
    // 两个 catch-all 共用受审查的正文出口组件；条件语义不变。
    const body = read('app/components/extensions/public/SFRegistryPageBody.vue')
    expect(body).toContain('v-else-if="renderOutput || templateHtml"')
    // 绝不因 fallback 抑制渲染：宿主的 emergency 输出必须仍然可见。
    expect(body).not.toContain('&& !fallback')
    expect(body).not.toContain('fallback &&')
  })

  it('renders a page-provided bounded schema descriptor through the reviewed body exit', () => {
    // view=schema 的公开页：正文是包内声明的有界描述符，由宿主渲染，仍只经这一处出口。
    const body = read('app/components/extensions/public/SFRegistryPageBody.vue')
    expect(body).toContain('v-else-if="schemaView"')
    expect(body).toContain('<SFPublicSchemaView')
    expect(body).toContain(':descriptor="schemaView"')
    // 显式导入：同目录兄弟组件不依赖自动导入名，改名时不会静默失效。
    expect(body).toContain("import SFPublicSchemaView from './SFPublicSchemaView.vue'")
    // schema 分支排在模板分支之前：view=schema 时 templateHtml 为空，顺序决定语义。
    expect(body.indexOf('v-else-if="schemaView"')).toBeLessThan(body.indexOf('v-else-if="renderOutput || templateHtml"'))
    for (const forbidden of ['SFNavbar', 'SFFooter', 'sf-navbar', 'sf-footer']) {
      expect(body).not.toContain(forbidden)
    }

    // 渲染器只渲染数据本身：不得继承 admin outlet 的 invokable/命令/资源可见性语义。
    const renderer = read('app/components/extensions/public/SFPublicSchemaView.vue')
    for (const forbidden of ['commandSurfaceId', 'invokable', 'visibleResourceIds', 'SFAdminSurfaceOutlet']) {
      expect(renderer).not.toContain(forbidden)
    }
    // 四种有界种类都必须有分支，且 form 不伪造提交（提交依赖仍被阻塞的 dataRoute 绑定）。
    for (const kind of ["'list'", "'detail'", "'notice'", "'form'"]) {
      expect(renderer).toContain(kind)
    }
    expect(renderer).toContain('data-schema-form-pending')

    // 正文契约、两个路由壳与解析载荷都必须透传 schemaView。
    expect(read('app/composables/pages/useRegistryPageBody.ts')).toContain('schemaView?: Record<string, unknown>')
    expect(read('app/utils/pageResolve.ts')).toContain('schemaView?: Record<string, unknown>')
    for (const shell of ['app/pages/[...sfRegistryPage].vue', 'app/pages/x/[...path].vue']) {
      expect(read(shell)).toContain('schemaView: data?.schemaView')
    }
  })

  it('mounts a page-provided public component through the reviewed body exit', () => {
    // view=component 的公开页：正文由包内公开 L2 组件提供，仍只经 SFRegistryPageBody 这一处出口。
    const body = read('app/components/extensions/public/SFRegistryPageBody.vue')
    expect(body).toContain('v-else-if="component"')
    expect(body).toContain('<SFExtensionWidget')
    expect(body).toContain(':component-id="component"')
    // 组件分支排在模板分支之前：view=component 时 templateHtml 为空，顺序决定语义。
    expect(body.indexOf('v-else-if="component"')).toBeLessThan(body.indexOf('v-else-if="renderOutput || templateHtml"'))
    // 组件分支同样不得自带 chrome。
    for (const forbidden of ['SFNavbar', 'SFFooter', 'sf-navbar', 'sf-footer']) {
      expect(body).not.toContain(forbidden)
    }
    // 正文契约与两个路由壳都必须透传 view/component，否则组件页会被当成空模板页。
    const contract = read('app/composables/pages/useRegistryPageBody.ts')
    expect(contract).toContain('component?: string')
    for (const shell of ['app/pages/[...sfRegistryPage].vue', 'app/pages/x/[...path].vue']) {
      const src = read(shell)
      expect(src).toContain('component: data?.component')
      expect(src).toContain('view: data?.view')
    }
    // 解析载荷必须携带这两个字段，否则组件 id 到不了正文出口。
    const payload = read('app/utils/pageResolve.ts')
    expect(payload).toContain('component?: string')
    expect(payload).toContain('view?: string')
  })

  it('gives layout: site exactly one host chrome shell and leaves bare unchanged', () => {
    const shell = read('app/components/extensions/public/SFRegistryPageShell.vue')
    // 两个互斥外壳分支之一挂宿主 chrome；「恰好一个 navbar/footer」由「只有一个外壳」保证。
    expect(shell.match(/<SFHostPublicChrome/g)).toHaveLength(1)
    expect(shell).toContain('data-registry-shell="site"')
    expect(shell).toContain('data-registry-shell="bare"')
    // 默认 site：只有显式 bare 才退出外壳。
    expect(shell).toContain("props.layout !== 'bare'")
    // bare 分支保持既有几何，避免改变已批准精确制品的行为。
    expect(shell).toContain('class="sf-public-page sf-registry-add mx-auto w-full max-w-4xl px-4 py-8"')
    // chrome 只能来自外壳：正文不得自带 navbar/footer，否则会双层导航。
    const body = read('app/components/extensions/public/SFRegistryPageBody.vue')
    for (const forbidden of ['SFNavbar', 'SFFooter', 'sf-navbar', 'sf-footer']) {
      expect(body).not.toContain(forbidden)
    }
  })

  it('uses the theme shell when one is declared, with the body in the reviewed island', () => {
    const shell = read('app/components/extensions/public/SFRegistryPageShell.vue')
    // 三个互斥分支：主题外壳 / 宿主回退 / 显式 bare。主题分支优先。
    expect(shell.match(/<SFThemeTemplate/g)).toHaveLength(1)
    expect(shell.match(/<SFHostPublicChrome/g)).toHaveLength(1)
    expect(shell).toContain('v-if="themeShellHtml"')
    expect(shell).toContain('v-else-if="siteShell"')
    expect(shell).toContain('data-registry-shell-source="theme"')
    // 正文以数据形式 provide 给岛：主题外壳里的岛是独立组件，拿不到 slot。
    expect(shell).toContain('provideRegistryPageBody')
    expect(shell).not.toContain('<slot')

    const island = read('app/components/extensions/public/SFPluginPageBodyIsland.vue')
    expect(island).toContain('useRegistryPageBody')
    expect(island).toContain('SFRegistryPageBody')

    // 两张映射表必须同时登记正文岛，否则主题模板要么校验不过、要么渲染为空。
    const themeTemplate = read('app/components/SFThemeTemplate.vue')
    expect(themeTemplate).toContain("'plugin.component.page_body'")
    expect(themeTemplate).toContain("'sf-plugin-page-body'")
  })

  it('registers the account-settings sidebar island on both resolution paths', () => {
    // 插件公开页（如 /settings/oauth-apps）靠这个岛取得与 /settings/profile 同构的左栏。
    // 插件页无 renderOutput，走的是 legacy HTML 解析路径，因此两张映射表都必须登记，
    // 只登记一张会让左栏渲染成 sforum-island-unavailable 注释。
    const themeTemplate = read('app/components/SFThemeTemplate.vue')
    expect(themeTemplate).toContain("'settings.component.account_sidebar'")
    expect(themeTemplate).toContain("'sf-account-settings-sidebar'")
    // 岛组件与 SFSettingsShell 共用同一份左栏实现，不得各自复制。
    const sidebar = read('app/components/settings/SFSettingsAccountSidebar.vue')
    expect(sidebar).toContain('<SFSettingsAccountNav')
    expect(sidebar).toContain('<SFResponsivePublicSidebar')
    expect(sidebar).toContain('owner-id="forum.settings"')
    const shell = read('app/components/settings/SFSettingsShell.vue')
    expect(shell).toContain('<SFSettingsAccountSidebar')
    expect(shell).not.toContain('<SFSettingsAccountNav')
  })

  it('chooses the shell from the contribution layout alone, not from theme state', () => {    const shell = read('app/components/extensions/public/SFRegistryPageShell.vue')
    // 切主题不得改变 chrome 的有无与数量，因此外壳选择不得读取主题状态。
    // （`SFHostPublicChrome` 自身可读主题设置来决定页脚/公告可见性，那是外壳内部的呈现，
    //  不是外壳的选择。）
    for (const forbidden of ['useActiveThemeSettings', 'useActiveThemeSkin']) {
      expect(shell).not.toContain(forbidden)
    }
  })

  it('treats bare as the only chrome opt-out', () => {
    const shell = read('app/components/extensions/public/SFRegistryPageShell.vue')
    // 默认 site（与其它公开页共享几何契约），作者用显式 bare 退出。
    // 未知值由安装/启用预检 fail closed，不依赖渲染层兜底判断。
    expect(shell).toContain("props.layout !== 'bare'")
    expect(shell).not.toContain("props.layout === 'site'")
  })

  it('keeps non-themeable failures outside the replaceable system-error surface', () => {
    const src = read('app/error.vue')
    expect(src).toContain('isThemeableSystemErrorStatus(nuxtError.value?.statusCode)')
    expect(src).toContain('<UApp v-else')
    expect(src).toContain('<SFErrorPageContent :error="nuxtError" />')
    expect(src.indexOf('<SFSystemErrorPage')).toBeLessThan(src.indexOf('<UApp v-else'))
  })

  it('lets the active theme present system error pages while Host keeps the error islands', () => {
    const outlet = [
      read('app/components/SFPageOutlet.vue'),
      read('app/components/SFPageOutletRender.vue')
    ].join('\n')
    const errorPage = read('app/error.vue')
    const catalog = read('../../apps/api/app/Support/Pages/catalog.go')

    expect(outlet).not.toContain('forceDefaultTheme')
    expect(errorPage).not.toContain('forceDefaultTheme')
    expect(errorPage).toContain('SFSystemErrorPage')
    expect(catalog).toMatch(/ID: "system\.forbidden"[^\n]+Replaceable: true[^\n]+Virtual: true/)
    expect(catalog).toMatch(/ID: "system\.not_found"[^\n]+Replaceable: true/)
    expect(catalog).toMatch(/ID: "system\.rate_limited"[^\n]+Replaceable: true[^\n]+Virtual: true/)
    expect(catalog).toMatch(/ID: "system\.server_error"[^\n]+Replaceable: true[^\n]+Virtual: true/)
  })
})
