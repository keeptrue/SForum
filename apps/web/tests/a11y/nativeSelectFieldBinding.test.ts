import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { inputIdInjectionKey } from '@nuxt/ui/composables/useFormField'
import { flushPromises, mount } from '../helpers/vueSfc'
import { ref } from 'vue'

/**
 * SFNativeSelect（UX2-08）：把 UFormField 注入的字段 id 落到原生 select 上。
 *
 * UFormField 的 `<label for>` 指向它自己 `useId()` 生成的 id；只有消费
 * `inputIdInjectionKey` 的控件才能被 label 命中。原生 select 不消费，
 * 所以读屏里没有名字、点标签也不聚焦。这个用例直接注入一个字段 id，
 * 断言 select 的 id 与 label 的 for 指向同一个值。
 */

const componentPath = fileURLToPath(
  new URL('../../app/components/forms/SFNativeSelect.vue', import.meta.url)
)
const NativeSelect = await compileSelect()

async function compileSelect() {
  const { compileVueSfc } = await import('../helpers/vueSfc')
  return compileVueSfc(componentPath, 'sf-native-select', { inputIdInjectionKey })
}

function mountWithField(fieldId: string | null, props: Record<string, unknown> = {}, attrs: Record<string, unknown> = {}) {
  const provide = fieldId === null ? {} : { [inputIdInjectionKey]: ref(fieldId) }
  return mount(NativeSelect, {
    attachTo: document.body,
    props,
    attrs,
    slots: { default: '<option value="a">A</option><option value="b">B</option>' },
    global: { provide }
  })
}

describe('SFNativeSelect 与 UFormField 的 label 绑定', () => {
  test('select 的 id 等于 UFormField 注入的字段 id（label 的 for 因此命中）', async () => {
    const wrapper = mountWithField('form-field-7')
    await flushPromises()

    const select = wrapper.element as HTMLSelectElement
    expect(select.tagName).toBe('SELECT')
    expect(select.id).toBe('form-field-7')

    wrapper.unmount()
  })

  test('调用点显式给的 id 优先，并回写到注入值（label 的 for 跟着改）', async () => {
    const injected = ref('form-field-7')
    const wrapper = mount(NativeSelect, {
      attachTo: document.body,
      attrs: { id: 'explicit-id' },
      slots: { default: '<option value="a">A</option>' },
      global: { provide: { [inputIdInjectionKey]: injected } }
    })
    await flushPromises()

    expect((wrapper.element as HTMLSelectElement).id).toBe('explicit-id')
    // 关键：UFormField 的 <label for> 读的是注入 ref，必须一起改，否则 label 重新失联
    expect(injected.value).toBe('explicit-id')

    wrapper.unmount()
  })

  test('没有 UFormField 包裹时退化成自生成 id，不会渲染成无 id 的 select', async () => {
    const wrapper = mountWithField(null)
    await flushPromises()

    expect((wrapper.element as HTMLSelectElement).id.length).toBeGreaterThan(0)

    wrapper.unmount()
  })

  test('v-model 双向绑定照常工作（change 事件写回）', async () => {
    const wrapper = mountWithField('f', { modelValue: 'a' })
    await flushPromises()

    const select = wrapper.element as HTMLSelectElement
    expect(select.value).toBe('a')

    select.value = 'b'
    select.dispatchEvent(new Event('change'))
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')).toEqual([['b']])

    wrapper.unmount()
  })

  test('v-model.number 会把回写值转成数字（避免类型从 number 漂成 string）', async () => {
    const wrapper = mount(NativeSelect, {
      attachTo: document.body,
      props: { modelValue: 1 },
      attrs: { modelModifiers: { number: true } },
      slots: { default: '<option value="1">1</option><option value="2">2</option>' },
      global: { provide: { [inputIdInjectionKey]: ref('f') } }
    })
    await flushPromises()

    const select = wrapper.element as HTMLSelectElement
    select.value = '2'
    select.dispatchEvent(new Event('change'))
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')).toEqual([[2]])
    // modelModifiers 不该作为 DOM 属性漏到 select 上
    expect(select.getAttribute('modelmodifiers')).toBeNull()

    wrapper.unmount()
  })
})
