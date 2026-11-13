import { describe, expect, test } from 'bun:test'
import { computed, defineComponent, nextTick, onMounted, ref, watch } from 'vue'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount } from '../helpers/vueSfc'
import {
  TIER_MIN_DARK_CONTRAST,
  isTierCustomColor,
  normalizeTierColor,
  normalizeTierColorInput,
  tierAccentContrast,
  tierColorVar
} from '~/utils/theme/tierColor'

/**
 * 徽章等级配色字段的写入口径（2026-11-13 起）：
 * 1. 取色器 `input` 即时写入表单 —— 拖动过程中色块 / hex 框 / 对比度提示 / 图标强调色要一起变，
 *    只在 `change` 写入会让整个弹窗停在旧色上；同值必须短路，否则每帧都在重刷父组件。
 * 2. 取色器显示的是**有效色**：token 走 CSS 变量运行时读取（不新增第二份 JS 色表），
 *    「按位置自动」时显示调用方给的位次默认色，而不是写死的 slate。
 */
const fieldPath = fileURLToPath(
  new URL('../../app/components/admin/shared/SFAdminTierColorField.vue', import.meta.url)
)
const dialogPath = fileURLToPath(
  new URL('../../app/components/admin/badges/SFAdminBadgeTierDialog.vue', import.meta.url)
)

const UIconStub = defineComponent({ name: 'UIcon', props: ['name'], template: '<span />' })
const UInputStub = defineComponent({
  name: 'UInput',
  props: ['modelValue', 'ariaLabel', 'placeholder', 'size', 'disabled'],
  emits: ['update:modelValue'],
  template: '<input data-testid="hex-input" :value="modelValue">'
})
const UButtonStub = defineComponent({
  name: 'UButton',
  props: ['color', 'variant', 'size', 'disabled'],
  template: '<button type="button"><slot /></button>'
})

const globalComponents = { UIcon: UIconStub, UInput: UInputStub, UButton: UButtonStub }

// 该 SFC 依赖 Nuxt 自动导入的 vue API，编译时 import 被剥掉，只能靠注入补回。
const SFAdminTierColorField = await compileVueSfc(fieldPath, 'sf-admin-tier-color-field', {
  computed,
  ref,
  watch,
  onMounted,
  TIER_MIN_DARK_CONTRAST,
  isTierCustomColor,
  normalizeTierColor,
  normalizeTierColorInput,
  tierAccentContrast,
  tierColorVar
})

const CHOICES = ['slate', 'teal', 'emerald', 'sky', 'indigo', 'violet', 'fuchsia', 'rose', 'amber']

function mountField(overrides: Record<string, unknown> = {}) {
  return mount(SFAdminTierColorField, {
    props: {
      modelValue: '',
      choices: CHOICES,
      label: '配色',
      autoLabel: '按位置自动',
      pickerLabel: '取色器',
      hexLabel: '色值',
      applyLabel: '应用',
      invalidLabel: '请输入 #rrggbb 形式的色值。',
      lowContrastLabel: '该色在深色页面对比度约 {ratio}:1',
      ...overrides
    },
    global: { components: globalComponents }
  })
}

/**
 * 把 `--sf-tl-<token>-light` 的读取替换成固定表：happy-dom 不加载站点样式表，
 * 不打这根桩就永远走兜底分支，测不到「token → 字面量 hex」这条路径。
 */
function stubTierCssVars(vars: Record<string, string>) {
  const original = globalThis.getComputedStyle
  globalThis.getComputedStyle = (() => ({
    getPropertyValue: (name: string) => vars[name] ?? ''
  })) as unknown as typeof globalThis.getComputedStyle
  return () => {
    globalThis.getComputedStyle = original
  }
}

