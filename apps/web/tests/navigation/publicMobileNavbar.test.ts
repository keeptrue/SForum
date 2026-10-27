import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (relative: string) => readFileSync(new URL(relative, import.meta.url), 'utf8')

// 公开 body 岛固定清单：每份都必须消费 forum-mobile-info-open 并渲染移动端右抽屉。
const PUBLIC_BODY_ISLANDS = [
  'app/components/forum/SFHomePage.vue',
  'app/components/forum/SFCategoryShowPage.vue',
  'app/components/forum/SFCategoryIndexPage.vue',
  'app/components/forum/SFTagIndexPage.vue',
  'app/components/forum/SFTagShowPage.vue',
  'app/components/forum/SFTopicShowPage.vue',
  'app/components/notifications/SFNotificationsPage.vue',
  'app/components/profile/SFProfileShowPage.vue'
]

// 把右抽屉委托给专用组件的页面：页面 -> 承载抽屉标记的组件。
const MOBILE_DRAWER_DELEGATES: Record<string, string> = {
  'app/components/forum/SFTopicShowPage.vue': 'app/components/forum/SFTopicMobileInfoDrawer.vue'
}

const navbar = source('../../app/components/SFNavbar.vue')
const searchBar = source('../../app/components/navigation/SFPublicMobileSearchBar.vue')
const bottomNavigation = source('../../app/components/navigation/SFPublicMobileBottomNavigation.vue')
const mobileRightDrawerHeader = source('../../app/components/navigation/SFPublicMobileRightDrawerHeader.vue')
const mobileUserMenu = source('../../app/components/navigation/SFPublicMobileUserMenu.vue')
const userMenuComposable = source('../../app/composables/navigation/usePublicUserMenu.ts')

