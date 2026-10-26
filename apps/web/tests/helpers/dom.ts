import { Window } from 'happy-dom'

// Centralized DOM bootstrap for Bun tests that mount Vue components.
//
// @vue/runtime-dom captures `document` at module evaluation time:
//   const doc = typeof document !== "undefined" ? document : null
// If Vue (directly or through @tiptap/vue-3 / @vue/test-utils) is imported
// before `document` exists, that internal binding is frozen to `null` and every
// later `mount()` throws `TypeError: null is not an object (evaluating
// 'doc.createElement')`. Because `bun test` runs all files in one process with a
// shared module registry, whichever file imports Vue first wins. A file that
// imports Vue without installing the DOM (e.g. editorL2Load -> sfEditor ->
// @tiptap/vue-3) therefore poisons every later mount test.
//
// The deterministic fix is a Bun test preload (tests/helpers/setup-dom.ts) that calls
// this helper before any test module — and therefore before any Vue import —
// is evaluated. Test files that need a specific origin can call
// `installTestDom({ url })` again; Vue keeps referencing the preload document,
// which is harmless because happy-dom does not enforce cross-document ownership
// for the read-only assertions these suites make.
export interface TestDomOptions {
  url?: string
}

export function installTestDom(options: TestDomOptions = {}) {
  // 测试 DOM 从不执行脚本：显式关闭 JS 求值，把这条安全边界写成我们自己的契约，
  // 而不是依赖 happy-dom 的默认值（GHSA-834v-8h66-3h79 的触发前提正是开启它）。
  const testWindow = new Window({
    url: options.url ?? 'http://localhost/',
    settings: { enableJavaScriptEvaluation: false }
  })
  Object.assign(globalThis, {
    window: testWindow,
    document: testWindow.document,
    navigator: testWindow.navigator,
    Document: testWindow.Document,
    ShadowRoot: testWindow.ShadowRoot,
    Element: testWindow.Element,
    HTMLElement: testWindow.HTMLElement,
    SVGElement: testWindow.SVGElement,
    Node: testWindow.Node,
    Event: testWindow.Event,
    MouseEvent: testWindow.MouseEvent,
    KeyboardEvent: testWindow.KeyboardEvent,
    ClipboardEvent: testWindow.ClipboardEvent,
    DataTransfer: testWindow.DataTransfer,
    DragEvent: testWindow.DragEvent,
    MutationObserver: testWindow.MutationObserver,
    requestAnimationFrame: testWindow.requestAnimationFrame.bind(testWindow),
    cancelAnimationFrame: testWindow.cancelAnimationFrame.bind(testWindow),
    getComputedStyle: testWindow.getComputedStyle.bind(testWindow)
  })
  return testWindow
}
