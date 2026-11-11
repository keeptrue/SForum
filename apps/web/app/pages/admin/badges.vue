<script setup lang="ts">
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminBadges } from '~/composables/admin/badges/useAdminBadges'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { toAdminBadgeInput, type AdminBadgeRow } from '~/utils/admin/adminBadges'
import SFAdminBadgeTable from '~/components/admin/badges/SFAdminBadgeTable.vue'
import SFAdminBadgeEditDialog from '~/components/admin/badges/SFAdminBadgeEditDialog.vue'
import SFAdminBadgeGrantDialog from '~/components/admin/badges/SFAdminBadgeGrantDialog.vue'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})
defineOptions({
  name: 'AdminBadges'
})

const { t } = useI18n()
const adminPage = useAdminPage('/badges')
const { can } = useAuthSession()
const toast = useToast()

// 读用 badge.view（只读管理员也能看列表），写用 badge.manage：重算/编辑/授予都归后者。
const canManage = computed(() => can('badge.manage'))

const {
  badges,
  groups,
  pending,
  failed,
  blocked,
  load,
  updateBadge,
  recompute
} = useAdminBadges()

const editOpen = ref(false)
const grantOpen = ref(false)
const editing = ref<AdminBadgeRow | null>(null)
const granting = ref<AdminBadgeRow | null>(null)
const recomputing = ref(false)
const confirmRecompute = ref(false)
const togglePendingId = ref<number | null>(null)

onMounted(() => {
  void load()
})

function openCreate() {
  editing.value = null
  editOpen.value = true
}

function openEdit(badge: AdminBadgeRow) {
  editing.value = badge
  editOpen.value = true
}

function openGrant(badge: AdminBadgeRow) {
  granting.value = badge
  grantOpen.value = true
}

async function toggleEnabled(badge: AdminBadgeRow) {
  if (!canManage.value || togglePendingId.value !== null) return
  togglePendingId.value = badge.id
  try {
    await updateBadge(badge.id, toAdminBadgeInput({
      slug: badge.slug,
      name: badge.name,
      description: badge.description,
      groupId: badge.groupId ?? null,
      tier: badge.tier,
      icon: badge.icon,
      grantMode: badge.grantMode,
      metric: badge.metric ?? null,
      threshold: badge.threshold ?? null,
      enabled: !badge.enabled,
      position: badge.position
    }))
    toast.add({
      title: badge.enabled ? t('admin.badges.toast.disabled') : t('admin.badges.toast.enabled'),
      color: 'success',
      icon: 'i-lucide-check',
      duration: 5000
    })
  } catch {
    toast.add({ title: t('admin.badges.toast.saveFailed'), color: 'error', icon: 'i-lucide-triangle-alert', duration: 0 })
  } finally {
    togglePendingId.value = null
  }
}

async function runRecompute() {
  if (!canManage.value || recomputing.value) return
  if (!confirmRecompute.value) {
    confirmRecompute.value = true
    return
  }
  recomputing.value = true
  try {
    const changed = await recompute()
    toast.add({
      title: t('admin.badges.toast.recomputeStarted'),
      description: changed > 0 ? t('admin.badges.toast.recomputeChanged', { count: changed }) : undefined,
      color: 'success',
      icon: 'i-lucide-refresh-cw',
      duration: 5000
    })
  } catch {
    toast.add({ title: t('admin.badges.toast.recomputeFailed'), color: 'error', icon: 'i-lucide-triangle-alert', duration: 0 })
  } finally {
    recomputing.value = false
    confirmRecompute.value = false
  }
}
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <h1 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
          <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" aria-hidden="true" />
          {{ t('admin.badges.title') }}
        </h1>
        <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.intro') }}</p>
      </div>
      <UButton v-if="canManage" color="primary" icon="i-lucide-plus" class="self-start" @click="openCreate">
        {{ t('admin.badges.create') }}
      </UButton>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 items-center gap-2 text-sm">
          <UIcon name="i-lucide-award" class="size-4 shrink-0" aria-hidden="true" />
          <span class="truncate">{{ t('admin.badges.toolbar') }}</span>
        </div>
      </template>
      <template #right>
        <UButton color="neutral" variant="outline" icon="i-lucide-refresh-cw" :loading="pending" @click="load">
          {{ t('admin.badges.refresh') }}
        </UButton>
        <UButton
          v-if="canManage"
          color="neutral"
          variant="outline"
          icon="i-lucide-refresh-ccw-dot"
          :loading="recomputing"
          @click="runRecompute"
        >
          {{ confirmRecompute ? t('admin.badges.recompute.confirm') : t('admin.badges.recompute.action') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <UAlert
      v-if="blocked === 'disabled'"
      color="warning"
      variant="soft"
      icon="i-lucide-power-off"
      class="mb-4"
      :description="t('admin.badges.disabled')"
    />
    <UAlert
      v-else-if="blocked === 'forbidden'"
      color="error"
      variant="soft"
      icon="i-lucide-shield-off"
      class="mb-4"
      :description="t('admin.badges.forbidden')"
    />
    <UAlert
      v-else-if="failed"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      class="mb-4"
      :description="t('admin.badges.loadFailed')"
    />

    <SFAdminBadgeTable
      :badges="badges"
      :pending="pending"
      :can-manage="canManage"
      @edit="openEdit"
      @grant="openGrant"
      @toggle="toggleEnabled"
    />

    <SFAdminBadgeEditDialog v-model:open="editOpen" :groups="groups" :badge="editing" @saved="load" />
    <SFAdminBadgeGrantDialog v-model:open="grantOpen" :badge="granting" @saved="load" />
  </div>
</template>
