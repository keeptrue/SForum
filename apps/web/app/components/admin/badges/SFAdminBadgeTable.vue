<script setup lang="ts">
/**
 * 后台徽章列表：九列，窄屏按列 `meta.class` 逐列收起，不做横向滚动。
 *
 * 纯展示 + 事件上抛：CRUD、授予、重算都在页面与 useAdminBadges 里，表格不持有状态。
 * tier 列不允许只用颜色表达级别，所以固定是「图标 + 等级名」，色只染图标。
 *
 * 等级名/色/图标全部来自服务端等级表（`tiers` prop），不再写死 gold/silver/bronze 三套规则：
 * 运营新建的等级在列表里必须有名字与颜色，否则会显示成裸 key。
 */
import { adminBadgeMetricLabel, adminBadgeRule, type AdminBadgeRow, type AdminBadgeSortDir, type AdminBadgeSortKey } from '~/utils/admin/adminBadges'
import type { AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import { forumBadgeTier, forumBadgeTierIcon, forumBadgeTierLabel, forumBadgeTierStyle, pickTierName } from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  badges: AdminBadgeRow[]
  /** 服务端等级表：tier 列的名字、图标、色都从这里取。 */
  tiers?: AdminBadgeTierRow[]
  pending?: boolean
  /** 只有 badge.manage 才渲染操作按钮；无权限时列仍保留表头。 */
  canManage?: boolean
  /** 当前排序键；'' 表示保持后端默认次序（分组 position → 徽章 position）。 */
  sortKey?: AdminBadgeSortKey | ''
  sortDir?: AdminBadgeSortDir
}>(), {
  tiers: () => [],
  pending: false,
  canManage: false,
  sortKey: '',
  sortDir: 'asc'
})

const emit = defineEmits<{
  edit: [badge: AdminBadgeRow]
  grant: [badge: AdminBadgeRow]
  /** 点了「授予」但这枚徽章是自动徽章：由页面决定怎么解释（toast 归页面，表格不持有反馈状态）。 */
  grantBlocked: [badge: AdminBadgeRow]
  toggle: [badge: AdminBadgeRow]
  sort: [key: AdminBadgeSortKey]
}>()

const { t, te, locale } = useI18n()

// 断点口径（规范 §4.3）：≤640 收「分组 / 授予方式 / 已授予」，≤1023 再收「规则」。
const HIDDEN_SM = 'hidden sm:table-cell'
const HIDDEN_LG = 'hidden lg:table-cell'

const columns = computed(() => [
  {
    id: 'icon',
    header: '',
    meta: { class: { th: 'w-12', td: 'w-12' } }
  },
  {
    id: 'name',
    header: t('admin.badges.columns.name'),
    sortable: true,
    meta: { class: { th: 'min-w-[180px]', td: 'min-w-[180px]' } }
  },
  {
    id: 'group',
    header: t('admin.badges.columns.group'),
    meta: { class: { th: `w-[100px] ${HIDDEN_SM}`, td: `w-[100px] ${HIDDEN_SM}` } }
  },
  {
    id: 'tier',
    header: t('admin.badges.columns.tier'),
    sortable: true,
    meta: { class: { th: 'w-[88px]', td: 'w-[88px]' } }
  },
  {
    id: 'grantMode',
    header: t('admin.badges.columns.grantMode'),
    meta: { class: { th: `w-[96px] ${HIDDEN_SM}`, td: `w-[96px] ${HIDDEN_SM}` } }
  },
  {
    id: 'rule',
    header: t('admin.badges.columns.rule'),
    meta: { class: { th: `min-w-[160px] ${HIDDEN_LG}`, td: `min-w-[160px] ${HIDDEN_LG}` } }
  },
  {
    id: 'grantedCount',
    header: t('admin.badges.columns.grantedCount'),
    sortable: true,
    meta: { class: { th: `w-[96px] text-right ${HIDDEN_SM}`, td: `w-[96px] text-right ${HIDDEN_SM}` } }
  },
  {
    id: 'enabled',
    header: t('admin.badges.columns.enabled'),
    meta: { class: { th: 'w-[72px]', td: 'w-[72px]' } }
  },
  {
    id: 'actions',
    header: t('admin.badges.columns.actions'),
    meta: { class: { th: 'w-[96px]', td: 'w-[96px]' } }
  }
])

function tierOf(badge: AdminBadgeRow) {
  return forumBadgeTier(badge.tier)
}

