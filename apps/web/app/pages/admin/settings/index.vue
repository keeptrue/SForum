<script setup lang="ts">
import type { Component } from 'vue'
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import SFAdminSiteAccountSecurityTab from '~/components/admin/settings/site/tabs/SFAdminSiteAccountSecurityTab.vue'
import SFAdminSiteBasicTab from '~/components/admin/settings/site/tabs/SFAdminSiteBasicTab.vue'
import SFAdminSiteClientTab from '~/components/admin/settings/site/tabs/SFAdminSiteClientTab.vue'
import SFAdminSiteMaintenanceTab from '~/components/admin/settings/site/tabs/SFAdminSiteMaintenanceTab.vue'
import SFAdminSiteNewcomersTab from '~/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue'
import SFAdminSiteRegistrationTab from '~/components/admin/settings/site/tabs/SFAdminSiteRegistrationTab.vue'
import SFAdminTrustLevelsTab from '~/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue'
import SFAdminSiteUpdatesTab from '~/components/admin/settings/site/tabs/SFAdminSiteUpdatesTab.vue'
import SFAdminSiteVerificationTab from '~/components/admin/settings/site/tabs/SFAdminSiteVerificationTab.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminSettings'
})

type SettingsTab = 'basic' | 'accountSecurity' | 'registration' | 'newcomers' | 'trustLevels' | 'maintenance' | 'client' | 'verification' | 'updates'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const { options, fetchAdminEnvelope } = useWebOptions()
const adminPage = useAdminPage('/settings')
const validTabs: SettingsTab[] = ['basic', 'accountSecurity', 'registration', 'newcomers', 'trustLevels', 'maintenance', 'client', 'verification', 'updates']
const activeTab = ref<SettingsTab>(normalizeTab(route.query.tab))

const tabs = computed(() => [
  { id: 'basic', label: t('admin.settings.tabs.basic'), icon: 'i-lucide-sliders-horizontal' },
  { id: 'accountSecurity', label: t('admin.settings.tabs.accountSecurity'), icon: 'i-lucide-shield' },
  { id: 'registration', label: t('admin.settings.tabs.registration'), icon: 'i-lucide-user-plus' },
  { id: 'newcomers', label: t('admin.settings.tabs.newcomers'), icon: 'i-lucide-sprout' },
  { id: 'trustLevels', label: t('admin.settings.tabs.trustLevels'), icon: 'i-lucide-gauge' },
  { id: 'maintenance', label: t('admin.settings.tabs.maintenance'), icon: 'i-lucide-construction' },
  { id: 'client', label: t('admin.settings.tabs.client'), icon: 'i-lucide-smartphone' },
  { id: 'verification', label: t('admin.settings.tabs.verification'), icon: 'i-lucide-shield-check' },
  { id: 'updates', label: t('admin.settings.tabs.updates'), icon: 'i-lucide-refresh-cw' }
])
const tabComponents: Record<SettingsTab, Component> = {
  basic: SFAdminSiteBasicTab,
  accountSecurity: SFAdminSiteAccountSecurityTab,
  registration: SFAdminSiteRegistrationTab,
  newcomers: SFAdminSiteNewcomersTab,
  trustLevels: SFAdminTrustLevelsTab,
  maintenance: SFAdminSiteMaintenanceTab,
  client: SFAdminSiteClientTab,
  verification: SFAdminSiteVerificationTab,
  updates: SFAdminSiteUpdatesTab
}
const activeComponent = computed(() => tabComponents[activeTab.value])

const { data: adminWebOptions, pending, error, refresh } = await useAsyncData<AdminWebOption[]>(
  'admin-web-options',
  async () => (await fetchAdminEnvelope()).data
)

watch(() => route.query.tab, (tab) => {
  const normalized = normalizeTab(tab)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(activeTab, async (tab) => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({
  title: t('admin.settings.metaTitle')
})

function normalizeTab(value: unknown): SettingsTab {
  const candidate = Array.isArray(value) ? value[0] : value
  return typeof candidate === 'string' && validTabs.includes(candidate as SettingsTab)
    ? candidate as SettingsTab
    : 'basic'
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

function applySavedOptions(items: AdminWebOption[]) {
  adminWebOptions.value = items
  const publicOptions = items.filter(item => item.public && !item.secret)
  options.value = {
    ...options.value,
    ...Object.fromEntries(publicOptions.map(item => [item.name, item.value]))
  }
}
</script>

<template>
  <div class="mb-4">
    <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.settings.title') }}
    </h2>
  </div>

  <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 items-center gap-2 text-sm">
        <UIcon name="i-lucide-database" class="size-4" />
        <span class="truncate">{{ t('admin.settings.intro') }}</span>
      </div>
    </template>
    <template #right>
      <UButton color="neutral" variant="outline" leading-icon="i-lucide-refresh-cw" :loading="pending" @click="refresh()">
        {{ t('admin.settings.refresh') }}
      </UButton>
    </template>
  </UDashboardToolbar>

  <div class="flex flex-col gap-4">
    <UAlert v-if="error" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="t('admin.settings.loadFailed')" />
    <SFAdminFixedTabNav
      :items="tabs"
      :model-value="activeTab"
      :ariaLabel="t('admin.settings.tabs.label')"
      @update:model-value="setActiveTab"
    />
    <KeepAlive>
      <component
        :is="activeComponent"
        :key="activeTab"
        :items="adminWebOptions || []"
        @saved="applySavedOptions"
      />
    </KeepAlive>
  </div>
</template>
