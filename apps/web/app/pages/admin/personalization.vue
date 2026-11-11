<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import type { Component } from 'vue'
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import SFAdminPersonalizationAnnouncementsTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationAnnouncementsTab.vue'
import SFAdminPersonalizationAppearanceTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationAppearanceTab.vue'
import SFAdminPersonalizationBrandTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationBrandTab.vue'
import SFAdminPersonalizationFriendLinksTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationFriendLinksTab.vue'
import SFAdminPersonalizationLegalTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationLegalTab.vue'
import SFAdminPersonalizationNavigationTab from '~/components/admin/settings/personalization/tabs/SFAdminPersonalizationNavigationTab.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminPersonalization'
})

type PersonalizationTab = 'appearance' | 'brand' | 'nav' | 'announcements' | 'legal' | 'friendLinks'
type RefreshableTab = { refresh?: () => Promise<void>, loading?: boolean }

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { can } = usePermissions()
const { options, fetchAdminEnvelope } = useWebOptions()
const adminPage = useAdminPage('/personalization')
const childRef = ref<RefreshableTab | null>(null)
const canManageAppearance = computed(() => can('settings.appearance.manage'))
const canManageSiteChrome = computed(() => can('settings.site.manage'))
const allTabs = [
  { id: 'appearance' as const, labelKey: 'admin.personalization.tabs.appearance', icon: 'i-lucide-palette', permission: 'appearance' },
  { id: 'brand' as const, labelKey: 'admin.personalization.tabs.brand', icon: 'i-lucide-image', permission: 'site' },
  { id: 'nav' as const, labelKey: 'admin.personalization.tabs.nav', icon: 'i-lucide-menu', permission: 'site' },
  { id: 'announcements' as const, labelKey: 'admin.personalization.tabs.announcements', icon: 'i-lucide-megaphone', permission: 'site' },
  { id: 'legal' as const, labelKey: 'admin.personalization.tabs.legal', icon: 'i-lucide-scale', permission: 'site' },
  { id: 'friendLinks' as const, labelKey: 'admin.personalization.tabs.friendLinks', icon: 'i-lucide-external-link', permission: 'site' }
]
const tabs = computed(() => allTabs.filter(tab => tab.permission === 'appearance' ? canManageAppearance.value : canManageSiteChrome.value).map(tab => ({ id: tab.id, label: t(tab.labelKey), icon: tab.icon })))
const activeTab = ref<PersonalizationTab>(normalizeTab(route.query.tab))
const components: Record<PersonalizationTab, Component> = {
  appearance: SFAdminPersonalizationAppearanceTab,
  brand: SFAdminPersonalizationBrandTab,
  nav: SFAdminPersonalizationNavigationTab,
  announcements: SFAdminPersonalizationAnnouncementsTab,
  legal: SFAdminPersonalizationLegalTab,
  friendLinks: SFAdminPersonalizationFriendLinksTab
}
const activeComponent = computed(() => components[activeTab.value])
const optionTab = computed(() => ['appearance', 'brand', 'legal'].includes(activeTab.value))
const toolbarPending = computed(() => optionTab.value ? optionsPending.value : Boolean(childRef.value?.loading))

const { data: adminOptions, pending: optionsPending, error, refresh: refreshOptions } = await useAsyncData<AdminWebOption[] | null>(
  'admin-personalization-options',
  async () => canManageAppearance.value || canManageSiteChrome.value ? (await fetchAdminEnvelope()).data : null
)

watch(tabs, available => {
  if (!available.some(tab => tab.id === activeTab.value) && available[0]) activeTab.value = available[0].id
}, { immediate: true })
watch(() => route.query.tab, value => { activeTab.value = normalizeTab(value) })
watch(activeTab, async tab => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({ title: t('admin.personalization.metaTitle') })

function normalizeTab(value: unknown): PersonalizationTab {
  const raw = Array.isArray(value) ? value[0] : value
  const aliases: Record<string, PersonalizationTab> = { theme: 'appearance', footer: 'appearance', chrome: 'brand', 'friend-links': 'friendLinks', friends: 'friendLinks' }
  const candidate = typeof raw === 'string' ? aliases[raw] || raw as PersonalizationTab : 'appearance'
  return tabs.value.some(tab => tab.id === candidate) ? candidate : tabs.value[0]?.id || 'appearance'
}
function setActiveTab(value: string) { activeTab.value = normalizeTab(value) }
function applySavedOptions(items: AdminWebOption[]) {
  adminOptions.value = items
  options.value = { ...options.value, ...Object.fromEntries(items.filter(item => item.public && !item.secret).map(item => [item.name, item.value])) }
}
async function refreshActive() {
  if (optionTab.value) await refreshOptions()
  else await childRef.value?.refresh?.()
}
</script>

<template>
  <div class="w-full min-w-0">
    <div class="mb-4 flex flex-col gap-1"><h2 class="flex items-center gap-2 text-xl font-bold"><UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)]" />{{ t('admin.personalization.title') }}</h2><p class="text-sm text-muted">{{ t('admin.personalization.intro') }}</p></div>
    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900">
      <template #left><div class="flex items-center gap-2 text-sm"><UIcon name="i-lucide-swatch-book" class="size-4" /><span>{{ t('admin.personalization.toolbar') }}</span></div></template>
      <template #right><UButton color="neutral" variant="outline" leading-icon="i-lucide-refresh-cw" :loading="toolbarPending" @click="refreshActive">{{ t('admin.personalization.refresh') }}</UButton></template>
    </UDashboardToolbar>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <UAlert v-if="canManageSiteChrome" color="primary" variant="soft" icon="i-lucide-sparkles" :title="t('admin.siteChrome.recommendedTitle')" :description="t('admin.siteChrome.recommendedBody')" />
      <UAlert v-if="error" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="t('admin.personalization.loadFailed')" />
      <SFAdminFixedTabNav v-if="tabs.length" :items="tabs" :model-value="activeTab" :ariaLabel="t('admin.personalization.tabs.label')" @update:model-value="setActiveTab" />
      <KeepAlive v-if="tabs.length">
        <component :is="activeComponent" :key="activeTab" ref="childRef" :items="adminOptions || []" @saved="applySavedOptions" />
      </KeepAlive>
      <UAlert v-else color="warning" variant="soft" icon="i-lucide-lock" :title="t('admin.personalization.noAccessTitle')" :description="t('admin.personalization.noAccessBody')" />
    </div>
  </div>
</template>
