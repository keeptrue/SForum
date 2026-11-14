import { afterAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileScript, compileTemplate, parse, rewriteDefault } from '@vue/compiler-sfc'
import { flushPromises, mount, testVue } from '../helpers/vueSfc'
import { isRef, nextTick } from 'vue'

/**
 * 举报对话框状态机的回归守卫。
 *
 * 背景（2026-10-03 起）：`useModerationReportDialog()` 曾用 `reactive({...})` 包装后返回，
 * reactive 在属性读取时会把 ref 解包成一次性快照；页面（`SFTopicShowPage.vue`）解构了这份
 * 返回值，于是 `target` 永远是 null、`reason`/`body` 永远是初始值 → 公开页「举报」按钮
 * 点了没有任何反应（对话框根节点 `v-if="open"` 恒为 false）。
 *
 * 这个文件钉死两件事：
 * 1. composable 必须返回 **ref**（解构后仍是 ref），调用方可以安全解构；
 * 2. 页面那种「解构 → 模板读写」的绑定方式在渲染级跑得通：对话框能打开、原因/正文能写进去、
 *    提交后拿到正确的载荷并进入成功态。
 *
 * 渲染级 harness 用 `<script setup>` 编译，绑定方式与页面**逐字一致**
 * （`Boolean(target)` / `:reason` / `@update:body="body = $event"`），
 * 这样「模板读 ref、写 ref」这条路径是真的被走了一遍，而不是被测试自己绕开。
 *
 * 网络层怎么替：见下面「替身策略」注释 —— 本文件**不使用 module mock**。
 */

type ReportRequest = {
  path: string
  options: { method?: string, body?: unknown }
}

const reportRequests: ReportRequest[] = []

/**
 * 替身策略（本文件**不使用 module mock**）。
 *
 * 两个事实（bun 1.4.2 实测，别再写成相反的）：
 * 1. `bun:test` 没有导出 `importOriginal`，拿不到「原始模块」的唯一官方入口；
 * 2. `mock.restore()` **不会**移除 `mock.module()` 注册的模块替身
 *    （Bun 文档原文：mock.restore() doesn't remove module mocks）。
 * 而仓库跑的是裸 `bun test`（scripts/test.sh 没有 --isolate），所有测试文件共享同一个
 * 进程级模块注册表 → module mock 会**泄漏**到后续测试文件，且无法还原。
 *
 * 所以这里改替**网络层**：`useModerationApi.ts` 整个文件没有 import 语句，
 * `useApiClient()` 是 Nuxt 自动导入的自由变量（在 bun 里从 globalThis 解析），
 * 并且它在工厂调用时立刻执行 `const { request } = useApiClient()`。
 * 因此只要在创建 composable **之前**把 `globalThis.useApiClient` 换成记录器，
 * 真实的 `useModerationApi` 就会照常拼装请求（路径、方法、body），把出网形状交给记录器。
 *
 * globalThis 赋值是**可还原**的（与 module mock 不同）：文件结束时还原成原值，
 * 下一个测试文件看到的还是原样，不会留下跨文件污染。
 *
 * 替身的安装窗口也压到最小：不放在模块作用域（bun 会把模块作用域的全局带进下一个测试
 * 文件），而是在**每个用例内部**、创建 composable 之前装，文件结束时统一还原。
 */
const globals = globalThis as Record<string, unknown>
const originalUseApiClient = globals.useApiClient
const originalUseI18n = globals.useI18n
let installedUseApiClient: unknown = null
let useApiClientRestored = false

function installUseApiClientStub() {
  const stub = () => ({
    request: async (path: string, options: { method?: string, body?: unknown } = {}) => {
      reportRequests.push({ path, options })
      // 假的举报记录：真实实现返回 `ModerationReport`，这里只回一个形状相同的对象。
      return {
        id: 1,
        reporterUserId: 1,
        targetType: 'comment',
        targetId: 7,
        reasonCode: 'spam',
        body: '',
        status: 'open',
        reviewNote: '',
        createdAt: '',
        updatedAt: ''
      }
    }
  })
  installedUseApiClient = stub
  globals.useApiClient = stub
}

