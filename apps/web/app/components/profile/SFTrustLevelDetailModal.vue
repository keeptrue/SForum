<script setup lang="ts">
/**
 * 信任等级详情弹窗：当前档 + 下一档门槛 + 本人进度 + 规则条件。
 *
 * 只讲「当前档」与「下一档」，不放完整阶梯（规范 §6 / §11）：读者点进来是为了知道
 * 「这一档是什么、下一档要什么」，全景阶梯属于后台，不属于资料页。
 *
 * 他人视角：后端不下发 `bottleneck` / `conditions`（`like_given_count` / `bookmark_count`
 * 是刻意不公开的计数），所以进度与条件两块整块不渲染；门槛照常显示 —— 阈值本来就随公开
 * 目录下发，人人可见，而且对他人页那是「描述档位规则」，不是「描述这个人」（规范 §7）。
 *
 * 缺哪块不渲染哪块：没有下一档、没有门槛、没有 metrics、没有 conditions 时都不留空标题、
 * 不留空段落，也不写「没有门槛」这类解释文案（规范 §8）。
 */
import {
  FALLBACK_LADDER,
  localizedTierName,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelNextLevel,
  trustLevelProgress,
  trustLevelRequirementRows,
  type TrustLevelCondition,
  type TrustLevelMetric,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  /** 弹窗开关（父级 `v-model:open`；本组件只回发，不自己决定开关）。 */
  open: boolean
  level: number
  /** 下一档 rank（`useTrustLevel` 的 `nextLevel`）；null/缺省时按阶梯邻位推算。 */
  nextLevel?: number | null
  /** **最终**档位阶梯（rank 0 = TL0），与等级卡同一份；不传时走内置回退。 */
  ladder?: TrustLevelTier[]
  metrics?: TrustLevelMetric[]
  conditions?: TrustLevelCondition[]
  /** 本人视角才出现进度与规则条件；他人页只有当前档 + 下一档门槛。 */
  self?: boolean
}>(), {
  nextLevel: null,
  ladder: () => [],
  metrics: () => [],
  conditions: () => [],
  self: false
})

const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const { t, locale } = useI18n()

const titleId = 'sf-trust-level-detail-title'

const displayLadder = computed(() => (props.ladder?.length ? props.ladder : FALLBACK_LADDER))
const maxLevel = computed(() => trustLevelLadderMax(displayLadder.value))
const levelValue = computed(() => Math.min(maxLevel.value, Math.max(0, Math.trunc(Number(props.level) || 0))))
const currentTier = computed(() => displayLadder.value[levelValue.value] ?? null)
const name = computed(() => trustLevelName(currentTier.value, locale.value, levelValue.value, t))
const icon = computed(() => trustLevelIconOf(currentTier.value, levelValue.value))
const code = computed(() => t('profile.trustLevel.code', { level: levelValue.value }))
const earned = computed(() => levelValue.value >= 1)
const maxed = computed(() => levelValue.value >= maxLevel.value)
const accentStyle = computed(() => tierAccentStyle(tierColorOf(currentTier.value, levelValue.value)))

/**
 * 下一档：后端下发的 `nextLevel` 优先，越界/缺失按阶梯推算；满级为 null。
 *
 * **必须复用 `trustLevelNextLevel`**，不能在这里再写一遍判定：两份等价实现的分歧不会立刻
 * 报错，而是「测试钉的是 helper、页面跑的是副本」，改规则时全绿但行为漂移。
 */
const nextRank = computed(() => trustLevelNextLevel(levelValue.value, maxLevel.value, props.nextLevel))
const nextTier = computed(() => (nextRank.value === null ? null : displayLadder.value[nextRank.value] ?? null))
const nextName = computed(() => (nextRank.value === null
  ? ''
  : trustLevelName(nextTier.value, locale.value, nextRank.value, t)))
/** 门槛清单：只列 `> 0` 的指标（0 = 该指标不参与判定），全 0 时整块不渲染。 */
const requirements = computed(() => trustLevelRequirementRows(nextTier.value))

// 进度六件套与等级卡同一口径：阈值是 valuemax、now 是 clamp 后的值、valuetext 用人话。
// `self` 显式参与判定：他人视角即便误传了 metrics 也不该出现进度块。
const progress = computed(() => (props.self && !maxed.value ? trustLevelProgress(props.metrics) : null))
const progressMax = computed(() => progress.value?.max ?? 0)
const progressNow = computed(() => progress.value?.now ?? 0)
const progressPercent = computed(() => progress.value?.percent ?? 0)
const progressValueText = computed(() => (progress.value
  ? t('profile.trustLevel.progress.valueText', {
      metric: t(`profile.trustLevel.metric.${progress.value.metric.key}`),
      current: Math.max(0, Math.trunc(progress.value.metric.current)),
      threshold: progress.value.max
    })
  : ''))
