/** Site chrome：导航 / 友情链接 / 公告 的公开与管理 API。 */

export type SiteNavItem = {
  id: number
  labelZhCN: string
  labelEnUS: string
  href: string
  openInNewTab: boolean
  position: number
  enabled: boolean
  createdAt: string
  updatedAt: string
}

/** forum.nav.items 宿主描述符（E2.3）；公开顶栏在运营项之后合并 */
export type SiteExtensionNavItem = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  kind: 'hostLink' | 'extensionRoute'
  method?: string
  url: string
}

export type SitePublicNav = {
  items: SiteNavItem[]
  extensionItems?: SiteExtensionNavItem[]
}

export type SiteFriendLink = {
  id: number
  name: string
  url: string
  description: string
  logoUrl: string
  position: number
  enabled: boolean
  createdAt: string
  updatedAt: string
}

export type SiteAnnouncementStyle = 'info' | 'success' | 'warning' | 'danger'

export type SiteAnnouncement = {
  id: number
  titleZhCN: string
  titleEnUS: string
  bodyZhCN: string
  bodyEnUS: string
  bodyHtmlZhCN: string
  bodyHtmlEnUS: string
  style: SiteAnnouncementStyle
  href: string
  dismissible: boolean
  position: number
  enabled: boolean
  startsAt?: string | null
  endsAt?: string | null
  createdAt: string
  updatedAt: string
}

export type SiteNavItemInput = {
  labelZhCN: string
  labelEnUS: string
  href: string
  openInNewTab?: boolean
  position?: number
  enabled?: boolean
}

export type SiteNavigationLocation = 'public.topbar.primary' | 'public.sidebar.primary' | 'public.mobile.primary' | 'public.footer.primary'
export type SiteNavigationSourceKind = 'core' | 'operator' | 'extension' | 'dynamic'
export type SiteNavigationLinkKind = 'coreRoute' | 'internalLink' | 'externalLink' | 'extensionHostLink' | 'extensionRoute' | 'dynamicBlock'
export type SiteNavigationVisibility = 'public' | 'anonymous' | 'authenticated' | 'permission'

export type SiteNavigationDefinition = {
  sourceKey: string
  sourceKind: SiteNavigationSourceKind
  linkKind: SiteNavigationLinkKind
  labelZhCN?: string
  labelEnUS?: string
  href?: string
  icon?: string
  openInNewTab?: boolean
  extensionId?: string
  contributionId?: string
}

export type SiteNavigationPlacement = {
  sourceKey: string
  location: SiteNavigationLocation
  order: number
  enabled: boolean
  visibility: SiteNavigationVisibility
  permission?: string
  labelZhCN?: string
  labelEnUS?: string
  icon?: string
  iconHidden?: boolean
  maxItems?: number
}

export type SiteNavigationThemeLocation = { location: SiteNavigationLocation, supported: boolean }
export type SiteNavigationDocument = {
  revision: number
  definitions: SiteNavigationDefinition[]
  placements: SiteNavigationPlacement[]
  themeLocations: SiteNavigationThemeLocation[]
}

export type SiteNavigationImportMode = 'merge' | 'replace'

export type SiteNavigationBackup = {
  schema: 'sforum.site-navigation-backup@1'
  exportedAt?: string
  definitions: SiteNavigationDefinition[]
  placements: SiteNavigationPlacement[]
}

export type SiteNavigationPreview = {
  previewToken: string
  expectedRevision: number
  mode: 'defaults' | SiteNavigationImportMode
  changes: string[]
  warnings: string[]
  changeEntries?: Array<{
    kind: 'location' | 'definitions'
    location?: SiteNavigationLocation
    beforeCount: number
    afterCount: number
  }>
  warningEntries?: Array<{
    code: 'extension_reference_inert'
    sourceKey?: string
    extensionId?: string
  }>
}

export type SiteNavigationSnapshot = {
  id: number
  revision: number
  operation: string
  reason?: string
  affectedLocations: SiteNavigationLocation[]
  createdAt: string
  actorUserId?: number | null
  document?: SiteNavigationDocument
}

export type SiteFriendLinkInput = {
  name: string
  url: string
  description?: string
  logoUrl?: string
  position?: number
  enabled?: boolean
}

