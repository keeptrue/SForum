<script setup lang="ts">
/**
 * 资料页右栏「信任等级」卡（M5）。
 *
 * 纯展示：取数在 `useTrustLevel`（M4 公开等级查询），本组件只渲染。
 * class / `data-*` / `aria-*` 是冻结契约（reports/2026-11-10-trust-level-visual-spec.md §1.3），
 * 改名先改规范。刻度条是 `aria-hidden` 的装饰冗余，信息由档位名 + TL 编号 + aria 承载。
 *
 * `interactive`（可选，默认 false）只追加一层**透明覆盖按钮**，整卡内容与既有契约零改动：
 * 不把根节点换成 `<button>` —— 那样 `role="progressbar"` 会被吞进按钮的可访问名，读屏会念成
 * 「查看信任等级详情 信任等级进度：常驻用户 → 资深用户」。
 */
import {
  FALLBACK_LADDER,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelProgress,
  type TrustLevelMetric,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  level: number
  metrics?: TrustLevelMetric[]
  /**
   * **最终**档位阶梯：rank 0 = TL0，其后 active 档位（由 `useTrustLevel` 组装，见 `FALLBACK_LADDER`）。
   *
   * 卡片「收到什么就渲染什么」：这里**不再**补 TL0、也不再过滤，否则页面给一份含 TL0 的阶梯
   * 就会被拼成两条 TL0，整张卡错位一档。不传时名字/图标走内置回退，但**不渲染刻度**。
   */
  ladder?: TrustLevelTier[]
  /** true = 整卡可点（覆盖层按钮）；默认 false，纯展示契约与既有调用点不变。 */
  interactive?: boolean
}>(), {
  metrics: () => [],
  ladder: () => [],
  interactive: false
})

const emit = defineEmits<{ open: [] }>()

const { t, locale } = useI18n()

/** 收到的阶梯（含 TL0）；空 = 目录不可用，此时只渲染名字/图标/进度，不画刻度。 */
const rungsSource = computed(() => props.ladder ?? [])
const hasLadder = computed(() => rungsSource.value.length > 0)
/** 名字/图标的兜底来源：与刻度共用同一套解析，只是刻度不渲染。 */
const displayLadder = computed(() => (hasLadder.value ? rungsSource.value : FALLBACK_LADDER))
const maxLevel = computed(() => trustLevelLadderMax(displayLadder.value))
const levelValue = computed(() => Math.min(maxLevel.value, Math.max(0, Math.trunc(Number(props.level) || 0))))
const earned = computed(() => levelValue.value >= 1)
const currentTier = computed(() => displayLadder.value[levelValue.value] ?? null)
const nextTier = computed(() => displayLadder.value[levelValue.value + 1] ?? null)
const name = computed(() => trustLevelName(currentTier.value, locale.value, levelValue.value, t))
const nextName = computed(() => trustLevelName(nextTier.value, locale.value, levelValue.value + 1, t))
const icon = computed(() => trustLevelIconOf(currentTier.value, levelValue.value))
/**
 * 档位色（D-C1）：`--tl-accent` 由**当前档**派生（TL0 = slate），
 * 阶梯（图标 / 进度条 / 最高档实心块）都跟着它走。内联变量在明暗两套里都成立，
 * 因为 `--sf-tl-*` 自己在 `.dark` 下换色（见 `sforum-trust-colors.css`）。
 */
const accentStyle = computed(() => tierAccentStyle(tierColorOf(currentTier.value, levelValue.value)))

// 最高档没有下一档 → 由 `__maxed` 取代进度块（最高档按 active 档数算，不是写死的 4）。
// level < 最高档但下一档所有 threshold 都是 0（实际只会是「新人期上限闸压住等级」）→ 什么都不渲染：
// 那种状态不是「已达最高等级」，也不猜原因（规范 §1.4-2 + lead 裁决）。
const bar = computed(() => (levelValue.value >= maxLevel.value ? null : trustLevelProgress(props.metrics)))
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
// 刻度：**只**按收到的阶梯画（含 TL0），不补、不改序；`data-earned` 只回答「走过几档」。
const rungs = computed(() => rungsSource.value.map((tier, level) => ({
  id: tier.id,
  level,
  earned: level <= levelValue.value
})))
const rungColumns = computed(() => `repeat(${Math.max(1, rungs.value.length)}, minmax(0, 1fr))`)
</script>

