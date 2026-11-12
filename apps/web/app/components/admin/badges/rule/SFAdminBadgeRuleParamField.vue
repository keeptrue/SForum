<script setup lang="ts">
/**
 * 单个条件参数的控件（**唯一实现**）：简单表单与高级模式都渲染这一个组件。
 *
 * 上一版这段链在两个文件里各写了一遍，结果一处补了兜底分支、另一处没补，运营就看到了
 * 重复文本框。现在控件由 `spec.kind` 决定，只有这一条 `v-if / v-else-if` 链，顺序固定：
 * `enum` → `window` → `objectRef`（内层分 fact 候选 / 待下发占位）→ `int` → `bool` →
 * `string` → 只读的「不支持的参数类型」。
 *
 * 组件**不持有草稿副本**：每次改动把整份新 `params` 交给调用方（`update:params`），
 * 由调用方决定怎么落进它自己的位置（叶子原地替换 / 简单表单重建扁平树）。
 */
import type {
  BadgeConditionCatalogEntry,
  BadgeConditionParam,
  BadgeFactCandidate,
  BadgeRuleLeaf
} from '~/utils/admin/badgeRule'
import { resolveParamOptionLabel } from '~/utils/admin/badgeRuleParams'
import {
  applyFactKeySelection,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions,
  type FactOption
} from '~/utils/admin/badgeFactCandidates'

const props = withDefaults(defineProps<{
  spec: BadgeConditionParam
  node: BadgeRuleLeaf
  /** 条件目录条目：仅用于判断事实叶子（`fact.count`）的失效提示，不参与渲染分支。 */
  entry?: BadgeConditionCatalogEntry | null
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /** 语言判定由调用方给：组件不读 locale，避免同一数据在两条渲染路径上取到不同语言。 */
  zh?: boolean
  /**
   * 窗口文案的口语化开关：简单模式说「累计（不限时间）／最近 N 天」，高级模式沿用
   * 引擎词表「累计／最近 N 天」。控件结构与取值完全一致，只有两句话不同。
   */
  simpleMode?: boolean
}>(), {
  entry: null,
  factCandidates: () => [],
  factCandidatesFailed: false,
  zh: true,
  simpleMode: false
})

const emit = defineEmits<{ 'update:params': [params: Record<string, unknown>] }>()

const { t, te } = useI18n()

const currentFactKey = computed(() => paramText('factKey'))
const isFactCount = computed(() => props.entry?.key === 'fact.count')

const factItems = computed(() => factKeyOptions(props.spec.options ?? [], props.factCandidates, props.zh))
// 候选整体读不到时**不判失效**：不知道它是否存在，就不能说它失效（两种降级不能混）。
const factStale = computed(() => isFactCount.value
  && !props.factCandidatesFailed
  && factKeyMissing(props.factCandidates, currentFactKey.value))
const variantItems = computed(() => factVariantOptions(props.factCandidates, currentFactKey.value, props.zh))

/**
 * `variant` 的候选是运行时填的（目录里 `options: nil`）：没有候选就整项不渲染，
 * 绝不渲染一个空下拉——那会让运营以为「不能选变体」。
 */
const visible = computed(() => !(props.spec.name === 'variant' && variantItems.value.length === 0))

const enumOptions = computed<FactOption[]>(() => {
  if (props.spec.name === 'variant') return variantItems.value
  return (props.spec.options ?? []).map(value => ({
    value,
    label: resolveParamOptionLabel(props.spec.name, value, key => t(key), key => te(key))
  }))
})

const windowAllLabel = computed(() => (props.simpleMode
  ? t('admin.badges.rule.simple.windowAll')
  : t('admin.badges.rule.window.all')))
const windowRecentLabel = computed(() => (props.simpleMode
  ? t('admin.badges.rule.simple.windowRecent')
  : t('admin.badges.rule.window.recent_days')))

const windowValue = computed(() => {
  const value = props.node.params?.[props.spec.name]
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : null
})

const windowKind = computed(() => String(windowValue.value?.kind ?? 'all'))
const windowDays = computed(() => {
  const days = windowValue.value?.days
  return typeof days === 'number' ? days : 30
})

/** 参数文案走共享键；插件自定义参数没有文案时回退原始 name，不显示裸 i18n key。 */
const label = computed(() => {
  const key = `admin.badges.conditions.params.${props.spec.name}`
  return te(key) ? t(key) : props.spec.name
})

const textValue = computed(() => paramText(props.spec.name))
const numberValue = computed(() => {
  const value = props.node.params?.[props.spec.name]
  return typeof value === 'number' ? value : null
})
const boolValue = computed(() => props.node.params?.[props.spec.name] === true)

function paramText(name: string) {
  const value = props.node.params?.[name]
  return value === undefined || value === null ? '' : String(value)
}

function setParam(value: unknown) {
  emit('update:params', { ...(props.node.params ?? {}), [props.spec.name]: value })
}

