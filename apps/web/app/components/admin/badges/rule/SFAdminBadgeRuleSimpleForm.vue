<script setup lang="ts">
/**
 * 简单模式表单：一行一个条件，行间用「并且」连接，产出仍是 `badgeRule.ts` 的同一份 AST。
 *
 * 刻意不持有第二套模型：行 = `flattenAllLeaves(rule)`，任何编辑都用
 * `buildFlatAllRule` 算出一棵新树再 emit。组件只管渲染与事件，句子生成在
 * `badgeRuleSentence.ts`（纯函数、可单测）。表达不了任何 `any` / `not` / 嵌套组——
 * 那些形态进不了本组件（Editor 负责判 `isSimpleRule` 并降级）。
 */
import {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  isGroupNode,
  validateRuleDraft,
  type BadgeConditionCatalogEntry,
  type BadgeFactCandidate,
  type BadgeRule,
  type BadgeRuleLeaf
} from '~/utils/admin/badgeRule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import { buildFlatAllRule, flattenAllLeaves, leafSentence } from '~/utils/admin/badgeRuleSentence'
import SFAdminBadgeRuleParamField from '~/components/admin/badges/rule/SFAdminBadgeRuleParamField.vue'

const props = withDefaults(defineProps<{
  rule: BadgeRule
  catalog: BadgeConditionCatalogEntry[]
  /** 插件事实候选：只有 fact.count 行消费。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /** objectRef 参数候选：只有 objectRef 参数行消费。 */
  refCandidates?: BadgeRefCandidateMap
}>(), {
  factCandidates: () => [],
  factCandidatesFailed: false,
  refCandidates: () => ({})
})

const emit = defineEmits<{ 'update:rule': [rule: BadgeRule] }>()

const { t, te, locale } = useI18n()

const zh = computed(() => String(locale.value ?? '').toLowerCase().startsWith('zh'))
const leaves = computed(() => flattenAllLeaves(props.rule))
const canAdd = computed(() => leaves.value.length < BADGE_RULE_LIMITS.maxLeaves)
/** 模板里不直接用导入的常量：SFC 测试按名注入绑定，少一个名字就少一处断层。 */
const maxValue = BADGE_RULE_LIMITS.maxValue

/** 行内错误直接来自 `validateRuleDraft` 的 `error.path`，不另立一套校验口径。 */
const errors = computed(() => validateRuleDraft(props.rule, props.catalog).errors)

const sentenceI18n = {
  t: (key: string, params?: Record<string, unknown>) => t(key, params as Record<string, unknown>),
  te: (key: string) => te(key)
}

function entryOf(type: string) {
  return props.catalog.find(item => item.key === type) ?? null
}

function rowSentence(leaf: BadgeRuleLeaf) {
  return leafSentence(leaf, props.catalog, String(locale.value ?? 'zh-CN'), sentenceI18n)
}

/** 叶子根（单条件规则）没有组层级：`validateRuleDraft` 给它的 path 是 `[]`。 */
const rootIsLeaf = computed(() => !isGroupNode(props.rule.root))

/** 错误码带 `rule.` 前缀，i18n 键不带（与弹窗的 `ruleErrorText` 同一口径）。 */
function rowErrors(index: number) {
  return errors.value
    // 空 path 只在本规则就是叶子根时才指向第 0 行；组根的空 path 是规则级错误
    // （too_many_leaves / too_many_nodes），不能误挂到第一行上。
    .filter(error => (error.path.length === 0 ? rootIsLeaf.value && index === 0 : error.path[0] === index))
    .map((error) => {
      const key = `admin.badges.rule.errors.${error.code.replace('rule.', '')}`
      return te(key) ? t(key, error.params as Record<string, unknown>) : t('admin.badges.rule.errors.generic', { code: error.code })
    })
}

function patch(next: readonly BadgeRuleLeaf[]) {
  emit('update:rule', buildFlatAllRule(next))
}

function replaceLeaf(index: number, leaf: BadgeRuleLeaf) {
  patch(leaves.value.map((current, position) => (position === index ? leaf : current)))
}

function setCondition(index: number, event: Event) {
  const entry = props.catalog.find(item => item.key === (event.target as HTMLSelectElement).value)
  if (!entry) return
  // 换条件 = 重生成默认参数：保留上一个条件的参数会让必填项对不上（与高级模式同口径）。
  replaceLeaf(index, createDefaultLeaf(entry))
}

