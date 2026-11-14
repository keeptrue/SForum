<script setup lang="ts">
/**
 * 单个条件参数的控件（**唯一实现**）：简单表单与高级模式都渲染这一个组件。
 *
 * 上一版这段链在两个文件里各写了一遍，结果一处补了兜底分支、另一处没补，运营就看到了
 * 重复文本框。现在控件由 `spec.kind` 决定，只有这一条 `v-if / v-else-if` 链，顺序固定：
 * `enum` → `window` → `objectRef`（内层再分 fact 候选 / 可搜索候选选择 / 文本兜底）→
 * `int` → `bool` → `string` → 只读的「不支持的参数类型」。
 *
 * 组件**不持有草稿副本**：每次改动把整份新 `params` 交给调用方（`update:params`），
 * 由调用方决定怎么落进它自己的位置（叶子原地替换 / 简单表单重建扁平树）。
 */
import type {
  BadgeConditionCatalogEntry,
  BadgeConditionParam,
  BadgeFactCandidate,
  BadgeRuleLeaf
} from '~/utils/admin/rule'
import { BADGE_LABEL_FAMILY, resolveParamOptionLabel, type BadgeRuleLabelFamily } from '~/utils/admin/badgeRuleParams'
import {
  applyFactKeySelection,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions,
  type FactOption
} from '~/utils/admin/badgeFactCandidates'
import {
  isBadgeRefKind,
  refKindGroup,
  refKindItems,
  refKindMode,
  refOptionLabel,
  type BadgeRefCandidateMap,
  type BadgeRefKind
} from '~/utils/admin/badgeRefCandidates'

const props = withDefaults(defineProps<{
  spec: BadgeConditionParam
  node: BadgeRuleLeaf
  /** 条件目录条目：仅用于判断事实叶子（`fact.count`）的失效提示，不参与渲染分支。 */
  entry?: BadgeConditionCatalogEntry | null
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /**
   * `objectRef` 候选（分类 / 标签 / 角色 / 提供方 / 徽章）：唯一来源是后端顶层
   * `refCandidates`。缺项由读侧当作「不可用」，控件退成文本输入。
   */
  refCandidates?: BadgeRefCandidateMap
  /** 语言判定由调用方给：组件不读 locale，避免同一数据在两条渲染路径上取到不同语言。 */
  zh?: boolean
  /**
   * 窗口文案的口语化开关：简单模式说「累计（不限时间）／最近 N 天」，高级模式沿用
   * 引擎词表「累计／最近 N 天」。控件结构与取值完全一致，只有两句话不同。
   */
  simpleMode?: boolean
  /**
   * i18n 键前缀：共享编辑器不假定宿主域。默认徽章规则族（`admin.badges` 下的 `rule`）；
   * 信任等级传 `admin.forum.trustLevels.rule`（两份文案一致是有意的重复，禁止跨族取词）。
   */
  i18nKeyPrefix?: string
  /**
   * 条件族的 i18n 键前缀（提示句 / 占位符 / 参数名文案的父级）：默认徽章条件族。
   *
   * 它和 `i18nKeyPrefix` 是**两个族**：规则族只放按钮、句子模板与错误码；条件族放
   * 「候选读不到」「事实已失效」这类跟条件目录绑在一起的提示。信任等级传自己那份，避免跨族取词。
   */
  conditionI18nPrefix?: string
  /**
   * 参数取值文案的三个前缀族（参数专属选项 / 共享指标名 / 共享枚举值名）。
   *
   * `paramOptionLabelKey` 只认前缀、不认域：徽章编辑器用默认的徽章三元组，信任等级
   * 传自己那一份。缺省时行为与旧版逐字一致（徽章族绝对键）。
   */
  labelFamily?: BadgeRuleLabelFamily
}>(), {
  i18nKeyPrefix: 'admin.badges.rule',
  conditionI18nPrefix: 'admin.badges.conditions',
  labelFamily: () => BADGE_LABEL_FAMILY,
  entry: null,
  factCandidates: () => [],
  factCandidatesFailed: false,
  refCandidates: () => ({}),
  zh: true,
  simpleMode: false
})

const emit = defineEmits<{ 'update:params': [params: Record<string, unknown>] }>()

const { t: rawT, te: rawTE } = useI18n()

