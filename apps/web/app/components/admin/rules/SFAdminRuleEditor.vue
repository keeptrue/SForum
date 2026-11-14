<script setup lang="ts">
/**
 * 共享条件规则编辑器（徽章 / 信任等级）：整棵条件树的入口，带「简单 / 高级」双模式。
 *
 * 受控组件：`modelValue` 进、`update:modelValue` 出新文档；组件内部没有可变副本，
 * 所有增删改都由子节点用 `rule.ts` 的不可变原语算完再逐层 emit 上来。
 * 目录（条件清单 + 参数形状）由宿主注入，本组件不取数。
 *
 * 两种模式写的是**同一份 AST**：简单模式 = 扁平 `all`（`badgeRuleSentence.ts`
 * 的 `isSimpleRule`），高级模式 = 完整布尔树。切模式**绝不迁移语义**——把 `any`
 * 压平会直接改变判定结果、误发授予，属于数据事故而不是体验问题。
 */
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import { isGroupNode, type BadgeConditionCatalogEntry, type BadgeFactCandidate, type BadgeRule } from '~/utils/admin/rule'
import { isSimpleRule, ruleSentence } from '~/utils/admin/badgeRuleSentence'
import SFAdminRuleGroup from '~/components/admin/rules/SFAdminRuleGroup.vue'
import SFAdminRuleLeaf from '~/components/admin/rules/SFAdminRuleLeaf.vue'
import SFAdminRuleSimpleForm from '~/components/admin/rules/SFAdminRuleSimpleForm.vue'
import SFAdminRuleJsonPreview from '~/components/admin/rules/SFAdminRuleJsonPreview.vue'

const props = withDefaults(defineProps<{
  modelValue: BadgeRule
  catalog: BadgeConditionCatalogEntry[]
  /** 插件事实候选：只有 fact.count 叶子消费，但树上每一层都要往下带到它。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /** objectRef 参数候选：树上每层都要往下带到参数控件。 */
  refCandidates?: BadgeRefCandidateMap
  /**
   * **仅首次渲染的种子**：只在组件挂载时读一次，之后既不随 prop 变化、也不随规则变化
   * 重新推断（否则用户点了切换会被下一次 props 更新顶回去）。唯一用途是测试可控
   * 与宿主强制首屏视图；缺省仍按 `isSimpleRule(modelValue)` 推断。
   *
   * 误用不会改数据：把非简单规则强开成 `simple` 只会进降级视图（只读摘要 + 提示），
   * 不 emit、不压平 AST——见 `adminBadgeRuleSimpleMode.test.ts` 的降级用例。
   */
  initialMode?: RuleMode
  /**
   * i18n 键前缀：共享编辑器不假定宿主域。默认徽章族 `admin.badges.rule`；
   * 信任等级传 `admin.forum.trustLevels.rule`。两份文案一致是**有意的重复**——
   * 跨族取词会让一处改名静默改掉另一处（copy-spec §3.3.2 同一裁决）。
   */
  i18nKeyPrefix?: string
}>(), {
  factCandidates: () => [],
  factCandidatesFailed: false,
  refCandidates: () => ({}),
  initialMode: undefined,
  i18nKeyPrefix: 'admin.badges.rule'
})

type RuleMode = 'simple' | 'advanced'

const emit = defineEmits<{ 'update:modelValue': [value: BadgeRule] }>()

const { t: rawT, te: rawTE, locale: rawLOCALE } = useI18n()

// 前缀化包装：调用点继续写短键，宿主域由 prop 决定。
const t = (key: string, params?: Record<string, unknown>) => rawT(`${props.i18nKeyPrefix}.${key}`, params as never)
const te = (key: string) => rawTE(`${props.i18nKeyPrefix}.${key}`)
const locale = rawLOCALE

/**
 * 切到简单模式时的降级：规则本身超出表达能力（`any` / `not` / 嵌套组）就只显示
 * 只读摘要 + 提示，**不 emit**。绝不为了「看起来能编辑」把树压平或截断。
 */
const mode = ref<RuleMode>(props.initialMode ?? (isSimpleRule(props.modelValue) ? 'simple' : 'advanced'))
const simple = computed(() => isSimpleRule(props.modelValue))
const degraded = computed(() => mode.value === 'simple' && !simple.value)

