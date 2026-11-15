import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

import {
  emptyPublicNavigation,
  isCoreDynamicCategories,
  isExternalNavigationItem,
  isInternalNavigationItem,
  limitDynamicNavigationItems,
  PUBLIC_NAVIGATION_LOCATIONS,
  PUBLIC_NAVIGATION_SCHEMA,
  publicNavigationItems,
  renderablePublicNavigationItems,
  type PublicNavigationDocument,
  type PublicNavigationItem
} from '../../app/utils/navigation/publicNavigation'

const source = (relative: string) => readFileSync(new URL(relative, import.meta.url), 'utf8')
const composableSource = source('../../app/composables/navigation/usePublicNavigation.ts')
const navbarSource = source('../../app/components/SFNavbar.vue')
const linksSource = source('../../app/components/navigation/SFPublicNavigationLinks.vue')
const mobileSource = source('../../app/components/navigation/SFPublicMobileNavigation.vue')
const sidebarSource = source('../../app/components/forum/SFHomeNavigation.vue')
const sidebarContentSource = source('../../app/components/forum/navigation/SFPublicSidebarContent.vue')
const categoryBlockSource = source('../../app/components/forum/navigation/SFCategoryNavigationBlock.vue')
const mobileSidebarSource = source('../../app/components/forum/navigation/SFMobileSidebarContent.vue')
const responsiveSidebarSource = source('../../app/components/forum/navigation/SFResponsivePublicSidebar.vue')
const sidebarDrawerSource = source('../../app/composables/navigation/usePublicSidebarDrawer.ts')
const footerSource = source('../../app/components/SFFooter.vue')
const pageSidebarSources = [
  '../../app/components/forum/SFHomePage.vue',
  '../../app/components/forum/SFCategoryIndexPage.vue',
  '../../app/components/forum/SFCategoryShowPage.vue',
  '../../app/components/forum/SFTagIndexPage.vue',
  '../../app/components/forum/SFTagShowPage.vue',
  '../../app/components/forum/SFTopicComposerPage.vue',
  '../../app/components/forum/SFTopicEditPage.vue',
  '../../app/components/forum/SFTopicShowPage.vue',
  '../../app/components/profile/SFProfileShowPage.vue',
  // 账号设置三栏页的左栏所有者是侧栏组件本身（插件公开页的
  // sf-account-settings-sidebar 岛复用同一份），不再是 SFSettingsShell。
  '../../app/components/settings/SFSettingsAccountSidebar.vue',
  '../../app/components/notifications/SFNotificationsPage.vue',
  '../../app/components/notifications/detail/SFNotificationDetailPage.vue',
  '../../app/components/moderation/SFModerationReviewPage.vue',
  '../../app/components/errors/SFSystemErrorSidebar.vue'
].map(source)

function item(overrides: Partial<PublicNavigationItem> = {}): PublicNavigationItem {
  return {
    sourceKey: 'core.home',
    sourceKind: 'core',
    linkKind: 'coreRoute',
    label: 'Home',
    href: '/',
    ...overrides
  }
}

