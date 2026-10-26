export const ADMIN_DASHBOARD_PAGE_ID = '/'

export type AdminPermissionMode = 'all' | 'any'

export type AdminPageDefinition = {
  id: string
  labelKey: string
  icon: string
  componentName: string
  closable?: boolean
  badgeKey?: string
  requiredPermissions?: string[]
  permissionMode?: AdminPermissionMode
  /** 仅专业模式下出现在侧栏（默认关闭，面向日常运营隐藏扩展高级工具）。 */
  professionalMode?: boolean
  /** 仅运维管理开关打开时出现在侧栏（默认关闭）。 */
  operationsMode?: boolean
}

export type AdminNavigationPageEntry = {
  type: 'page'
  pageId: string
  badgeKey?: string
}

export type AdminNavigationFolderEntry = {
  type: 'folder'
  labelKey: string
  icon: string
  defaultOpen?: boolean
  /**
   * sectionId 把这个文件夹标记为「Core 注册的共享分区」：扩展 admin 页只要在
   * manifest 里声明 `group: <sectionId>`，就会作为子项并入这里，与 Core 自己的
   * 页面同处一个一级菜单。容器归 Core，成员由各自的所有者注册。
   */
  sectionId?: string
  children: readonly AdminNavigationEntry[]
}

export type AdminNavigationForumHomeEntry = {
  type: 'forum-home'
  labelKey: string
  icon: string
}

export type AdminNavigationEntry =
  | AdminNavigationPageEntry
  | AdminNavigationFolderEntry
  | AdminNavigationForumHomeEntry

export type AdminNavigationGroup = readonly AdminNavigationEntry[]

