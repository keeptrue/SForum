<script setup lang="ts">
/**
 * 徽章后台页壳：只承担 tab 选择、?tab= query 同步与顶部编排。
 *
 * 表格行动作（启停/编辑/授予）在 badges tab、分组 CRUD 在 groups tab、
 * 等级增删改与调序在 tiers tab、授予记录与收回在 grants tab；壳层持有唯一数据层实例（store）下发给三个
 * tab，保证任意 tab 的写动作刷新后其它 tab 立即看到同一份状态。
 *
 * 三态顶部告警（功能关闭 / 无权限 / 加载失败）与 toolbar 的统计、刷新、重算
 * 属于页面级编排，按 settings/index.vue 的几何契约留在壳层。
 */
import type { Component } from 'vue'
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import SFAdminBadgesBadgesTab from '~/components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue'
import SFAdminBadgesGroupsTab from '~/components/admin/badges/tabs/SFAdminBadgesGroupsTab.vue'
import SFAdminBadgesGrantsTab from '~/components/admin/badges/tabs/SFAdminBadgesGrantsTab.vue'
import SFAdminBadgesTiersTab from '~/components/admin/badges/tabs/SFAdminBadgesTiersTab.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useAdminBadges } from '~/composables/admin/badges/useAdminBadges'
import { useAdminBadgeConditions } from '~/composables/admin/badges/useAdminBadgeConditions'
import { useAdminBadgeRuleHealth } from '~/composables/admin/badges/useAdminBadgeRuleHealth'
import { useAdminBadgeBackfillStatus } from '~/composables/admin/badges/useAdminBadgeBackfillStatus'
import { recomputeToastDescription, shouldRefreshBackfillAfterRecompute, summarizeBackfillRuns } from '~/utils/admin/adminBadges'
import { useAuthSession } from '~/composables/identity/useAuthSession'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminBadges'
})

type BadgesTab = 'badges' | 'groups' | 'tiers' | 'grants'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const adminPage = useAdminPage('/badges')
const { can } = useAuthSession()
const toast = useToast()

// 读用 badge.view（只读管理员也能看列表），写用 badge.manage：重算/编辑/授予都归后者。
const canManage = computed(() => can('badge.manage'))

const store = useAdminBadges()
// 条件目录：端点未上线/失败时退化成空数组，弹窗自己提示；壳层不做第二套判断。
const badgeConditions = useAdminBadgeConditions()
// 规则健康清单：同样按「端点可能不存在」降级，失败只影响编辑弹窗里的一条提示。
const badgeRuleHealth = useAdminBadgeRuleHealth()
// 回扫进度：badges 分区筛选区上方那一行；端点失败退化成空清单 + 一条可见提示。
const badgeBackfill = useAdminBadgeBackfillStatus()
const backfillSummary = computed(() => summarizeBackfillRuns(badgeBackfill.runs.value))

const validTabs: BadgesTab[] = ['badges', 'groups', 'tiers', 'grants']
const activeTab = ref<BadgesTab>(normalizeTab(route.query.tab))

// 放在 tabs 之前：tabs 的每一项都读 blockedActive.value，声明晚于引用只是靠 computed
// 惰性求值侥幸不炸，谁加个 immediate 或在 setup 里读 tabs.value 就是 TDZ。
const blockedActive = computed(() => store.blocked.value !== '')

const tabs = computed(() => [
  { id: 'badges', label: t('admin.badges.tabs.badges'), icon: 'i-lucide-medal', disabled: blockedActive.value },
  { id: 'groups', label: t('admin.badges.tabs.groups'), icon: 'i-lucide-folder-open', disabled: blockedActive.value },
  { id: 'tiers', label: t('admin.badges.tabs.tiers'), icon: 'i-lucide-layers', disabled: blockedActive.value },
  { id: 'grants', label: t('admin.badges.tabs.grants'), icon: 'i-lucide-scroll-text', disabled: blockedActive.value }
])

const tabComponents: Record<BadgesTab, Component> = {
  badges: SFAdminBadgesBadgesTab,
  groups: SFAdminBadgesGroupsTab,
  tiers: SFAdminBadgesTiersTab,
  grants: SFAdminBadgesGrantsTab
}
const activeComponent = computed(() => tabComponents[activeTab.value])

// 统计紧凑数字全部从列表响应推导，不接新路由：总数 / 启用 / 自动 / 累计授予。
const stats = computed(() => {
  const list = store.badges.value
  return {
    total: list.length,
    enabled: list.filter(badge => badge.enabled).length,
    automatic: list.filter(badge => badge.grantMode === 'automatic').length,
    granted: list.reduce((sum, badge) => sum + badge.grantedCount, 0)
  }
})

// 功能关闭 / 加载失败时列表恒为空：渲染成「0 枚徽章」等于把「没数据」说成「数据是 0」。
const statsVisible = computed(() => !blockedActive.value && !store.failed.value)

// 每个分区只收到自己声明过的 props：统一传会让未声明的 props 变成 fallthrough attr
// （grants 分区不声明 groups/pending，DOM 里会落下 groups="[object Object]"）。
const tabProps = computed(() => {
  if (activeTab.value === 'grants') {
    return { store, badges: store.badges.value, canManage: canManage.value }
  }
  if (activeTab.value === 'tiers') {
    return {
      store,
      tiers: store.tiers.value,
      colorChoices: store.colorChoices.value,
      canManage: canManage.value
    }
  }
  return {
    store,
    badges: store.badges.value,
    groups: store.groups.value,
    pending: store.pending.value,
    canManage: canManage.value,
    catalog: badgeConditions.catalog.value,
    catalogFailed: badgeConditions.failed.value,
    factCandidates: badgeConditions.factCandidates.value,
    factCandidatesFailed: badgeConditions.factCandidatesFailed.value,
    refCandidates: badgeConditions.refCandidates.value,
    ruleHealth: badgeRuleHealth.items.value,
    ruleHealthFailed: badgeRuleHealth.failed.value,
    backfillSummary: backfillSummary.value,
    backfillFailed: badgeBackfill.failed.value,
    backfillPending: badgeBackfill.pending.value,
    backfillRefresh: () => { void badgeBackfill.load() }
  }
})