// 句子生成器拿到的是**已前缀化**的 t/te，且前缀一起传下去（它内部按 `${prefix}.sentence.*` 拼键）：
// 只参数化组件里的 t 不改这里，缺键时 `te` 会在错族里查、静默回落到原始 key。
const sentenceI18n = {
  t: (key: string, params?: Record<string, unknown>) => rawT(key, params as never),
  te: (key: string) => rawTE(key),
  i18nKeyPrefix: props.i18nKeyPrefix
}

/** 常显的一句话预览：两种模式下都看得到「这条规则到底在说什么」。 */
const sentence = computed(() => ruleSentence(
  props.modelValue,
  props.catalog,
  String(locale.value ?? 'zh-CN'),
  sentenceI18n
))

const rootPath: number[] = []
const rootGroup = computed(() => (isGroupNode(props.modelValue.root) ? props.modelValue.root : null))
const rootLeaf = computed(() => (isGroupNode(props.modelValue.root) ? null : props.modelValue.root))

function setMode(next: RuleMode) {
  mode.value = next
}

function update(next: BadgeRule) {
  emit('update:modelValue', next)
}
</script>

<template>
  <section
    class="sf-rule-editor"
    data-sf-rule-editor
    data-sf-badge-rule-editor
    :data-rule-mode="mode"
    :aria-label="t('title')"
  >
    <p v-if="catalog.length === 0" class="sf-rule-editor__notice">
      {{ rawT('admin.badges.conditions.empty') }}
    </p>

    <template v-else>
      <div class="sf-rule-editor__modes" role="group" :aria-label="t('title')">
        <button
          type="button"
          class="sf-rule-editor__mode"
          data-rule-mode-simple
          :aria-pressed="mode === 'simple'"
          @click="setMode('simple')"
        >
          {{ t('mode.simple') }}
        </button>
        <button
          type="button"
          class="sf-rule-editor__mode"
          data-rule-mode-advanced
          :aria-pressed="mode === 'advanced'"
          @click="setMode('advanced')"
        >
          {{ t('mode.advanced') }}
        </button>
      </div>

      <p class="sf-rule-editor__sentence" data-rule-sentence>{{ sentence }}</p>

      <p v-if="degraded" class="sf-rule-editor__notice" data-rule-mode-advanced-only>
        {{ t('modeAdvancedOnly') }}
      </p>

      <SFAdminRuleSimpleForm
        v-if="mode === 'simple' && simple"
        :i18n-key-prefix="i18nKeyPrefix"
        :rule="modelValue"
        :catalog="catalog"
        :fact-candidates="factCandidates ?? []"
        :fact-candidates-failed="factCandidatesFailed === true"
        :ref-candidates="refCandidates ?? {}"
        @update:rule="update"
      />

      <template v-else-if="!degraded">
        <SFAdminRuleGroup
          v-if="rootGroup"
          :i18n-key-prefix="i18nKeyPrefix"
          :rule="modelValue"
          :node="rootGroup"
          :path="rootPath"
          :depth="1"
          :catalog="catalog"
          :fact-candidates="factCandidates ?? []"
          :fact-candidates-failed="factCandidatesFailed === true"
          :ref-candidates="refCandidates ?? {}"
          @update:rule="update"
        />
        <SFAdminRuleLeaf
          v-else-if="rootLeaf"
          :i18n-key-prefix="i18nKeyPrefix"
          :rule="modelValue"
          :node="rootLeaf"
          :path="rootPath"
          :depth="1"
          :catalog="catalog"
          :fact-candidates="factCandidates ?? []"
          :fact-candidates-failed="factCandidatesFailed === true"
          :ref-candidates="refCandidates ?? {}"
          @update:rule="update"
        />
      </template>

      <SFAdminRuleJsonPreview :i18n-key-prefix="i18nKeyPrefix" :rule="modelValue" />
    </template>
  </section>
</template>

<style scoped>
.sf-rule-editor {
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  min-width: 0;
}

.sf-rule-editor__notice {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
}

.sf-rule-editor__modes {
  display: inline-flex;
  gap: 4px;
  align-self: flex-start;
  margin-bottom: 8px;
  padding: 2px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-muted);
}

.sf-rule-editor__mode {
  padding: 4px 12px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sf-rule-editor__mode[aria-pressed='true'] {
  background: var(--sf-card);
  color: var(--sf-fg);
  font-weight: 600;
}

.sf-rule-editor__mode:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-rule-editor__sentence {
  margin: 0 0 8px;
  color: var(--sf-fg);
  font-size: 13px;
  line-height: 1.5;
}
</style>