export const adminPageDefinitions = [
  {
    id: ADMIN_DASHBOARD_PAGE_ID,
    labelKey: 'admin.nav.dashboard',
    icon: 'i-lucide-layout-dashboard',
    componentName: 'AdminIndex',
    closable: false
  },
  {
    id: '/ai',
    labelKey: 'admin.nav.ai',
    icon: 'i-lucide-sparkles',
    componentName: 'AdminAI',
    requiredPermissions: ['ai.manage']
  },
  {
    id: '/users',
    labelKey: 'admin.nav.userManagement',
    icon: 'i-lucide-contact',
    componentName: 'AdminUsers',
    requiredPermissions: ['user.view', 'user.manage'],
    permissionMode: 'any'
  },
  {
    id: '/personalization',
    labelKey: 'admin.nav.personalization',
    icon: 'i-lucide-palette',
    componentName: 'AdminPersonalization',
    // 外观与前台壳合并到同一页；任一权限即可进入，页内再按 tab 过滤。
    requiredPermissions: ['settings.appearance.manage', 'settings.site.manage'],
    permissionMode: 'any'
  },
  {
    // 保留页面定义供旧链接 / 重定向；不挂侧栏。
    id: '/site-chrome',
    labelKey: 'admin.nav.siteChrome',
    icon: 'i-lucide-panel-top',
    componentName: 'AdminSiteChromeRedirect',
    requiredPermissions: ['settings.site.manage', 'settings.appearance.manage'],
    permissionMode: 'any'
  },
  {
    id: '/roles',
    labelKey: 'admin.nav.userGroups',
    icon: 'i-lucide-shield-check',
    componentName: 'AdminRoles',
    badgeKey: 'admin.nav.rolesBadge',
    requiredPermissions: ['role.manage']
  },
  {
    id: '/permissions',
    labelKey: 'admin.nav.permissionManagement',
    icon: 'i-lucide-shield-check',
    componentName: 'AdminPermissions',
    requiredPermissions: ['role.manage', 'user.view', 'user.manage'],
    permissionMode: 'any'
  },
  {
    id: '/settings',
    labelKey: 'admin.nav.settings',
    icon: 'i-lucide-settings-2',
    componentName: 'AdminSettings',
    requiredPermissions: ['settings.site.manage']
  },
  {
    id: '/settings/mail',
    labelKey: 'admin.nav.mailSettings',
    icon: 'i-lucide-mail',
    componentName: 'AdminMailSettings',
    requiredPermissions: ['settings.mail.manage', 'settings.notifications.manage'],
    permissionMode: 'any'
  },
  {
    // 保留旧通知设置地址供书签兼容；统一入口位于 /settings/mail。
    id: '/settings/notifications',
    labelKey: 'admin.nav.notificationSettings',
    icon: 'i-lucide-bell-ring',
    componentName: 'AdminNotificationSettings',
    requiredPermissions: ['settings.notifications.manage']
  },
  {
    id: '/settings/login-methods',
    labelKey: 'admin.nav.loginMethods',
    icon: 'i-lucide-log-in',
    componentName: 'AdminLoginMethods',
    requiredPermissions: ['identity.provider.manage']
  },
  {
    id: '/settings/avatar',
    labelKey: 'admin.nav.avatarSettings',
    icon: 'i-lucide-user-round-cog',
    componentName: 'AdminAvatarSettings',
    requiredPermissions: ['settings.avatar.manage']
  },
  {
    id: '/settings/features',
    labelKey: 'admin.nav.featureFlags',
    icon: 'i-lucide-toggle-left',
    componentName: 'AdminFeatureFlags',
    requiredPermissions: ['settings.site.manage'],
    professionalMode: true
  },
  {
    id: '/entity-meta',
    labelKey: 'admin.nav.entityMeta',
    icon: 'i-lucide-tags',
    componentName: 'AdminEntityMeta',
    requiredPermissions: ['entity_meta.manage', 'settings.manage'],
    permissionMode: 'any',
    professionalMode: true
  },
  {
    id: '/moderation',
    labelKey: 'admin.nav.moderation',
    icon: 'i-lucide-shield-alert',
    componentName: 'AdminModeration',
    requiredPermissions: ['moderation.manage']
  },
  {
    id: '/seo',
    labelKey: 'admin.nav.seo',
    icon: 'i-lucide-search',
    componentName: 'AdminSeo',
    requiredPermissions: ['seo.manage']
  },
  {
    id: '/database',
    labelKey: 'admin.nav.database',
    icon: 'i-lucide-database',
    componentName: 'AdminDatabase',
    requiredPermissions: ['database.manage'],
    operationsMode: true
  },
  {
    id: '/attachments',
    labelKey: 'admin.nav.attachments',
    icon: 'i-lucide-paperclip',
    componentName: 'AdminAttachmentsRedirect',
    requiredPermissions: ['attachment.settings.manage', 'attachment.upload_policy.manage', 'attachment.manage'],
    permissionMode: 'any'
  },
  {
    id: '/attachments/settings',
    labelKey: 'admin.attachments.configuration',
    icon: 'i-lucide-sliders-horizontal',
    componentName: 'AdminAttachmentSettings',
    requiredPermissions: ['attachment.settings.manage', 'attachment.upload_policy.manage'],
    permissionMode: 'any'
  },
  {
    id: '/attachments/manager',
    labelKey: 'admin.attachments.tabs.manager',
    icon: 'i-lucide-folder-search',
    componentName: 'AdminAttachmentManager',
    requiredPermissions: ['attachment.manage']
  },
  {
    id: '/forum/categories',
    labelKey: 'admin.nav.forumCategories',
    icon: 'i-lucide-folder-tree',
    componentName: 'AdminForumCategories',
    requiredPermissions: ['category.manage']
  },
  {
    id: '/forum/tags',
    labelKey: 'admin.nav.forumTags',
    icon: 'i-lucide-tags',
    componentName: 'AdminForumTags',
    requiredPermissions: ['tag.manage']
  },
  {
    id: '/forum/settings',
    labelKey: 'admin.nav.forumSettings',
    icon: 'i-lucide-sliders-horizontal',
    componentName: 'AdminForumSettings',
    requiredPermissions: ['category.manage', 'tag.manage', 'forum.settings.manage', 'search.manage'],
    permissionMode: 'any'
  },
  {
    id: '/forum/content',
    labelKey: 'admin.nav.forumContent',
    icon: 'i-lucide-files',
    componentName: 'AdminForumContent',
    requiredPermissions: ['topic.edit_any', 'topic.revision.view_any', 'post.edit_any', 'post.revision.view_any'],
    permissionMode: 'any'
  },
  {
    id: '/extensions',
    labelKey: 'admin.nav.extensionOverview',
    icon: 'i-lucide-layout-dashboard',
    componentName: 'AdminExtensions',
    requiredPermissions: ['extension.view', 'extension.plugin.manage', 'extension.theme.manage'],
    permissionMode: 'any'
  },
  {
    id: '/extensions/install',
    labelKey: 'admin.nav.extensionInstall',
    icon: 'i-lucide-upload',
    componentName: 'AdminExtensionInstall',
    requiredPermissions: ['extension.plugin.manage', 'extension.theme.manage'],
    permissionMode: 'any'
  },
  {
    id: '/extensions/plugins',
    labelKey: 'admin.nav.extensionPlugins',
    icon: 'i-lucide-plug',
    componentName: 'AdminExtensionPlugins',
    requiredPermissions: ['extension.plugin.manage', 'extension.view'],
    permissionMode: 'any'
  },
  {
    id: '/extensions/themes',
    labelKey: 'admin.nav.extensionThemes',
    icon: 'i-lucide-palette',
    componentName: 'AdminExtensionThemes',
    requiredPermissions: ['extension.theme.manage', 'extension.view'],
    permissionMode: 'any'
  },
  {
    id: '/extensions/settings',
    labelKey: 'admin.nav.extensionSettings',
    icon: 'i-lucide-sliders-horizontal',
    componentName: 'AdminExtensionSettings',
    requiredPermissions: ['extension.plugin.manage'],
    professionalMode: true
  },
  {
    id: '/extensions/events',
    labelKey: 'admin.nav.extensionEvents',
    icon: 'i-lucide-scroll-text',
    componentName: 'AdminExtensionEvents',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/contributions',
    labelKey: 'admin.nav.extensionContributions',
    icon: 'i-lucide-blocks',
    componentName: 'AdminExtensionContributions',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/pages',
    labelKey: 'admin.nav.extensionPages',
    icon: 'i-lucide-layout-template',
    componentName: 'AdminExtensionPages',
    requiredPermissions: ['extension.view', 'extension.theme.manage'],
    permissionMode: 'any',
    professionalMode: true
  },
  {
    id: '/extensions/route-catalog',
    labelKey: 'admin.nav.extensionRouteCatalog',
    icon: 'i-lucide-table-2',
    componentName: 'AdminExtensionRouteCatalog',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/query-catalog',
    labelKey: 'admin.nav.extensionQueryCatalog',
    icon: 'i-lucide-database-zap',
    componentName: 'AdminExtensionQueryCatalog',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/route-providers',
    labelKey: 'admin.nav.extensionRouteProviders',
    icon: 'i-lucide-route',
    componentName: 'AdminExtensionRouteProviders',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/route-inspector',
    labelKey: 'admin.nav.extensionRouteInspector',
    icon: 'i-lucide-scan-search',
    componentName: 'AdminExtensionRouteInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/cache-inspector',
    labelKey: 'admin.nav.extensionCacheInspector',
    icon: 'i-lucide-database-zap',
    componentName: 'AdminExtensionCacheInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/asset-inspector',
    labelKey: 'admin.nav.extensionAssetInspector',
    icon: 'i-lucide-package',
    componentName: 'AdminExtensionAssetInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/template-inspector',
    labelKey: 'admin.nav.extensionTemplateInspector',
    icon: 'i-lucide-layout-template',
    componentName: 'AdminExtensionTemplateInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/component-inspector',
    labelKey: 'admin.nav.extensionComponentInspector',
    icon: 'i-lucide-boxes',
    componentName: 'AdminExtensionComponentInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/navigation-inspector',
    labelKey: 'admin.nav.extensionNavigationInspector',
    icon: 'i-lucide-map',
    componentName: 'AdminExtensionNavigationInspector',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/registry-catalogs',
    labelKey: 'admin.nav.extensionRegistryCatalogs',
    icon: 'i-lucide-library',
    componentName: 'AdminExtensionRegistryCatalogs',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/extensions/provider-slots',
    labelKey: 'admin.nav.extensionProviderSlots',
    icon: 'i-lucide-waypoints',
    componentName: 'AdminExtensionProviderSlots',
    requiredPermissions: ['extension.view'],
    professionalMode: true
  },
  {
    id: '/search',
    labelKey: 'admin.nav.search',
    icon: 'i-lucide-search',
    componentName: 'AdminSearch',
    requiredPermissions: ['search.manage']
  },
  {
    id: '/jobs',
    labelKey: 'admin.nav.jobs',
    icon: 'i-lucide-list-checks',
    componentName: 'AdminJobs',
    requiredPermissions: ['jobs.view'],
    operationsMode: true
  },
  {
    id: '/schedules',
    labelKey: 'admin.nav.schedules',
    icon: 'i-lucide-calendar-clock',
    componentName: 'AdminSchedules',
    requiredPermissions: ['jobs.view'],
    operationsMode: true
  },
  {
    id: '/webhooks',
    labelKey: 'admin.nav.webhooks',
    icon: 'i-lucide-webhook',
    componentName: 'AdminWebhooks',
    requiredPermissions: ['settings.manage', 'settings.site.manage'],
    permissionMode: 'any',
    operationsMode: true
  }
] as const satisfies readonly AdminPageDefinition[]

