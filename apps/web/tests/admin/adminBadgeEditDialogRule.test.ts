import { describe, expect, mock, test } from 'bun:test'
import { computed, defineComponent, reactive, ref, watch } from 'vue'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  BADGE_METRIC_OPTIONS,
  toAdminBadgeInput,
  type AdminBadgeRow
} from '../../app/utils/admin/adminBadges'
import {
  badgeRuleNeedsSummary,
  validateRuleDraft,
  type BadgeConditionCatalogEntry,
  type BadgeRule
} from '../../app/utils/admin/badgeRule'
import { useBadgeRuleDraft } from '../../app/composables/admin/badges/useBadgeRuleDraft'
import { useAdminBadgeRuleImpact } from '../../app/composables/admin/badges/useAdminBadgeRuleImpact'
import type { BadgeRuleHealthRow } from '../../app/composables/admin/badges/useAdminBadgeRuleHealth'
import { forumBadgeTier, pickTierName } from '../../app/utils/forum/forumBadge'
import { tierColorVar } from '../../app/utils/theme/tierColor'
import { apiErrorFields, apiErrorMessage } from '../../app/composables/useApiClient'

/**
 * 规则编辑器接进编辑弹窗后的提交契约：
 * 三态（缺省 = 不改 / null = 清空 / 对象 = 替换）、保存前校验、legacy 字段只读。
 */
const i18nStub = () => ({
  t: (key: string) => key,
  // 除哨兵码外都视为有文案：这样才能同时覆盖「查得到」与「查不到回退 generic」两条路径。
  te: (key: string) => !key.includes('weird_engine_code'),
  locale: { value: 'zh-CN' }
})

const toastCalls: unknown[] = []
const useToastStub = () => ({ add: (payload: unknown) => { toastCalls.push(payload) } })

const requests: Array<{ path: string, options?: { method?: string, body?: Record<string, unknown> } }> = []
/** 影响面端点可单独失败：用来验「拉取失败仍可保存」。 */
let impactFails = false
let impactPayload: Record<string, unknown> = { estimated: 12, method: 'single_leaf', partial: false, unsupportedConditions: [] }
const requestMock = mock(async (path: string, options?: unknown) => {
  requests.push({ path, options: options as { method?: string, body?: Record<string, unknown> } })
  if (path.endsWith('/rule-impact')) {
    if (impactFails) throw new Error('rule-impact unavailable')
    return impactPayload
  }
  return {}
})
const useApiClientStub = () => ({ request: requestMock })
// 影响面 composable 是真实模块（不经 SFC 编译），它里面的 `useApiClient` 需要全局注入。
;(globalThis as unknown as Record<string, unknown>).useApiClient = useApiClientStub

const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })
const UModalStub = defineComponent({ name: 'UModal', props: ['open'], template: '<div><slot name="content" /></div>' })
const UFormFieldStub = defineComponent({
  name: 'UFormField',
  props: ['label', 'error'],
  template: '<div><span>{{ label }}</span><slot /><slot name="hint" /></div>'
})
const UInputStub = defineComponent({
  name: 'UInput',
  props: { modelValue: [String, Number], disabled: Boolean, type: String },
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" :disabled="disabled" :type="type" @input="$emit(\'update:modelValue\', $event.target.value)">'
})
const USelectStub = defineComponent({
  name: 'USelect',
  props: { modelValue: { type: [String, Number, Object], default: null }, items: { type: Array, default: () => [] }, disabled: Boolean },
  emits: ['update:modelValue'],
  template: `<select :value="modelValue" :disabled="disabled" @change="$emit('update:modelValue', $event.target.value)">
    <option v-for="(item, index) in items" :key="index" :value="item.value">{{ item.label }}</option>
    <slot />
  </select>`
})
const UTextareaStub = defineComponent({
  name: 'UTextarea',
  props: { modelValue: String, rows: Number },
  emits: ['update:modelValue'],
  template: '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
})
const USwitchStub = defineComponent({
  name: 'USwitch',
  props: { modelValue: Boolean },
  emits: ['update:modelValue'],
  template: '<button type="button" />'
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean, loading: Boolean },
  emits: ['click'],
  template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
})
const IconFieldStub = defineComponent({
  name: 'SFAdminBadgeIconField',
  props: { modelValue: String, label: String, moreLabel: String, lessLabel: String, accent: String, error: String },
  emits: ['update:modelValue'],
  template: '<div data-icon-field />'
})
/** 影响面弹窗在接线测试里只需要「能确认/能取消」与它收到的 props。 */
const ImpactDialogStub = defineComponent({
  name: 'SFAdminBadgeRuleImpactDialog',
  props: {
    open: Boolean,
    impact: { type: Object, default: null },
    status: { type: String, default: 'idle' },
    ruleTouched: Boolean,
    catalog: { type: Array, default: () => [] }
  },
  emits: ['confirm', 'cancel', 'update:open'],
  template: `<div data-impact-dialog :data-status="status" :data-open="String(open)" :data-rule-touched="String(ruleTouched)">
    <button data-impact-confirm type="button" @click="$emit('confirm')">confirm</button>
    <button data-impact-cancel type="button" @click="$emit('cancel')">cancel</button>
  </div>`
})

