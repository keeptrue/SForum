<script setup lang="ts">
/**
 * 后台「信任等级 · 发言限额」分区（D-N11 当初明确留给独立一刀）。
 *
 * 本组件只画面板：**不持有表单状态、不渲染页头/toolbar/tablist/footer**（route shell 只有一个保存入口）。
 *
 * 三态语义（后端 `config.go` 的口径，不可简化）：
 * 1. **继承站点默认** → 该档 `limits = null`，四个输入禁用并显示「跟随站点默认」；
 * 2. **逐字段覆盖** → `limits = { … }`，留空的字段仍写 `null`（= 该字段继承）；
 * 3. **`0` 有语义** → 免冷却 / 不限量，与「没配」（`null`）是两件事，所以输入框必须能区分空串与 0。
 *
 * 停用档位同样可编辑：停用是软停用，配置保留（D-N8）。
 */
import { blockNonIntegerKey, type TrustLevelsForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { activeRankOf, type TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  limitsModeOf,
  parseLimitInput,
  toLimitsForm,
  TRUST_LEVEL_LIMIT_KEYS,
  type TrustLevelLimitKey,
  type TrustLevelLimitMode
} from '~/composables/admin/forum/trustLevels/useTrustLevelLimits'
import { trustLevelIconOf, trustLevelName } from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import type { TrustLevelSaveError } from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import SFAdminTrustLevelFieldErrors from '~/components/admin/forum/trust-levels/SFAdminTrustLevelFieldErrors.vue'

const props = defineProps<{
  form: TrustLevelsForm
  /** 写权限 `trust.manage`；没有时输入与开关都禁用。 */
  canManage: boolean
  /** 按 tier id 归位的保存失败明细（P2）：`field` 形如 `limits.dailyTopicLimit`。 */
  saveErrors?: Record<string, TrustLevelSaveError[]>
}>()

const emit = defineEmits<{
  mode: [tierId: string, mode: TrustLevelLimitMode]
  limit: [tierId: string, key: TrustLevelLimitKey, value: string | number | null]
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

function isInherit(tier: TierForm) {
  return limitsModeOf(tier.limits) === 'inherit'
}

/** 输入框的值：空串 = 该字段继承（不是 0）；`'0'` = 明确不限额。 */
function limitInput(tier: TierForm, key: TrustLevelLimitKey) {
  return toLimitsForm(tier.limits)[key]
}

function limitLabel(key: TrustLevelLimitKey) {
  return t(`admin.forum.trustLevels.limits.fields.${key}`)
}

function onModeChange(tier: TierForm, inherit: boolean) {
  emit('mode', tier.id, inherit ? 'inherit' : 'custom')
}

/**
 * 粘贴/失焦时的本地回弹。
 *
 * `blockNonIntegerKey` 只拦 keydown，粘贴 `1.5` / `abc` 会走到 `setLimitValue`：纯函数 fail-closed
 * 返回等值副本 → prop 没变 → Vue 不会重渲染，DOM 里就留着那段脏文本，运营会以为存进去了。
 * 这里在 `change`/`blur` 时用同一套解析复核：解析不出合法值就把输入框显式写回 prop 值。
 */
function onLimitCommit(tier: TierForm, key: TrustLevelLimitKey, event: Event) {
  const input = event.target as HTMLInputElement | null
  const raw = input?.value ?? ''
  const parsed = parseLimitInput(raw, key)
  if (parsed === undefined && input) {
    // 非法值（负数/小数/非数字/超输入护栏）：回弹，不写表单。
    input.value = limitInput(tier, key)
    return
  }
  // 合法（含空串 = 继承、0 = 不限额）：把归一后的原始文本交给上层。
  emit('limit', tier.id, key, raw)
}
</script>

<template>
  <div class="grid min-w-0 gap-6" data-sf-trust-levels-limits>
    <!-- 三态一句话说清：这是运营唯一一次需要读懂「空 = 继承 / 0 = 不限」的地方。 -->
    <p class="text-xs text-muted" data-sf-limits-hint>{{ t('admin.forum.trustLevels.limits.hint') }}</p>

    <fieldset
      v-for="tier in form.tiers"
      :key="tier.id"
      class="sf-trust-limits-row"
      data-sf-tl-accent
      :style="accentStyle(tier)"
      :data-sf-limits-tier="tier.id"
      :data-tier-id="tier.id"
      :data-retired="tier.retired ? 'true' : 'false'"
      :data-limits-mode="isInherit(tier) ? 'inherit' : 'custom'"
    >
      <legend class="sf-trust-limits-row__legend">
        <span
          class="sf-trust-limits-row__icon sf-tl-mark"
          :data-level="String(rankOf(tier) ?? 0)"
          :data-step="String(Math.min(4, rankOf(tier) ?? 0))"
          aria-hidden="true"
        >
          <UIcon :name="tierIcon(tier)" class="sf-trust-limits-row__glyph" />
        </span>
        <span class="sf-trust-limits-row__name">{{ tierLabel(tier) }}</span>
        <span class="sf-trust-limits-row__code">
          {{ rankOf(tier) === null ? t('admin.forum.trustLevels.tiers.retiredBadge') : t('profile.trustLevel.code', { level: rankOf(tier) }) }}
        </span>
      </legend>

      <SFAdminTrustLevelFieldErrors :errors="saveErrors?.[tier.id] ?? []" compact />

      <!-- 继承开关：开 = 该档 limits 为 null（四个输入全部禁用并显示跟随站点默认）。 -->
      <div class="sf-trust-limits-row__mode">
        <USwitch
          :model-value="isInherit(tier)"
          :disabled="!canManage"
          :aria-label="t('admin.forum.trustLevels.limits.inherit')"
          :data-sf-limits-mode="tier.id"
          @update:model-value="(value: boolean) => onModeChange(tier, value)"
        />
        <span class="sf-trust-limits-row__mode-label">{{ t('admin.forum.trustLevels.limits.inherit') }}</span>
        <span class="sf-trust-limits-row__mode-state" data-sf-limits-mode-state>
          {{ isInherit(tier) ? t('admin.forum.trustLevels.limits.inheritBadge') : t('admin.forum.trustLevels.limits.customBadge') }}
        </span>
      </div>

      <div class="sf-trust-limits-row__grid">
        <UFormField
          v-for="key in TRUST_LEVEL_LIMIT_KEYS"
          :key="key"
          :label="limitLabel(key)"
          :name="`trust.tier.${tier.id}.limits.${key}`"
        >
          <UInput
            :model-value="limitInput(tier, key)"
            inputmode="numeric"
            :disabled="!canManage || isInherit(tier)"
            :placeholder="t('admin.forum.trustLevels.limits.inheritPlaceholder')"
            class="w-full"
            :data-sf-limit-input="`${tier.id}.${key}`"
            @keydown="blockNonIntegerKey"
            @update:model-value="(value: string | number) => emit('limit', tier.id, key, value)"
            @change="(event: Event) => onLimitCommit(tier, key, event)"
            @blur="(event: Event) => onLimitCommit(tier, key, event)"
          />
        </UFormField>
      </div>

      <p v-if="isInherit(tier)" class="text-xs text-muted" data-sf-limits-inherit-note>
        {{ t('admin.forum.trustLevels.limits.inheritNote') }}
      </p>
      <p v-else class="text-xs text-muted" data-sf-limits-zero-hint>
        {{ t('admin.forum.trustLevels.limits.zeroHint') }}
      </p>
    </fieldset>

    <p class="text-xs text-muted" data-sf-trust-levels-tl0-limits-hint>
      {{ t('admin.forum.trustLevels.limits.tl0Hint') }}
    </p>
  </div>
</template>

<style scoped>
.sf-trust-limits-row {
  margin: 0;
  padding: 0;
  border: 0;
}

/* 一行一档之间用分隔线，不画卡片：与「门槛设置」分区同一套节律。 */
.sf-trust-limits-row + .sf-trust-limits-row {
  border-top: 1px solid var(--sf-border);
  padding-top: 14px;
}

.sf-trust-limits-row__legend {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
  padding: 0;
}

/* 24×24 与门槛设置同尺寸：后台行密度优先。几何与配色走共享 .sf-tl-mark，
   本分区只保留尺寸与字形大小（色相来自 fieldset 根上的内联 --tl-accent）。 */
.sf-trust-limits-row__icon {
  width: 24px;
  --sf-tl-mark-radius: 8px;

  height: 24px;
}

.sf-trust-limits-row__glyph {
  width: 14px;
  height: 14px;
}

/* 停用档位：虚线边框表达「保留但不参与判定」。 */
.sf-trust-limits-row[data-retired="true"] .sf-trust-limits-row__icon {
  border-style: dashed;
}

.sf-trust-limits-row__name {
  color: inherit;
  font-size: 14px;
  font-weight: 600;
}

.sf-trust-limits-row__code {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.sf-trust-limits-row__mode {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}

.sf-trust-limits-row__mode-label {
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.sf-trust-limits-row__mode-state {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

/* 手机/平板 2 列（390 下不横向溢出）；宽屏 4 列一行放下四个字段，不留半行空白。 */
.sf-trust-limits-row__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (min-width: 1280px) {
  .sf-trust-limits-row__grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}
</style>