/** 还原 globalThis 替身；被别的代码占用时明确报错，不覆盖别人的替身。 */
function restoreUseApiClient() {
  if (installedUseApiClient === null) {
    return
  }
  if (globals.useApiClient !== installedUseApiClient) {
    throw new Error(
      '[moderationReportDialog.test] globalThis.useApiClient 已被其它代码替换，'
      + '拒绝还原（否则会覆盖别人的替身）'
    )
  }
  globals.useApiClient = originalUseApiClient
  installedUseApiClient = null
  useApiClientRestored = true
}

// Nuxt 自动导入的最小替身：i18n 的 `t` 直接回键名，断言不依赖第二份中文文案。
globals.useI18n = () => ({ t: (key: string) => key })

afterAll(() => {
  restoreUseApiClient()
  globals.useI18n = originalUseI18n
})

// 守卫（注册在还原钩子之后，bun 按注册顺序执行 afterAll）：
// 文件结束时 globalThis.useApiClient 必须已经还原成原值，下一个测试文件不受影响。
afterAll(() => {
  if (!useApiClientRestored) {
    throw new Error('[moderationReportDialog.test] 文件结束时 globalThis.useApiClient 尚未还原')
  }
  if (globals.useApiClient !== originalUseApiClient) {
    throw new Error('[moderationReportDialog.test] 文件结束时 globalThis.useApiClient 与原值不一致')
  }
})

const { useModerationReportDialog } = await import('~/composables/moderation/useModerationReportDialog')

const SFReportDialog = await compileSfcSource(
  await Bun.file(fileURLToPath(new URL('../../app/components/moderation/SFReportDialog.vue', import.meta.url))).text(),
  'sf-report-dialog',
  { useI18n: () => ({ t: (key: string) => key }) }
)

/**
 * 页面绑定方式的 harness（与 `SFTopicShowPage.vue` 里那段模板逐字对齐）。
 * `canReport` 用来复刻「未登录不打开」的前置过滤。
 */
const harnessSource = `<script setup lang="ts">
import { useModerationReportDialog } from '~/composables/moderation/useModerationReportDialog'
import SFReportDialog from '~/components/moderation/SFReportDialog.vue'

const props = defineProps<{ canReport?: boolean }>()

const {
  target: reportingTarget,
  reason: reportReason,
  body: reportBody,
  submitting: reportSubmitting,
  error: reportError,
  success: reportSuccess,
  reasonOptions: reportReasonOptions,
  open: openReportDialog,
  close: closeReportDialog,
  setReason: updateReportReason,
  submit: submitReport
} = useModerationReportDialog({ canReport: () => props.canReport !== false })
</script>

<template>
  <div>
    <button
      type="button"
      data-testid="open-report"
      @click="openReportDialog({ type: 'comment', id: 7 })"
    >
      open
    </button>
    <SFReportDialog
      :open="Boolean(reportingTarget)"
      :reasons="reportReasonOptions"
      :reason="reportReason"
      :body="reportBody"
      :submitting="reportSubmitting"
      :error="reportError"
      :success="reportSuccess"
      @update:reason="updateReportReason"
      @update:body="reportBody = $event"
      @dismiss-error="reportError = ''"
      @close="closeReportDialog"
      @submit="submitReport"
    />
  </div>
</template>
`

const Harness = await compileSfcSource(harnessSource, 'sf-report-dialog-harness', {
  useModerationReportDialog,
  SFReportDialog
})

/** `SFButton` 桩：保留 variant / disabled，并把点击上抛成组件事件。 */
const SFButtonStub = {
  name: 'SFButton',
  props: {
    variant: { type: String, default: '' },
    size: { type: String, default: '' },
    disabled: { type: Boolean, default: false }
  },
  emits: ['click'],
  template: '<button type="button" :disabled="disabled" :data-variant="variant" @click="$emit(\'click\')"><slot /></button>'
}

/** `SFAlert` 桩：把 title 落到 data-title 上，成功态可以直接断言。 */
const SFAlertStub = {
  name: 'SFAlert',
  props: {
    variant: { type: String, default: '' },
    title: { type: String, default: '' },
    closable: { type: Boolean, default: false }
  },
  template: '<div class="sf-alert-stub" :data-variant="variant" :data-title="title"><slot /></div>'
}

