import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, ref } from 'vue'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'
import type { TrustLevelImpactTierResult } from '../../../app/composables/admin/forum/trustLevels/useTrustLevelRuleImpact'

/**
 * 影响面弹窗的三态契约（copy-spec §3.3 / ui-spec §2）。
 *
 * 本文件只测「渲染有没有走错分支」，不测估算算法 —— 那是后端聚合 SQL 的事。
 * 最容易写错的一条在这里重复一遍：**`estimated: 0` 必须走有值态**，
 * 任何 `estimated ? … : …` 都会把 0 渲染成「无法估算」。
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

const UModalStub = defineComponent({
  name: 'UModal',
  props: { open: Boolean },
  template: '<div v-if="open" data-modal><slot name="content" /></div>'
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean },
  emits: ['click'],
  template: '<button type="button" :disabled="disabled" v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>'
})
const UIconStub = defineComponent({ name: 'UIcon', props: { name: String }, template: '<i />' })

const dialogPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelRuleImpactDialog.vue', import.meta.url)
)

const Dialog = await compileVueSfc(dialogPath, 'sf-tl-rule-impact-dialog', {
  computed,
  ref,
  useI18n: () => ({ t, te: (key: string) => typeof lookup(key) === 'string', locale: ref('zh-CN') }),
  trustLevelName: (tier: { name?: { zh?: string, en?: string } }) => tier.name?.zh ?? ''
})

const tiers = [{ id: 'member', name: { zh: '正式用户', en: 'Member' }, icon: null, color: null, requirements: {}, limits: null, earnable: [], retired: false, rule: null }]

const result = (patch: Partial<TrustLevelImpactTierResult> = {}): TrustLevelImpactTierResult => ({
  tierId: 'member',
  estimated: 128,
  method: 'strictest_leaf',
  partial: false,
  unsupportedConditions: [],
  ...patch
})

function mountDialog(status: string, results: TrustLevelImpactTierResult[], catalog: unknown[] = []) {
  return mount(Dialog, {
    props: { open: true, status, results, tiers, catalog },
    global: { components: { UModal: UModalStub, UButton: UButtonStub, UIcon: UIconStub } }
  })
}

describe('影响面弹窗 · 三态互斥', () => {
  test('CP-1 有值（含 0）走 A 态：显示 ≈ 0 人，且不出现「无法估算」', async () => {
    const wrapper = mountDialog('ready', [result({ estimated: 0, method: 'single_leaf' })])
    try {
      await flushPromises()
      const estimate = wrapper.find('[data-sf-tl-impact-estimate]')
      expect(estimate.exists()).toBe(true)
      expect(estimate.text()).toBe(t('admin.forum.trustLevels.conditions.impact.estimate', { count: 0 }))
      expect(wrapper.text()).not.toContain(t('admin.forum.trustLevels.conditions.impact.unestimable'))
      expect(wrapper.find('[data-sf-tl-impact-unestimable]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('CP-2 `estimated: null` 走 B 态：全屏无 ≈、无数字，且不渲染 partial 行', async () => {
    const wrapper = mountDialog('ready', [result({ estimated: null, partial: true, unsupportedConditions: ['fact.count'] })])
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-impact-unestimable]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-impact-estimate]').exists()).toBe(false)
      expect(wrapper.text()).not.toContain('≈')
      // 「无法估算」与「只算了部分条件」互斥：同时出现等于互相否定。
      expect(wrapper.find('[data-sf-tl-impact-partial]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('CP-3 partial 是 A 的追加行：数字与「上界」句同时在', async () => {
    const wrapper = mountDialog('ready', [result({ estimated: 64, partial: true })])
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-impact-estimate]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-impact-partial]').text())
        .toBe(t('admin.forum.trustLevels.conditions.impact.partialHint'))
    } finally {
      wrapper.unmount()
    }
  })

  test('CP-5 数字只出现一次：单档时 ≈ 恰好 1 次，且说明句零数字', async () => {
    const wrapper = mountDialog('ready', [result()])
    try {
      await flushPromises()
      expect((wrapper.text().match(/≈/g) ?? []).length).toBe(1)
      const meaning = wrapper.find('[data-sf-tl-impact-meaning]').text()
      expect(meaning).not.toMatch(/\d/)
    } finally {
      wrapper.unmount()
    }
  })

  test('多档各有规则时逐档一个数字（Q-15 的单档判据只在单档夹具上成立）', async () => {
    const wrapper = mountDialog('ready', [
      result({ tierId: 'member', estimated: 12 }),
      result({ tierId: 'resident', estimated: 3 })
    ])
    try {
      await flushPromises()
      expect((wrapper.text().match(/≈/g) ?? []).length).toBe(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('CP-6 无内部词：不出现 cmp / params / children / AST', async () => {
    const wrapper = mountDialog('ready', [result({ estimated: null, unsupportedConditions: ['fact.count'] })])
    try {
      await flushPromises()
      const text = wrapper.text()
      for (const word of ['cmp', 'params', 'children', 'AST', 'ruleengine']) expect(text).not.toContain(word)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('影响面弹窗 · 不可估算条件清单（>5 条收口）', () => {
  test('Q-17 默认 5 条 + 「还有 N 条」，展开后渲染全量', async () => {
    const keys = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h']
    const wrapper = mountDialog('ready', [result({ estimated: null, unsupportedConditions: keys })])
    try {
      await flushPromises()
      expect(wrapper.findAll('[data-sf-tl-impact-unsupported] li')).toHaveLength(5)
      const more = wrapper.find('[data-sf-tl-impact-unsupported-more]')
      expect(more.attributes('aria-expanded')).toBe('false')
      expect(more.text()).toBe(t('admin.forum.trustLevels.conditions.impact.unsupportedMore', { count: 3 }))

      await more.trigger('click')
      expect(wrapper.findAll('[data-sf-tl-impact-unsupported] li')).toHaveLength(8)
      expect(wrapper.find('[data-sf-tl-impact-unsupported-more]').attributes('aria-expanded')).toBe('true')
    } finally {
      wrapper.unmount()
    }
  })

  test('条件名优先取目录文案，查不到时回落原始 key（后台允许显示 key）', async () => {
    const wrapper = mountDialog(
      'ready',
      [result({ estimated: null, unsupportedConditions: ['fact.count', 'object.engagement'] })],
      [{ key: 'fact.count', valueKind: 'count', compare: [], labelZh: '插件事实计数', labelEn: 'Plugin fact count' }]
    )
    try {
      await flushPromises()
      const items = wrapper.findAll('[data-sf-tl-impact-unsupported] li').map(item => item.text())
      expect(items).toEqual(['插件事实计数', 'object.engagement'])
    } finally {
      wrapper.unmount()
    }
  })
})

describe('影响面弹窗 · 加载与失败', () => {
  test('加载中：只出「正在估算」，确认按钮禁用（还没数就确认）', async () => {
    const wrapper = mountDialog('loading', [])
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-impact-loading]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-impact-confirm]').attributes('disabled')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('估算失败：仍可保存，按钮措辞变成「仍然保存」', async () => {
    const wrapper = mountDialog('error', [])
    try {
      await flushPromises()
      expect(wrapper.find('[data-sf-tl-impact-load-failed]').exists()).toBe(true)
      expect(wrapper.find('[data-sf-tl-impact-confirm]').text())
        .toBe(t('admin.forum.trustLevels.conditions.impact.confirmAnyway'))
    } finally {
      wrapper.unmount()
    }
  })

  test('确认 / 取消各发一次事件，弹窗自己不写库', async () => {
    const confirmWrapper = mountDialog('ready', [result()])
    const cancelWrapper = mountDialog('ready', [result()])
    try {
      await flushPromises()
      await confirmWrapper.find('[data-sf-tl-impact-confirm]').trigger('click')
      await cancelWrapper.find('[data-sf-tl-impact-cancel]').trigger('click')
      expect(confirmWrapper.emitted('confirm')).toHaveLength(1)
      expect(cancelWrapper.emitted('cancel')).toHaveLength(1)
    } finally {
      confirmWrapper.unmount()
      cancelWrapper.unmount()
    }
  })

  test('文案不含升降承诺（Q1）：不出现「受影响」「将升级」「将降级」', async () => {
    const wrapper = mountDialog('ready', [result()])
    try {
      await flushPromises()
      const text = wrapper.text()
      for (const phrase of ['受影响', '将升级', '将降级']) expect(text).not.toContain(phrase)
    } finally {
      wrapper.unmount()
    }
  })
})
