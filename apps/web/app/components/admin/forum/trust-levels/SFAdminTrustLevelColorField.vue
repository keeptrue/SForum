<script setup lang="ts">
/**
 * 档位颜色字段（小设 spec §2 / §3）：推荐色板 + 「按位次自动」chip + 自定义 `#rrggbb`。
 *
 * 三条几何裁决（照抄 spec，别"优化"）：
 * 1. **9 色必须单行**：块固定 32×32 不缩不折，间距只走 `gap`（桌面 6px、窄屏 4px）。
 *    给色块加任何 margin 都会在 390 宽度上把它顶到第二行 —— 折出来的孤块会被读成「另一种类别」。
 * 2. **「自动」不是第 10 个色块**：它是推荐色板下方一行 24px chip，仍属同一个 radio group。
 *    10 块在 390 上必然折成 9+1，而孤块正好是「自动」，语义上像「第 10 种颜色」。
 * 3. **候选色只有一个来源**：`choices` 来自服务端 `colorChoices`，为空时只说明「没有候选」，
 *    绝不前端补一份色表（抄一份就是「UI 能选、保存 422」）。自定义 hex 是用户输入值，不是候选表。
 *
 * 选中态双通道（环线 + 对勾）：只靠颜色区分对色盲用户不成立，浅底上更读不出来。
 * `input[type=color]` 与文本框只改**本地草稿**，点「应用」才写入表单（spec §3 交互口径）。
 */
import { computed, onMounted, ref, useId, watch } from 'vue'
import type { TrustLevelColorChoice } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import {
  TIER_MIN_DARK_CONTRAST,
  isTierCustomColor,
  normalizeTierColor,
  tierAccentContrast,
  tierColorVar
} from '~/utils/profile/trustLevelColors'

const props = withDefaults(defineProps<{
  /** 当前存储色：token / `#rrggbb` / `null` = 按位次自动。 */
  modelValue: string | null
  /** 服务端推荐色板（可以是空数组 = 后端未下发）。 */
  choices: TrustLevelColorChoice[]
  /** 该档当前位次的默认 token：「自动」chip 的色点用它，运营一眼看到自动长什么样。 */
  defaultColor: string
  /** radio group 的无障碍名字，由调用方传「某档 的颜色」。 */
  label: string
  disabled?: boolean
}>(), {
  disabled: false
})

const emit = defineEmits<{
  'update:modelValue': [value: string | null]
}>()

const { t, te } = useI18n()

/** radio 的 `name` 用 `useId()` 保证 SSR/CSR 一致（同图标选择器的做法）。 */
const groupName = `sf-trust-tier-color-${useId()}`

/** 自定义色的本地草稿：文本框里的原样内容（提交前不碰表单）。 */
const draft = ref('')

watch(() => props.modelValue, (value) => {
  // 存储值是归一化的小写 hex；输入框照它显示，不再另外造一种大小写形态。
  draft.value = isTierCustomColor(value) ? value.trim().toLowerCase() : ''
}, { immediate: true })

const selectedCustom = computed(() => isTierCustomColor(props.modelValue))
const draftHex = computed(() => normalizeHexDraft(draft.value))
const draftInvalid = computed(() => draft.value.trim().length > 0 && draftHex.value === null)
/**
 * 当前有效色若来自 token，「取色块」要显示它在浅色页面上的实际颜色 ——
 * 否则运营看到的是一个空白白块，读不出「现在这一档是什么色」。
 */
const effectiveToken = computed(() => {
  const value = normalizeTierColor(props.modelValue)
  if (!value) return props.defaultColor
  return isTierCustomColor(value) ? null : value
})

/**
 * token 的浅色取值：**从 CSS 读**，不新增第二份 JS 色表 ——
 * `--sf-tl-<token>-light` 在 `sforum-trust-colors.css` 是唯一真值。
 * SSR 阶段没有 CSSOM，先留 `null`（回退 #ffffff），挂载后再同步。
 */
const tokenLightHex = ref<string | null>(null)

function readTokenLightHex(token: string | null) {
  if (!token || typeof document === 'undefined') {
    tokenLightHex.value = null
    return
  }
  const raw = getComputedStyle(document.documentElement).getPropertyValue(`--sf-tl-${token}-light`).trim()
  tokenLightHex.value = /^#[0-9a-f]{6}$/i.test(raw) ? raw.toLowerCase() : null
}

onMounted(() => readTokenLightHex(effectiveToken.value))
watch(effectiveToken, token => readTokenLightHex(token))

/** 色块控件的取值：草稿优先 → 当前自定义色 → 当前 token 的浅色实际值 → 白色兜底（仅 SSR/异常）。 */
const pickerValue = computed(() => {
  const custom = draftHex.value ?? (selectedCustom.value && props.modelValue ? props.modelValue : null)
  return custom ?? tokenLightHex.value ?? '#ffffff'
})

/**
 * 深色页面上的对比度提示（只提示不阻断）：算的是「提亮后 vs `#1b201f` 卡面」，
 * 与预览暗卡看到的是同一个值。公式在 `trustLevelColors.ts`，组件里不写第二份。
 */