export const adminSidebarNavigation = [
  [
    { type: 'page', pageId: ADMIN_DASHBOARD_PAGE_ID },
    {
      type: 'folder',
      labelKey: 'admin.nav.userPermission',
      icon: 'i-lucide-user-cog',
      children: [
        { type: 'page', pageId: '/users' },
        { type: 'page', pageId: '/roles' },
        { type: 'page', pageId: '/permissions' }
      ]
    },
    {
      type: 'folder',
      labelKey: 'admin.nav.forum',
      icon: 'i-lucide-message-square-text',
      children: [
        { type: 'page', pageId: '/moderation' },
        { type: 'page', pageId: '/forum/categories' },
        { type: 'page', pageId: '/forum/tags' },
        { type: 'page', pageId: '/forum/settings' },
        { type: 'page', pageId: '/forum/content' }
      ]
    },
    {
      type: 'folder',
      labelKey: 'admin.nav.system',
      icon: 'i-lucide-settings-2',
      children: [
        { type: 'page', pageId: '/settings' },
        { type: 'page', pageId: '/settings/mail' },
        { type: 'page', pageId: '/settings/login-methods' },
        { type: 'page', pageId: '/settings/avatar' },
        { type: 'page', pageId: '/settings/features' },
        { type: 'page', pageId: '/entity-meta' },
        { type: 'page', pageId: '/personalization' },
        { type: 'page', pageId: '/seo' },
        { type: 'page', pageId: '/search' }
      ]
    },
    {
      // 「AI 能力」是一级菜单：Core 的 AI 控制台在这里，插件把 manifest 的
      // admin.pages[].group 写成 "ai-tools" 就能把自己的设置页并进来。
      // 容器归 Core（label/icon/位置），成员归各自的所有者。
      type: 'folder',
      labelKey: 'admin.nav.aiTools',
      icon: 'i-lucide-sparkles',
      sectionId: 'ai-tools',
      children: [
        { type: 'page', pageId: '/ai' }
      ]
    },
    {
      type: 'folder',
      labelKey: 'admin.nav.extensions',
      icon: 'i-lucide-blocks',
      children: [
        { type: 'page', pageId: '/extensions' },
        { type: 'page', pageId: '/extensions/install' },
        { type: 'page', pageId: '/extensions/plugins' },
        { type: 'page', pageId: '/extensions/themes' },
        { type: 'page', pageId: '/extensions/pages' },
        { type: 'page', pageId: '/extensions/route-catalog' },
        { type: 'page', pageId: '/extensions/query-catalog' },
        { type: 'page', pageId: '/extensions/route-providers' },
        { type: 'page', pageId: '/extensions/route-inspector' },
        { type: 'page', pageId: '/extensions/cache-inspector' },
        { type: 'page', pageId: '/extensions/asset-inspector' },
        { type: 'page', pageId: '/extensions/template-inspector' },
        { type: 'page', pageId: '/extensions/component-inspector' },
        { type: 'page', pageId: '/extensions/navigation-inspector' },
        { type: 'page', pageId: '/extensions/registry-catalogs' },
        { type: 'page', pageId: '/extensions/provider-slots' },
        { type: 'page', pageId: '/extensions/settings' },
        { type: 'page', pageId: '/extensions/events' },
        { type: 'page', pageId: '/extensions/contributions' }
      ]
    },
    {
      // 运维工具不属于配置项，放在扩展管理之后
      type: 'folder',
      labelKey: 'admin.nav.operations',
      icon: 'i-lucide-server-cog',
      children: [
        { type: 'page', pageId: '/database' },
        { type: 'page', pageId: '/jobs' },
        { type: 'page', pageId: '/schedules' },
        { type: 'page', pageId: '/webhooks' }
      ]
    },
    {
      type: 'folder',
      labelKey: 'admin.nav.attachments',
      icon: 'i-lucide-paperclip',
      children: [
        { type: 'page', pageId: '/attachments/settings' },
        { type: 'page', pageId: '/attachments/manager' }
      ]
    }
  ],
  [
    {
      type: 'forum-home',
      labelKey: 'admin.nav.forumHome',
      icon: 'i-lucide-house'
    }
  ]
] as const satisfies readonly AdminNavigationGroup[]

