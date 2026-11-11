<script setup lang="ts">
/**
 * 能力解锁（earnable）勾选矩阵 —— N 档版（M10 / D33 A 路 + D-N12）。
 *
 * 数据契约（只读，组件不自己请求、不自己保存）：
 * - `form`：宿主编排层的表单模型，`tiers[]` 里每档自带 `earnable: string[]`；
 * - `whitelist`：`useTrustLevelEarnableWhitelist()` 的四态，**唯一**可勾键来源（前端零硬编码，T22）；
 * - `canManage`：写权限 `trust.manage`。
 *
 * 三条不可退让的语义：
 * 1. **累积**：勾「某档的 topic.create」= 该档及以上全部解锁（后端 `Earnable.Grants()`）；UI 只写该档新增。
 * 2. **失败 ≠ 空**：`status === 'failed'` 时矩阵整块不渲染（换成锁定占位 + 错误条）。
 * 3. **键 → 人话复用既有权限文案**（`admin.permissionCatalog.<key>.label`）。
 *
 * 几何（D-N12）：≤6 档用表格（表头 + 裸勾选框），7–8 档切成「按档分组卡片」
 * （每档一张卡、卡内 7 个勾选格）。行与行的几何在 `SFAdminTrustLevelEarnableRow.vue`，
 * 本文件只剩容器语义（计数、基线说明、失败/加载、空态、残留披露）。
 */