function tierRow(badge: AdminBadgeRow): AdminBadgeTierRow | undefined {
  return props.tiers.find(tier => tier.key === tierOf(badge))
}

function tierLabel(badge: AdminBadgeRow) {
  const row = tierRow(badge)
  if (row) return pickTierName(row.name, locale.value)
  return forumBadgeTierLabel(badge, locale.value, t, te)
}

function tierIcon(badge: AdminBadgeRow) {
  return tierRow(badge)?.icon || forumBadgeTierIcon(badge.tier)
}

/** 等级色走行内变量：等级可增删，写死 [data-tier] 规则就等于把色表钉在前端。 */
function tierStyle(badge: AdminBadgeRow) {
  return forumBadgeTierStyle({ tier: tierOf(badge), tierColor: tierRow(badge)?.color })
}

function ruleOf(badge: AdminBadgeRow) {
  return adminBadgeRule(badge)
}

/**
 * 「授予」按钮的点击分流。
 *
 * 自动徽章不能用原生 `disabled`：禁用按钮不触发 click，用户点了什么也不会发生，
 * 而「为什么点不动」恰恰是他最需要知道的。所以按钮保持可聚焦、可点击，用
 * `aria-disabled` 表达状态，点击时上抛 grantBlocked 由页面给一句解释。
 */
function requestGrant(badge: AdminBadgeRow) {
  if (badge.grantMode !== 'manual') {
    emit('grantBlocked', badge)
    return
  }
  emit('grant', badge)
}

/** 可排序列头的图标与 aria 态由父层 query 驱动；表格本身不感知 URL。 */
function sortState(id: string): { active: boolean, dir: AdminBadgeSortDir | '' } {
  if (props.sortKey !== id) return { active: false, dir: '' }
  return { active: true, dir: props.sortDir }
}

function sortIcon(id: string): string {
  const state = sortState(id)
  if (!state.active) return 'i-lucide-chevrons-up-down'
  return state.dir === 'asc' ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'
}

function sortAria(id: string): string {
  const label = t(`admin.badges.columns.${id}`)
  const state = sortState(id)
  if (!state.active) return t('admin.badges.sort.inactive', { column: label })
  return state.dir === 'asc'
    ? t('admin.badges.sort.activeAsc', { column: label })
    : t('admin.badges.sort.activeDesc', { column: label })
}
</script>

