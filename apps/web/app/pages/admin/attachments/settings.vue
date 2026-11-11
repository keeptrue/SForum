<script setup lang="ts">
import type { Component } from 'vue'
import SFAdminAttachmentCompressionTab from '~/components/admin/settings/attachments/tabs/SFAdminAttachmentCompressionTab.vue'
import SFAdminAttachmentSettingsTab from '~/components/admin/settings/attachments/tabs/SFAdminAttachmentSettingsTab.vue'
import SFAdminAttachmentUploadPoliciesTab from '~/components/admin/settings/attachments/tabs/SFAdminAttachmentUploadPoliciesTab.vue'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminAttachmentSettings' })

type RefreshablePage = { refresh?: () => Promise<void>, pending?: boolean }
type AttachmentConfigurationTab = 'basic' | 'compression' | 'policies'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const adminPage = useAdminPage('/attachments/settings')
const { can } = usePermissions()
const pageRef = ref<RefreshablePage | null>(null)
const tabs = computed(() => {
  const items: Array<{ id: AttachmentConfigurationTab, label: string, icon: string }> = []
  if (can(FORUM_PERMISSIONS.attachmentSettingsManage)) {
    items.push(
      { id: 'basic', label: t('admin.attachments.tabs.settings'), icon: 'i-lucide-sliders-horizontal' },
      { id: 'compression', label: t('admin.attachments.tabs.compression'), icon: 'i-lucide-image-down' }
    )
  }
  if (can(FORUM_PERMISSIONS.attachmentUploadPolicyManage)) {
    items.push({ id: 'policies', label: t('admin.attachments.tabs.policies'), icon: 'i-lucide-shield-check' })
  }
  return items
})
const activeTab = ref<AttachmentConfigurationTab>(normalizeTab(route.query.tab))
const components: Record<AttachmentConfigurationTab, Component> = {
  basic: SFAdminAttachmentSettingsTab,
  compression: SFAdminAttachmentCompressionTab,
  policies: SFAdminAttachmentUploadPoliciesTab
}
const activeComponent = computed(() => components[activeTab.value])
const toolbarPending = computed(() => Boolean(pageRef.value?.pending))
const canRefresh = computed(() => Boolean(pageRef.value?.refresh))
const toolbarText = computed(() => {
  if (activeTab.value === 'compression') return t('admin.attachments.compression.toolbar')
  if (activeTab.value === 'policies') return t('admin.attachments.uploadPolicies.toolbar')
  return t('admin.attachments.toolbar')
})

watch(() => route.query.tab, value => {
  activeTab.value = normalizeTab(value)
})
watch(tabs, items => {
  if (!items.some(item => item.id === activeTab.value) && items[0]) activeTab.value = items[0].id
})
watch(activeTab, async tab => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({ title: t('admin.attachments.configuration') })

function normalizeTab(value: unknown): AttachmentConfigurationTab {
  const raw = Array.isArray(value) ? value[0] : value
  if (raw === 'policies' && can(FORUM_PERMISSIONS.attachmentUploadPolicyManage)) return 'policies'
  if ((raw === 'basic' || raw === 'compression') && can(FORUM_PERMISSIONS.attachmentSettingsManage)) return raw
  return (tabs.value[0]?.id || 'basic') as AttachmentConfigurationTab
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

async function refreshPage() {
  await pageRef.value?.refresh?.()
}
</script>

<template>
  <div class="w-full min-w-0">
    <header class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.attachments.configuration') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.attachments.configurationDescription') }}
      </p>
    </header>

    <UDashboardToolbar class="mb-6 overflow-x-hidden rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-paperclip" class="size-4" />
          <span class="truncate">{{ toolbarText }}</span>
        </div>
      </template>
      <template #right>
        <UButton v-if="canRefresh" color="neutral" variant="outline" leading-icon="i-lucide-refresh-cw" :loading="toolbarPending" @click="refreshPage">
          {{ t('admin.attachments.refresh') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <SFAdminFixedTabNav
        :items="tabs"
        :model-value="activeTab"
        :ariaLabel="t('admin.attachments.configurationTabsLabel')"
        @update:model-value="setActiveTab"
      />

      <KeepAlive>
        <component :is="activeComponent" :key="activeTab" ref="pageRef" />
      </KeepAlive>
    </div>
  </div>
</template>
