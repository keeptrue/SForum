import { describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'
import { useI18nFor } from '../helpers/locale'

// 组件内部用自动导入的 computed/ref/useI18n：显式注入，别依赖别的测试文件
// `Object.assign(globalThis, ...)` 泄漏出来的全局（单独跑这个文件就会红）。
Object.assign(globalThis, { ref: testVue.ref, computed: testVue.computed })

const componentPath = fileURLToPath(
  new URL('../../app/components/SFSearch.vue', import.meta.url)
)
const SFSearch = await compileVueSfc(componentPath, 'sf-search-submit', {
  computed: testVue.computed,
  ref: testVue.ref,
  useI18n: useI18nFor('zh-CN')
})

describe('SFSearch submission', () => {
  test('submits the current trimmed input through Enter and the accessible command', async () => {
    const wrapper = mount(SFSearch, {
      props: {
        modelValue: '',
        ariaLabel: 'Search forum'
      },
      global: {
        stubs: {
          UIcon: true
        }
      }
    })

    try {
      const input = wrapper.get('input[type="search"]')
      await input.setValue('  launch  ')

      expect(wrapper.emitted('update:modelValue')).toEqual([['  launch  ']])

      await input.trigger('keydown', { key: 'Enter', isComposing: true })
      expect(wrapper.emitted('submit')).toBeUndefined()

      await input.trigger('keydown', { key: 'Enter' })
      expect(wrapper.emitted('submit')).toEqual([['launch']])

      const submit = wrapper.get('button[type="button"]')
      expect(submit.attributes('aria-label')).toBe('Search forum')
      expect(submit.attributes('title')).toBe('Search forum')

      await submit.trigger('click')
      expect(wrapper.emitted('submit')).toEqual([['launch'], ['launch']])
    } finally {
      wrapper.unmount()
    }
  })
})
