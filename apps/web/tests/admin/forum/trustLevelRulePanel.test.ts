import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, ref } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'
import { decodeRuleDocument } from '../../../app/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import {
  hasRulePayload,
  normalizeRuleImpactResponse,
  ruleImpactRequestBody
} from '../../../app/composables/admin/forum/trustLevels/useTrustLevelRuleImpact'
import { createDefaultRule } from '../../../app/utils/admin/rule'

/**
 * 面板五态互斥 + 影响面响应归一化。
 *
 * 五态优先级是 copy-spec §3.2 的硬约束：**失败态绝不能落成空白** —— 空白的后果是
 * 运营以为「这档没条件」然后保存，等于把已存条件当成不存在。
 */
const zh = JSON.parse(
  readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    zh
  )
}
const t = (key: string, params: Record<string, string | number> = {}) => {
  const value = lookup(key)
  if (typeof value !== 'string') throw new Error(`missing locale key: ${key}`)
  return value.replace(/\{(\w+)\}/g, (_m, name: string) => String(params[name] ?? `{${name}}`))
}

const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean },
  emits: ['click'],
  template: '<button type="button" v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>'
})
const RuleEditorStub = defineComponent({
  name: 'SFAdminRuleEditor',
  props: { modelValue: Object, catalog: Array, i18nKeyPrefix: String },
  emits: ['update:modelValue'],
  template: '<div data-rule-editor-stub :data-prefix="i18nKeyPrefix" />'
})

const sectionPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleSection.vue', import.meta.url)
)
const Section = await compileVueSfc(sectionPath, 'sf-tl-rule-section', {
  computed,
  useI18n: () => ({ t, te: (key: string) => typeof lookup(key) === 'string', locale: ref('zh-CN') }),
  decodeRuleDocument,
  createDefaultRule,
  SFAdminRuleEditor: RuleEditorStub
})

const readyCatalog = [{
  key: 'activity.count',
  valueKind: 'count',
  compare: ['gte', 'lte'],
  labelZh: '活跃计数',
  labelEn: 'Activity count',
  params: []
}]

function mountSection(props: Record<string, unknown> = {}) {
  return mount(Section, {
    props: { tierId: 'member', rule: null, conditionsStatus: 'ready', catalog: readyCatalog, ...props },
    global: { components: { UIcon: UIconStub, UButton: UButtonStub } }
  })
}

