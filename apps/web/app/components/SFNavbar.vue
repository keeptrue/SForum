<script setup lang="ts">
import SFNotificationPreview from '~/components/notifications/SFNotificationPreview.vue'
import SFPublicMobileBottomNavigation from '~/components/navigation/SFPublicMobileBottomNavigation.vue'
import SFPublicMobileNavigation from '~/components/navigation/SFPublicMobileNavigation.vue'
import SFPublicMobileSearchBar from '~/components/navigation/SFPublicMobileSearchBar.vue'
import SFPublicNavigationLinks from '~/components/navigation/SFPublicNavigationLinks.vue'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import {
  useNavbarLanguageMenu
} from '~/composables/navigation/useNavbarLanguageMenu'
import { usePublicUserMenu } from '~/composables/navigation/usePublicUserMenu'
import { usePublicNavigation } from '~/composables/navigation/usePublicNavigation'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
import { useColorModePreference } from '~/composables/appearance/useColorModePreference'
import { buildForumHomeQuery } from '~/utils/forum/forumHome'
import { parseForumTagPublicPagesOption } from '~/utils/forum/forumTaxonomy'

const props = withDefaults(defineProps<{
  /** Core 404 应急页不得在 API 已失效时继续启动 chrome 请求。 */
  fetchRemoteChrome?: boolean
  /** Host 回退壳的桌面导航几何；主题路径继续由主题 CSS 管理。 */
  layout?: 'default' | 'fullwidth-3col'
}>(), {
  fetchRemoteChrome: true,
  layout: 'default'
})

const { t } = useI18n()
const localePath = useLocalePath()
const { user, status, displayName, needsEmailVerification, userMenuItems } = usePublicUserMenu()
const {
  siteName,
  siteTagline,
  siteLogoUrl,
  webOption
} = useWebOptions()
const { request } = useApiClient()
const route = useRoute()
const {
  preference: colorModePreference,
  options: colorModeOptions,
  cyclePreference: cycleColorModePreference
} = useColorModePreference()
const { can } = usePermissions()
const { topbarItems, sidebarItems } = usePublicNavigation(props.fetchRemoteChrome)

// 标签公开列表受运行时选项控制；关闭时隐藏导航入口（详情页同样 404）。
const publicTagPagesEnabled = computed(() => parseForumTagPublicPagesOption(
  webOption('forum.tags.public_pages', 'enabled')
))

function filterTagNav(href: string) {
  if (publicTagPagesEnabled.value) {
    return true
  }
  return !href.replace(/\/$/, '').endsWith('/tags')
}

const visibleTopbarItems = computed(() => topbarItems.value
  .filter(item => !item.href || filterTagNav(item.href)))
const visibleSidebarItems = computed(() => sidebarItems.value
  .filter(item => !item.href || filterTagNav(item.href)))

// 导航栏注册入口以 registration-status 为准（含 bootstrap 覆盖）。
type RegistrationStatus = {
  nextUserIsInitialSuperAdmin: boolean
  registrationEnabled?: boolean
}
const defaultRegistrationStatus = (): RegistrationStatus => ({
  nextUserIsInitialSuperAdmin: false,
  registrationEnabled: true
})
const registrationStatus = props.fetchRemoteChrome
  ? useAsyncData('auth-registration-status-navbar', async () => {
      try {
        return await request<RegistrationStatus>('/auth/registration-status')
      } catch {
        // 接口失败时保守显示注册，避免误关 bootstrap 入口。
        return defaultRegistrationStatus()
      }
    }, { default: defaultRegistrationStatus }).data
  : shallowRef<RegistrationStatus>(defaultRegistrationStatus())
const showRegisterLinks = computed(() => registrationStatus.value?.registrationEnabled !== false)
const logoAriaLabel = computed(() => {
  const tagline = siteTagline.value
  return tagline ? `${siteName.value} — ${tagline}` : siteName.value
})

const routeSearchQuery = computed(() => typeof route.query.q === 'string' ? route.query.q.trim() : '')
const searchQuery = ref(routeSearchQuery.value)
const {
  open: mobileMenuOpen,
  hasPageOwner: hasPageSidebarOwner,
  closeDrawer: closeMobileMenu,
  toggleDrawer: toggleMobileSidebar
} = usePublicSidebarDrawer()
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)

