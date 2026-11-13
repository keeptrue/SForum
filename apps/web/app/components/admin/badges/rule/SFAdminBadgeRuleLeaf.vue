<script setup lang="ts">
/**
 * 规则树的一片叶子：条件选择 + 比较符/阈值 + 目录声明的参数控件。
 *
 * 参数控件本身**不在这个文件里**：`SFAdminBadgeRuleParamField` 是唯一实现，
 * 简单模式表单与高级模式树都渲染它。这里只负责「叶子这一行」的头部（条件 / 比较符 /
 * 阈值 / 单位）与把参数改动合并回本节点。
 */
import {
  createDefaultLeaf,
  replaceNode,
  type BadgeConditionCatalogEntry,
  type BadgeConditionParam,
  type BadgeFactCandidate,
  type BadgeRule,
  type BadgeRuleLeaf
} from '~/utils/admin/badgeRule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import { factCandidateUnit } from '~/utils/admin/badgeFactCandidates'
import SFAdminBadgeRuleParamField from '~/components/admin/badges/rule/SFAdminBadgeRuleParamField.vue'

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
  /** objectRef 参数候选：不可用/为空时参数控件退成文本输入。 */
  refCandidates?: BadgeRefCandidateMap
}>(), {
  factCandidates: () => [],
  factCandidatesFailed: false,
  refCandidates: () => ({})
})

const emit = defineEmits<{ 'update:rule': [rule: BadgeRule] }>()

const { t, locale } = useI18n()

const entry = computed(() => props.catalog.find(item => item.key === props.node.type) ?? null)
const params = computed<BadgeConditionParam[]>(() => entry.value?.params ?? [])
const zh = computed(() => String(locale.value ?? '').toLowerCase().startsWith('zh'))

const currentFactKey = computed(() => paramText('factKey'))
const factUnit = computed(() => factCandidateUnit(props.factCandidates, currentFactKey.value, zh.value))
/** 目录级计量单位（`conditionDTO.unitZh/unitEn`）：事实候选没给单位时的兜底。 */
const entryUnit = computed(() => {
  const pair = zh.value
    ? [entry.value?.unitZh, entry.value?.unitEn]
    : [entry.value?.unitEn, entry.value?.unitZh]
  return pair.find(unit => typeof unit === 'string' && unit.trim() !== '') ?? ''
})

/**
 * 阈值格后缀：插件事实取候选单位，其余取目录单位。
 * `days_visited` 例外——它本身就是「天数」，「访问天数 ≥ 7 次」读不通，所以不挂单位。
 */
const thresholdUnit = computed(() => (paramText('metric') === 'days_visited'
  ? ''
  : factUnit.value || entryUnit.value))

function entryLabel(item: BadgeConditionCatalogEntry) {
  return zh.value ? item.labelZh : item.labelEn
}

function compareLabel(op: string) {
  return t(`admin.badges.rule.compare.${op}`)
}

function paramText(name: string) {
  const value = props.node.params?.[name]
  return value === undefined || value === null ? '' : String(value)
}

function patch(next: BadgeRuleLeaf) {
  emit('update:rule', replaceNode(props.rule, props.path, next))
}

/** 参数组件的唯一出口：整份新 params 落到本节点，位置与其余字段保持原样。 */
function applyParams(next: Record<string, unknown>) {
  patch({ ...props.node, params: next })
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
        <!-- 单位是 input 的行内后缀（与简单模式同一套语言），不再独占第三行。 -->
        <span class="sf-rule-field__value">
          <input
            class="sf-rule-control"
            data-rule-value
            type="number"
            min="1"
            max="1000000000"
            :value="node.cmp.value"
            @input="setValue"
          >
          <span v-if="thresholdUnit" class="sf-rule-field__unit">{{ thresholdUnit }}</span>
        </span>
      </label>
    </div>

    <div v-if="params.length > 0" class="sf-rule-params">
      <SFAdminBadgeRuleParamField
        v-for="spec in params"
        :key="spec.name"
        :spec="spec"
        :node="node"
        :entry="entry"
        :fact-candidates="factCandidates ?? []"
        :fact-candidates-failed="factCandidatesFailed === true"
        :ref-candidates="refCandidates ?? {}"
        :zh="zh"
        @update:params="applyParams"
      />
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

.sf-rule-field__value {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.sf-rule-field__value .sf-rule-control {
  flex: 1 1 auto;
  min-width: 0;
}

.sf-rule-field__unit {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  white-space: nowrap;
}

.sf-rule-leaf__missing-key {
  overflow-wrap: anywhere;
  color: var(--sf-fg);
  font-size: 12px;
}
</style>