// 前缀化包装：调用点继续写短键，宿主域由 prop 决定。
const t = (key: string, params?: Record<string, unknown>) => rawT(`${props.i18nKeyPrefix}.${key}`, params as never)
const te = (key: string) => rawTE(`${props.i18nKeyPrefix}.${key}`)
// 条件族的短键包装：提示句/占位符/参数名都归宿主域的条件族（不是规则族）。
const cc = (key: string, params?: Record<string, unknown>) =>
  rawT(`${props.conditionI18nPrefix}.${key}`, params as never)

const currentFactKey = computed(() => paramText('factKey'))
const isFactCount = computed(() => props.entry?.key === 'fact.count')

const factItems = computed(() => factKeyOptions(props.spec.options ?? [], props.factCandidates, props.zh))
// 候选整体读不到时**不判失效**：不知道它是否存在，就不能说它失效（两种降级不能混）。
const factStale = computed(() => isFactCount.value
  && !props.factCandidatesFailed
  && factKeyMissing(props.factCandidates, currentFactKey.value))
const variantItems = computed(() => factVariantOptions(props.factCandidates, currentFactKey.value, props.zh))

/**
 * `objectRef`（非 `fact`）的候选来源。`fact` 走上面那套事实候选，这里恒为 `undefined`。
 */
const refKind = computed<BadgeRefKind | undefined>(() => {
  const kind = props.spec.refKind
  return kind && kind !== 'fact' ? kind : undefined
})
const refGroup = computed(() => refKindGroup(props.refCandidates, refKind.value))
/**
 * 控件形态三态（`select` / `unavailable` / `empty`）：渲染只有两叉（后两者都出文本框），
 * 但**提示文案必须分开**——「取不到」是故障，「本来就没有」是正常状态，说成同一句话
 * 会让运营去查一个没坏的后端。曾经这里渲染一个只含占位 option 的下拉，值永远为空，
 * 保存必报「缺少必填参数」。
 */
const refMode = computed(() => refKindMode(props.refCandidates, refKind.value))
const refManual = computed(() => refMode.value !== 'select')
const refManualHint = computed(() => (refMode.value === 'empty'
  ? cc('refEmptyHint')
  : cc('refUnavailableHint')))
/**
 * 目录声明了一个白名单外的 `refKind`（插件拼错 / 前后端版本不一致）。
 *
 * 这种情况**不能静默退成文本框**：候选必然取不到，而文本框什么都不会说，运营会以为
 * 自己手填的值是对的。旧版的表现就是「只有一句候选读不到」，没有任何目录诊断。
 * 值仍然可见可改（旧规则可能已经存了这个参数），但提示先讲清根因。
 */
const refUnknownKind = computed(() => {
  const kind = props.spec.refKind
  if (!kind || kind === 'fact') return ''
  return isBadgeRefKind(kind) ? '' : String(kind)
})
const refTruncated = computed(() => !refManual.value && refGroup.value.truncated)
/** 下拉项 = 后端候选（+ 必要时的失效项）；显示文案在这里拼标记后缀。 */
const refItems = computed<Array<{ value: string, label: string }>>(() =>
  refKindItems(props.refCandidates, refKind.value, textValue.value)
  .map(option => ({
    value: option.value,
    label: refOptionLabel(option, {
      stale: cc('refStale'),
      hidden: cc('refFlagHidden'),
      disabled: cc('refFlagDisabled')
    })
  })))

/**
 * `variant` 的候选是运行时填的（目录里 `options: nil`）：没有候选就整项不渲染，
 * 绝不渲染一个空下拉——那会让运营以为「不能选变体」。
 */
const visible = computed(() => !(props.spec.name === 'variant' && variantItems.value.length === 0))

const enumOptions = computed<FactOption[]>(() => {
  if (props.spec.name === 'variant') return variantItems.value
  return (props.spec.options ?? []).map(value => ({
    value,
    // 这条 util 返回的是**全键**（`<family.params>.<name>.options.<v>`），族由宿主注入；
    // 直接喂前缀化 `t` 会双前缀，所以这里走 rawT/rawTE。
    label: resolveParamOptionLabel(props.spec.name, value, key => rawT(key), key => rawTE(key), props.labelFamily)
  }))
})

const windowAllLabel = computed(() => (props.simpleMode
  ? t('simple.windowAll')
  : t('window.all')))