// aria-label 不含数字：数字只在 valuetext 里（与卡片同一约束）。
const progressLabel = computed(() => t('profile.trustLevel.progress.ariaLabel', {
  current: name.value,
  next: nextName.value
}))
const progressText = computed(() => t('profile.trustLevel.progress.text', {
  current: Math.max(0, Math.trunc(progress.value?.metric.current ?? 0)),
  threshold: progressMax.value
}))
const hint = computed(() => (progress.value
  ? t('profile.trustLevel.nextHint', {
      count: progress.value.remaining,
      metric: t(`profile.trustLevel.metric.${progress.value.metric.key}`),
      next: nextName.value
    })
  : ''))

/** 规则条件的句子是服务端下发的双语文本，取值口径与档位名完全一致。 */
function conditionText(condition: TrustLevelCondition): string {
  return localizedTierName(condition.text, locale.value)
}

function close() {
  emit('update:open', false)
}
</script>

<template>
  <UModal
    :open="props.open"
    :title="t('profile.trustLevel.detail.title')"
    :ui="{ content: 'sf-trust-level-detail-modal' }"
    @update:open="emit('update:open', $event)"
  >
    <template #content>
      <section
        class="sf-trust-level-detail"
        :aria-labelledby="titleId"
        :data-level="String(levelValue)"
        data-sf-tl-accent
        :style="accentStyle"
      >
        <header class="sf-trust-level-detail__head">
          <!-- 形状轴（data-step）属于图标块：与等级卡共用 .sf-tl-mark 的阶梯几何。 -->
          <span
            class="sf-trust-level-detail__mark sf-tl-mark"
            :data-step="String(Math.min(4, levelValue))"
            aria-hidden="true"
          >
            <UIcon :name="icon" class="sf-trust-level-detail__glyph" />
          </span>
          <div class="sf-trust-level-detail__identity">
            <h2 :id="titleId" class="sf-trust-level-detail__name">{{ name }}</h2>
            <span class="sf-trust-level-detail__code">{{ code }}</span>
            <span class="sf-trust-level-detail__state">
              {{ earned ? t('profile.trustLevel.earned') : t('profile.trustLevel.notEarned') }}
            </span>
          </div>
          <button
            type="button"
            class="sf-trust-level-detail__close"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="close"
          >
            <UIcon name="i-lucide-x" class="sf-trust-level-detail__close-glyph" aria-hidden="true" />
          </button>
        </header>

        <div class="sf-trust-level-detail__body">
          <!-- 满级：取代门槛与进度两块，不再解释「为什么没有下一档」。 -->
          <p v-if="maxed" class="sf-trust-level-detail__maxed">{{ t('profile.trustLevel.maxed') }}</p>

          <section v-else-if="requirements.length > 0" class="sf-trust-level-detail__block">
            <h3 class="sf-trust-level-detail__block-title">
              {{ t('profile.trustLevel.detail.nextTitle', { next: nextName }) }}
            </h3>
            <ul class="sf-trust-level-detail__rows">
              <li
                v-for="row in requirements"
                :key="row.key"
                class="sf-trust-level-detail__row"
                :data-metric="row.key"
              >
                <span class="sf-trust-level-detail__row-label">{{ t(`profile.trustLevel.metric.${row.key}`) }}</span>
                <span class="sf-trust-level-detail__row-value">
                  {{ t('profile.trustLevel.detail.threshold', { threshold: row.threshold }) }}
                </span>
              </li>
            </ul>
          </section>

          <section v-if="progress" class="sf-trust-level-detail__block">
            <h3 class="sf-trust-level-detail__block-title">{{ t('profile.trustLevel.detail.progressTitle') }}</h3>
            <div class="sf-trust-level-detail__progress">
              <div
                class="sf-trust-level-detail__track"
                role="progressbar"
                aria-valuemin="0"
                :aria-valuemax="String(progressMax)"
                :aria-valuenow="String(progressNow)"
                :aria-valuetext="progressValueText"
                :aria-label="progressLabel"
              >
                <span class="sf-trust-level-detail__fill" :style="{ width: `${progressPercent}%` }" />
              </div>
              <span class="sf-trust-level-detail__progress-text">{{ progressText }}</span>
            </div>
            <p class="sf-trust-level-detail__hint">{{ hint }}</p>
          </section>

          <!-- 规则条件与数值进度并列、不合成一个分数：句子是「还差哪几类事」，不是百分比。 -->
          <section v-if="props.self && props.conditions.length > 0" class="sf-trust-level-detail__block">
            <h3 class="sf-trust-level-detail__block-title">{{ t('profile.trustLevel.detail.conditionsTitle') }}</h3>
            <ul class="sf-trust-level-detail__conditions">
              <li
                v-for="(condition, index) in props.conditions"
                :key="index"
                class="sf-trust-level-detail__condition"
                :data-satisfied="condition.satisfied ? 'true' : 'false'"
              >
                <UIcon
                  :name="condition.satisfied ? 'i-lucide-check' : 'i-lucide-x'"
                  class="sf-trust-level-detail__condition-glyph"
                  aria-hidden="true"
                />
                <span class="sr-only">
                  {{ condition.satisfied ? t('profile.trustLevel.detail.met') : t('profile.trustLevel.detail.unmet') }}
                </span>
                <span>{{ conditionText(condition) }}</span>
              </li>
            </ul>
          </section>
        </div>

        <footer class="sf-trust-level-detail__footer">
          <button type="button" class="sf-trust-level-detail__done" @click="close">
            {{ t('common.close') }}
          </button>
        </footer>
      </section>
    </template>
  </UModal>