function setCompareOp(index: number, leaf: BadgeRuleLeaf, event: Event) {
  const op = (event.target as HTMLSelectElement).value as 'gte' | 'lte'
  replaceLeaf(index, { ...leaf, cmp: { op, value: leaf.cmp?.value ?? 1 } })
}

function setValue(index: number, leaf: BadgeRuleLeaf, event: Event) {
  const value = Number((event.target as HTMLInputElement).value)
  replaceLeaf(index, {
    ...leaf,
    cmp: { op: leaf.cmp?.op ?? 'gte', value: Number.isFinite(value) ? value : 0 }
  })
}

function paramText(leaf: BadgeRuleLeaf, name: string) {
  const value = leaf.params?.[name]
  return value === undefined || value === null ? '' : String(value)
}

function compareOps(leaf: BadgeRuleLeaf) {
  return entryOf(leaf.type)?.compare ?? []
}

/** 该行条件的参数声明（原样给 `SFAdminBadgeRuleParamField`，隐藏判定由它自己做）。 */
function rowParams(leaf: BadgeRuleLeaf) {
  return entryOf(leaf.type)?.params ?? []
}

/** 参数组件的唯一出口：整份新 params 落到本行。 */
function applyParams(index: number, leaf: BadgeRuleLeaf, params: Record<string, unknown>) {
  replaceLeaf(index, { ...leaf, params })
}

function valueUnit(leaf: BadgeRuleLeaf) {
  const entry = entryOf(leaf.type)
  if (!entry || paramText(leaf, 'metric') === 'days_visited') return ''
  const primary = zh.value ? entry.unitZh : entry.unitEn
  const secondary = zh.value ? entry.unitEn : entry.unitZh
  return (primary ?? '') || (secondary ?? '')
}

function addCondition() {
  if (!canAdd.value) return
  const entry = props.catalog.find(item => item.key === 'activity.count') ?? props.catalog[0]
  if (!entry) return
  patch([...leaves.value, createDefaultLeaf(entry)])
}

function removeCondition(index: number) {
  patch(leaves.value.filter((_leaf, position) => position !== index))
}
</script>

