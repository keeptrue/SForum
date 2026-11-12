<script setup lang="ts">
/**
 * 规则树的一片叶子：条件选择 + 比较符/阈值 + 目录声明的参数控件。
 *
 * 参数控件完全由 `catalog[].params` 驱动（`int` / `enum` / `window` / `objectRef` /
 * `string` / `bool`），这里不写死任何内置条件的参数表——插件条件用同一套控件。
 * `objectRef` 的候选来自后端，M1.4 落地前只渲染「待下发」占位，不让运营手打 slug。
 */
import {
  createDefaultLeaf,
  replaceNode,
  type BadgeConditionCatalogEntry,
  type BadgeConditionParam,
  type BadgeConditionParamOption,
  type BadgeFactCandidate,
  type BadgeRule,
  type BadgeRuleLeaf
} from '~/utils/admin/badgeRule'
import {
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions
} from '~/utils/admin/badgeFactCandidates'

const props = withDefaults(defineProps<{
  rule: BadgeRule
  node: BadgeRuleLeaf
  path: number[]
  depth: number
  catalog: BadgeConditionCatalogEntry[]
  /** 插件事实候选（`factCandidates`）：只用于 fact.count 的两级下拉显示文案。 */
  factCandidates?: BadgeFactCandidate[]
  /** 候选整体读不到：编辑器照常可用，只在事实叶子下给一条提示。 */
  factCandidatesFailed?: boolean
}>(), {
  factCandidates: () => [],
  factCandidatesFailed: false
})

const emit = defineEmits<{ 'update:rule': [rule: BadgeRule] }>()

const { t, te, locale } = useI18n()

const entry = computed(() => props.catalog.find(item => item.key === props.node.type) ?? null)
const params = computed(() => entry.value?.params ?? [])
const zh = computed(() => String(locale.value ?? '').toLowerCase().startsWith('zh'))

const isFactCount = computed(() => entry.value?.key === 'fact.count')
// 先声明 spec 再声明依赖它的 computed：靠惰性求值侥幸不炸的写法，加个 immediate 就是 TDZ。
const factKeySpec = computed<BadgeConditionParam | undefined>(
  () => params.value.find(spec => spec.name === 'factKey')
)
const currentFactKey = computed(() => paramText('factKey'))
/** 事实候选只属于事实叶子，别的条件一次都不该读它。 */
const factItems = computed(() => factKeyOptions(
  factKeySpec.value?.options?.map(option => option.value) ?? [],
  props.factCandidates,
  zh.value
))
// 候选整体读不到时**不判失效**：不知道它是否存在，就不能说它失效（两种降级不能混）。
const factStale = computed(() => isFactCount.value
  && !props.factCandidatesFailed
  && factKeyMissing(props.factCandidates, currentFactKey.value))
const variantItems = computed(() => factVariantOptions(props.factCandidates, currentFactKey.value, zh.value))
const factUnit = computed(() => factCandidateUnit(props.factCandidates, currentFactKey.value, zh.value))

/**
 * `variant` 的候选是运行时填的（目录里 `options: nil`）：这一项**没有候选就整项隐藏**，
 * 绝不渲染一个空下拉——那会让运营以为「不能选变体」。
 */
const visibleParams = computed(() => params.value.filter(spec =>
  !(spec.name === 'variant' && variantItems.value.length === 0)
))

function entryLabel(item: BadgeConditionCatalogEntry) {
  return zh.value ? item.labelZh : item.labelEn
}

function optionLabel(option: BadgeConditionParamOption) {
  return zh.value ? option.labelZh : option.labelEn
}

/** 参数文案走共享键；插件自定义参数没有文案时回退原始 name，不显示裸 i18n key。 */
/** `variant` 的候选来自插件（含插件双语 label）；其余 enum 仍用目录 options。 */
function enumOptions(spec: BadgeConditionParam) {
  if (spec.name === 'variant') return variantItems.value
  return (spec.options ?? []).map(option => ({ value: option.value, label: optionLabel(option) }))
}

function paramLabel(name: string) {
  const key = `admin.badges.conditions.params.${name}`
  return te(key) ? t(key) : name
}

function compareLabel(op: string) {
  return t(`admin.badges.rule.compare.${op}`)
}

function patch(next: BadgeRuleLeaf) {
  emit('update:rule', replaceNode(props.rule, props.path, next))
}

function setType(event: Event) {
  const key = (event.target as HTMLSelectElement).value
  const next = props.catalog.find(item => item.key === key)
  if (!next) return
  patch(createDefaultLeaf(next))
}

