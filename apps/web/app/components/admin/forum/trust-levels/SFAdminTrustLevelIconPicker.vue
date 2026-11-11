<script setup lang="ts">
/**
 * 档位图标选择器（D-N4）。
 *
 * 语义是 **radio group**：每个候选图标一个原生 `<input type="radio">`（同一 `name`），
 * 于是方向键/空格/Home/End 全部由浏览器原生提供，不需要自己实现 roving tabindex。
 * 视觉上是一排 32px 方块；选中态用环线 + 对勾双通道，不只靠颜色。
 *
 * 白名单**只**来自后端 `iconChoices`（组件零字面量：写死一份就是第二份副本）。
 * 白名单为空 = 后端没下发，此时只展示当前值 + 一句说明，不编造候选。
 * `modelValue === null` 表示「未选」，档位渲染时按 rank 取默认序列（见 `trustLevelIconOf`）。
 */
import { useId } from 'vue'
import { tierIconClass } from '~/composables/profile/useTrustLevel'
import type { TrustLevelIconChoice } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'

const props = withDefaults(defineProps<{
  modelValue: string | null
  choices: TrustLevelIconChoice[]
  /** 无障碍名字（radio group 的 aria-label），由调用方传「某档 图标」。 */
  label: string
  /** 默认图标的短名：选择器用它说明「未选时会用哪个」。 */
  defaultIcon: string
  disabled?: boolean
}>(), {
  disabled: false
})

const emit = defineEmits<{
  'update:modelValue': [value: string | null]
}>()

const { t } = useI18n()

/**
 * radio 的 `name` 必须 SSR/CSR 一致：用 `Math.random()` 时服务端与客户端会算出两个值，
 * 属性对不上会让水合报告 mismatch，也会让「同一组」的语义在两帧之间断掉。`useId()` 是确定性的。
 */
const groupName = `sf-trust-tier-icon-${useId()}`
const current = computed(() => (props.modelValue ? tierIconClass(props.modelValue) : ''))
const defaultValue = computed(() => tierIconClass(props.defaultIcon))

function select(value: string | null) {
  if (props.disabled) return
  emit('update:modelValue', value)
}
</script>

<template>
  <div class="sf-icon-picker" data-sf-trust-tier-icon-picker>
    <div class="sf-icon-picker__head">
      <span class="sf-icon-picker__label">{{ label }}</span>
      <span class="sf-icon-picker__current" data-sf-icon-picker-current>
        <UIcon v-if="current" :name="current" class="size-4" aria-hidden="true" />
        <span>{{ modelValue || t('admin.forum.trustLevels.iconPicker.defaultBadge') }}</span>
      </span>
    </div>

    <div
      v-if="choices.length > 0"
      class="sf-icon-picker__grid"
      role="radiogroup"
      :aria-label="label"
      :aria-disabled="disabled ? 'true' : undefined"
      data-sf-icon-picker-grid
    >
      <!-- 「未选」也是一项：运营能明确表达「按顺序用默认图标」，而不是只能选一个再无法退回。 -->
      <label class="sf-icon-picker__option" :data-selected="modelValue === null ? 'true' : 'false'">
        <input
          :name="groupName"
          type="radio"
          class="sf-icon-picker__input"
          :checked="modelValue === null"
          :disabled="disabled"
          :aria-label="t('admin.forum.trustLevels.iconPicker.useDefault', { icon: defaultIcon })"
          data-sf-icon-picker-default
          @change="select(null)"
        >
        <UIcon :name="defaultValue" class="sf-icon-picker__glyph" aria-hidden="true" />
        <span class="sf-icon-picker__badge">{{ t('admin.forum.trustLevels.iconPicker.defaultBadge') }}</span>
      </label>

      <label
        v-for="choice in choices"
        :key="choice.value"
        class="sf-icon-picker__option"
        :data-selected="modelValue === choice.value ? 'true' : 'false'"
      >
        <input
          :name="groupName"
          type="radio"
          class="sf-icon-picker__input"
          :value="choice.value"
          :checked="modelValue === choice.value"
          :disabled="disabled"
          :aria-label="choice.label"
          :data-sf-icon-choice="choice.value"
          @change="select(choice.value)"
        >
        <UIcon :name="tierIconClass(choice.value)" class="sf-icon-picker__glyph" aria-hidden="true" />
        <span v-if="modelValue === choice.value" class="sf-icon-picker__check" aria-hidden="true">
          <UIcon name="i-lucide-check" class="size-2.5" />
        </span>
      </label>
    </div>

    <p v-else class="sf-icon-picker__empty" data-sf-icon-picker-empty>
      {{ t('admin.forum.trustLevels.iconPicker.noChoices', { icon: defaultIcon }) }}
    </p>
  </div>
</template>

<style scoped>
.sf-icon-picker {
  min-width: 0;
}

.sf-icon-picker__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.sf-icon-picker__label {
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.sf-icon-picker__current {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.sf-icon-picker__grid {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

/* 32px 是触达最小尺寸（WCAG 2.5.8）：390 窄屏下 6 个一行仍然可点。 */
.sf-icon-picker__option {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  cursor: pointer;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-tertiary);
}

.sf-icon-picker__option[data-selected="true"] {
  border-color: var(--sf-accent);
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
}

/* 原生 radio 只做语义载体：保留在可聚焦层，视觉交给方块本身。 */
.sf-icon-picker__input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  cursor: pointer;
  opacity: 0;
}

.sf-icon-picker__option:focus-within {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-icon-picker__input:disabled {
  cursor: not-allowed;
}

.sf-icon-picker__glyph {
  width: 16px;
  height: 16px;
}

/* 超小号角标只标记「默认」这一层语义；选中态由环线承担。 */
.sf-icon-picker__badge {
  position: absolute;
  right: -2px;
  bottom: -6px;
  padding: 0 2px;
  border-radius: 4px;
  background: var(--sf-card);
  color: var(--sf-fg-tertiary);
  font-size: 8px;
  font-weight: 700;
  line-height: 12px;
}

.sf-icon-picker__check {
  position: absolute;
  top: -4px;
  right: -4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 12px;
  height: 12px;
  border-radius: 999px;
  background: var(--sf-accent);
  color: var(--sf-card);
}

.sf-icon-picker__empty {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}
</style>