const UIconStub = {
  name: 'UIcon',
  props: { name: { type: String, default: '' } },
  template: '<i :data-icon="name" />'
}

function mountHarness(canReport = true) {
  return mount(Harness, {
    props: { canReport },
    global: {
      components: { SFButton: SFButtonStub, SFAlert: SFAlertStub, UIcon: UIconStub },
      // 对话框用 `<Teleport to="body">`；桩成内联渲染，断言就不用依赖全局 document
      // （测试进程里 document 可能被别的文件换过，Teleport 会写到另一个 document.body）。
      stubs: { teleport: true }
    }
  })
}

describe('useModerationReportDialog 返回的是 ref 契约（公开页举报入口的回归守卫）', () => {
  test('解构出来的仍然是 ref：open() 后 target.value 就是本次举报目标', () => {
    // 装替身：必须在创建 composable 之前（工厂里立刻取 useApiClient）。
    installUseApiClientStub()
    const dialog = useModerationReportDialog()

    // 字段名是公开契约，不能改名。
    expect(Object.keys(dialog).sort()).toEqual([
      'body',
      'close',
      'error',
      'open',
      'reason',
      'reasonOptions',
      'setReason',
      'submit',
      'submitting',
      'success',
      'target'
    ])

    // 关键守卫：解构后必须是 ref（reactive 包装会让这里变成快照 / 普通值）。
    const { target, reason, body, submitting, error, success, reasonOptions, open } = dialog
    expect(isRef(target)).toBe(true)
    expect(isRef(reason)).toBe(true)
    expect(isRef(body)).toBe(true)
    expect(isRef(submitting)).toBe(true)
    expect(isRef(error)).toBe(true)
    expect(isRef(success)).toBe(true)
    expect(isRef(reasonOptions)).toBe(true)

    open({ type: 'comment', id: 7 })

    expect(target.value).toEqual({ type: 'comment', id: 7 })
    expect(reason.value).toBe('')
    expect(body.value).toBe('')
    expect(success.value).toBe(false)
  })

  test('渲染级：对话框能打开，原因与正文能写进去，提交拿到正确载荷并进入成功态', async () => {
    // 装替身：mount 时 harness 的 setup 会创建 composable，必须先装。
    installUseApiClientStub()
    reportRequests.length = 0
    const wrapper = mountHarness()
    try {
      // 打开前：对话框不存在。
      expect(wrapper.find('.sf-report-dialog').exists()).toBe(false)

      await wrapper.find('[data-testid="open-report"]').trigger('click')
      await nextTick()

      // 打开后：对话框真的渲染出来了（之前 target 恒为 null，这里永远是 false）。
      expect(wrapper.find('.sf-report-dialog').exists()).toBe(true)

      // 选原因（白名单里的 spam）。
      const reasons = wrapper.findAll('.sf-report-dialog__reason')
      expect(reasons.length).toBe(5)
      const spam = reasons.find((button) => button.text().trim() === 'moderation.reason.spam')
      expect(spam).toBeDefined()
      await spam!.trigger('click')
      await nextTick()

      // 填正文：模板的 `@update:body="body = $event"` 必须真的写进 composable 的 body ref。
      const textarea = wrapper.find('.sf-report-dialog__textarea')
      expect(textarea.exists()).toBe(true)
      await textarea.setValue('这里是一条广告链接')
      await nextTick()

      // 提交。
      const submit = wrapper.find('.sf-report-dialog__footer button[data-variant="primary"]')
      expect(submit.exists()).toBe(true)
      expect(submit.attributes('disabled')).toBeUndefined()
      await submit.trigger('click')
      await flushPromises()

      // 真实出网形状：路径、方法、body 都由**真实**的 useModerationApi 拼装，
      // 记录器只负责把它们留下来（这比「替身收到 createReport 参数」更强）。
      expect(reportRequests).toHaveLength(1)
      expect(reportRequests[0]!.path).toBe('/moderation/reports')
      expect(reportRequests[0]!.options.method).toBe('POST')
      expect(reportRequests[0]!.options.body).toEqual({
        targetType: 'comment',
        targetId: 7,
        reasonCode: 'spam',
        body: '这里是一条广告链接'
      })

      // 成功态渲染出来了。
      expect(wrapper.find('.sf-alert-stub[data-title="moderation.reportSubmitted"]').exists()).toBe(true)
    } finally {
      wrapper.unmount()
    }
  })

  test('canReport=false 时 open() 是空操作：对话框不打开，也不发请求', async () => {
    // 装替身：mount 时 harness 的 setup 会创建 composable，必须先装。
    installUseApiClientStub()
    reportRequests.length = 0
    const wrapper = mountHarness(false)
    try {
      await wrapper.find('[data-testid="open-report"]').trigger('click')
      await nextTick()

      expect(wrapper.find('.sf-report-dialog').exists()).toBe(false)
      expect(reportRequests).toHaveLength(0)

      // composable 层面同样是空操作：target 不被写入。
      const { target, open } = useModerationReportDialog({ canReport: () => false })
      open({ type: 'comment', id: 7 })
      expect(target.value).toBeNull()
    } finally {
      wrapper.unmount()
    }
  })

  test('守卫：替身被别的代码占用时，还原会明确报错而不是覆盖别人的替身', () => {
    installUseApiClientStub()
    const ours = installedUseApiClient
    expect(ours).not.toBeNull()
    expect(globals.useApiClient).toBe(ours)

    // 模拟别的代码覆盖了同一个全局：还原必须拒绝并报错。
    globals.useApiClient = () => ({ request: async () => ({}) })
    expect(() => restoreUseApiClient()).toThrow(/拒绝还原/)

    // 把自己的替身放回去，交给 afterAll 正常还原（文件结束时状态仍然是可还原的）。
    globals.useApiClient = ours
    expect(globals.useApiClient).toBe(ours)
  })
})