function setCompareOp(event: Event) {
  const op = (event.target as HTMLSelectElement).value as 'gte' | 'lte'
  patch({ ...props.node, cmp: { op, value: props.node.cmp?.value ?? 1 } })
}

function setValue(event: Event) {
  const value = Number((event.target as HTMLInputElement).value)
  patch({ ...props.node, cmp: { op: props.node.cmp?.op ?? 'gte', value: Number.isFinite(value) ? value : 0 } })
}

/** 选 factKey：值原样落进 params，显示文案只来自候选；顺带归一变体（无变体置空串）。 */
function setFactKey(event: Event) {
  const key = (event.target as HTMLSelectElement).value
  patch({ ...props.node, params: applyFactKeySelection(props.node.params, props.factCandidates, key, zh.value) })
}

function setParam(name: string, value: unknown) {
  patch({ ...props.node, params: { ...(props.node.params ?? {}), [name]: value } })
}

function paramText(name: string) {
  const value = props.node.params?.[name]
  return value === undefined || value === null ? '' : String(value)
}

function paramNumber(name: string) {
  const value = props.node.params?.[name]
  return typeof value === 'number' ? value : null
}

function paramBool(name: string) {
  return props.node.params?.[name] === true
}

function windowValue(name: string) {
  const value = props.node.params?.[name]
  return value && typeof value === 'object' ? (value as Record<string, unknown>) : null
}

function windowKind(name: string) {
  return String(windowValue(name)?.kind ?? 'all')
}

function windowDays(name: string) {
  const days = windowValue(name)?.days
  return typeof days === 'number' ? days : 30
}

function setText(name: string, event: Event) {
  setParam(name, (event.target as HTMLInputElement | HTMLSelectElement).value)
}

function setNumber(name: string, event: Event) {
  const value = Number((event.target as HTMLInputElement).value)
  setParam(name, Number.isFinite(value) ? value : null)
}

function setBool(name: string, event: Event) {
  setParam(name, (event.target as HTMLInputElement).checked)
}

function setWindowKind(name: string, event: Event) {
  const kind = (event.target as HTMLSelectElement).value
  setParam(name, kind === 'recent_days' ? { kind, days: windowDays(name) } : { kind })
}

function setWindowDays(name: string, event: Event) {
  const days = Number((event.target as HTMLInputElement).value)
  setParam(name, { kind: windowKind(name), days: Number.isFinite(days) ? days : 1 })
}
</script>

