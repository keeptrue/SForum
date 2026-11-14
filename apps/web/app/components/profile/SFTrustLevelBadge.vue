<script setup lang="ts">
/**
 * 资料页头部身份行的信任等级小牌（`图标 + TLn`）。
 *
 * 几何与配色见 `knowledge/reports/2026-11-14-profile-trust-level-badge-modal-spec.md` §2–§4：
 * 20px 高（与 `.sf-profile-uid` 同高，身份行行高不变）、12% 档位色底 + 34% 环、图标 100%
 * 档位色、文字恒用前景色 —— 可访问性由 `TLn` 文字承载（最差 11.80:1），颜色只做加速识别。
 * 环线的 34% 只表达「这块是一个整体」，**不承诺 3:1**，也不要为了「边界可见」加粗加深。
 *
 * 色相只走 `tierAccentStyle` + `data-sf-tl-accent`：写 9 个 `--sf-tl-*` token 类会绕开
 * 那条全局规则，自定义 hex 档在暗色下会显示未提亮的原色（与等级卡同一坑）。
 *
 * `interactive` 时是原生 `<button>`（Enter/Space 可开、Tab 可达）；否则 `role="img"` ——
 * 只读徽章不该出现在 tab 序列里。
 */
import {
  FALLBACK_LADDER,
  trustLevelIconOf,
  trustLevelLadderMax,
  trustLevelName,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  level: number
  /** **最终**档位阶梯（rank 0 = TL0），与等级卡同一份；不传时走内置回退。 */
  ladder?: TrustLevelTier[]
  /** true = 渲染成按钮并 `emit('open')`；false = 纯展示（`role="img"`）。 */
  interactive?: boolean
}>(), {
  ladder: () => [],
  interactive: false
})

const emit = defineEmits<{ open: [] }>()

const { t, locale } = useI18n()

/** 收到的阶梯优先；空 = 目录不可用，名字/图标/颜色按内置回退（规范 §8）。 */
const displayLadder = computed(() => (props.ladder?.length ? props.ladder : FALLBACK_LADDER))
const levelValue = computed(() => Math.min(
  trustLevelLadderMax(displayLadder.value),
  Math.max(0, Math.trunc(Number(props.level) || 0))
))
const currentTier = computed(() => displayLadder.value[levelValue.value] ?? null)
const name = computed(() => trustLevelName(currentTier.value, locale.value, levelValue.value, t))
const icon = computed(() => trustLevelIconOf(currentTier.value, levelValue.value))
const code = computed(() => t('profile.trustLevel.code', { level: levelValue.value }))
const state = computed(() => (levelValue.value >= 1
  ? t('profile.trustLevel.earned')
  : t('profile.trustLevel.notEarned')))
// aria-label 必须自带档位名与 TLn：任何语义都不许只靠 :title（规范 §9）。
const label = computed(() => t('profile.trustLevel.badge.ariaLabel', {
  name: name.value,
  code: code.value,
  state: state.value
}))
// 悬停补充信息只放「全名 + 状态」：TLn 已在牌面上，重复一遍没有新信息。
const title = computed(() => t('profile.trustLevel.ariaLabel', { name: name.value, state: state.value }))
const accentStyle = computed(() => tierAccentStyle(tierColorOf(currentTier.value, levelValue.value)))

function open() {
  if (!props.interactive) return
  emit('open')
}
</script>

<template>
  <button
    v-if="props.interactive"
    type="button"
    class="sf-trust-level-badge"
    aria-haspopup="dialog"
    :data-level="String(levelValue)"
    data-sf-tl-accent
    :style="accentStyle"
    :aria-label="label"
    :title="title"
    @click="open"
  >
    <UIcon :name="icon" class="sf-trust-level-badge__icon" aria-hidden="true" />
    <span class="sf-trust-level-badge__code">{{ code }}</span>
  </button>
  <span
    v-else
    class="sf-trust-level-badge"
    role="img"
    :data-level="String(levelValue)"
    data-sf-tl-accent
    :style="accentStyle"
    :aria-label="label"
    :title="title"
  >
    <UIcon :name="icon" class="sf-trust-level-badge__icon" aria-hidden="true" />
    <span class="sf-trust-level-badge__code">{{ code }}</span>
  </span>
</template>

<style scoped>
/* 与 `.sf-profile-uid` 同高（20px）是硬约束：身份行显式钉了 line-height: 1.4，
   徽章高一点就会把整行撑开，两个 pill 也会错位。 */
.sf-trust-level-badge {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  box-sizing: border-box;
  height: 20px;
  padding: 0 6px;
  border: 1px solid color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 34%, transparent);
  border-radius: 5px;
  background: color-mix(in srgb, var(--tl-accent, var(--sf-tl-slate)) 12%, transparent);
  color: var(--sf-public-text);
  font-size: 11px;
  font-weight: 700;
  line-height: 1;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

/* 原生按钮的字体/外观会破坏 20px 行盒，也带不来任何好处。 */
button.sf-trust-level-badge {
  font-family: inherit;
  cursor: pointer;
  appearance: none;
}

button.sf-trust-level-badge:hover {
  border-color: var(--tl-accent, var(--sf-tl-slate));
}

button.sf-trust-level-badge:focus-visible {
  outline: 2px solid var(--tl-accent, var(--sf-tl-slate));
  outline-offset: 2px;
}

.sf-trust-level-badge__icon {
  flex: 0 0 auto;
  width: 12px;
  height: 12px;
  color: var(--tl-accent, var(--sf-tl-slate));
}

.sf-trust-level-badge__code {
  line-height: 1;
}

/* TL0 是虚拟档：虚线环表达「未入门」，与等级卡 `[data-level="0"]` 同一套语言。
   不降透明度 —— 那会把文字与图标一起压到阈值以下，而 TL0 恰恰最需要被读懂。 */
.sf-trust-level-badge[data-level="0"] {
  border-style: dashed;
}

/* 没有 color-mix：基础规则那条 `border: 1px solid color-mix(...)` 是 shorthand，取值非法时
   **整条声明失效**（`border-style` 回 none），只覆盖 `border-color` 根本画不出环。
   所以兜底必须重写完整 shorthand。底色退回透明、色相只留在图标上，不写三段渐变。 */
@supports not (color: color-mix(in srgb, red, blue)) {
  .sf-trust-level-badge {
    border: 1px solid var(--sf-border);
    background: transparent;
  }
}
</style>