export function normalizeAdminPageId(id?: string | null) {
  const value = `${id || ''}`.trim()

  if (!value || value === '/') {
    return ADMIN_DASHBOARD_PAGE_ID
  }

  return `/${value.replace(/^\/+|\/+$/g, '')}`
}

export function isExtensionAdminPageId(id?: string | null) {
  const pageId = normalizeAdminPageId(id)
  return /^\/extensions\/[^/]+\/pages(?:\/|$)/.test(pageId)
}

export function findAdminPageDefinition(id: string): AdminPageDefinition | undefined {
  const pageId = normalizeAdminPageId(id)
  return adminPageDefinitions.find(page => page.id === pageId)
}

export function requireAdminPageDefinition(id: string): AdminPageDefinition {
  const page = findAdminPageDefinition(id)

  if (!page) {
    throw new Error(`Unknown admin page: ${id}`)
  }

  return page
}

export function isAdminNavigationEntryActive(entry: AdminNavigationEntry, pageId?: string | null): boolean {
  const activePageId = normalizeAdminPageId(pageId)

  if (entry.type === 'page') {
    return normalizeAdminPageId(entry.pageId) === activePageId
  }

  if (entry.type === 'folder') {
    // 扩展页归哪个菜单由 admin shell 依据 manifest group 计算，静态 config 不猜测。
    return entry.children.some(child => isAdminNavigationEntryActive(child, activePageId))
  }

  return false
}