</template>

<style scoped>
.sf-trust-level-detail {
  position: relative;
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  /* UModal 的 content 是 overflow-hidden + 视口上限：这里自己封顶并让中段滚动，
     否则长条件清单会被裁掉（390×844 上尤其明显）。 */
  max-height: min(82dvh, 760px);
  padding: 20px;
}

.sf-trust-level-detail__head {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 12px;
  /* 关闭按钮压在右上角：给头部预留内缩，长档位名不会钻到按钮底下。 */
  padding-right: 32px;
}

.sf-trust-level-detail__mark {
  --sf-tl-mark-radius: 12px;

  width: 40px;
  height: 40px;
}

.sf-trust-level-detail__glyph {
  width: 20px;
  height: 20px;
}

/* TL0 是虚拟档：虚线表达「未入门」，与徽章/等级卡同一套语言。
   只作用在图标块上 —— 整块虚线边框会把弹窗读成「未完成」。 */
.sf-trust-level-detail[data-level="0"] .sf-trust-level-detail__mark {
  border-style: dashed;
}

.sf-trust-level-detail__identity {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 8px;
  min-width: 0;
}

.sf-trust-level-detail__name {
  margin: 0;
  overflow: hidden;
  color: var(--sf-fg);
  font-size: 17px;
  font-weight: 760;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-level-detail__code {
  border-radius: 5px;
  background: var(--sf-muted);
  padding: 1px 6px;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.sf-trust-level-detail__state {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

.sf-trust-level-detail__close {
  position: absolute;
  top: 12px;
  right: 12px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  color: var(--sf-fg-tertiary);
}

.sf-trust-level-detail__close:hover {
  background: var(--sf-muted);
  color: var(--sf-fg);
}

.sf-trust-level-detail__close:focus-visible {
  outline: 2px solid var(--tl-accent, var(--sf-tl-slate));
  outline-offset: 2px;
}

.sf-trust-level-detail__close-glyph {
  width: 16px;
  height: 16px;
}

/* 中段滚动：头部与底部按钮常驻，长清单只在中间滚。 */
.sf-trust-level-detail__body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  scrollbar-gutter: stable;
}

.sf-trust-level-detail__block {
  margin-top: 16px;
}

.sf-trust-level-detail__block-title {
  margin: 0 0 6px;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.02em;
}

.sf-trust-level-detail__rows {
  margin: 0;
  padding: 0;
  list-style: none;
}

.sf-trust-level-detail__row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  padding: 4px 0;
  border-top: 1px solid color-mix(in srgb, var(--sf-border) 60%, transparent);
  font-size: 12px;
  line-height: 1.5;
}

.sf-trust-level-detail__row:first-child {
  border-top: 0;
}

.sf-trust-level-detail__row-label {
  min-width: 0;
  overflow: hidden;
  color: var(--sf-fg-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-level-detail__row-value {
  flex: 0 0 auto;
  color: var(--sf-fg);
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.sf-trust-level-detail__progress {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sf-trust-level-detail__track {
  flex: 1 1 auto;
  height: 6px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
}

.sf-trust-level-detail__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--tl-accent, var(--sf-tl-slate));
}

.sf-trust-level-detail__progress-text {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.sf-trust-level-detail__hint {
  margin: 6px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  line-height: 1.5;
}

.sf-trust-level-detail__conditions {
  margin: 0;
  padding: 0;
  list-style: none;
}

.sf-trust-level-detail__condition {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  padding: 3px 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}

.sf-trust-level-detail__condition[data-satisfied="true"] {
  color: var(--sf-fg-tertiary);
}

.sf-trust-level-detail__condition-glyph {
  flex: 0 0 auto;
  width: 14px;
  height: 14px;
  margin-top: 2px;
  color: var(--tl-accent, var(--sf-tl-slate));
}

.sf-trust-level-detail__condition[data-satisfied="false"] .sf-trust-level-detail__condition-glyph {
  color: var(--sf-fg-tertiary);
}

.sf-trust-level-detail__maxed {
  margin: 16px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}

.sf-trust-level-detail__footer {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.sf-trust-level-detail__done {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 14px;
  border: 1px solid var(--sf-border);
  border-radius: 999px;
  color: var(--sf-fg);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
}

.sf-trust-level-detail__done:hover {
  border-color: var(--tl-accent, var(--sf-tl-slate));
  color: var(--tl-accent, var(--sf-tl-slate));
}

.sf-trust-level-detail__done:focus-visible {
  outline: 2px solid var(--tl-accent, var(--sf-tl-slate));
  outline-offset: 2px;
}
</style>