<template>
  <UTable
    :data="props.badges"
    :columns="columns"
    :loading="props.pending"
    :empty="t('admin.badges.empty')"
    :caption="t('admin.badges.title')"
    sticky
    class="max-h-[calc(100vh-24rem)]"
  >
    <template #icon-cell="{ row }">
      <span class="sf-admin-badge-icon" :data-tier="tierOf(row.original)" :style="tierStyle(row.original)">
        <UIcon
          :name="row.original.icon || tierIcon(row.original)"
          class="size-[18px]"
          aria-hidden="true"
        />
      </span>
    </template>

    <template #name-header>
      <UButton
        color="neutral"
        variant="ghost"
        size="xs"
        class="-ml-2 font-semibold"
        :icon="sortIcon('name')"
        :aria-label="sortAria('name')"
        @click="emit('sort', 'name')"
      >
        {{ t('admin.badges.columns.name') }}
      </UButton>
    </template>

    <template #name-cell="{ row }">
      <div class="min-w-0">
        <div class="truncate font-semibold text-slate-900 dark:text-white">{{ row.original.name }}</div>
        <code class="text-xs text-slate-500 dark:text-zinc-400">{{ row.original.slug }}</code>
      </div>
    </template>

    <template #group-cell="{ row }">
      <span class="text-sm text-slate-600 dark:text-zinc-300">{{ row.original.groupName || '—' }}</span>
    </template>

    <template #tier-header>
      <UButton
        color="neutral"
        variant="ghost"
        size="xs"
        class="-ml-2 font-semibold"
        :icon="sortIcon('tier')"
        :aria-label="sortAria('tier')"
        @click="emit('sort', 'tier')"
      >
        {{ t('admin.badges.columns.tier') }}
      </UButton>
    </template>

    <template #tier-cell="{ row }">
      <span class="sf-admin-badge-tier" :data-tier="tierOf(row.original)" :style="tierStyle(row.original)">
        <!-- 颜色必须落在图标元素自己身上：@nuxt/icon 可能把图标渲染成「掩码元素」而不是
             `<svg>`（此时图标色 = currentColor），只给 svg 上色会漏掉一半渲染路径。 -->
        <UIcon
          :name="tierIcon(row.original)"
          class="size-3.5"
          :style="{ color: 'var(--sf-tier-hue)' }"
          aria-hidden="true"
        />
        <span>{{ tierLabel(row.original) }}</span>
      </span>
    </template>

    <template #grantMode-cell="{ row }">
      <UBadge color="neutral" variant="soft">
        {{ t(`admin.badges.grantMode.${row.original.grantMode}`) }}
      </UBadge>
    </template>

    <template #rule-cell="{ row }">
      <span v-if="ruleOf(row.original)" class="tabular-nums text-sm text-slate-600 dark:text-zinc-300">
        {{
          t('admin.badges.rule.metricThreshold', {
            metric: adminBadgeMetricLabel(ruleOf(row.original)?.metric, t, te),
            threshold: ruleOf(row.original)?.threshold
          })
        }}
      </span>
      <span v-else class="text-sm text-slate-400 dark:text-zinc-500">{{ t('admin.badges.rule.none') }}</span>
    </template>

    <template #grantedCount-header>
      <UButton
        color="neutral"
        variant="ghost"
        size="xs"
        class="-ml-1 font-semibold"
        :icon="sortIcon('grantedCount')"
        :aria-label="sortAria('grantedCount')"
        @click="emit('sort', 'grantedCount')"
      >
        {{ t('admin.badges.columns.grantedCount') }}
      </UButton>
    </template>

    <template #grantedCount-cell="{ row }">
      <span class="tabular-nums text-sm text-slate-600 dark:text-zinc-300">{{ row.original.grantedCount }}</span>
    </template>

    <template #enabled-cell="{ row }">
      <USwitch
        :model-value="row.original.enabled"
        :disabled="!props.canManage"
        :aria-label="t('admin.badges.actions.toggleLabel', { name: row.original.name })"
        @update:model-value="emit('toggle', row.original)"
      />
    </template>

    <template #actions-cell="{ row }">
      <div v-if="props.canManage" class="flex items-center gap-1">
        <UButton
          color="neutral"
          variant="ghost"
          size="xs"
          icon="i-lucide-pencil"
          :aria-label="t('admin.badges.actions.editLabel', { name: row.original.name })"
          :title="t('admin.badges.actions.edit')"
          @click="emit('edit', row.original)"
        />
        <UButton
          color="neutral"
          variant="ghost"
          size="xs"
          icon="i-lucide-award"
          :aria-disabled="row.original.grantMode !== 'manual'"
          :class="row.original.grantMode === 'manual' ? '' : 'opacity-40'"
          :aria-label="t('admin.badges.actions.grantLabel', { name: row.original.name })"
          :title="row.original.grantMode === 'manual'
            ? t('admin.badges.actions.grant')
            : t('admin.badges.actions.grantAutomaticHint')"
          @click="requestGrant(row.original)"
        />
        <UButton
          color="neutral"
          variant="ghost"
          size="xs"
          :icon="row.original.enabled ? 'i-lucide-ban' : 'i-lucide-power'"
          :aria-label="
            row.original.enabled
              ? t('admin.badges.actions.disableLabel', { name: row.original.name })
              : t('admin.badges.actions.enableLabel', { name: row.original.name })
          "
          :title="row.original.enabled ? t('admin.badges.actions.disable') : t('admin.badges.actions.enable')"
          @click="emit('toggle', row.original)"
        />
      </div>
      <span v-else class="text-sm text-slate-400 dark:text-zinc-500">—</span>
    </template>
  </UTable>
</template>

<style scoped>
/* 等级色：hue 由行内 `--sf-tier-hue-light/-dark` 提供，这里只按主题挑一个。 */
.sf-admin-badge-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  --sf-tier-hue: var(--sf-tier-hue-light);
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  color: var(--sf-tier-hue);
}

.dark .sf-admin-badge-icon {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}

/* tier 列：色只上图标，文字恒为 --sf-fg，避免 11px 文字吃低对比度。 */
.sf-admin-badge-tier {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  --sf-tier-hue: var(--sf-tier-hue-light);
  color: var(--sf-fg);
  font-size: 12px;
  font-weight: 600;
}

.sf-admin-badge-tier :deep(svg) {
  color: var(--sf-tier-hue);
}

.dark .sf-admin-badge-tier {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}
</style>
