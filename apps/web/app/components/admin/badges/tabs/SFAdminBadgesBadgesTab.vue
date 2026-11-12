<script setup lang="ts">
/**
 * 徽章 tab：列表 + 筛选/排序 + 编辑/授予弹窗 + 启停开关。
 *
 * 筛选与排序状态进 URL query（q/group/tier/grantMode/enabled/sort/dir），
 * 深链可直接还原视图；数据层 store 由壳层创建并传入（单一数据源，三 tab 共享）。
 * 空态区分两种：目录本来就空（引导创建）与筛选把结果筛空（引导清除筛选）。
 */
import SFAdminBadgeTable from '~/components/admin/badges/SFAdminBadgeTable.vue'
import SFAdminBadgeEditDialog from '~/components/admin/badges/SFAdminBadgeEditDialog.vue'
import SFAdminBadgeGrantDialog from '~/components/admin/badges/SFAdminBadgeGrantDialog.vue'
import type { AdminBadgeGroupRow, useAdminBadges } from '~/composables/admin/badges/useAdminBadges'
import { pickTierName } from '~/utils/forum/forumBadge'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  badgeFiltersActive,
  filterBadges,
  sortBadges,
  tierOrderMap,
  toAdminBadgeInput,
  type AdminBadgeFilters,
  type AdminBadgeRow,
  type AdminBadgeSortDir,
  type AdminBadgeSortKey
} from '~/utils/admin/adminBadges'

export type AdminBadgesStore = ReturnType<typeof useAdminBadges>

const props = defineProps<{
  store: AdminBadgesStore
  badges: AdminBadgeRow[]
  groups: AdminBadgeGroupRow[]
  pending: boolean
  /** 壳层的 can（badge.manage）判定结果。 */
  canManage: boolean
}>()

const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const toast = useToast()
const { can } = useAuthSession()

// 读用 badge.view（壳层已放行），写动作仍需 badge.manage：无权限时按钮与开关全部隐藏/禁用。
const canManage = computed(() => props.canManage && can('badge.manage'))

const FILTER_KEYS = ['q', 'group', 'tier', 'grantMode', 'enabled', 'sort', 'dir'] as const

/**
 * 「全部/不筛选」在 Select 里的哨兵值。
 *
 * Reka UI 的 SelectItem 只接受非空字符串 value（空字符串保留给清空/placeholder
 * 语义），把空字符串当作 item value 会让整个 SelectContent 在 update 阶段抛错。
 * 所以下拉内部统一用 ALL_FILTER，落到 URL query 之前再翻译回空值 —— sentinel
 * 不泄漏进 query，`applyQuery` 依旧按「空值即 delete key」处理。
 */
const ALL_FILTER = 'all'

/**
 * 读侧白名单归一。
 *
 * URL 是可分享、也可手改的输入面：`?group=all` 或 `?enabled=all` 这类写侧永远不会
 * 产生、但用户能手敲出来的值，必须在读取时收口成「不筛选」。否则 `filterBadges`
 * 会把 'all' 当分组 id 比对（一条不剩落 noMatch），或让 `badgeFiltersActive` 误报
 * 「有筛选」而多出一个「清除筛选」按钮——读写契约就不对称了。
 */
const GRANT_MODE_VALUES = ['automatic', 'manual'] as const
const ENABLED_VALUES = ['true', 'false'] as const

function oneOf(value: string, allowed: readonly string[]): string {
  return allowed.includes(value) ? value : ''
}

/**
 * tier 的合法值 = 已加载的等级 key。
 *
 * 等级是运营可增删的数据，前端不写死三档：`?tier=platinum` 这种手改 URL 或已删除等级的
 * 旧深链必须在读侧收口成「不筛选」，否则会筛出一条不剩却看不出原因。
 */
const tierKeys = computed(() => props.store.tiers.value.map(tier => tier.key))

const filters = computed<AdminBadgeFilters>(() => {
  const group = queryValue(route.query.group)
  return {
    q: queryValue(route.query.q),
    // 分组只有两种合法形态：数字 id 或 'none'（未分组）。
    group: group === 'none' || /^\d+$/.test(group) ? group : '',
    tier: oneOf(queryValue(route.query.tier), tierKeys.value),
    grantMode: oneOf(queryValue(route.query.grantMode), GRANT_MODE_VALUES),
    enabled: oneOf(queryValue(route.query.enabled), ENABLED_VALUES)
  }
})