const RuleEditorStub = defineComponent({
  name: 'SFAdminBadgeRuleEditor',
  props: { modelValue: { type: Object, required: true }, catalog: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<div data-rule-editor />'
})

const RuleSectionPath = fileURLToPath(
  new URL('../../app/components/admin/badges/SFAdminBadgeRuleSection.vue', import.meta.url)
)
const RuleSection = await compileVueSfc(RuleSectionPath, 'badge-rule-section', {
  BADGE_METRIC_OPTIONS,
  badgeRuleNeedsSummary,
  computed,
  useI18n: i18nStub,
  SFAdminBadgeRuleEditor: RuleEditorStub
})

const dialogPath = fileURLToPath(
  new URL('../../app/components/admin/badges/SFAdminBadgeEditDialog.vue', import.meta.url)
)
const EditDialog = await compileVueSfc(dialogPath, 'badge-edit-dialog', {
  BADGE_METRIC_OPTIONS,
  toAdminBadgeInput,
  validateRuleDraft,
  useBadgeRuleDraft,
  forumBadgeTier,
  pickTierName,
  tierColorVar,
  apiErrorFields,
  apiErrorMessage,
  computed,
  reactive,
  ref,
  watch,
  useI18n: i18nStub,
  useApiClient: useApiClientStub,
  useToast: useToastStub,
  useAdminBadgeRuleImpact,
  SFAdminBadgeIconField: IconFieldStub,
  SFAdminBadgeRuleSection: RuleSection,
  SFAdminBadgeRuleImpactDialog: ImpactDialogStub
})

const catalog: BadgeConditionCatalogEntry[] = [
  {
    key: 'activity.count',
    valueKind: 'count',
    compare: ['gte', 'lte'],
    labelZh: '计数',
    labelEn: 'Count',
    params: [
      { name: 'metric', kind: 'enum', required: true, options: [{ value: 'topic_count', labelZh: '主题数', labelEn: 'Topics' }] },
      { name: 'window', kind: 'window', required: true }
    ]
  }
]

const validRule: BadgeRule = {
  v: 1,
  root: {
    op: 'all',
    children: [
      { type: 'activity.count', params: { metric: 'topic_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 1 } }
    ]
  }
}

const emptyGroupRule: BadgeRule = { v: 1, root: { op: 'all', children: [] } }

function badgeRow(overrides: Partial<AdminBadgeRow> = {}): AdminBadgeRow {
  return {
    id: 1,
    slug: 'resident',
    name: 'Resident',
    description: '',
    icon: 'i-lucide-medal',
    tier: 'silver',
    groupId: null,
    groupName: null,
    grantMode: 'automatic',
    metric: 'topic_count',
    threshold: 10,
    grantedCount: 0,
    enabled: true,
    position: 0,
    ...overrides
  }
}

async function mountOpen(
  badge: AdminBadgeRow | null,
  items: BadgeConditionCatalogEntry[] = catalog,
  catalogFailed = false,
  ruleHealth: BadgeRuleHealthRow[] = [],
  ruleHealthFailed = false
) {
  const wrapper = mount(EditDialog, {
    props: {
      groups: [],
      tiers: [],
      badge,
      catalog: items,
      catalogFailed,
      ruleHealth,
      ruleHealthFailed,
      open: false
    },
    global: {
      stubs: {
        UModal: UModalStub,
        UFormField: UFormFieldStub,
        UInput: UInputStub,
        USelect: USelectStub,
        UTextarea: UTextareaStub,
        USwitch: USwitchStub,
        UButton: UButtonStub,
        UIcon: UIconStub
      }
    }
  })
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

function saveButton(wrapper: Awaited<ReturnType<typeof mountOpen>>) {
  return wrapper.findAll('button').find(button => button.text() === 'admin.badges.actions.save')!
}

/**
 * 点保存；若弹出了影响面确认（编辑既有自动徽章），再点「确认」。
 * 返回是否真的弹过确认 —— 新建路径必须为 false。
 */
async function saveAndConfirm(wrapper: Awaited<ReturnType<typeof mountOpen>>): Promise<boolean> {
  await saveButton(wrapper).trigger('click')
  await flushPromises()
  const confirm = wrapper.find('[data-impact-confirm]')
  const opened = confirm.exists()
  if (opened) {
    await confirm.trigger('click')
    await flushPromises()
  }
  return opened
}

function lastBody() {
  const last = requests[requests.length - 1]
  expect(last).toBeDefined()
  return last!.options!.body as Record<string, unknown>
}

describe('badge edit dialog · rule editor wiring', () => {
  test('automatic 显示规则编辑器，legacy 指标/阈值只读', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(true)
      expect(wrapper.find('[data-field="metric"]').attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-field="threshold"]').attributes('disabled')).toBeDefined()
      expect(wrapper.text()).toContain('admin.badges.form.legacyRuleHint')
    } finally {
      wrapper.unmount()
    }
  })

  test('切回 manual 隐藏编辑器，且提交体不带 rule', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      await wrapper.find('[data-field="grantMode"]').setValue('manual')
      await flushPromises()
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(false)

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      const body = lastBody()
      expect(body.grantMode).toBe('manual')
      expect('rule' in body).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('只改名称时提交体不含 rule（缺省 = 不改）', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      await wrapper.find('[data-field="name"]').setValue('Resident Plus')
      requests.length = 0
      expect(await saveAndConfirm(wrapper)).toBe(true)
      const body = lastBody()
      expect(body.name).toBe('Resident Plus')
      expect('rule' in body).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('manual 徽章切到 automatic 时带默认规则提交', async () => {
    const wrapper = await mountOpen(badgeRow({ grantMode: 'manual', metric: null, threshold: null, rule: null }))
    try {
      await wrapper.find('[data-field="grantMode"]').setValue('automatic')
      await flushPromises()
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(true)

      requests.length = 0
      await saveAndConfirm(wrapper)
      const body = lastBody()
      expect(body.grantMode).toBe('automatic')
      expect((body.rule as BadgeRule).root).toMatchObject({ op: 'all' })
      expect(validateRuleDraft(body.rule as BadgeRule, catalog).ok).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('目录未接线时保持旧口径：新建自动徽章不发 rule', async () => {
    const wrapper = await mountOpen(null, [])
    try {
      await wrapper.find('[data-field="slug"]').setValue('newcomer')
      await wrapper.find('[data-field="name"]').setValue('Newcomer')
      await wrapper.find('[data-field="grantMode"]').setValue('automatic')
      await flushPromises()

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      const body = lastBody()
      expect(body.grantMode).toBe('automatic')
      expect(body.metric).toBe('topic_count')
      expect('rule' in body).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('动过规则且不合法时阻止提交，错误挂在 rule 字段上', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      // 模拟编辑器把树改空：touched = true，提交体就会带上这条规则。
      wrapper.findComponent(RuleEditorStub).vm.$emit('update:modelValue', emptyGroupRule)
      await flushPromises()

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      expect(requests).toHaveLength(0)
      expect(wrapper.find('[data-rule-errors]').text()).toContain('admin.badges.rule.errors.empty_group')
    } finally {
      wrapper.unmount()
    }
  })

  test('健康清单只渲染当前徽章的失效引用（不渲染零授予、不串号）', async () => {
    const health: BadgeRuleHealthRow[] = [
      {
        badgeId: 1,
        slug: 'resident',
        name: 'Resident',
        issues: [
          { kind: 'missing_topic', path: 'root.children[0].params.topicId', ref: '123', label: '主题 #123' }
        ]
      },
      {
        badgeId: 99,
        slug: 'other',
        name: 'Other',
        issues: [
          { kind: 'missing_role', path: 'root.children[0].params.roleKey', ref: 'mod', label: '角色 mod' }
        ]
      }
    ]
    const wrapper = await mountOpen(badgeRow({ rule: validRule }), catalog, false, health)
    try {
      const list = wrapper.find('[data-rule-health]')
      expect(list.exists()).toBe(true)
      expect(list.text()).toContain('主题 #123')
      expect(list.text()).toContain('admin.badges.rule.health.missing_topic')
      expect(list.text()).not.toContain('角色 mod')
      expect(list.findAll('li')).toHaveLength(1)
      expect(list.find('li').attributes('title')).toBe('root.children[0].params.topicId')
    } finally {
      wrapper.unmount()
    }
  })

  test('invalid_rule_shape 用 ref 查错误码文案，而不是空标题', async () => {
    const health: BadgeRuleHealthRow[] = [
      {
        badgeId: 1,
        slug: 'resident',
        name: 'Resident',
        issues: [
          { kind: 'invalid_rule_shape', path: 'root.children[0]', ref: 'shape_invalid', label: '' }
        ]
      }
    ]
    const wrapper = await mountOpen(badgeRow({ rule: validRule }), catalog, false, health)
    try {
      const list = wrapper.find('[data-rule-health]')
      expect(list.text()).toBe('admin.badges.rule.errors.engine.shape_invalid')
      expect(list.text()).not.toContain('（')
    } finally {
      wrapper.unmount()
    }
  })

  test('invalid_rule_shape 的未知 ref 回退通用文案（真实 i18n 会带上码）', async () => {
    const health: BadgeRuleHealthRow[] = [
      {
        badgeId: 1,
        slug: 'resident',
        name: 'Resident',
        issues: [
          { kind: 'invalid_rule_shape', path: 'root', ref: 'weird_engine_code', label: '' }
        ]
      }
    ]
    const wrapper = await mountOpen(badgeRow({ rule: validRule }), catalog, false, health)
    try {
      expect(wrapper.find('[data-rule-health]').text()).toBe('admin.badges.rule.errors.generic')
    } finally {
      wrapper.unmount()
    }
  })

  test('健康清单为空（全站健康）时不渲染任何提示', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      expect(wrapper.find('[data-rule-health]').exists()).toBe(false)
      expect(wrapper.find('[data-rule-health-failed]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('健康检查加载失败时给可见提示，不静默', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }), catalog, false, [], true)
    try {
      expect(wrapper.find('[data-rule-health-failed]').text()).toBe('admin.badges.rule.healthUnavailable')
      expect(wrapper.find('[data-rule-health]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('目录加载失败时条件区给出可见提示，不静默降级', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: null }), [], true)
    try {
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(false)
      expect(wrapper.find('[data-rule-catalog-state]').text()).toBe('admin.badges.rule.catalogUnavailable')
    } finally {
      wrapper.unmount()
    }
  })

  test('automatic 徽章服务端没有规则时显示空树 + 提示，且保存被拒（不自动补默认规则）', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: null }))
    try {
      expect(wrapper.find('[data-rule-missing]').text()).toBe('admin.badges.rule.missingHint')
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(true)

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      expect(requests).toHaveLength(0)
      expect(wrapper.find('[data-rule-errors]').text()).toContain('admin.badges.rule.errors.empty_group')
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 影响面确认的接线（2-4）：只有「编辑既有自动徽章」才先估再确认。
 *
 * 新建徽章没有 id，端点只能估「库里已保存的规则」，草稿还没有身份 —— 所以新建路径
 * 连请求都不该发，更不能编一个假数字。
 */
describe('badge edit dialog · rule impact confirmation', () => {
  test('editing an automatic badge asks for the estimate first and sends no PATCH until confirmed', async () => {
    impactFails = false
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      await saveButton(wrapper).trigger('click')
      await flushPromises()

      // 先拉估算，还没有 PATCH。
      expect(requests.map(entry => entry.path)).toEqual(['/admin/badges/1/rule-impact'])
      const dialog = wrapper.getComponent(ImpactDialogStub)
      expect(dialog.props('status')).toBe('ready')
      expect(dialog.props('impact')).toEqual({
        estimated: 12,
        method: 'single_leaf',
        partial: false,
        unsupportedConditions: []
      })
      expect(dialog.props('ruleTouched')).toBe(false)

      await wrapper.get('[data-impact-confirm]').trigger('click')
      await flushPromises()
      expect(requests[requests.length - 1]!.path).toBe('/admin/badges/1')
      expect(requests[requests.length - 1]!.options?.method).toBe('PATCH')
      expect(toastCalls.length).toBeGreaterThan(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('cancelling the confirmation sends no PATCH at all', async () => {
    impactFails = false
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      await wrapper.get('[data-impact-cancel]').trigger('click')
      await flushPromises()

      expect(requests.map(entry => entry.path)).toEqual(['/admin/badges/1/rule-impact'])
      expect(wrapper.find('[data-impact-dialog]').attributes('data-open')).toBe('false')
    } finally {
      wrapper.unmount()
    }
  })

  test('editing an existing badge keeps the dialog open and reports a failed estimate', async () => {
    impactFails = true
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      await saveButton(wrapper).trigger('click')
      await flushPromises()

      const dialog = wrapper.getComponent(ImpactDialogStub)
      expect(dialog.props('status')).toBe('error')
      expect(dialog.props('impact')).toBeNull()
      // 失败不阻塞：确认后照样发 PATCH。
      await wrapper.get('[data-impact-confirm]').trigger('click')
      await flushPromises()
      expect(requests[requests.length - 1]!.options?.method).toBe('PATCH')
    } finally {
      impactFails = false
      wrapper.unmount()
    }
  })

  test('a new badge never calls the impact endpoint (no id, nothing to estimate)', async () => {
    impactFails = false
    requests.length = 0
    const wrapper = await mountOpen(null)
    try {
      await wrapper.find('[data-field="slug"]').setValue('newcomer')
      await wrapper.find('[data-field="name"]').setValue('Newcomer')
      await wrapper.find('[data-field="grantMode"]').setValue('automatic')
      await flushPromises()

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()

      expect(requests.filter(entry => entry.path.includes('/rule-impact'))).toHaveLength(0)
      expect(requests.map(entry => entry.options?.method)).toEqual(['POST'])
      // 弹窗根节点常驻（受控 open），新建路径必须始终是关着的。
      expect(wrapper.get('[data-impact-dialog]').attributes('data-open')).toBe('false')
    } finally {
      wrapper.unmount()
    }
  })

  test('a touched rule is flagged so the dialog can mention the background re-evaluation', async () => {
    impactFails = false
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      wrapper.findComponent(RuleEditorStub).vm.$emit('update:modelValue', validRule)
      await flushPromises()
      await saveButton(wrapper).trigger('click')
      await flushPromises()

      expect(wrapper.get('[data-impact-dialog]').attributes('data-rule-touched')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * 公开说明（requirementSummary）的字段提示与写回契约（本轮 5）。
 *
 * 两条边界：
 * 1. 提示**只是提醒** —— 按钮不禁用、不进 fieldErrors、保存照发；
 * 2. 写回是整体替换 —— 两语言要么整对给，要么完全不给；读不到现值时必须「完全不给」，
 *    否则一个空串就会把线上已有文案抹掉。
 */
const autoRule: BadgeRule = {
  v: 1,
  root: {
    type: 'activity.count',
    params: { metric: 'topic_count', window: { kind: 'all' } },
    cmp: { op: 'gte', value: 3 }
  }
}

const twoLeafRule: BadgeRule = {
  v: 1,
  root: {
    op: 'all',
    children: [
      { type: 'activity.count', params: { metric: 'topic_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 3 } },
      { type: 'activity.count', params: { metric: 'comment_count', window: { kind: 'all' } }, cmp: { op: 'gte', value: 5 } }
    ]
  }
}

describe('badge edit dialog · requirementSummary', () => {
  test('the hint shows for a shape the server cannot describe, and saving still goes through', async () => {
    requests.length = 0
    toastCalls.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '', en: '' } }))
    try {
      expect(wrapper.get('[data-rule-summary-hint]').text()).toBe('admin.badges.form.requirementSummaryMissing')
      // 提醒不是阻止：按钮仍可点，点完确实发了请求。
      const button = saveButton(wrapper)
      expect(button.attributes('disabled')).toBeUndefined()
      await saveAndConfirm(wrapper)
      expect(requests.some(entry => entry.options?.method === 'PATCH')).toBe(true)
      expect(wrapper.find('[data-rule-errors]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('the server-describable single-leaf shape shows no hint', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: autoRule, requirementSummary: { zh: '', en: '' } }))
    try {
      expect(wrapper.find('[data-rule-summary-hint]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('either language filled silences the hint (only both-empty counts)', async () => {
    const zhFilled = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '连续发帖三篇', en: '' } }))
    try {
      expect(zhFilled.find('[data-rule-summary-hint]').exists()).toBe(false)
    } finally {
      zhFilled.unmount()
    }

    const enOnly = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '', en: 'three topics' } }))
    try {
      expect(enOnly.find('[data-rule-summary-hint]').exists()).toBe(false)
    } finally {
      enOnly.unmount()
    }
  })

  test('typing a summary hides the hint without leaving the dialog', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '', en: '' } }))
    try {
      expect(wrapper.find('[data-rule-summary-hint]').exists()).toBe(true)
      await wrapper.find('[data-field="requirementSummaryZh"]').setValue('连续发帖三篇')
      await flushPromises()
      expect(wrapper.find('[data-rule-summary-hint]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('the body always carries both locales together, even when only one side has text', async () => {
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '连续发帖三篇', en: '' } }))
    try {
      await saveAndConfirm(wrapper)
      const body = lastBody()
      expect(body.requirementSummary).toEqual({ zh: '连续发帖三篇', en: '' })
      expect(Object.keys(body.requirementSummary as Record<string, unknown>).sort()).toEqual(['en', 'zh'])
    } finally {
      wrapper.unmount()
    }
  })

  test('an old backend without the field sends no requirementSummary at all', async () => {
    requests.length = 0
    // badgeRow 默认不带 requirementSummary（= undefined，模拟旧后端缺字段）。
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      await saveAndConfirm(wrapper)
      const body = lastBody()
      expect('requirementSummary' in body).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('a new badge sends the pair too (nothing to overwrite)', async () => {
    requests.length = 0
    const wrapper = await mountOpen(null)
    try {
      await wrapper.find('[data-field="slug"]').setValue('newcomer')
      await wrapper.find('[data-field="name"]').setValue('Newcomer')
      await wrapper.find('[data-field="grantMode"]').setValue('automatic')
      await flushPromises()
      await wrapper.find('[data-field="requirementSummaryEn"]').setValue('ten topics')
      await flushPromises()

      await saveButton(wrapper).trigger('click')
      await flushPromises()
      const body = lastBody()
      expect(body.requirementSummary).toEqual({ zh: '', en: 'ten topics' })
    } finally {
      wrapper.unmount()
    }
  })
})