<template>
  <article class="sf-rule-leaf" :data-rule-leaf="node.type" :data-depth="depth">
    <!-- 目录里没有这个条件（插件卸载 / 版本回退）：显示原始 key + 只读提示，不渲染空控件。 -->
    <div v-if="!entry" class="sf-rule-leaf__missing" data-rule-leaf-missing>
      <p class="sf-rule-leaf__missing-title">{{ t('admin.badges.conditions.unknown', { type: node.type }) }}</p>
      <code class="sf-rule-leaf__missing-key">{{ node.type }}</code>
      <p class="sf-rule-leaf__missing-hint">{{ t('admin.badges.conditions.unknownHint') }}</p>
    </div>

    <template v-else>
    <div class="sf-rule-leaf__head">
      <label class="sf-rule-field">
        <span class="sf-rule-field__label">{{ t('admin.badges.rule.condition') }}</span>
        <select class="sf-rule-control" data-rule-condition :value="node.type" @change="setType">
          <option v-for="item in catalog" :key="item.key" :value="item.key">{{ entryLabel(item) }}</option>
        </select>
      </label>

      <label v-if="node.cmp && entry" class="sf-rule-field">
        <span class="sf-rule-field__label">{{ t('admin.badges.rule.compareOp') }}</span>
        <select class="sf-rule-control" data-rule-compare :value="node.cmp.op" @change="setCompareOp">
          <option v-for="op in entry.compare" :key="op" :value="op">{{ compareLabel(op) }}</option>
        </select>
      </label>

      <label v-if="node.cmp && entry" class="sf-rule-field">
        <span class="sf-rule-field__label">{{ t('admin.badges.rule.value') }}</span>
        <input
          class="sf-rule-control"
          data-rule-value
          type="number"
          min="1"
          max="1000000000"
          :value="node.cmp.value"
          @input="setValue"
        >
        <span v-if="factUnit" class="sf-rule-field__unit">{{ factUnit }}</span>
      </label>
    </div>

    <div v-if="params.length > 0" class="sf-rule-params">
      <label
        v-for="spec in visibleParams"
        :key="spec.name"
        class="sf-rule-field"
        :data-param="spec.name"
        :data-param-kind="spec.kind"
      >
        <span class="sf-rule-field__label">{{ paramLabel(spec.name) }}</span>

        <select
          v-if="spec.kind === 'enum'"
          class="sf-rule-control"
          :value="paramText(spec.name)"
          @change="setText(spec.name, $event)"
        >
          <option v-for="option in enumOptions(spec)" :key="option.value" :value="option.value">
            {{ option.label }}
          </option>
        </select>

        <template v-else-if="spec.kind === 'window'">
          <select class="sf-rule-control" :value="windowKind(spec.name)" @change="setWindowKind(spec.name, $event)">
            <option value="all">{{ t('admin.badges.rule.window.all') }}</option>
            <option value="recent_days">{{ t('admin.badges.rule.window.recent_days') }}</option>
          </select>
          <input
            v-if="windowKind(spec.name) === 'recent_days'"
            class="sf-rule-control"
            type="number"
            min="1"
            max="3650"
            :value="windowDays(spec.name)"
            @input="setWindowDays(spec.name, $event)"
          >
        </template>

        <!-- factKey：值来自目录 options，显示文案来自插件候选（Errata 2）；失效旧值保留可见。 -->
        <template v-if="spec.kind === 'objectRef' && spec.refKind === 'fact'">
          <select
            class="sf-rule-control"
            data-fact-key
            :value="paramText(spec.name)"
            :aria-label="paramLabel(spec.name)"
            @change="setFactKey"
          >
            <option value="">{{ t('admin.badges.conditions.factKeyPlaceholder') }}</option>
            <option v-for="item in factItems" :key="item.value" :value="item.value">{{ item.label }}</option>
            <option v-if="factStale" :value="currentFactKey">{{ currentFactKey }}</option>
          </select>
          <p v-if="factStale" class="sf-rule-leaf__flag" data-fact-stale>
            {{ t('admin.badges.rule.health.missing_fact_key') }} · {{ t('admin.badges.conditions.factStaleHint') }}
          </p>
          <p v-else-if="factCandidatesFailed" class="sf-rule-leaf__flag" data-fact-candidates-failed>
            {{ t('admin.badges.conditions.factCandidatesFailed') }}
          </p>
        </template>

        <select
          v-else-if="spec.kind === 'objectRef'"
          class="sf-rule-control"
          :value="paramText(spec.name)"
          @change="setText(spec.name, $event)"
        >
          <option value="">{{ t('admin.badges.conditions.candidatesPending') }}</option>
          <option v-if="paramText(spec.name)" :value="paramText(spec.name)">{{ paramText(spec.name) }}</option>
        </select>

        <input
          v-else-if="spec.kind === 'int'"
          class="sf-rule-control"
          type="number"
          :min="spec.min"
          :max="spec.max"
          :value="paramNumber(spec.name)"
          @input="setNumber(spec.name, $event)"
        >

        <input
          v-else-if="spec.kind === 'bool'"
          type="checkbox"
          :checked="paramBool(spec.name)"
          @change="setBool(spec.name, $event)"
        >

        <input
          v-else
          class="sf-rule-control"
          type="text"
          :value="paramText(spec.name)"
          @input="setText(spec.name, $event)"
        >
      </label>
    </div>
    </template>
  </article>
</template>

<style scoped>
.sf-rule-leaf {
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-sizing: border-box;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-card);
}

.sf-rule-leaf__head,
.sf-rule-params {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  min-width: 0;
}

.sf-rule-params {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
}

.sf-rule-field {
  display: flex;
  flex: 1 1 140px;
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

.sf-rule-leaf__missing {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 8px;
  border: 1px dashed var(--sf-warning, var(--sf-border));
  border-radius: 8px;
  background: var(--sf-muted);
}

.sf-rule-leaf__missing-title,
.sf-rule-leaf__missing-hint {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.4;
}

.sf-rule-field__unit {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

.sf-rule-leaf__flag {
  margin: 0;
  color: var(--sf-warning, var(--sf-fg-secondary));
  font-size: 11px;
  line-height: 1.4;
}

.sf-rule-leaf__missing-key {
  overflow-wrap: anywhere;
  color: var(--sf-fg);
  font-size: 12px;
}
</style>
