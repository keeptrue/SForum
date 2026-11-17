import { describe, expect, test } from 'bun:test'
import { parse } from '@vue/compiler-sfc'

import { installTestDom } from '../helpers/dom'

installTestDom()

const pageComponentSource = await Bun.file(
  new URL('../../app/components/extensions/admin/SFTrustedAdminPageComponent.vue', import.meta.url)
).text()
const settingsComponentSource = await Bun.file(
  new URL('../../app/components/extensions/settings/SFTrustedSettingsComponent.vue', import.meta.url)
).text()
const routeSource = await Bun.file(
  new URL('../../app/pages/admin/extensions/[extensionId]/pages/[...pagePath].vue', import.meta.url)
).text()
const trustComposableSource = await Bun.file(
  new URL('../../app/composables/admin/useAdminFrontendTrust.ts', import.meta.url)
).text()
const trustPanelSource = await Bun.file(
  new URL('../../app/components/admin/SFAdminFrontendTrustPanel.vue', import.meta.url)
).text()

type SfcNode = {
  type: number
  tag?: string
  props: Array<{ type: number, name: string, exp?: { type: number, content: string } }>
  children: SfcNode[]
}

function normalize(expression: string) {
  return expression.replace(/\s+/g, ' ').trim()
}

function directive(node: SfcNode, name: string): string | undefined {
  const prop = node.props.find(item => item.type === 7 && item.name === name)
  if (!prop) return undefined
  return prop.exp ? normalize(prop.exp.content) : ''
}

function elements(node: SfcNode): SfcNode[] {
  return node.children.filter(child => child.type === 1)
}

function rootElement(source: string, filename: string): SfcNode {
  const { descriptor, errors } = parse(source, { filename })
  expect(errors).toEqual([])
  const root = descriptor.template?.ast?.children.find(child => child.type === 1)
  if (!root) throw new Error(`${filename}: no root element`)
  return root as SfcNode
}

/** 收集以 firstExpression 开头的 v-if / v-else-if / v-else 链，按模板顺序返回。 */
function branchChain(node: SfcNode, firstExpression: string): string[] {
  const children = elements(node)
  const start = children.findIndex(child => directive(child, 'if') === firstExpression)
  if (start === -1) return []
  const chain = [firstExpression]
  for (let index = start + 1; index < children.length; index += 1) {
    const child = children[index] as SfcNode
    const elseIf = directive(child, 'else-if')
    if (elseIf !== undefined) {
      chain.push(elseIf)
      continue
    }
    if (child.props.some(prop => prop.type === 7 && prop.name === 'else')) {
      chain.push('else')
      continue
    }
    break
  }
  return chain
}

/** 找到第一个满足 predicate 的元素所在的父元素（即 v-if 链的宿主）。 */
function chainHost(node: SfcNode, firstExpression: string): SfcNode | undefined {
  for (const child of elements(node)) {
    if (directive(child, 'if') === firstExpression) return node
    const nested = chainHost(child, firstExpression)
    if (nested) return nested
  }
  return undefined
}

function chainFor(source: string, filename: string, firstExpression: string): string[] {
  const root = rootElement(source, filename)
  const host = chainHost(root, firstExpression)
  if (!host) throw new Error(`${filename}: branch "${firstExpression}" not found`)
  return branchChain(host, firstExpression)
}

describe('admin extension page first-paint trust state', () => {
  test('never renders the trust-required warning while trust is unresolved', () => {
    const chain = chainFor(
      pageComponentSource,
      'SFTrustedAdminPageComponent.vue',
      '(!trustResolved || (trusted && !mounted)) && !failed'
    )
    // 未知/未挂载 → 加载失败 → 状态读取失败 → 明确未授权
    expect(chain[0]).toBe('(!trustResolved || (trusted && !mounted)) && !failed')
    expect(chain[1]).toBe('failed')
    expect(chain[2]).toBe('!status')
    expect(chain[3]).toBe('!trusted')
    // status 为空时链已在 !status 分支终止，!trusted 只可能表示"确实未授权"
    expect(chain.indexOf('!status')).toBeLessThan(chain.indexOf('!trusted'))
    expect(chain[0]).not.toContain('!trusted')
  })

  test('settings view separates unresolved, failed, forced fallback and denied trust states', () => {
    const chain = chainFor(settingsComponentSource, 'SFTrustedSettingsComponent.vue', 'showSkeleton')
    // 骨架 → 加载失败 → 强制回退 → 状态读取失败 → 明确未授权
    expect(chain[0]).toBe('showSkeleton')
    expect(chain[1]).toBe('failed')
    expect(chain[2]).toBe('forcedFallback')
    expect(chain[3]).toBe('!status')
    expect(chain[4]).toBe('!trusted')
    expect(chain).toContain('!status')
    expect(chain.indexOf('!status')).toBeLessThan(chain.indexOf('!trusted'))
    expect(chain[0]).not.toContain('!trusted')
    // 已授权但尚未挂载时同样不出 Schema 表单：fallback 插槽只认 showSchemaFallback，
    // 否则 page-bootstrap 同步带回信任结论后，首帧就会先铺表单再整块换成组件。
    expect(settingsComponentSource).not.toContain('<div v-if="trustResolved"')
    expect(settingsComponentSource).toContain('<div v-if="showSchemaFallback" class="mt-4">')
  })

  test('composable models trust as a tri-state and seeds from the server bootstrap', () => {
    expect(trustComposableSource).toContain('const resolved = ref(false)')
    expect(trustComposableSource).toContain('resolved.value = true')
    // 未知窗口只由 /frontend 决定；/trust 只影响授权 UI 形态，不进关键路径
    expect(trustComposableSource).toContain('void loadExactTrust(item, current)')
    expect(trustComposableSource).toContain('async function loadExactTrust')
    expect(trustComposableSource).toMatch(/function load\(force = false\)/)
    // 初值必须校验归属，避免 SPA 导航时套用上一个扩展的结论
    expect(trustComposableSource).toContain('next.extensionId !== item.id')
    expect(trustComposableSource).toContain('next.digest !== item.adminFrontendDigest')
    // 竞态守卫：过期响应不得写回
    expect(trustComposableSource).toContain('if (current !== generation) return')
    expect(trustComposableSource).toContain('return { status, resolved,')
  })

  test('force reloads bypass the bootstrap seed after grant or revoke', () => {
    expect(trustComposableSource).toContain('if (!force && consumedSeedKey !== key)')
    expect(trustComposableSource).toContain('consumedSeedKey = key')
    expect(pageComponentSource).toContain('await loadTrust(true)')
    expect(pageComponentSource).toContain('@changed="() => loadTrust(true)"')
    expect(settingsComponentSource).toContain('await loadTrust(true)')
    expect(settingsComponentSource).toContain('@changed="() => loadTrust(true)"')
  })

  test('route shell forwards the bootstrap trust status to both extension views', () => {
    expect(routeSource).toContain('const pageFrontendStatus = computed(() => pageBootstrap.value?.frontend || null)')
    const forwarded = routeSource.match(/:initial-status="pageFrontendStatus"/g) || []
    expect(forwarded).toHaveLength(2)
    expect(pageComponentSource).toContain(':initial-status="status"')
    expect(settingsComponentSource).toContain(':initial-status="status"')
    expect(trustPanelSource).toContain('initialStatus?: AdminFrontendStatus | null')
  })

  test('gate blocks the micro-frontend until trust is resolved', () => {
    expect(pageComponentSource).toContain('!import.meta.client || !trustResolved.value || !trusted.value')
    expect(settingsComponentSource).toContain('!import.meta.client || !trustResolved.value || !trusted.value')
  })
})