// 发帖入口只对拥有论坛发帖权限的用户显示，API 仍负责最终鉴权。
const canCreateTopic = computed(() => can(FORUM_PERMISSIONS.topicCreate) && !needsEmailVerification.value)
const { currentLocaleName, languageMenuItems } = useNavbarLanguageMenu()
const currentColorModeOption = computed(() =>
  colorModeOptions.find(option => option.value === colorModePreference.value) || colorModeOptions[0]!
)
const colorModePreferenceLabel = computed(() => t(currentColorModeOption.value.labelKey))
const colorModeTriggerLabel = computed(() => t('appearance.colorMode.currentPreference', {
  preference: colorModePreferenceLabel.value
}))
const colorModeTriggerIcon = computed(() => currentColorModeOption.value.icon)

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

const mobileSearchOpen = ref(false)

function toggleMobileSearch() {
  closeMobileDrawers()
  mobileSearchOpen.value = !mobileSearchOpen.value
}

function closeMobileSearch() {
  mobileSearchOpen.value = false
}

function toggleMobileMenu() {
  const opening = !mobileMenuOpen.value
  closeMobileDrawers()
  if (opening) toggleMobileSidebar()
}

function toggleMobileInfo() {
  const opening = !mobileInfoOpen.value
  closeMobileDrawers()
  mobileInfoOpen.value = opening
}

watch(() => route.fullPath, () => {
  closeMobileDrawers()
  closeMobileSearch()
})
watch(routeSearchQuery, (query) => {
  if (searchQuery.value !== query) {
    searchQuery.value = query
  }
})

function submitSearch(query: string) {
  const normalizedQuery = query.trim()
  return navigateTo({
    path: localePath(normalizedQuery ? '/search' : '/'),
    // 搜索端点没有 sort，回到首页也用站点默认排序；这里从不携带 sort 参数
    query: buildForumHomeQuery({
      query: normalizedQuery,
      categorySlug: '',
      tagSlug: '',
      sort: ''
    })
  })
}

</script>