const sortKey = computed<AdminBadgeSortKey | ''>(() => {
  const value = queryValue(route.query.sort)
  return value === 'name' || value === 'tier' || value === 'grantedCount' ? value : ''
})
const sortDir = computed<AdminBadgeSortDir>(() => (queryValue(route.query.dir) === 'desc' ? 'desc' : 'asc'))

function queryValue(value: unknown): string {
  const candidate = Array.isArray(value) ? value[0] : value
  return typeof candidate === 'string' ? candidate : ''
}

/**
 * `route.query` 的值是 `string | null | string[]`，不是 `string`。
 * 先归一成单值字符串（数组取首项，与 `queryValue` 同口径），空值即丢弃——等于原来
 * 的 delete key，只是不会再让 `?q=a&q=b` 这种重复 key 因为 truthy 而弹回地址栏。
 */
function normalizeQuery(query: Record<string, unknown>): Record<string, string> {
  const normalized: Record<string, string> = {}
  for (const [key, raw] of Object.entries(query)) {
    const single = Array.isArray(raw) ? raw[0] : raw
    if (typeof single === 'string' && single !== '') normalized[key] = single
  }
  return normalized
}

async function applyQuery(patch: Partial<Record<(typeof FILTER_KEYS)[number], string>>) {
  await router.replace({ query: normalizeQuery({ ...route.query, ...patch }) })
}

function setFilters(patch: Partial<AdminBadgeFilters>) {
  void applyQuery(patch)
}

function clearFilters() {
  void applyQuery({ q: '', group: '', tier: '', grantMode: '', enabled: '' })
}

/** URL 空值 → 下拉哨兵值（Select 的 model-value 永远不是空串）。 */
function filterSelectModel(value: string) {
  return value || ALL_FILTER
}

/** 下拉哨兵值 → URL 空值，语义与 `clearFilters` 一致：空值即「不筛选」。 */
function onFilterSelect(key: 'group' | 'tier' | 'grantMode' | 'enabled', event: unknown) {
  const next = String(event ?? '')
  setFilters({ [key]: next === ALL_FILTER ? '' : next })
}

function onSort(key: AdminBadgeSortKey) {
  if (sortKey.value === key) {
    void applyQuery({ sort: key, dir: sortDir.value === 'asc' ? 'desc' : 'asc' })
    return
  }
  void applyQuery({ sort: key, dir: 'asc' })
}

const tierOrder = computed(() => tierOrderMap(props.store.tiers.value))
const visibleBadges = computed(() => sortBadges(
  filterBadges(props.badges, filters.value),
  sortKey.value,
  sortDir.value,
  tierOrder.value
))
const filtersActive = computed(() => badgeFiltersActive(filters.value))
const directoryEmpty = computed(() => props.badges.length === 0)

const groupItems = computed(() => [
  { value: ALL_FILTER, label: t('admin.badges.filters.allGroups') },
  { value: 'none', label: t('admin.badges.filters.ungrouped') },
  ...props.groups.map(group => ({ value: String(group.id), label: group.name }))
])

// 等级筛选项来自服务端等级表（含停用档：停用徽章仍可被筛出来处理）。
const tierItems = computed(() => [
  { value: ALL_FILTER, label: t('admin.badges.filters.all') },
  ...props.store.tiers.value
    .slice()
    .sort((left, right) => left.position - right.position)
    .map(tier => ({ value: tier.key, label: pickTierName(tier.name, locale.value) }))
])

const grantModeItems = computed(() => [
  { value: ALL_FILTER, label: t('admin.badges.filters.all') },
  { value: 'automatic', label: t('admin.badges.grantMode.automatic') },
  { value: 'manual', label: t('admin.badges.grantMode.manual') }
])

const enabledItems = computed(() => [
  { value: ALL_FILTER, label: t('admin.badges.filters.all') },
  { value: 'true', label: t('admin.badges.filters.enabled') },
  { value: 'false', label: t('admin.badges.filters.disabled') }
])

const editOpen = ref(false)
const grantOpen = ref(false)
const editing = ref<AdminBadgeRow | null>(null)
const granting = ref<AdminBadgeRow | null>(null)
const togglePendingId = ref<number | null>(null)

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

/**
 * 自动徽章的「授予」点击：不打开一个全禁用的弹窗，就地解释为什么不行。
 *
 * 按钮本身不是原生 disabled（那样 click 根本不触发，用户点了没反应也不知道原因），
 * 所以这个分支必须给一条能读完的说明，并指出下一步该怎么做。
 */
