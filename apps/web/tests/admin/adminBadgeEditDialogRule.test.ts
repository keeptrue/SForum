import { describe, expect, mock, test } from 'bun:test'
import { computed, defineComponent, reactive, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  BADGE_METRIC_OPTIONS,
  toAdminBadgeInput,
  type AdminBadgeRow
} from '../../app/utils/admin/adminBadges'
import {
  validateRuleDraft,
  type BadgeConditionCatalogEntry,
  type BadgeRule
} from '../../app/utils/admin/badgeRule'
import { useBadgeRuleDraft } from '../../app/composables/admin/badges/useBadgeRuleDraft'
import { useAdminBadgeRuleImpact } from '../../app/composables/admin/badges/useAdminBadgeRuleImpact'
import { useBadgeRuleTextPreview } from '../../app/composables/admin/badges/useBadgeRuleTextPreview'
import type { BadgeRuleHealthRow } from '../../app/composables/admin/badges/useAdminBadgeRuleHealth'
import { forumBadgeTier, pickTierName } from '../../app/utils/forum/forumBadge'
import { tierColorVar } from '../../app/utils/theme/tierColor'
import { apiErrorFields, apiErrorMessage } from '../../app/composables/useApiClient'
import { FIELD_SLOT_KEYS } from '../../app/utils/admin/badgeFieldSlots'
import { excludeBadgeSlug } from '../../app/utils/admin/badgeRefCandidates'

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
/** 保存失败信封：用来验「没有展示位的字段错误」也不会消失。 */
let saveFailure: unknown = null
/** 预览端点可单独失败：用来验「预览失败不阻断保存、也不清空旧文案」。 */
let ruleTextFails = false
/** 保存成功响应可注入：用来验「保留后以服务端 requirementText 为准」。 */
let saveResponse: unknown = null
let impactPayload: Record<string, unknown> = { estimated: 12, method: 'single_leaf', partial: false, unsupportedConditions: [] }
const requestMock = mock(async (path: string, options?: unknown) => {
  requests.push({ path, options: options as { method?: string, body?: Record<string, unknown> } })
  if (path.endsWith('/rule-text')) {
    if (ruleTextFails) throw new Error('rule-text unavailable')
    return { zh: '主题数 ≥ 3', en: 'Topics ≥ 3' }
  }
  if (path.endsWith('/rule-impact')) {
    if (impactFails) throw new Error('rule-impact unavailable')
    return impactPayload
  }
  const method = (options as { method?: string } | undefined)?.method
  if (saveFailure && method === 'POST') throw saveFailure
  if (saveResponse && (method === 'POST' || method === 'PATCH')) return saveResponse
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
  // 与 Nuxt UI 同构：标题是 label 元素，hint 曾作为同名插槽塞在同一行。
  template: '<div><label>{{ label }}</label><slot /><slot name="hint" /></div>'
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
  excludeBadgeSlug,
  FIELD_SLOT_KEYS,
  computed,
  reactive,
  ref,
  watch,
  useI18n: i18nStub,
  useApiClient: useApiClientStub,
  useToast: useToastStub,
  useAdminBadgeRuleImpact,
  useBadgeRuleTextPreview,
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
      // wire 形状：目录只下发取值，文案走 i18n。
      { name: 'metric', kind: 'enum', required: true, options: ['topic_count'] },
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
  test('automatic 显示规则编辑器，legacy 指标/阈值整段不再渲染', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      expect(wrapper.find('[data-rule-editor]').exists()).toBe(true)
      // 规则接管判定后这两栏既没有写路径也没有信息量：控件与提示都必须消失，
      // 而不是留在界面上当「只读噪音」。
      expect(wrapper.find('[data-field="metric"]').exists()).toBe(false)
      expect(wrapper.find('[data-field="threshold"]').exists()).toBe(false)
      expect(wrapper.text()).not.toContain('admin.badges.form.legacyRuleHint')
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
 * 公开说明 = **只读预览**（草稿实时，句子由服务端按规则生成）。
 *
 * 锁四件事：没有输入控件、三态（loading/ready/empty/error）可见、预览失败不阻断保存
 * 且不进 `fieldErrors`、保存体永不携带旧的手填字段。
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

/** 预览防抖 300ms：断言结果前必须等过这一档。 */
const PREVIEW_SETTLED_MS = 350

describe('badge edit dialog · 公开说明只读预览', () => {
  test('没有可编辑控件：不存在 textarea 与手填字段', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      expect(wrapper.find('textarea[data-field="requirementSummaryZh"]').exists()).toBe(false)
      expect(wrapper.find('textarea[data-field="requirementSummaryEn"]').exists()).toBe(false)
      expect(wrapper.find('[data-field="requirementSummaryZh"]').exists()).toBe(false)
      expect(wrapper.find('[data-field="requirementSummaryEn"]').exists()).toBe(false)
      expect(wrapper.find('[data-requirement-text]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('草稿预览：防抖后渲染服务端返回的中英文只读文本 + 一行「自动生成」说明', async () => {
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      // 防抖窗口内还没发请求：标成 pending，只给占位。
      expect(wrapper.find('[data-requirement-text-zh]').attributes('aria-busy')).toBe('true')
      await Bun.sleep(PREVIEW_SETTLED_MS)
      await flushPromises()

      expect(requests.some(entry => entry.path.includes('/rule-text'))).toBe(true)
      expect(wrapper.get('[data-requirement-text-zh]').text()).toBe('主题数 ≥ 3')
      expect(wrapper.get('[data-requirement-text-en]').text()).toBe('Topics ≥ 3')
      expect(wrapper.get('[data-requirement-text-auto]').text()).toBe('admin.badges.form.requirementTextAuto')
      expect(wrapper.find('[data-requirement-text-failed]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('加载中：只给占位符号，不渲染上一次的旧句子', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      const zh = wrapper.get('[data-requirement-text-zh]')
      expect(zh.attributes('aria-busy')).toBe('true')
      expect(zh.text()).toBe('…')
      expect(wrapper.get('[data-requirement-text-en]').text()).toBe('…')
      // 「由规则自动生成」的说明在加载态就给，文案不依赖请求结果。
      expect(wrapper.find('[data-requirement-text-auto]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('保存成功后以服务端返回的 requirementText 为准（覆盖草稿预览）', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      await Bun.sleep(PREVIEW_SETTLED_MS)
      await flushPromises()
      expect(wrapper.get('[data-requirement-text-zh]').text()).toBe('主题数 ≥ 3')

      saveResponse = { requirementText: { zh: '落库后的公开说明', en: 'Saved public copy' } }
      await saveAndConfirm(wrapper)
      await flushPromises()

      expect(wrapper.get('[data-requirement-text-zh]').text()).toBe('落库后的公开说明')
      expect(wrapper.get('[data-requirement-text-en]').text()).toBe('Saved public copy')
    } finally {
      saveResponse = null
      wrapper.unmount()
    }
  })

  test('规则为空（目录未就绪 + 无规则）显示空状态，而不是空字符串', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: null }), [])
    try {
      expect(wrapper.find('[data-requirement-text-empty]').text()).toBe('admin.badges.form.requirementTextEmpty')
      expect(wrapper.find('[data-requirement-text-zh]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('预览失败：保留上一次文案 + 一条提示，且不阻断保存、不进 rule 错误', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: autoRule }))
    try {
      await Bun.sleep(PREVIEW_SETTLED_MS)
      await flushPromises()
      expect(wrapper.get('[data-requirement-text-zh]').text()).toBe('主题数 ≥ 3')

      ruleTextFails = true
      wrapper.findComponent(RuleEditorStub).vm.$emit('update:modelValue', twoLeafRule)
      await flushPromises()
      await Bun.sleep(PREVIEW_SETTLED_MS)
      await flushPromises()

      expect(wrapper.find('[data-requirement-text-failed]').text()).toBe('admin.badges.form.requirementTextPreviewFailed')
      // 派生数据断了不清空已看到的句子。
      expect(wrapper.get('[data-requirement-text-zh]').text()).toBe('主题数 ≥ 3')
      // 预览失败不进字段错误，也不拦保存。
      expect(wrapper.find('[data-rule-errors]').exists()).toBe(false)
      expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()

      requests.length = 0
      await saveAndConfirm(wrapper)
      expect(requests.some(entry => entry.options?.method === 'PATCH')).toBe(true)
    } finally {
      ruleTextFails = false
      wrapper.unmount()
    }
  })

  test('保存体永不携带 requirementSummary（手填口径已移除）', async () => {
    requests.length = 0
    const wrapper = await mountOpen(badgeRow({ rule: twoLeafRule, requirementSummary: { zh: '旧文案', en: 'legacy' } }))
    try {
      await saveAndConfirm(wrapper)
      const body = lastBody()
      expect('requirementSummary' in body).toBe(false)
      expect('requirementText' in body).toBe(false)
      // 其余字段照旧：grantMode 照发，未改动的规则按三态契约连 `rule` 键都不出现。
      expect(body.grantMode).toBe('automatic')
      expect('rule' in body).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('没有展示位的字段错误并进通用提示，有展示位的仍走自己的字段位', async () => {
    // 后端仍可能对已经没有控件的列（legacy metric / threshold）报 422：这些键没有落点，
    // 必须出现在通用提示里，否则运营看不到任何线索。
    saveFailure = {
      code: 422,
      message: 'validation failed',
      data: { fields: { metric: ['bad'], threshold: ['bad too'], name: ['keep'] } }
    }
    const wrapper = await mountOpen(null)
    try {
      await wrapper.find('[data-field="slug"]').setValue('newcomer')
      await wrapper.find('[data-field="name"]').setValue('Newcomer')
      await flushPromises()

      requests.length = 0
      await saveButton(wrapper).trigger('click')
      await flushPromises()
      expect(requests.at(-1)?.options?.method).toBe('POST')

      const text = wrapper.text()
      expect(text).toContain('metric: bad')
      expect(text).toContain('threshold: bad too')
      // 有字段位的键不进通用提示：那边已经有它的位置，重复反而是噪音。
      expect(text).not.toContain('name: keep')
      const nameField = wrapper.findAllComponents(UFormFieldStub)
        .find(component => component.props('label') === 'admin.badges.form.name')
      expect(nameField?.props('error')).toBe('keep')
    } finally {
      saveFailure = null
      wrapper.unmount()
    }
  })

  test('新建 automatic 徽章：保存体同样不含 requirementSummary', async () => {
    requests.length = 0
    const wrapper = await mountOpen(null)
    try {
      await wrapper.find('[data-field="slug"]').setValue('newcomer')
      await wrapper.find('[data-field="name"]').setValue('Newcomer')
      await wrapper.find('[data-field="grantMode"]').setValue('automatic')
      await flushPromises()

      await saveButton(wrapper).trigger('click')
      await flushPromises()
      const body = lastBody()
      expect('requirementSummary' in body).toBe(false)
      expect(body.grantMode).toBe('automatic')
    } finally {
      wrapper.unmount()
    }
  })
})

/**
 * `FIELD_SLOT_KEYS` 是手抄的「哪些字段有专属展示位」清单。它一旦和模板脱钩，
 * 要么某些错误又变静默（清单多了），要么被正确展示的字段被重复塞进通用提示（清单少了）。
 * 这条测试直接比对源码里的 `fieldErrors.<key>`，不靠运行时渲染树反推。
 */
describe('FIELD_SLOT_KEYS 与模板中的展示位一致', () => {
  test('声明的键集合 = 模板里 fieldErrors.<key> 的键集合（多一个少一个都红）', () => {
    const sources = [dialogPath, RuleSectionPath]
      .map(path => readFileSync(path, 'utf8'))
      .join('\n')
    const used = new Set(
      [...sources.matchAll(/fieldErrors\.(\w+)/g)].map(match => match[1]!)
    )
    // 脚本里读写整个对象是 `fieldErrors.value`，不是某个字段的展示位。
    used.delete('value')

    expect([...FIELD_SLOT_KEYS].sort()).toEqual([...used].sort())
  })
})

/**
 * 长 hint 的落位锁：`UFormField` 的 `#hint` 插槽会把文案排进 label 同一行，
 * 257px 宽的 grid 列里「公开说明（中文）」被挤成三行。公开说明现在整块是只读预览，
 * 不再有 hint 节点；slug 那处仍要保证「移出 label 但可见」。
 */
describe('长 hint 不再挤在 label 行', () => {
  const SUMMARY_HINT = 'admin.badges.form.requirementSummaryHint'

  test('公开说明是只读预览：既没有 hint 节点，也没有把它塞进 label', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      expect(wrapper.find('[data-requirement-summary-hint]').exists()).toBe(false)
      expect(wrapper.find('[data-rule-summary-hint]').exists()).toBe(false)
      // 标题仍在（只读块用「公开说明（中文/英文）」做标题）。
      expect(wrapper.text()).toContain('admin.badges.form.requirementSummaryZh')
      expect(wrapper.find('label [data-field-hint]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('slug 的提示移出 label 但仍可见（迁移不是删除）', async () => {
    const wrapper = await mountOpen(badgeRow({ rule: validRule }))
    try {
      const slugField = wrapper.findAllComponents(UFormFieldStub)
        .find(component => component.props('label') === 'admin.badges.form.slug')
      expect(slugField?.find('label').text()).toBe('admin.badges.form.slug')
      expect(slugField?.text()).not.toContain('admin.badges.form.slugHint')

      const hint = wrapper.find('[data-field-hint-for="slug"]')
      expect(hint.exists()).toBe(true)
      expect(hint.text()).toBe('admin.badges.form.slugHint')
      expect(wrapper.find('label [data-field-hint-for="slug"]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})
