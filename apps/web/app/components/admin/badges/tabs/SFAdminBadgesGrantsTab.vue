<script setup lang="ts">
/**
 * 授予记录 tab：徽章下拉 + 记录表 + 手动收回（两步确认，不弹窗）。
 *
 * 懒加载：选中徽章才发 listGrants 请求；徽章切换与翻页都会重新拉取。
 * 收回按钮只对手动授予行渲染（grantedBy 非空）：自动/重算来源收回后端会
 * 422 forum.badge_manual_only 且规则会重新授予，前端不提供这个不可逆的假动作。
 * revoked:false 是幂等语义（本来未持有），单独 toast 分支，不当成功也不当失败。
 */
import SFAdminBadgeGrantDialog from '~/components/admin/badges/SFAdminBadgeGrantDialog.vue'
import type { useAdminBadges } from '~/composables/admin/badges/useAdminBadges'
import type { AdminBadgeGrantRow, AdminBadgeRow } from '~/utils/admin/adminBadges'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { grantRevocable, grantSourceOf } from '~/utils/admin/adminBadges'

const props = defineProps<{
  store: ReturnType<typeof useAdminBadges>
  badges: AdminBadgeRow[]
  canManage: boolean
}>()

const { t } = useI18n()
const toast = useToast()
const { can } = useAuthSession()

const canManage = computed(() => props.canManage && can('badge.manage'))

const PER_PAGE = 20

const badgeItems = computed(() => props.badges.map(badge => ({
  value: String(badge.id),
  label: badge.name
})))

const selectedBadgeId = ref<number | null>(null)
const selectedBadge = computed(() => props.badges.find(badge => badge.id === selectedBadgeId.value) ?? null)

const grants = ref<AdminBadgeGrantRow[]>([])
const total = ref(0)
const offset = ref(0)
const grantsPending = ref(false)
const grantsFailed = ref(false)
// 请求序号：切徽章/翻页会并发发请求，只有最后一次的响应允许写状态。
let grantsSeq = 0

const dialogOpen = ref(false)

function openGrantDialog() {
  dialogOpen.value = true
}
// 收回两步确认：按行独立记忆确认态（userId），第二次点击才发 DELETE。
const confirmRevokeId = ref<number | null>(null)
const revokingId = ref<number | null>(null)

// 默认选中第一枚徽章（目录非空时）；列表刷新后保持当前选择仍然有效。
watch(() => props.badges, (list) => {
  const first = list[0]
  if (selectedBadgeId.value === null && first) {
    selectedBadgeId.value = first.id
  }
  if (selectedBadgeId.value !== null && !list.some(badge => badge.id === selectedBadgeId.value)) {
    selectedBadgeId.value = first ? first.id : null
  }
}, { immediate: true })

// immediate 不能省：上面 props.badges 的 immediate 回调在本 watch 注册之前就同步设好了
// selectedBadgeId（目录已加载后切进本分区就是这条路），没有 immediate 就永远不发请求。
watch(selectedBadgeId, () => {
  offset.value = 0
  confirmRevokeId.value = null
  void loadGrants()
}, { immediate: true })

async function loadGrants() {
  if (selectedBadgeId.value === null) {
    grantsSeq += 1 // 作废在途请求，避免清空后被旧响应写回
    grantsPending.value = false
    grants.value = []
    total.value = 0
    return
  }
  const seq = ++grantsSeq
  grantsPending.value = true
  grantsFailed.value = false
  try {
    const result = await props.store.listGrants(selectedBadgeId.value, {
      perPage: PER_PAGE,
      offset: offset.value
    })
    if (seq !== grantsSeq) return
    grants.value = result.items
    total.value = result.total
  } catch {
    if (seq !== grantsSeq) return
    grantsFailed.value = true
    grants.value = []
    total.value = 0
  } finally {
    if (seq === grantsSeq) grantsPending.value = false
  }
}

const page = computed(() => Math.floor(offset.value / PER_PAGE) + 1)
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PER_PAGE)))