const lowContrast = computed(() => {
  const target = draftHex.value ?? (selectedCustom.value ? props.modelValue : null)
  const contrast = tierAccentContrast(target)
  return contrast !== null && contrast < TIER_MIN_DARK_CONTRAST
})

/** 色名的人话：优先 i18n（`colors.<token>`），后端没给 label 时也有兜底文本。 */
function colorLabel(token: string): string {
  const key = `admin.forum.trustLevels.colors.${token}`
  return te(key) ? t(key) : token
}

/**
 * 输入归一：接受 `#7c3aed` / `7c3aed` / `#7C3AED` / `#ABC`（短写法展开成六位）。
 * 新手最常输错的就是三位短写法，展开比报错友好（spec §8.1）。
 */
function normalizeHexDraft(value: string): string | null {
  const raw = value.trim().toLowerCase()
  if (!raw) return null
  const withHash = raw.startsWith('#') ? raw : `#${raw}`
  const short = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/.exec(withHash)
  if (short) return `#${short[1]}${short[1]}${short[2]}${short[2]}${short[3]}${short[3]}`
  return normalizeTierColor(withHash)
}

function select(value: string | null) {
  if (props.disabled) return
  emit('update:modelValue', value)
}

function onDraftInput(event: Event) {
  const target = event.target as HTMLInputElement | null
  draft.value = target?.value ?? ''
}

function onPickerInput(event: Event) {
  const target = event.target as HTMLInputElement | null
  draft.value = target?.value ?? ''
}

/** 应用：写入归一后的 hex（小写存储），输入框同步显示同一个归一值。 */
function applyDraft() {
  const hex = draftHex.value
  if (!hex || props.disabled) return
  emit('update:modelValue', hex)
  draft.value = hex
}
</script>

<template>
  <fieldset class="sf-tl-color" :disabled="disabled" data-sf-trust-tier-color-picker>
    <legend class="sf-tl-color__title">
      {{ t('admin.forum.trustLevels.appearanceDialog.colorTitle') }}
    </legend>

    <p class="sf-tl-color__subtitle">
      {{ t('admin.forum.trustLevels.appearanceDialog.colorRecommended') }}
    </p>

    <div
      v-if="choices.length > 0"
      class="sf-tl-color__palette"
      role="radiogroup"
      :aria-label="label"
      data-sf-color-picker-grid
    >
      <label
        v-for="choice in choices"
        :key="choice.value"
        class="sf-tl-color__swatch"
        :data-selected="modelValue === choice.value ? 'true' : 'false'"
      >
        <input
          :name="groupName"
          type="radio"
          class="sf-tl-color__radio"
          :value="choice.value"
          :checked="modelValue === choice.value"
          :aria-label="colorLabel(choice.value)"
          :data-sf-color-choice="choice.value"
          @change="select(choice.value)"
        >
        <span class="sf-tl-color__dot" :style="{ background: tierColorVar(choice.value) }" aria-hidden="true" />
        <span v-if="modelValue === choice.value" class="sf-tl-color__check" aria-hidden="true">
          <UIcon name="i-lucide-check" class="size-2.5" />
        </span>
      </label>
    </div>

    <!-- 空白名单：只说明「没有候选」，不编造色块；自定义色仍然可用（下面那一行不受影响）。 -->
    <p v-else class="sf-tl-color__empty" data-sf-color-picker-empty>
      {{ t('admin.forum.trustLevels.colors.noChoices', { color: colorLabel(defaultColor) }) }}
    </p>

    <!-- 「自动」：独立一行 chip（不是第 10 个色块），仍是同一 radio group 的成员。 -->
    <label class="sf-tl-color__auto" :data-selected="modelValue === null ? 'true' : 'false'">
      <input
        :name="groupName"
        type="radio"
        class="sf-tl-color__radio"
        :checked="modelValue === null"
        :aria-label="t('admin.forum.trustLevels.colors.autoWith', { color: colorLabel(defaultColor) })"
        data-sf-color-picker-auto
        @change="select(null)"
      >
      <span class="sf-tl-color__dot" :style="{ background: tierColorVar(defaultColor) }" aria-hidden="true" />
      <span class="sf-tl-color__auto-text">
        {{ t('admin.forum.trustLevels.colors.autoWith', { color: colorLabel(defaultColor) }) }}
      </span>
    </label>

    <p class="sf-tl-color__subtitle">
      {{ t('admin.forum.trustLevels.appearanceDialog.colorCustom') }}
    </p>
    <p class="sf-tl-color__hint">
      {{ t('admin.forum.trustLevels.appearanceDialog.colorCustomHint') }}
    </p>

    <div class="sf-tl-color__custom">
      <input
        class="sf-tl-color__picker"
        type="color"
        :value="pickerValue"
        :aria-label="t('admin.forum.trustLevels.appearanceDialog.colorCustom')"
        data-sf-color-custom-picker
        @input="onPickerInput"
      >
      <input
        class="sf-tl-color__hex"
        :class="{ 'sf-tl-color__hex--invalid': draftInvalid }"
        :value="draft"
        type="text"
        maxlength="7"
        placeholder="#RRGGBB"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        :aria-label="t('admin.forum.trustLevels.appearanceDialog.colorCustom')"
        :aria-invalid="draftInvalid ? 'true' : 'false'"
        data-sf-color-hex-input
        @input="onDraftInput"
        @keydown.enter.prevent="applyDraft"
      >
      <UButton
        size="sm"
        color="neutral"
        variant="outline"
        :disabled="disabled || !draftHex"
        data-sf-color-apply
        @click="applyDraft"
      >
        {{ t('admin.forum.trustLevels.appearanceDialog.colorApply') }}
      </UButton>
    </div>

    <!-- 提示区常驻一行高度：出现/消失不得推动整行重排。 -->
    <div class="sf-tl-color__messages">
      <p v-if="draftInvalid" class="sf-tl-color__error" data-sf-color-invalid>
        {{ t('admin.forum.trustLevels.appearanceDialog.colorInvalidHex') }}
      </p>
      <p v-else-if="lowContrast" class="sf-tl-color__notice" data-sf-color-low-contrast>
        {{ t('admin.forum.trustLevels.appearanceDialog.colorLowContrast') }}
      </p>
    </div>
  </fieldset>
