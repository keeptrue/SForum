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
 *
 * 折叠面板（M3b，ui-spec §1）的三条不可让渡：
 * 1. 开关在 `<legend>` 最右端、高 24px（= 图标高度）→ **折叠态零位移**；
 * 2. 面板在数值网格**之后**（DOM 顺序 = Tab 顺序：6 个阈值输入 → 规则树）；
 * 3. 面板自身**零 overflow / 零 max-height / 零 sticky**：唯一滚动容器是
 *    `layouts/admin.vue` 的 `.sforum-admin-scroll-area`。为了「保险」加一层 `overflow-*`
 *    会造出与页面同轴的第二个 scroller（徽章那刀实测过）。
 */
import {
  blockNonIntegerKey,
  TRUST_LEVEL_THRESHOLD_METRICS,
  type TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate } from '~/utils/admin/rule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import { ruleLeaves } from '~/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import type { TrustLevelRuleDocument } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import type { TrustLevelConditionsStatus } from '~/composables/admin/forum/trustLevels/useAdminTrustLevelConditions'
import SFAdminTrustLevelRuleSection from '~/components/admin/forum/trust-levels/SFAdminTrustLevelRuleSection.vue'
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
  /**
   * 当前展开的档位 id（**单开手风琴**，`null` = 全部折叠）。
   * 状态归编排层所有：切 tab 不丢、422 时要由它选中出错档。
   */
  expandedTierId?: string | null
  /** 条件目录三态与数据：面板内容直接消费，不在组件里二次判断。 */
  conditionsStatus?: TrustLevelConditionsStatus
  conditionsError?: string
  conditionsCatalog?: BadgeConditionCatalogEntry[]
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  refCandidates?: BadgeRefCandidateMap
  /** 功能开关关闭：面板显示 featureOff（优先于 loadFailed，别让运营去查网络）。 */
  trustLevelsEnabled?: boolean
}>()