<template>
  <header
    class="navbar"
    :class="{ 'navbar--fullwidth-3col': props.layout === 'fullwidth-3col' }"
  >
    <div class="navbar__inner">
      <button
        type="button"
        class="navbar__mobile-shell-button"
        :aria-label="t('nav.openMenu')"
        :aria-expanded="mobileMenuOpen"
        @click="toggleMobileMenu"
      >
        <UIcon name="i-lucide-menu" class="size-5" aria-hidden="true" />
      </button>

      <NuxtLink
        :to="localePath('/')"
        class="navbar__logo"
        :aria-label="logoAriaLabel"
      >
        <span v-if="siteLogoUrl" class="navbar__logo-image-wrap" aria-hidden="true">
          <img :src="siteLogoUrl" alt="" class="navbar__logo-image">
        </span>
        <span v-else class="navbar__logo-mark" aria-hidden="true">
          <UIcon name="i-tabler-message-circle-filled" class="size-5" />
        </span>
        <span class="navbar__logo-text-wrap">
          <span class="navbar__logo-text">{{ siteName }}</span>
          <span v-if="siteTagline" class="navbar__logo-tagline">{{ siteTagline }}</span>
        </span>
      </NuxtLink>

      <SFPublicNavigationLinks
        class="navbar__desktop-nav"
        :items="visibleTopbarItems"
      />

      <SFSearch
        v-model="searchQuery"
        class="navbar__search"
        :placeholder="t('home.searchPlaceholder')"
        :aria-label="t('nav.search')"
        :form-label="t('nav.search')"
        @submit="submitSearch"
      />

      <NuxtLink
        v-if="canCreateTopic"
        :to="localePath('/topics/new')"
        class="navbar__new-topic"
        :aria-label="t('nav.newTopic')"
      >
        <UIcon name="i-lucide-square-pen" class="size-4" aria-hidden="true" />
        <span>{{ t('nav.newTopic') }}</span>
      </NuxtLink>
      <span v-else class="navbar__new-topic-placeholder" aria-hidden="true" />

      <!-- 工具区：通知 / 语言 / 日夜模式；会话区单独一列与右侧栏对齐 -->
      <div class="navbar__utility">
        <button
          v-if="user"
          type="button"
          class="navbar__mobile-info-button"
          :aria-label="t('nav.userMenu')"
          :title="t('nav.personalCenter')"
          :aria-expanded="mobileInfoOpen"
          @click="toggleMobileInfo"
        >
          <SFAvatar
            :name="displayName"
            :avatar="user.avatar"
            size="sm"
            shape="circle"
          />
        </button>
        <button
          type="button"
          class="navbar__control navbar__mobile-search-trigger"
          :aria-label="t('nav.search')"
          :aria-expanded="mobileSearchOpen"
          :title="t('nav.search')"
          @click="toggleMobileSearch"
        >
          <UIcon name="i-lucide-search" class="size-5" aria-hidden="true" />
        </button>
        <SFNotificationPreview v-if="user" />
        <NuxtLink
          v-if="canCreateTopic"
          :to="localePath('/topics/new')"
          class="navbar__mobile-new-topic"
          :aria-label="t('nav.newTopic')"
        >
          <UIcon name="i-lucide-square-pen" class="size-5" aria-hidden="true" />
        </NuxtLink>

        <ClientOnly>
          <UDropdownMenu
            :items="languageMenuItems"
            :content="{ align: 'end' }"
          >
            <UButton
              color="neutral"
              variant="ghost"
              square
              class="navbar__control navbar__language-control"
              :aria-label="t('nav.language')"
              :title="currentLocaleName"
            >
              <UIcon name="i-tabler-language" class="size-5" aria-hidden="true" />
            </UButton>
          </UDropdownMenu>
          <template #fallback>
            <UButton
              color="neutral"
              variant="ghost"
              square
              class="navbar__control"
              :aria-label="t('nav.language')"
              :title="currentLocaleName"
              aria-hidden="true"
              tabindex="-1"
              data-ssr-fallback="navbar-language"
            >
              <UIcon name="i-tabler-language" class="size-5" aria-hidden="true" />
            </UButton>
          </template>
        </ClientOnly>

        <ClientOnly>
          <UButton
            color="neutral"
            variant="ghost"
            square
            class="navbar__control navbar__appearance-control"
            :aria-label="colorModeTriggerLabel"
            :title="colorModeTriggerLabel"
            @click="cycleColorModePreference"
          >
            <UIcon :name="colorModeTriggerIcon" class="size-5" aria-hidden="true" />
          </UButton>
          <template #fallback>
            <UButton
              color="neutral"
              variant="ghost"
              square
              class="navbar__control navbar__appearance-control"
              :aria-label="t('nav.appearance')"
              :title="t('nav.appearance')"
              aria-hidden="true"
              tabindex="-1"
              data-ssr-fallback="navbar-appearance"
            >
              <UIcon name="i-tabler-brightness-filled" class="size-5" aria-hidden="true" />
            </UButton>
          </template>
        </ClientOnly>
      </div>

      <div class="navbar__session" :class="{ 'navbar__session--authenticated': Boolean(user) }">
        <template v-if="status === 'guest'">
          <NuxtLink
            :to="localePath('/login')"
            class="navbar__auth-link navbar__auth-link--quiet"
            :aria-label="t('nav.login')"
          >
            {{ t('nav.login') }}
          </NuxtLink>
          <NuxtLink
            v-if="showRegisterLinks"
            :to="localePath('/register')"
            class="navbar__auth-link navbar__auth-link--primary"
            :aria-label="t('nav.register')"
          >
            {{ t('nav.register') }}
          </NuxtLink>
        </template>

        <ClientOnly
          v-else-if="user"
        >
          <UDropdownMenu
            :items="userMenuItems"
            :content="{ align: 'end' }"
          >
            <UButton
              color="neutral"
              variant="ghost"
              class="navbar__user-trigger"
              :aria-label="t('nav.userMenu')"
            >
              <SFAvatar
                :name="displayName"
                :avatar="user.avatar"
                size="sm"
                shape="circle"
              />
              <span class="navbar__username">{{ displayName }}</span>
              <UIcon name="i-lucide-chevron-down" class="size-3.5" aria-hidden="true" />
            </UButton>
          </UDropdownMenu>
          <template #fallback>
            <UButton
              color="neutral"
              variant="ghost"
              class="navbar__user-trigger"
              :aria-label="t('nav.userMenu')"
              aria-hidden="true"
              tabindex="-1"
              data-ssr-fallback="navbar-user"
            >
              <SFAvatar
                :name="displayName"
                :avatar="user.avatar"
                size="sm"
                shape="circle"
              />
              <span class="navbar__username">{{ displayName }}</span>
              <UIcon name="i-lucide-chevron-down" class="size-3.5" aria-hidden="true" />
            </UButton>
          </template>
        </ClientOnly>
        <span
          v-else
          class="navbar__session-loading"
          :aria-label="t('nav.userMenu')"
          aria-busy="true"
        >
          <span class="navbar__session-loading-avatar" aria-hidden="true" />
          <span class="navbar__session-loading-name" aria-hidden="true" />
        </span>
        <NuxtLink
          v-if="status === 'guest'"
          :to="localePath('/login')"
          class="navbar__mobile-auth-link"
        >
          {{ showRegisterLinks ? t('nav.loginOrRegister') : t('nav.login') }}
        </NuxtLink>
      </div>
    </div>

    <SFPublicMobileSearchBar
      v-model="searchQuery"
      :can-create-topic="canCreateTopic"
      :open="mobileSearchOpen"
      @submit="submitSearch"
      @close="closeMobileSearch"
    />
  </header>
  <SFPublicMobileBottomNavigation
    :can-create-topic="canCreateTopic"
    :authenticated="Boolean(user)"
  />
  <SFPublicMobileNavigation
    :open="mobileMenuOpen && !hasPageSidebarOwner"
    :items="visibleSidebarItems"
    @close="closeMobileMenu"
  />