import {
  earnableOrphanText,
  earnableMatrixState,
  isBaselineKey
} from '~/composables/admin/forum/trustLevels/useTrustLevelEarnableMatrix'
import type {
  TrustLevelWhitelistState,
  TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { usePermissionText } from '~/composables/identity/usePermissionText'
import { activeRankOf } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { trustLevelName } from '~/composables/profile/useTrustLevel'
import SFAdminTrustLevelEarnableRow from '~/components/admin/forum/trust-levels/SFAdminTrustLevelEarnableRow.vue'

const props = defineProps<{
  form: TrustLevelsForm
  whitelist: TrustLevelWhitelistState
  canManage: boolean
}>()

const emit = defineEmits<{
  toggle: [tierId: string, key: string]
  retry: []
}>()

const { t, locale } = useI18n()
const { permissionLabel } = usePermissionText()

const state = computed(() => earnableMatrixState({
  form: props.form,
  whitelist: props.whitelist,
  canManage: props.canManage
}))

function keyLabel(key: string) {
  return permissionLabel({ key, module: '' })
}

function tierLabel(tierId: string) {
  const tier = props.form.tiers.find(entry => entry.id === tierId)
  if (!tier) return tierId
  const rank = activeRankOf(props.form.tiers, tierId) ?? 0
  return trustLevelName(tier, locale.value, rank, t)
}

const orphanText = computed(() => earnableOrphanText(state.value.orphans, tierLabel))
/**
 * 系统基线键（默认安装下 TL0 不生效、TL1 起生效）——**只来自后端**，组件零字面量。
 * 空数组 = 后端没下发（或没有可披露的基线），此时所有基线标注与相关文案都不渲染。
 */
const hasBaselineKeys = computed(() => state.value.baselineKeys.length > 0)
const baselineKeyText = computed(() =>
  state.value.baselineKeys.map(key => keyLabel(key)).join(t('admin.forum.trustLevels.earnable.baseline.keysSeparator'))
)
const gateTitle = computed(() => t('admin.forum.trustLevels.earnable.baseline.gateTitle'))
</script>

<template>
  <section
    class="sf-trust-earnable"
    aria-labelledby="sf-trust-levels-panel-title"
    data-sf-earnable-region
    :data-layout="state.layout"
    :style="{ '--sf-earnable-cols': String(state.columnCount) }"
  >
    <div class="sf-trust-earnable__head">
      <!-- 计数徽标：勾选后读屏能听到「新增项 3/20」；失败态不渲染（没有可解释的对象）。 -->
      <span
        v-if="state.ready"
        class="sf-trust-earnable__count"
        aria-live="polite"
        data-sf-earnable-count
      >{{ t('admin.forum.trustLevels.earnable.count', { checked: state.checkedCount, total: state.totalCells }) }}</span>
    </div>

    <!--
      ① 基线说明（前提）：无条件常驻，不依赖白名单数据，所以 loading / failed 下照常渲染。
      字段在 → 走 gated 分支；字段不在 → 走兜底分支，只断言单键机制，不断言「人人都有」。
    -->
    <div class="sf-trust-earnable__baseline" data-sf-earnable-baseline>
      <p class="sf-trust-earnable__baseline-title">
        <UIcon name="i-tabler-info-circle" class="sf-trust-earnable__baseline-icon" aria-hidden="true" />
        <span>{{ hasBaselineKeys
          ? t('admin.forum.trustLevels.earnable.baseline.titleGated', { keys: baselineKeyText })
          : t('admin.forum.trustLevels.earnable.baseline.title') }}</span>
      </p>
      <p class="sf-trust-earnable__baseline-text">
        {{ hasBaselineKeys
          ? t('admin.forum.trustLevels.earnable.baseline.textGated', { keys: baselineKeyText })
          : t('admin.forum.trustLevels.earnable.baseline.text') }}
      </p>
    </div>

    <!-- ② 机制说明（累积语义）：必须在按下勾选**之前**读得到（T21）。失败态不渲染。 -->
    <p v-if="state.ready" id="sf-trust-earnable-note" class="sf-trust-earnable__note">
      {{ t('admin.forum.trustLevels.earnable.note') }}
    </p>

    <UAlert
      v-if="!canManage"
      class="sf-trust-earnable__alert"
      color="neutral"
      variant="soft"
      icon="i-tabler-lock"
      :title="t('admin.forum.trustLevels.earnable.readOnly.title')"
      :description="t('admin.forum.trustLevels.earnable.readOnly.description')"
      data-sf-earnable-readonly
    />

    <!-- 失败态（T22）：错误条 + 锁定占位，**整块不渲染矩阵**；错误不自动消失。 -->
    <template v-if="state.failed">
      <UAlert
        class="sf-trust-earnable__alert"
        color="error"
        variant="soft"
        icon="i-tabler-alert-triangle"
        :title="t('admin.forum.trustLevels.earnable.loadFailed.title')"
        data-sf-earnable-error
      >
        <template #description>
          <p>{{ t('admin.forum.trustLevels.earnable.loadFailed.description') }}</p>
          <p class="sf-trust-earnable__reason" data-sf-earnable-reason>
            {{ t('admin.forum.trustLevels.earnable.loadFailed.reason', { reason: whitelist.error || 'request_failed' }) }}
          </p>
          <UButton
            class="sf-trust-earnable__retry"
            size="xs"
            color="neutral"
            variant="outline"
            leading-icon="i-tabler-refresh"
            type="button"
            data-sf-earnable-retry
            @click="emit('retry')"
          >
            {{ t('admin.forum.trustLevels.earnable.loadFailed.retry') }}
          </UButton>
        </template>
      </UAlert>
      <div class="sf-trust-earnable__locked" data-sf-earnable-locked>
        <UIcon name="i-tabler-lock" class="sf-trust-earnable__locked-icon" aria-hidden="true" />
        <span>{{ t('admin.forum.trustLevels.earnable.loadFailed.locked') }}</span>
      </div>
    </template>

    <!-- 加载态：同尺寸占位，不加骨架微光；SSR 与客户端首帧同形。 -->
    <div v-else-if="!state.ready" class="sf-trust-earnable__locked" aria-busy="true" data-sf-earnable-loading>
      <span>{{ t('admin.forum.trustLevels.earnable.loading') }}</span>
    </div>

    <template v-else>
      <!-- 表头：只有表格形态有（卡片形态每格自带名称）；窄屏 display:none。 -->
      <div v-if="state.layout === 'table'" class="sf-trust-earnable__columns" aria-hidden="true">
        <span v-for="key in state.keys" :key="`head-${key}`" class="sf-trust-earnable__column">
          <span
            v-if="isBaselineKey(state, key)"
            class="sf-trust-earnable__column-gate"
            data-sf-earnable-gate-badge
            :title="gateTitle"
            :aria-label="gateTitle"
          >
            <UIcon name="i-tabler-shield-lock" class="sf-trust-earnable__gate-glyph" aria-hidden="true" />
            <span>{{ t('admin.forum.trustLevels.earnable.baseline.keyBadge') }}</span>
          </span>
          <span class="sf-trust-earnable__column-name">{{ keyLabel(key) }}</span>
          <span class="sf-trust-earnable__column-key">{{ key }}</span>
        </span>
      </div>

      <SFAdminTrustLevelEarnableRow
        v-for="row in state.rows"
        :key="row.id"
        :tier="row.tier"
        :rank="row.rank"
        :keys="state.keys"
        :baseline-keys="state.baselineKeys"
        :editable="state.editable"
        :layout="state.layout"
        :span-two="state.spanTwo"
        :span-three="state.spanThree"
        note-id="sf-trust-earnable-note"
        @toggle="(key: string) => emit('toggle', row.id, key)"
      />

      <!-- 空态：出厂状态（推荐默认 earnable = ∅），不是错误。矩阵照常渲染。 -->
      <p v-if="state.checkedCount === 0" class="sf-trust-earnable__empty" data-sf-earnable-empty>
        <UIcon name="i-tabler-info-circle" class="sf-trust-earnable__empty-icon" aria-hidden="true" />
        <span>
          {{ hasBaselineKeys
            ? t('admin.forum.trustLevels.earnable.emptyGated', { keys: baselineKeyText })
            : t('admin.forum.trustLevels.earnable.empty') }}
        </span>
      </p>

      <!-- 白名单外残留：保存会移除，只披露不提供「保留」（后端必拒）。 -->
      <UAlert
        v-if="state.orphans.length > 0"
        class="sf-trust-earnable__alert"
        color="warning"
        variant="soft"
        icon="i-tabler-alert-triangle"
        :title="t('admin.forum.trustLevels.earnable.orphan.title', { count: state.orphans.length })"
        :description="t('admin.forum.trustLevels.earnable.orphan.description', { keys: orphanText })"
        data-sf-earnable-orphans
      />
    </template>
  </section>
</template>

<style scoped>
.sf-trust-earnable {
  min-width: 0;
  border-top: 1px solid var(--sf-border, var(--color-slate-200));
  padding-top: 20px;
}

/* 卡片形态（7–8 档）：每档一张卡，档间 12px；表格形态保持原有分隔。 */
.sf-trust-earnable[data-layout="cards"] {
  display: grid;
  gap: 12px;
}

.sf-trust-earnable__head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.sf-trust-earnable__count {
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* 常驻行内说明：不折叠、不悬浮、不进 Toast。 */
.sf-trust-earnable__note {
  margin-top: 6px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
  line-height: 16px;
}

/* 基线说明：左边框 + 提一档字重表达「先读这段」，不占用严重性颜色通道（规范 §3.3.2）。 */
.sf-trust-earnable__baseline {
  margin-top: 8px;
  border-left: 2px solid var(--color-slate-300, #cbd5e1);
  padding-left: 12px;
}

.dark .sf-trust-earnable__baseline {
  border-left-color: var(--color-zinc-700, #3f3f46);
}

.sf-trust-earnable__baseline-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 2px;
  color: var(--color-slate-900, #0f172a);
  font-size: 13px;
  font-weight: 600;
  line-height: 20px;
}

.dark .sf-trust-earnable__baseline-title {
  color: var(--color-zinc-100, #f4f4f5);
}

.sf-trust-earnable__baseline-icon {
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
}

.sf-trust-earnable__baseline-text {
  color: var(--color-slate-600, #475569);
  font-size: 13px;
  line-height: 20px;
}

.dark .sf-trust-earnable__baseline-text {
  color: var(--color-zinc-300, #d4d4d8);
}

/* 表头与行共用同一列模板（列数 = 白名单键数），所以天然对齐。 */
.sf-trust-earnable__columns {
  display: grid;
  grid-template-columns: repeat(var(--sf-earnable-cols, 7), minmax(0, 1fr));
  gap: 8px;
  margin-top: 16px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--sf-border, var(--color-slate-200));
}

.sf-trust-earnable__column {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.sf-trust-earnable__column-gate {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 9px;
  font-weight: 700;
}

.sf-trust-earnable__gate-glyph {
  width: 10px;
  height: 10px;
}

.sf-trust-earnable__column-name {
  overflow: hidden;
  color: var(--color-slate-700, #334155);
  font-size: 11px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dark .sf-trust-earnable__column-name {
  color: var(--color-zinc-300, #d4d4d8);
}

.sf-trust-earnable__column-key {
  overflow: hidden;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 10px;
  font-family: var(--sf-font-mono, ui-monospace, monospace);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-earnable__alert {
  margin-top: 12px;
}

.sf-trust-earnable__reason {
  margin-top: 4px;
  font-family: var(--sf-font-mono, ui-monospace, monospace);
  font-size: 11px;
}

.sf-trust-earnable__retry {
  margin-top: 8px;
}

/* 锁定/加载占位：与矩阵同尺寸，切态不跳动。 */
.sf-trust-earnable__locked {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 180px;
  margin-top: 16px;
  border: 1px dashed var(--sf-border, var(--color-slate-200));
  border-radius: 8px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
  text-align: center;
}

.sf-trust-earnable__locked-icon {
  width: 16px;
  height: 16px;
}

.sf-trust-earnable__empty {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-top: 12px;
  color: var(--sf-fg-tertiary, var(--color-slate-500));
  font-size: 12px;
}

.sf-trust-earnable__empty-icon {
  width: 14px;
  height: 14px;
  margin-top: 2px;
  flex: 0 0 auto;
}

/* 表格形态的档间分隔（卡片形态由每个卡片自己的边框承担）。 */
.sf-trust-earnable[data-layout="table"] :deep(.sf-trust-earnable__row) {
  margin-top: 14px;
  padding-top: 14px;
  border-top: 1px solid var(--sf-border, var(--color-slate-200));
}

.sf-trust-earnable[data-layout="table"] :deep(.sf-trust-earnable__row:first-of-type) {
  border-top: 0;
}
</style>
