import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

/**
 * 后台导航 labelKey 的存在性守卫（遍历型）。
 *
 * 为什么单独一个文件：现有 admin 测试都是按功能域切片的（`adminForum` 只查论坛文件夹、
 * `adminSidebarExtensionNav` 只查扩展文件夹），没有一条横跨**全部** labelKey 的不变量。
 * HEAD 上 `adminModules.ts:226` 的页级 `labelKey: 'admin.nav.badges'` 两份 locale 都没有，
 * 2079 条断言全绿照不出来——dev 里「用户与权限」下的徽章项显示裸键名。
 * 所以这里对 `adminModules.ts` 里出现的每个字面 labelKey 逐个查两份 locale，缺一个就红。
 */
const modulesPath = new URL('../../app/config/adminModules.ts', import.meta.url)
const localesRoot = new URL('../../i18n/locales/', import.meta.url)

function readJson(path: URL): Record<string, unknown> {
  return JSON.parse(readFileSync(path, 'utf8')) as Record<string, unknown>
}

const zh = readJson(new URL('zh-CN.json', localesRoot))
const en = readJson(new URL('en-US.json', localesRoot))

function flatten(node: unknown, prefix = ''): Set<string> {
  const out = new Set<string>()
  if (!node || typeof node !== 'object') return out
  for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      for (const child of flatten(value, path)) out.add(child)
    } else {
      out.add(path)
    }
  }
  return out
}

function lookup(node: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (current, key) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined),
    node
  )
}

const modulesSource = readFileSync(modulesPath, 'utf8')

/** `adminModules.ts` 里所有字面 labelKey（页级 + 文件夹级 + 扩展页）。 */
function literalLabelKeys(): Set<string> {
  const keys = new Set<string>()
  for (const match of modulesSource.matchAll(/labelKey:\s*'([^']+)'/g)) {
    keys.add(match[1]!)
  }
  return keys
}

describe('admin navigation labelKeys', () => {
  test('every literal labelKey resolves in both locales', () => {
    const keys = literalLabelKeys()
    // 防真空绿：扫描面必须真的覆盖整份注册表，而不是正则失配后的空集合。
    expect(keys.size).toBeGreaterThanOrEqual(40)

    for (const [name, locale] of [['zh-CN', zh], ['en-US', en]] as const) {
      const defined = flatten(locale)
      const missing = [...keys].filter(key => !defined.has(key)).sort()
      // 点名句：红的时候直接告诉你缺哪个键、缺在哪份 locale。
      if (missing.length > 0) {
        throw new Error(`missing labelKey in ${name}: ${missing.join(', ')}`)
      }
    }
  })

  test('no labelKey is assembled at runtime, so the scan cannot silently skip one', () => {
    const dynamic = [...modulesSource.matchAll(/labelKey:[ \t]*([^\n]+)/g)]
      .map(match => match[1]!.trim())
      // 字面量以引号开头；三处类型声明是 `labelKey: string`，都不是运行时拼装。
      .filter(value => !value.startsWith("'") && value !== 'string')
    expect(dynamic).toEqual([])
  })

  test('the badge entry has real copy in both locales', () => {
    // P1 的回归锚：徽章挂在「用户与权限」下，必须显示「徽章」而不是裸键。
    expect(lookup(zh, 'admin.nav.badges')).toBe('徽章')
    expect(typeof lookup(en, 'admin.nav.badges')).toBe('string')
  })
})
