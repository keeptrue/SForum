<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useAdminSurfaces } from '~/composables/admin/useAdminSurfaces'
import { useAdminAdvancedSettings } from '~/composables/admin/useAdminAdvancedSettings'
import { useAdminSystemUpdates } from '~/composables/admin/useAdminSystemUpdates'
import { useColorModePreference } from '~/composables/appearance/useColorModePreference'
import type { DropdownMenuItem } from '@nuxt/ui/components/DropdownMenu.vue'
import SFAdminFooter from '~/components/admin/SFAdminFooter.vue'
import SFAdminSurfaceOutlet from '~/components/admin/SFAdminSurfaceOutlet.vue'
import SFAdminSystemUpdatePrompt from '~/components/admin/system/SFAdminSystemUpdatePrompt.vue'
import {
  ADMIN_DASHBOARD_PAGE_ID,
  ADMIN_NAV_SECTION_IDS,
  adminSidebarNavigation,
  findAdminPageDefinition,
  isExtensionAdminPageId,
  isAdminNavigationEntryActive,
  type AdminNavigationEntry,
  requireAdminPageDefinition,
  shouldOpenAdminNavigationEntry,
  shouldShowAdminPageInNav
} from '~/config/adminModules'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { type AdminTab, useAdminTabs } from '~/composables/admin/useAdminTabs'
import {
  adminExtensionNavPageId,
  extensionPageSitsUnderExtensionsFolder,
  groupExtensionNavigation,
  ungroupedExtensionNavigation,
  type AdminExtensionNavigationItem
} from '~/utils/admin/adminExtensions'
import {
  adminSurfaceKindIcon,
  adminSurfacePlacementPageId,
  type AdminSurfaceContract
} from '~/utils/admin/adminSurfaces'
import { formatOverviewVersion } from '~/utils/admin/adminOverview'

type SidebarNavigationItem = {
  label: string
  icon: string
  to?: string
  badge?: string
  active?: boolean
  defaultOpen?: boolean
  open?: boolean
  value?: string
  children?: SidebarNavigationItem[]
}

/** 「扩展管理」分组在侧栏里的 value，扩展自带分组插在它之后。 */
const EXTENSION_FOLDER_NAV_VALUE = 'admin.nav.extensions'

const { t } = useI18n()
const localePath = useLocalePath()
const adminRoutes = useAdminRoutes()
const { user, can } = useAuthSession()
const { request } = useApiClient()
const { siteName } = useWebOptions()
const sforumBuild = useRuntimeConfig().public.sforumBuild
const fallbackSforumVersion = formatOverviewVersion('', sforumBuild.version, sforumBuild.commit)
const systemUpdates = useAdminSystemUpdates()
const systemUpdateRefreshIntervalMs = 5 * 60 * 1000
await useAsyncData(
  'admin-system-update-status-fetch',
  async () => {
    if (!can('admin.access')) return null
    try {
      return await systemUpdates.refresh({ serverInternal: import.meta.server })
    } catch {
      return null
    }
  },
  { default: () => null }
)
let systemUpdateRefreshTimer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  systemUpdateRefreshTimer = setInterval(() => {
    if (!can('admin.access') || systemUpdates.pending.value) return
    void systemUpdates.refresh().catch(() => null)
  }, systemUpdateRefreshIntervalMs)
})
onBeforeUnmount(() => {
  if (systemUpdateRefreshTimer) clearInterval(systemUpdateRefreshTimer)
})
const sforumVersion = computed(() => {
  const status = systemUpdates.status.value
  return status
    ? formatOverviewVersion('', status.currentVersion, status.currentCommit || '')
    : fallbackSforumVersion
})
const systemUpdateAvailable = computed(() => Boolean(systemUpdates.status.value?.updateAvailable))
const systemUpdateSettingsRoute = computed(() => ({
  path: adminRoutes.path('/settings'),
  query: { tab: 'updates' }
}))
const systemUpdateLabel = computed(() => t('admin.shell.updateAvailable', {
  version: systemUpdates.status.value?.latestVersion
    ? formatOverviewVersion('', systemUpdates.status.value.latestVersion)
    : ''
}))
const { data: extensionNavigation } = await useAsyncData<AdminExtensionNavigationItem[]>(
  'admin-extension-navigation',
  () => (can('extension.view') || can('extension.plugin.manage') || can('extension.theme.manage') || can('extension.manage'))
    ? request<AdminExtensionNavigationItem[]>('/admin/extensions/navigation')
    : Promise.resolve([]),
  { default: (): AdminExtensionNavigationItem[] => [] }
)
const adminSurfaces = useAdminSurfaces()
const { data: surfaceNavigation } = await useAsyncData<AdminSurfaceContract[]>(
  'admin-surface-navigation',
  async () => {
    const catalog = await adminSurfaces.list(undefined, 'navigation')
    return catalog.surfaces.filter(surface => surface.action === 'add' && Boolean(adminSurfacePlacementPageId(surface.placementId)))
  },
  { default: (): AdminSurfaceContract[] => [] }
)