const recomputing = ref(false)
const confirmRecompute = ref(false)

onMounted(() => {
  void store.load()
  void badgeConditions.load()
  void badgeRuleHealth.load()
  void badgeBackfill.load()
})

watch(() => route.query.tab, (tab) => {
  const normalized = normalizeTab(tab)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(activeTab, async (tab) => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({
  title: t('admin.badges.metaTitle')
})

function normalizeTab(value: unknown): BadgesTab {
  const candidate = Array.isArray(value) ? value[0] : value
  return typeof candidate === 'string' && validTabs.includes(candidate as BadgesTab)
    ? candidate as BadgesTab
    : 'badges'
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

/** 重算按钮的可读名：两态（待确认 / 已确认）都必须有 aria-label 与 title。 */
const recomputeLabel = computed(() =>
  confirmRecompute.value ? t('admin.badges.recompute.confirm') : t('admin.badges.recompute.action')
)

async function runRecompute() {
  if (!canManage.value || recomputing.value) return
  if (!confirmRecompute.value) {
    confirmRecompute.value = true
    return
  }
  recomputing.value = true
  try {
    const result = await store.recompute()
    toast.add({
      title: t('admin.badges.toast.recomputeStarted'),
      // 三个数语义不同，措辞由纯函数统一决策（queued 与影响量各一句，都为 0 就不编）。
      description: recomputeToastDescription(result, t),
      color: 'success',
      icon: 'i-lucide-refresh-cw',
      duration: 5000
    })
    // queued > 0 才可能新增回扫 run：否则这一行没必要替一次空操作重拉。
    if (shouldRefreshBackfillAfterRecompute(result)) void badgeBackfill.load()
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
    <div class="mb-4">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" aria-hidden="true" />
        {{ t('admin.badges.title') }}
      </h2>
    </div>

    <UDashboardToolbar class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400">
      <template #left>
        <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <template v-if="statsVisible">
            <span class="whitespace-nowrap">{{ t('admin.badges.stats.badges', { total: stats.total }) }}</span>
            <span aria-hidden="true">·</span>
            <span class="whitespace-nowrap">{{ t('admin.badges.stats.enabled', { count: stats.enabled }) }}</span>
            <span aria-hidden="true">·</span>
            <span class="whitespace-nowrap">{{ t('admin.badges.stats.automatic', { count: stats.automatic }) }}</span>
            <span aria-hidden="true">·</span>
            <span class="whitespace-nowrap">{{ t('admin.badges.stats.granted', { count: stats.granted }) }}</span>
          </template>
          <span v-else class="text-slate-400 dark:text-zinc-500">—</span>
        </div>
      </template>
      <!-- 窄屏只留图标：文字进 hidden sm:inline，aria-label / title 两态都保留可读名。 -->
      <template #right>
        <UButton
          color="neutral"
          variant="outline"
          icon="i-lucide-refresh-cw"
          :loading="store.pending.value"
          :aria-label="t('admin.badges.refresh')"
          :title="t('admin.badges.refresh')"
          @click="store.load"
        >
          <span class="hidden sm:inline">{{ t('admin.badges.refresh') }}</span>
        </UButton>
        <UButton
          v-if="canManage"
          color="neutral"
          variant="outline"
          icon="i-lucide-refresh-ccw-dot"
          :loading="recomputing"
          :aria-label="recomputeLabel"
          :title="recomputeLabel"
          @click="runRecompute"
        >
          <span class="hidden sm:inline">{{ recomputeLabel }}</span>
        </UButton>
      </template>
    </UDashboardToolbar>

    <UAlert
      v-if="store.blocked.value === 'disabled'"
      color="warning"
      variant="soft"
      icon="i-lucide-power-off"
      class="mb-4"
      :description="t('admin.badges.disabled')"
    />
    <UAlert
      v-else-if="store.blocked.value === 'forbidden'"
      color="error"
      variant="soft"
      icon="i-lucide-shield-off"
      class="mb-4"
      :description="t('admin.badges.forbidden')"
    />
    <UAlert
      v-else-if="store.failed.value"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      class="mb-4"
      :description="t('admin.badges.loadFailed')"
    />

    <div class="flex flex-col gap-4">
      <SFAdminFixedTabNav
        :items="tabs"
        :model-value="activeTab"
        :ariaLabel="t('admin.badges.tabs.label')"
        @update:model-value="setActiveTab"
      />
      <!--
        内容区承载容器：与 pages/admin/permissions.vue 同款。卡片在 KeepAlive 外，
        切 tab 只换内部组件，卡片本身不重建（不闪）；不写 #header，分区名由
        SFAdminFixedTabNav 承担，加卡头会多出第三个标题层级。
      -->
      <UCard class="border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
        <KeepAlive>
          <component
            :is="activeComponent"
            :key="activeTab"
            v-bind="tabProps"
          />
        </KeepAlive>
      </UCard>
    </div>
  </div>
</template>
