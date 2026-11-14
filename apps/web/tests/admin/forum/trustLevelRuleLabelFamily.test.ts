import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, h, ref } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'
import { decodeRuleDocument } from '../../../app/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import { createDefaultRule } from '../../../app/utils/admin/rule'
import { BADGE_LABEL_FAMILY, resolveParamOptionLabel } from '../../../app/utils/admin/badgeRuleParams'
import {
  buildFlatAllRule,
  flattenAllLeaves,
  isSimpleRule,
  leafSentence,
  ruleSentence
} from '../../../app/utils/admin/badgeRuleSentence'
import {
  applyFactKeySelection,
  factCandidateUnit,
  factKeyMissing,
  factKeyOptions,
  factVariantOptions
} from '../../../app/utils/admin/badgeFactCandidates'
import {
  isBadgeRefKind,
  refKindGroup,
  refKindItems,
  refKindMode,
  refOptionLabel
} from '../../../app/utils/admin/badgeRefCandidates'
import {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  createGroup,
  insertChild,
  isGroupNode,
  removeNode,
  replaceNode,
  ruleStats,
  validateRuleDraft,
  wrapWithGroup
} from '../../../app/utils/admin/rule'

/**
 * 信任等级的规则编辑器**读自己那一族的标签文案**（M3b-F1）。
 *
 * 缺陷原状：`paramOptionLabelKey` 硬编码返回 `admin.badges.*` 全键，信任族没有
 * `params` / `metrics` / `enums` 三族，于是信任侧的指标下拉与句子里的指标名**永远
 * 读徽章族** —— 徽章族改一次名就会静默改掉信任侧的文案。
 *
 * 本文件就是那条**必红断言**：把 `admin.badges.metrics.topic_count` 改成 `'X'`，
 * 信任编辑器必须仍渲染「主题数」，而徽章编辑器跟着变成 `'X'`。
 * 修复前这条红（信任侧显示 'X'），修复后绿。
 */
const MUTATED = 'X'

const zhSource = JSON.parse(
  readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

/** 变异：徽章族改名。信任侧的文案不该被这一次改名带走。 */
const mutated = JSON.parse(JSON.stringify(zhSource)) as Record<string, unknown>
;(mutated.admin as { badges: { metrics: Record<string, string> } }).badges.metrics.topic_count = MUTATED

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    mutated
  )
}

/** 缺键不 throw：缺文案的观感（显示成键名）也要能被断言看到。 */
const t = (key: string, params: Record<string, string | number> = {}) => {
  const value = lookup(key)
  if (typeof value !== 'string') return `[${key}]`
  return value.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}
const te = (key: string) => typeof lookup(key) === 'string'
const i18nStub = () => ({ t, te, locale: ref('zh-CN') })

const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
/** `USelectMenu` 没注册时 Vue 会把未解析组件压成字符串属性（并打警告），这里给同形 stub。 */
const SelectMenuStub = defineComponent({
  name: 'USelectMenu',
  inheritAttrs: false,
  props: {
    modelValue: { type: String, default: '' },
    items: { type: Array, default: () => [] },
    valueKey: { type: String, default: '' },
    labelKey: { type: String, default: '' },
    placeholder: { type: String, default: '' }
  },
  setup(props) {
    return () => h('div', {
      'data-select-stub': '',
      'data-model': props.modelValue as string,
      'data-placeholder': props.placeholder as string
    }, (props.items as Array<{ value: string, label: string }>).map(item => h('span', {}, item.label)))
  }
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean },
  emits: ['click'],
  template: '<button type="button" v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>'
})

/**
 * 真实链路编译：ParamField ← SimpleForm / Leaf ← Group ← Editor ← 信任宿主。
 * 不 stub 编辑器——要断言的正是「宿主有没有把信任族注入下去」。
 */
const ParamField = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleParamField.vue', import.meta.url)),
  'label-family-param-field',
  {
    computed,
    useI18n: i18nStub,
    resolveParamOptionLabel,
    BADGE_LABEL_FAMILY,
    applyFactKeySelection,
    factKeyMissing,
    factKeyOptions,
    factVariantOptions,
    refKindGroup,
    refKindItems,
    isBadgeRefKind,
    refKindMode,
    refOptionLabel
  }
)

const SimpleForm = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleSimpleForm.vue', import.meta.url)),
  'label-family-simple-form',
  {
    BADGE_RULE_LIMITS,
    createDefaultLeaf,
    isGroupNode,
    validateRuleDraft,
    buildFlatAllRule,
    flattenAllLeaves,
    leafSentence,
    computed,
    useI18n: i18nStub,
    SFAdminRuleParamField: ParamField
  }
)

