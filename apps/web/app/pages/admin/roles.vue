<script setup lang="ts">
import type { AppliedRolePermission } from '~/components/admin/identity/roles/model'
import SFAdminRoleApprovalsTab from '~/components/admin/identity/roles/tabs/SFAdminRoleApprovalsTab.vue'
import SFAdminRoleGroupsTab from '~/components/admin/identity/roles/tabs/SFAdminRoleGroupsTab.vue'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminRoles'
})

type RolesTab = 'groups' | 'approvals'
type SelectItem = { label: string, value: string }
type RolesTabHandle = {
  refresh?: () => Promise<void>
  pending?: boolean
  startCreate?: () => void
  visibleCount?: number
  filterItems?: SelectItem[]
  selectedFilter?: string
  setFilter?: (value: unknown) => void
}

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const adminPage = useAdminPage('/roles')
const activeTab = ref<RolesTab>(normalizeTab(route.query.tab))
const activePanelRef = ref<RolesTabHandle | null>(null)
const search = ref('')
const appliedPermissionSequence = ref(0)
const appliedPermission = ref<AppliedRolePermission | null>(null)

const tabs = computed(() => [
  { id: 'groups', label: t('admin.roles.tabs.groups'), icon: 'i-lucide-users-round' },
  { id: 'approvals', label: t('admin.roles.tabs.approvals'), icon: 'i-lucide-shield-check' }
])
const activePending = computed(() => Boolean(activePanelRef.value?.pending))
const approvalFilterItems = computed(() => activePanelRef.value?.filterItems ?? [])
const approvalFilter = computed(() => activePanelRef.value?.selectedFilter ?? 'pending')

useSeoMeta({
  title: t('admin.roles.metaTitle')
})

watch(() => route.query.tab, value => {
  const normalized = normalizeTab(value)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(activeTab, async tab => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

function normalizeTab(value: unknown): RolesTab {
  const raw = Array.isArray(value) ? value[0] : value
  return raw === 'approvals' ? 'approvals' : 'groups'
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

function recordAppliedPermission(permission: Omit<AppliedRolePermission, 'sequence'>) {
  appliedPermissionSequence.value += 1
  appliedPermission.value = {
    ...permission,
    sequence: appliedPermissionSequence.value
  }
}
</script>

<template>
  <div class="w-full min-w-0">
    <header class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.roles.title') }}
      </h2>
      <p class="text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.roles.intro') }}</p>
    </header>

    <UDashboardToolbar class="mb-6 overflow-x-hidden rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <UInput
          v-if="activeTab === 'groups'"
          v-model="search"
          icon="i-lucide-search"
          :placeholder="t('admin.roles.searchPlaceholder')"
          class="w-40 max-w-full sm:w-72"
        />
        <USelect
          v-else
          :model-value="approvalFilter"
          :items="approvalFilterItems"
          value-key="value"
          class="w-44"
          :aria-label="t('admin.roles.suggestions.filterLabel')"
          @update:model-value="activePanelRef?.setFilter?.($event)"
        />
      </template>
      <template #right>
        <div class="flex items-center gap-2 sm:gap-3">
          <UButton
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-refresh-cw"
            :loading="activePending"
            class="border-slate-200 dark:border-zinc-700"
            :aria-label="activeTab === 'groups' ? t('admin.roles.refresh') : t('admin.roles.suggestions.refresh')"
            :title="activeTab === 'groups' ? t('admin.roles.refresh') : t('admin.roles.suggestions.refresh')"
            @click="activePanelRef?.refresh?.()"
          >
            <span class="hidden sm:inline">{{ t('admin.roles.refresh') }}</span>
          </UButton>
          <UButton
            v-if="activeTab === 'groups'"
            color="primary"
            leading-icon="i-lucide-plus"
            :aria-label="t('admin.roles.create')"
            :title="t('admin.roles.create')"
            @click="activePanelRef?.startCreate?.()"
          >
            <span class="hidden sm:inline">{{ t('admin.roles.create') }}</span>
          </UButton>
          <UBadge
            v-if="activeTab === 'groups'"
            color="neutral"
            variant="soft"
            class="hidden border border-slate-200 font-medium md:inline-flex dark:border-zinc-800"
          >
            {{ t('admin.roles.count', { count: activePanelRef?.visibleCount ?? 0 }) }}
          </UBadge>
        </div>
      </template>
    </UDashboardToolbar>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <SFAdminFixedTabNav
        :items="tabs"
        :model-value="activeTab"
        :ariaLabel="t('admin.roles.tabs.label')"
        @update:model-value="setActiveTab"
      />

      <KeepAlive>
        <SFAdminRoleGroupsTab
          v-if="activeTab === 'groups'"
          ref="activePanelRef"
          :search="search"
          :applied-permission="appliedPermission"
        />
        <SFAdminRoleApprovalsTab
          v-else
          ref="activePanelRef"
          @permission-applied="recordAppliedPermission"
        />
      </KeepAlive>
    </div>
  </div>
</template>
