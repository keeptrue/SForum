import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, ref } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 影响面确认弹窗的呈现契约（2-3）。
 *
 * 三种呈现必须**互斥且能看出来**：
 * - 有读数（**包括 0**）→「≈ N 人」+ 依据句，不得出现等号/精确口气；
 * - `estimated === null` →「无法估算」+ 逐个列出不可估的条件，**不得**出现「≈」；
 * - `partial === true` → 追加「只算了部分条件，是上界」。
 *
 * 0 与 null 是两件事：0 是「算出来是零人」，null 是「算不出来」。把 null 当 0 渲染
 * 会凭空造出一个「≈ 0 人」的假结论，所以两条断言各自反过来查对方的关键词。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const UModalStub = defineComponent({
  name: 'UModal',
  props: { open: Boolean },
  template: '<div><slot name="content" /></div>'
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: { disabled: Boolean, loading: Boolean, color: String, variant: String },
  emits: ['click'],
  template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>'
})

const dialogPath = fileURLToPath(
  new URL('../../app/components/admin/badges/rule/SFAdminBadgeRuleImpactDialog.vue', import.meta.url)
)
const ImpactDialog = await compileVueSfc(dialogPath, 'badge-rule-impact-dialog', {
  computed,
  useI18n: () => ({ t, locale: ref('zh-CN') })
})

type Impact = {
  estimated: number | null
  method: string
  partial: boolean
  unsupportedConditions: string[]
}

function impact(overrides: Partial<Impact> = {}): Impact {
  return {
    estimated: 12,
    method: 'single_leaf',
    partial: false,
    unsupportedConditions: [],
    ...overrides
  }
}

function mountDialog(value: Impact | null, options: {
  status?: 'idle' | 'loading' | 'error' | 'ready'
  ruleTouched?: boolean
  catalog?: Array<Record<string, unknown>>
} = {}) {
  return mount(ImpactDialog, {
    props: {
      impact: value,
      status: options.status ?? 'ready',
      ruleTouched: options.ruleTouched ?? false,
      catalog: options.catalog ?? [],
      open: true
    },
    global: { stubs: { UModal: UModalStub, UButton: UButtonStub } }
  })
}

describe('SFAdminBadgeRuleImpactDialog · 三种呈现', () => {
  test('estimated 0 renders as ≈ 0 instead of "cannot estimate"', () => {
    const wrapper = mountDialog(impact({ estimated: 0 }))
    try {
      expect(wrapper.get('[data-impact-estimate]').text()).toBe('≈ 0 人')
      expect(wrapper.text()).not.toContain('无法估算')
      expect(wrapper.find('[data-impact-unestimable]').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })

  test('a readable estimate never claims precision (no equals sign, no "exact" wording)', () => {
    const wrapper = mountDialog(impact({ estimated: 128 }))
    try {
      const text = wrapper.get('[data-rule-impact]').text()
      expect(text).toContain('≈ 128 人')
      expect(text).not.toContain('=')
      expect(text).not.toMatch(/精确|正好|恰好|exactly|precisely/i)
    } finally {
      wrapper.unmount()
    }
  })

  test('estimated null says it cannot estimate, lists every unsupported key and never prints ≈', () => {
    const wrapper = mountDialog(impact({
      estimated: null,
      method: 'unsupported_condition',
      unsupportedConditions: ['object.authoring', 'plugin.unknown_fact']
    }), {
      // 目录里只有第一条：查得到的用本地化名字，查不到的必须回退原始 key，不能静默丢。
      catalog: [{ key: 'object.authoring', labelZh: '发布主题', labelEn: 'Authoring' }]
    })
    try {
      const text = wrapper.get('[data-rule-impact]').text()
      expect(text).toContain('无法估算')
      expect(text).not.toContain('≈')
      expect(wrapper.find('[data-impact-estimate]').exists()).toBe(false)

      const list = wrapper.get('[data-impact-unsupported]')
      expect(list.text()).toContain('发布主题')
      expect(list.text()).toContain('plugin.unknown_fact')
      expect(list.findAll('li')).toHaveLength(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('partial true adds the upper-bound sentence, partial false does not', () => {
    const partial = mountDialog(impact({ partial: true }))
    try {
      expect(partial.get('[data-impact-partial]').text()).toContain('上界')
      expect(partial.get('[data-impact-partial]').text()).toContain('部分条件')
    } finally {
      partial.unmount()
    }

    const complete = mountDialog(impact({ partial: false }))
    try {
      expect(complete.find('[data-impact-partial]').exists()).toBe(false)
      expect(complete.text()).not.toContain('上界')
    } finally {
      complete.unmount()
    }
  })

  test('each method gets its own explicit sentence, and an unknown method falls back to 无法估算的措辞', () => {
    const methods = [
      'single_leaf',
      'strictest_leaf',
      'strictest_leaf_partial',
      'sum_of_leaves',
      'complement_of_leaf',
      'unsupported_condition'
    ]
    for (const method of methods) {
      const wrapper = mountDialog(impact({ estimated: 5, method }))
      try {
        const expected = t(`admin.badges.rule.impact.method.${method}`)
        expect(wrapper.get('[data-impact-method]').text()).toBe(expected)
        expect(wrapper.text()).not.toContain('admin.badges.rule.impact.method')
      } finally {
        wrapper.unmount()
      }
    }

    const unknown = mountDialog(impact({ estimated: 5, method: 'brand_new_method' }))
    try {
      expect(unknown.get('[data-impact-method]').text())
        .toBe(t('admin.badges.rule.impact.method.unsupported_condition'))
    } finally {
      unknown.unmount()
    }
  })

  test('the scope line always says the estimate comes from the saved rule', () => {
    const wrapper = mountDialog(impact())
    try {
      expect(wrapper.get('[data-impact-scope]').text()).toContain('按当前已保存的规则估算')
    } finally {
      wrapper.unmount()
    }
  })

  test('a touched rule adds the "re-evaluated in the background" line', () => {
    const touched = mountDialog(impact(), { ruleTouched: true })
    try {
      expect(touched.get('[data-impact-rule-changed]').text()).toContain('后台重新评估')
    } finally {
      touched.unmount()
    }

    const untouched = mountDialog(impact())
    try {
      expect(untouched.find('[data-impact-rule-changed]').exists()).toBe(false)
    } finally {
      untouched.unmount()
    }
  })

  test('a failed estimate is visible and still lets the operator save', async () => {
    const wrapper = mountDialog(null, { status: 'error' })
    try {
      expect(wrapper.get('[data-impact-failed]').text()).toBe(t('admin.badges.rule.impact.loadFailed'))
      const confirm = wrapper.get('[data-impact-confirm]')
      expect(confirm.text()).toBe(t('admin.badges.rule.impact.confirmAnyway'))
      expect(confirm.attributes('disabled')).toBeUndefined()

      await confirm.trigger('click')
      expect(wrapper.emitted('confirm')).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('cancel emits cancel so the host can skip the PATCH', async () => {
    const wrapper = mountDialog(impact())
    try {
      await wrapper.get('[data-impact-cancel]').trigger('click')
      expect(wrapper.emitted('cancel')).toHaveLength(1)
      expect(wrapper.emitted('confirm')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })
})
