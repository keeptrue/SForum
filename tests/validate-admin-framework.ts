import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.cwd()

function file(path: string) {
  return resolve(root, path)
}

function read(path: string) {
  return readFileSync(file(path), 'utf8')
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

const adminRouteModule = file('apps/web/app/utils/admin/adminRoutePrefix.ts')
assert(existsSync(adminRouteModule), 'Missing admin route prefix helper')

const routes = await import(pathToFileURL(adminRouteModule).href)

assert(routes.DEFAULT_ADMIN_ROUTE_PREFIX === '/control-panel', 'Default admin prefix must be /control-panel')
assert(routes.normalizeAdminRoutePrefix(undefined) === '/control-panel', 'Undefined prefix should use the default')
assert(routes.normalizeAdminRoutePrefix('') === '/control-panel', 'Blank prefix should use the default')
assert(routes.normalizeAdminRoutePrefix('staff') === '/staff', 'Prefix should gain a leading slash')
assert(routes.normalizeAdminRoutePrefix('/staff/') === '/staff', 'Prefix should lose trailing slashes')
assert(routes.joinAdminRoutePath('/staff', 'roles') === '/staff/roles', 'Child paths should join cleanly')
assert(routes.joinAdminRoutePath('/staff', '/') === '/staff', 'Root child path should not add a trailing slash')
assert(routes.normalizeAdminChildPath('roles') === '/roles', 'Admin child paths should normalize to leading slash')
assert(routes.resolveAdminRouteChildPath('/control-panel', '/control-panel') === '/', 'Admin root route should resolve to dashboard id')
assert(routes.resolveAdminRouteChildPath('/control-panel', '/en-US/control-panel/roles') === '/roles', 'Localized admin route should resolve to child id')
assert(routes.resolveAdminRouteChildPath('/control-panel', '/forum') === null, 'Non-admin routes should not resolve to admin child ids')

assert(existsSync(file('apps/web/app/composables/admin/useAdminRoutes.ts')), 'Missing runtime admin route composable')
assert(existsSync(file('apps/web/app/layouts/admin.vue')), 'Missing admin dashboard layout')
assert(existsSync(file('apps/web/app/config/adminModules.ts')), 'Missing low-code admin module registry')
assert(existsSync(file('apps/web/app/composables/admin/useAdminPage.ts')), 'Missing admin page registration composable')

const adminModulesModule = await import(pathToFileURL(file('apps/web/app/config/adminModules.ts')).href)
const adminPageDefinitions = adminModulesModule.adminPageDefinitions as Array<{
  id: string
  labelKey: string
  icon: string
  componentName: string
  requiredPermissions?: string[]
  permissionMode?: string
}>
const isAdminNavigationEntryActive = adminModulesModule.isAdminNavigationEntryActive as (entry: unknown, pageId: string) => boolean
const shouldOpenAdminNavigationEntry = adminModulesModule.shouldOpenAdminNavigationEntry as (entry: unknown, pageId: string) => boolean
const isExtensionAdminPageId = adminModulesModule.isExtensionAdminPageId as (pageId: string) => boolean
const adminPageIds = adminPageDefinitions.map(page => page.id)
for (const requiredPageId of [
  '/',
  '/users',
  '/roles',
  '/permissions',
  '/settings',
  '/personalization',
  '/seo',
  '/database',
  '/attachments',
  '/attachments/settings',
  '/attachments/manager',
  '/forum/categories',
  '/forum/tags',
  '/forum/settings',
  '/extensions',
  '/extensions/install',
  '/extensions/plugins',
  '/extensions/themes',
  '/extensions/settings',
  '/extensions/events'
]) {
  assert(adminPageIds.includes(requiredPageId), `Admin module registry should define ${requiredPageId}`)
}
assert(adminPageDefinitions.every(page => page.icon.startsWith('i-lucide-')), 'Admin page registry should use lucide icons')
assert(adminPageDefinitions.find(page => page.id === '/permissions')?.permissionMode === 'any', 'Permission matrix should allow role.manage or user.manage')
// 扩展相关页使用细粒度权限（view / plugin.manage / theme.manage），不再统一 extension.manage
for (const extensionPageId of ['/extensions', '/extensions/install', '/extensions/plugins', '/extensions/themes', '/extensions/settings', '/extensions/events']) {
  const perms = adminPageDefinitions.find(page => page.id === extensionPageId)?.requiredPermissions || []
  assert(
    perms.some(permission => permission.startsWith('extension.')),
    `${extensionPageId} should require an extension.* permission`
  )
}

const adminRoutesComposable = read('apps/web/app/composables/admin/useAdminRoutes.ts')
assert(adminRoutesComposable.includes('useLocalePath'), 'Admin routes should delegate localization to the i18n localePath authority')
assert(adminRoutesComposable.includes('normalizeAdminRoutePrefix'), 'Admin routes should follow the operator adminRoutePrefix')
assert(!adminRoutesComposable.includes('locale.value'), 'Admin routes should not hand-roll a locale segment')

const nuxtConfig = read('apps/web/nuxt.config.ts')
assert(nuxtConfig.includes('NUXT_PUBLIC_ADMIN_ROUTE_PREFIX'), 'Nuxt config should expose the admin route prefix')
assert(nuxtConfig.includes('pages:extend'), 'Nuxt config should rewrite admin page routes')
assert(nuxtConfig.includes('rewriteAdminPageRoutes'), 'Nuxt config should use a focused admin route rewrite helper')

const adminExtensionsHelper = read('apps/web/app/utils/admin/adminExtensions.ts')
for (const requiredThemeState of ["'active'", "'activateDefault'", "'activate'"]) {
  assert(adminExtensionsHelper.includes(requiredThemeState), `Theme action state should include ${requiredThemeState}`)
}
assert(!adminExtensionsHelper.includes('themeRelease'), 'Theme action state must not depend on a release pipeline')

const executableTrustImpact = read('apps/web/app/components/admin/SFAdminExecutableTrustImpact.vue')
for (const requiredDatabaseRiskContract of [
  'data-testid="extension-database-risk"',
  'database.coreCompatibility',
  'database.backup.required',
  'database.backup.strategy',
  'database.retention.onDisable',
  'database.retention.onUninstall',
  'migration.digest',
  'migrationTransaction(migration)',
  'data-testid="extension-database-high-risk"',
  'data-testid="extension-database-backup-guidance"',
  '}, 10000)'
]) {
  assert(executableTrustImpact.includes(requiredDatabaseRiskContract), `Executable trust impact should expose ${requiredDatabaseRiskContract}`)
}
const executableTrustDialog = read('apps/web/app/components/admin/SFAdminExtensionEnableDialog.vue')
assert(executableTrustDialog.includes('v-if="error"'), 'Executable trust blocking errors must remain visible until resolved')
const zhLocale = JSON.parse(read('apps/web/i18n/locales/zh-CN.json'))
const enLocale = JSON.parse(read('apps/web/i18n/locales/en-US.json'))
for (const locale of [zhLocale, enLocale]) {
  const databaseRisk = locale.admin?.extensions?.trust?.databaseRisk
  assert(databaseRisk?.highRiskTitle, 'Executable trust locale should explain high-risk database changes')
  assert(databaseRisk?.backupGuidanceDescription, 'Executable trust locale should recommend a restorable backup or export')
  assert(databaseRisk?.transactions?.forbidden, 'Executable trust locale should label non-transactional migrations')
}

const envExample = read('.env.example')
const productionEnvExample = read('.env.production.example')
assert(envExample.includes('NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=/control-panel'), '.env.example should document the admin route prefix')
assert(productionEnvExample.includes('NUXT_PUBLIC_ADMIN_ROUTE_PREFIX=/control-panel'), '.env.production.example should document the admin route prefix')

// 路由壳只保留 layout/middleware + outlet；表单与回跳逻辑在 Host body 岛组件中。
for (const authForm of [
  'apps/web/app/components/identity/SFLoginFormPage.vue',
  'apps/web/app/components/identity/SFRegisterFormPage.vue'
]) {
  const content = read(authForm)
  assert(content.includes('useAdminRoutes') || content.includes('useAuthReturnNavigation'), `${authForm} should use the admin route or centralized auth return helper`)
  assert(!content.includes("? '/admin'"), `${authForm} should not hard-code the legacy /admin prefix`)
}
// Runtime Page Registry：公开 auth 页由 host 拥有，不再要求默认主题 Layer。
assert(existsSync(file('apps/web/app/pages/login.vue')), 'Login page should live on the host after theme migration')
assert(existsSync(file('apps/web/app/pages/register.vue')), 'Register page should live on the host after theme migration')
assert(existsSync(file('apps/web/app/components/identity/SFLoginFormPage.vue')), 'Login form body island should live on the host')
assert(existsSync(file('apps/web/app/components/identity/SFRegisterFormPage.vue')), 'Register form body island should live on the host')

const adminPagePathsById: Record<string, string> = {
  '/': 'apps/web/app/pages/admin/index.vue',
  '/roles': 'apps/web/app/pages/admin/roles.vue',
  '/users': 'apps/web/app/pages/admin/users.vue',
  '/permissions': 'apps/web/app/pages/admin/permissions.vue',
  '/settings': 'apps/web/app/pages/admin/settings/index.vue',
  '/settings/mail': 'apps/web/app/pages/admin/settings/mail.vue',
  '/settings/notifications': 'apps/web/app/pages/admin/settings/notifications.vue',
  '/settings/login-methods': 'apps/web/app/pages/admin/settings/login-methods.vue',
  '/settings/avatar': 'apps/web/app/pages/admin/settings/avatar.vue',
  '/settings/features': 'apps/web/app/pages/admin/settings/features.vue',
  '/entity-meta': 'apps/web/app/pages/admin/entity-meta.vue',
  '/personalization': 'apps/web/app/pages/admin/personalization.vue',
  '/site-chrome': 'apps/web/app/pages/admin/site-chrome.vue',
  '/moderation': 'apps/web/app/pages/admin/moderation.vue',
  '/seo': 'apps/web/app/pages/admin/seo.vue',
  '/database': 'apps/web/app/pages/admin/database.vue',
  '/attachments': 'apps/web/app/pages/admin/attachments/index.vue',
  '/attachments/settings': 'apps/web/app/pages/admin/attachments/settings.vue',
  '/attachments/manager': 'apps/web/app/pages/admin/attachments/manager.vue',
  '/forum/categories': 'apps/web/app/pages/admin/forum/categories.vue',
  '/forum/tags': 'apps/web/app/pages/admin/forum/tags.vue',
  '/forum/settings': 'apps/web/app/pages/admin/forum/settings.vue',
  '/forum/content': 'apps/web/app/pages/admin/forum/content.vue',
  '/extensions': 'apps/web/app/pages/admin/extensions/index.vue',
  '/extensions/install': 'apps/web/app/pages/admin/extensions/install.vue',
  '/extensions/plugins': 'apps/web/app/pages/admin/extensions/plugins.vue',
  '/extensions/themes': 'apps/web/app/pages/admin/extensions/themes.vue',

  '/extensions/settings': 'apps/web/app/pages/admin/extensions/settings.vue',
  '/extensions/events': 'apps/web/app/pages/admin/extensions/events.vue',
  '/extensions/contributions': 'apps/web/app/pages/admin/extensions/contributions.vue',
  '/extensions/releases': 'apps/web/app/pages/admin/extensions/releases.vue',
  '/extensions/pages': 'apps/web/app/pages/admin/extensions/pages.vue',
  '/extensions/route-providers': 'apps/web/app/pages/admin/extensions/route-providers.vue',
  '/extensions/route-inspector': 'apps/web/app/pages/admin/extensions/route-inspector.vue',
  '/extensions/cache-inspector': 'apps/web/app/pages/admin/extensions/cache-inspector.vue',
  '/extensions/asset-inspector': 'apps/web/app/pages/admin/extensions/asset-inspector.vue',
  '/extensions/template-inspector': 'apps/web/app/pages/admin/extensions/template-inspector.vue',
  '/extensions/component-inspector': 'apps/web/app/pages/admin/extensions/component-inspector.vue',
  '/extensions/navigation-inspector': 'apps/web/app/pages/admin/extensions/navigation-inspector.vue',
  '/extensions/registry-catalogs': 'apps/web/app/pages/admin/extensions/registry-catalogs.vue',
  '/extensions/provider-slots': 'apps/web/app/pages/admin/extensions/provider-slots.vue',
  '/extensions/route-catalog': 'apps/web/app/pages/admin/extensions/route-catalog.vue',
  '/extensions/query-catalog': 'apps/web/app/pages/admin/extensions/query-catalog.vue',
  '/ai': 'apps/web/app/pages/admin/ai.vue',
  '/jobs': 'apps/web/app/pages/admin/jobs.vue',
  '/schedules': 'apps/web/app/pages/admin/schedules.vue',
  '/webhooks': 'apps/web/app/pages/admin/webhooks.vue',
  '/search': 'apps/web/app/pages/admin/search.vue'
}

// 纯重定向页：只校验 layout + 组件名 + 不走 tab 手写
const adminRedirectPageIds = new Set(['/site-chrome', '/settings/notifications', '/attachments'])

const attachmentConfigurationPage = read('apps/web/app/pages/admin/attachments/settings.vue')
assert(attachmentConfigurationPage.includes('SFAdminFixedTabNav'), 'Attachment Configuration should expose fixed tabs')
assert(attachmentConfigurationPage.includes('SFAdminAttachmentSettingsTab'), 'Attachment Configuration should retain the Basic Configuration content')
assert(attachmentConfigurationPage.includes('SFAdminAttachmentCompressionTab'), 'Attachment Configuration should expose Compression Configuration content')
assert(attachmentConfigurationPage.includes("'basic' | 'compression'"), 'Attachment Configuration should keep stable basic and compression tab ids')

for (const page of adminPageDefinitions) {
  const adminPage = adminPagePathsById[page.id]
  assert(adminPage, `Admin module registry points to an unknown page id ${page.id}`)
  const content = read(adminPage)
  assert(content.includes("layout: 'admin'"), `${adminPage} should use the admin layout`)
  assert(content.includes(`name: '${page.componentName}'`), `${adminPage} should keep component name aligned with registry`)
  assert(!content.includes('useAdminTabs'), `${adminPage} should not wire tabs manually`)
  assert(!content.includes('openTab('), `${adminPage} should not hard-code tab metadata`)

  if (adminRedirectPageIds.has(page.id)) {
    assert(content.includes('navigateTo'), `${adminPage} should redirect via navigateTo`)
    continue
  }


  assert(content.includes('UDashboardToolbar') || content.includes('useAdminPage'), `${adminPage} should render inside the admin shell or register via useAdminPage`)
  assert(content.includes('useAdminPage'), `${adminPage} should use low-code admin page registration`)
  assert(content.includes(`useAdminPage('${page.id}')`), `${adminPage} should register itself by page id only`)
}

const adminLayout = read('apps/web/app/layouts/admin.vue')
const adminModules = read('apps/web/app/config/adminModules.ts')
const adminFooterPath = 'apps/web/app/components/admin/SFAdminFooter.vue'
assert(existsSync(file(adminFooterPath)), 'Admin shell should have a dedicated global footer component')
const adminFooter = read(adminFooterPath)
for (const requiredComponent of [
  'UDashboardGroup',
  'UDashboardSidebar',
  'UDashboardPanel',
  'UNavigationMenu',
  'UIcon'
]) {
  assert(adminLayout.includes(requiredComponent), `Admin layout should use ${requiredComponent}`)
}

assert(adminLayout.includes('i-lucide-'), 'Admin layout should use Nuxt Icon lucide icons')
assert(adminLayout.includes('adminSidebarNavigation'), 'Admin layout should build sidebar navigation from the module registry')
// 侧栏可见性经 shouldShowAdminPageInNav（内含 canAccessAdminPage + 专业/运维模式）过滤。
assert(adminLayout.includes('shouldShowAdminPageInNav'), 'Admin layout should hide registry pages by frontend-visible permissions')
assert(adminLayout.includes('<SFAdminFooter />'), 'Admin layout should render the dedicated global footer')
assert(!adminLayout.includes('<SFFooter'), 'Admin layout should not reuse the public site footer')
assert(adminFooter.includes('data-testid="sforum-admin-footer"'), 'Admin footer should expose a stable test id')
assert(adminFooter.includes('data-testid="sforum-admin-footer-left"'), 'Admin footer should expose a stable left content area')
assert(adminFooter.includes('data-testid="sforum-admin-footer-right"'), 'Admin footer should expose a stable right content area')
assert(adminFooter.includes('admin.shell.footerCopyright'), 'Admin footer should use admin shell i18n copy')
assert(adminFooter.includes('admin.shell.footerProductSummary'), 'Admin footer should render official product summary copy')
assert(adminFooter.includes('justify-between'), 'Admin footer content should be split across left and right sides')
assert(adminFooter.includes('new Date().getFullYear()'), 'Admin footer should render the current copyright year')
assert(!adminLayout.includes("label: '系统配置'"), 'Admin layout should not hard-code Chinese sidebar labels')
assert(!adminLayout.includes('navigationItems = computed(() => ['), 'Admin layout should not hard-code sidebar menu arrays')
assert(adminModules.includes('admin.nav.personalization'), 'Admin modules should expose the personalization top-level menu')
assert(adminModules.includes('i-lucide-palette'), 'Personalization menu should use the palette icon')
assert(adminModules.includes('admin.nav.system'), 'Admin modules should expose the system navigation folder via translation key')
assert(adminModules.includes('admin.nav.operations'), 'Admin modules should expose the operations navigation folder via translation key')
assert(adminModules.includes('i-lucide-server-cog'), 'Operations menu should use the server-cog icon')
assert(adminModules.includes('admin.nav.extensions'), 'Admin modules should expose the extension manager menu')
assert(adminModules.includes('i-lucide-blocks'), 'Extension manager menu should use the blocks icon')
assert(adminModules.includes('admin.nav.extensionOverview'), 'Admin modules should expose extension overview submenu')
assert(adminModules.includes('admin.nav.extensionInstall'), 'Admin modules should expose the extension install submenu')
assert(adminModules.includes('admin.nav.extensionPlugins'), 'Admin modules should expose plugin manager submenu')
assert(adminModules.includes('admin.nav.extensionThemes'), 'Admin modules should expose theme manager submenu')
assert(adminModules.includes('admin.nav.extensionSettings'), 'Admin modules should expose extension settings submenu')
assert(adminModules.includes('admin.nav.extensionEvents'), 'Admin modules should expose extension events submenu')

const adminSidebarNavigation = adminModulesModule.adminSidebarNavigation as Array<Array<unknown>>
const firstSidebarGroup = adminSidebarNavigation[0] as Array<{
  type: string
  labelKey?: string
  children?: Array<{ type: string, pageId?: string }>
}>
const forumFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.forum')
assert(forumFolder, 'Admin sidebar should keep a forum folder')
assert(
  !firstSidebarGroup.some(entry => entry.type === 'page' && entry.pageId === '/moderation'),
  'Moderation management should live under the forum folder, not the top-level sidebar'
)
assert(forumFolder.children?.some(entry => entry.pageId === '/moderation'), 'Forum folder should contain the moderation page')
assert(
  forumFolder.children?.map(entry => entry.pageId).join(',') === '/moderation,/forum/categories,/forum/tags,/forum/settings,/forum/content',
  'Forum folder should keep the approved submenu order'
)
const systemFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.system')
assert(systemFolder, 'Admin sidebar should keep a system folder')
assert(
  !firstSidebarGroup.some(entry => entry.type === 'page' && entry.pageId === '/personalization'),
  'Personalization should live under the system folder, not the top-level sidebar'
)
assert(systemFolder.children?.some(entry => entry.pageId === '/personalization'), 'System folder should contain the personalization page')
assert(
  systemFolder.children?.map(entry => entry.pageId).join(',') === '/settings,/settings/mail,/settings/login-methods,/settings/avatar,/settings/features,/entity-meta,/personalization,/seo,/search',
  'System folder should keep the approved settings submenu order without ops tools'
)
// AI 控制台是一级菜单（admin.nav.aiTools），不再是「系统」文件夹的一员：
// 它有自己的 sectionId 供插件把设置页并进同一个容器。
const aiFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.aiTools')
assert(aiFolder, 'Admin sidebar should expose the AI tools folder as a first-level entry')
assert(
  aiFolder?.children?.map(entry => entry.pageId).join(',') === '/ai',
  'AI tools folder should own exactly the AI console page'
)
assert(!systemFolder.children?.some(entry => entry.pageId === '/settings/notifications'), 'Notification settings should be tabs inside the unified Mail and Notifications page')
assert(
  adminPageDefinitions.find(page => page.id === '/settings/mail')?.permissionMode === 'any',
  'Unified Mail and Notifications settings should allow either fine-grained permission'
)
assert(!systemFolder.children?.some(entry => entry.pageId === '/site-chrome'), 'Site chrome should be merged into personalization, not a separate sidebar page')
assert(
  adminPageDefinitions.find(page => page.id === '/personalization')?.permissionMode === 'any',
  'Personalization page should allow appearance or site chrome permissions'
)
assert(
  adminPageDefinitions.find(page => page.id === '/personalization')?.requiredPermissions?.includes('settings.site.manage'),
  'Personalization page should accept settings.site.manage after site-chrome merge'
)
assert(!systemFolder.children?.some(entry => entry.pageId === '/database'), 'System folder should not contain database ops page')
assert(!systemFolder.children?.some(entry => entry.pageId === '/jobs'), 'System folder should not contain background jobs page')
assert(!systemFolder.children?.some(entry => entry.pageId === '/extensions'), 'System folder should not contain the extension overview page')
const extensionFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.extensions')
assert(extensionFolder, 'Admin sidebar should expose extensions as an independent folder')
assert(
  extensionFolder.children?.map(entry => entry.pageId).join(',') ===
    '/extensions,/extensions/install,/extensions/plugins,/extensions/themes,/extensions/pages,/extensions/route-catalog,/extensions/query-catalog,/extensions/route-providers,/extensions/route-inspector,/extensions/cache-inspector,/extensions/asset-inspector,/extensions/template-inspector,/extensions/component-inspector,/extensions/navigation-inspector,/extensions/registry-catalogs,/extensions/provider-slots,/extensions/settings,/extensions/events,/extensions/contributions',
  'Extension folder should keep the approved submenu order'
)
const operationsFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.operations')
assert(operationsFolder, 'Admin sidebar should expose operations as an independent folder')
assert(
  operationsFolder.children?.map(entry => entry.pageId).join(',') === '/database,/jobs,/schedules,/webhooks',
  'Operations folder should keep the approved ops submenu order'
)
assert(
  !firstSidebarGroup.some(entry => entry.type === 'page' && (entry.pageId === '/database' || entry.pageId === '/jobs')),
  'Database and jobs should live under the operations folder, not the top-level sidebar'
)
const attachmentsFolder = firstSidebarGroup.find(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.attachments')
assert(attachmentsFolder, 'Admin sidebar should expose attachment settings as a folder')
assert(
  !firstSidebarGroup.some(entry => entry.type === 'page' && entry.pageId === '/attachments'),
  'Attachment settings should expose Basic Configuration and Attachment Management as submenus'
)
assert(
  attachmentsFolder.children?.map(entry => entry.pageId).join(',') === '/attachments/settings,/attachments/manager',
  'Attachment settings folder should expose independent settings and manager routes'
)
const extensionFolderIndex = firstSidebarGroup.findIndex(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.extensions')
const operationsFolderIndex = firstSidebarGroup.findIndex(entry => entry.type === 'folder' && entry.labelKey === 'admin.nav.operations')
assert(extensionFolderIndex >= 0 && operationsFolderIndex > extensionFolderIndex, 'Operations folder should appear below the extensions folder')
const extensionEventsPage = read('apps/web/app/pages/admin/extensions/events.vue')
assert(extensionEventsPage.includes('data-testid="admin-extension-events-page"'), 'Extension event log page should expose a stable page wrapper for layout checks')
assert(extensionEventsPage.includes('data-testid="admin-extension-events-page" class="min-w-0 shrink-0"'), 'Extension event log page wrapper should not shrink inside the admin flex scroll container')
assert(typeof isAdminNavigationEntryActive === 'function', 'Admin navigation should expose an active-state helper')
assert(typeof shouldOpenAdminNavigationEntry === 'function', 'Admin navigation should expose an initial-open helper')
assert(typeof isExtensionAdminPageId === 'function', 'Admin navigation should identify dynamic extension admin pages')
assert(isExtensionAdminPageId('/extensions/sforum.default-theme/pages/about'), 'Dynamic extension admin page ids should be recognized')
assert(!isExtensionAdminPageId('/extensions/themes'), 'Static extension manager pages should not be treated as dynamic extension admin pages')
assert(isAdminNavigationEntryActive(extensionFolder, '/extensions/themes'), 'Extension folder should be active when one of its child pages is active')
// 动态扩展页归属由 admin shell 依据 manifest `group` 计算：声明了 group 的页有自己的
// 一级菜单/分区，未声明的仍归「扩展管理」。静态 config 拿不到 manifest，不能替它们
// 认领——否则 group 页会同时点亮自己的一级菜单和「扩展管理」。
assert(!isAdminNavigationEntryActive(extensionFolder, '/extensions/sforum.default-theme/pages/about'), 'Static admin config should not claim dynamic extension pages for the extension folder')
assert(adminLayout.includes('extensionPageSitsUnderExtensionsFolder('), 'Admin layout should delegate dynamic extension page ownership to the manifest group placement rule')
assert(/const isActive = isAdminNavigationEntryActive\(entry, currentAdminPageId\)[^\n]*ownsActiveExtensionPage/.test(adminLayout), 'Admin layout should highlight the extension folder for ungrouped dynamic extension pages')
assert(/entry\.labelKey === EXTENSION_FOLDER_NAV_VALUE\s*&&\s*activeExtensionPageIsFlat\.value/.test(adminLayout), 'The dynamic extension page fallback should be scoped to the extension folder')
assert(!isAdminNavigationEntryActive(systemFolder, '/extensions/themes'), 'System folder should not be active for extension child pages')
assert(isAdminNavigationEntryActive(operationsFolder, '/database'), 'Operations folder should be active when database page is active')
assert(isAdminNavigationEntryActive(operationsFolder, '/jobs'), 'Operations folder should be active when jobs page is active')
assert(!isAdminNavigationEntryActive(systemFolder, '/database'), 'System folder should not be active for operations child pages')
assert(shouldOpenAdminNavigationEntry(extensionFolder, '/extensions/themes'), 'Active sidebar folders should open initially')
assert(shouldOpenAdminNavigationEntry(operationsFolder, '/jobs'), 'Active operations folder should open initially')
assert(isAdminNavigationEntryActive(attachmentsFolder, '/attachments/settings'), 'Attachment folder should be active for Basic Configuration')
assert(isAdminNavigationEntryActive(attachmentsFolder, '/attachments/manager'), 'Attachment folder should be active for Attachment Management')
assert(shouldOpenAdminNavigationEntry(attachmentsFolder, '/attachments/manager'), 'Attachment folder should open for its active submenu')
assert(/open: shouldOpenAdminNavigationEntry\(entry, currentAdminPageId\)[^\n]*ownsActiveExtensionPage/.test(adminLayout), 'Admin layout should open the extension folder for ungrouped dynamic extension pages')
assert(!shouldOpenAdminNavigationEntry(systemFolder, '/extensions/themes'), 'Inactive sidebar folders should stay collapsed by default')
assert(firstSidebarGroup
  .filter(entry => entry.type === 'folder')
  .every(entry => entry.defaultOpen !== true), 'Top-level admin sidebar folders should not all be configured as default-open')
assert(adminLayout.includes('active: isActive'), 'Admin layout should pass active state to top-level folder items')
assert(adminLayout.includes('shouldOpenAdminNavigationEntry(entry, currentAdminPageId)'), 'Admin layout should open only the active/default folder initially')
assert(adminLayout.includes('adminTabs.activateTab(tabId)'), 'Admin layout should activate existing custom tabs from the current route')
assert(adminLayout.includes('openExtensionRoutePlaceholderTab(tabId)'), 'Admin layout should open route-backed placeholder tabs for dynamic extension pages')
assert(adminLayout.includes('overflow-y-auto'), 'Admin sidebar navigation should scroll independently when there are many menu items')
assert(adminLayout.includes('min-h-0') && adminLayout.includes('flex-1'), 'Admin sidebar navigation container should flex-fill and allow independent scrolling')
assert(adminLayout.includes('sforum-admin-tabs'), 'Admin layout should expose a dedicated multi-tab bar class')
assert(adminLayout.includes('shrink-0'), 'Admin multi-tabs should not shrink when many pages are open')
assert(adminLayout.includes('data-admin-tab-id'), 'Admin multi-tabs should expose stable tab ids for scroll-into-view')
assert(adminLayout.includes('scrollActiveAdminTabIntoView'), 'Admin multi-tabs should scroll the active tab into view')
assert(adminLayout.includes('whitespace-nowrap'), 'Admin multi-tab labels should stay on one line')
assert(adminLayout.includes('max-w-[12.5rem]'), 'Admin multi-tab labels should cap width and truncate long titles')

console.log('Admin framework validation passed.')
