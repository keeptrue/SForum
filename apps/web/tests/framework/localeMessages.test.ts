import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { baseCompile } from '@intlify/message-compiler'

/**
 * locale 文案走 vue-i18n 的模板语法，不是普通字符串：`@` 是链接消息的起始符
 * （`@:key` / `@.modifier:key`），裸写一个 `@` 会让 message-compiler 抛
 * INVALID_LINKED_FORMAT(10)。unplugin-vue-i18n 的 resource 插件在 import 阶段
 * 编译整份 locale，一失败整个资源模块就作废 —— dev 下每页都红屏，build 直接挂。
 *
 * 这里用构建期同一个编译器把全部消息过一遍，把「文案写坏」提前到 `bun test`。
 * `@intlify/message-compiler` 是 @nuxtjs/i18n 的传递依赖：bun 的隔离链接器能裸
 * 解析它，且生成的 .nuxt-typecheck 的 include 不含 tests/**，所以不新增直接依赖、
 * 不触发 bun install 重链（重链会连带清掉 .nuxt / node_modules/.vite 缓存）。
 */

const LOCALES = ['zh-CN', 'en-US'] as const

// NodeTypes 是 const enum（运行时被擦除），数值与 @intlify/message-compiler 一致。
const NODE_NAMED = 4
const NODE_LITERAL = 9

interface MessageNode {
  type: number
  value?: string
  key?: string
  items?: MessageNode[]
  cases?: MessageNode[]
}

function readLocale(locale: (typeof LOCALES)[number]): Record<string, unknown> {
  const url = new URL(`../../i18n/locales/${locale}.json`, import.meta.url)
  return JSON.parse(readFileSync(url, 'utf8')) as Record<string, unknown>
}

/** 拍平成 key path → 文案；嵌套命名空间与扁平 key 一视同仁。 */
function flatten(
  value: Record<string, unknown>,
  prefix = '',
  out: Array<[string, string]> = []
): Array<[string, string]> {
  for (const [key, entry] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (entry && typeof entry === 'object') {
      flatten(entry as Record<string, unknown>, path, out)
    } else if (typeof entry === 'string') {
      out.push([path, entry])
    }
  }
  return out
}

function compileMessage(message: string): { errors: string[], body: MessageNode } {
  const errors: string[] = []
  const result = baseCompile(message, {
    onError: error => errors.push(`${error.code}:${error.message}`)
  })
  return { errors, body: result.ast.body as MessageNode }
}

/** 取出参与渲染的节点；复数消息展开每个分支，单条消息直接取 items。 */
function nodes(body: MessageNode): MessageNode[] {
  if (body.items) {
    return body.items
  }
  return (body.cases ?? []).flatMap(branch => branch.items ?? [])
}

describe('locale message syntax', () => {
  for (const locale of LOCALES) {
    test(`${locale} 的每条消息都能被 vue-i18n 编译`, () => {
      const messages = flatten(readLocale(locale))
      // 拍平失败时不该静默通过：locale 至少上千条消息。
      expect(messages.length).toBeGreaterThan(1000)

      const failures: string[] = []
      for (const [path, message] of messages) {
        try {
          const { errors } = compileMessage(message)
          if (errors.length > 0) {
            failures.push(`${path} :: ${message} -> ${errors.join(' | ')}`)
          }
        } catch (error) {
          failures.push(`${path} :: ${message} -> ${(error as Error).message}`)
        }
      }

      expect(failures).toEqual([])
    })
  }

  test('资料设置 tab / 插件设置 / 扩展点治理的新文案两种语言键集一致', () => {
    // 新增命名空间必须成对落地：缺一条就是运行时 t() 直接漏键。
    const groups: Array<(locale: Record<string, unknown>) => unknown> = [
      locale => (locale.profileSettings as Record<string, unknown>).tabs,
      locale => (locale.profileSettings as Record<string, unknown>).extension,
      locale => ((locale.admin as Record<string, Record<string, unknown>>).settings as Record<string, unknown>).contributions,
      locale => (((locale.admin as Record<string, Record<string, unknown>>).extensions as Record<string, unknown>).contributions as Record<string, unknown>).governance
    ]
    const zh = readLocale('zh-CN')
    const en = readLocale('en-US')
    for (const pick of groups) {
      const zhKeys = Object.keys(flatten(pick(zh) as Record<string, unknown>)).sort()
      const enKeys = Object.keys(flatten(pick(en) as Record<string, unknown>)).sort()
      expect(zhKeys.length).toBeGreaterThan(0)
      expect(zhKeys).toEqual(enKeys)
    }
  })

  test('昵称文案用字面量转义 @，且 username 插值仍可用', () => {
    for (const locale of LOCALES) {
      const messages = new Map(flatten(readLocale(locale)))

      // 服务端把同名的 profile.nickname_* key 交给前端 t() 翻译，缺一条就是运行时缺失。
      const atPrefix = messages.get('profile.nickname_at_prefix')
      expect(atPrefix).toBeTruthy()
      const atNodes = nodes(compileMessage(atPrefix as string).body)
      expect(atNodes.some(node => node.type === NODE_LITERAL && node.value === '@')).toBe(true)

      const emptyHint = messages.get('profileSettings.nicknameEmptyHint')
      expect(emptyHint).toBeTruthy()
      const hintNodes = nodes(compileMessage(emptyHint as string).body)
      expect(hintNodes.some(node => node.type === NODE_LITERAL && node.value === '@')).toBe(true)
      expect(hintNodes.some(node => node.type === NODE_NAMED && node.key === 'username')).toBe(true)
    }
  })
})
