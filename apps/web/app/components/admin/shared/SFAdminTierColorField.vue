<script setup lang="ts">
/**
 * 通用「等级色」字段：徽章等级与信任等级共用（文案全部走 props，组件不认业务名词）。
 *
 * 三条硬约束：
 * 1. **候选色来自服务端**：`choices` 是 `GET /admin/badges/tiers` 的 `colorChoices`，
 *    组件不内置色表 —— 内置一份就会与运营调整后的色板分叉。
 * 2. **单行不折行**：9 个 32×32 在 390 宽度下必须一行放得下（桌面 gap 6 / 窄屏 gap 4）。
 *    宽度不够时横向滚动，绝不 wrap —— 折行会让「第几档」的视觉顺序失效。
 * 3. **只提示不阻断**：自定义 hex 在深色卡面下对比度不足时给一行提示，仍允许保存。
 *    配色是审美决策，工具不该替运营拍板。
 */
import {
  TIER_MIN_DARK_CONTRAST,
  normalizeTierColor,
  normalizeTierColorInput,
  tierAccentContrast,
  tierColorVar,
  isTierCustomColor
} from '~/utils/theme/tierColor'

const props = withDefaults(defineProps<{
  modelValue: string
  /** 服务端候选色（token 名），顺序即服务端顺序。 */
  choices: readonly string[]
  label: string
  autoLabel: string
  pickerLabel: string
  hexLabel: string
  applyLabel: string
  invalidLabel: string
  /** 低对比度提示模板，含 `{ratio}`。 */
  lowContrastLabel: string
  hint?: string
  disabled?: boolean
}>(), {
  hint: '',
  disabled: false
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

/** `''` = 按位次自动；token / hex = 运营显式选择。 */
const selected = computed(() => normalizeTierColor(props.modelValue))
const isAuto = computed(() => selected.value === null)
const customHex = computed(() => (isTierCustomColor(selected.value) ? selected.value : ''))

const hexDraft = ref(customHex.value)
const hexInvalid = ref(false)

// 外部值变化（弹窗打开、切等级）时同步草稿；只在 hex 变化时覆盖，避免打断正在输入的字符。
watch(customHex, (value) => {
  hexDraft.value = value
  hexInvalid.value = false
})

const contrast = computed(() => (customHex.value ? tierAccentContrast(customHex.value) : null))
const lowContrast = computed(() => contrast.value !== null && contrast.value < TIER_MIN_DARK_CONTRAST)
const contrastText = computed(() => (contrast.value === null ? '' : contrast.value.toFixed(2)))

function pick(value: string) {
  if (props.disabled) return
  emit('update:modelValue', value)
}

function onHexInput(value: string | number) {
  hexDraft.value = String(value)
  hexInvalid.value = false
}

function applyHex() {
  if (props.disabled) return
  const normalized = normalizeTierColorInput(hexDraft.value)
  if (!normalized) {
    hexInvalid.value = true
    return
  }
  hexInvalid.value = false
  emit('update:modelValue', normalized)
}

function swatchStyle(token: string) {
  const value = tierColorVar(token)
  return { background: value, '--sf-tier-swatch': value }
}
</script>

<template>
  <div class="min-w-0">
    <p class="mb-1.5 text-sm font-medium text-slate-700 dark:text-zinc-200">{{ props.label }}</p>

    <!-- 单行：9 × 32 + 8 × 4 = 320，390 宽度下不折行；超窄时横向滚动。 -->
    <div class="sf-tier-color-row" role="group" :aria-label="props.label">
      <button
        v-for="token in props.choices"
        :key="token"
        type="button"
        class="sf-tier-color-swatch"
        :style="swatchStyle(token)"
        :data-selected="selected === token ? 'true' : 'false'"
        :aria-pressed="selected === token"
        :aria-label="`${props.label} ${token}`"
        :title="token"
        :disabled="props.disabled"
        @click="pick(token)"
      >
        <UIcon v-if="selected === token" name="i-lucide-check" class="sf-tier-color-swatch__check" aria-hidden="true" />
      </button>

      <button
        type="button"
        class="sf-tier-color-auto"
        :data-selected="isAuto ? 'true' : 'false'"
        :aria-pressed="isAuto"
        :disabled="props.disabled"
        @click="pick('')"
      >
        {{ props.autoLabel }}
      </button>
    </div>

    <div class="mt-2 flex flex-wrap items-center gap-2">
      <label class="sf-tier-color-picker" :title="props.pickerLabel">
        <span class="sr-only">{{ props.pickerLabel }}</span>
        <input
          type="color"
          :value="customHex || '#475569'"
          :disabled="props.disabled"
          :aria-label="props.pickerLabel"
          class="sf-tier-color-picker__input"
          @input="onHexInput(($event.target as HTMLInputElement).value)"
          @change="applyHex"
        >
      </label>

      <UInput
        :model-value="hexDraft"
        :aria-label="props.hexLabel"
        :placeholder="props.hexLabel"
        :disabled="props.disabled"
        size="sm"
        class="w-28"
        @update:model-value="onHexInput"
      />
      <UButton
        color="neutral"
        variant="outline"
        size="sm"
        :disabled="props.disabled"
        @click="applyHex"
      >
        {{ props.applyLabel }}
      </UButton>
    </div>

    <p v-if="hexInvalid" class="mt-1.5 text-xs text-red-600 dark:text-red-400">{{ props.invalidLabel }}</p>
    <p v-else-if="lowContrast" class="mt-1.5 text-xs text-amber-600 dark:text-amber-400">
      {{ props.lowContrastLabel.replace('{ratio}', contrastText) }}
    </p>
    <p v-if="props.hint" class="mt-1.5 text-xs text-slate-500 dark:text-zinc-400">{{ props.hint }}</p>
  </div>
</template>

<style scoped>
.sf-tier-color-row {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 4px;
  overflow-x: auto;
  padding: 2px;
  scrollbar-width: thin;
}

@media (min-width: 640px) {
  .sf-tier-color-row {
    gap: 6px;
  }
}

.sf-tier-color-swatch {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 0;
  border-radius: 8px;
  cursor: pointer;
}

/* 选中双通道：环线 + 对勾。只靠颜色在色弱下分不出「选了哪个」。 */
.sf-tier-color-swatch[data-selected="true"] {
  box-shadow:
    0 0 0 2px var(--sf-card, #fff),
    0 0 0 4px var(--sf-tier-swatch);
}

.sf-tier-color-swatch:focus-visible,
.sf-tier-color-auto:focus-visible,
.sf-tier-color-picker__input:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-tier-color-swatch__check {
  width: 16px;
  height: 16px;
  color: #fff;
  filter: drop-shadow(0 1px 1px rgb(0 0 0 / 45%));
}

.sf-tier-color-auto {
  flex: 0 0 auto;
  height: 32px;
  padding: 0 8px;
  border: 1px dashed var(--sf-public-border-strong, #cbd5e1);
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-secondary, #475569);
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
}

.sf-tier-color-auto[data-selected="true"] {
  border-style: solid;
  border-color: var(--sf-accent);
  color: var(--sf-fg);
}

.sf-tier-color-picker {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 1px solid var(--sf-border, #e2e8f0);
  border-radius: 8px;
  overflow: hidden;
  cursor: pointer;
}

.sf-tier-color-picker__input {
  width: 44px;
  height: 44px;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
}
</style>