// 引入多页签状态与主题模式
const adminTabs = useAdminTabs()
const {
  preference: colorModePreference,
  options: colorModeOptions,
  setPreference: setColorModePreference,
  cyclePreference: cycleColorModePreference
} = useColorModePreference()
const { professionalMode, operationsMode } = useAdminAdvancedSettings()
const advancedSettingsOpen = ref(false)

const displayName = computed(() => {
  return userDisplayName(user.value, t('admin.shell.unknownUser'))
})

const currentColorModeOption = computed(() =>
  colorModeOptions.find(option => option.value === colorModePreference.value) || colorModeOptions[0]!
)
const colorModePreferenceLabel = computed(() => t(currentColorModeOption.value.labelKey))
const colorModeTriggerLabel = computed(() => t('appearance.colorMode.currentPreference', {
  preference: colorModePreferenceLabel.value
}))
const colorModeTriggerIcon = computed(() => currentColorModeOption.value.icon)
const appearanceMenuItems = computed<DropdownMenuItem[]>(() =>
  colorModeOptions.map((option) => {
    const isCurrent = option.value === colorModePreference.value
    return {
      label: t(option.labelKey),
      description: option.descriptionKey ? t(option.descriptionKey) : undefined,
      icon: option.icon,
      type: 'checkbox',
      checked: isCurrent,
      onUpdateChecked: (checked: boolean) => {
        if (checked) setColorModePreference(option.value)
      }
    }
  })
)

// 计算当前激活标签页的标题，用于面包屑展示
const activeTabLabel = computed(() => {
  const activeTab = adminTabs.tabs.value.find(tab => tab.id === adminTabs.activeTabId.value)
  if (activeTab?.label) {
    return activeTab.label
  }
  return activeTab?.labelKey ? t(activeTab.labelKey) : t(requireAdminPageDefinition('/').labelKey)
})

// 后台 SEO 标题固定为：页面名 - 管理后台 - 网站名（覆盖 app.vue 的前台模板）
useHead(() => ({
  bodyAttrs: {
    class: 'sforum-admin-shell'
  },
  titleTemplate: (title?: string) => applyAdminSEOTitleTemplate(
    (title || '').trim() || activeTabLabel.value,
    t('nav.admin'),
    siteName.value
  )
}))

const route = useRoute()
const currentAdminPageId = computed(() => adminRoutes.routeId(route.path) || ADMIN_DASHBOARD_PAGE_ID)

// KeepAlive 页面不会重复 mounted，路由变化时用注册表同步当前 tab。
watch(() => route.path, (newPath) => {
  const tabId = adminRoutes.routeId(newPath)
  if (!tabId) {
    return
  }

  const page = tabId ? findAdminPageDefinition(tabId) : null

  if (page) {
    adminTabs.openTab(page.id)
    return
  }

  if (adminTabs.activateTab(tabId)) {
    return
  }

  if (isExtensionAdminPageId(tabId)) {
    openExtensionRoutePlaceholderTab(tabId)
  }
}, { immediate: true })

// 高级设置开关切换后强制重建侧栏，避免 UNavigationMenu 缓存旧 children。
const navVisibility = computed(() => ({
  professionalMode: professionalMode.value,
  operationsMode: operationsMode.value
}))