/**
 * 编译单文件组件：手法与 `tests/helpers/vueSfc.ts` 一致 —— 编译时剥掉 import，
 * 用 `bindings` 把被剥掉的依赖以函数参数注入，这样组件在 bun 里不依赖 Nuxt 的自动导入。
 */
async function compileSfcSource(source: string, id: string, bindings: Record<string, unknown>) {
  const parsed = parse(source, { filename: `${id}.vue` })
  if (parsed.errors.length > 0) {
    throw new Error(`SFC parse error in ${id}: ${String((parsed.errors[0] as { message?: string }).message)}`)
  }
  const script = compileScript(parsed.descriptor, { id })
  const template = compileTemplate({
    source: parsed.descriptor.template?.content || '',
    filename: `${id}.vue`,
    id,
    compilerOptions: { bindingMetadata: script.bindings }
  })
  if (template.errors.length) {
    throw new Error(String(template.errors[0]))
  }

  const rewrittenScript = script.content
    .replace(/^import\s*\{([^}]+)\}\s*from\s*['"]vue['"];?\s*$/gm, (_match, names: string) =>
      `const {${names.replace(/\b(\w+)\s+as\s+(\w+)\b/g, '$1: $2')}} = Vue`)
    .replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
    .replace(/^export type /gm, 'type ')
    .replace(/^export interface /gm, 'interface ')
  const defineComponentPrefix = /(?:const|let|var)\s*\{[^}]*_defineComponent/.test(rewrittenScript)
    ? ''
    : 'const { defineComponent: _defineComponent } = Vue\n'
  const templateCode = template.code
    .replace(/import \{([^}]+)\} from "vue"/, (_match, names) => `const {${names.replace(/(\w+) as (\w+)/g, '$1: $2')}} = Vue`)
    .replace('export function render', 'function render')
  const executableSource = `${rewriteDefault(`${defineComponentPrefix}${rewrittenScript}`, '__sfc__', ['typescript'])}
${templateCode}
__sfc__.render = render
return __sfc__`
  const executable = new Bun.Transpiler({ loader: 'ts', target: 'esnext' }).transformSync(executableSource)
  const names = Object.keys(bindings)
  return new Function('Vue', ...names, executable)(testVue, ...names.map((name) => bindings[name]))
}
