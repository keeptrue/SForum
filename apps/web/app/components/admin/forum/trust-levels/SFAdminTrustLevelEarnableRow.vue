<script setup lang="ts">
/**
 * 能力解锁矩阵的**一行**（= 一张档位卡）：一档 + 该档新增解锁的勾选格。
 *
 * 从 `SFAdminTrustLevelEarnableMatrix.vue` 拆出来的原因写在 AGENTS.md 的文件体量规则里：
 * 矩阵本体 ~667 行里约 300 行是几何 CSS，N 档改造还要再加卡片布局，先按「一档」这个天然边界
 * 把行与它的几何切出去，矩阵只留容器语义（计数、基线说明、失败/加载、残留披露）。
 *
 * 语义不变：这里**只**写「该档新增」，不跨档复制键（累积语义归后端 `Earnable.Grants()`）；
 * 门控键（基线键）没有控件，只有标记。
 */
import { type TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { usePermissionText } from '~/composables/identity/usePermissionText'
import { trustLevelIconOf, trustLevelName } from '~/composables/profile/useTrustLevel'

const props = defineProps<{
  tier: TierForm
  /** active 位次（1 起）。 */
  rank: number
  keys: string[]
  baselineKeys: string[]
  editable: boolean
  /** 每行/每卡内勾选格的网格布局：表格行用 7 列，卡片用 2/3 列。 */
  layout: 'table' | 'cards'
  /** 末项是否跨列（余数判据由 `earnableMatrixState` 算出，这里只负责落地，不自己算）。 */
  spanTwo?: boolean
  spanThree?: boolean
  /** 机制说明的 id（`aria-describedby`）。 */
  noteId?: string
}>()

const emit = defineEmits<{
  toggle: [key: string]
}>()

const { t, locale } = useI18n()
const { permissionLabel } = usePermissionText()

function keyLabel(key: string) {
  return permissionLabel({ key, module: '' })
}

function isChecked(key: string) {
  return props.tier.earnable.includes(key)
}

function isGate(key: string) {
  return props.baselineKeys.includes(key)
}

/** 三元组顺序固定：档位 → 语义（新增解锁）→ 键名（规范 §7.1）。门控键没有勾选框，语义在标记上。 */
function cellLabel(key: string) {
  return t('admin.forum.trustLevels.earnable.aria.cell', {
    level: levelName.value,
    permission: keyLabel(key)
  })
}

function gateLabel(key: string) {
  return t('admin.forum.trustLevels.earnable.baseline.gateAria', {
    level: levelName.value,
    permission: keyLabel(key)
  })
}

const levelName = computed(() => trustLevelName(props.tier, locale.value, props.rank, t))
const tierIcon = computed(() => trustLevelIconOf(props.tier, props.rank))
</script>

<template>
  <fieldset
    class="sf-trust-earnable__row"
    :data-level="String(rank)"
    :data-step="String(Math.min(4, rank))"
    :data-tier-id="tier.id"
    :data-readonly="editable ? 'false' : 'true'"
    :data-layout="layout"
  >
    <legend class="sf-trust-earnable__legend">
      <span class="sf-trust-earnable__icon" :data-level="String(rank)" :data-step="String(Math.min(4, rank))" aria-hidden="true">
        <UIcon :name="tierIcon" class="sf-trust-earnable__glyph" />
      </span>
      <span class="sf-trust-earnable__name">{{ levelName }}</span>
      <!-- 与资料页卡同一键：TL 编号是标识符不是文案。 -->
      <span class="sf-trust-earnable__code">{{ t('profile.trustLevel.code', { level: rank }) }}</span>
    </legend>

    <div
      class="sf-trust-earnable__cells"
      :data-span-two="spanTwo ? 'true' : 'false'"
      :data-span-three="spanThree ? 'true' : 'false'"
    >
      <div v-for="key in keys" :key="`${tier.id}-${key}`" class="sf-trust-earnable__cell">
        <!-- 门控键没有控件：点了永远没用才是谎（规范 §2.2）。 -->
        <span
          v-if="isGate(key)"
          class="sf-trust-earnable__gate"
          role="img"
          data-sf-earnable-gate
          :title="t('admin.forum.trustLevels.earnable.baseline.gateTitle')"
          :aria-label="gateLabel(key)"
        >
          <UIcon name="i-tabler-shield-lock" class="sf-trust-earnable__gate-glyph" aria-hidden="true" />
        </span>
        <UCheckbox
          v-else
          :model-value="isChecked(key)"
          :disabled="!editable"
          :aria-disabled="!editable ? 'true' : 'false'"
          :aria-label="cellLabel(key)"
          :aria-describedby="noteId"
          @update:model-value="emit('toggle', key)"
        />
        <span class="sf-trust-earnable__cell-text" aria-hidden="true">
          <span class="sf-trust-earnable__cell-name">{{ keyLabel(key) }}</span>
          <span class="sf-trust-earnable__cell-key">{{ key }}</span>
        </span>
      </div>
    </div>
  </fieldset>
</template>

<style scoped>
.sf-trust-earnable__row {
  min-width: 0;
  margin: 0;
  padding: 0;
  border: 0;
}

/* 7–8 档切成卡片（D-N12）：卡片自带边框与内距，表格形态不需要。 */
.sf-trust-earnable__row[data-layout="cards"] {
  padding: 12px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
}

.sf-trust-earnable__legend {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  padding: 0;
}

.sf-trust-earnable__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-tertiary);
}