describe('public navigation document utilities', () => {
  test('selects only the requested canonical location and defaults missing locations to empty', () => {
    const document: PublicNavigationDocument = {
      schemaVersion: PUBLIC_NAVIGATION_SCHEMA,
      revision: 7,
      locations: [
        { location: PUBLIC_NAVIGATION_LOCATIONS.topbar, supported: true, items: [item()] },
        {
          location: PUBLIC_NAVIGATION_LOCATIONS.mobile,
          supported: true,
          items: [item({ sourceKey: 'operator.help', label: 'Help', href: '/help' })]
        }
      ]
    }

    expect(publicNavigationItems(document, PUBLIC_NAVIGATION_LOCATIONS.topbar).map(entry => entry.sourceKey)).toEqual(['core.home'])
    expect(publicNavigationItems(document, PUBLIC_NAVIGATION_LOCATIONS.mobile).map(entry => entry.sourceKey)).toEqual(['operator.help'])
    expect(publicNavigationItems(document, 'public.footer.primary')).toEqual([])
    expect(publicNavigationItems(null, PUBLIC_NAVIGATION_LOCATIONS.topbar)).toEqual([])
  })

  test('provides a schema-valid, fail-closed empty document', () => {
    expect(emptyPublicNavigation()).toEqual({
      schemaVersion: PUBLIC_NAVIGATION_SCHEMA,
      revision: 0,
      locations: []
    })
  })

  test('accepts safe internal and HTTP(S) external destinations only', () => {
    expect(isInternalNavigationItem(item({ href: '/categories' }))).toBe(true)
    expect(isInternalNavigationItem(item({ linkKind: 'extensionRoute', href: '/extensions/demo' }))).toBe(true)
    expect(isInternalNavigationItem(item({ href: '//evil.example/path' }))).toBe(false)
    expect(isInternalNavigationItem(item({ href: 'relative/path' }))).toBe(false)
    expect(isExternalNavigationItem(item({ linkKind: 'externalLink', href: 'https://example.com/docs' }))).toBe(true)
    expect(isExternalNavigationItem(item({ linkKind: 'externalLink', href: 'http://example.com' }))).toBe(true)
    expect(isExternalNavigationItem(item({ linkKind: 'externalLink', href: 'javascript:alert(1)' }))).toBe(false)
    expect(isExternalNavigationItem(item({ linkKind: 'externalLink', href: '//example.com' }))).toBe(false)
  })

  test('fails closed for dynamic blocks, unsafe URLs, and blank labels', () => {
    const validInternal = item()
    const validExternal = item({
      sourceKey: 'operator.docs',
      linkKind: 'externalLink',
      href: 'https://example.com',
      label: 'Docs'
    })
    const filtered = renderablePublicNavigationItems([
      validInternal,
      validExternal,
      item({ sourceKey: 'dynamic.categories', sourceKind: 'dynamic', linkKind: 'dynamicBlock', href: '/categories' }),
      item({ sourceKey: 'unsafe', href: '//evil.example' }),
      item({ sourceKey: 'blank', label: '   ' })
    ])

    expect(filtered).toEqual([validInternal, validExternal])
  })

  test('recognizes only the bounded Core category block', () => {
    expect(isCoreDynamicCategories(item({
      sourceKey: 'core.dynamic.categories',
      sourceKind: 'dynamic',
      linkKind: 'dynamicBlock',
      href: undefined
    }))).toBe(true)
    expect(isCoreDynamicCategories(item({
      sourceKey: 'plugin.dynamic.categories',
      sourceKind: 'extension',
      linkKind: 'dynamicBlock',
      href: undefined
    }))).toBe(false)
    expect(isCoreDynamicCategories(item({
      sourceKey: 'core.dynamic.categories',
      sourceKind: 'dynamic',
      linkKind: 'coreRoute'
    }))).toBe(false)
  })

  test('limits dynamic categories while retaining the selected category', () => {
    const categories = Array.from({ length: 6 }, (_, index) => ({ slug: `category-${index + 1}` }))

    expect(limitDynamicNavigationItems(categories, 0)).toEqual(categories)
    expect(limitDynamicNavigationItems(categories, undefined)).toEqual(categories)
    expect(limitDynamicNavigationItems(categories, 3).map(item => item.slug)).toEqual(['category-1', 'category-2', 'category-3'])
    expect(limitDynamicNavigationItems(categories, 3, 'category-6').map(item => item.slug)).toEqual(['category-1', 'category-2', 'category-6'])
  })
})