const navigationItems = computed(() => {
  const groups = adminSidebarNavigation
    .map(group => group
      .map(entry => buildNavigationItem(entry, currentAdminPageId.value, navVisibility.value))
      .filter((item): item is SidebarNavigationItem => Boolean(item)))
    .filter(group => group.length > 0)

  // 声明了 group 的扩展页签自成一级菜单：插在「扩展管理」之后，与内置分组同级。
  const extensionGroups = extensionGroupNavigation.value
  if (!extensionGroups.length) {
    return groups
  }
  let inserted = false
  const withExtensionGroups = groups.map((group) => {
    const index = group.findIndex(item => item.value === EXTENSION_FOLDER_NAV_VALUE)
    if (index < 0) {
      return group
    }
    inserted = true
    return [...group.slice(0, index + 1), ...extensionGroups, ...group.slice(index + 1)]
  })
  if (!inserted && withExtensionGroups.length > 0) {
    withExtensionGroups[0] = [...withExtensionGroups[0]!, ...extensionGroups]
  }
  return withExtensionGroups
})

/** group 命中 Core 注册分区时，该页并入对应的一级菜单，不再自成一组。 */
function isRegisteredNavSection(group?: string) {
  const value = (group || '').trim()
  return value !== '' && ADMIN_NAV_SECTION_IDS.includes(value)
}

/**
 * 按分区归拢插件页。分区容器由 Core 声明（label/icon/位置），成员由各插件在
 * manifest 里用 `group` 注册，因此 Core 不需要知道任何插件 id。
 */
const sectionExtensionPages = computed<Map<string, AdminExtensionNavigationItem[]>>(() => {
  const sections = new Map<string, AdminExtensionNavigationItem[]>()
  for (const item of extensionNavigation.value || []) {
    if (!isRegisteredNavSection(item.group)) {
      continue
    }
    const key = item.group!.trim()
    const bucket = sections.get(key) ?? []
    bucket.push(item)
    sections.set(key, bucket)
  }
  return sections
})

/**
 * 把同一扩展里 `group` 相同的菜单页签收进一个可展开的一级菜单。
 * 未声明 group 的页签保持旧行为（平铺在「扩展管理」内，且仅专业模式可见）。
 */
const extensionGroupNavigation = computed<SidebarNavigationItem[]>(() => {
  // 已并入 Core 分区的页不再单独成组，否则同一个设置页会出现两次。
  const grouped = (extensionNavigation.value || []).filter(item => !isRegisteredNavSection(item.group))
  return groupExtensionNavigation(grouped).map((group) => {
    const children = group.pages.map((item): SidebarNavigationItem => {
      const pageId = adminExtensionNavPageId(item)
      return {
        label: item.label,
        icon: item.icon || group.icon || 'i-lucide-plug',
        to: adminRoutes.path(pageId),
        value: pageId,
        active: currentAdminPageId.value === pageId
      }
    })
    const active = children.some(child => child.active)
    return {
      label: group.label,
      icon: group.icon || 'i-lucide-plug',
      value: `extension-group:${group.extensionId}:${group.label}`,
      active,
      open: active,
      children
    }
  })
})

// 未分组插件页没有独立菜单，仍归「扩展管理」；基础模式下也不掉高亮。
const activeExtensionPageIsFlat = computed(() =>
  isExtensionAdminPageId(currentAdminPageId.value)
  && extensionPageSitsUnderExtensionsFolder(extensionNavigation.value || [], currentAdminPageId.value)
)

