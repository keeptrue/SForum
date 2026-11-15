import { compileScript, compileTemplate, parse, rewriteDefault } from '@vue/compiler-sfc'
import { installTestDom } from './dom'

installTestDom()

export const testVue = await import('vue')
export const { mount, flushPromises } = await import('@vue/test-utils')

/**
 * `definePageMeta()` 是 Nuxt 的编译期宏：`new Function` 里没有这个全局，直接执行会
 * `ReferenceError`。挂载测试不关心路由元数据，按括号配平整段摘掉。
 */
function stripDefinePageMeta(source: string) {
  const marker = 'definePageMeta('
  let result = source
  let index = result.indexOf(marker)
  while (index !== -1) {
    let depth = 0
    let end = -1
    for (let cursor = index + marker.length - 1; cursor < result.length; cursor += 1) {
      const char = result[cursor]
      if (char === '(' || char === '{' || char === '[') depth += 1
      else if (char === ')' || char === '}' || char === ']') {
        depth -= 1
        if (depth === 0) { end = cursor; break }
      }
    }
    if (end === -1) break
    result = `${result.slice(0, index)}${result.slice(end + 1)}`
    index = result.indexOf(marker)
  }
  return result
}

export async function compileVueSfc(path: string, id = 'test-sfc', bindings: Record<string, unknown> = {}) {
  return compileSfcSource(await Bun.file(path).text(), id, bindings, path)
}

/**
 * 与 `compileVueSfc` 同一套编译手法，但直接吃源码字符串。
 *
 * 用途：测试里需要一段「与页面绑定方式逐字一致」的 harness SFC 时，不必先落盘。
 * `filename` 只用于报错定位，不参与模块解析（编译期所有 import 都被剥掉）。
 */
export async function compileSfcSource(
  source: string,
  id = 'test-sfc',
  bindings: Record<string, unknown> = {},
  filename = `${id}.vue`
) {
  const parsed = parse(source, { filename })
  // `parse()` 的模板/标签错误与 `template.errors` 是两条独立通道：只查后者会漏掉
  // 「多了一段残留标签」这类 SFC（当时 bun 全绿，dev 里却 500 到动态 import 失败）。
  if (parsed.errors.length > 0) {
    const first = parsed.errors[0] as { message?: string, loc?: { start?: { line?: number } } }
    throw new Error(`SFC parse error in ${filename}:${first?.loc?.start?.line ?? '?'} :: ${first?.message ?? first}`)
  }
  const script = compileScript(parsed.descriptor, { id })
  const template = compileTemplate({
    source: parsed.descriptor.template?.content || '',
    filename,
    id,
    compilerOptions: { bindingMetadata: script.bindings }
  })
  if (template.errors.length) throw new Error(String(template.errors[0]))

  // `vue` 的运行时应先改写成从注入的 Vue 对象解构，再统一剥掉其余 import，
  // 否则使用 inject/ref/watch 等运行时的 SFC 在这个测试环境里拿不到它们。
  const rewrittenScript = stripDefinePageMeta(script.content)
    .replace(/^import\s*\{([^}]+)\}\s*from\s*['"]vue['"];?\s*$/gm, (_match, names: string) =>
      `const {${names.replace(/\b(\w+)\s+as\s+(\w+)\b/g, '$1: $2')}} = Vue`)
    .replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
    // `export type { X }`（纯类型再导出）没有运行时含义，整行摘掉；否则会变成
    // 非法的 `type { X }` 语句。单行/多行两种写法都覆盖。
    .replace(/^export\s+type\s*\{[\s\S]*?\}\s*$/gm, '')
    .replace(/^export type /gm, 'type ')
    .replace(/^export interface /gm, 'interface ')
    // `import.meta` 在 `new Function` 体内是语法错误。挂载测试跑在 happy-dom 里，
    // 语义等价于客户端：`client` → true、`server`/`dev` → false。
    .replace(/\bimport\.meta\.client\b/g, 'true')
    .replace(/\bimport\.meta\.server\b/g, 'false')
    .replace(/\bimport\.meta\.dev\b/g, 'false')
  // `<script setup>` 的编译结果自带 `_defineComponent` 导入（已被上面的改写声明掉）；
  // 普通 `<script>` 没有，需要补一个。
  const defineComponentPrefix = /(?:const|let|var)\s*\{[^}]*_defineComponent/.test(rewrittenScript)
    ? ''
    : 'const { defineComponent: _defineComponent } = Vue\n'
  const scriptCode = `${defineComponentPrefix}${rewrittenScript}`
  const templateCode = template.code
    .replace(/import \{([^}]+)\} from "vue"/, (_match, names) => `const {${names.replace(/(\w+) as (\w+)/g, '$1: $2')}} = Vue`)
    .replace('export function render', 'function render')
  const executable = `${rewriteDefault(scriptCode, '__sfc__', ['typescript'])}
${templateCode}
__sfc__.render = render
return __sfc__`
  const transformed = new Bun.Transpiler({ loader: 'ts', target: 'esnext' }).transformSync(executable)
  // 编译时所有 import 语句都被剥掉，但 `<script setup>` 的 bindingMetadata 仍把导入名
  // 当作 setup 绑定引用。通过 `bindings` 把这些名字以函数参数注入，就能让单文件 SFC
  // 依赖其它模块（例如 @sforum/plugin-ui 的组件与注入键）而不破坏现有用例。
  const names = Object.keys(bindings)
  return new Function('Vue', ...names, transformed)(testVue, ...names.map((name) => bindings[name]))
}
