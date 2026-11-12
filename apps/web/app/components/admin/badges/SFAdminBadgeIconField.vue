<script setup lang="ts">
/**
 * 徽章图标字段：15 个快捷图标 + 「更多图标」内联展开 `SFIconPicker`。
 *
 * 为什么不直接放 picker：后台编辑弹窗里最常见的动作是「从这十几个里挑一个」，
 * 让运营每次都先翻 60 个一页的图标墙是把高频路径做成了低频路径。
 * picker 自己带分页与搜索，这里**不再套第二层固定高滚动区**（套了会出现双滚动条）。
 *
 * 选中态的颜色跟随**当前等级**（`accent` prop）：图标与等级是同一枚徽章的两半身份，
 * 用站点强调色去标选中项会让运营把它读成「系统推荐」。实现上是把 `--sf-accent*`
 * 三个变量就地覆盖在这个子树里，于是内联 picker 的选中项 / 预览卡也跟着变，
 * 不需要给 SFIconPicker 加任何 prop。
 */
import { BADGE_ICON_CHOICES } from '~/utils/admin/adminBadges'
import { isTierCustomColor } from '~/utils/theme/tierColor'

const props = withDefaults(defineProps<{
  modelValue: string
  label: string
  moreLabel: string
  lessLabel: string
  hint?: string
  error?: string
  disabled?: boolean
  /** 当前等级的识别色（CSS 值：`var(--sf-tl-*)` 或 `#rrggbb`）；空串回退站点强调色。 */
  accent?: string
}>(), {
  hint: '',
  error: '',
  disabled: false,
  accent: ''
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const expanded = ref(false)

/**
 * 子树级的强调色覆盖。
 *
 * `--sf-accent` 是全局变量，这里只在组件根节点内重定义：快捷图标的选中态用
 * `--sf-icon-accent`（显式名），而 `SFIconPicker` 内部沿用 `--sf-accent*`，
 * 所以三个变量一起给，内联 picker 的选中项与「当前选择」预览卡就自动同色。
 */
const accentVars = computed<Record<string, string>>(() => {
  // 兜底必须是**具体色**而不是 `var(--sf-accent)`：下面的类规则会写
  // `--sf-accent: var(--sf-icon-accent)`，若这里再回指 `--sf-accent` 就构成
  // 自定义属性循环（两边一起失效，picker 的边框会整片消失）。slate 与
  // `tierColorVar('')` 的兜底一致。
  const accent = props.accent || 'var(--sf-tl-slate)'
  // token（`var(--sf-tl-*)`）自己带明暗两套取值；自定义 hex 没有第二组，深色按
  // 与信任等级同一条公式提亮（`color-mix(in srgb, <hex> 74%, white)`），
  // 否则深色页面上的选中图标会糊进背景。
  //
  // 这里**只**下发两个基础变量，派生变量（`--sf-accent*`）交给下面的 CSS：
  // 内联样式优先级高于类规则，若把 `--sf-accent` 也写在行内，深色覆盖就永远不生效。
  const dark = isTierCustomColor(accent) ? `color-mix(in srgb, ${accent} 74%, white)` : accent
  return {
    '--sf-icon-accent': accent,
    '--sf-icon-accent-dark': dark
  }
})

function selectIcon(value: string) {
  if (props.disabled) return
  emit('update:modelValue', value)
}

function toggleExpanded() {
  expanded.value = !expanded.value
}
</script>

<template>
  <div class="sf-badge-icon-field min-w-0" :style="accentVars">
    <p class="mb-1.5 text-sm font-medium text-slate-700 dark:text-zinc-200">{{ props.label }}</p>

    <div class="flex flex-wrap gap-2">
      <button
        v-for="choice in BADGE_ICON_CHOICES"
        :key="choice"
        type="button"
        class="sf-badge-icon-choice"
        :data-selected="props.modelValue === choice ? 'true' : 'false'"
        :aria-label="choice"
        :aria-pressed="props.modelValue === choice"
        :title="choice"
        :disabled="props.disabled"
        @click="selectIcon(choice)"
      >
        <UIcon :name="choice" class="size-4" aria-hidden="true" />
      </button>
    </div>

    <UButton
      type="button"
      color="neutral"
      variant="ghost"
      size="xs"
      class="mt-2"
      :icon="expanded ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
      :aria-expanded="expanded"
      :disabled="props.disabled"
      @click="toggleExpanded"
    >
      {{ expanded ? props.lessLabel : props.moreLabel }}
    </UButton>

    <!-- picker 自带分页；这里只负责「展开 / 收起」，不限制高度。 -->
    <div v-if="expanded" class="mt-2 rounded-lg border border-slate-200 p-3 dark:border-zinc-800">
      <SFIconPicker
        :model-value="props.modelValue"
        :label="props.label"
        :disabled="props.disabled"
        @update:model-value="selectIcon"
      />
    </div>

    <p v-if="props.error" class="mt-1.5 text-xs text-red-600 dark:text-red-400">{{ props.error }}</p>
    <p v-else-if="props.hint" class="mt-1.5 text-xs text-slate-500 dark:text-zinc-400">{{ props.hint }}</p>
  </div>
</template>

<style scoped>
/*
 * 快捷图标：32×32 与颜色选择器的色块同尺寸（WCAG 2.5.8 触达下限）。
 * 选中态用「等级色染图标 + 12% 同色底 + 34% 同色环线」，与公开面 chip 同一套语言，
 * 不再用站点强调色的实心块——实心块会被读成「系统推荐」而不是「你选了它」。
 *
 * 子树内的 `--sf-accent*` 由等级色派生：`SFIconPicker` 的选中项 / 预览卡沿用这三个
 * 变量，于是不用给它加 prop 也能同色。
 */
.sf-badge-icon-field {
  --sf-accent: var(--sf-icon-accent);
  --sf-accent-soft: color-mix(in srgb, var(--sf-icon-accent) 12%, transparent);
  --sf-accent-focus: color-mix(in srgb, var(--sf-icon-accent) 25%, transparent);
}
.sf-badge-icon-choice {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  border: 1px solid var(--sf-border, rgb(148 163 184 / 0.4));
  background: transparent;
  color: var(--sf-fg-secondary, #475569);
  transition: border-color 0.12s ease, background-color 0.12s ease, color 0.12s ease;
}

.sf-badge-icon-choice:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--sf-icon-accent) 45%, transparent);
  color: var(--sf-fg, #0f172a);
}

.sf-badge-icon-choice[data-selected="true"] {
  border-color: color-mix(in srgb, var(--sf-icon-accent) 34%, transparent);
  background: color-mix(in srgb, var(--sf-icon-accent) 12%, transparent);
  color: var(--sf-icon-accent);
}

/* 深色：自定义 hex 用提亮后的值；token 的明暗由 `--sf-tl-*` 自己处理。 */
:global(.dark) .sf-badge-icon-field {
  --sf-accent: var(--sf-icon-accent-dark, var(--sf-icon-accent));
  --sf-accent-soft: color-mix(in srgb, var(--sf-icon-accent-dark, var(--sf-icon-accent)) 12%, transparent);
  --sf-accent-focus: color-mix(in srgb, var(--sf-icon-accent-dark, var(--sf-icon-accent)) 25%, transparent);
}

:global(.dark) .sf-badge-icon-choice[data-selected="true"] {
  border-color: color-mix(in srgb, var(--sf-icon-accent-dark, var(--sf-icon-accent)) 34%, transparent);
  background: color-mix(in srgb, var(--sf-icon-accent-dark, var(--sf-icon-accent)) 12%, transparent);
  color: var(--sf-icon-accent-dark, var(--sf-icon-accent));
}

.sf-badge-icon-choice:focus-visible {
  outline: 2px solid var(--sf-icon-accent);
  outline-offset: 2px;
}

.sf-badge-icon-choice:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