<template>
  <div class="sf-simple-rule" data-simple-rule>
    <p v-if="leaves.length === 0" class="sf-simple-rule__empty" data-simple-empty>
      {{ t('admin.badges.rule.simple.empty') }}
    </p>

    <div v-for="(leaf, index) in leaves" :key="index" class="sf-simple-rule__row" :data-simple-row="index">
      <p v-if="index > 0" class="sf-simple-rule__joiner">{{ t('admin.badges.rule.simple.allOf') }}</p>
      <!-- 卡片头：一句人话 + 删除。删除不跟字段抢同一行，窄屏也不会变成孤立图标。 -->
      <div class="sf-simple-rule__head">
        <p class="sf-simple-rule__sentence" data-simple-sentence>{{ rowSentence(leaf) }}</p>
        <button
          type="button"
          class="sf-simple-rule__remove"
          data-simple-remove
          :aria-label="t('admin.badges.rule.simple.delete')"
          :title="t('admin.badges.rule.simple.delete')"
          @click="removeCondition(index)"
        >
          <UIcon name="i-lucide-trash-2" aria-hidden="true" />
        </button>
      </div>

      <div class="sf-simple-rule__controls">
        <label class="sf-simple-field sf-simple-field--condition">
          <span class="sf-simple-field__label">{{ t('admin.badges.rule.condition') }}</span>
          <select class="sf-simple-control" data-simple-condition :value="leaf.type" @change="setCondition(index, $event)">
            <option v-for="item in catalog" :key="item.key" :value="item.key">{{ zh ? item.labelZh : item.labelEn }}</option>
          </select>
        </label>

        <label v-if="leaf.cmp && compareOps(leaf).length > 0" class="sf-simple-field sf-simple-field--compare">
          <span class="sf-simple-field__label">{{ t('admin.badges.rule.compareOp') }}</span>
          <select class="sf-simple-control" data-simple-compare :value="leaf.cmp.op" @change="setCompareOp(index, leaf, $event)">
            <option v-for="op in compareOps(leaf)" :key="op" :value="op">{{ t(`admin.badges.rule.compare.${op}`) }}</option>
          </select>
        </label>

        <label v-if="leaf.cmp" class="sf-simple-field sf-simple-field--value">
          <span class="sf-simple-field__label">{{ t('admin.badges.rule.value') }}</span>
          <!-- 单位是 input 的行内后缀，不是第三行：字段高度才与其它字段一致。 -->
          <span class="sf-simple-field__value">
            <input
              class="sf-simple-control"
              data-simple-value
              type="number"
              min="1"
              :max="maxValue"
              :value="leaf.cmp.value"
              @input="setValue(index, leaf, $event)"
            >
            <span v-if="valueUnit(leaf)" class="sf-simple-field__unit">{{ valueUnit(leaf) }}</span>
          </span>
        </label>

        <!-- 参数控件只有一份实现（ParamField）：简单模式与高级模式渲染同一个组件。 -->
        <SFAdminBadgeRuleParamField
          v-for="spec in rowParams(leaf)"
          :key="spec.name"
          :spec="spec"
          :node="leaf"
          :entry="entryOf(leaf.type)"
          :fact-candidates="factCandidates ?? []"
          :fact-candidates-failed="factCandidatesFailed === true"
          :ref-candidates="refCandidates ?? {}"
          :zh="zh"
          :simple-mode="true"
          @update:params="applyParams(index, leaf, $event)"
        />
      </div>

      <p v-if="factCandidatesFailed && leaf.type === 'fact.count'" class="sf-simple-rule__flag">
        {{ t('admin.badges.conditions.factCandidatesFailed') }}
      </p>
      <ul v-if="rowErrors(index).length > 0" class="sf-simple-rule__errors" data-simple-row-error>
        <li v-for="message in rowErrors(index)" :key="message">{{ message }}</li>
      </ul>
    </div>

    <div class="sf-simple-rule__footer">
      <button
        type="button"
        class="sf-simple-rule__add"
        data-simple-add
        :disabled="!canAdd"
        :title="canAdd ? t('admin.badges.rule.simple.addCondition') : t('admin.badges.rule.limitLeaves')"
        @click="addCondition"
      >
        <UIcon name="i-lucide-plus" aria-hidden="true" />
        <span>{{ t('admin.badges.rule.simple.addCondition') }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.sf-simple-rule {
  display: flex;
  flex-direction: column;
  gap: 10px;
  box-sizing: border-box;
  min-width: 0;
}

.sf-simple-rule__row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  box-sizing: border-box;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-card);
}

.sf-simple-rule__empty {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.45;
}

.sf-simple-rule__flag,
.sf-simple-rule__errors {
  margin: 0;
  color: var(--sf-warning, var(--sf-fg-secondary));
  font-size: 12px;
  line-height: 1.45;
}

.sf-simple-rule__errors { padding-left: 16px; }
.sf-simple-rule__joiner { margin: 0; color: var(--sf-fg-tertiary); font-size: 11px; }
.sf-simple-rule__footer { display: flex; justify-content: flex-start; }

/* 卡片头：句子占满剩余宽度，删除固定在右上，窄屏不会被挤成独立一行。 */
.sf-simple-rule__head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}

.sf-simple-rule__sentence {
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  color: var(--sf-fg);
  font-size: 13px;
  font-weight: 500;
}

/* 控件行只放控件：删除按钮已在头部，这里不再有抢行的元素。 */
.sf-simple-rule__controls {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 8px;
  min-width: 0;
}

.sf-simple-field {
  display: flex;
  flex: 1 1 132px;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sf-simple-field--condition { flex: 2 1 180px; }
.sf-simple-field--compare { flex: 1 1 132px; }
.sf-simple-field--value { flex: 0 1 104px; }

/* 阈值 + 单位同一行：字段因此只有两行（label / 控件），与其它字段等高、label 齐。 */
.sf-simple-field__value {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.sf-simple-field__value .sf-simple-control {
  flex: 1 1 auto;
  min-width: 0;
}

.sf-simple-field__label {
  overflow: hidden;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-simple-field__unit {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  white-space: nowrap;
}

.sf-simple-control {
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

.sf-simple-control:focus-visible,
.sf-simple-rule__remove:focus-visible,
.sf-simple-rule__add:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-simple-rule__remove,
.sf-simple-rule__add {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-card);
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sf-simple-rule__remove { flex: 0 0 auto; }
.sf-simple-rule__add:disabled { opacity: 0.5; }
</style>