const Leaf = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleLeaf.vue', import.meta.url)),
  'label-family-leaf',
  {
    createDefaultLeaf,
    replaceNode,
    computed,
    useI18n: i18nStub,
    factCandidateUnit,
    SFAdminRuleParamField: ParamField
  }
)

const Group = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleGroup.vue', import.meta.url)),
  'label-family-group',
  {
    BADGE_RULE_LIMITS,
    createDefaultLeaf,
    createGroup,
    insertChild,
    isGroupNode,
    removeNode,
    replaceNode,
    ruleStats,
    wrapWithGroup,
    computed,
    useI18n: i18nStub,
    SFAdminRuleLeaf: Leaf
  }
)

const JsonPreview = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleJsonPreview.vue', import.meta.url)),
  'label-family-json',
  {
    computed,
    useI18n: i18nStub,
    useToast: () => ({ add: () => {} })
  }
)

const Editor = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/rules/SFAdminRuleEditor.vue', import.meta.url)),
  'label-family-editor',
  {
    computed,
    ref,
    useI18n: i18nStub,
    isGroupNode,
    isSimpleRule,
    ruleSentence,
    SFAdminRuleGroup: Group,
    SFAdminRuleLeaf: Leaf,
    SFAdminRuleSimpleForm: SimpleForm,
    SFAdminRuleJsonPreview: JsonPreview
  }
)

