<script setup lang="ts">
/**
 * 档位外观的**双底预览**（小设 spec §4）：同一个色在明底卡与暗底卡上并排画一次。
 *
 * 三条硬约束：
 * 1. **卡底色是固定值**（`#ffffff` / `#1b201f`），不跟 `var(--sf-card)`：后台本身切了深色时，
 *    跟着主题走的预览卡会一起变暗，「明/暗预览」当场失去意义。这是全篇唯一允许硬编码颜色的地方。
 * 2. **明暗由 `data-sf-tl-scope` 表达**，组件里没有「当前是否暗色」的判断：token 色的深色取值
 *    由 `sforum-trust-colors.css` 的 `[data-sf-tl-scope="dark"]` 映射，元素自身声明胜过从
 *    `html.dark` 继承来的值 —— 暗色后台里的「浅色页面」卡也能拿到真正的浅色。
 * 3. `--tl-accent` 挂在**卡片**（祖先）上，不写在 `.sf-tl-mark` 自己身上：更深一层的声明会遮蔽
 *    祖先的内联值，图标块就会永远显示站点色（`sforum-trust-colors.css` 头部记着这个坑）。
 */
import { tierAccentPreview } from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  /** 该档当前存储色（token / `#rrggbb` / `null` = 按位次自动）。 */
  value: string | null
  /** 位次：`null` 色的预览按它取默认 token。 */
  rank: number
  /** 档位名（预览卡里跟图标并排显示）。 */
  name: string
  /** 已归一成 `i-<collection>-<name>` 的图标类名。 */
  icon: string
  /** 色值的人话：token 走 i18n 色名，自定义色显示大写 hex。 */
  colorLabel: string
}>(), {
  value: null
})

const { t } = useI18n()

const preview = computed(() => tierAccentPreview(props.value, props.rank))

const sides = computed(() => [
  { key: 'light', label: t('admin.forum.trustLevels.appearanceDialog.previewLight'), side: preview.value.light },
  { key: 'dark', label: t('admin.forum.trustLevels.appearanceDialog.previewDark'), side: preview.value.dark }
])
</script>

<template>
  <div class="sf-tl-preview" data-sf-tl-preview>
    <div
      v-for="entry in sides"
      :key="entry.key"
      class="sf-tl-preview__card"
      :class="`sf-tl-preview__card--${entry.key}`"
      :data-sf-tl-preview-card="entry.key"
      :data-sf-tl-scope="entry.side.scope"
      :style="entry.side.style"
    >
      <span class="sf-tl-preview__label">{{ entry.label }}</span>
      <div class="sf-tl-preview__body">
        <span class="sf-tl-mark sf-tl-preview__mark" data-step="2" aria-hidden="true">
          <UIcon :name="icon" class="size-3.5" />
        </span>
        <span class="sf-tl-preview__text">
          <span class="sf-tl-preview__name">{{ name }}</span>
          <span class="sf-tl-preview__value">{{ colorLabel }}</span>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sf-tl-preview {
  display: grid;
  /* 两列永不堆叠：堆起来就失去「同一色在两套页面上的对比」意义。 */
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  min-width: 0;
}

@media (min-width: 640px) {
  .sf-tl-preview {
    gap: 12px;
  }
}

.sf-tl-preview__card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  height: 84px;
  padding: 10px;
  border-radius: 10px;
  /* 卡片内部只用固定色；几何变量按侧覆盖，让 .sf-tl-mark 的描边/淡底落在本卡底色上。 */
  --sf-tl-mark-radius: 9px;
}

@media (min-width: 640px) {
  .sf-tl-preview__card {
    height: 88px;
    padding: 12px;
  }
}

.sf-tl-preview__card--light {
  background: #ffffff;
  border: 1px solid #e5e7eb;
  --sf-card: #ffffff;
  --sf-border: #e5e7eb;
}

.sf-tl-preview__card--dark {
  background: #1b201f;
  border: 1px solid #2a3230;
  --sf-card: #1b201f;
  --sf-border: #2a3230;
}

.sf-tl-preview__label {
  font-size: 11px;
  line-height: 1;
}

.sf-tl-preview__card--light .sf-tl-preview__label {
  color: #6b7280;
}

.sf-tl-preview__card--dark .sf-tl-preview__label {
  color: #9ca3af;
}

.sf-tl-preview__body {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.sf-tl-preview__mark {
  width: 28px;
  height: 28px;
}

.sf-tl-preview__text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.2;
}

.sf-tl-preview__name,
.sf-tl-preview__value {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-tl-preview__name {
  font-size: 14px;
  font-weight: 600;
}

.sf-tl-preview__value {
  font-size: 11px;
  font-family: var(--sf-font-mono, ui-monospace, monospace);
}

.sf-tl-preview__card--light .sf-tl-preview__name {
  color: #111827;
}

.sf-tl-preview__card--light .sf-tl-preview__value {
  color: #4b5563;
}

.sf-tl-preview__card--dark .sf-tl-preview__name {
  color: #f3f4f6;
}

.sf-tl-preview__card--dark .sf-tl-preview__value {
  color: #9ca3af;
}
</style>