export function shouldOpenAdminNavigationEntry(entry: AdminNavigationEntry, pageId?: string | null): boolean {
  return entry.type === 'folder' && (entry.defaultOpen === true || isAdminNavigationEntryActive(entry, pageId))
}

export function canAccessAdminPage(page: AdminPageDefinition, can: (permission: string) => boolean) {
  const permissions = page.requiredPermissions ?? []

  if (permissions.length === 0) {
    return true
  }

  if (page.permissionMode === 'any') {
    return permissions.some(permission => can(permission))
  }

  return permissions.every(permission => can(permission))
}

/** 侧栏可见性偏好（系统高级设置 Modal 开关，默认均关闭）。 */
export type AdminNavVisibility = {
  professionalMode: boolean
  operationsMode: boolean
}

/** 侧栏是否应展示该页面：权限 + 高级设置偏好。 */
export function shouldShowAdminPageInNav(
  page: AdminPageDefinition,
  can: (permission: string) => boolean,
  visibility: AdminNavVisibility | boolean
) {
  // 兼容旧调用：第三个参数曾是 professionalMode 布尔值。
  const flags: AdminNavVisibility = typeof visibility === 'boolean'
    ? { professionalMode: visibility, operationsMode: true }
    : visibility

  if (!canAccessAdminPage(page, can)) {
    return false
  }
  if (page.professionalMode && !flags.professionalMode) {
    return false
  }
  if (page.operationsMode && !flags.operationsMode) {
    return false
  }
  return true
}

/**
 * Core 注册的共享一级菜单分区 id。
 *
 * 扩展在 manifest 里写 `admin.pages[].group: "<id>"` 就并入对应的 Core 一级菜单；
 * 未注册的 group 保持旧行为（按扩展各自成组）。容器（label/icon/位置）归 Core，
 * 成员归各自的所有者——Core 不硬编码任何插件 id。
 *
 * 必须与 `adminSidebarNavigation` 里带 `sectionId` 的文件夹保持一致，由
 * `tests/admin/adminAiSectionNav.test.ts` 钉住。
 */
export const ADMIN_NAV_SECTION_IDS: readonly string[] = ['ai-tools']
