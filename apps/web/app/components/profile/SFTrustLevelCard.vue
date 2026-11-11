<script setup lang="ts">
/**
 * 资料页右栏「信任等级」卡（M5）。
 *
 * 纯展示：取数在 `useTrustLevel`（M4 公开等级查询），本组件只渲染。
 * class / `data-*` / `aria-*` 是冻结契约（reports/2026-11-10-trust-level-visual-spec.md §1.3），
 * 改名先改规范。刻度条是 `aria-hidden` 的装饰冗余，信息由档位名 + TL 编号 + aria 承载。
 */
import {
  TRUST_LEVEL_MAX,
  TRUST_LEVEL_SLUGS,
  trustLevelIcon,
  trustLevelProgress,
  trustLevelSlug,
  type TrustLevelMetric
} from '~/composables/profile/useTrustLevel'

const props = withDefaults(defineProps<{
  level: number
  metrics?: TrustLevelMetric[]
}>(), {
  metrics: () => []
})

const { t } = useI18n()

const levelValue = computed(() => Math.min(TRUST_LEVEL_MAX, Math.max(0, Math.trunc(Number(props.level) || 0))))
const earned = computed(() => levelValue.value >= 1)
const name = computed(() => t(`profile.trustLevel.name.${trustLevelSlug(levelValue.value)}`))
const nextName = computed(() => t(`profile.trustLevel.name.${trustLevelSlug(levelValue.value + 1)}`))
const icon = computed(() => trustLevelIcon(levelValue.value))

// TL4 没有下一档 → 由 `__maxed` 取代进度块。
// level<4 但下一档所有 threshold 都是 0（实际只会是「新人期上限闸压住等级」）→ 什么都不渲染：
// 那种状态不是「已达最高等级」，也不猜原因（规范 §1.4-2 + lead 裁决）。
const bar = computed(() => (levelValue.value >= TRUST_LEVEL_MAX ? null : trustLevelProgress(props.metrics)))
const hint = computed(() => {
  const progress = bar.value
  if (!progress) return ''
  return t('profile.trustLevel.nextHint', {
    count: progress.remaining,
    metric: t(`profile.trustLevel.metric.${progress.metric.key}`),
    next: nextName.value
  })
})
const progressMax = computed(() => bar.value?.max ?? 0)
const progressNow = computed(() => bar.value?.now ?? 0)
const progressPercent = computed(() => bar.value?.percent ?? 0)
// valuetext 用真实 current（不是 clamp 后的 now）：读者需要知道「已经 78 个赞、要求 60」。
const progressValueText = computed(() => (bar.value
  ? t('profile.trustLevel.progress.valueText', {
      metric: t(`profile.trustLevel.metric.${bar.value.metric.key}`),
      current: Math.max(0, Math.trunc(bar.value.metric.current)),
      threshold: bar.value.max
    })
  : ''))
const progressLabel = computed(() => t('profile.trustLevel.progress.ariaLabel', {
  current: name.value,
  next: nextName.value
}))
const progressText = computed(() => t('profile.trustLevel.progress.text', {
  current: Math.max(0, Math.trunc(bar.value?.metric.current ?? 0)),
  threshold: progressMax.value
}))
const rootLabel = computed(() => t('profile.trustLevel.ariaLabel', {
  name: name.value,
  state: earned.value ? t('profile.trustLevel.earned') : t('profile.trustLevel.notEarned')
}))
// 五格固定渲染，顺序 0→4；`data-earned` 只回答「走过几档」，不标记当前档。
const rungs = computed(() => TRUST_LEVEL_SLUGS.map((slug, level) => ({
  slug,
  level,
  earned: level <= levelValue.value
})))
</script>

<template>
  <section
    class="sf-profile-side-section sf-trust-level-card"
    :data-level="String(levelValue)"
    :data-earned="earned ? 'true' : 'false'"
    :aria-label="rootLabel"
  >
    <header class="sf-profile-side-section__head">
      <h3>{{ t('profile.trustLevel.title') }}</h3>
      <span class="sf-trust-level-card__code">{{ t('profile.trustLevel.code', { level: levelValue }) }}</span>
    </header>

    <div class="sf-trust-level-card__main">
      <span class="sf-trust-level-card__icon" aria-hidden="true">
        <UIcon :name="icon" class="sf-trust-level-card__glyph" />
      </span>
      <div class="sf-trust-level-card__text">
        <strong class="sf-trust-level-card__name">{{ name }}</strong>
        <p v-if="bar" class="sf-trust-level-card__hint">{{ hint }}</p>
      </div>
    </div>

    <div v-if="bar" class="sf-trust-level-card__progress">
      <div
        class="sf-trust-level-card__track"
        role="progressbar"
        aria-valuemin="0"
        :aria-valuemax="String(progressMax)"
        :aria-valuenow="String(progressNow)"
        :aria-valuetext="progressValueText"
        :aria-label="progressLabel"
      >
        <span class="sf-trust-level-card__fill" :style="{ width: `${progressPercent}%` }" />
      </div>
      <span class="sf-trust-level-card__progress-text">{{ progressText }}</span>
    </div>
    <p v-else-if="levelValue >= TRUST_LEVEL_MAX" class="sf-trust-level-card__maxed">{{ t('profile.trustLevel.maxed') }}</p>

    <ol class="sf-trust-level-card__rungs" aria-hidden="true">
      <li
        v-for="rung in rungs"
        :key="rung.slug"
        class="sf-trust-level-card__rung"
        :data-level="String(rung.level)"
        :data-earned="rung.earned ? 'true' : 'false'"
      />
    </ol>
  </section>