function gotoPage(delta: number) {
  const next = (page.value - 1 + delta) * PER_PAGE
  if (next < 0 || next >= total.value) return
  offset.value = next
  void loadGrants()
}

function onGranted() {
  // 授予成功同时刷新徽章目录（grantedCount 变化）与本 tab 记录。
  void props.store.load()
  void loadGrants()
}

function sourceLabel(row: AdminBadgeGrantRow): string {
  const source = grantSourceOf(row)
  if (source === 'manual') return t('admin.badges.grants.source.manual')
  if (source === 'recompute') return t('admin.badges.grants.source.recompute')
  return t('admin.badges.grants.source.automatic')
}

const SOURCE_COLOR: Record<string, 'primary' | 'info' | 'neutral'> = {
  manual: 'primary',
  recompute: 'info',
  automatic: 'neutral'
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

async function revoke(row: AdminBadgeGrantRow) {
  if (!canManage.value || selectedBadgeId.value === null || revokingId.value !== null) return
  if (confirmRevokeId.value !== row.userId) {
    confirmRevokeId.value = row.userId
    return
  }
  revokingId.value = row.userId
  try {
    const revoked = await props.store.revokeGrant(selectedBadgeId.value, row.userId)
    confirmRevokeId.value = null
    if (revoked) {
      toast.add({
        title: t('admin.badges.grants.toast.revoked', { name: row.displayName || row.username }),
        color: 'success',
        icon: 'i-lucide-check',
        duration: 5000
      })
    } else {
      // 幂等分支：授予行已不存在（别人已收回）。200 不是失败，但也不该说「已收回」。
      toast.add({
        title: t('admin.badges.grants.toast.notRevoked', { name: row.displayName || row.username }),
        color: 'warning',
        icon: 'i-lucide-info',
        duration: 5000
      })
    }
    void onGranted()
  } catch {
    toast.add({
      title: t('admin.badges.grants.toast.revokeFailed'),
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      duration: 0
    })
  } finally {
    revokingId.value = null
  }
}

defineOptions({ name: 'SFAdminBadgesGrantsTab' })
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <USelect
        :model-value="selectedBadgeId === null ? undefined : String(selectedBadgeId)"
        :items="badgeItems"
        :aria-label="t('admin.badges.grants.badge')"
        :placeholder="t('admin.badges.grants.badgePlaceholder')"
        class="w-56"
        @update:model-value="selectedBadgeId = Number($event)"
      />
      <UButton
        color="neutral"
        variant="outline"
        icon="i-lucide-refresh-cw"
        :loading="grantsPending"
        :disabled="selectedBadgeId === null"
        @click="loadGrants"
      >
        {{ t('admin.badges.refresh') }}
      </UButton>
      <UButton
        v-if="canManage && selectedBadge && selectedBadge.grantMode === 'manual'"
        color="primary"
        icon="i-lucide-award"
        class="ml-auto"
        @click="openGrantDialog"
      >
        {{ t('admin.badges.grants.grant') }}
      </UButton>
    </div>

    <p v-if="!selectedBadge" class="rounded-lg border border-dashed border-slate-300 p-8 text-center text-sm text-slate-500 dark:border-zinc-700 dark:text-zinc-400">
      {{ t('admin.badges.grants.badgePlaceholder') }}
    </p>

    <template v-else>
      <div class="mb-3 flex items-center justify-between gap-2 text-sm text-slate-500 dark:text-zinc-400">
        <span>{{ t('admin.badges.grants.total', { count: total }) }}</span>
        <span v-if="pageCount > 1" class="flex items-center gap-1">
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-chevron-left"
            :disabled="page <= 1"
            :aria-label="t('admin.badges.grants.prevPage')"
            :title="t('admin.badges.grants.prevPage')"
            @click="gotoPage(-1)"
          />
          <span class="tabular-nums">{{ page }} / {{ pageCount }}</span>
          <UButton
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-chevron-right"
            :disabled="page >= pageCount"
            :aria-label="t('admin.badges.grants.nextPage')"
            :title="t('admin.badges.grants.nextPage')"
            @click="gotoPage(1)"
          />
        </span>
      </div>

      <UAlert
        v-if="grantsFailed"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        class="mb-4"
        :description="t('admin.badges.grants.loadFailed')"
      />

      <div v-else-if="grants.length === 0 && !grantsPending" class="rounded-lg border border-dashed border-slate-300 p-8 text-center dark:border-zinc-700">
        <UIcon name="i-lucide-award" class="mx-auto size-8 text-slate-400 dark:text-zinc-500" aria-hidden="true" />
        <p class="mt-3 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.grants.empty') }}</p>
      </div>

      <UTable
        v-else
        :data="grants"
        :columns="[
          { id: 'user', header: t('admin.badges.grants.columns.user') },
          { id: 'grantedAt', header: t('admin.badges.grants.columns.grantedAt'), meta: { class: { th: 'w-[180px]', td: 'w-[180px]' } } },
          { id: 'source', header: t('admin.badges.grants.columns.source'), meta: { class: { th: 'w-[110px]', td: 'w-[110px]' } } },
          { id: 'reason', header: t('admin.badges.grants.columns.reason') },
          { id: 'actions', header: t('admin.badges.columns.actions'), meta: { class: { th: 'w-[96px]', td: 'w-[96px]' } } }
        ]"
        :loading="grantsPending"
        :caption="t('admin.badges.grants.caption')"
      >
        <template #user-cell="{ row }">
          <div class="min-w-0">
            <div class="truncate font-medium text-slate-900 dark:text-white">
              {{ row.original.displayName || row.original.username }}
            </div>
            <code class="text-xs text-slate-500 dark:text-zinc-400">@{{ row.original.username }}</code>
          </div>
        </template>

        <template #grantedAt-cell="{ row }">
          <span class="text-sm text-slate-600 dark:text-zinc-300">{{ formatTime(row.original.grantedAt) }}</span>
        </template>

        <template #source-cell="{ row }">
          <UBadge :color="SOURCE_COLOR[grantSourceOf(row.original)]" variant="soft">
            {{ sourceLabel(row.original) }}
          </UBadge>
        </template>

        <template #reason-cell="{ row }">
          <span v-if="row.original.grantReason" class="text-sm text-slate-600 dark:text-zinc-300">
            {{ row.original.grantReason }}
          </span>
          <span v-else class="text-sm text-slate-400 dark:text-zinc-500">—</span>
        </template>

        <template #actions-cell="{ row }">
          <!-- 只对手动授予行渲染收回：自动/重算来源的收回是 422 假动作。 -->
          <UButton
            v-if="canManage && grantRevocable(row.original)"
            color="neutral"
            variant="ghost"
            size="xs"
            :icon="confirmRevokeId === row.original.userId ? 'i-lucide-triangle-alert' : 'i-lucide-undo-2'"
            :loading="revokingId === row.original.userId"
            :aria-label="t('admin.badges.grants.revokeLabel', {
              name: row.original.displayName || row.original.username
            })"
            :title="confirmRevokeId === row.original.userId
              ? t('admin.badges.grants.revokeConfirm')
              : t('admin.badges.grants.revoke')"
            @click="revoke(row.original)"
          />
          <span v-else class="text-sm text-slate-400 dark:text-zinc-500">—</span>
        </template>
      </UTable>

      <p
        v-if="confirmRevokeId !== null && grants.some(row => row.userId === confirmRevokeId)"
        class="mt-2 text-sm text-amber-600 dark:text-amber-400"
      >
        {{ t('admin.badges.grants.revokeHint') }}
      </p>
    </template>

    <SFAdminBadgeGrantDialog v-model:open="dialogOpen" :badge="selectedBadge" :store="store" @saved="onGranted" />
  </div>
</template>
