import { describe, expect, test } from 'bun:test'
import { compileVueSfc, mount } from '../helpers/vueSfc'
import type { ProfileSettingsField } from '../../app/utils/settings/profileSettings'

/**
 * SFDeclarativeSettingsField：中性声明式字段渲染器。
 * 这里是挂载真实 SFC 的行为验证（不是源码字符串断言）：
 * 5 种字段类型、必填标记、描述与错误的 aria-describedby 关联。
 */
const fieldPath = new URL('../../app/components/settings/shared/SFDeclarativeSettingsField.vue', import.meta.url).pathname
const DeclarativeSettingsField = await compileVueSfc(fieldPath, 'declarative-settings-field')

function base(overrides: Partial<ProfileSettingsField> = {}): ProfileSettingsField {
  return { fieldKey: 'demo.city', label: '城市', type: 'string', ...overrides }
}

function mountField(props: Record<string, unknown>) {
  return mount(DeclarativeSettingsField, { props: props as never })
}

describe('SFDeclarativeSettingsField rendering', () => {
  test('string / text / number / boolean / select each pick their own control', () => {
    const stringField = mountField({ field: base(), modelValue: '上海' })
    const input = stringField.find('input')
    expect(input.attributes('type')).toBe('text')
    expect((input.element as HTMLInputElement).value).toBe('上海')

    const textField = mountField({ field: base({ type: 'text' }), modelValue: '一段简介' })
    expect(textField.find('textarea').exists()).toBe(true)
    expect((textField.find('textarea').element as HTMLTextAreaElement).value).toBe('一段简介')

    const numberField = mountField({ field: base({ type: 'number', constraints: { min: 1, max: 9 } }), modelValue: 3 })
    const numberInput = numberField.find('input')
    expect(numberInput.attributes('type')).toBe('number')
    expect(numberInput.attributes('min')).toBe('1')
    expect(numberInput.attributes('max')).toBe('9')

    const booleanField = mountField({ field: base({ type: 'boolean', description: '接收私信' }), modelValue: true })
    const checkbox = booleanField.find('input[type="checkbox"]')
    expect(checkbox.exists()).toBe(true)
    expect((checkbox.element as HTMLInputElement).checked).toBe(true)

    const selectField = mountField({
      field: base({ type: 'select', placeholder: '请选择', options: [{ value: 'a', label: '选项 A' }, { value: 'b', label: '选项 B' }] }),
      modelValue: 'b'
    })
    const select = selectField.find('select')
    expect(select.exists()).toBe(true)
    expect(select.findAll('option')).toHaveLength(3)
    expect((select.element as HTMLSelectElement).value).toBe('b')
  })

  test('label is bound to the control, required is visible plus announced', () => {
    const wrapper = mountField({ field: base({ required: true }), modelValue: '', requiredLabel: '必填' })
    const label = wrapper.find('label')
    const input = wrapper.find('input')
    expect(label.attributes('for')).toBe(input.attributes('id'))
    expect(wrapper.text()).toContain('*')
    expect(wrapper.text()).toContain('必填')
    expect(wrapper.find('.sr-only').exists()).toBe(true)
  })

  test('description and error both land in aria-describedby', () => {
    const wrapper = mountField({
      field: base({ description: '最多 20 字', constraints: { maxLength: 20 } }),
      modelValue: '',
      errors: ['不能包含链接', '最多 20 字']
    })
    const input = wrapper.find('input')
    const describedBy = input.attributes('aria-describedby') || ''
    const ids = describedBy.split(' ')
    expect(ids).toHaveLength(2)
    expect(input.attributes('aria-invalid')).toBe('true')
    expect(input.attributes('maxlength')).toBe('20')
    const error = wrapper.find('[role="alert"]')
    expect(error.exists()).toBe(true)
    expect(error.attributes('id')).toBe(ids[1])
    expect(error.text()).toBe('不能包含链接；最多 20 字')
  })

  test('no error means no aria-invalid and no alert node', () => {
    const wrapper = mountField({ field: base({ description: '说明' }), modelValue: '' })
    const input = wrapper.find('input')
    expect(input.attributes('aria-invalid')).toBeUndefined()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(input.attributes('aria-describedby')).toBe(wrapper.find('p').attributes('id'))
  })

  test('emits typed values: numbers coerce, empty number clears to an empty draft', async () => {
    const wrapper = mountField({ field: base({ type: 'number' }), modelValue: '' })
    const input = wrapper.find('input')
    await input.setValue('42')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([42])
    await input.setValue('')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([''])
    expect(wrapper.emitted('change')).toHaveLength(2)
  })

  test('boolean and select emit their raw values', async () => {
    const booleanField = mountField({ field: base({ type: 'boolean' }), modelValue: false })
    await booleanField.find('input[type="checkbox"]').setValue(true)
    expect(booleanField.emitted('update:modelValue')?.[0]).toEqual([true])

    const selectField = mountField({
      field: base({ type: 'select', options: [{ value: 'a', label: 'A' }] }),
      modelValue: ''
    })
    await selectField.find('select').setValue('a')
    expect(selectField.emitted('update:modelValue')?.[0]).toEqual(['a'])
  })

  test('disabled stops the controls from accepting input', () => {
    const wrapper = mountField({ field: base(), modelValue: '上海', disabled: true })
    expect(wrapper.find('input').attributes('disabled')).toBeDefined()
  })
})