</template>

<style scoped>
.navbar {
  position: sticky;
  top: 0;
  z-index: 50;
  min-height: var(--sf-public-topbar-height, 52px);
  border-bottom: 1px solid var(--sf-public-border);
  background: var(--sf-public-surface);
  box-shadow: none;
}

.navbar__inner {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: var(--sf-public-topbar-height, 52px);
  max-width: none;
  margin: 0;
  padding: 0 18px 0 20px;
}

.navbar__logo,
.navbar__desktop-nav,
.navbar__new-topic,
.navbar__mobile-new-topic,
.navbar__utility,
.navbar__session,
.navbar__auth-link {
  display: flex;
  align-items: center;
}

.navbar__logo {
  min-width: 0;
  flex-shrink: 0;
  gap: 10px;
  color: #111827;
  font-size: 15px;
  font-weight: 700;
  text-decoration: none;
}

.navbar__logo-mark {
  display: grid;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  place-items: center;
  border: 1px solid var(--sf-accent);
  border-radius: 7px;
  background: var(--sf-accent);
  color: #ffffff;
}

.navbar__logo-image-wrap {
  display: grid;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  place-items: center;
  overflow: hidden;
  border-radius: 7px;
}

.navbar__logo-image {
  width: 32px;
  height: 32px;
  object-fit: contain;
}

.navbar__logo-text-wrap {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 1px;
  line-height: 1.15;
}

