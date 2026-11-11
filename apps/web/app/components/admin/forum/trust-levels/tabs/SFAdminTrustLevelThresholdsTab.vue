<script setup lang="ts">
/**
 * 后台「信任等级 · 阈值」分区（N 档版）。
 *
 * 本组件只画本分区，**不持有表单状态、不渲染页头/toolbar/tablist/footer**：
 * 表单归编排层，页头与吸底 footer 归 route shell。
 *
 * 档位来自 `form.tiers`（数组顺序 = 判定顺序，`rank` 由 active 位次派生）。
 * 停用档位保留配置、可继续编辑，只是判定跳过（D-N8）——它们带「已停用」标记，不是消失。
 *
 * D12 的「恢复默认不会影响已获得等级」披露点只有一条常驻 UAlert，紧贴「恢复推荐默认」按钮：
 * `runRestore` 没有确认步骤，这句话必须在按下按钮**之前**读得到，因此禁止改写成 Toast。
 */
import {
  blockNonIntegerKey,
  TRUST_LEVEL_THRESHOLD_METRICS,
  type TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { activeRankOf, type TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { trustLevelIconOf, trustLevelName } from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import type { TrustLevelSaveError } from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import SFAdminTrustLevelFieldErrors from '~/components/admin/forum/trust-levels/SFAdminTrustLevelFieldErrors.vue'

const props = defineProps<{
  form: TrustLevelsForm
  /** 写权限 `trust.manage`；没有时输入框禁用（保存按钮由 shell 统一禁用）。 */
  canManage: boolean
  /** 推荐值是否来自本地兜底（读面没给 `recommendedConfig`）：决定披露措辞的诚实度。 */
  usingFallbackRecommended?: boolean
  /** 按 tier id 归位的保存失败明细（P2）：空对象 = 没有字段级错误。 */
  saveErrors?: Record<string, TrustLevelSaveError[]>
  /** 服务端「恢复默认」请求进行中：按钮保持忙碌态，避免重复 POST。 */
  restoring?: boolean
}>()

const emit = defineEmits<{
  threshold: [tierId: string, suffix: string, value: string | number]
  restore: []
}>()

const { t, locale } = useI18n()

function rankOf(tier: TierForm) {
  return activeRankOf(props.form.tiers, tier.id)
}

function tierLabel(tier: TierForm) {
  const rank = rankOf(tier) ?? props.form.tiers.indexOf(tier) + 1
  return trustLevelName(tier, locale.value, rank, t)
}

function tierIcon(tier: TierForm) {
  return trustLevelIconOf(tier, rankOf(tier) ?? props.form.tiers.indexOf(tier))
}

/** 行根的 `--tl-accent`：停用档没有 rank，按数组位置取色（与 tierIcon 同一口径）。 */
function accentStyle(tier: TierForm) {
  return tierAccentStyle(tierColorOf(tier, rankOf(tier) ?? props.form.tiers.indexOf(tier)))
}

function metricLabel(i18nKey: string) {
  return t(`admin.forum.trustLevels.metric.${i18nKey}`)
}

// 阈值读写走这一对访问器：`noUncheckedIndexedAccess` 下双下标会触发 TS2532，且真缺键时会写出 NaN。
function thresholdValue(tier: TierForm, suffix: string) {
  return tier.requirements[suffix] ?? 0
}
</script>

<template>
  <div class="grid min-w-0 gap-6" data-sf-trust-levels-thresholds>
    <!-- D12 唯一披露点：runRestore 无确认框，这句必须常驻，不能改成 Toast。 -->
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="flex min-w-0 flex-1 flex-col gap-2">
        <UAlert
          color="neutral"
          variant="soft"
          icon="i-lucide-info"
          :title="t('admin.forum.trustLevels.restoreHint')"
          data-sf-trust-levels-restore-hint
        />
        <!-- 读面没给推荐值时说实话：本地兜底不是后端推荐值（D-N9）。 -->
        <UAlert
          v-if="usingFallbackRecommended"
          color="neutral"
          variant="soft"
          icon="i-lucide-triangle-alert"
          :title="t('admin.forum.trustLevels.restoreFallback.title')"
          :description="t('admin.forum.trustLevels.restoreFallback.description')"
          data-sf-trust-levels-restore-fallback
        />
      </div>
      <UButton
        type="button"
        color="neutral"
        variant="outline"
        leading-icon="i-lucide-rotate-ccw"
        :disabled="!canManage"
        :loading="restoring"
        class="self-start"
        data-sf-trust-levels-restore
        @click="emit('restore')"
      >
        {{ t('admin.forum.trustLevels.restoreDefaults') }}
      </UButton>
    </div>

    <!-- 一行一档：折叠会让运营漏改一档。 -->
    <fieldset
      v-for="tier in form.tiers"
      :key="tier.id"
      class="sf-trust-threshold-row mt-3.5 border-t border-slate-200 pt-3.5 first:mt-0 first:border-t-0 first:pt-0 dark:border-zinc-800"
      :style="accentStyle(tier)"
      :data-tier-id="tier.id"
      :data-retired="tier.retired ? 'true' : 'false'"
    >
      <legend class="sf-trust-threshold-row__legend">
        <span class="sf-trust-threshold-row__icon sf-tl-mark" :data-level="String(rankOf(tier) ?? 0)" :data-step="String(Math.min(4, rankOf(tier) ?? 0))" aria-hidden="true">
          <UIcon :name="tierIcon(tier)" class="sf-trust-threshold-row__glyph" />
        </span>
        <span class="sf-trust-threshold-row__name">{{ tierLabel(tier) }}</span>
        <!-- 与资料页卡同一键：TL 编号是标识符不是文案，一处格式、两处展示。 -->
        <span class="sf-trust-threshold-row__code">
          {{ rankOf(tier) === null ? t('admin.forum.trustLevels.tiers.retiredBadge') : t('profile.trustLevel.code', { level: rankOf(tier) }) }}
        </span>
      </legend>

      <SFAdminTrustLevelFieldErrors :errors="saveErrors?.[tier.id] ?? []" compact />

      <div class="sf-trust-threshold-row__grid">
        <UFormField
          v-for="metric in TRUST_LEVEL_THRESHOLD_METRICS"
          :key="metric.suffix"
          :label="metricLabel(metric.i18nKey)"
          :name="`trust.tier.${tier.id}.${metric.suffix}`"
        >
          <UInput
            :model-value="thresholdValue(tier, metric.suffix)"
            type="number"
            min="0"
            :max="metric.max"
            :disabled="!canManage"
            class="w-full"
            :data-sf-threshold-input="`${tier.id}.${metric.suffix}`"
            @keydown="blockNonIntegerKey"
            @update:model-value="(value: string | number) => emit('threshold', tier.id, metric.suffix, value)"
          />
        </UFormField>
      </div>
    </fieldset>

    <!-- TL0 由 `trust.new_user_*` 决定，不在 `tiers` 数组里：只说明，不给控件。 -->
    <p class="text-xs text-muted" data-sf-trust-levels-tl0-hint>{{ t('admin.forum.trustLevels.tl0Hint') }}</p>

    <p class="text-xs text-muted" data-sf-trust-levels-zero-unlimited>
      {{ t('admin.forum.trustLevels.zeroUnlimited') }}
    </p>
  </div>
</template>

<style scoped>
.sf-trust-threshold-row {
  margin: 0;
  padding: 0;
  border: 0;
}

.sf-trust-threshold-row__legend {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
  padding: 0;
}

/* 24×24 比资料页卡小一档：后台行密度优先。几何与配色走共享 .sf-tl-mark，
   本分区只保留尺寸与字形大小（色相来自 fieldset 根上的内联 --tl-accent）。 */
.sf-trust-threshold-row__icon {
  width: 24px;
  --sf-tl-mark-radius: 8px;

  height: 24px;
}

.sf-trust-threshold-row__glyph {
  width: 14px;
  height: 14px;
}

/* 停用档位：虚线边框表达「保留但不参与判定」。 */
.sf-trust-threshold-row[data-retired="true"] .sf-trust-threshold-row__icon {
  border-style: dashed;
}

.sf-trust-threshold-row__name {
  color: inherit;
  font-size: 14px;
  font-weight: 600;
}

.sf-trust-threshold-row__code {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* ≤767（含 390）：2 列 + 末项跨满，无半格空隙、无横向溢出。 */
.sf-trust-threshold-row__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.sf-trust-threshold-row__grid > :last-child {
  grid-column: span 2;
}

/* 768–1279：3 列（6 项 → 3+3，无空格）。 */
@media (min-width: 768px) {
  .sf-trust-threshold-row__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .sf-trust-threshold-row__grid > :last-child {
    grid-column: auto;
  }
}

/* ≥1280：6 列 —— 恰好等于 metric 数（Go `Metrics` 6 项），一行一档才成立。 */
@media (min-width: 1280px) {
  .sf-trust-threshold-row__grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}
</style>