const emit = defineEmits<{
  threshold: [tierId: string, suffix: string, value: string | number]
  restore: []
  'toggle-rule': [tierId: string]
  'retry-conditions': []
  rule: [tierId: string, rule: TrustLevelRuleDocument]
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

/** 面板/开关的 DOM id：`aria-controls` 必须能解析到具体元素（APG disclosure）。 */
function toggleId(tierId: string) {
  return `sf-tl-rule-toggle-${tierId}`
}

function panelId(tierId: string) {
  return `sf-tl-rule-panel-${tierId}`
}

/** 条件条数（含嵌套叶子）：开关文案「更多条件（{count}）」用。宽容遍历，脏规则也数得出来。 */
function ruleCount(tier: TierForm) {
  return ruleLeaves(tier.rule ?? null).length
}

function expanded(tierId: string) {
  return props.expandedTierId === tierId
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
      data-sf-tl-accent
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
        <!--
          展开开关：塞进 24px 图例行 → 折叠态零位移。
          只挂 `aria-expanded` + `aria-controls`，**不挂 `aria-label`**：可见文本已经是它的名字，
          再挂一遍会让读屏念两次（ui-spec §6.1）。
        -->
        <button
          type="button"
          class="sf-tl-rule-toggle"
          :id="toggleId(tier.id)"
          :data-sf-tl-rule-toggle="tier.id"
          :aria-expanded="expanded(tier.id) ? 'true' : 'false'"
          :aria-controls="panelId(tier.id)"
          @click="emit('toggle-rule', tier.id)"
        >
          {{ ruleCount(tier) > 0
            ? t('admin.forum.trustLevels.conditions.toggle.more', { count: ruleCount(tier) })
            : t('admin.forum.trustLevels.conditions.toggle.add') }}
          <UIcon
            name="i-lucide-chevron-down"
            class="sf-tl-rule-toggle__chevron"
            :class="{ 'sf-tl-rule-toggle__chevron--open': expanded(tier.id) }"
            aria-hidden="true"
          />
        </button>
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

      <!--
        面板：`[hidden]` 保证 `aria-controls` 始终能解析（APG disclosure），
        内层 `v-if` 保证收起时不挂载重型编辑器。**不用** max-height 动画（本页禁动效，
        而且那会变成隐藏的滚动容器）。
      -->
      <div
        class="sf-tl-rule-panel"
        :class="{ 'sf-tl-rule-panel--retired': tier.retired }"
        :id="panelId(tier.id)"
        role="region"
        :aria-labelledby="toggleId(tier.id)"
        :data-sf-tl-rule-panel="tier.id"
        :hidden="!expanded(tier.id)"
      >
        <SFAdminTrustLevelRuleSection
          v-if="expanded(tier.id)"
          :tier-id="tier.id"
          :rule="tier.rule ?? null"
          :catalog="conditionsCatalog ?? []"
          :fact-candidates="factCandidates ?? []"
          :fact-candidates-failed="factCandidatesFailed === true"
          :ref-candidates="refCandidates ?? {}"
          :conditions-status="conditionsStatus ?? 'idle'"
          :conditions-error="conditionsError ?? ''"
          :can-manage="canManage"
          :feature-off="trustLevelsEnabled === false"
          :retired="tier.retired"
          @update:rule="(rule: TrustLevelRuleDocument) => emit('rule', tier.id, rule)"
          @retry="emit('retry-conditions')"
        />
      </div>
    </fieldset>

    <!-- TL0 由 `trust.new_levels_*` 决定，不在 `tiers` 数组里：只说明，不给控件。 -->
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

/*
 * 档位名必须可收缩 + 省略：长名字（40 字上限）会把右对齐的展开开关顶出 390。
 * 这是本次改动**唯一**可能引入横向溢出的地方（ui-spec §1.1）。
 */
.sf-trust-threshold-row__name {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  color: inherit;
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
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

/*
 * 展开开关：高 24px = `.sf-trust-threshold-row__icon` 的高度，所以折叠态整行高度与今天
 * 逐像素相同。颜色只**消费** `--tl-accent`（祖先内联），绝不在这里声明——
 * 内联会压过 `sforum-trust-colors.css` 的 `.dark` 明暗选择。
 */
.sf-tl-rule-toggle {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  height: 24px;
  margin-left: auto;
  padding: 0 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-card);
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
}

.sf-tl-rule-toggle[aria-expanded='true'] {
  border-color: color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 45%, var(--sf-border));
  background: color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 8%, var(--sf-card));
  color: var(--tl-accent, var(--sf-tl-slate));
}

.sf-tl-rule-toggle:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-tl-rule-toggle__chevron {
  width: 14px;
  height: 14px;
}

/* 箭头瞬时翻转，不做 transition（本页视觉规范禁动效）。 */
.sf-tl-rule-toggle__chevron--open {
  transform: rotate(180deg);
}

/*
 * 折叠面板：**零 overflow / 零 max-height / 零 sticky**。
 * 唯一滚动容器是 `layouts/admin.vue` 的 `.sforum-admin-scroll-area`；5 层嵌套靠
 * `min-width: 0` + 换行消化（390 最坏路径仍有 170px ≥ 单字段 140px 基宽）。
 */
.sf-tl-rule-panel {
  --tl-accent-strength: 65%;

  min-width: 0;
  margin-top: 12px;
  padding: 12px;
  border: 1px solid color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 28%, var(--sf-border));
  border-left: 3px solid color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) var(--tl-accent-strength), var(--sf-border));
  border-radius: 10px;
  background: var(--sf-card);
}

/* 停用档只降强度、加虚线；左侧 3px 色条保持实线（虚线读起来像「不可编辑」）。 */
.sf-tl-rule-panel--retired {
  --tl-accent-strength: 45%;

  border-style: dashed;
  border-left-style: solid;
}

.dark .sf-tl-rule-panel--retired {
  --tl-accent-strength: 55%;
}
</style>