describe('public navigation fetch and renderer contracts', () => {
  test('uses one responsive page-sidebar owner across desktop and mobile', () => {
    for (const pageSource of pageSidebarSources) {
      expect(pageSource).toContain('<SFResponsivePublicSidebar')
      expect(pageSource).not.toContain('sforum-mobile-drawer--left')
      expect(pageSource).not.toContain('forum-mobile-menu-open')
      expect(pageSource).not.toContain('public-mobile-navigation-open')
      expect(pageSource).not.toContain('system-error-mobile-menu-open')
    }

    expect(sidebarDrawerSource).toContain("useState<boolean>('public-sidebar-drawer-open'")
    expect(sidebarDrawerSource).toContain("useState<PublicSidebarOwner | null>('public-sidebar-drawer-owner'")
    expect(sidebarDrawerSource).toContain('owner.value?.token !== token')
    expect(responsiveSidebarSource).toContain('claimOwner(props.ownerId, ownerToken)')
    expect(responsiveSidebarSource).toContain('releaseOwner(ownerToken)')
    expect(responsiveSidebarSource).toContain('<slot />')
    expect(responsiveSidebarSource).toContain("'sforum-mobile-drawer sforum-mobile-drawer--left': open")
    expect(navbarSource).toContain('mobileMenuOpen && !hasPageSidebarOwner')
  })

  test('uses one canonical request for the three rendered public locations', () => {
    expect(composableSource.match(/request<PublicNavigationDocument>/g)?.length).toBe(1)
    expect(composableSource).toContain('`/site/navigation?locations=${encodeURIComponent(requestedLocations)}`')
    expect(composableSource).toContain('PUBLIC_NAVIGATION_LOCATIONS.topbar')
    expect(composableSource).toContain('PUBLIC_NAVIGATION_LOCATIONS.sidebar')
    expect(composableSource).not.toContain('PUBLIC_NAVIGATION_LOCATIONS.mobile')
    expect(composableSource).toContain('PUBLIC_NAVIGATION_LOCATIONS.footer')
    expect(composableSource).toContain('const sidebarItems = computed')
    expect(composableSource).toContain('const footerItems = computed')
    expect(composableSource).not.toContain('/site/nav-items')
  })

  test('isolates async data by locale and authoritative actor attributes', () => {
    expect(composableSource).toContain("if (!user.value) return 'guest'")
    expect(composableSource).toContain('user.value.id')
    expect(composableSource).toContain('user.value.status')
    expect(composableSource).toContain('user.value.roleKeys')
    expect(composableSource).toContain('user.value.permissions')
    expect(composableSource).toContain('`site-public-navigation:${locale.value}:${actorKey.value}`')
  })

  test('returns before useAsyncData when remote chrome is disabled', () => {
    const disabledBranch = composableSource.slice(
      composableSource.indexOf('if (!enabled)'),
      composableSource.indexOf('const { data, pending, refresh } = useAsyncData')
    )
    expect(disabledBranch).toContain('return {')
    expect(disabledBranch).toContain('topbarItems: computed(() => [])')
    expect(disabledBranch).toContain('sidebarItems: computed(() => [])')
    expect(disabledBranch).toContain('footerItems: computed(() => [])')
    expect(disabledBranch).not.toContain('request<PublicNavigationDocument>')
  })

  test('fails closed and disables shared SSR cache after request failure', () => {
    expect(composableSource).toContain('disableSharedDocumentCache()')
    expect(composableSource).toContain('return emptyState(true)')
    expect(composableSource).toContain('disableSharedPageCacheForPageResolve')
  })

  test('bounds topbar overflow and makes external links safe', () => {
    expect(linksSource).toContain('visibleLimit: 4')
    expect(linksSource).toContain('safeItems.value.slice(0, props.visibleLimit)')
    expect(linksSource).toContain('safeItems.value.slice(props.visibleLimit)')
    expect(linksSource).toContain('<UDropdownMenu')
    expect(linksSource).toContain("t('nav.more')")
    expect(linksSource).toContain('target="_blank"')
    expect(linksSource).toContain('rel="noopener noreferrer"')
  })

  test('renders the sidebar links and dynamic categories in the mobile drawer', () => {
    expect(linksSource).toContain("props.mode === 'topbar'")
    expect(linksSource).toContain(':class="`sf-public-navigation-links--${mode}`"')
    expect(navbarSource).toContain('const { topbarItems, sidebarItems } = usePublicNavigation')
    expect(navbarSource).toContain(':items="visibleSidebarItems"')
    expect(navbarSource).not.toContain('mobileItems')
    expect(mobileSource).toContain('data-navigation-location="public.sidebar.primary"')
    expect(mobileSource).toContain('data-navigation-viewport="mobile"')
    expect(mobileSource).toContain('<SFMobileSidebarContent')
    expect(mobileSource).toContain('@navigate="emit(\'close\')"')
    expect(mobileSidebarSource).toContain('useCategoryGroups({ serverInternal: false })')
    expect(mobileSidebarSource).toContain('<SFPublicSidebarContent')
    expect(sidebarSource).toContain('<SFPublicSidebarContent')
  })

  test('renders sidebar links and the bounded category block in resolver order', () => {
    expect(sidebarSource).toContain('const { sidebarItems } = usePublicNavigation()')
    expect(sidebarContentSource).toContain('v-for="item in resolvedItems"')
    expect(sidebarContentSource).toContain('v-if="isCoreDynamicCategories(item)"')
    expect(sidebarContentSource.indexOf('v-for="item in resolvedItems"')).toBeLessThan(
      sidebarContentSource.indexOf('v-if="isCoreDynamicCategories(item)"')
    )
    expect(sidebarContentSource).toContain("navigationMode?: 'filter' | 'route'")
    expect(sidebarContentSource).toContain("emit('select-category', slug)")
    expect(sidebarContentSource).toContain('void router.push(slug ? categoryTo(slug) : allTopicsTo())')
    expect(sidebarContentSource).toContain('<SFCategoryNavigationBlock')
    expect(sidebarContentSource).toContain(':max-items="item.maxItems"')
    expect(categoryBlockSource).toContain("return icon.startsWith('i-') ? icon : 'i-lucide-folder'")
    expect(categoryBlockSource).toContain('category.topicCount')
    expect(categoryBlockSource).toContain("selectedCategorySlug === category.slug")
    expect(categoryBlockSource).toContain('const visibleCategories = computed')
    expect(categoryBlockSource).toContain('limitDynamicNavigationItems(')
    expect(categoryBlockSource).toContain('v-for="category in visibleCategories"')
    expect(categoryBlockSource).toContain('const hasHiddenCategories = computed')
    expect(categoryBlockSource).toContain("t('home.sidebar.viewAllCategories')")
    expect(categoryBlockSource).toContain(':to="localePath(\'/categories\')"')
  })

  test('fails the dynamic category block closed without hiding ordinary sidebar links', () => {
    expect(sidebarContentSource).toContain('if (isCoreDynamicCategories(item)) return props.showCategories')
    expect(sidebarContentSource).toContain('return Boolean(item.label.trim()) && (isExternalNavigationItem(item) || isInternalNavigationItem(item))')
    expect(sidebarSource).toContain('v-if="!mobileOnly"')
    expect(sidebarSource).not.toContain('sf-home-navigation__select')
    expect(sidebarContentSource).not.toContain("t('home.sidebar.guidelines')")
  })

  test('isolates external sidebar and footer destinations from the opener', () => {
    for (const renderer of [sidebarContentSource, linksSource, footerSource]) {
      expect(renderer).toContain('target="_blank"')
      expect(renderer).toContain('rel="noopener noreferrer"')
    }
  })

  test('keeps canonical footer navigation beside copyright and friend-link owners', () => {
    expect(footerSource).toContain('const { footerItems } = usePublicNavigation(props.fetchRemoteChrome)')
    expect(footerSource).toContain('<SFPublicNavigationLinks')
    expect(footerSource).toContain('mode="footer"')
    expect(footerSource).toContain(':items="footerItems"')
    expect(footerSource).toContain('footerCopyrightTemplate(locale.value)')
    expect(footerSource).toContain('chromeApi.listPublicFriendLinks()')
    expect(footerSource).not.toContain('webOptions.footerLinks')
  })
})