const TrustSection = await compileVueSfc(
  fileURLToPath(new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleSection.vue', import.meta.url)),
  'label-family-trust-section',
  {
    computed,
    useI18n: i18nStub,
    decodeRuleDocument,
    createDefaultRule,
    SFAdminRuleEditor: Editor
  }
)

const METRIC_VALUES = [
  'topic_count',
  'comment_count',
  'like_received_count',
  'like_given_count',
  'bookmark_count',
  'days_visited'
]

const catalog = [{
  key: 'activity.count',
  valueKind: 'count',
  compare: ['gte', 'lte'],
  labelZh: '活跃计数',
  labelEn: 'Activity count',
  unitZh: '次',
  unitEn: 'times',
  params: [
    { name: 'metric', kind: 'enum', required: true, options: METRIC_VALUES },
    { name: 'window', kind: 'window', required: false }
  ]
}]

const rule = {
  v: 1,
  root: {
    type: 'activity.count',
    params: { metric: 'topic_count', window: { kind: 'all' } },
    cmp: { op: 'gte', value: 5 }
  }
}

function mountTrust() {
  return mount(TrustSection, {
    props: {
      tierId: 'member',
      rule,
      conditionsStatus: 'ready',
      catalog
    },
    global: { components: { UIcon: UIconStub, UButton: UButtonStub, USelectMenu: SelectMenuStub } }
  })
}

function mountBadgeEditor() {
  return mount(Editor, {
    props: { modelValue: rule, catalog },
    global: { components: { UIcon: UIconStub, UButton: UButtonStub, USelectMenu: SelectMenuStub } }
  })
}

describe('规则编辑器不跨族读标签文案（必红断言）', () => {
  test('徽章族把 topic_count 改名为 X：信任编辑器仍渲染「主题数」', async () => {
    const wrapper = mountTrust()
    try {
      await flushPromises()
      const labels = wrapper.findAll('[data-param="metric"] option').map(option => option.text())
      // 修复前：信任侧读的是 admin.badges.metrics.*，这里会拿到 'X'。
      expect(labels).toContain('主题数')
      expect(labels).not.toContain(MUTATED)
      // 句子里的指标名走同一条解析链，也必须读信任族。
      expect(wrapper.find('[data-rule-sentence]').text()).toContain('主题数')
      expect(wrapper.find('[data-rule-sentence]').text()).not.toContain(MUTATED)
    } finally {
      wrapper.unmount()
    }
  })

  test('同一份变异下，徽章编辑器跟着变（改动确实生效，不是两边都没读）', async () => {
    const wrapper = mountBadgeEditor()
    try {
      await flushPromises()
      const labels = wrapper.findAll('[data-param="metric"] option').map(option => option.text())
      expect(labels).toContain(MUTATED)
      expect(labels).not.toContain('主题数')
      expect(wrapper.find('[data-rule-sentence]').text()).toContain(MUTATED)
    } finally {
      wrapper.unmount()
    }
  })

  test('参数名文案读条件族：两边都渲染「指标」而不是裸 name', async () => {
    const trust = mountTrust()
    const badge = mountBadgeEditor()
    try {
      await flushPromises()
      expect(trust.find('[data-param="metric"] .sf-rule-field__label').text()).toBe('指标')
      expect(badge.find('[data-param="metric"] .sf-rule-field__label').text()).toBe('指标')
    } finally {
      trust.unmount()
      badge.unmount()
    }
  })
})

/**
 * `identity.profile` 的 `field`：参数**取值**文案（`params.<name>.options.<value>`）。
 *
 * 形状说明：扁平字符串 `params.<name>` 只能放一个标签，放不下「标签 + 取值文案」两个维度，
 * 所以 `field` 是对象（`label` + `options.*`）—— 也正好是 `paramOptionLabelKey`
 * 优先级 1 认得的那条路径。其余 12 个内置参数仍是扁平字符串，读侧两种形状都吃。
 *
 * 没有文案的后果不是「少一行提示」，而是下拉里出现裸 `bio` / `signature`（运营看不懂的
 * 插件内部取值），所以这一条要同时钉住 locale 与渲染两侧。
 */
const FIELD_SLUGS = ['bio', 'signature', 'website', 'location'] as const
const FIELD_ZH: Record<string, string> = { bio: '简介', signature: '签名', website: '个人网站', location: '所在地' }
const FIELD_EN: Record<string, string> = { bio: 'Bio', signature: 'Signature', website: 'Website', location: 'Location' }

const fieldCatalog = [{
  key: 'identity.profile',
  valueKind: 'boolean',
  compare: [],
  labelZh: '资料已填写',
  labelEn: 'Profile filled in',
  params: [{ name: 'field', kind: 'enum', required: true, options: [...FIELD_SLUGS] }]
}]

const fieldRule = {
  v: 1,
  root: { type: 'identity.profile', params: { field: 'location' } }
}

function mountFieldTrust() {
  return mount(TrustSection, {
    props: { tierId: 'member', rule: fieldRule, conditionsStatus: 'ready', catalog: fieldCatalog },
    global: { components: { UIcon: UIconStub, UButton: UButtonStub, USelectMenu: SelectMenuStub } }
  })
}

function mountFieldBadge() {
  return mount(Editor, {
    props: { modelValue: fieldRule, catalog: fieldCatalog },
    global: { components: { UIcon: UIconStub, UButton: UButtonStub, USelectMenu: SelectMenuStub } }
  })
}

describe('identity.profile 的 field 取值文案', () => {
  test('两语言、两族：四个取值都有非空文案（徽章族与信任族逐条一致）', () => {
    for (const locale of ['zh-CN', 'en-US'] as const) {
      const doc = JSON.parse(
        readFileSync(new URL(`../../../i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      ) as Record<string, unknown>
      const at = (path: string) => path.split('.').reduce<unknown>(
        (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
        doc
      )
      const badge = at('admin.badges.conditions.params.field.options') as Record<string, string>
      const trust = at('admin.forum.trustLevels.ruleConditions.params.field.options') as Record<string, string>
      expect(Object.keys(badge).sort()).toEqual([...FIELD_SLUGS].sort())
      expect(Object.keys(trust).sort()).toEqual([...FIELD_SLUGS].sort())
      for (const slug of FIELD_SLUGS) {
        expect({ locale, slug, value: badge[slug] }).toEqual({ locale, slug, value: trust[slug] })
        expect(badge[slug].trim()).toBe((locale === 'zh-CN' ? FIELD_ZH : FIELD_EN)[slug]!)
      }
      // 参数标签也在这条路径上（对象形态的 `.label`）。
      expect(at('admin.badges.conditions.params.field.label')).toBe(locale === 'zh-CN' ? '字段' : 'Field')
    }
  })

  test('信任编辑器渲染中文取值，不出现裸 slug', async () => {
    const wrapper = mountFieldTrust()
    try {
      await flushPromises()
      const label = wrapper.find('[data-param="field"] .sf-rule-field__label').text()
      expect(label).toBe('字段')
      const options = wrapper.findAll('[data-param="field"] option').map(option => option.text())
      expect(options).toEqual(FIELD_SLUGS.map(slug => FIELD_ZH[slug]!))
      for (const slug of FIELD_SLUGS) expect(options).not.toContain(slug)
    } finally {
      wrapper.unmount()
    }
  })

  test('徽章编辑器同样渲染中文取值（同一个控件实现）', async () => {
    const wrapper = mountFieldBadge()
    try {
      await flushPromises()
      expect(wrapper.find('[data-param="field"] .sf-rule-field__label').text()).toBe('字段')
      const options = wrapper.findAll('[data-param="field"] option').map(option => option.text())
      expect(options).toEqual(FIELD_SLUGS.map(slug => FIELD_ZH[slug]!))
    } finally {
      wrapper.unmount()
    }
  })
})