<template>
  <section
    class="sf-profile-side-section sf-trust-level-card"
    data-sf-tl-accent
    :style="accentStyle"
    :data-level="String(levelValue)"
    :data-earned="earned ? 'true' : 'false'"
    :aria-label="rootLabel"
  >
    <header class="sf-profile-side-section__head">
      <h3>{{ t('profile.trustLevel.title') }}</h3>
      <span class="sf-trust-level-card__code">{{ t('profile.trustLevel.code', { level: levelValue }) }}</span>
    </header>

    <div class="sf-trust-level-card__main">
      <!-- 形状轴（data-step）属于图标块自己：与后台四分区共用 .sf-tl-mark 的阶梯几何。 -->
      <span class="sf-trust-level-card__icon sf-tl-mark" :data-step="String(Math.min(4, levelValue))" aria-hidden="true">
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
    <p v-else-if="levelValue >= maxLevel" class="sf-trust-level-card__maxed">{{ t('profile.trustLevel.maxed') }}</p>

    <ol v-if="rungs.length > 0" class="sf-trust-level-card__rungs" aria-hidden="true" :style="{ gridTemplateColumns: rungColumns }">
      <li
        v-for="rung in rungs"
        :key="rung.id"
        class="sf-trust-level-card__rung"
        :data-level="String(rung.level)"
        :data-earned="rung.earned ? 'true' : 'false'"
      />
    </ol>

    <!-- 整卡入口：透明覆盖按钮是**最后一个子节点**（`:has()` 够不到前面的兄弟），
         卡面内容与冻结契约一行未动。 -->
    <button
      v-if="props.interactive"
      type="button"
      class="sf-trust-level-card__hit"
      aria-haspopup="dialog"
      :aria-label="t('profile.trustLevel.detail.open')"
      @click="emit('open')"
    />
  </section>
</template>

<style scoped>
/* 卡面锁 --sf-card：12 个日间预设下 --sf-surface 与页面底只差 2~3 个明度级，卡片边界会消失。
   外边距保持 0，间距由 .sf-profile-side-section 既有规则提供。 */
.sf-profile-side-section.sf-trust-level-card {
  /* 不在这里声明 --tl-accent：真正色相来自 :style="accentStyle"（D-C6）。
     自定义 hex 档靠全局 [data-sf-tl-accent] 把 --tl-accent-light/-dark 落成 --tl-accent，
     而这条选择器的特异度（0,2,0）高于那条属性规则 —— 声明在这里会把 hex 档静默打回 slate。 */
  position: relative;
  margin: 0;
  padding: 14px;
  border: 1px solid var(--sf-border);
  border-radius: 12px;
  background: var(--sf-card);
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
  --sf-tl-mark-radius: 9px;

  width: 28px;
  height: 28px;
}

.sf-trust-level-card__glyph {
  width: 16px;
  height: 16px;
}

/* TL0 是虚拟档：虚线表达「未入门」；阶梯填充/环线全部由 .sf-tl-mark 按 data-step 给。 */
.sf-trust-level-card[data-level="0"] .sf-trust-level-card__icon {
  border-style: dashed;
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

/* 进度条跟档位色走（不再写死站点 accent）：同一条卡面上「等级」与「进度」必须是同一个色相。 */
.sf-trust-level-card__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--tl-accent);
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
  grid-template-columns: repeat(var(--sf-rungs, 5), minmax(0, 1fr));
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
  background: var(--tl-accent);
}

/* 不用 --sf-muted：paper / parchment 下 muted 与卡面几乎同色，刻度会消失。 */
.sf-trust-level-card__rung[data-earned="false"] {
  background: color-mix(in srgb, var(--sf-fg) 16%, transparent);
}

/* 覆盖层按钮：透明、铺满卡面，`border-radius: inherit` 让焦点环贴着圆角走。 */
.sf-trust-level-card__hit {
  position: absolute;
  inset: 0;
  border: 0;
  border-radius: inherit;
  background: transparent;
  cursor: pointer;
}

/* 100% 档位色，不是 34% 混色：后者在 parchment 上只有 1.63:1，等于没有反馈
   （100% 起 4.38:1，见规范 §3 的实测表）。 */
.sf-trust-level-card__hit:focus-visible {
  outline: 2px solid var(--tl-accent, var(--sf-tl-slate));
  outline-offset: 2px;
}

/* 整卡 hover 反馈只能从 `:has()` 拿：按钮是最后一个子节点，兄弟选择器够不到卡面。
   `:has()` 的特异度按参数算（0,3,0），压得住卡面那条 0,2,0 的 border 声明。 */
.sf-trust-level-card:has(.sf-trust-level-card__hit:hover) {
  border-color: var(--tl-accent, var(--sf-tl-slate));
}
</style>
