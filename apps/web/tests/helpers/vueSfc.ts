import { compileScript, compileTemplate, parse, rewriteDefault } from '@vue/compiler-sfc'
import { installTestDom } from './dom'

installTestDom()

export const testVue = await import('vue')
export const { mount, flushPromises } = await import('@vue/test-utils')

export async function compileVueSfc(path: string, id = 'test-sfc', bindings: Record<string, unknown> = {}) {
  const parsed = parse(await Bun.file(path).text(), { filename: path })
  // `parse()` 的模板/标签错误与 `template.errors` 是两条独立通道：只查后者会漏掉
  // 「多了一段残留标签」这类 SFC（当时 bun 全绿，dev 里却 500 到动态 import 失败）。
  if (parsed.errors.length > 0) {
    const first = parsed.errors[0] as { message?: string, loc?: { start?: { line?: number } } }
    throw new Error(`SFC parse error in ${path}:${first?.loc?.start?.line ?? '?'} :: ${first?.message ?? first}`)
  }
  const script = compileScript(parsed.descriptor, { id })
  const template = compileTemplate({
    source: parsed.descriptor.template?.content || '',
    filename: path,
    id,
    compilerOptions: { bindingMetadata: script.bindings }
  })
  if (template.errors.length) throw new Error(String(template.errors[0]))

  // `vue` 的运行时应先改写成从注入的 Vue 对象解构，再统一剥掉其余 import，
  // 否则使用 inject/ref/watch 等运行时的 SFC 在这个测试环境里拿不到它们。
  const rewrittenScript = script.content
    .replace(/^import\s*\{([^}]+)\}\s*from\s*['"]vue['"];?\s*$/gm, (_match, names: string) =>
      `const {${names.replace(/\b(\w+)\s+as\s+(\w+)\b/g, '$1: $2')}} = Vue`)
    .replace(/^import(?:[\s\S]*?from\s+)?['"][^'"]+['"];?\s*$/gm, '')
    .replace(/^export type /gm, 'type ')
    .replace(/^export interface /gm, 'interface ')
  // `<script setup>` 的编译结果自带 `_defineComponent` 导入（已被上面的改写声明掉）；
  // 普通 `<script>` 没有，需要补一个。
  const defineComponentPrefix = /(?:const|let|var)\s*\{[^}]*_defineComponent/.test(rewrittenScript)
    ? ''
    : 'const { defineComponent: _defineComponent } = Vue\n'
  const scriptCode = `${defineComponentPrefix}${rewrittenScript}`
  const templateCode = template.code
    .replace(/import \{([^}]+)\} from "vue"/, (_match, names) => `const {${names.replace(/(\w+) as (\w+)/g, '$1: $2')}} = Vue`)
    .replace('export function render', 'function render')
  const source = `${rewriteDefault(scriptCode, '__sfc__', ['typescript'])}
${templateCode}
__sfc__.render = render
return __sfc__`
  const executable = new Bun.Transpiler({ loader: 'ts', target: 'esnext' }).transformSync(source)
  // 编译时所有 import 语句都被剥掉，但 `<script setup>` 的 bindingMetadata 仍把导入名
  // 当作 setup 绑定引用。通过 `bindings` 把这些名字以函数参数注入，就能让单文件 SFC
  // 依赖其它模块（例如 @sforum/plugin-ui 的组件与注入键）而不破坏现有用例。
  const names = Object.keys(bindings)
  return new Function('Vue', ...names, executable)(testVue, ...names.map((name) => bindings[name]))
}
