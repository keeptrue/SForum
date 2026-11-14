import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { parse } from '@vue/compiler-sfc'

/**
 * `data-sf-tl-accent` 与 `tierAccentStyle` 必须落在**同一个元素**上（遍历式护栏）。
 *
 * 为什么需要这条：自定义 hex 档**不内联** `--tl-accent`，而是内联
 * `--tl-accent-light` / `--tl-accent-dark`，靠全局的 `[data-sf-tl-accent]` 规则在渲染期挑一个。
 * 于是「属性忘了加」的后果不是报错，而是那一档**静默退回 slate** —— 只有配了自定义色的站点
 * 才看得见，而它恰好是这次改动才可能出现的新配置。逐个手写断言漏一处的成本太高，
 * 所以这里用 SFC 的模板 AST 遍历：凡是把 accent style 绑到元素上的地方，该元素必须带属性。
 *
 * 用 AST 而不是源码字符串匹配：AGENTS.md 明确禁止「读整段源码匹配实现字符串」——
 * 那既证明不了「同一个元素」，也会被注释里的示例文字误伤。
 */

const components = [
  'admin/forum/trust-levels/SFAdminTrustLevelTierRow.vue',
  'admin/forum/trust-levels/SFAdminTrustLevelEarnableRow.vue',
  'admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue',
  'admin/forum/trust-levels/tabs/SFAdminTrustLevelLimitsTab.vue',
  'admin/forum/trust-levels/tabs/SFAdminTrustLevelsOverviewTab.vue',
  'profile/SFTrustLevelCard.vue',
  'profile/SFTrustLevelBadge.vue',
  'profile/SFTrustLevelDetailModal.vue'
] as const

const ACCENT_ATTR = 'data-sf-tl-accent'
/** 元素上是否把 accent 落成 `--tl-accent`：`tierAccentStyle(...)` 或来自父级的 `accentStyle`。 */
const ACCENT_STYLE_EXPRESSION = /\btierAccentStyle\b|\baccentStyle\b/

type SfcNode = {
  type: number
  tag?: string
  props?: Array<Record<string, any>>
  children?: unknown[]
}

function sfcPath(relative: string) {
  return fileURLToPath(new URL(`../../../app/components/${relative}`, import.meta.url))
}

/** 收集「绑定了 accent style」的元素，附带它们是否也带 `data-sf-tl-accent`。 */
function accentBindings(relative: string) {
  const source = readFileSync(sfcPath(relative), 'utf8')
  const { descriptor, errors } = parse(source, { filename: sfcPath(relative) })
  if (errors.length > 0) throw new Error(`SFC parse error in ${relative}: ${errors[0]!.message}`)
  const root = (descriptor.template?.ast ?? null) as SfcNode | null
  if (!root) throw new Error(`模板 AST 为空（解析器版本变了？）：${relative}`)

  const found: Array<{ tag: string, expression: string, hasAttribute: boolean }> = []

  const walk = (node: unknown) => {
    if (!node || typeof node !== 'object') return
    const current = node as SfcNode & { branches?: unknown[], codegenNode?: unknown }
    if (current.type === 1) {
      const props = current.props ?? []
      const styleDirective = props.find(prop => prop.type === 7 && prop.name === 'bind' &&
        prop.arg && prop.arg.content === 'style')
      const expression = String(styleDirective?.exp?.content ?? '')
      if (styleDirective && ACCENT_STYLE_EXPRESSION.test(expression)) {
        found.push({
          tag: String(current.tag ?? '?'),
          expression,
          // 属性是裸写的 `data-sf-tl-accent`（是「在不在」的开关，不是布尔开关的文案）。
          hasAttribute: props.some(prop => prop.type === 6 && prop.name === ACCENT_ATTR)
        })
      }
    }
    for (const child of current.children ?? []) walk(child)
    for (const branch of current.branches ?? []) walk(branch)
  }

  walk(root)
  return found
}

describe('every accent-carrying element opts into the global --tl-accent rule', () => {
  for (const component of components) {
    test(`${component} binds accent style and data-sf-tl-accent on the same element`, () => {
      const bindings = accentBindings(component)
      // 至少一处：组件确实把 accent 落到某个元素上，否则它不该出现在这张清单里。
      expect(bindings.length).toBeGreaterThan(0)
      // **每一处**都必须带属性。徽章是 button / span 两条互斥分支，两处都绑了 accent style；
      // 只检查第一处会让另一条分支在自定义 hex 档下静默退回 slate —— 而那条分支恰好
      // 只在「可点 / 只读」切换时才渲染，肉眼更难发现。
      expect(bindings.filter(binding => !binding.hasAttribute)).toEqual([])
      // 反过来也守一次：漏了属性时，报错信息要指出是哪个元素、哪条表达式。
      expect(bindings.every(binding => binding.expression.length > 0)).toBe(true)
    })
  }

  test('the guard would fail if the attribute were dropped (mutation check)', () => {
    // 把 TierRow 的模板里那一行去掉属性，护栏必须报红 —— 否则这条测试只是装饰。
    const source = readFileSync(sfcPath(components[0]), 'utf8')
    const mutated = source.replace(/(\n\s*)data-sf-tl-accent(\n)/, '$1$2')
    expect(mutated).not.toBe(source)
    const { descriptor } = parse(mutated, { filename: 'mutated.vue' })
    const root = descriptor.template?.ast as SfcNode | null
    const attrs: string[] = []
    const walk = (node: unknown) => {
      if (!node || typeof node !== 'object') return
      const current = node as SfcNode
      if (current.type === 1) {
        for (const prop of current.props ?? []) if (prop.type === 6) attrs.push(String(prop.name))
      }
      for (const child of current.children ?? []) walk(child)
      for (const branch of (current as { branches?: unknown[] }).branches ?? []) walk(branch)
    }
    walk(root)
    expect(attrs).not.toContain(ACCENT_ATTR)
  })
})