describe('规则面板 · 五态互斥（copy-spec §3.2）', () => {
  test('无权限 > 一切：只出 readOnly，不加载也不提示失败', async () => {
    const wrapper = mountSection({ canManage: false, featureOff: true, conditionsStatus: 'failed' })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-readonly]').text())
        .toBe(t('admin.forum.trustLevels.conditions.panel.readOnly.title'))
      expect(wrapper.find('[data-sf-tl-rule-load-failed]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('功能开关关闭优先于失败态（否则运营会去查网络）', async () => {
    const wrapper = mountSection({ featureOff: true, conditionsStatus: 'failed' })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-feature-off]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-rule-load-failed]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('加载中：一行「正在读取条件目录」', async () => {
    const wrapper = mountSection({ conditionsStatus: 'loading' })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-loading]').text())
        .toBe(t('admin.forum.trustLevels.conditions.panel.loading'))
    } finally {
      wrapper.unmount()
    }
  })

  test('失败态**永不空白**：标题 + 说明 + 原因 + 重试入口', async () => {
    const wrapper = mountSection({ conditionsStatus: 'failed', conditionsError: 'boom' })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-load-failed]').text())
        .toContain(t('admin.forum.trustLevels.conditions.panel.loadFailed.title'))
      expect(wrapper.find('[data-sf-tl-rule-load-failed-reason]').text())
        .toBe(t('admin.forum.trustLevels.conditions.panel.loadFailed.reason', { reason: 'boom' }))
      const retry = wrapper.find('[data-sf-tl-rule-retry]')
      expect(retry.exists()).toBe(true)
      await retry.trigger('click')
      expect(wrapper.emitted('retry')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('成功但目录 0 条：也按不可用处理（等于没得选），不给编辑入口', async () => {
    const wrapper = mountSection({ catalog: [] })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-load-failed]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-rule-add]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('没有规则：一行说明 + 「添加条件」，点了发出默认规则文档', async () => {
    const wrapper = mountSection()
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-empty]').text())
        .toBe(t('admin.forum.trustLevels.conditions.panel.empty.description'))
      await wrapper.find('[data-sf-tl-rule-add]').trigger('click')
      const emitted = wrapper.emitted('update:rule')
      expect(emitted).toHaveLength(1)
      expect(emitted![0]![0]).toMatchObject({ v: 1 })
    } finally {
      wrapper.unmount()
    }
  })

  test('有规则：渲染共享编辑器，且前缀走信任等级族（禁止跨族取词）', async () => {
    const rule = { v: 1, root: { type: 'activity.count', params: {}, cmp: { op: 'gte', value: 3 } } }
    const wrapper = mountSection({ rule })
    try {
      await flushPromises()
      const editor = wrapper.find('[data-rule-editor-stub]')
      expect(editor.exists()).toBe(true)
      expect(editor.attributes('data-prefix')).toBe('admin.forum.trustLevels.rule')
    } finally {
      wrapper.unmount()
    }
  })

  test('原值读不出来：给一行解码失败诊断 + 替换入口，不静默当成「没规则」', async () => {
    const wrapper = mountSection({ rule: { v: 2, root: {} } })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-undecodable]').text())
        .toBe(t('admin.forum.trustLevels.rule.errors.engine.decode_failed'))
      expect(wrapper.find('[data-sf-tl-rule-add]').exists()).toBe(true)
      expect(wrapper.find('[data-rule-editor-stub]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('停用档：允许编辑，只加一句「已停用」说明（不加 alert/新信号）', async () => {
    const wrapper = mountSection({
      retired: true,
      rule: { v: 1, root: { type: 'activity.count', params: {}, cmp: { op: 'gte', value: 1 } } }
    })
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-rule-retired]').text())
        .toBe(t('admin.forum.trustLevels.conditions.panel.retired'))
      expect(wrapper.find('[data-rule-editor-stub]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('影响面响应归一化：0 与 null 严格分开', () => {
  test('0 保持 0（不许被 `||` 变成 null），null 保持 null', () => {
    const parsed = normalizeRuleImpactResponse({
      tiers: [
        { tierId: 'a', estimated: 0, method: 'single_leaf', partial: false, unsupportedConditions: [] },
        { tierId: 'b', estimated: null, method: 'unsupported_condition', partial: false, unsupportedConditions: ['fact.count'] },
        { tierId: 'c', estimated: 12, method: 'sum_of_leaves', partial: true, unsupportedConditions: [] }
      ]
    })
    expect(parsed?.map(item => item.estimated)).toEqual([0, null, 12])
    expect(parsed?.[1]?.unsupportedConditions).toEqual(['fact.count'])
    expect(parsed?.[2]?.partial).toBe(true)
  })

  test('形状不符 → null（调用方按失败处理，不静默当空列表）', () => {
    expect(normalizeRuleImpactResponse(null)).toBeNull()
    expect(normalizeRuleImpactResponse({})).toBeNull()
    expect(normalizeRuleImpactResponse({ tiers: 'nope' })).toBeNull()
  })

  test('请求体带草稿档位：新档（id 为空串）也照样发', () => {
    const tiers = [
      { id: '', requirements: { topic_count: 1 }, rule: { v: 1, root: { type: 'x' } } },
      { id: 'member', requirements: {}, rule: null }
    ] as never
    expect(ruleImpactRequestBody(tiers)).toEqual({
      tiers: [
        { tierId: '', requirements: { topic_count: 1 }, rule: { v: 1, root: { type: 'x' } } },
        { tierId: 'member', requirements: {}, rule: null }
      ]
    })
  })

  test('纯数值编辑不发估算请求（hasRulePayload 只看 rule 字节）', () => {
    expect(hasRulePayload([{ rule: null }, { rule: undefined }] as never)).toBe(false)
    expect(hasRulePayload([{ rule: null }, { rule: { v: 1 } }] as never)).toBe(true)
  })
})