.sf-trust-earnable__icon[data-step="1"] {
  color: var(--sf-fg-secondary);
}

.sf-trust-earnable__icon[data-step="2"] {
  border-color: color-mix(in srgb, var(--sf-accent) 34%, transparent);
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
}

.sf-trust-earnable__icon[data-step="3"] {
  border: 2px solid color-mix(in srgb, var(--sf-accent) 55%, transparent);
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
}

.sf-trust-earnable__icon[data-step="4"] {
  border-color: var(--sf-accent);
  background: var(--sf-accent);
  color: var(--sf-card);
}

.sf-trust-earnable__glyph {
  width: 14px;
  height: 14px;
}

.sf-trust-earnable__name {
  color: inherit;
  font-size: 13px;
  font-weight: 600;
}

.sf-trust-earnable__code {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* 表格形态：与表头共用 `--sf-earnable-cols` 列模板（由矩阵容器注入），所以天然对齐。 */
.sf-trust-earnable__cells {
  display: grid;
  grid-template-columns: repeat(var(--sf-earnable-cols, 7), minmax(0, 1fr));
  gap: 8px;
}

/* 卡片形态：格内带名称，窄屏 2 列、≥640 三列；末项跨列由矩阵算出的余数决定（D-N12）。 */
.sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cells {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

@media (min-width: 640px) {
  .sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cells {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

/* 余数跨列：2 列时看 spanTwo，3 列时看 spanThree；表格形态由表头列模板对齐，不跨。 */
.sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cells[data-span-two="true"] > :last-child {
  grid-column: span 2;
}

@media (min-width: 640px) {
  .sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cells[data-span-two="true"] > :last-child {
    grid-column: auto;
  }

  .sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cells[data-span-three="true"] > :last-child {
    grid-column: span 3;
  }
}

.sf-trust-earnable__cell {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.sf-trust-earnable__cell-text {
  display: none;
  min-width: 0;
  flex-direction: column;
}

/* 卡片形态必须显示名称：没有表头可依赖。 */
.sf-trust-earnable__row[data-layout="cards"] .sf-trust-earnable__cell-text {
  display: flex;
}

.sf-trust-earnable__cell-name {
  overflow: hidden;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-earnable__cell-key {
  overflow: hidden;
  color: var(--sf-fg-tertiary);
  font-size: 10px;
  font-family: var(--sf-font-mono, ui-monospace, monospace);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-earnable__gate {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  color: var(--sf-fg-tertiary);
}

.sf-trust-earnable__gate-glyph {
  width: 14px;
  height: 14px;
}
</style>