function setText(event: Event) {
  setParam((event.target as HTMLInputElement | HTMLSelectElement).value)
}

function setNumber(event: Event) {
  const value = Number((event.target as HTMLInputElement).value)
  setParam(Number.isFinite(value) ? value : null)
}

function setBool(event: Event) {
  setParam((event.target as HTMLInputElement).checked)
}

function setSelection(event: Event) {
  setParam((event.target as HTMLSelectElement).value)
}

function setWindowKind(event: Event) {
  const kind = (event.target as HTMLSelectElement).value
  setParam(kind === 'recent_days' ? { kind, days: windowDays.value } : { kind })
}

function setWindowDays(event: Event) {
  const days = Number((event.target as HTMLInputElement).value)
  setParam({ kind: windowKind.value, days: Number.isFinite(days) ? days : 1 })
}

/** 选 factKey：值原样落进 params，显示文案只来自候选；顺带归一变体（无变体置空串）。 */
function setFactKey(event: Event) {
  const key = (event.target as HTMLSelectElement).value
  emit('update:params', applyFactKeySelection(props.node.params, props.factCandidates, key, props.zh))
}
</script>

<template>
  <label v-if="visible" class="sf-rule-field" :data-param="spec.name" :data-param-kind="spec.kind">
    <span class="sf-rule-field__label">{{ label }}</span>

    <!-- 单一链：每个 kind 恰好一个分支，末尾绝不出现对所有 kind 都命中的兜底文本框。 -->
    <select
      v-if="spec.kind === 'enum'"
      class="sf-rule-control"
      :value="textValue"
      @change="setText"
    >
      <option v-for="option in enumOptions" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>

    <template v-else-if="spec.kind === 'window'">
      <div class="sf-rule-window">
        <select
          class="sf-rule-control"
          :aria-label="label"
          :value="windowKind"
          @change="setWindowKind"
        >
          <option value="all">{{ windowAllLabel }}</option>
          <option value="recent_days">{{ windowRecentLabel }}</option>
        </select>
        <input
          v-if="windowKind === 'recent_days'"
          class="sf-rule-control sf-rule-window__days"
          type="number"
          min="1"
          max="3650"
          :aria-label="label"
          :value="windowDays"
          @input="setWindowDays"
        >
      </div>
    </template>

    <template v-else-if="spec.kind === 'objectRef'">
      <!-- factKey：值来自目录 options，显示文案来自插件候选（Errata 2）；失效旧值保留可见。 -->
      <template v-if="spec.refKind === 'fact'">
        <select
          class="sf-rule-control"
          data-fact-key
          :value="textValue"
          :aria-label="label"
          @change="setFactKey"
        >
          <option value="">{{ t('admin.badges.conditions.factKeyPlaceholder') }}</option>
          <option v-for="item in factItems" :key="item.value" :value="item.value">{{ item.label }}</option>
          <option v-if="factStale" :value="currentFactKey">{{ currentFactKey }}</option>
        </select>
        <p v-if="factStale" class="sf-rule-field__flag" data-fact-stale>
          {{ t('admin.badges.rule.health.missing_fact_key') }} · {{ t('admin.badges.conditions.factStaleHint') }}
        </p>
        <p v-else-if="factCandidatesFailed" class="sf-rule-field__flag" data-fact-candidates-failed>
          {{ t('admin.badges.conditions.factCandidatesFailed') }}
        </p>
      </template>

      <select
        v-else
        class="sf-rule-control"
        :value="textValue"
        @change="setSelection"
      >
        <option value="">{{ t('admin.badges.conditions.candidatesPending') }}</option>
        <option v-if="textValue" :value="textValue">{{ textValue }}</option>
      </select>
    </template>

    <input
      v-else-if="spec.kind === 'int'"
      class="sf-rule-control"
      type="number"
      :min="spec.min"
      :max="spec.max"
      :value="numberValue"
      @input="setNumber"
    >

    <input
      v-else-if="spec.kind === 'bool'"
      type="checkbox"
      :checked="boolValue"
      @change="setBool"
    >

    <input
      v-else-if="spec.kind === 'string'"
      class="sf-rule-control"
      type="text"
      :value="textValue"
      @input="setText"
    >

    <p v-else class="sf-rule-field__flag" data-param-unsupported>
      {{ t('admin.badges.rule.paramUnsupported', { kind: spec.kind }) }}
    </p>
  </label>
</template>

<style scoped>
.sf-rule-field {
  display: flex;
  flex: 1 1 132px;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sf-rule-field__label {
  overflow: hidden;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-rule-control {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 4px 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-bg);
  color: var(--sf-fg);
  font-size: 13px;
}

.sf-rule-control:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-rule-window {
  display: flex;
  gap: 6px;
  min-width: 0;
}

.sf-rule-window__days {
  flex: 0 0 80px;
}

.sf-rule-field__flag {
  margin: 0;
  color: var(--sf-warning, var(--sf-fg-secondary));
  font-size: 11px;
  line-height: 1.4;
}
</style>