export type SiteAnnouncementInput = {
  titleZhCN?: string
  titleEnUS?: string
  bodyZhCN?: string
  bodyEnUS?: string
  style?: SiteAnnouncementStyle
  href?: string
  dismissible?: boolean
  position?: number
  enabled?: boolean
  startsAt?: string | null
  endsAt?: string | null
}

export function useSiteChromeApi() {
  const { request } = useApiClient()

  return {
    listPublicNav: () => request<SitePublicNav>('/site/nav-items'),
    listAdminNavItems: () => request<SiteNavItem[]>('/admin/site/nav-items'),
    createNavItem: (body: SiteNavItemInput) =>
      request<SiteNavItem>('/admin/site/nav-items', { method: 'POST', body }),
    updateNavItem: (id: number, body: Partial<SiteNavItemInput>) =>
      request<SiteNavItem>(`/admin/site/nav-items/${id}`, { method: 'PATCH', body }),
    deleteNavItem: (id: number) =>
      request<{ deleted: boolean }>(`/admin/site/nav-items/${id}`, { method: 'DELETE' }),

    getAdminNavigation: () => request<SiteNavigationDocument>('/admin/site/navigation'),
    applyAdminNavigation: (body: { expectedRevision: number, reason?: string, document: SiteNavigationDocument }) =>
      request<SiteNavigationDocument>('/admin/site/navigation/apply', { method: 'POST', body }),
    previewNavigationDefaults: (body: { expectedRevision: number, scope: 'location' | 'all', location?: SiteNavigationLocation }) =>
      request<SiteNavigationPreview>('/admin/site/navigation/defaults/preview', { method: 'POST', body }),
    applyNavigationDefaults: (body: { expectedRevision: number, previewToken: string, reason?: string }) =>
      request<SiteNavigationDocument>('/admin/site/navigation/defaults/apply', { method: 'POST', body }),
    listNavigationSnapshots: () => request<SiteNavigationSnapshot[]>('/admin/site/navigation/snapshots'),
    getNavigationSnapshot: (id: number) => request<SiteNavigationSnapshot>(`/admin/site/navigation/snapshots/${id}`),
    restoreNavigationSnapshot: (id: number, body: { expectedRevision: number, reason?: string }) =>
      request<SiteNavigationDocument>(`/admin/site/navigation/snapshots/${id}/restore`, { method: 'POST', body }),
    exportNavigationBackup: () => request<SiteNavigationBackup>('/admin/site/navigation/export'),
    previewNavigationImport: (body: { expectedRevision: number, mode: SiteNavigationImportMode, backup: SiteNavigationBackup }) =>
      request<SiteNavigationPreview>('/admin/site/navigation/import/preview', { method: 'POST', body }),
    applyNavigationImport: (body: { expectedRevision: number, previewToken: string, reason?: string }) =>
      request<SiteNavigationDocument>('/admin/site/navigation/import/apply', { method: 'POST', body }),

    listPublicFriendLinks: () => request<SiteFriendLink[]>('/site/friend-links'),
    listAdminFriendLinks: () => request<SiteFriendLink[]>('/admin/site/friend-links'),
    createFriendLink: (body: SiteFriendLinkInput) =>
      request<SiteFriendLink>('/admin/site/friend-links', { method: 'POST', body }),
    updateFriendLink: (id: number, body: Partial<SiteFriendLinkInput>) =>
      request<SiteFriendLink>(`/admin/site/friend-links/${id}`, { method: 'PATCH', body }),
    deleteFriendLink: (id: number) =>
      request<{ deleted: boolean }>(`/admin/site/friend-links/${id}`, { method: 'DELETE' }),

    listPublicAnnouncements: () => request<SiteAnnouncement[]>('/site/announcements'),
    listAdminAnnouncements: () => request<SiteAnnouncement[]>('/admin/site/announcements'),
    createAnnouncement: (body: SiteAnnouncementInput) =>
      request<SiteAnnouncement>('/admin/site/announcements', { method: 'POST', body }),
    updateAnnouncement: (id: number, body: SiteAnnouncementInput) =>
      request<SiteAnnouncement>(`/admin/site/announcements/${id}`, { method: 'PATCH', body }),
    deleteAnnouncement: (id: number) =>
      request<{ deleted: boolean }>(`/admin/site/announcements/${id}`, { method: 'DELETE' })
  }
}