</template>

<style scoped>
.sf-tl-color {
  min-width: 0;
  margin: 0;
  padding: 0;
  border: 0;
}

.sf-tl-color__title {
  padding: 0;
  color: var(--sf-fg);
  font-size: 13px;
  font-weight: 700;
}

.sf-tl-color__subtitle {
  margin: 8px 0 6px;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

.sf-tl-color__hint {
  margin: 0 0 8px;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  line-height: 1.45;
}

/*
 * 9 块单行：宽度 = 9×32 + 8×gap。窄屏 4px → 320px（390 视口内容宽约 334，余 14px）；
 * 桌面 6px → 336px。`nowrap` 是硬约束：折行会把最后一块读成「另一种类别」。
 */
.sf-tl-color__palette {
  display: flex;
  flex-wrap: nowrap;
  gap: 4px;
  min-width: 0;
}

@media (min-width: 640px) {
  .sf-tl-color__palette {
    gap: 6px;
  }
}

.sf-tl-color__swatch {
  position: relative;
  display: inline-flex;
  flex: 0 0 32px;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  cursor: pointer;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
}

/* 选中态第一条通道：环线 + 淡底（与图标选择器同一套几何）。 */
.sf-tl-color__swatch[data-selected="true"],
.sf-tl-color__auto[data-selected="true"] {
  border-color: var(--sf-accent);
  background: var(--sf-accent-soft);
}

.sf-tl-color__radio {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  cursor: pointer;
  opacity: 0;
}

.sf-tl-color__swatch:focus-within,
.sf-tl-color__auto:focus-within {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

/* 色点只用纯色：老环境（无 color-mix）也要看得见色相；内描边避免浅色糊进卡面。 */
.sf-tl-color__dot {
  display: inline-block;
  flex: 0 0 16px;
  width: 16px;
  height: 16px;
  border-radius: 999px;
  box-shadow: inset 0 0 0 1px rgb(0 0 0 / 0.12);
}

/* 选中态第二条通道：对勾。垫一层卡面色圆底，任何色相上都读得出来。 */
.sf-tl-color__check {
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

/* 「自动」chip：24px 高、单行，不参与色板几何。 */
.sf-tl-color__auto {
  position: relative;
  display: flex;
  align-items: center;
  gap: 6px;
  width: fit-content;
  max-width: 100%;
  min-height: 24px;
  margin-top: 8px;
  padding: 0 8px;
  cursor: pointer;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
}

.sf-tl-color__auto-text {
  overflow: hidden;
  color: var(--sf-fg-secondary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-tl-color__empty {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

/* 自定义行：36×32 取色块 + 96px hex + 应用按钮，390 也放得下，不堆叠。 */
.sf-tl-color__custom {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.sf-tl-color__picker {
  flex: 0 0 36px;
  width: 36px;
  height: 32px;
  padding: 0;
  cursor: pointer;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
}

/* 清掉 UA 内衬：默认的 padding 与 swatch 边框会让色块看起来缩了一圈。 */
.sf-tl-color__picker::-webkit-color-swatch-wrapper {
  padding: 0;
}

.sf-tl-color__picker::-webkit-color-swatch {
  border: 0;
  border-radius: 7px;
}

.sf-tl-color__picker::-moz-color-swatch {
  border: 0;
  border-radius: 7px;
}

.sf-tl-color__hex {
  flex: 0 0 96px;
  width: 96px;
  height: 32px;
  padding: 0 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-card);
  color: var(--sf-fg);
  font-family: var(--sf-font-mono, ui-monospace, monospace);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
}

.sf-tl-color__hex--invalid {
  border-color: var(--sf-danger);
}

.sf-tl-color__messages {
  min-height: 16px;
  margin-top: 4px;
}

.sf-tl-color__error,
.sf-tl-color__notice {
  margin: 0;
  font-size: 11px;
  line-height: 16px;
}

.sf-tl-color__error {
  color: var(--sf-danger);
}

.sf-tl-color__notice {
  color: var(--sf-fg-tertiary);
}
</style>
