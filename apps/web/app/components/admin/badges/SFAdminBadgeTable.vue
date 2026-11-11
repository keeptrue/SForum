<script setup lang="ts">
/**
 * 后台徽章列表：九列，窄屏按列 `meta.class` 逐列收起，不做横向滚动。
 *
 * 纯展示 + 事件上抛：CRUD、授予、重算都在页面与 useAdminBadges 里，表格不持有状态。
 * tier 列不允许只用颜色表达级别，所以固定是「图标 + tier 名」，色只染图标。
 */
import { adminBadgeMetricLabel, adminBadgeRule, type AdminBadgeRow, type AdminBadgeSortDir, type AdminBadgeSortKey } from '~/utils/admin/adminBadges'
import { forumBadgeTier, forumBadgeTierIcon } from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  badges: AdminBadgeRow[]
  pending?: boolean
  /** 只有 badge.manage 才渲染操作按钮；无权限时列仍保留表头。 */
  canManage?: boolean
  /** 当前排序键；'' 表示保持后端默认次序（分组 position → 徽章 position）。 */
  sortKey?: AdminBadgeSortKey | ''
  sortDir?: AdminBadgeSortDir
}>(), {
  pending: false,
  canManage: false,
  sortKey: '',
  sortDir: 'asc'
})

const emit = defineEmits<{
  edit: [badge: AdminBadgeRow]
  grant: [badge: AdminBadgeRow]
  toggle: [badge: AdminBadgeRow]
  sort: [key: AdminBadgeSortKey]
}>()

const { t, te } = useI18n()

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

function tierLabel(badge: AdminBadgeRow) {
  return t(`badges.tier.${tierOf(badge)}`)
}

function ruleOf(badge: AdminBadgeRow) {
  return adminBadgeRule(badge)
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
      <span class="sf-admin-badge-icon" :data-tier="tierOf(row.original)">
        <UIcon
          :name="row.original.icon || forumBadgeTierIcon(row.original.tier)"
          class="size-[18px]"
          aria-hidden="true"
        />
      </span>
    </template>

    <template #name-header="{ column }">
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

    <template #tier-header="{ column }">
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
      <span class="sf-admin-badge-tier" :data-tier="tierOf(row.original)">
        <UIcon :name="forumBadgeTierIcon(row.original.tier)" class="size-3.5" aria-hidden="true" />
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

    <template #grantedCount-header="{ column }">
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
          :aria-label="t('admin.badges.actions.grantLabel', { name: row.original.name })"
          :title="t('admin.badges.actions.grant')"
          @click="emit('grant', row.original)"
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
.sf-admin-badge-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: var(--sf-badge-icon-soft, var(--sf-muted));
  color: var(--sf-badge-icon-hue, var(--sf-fg-secondary));
}

.sf-admin-badge-icon[data-tier="gold"] {
  --sf-badge-icon-hue: var(--sf-accent);
  --sf-badge-icon-soft: var(--sf-accent-soft);
}

.sf-admin-badge-icon[data-tier="silver"] {
  --sf-badge-icon-hue: var(--sf-fg-secondary);
  --sf-badge-icon-soft: var(--sf-muted);
}

.sf-admin-badge-icon[data-tier="bronze"] {
  --sf-badge-icon-hue: var(--sf-warning);
  --sf-badge-icon-soft: var(--sf-warning-soft);
}

/* tier 列：色只上图标，文字恒为 --sf-fg，避免 11px 文字吃低对比度。 */
.sf-admin-badge-tier {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--sf-fg);
  font-size: 12px;
  font-weight: 600;
}

.sf-admin-badge-tier[data-tier="gold"] {
  --sf-badge-icon-hue: var(--sf-accent);
  color: var(--sf-fg);
}

.sf-admin-badge-tier[data-tier="gold"] :deep(svg) {
  color: var(--sf-accent);
}

.sf-admin-badge-tier[data-tier="silver"] :deep(svg) {
  color: var(--sf-fg-secondary);
}

.sf-admin-badge-tier[data-tier="bronze"] :deep(svg) {
  color: var(--sf-warning);
}

.dark .sf-admin-badge-icon[data-tier="gold"] {
  --sf-badge-icon-hue: var(--sf-accent-dark);
}
</style>