describe('public mobile navbar composition', () => {
  test('keeps the shared navbar focused on chrome orchestration', () => {
    expect(navbar).toContain("import SFPublicMobileSearchBar from '~/components/navigation/SFPublicMobileSearchBar.vue'")
    expect(navbar).toContain("import SFPublicMobileBottomNavigation from '~/components/navigation/SFPublicMobileBottomNavigation.vue'")
    expect(navbar).toContain('v-model="searchQuery"')
    expect(navbar).toContain(':can-create-topic="canCreateTopic"')
    expect(navbar).toContain(':authenticated="Boolean(user)"')
    expect(navbar).not.toContain('sf-public-mobile-bottom-nav__badge')
  })

  test('keeps mobile search and compose destinations permission-aware', () => {
    expect(searchBar).toContain("emit('update:modelValue', value)")
    expect(searchBar).toContain("@submit=\"emit('submit', $event)\"")
    expect(searchBar).toContain("canCreateTopic ? localePath('/topics/new') : localePath('/login')")
    expect(searchBar).toContain("canCreateTopic ? 'i-lucide-square-pen' : 'i-lucide-log-in'")
  })

  test('keeps the fixed bottom navigation order and unread badge contract', () => {
    const homeIndex = bottomNavigation.indexOf("<span>{{ t('nav.home') }}</span>")
    const composeIndex = bottomNavigation.indexOf("<span>{{ t('nav.newTopic') }}</span>")
    const notificationsIndex = bottomNavigation.indexOf("<span>{{ t('nav.notifications') }}</span>")

    expect(homeIndex).toBeGreaterThan(-1)
    expect(composeIndex).toBeGreaterThan(homeIndex)
    expect(notificationsIndex).toBeGreaterThan(composeIndex)
    expect(bottomNavigation).toContain("authenticated ? localePath('/notifications') : localePath('/login')")
    expect(bottomNavigation).toContain('authenticated && notifications.unreadCount.value')
    expect(bottomNavigation).toContain("notifications.unreadCount.value > 99 ? '99+' : notifications.unreadCount.value")
  })

  test('reserves the mobile right drawer for authenticated users', () => {
    expect(navbar).toContain('class="navbar__mobile-info-button"')
    expect(navbar).toContain('v-if="user"')
    expect(navbar).toContain(':avatar="user.avatar"')
    expect(navbar).not.toContain('i-lucide-panel-right')
    expect(navbar).toContain("'navbar__session--authenticated': Boolean(user)")
    expect(navbar).toContain('.navbar__session--authenticated')
    expect(navbar).toContain('display: none')
  })

  test('shows one mobile login and registration action for guests', () => {
    expect(navbar).toContain('class="navbar__mobile-auth-link"')
    expect(navbar).toContain("showRegisterLinks ? t('nav.loginOrRegister') : t('nav.login')")
    expect(navbar).not.toContain('navbar__mobile-guest-avatar')
    expect(navbar).not.toContain('<SFAvatar :name="siteName"')
  })

  test('renders the desktop avatar dropdown actions inside the mobile right drawer', () => {
    expect(navbar).toContain('usePublicUserMenu()')
    expect(navbar).toContain(':items="userMenuItems"')
    expect(mobileUserMenu).toContain('usePublicUserMenu()')
    expect(mobileUserMenu).toContain('v-for="(group, groupIndex) in menuGroups"')
    expect(mobileUserMenu).toContain('class="sf-public-mobile-user-menu__identity"')
    expect(mobileUserMenu).toContain('@click="closeDrawer"')
    expect(userMenuComposable).toContain("key: 'profile'")
    expect(userMenuComposable).toContain("key: 'profile-settings'")
    expect(userMenuComposable).toContain("key: 'moderation'")
    expect(userMenuComposable).toContain("key: 'logout'")
    expect(userMenuComposable).toContain("key: 'resend-email-verification'")
  })

  test('shows the mobile user actions on open and keeps an accessible toggle', () => {
    // 抽屉内用户菜单是首屏唯一操作区，默认展开；折叠能力与 aria 契约不变。
    expect(mobileUserMenu).toContain('const menuOpen = ref(true)')
    expect(mobileUserMenu).toContain('@click="menuOpen = !menuOpen"')
    expect(mobileUserMenu).toContain(':aria-expanded="menuOpen"')
    expect(mobileUserMenu).toContain('aria-controls="sf-public-mobile-user-menu-actions"')
    expect(mobileUserMenu).toContain('v-if="menuOpen" id="sf-public-mobile-user-menu-actions"')
    expect(mobileUserMenu).toContain("menuOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'")
  })

  test('uses personal center as the authenticated drawer heading without losing the guest context', () => {
    expect(mobileRightDrawerHeader).toContain("useAuthSession()")
    expect(mobileRightDrawerHeader).toContain("user ? t('nav.personalCenter') : title")
    expect(mobileRightDrawerHeader).toContain(':aria-label="closeLabel"')
    expect(mobileRightDrawerHeader).toContain("@click=\"$emit('close')\"")
  })

  test('gates the admin entry on the same permission authority the API enforces', () => {
    // 入口复用 admin.access，而不是只看 moderation.review 或硬编码角色判断。
    expect(userMenuComposable).toContain('can(FORUM_PERMISSIONS.adminAccess)')
    expect(userMenuComposable).toContain("key: 'admin'")
    expect(userMenuComposable).toContain("to: adminRoutes.path('/')")
    // 后台路径必须经 useAdminRoutes，才能跟随运营者自定义的 adminRoutePrefix。
    expect(userMenuComposable).toContain("import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'")
    expect(userMenuComposable).not.toContain("'/control-panel'")
    // 桌面下拉与移动抽屉共用同一 menuGroups，改一处即覆盖两个面。
    expect(navbar).toContain(':items="userMenuItems"')
    expect(mobileUserMenu).toContain('v-for="(group, groupIndex) in menuGroups"')
  })

  test('every public body island consumes the shared mobile info state and renders the right drawer', () => {
    // 移动端导航栏头像只翻转 forum-mobile-info-open；抽屉不是全局组件，
    // 每个公开 body 岛必须自己消费该状态并渲染右抽屉，否则点击毫无 UI 响应。
    for (const page of PUBLIC_BODY_ISLANDS) {
      const pageSource = source(`../../${page}`)
      expect({ page, consumesState: pageSource.includes('forum-mobile-info-open') })
        .toEqual({ page, consumesState: true })

      // 主题详情页把右抽屉封装进 SFTopicMobileInfoDrawer（额外承载主题信息），
      // 抽屉标记落在那份组件里；其余页面必须直接在页面内渲染。
      const drawerSource = MOBILE_DRAWER_DELEGATES[page]
        ? source(`../../${MOBILE_DRAWER_DELEGATES[page]}`)
        : pageSource
      expect({ page, rendersRightDrawer: drawerSource.includes('sforum-mobile-drawer--right') })
        .toEqual({ page, rendersRightDrawer: true })
      expect({ page, rendersUserMenu: drawerSource.includes('<SFPublicMobileUserMenu') })
        .toEqual({ page, rendersUserMenu: true })
    }
  })

  test('keeps the topic page delegation to its mobile info drawer explicit', () => {
    const topicPage = source('../../app/components/forum/SFTopicShowPage.vue')
    expect(topicPage).toContain('<SFTopicMobileInfoDrawer')
    expect(topicPage).toContain('v-if="mobileInfoOpen && topic"')
    expect(topicPage).toContain('class="sforum-mobile-drawer__backdrop"')
    expect(topicPage).toContain('@click="closeMobileDrawers"')
  })

  test('ships bilingual copy for the admin entry', () => {
    const zh = JSON.parse(source('../../i18n/locales/zh-CN.json'))
    const en = JSON.parse(source('../../i18n/locales/en-US.json'))
    for (const locale of [zh, en]) {
      expect(locale.nav.admin).toBeTruthy()
      expect(locale.nav.adminEntryHint).toBeTruthy()
    }
  })
})
