<script setup lang="ts">
/**
 * 档位配色选择器（D-C1）。
 *
 * 语义与 `SFAdminTrustLevelIconPicker` **逐字同构**：每个候选一个原生 `<input type="radio">`
 * （同一 `name`），方向键 / 空格 / Home / End 全部由浏览器提供；选中态用**环线 + 对勾**双通道，
 * 不只靠颜色（色盲用户与 12% 浅底上都要能看出选中）。
 *
 * 白名单只来自后端 `colorChoices`：为空时只展示当前色 + 一句说明（`data-sf-color-picker-empty`），
 * 绝不编造候选 —— 前端硬编码一份色表就是第二份副本，漂移的表现是「能选、保存 422」。
 *
 * 首项是「按位次自动」（`modelValue === null`）：色点直接画成该档当前的默认色，
 * 所以运营一眼能看出「自动」到底长什么样，而不是一个抽象开关。
 */
import type { TrustLevelColorChoice } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { useId } from 'vue'
import { tierColorVar } from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  modelValue: string | null
  choices: TrustLevelColorChoice[]
  /** 无障碍名字（radio group 的 aria-label），由调用方传「某档 颜色」。 */
  label: string
  /** 该档当前位次的默认色 token：用来说明「自动」会取哪个色。 */
  defaultColor: string
  disabled?: boolean
}>(), {
  disabled: false
})

const emit = defineEmits<{
  'update:modelValue': [value: string | null]
}>()

const { t, te } = useI18n()

/** 同 `SFAdminTrustLevelIconPicker`：radio 的 `name` 用 `useId()` 保证 SSR/CSR 一致。 */
const groupName = `sf-trust-tier-color-${useId()}`
const currentLabel = computed(() => (props.modelValue ? colorLabel(props.modelValue) : t('admin.forum.trustLevels.colors.auto')))

/** 色名的人话：优先 i18n（`colors.<token>`），后端没给 label 时也有兜底文本。 */
function colorLabel(token: string): string {
  const key = `admin.forum.trustLevels.colors.${token}`
  return te(key) ? t(key) : token
}

function select(value: string | null) {
  if (props.disabled) return
  emit('update:modelValue', value)
}
</script>

<template>
  <div class="sf-color-picker" data-sf-trust-tier-color-picker>
    <div class="sf-color-picker__head">
      <span class="sf-color-picker__label">{{ label }}</span>
      <span class="sf-color-picker__current" data-sf-color-picker-current>
        <span class="sf-color-picker__dot" :style="{ background: tierColorVar(modelValue ?? defaultColor) }" aria-hidden="true" />
        <span>{{ currentLabel }}</span>
      </span>
    </div>

    <div
      v-if="choices.length > 0"
      class="sf-color-picker__grid"
      role="radiogroup"
      :aria-label="label"
      :aria-disabled="disabled ? 'true' : undefined"
      data-sf-color-picker-grid
    >
      <!-- 「自动」也是一项：运营能明确表达「跟着位次走」，而不是选一个色之后再也退不回来。 -->
      <label class="sf-color-picker__option" :data-selected="modelValue === null ? 'true' : 'false'">
        <input
          :name="groupName"
          type="radio"
          class="sf-color-picker__input"
          :checked="modelValue === null"
          :disabled="disabled"
          :aria-label="t('admin.forum.trustLevels.colors.autoWith', { color: colorLabel(defaultColor) })"
          data-sf-color-picker-auto
          @change="select(null)"
        >
        <span class="sf-color-picker__dot" :style="{ background: tierColorVar(defaultColor) }" aria-hidden="true" />
        <span class="sf-color-picker__badge">{{ t('admin.forum.trustLevels.colors.autoBadge') }}</span>
      </label>

      <label
        v-for="choice in choices"
        :key="choice.value"
        class="sf-color-picker__option"
        :data-selected="modelValue === choice.value ? 'true' : 'false'"
      >
        <input
          :name="groupName"
          type="radio"
          class="sf-color-picker__input"
          :value="choice.value"
          :checked="modelValue === choice.value"
          :disabled="disabled"
          :aria-label="colorLabel(choice.value)"
          :data-sf-color-choice="choice.value"
          @change="select(choice.value)"
        >
        <span class="sf-color-picker__dot" :style="{ background: tierColorVar(choice.value) }" aria-hidden="true" />
        <span v-if="modelValue === choice.value" class="sf-color-picker__check" aria-hidden="true">
          <UIcon name="i-lucide-check" class="size-2.5" />
        </span>
      </label>
    </div>

    <p v-else class="sf-color-picker__empty" data-sf-color-picker-empty>
      {{ t('admin.forum.trustLevels.colors.noChoices', { color: colorLabel(defaultColor) }) }}
    </p>
  </div>
</template>

<style scoped>
.sf-color-picker {
  min-width: 0;
}

.sf-color-picker__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}

.sf-color-picker__label {
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.sf-color-picker__current {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

.sf-color-picker__grid {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

/* 32px 是触达最小尺寸（WCAG 2.5.8）：390 窄屏下也能一指戳中。 */
.sf-color-picker__option {
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
}

/* 圆点本身只用纯色，不做渐变/混合：老环境（color-mix 不可用）也要看得见色相。 */
.sf-color-picker__dot {
  display: inline-block;
  width: 16px;
  height: 16px;
  border-radius: 999px;
  /* 浅色 dot 与卡片底之间需要一条边，否则 amber/emerald 在纸色主题上会糊进背景。 */
  box-shadow: inset 0 0 0 1px rgb(0 0 0 / 0.12);
}

/* 选中态第一条通道：环线落在选项方块上（与图标选择器同一套几何）。 */
.sf-color-picker__option[data-selected="true"] {
  border-color: var(--sf-accent);
  background: var(--sf-accent-soft);
}

.sf-color-picker__input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  cursor: pointer;
  opacity: 0;
}

.sf-color-picker__option:focus-within {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-color-picker__input:disabled {
  cursor: not-allowed;
}

/* 「自动」角标：只表达这一层语义，不参与选中判断。 */
.sf-color-picker__badge {
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

/* 选中态第二条通道：对勾。垫一层卡面色圆底，任何色相上都读得出来。 */
.sf-color-picker__check {
  position: absolute;
  top: -4px;
  right: -4px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border: 1px solid var(--sf-card);
  border-radius: 999px;
  background: var(--sf-fg);
  color: var(--sf-card);
}

.sf-color-picker__empty {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}
</style>