const windowRecentLabel = computed(() => (props.simpleMode
  ? t('simple.windowRecent')
  : t('window.recent_days')))

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
  // 参数名文案属于条件族（不是规则族，见 props 注释）：`cc` 只补条件族前缀。
  // 插件自定义参数没有文案时回退原始 name。
  //
  // 两种形状都吃（这是唯一的两种，不再加第三种）：
  // 1) 扁平字符串 `params.<name>`：参数只有标签时用（12 个内置参数都是这样）；
  // 2) 对象 `params.<name>.label`：同一参数**既要有标签又要给取值文案**时用
  //    （`identity.profile` 的 `field`：标签「字段」+ `options.{bio,signature,website,location}`）。
  //    扁平字符串放不下第二个维度，而 `options` 又必须挂在 `params.<name>.options.*`
  //    才能被 `paramOptionLabelKey` 的优先级 1 命中。
  // `te` 对对象节点返回 false（vue-i18n 只认 string / AST / function），所以顺序不会误判。
  const base = `params.${props.spec.name}`
  if (rawTE(`${props.conditionI18nPrefix}.${base}`)) return cc(base)
  if (rawTE(`${props.conditionI18nPrefix}.${base}.label`)) return cc(`${base}.label`)
  return props.spec.name
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

function setWindowKind(event: Event) {
  const kind = (event.target as HTMLSelectElement).value
  setParam(kind === 'recent_days' ? { kind, days: windowDays.value } : { kind })
}

function setWindowDays(event: Event) {
  const days = Number((event.target as HTMLInputElement).value)
  setParam({ kind: windowKind.value, days: Number.isFinite(days) ? days : 1 })
}

/** 选 objectRef 候选：值原样落进 params（显示文案只来自后端候选，不参与取值）。 */
function setRefValue(value: unknown) {
  setParam(typeof value === 'string' ? value : '')
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
          <option value="">{{ cc('factKeyPlaceholder') }}</option>
          <option v-for="item in factItems" :key="item.value" :value="item.value">{{ item.label }}</option>
          <option v-if="factStale" :value="currentFactKey">{{ currentFactKey }}</option>
        </select>
        <p v-if="factStale" class="sf-rule-field__flag" data-fact-stale>
          {{ t('health.missing_fact_key') }} · {{ cc('factStaleHint') }}
        </p>
        <p v-else-if="factCandidatesFailed" class="sf-rule-field__flag" data-fact-candidates-failed>
          {{ cc('factCandidatesFailed') }}
        </p>
      </template>

      <template v-else>
        <!-- 候选不可用 / 为空：文本输入 + 提示（两句分开）。绝不渲染只有占位项的空下拉。 -->
        <template v-if="refManual">
          <input
            class="sf-rule-control"
            data-ref-manual
            :data-ref-kind="spec.refKind"
            :data-ref-manual-state="refMode"
            type="text"
            :value="textValue"
            :aria-label="label"
            @input="setText"
          >
          <p v-if="refUnknownKind" class="sf-rule-field__flag" data-ref-unknown-kind :data-ref-kind="refUnknownKind">
            {{ cc('refUnknownKind', { kind: refUnknownKind }) }}
          </p>
          <p v-else class="sf-rule-field__flag" data-ref-manual-hint>
            {{ refManualHint }}
          </p>
        </template>

        <template v-else>
          <!-- 首用 USelectMenu：`:content` 走 reka-ui Portal（`portal: true` 默认），
               弹层挂在 body 上，不受 Modal 的 overflow 裁切。 -->
          <USelectMenu
            class="sf-rule-ref"
            data-ref-select
            :data-ref-kind="spec.refKind"
            :model-value="textValue"
            :items="refItems"
            value-key="value"
            label-key="label"
            :search-input="true"
            :filter-fields="['label', 'value']"
            :clear="false"
            size="sm"
            :placeholder="cc('refCandidatesPlaceholder')"
            :aria-label="label"
            @update:model-value="setRefValue"
          />
          <p v-if="refTruncated" class="sf-rule-field__flag" data-ref-truncated>
            {{ cc('refTruncated') }}
          </p>
        </template>
      </template>
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
      {{ t('paramUnsupported', { kind: spec.kind }) }}
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

.sf-rule-ref {
  width: 100%;
  min-width: 0;
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