function buildNavigationItem(
  entry: AdminNavigationEntry,
  currentAdminPageId: string,
  visibility: { professionalMode: boolean, operationsMode: boolean }
): SidebarNavigationItem | null {
  if (entry.type === 'forum-home') {
    return {
      label: t(entry.labelKey),
      icon: entry.icon,
      to: localePath('/')
    }
  }

  if (entry.type === 'folder') {
    const children = entry.children
      .map(child => buildNavigationItem(child, currentAdminPageId, visibility))
      .filter((item): item is SidebarNavigationItem => Boolean(item))

    // Core 注册的共享分区：把声明了同一 group 的插件页并进来。静态子项在前，
    // 插件成员按 manifest order 追加在后。
    if (entry.sectionId) {
      for (const item of sectionExtensionPages.value.get(entry.sectionId) ?? []) {
        const pageId = adminExtensionNavPageId(item)
        children.push({
          label: item.label,
          icon: item.icon || entry.icon,
          to: adminRoutes.path(pageId),
          value: pageId,
          active: currentAdminPageId === pageId
        })
      }
    }

    // 扩展贡献页与 Admin Surface 导航属于高级工具，随专业模式显示。
    if (entry.labelKey === 'admin.nav.extensions' && visibility.professionalMode) {
      children.push(...buildExtensionNavigationItems(currentAdminPageId))
      children.push(...buildAdminSurfaceNavigationItems(currentAdminPageId))
    }

    if (children.length === 0) {
      return null
    }

    // 动态子项（分区成员、未分组插件页）不在静态 entry 里：用已渲染子项的 active 补一次。
    const childActive = children.some(child => child.active)
    const ownsActiveExtensionPage = entry.labelKey === EXTENSION_FOLDER_NAV_VALUE && activeExtensionPageIsFlat.value
    const isActive = isAdminNavigationEntryActive(entry, currentAdminPageId) || childActive || ownsActiveExtensionPage

    return {
      label: t(entry.labelKey),
      icon: entry.icon,
      value: entry.labelKey,
      active: isActive,
      defaultOpen: entry.defaultOpen,
      open: shouldOpenAdminNavigationEntry(entry, currentAdminPageId) || childActive || ownsActiveExtensionPage,
      children
    }
  }

  const page = findAdminPageDefinition(entry.pageId)
  if (!page || !shouldShowAdminPageInNav(page, can, visibility)) {
    return null
  }

  const badgeKey = entry.badgeKey || page.badgeKey

  return {
    label: t(page.labelKey),
    icon: page.icon,
    to: adminRoutes.path(page.id),
    value: page.id,
    active: isAdminNavigationEntryActive(entry, currentAdminPageId),
    ...(badgeKey ? { badge: t(badgeKey) } : {})
  }
}

function buildAdminSurfaceNavigationItems(currentAdminPageId: string): SidebarNavigationItem[] {
  return (surfaceNavigation.value || []).flatMap((surface) => {
    const pageId = adminSurfacePlacementPageId(surface.placementId)
    if (!pageId) return []
    return [{
      label: surface.label,
      icon: adminSurfaceKindIcon(surface.kind),
      to: adminRoutes.path(pageId),
      value: `admin-surface:${surface.id}`,
      active: currentAdminPageId === pageId
    }]
  })
}

function buildExtensionNavigationItems(currentAdminPageId: string): SidebarNavigationItem[] {
  // 声明了 group 的页签已经收进独立一级菜单，这里只平铺剩下的。
  return ungroupedExtensionNavigation(extensionNavigation.value || [])
    .map((item) => {
      const pageId = adminExtensionNavPageId(item)
      return {
        label: item.label,
        icon: item.icon || (item.extensionType === 'theme' ? 'i-lucide-palette' : 'i-lucide-plug'),
        to: adminRoutes.path(pageId),
        value: pageId,
        active: currentAdminPageId === pageId
      }
    })
}

function openExtensionRoutePlaceholderTab(tabId: string) {
  // 导航数据在 SSR 阶段已经可用时直接复用正式标题，避免服务端占位标题与客户端扩展详情数据产生水合差异。
  const navigationItem = (extensionNavigation.value || []).find((item) => {
    return adminExtensionNavPageId(item) === tabId
  })
  adminTabs.openCustomTab({
    id: tabId,
    label: navigationItem?.label || extensionRouteFallbackLabel(tabId),
    to: adminRoutes.path(tabId),
    icon: navigationItem?.icon || (navigationItem?.extensionType === 'theme' ? 'i-lucide-palette' : 'i-lucide-blocks'),
    closable: true,
    componentName: 'AdminExtensionDynamicPage'
  })
}

function extensionRouteFallbackLabel(tabId: string) {
  const match = tabId.match(/^\/extensions\/([^/]+)\/pages(?:\/(.*))?$/)
  if (!match) {
    return tabId
  }

  const extensionId = decodeRouteSegment(match[1] || 'extension')
  const pagePath = match[2]?.split('/').filter(Boolean).pop() || 'about'
  return `${extensionId} / ${decodeRouteSegment(pagePath)}`
}

