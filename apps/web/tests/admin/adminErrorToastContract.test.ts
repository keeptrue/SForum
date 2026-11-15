import { afterAll, describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'

/**
 * RV-20：错误 toast 必须常驻。
 *
 * 契约来源 AGENTS.md：非错误提示 5 秒自动消失，错误提示必须留在屏幕上直到用户处理。
 * Nuxt UI 的 `duration: 0` 表示不自动关闭；省略该字段会退回全局默认（5s）。
 *
 * 这里同时钉两条：
 * 1. **行为**：真实组件在失败分支上产出的 toast 载荷带 `duration: 0`，成功分支仍是 5000；
 * 2. **棘轮**：`app/` 下所有 `toast.add({ color: 'error' ... })` 必须同对象带 `duration: 0`，
 *    防止新代码再写回默认 5 秒。
 */

const APP_ROOT = fileURLToPath(new URL('../../app', import.meta.url))

/**
 * 豁免名单：**必须为空**。
 *
 * 加条目要写清三件事：为什么这条 toast 不该常驻、为什么它不是错误态、以及什么条件下删掉。
 * 空白豁免是这份棘轮唯一可接受的状态 —— 「漏写 duration」不是理由。
 */
const ALLOWLIST: Array<{ file: string, reason: string }> = []

type ToastCall = { file: string, line: number, hasDuration0: boolean, isError: boolean }

/**
 * 扫描一段源码里的 `toast.add` 调用。
 *
 * 两处容易写成假绿的细节（变异 M1b/M1c 实测过）：
 * - `toast?.add(`（可选链）—— 只匹配 `toast\.add(` 会整条漏掉；
 * - `color: "error"`（双引号）—— 只匹配单引号会把错误态判成非错误态。
 * 所以两个正则都放宽，并用下面的「合成源码」用例把它们钉住。
 */
function scanToastSource(text: string, file = 'synthetic.ts'): ToastCall[] {
  const calls: ToastCall[] = []
  const matcher = /toast\??\.add\(/g
  let match: RegExpExecArray | null
  while ((match = matcher.exec(text))) {
    // 括号配平：toast.add({...}) / toast.add({ ... }, extra) 都要吃到完整调用。
    const openParen = match.index + match[0].length - 1
    let depth = 0
    let end = -1
    for (let index = openParen; index < text.length; index += 1) {
      const char = text[index]
      if (char === '(' || char === '{' || char === '[') depth += 1
      else if (char === ')' || char === '}' || char === ']') {
        depth -= 1
        if (depth === 0) { end = index; break }
      }
    }
    if (end === -1) continue
    const call = text.slice(match.index, end + 1)
    calls.push({
      file,
      line: text.slice(0, match.index).split('\n').length,
      isError: /color:\s*['"]error['"]/.test(call),
      hasDuration0: /duration:\s*0\b/.test(call)
    })
    matcher.lastIndex = end + 1
  }
  return calls
}

function scanToastCalls(root: string): ToastCall[] {
  const files: string[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      const full = join(dir, entry)
      const stat = statSync(full)
      if (stat.isDirectory()) walk(full)
      else if (/\.(ts|vue)$/.test(entry)) files.push(full)
    }
  }
  walk(root)

  const calls: ToastCall[] = []
  for (const file of files) {
    calls.push(...scanToastSource(readFileSync(file, 'utf8'), relative(root, file)))
  }
  return calls
}

describe('RV-20 错误 toast 常驻契约', () => {
  test('app/ 下每个 color=error 的 toast 都带 duration: 0', () => {
    const calls = scanToastCalls(APP_ROOT)
    const offenders = calls.filter(call => call.isError && !call.hasDuration0)

    // 先证明扫描器真的扫到了东西：全 0 会让下面的断言永远绿。
    expect(calls.filter(call => call.isError).length).toBeGreaterThan(100)
    expect(offenders.map(call => `${call.file}:${call.line}`)).toEqual([])
  })

  test('豁免名单为空，且每条豁免都写得出理由', () => {
    expect(ALLOWLIST).toEqual([])
    for (const entry of ALLOWLIST) {
      expect(entry.reason.trim().length).toBeGreaterThan(20)
    }
  })

  test('扫描器抓得住可选链与双引号写法（M1b / M1c）', () => {
    const optionalChain = scanToastSource(`toast?.add({ color: 'error', title: 'x' })`)
    expect(optionalChain).toHaveLength(1)
    expect(optionalChain[0]?.isError).toBe(true)
    expect(optionalChain[0]?.hasDuration0).toBe(false)

    const doubleQuotes = scanToastSource(`toast.add({ color: "error", title: "x" })`)
    expect(doubleQuotes[0]?.isError).toBe(true)
    expect(doubleQuotes[0]?.hasDuration0).toBe(false)

    // 合规写法与「非错误 + 无 duration」都不能被误判成违规。
    const compliant = scanToastSource(`toast.add({ color: 'error', duration: 0, title: 'x' })`)
    expect(compliant[0]?.hasDuration0).toBe(true)
    const success = scanToastSource(`toast.add({ color: 'success', title: 'ok' })`)
    expect(success[0]?.isError).toBe(false)
  })

  test('非错误 toast 不受影响：成功/提示仍按 5 秒自动消失', () => {
    const calls = scanToastCalls(APP_ROOT)
    const nonError = calls.filter(call => !call.isError)
    // 至少存在大量非错误 toast，且它们没有被批量改成 0（那会违反「非错误 5 秒消失」）。
    expect(nonError.length).toBeGreaterThan(100)
    expect(nonError.filter(call => call.hasDuration0).length).toBe(0)
  })
})

// ---------------------------------------------------------------------------
// 行为证据：真实组件的失败/成功分支各自产出正确的 toast 载荷。
// ---------------------------------------------------------------------------

type ToastPayload = { color?: string, duration?: number, title?: string }

const globals = globalThis as Record<string, unknown>
const originalUseI18n = globals.useI18n
const originalUseToast = globals.useToast

afterAll(() => {
  globals.useI18n = originalUseI18n
  globals.useToast = originalUseToast
})

const toasts: ToastPayload[] = []

const FooterStub = {
  name: 'SFAdminFormFooter',
  props: {
    saving: { type: Boolean, default: false },
    showUnsavedAlert: { type: Boolean, default: false },
    submitText: { type: String, default: '' },
    resetText: { type: String, default: '' },
    resetIcon: { type: String, default: '' }
  },
  emits: ['submit', 'reset'],
  template: `<div class="footer-stub">
    <button type="button" data-testid="footer-submit" @click="$emit('submit')">{{ submitText }}</button>
    <button type="button" data-testid="footer-reset" @click="$emit('reset')">{{ resetText }}</button>
    <slot name="actions" />
  </div>`
}

const CardStub = {
  name: 'UCard',
  template: '<section class="card-stub"><slot name="header" /><slot /><slot name="footer" /></section>'
}

const AlertStub = {
  name: 'UAlert',
  props: { color: { type: String, default: '' }, title: { type: String, default: '' }, variant: { type: String, default: '' }, icon: { type: String, default: '' } },
  template: '<div class="alert-stub" :data-color="color">{{ title }}</div>'
}

const PassthroughStub = { template: '<div><slot /></div>' }
const BadgeStub = { props: ['color', 'variant'], template: '<span><slot /></span>' }
const CheckboxStub = { props: ['modelValue'], template: '<input type="checkbox" />' }
const InputStub = { props: ['modelValue'], template: '<input />' }
const ButtonStub = { props: ['disabled', 'loading'], emits: ['click'], template: '<button type="button" @click="$emit(\'click\')"><slot /></button>' }

const ModerationSettingsForm = await compileVueSfc(
  fileURLToPath(new URL('../../app/components/moderation/ModerationSettingsForm.vue', import.meta.url)),
  'moderation-settings-form',
  {
    useModerationApi: () => apiStub,
    apiErrorMessage: (error: unknown) => (error as { data?: { message?: string } })?.data?.message || '',
    SFAdminFormFooter: FooterStub,
    reactive: testVue.reactive,
    ref: testVue.ref,
    computed: testVue.computed,
    watch: testVue.watch,
    useI18n: () => ({ t: (key: string) => key }),
    useToast: () => ({ add: (payload: ToastPayload) => toasts.push(payload) })
  }
)

let apiStub = {
  updateSettings: async (_input: unknown) => ({}),
  resetSettings: async () => ({})
}

const MODEL = { mode: 'rules', reviewNewUsers: true, newUserMaxAgeDays: 7, reviewExternalLinks: false }

function mountForm() {
  return mount(ModerationSettingsForm, {
    props: { modelValue: MODEL },
    global: {
      stubs: {
        UCard: CardStub,
        UAlert: AlertStub,
        UBadge: BadgeStub,
        UCheckbox: CheckboxStub,
        UInput: InputStub,
        UButton: ButtonStub,
        UFormField: PassthroughStub
      }
    }
  })
}

describe('RV-20 失败分支的 toast 载荷', () => {
  test('保存失败：错误 toast 常驻（duration 0），且字段旁仍有错误提示', async () => {
    toasts.length = 0
    apiStub = {
      updateSettings: async () => {
        throw { statusCode: 500, data: { message: 'moderation.save_failed' } }
      },
      resetSettings: async () => ({})
    }
    const wrapper = mountForm()
    try {
      await wrapper.get('[data-testid="footer-submit"]').trigger('click')
      await flushPromises()

      expect(toasts).toHaveLength(1)
      expect(toasts[0]?.color).toBe('error')
      expect(toasts[0]?.duration).toBe(0)
      // toast 不得替代字段级/页内错误：表单里的 alert 同时要在。
      expect(wrapper.get('.alert-stub').text()).toBe('moderation.save_failed')
    } finally {
      wrapper.unmount()
    }
  })

  test('恢复默认失败：错误 toast 同样常驻', async () => {
    toasts.length = 0
    apiStub = {
      updateSettings: async () => ({}),
      resetSettings: async () => {
        throw { statusCode: 403, data: { message: 'moderation.reset_forbidden' } }
      }
    }
    const wrapper = mountForm()
    try {
      await wrapper.get('[data-testid="footer-reset"]').trigger('click')
      await flushPromises()

      expect(toasts[0]?.color).toBe('error')
      expect(toasts[0]?.duration).toBe(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('成功分支保持 5 秒自动消失（没有被 RV-20 误伤）', async () => {
    toasts.length = 0
    apiStub = {
      updateSettings: async () => ({ ...MODEL }),
      resetSettings: async () => ({ ...MODEL })
    }
    const wrapper = mountForm()
    try {
      await wrapper.get('[data-testid="footer-submit"]').trigger('click')
      await flushPromises()

      expect(toasts[0]?.color).toBe('success')
      expect(toasts[0]?.duration).toBe(5000)
    } finally {
      wrapper.unmount()
    }
  })
})