function onGrantBlocked(badge: AdminBadgeRow) {
  toast.add({
    title: t('admin.badges.actions.grantAutomaticHint'),
    description: t('admin.badges.actions.grantAutomaticDetail', { name: badge.name }),
    color: 'warning',
    icon: 'i-lucide-info',
    duration: 5000
  })
}

// 弹窗 saved 后统一重拉：编辑/删除可能改变分组与 grantedCount，全量刷新最稳。
function onSaved() {
  void props.store.load()
}

async function toggleEnabled(badge: AdminBadgeRow) {
  if (!canManage.value || togglePendingId.value !== null) return
  togglePendingId.value = badge.id
  try {
    await props.store.updateBadge(badge.id, toAdminBadgeInput({
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

defineOptions({ name: 'SFAdminBadgesBadgesTab' })
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-center gap-2">
      <UInput
        :model-value="filters.q"
        icon="i-lucide-search"
        :placeholder="t('admin.badges.filters.searchPlaceholder')"
        :aria-label="t('admin.badges.filters.search')"
        class="w-56"
        @update:model-value="setFilters({ q: String($event) })"
      />
      <USelect
        :model-value="filterSelectModel(filters.group)"
        :items="groupItems"
        :aria-label="t('admin.badges.filters.group')"
        class="w-40"
        @update:model-value="onFilterSelect('group', $event)"
      />
      <USelect
        :model-value="filterSelectModel(filters.tier)"
        :items="tierItems"
        :aria-label="t('admin.badges.filters.tier')"
        class="w-36"
        @update:model-value="onFilterSelect('tier', $event)"
      />
      <USelect
        :model-value="filterSelectModel(filters.grantMode)"
        :items="grantModeItems"
        :aria-label="t('admin.badges.filters.grantMode')"
        class="w-36"
        @update:model-value="onFilterSelect('grantMode', $event)"
      />
      <USelect
        :model-value="filterSelectModel(filters.enabled)"
        :items="enabledItems"
        :aria-label="t('admin.badges.filters.enabledState')"
        class="w-32"
        @update:model-value="onFilterSelect('enabled', $event)"
      />
      <UButton
        v-if="filtersActive"
        color="neutral"
        variant="ghost"
        icon="i-lucide-x"
        @click="clearFilters"
      >
        {{ t('admin.badges.filters.clear') }}
      </UButton>
      <UButton
        v-if="canManage"
        color="primary"
        icon="i-lucide-plus"
        class="ml-auto"
        @click="openCreate"
      >
        {{ t('admin.badges.create') }}
      </UButton>
    </div>

    <div v-if="directoryEmpty" class="rounded-lg border border-dashed border-slate-300 p-8 text-center dark:border-zinc-700">
      <UIcon name="i-lucide-medal" class="mx-auto size-8 text-slate-400 dark:text-zinc-500" aria-hidden="true" />
      <p class="mt-3 text-sm font-semibold text-slate-700 dark:text-zinc-200">{{ t('admin.badges.empty') }}</p>
      <p class="mx-auto mt-1 max-w-md text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.intro') }}</p>
    </div>
    <div v-else-if="visibleBadges.length === 0" class="rounded-lg border border-dashed border-slate-300 p-8 text-center dark:border-zinc-700">
      <p class="text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.badges.filters.noMatch') }}</p>
      <UButton color="neutral" variant="outline" icon="i-lucide-x" class="mt-3" @click="clearFilters">
        {{ t('admin.badges.filters.clear') }}
      </UButton>
    </div>

    <SFAdminBadgeTable
      v-else
      :badges="visibleBadges"
      :tiers="store.tiers.value"
      :pending="pending"
      :can-manage="canManage"
      :sort-key="sortKey"
      :sort-dir="sortDir"
      @edit="openEdit"
      @grant="openGrant"
      @grant-blocked="onGrantBlocked"
      @toggle="toggleEnabled"
      @sort="onSort"
    />

    <SFAdminBadgeEditDialog
      v-model:open="editOpen"
      :groups="groups"
      :tiers="store.tiers.value"
      :badge="editing"
      @saved="onSaved"
    />
    <SFAdminBadgeGrantDialog v-model:open="grantOpen" :badge="granting" :store="store" @saved="onSaved" />
  </div>
</template>