function decodeRouteSegment(value: string | undefined) {
  try {
    return decodeURIComponent(value || '')
  } catch {
    return value || ''
  }
}

const sidebarNavigationUi = {
  list: 'flex flex-col gap-1 min-w-0',
  item: 'min-w-0',
  link: '!min-h-[42px] !max-w-full !gap-2 !rounded-md !px-3.5 !py-2 !text-[14.5px] !font-semibold !leading-tight',
  // min-w-0 确保 flex 子项可收缩，truncate 避免长菜单文案撑出横向滚动条
  linkLabel: '!min-w-0 !truncate !leading-tight',
  linkLeadingIcon: '!size-[18px] shrink-0',
  linkTrailingIcon: '!size-[17px] shrink-0',
  linkTrailingBadge: '!text-xs !px-2 !py-0.5 shrink-0',
  childList: '!mt-1 !mb-2 !ms-5 !min-w-0 !border-s !border-dashed !border-slate-200 dark:!border-zinc-800 !ps-3',
  childItem: '!min-w-0 !ps-0',
  childLink: '!min-h-[36px] !max-w-full !gap-1.5 !rounded-md !px-3 !py-1.5 !text-[13.5px] !font-medium !leading-tight',
  childLinkIcon: '!size-[15px] shrink-0',
  childLinkLabel: '!min-w-0 !truncate !leading-tight'
}

const userMenuItems = computed<DropdownMenuItem[][]>(() => [
  [
    {
      label: displayName.value,
      type: 'label'
    }
  ],
  [
    {
      label: t('admin.shell.advancedSettings.title'),
      icon: 'i-lucide-sliders-horizontal',
      onSelect: () => {
        advancedSettingsOpen.value = true
      }
    },
    {
      label: t('admin.shell.visitForum'),
      icon: 'i-lucide-house',
      to: localePath('/')
    },
    {
      label: t('nav.appearance'),
      icon: colorModeTriggerIcon.value,
      children: appearanceMenuItems.value
    },
    {
      label: t('admin.shell.signOut'),
      icon: 'i-lucide-log-out',
      onSelect: () => {
        void signOut()
      }
    }
  ]
])

function navigateAdminTab(tab: AdminTab) {
  adminTabs.activateTab(tab.id)
  void navigateTo(tab.to)
}

// 页签过多时保持横向滚动，激活项滚入可视区，避免被 flex 挤压
function scrollActiveAdminTabIntoView() {
  if (!import.meta.client) {
    return
  }

  nextTick(() => {
    const activeId = adminTabs.activeTabId.value
    const tabEl = document.querySelector<HTMLElement>(`[data-admin-tab-id="${CSS.escape(activeId)}"]`)
    tabEl?.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'nearest' })
  })
}

watch(() => adminTabs.activeTabId.value, () => {
  scrollActiveAdminTabIntoView()
})

async function signOut() {
  await request<null>('/auth/logout', {
    method: 'POST'
  }).catch(() => null)

  user.value = null
  adminTabs.resetTabs()
  await navigateTo(localePath('/login'))
}
</script>