.navbar__logo-text,
.navbar__username {
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.navbar__logo-tagline {
  display: none;
}

.navbar__desktop-nav {
  align-self: stretch;
  flex: 0 1 auto;
}

.navbar__new-topic,
.navbar__mobile-new-topic {
  justify-content: center;
  gap: 6px;
  flex-shrink: 0;
  border-radius: 7px;
  color: #fff;
  background: var(--sf-accent);
  font-size: 13px;
  font-weight: 700;
  text-decoration: none;
}

.navbar__new-topic-placeholder {
  width: 92px;
  height: 38px;
  display: block;
  flex: 0 0 92px;
}

.navbar__new-topic {
  min-height: 38px;
  padding: 0 15px;
}

.navbar__new-topic:hover,
.navbar__mobile-new-topic:hover {
  background: var(--sf-accent-hover);
}

.navbar__mobile-new-topic {
  display: none;
}

/* 紧凑搜索：不抢占顶栏中间全部空间；默认主题网格内再 cap max-width */
.navbar__search {
  width: min(260px, 24vw);
  min-width: 140px;
  max-width: 280px;
  margin-left: auto;
  flex: 0 1 260px;
}

.navbar__search :deep(.sf-search__box) {
  min-height: 38px;
  border-radius: 7px;
}

/* 工具按钮（语言 / 日夜 / 通知）与会话区分离，便于与右侧栏对齐 */
.navbar__utility {
  flex-shrink: 0;
  justify-content: flex-end;
  gap: 4px;
}

.navbar__session {
  flex-shrink: 0;
  justify-content: flex-end;
  gap: 6px;
  min-width: 0;
}

.navbar__session-loading {
  width: 112px;
  height: 36px;
  display: flex;
  flex: 0 0 112px;
  align-items: center;
  gap: 7px;
  padding: 0 10px;
  box-sizing: border-box;
  overflow: hidden;
  border-radius: 7px;
}

.navbar__session-loading-avatar,
.navbar__session-loading-name {
  display: block;
  background: var(--sf-public-surface-muted);
}

.navbar__session-loading-avatar {
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  border-radius: 50%;
}

.navbar__session-loading-name {
  width: 56px;
  height: 10px;
  border-radius: 4px;
}

[data-ssr-fallback] {
  pointer-events: none;
}

@media (min-width: 981px) {
  .navbar--fullwidth-3col .navbar__inner {
    display: grid;
    min-height: 58px;
    grid-template-columns:
      var(--sf-public-sidebar-width, 230px)
      auto
      minmax(0, 1fr)
      auto
      auto
      var(--sf-public-right-rail-width, 270px);
    gap: 10px;
    padding: 0 var(--sf-public-edge-inset, 24px);
  }

  .navbar--fullwidth-3col .navbar__logo {
    padding-left: 16px;
  }

  .navbar--fullwidth-3col .navbar__desktop-nav {
    gap: 28px;
  }

  .navbar--fullwidth-3col .navbar__search {
    width: min(260px, 100%);
    min-width: 0;
    max-width: 280px;
    margin-left: 0;
    justify-self: start;
  }

  .navbar--fullwidth-3col .navbar__new-topic,
  .navbar--fullwidth-3col .navbar__session {
    box-sizing: border-box;
    width: 100%;
  }

  .navbar--fullwidth-3col .navbar__utility,
  .navbar--fullwidth-3col .navbar__session {
    justify-content: flex-end;
  }

  .navbar--fullwidth-3col .navbar__utility {
    gap: 2px;
  }

  .navbar--fullwidth-3col .navbar__session {
    min-width: 0;
    padding: 0 16px 0 12px;
  }

  .navbar--fullwidth-3col .navbar__user-trigger {
    max-width: 100%;
  }
}

.navbar__control,
.navbar__user-trigger,
.navbar__mobile-trigger {
  min-height: 36px;
  border-radius: 7px;
}

.navbar__control {
  width: 36px;
  color: #4b5563;
}

/* 移动端搜索触发只出现在窄屏；桌面用常规搜索输入 */
.navbar__mobile-search-trigger {
  display: none;
}
.navbar__auth-link {
  min-height: 34px;
  padding: 0 12px;
  border-radius: 7px;
  font-size: 13px;
  font-weight: 650;
  text-decoration: none;
}

.navbar__auth-link--quiet {
  border: 1px solid #d1d5db;
  color: #4b5563;
}

.navbar__auth-link--quiet:hover {
  border-color: #9ca3af;
  color: #111827;
  background: #f9fafb;
}

.navbar__auth-link--primary {
  color: #fff;
  background: var(--sf-accent);
}

.navbar__auth-link--primary:hover {
  background: var(--sf-accent-hover);
}

.navbar__user-trigger {
  max-width: 190px;
  gap: 7px;
  color: #374151;
}

.navbar__username {
  font-size: 13px;
  font-weight: 650;
}

.navbar__mobile-trigger {
  display: none;
  color: #374151;
}

.navbar__mobile-shell-button,
.navbar__mobile-info-button {
  display: none;
  width: 40px;
  height: 40px;
  flex: 0 0 40px;
  place-items: center;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--sf-public-text-secondary);
  cursor: pointer;
}