</template>

<style scoped>
/* 卡面锁 --sf-card：12 个日间预设下 --sf-surface 与页面底只差 2~3 个明度级，卡片边界会消失。
   外边距保持 0，间距由 .sf-profile-side-section 既有规则提供。 */
.sf-profile-side-section.sf-trust-level-card {
  --tl-accent: var(--sf-accent);

  margin: 0;
  padding: 14px;
  border: 1px solid var(--sf-border);
  border-radius: 12px;
  background: var(--sf-card);
}

/* --sf-accent-dark 是按 accent 主题分别定义的，暗色必须走 token，禁止写死亮色 accent。 */
.dark .sf-trust-level-card {
  --tl-accent: var(--sf-accent-dark);
}

.sf-trust-level-card__code {
  margin-left: auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.sf-trust-level-card__main {
  display: flex;
  align-items: center;
  gap: 10px;
}

.sf-trust-level-card__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: 1px solid var(--sf-border);
  border-radius: 9px;
  background: transparent;
  color: var(--sf-fg-tertiary);
}

.sf-trust-level-card__glyph {
  width: 16px;
  height: 16px;
}

/* 五档阶梯建在「填充强度 + 环线权重」两轴上，两轴同向递增；未达成态不用 opacity。 */
.sf-trust-level-card[data-level="0"] .sf-trust-level-card__icon {
  border-style: dashed;
}

.sf-trust-level-card[data-level="1"] .sf-trust-level-card__icon {
  color: var(--sf-fg-secondary);
}

.sf-trust-level-card[data-level="2"] .sf-trust-level-card__icon {
  border-color: color-mix(in srgb, var(--tl-accent) 34%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

/* 唯一的 2px 档：accent-soft 与 accent 的明度差会被背景色吃掉，纯几何差异任何预设都成立。 */
.sf-trust-level-card[data-level="3"] .sf-trust-level-card__icon {
  border: 2px solid color-mix(in srgb, var(--tl-accent) 55%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

/* 整站唯一的实心 accent 方块。 */
.sf-trust-level-card[data-level="4"] .sf-trust-level-card__icon {
  border-color: var(--tl-accent);
  background: var(--tl-accent);
  color: var(--sf-card);
}

.sf-trust-level-card__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.sf-trust-level-card__name {
  overflow: hidden;
  color: var(--sf-fg);
  font-size: 14px;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-level-card[data-earned="false"] .sf-trust-level-card__name {
  color: var(--sf-fg-secondary);
}

/* 副文案是行动指引，不是元数据：停在 secondary，不进 tertiary。 */
.sf-trust-level-card__hint {
  overflow: hidden;
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-trust-level-card__progress {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}

.sf-trust-level-card__track {
  flex: 1 1 auto;
  height: 6px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
}

.sf-trust-level-card__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--sf-accent);
}

.dark .sf-trust-level-card__fill {
  background: var(--sf-accent-dark);
}

.sf-trust-level-card__progress-text {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

/* 规范写 margin:0；maxed 与档位名贴死会读成同一段文本，故保留与进度块同值的 10px 上间距。 */
.sf-trust-level-card__maxed {
  margin: 10px 0 0;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

.sf-trust-level-card__rungs {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 3px;
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}

/* 3px 是「刻度」不是「进度」：比进度条细，避免两条同权重横线打架。 */
.sf-trust-level-card__rung {
  height: 3px;
  border-radius: 2px;
}

.sf-trust-level-card__rung[data-earned="true"] {
  background: var(--sf-accent);
}

.dark .sf-trust-level-card__rung[data-earned="true"] {
  background: var(--sf-accent-dark);
}

/* 不用 --sf-muted：paper / parchment 下 muted 与卡面几乎同色，刻度会消失。 */
.sf-trust-level-card__rung[data-earned="false"] {
  background: color-mix(in srgb, var(--sf-fg) 16%, transparent);
}
</style>