<template>
  <UDashboardGroup storage-key="sforum-admin" class="sforum-admin-shell">
    <UDashboardSidebar
      id="sforum-admin-sidebar"
      collapsible
      resizable
      :default-size="16"
      :min-size="14"
      :max-size="22"
      class="sforum-admin-sidebar min-w-0 overflow-x-hidden border-r border-[var(--border-admin)] bg-[var(--bg-admin-sidebar)] text-[var(--text-admin-sidebar)]"
    >
      <template #header="{ collapsed }">
        <div class="flex h-[50px] min-w-0 max-w-full items-center gap-1 px-1">
          <NuxtLink
            :to="adminRoutes.path('/')"
            class="flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-1 py-1 text-[var(--text-admin-main)] hover:bg-[var(--bg-admin-sidebar-hover)]"
            aria-label="SForum"
          >
            <img
              src="/brand/sforum-logo.svg"
              alt=""
              class="size-[30px] shrink-0 object-contain"
            >
            <span v-if="!collapsed" class="min-w-0 flex-1 overflow-hidden">
              <span class="block truncate text-[14.5px] font-bold text-[var(--text-admin-main)]">
                SForum
              </span>
              <span class="block truncate text-xs font-medium text-slate-500 dark:text-zinc-400">
                {{ t('admin.shell.section') }}
              </span>
            </span>
          </NuxtLink>
          <NuxtLink
            v-if="!collapsed && systemUpdateAvailable"
            :to="systemUpdateSettingsRoute"
            class="shrink-0 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)]"
            :aria-label="systemUpdateLabel"
            :title="systemUpdateLabel"
          >
            <UBadge color="warning" variant="soft" size="sm" class="gap-1 font-mono text-[10px] font-semibold">
              <UIcon name="i-lucide-arrow-up-circle" class="size-3" />
              {{ sforumVersion }}
            </UBadge>
          </NuxtLink>
          <UBadge
            v-else-if="!collapsed"
            color="neutral"
            variant="soft"
            size="sm"
            class="shrink-0 font-mono text-[10px] font-semibold"
          >
            {{ sforumVersion }}
          </UBadge>
        </div>
      </template>

      <template #default="{ collapsed }">
        <!-- overflow-x-hidden：仅允许纵向滚动，避免侧栏出现横向滚动条 -->
        <div class="min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto pr-1">
          <UNavigationMenu
            :key="`${currentAdminPageId}:${professionalMode ? 'pro' : 'basic'}:${operationsMode ? 'ops' : 'noops'}`"
            :items="navigationItems"
            :collapsed="collapsed"
            tooltip
            highlight
            color="primary"
            orientation="vertical"
            class="min-w-0"
            :ui="sidebarNavigationUi"
          />
        </div>
      </template>

      <template #footer="{ collapsed }">
        <div class="flex min-w-0 w-full max-w-full flex-col gap-2">
          <!-- 桌面端快捷切换主题按钮 -->
          <ClientOnly>
            <UButton
              v-if="!collapsed"
              color="neutral"
              variant="ghost"
              block
              class="justify-start px-2 py-2 text-[var(--text-admin-sidebar)] hover:bg-[var(--bg-admin-sidebar-hover)] hover:text-[var(--text-admin-main)]"
              :aria-label="colorModeTriggerLabel"
              @click="cycleColorModePreference"
            >
              <UIcon :name="colorModeTriggerIcon" class="size-4" />
              <span class="text-sm font-semibold">
                {{ colorModeTriggerLabel }}
              </span>
            </UButton>
            <template #fallback>
              <UButton
                v-if="!collapsed"
                color="neutral"
                variant="ghost"
                block
                class="pointer-events-none justify-start px-2 py-2 text-[var(--text-admin-sidebar)]"
                :aria-label="t('nav.appearance')"
                aria-hidden="true"
                tabindex="-1"
                data-ssr-fallback="admin-appearance"
              >
                <UIcon name="i-tabler-brightness-filled" class="size-4" />
                <span class="text-sm font-semibold">{{ t('nav.appearance') }}</span>
              </UButton>
            </template>
          </ClientOnly>

          <UDropdownMenu :items="userMenuItems" :content="{ side: 'top', align: 'start' }">
            <UButton
              color="neutral"
              variant="ghost"
              block
              class="min-w-0 max-w-full justify-start overflow-hidden px-2 py-3.5 text-[var(--text-admin-sidebar)] hover:bg-[var(--bg-admin-sidebar-hover)] hover:text-[var(--text-admin-main)]"
              :class="{ 'justify-center': collapsed }"
            >
              <SFAvatar :name="displayName" :avatar="user?.avatar" size="md" class="shrink-0 shadow-sm border border-slate-100 dark:border-zinc-800" />
              <span v-if="!collapsed" class="min-w-0 flex-1 overflow-hidden text-left ml-2.5">
                <span class="block truncate text-base font-bold text-slate-900 dark:text-white">
                  {{ displayName }}
                </span>
                <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">
                  {{ user?.roleKeys?.join(', ') || t('admin.shell.member') }}
                </span>
              </span>
              <UIcon v-if="!collapsed" name="i-lucide-chevrons-up-down" class="size-4 shrink-0 text-slate-400 dark:text-zinc-500" />
            </UButton>
          </UDropdownMenu>
        </div>
      </template>
    </UDashboardSidebar>

    <UDashboardPanel class="sforum-admin-panel flex flex-col min-w-0 flex-1 bg-[var(--bg-admin-app)] text-[var(--text-admin-main)]">
      <!-- 1. 置顶全局 Topbar -->
      <div class="sforum-admin-topbar flex items-center justify-between h-[68px] sm:h-[76px] px-4 sm:px-8 bg-[var(--bg-admin-card)] border-b border-[var(--border-admin)] flex-shrink-0 z-20 transition-all">
        <div class="flex min-w-0 items-center gap-2 sm:gap-3">
          <UDashboardSidebarToggle class="shrink-0" />
          <div class="flex min-w-0 items-center gap-2 sm:gap-3">
            <span class="shrink-0 text-base sm:text-lg font-bold text-slate-900 dark:text-zinc-100 tracking-wide">
              {{ t('admin.shell.controlPanel', { siteName }) }}
            </span>
            <span class="shrink-0 text-sm text-slate-300 dark:text-zinc-600">/</span>
            <span class="truncate text-sm sm:text-base font-semibold text-slate-600 dark:text-zinc-300">{{ activeTabLabel }}</span>
          </div>
        </div>
        <div class="flex items-center gap-4 text-sm">
          <button
            type="button"
            class="inline-flex max-w-full items-center gap-2.5 rounded-full border border-[var(--sf-accent-soft-border)] bg-[var(--sf-accent-soft)] px-3 py-2 text-left text-slate-600 transition-colors hover:border-[var(--sf-accent)] hover:bg-[rgb(var(--sf-accent-rgb)/0.14)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] sm:px-4 sm:py-2.5 dark:border-[rgb(var(--sf-accent-rgb)/0.35)] dark:bg-[rgb(var(--sf-accent-rgb)/0.16)] dark:text-zinc-200 dark:hover:bg-[rgb(var(--sf-accent-rgb)/0.24)]"
            :aria-label="t('admin.shell.openAdvancedSettings')"
            @click="advancedSettingsOpen = true"
          >
            <span class="size-2.5 shrink-0 rounded-full bg-[var(--sf-accent)] shadow-[0_0_0_4px_rgb(var(--sf-accent-rgb)/0.12)] dark:bg-[var(--sf-accent-dark)] dark:shadow-[0_0_0_4px_rgb(var(--sf-accent-rgb)/0.18)] animate-pulse"></span>
            <span class="min-w-0 truncate">
              <span class="hidden sm:inline">{{ t('admin.shell.administratorLabel') }}:</span>
              <strong class="font-semibold text-slate-800 dark:text-zinc-50">{{ user?.username }}</strong>
            </span>
            <UIcon name="i-lucide-chevron-down" class="size-3.5 shrink-0 text-slate-400 dark:text-zinc-500" />
          </button>
        </div>
      </div>

      <!-- 2. 多页签栏：单项不收缩，超出横向滚动，避免被压扁 -->
      <div
        class="sforum-admin-tabs flex min-w-0 shrink-0 items-end h-[52px] px-3 gap-1.5 bg-[var(--bg-admin-card)] border-b border-[var(--border-admin)] overflow-x-auto overflow-y-hidden select-none no-scrollbar z-15"
        role="tablist"
        :aria-label="t('admin.shell.tabsLabel')"
      >
        <div
          v-for="tab in adminTabs.tabs.value"
          :key="tab.id"
          role="tab"
          :data-admin-tab-id="tab.id"
          :aria-selected="adminTabs.activeTabId.value === tab.id"
          class="group relative z-10 inline-flex h-[44px] max-w-[12.5rem] shrink-0 items-center gap-2 whitespace-nowrap border border-b-0 border-[var(--border-admin)] mb-[-1px] rounded-t-lg px-3 sm:px-4 text-sm font-semibold cursor-pointer transition-colors"
          :class="adminTabs.activeTabId.value === tab.id
            ? 'bg-[var(--bg-admin-app)] text-[var(--sf-accent)] border-[var(--border-admin)]'
            : 'bg-transparent text-slate-500 dark:text-zinc-400 border-transparent hover:text-[var(--text-admin-main)]'"
          @click="navigateAdminTab(tab)"
        >
          <UIcon :name="tab.icon" class="size-4.5 shrink-0" />
          <span class="min-w-0 truncate">{{ tab.label || (tab.labelKey ? t(tab.labelKey) : '') }}</span>

          <span
            v-if="tab.closable"
            class="inline-flex size-4.5 shrink-0 items-center justify-center rounded-full text-slate-500 dark:text-zinc-400 hover:bg-red-500/20 hover:text-red-500 transition-colors"
            :aria-label="t('admin.shell.closeTab')"
            @click.stop="adminTabs.closeTab(tab.id)"
          >
            <UIcon name="i-lucide-x" class="size-3" />
          </span>
        </div>
      </div>

      <!--
        3. 内容区滚动面板：footer 在区内随内容滚动，短页用 mt-auto 沉底。
        页面 slot 包一层 full-width 容器：多根页面（Fragment）的 UAlert/UCard 等
        不再与 footer 同为 flex 子项，避免宽度 shrink / 背景“没撑开”。
      -->
      <div class="sforum-admin-scroll-area flex min-h-0 flex-1 flex-col overflow-y-auto bg-[var(--bg-admin-app)] p-4 sm:p-6">
        <div class="sforum-admin-page min-w-0 w-full">
          <SFAdminSurfaceOutlet :page-id="currentAdminPageId" />
          <slot />
        </div>
        <SFAdminFooter />
      </div>
    </UDashboardPanel>

    <SFAdminSystemUpdatePrompt
      :status="systemUpdates.status.value"
      :settings-route="systemUpdateSettingsRoute"
    />

    <!-- 系统高级设置：专业模式 + 运维管理等侧栏偏好 -->
    <UModal v-model:open="advancedSettingsOpen" :ui="{ content: 'sm:max-w-md' }">
      <template #content>
        <div class="flex flex-col">
          <header class="flex items-start justify-between gap-4 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
            <div class="min-w-0">
              <h2 class="text-base font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.shell.advancedSettings.title') }}
              </h2>
              <p class="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-300">
                {{ t('admin.shell.advancedSettings.description') }}
              </p>
            </div>
            <UButton
              icon="i-lucide-x"
              color="neutral"
              variant="ghost"
              :aria-label="t('admin.shell.advancedSettings.close')"
              @click="() => { advancedSettingsOpen = false }"
            />
          </header>

          <div class="space-y-3 px-5 py-5">
            <div class="flex items-start justify-between gap-4 rounded-xl border border-slate-200 bg-slate-50/80 px-4 py-3.5 dark:border-zinc-800 dark:bg-zinc-900/50">
              <div class="min-w-0">
                <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.shell.advancedSettings.professionalMode.label') }}
                </p>
                <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-zinc-400">
                  {{ t('admin.shell.advancedSettings.professionalMode.description') }}
                </p>
              </div>
              <USwitch
                v-model="professionalMode"
                class="mt-0.5 shrink-0"
                :aria-label="t('admin.shell.advancedSettings.professionalMode.label')"
              />
            </div>

            <div class="flex items-start justify-between gap-4 rounded-xl border border-slate-200 bg-slate-50/80 px-4 py-3.5 dark:border-zinc-800 dark:bg-zinc-900/50">
              <div class="min-w-0">
                <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.shell.advancedSettings.operationsMode.label') }}
                </p>
                <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-zinc-400">
                  {{ t('admin.shell.advancedSettings.operationsMode.description') }}
                </p>
              </div>
              <USwitch
                v-model="operationsMode"
                class="mt-0.5 shrink-0"
                :aria-label="t('admin.shell.advancedSettings.operationsMode.label')"
              />
            </div>
          </div>

          <footer class="flex justify-end border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
            <UButton
              color="neutral"
              variant="soft"
              @click="() => { advancedSettingsOpen = false }"
            >
              {{ t('admin.shell.advancedSettings.close') }}
            </UButton>
          </footer>
        </div>
      </template>
    </UModal>
  </UDashboardGroup>
</template>