.navbar__mobile-shell-button:hover,
.navbar__mobile-info-button:hover {
  background: var(--sf-public-surface-muted);
  color: var(--sf-public-text);
}

.navbar__mobile-search-close {
  flex: 0 0 40px;
  border-radius: 7px;
  color: #374151;
}

.navbar__mobile-auth-link {
  display: none;
}

.dark .navbar {
  border-bottom-color: #27272a;
  background: #09090b;
  box-shadow: 0 1px 0 #27272a;
}

.dark .navbar__logo {
  color: #f4f4f5;
}

.dark .navbar__logo-mark {
  border-color: var(--sf-accent-dark);
  background: var(--sf-accent-dark);
  color: #052e2b;
}

.dark .navbar__control,
.dark .navbar__user-trigger,
.dark .navbar__mobile-trigger,
.dark .navbar__mobile-search-close {
  color: #d4d4d8;
}

.dark .navbar__auth-link--quiet {
  border-color: #3f3f46;
  color: #d4d4d8;
}

.dark .navbar__auth-link--quiet:hover {
  border-color: #52525b;
  color: #fff;
  background: #18181b;
}

.dark .navbar__auth-link--primary {
  color: #052e2b;
  background: var(--sf-accent-dark);
}

.dark .navbar__new-topic,
.dark .navbar__mobile-new-topic {
  color: #052e2b;
  background: var(--sf-accent-dark);
}

@media (max-width: 980px) {
  .navbar {
    min-height: 54px;
  }

  .navbar__inner {
    min-height: 54px;
    padding: 0 12px;
  }

  .navbar__desktop-nav,
  .navbar__search,
  .navbar__new-topic,
  .navbar__new-topic-placeholder,
  .navbar__auth-link {
    display: none;
  }

  .navbar__utility,
  .navbar__session {
    min-width: 0;
  }

  .navbar__utility {
    margin-left: auto;
    order: 0;
    gap: 3px;
  }

  .navbar__session {
    order: 3;
  }

  .navbar__session--authenticated {
    display: none;
  }

  .navbar__utility :deep(.sf-notification-preview),
  .navbar__language-control,
  .navbar__mobile-new-topic {
    display: none !important;
  }

  .navbar__appearance-control {
    order: 1;
  }

  .navbar__mobile-search-trigger {
    order: 0;
    display: grid;
    place-items: center;
    height: 36px;
  }

  .navbar__mobile-info-button {
    order: 2;
  }

  .navbar__session-loading {
    width: 40px;
    flex-basis: 40px;
    padding: 0 4px;
  }

  .navbar__session-loading-name {
    display: none;
  }

  .navbar__username {
    display: none;
  }

  .navbar__mobile-shell-button,
  .navbar__mobile-info-button {
    display: grid;
  }

  .navbar__logo,
  .navbar__mobile-new-topic,
  .navbar__user-trigger,
  .navbar__mobile-trigger,
  .navbar__mobile-search-close {
    min-width: 40px;
    min-height: 40px;
  }

  .navbar__user-trigger,
  .navbar__mobile-auth-link {
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }

  .navbar__mobile-auth-link {
    min-height: 34px;
    padding: 0 11px;
    border-radius: 7px;
    color: #fff;
    background: var(--sf-accent);
    font-size: 13px;
    font-weight: 700;
    line-height: 1;
    text-decoration: none;
    white-space: nowrap;
  }

  .navbar__mobile-auth-link:hover {
    background: var(--sf-accent-hover);
  }

  .navbar__mobile-new-topic,
  .navbar__mobile-trigger {
    display: none;
  }
}

@media (max-width: 520px) {
  .navbar__inner {
    gap: 6px;
    padding: 0 10px;
  }

  .navbar__logo-text,
  .navbar__username,
  .navbar__user-trigger > :deep(svg) {
    display: none;
  }

  .navbar__user-trigger {
    width: 30px;
    height: 30px;
    padding: 1px;
  }

}
</style>