describe('badge tier color field · 取色器即时写入', () => {
  test('input 立即下发归一后的 hex，change 同值时不再重发', async () => {
    const wrapper = mountField({ modelValue: 'amber' })
    const picker = wrapper.find('input[type="color"]')
    const element = picker.element as HTMLInputElement

    element.value = '#CEAF40'
    await picker.trigger('input')
    expect(wrapper.emitted('update:modelValue')).toEqual([['#ceaf40']])

    // 模型跟上之后，change（关闭系统取色器）不该再发一次同样的值。
    await wrapper.setProps({ modelValue: '#ceaf40' })
    element.value = '#ceaf40'
    await picker.trigger('change')
    expect(wrapper.emitted('update:modelValue')?.length).toBe(1)
  })

  test('禁用的字段不写表单', async () => {
    const wrapper = mountField({ disabled: true })
    const picker = wrapper.find('input[type="color"]')
    ;(picker.element as HTMLInputElement).value = '#ceaf40'
    await picker.trigger('input')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  test('非 hex 形态的输入被丢弃', async () => {
    const wrapper = mountField()
    const picker = wrapper.find('input[type="color"]')
    // 真实取色器不会给非法值，这条守的是「上游给什么都别写进表单」。
    Object.defineProperty(picker.element, 'value', { value: 'red', configurable: true })
    await picker.trigger('input')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  test('拖动过程中 hex 草稿跟着刷新', async () => {
    const wrapper = mountField()
    const picker = wrapper.find('input[type="color"]')
    ;(picker.element as HTMLInputElement).value = '#ceaf40'
    await picker.trigger('input')
    // 测试里父级不会回写模型，手动同步 props 后草稿应由 watch 回灌。
    await wrapper.setProps({ modelValue: '#ceaf40' })
    await nextTick()
    expect((wrapper.find('[data-testid="hex-input"]').element as HTMLInputElement).value).toBe('#ceaf40')
  })
})

describe('badge tier color field · 取色块显示有效色', () => {
  test('token 值从 CSS 变量读浅色字面量', async () => {
    const restore = stubTierCssVars({ '--sf-tl-amber-light': '#92400e' })
    try {
      const wrapper = mountField({ modelValue: 'amber' })
      await flushPromises()
      expect((wrapper.find('input[type="color"]').element as HTMLInputElement).value).toBe('#92400e')
    } finally {
      restore()
    }
  })

  test('「按位置自动」显示调用方给的位次默认色', async () => {
    const restore = stubTierCssVars({ '--sf-tl-violet-light': '#7c3aed' })
    try {
      const wrapper = mountField({ modelValue: '', defaultColor: 'violet' })
      await flushPromises()
      expect((wrapper.find('input[type="color"]').element as HTMLInputElement).value).toBe('#7c3aed')
    } finally {
      restore()
    }
  })

  test('自定义色直接显示自身，无 CSS 时回落到兜底色而不是空值', async () => {
    const custom = mountField({ modelValue: '#ceaf40' })
    await flushPromises()
    expect((custom.find('input[type="color"]').element as HTMLInputElement).value).toBe('#ceaf40')

    // 默认色本身是 hex（调用方可能这么传）时也要显示出来，不能只看 token。
    const hexDefault = mountField({ modelValue: '', defaultColor: '#112233' })
    await flushPromises()
    expect((hexDefault.find('input[type="color"]').element as HTMLInputElement).value).toBe('#112233')

    const restore = stubTierCssVars({})
    try {
      const fallback = mountField({ modelValue: 'amber' })
      await flushPromises()
      expect((fallback.find('input[type="color"]').element as HTMLInputElement).value).toBe('#475569')
    } finally {
      restore()
    }
  })
})

describe('badge tier dialog 接线', () => {
  const dialogSource = readFileSync(dialogPath, 'utf8')

  test('位次默认色只算一次，颜色字段与图标强调色共用', () => {
    expect(dialogSource).toContain('const positionDefault = computed(() => defaultTierColorName(')
    expect(dialogSource).toContain('return form.color || positionDefault.value')
    expect(dialogSource).toContain(':default-color="positionDefault"')
    expect(dialogSource).toContain(':accent="tierColorVar(resolvedColor())"')
  })
})
