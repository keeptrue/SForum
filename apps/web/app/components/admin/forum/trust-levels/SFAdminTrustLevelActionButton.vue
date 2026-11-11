<script setup lang="ts">
/**
 * 档位行内的动作按钮：**原生 `<button>`**，禁用语义直接落在 DOM 上。
 *
 * 为什么不用 `UButton`：`UButton` 的 `disabled` 是组件 prop，最终属性是否落到原生元素取决于
 * 版本与渲染路径；`[data-sf-tier-retire]` 实测出现过「实例 props.disabled = true，但原生
 * `<button disabled=false aria-disabled="false">`」——状态对、属性没落地，屏幕阅读器会被告知
 * 「这个按钮可用」。B2 的教训就是：谓词对不等于渲染对，禁用必须写在浏览器与 AT 都认的那一层。
 *
 * 视觉与 `UButton` 的 ghost / outline 两个变体一致（同一套 slate/zinc token），
 * 所以换实现不改变行内命令的几何与配色。
 */
withDefaults(defineProps<{
  /** 视觉变体：图标命令用 ghost，带文字的动作用 outline。 */
  variant?: 'ghost' | 'outline'
  /** 图标类名（`i-lucide-*`）；文字按钮也保留图标位。 */
  icon?: string
  disabled?: boolean
  /** 原生 `title`：禁用时用它说明原因（悬停可见）。 */
  title?: string
  /** 无障碍名字：图标按钮必须有。 */
  label?: string
}>(), {
  variant: 'ghost',
  icon: '',
  disabled: false,
  title: undefined,
  label: undefined
})

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()

function onClick(event: MouseEvent) {
  // 原生 disabled 已经挡住点击；这里再拦一次，防止有人把 disabled 改成纯样式。
  if ((event.currentTarget as HTMLButtonElement | null)?.disabled) return
  emit('click', event)
}
</script>

<template>
  <button
    type="button"
    :disabled="disabled"
    :aria-disabled="disabled ? 'true' : undefined"
    :title="title"
    :aria-label="label"
    :class="variant === 'outline' ? 'sf-action-btn sf-action-btn--outline' : 'sf-action-btn sf-action-btn--ghost'"
    @click="onClick"
  >
    <UIcon v-if="icon" :name="icon" class="sf-action-btn__icon" aria-hidden="true" />
    <span v-if="$slots.default" class="sf-action-btn__label"><slot /></span>
  </button>
</template>

<style scoped>
.sf-action-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 500;
  line-height: 1;
  cursor: pointer;
  background: transparent;
  transition: background-color 120ms ease;
}

.sf-action-btn--ghost {
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--sf-fg-secondary);
}

.sf-action-btn--outline {
  padding: 5px 8px;
  border: 1px solid var(--sf-border);
  color: var(--sf-fg-secondary);
}

.sf-action-btn--ghost:not(:disabled):hover,
.sf-action-btn--outline:not(:disabled):hover {
  background: var(--sf-muted);
}

/* 禁用态：cursor + 对比度双通道，不靠 tooltip 才知道点不动。 */
.sf-action-btn:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.sf-action-btn__icon {
  width: 14px;
  height: 14px;
}

.sf-action-btn__label {
  white-space: nowrap;
}
</style>
